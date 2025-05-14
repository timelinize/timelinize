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
	"sync/atomic"

	"go.uber.org/zap"
)

type processor struct {
	// these three fields are used for checkpointing; the first is
	// accessed atomically and 64-bit aligned; also used for estimating
	// total size before running
	estimatedCount             *int64
	outerLoopIdx, innerLoopIdx int

	ij *ImportJob

	ds      DataSource
	dsRowID uint64
	dsOpt   any

	tl *Timeline

	log      *zap.Logger
	progress *zap.Logger

	// batching inserts can greatly increase speed
	batch     []*Graph
	batchSize int // size is at least len(batch) but edges on a graph can add to it

	// counter used for periodic DB optimization during an import
	// TODO: this should ideally be global per timeline, even if multiple jobs run simultaneously
	rootGraphCount int
}

func (p processor) process(ctx context.Context, dirEntry DirEntry, dsCheckpoint json.RawMessage) error {
	// don't allow the importer, which is processing who-knows-what input, to completely bring down the app
	defer func() {
		if r := recover(); r != nil {
			p.log.Error("panic", zap.Any("error", r))
		}
	}()

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
			// don't call SetTotal() yet -- wait until we're done counting,
			// so progress bars don't think we are done with the estimate
			atomic.AddInt64(p.estimatedCount, int64(totalSize))
		} else {
			done := make(chan struct{})
			wg, graphs := p.beginProcessing(ctx, p.ij.ProcessingOptions, true, done)
			params.Pipeline = graphs

			// slow path: data source does not have an optimized estimator implementation, but we can count
			// the graph sizes that come in to do our own estimates
			err := fileImporter.FileImport(ctx, dirEntry, params)
			if err != nil {
				params.Log.Error("failed estimating size before import", zap.Error(err))
			}

			// sending on the pipeline must be complete by now; signal to workers to exit
			close(done)

			// wait for all processing workers to complete so we have an accurate count
			wg.Wait()
		}
		return nil
	}

	done := make(chan struct{})
	wg, graphs := p.beginProcessing(ctx, p.ij.ProcessingOptions, false, done)
	params.Pipeline = graphs

	// even if we estimated size above, use a fresh file importer to avoid any potentially reused state (TODO: necessary?)
	err := p.ds.NewFileImporter().FileImport(ctx, dirEntry, params)
	// handle error in a little bit (see below)

	// sending on the pipeline must be complete by now; signal to workers to exit
	close(done)

	params.Log.Info("importer done sending items; waiting for processing to finish", zap.Error(err))

	// wait for all processing workers to complete
	wg.Wait()

	return err
}
