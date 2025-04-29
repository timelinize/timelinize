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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	sqlite_vec "github.com/asg017/sqlite-vec-go-bindings/cgo"
	"go.uber.org/zap"
)

const (
	PyHost = "127.0.0.1"
	PyPort = 12003
)

type embeddingJob struct {
	// be sure not to include duplicates in this list
	ItemIDs []int64 `json:"item_ids,omitempty"`

	// infer items from the given import job
	ItemsFromImportJob uint64 `json:"items_from_import_job,omitempty"`
}

func (ej embeddingJob) Run(job *ActiveJob, checkpoint []byte) error {
	var chkpt embeddingJobCheckpoint
	if checkpoint != nil {
		if err := json.Unmarshal(checkpoint, &chkpt); err != nil {
			job.logger.Error("failed to resume from checkpoint", zap.Error(err))
		}
		job.Logger().Info("resuming from checkpoint",
			zap.Int64("page_start", chkpt.PageStart),
			zap.Int("position", chkpt.IndexOnPage),
			zap.Int("configured_item_count", len(ej.ItemIDs)),
			zap.Uint64("items_from_import_job", ej.ItemsFromImportJob))
	}

	// if the embeddings to generate were explicitly enumerated, simply do those
	if len(ej.ItemIDs) > 0 {
		job.Logger().Info("waiting until Python server is ready")
		if !pythonServerReady(job.ctx, true) {
			return errors.New("python server not ready")
		}
		job.Logger().Info("generating embeddings using predefined list", zap.Int("count", len(ej.ItemIDs)))
		return ej.processInBatches(job, ej.ItemIDs, 0, chkpt.IndexOnPage)
	}

	if ej.ItemsFromImportJob == 0 {
		return errors.New("no items; expecting either individual items listed, or an import job")
	}

	logger := job.Logger().With(zap.Uint64("import_job_id", ej.ItemsFromImportJob))

	logger.Info("counting total size of job")

	const mostOfQuery = `FROM items
			LEFT JOIN embeddings ON items.embedding_id = embeddings.id
			WHERE items.job_id=?
				AND (items.data_type LIKE 'image/%' OR items.data_type LIKE 'text/%')
				AND (items.data_id IS NOT NULL OR items.data_text IS NOT NULL OR items.data_file IS NOT NULL)
				AND (items.embedding_id IS NULL OR embeddings.generated < items.stored OR embeddings.generated < items.modified)`

	job.tl.dbMu.RLock()
	var jobSize int
	err := job.tl.db.QueryRowContext(job.ctx, `
		SELECT count()
			`+mostOfQuery,
		ej.ItemsFromImportJob).Scan(&jobSize)
	job.tl.dbMu.RUnlock()
	if err != nil {
		return fmt.Errorf("failed counting size of job: %w", err)
	}
	job.SetTotal(jobSize)

	if jobSize == 0 {
		logger.Info("nothing to do", zap.Int("count", jobSize))
		return nil
	}

	logger.Info("waiting until Python server is ready")

	if !pythonServerReady(job.ctx, true) {
		return errors.New("python server not ready")
	}

	logger.Info("generating embeddings for items from import job", zap.Int("count", jobSize))

	var thisPageStart int64
	lastItemID := chkpt.PageStart

	for {
		var pageResults []int64

		const pageSize = 1000

		thisPageStart = lastItemID

		// select items from the configured import job that have a data type we can generate embeddings for,
		// and which actually have data, and which do not yet have embeddings (OR the embedding is outdated
		// and the item was updated more recently than the embedding), and which are on this page of results
		job.tl.dbMu.RLock()
		rows, err := job.tl.db.QueryContext(job.ctx, `
			SELECT items.id, items.stored, items.data_type
			`+mostOfQuery+`
				AND items.id > ?
			ORDER BY items.id
			LIMIT ?
			`, ej.ItemsFromImportJob, lastItemID, pageSize)
		if err != nil {
			job.tl.dbMu.RUnlock()
			return fmt.Errorf("failed querying page of database table: %w", err)
		}
		var hadRow bool
		for rows.Next() {
			hadRow = true

			var rowID, stored int64
			var dataType *string

			err := rows.Scan(&rowID, &stored, &dataType)
			if err != nil {
				defer rows.Close()
				job.tl.dbMu.RUnlock()
				return fmt.Errorf("failed to scan row from database page: %w", err)
			}

			// Keep the last item ID as a fast way to offset for the next page
			lastItemID = rowID

			if qualifiesForEmbedding(dataType) {
				pageResults = append(pageResults, rowID)
			}
		}
		rows.Close()
		job.tl.dbMu.RUnlock()
		if err = rows.Err(); err != nil {
			return fmt.Errorf("iterating rows for researching embeddings failed: %w", err)
		}

		if !hadRow {
			break // all done!
		}

		if len(pageResults) == 0 {
			continue
		}

		if err := ej.processInBatches(job, pageResults, thisPageStart, chkpt.IndexOnPage); err != nil {
			return fmt.Errorf("processing page of embedding tasks: %w", err)
		}

		// clear this so that we don't skip items on the next page after the first one, when resuming from a checkpoint!
		chkpt.IndexOnPage = 0
	}

	return nil
}

