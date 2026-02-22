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
	"runtime/debug"
	"sync/atomic"

	"github.com/ringsaturn/tzf"
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

	log *zap.Logger

	// batching inserts can greatly increase speed
	batch     []*Graph
	batchSize int // size is at least len(batch) but edges on a graph can add to it

	// counter used for periodic DB optimization during an import
	// TODO: this should ideally be global per timeline, even if multiple jobs run simultaneously
	rootGraphCount int

	// used for guessing time zones based on geo coordinates
	tzFinder tzf.F
}

func (p processor) process(ctx context.Context, dirEntry DirEntry, dsCheckpoint json.RawMessage) error {
	// don't allow the importer, which is processing who-knows-what input, to completely bring down the app
	defer func() {
		if r := recover(); r != nil {
			p.log.Error("panic",
				zap.Any("error", r),
				zap.String("stack", string(debug.Stack())))
		}
	}()

	// add timeline owner to the context, since that is useful if data sources need an
	// item owner that is otherwise unknown
	owner, err := p.tl.LoadEntity(ownerEntityID)
	if err == nil {
		ctx = context.WithValue(ctx, RepoOwnerCtxKey, owner)
	}

	params := ImportParams{
		Log:               p.log.With(zap.String("filename", dirEntry.FullPath())),
		Continue:          p.ij.job.Continue,
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
	err = p.ds.NewFileImporter().FileImport(ctx, dirEntry, params)
	// handle error in a little bit (see below)

	// sending on the pipeline must be complete by now; signal to workers to exit
	close(done)

	params.Log.Info("importer done sending items; waiting for processing to finish", zap.Error(err))

	// wait for all processing workers to complete
	wg.Wait()

	return err
}

// ProcessingOptions configures how item processing is carried out.
type ProcessingOptions struct {
	// Whether to perform integrity checks
	Integrity bool `json:"integrity,omitempty"`

	// Constrain processed items to within a timeframe
	Timeframe Timeframe `json:"timeframe,omitempty"`

	// If true, items with manual modifications may be updated, overwriting local changes.
	OverwriteLocalChanges bool `json:"overwrite_local_changes,omitempty"`

	// Names of columns in the items table to check for sameness when loading an item
	// that doesn't have data_source+original_id. The field/column is the same if the
	// values are identical or if one of the values is NULL. If the map value is true,
	// however, strict NULL comparison is applied, where NULL=NULL only. In other words,
	// with a false value, it is as if the field is not in the unique constraints at
	// all when applied to a field that is null.
	ItemUniqueConstraints map[string]bool `json:"item_unique_constraints,omitempty"`

	// How to update existing items, specified per-field.
	// If not set, items that already exist will simply be skipped.
	ItemUpdatePreferences []FieldUpdatePreference `json:"item_update_preferences,omitempty"`

	// TODO: WIP (Should this be in importjob or processingoptions?)
	Interactive *InteractiveImport `json:"interactive,omitempty"`

	// How many items to process in one database transaction. This is a minimum value,
	// not a maximum, due to the recursive nature of graphs. (Hopefully data sources
	// do not build out graphs that are too big for available memory. Most graphs
	// just have 1-2 things on it). This setting is sensitive. Smaller batches can
	// reduce performance, slowing down imports. Larger batches can be faster, allowing
	// for more throughput especially when data files are large, but reduces the
	// granularity of the live progress updates, and the performance benefit gets
	// smaller as the database gets larger and the items get smaller.
	BatchSize int `json:"batch_size,omitempty"`

	// When enabled, items that are received for processing which have coordinates and
	// a timestamp but lack a time zone may have the time zone augmented by doing a
	// geopoly lookup. The data set uses more memory but can help make timestamps more
	// correct/complete. Since Go does not actually support time.Time values without a
	// time zone, by "lack a time zone" we mean the location is time.Local (i.e. "wall
	// time"), which essentially means, "time zone unknown".
	InferTimeZone bool `json:"infer_time_zone,omitempty"`

	// Generate thumbnails of images and videos as they are processed, rather than waiting
	// until the thumbnailing job after the import finishes. This uses more memory and
	// slows down the import job during media items.
	Thumbnails bool `json:"thumbnails,omitempty"`
}

type ctxKey string

var RepoOwnerCtxKey ctxKey = "repo_owner"

const ownerEntityID = 1
