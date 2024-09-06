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
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

// TODO: update godoc
// processor wraps a Client instance with unexported
// fields that contain necessary state for performing
// data collection operations. Do not craft this type
// manually; use Timeline.NewProcessor() to obtain one.
type processor struct {
	// accessed atomically (align on 64-bit word boundary, for 32-bit systems)
	itemCount, newItemCount, updatedItemCount, skippedItemCount *int64
	newEntityCount                                              *int64

	tl        *Timeline
	ds        DataSource
	dsRowID   int64 // only used directly with DB to reduce DB queries; different for every DB
	acc       Account
	impRow    importRow
	params    ImportParameters
	filenames []string
	log       *zap.Logger
	progress  *zap.Logger

	// batching inserts can greatly increase speed
	batch     []*Graph
	batchSize int // size is at least len(batch) but edges on a graph can add to it
	batchMu   *sync.Mutex

	// allow many concurrent file downloads as they can be massively parallel
	downloadThrottle chan struct{}
}

func (tl *Timeline) Import(ctx context.Context, params ImportParameters) error {
	// ensure data source is compatible with mode of import
	ds, ok := dataSources[params.DataSourceName]
	if !ok {
		return fmt.Errorf("unknown data source: %s", params.DataSourceName)
	}
	if len(params.Filenames) > 0 && ds.NewFileImporter == nil {
		return fmt.Errorf("data source %s does not support importing from files", ds.Name)
	}
	if len(params.Filenames) == 0 && ds.NewAPIImporter == nil {
		return fmt.Errorf("data source %s does not support importing via API", ds.Name)
	}

	// create or resume import operation
	var impRow importRow
	var err error
	if params.ResumeImportID == 0 {
		mode := importModeAPI
		if len(params.Filenames) > 0 {
			mode = importModeFile
		}
		impRow, err = tl.newImport(ctx, params.DataSourceName, mode, params.ProcessingOptions, params.AccountID)
		if err != nil {
			return fmt.Errorf("creating new import row: %w", err)
		}
	} else {
		impRow, err = tl.loadImport(ctx, params.ResumeImportID)
		if err != nil {
			return fmt.Errorf("loading existing import row: %w", err)
		}
		if impRow.checkpoint == nil {
			return fmt.Errorf("import %d has no checkpoint to resume from", impRow.id)
		}
		if params.DataSourceName != "" || params.AccountID != 0 ||
			len(params.Filenames) > 0 || !params.ProcessingOptions.IsEmpty() ||
			params.DataSourceOptions != nil {
			// no need to specify these; it only risks being different and thus in conflict
			return errors.New("pointless to specify any other parameters when resuming import")
		}

		// adjust parameters to set up resumption
		params.Filenames = impRow.checkpoint.Filenames
		params.DataSourceName = impRow.dataSourceName
		if impRow.accountID != nil {
			params.AccountID = *impRow.accountID
		}
		params.ProcessingOptions = impRow.checkpoint.ProcOpt
	}

	return tl.doImport(ctx, ds, params, impRow)
}

// TODO: detect a moved repo while processing, somehow...? weird edge case, but might be good to be resilient against...

// TODO: update godoc
// Import adds items to the timeline. If filename is non-empty, the items will be imported
// from the specified file. Any data-source-specific options should be passed in as dsOptJSON.
// Processing will follow the rules specified in procOpt.
func (tl *Timeline) doImport(ctx context.Context, ds DataSource, params ImportParameters, impRow importRow) error {
	var acc Account
	if params.AccountID > 0 {
		var err error
		acc, err = tl.LoadAccount(ctx, params.AccountID)
		if err != nil {
			return err
		}
	}

	logger := Log.Named("processor").With(
		zap.String("data_source", ds.Name),
		zap.String("job_id", params.JobID),
	)
	if len(params.Filenames) > 0 {
		logger = logger.With(zap.Strings("filenames", params.Filenames))
	}

	dsRowID, ok := tl.dataSources[ds.Name]
	if !ok {
		return fmt.Errorf("unknown data source: %s", ds.Name)
	}

	// batchSize is a minimum, so multiplier speeds up larger batches
	const multiplier = 2

	proc := processor{
		itemCount:        new(int64),
		newItemCount:     new(int64),
		updatedItemCount: new(int64),
		skippedItemCount: new(int64),
		newEntityCount:   new(int64),
		ds:               ds,
		dsRowID:          dsRowID,
		params:           params,
		tl:               tl,
		acc:              acc,
		impRow:           impRow,
		log:              logger,
		progress:         logger.Named("progress"),
		batchMu:          new(sync.Mutex),
		downloadThrottle: make(chan struct{}, batchSize*workers*multiplier),
	}

	return proc.doImport(ctx)
}