func (ej embeddingJob) processInBatches(job *ActiveJob, itemIDs []int64, firstItemIDOfPage int64, startIndexInPage int) error {
	// run each task in a goroutine which we group by batch; this allows
	// us to throttle concurrent goroutines, run tasks in parallel for speed,
	// and checkpoint after the entire batch has finished, which allows for
	// reliable resumption
	var wg sync.WaitGroup
	const batchSize = 10

	// all goroutines must be done before we return
	defer wg.Wait()

	for i := startIndexInPage; i < len(itemIDs); i++ {
		if err := job.Continue(); err != nil {
			return err
		}

		// At the end of every batch, wait for all the goroutines to complete before
		// proceeding; and once they complete, that's a good time to checkpoint
		if i%batchSize == batchSize-1 {
			wg.Wait()

			if err := job.Checkpoint(embeddingJobCheckpoint{
				PageStart:   firstItemIDOfPage,
				IndexOnPage: i,
			}); err != nil {
				job.logger.Error("failed to save checkpoint",
					zap.Int("position", i),
					zap.Error(err))
			}
		}

		itemID := itemIDs[i]

		// proceed to spawn a new goroutine as part of this batch
		wg.Add(1)
		go func(job *ActiveJob, itemID int64) {
			defer wg.Done()

			err := ej.generateEmbeddingForItem(job.Context(), job, itemID)
			if err != nil {
				job.logger.Error("failed generating embedding",
					zap.Int64("item_id", itemID),
					zap.Error(err))
			}

			job.Progress(1)
		}(job, itemID)
	}

	return nil
}

func (ej embeddingJob) generateEmbeddingForItem(ctx context.Context, job *ActiveJob, itemID int64) error {
	var data []byte
	var dataFile, dataText, dataType, filename *string

	// we query all the ways the content of the item could be stored: as a file (get its filename),
	// text (read the text directly from the DB), and a separate data table (read the blob directly
	// from the DB); only 1 of those should be non-nil, so we set the "data" variable to whichever
	// one it is, to be sure we send it in for an embedding
	job.tl.dbMu.RLock()
	err := job.tl.db.QueryRowContext(ctx,
		`SELECT items.data_file, items.data_text, items.data_type, item_data.content
		FROM items
		LEFT JOIN item_data ON item_data.id = items.data_id
		WHERE items.id=?
		LIMIT 1`, itemID).Scan(&dataFile, &dataText, &dataType, &data)
	job.tl.dbMu.RUnlock()
	if err != nil {
		return fmt.Errorf("querying item for which to generate embedding: %w", err)
	}
	if dataType == nil {
		return fmt.Errorf("item %d has no data type", itemID)
	}

	// convert data file path (if there is one) into a full filename
	if dataFile != nil {
		fn := job.tl.FullPath(*dataFile)
		filename = &fn
	}
	// if the item is text content in the DB, set the data as it so it gets passed in for an embedding
	if data == nil && dataText != nil {
		data = []byte(*dataText)
	}

	v, err := generateSerializedEmbedding(ctx, *dataType, data, filename)
	if err != nil {
		return err
	}

	job.tl.dbMu.Lock()
	defer job.tl.dbMu.Unlock()

	tx, err := job.tl.db.Begin()
	if err != nil {
		return fmt.Errorf("opening transaction: %w", err)
	}
	defer tx.Rollback()

	// TODO: Why don't we use RETURNING here? is it because the tx isn't committed?
	_, err = tx.ExecContext(ctx, "INSERT INTO embeddings (embedding) VALUES (?)", v)
	if err != nil {
		return fmt.Errorf("storing embedding for item %d: %w", itemID, err)
	}

	var embedRowID int64
	err = tx.QueryRowContext(ctx, "SELECT last_insert_rowid() FROM embeddings LIMIT 1").Scan(&embedRowID)
	if err != nil {
		return fmt.Errorf("getting last-stored embedding ID for item %d: %w", itemID, err)
	}

	_, err = tx.Exec(`UPDATE items SET embedding_id=? WHERE id=?`, embedRowID, itemID) // TODO: LIMIT 1
	if err != nil {
		return fmt.Errorf("linking item %d to embedding: %w", itemID, err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing transaction: %w", err)
	}

	return nil
}

func generateSerializedEmbedding(ctx context.Context, dataType string, data []byte, filename *string) ([]byte, error) {
	embeddingJSON, err := generateEmbedding(ctx, dataType, data, filename)
	if err != nil {
		return nil, err
	}
	var embedding []float32
	err = json.Unmarshal(embeddingJSON, &embedding)
	if err != nil {
		return nil, fmt.Errorf("unmarshaling JSON embedding: %w", err)
	}
	v, err := sqlite_vec.SerializeFloat32(embedding)
	if err != nil {
		return nil, fmt.Errorf("serializing embedding: %w", err)
	}
	return v, nil
}

func generateEmbedding(ctx context.Context, dataType string, data []byte, filename *string) ([]byte, error) {
	if dataType == "" {
		return nil, errors.New("content type is required")
	}

	endpoint := pyServerURL("/embedding")

	var body io.Reader
	if data != nil {
		body = bytes.NewReader(data)
	}
	if filename != nil {
		qs := make(url.Values)
		qs.Set("filename", *filename)
		endpoint += "?" + qs.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, "QUERY", endpoint, body)
	if err != nil {
		return nil, fmt.Errorf("making request to generate embedding: %w", err)
	}
	req.Header.Set("Content-Type", dataType)

	// throttle expensive operation
	cpuIntensiveThrottle <- struct{}{}
	defer func() { <-cpuIntensiveThrottle }()

	// check here in case the job was cancelled while we waited on the throttle
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("performing embedding request to ML server: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		const maxSize = 1024 * 100
		msg, err := io.ReadAll(io.LimitReader(resp.Body, maxSize))
		if err != nil {
			return nil, fmt.Errorf("error reading error response from ML server, HTTP %d: %w", resp.StatusCode, err)
		}
		return nil, fmt.Errorf("got error status from ML server: HTTP %d (message='%s')", resp.StatusCode, msg)
	}

	const maxSize = 1024 * 100
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxSize))
	if err != nil {
		return nil, err
	}

	return respBody, nil
}

