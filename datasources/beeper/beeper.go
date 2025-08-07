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

// Package beeper implements a data source for Beeper chat history databases.
//
// Beeper aggregates chats from many networks using Matrix under the hood.
// The desktop app typically stores an SQLite database at:
//   - macOS:   ~/Library/Application Support/BeeperTexts/index.db
//   - Windows: %APPDATA%\BeeperTexts\index.db
//   - Linux:   ~/.local/share/BeeperTexts/index.db
//
// This importer looks for a Matrix-style events table and imports
// m.room.message events as timeline items. Room threads are represented
// as collection items and messages are linked using RelInCollection.
package beeper

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3" // register sqlite3 driver
	"github.com/timelinize/timelinize/timeline"
	"go.uber.org/zap"
)

func init() {
	if err := timeline.RegisterDataSource(timeline.DataSource{
		Name:            "beeper",
		Title:           "Beeper",
		Icon:            "beeper.svg",
		NewOptions:      func() any { return new(Options) },
		NewFileImporter: func() timeline.FileImporter { return new(FileImporter) },
	}); err != nil {
		timeline.Log.Fatal("registering data source", zap.Error(err))
	}
}

// FileImporter imports from a Beeper SQLite database.
type FileImporter struct{}

// Options contains provider-specific options.
type Options struct{}

// Recognize returns whether the input looks like a Beeper DB.
// We are conservative and look for an SQLite file named "index.db"
// and the presence of an events-like table typical of Matrix stores.
func (FileImporter) Recognize(ctx context.Context, dirEntry timeline.DirEntry, _ timeline.RecognizeParams) (timeline.Recognition, error) {
	name := strings.ToLower(dirEntry.Name())
	if name != "index.db" {
		// allow recognizing if it's a directory containing index.db
		if !dirEntry.IsDir() || !dirEntry.FileExists("index.db") {
			return timeline.Recognition{}, nil
		}
	}

	// Note: SQL packages cannot work over fs.FS. We only probe cheaply by name here.
	// We'll validate schema during import, returning a clear error if unsupported.
	return timeline.Recognition{Confidence: 0.8}, nil
}

