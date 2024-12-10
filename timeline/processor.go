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
	"encoding/json"
	"sync"
	"sync/atomic"

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
	if p.estimatedCount != nil {
		fileImporter := p.ds.NewFileImporter()
		if estimator, ok := fileImporter.(SizeEstimator); ok {
			// faster (but probably still slow) path: data source supports optimized size estimation
			totalSize, err := estimator.EstimateSize(ctx, dirEntry, params)
			if err != nil {
				params.Log.Error("could not estimate import size", zap.Error(err))
			}
			newTotal := atomic.AddInt64(p.estimatedCount, int64(totalSize))
			p.ij.job.SetTotal(int(newTotal))
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
		return nil
	}

	wg, ch := p.beginProcessing(ctx, p.ij.ProcessingOptions, false)
	params.Pipeline = ch

	// even if we estimated size above, use a fresh file importer to avoid any potentially reused state (TODO: necessary?)
	err := p.ds.NewFileImporter().FileImport(ctx, dirEntry, params)
	// handle error in a little bit (see below)

	// we are no longer sending to the pipeline channel; closing it signals to the workers to exit
	close(ch)

	params.Log.Info("importer done sending items; waiting for processing to finish", zap.Error(err))

	// wait for all processing workers to complete
	wg.Wait()

	return err
}
