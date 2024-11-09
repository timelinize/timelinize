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
		NewFileImporter: func() timeline.FileImporter { return new(Firefox) },
	})
	if err != nil {
		timeline.Log.Fatal("registering data source", zap.Error(err))
	}
}

// Firefox interacts with the places.sqlite database to get visited sites.
type Firefox struct{}

// Recognize returns whether the input file is recognized.
func (Firefox) Recognize(_ context.Context, filenames []string) (timeline.Recognition, error) {
	for _, filename := range filenames {
		if filepath.Base(filename) == "places.sqlite" {
			return timeline.Recognition{Confidence: 1}, nil
		}
	}
	return timeline.Recognition{}, nil
}

// FileImport conducts an import of the data using this data source.
func (f *Firefox) FileImport(ctx context.Context, filenames []string, itemChan chan<- *timeline.Graph, _ timeline.ListingOptions) error {
	for _, filename := range filenames {
		err := f.process(ctx, filename, itemChan)
		if err != nil {
			return fmt.Errorf("processing %s: %w", filename, err)
		}
	}
	return nil
}

// process reads the places.sqlite database and sends the items to the channel.
func (f *Firefox) process(ctx context.Context, path string, itemChan chan<- *timeline.Graph) error {
	tempDir, err := os.MkdirTemp("", "firefox_import_*")
	if err != nil {
		return fmt.Errorf("creating temp directory: %w", err)
	}
	defer os.RemoveAll(tempDir)

	tmpDB := filepath.Join(tempDir, filepath.Base(path))
	// copyFile copies the contents of the source file to the destination file.
	copyFile := func(src, dst string) error {
		sourceFile, err := os.Open(src)
		if err != nil {
			return fmt.Errorf("opening source file: %w", err)
		}
		defer sourceFile.Close()

		destFile, err := os.Create(dst)
		if err != nil {
			return fmt.Errorf("creating destination file: %w", err)
		}
		defer destFile.Close()

		_, err = io.Copy(destFile, sourceFile)
		if err != nil {
			return fmt.Errorf("copying file contents: %w", err)
		}

		return nil
	}
	err = copyFile(path, tmpDB)
	if err != nil {
		return fmt.Errorf("copying file to temp directory: %w", err)
	}

	// Open the database in read-only mode
	db, err := sql.Open("sqlite3", fmt.Sprintf("file:%s?mode=ro", tmpDB))
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
			return ctx.Err()
		default:
			item := &timeline.Item{
				Classification:       timeline.ClassPageView,
				Timestamp:            timestamp,
				IntermediateLocation: path,
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