// FileImport imports Beeper messages. It supports typical Matrix-style schemas
// where an events table has: event_id, room_id, sender, origin_server_ts, type, content.
// Rooms are represented as collection items and message items are linked to them.
func (fimp *FileImporter) FileImport(ctx context.Context, dirEntry timeline.DirEntry, params timeline.ImportParams) error {
	// open DB read-only using direct disk path
	dbPath := dirEntry.FullPath()
	if dirEntry.IsDir() {
		dbPath = filepath.Join(dirEntry.FullPath(), "index.db")
	}
	if !timeline.FileExists(dbPath) {
		return fmt.Errorf("expected Beeper database at %s", dbPath)
	}

	db, err := sql.Open("sqlite3", dbPath+"?mode=ro")
	if err != nil {
		return fmt.Errorf("opening beeper sqlite: %w", err)
	}
	defer db.Close()

	// ensure events table exists and looks matrix-like
	hasEvents, err := tableExists(ctx, db, "events")
	if err != nil {
		return err
	}
	if !hasEvents {
		// some Beeper builds store events in "event" (singular)
		hasEvent, err := tableExists(ctx, db, "event")
		if err != nil {
			return err
		}
		if !hasEvent {
			return errors.New("unsupported Beeper DB: missing events table")
		}
	}

	roomNames, _ := loadRoomNames(ctx, db) // best-effort

	// Prefer canonical column names, with fallbacks
	// event_id, room_id, sender, origin_server_ts, type, content
	// We'll try a sequence of queries until one works.
	candidateQueries := []string{
		`SELECT event_id, room_id, sender, origin_server_ts, type, content FROM events WHERE type='m.room.message' ORDER BY origin_server_ts ASC`,
		`SELECT event_id, room_id, sender, origin_server_ts, type, content FROM event WHERE type='m.room.message' ORDER BY origin_server_ts ASC`,
		`SELECT event_id, room_id, sender, origin_server_ts, event_type, content FROM events WHERE event_type='m.room.message' ORDER BY origin_server_ts ASC`,
		`SELECT id, room_id, sender, timestamp, type, content FROM events WHERE type='m.room.message' ORDER BY timestamp ASC`,
	}

	var rows *sql.Rows
	for _, q := range candidateQueries {
		rows, err = db.QueryContext(ctx, q)
		if err == nil {
			break
		}
		params.Log.Debug("beeper: query variant failed", zap.String("query", q), zap.Error(err))
	}
	if err != nil {
		return fmt.Errorf("querying beeper events: %w", err)
	}
	defer rows.Close()

	// Cache of created room collection items by room_id
	roomCollections := make(map[string]*timeline.Item)

	type row struct {
		eventID string
		roomID  string
		sender  string
		tsMs    int64
		typ     string
		content string
	}

	// Scan loop: use column names to be resilient to different schemas
	cols, err := rows.Columns()
	if err != nil {
		return err
	}
	// index mapping
	var idx = struct{ id, room, sender, ts, typ, content int }{-1, -1, -1, -1, -1, -1}
	for i, c := range cols {
		lc := strings.ToLower(c)
		switch lc {
		case "event_id", "id":
			if idx.id < 0 {
				idx.id = i
			}
		case "room_id":
			idx.room = i
		case "sender":
			idx.sender = i
		case "origin_server_ts", "timestamp", "ts":
			if idx.ts < 0 {
				idx.ts = i
			}
		case "type", "event_type":
			idx.typ = i
		case "content":
			idx.content = i
		}
	}
	if idx.id < 0 || idx.room < 0 || idx.sender < 0 || idx.ts < 0 || idx.typ < 0 || idx.content < 0 {
		return errors.New("beeper: unsupported events schema (missing required columns)")
	}

	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return fmt.Errorf("scanning beeper row: %w", err)
		}

		getString := func(i int) string {
			if i < 0 || i >= len(vals) {
				return ""
			}
			switch v := vals[i].(type) {
			case string:
				return v
			case []byte:
				return string(v)
			default:
				return fmt.Sprintf("%v", v)
			}
		}
		getInt64 := func(i int) int64 {
			if i < 0 || i >= len(vals) {
				return 0
			}
			switch v := vals[i].(type) {
			case int64:
				return v
			case int:
				return int64(v)
			case int32:
				return int64(v)
			case float64:
				return int64(v)
			case []byte:
				// try to parse integer stored as text
				s := strings.TrimSpace(string(v))
				if s == "" {
					return 0
				}
				if n, err := parseInt64(s); err == nil {
					return n
				}
				return 0
			default:
				return 0
			}
		}

		r := row{
			eventID: getString(idx.id),
			roomID:  getString(idx.room),
			sender:  getString(idx.sender),
			tsMs:    getInt64(idx.ts),
			typ:     strings.ToLower(getString(idx.typ)),
			content: getString(idx.content),
		}

		if r.typ != "m.room.message" {
			continue
		}

		// parse content JSON to extract textual body when present
		var content struct {
			Body    string `json:"body"`
			MsgType string `json:"msgtype"`
			Format  string `json:"format"`
			// NOTE: we intentionally ignore other fields or attachments for initial support
		}
		_ = json.Unmarshal([]byte(r.content), &content)

		// build sender entity with a data-source-unique identity attribute name
		senderEntity := timeline.Entity{
			Attributes: []timeline.Attribute{
				{
					Name:     "matrix_user_id",
					Value:    r.sender,
					Identity: true,
				},
			},
		}

		// create message item
		it := &timeline.Item{
			ID:             r.eventID,
			Classification: timeline.ClassMessage,
			Timestamp:      time.UnixMilli(r.tsMs),
			Owner:          senderEntity,
		}
		body := strings.TrimSpace(content.Body)
		if body != "" {
			it.Content = timeline.ItemData{Data: timeline.StringData(body)}
		}

		// ensure room collection exists
		coll := roomCollections[r.roomID]
		if coll == nil {
			coll = &timeline.Item{
				ID:             r.roomID,
				Classification: timeline.ClassCollection,
			}
			if name := roomNames[r.roomID]; name != "" {
				coll.Metadata = timeline.Metadata{"Name": name}
			}
			roomCollections[r.roomID] = coll
		}

		// link message to its room as a collection
		ig := &timeline.Graph{Item: it}
		ig.ToItem(timeline.RelInCollection, coll)

		params.Pipeline <- ig
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterating beeper rows: %w", err)
	}

	return nil
}

func tableExists(ctx context.Context, db *sql.DB, name string) (bool, error) {
	var exists int
	err := db.QueryRowContext(ctx, `SELECT 1 FROM sqlite_master WHERE type='table' AND name=? LIMIT 1`, name).Scan(&exists)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, fmt.Errorf("checking table %s: %w", name, err)
	}
	return exists == 1, nil
}

func loadRoomNames(ctx context.Context, db *sql.DB) (map[string]string, error) {
	names := make(map[string]string)
	// Try a couple likely schemas
	candidates := []string{
		`SELECT room_id, name FROM rooms`,
		`SELECT room_id, display_name FROM rooms`,
		`SELECT id, name FROM rooms`,
	}
	for _, q := range candidates {
		rows, err := db.QueryContext(ctx, q)
		if err != nil {
			continue
		}
		defer rows.Close()
		var cols []string
		cols, err = rows.Columns()
		if err != nil {
			continue
		}
		if len(cols) < 2 {
			continue
		}
		for rows.Next() {
			var a, b sql.NullString
			if err := rows.Scan(&a, &b); err == nil {
				if a.Valid && b.Valid {
					names[a.String] = b.String
				}
			}
		}
		if len(names) > 0 {
			return names, nil
		}
	}
	return names, nil
}

func parseInt64(s string) (int64, error) {
	// handle milliseconds in decimal string
	// avoid strconv import elsewhere by relying on time.ParseDuration failback
	// Use a simple manual parse to int64
	var n int64
	neg := false
	for i, r := range s {
		if i == 0 && (r == '+' || r == '-') {
			if r == '-' {
				neg = true
			}
			continue
		}
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("not an integer: %q", s)
		}
		n = n*10 + int64(r-'0')
	}
	if neg {
		n = -n
	}
	return n, nil
}
