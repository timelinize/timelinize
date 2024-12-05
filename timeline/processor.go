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
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"
)

type processor struct {
	// accessed atomically and 64-bit aligned; used by workers for estimating total size before running
	estimatedCount *int64

	ij ImportJob

	ds      DataSource
	dsRowID int64
	dsOpt   any

	tl *Timeline

	log      *zap.Logger
	progress *zap.Logger

	// batching inserts can greatly increase speed
	batch     []*Graph
	batchSize int // size is at least len(batch) but edges on a graph can add to it
	batchMu   *sync.Mutex

	// allow many concurrent file downloads as they can be massively parallel
	downloadThrottle chan struct{}
}

func (p processor) process(ctx context.Context, dirEntry DirEntry, dsCheckpoint json.RawMessage) error {
	params := ImportParams{
		Log:               p.log.With(zap.String("filename", dirEntry.FullPath())),
		Timeframe:         p.ij.ProcessingOptions.Timeframe,
		Checkpoint:        dsCheckpoint,
		DataSourceOptions: p.dsOpt,
	}

	// if enabled, estimate the units of work to be completed before starting the actual import
	if p.ij.EstimateTotal {
		params.Log.Info("estimating size; this may take a bit")

		fileImporter := p.ds.NewFileImporter()
		if estimator, ok := fileImporter.(SizeEstimator); ok {
			// faster (but probably still slow) path: data source supports optimized size estimation
			totalSize, err := estimator.EstimateSize(ctx, dirEntry, params)
			if err != nil {
				params.Log.Error("could not estimate import size", zap.Error(err))
			}
			newTotal := atomic.AddInt64(p.estimatedCount, int64(totalSize))
			if err = p.ij.job.SetTotal(int(newTotal)); err != nil {
				params.Log.Error("could not update total size", zap.Error(err))
			}
		} else {
			wg, ch := p.beginProcessing(ctx, p.ij.ProcessingOptions, true)
			params.Pipeline = ch

			// slow path: data source does not have an optimized estimator implementation, but we can count
			// the graph sizes that come in to do our own estimates
			err := fileImporter.FileImport(ctx, dirEntry, params)
			if err != nil {
				params.Log.Error("could not estimate size before import", zap.Error(err))
			}
			// we are no longer sending to the pipeline channel; closing it signals to the workers to exit
			close(ch)
			// wait for all processing workers to complete so we have an accurate count
			wg.Wait()
		}

		params.Log.Info("done with size estimation", zap.Int64("estimated_size", atomic.LoadInt64(p.estimatedCount)))
	}

	start := time.Now()

	wg, ch := p.beginProcessing(ctx, p.ij.ProcessingOptions, false)
	params.Pipeline = ch

	// even if we estimated size above, use a fresh file importer to avoid any potentially reused state (TODO: necessary?)
	err := p.ds.NewFileImporter().FileImport(ctx, dirEntry, params)
	// handle error in a little bit (see below)

	// we are no longer sending to the pipeline channel; closing it signals to the workers to exit
	close(ch)

	params.Log.Info("importer done sending items; waiting for processing to finish",
		zap.Int64("job_id", p.ij.job.id),
		zap.Error(err))

	// wait for all processing workers to complete
	wg.Wait()

	params.Log.Info("importer done sending items; waiting for processing to finish",
		zap.Int64("job_id", p.ij.job.id),
		zap.Error(err))

	// handle any error returned from import
	if err != nil {
		return fmt.Errorf("import: %w", err)
	}

	p.log.Info("import complete; cleaning up", zap.Duration("duration", time.Since(start)))

	err = p.successCleanup()
	if err != nil {
		return fmt.Errorf("processing completed, but error cleaning up: %w", err)
	}

	p.generateThumbnailsForImportedItems()
	p.generateEmbeddingsForImportedItems()

	return nil
}

func (p processor) successCleanup() error {
	// delete empty items from this import (items with no content and no meaningful relationships)
	if err := p.deleteEmptyItems(p.ij.job.id); err != nil {
		return fmt.Errorf("deleting empty items: %w (job_id=%d)", err, p.ij.job.id)
	}
	return nil
}