// TODO: endpoint currently works for images only
func classify(ctx context.Context, itemFiles map[uint64]string, labels []string) (map[uint64]float64, error) {
	endpoint := pyServerURL("/classify")

	jsonBytes, err := json.Marshal(itemFiles)
	if err != nil {
		return nil, err
	}
	body := bytes.NewReader(jsonBytes)

	qs := make(url.Values)
	for _, label := range labels {
		qs.Add("labels", label)
	}
	endpoint += "?" + qs.Encode()

	req, err := http.NewRequestWithContext(ctx, "QUERY", endpoint, body)
	if err != nil {
		return nil, fmt.Errorf("making request to classify: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("performing classification request to ML server: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		const maxSize = 1024 * 10
		msg, err := io.ReadAll(io.LimitReader(resp.Body, maxSize))
		if err != nil {
			return nil, fmt.Errorf("error reading error response from ML server, HTTP %d: %w", resp.StatusCode, err)
		}
		return nil, fmt.Errorf("got error status from ML server: HTTP %d (message='%s')", resp.StatusCode, msg)
	}

	var scores map[uint64]float64
	err = json.NewDecoder(resp.Body).Decode(&scores)
	if err != nil {
		return nil, fmt.Errorf("decoding JSON response: %w", err)
	}

	return scores, nil
}

func qualifiesForEmbedding(mimeType *string) bool {
	return mimeType != nil &&
		(strings.HasPrefix(*mimeType, "image/") ||
			strings.HasPrefix(*mimeType, "text/"))
}

type embeddingJobCheckpoint struct {
	PageStart   int64 `json:"page_start"`
	IndexOnPage int   `json:"index_on_page"`
}

func pythonServerReady(ctx context.Context, wait bool) bool {
	healthCheckURL := pyServerURL("/health-check")

	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, healthCheckURL, nil)
		if err != nil {
			panic("could not construct health check request: " + err.Error())
		}

		resp, err := pythonServerHTTPClient.Do(req)
		if err == nil && resp.StatusCode >= 200 && resp.StatusCode < 300 {
			resp.Body.Close()
			return true
		}

		if !wait {
			return false
		}

		const pause = 500 * time.Millisecond
		select {
		case <-time.After(pause):
		case <-ctx.Done():
			return false
		}
	}
}

func pyServerURL(path string) string {
	hostPort := net.JoinHostPort(PyHost, strconv.Itoa(PyPort))
	return fmt.Sprintf("http://%s%s", hostPort, path)
}

var pythonServerHTTPClient = &http.Client{
	Timeout: 2 * time.Second,
}
