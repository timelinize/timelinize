/*
	Timelinize
	Copyright (c) 2013 Matthew Holt

	This program is free software: you can redistribute it and/or modify
	it under the terms of the GNU Affero General Public License as published
	by the Free Software Foundation, either version 3 of the License, or
	(at your option) any later version.

	This program is distributed in the hope that it will be useful,
	but WITHOUT ANY WARRANTY; without even the implied warranty of
	MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
	GNU Affero General Public License for more details.

	You should have received a copy of the GNU Affero General Public License
	along with this program.  If not, see <https://www.gnu.org/licenses/>.
*/

package timeline

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"mime"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"text/tabwriter"
	"time"

	sqlite_vec "github.com/asg017/sqlite-vec-go-bindings/cgo"
	"github.com/google/uuid"
	_ "github.com/mattn/go-sqlite3" // register the sqlite3 driver
	"github.com/timelinize/timelinize/datasources"
	"go.uber.org/zap"
)

func init() {
	sqlite_vec.Auto()
}

//go:embed schema.sql
var createDB string

//go:embed thumbnails.sql
var createThumbsDB string

func openAndProvisionTimelineDB(ctx context.Context, repoDir string) (sqliteDB, error) {
	db, err := openTimelineDB(ctx, repoDir)
	if err != nil {
		return db, err
	}
	if err = provisionTimelineDB(ctx, db); err != nil {
		db.Close()
		return db, err
	}
	return db, nil
}

// sqliteDB wraps *sql.DB (which is a connection pool)
// such that there is a pool for reading, and a pool
// (of 1 connection) for writing. When opened properly
// with the right PRAGMAs (including via DSN, depending
// on the driver), this allows us to take advantage of
// sqlite's concurrency control which is closer to the
// metal and more efficient. It also means simpler code.
// In WAL mode, we can perform reads while writes happen.
//
// Use the WritePool for any query that mutates the DB
// (such as INSERT, UPDATE, DELETE, etc); use ReadPool
// for SELECT, EXPLAIN QUERY PLAN, and other transactions
// that only do reads.
type sqliteDB struct {
	WritePool, ReadPool *sql.DB
}

func (db sqliteDB) Close() error {
	var errR, errW error
	if db.ReadPool != nil {
		errR = db.ReadPool.Close()
	}
	if db.WritePool != nil {
		errW = db.WritePool.Close()
	}
	if errR != nil || errW != nil {
		return fmt.Errorf("closing DB pools: read: %w; write: %w", errR, errW)
	}
	return nil
}