// deleteEmptyItems deletes items that have no content and no meaningful relationships,
// from the given import.
func (p *processor) deleteEmptyItems(importID int64) error {
	// TODO: we can perform the deletes all at once with the commented query below,
	// but it does not account for cleaning up the data files, which should only
	// be done if they're only used by the one item -- maybe we could use `RETURNING data_file` to take care of this?
	/*
		DELETE FROM items WHERE id IN (SELECT id FROM items
			WHERE job_id=?
			AND (data_text IS NULL OR data_text='')
				AND data_file IS NULL
				AND longitude IS NULL
				AND latitude IS NULL
				AND altitude IS NULL
				AND retrieval_key IS NULL
				AND id NOT IN (SELECT from_item_id FROM relationships WHERE to_item_id IS NOT NULL))
	*/

	// we actually keep rows with no content if they are in a relationship, or if
	// they have a retrieval key, which implies that they will be completed later
	// (bookmark items are also a special case: they may be empty, to later be populated
	// by a snapshot, as long as they have metadata)
	p.tl.dbMu.RLock()
	rows, err := p.tl.db.Query(`SELECT id FROM extended_items
		WHERE job_id=?
		AND (data_text IS NULL OR data_text='')
			AND (classification_name != ? OR metadata IS NULL)
			AND data_file IS NULL
			AND longitude IS NULL
			AND latitude IS NULL
			AND altitude IS NULL
			AND retrieval_key IS NULL
			AND id NOT IN (SELECT from_item_id FROM relationships WHERE to_item_id IS NOT NULL)`,
		importID, ClassBookmark.Name) // TODO: consider deleting regardless of relationships existing (remember the iMessage data source until we figured out why some referred-to rows were totally missing?)
	if err != nil {
		p.tl.dbMu.RUnlock()
		return fmt.Errorf("querying empty items: %w", err)
	}

	var emptyItems []int64
	for rows.Next() {
		var rowID int64
		err := rows.Scan(&rowID)
		if err != nil {
			defer p.tl.dbMu.RUnlock()
			defer rows.Close()
			return fmt.Errorf("scanning item: %w", err)
		}
		emptyItems = append(emptyItems, rowID)
	}
	rows.Close()
	p.tl.dbMu.RUnlock()
	if err = rows.Err(); err != nil {
		return fmt.Errorf("iterating item rows: %w", err)
	}

	// nothing to do if no items were empty
	if len(emptyItems) == 0 {
		return nil
	}

	p.log.Info("deleting empty items from this import",
		zap.Int64("job_id", importID),
		zap.Int("count", len(emptyItems)))

	retention := time.Duration(0)
	return p.tl.deleteItemRows(p.tl.ctx, emptyItems, false, &retention)
}

// generateThumbnailsForImportedItems generates thumbnails for qualifying items
// that were a part of the import associated with this processor. It should be
// run after the import completes.
// TODO: What about generating thumbnails for... just anything that needs one
func (p processor) generateThumbnailsForImportedItems() {
	job := thumbnailJob{
		DataIDs:   make(map[int64]thumbnailTask),
		DataFiles: make(map[string]thumbnailTask),
	}

	p.tl.dbMu.RLock()
	rows, err := p.tl.db.QueryContext(p.tl.ctx,
		`SELECT id, data_id, data_type, data_file
		FROM items
		WHERE job_id=? AND (data_file IS NOT NULL OR data_id IS NOT NULL)`, p.ij.job.id)
	if err != nil {
		p.tl.dbMu.RUnlock()
		p.log.Error("unable to generate thumbnails from this import",
			zap.Int64("job_id", p.ij.job.id),
			zap.Error(err))
		return
	}

	for rows.Next() {
		var rowID int64
		var dataID *int64
		var dataType, dataFile *string

		err := rows.Scan(&rowID, &dataID, &dataType, &dataFile)
		if err != nil {
			p.log.Error("unable to scan row to generate thumbnails from this import",
				zap.Int64("job_id", p.ij.job.id),
				zap.Error(err))
			rows.Close()
			p.tl.dbMu.RUnlock()
			return
		}
		if qualifiesForThumbnail(dataType) {
			if dataID != nil {
				job.DataIDs[*dataID] = thumbnailTask{
					tl:        p.tl,
					DataID:    rowID,
					DataType:  *dataType,
					ThumbType: thumbnailType(*dataType, false),
				}
			}
			if dataFile != nil {
				job.DataFiles[*dataFile] = thumbnailTask{
					tl:        p.tl,
					DataFile:  *dataFile,
					DataType:  *dataType,
					ThumbType: thumbnailType(*dataType, false),
				}
			}
		}
	}
	rows.Close()
	p.tl.dbMu.RUnlock()

	if err = rows.Err(); err != nil {
		p.log.Error("iterating rows for generating thumbnails failed",
			zap.Int64("job_id", p.ij.job.id),
			zap.Error(err))
		return
	}

	total := len(job.DataFiles) + len(job.DataIDs)

	p.log.Info("generating thumbnails for imported items", zap.Int("count", total))

	if _, err = p.tl.CreateJob(job, total, 0); err != nil {
		p.log.Error("creating thumbnail job", zap.Error(err))
		return
	}
}

