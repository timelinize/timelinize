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

func (ej embeddingJob) Run(job *ActiveJob, checkpoint []byte) error {
	var startIdx int
	if checkpoint != nil {
		if err := json.Unmarshal(checkpoint, &startIdx); err != nil {
			job.logger.Error("failed to resume from checkpoint", zap.Error(err))
		}
	}

	// run each task in a goroutine which we group by batch; this allows
	// us to throttle concurrent goroutines, run tasks in parallel for speed,
	// and checkpoint after the entire batch has finished, which allows for
	// reliable resumption
	var wg sync.WaitGroup
	const batchSize = 10

	for i := startIdx; i < len(ej.ItemIDs); i++ {
		// At the end of every batch, wait for all the goroutines to complete before
		// proceeding; and once they complete, that's a good time to checkpoint
		if i%batchSize == batchSize-1 {
			wg.Wait()

			if err := job.Checkpoint(i); err != nil {
				job.logger.Error("failed to save checkpoint",
					zap.Int("position", i),
					zap.Error(err))
			}
		}

		itemID := ej.ItemIDs[i]

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

	// all goroutines must be done before we return
	wg.Wait()

	return nil
}

func (ej embeddingJob) generateEmbeddingForItem(ctx context.Context, job *ActiveJob, itemID int64) error {
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
