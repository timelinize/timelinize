/*
	Timelinize
	Copyright (c) 2025 Sergio Rubio

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

package firefox

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/timelinize/timelinize/timeline"
)

func TestFirefox_Recognize(t *testing.T) {
	tests := []struct {
		name     string
		filename string
		want     float64
	}{
		{
			name:     "Places database",
			filename: "places.sqlite",
			want:     1,
		},
		{
			name:     "Other SQLite file",
			filename: "other.sqlite",
			want:     0,
		},
		{
			name:     "Non-SQLite file",
			filename: "test.txt",
			want:     0,
		},
	}

	f := new(Firefox)
	ctx := context.Background()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dirEntry := timeline.DirEntry{
				DirEntry: testDirEntry{name: tt.filename},
			}

			recognition, err := f.Recognize(ctx, dirEntry, timeline.RecognizeParams{})
			if err != nil {
				t.Errorf("Recognize() error = %v", err)
				return
			}

			if recognition.Confidence != tt.want {
				t.Errorf("Recognize() confidence = %v, want %v", recognition.Confidence, tt.want)
			}
		})
	}
}

func TestFirefox_FileImport(t *testing.T) {
	tmpDir := t.TempDir()

	dbFilename := "places.sqlite"
	dbPath := filepath.Join(tmpDir, dbFilename)
	db, err := createTestDatabase(dbPath)
	if err != nil {
		t.Fatalf("Failed to create test database: %v", err)
	}
	defer db.Close()

	visitTime := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	err = insertTestData(db, "https://example.com", "Example Site", "Test description", visitTime)
	if err != nil {
		t.Fatalf("Failed to insert test data: %v", err)
	}

	err = insertTestData(db, "https://example.com", "Example Site", "Test description", visitTime)
	if err != nil {
		t.Fatalf("Failed to insert test data: %v", err)
	}

	itemChan := make(chan *timeline.Graph)
	defer close(itemChan)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Run the import in a goroutine
	go func() {
		f := new(Firefox)
		dirEntry := timeline.DirEntry{
			DirEntry: testDirEntry{name: dbFilename},
			FSRoot:   tmpDir,
			Filename: dbFilename,
		}
		err := f.FileImport(ctx, dirEntry, timeline.ImportParams{
			Pipeline: itemChan,
		})
		if err != nil {
			t.Errorf("FileImport() error = %v", err)
		}
	}()

	count := 0
	for count < 2 {
		select {
		case graph := <-itemChan:
			count++
			if graph == nil || graph.Item == nil {
				t.Fatal("Expected non-nil item")
			}

			item := graph.Item
			if item.Classification.Name != timeline.ClassPageView.Name {
				t.Errorf("Expected ClassPageView, got %v", item.Classification)
			}

			if !item.Timestamp.Equal(visitTime) {
				t.Errorf("Expected timestamp %v, got %v", visitTime, item.Timestamp)
			}

			if url, ok := item.Metadata["URL"].(string); !ok || url != "https://example.com" {
				t.Errorf("Expected URL https://example.com, got %v", url)
			}

			if title, ok := item.Metadata["Title"].(string); !ok || title != "Example Site" {
				t.Errorf("Expected title 'Example Site', got %v", title)
			}

			if desc, ok := item.Metadata["Description"].(string); !ok || desc != "Test description" {
				t.Errorf("Expected description 'Test description', got %v", desc)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("Timeout waiting for imported item")
		}
	}

	if count != 2 {
		t.Errorf("Expected count 2, got %d", count)
	}
}

type testDirEntry struct {
	name string
}

func (t testDirEntry) Name() string               { return t.name }
func (t testDirEntry) IsDir() bool                { return false }
func (t testDirEntry) Type() os.FileMode          { return 0 }
func (t testDirEntry) Info() (os.FileInfo, error) { return nil, nil }

func createTestDatabase(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		return nil, err
	}

	_, err = db.Exec(`
		CREATE TABLE moz_places (
			id INTEGER PRIMARY KEY,
			url TEXT NOT NULL,
			title TEXT,
			description TEXT
		);
		CREATE TABLE moz_historyvisits (
			id INTEGER PRIMARY KEY,
			place_id INTEGER NOT NULL,
			visit_date INTEGER NOT NULL
		);
	`)
	if err != nil {
		db.Close()
		return nil, err
	}

	return db, nil
}

func insertTestData(db *sql.DB, url, title, description string, visitTime time.Time) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Insert place
	res, err := tx.Exec(`
		INSERT INTO moz_places (url, title, description)
		VALUES (?, ?, ?)
	`, url, title, description)
	if err != nil {
		return err
	}

	placeID, err := res.LastInsertId()
	if err != nil {
		return err
	}

	// Insert visit
	_, err = tx.Exec(`
		INSERT INTO moz_historyvisits (place_id, visit_date)
		VALUES (?, ?)
	`, placeID, visitTime.UnixNano()/1000) // Convert to microseconds

	if err != nil {
		return err
	}

	return tx.Commit()
}
