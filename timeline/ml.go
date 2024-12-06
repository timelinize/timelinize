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
	"net/http"
	"net/url"
	"sync"

	sqlite_vec "github.com/asg017/sqlite-vec-go-bindings/cgo"
	"go.uber.org/zap"
)

const mlServer = "http://127.0.0.1:12003/"

type embeddingJob struct {
	// be sure not to include duplicates in this list
	ItemIDs []int64 `json:"item_ids,omitempty"`
}

func (ej embeddingJob) Run(job *Job, checkpoint []byte) error {
	// TODO: Resume from checkpoint

	if ej.ItemIDs == nil {
		// TODO: create embeddings for all qualifying items that need one;
		// iterate DB in chunks of 100 or 1000, maybe?

		return nil
	}

	// limit goroutines spawning, just in case there's a LOT of them...
	// we need just enough to keep the CPU-intensive parts busy
	goroutineThrottle := make(chan struct{}, 20)

	var wg sync.WaitGroup

	for _, itemID := range ej.ItemIDs {
		goroutineThrottle <- struct{}{}
		wg.Add(1)

		// TODO: Should we just use the cpuIntensiveThrottle for these (and move those throttles out to here)?
		// TODO: It'd be nice if we could batch our DB operations.
		go func(job *Job, itemID int64) {
			defer wg.Done()

			err := ej.generateEmbeddingForItem(job.Context(), job, itemID)
			if err != nil {
				job.logger.Error("failed generating embedding",
					zap.Int64("item_id", itemID),
					zap.Error(err))
			}

			if err := job.UpdateProgress(1); err != nil {
				job.logger.Error("updating job progress", zap.Error(err))
			}

			<-goroutineThrottle
		}(job, itemID)
	}

	wg.Wait()

	return nil
}

func (ej embeddingJob) generateEmbeddingForItem(ctx context.Context, job *Job, itemID int64) error {
	var data []byte
	var dataFile, dataText, dataType, filename *string

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

	v, err := GenerateSerializedEmbedding(ctx, *dataType, data, filename)
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

func GenerateSerializedEmbedding(ctx context.Context, dataType string, data []byte, filename *string) ([]byte, error) {
	embedding, err := generateEmbedding(ctx, dataType, data, filename)
	if err != nil {
		return nil, err
	}
	v, err := sqlite_vec.SerializeFloat32(embedding)
	if err != nil {
		return nil, fmt.Errorf("serializing embedding: %w", err)
	}
	return v, nil
}

func generateEmbedding(ctx context.Context, dataType string, data []byte, filename *string) ([]float32, error) {
	if dataType == "" {
		return nil, errors.New("content type is required")
	}

	endpoint := mlServer + "/embedding"

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

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("performing embedding request to ML server: %w", err)
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

	var embedding []float32
	err = json.NewDecoder(resp.Body).Decode(&embedding)
	if err != nil {
		return nil, fmt.Errorf("decoding JSON response: %w", err)
	}

	return embedding, nil
}

// TODO: endpoint currently works for images only
func classify(ctx context.Context, itemFiles map[int64]string, labels []string) (map[int64]float64, error) {
	endpoint := mlServer + "/classify"

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

	var scores map[int64]float64
	err = json.NewDecoder(resp.Body).Decode(&scores)
	if err != nil {
		return nil, fmt.Errorf("decoding JSON response: %w", err)
	}

	return scores, nil
}
