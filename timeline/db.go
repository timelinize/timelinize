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
	"database/sql"
	_ "embed"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	_ "github.com/mattn/go-sqlite3" // register the sqlite3 driver
)

//go:embed schema.sql
var createDB string

func openAndProvisionDB(repoDir string) (*sql.DB, error) {
	db, err := openDB(repoDir)
	if err != nil {
		return nil, err
	}
	if err = provisionDB(db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func openDB(repoDir string) (*sql.DB, error) {
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

	return db, nil
}

func provisionDB(db *sql.DB) error {
	_, err := db.Exec(createDB)
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
	err = saveAllDataSources(db)
	if err != nil {
		return fmt.Errorf("saving registered data sources to database: %w", err)
	}

	// add all standard classifications
	err = saveAllStandardClassifications(db)
	if err != nil {
		return fmt.Errorf("saving standard classifications to database: %w", err)
	}

	// add all standard entity types
	err = saveAllStandardEntityTypes(db)
	if err != nil {
		return fmt.Errorf("saving standard entity types to database: %w", err)
	}

	return nil
}

func saveAllDataSources(db *sql.DB) error {
	if len(dataSources) == 0 {
		return nil
	}

	query := `INSERT OR IGNORE INTO "data_sources" ("name") VALUES`

	vals := make([]any, 0, len(dataSources))
	var count int

	for _, ds := range dataSources {
		if count > 0 {
			query += ","
		}
		query += " (?)"
		vals = append(vals, ds.Name)
		count++
	}

	_, err := db.Exec(query, vals...)
	if err != nil {
		return fmt.Errorf("writing data sources to DB: %w", err)
	}

	return nil
}

func saveAllStandardEntityTypes(db *sql.DB) error {
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

	_, err := db.Exec(query, vals...)
	if err != nil {
		return fmt.Errorf("writing standard entity types to DB: %w", err)
	}

	return nil
}

func saveAllStandardClassifications(db *sql.DB) error {
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

	_, err := db.Exec(query, vals...)
	if err != nil {
		return fmt.Errorf("writing standard classifications to DB: %w", err)
	}

	return nil
}

func loadRepoID(db *sql.DB) (uuid.UUID, error) {
	var idStr string
	err := db.QueryRow(`SELECT value FROM repo WHERE key=? LIMIT 1`, "id").Scan(&idStr)
	if err != nil {
		return uuid.UUID{}, fmt.Errorf("selecting repo UUID: %w", err)
	}
	id, err := uuid.Parse(idStr)
	if err != nil {
		return uuid.UUID{}, fmt.Errorf("malformed UUID %s: %w", idStr, err)
	}
	return id, nil
}
