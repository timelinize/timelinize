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
	"fmt"
	"io/fs"
	"mime"
	"os"
	"path"
	"path/filepath"
	"strings"
	"text/tabwriter"

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

func openAndProvisionDB(ctx context.Context, repoDir string) (*sql.DB, error) {
	db, err := openDB(ctx, repoDir)
	if err != nil {
		return nil, err
	}
	if err = provisionDB(ctx, db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func openDB(ctx context.Context, repoDir string) (*sql.DB, error) {
	var db *sql.DB
	var err error
	defer func() {
		if err != nil && db != nil {
			db.Close()
		}
	}()

	dbPath := filepath.Join(repoDir, DBFilename)

	db, err = sql.Open("sqlite3", dbPath+"?_foreign_keys=on&_journal_mode=WAL")
	if err != nil {
		return nil, fmt.Errorf("opening database: %w", err)
	}

	// print version, because I keep losing track of it :)
	var version string
	err = db.QueryRowContext(ctx, "SELECT sqlite_version() AS version").Scan(&version)
	if err == nil {
		Log.Info("using sqlite", zap.String("version", version))
	}

	return db, nil
}

func provisionDB(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, createDB)
	if err != nil {
		return fmt.Errorf("setting up database: %w", err)
	}

	// assign this repo a persistent UUID for the UI, links, etc; and
	// store version so readers can know how to work with this DB/timeline repo
	repoID := uuid.New()
	_, err = db.Exec(`INSERT OR IGNORE INTO repo (key, value) VALUES (?, ?), (?, ?)`,
		"id", repoID.String(),
		"version", 1,
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

func saveAllDataSources(ctx context.Context, db *sql.DB) error {
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
					ct = "image/webp"
				case ".gif":
					ct = "image/gif"
				}
			}
			mediaType = &ct
		}

		vals = append(vals, ds.Name, ds.Title, ds.Description, media, mediaType, true)
		count++
	}

	_, err := db.ExecContext(ctx, query, vals...)
	if err != nil {
		return fmt.Errorf("writing data sources to DB: %w", err)
	}

	return nil
}

func saveAllStandardEntityTypes(ctx context.Context, db *sql.DB) error {
	entityTypes := []string{
		"person",
		"creature", // animals, insects, fish, pets... etc.
		"place",    // landmarks... etc.
		"company",
		"organization",
		"office", // president, ceo, etc... person swaps out over time TODO: rename to "designation"?
		"government",
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

	_, err := db.ExecContext(ctx, query, vals...)
	if err != nil {
		return fmt.Errorf("writing standard entity types to DB: %w", err)
	}

	return nil
}

func saveAllStandardClassifications(ctx context.Context, db *sql.DB) error {
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

	_, err := db.ExecContext(ctx, query, vals...)
	if err != nil {
		return fmt.Errorf("writing standard classifications to DB: %w", err)
	}

	return nil
}

func loadRepoID(ctx context.Context, db *sql.DB) (uuid.UUID, error) {
	var idStr string
	err := db.QueryRowContext(ctx, `SELECT value FROM repo WHERE key=? LIMIT 1`, "id").Scan(&idStr)
	if err != nil {
		return uuid.UUID{}, fmt.Errorf("selecting repo UUID: %w", err)
	}
	id, err := uuid.Parse(idStr)
	if err != nil {
		return uuid.UUID{}, fmt.Errorf("malformed UUID %s: %w", idStr, err)
	}
	return id, nil
}

func openAndProvisionThumbsDB(ctx context.Context, repoDir string, repoID uuid.UUID) (*sql.DB, error) {
	db, err := openThumbsDB(ctx, repoDir)
	if err != nil {
		return nil, err
	}
	if err = provisionThumbsDB(ctx, db, repoID); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func openThumbsDB(ctx context.Context, repoDir string) (*sql.DB, error) {
	var db *sql.DB
	var err error
	defer func() {
		if err != nil && db != nil {
			db.Close()
		}
	}()

	dbPath := filepath.Join(repoDir, ThumbsDBFilename)

	db, err = sql.Open("sqlite3", dbPath+"?_foreign_keys=on&_journal_mode=WAL")
	if err != nil {
		return nil, fmt.Errorf("opening thumbnail database: %w", err)
	}

	_, err = db.ExecContext(ctx, `PRAGMA optimize=0x10002`)
	if err != nil {
		Log.Error("optimizing database: %w", zap.Error(err))
	}

	return db, nil
}

func provisionThumbsDB(ctx context.Context, thumbsDB *sql.DB, repoID uuid.UUID) error {
	_, err := thumbsDB.ExecContext(ctx, createThumbsDB)
	if err != nil {
		return fmt.Errorf("setting up thumbnail database: %w", err)
	}

	// link this database to the repo
	_, err = thumbsDB.ExecContext(ctx, `INSERT OR IGNORE INTO repo_link (repo_id) VALUES (?)`, repoID.String())
	if err != nil {
		return fmt.Errorf("linking repo UUID: %w", err)
	}

	return nil
}

// explainQueryPlan prints out the query and its plan. You MUST acquire a lock on the dbMu first.
//
//nolint:unused
func (tl *Timeline) explainQueryPlan(ctx context.Context, tx *sql.Tx, q string, args ...any) {
	logger := Log.Named("query_planner")

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 1, ' ', 0)
	fmt.Fprintln(w, "\nQUERY PLAN FOR:\n============================================================================================")
	fmt.Fprintln(w, q)
	fmt.Fprintln(w, "============================================================================================")
	fmt.Fprintln(w, args...)
	fmt.Fprintln(w, "============================================================================================")

	explainQ := "EXPLAIN QUERY PLAN " + q

	var rows *sql.Rows
	var err error
	if tx == nil {
		rows, err = tl.db.QueryContext(ctx, explainQ, args...)
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