func (p *processor) doImport(ctx context.Context) error {
	ctx = context.WithValue(ctx, processorCtxKey, p) // for checkpoints

	timeframe := p.params.ProcessingOptions.Timeframe

	// convert data source options to their concrete type (we know it
	// only as interface{}, but actual data source can type-assert)
	dsOpt, err := p.ds.UnmarshalOptions(p.params.DataSourceOptions)
	if err != nil {
		return err
	}

	// get latest should only get the latest items since the last pull, i.e. from the most recent item from this account
	if p.params.ProcessingOptions.GetLatest {
		if len(p.params.ProcessingOptions.ItemFieldUpdates) > 0 || p.params.ProcessingOptions.Prune ||
			p.params.ProcessingOptions.Integrity || p.params.ProcessingOptions.Timeframe.Since != nil {
			return errors.New("get latest does not support reprocessing, pruning, integrity checking, and timeframe since constraints")
		}

		// get date and original ID of the most recent item from the last successful run,
		// which will be used to constrain this import to get only items newer than it;
		// note that we use the last item from the last *successful* import from this data
		// source, otherwise there could be a situation where the last import stopped part
		// way through after getting only the newest items, and there could be a gap of
		// time where data is missing, so we can't simply use the last item without
		// ensuring it is the last item from the last successful import
		// (note that )
		// TODO: in the old schema, we just recorded the item ID, I am not sure if this new query is correct
		var mostRecentTimestamp *int64
		var mostRecentOriginalID *string
		// if proc.acc.lastItemID != nil {
		// 	proc.tl.dbMu.RLock()
		// 	err := proc.tl.db.QueryRow(`SELECT timestamp, original_id FROM items WHERE id=? LIMIT 1`, *proc.acc.lastItemID).Scan(&mostRecentTimestamp, &mostRecentOriginalID)
		// 	proc.tl.dbMu.RUnlock()
		// 	if err != nil && err != sql.ErrNoRows {
		// 		return fmt.Errorf("getting most recent item: %v", err)
		// 	}
		// }
		p.tl.dbMu.RLock()
		err := p.tl.db.QueryRow(`
			SELECT items.original_id, items.timestamp
			FROM items, imports, data_sources
			WHERE imports.status=?
				AND imports.id = items.import_id
				AND data_sources.id = imports.data_source_id
				AND data_sources.name = ?
			ORDER BY imports.started DESC
			LIMIT 1`, importStatusSuccess, p.params.DataSourceName).Scan(&mostRecentOriginalID, &mostRecentTimestamp)
		p.tl.dbMu.RUnlock()
		if err != nil && err != sql.ErrNoRows {
			return fmt.Errorf("getting most recent item: %w", err)
		}

		// constrain the pull to the recent timeframe
		timeframe.Until = p.params.ProcessingOptions.Timeframe.Until
		if mostRecentTimestamp != nil {
			ts := time.Unix(*mostRecentTimestamp, 0)
			timeframe.Since = &ts
			if timeframe.Until != nil && timeframe.Until.Before(ts) {
				// most recent item is already after "until"/end date; nothing to do
				return nil
			}
		}
		if mostRecentOriginalID != nil {
			timeframe.SinceItemID = mostRecentOriginalID
		}
	}

	var checkpointData any
	if p.impRow.checkpoint != nil {
		checkpointData = p.impRow.checkpoint.Data
	}

	listOpt := ListingOptions{
		Log:               p.log,
		Timeframe:         timeframe,
		Checkpoint:        checkpointData,
		DataSourceOptions: dsOpt,
	}

	start := time.Now()

	// TODO: for an interactive import, we'd want to use only 1 worker, to get 1 item at most
	wg, ch := p.beginProcessing(ctx, p.params.ProcessingOptions)

	if len(p.params.Filenames) > 0 {
		err = p.ds.NewFileImporter().FileImport(ctx, p.params.Filenames, ch, listOpt)
	} else {
		err = p.ds.NewAPIImporter().APIImport(ctx, p.acc, ch, listOpt)
	}
	// handle error in a little bit (see below)

	// we are no longer using this; closing the channel signals to the workers to exit
	close(ch)

	// when we return, update the import row in the DB with the results
	importResult := "ok"
	defer func() {
		p.tl.dbMu.Lock()
		_, err := p.tl.db.Exec(`UPDATE imports SET ended=?, status=? WHERE id=?`, // TODO: limit 1 (see https://github.com/mattn/go-sqlite3/pull/802)
			time.Now().Unix(), importResult, p.impRow.id)
		p.tl.dbMu.Unlock()
		if err != nil {
			p.log.Error("updating import status",
				zap.Int64("import_id", p.impRow.id),
				zap.String("status", importResult),
				zap.Error(err))
		}
	}()

	// handle any error returned from import
	if err != nil {
		if errors.Is(err, context.Canceled) {
			p.log.Error("import aborted",
				zap.Error(err),
				zap.Duration("duration", time.Since(start)))
			importResult = "abort"
		} else {
			importResult = "err"
		}
		return fmt.Errorf("import: %w", err)
	}

	p.log.Info("all items received; waiting for processing to finish",
		zap.Int64("import_id", p.impRow.id),
		zap.String("status", importResult),
		zap.Error(err))

	// wait for all processing workers to complete
	wg.Wait()

	p.log.Info("import complete", zap.Duration("duration", time.Since(start)))

	// clear checkpoint and update last item ID for account
	err = p.successCleanup()
	if err != nil {
		return fmt.Errorf("processing completed, but error cleaning up: %w", err)
	}

	go p.generateThumbnailsForImportedItems()

	return nil
}

