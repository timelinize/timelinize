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
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/zeebo/blake3"
)

// ImportParameters describe an import.
type ImportParameters struct {
	ResumeJobID int64 `json:"resume_job_id"`

	DataSourceName string `json:"data_source_name"`
	// TODO: we might need a way to map filenames to the data source that will process them.
	Filenames         []string          `json:"filenames,omitempty"`  // file imports
	AccountID         int64             `json:"account_id,omitempty"` // API imports
	ProcessingOptions ProcessingOptions `json:"processing_options,omitempty"`
	DataSourceOptions json.RawMessage   `json:"data_source_options,omitempty"`

	JobID string `json:"job_id"` // assigned by application frontend
}

// Hash makes a hash of the import params.
func (params ImportParameters) Hash(repoID string) string {
	accountIDOrFilename := "files:" + strings.Join(params.Filenames, ",")
	if params.AccountID > 0 {
		accountIDOrFilename = "account:" + strconv.Itoa(int(params.AccountID))
	}
	str := fmt.Sprintf("%s:%s:%s", repoID, params.DataSourceName, accountIDOrFilename)
	hash := blake3.Sum256([]byte(str))
	return hex.EncodeToString(hash[:])
}

// TODO: needs to be updated to use schema updates (job table instead of imports table)
type jobRow struct {
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

func (tl *Timeline) loadJob(ctx context.Context, importID int64) (jobRow, error) {
	var job jobRow
	var snapshotTS *int64
	tl.dbMu.RLock()
	err := tl.db.QueryRowContext(ctx,
		`SELECT
			jobs.id, jobs.mode, jobs.snapshot_date, jobs.account_id,
			jobs.started, jobs.ended, jobs.status, jobs.checkpoint,
			data_source.name
		FROM jobs, data_sources
		WHERE jobs.id=?
			AND data_sources.id = accounts.data_source_id
		LIMIT 1`,
		// TODO: I'm pretty sure these won't all scan correctly, currently
		importID).Scan(&job.id, &job.mode, &snapshotTS, &job.accountID, &job.started, &job.ended,
		&job.status, &job.checkpointBytes, &job.dataSourceName)
	tl.dbMu.RUnlock()
	if err != nil {
		return job, fmt.Errorf("querying import %d from DB: %w", importID, err)
	}
	if len(job.checkpointBytes) > 0 {
		err = unmarshalGob(job.checkpointBytes, job.checkpoint)
		if err != nil {
			return job, fmt.Errorf("decoding checkpoint: %w", err)
		}
	}
	if snapshotTS != nil {
		ts := time.Unix(*snapshotTS, 0)
		job.snapshotDate = &ts
	}
	return job, nil
}

func (tl *Timeline) newJob(ctx context.Context, dataSourceID string, mode importMode, procOpt ProcessingOptions, accountID int64) (jobRow, error) {
	// ensure data source of the import and data source of the account are the same
	// (this should always be the case, but sanity check here to prevent confusion)
	if accountID > 0 {
		var accountDataSourceID string
		tl.dbMu.RLock()
		err := tl.db.QueryRowContext(ctx,
			`SELECT data_sources.name
		FROM data_sources, accounts
		WHERE accounts.id = ? AND data_source.id = accounts.data_source_id
		LIMIT 1`,
			accountID).Scan(&accountDataSourceID)
		tl.dbMu.RUnlock()
		if err != nil {
			return jobRow{}, fmt.Errorf("querying DB to verify data source IDs: %w", err)
		}
		if accountDataSourceID != dataSourceID {
			return jobRow{}, fmt.Errorf("data source ID and account's data source ID do not match: %s vs. %s",
				dataSourceID, accountDataSourceID)
		}
	}

	// TODO: Maybe this (getting a data source's row ID) should be a separate function
	var dataSourceRowID int64
	tl.dbMu.RLock()
	err := tl.db.QueryRowContext(ctx, `SELECT id FROM data_sources WHERE name=? LIMIT 1`,
		dataSourceID).Scan(&dataSourceRowID)
	tl.dbMu.RUnlock()
	if err != nil {
		return jobRow{}, fmt.Errorf("querying data source's row ID: %s: %w", dataSourceID, err)
	}

	imp := jobRow{
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
			return jobRow{}, fmt.Errorf("marshaling processing options: %w", err)
		}
	}

	hostname, _ := os.Hostname()

	var started int64
	tl.dbMu.Lock()
	// TODO: update this when we are done figuring this out...
	err = tl.db.QueryRow(`INSERT INTO jobs (action, configuration, status, hostname)
		VALUES (?, ?, ?, ?)
		RETURNING id, start, status`,
		"import", string(procOptJSON), "started", hostname).Scan(&imp.id, &started, &imp.status)
	tl.dbMu.Unlock()
	if err != nil {
		return jobRow{}, fmt.Errorf("inserting job row into DB: %w", err)
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