func openSqliteDB(ctx context.Context, dbPath string) (sqliteDB, error) {
	var db sqliteDB

	var err error
	defer func() {
		if err != nil {
			db.Close()
		}
	}()

	// prepare the query string for the DSN
	qs := url.Values{
		"mode":          {"rwc"},
		"_journal_mode": {"wal"},       // significant performance improvement, but requires modern file systems
		"_foreign_keys": {"on"},        // to help enforce correctness in case of bugs in program
		"_txlock":       {"immediate"}, // avoid potential "database is locked" errors
	}

	// the query string will change, so make this reusable
	makeDSN := func() string {
		return fmt.Sprintf("file:%s?%s", url.PathEscape(filepath.ToSlash(dbPath)), qs.Encode())
	}

	// This took a week to debug: the final boss before I was ready
	// to demo the app to a broader audience. During my final import
	// test of my full library, the import crashed right after it
	// spawned the embeddings and thumbnails jobs. The error was
	// "the database disk image is malformed" -- DB corruption. I
	// was able to reproduce it without the photo libraries, but it
	// was a SIGBUS error this time. The stack trace pointed deep in
	// C code, a syscall. Multiple LLMs and other developers pointed
	// to possible file system malfunction. Later, I tried reproducing
	// it with modernc.org/sqlite, which is a pure Go port of sqlite,
	// and it gave me the full stack trace: the error was in a function
	// called "walFindFrame", on an address that appears misaligned on
	// ARM64: 0x105b673ca % 8 = 2. This was consistent among crashes
	// (the address was always misaligned). The ExFAT driver on MacOS
	// appears to return misaligned file offsets. (Even with mmap
	// not enabled on sqlite, WAL mode still uses memory mapped I/O for
	// its -shm file.) I could not reproduce this bug using any other
	// file system on the Mac, or ExFAT on Linux. I thought it was a
	// timing issue since it wasn't 100% deterministically reproducible,
	// but the modernc test was much, much slower and it still crashed.
	// So, my current best guess is the Mac ExFAT driver specifically is
	// buggy, such that it corrupts databases or crashes when using
	// sqlite's WAL mode. That is really bad. So, we should recommend
	// AGAINST using ExFAT on Mac, or maybe in general (I'm not 100%
	// sure if the bug is Mac-only), but if people do, we can detect
	// ExFAT and disable WAL mode. This performs much worse. But it
	// should prevent database corruption and exceptions. Hopefully.
	// See https://github.com/mattn/go-sqlite3/issues/1355.
	if runtime.GOOS == "darwin" {
		dbDir := filepath.Dir(dbPath)
		fsType, err := getFileSystemType(dbDir)
		if err != nil {
			Log.Error("checking file system type",
				zap.String("filepath", dbDir),
				zap.Error(err))
		} else if fsType == "exfat" {
			Log.Warn("file system instability makes WAL mode unsafe; database performance will be degraded to avoid corruption (to avoid this, don't store timeline on exfat)",
				zap.String("filepath", dbDir),
				zap.String("file_system_type", fsType))
			qs.Set("_journal_mode", "TRUNCATE")
		}
	}

	dsn := makeDSN()

	Log.Info("opening DB write pool", zap.String("dsn", dsn))
	db.WritePool, err = sql.Open("sqlite3", dsn)
	if err != nil {
		return db, fmt.Errorf("opening database write pool: %w", err)
	}
	db.WritePool.SetMaxOpenConns(1) // sqlite doesn't support concurrent writers

	// adjust the query string for read-only
	qs.Set("mode", "ro")
	qs.Del("_txlock")
	dsn = makeDSN()

	Log.Info("opening DB read pool", zap.String("dsn", dsn))
	db.ReadPool, err = sql.Open("sqlite3", dsn)
	if err != nil {
		return db, fmt.Errorf("opening database read pool: %w", err)
	}

	// ensure DB file exists before we try querying it with a read-only connection
	if err := db.WritePool.PingContext(ctx); err != nil {
		return db, fmt.Errorf("pinging database file: %w", err)
	}

	return db, nil
}

func openTimelineDB(ctx context.Context, repoDir string) (sqliteDB, error) {
	db, err := openSqliteDB(ctx, filepath.Join(repoDir, DBFilename))
	if err != nil {
		return db, err
	}

	// print version, because I keep losing track of it :)
	var version string
	err = db.ReadPool.QueryRowContext(ctx, "SELECT sqlite_version() AS version").Scan(&version)
	if err == nil {
		Log.Info("using sqlite", zap.String("version", version))
	}

	return db, nil
}

func provisionTimelineDB(ctx context.Context, db sqliteDB) error {
	_, err := db.WritePool.ExecContext(ctx, createDB)
	if err != nil {
		return fmt.Errorf("setting up database: %w", err)
	}

	// assign this repo a persistent UUID for the UI, links, etc; and
	// store version so readers can know how to work with this DB/timeline repo
	repoID := uuid.New()
	_, err = db.WritePool.ExecContext(ctx, `INSERT OR IGNORE INTO repo (key, value, type) VALUES (?, ?, ?), (?, ?, ?)`,
		"id", repoID.String(), "string",
		"version", 1, "int",
	)
	if err != nil {
		return fmt.Errorf("persisting repo UUID and version: %w", err)
	}

	// add all registered data sources
	err = saveAllDataSources(ctx, db)
	if err != nil {
		return fmt.Errorf("saving registered data sources to database: %w", err)
	}

	// add all standard classifications
	err = saveAllStandardClassifications(ctx, db)
	if err != nil {
		return fmt.Errorf("saving standard classifications to database: %w", err)
	}

	// add all standard entity types
	err = saveAllStandardEntityTypes(ctx, db)
	if err != nil {
		return fmt.Errorf("saving standard entity types to database: %w", err)
	}

	return nil
}