func (p *processor) successCleanup() error {
	// delete empty items from this import (items with no content and no meaningful relationships)
	if err := p.deleteEmptyItems(p.impRow.id); err != nil {
		return fmt.Errorf("deleting empty items: %w (import_id=%d)", err, p.impRow.id)
	}

	// TODO: If no items were inserted or associated with this import, delete it from the DB?

	// clear checkpoint
	p.tl.dbMu.Lock()
	_, err := p.tl.db.Exec(`UPDATE imports SET checkpoint=NULL WHERE id=?`, p.impRow.id) // TODO: limit 1 (see https://github.com/mattn/go-sqlite3/pull/802)
	p.tl.dbMu.Unlock()
	if err != nil {
		return fmt.Errorf("clearing checkpoint: %w", err)
	}
	p.impRow.checkpoint = nil

	// // TODO: ... we don't use this in the new schema
	// // update the last item ID, to advance the window for future get-latest operations
	// p.lastItemMu.Lock()
	// lastItemID := p.lastItemRowID
	// p.lastItemMu.Unlock()
	// if lastItemID > 0 {
	// 	_, err = p.tl.db.Exec(`UPDATE accounts SET last_item_id=? WHERE id=?`, lastItemID, p.acc.ID) // TODO: limit 1
	// 	if err != nil {
	// 		return fmt.Errorf("advancing most recent item ID: %v", err)
	// 	}
	// }

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
			WHERE import_id=?
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
		WHERE import_id=?
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
		zap.Int64("import_id", importID),
		zap.Int("count", len(emptyItems)))

	retention := time.Duration(0)
	return p.tl.deleteItemRows(p.tl.ctx, emptyItems, false, &retention)
}

// DeleteItemRows deletes the item rows specified by their row IDs. If remember is true, the item rows will
// be hashed, and the hash will be stored with the row; if retention is non-zero, the items will be marked
// for deletion and then only deleted later after the retention period.
// TODO: WIP: remember and retention are not yet implemented
func (tl *Timeline) deleteItemRows(ctx context.Context, rowIDs []int64, remember bool, retention *time.Duration) error {
	if len(rowIDs) == 0 {
		return nil
	}

	Log.Info("deleting item rows",
		zap.Int64s("item_ids", rowIDs),
		zap.Bool("remember", remember),
		zap.Durationp("retention", retention))

	tl.dbMu.Lock()
	defer tl.dbMu.Unlock()

	tx, err := tl.db.Begin()
	if err != nil {
		return fmt.Errorf("beginning transaction: %w", err)
	}
	defer tx.Rollback()

	var dataFilesToDelete []string
	for _, rowID := range rowIDs {
		// before deleting the row, find out whether this item
		// has a data file and is the only one referencing it
		var count int
		var dataFile *string
		err = tx.QueryRow(`SELECT count(), data_file FROM items
		WHERE data_file = (SELECT data_file FROM items
							WHERE id=? AND data_file IS NOT NULL
							AND data_file != "" LIMIT 1)`,
			rowID).Scan(&count, &dataFile)
		if err != nil {
			return fmt.Errorf("querying count of rows sharing data file: %w", err)
		}

		_, err = tx.Exec(`DELETE FROM items WHERE id=?`, rowID) // TODO: limit 1 (see https://github.com/mattn/go-sqlite3/pull/802)
		if err != nil {
			return fmt.Errorf("deleting item %d from DB: %w", rowID, err)
		}

		// if this row is the only one that references the data file, we can delete it
		if count == 1 && dataFile != nil {
			dataFilesToDelete = append(dataFilesToDelete, *dataFile)
		}
	}

	// commit to delete the item from the DB first; even if deleting the data file fails, stray
	// data files can be cleaned up with a sweep later, whereas if we delete that file first and
	// then fail to delete from DB, the DB being the ultimate source of truth is now missing data
	// and we aren't sure whether we need to recover it or finish deleting it... by deleting the
	// DB row first we can know that we just need to delete the file if there's no row using it
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing deletion transaction: %w", err)
	}

	_, err = tl.deleteDataFiles(ctx, Log, dataFilesToDelete)
	if err != nil {
		return fmt.Errorf("deleting data files (after deleting associated item rows from DB): %w", err)
	}

	return nil
}

func (p processor) String() string {
	accountIDOrFilename := "files:" + strings.Join(p.filenames, ",")
	if p.acc.ID > 0 {
		accountIDOrFilename = "account:" + strconv.Itoa(int(p.acc.ID))
	}
	return p.ds.Name + ":" + accountIDOrFilename
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
