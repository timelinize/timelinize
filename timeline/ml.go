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
	"strings"

	sqlite_vec "github.com/asg017/sqlite-vec-go-bindings/cgo"
	"go.uber.org/zap"
)

const mlServer = "http://127.0.0.1:12003/"

func GenerateSerializedEmbedding(ctx context.Context, dataType string, data []byte) ([]byte, error) {
	embedding, err := generateEmbedding(ctx, dataType, data, nil)
	if err != nil {
		return nil, err
	}
	v, err := sqlite_vec.SerializeFloat32(embedding)
	if err != nil {
		return nil, fmt.Errorf("serializing embedding: %w", err)
	}
	return v, nil
}

func (tl *Timeline) GenerateEmbeddingForItem(ctx context.Context, itemID int64, dataType string, dataFile, dataText *string) error {
	if dataFile != nil && dataText != nil {
		return fmt.Errorf("data_file and data_text cannot both be set (data_file=%s data_text=%s item_id=%d)",
			*dataFile, *dataText, itemID)
	}

	var data []byte
	if dataText != nil {
		data = []byte(*dataText)
	}
	var filename *string
	if dataFile != nil {
		fn := tl.FullPath(*dataFile)
		filename = &fn
	}

	embedding, err := generateEmbedding(ctx, dataType, data, filename)
	if err != nil {
		return err
	}

	v, err := sqlite_vec.SerializeFloat32(embedding)
	if err != nil {
		return fmt.Errorf("serializing embedding: %w", err)
	}

	tl.dbMu.Lock()
	defer tl.dbMu.Unlock()

	tx, err := tl.db.Begin()
	if err != nil {
		return fmt.Errorf("opening transaction: %w", err)
	}
	defer tx.Rollback()

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

func (p *processor) generateEmbeddingsForImportedItems() {
	p.tl.dbMu.RLock()
	rows, err := p.tl.db.QueryContext(p.tl.ctx, `
		SELECT id, data_type, data_text, data_file
		FROM items
		WHERE job_id=?
			AND data_type IS NOT NULL
			AND (data_text IS NOT NULL OR data_file IS NOT NULL)
		`, p.jobRow.id)
	if err != nil {
		p.log.Error("unable to generate embeddings from this import",
			zap.Int64("job_id", p.jobRow.id),
			zap.Error(err))
		return
	}

	type rowInfo struct {
		rowID    int64
		dataType string
		dataText *string
		dataFile *string
	}
	embeddingsNeeded := make(map[int64]rowInfo) // map key is item ID; useful for deduplicating if shared by other items

	for rows.Next() {
		var rowID int64
		var dataType, dataText, dataFile *string

		err := rows.Scan(&rowID, &dataType, &dataText, &dataFile)
		if err != nil {
			p.log.Error("unable to scan row to generate embeddings from this import",
				zap.Int64("job_id", p.jobRow.id),
				zap.Error(err))
			defer p.tl.dbMu.RUnlock()
			defer rows.Close()
			return
		}

		if !strings.HasPrefix(*dataType, "image/") && !strings.HasPrefix(*dataType, "text/") {
			continue
		}

		// TODO: use thumbnails if available, they should be used instead
		embeddingsNeeded[rowID] = rowInfo{rowID, *dataType, dataText, dataFile}
	}
	rows.Close()
	p.tl.dbMu.RUnlock()
	if err = rows.Err(); err != nil {
		p.log.Error("iterating rows for generating embeddings failed",
			zap.Int64("job_id", p.jobRow.id),
			zap.Error(err))
		return
	}

	// now that our read lock on the DB is released, we can take our time generating embeddings in the background

	p.log.Info("generating embeddings for imported items", zap.Int("count", len(embeddingsNeeded)))

	const maxConcurrency = 10
	throttle := make(chan struct{}, maxConcurrency)

	for _, taskInfo := range embeddingsNeeded {
		throttle <- struct{}{}
		go func() {
			defer func() { <-throttle }()

			err := p.tl.GenerateEmbeddingForItem(p.tl.ctx, taskInfo.rowID, taskInfo.dataType, taskInfo.dataFile, taskInfo.dataText)
			if err != nil {
				p.log.Error("failed generating embedding",
					zap.Int64("job_id", p.jobRow.id),
					zap.Int64("row_id", taskInfo.rowID),
					zap.String("data_type", taskInfo.dataType),
					zap.Stringp("data_file", taskInfo.dataFile),
					zap.Stringp("data_text", taskInfo.dataText),
					zap.Error(err))
			}
		}()
	}
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