func saveAllDataSources(ctx context.Context, db sqliteDB) error {
	if len(dataSources) == 0 {
		return nil
	}

	query := `INSERT OR IGNORE INTO "data_sources" ("name", "title", "description", "media", "media_type", "standard") VALUES`

	vals := make([]any, 0, len(dataSources))
	var count int

	for _, ds := range dataSources {
		if count > 0 {
			query += ","
		}
		query += " (?, ?, ?, ?, ?, ?)"

		var media []byte
		var mediaType *string
		if ds.Icon != "" {
			var err error
			media, err = fs.ReadFile(datasources.Images, "_images/"+ds.Icon)
			if err != nil {
				return fmt.Errorf("reading data source icon: %w", err)
			}
			ct := mime.TypeByExtension(path.Ext(ds.Icon))
			if ct == "" {
				switch strings.ToLower(path.Ext(ds.Icon)) {
				case ".svg":
					ct = "image/svg+xml"
				case ".jpg", ".jpeg", ".jpe":
					ct = "image/jpeg"
				case ".png":
					ct = "image/png"
				case ".ico":
					ct = "image/x-icon" // debatable whether it should be that, or "image/vnd.microsoft.icon"
				case ".webp":
					ct = "image/webp"
				case ".avif":
					ct = "image/avif"
				case ".gif":
					ct = "image/gif"
				}
			}
			mediaType = &ct
		}

		vals = append(vals, ds.Name, ds.Title, ds.Description, media, mediaType, true)
		count++
	}

	_, err := db.WritePool.ExecContext(ctx, query, vals...)
	if err != nil {
		return fmt.Errorf("writing data sources to DB: %w", err)
	}

	return nil
}

func saveAllStandardEntityTypes(ctx context.Context, db sqliteDB) error {
	entityTypes := []string{
		EntityPerson,
		EntityCreature, // pets, animals, insects, fish, etc...
		EntityPlace,
		// TODO: could also have company/organization, office/designation, government, etc.
	}

	query := `INSERT INTO entity_types ("name") VALUES`

	vals := make([]any, 0, len(entityTypes))
	var count int

	for _, et := range entityTypes {
		if count > 0 {
			query += ","
		}
		query += " (?)"
		vals = append(vals, et)
		count++
	}
	query += ` ON CONFLICT DO UPDATE SET name=excluded.name`

	_, err := db.WritePool.ExecContext(ctx, query, vals...)
	if err != nil {
		return fmt.Errorf("writing standard entity types to DB: %w", err)
	}

	return nil
}

func saveAllStandardClassifications(ctx context.Context, db sqliteDB) error {
	query := `INSERT INTO "classifications" ("standard", "name", "labels", "description") VALUES`

	vals := make([]any, 0, len(classifications)*4) //nolint:mnd
	var count int

	for _, cl := range classifications {
		if count > 0 {
			query += ","
		}
		query += " (?, ?, ?, ?)"
		vals = append(vals, true, cl.Name, strings.Join(cl.Labels, ","), cl.Description)
		count++
	}
	query += ` ON CONFLICT DO UPDATE SET standard=excluded.standard, name=excluded.name,
		labels=excluded.labels, description=excluded.description`

	_, err := db.WritePool.ExecContext(ctx, query, vals...)
	if err != nil {
		return fmt.Errorf("writing standard classifications to DB: %w", err)
	}

	return nil
}

func loadRepoID(ctx context.Context, db sqliteDB) (uuid.UUID, error) {
	var idStr string
	err := db.WritePool.QueryRowContext(ctx, `SELECT value FROM repo WHERE key='id' LIMIT 1`).Scan(&idStr)
	if err != nil {
		return uuid.UUID{}, fmt.Errorf("selecting repo UUID: %w", err)
	}
	id, err := uuid.Parse(idStr)
	if err != nil {
		return uuid.UUID{}, fmt.Errorf("malformed UUID %s: %w", idStr, err)
	}
	return id, nil
}

