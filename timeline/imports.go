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
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/zeebo/blake3"
)

type ImportParameters struct {
	ResumeImportID int64 `json:"resume_import_id"`

	DataSourceName string `json:"data_source_name"`
	// TODO: we might need a way to map filenames to the data source that will process them.
	Filenames         []string          `json:"filenames,omitempty"`  // file imports
	AccountID         int64             `json:"account_id,omitempty"` // API imports
	ProcessingOptions ProcessingOptions `json:"processing_options,omitempty"`
	DataSourceOptions json.RawMessage   `json:"data_source_options,omitempty"`

	JobID string `json:"job_id"` // assigned by application frontend
}

func (params ImportParameters) Hash(repoID string) string {
	accountIDOrFilename := "files:" + strings.Join(params.Filenames, ",")
	if params.AccountID > 0 {
		accountIDOrFilename = "account:" + strconv.Itoa(int(params.AccountID))
	}
	str := fmt.Sprintf("%s:%s:%s", repoID, params.DataSourceName, accountIDOrFilename)
	hash := blake3.Sum256([]byte(str))
	return hex.EncodeToString(hash[:])
}

type importRow struct {
	id                int64
	dataSourceName    string // the actual row uses the row ID, this line uses the text ID ("name")
	mode              importMode
	processingOptions ProcessingOptions
	snapshotDate      *time.Time
	accountID         *int64
	started           time.Time
	ended             *time.Time
	status            importStatus
	checkpointBytes   []byte

	checkpoint *checkpoint // the decoded checkpointBytes
}

func (t *Timeline) loadImport(ctx context.Context, importID int64) (importRow, error) {
	var imp importRow
	var snapshotTs *int64
	t.dbMu.RLock()
	err := t.db.QueryRowContext(ctx,
		`SELECT
			imports.id, imports.mode, imports.snapshot_date, imports.account_id,
			imports.started, imports.ended, imports.status, imports.checkpoint,
			data_source.name
		FROM imports, data_sources
		WHERE imports.id=?
			AND data_sources.id = accounts.data_source_id
		LIMIT 1`,
		// TODO: I'm pretty sure these won't all scan correctly, currently
		importID).Scan(&imp.id, &imp.mode, &snapshotTs, &imp.accountID, &imp.started, &imp.ended,
		&imp.status, &imp.checkpointBytes, &imp.dataSourceName)
	t.dbMu.RUnlock()
	if err != nil {
		return imp, fmt.Errorf("querying import %d from DB: %v", importID, err)
	}
	if len(imp.checkpointBytes) > 0 {
		err = unmarshalGob(imp.checkpointBytes, imp.checkpoint)
		if err != nil {
			return imp, fmt.Errorf("decoding checkpoint: %v", err)
		}
	}
	if snapshotTs != nil {
		ts := time.Unix(*snapshotTs, 0)
		imp.snapshotDate = &ts
	}
	return imp, nil
}

func (t *Timeline) newImport(ctx context.Context, dataSourceID string, mode importMode, procOpt ProcessingOptions, accountID int64) (importRow, error) {
	// ensure data source of the import and data source of the account are the same
	// (this should always be the case, but sanity check here to prevent confusion)
	if accountID > 0 {
		var accountDataSourceID string
		t.dbMu.RLock()
		err := t.db.QueryRowContext(ctx,
			`SELECT data_sources.name
		FROM data_sources, accounts
		WHERE accounts.id = ? AND data_source.id = accounts.data_source_id
		LIMIT 1`,
			accountID).Scan(&accountDataSourceID)
		t.dbMu.RUnlock()
		if err != nil {
			return importRow{}, fmt.Errorf("querying DB to verify data source IDs: %v", err)
		}
		if accountDataSourceID != dataSourceID {
			return importRow{}, fmt.Errorf("data source ID and account's data source ID do not match: %s vs. %s",
				dataSourceID, accountDataSourceID)
		}
	}

	// TODO: Maybe this (getting a data source's row ID) should be a separate function
	var dataSourceRowID int64
	t.dbMu.RLock()
	err := t.db.QueryRowContext(ctx, `SELECT id FROM data_sources WHERE name=? LIMIT 1`,
		dataSourceID).Scan(&dataSourceRowID)
	t.dbMu.RUnlock()
	if err != nil {
		return importRow{}, fmt.Errorf("querying data source's row ID: %s: %v", dataSourceID, err)
	}

	imp := importRow{
		dataSourceName:    dataSourceID,
		mode:              mode,
		processingOptions: procOpt,
	}
	if accountID > 0 {
		imp.accountID = &accountID
	}

	var procOptJSON []byte
	if !imp.processingOptions.IsEmpty() {
		procOptJSON, err = json.Marshal(imp.processingOptions)
		if err != nil {
			return importRow{}, fmt.Errorf("marshaling processing options: %v", err)
		}
	}

	var started int64
	t.dbMu.Lock()
	err = t.db.QueryRow(`INSERT INTO imports (data_source_id, mode, account_id, processing_options)
		VALUES (?, ?, ?, ?)
		RETURNING id, started, status`,
		dataSourceRowID, imp.mode, imp.accountID, string(procOptJSON)).Scan(&imp.id, &started, &imp.status)
	t.dbMu.Unlock()
	if err != nil {
		return importRow{}, fmt.Errorf("inserting import row into DB: %v", err)
	}
	imp.started = time.Unix(started, 0)
	return imp, nil
}

type importMode string

const (
	importModeFile importMode = "file"
	importModeAPI  importMode = "api"
)

type importStatus string

const (
	importStatusStarted = "started"
	importStatusAborted = "abort"
	importStatusSuccess = "ok" // TODO: "success", to be clearer, maybe?
	importStatusError   = "err"
)
