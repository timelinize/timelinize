/*
	Timelinize
	Copyright (c) 2024 Sergio Rubio

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

// Package firefox implements a data source that imports visited sites
// from Firefox's places.sqlite database.
package firefox

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/timelinize/timelinize/timeline"
	"go.uber.org/zap"
)

// Data source name and ID.
const (
	DataSourceName = "Firefox"
	DataSourceID   = "firefox"
)

func init() {
	err := timeline.RegisterDataSource(timeline.DataSource{
		Name:            DataSourceID,
		Title:           DataSourceName,
		Icon:            "firefox.svg",
		Description:     "Firefox places.sqlite database importer",
		NewFileImporter: func() timeline.FileImporter { return new(Firefox) },
	})
	if err != nil {
		timeline.Log.Fatal("registering data source", zap.Error(err))
	}
}

// Firefox interacts with the places.sqlite database to get visited sites.
type Firefox struct{}

// Recognize returns whether the input file is recognized.
func (Firefox) Recognize(_ context.Context, dirEntry timeline.DirEntry, _ timeline.RecognizeParams) (timeline.Recognition, error) {
	if filepath.Base(dirEntry.Name()) == "places.sqlite" {
		var ok bool
		var err error
		if ok, err = checkTables(dirEntry.FullPath()); err != nil {
			return timeline.Recognition{}, fmt.Errorf("checking table existence: %w", err)
		}

		if ok {
			return timeline.Recognition{Confidence: 1}, nil
		}
	}

	return timeline.Recognition{}, nil
}

// FileImport conducts an import of the data using this data source.
func (f *Firefox) FileImport(ctx context.Context, dirEntry timeline.DirEntry, params timeline.ImportParams) error {
	return f.process(ctx, dirEntry.FullPath(), params.Pipeline)
}

// process reads the places.sqlite database and sends the items to the channel.
func (f *Firefox) process(ctx context.Context, path string, itemChan chan<- *timeline.Graph) error {
	tempDir, err := os.MkdirTemp("", "firefox_import_*")
	if err != nil {
		return fmt.Errorf("creating temp directory: %w", err)
	}
	defer os.RemoveAll(tempDir)

	// Open the database in read-only mode
	db, err := openDB(path, tempDir)
	if err != nil {
		return fmt.Errorf("opening database in read-only mode: %w", err)
	}
	defer db.Close()

	// Verify that we can read from the database
	err = db.Ping()
	if err != nil {
		return fmt.Errorf("verifying database connection: %w", err)
	}

	rows, err := db.QueryContext(ctx, `
		SELECT p.url, p.title, p.description, h.visit_date
		FROM moz_places p
		INNER JOIN moz_historyvisits h
		ON p.id = h.place_id
	`)
	if err != nil {
		return fmt.Errorf("querying database: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var url string
		var title, description sql.NullString
		var visitDate int64

		err := rows.Scan(&url, &title, &description, &visitDate)
		if err != nil {
			return fmt.Errorf("scanning row: %w", err)
		}

		timestamp := time.Unix(0, visitDate*1000) //nolint:mnd // Convert microseconds to nanoseconds

		select {
		case <-ctx.Done():
			if ctx.Err() == context.Canceled {
				return nil
			}
			return ctx.Err()
		default:
			item := &timeline.Item{
				Classification:       timeline.ClassPageView,
				Timestamp:            timestamp,
				IntermediateLocation: path,
				Content: timeline.ItemData{
					Data: timeline.StringData(url),
				},
				Metadata: timeline.Metadata{
					"URL":         url,
					"Title":       title.String,
					"Visit date":  timestamp.Format("2006-01-02 15:04:05"),
					"Description": description.String,
				},
			}

			itemChan <- &timeline.Graph{Item: item}
		}
	}

	if err := rows.Err(); err != nil {
		return fmt.Errorf("row iteration error: %w", err)
	}

	return nil
}

func checkTables(src string) (bool, error) {
	tempDir, err := os.MkdirTemp("", "firefox_import_*")
	if err != nil {
		return false, fmt.Errorf("creating temp directory: %w", err)
	}
	defer os.RemoveAll(tempDir)

	db, err := openDB(src, tempDir)
	if err != nil {
		return false, fmt.Errorf("opening database: %w", err)
	}
	defer db.Close()

	var exists bool
	err = db.QueryRow("SELECT 1 FROM sqlite_master WHERE type='table' AND name='moz_places'").Scan(&exists)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, fmt.Errorf("checking table existence: %w", err)
	}
	return exists, nil
}

func openDB(src, tempDir string) (*sql.DB, error) {
	tmpDB := filepath.Join(tempDir, filepath.Base(src))
	sourceFile, err := os.Open(src)
	if err != nil {
		return nil, fmt.Errorf("opening source file: %w", err)
	}
	defer sourceFile.Close()

	destFile, err := os.Create(tmpDB)
	if err != nil {
		return nil, fmt.Errorf("creating destination file: %w", err)
	}
	defer destFile.Close()

	_, err = io.Copy(destFile, sourceFile)
	if err != nil {
		return nil, fmt.Errorf("copying file contents from %s to %s: %w", src, tmpDB, err)
	}

	// Open the database in read-only mode
	db, err := sql.Open("sqlite3", fmt.Sprintf("file:%s?mode=ro", tmpDB))
	if err != nil {
		return nil, fmt.Errorf("opening database in read-only mode: %w", err)
	}

	return db, nil
}