func openAndProvisionThumbsDB(ctx context.Context, repoDir string, repoID uuid.UUID) (sqliteDB, error) {
	db, err := openThumbsDB(ctx, repoDir)
	if err != nil {
		return db, err
	}
	if err = provisionThumbsDB(ctx, db, repoID); err != nil {
		db.Close()
		return db, err
	}
	return db, nil
}

func openThumbsDB(ctx context.Context, repoDir string) (sqliteDB, error) {
	db, err := openSqliteDB(ctx, filepath.Join(repoDir, ThumbsDBFilename))
	if err != nil {
		return db, err
	}
	_, err = db.WritePool.ExecContext(ctx, `PRAGMA optimize=0x10002`)
	if err != nil {
		Log.Error("optimizing database: %w", zap.Error(err))
	}
	return db, nil
}

func provisionThumbsDB(ctx context.Context, thumbsDB sqliteDB, repoID uuid.UUID) error {
	_, err := thumbsDB.WritePool.ExecContext(ctx, createThumbsDB)
	if err != nil {
		return fmt.Errorf("setting up thumbnail database: %w", err)
	}

	// link this database to the repo
	_, err = thumbsDB.WritePool.ExecContext(ctx, `INSERT OR IGNORE INTO repo_link (repo_id) VALUES (?)`, repoID.String())
	if err != nil {
		return fmt.Errorf("linking repo UUID: %w", err)
	}

	return nil
}

// explainQueryPlan prints out the query and its plan.
//
//nolint:unused
func (tl *Timeline) explainQueryPlan(ctx context.Context, tx *sql.Tx, q string, args ...any) {
	logger := Log.Named("query_planner")

	// convert some common pointer types to values as that's how they are used in the DB
	argsToDisplay := make([]any, len(args))
	for i, arg := range args {
		switch v := arg.(type) {
		case *string:
			if v != nil {
				argsToDisplay[i] = *v
			}
		case *time.Time:
			if v != nil {
				argsToDisplay[i] = *v
			}
		case *int:
			if v != nil {
				argsToDisplay[i] = *v
			}
		case *int64:
			if v != nil {
				argsToDisplay[i] = *v
			}
		case *uint64:
			if v != nil {
				argsToDisplay[i] = *v
			}
		case *float64:
			if v != nil {
				argsToDisplay[i] = *v
			}
		case []byte:
			if v != nil {
				argsToDisplay[i] = hex.EncodeToString(v)
			}
		default:
			argsToDisplay[i] = v
		}
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 1, ' ', 0)
	fmt.Fprintln(w, "\nQUERY PLAN FOR:\n============================================================================================")
	fmt.Fprintln(w, q)
	fmt.Fprintln(w, "============================================================================================")
	fmt.Fprintln(w, argsToDisplay...)
	fmt.Fprintln(w, "============================================================================================")

	explainQ := "EXPLAIN QUERY PLAN " + q

	var rows *sql.Rows
	var err error
	if tx == nil {
		rows, err = tl.db.ReadPool.QueryContext(ctx, explainQ, args...)
	} else {
		rows, err = tx.QueryContext(ctx, explainQ, args...)
	}
	if err != nil {
		logger.Error("explaining query plan", zap.Error(err))
		return
	}
	defer rows.Close()
	for rows.Next() {
		var id, parent, notUsed int
		var detail string
		err := rows.Scan(&id, &parent, &notUsed, &detail)
		if err != nil {
			logger.Error("scanning query plan row", zap.Error(err))
			return
		}
		fmt.Fprintf(w, "%d\t%d\t%d\t%s\n", id, parent, notUsed, detail)
	}
	if err := rows.Err(); err != nil {
		logger.Error("iterating query plan rows", zap.Error(err))
		return
	}

	fmt.Fprintln(w, "============================================================================================")
	w.Flush()
}