func (p processor) generateEmbeddingsForImportedItems() {
	p.tl.dbMu.RLock()
	rows, err := p.tl.db.QueryContext(p.tl.ctx, `
		SELECT id, data_id, data_type, data_text, data_file
		FROM items
		WHERE job_id=?
			AND data_type IS NOT NULL
			AND (data_id IS NOT NULL OR data_text IS NOT NULL OR data_file IS NOT NULL)
		`, p.ij.job.id)
	if err != nil {
		p.tl.dbMu.RUnlock()
		p.log.Error("unable to generate embeddings from this import",
			zap.Int64("job_id", p.ij.job.id),
			zap.Error(err))
		return
	}

	job := embeddingJob{
		ItemIDs: make(map[int64]embeddingTask),
	}

	for rows.Next() {
		var rowID int64
		var dataID *int64
		var dataType, dataText, dataFile *string

		err := rows.Scan(&rowID, &dataID, &dataType, &dataText, &dataFile)
		if err != nil {
			p.log.Error("unable to scan row to generate embeddings from this import",
				zap.Int64("job_id", p.ij.job.id),
				zap.Error(err))
			rows.Close()
			p.tl.dbMu.RUnlock()
			return
		}

		if !strings.HasPrefix(*dataType, "image/") && !strings.HasPrefix(*dataType, "text/") {
			continue
		}

		// TODO: use thumbnails if available, maybe that should be used instead?
		job.ItemIDs[rowID] = embeddingTask{p.tl, rowID, *dataType, dataID, dataText, dataFile}
	}
	rows.Close()
	p.tl.dbMu.RUnlock()

	if err = rows.Err(); err != nil {
		p.log.Error("iterating rows for generating embeddings failed",
			zap.Int64("job_id", p.ij.job.id),
			zap.Error(err))
		return
	}

	total := len(job.ItemIDs)

	// now that our read lock on the DB is released, we can take our time generating embeddings in the background

	p.log.Info("generating embeddings for imported items", zap.Int("count", total))

	if _, err = p.tl.CreateJob(job, total, 0); err != nil {
		p.log.Error("creating embedding job", zap.Error(err))
		return
	}
}

// couldBeMarkdown is a very naive Markdown detector. I'm trying to avoid regexp for performance,
// but this implementation is (admittedly) mildly effective. May have lots of false positives.
func couldBeMarkdown(input []byte) bool {
	for _, pattern := range [][]byte{
		// links, images, and banners
		[]byte("](http"),
		[]byte("](/"),
		[]byte("!["), // images
		[]byte("[!"), // (GitHub-flavored) banners/notices
		// blocks
		[]byte("\n> "),
		[]byte("\n```"),
		// lists
		[]byte("\n1. "),
		[]byte("\n- "),
		[]byte("\n* "),
		// headings; ignore the h1 "# " since it could just as likely be a code comment
		[]byte("\n## "),
		[]byte("\n### "),
		[]byte("\n=\n"),
		[]byte("\n==\n"),
		[]byte("\n==="),
		[]byte("\n-\n"),
		[]byte("\n--\n"),
		[]byte("\n---"),
	} {
		if bytes.Contains(input, pattern) {
			return true
		}
	}
	for _, pair := range [][]byte{
		{'_'},
		{'*', '*'},
		{'*'},
		{'`'},
	} {
		// this isn't perfect, because the matching "end token" could have been truncated
		if count := bytes.Count(input, pair); count > 0 && count%2 == 0 {
			return true
		}
	}
	return false
}
