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
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"go.uber.org/zap"
)

// maintenanceLoop runs various operations on the timeline while it is open.
func (tl *Timeline) maintenanceLoop() {
	logger := Log.Named("maintenance")

	err := tl.deleteExpiredItems(logger)
	if err != nil {
		logger.Error("problem deleting expired items at startup", zap.Error(err))
	}

	// Best practice, according to the SQLite docs:
	//
	// "Applications with long-lived database connections should run "PRAGMA
	// optimize=0x10002" when the database connection first opens, then run
	// "PRAGMA optimize" again at periodic intervals - perhaps once per day.
	// All applications should run "PRAGMA optimize" after schema changes,
	// especially CREATE INDEX."
	// - https://www.sqlite.org/pragma.html#pragma_optimize
	//
	// We stray slightly from this guidance and just run ANALYZE in the
	// background at startup, and on a ticker or after large imports.
	// I found occasions where 'PRAGMA optimize' did not fix a slow query
	// when ANALYZE did. Maybe it was just under a threshold, I dunno.
	//
	// https://x.com/mholt6/status/1865169910940471492
	// --> https://x.com/carlsverre/status/1865185078067835167 (whole thread)
	go tl.optimizeDB(logger)

	deletionTicker := time.NewTicker(time.Minute)
	defer deletionTicker.Stop()

	const analyzeInterval = 24 * time.Hour
	analyzeTicker := time.NewTicker(analyzeInterval)
	defer analyzeTicker.Stop()

	for {
		select {
		case <-tl.ctx.Done():
			return
		case <-deletionTicker.C:
			err := tl.deleteExpiredItems(logger)
			if err != nil {
				logger.Error("problem deleting expired items", zap.Error(err))
			}
		case <-analyzeTicker.C:
			go tl.optimizeDB(logger)
		}
	}
}

// optimizeDB runs ANALYZE on the database. It can be very slow.
// Recommended to run in a goroutine.
func (tl *Timeline) optimizeDB(logger *zap.Logger) {
	// don't overlap if optimization is already running
	if !atomic.CompareAndSwapInt64(tl.optimizing, 0, 1) {
		return
	}
	defer atomic.StoreInt64(tl.optimizing, 0)

	// TODO: I assume a read lock is all we need for ANALYZE...
	tl.dbMu.RLock()
	defer tl.dbMu.RUnlock()
	logger.Info("optimizing database for performance")
	start := time.Now()
	_, err := tl.db.ExecContext(tl.ctx, "ANALYZE")
	if err != nil {
		logger.Error("analyzing database: %w", zap.Error(err))
	}
	logger.Info("finished optimizing database", zap.Duration("duration", time.Since(start)))
}

// deleteExpiredItems finds items marked as deleted that have passed their retention period
// and actually erases them.
func (tl *Timeline) deleteExpiredItems(logger *zap.Logger) error {
	tl.dbMu.Lock()
	defer tl.dbMu.Unlock()

	tx, err := tl.db.Begin()
	if err != nil {
		return fmt.Errorf("beginning transaction: %w", err)
	}
	defer tx.Rollback()

	// first identify which items are ready to be erased; we need their row
	// IDs and data files (we could do the erasure in a single UPDATE query,
	// but we do need to get their data files first so we can delete those after)
	rowIDsToEmpty, dataFilesToDelete, err := tl.findExpiredDeletedItems(tl.ctx, tx)
	if err != nil {
		return fmt.Errorf("finding expired deleted items: %w", err)
	}
	if len(rowIDsToEmpty) == 0 && len(dataFilesToDelete) == 0 {
		return nil // nothing to do
	}

	// clear out their rows
	err = tl.deleteDataInItemRows(tl.ctx, tx, rowIDsToEmpty, false)
	if err != nil {
		return fmt.Errorf("erasing deleted items (before deleting data files): %w", err)
	}

	// commit transaction so that the items in the DB are at least marked as
	// deleted; if deleting any of the data files fails, we'll log it, but
	// there's no good way to roll back the transaction for only the items
	// of which the data file failed to delete (TODO: maybe we need a sweeper routine to clean up zombie/stray data files)
	// this way the DB remains the source of truth
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("committing transaction (no data files have been deleted yet): %w", err)
	}

	// now that the database shows the new truth, delete the data files to match
	numFilesDeleted, err := tl.deleteDataFiles(tl.ctx, logger, dataFilesToDelete)
	if err != nil {
		logger.Error("error when deleting data files of erased items (items have already been marked as deleted in DB)", zap.Error(err))
	}

	if len(rowIDsToEmpty) > 0 || numFilesDeleted > 0 {
		logger.Info("erased deleted items",
			zap.Int("count", len(rowIDsToEmpty)),
			zap.Int("deleted_data_files", numFilesDeleted))
	}

	return nil
}

func (tl *Timeline) deleteThumbnails(ctx context.Context, itemRowIDs []uint64, dataFiles []string) error {
	tl.thumbsMu.Lock()
	defer tl.thumbsMu.Unlock()

	thumbsTx, err := tl.thumbs.Begin()
	if err != nil {
		return fmt.Errorf("beginning thumbnail transaction: %w", err)
	}
	defer thumbsTx.Rollback()

	for _, itemID := range itemRowIDs {
		_, err = thumbsTx.ExecContext(ctx, `DELETE FROM thumbnails WHERE item_id=?`, itemID)
		if err != nil {
			return fmt.Errorf("unable to delete thumbnail row for erased item %d: %w", itemID, err)
		}
	}
	for _, dataFile := range dataFiles {
		_, err = thumbsTx.ExecContext(ctx, `DELETE FROM thumbnails WHERE data_file=?`, dataFile)
		if err != nil {
			return fmt.Errorf("unable to delete thumbnail row for data file %s: %w", dataFile, err)
		}
	}

	return thumbsTx.Commit()
}

func (tl *Timeline) findExpiredDeletedItems(ctx context.Context, tx *sql.Tx) (rowIDs []uint64, dataFilesToDelete []string, err error) {
	now := time.Now().Unix()

	// this query selects the rows that are pending deletion ("in the trash") and returns their
	// row ID, the data file path, and the number of other items that reference the same data
	// file. The ("scalar"?) subquery is very specific to select exact matches of the data file
	// that aren't the current row and aren't being deleted at this time (because, of course, if
	// the other items using it are also being deleted at this time, we can still delete the file).
	rows, err := tx.QueryContext(ctx, `
		SELECT
			id, data_file,
			(SELECT count() FROM items AS otherItems
				WHERE otherItems.data_file = items.data_file
					AND otherItems.id != items.id
					AND (deleted IS NULL OR deleted > ?)
				LIMIT 1)
			AS other_items_using_data_file
		FROM items
		WHERE deleted > 1 AND deleted <= ?`, now, now)
	if err != nil {
		return nil, nil, fmt.Errorf("querying for deleted items: %w", err)
	}
	defer rows.Close()

	// it's possible, though uncommon, for multiple items being deleted in this batch to
	// share the same data file, so tidy up duplicates while we go
	dataFilesMap := make(map[string]struct{})

	for rows.Next() {
		var id uint64
		var dataFile *string
		var otherItemsUsingFile int
		if err := rows.Scan(&id, &dataFile, &otherItemsUsingFile); err != nil {
			return nil, nil, fmt.Errorf("scanning item row: %w", err)
		}
		rowIDs = append(rowIDs, id)

		if dataFile != nil && otherItemsUsingFile == 0 {
			dataFilesMap[*dataFile] = struct{}{}
		}
	}
	if err = rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("iterating item rows: %w", err)
	}

	// convert de-duplicated map entries into a slice
	for dataFile := range dataFilesMap {
		dataFilesToDelete = append(dataFilesToDelete, dataFile)
	}

	return
}

func (tl *Timeline) deleteDataInItemRows(ctx context.Context, tx *sql.Tx, rowIDs []uint64, preserveUserNotes bool) error {
	if len(rowIDs) == 0 {
		return nil
	}

	var sb strings.Builder

	// Empty all the columns except for id, deleted, and row hashes (and possibly note).
	// Set deleted to 1 to signify we have erased the row and completed the deletion.
	// Keep id unchanged to preserve relationships. (TODO: This could be configurable in the future.)
	// Keep the row hashes to remember the signature(s) of what was deleted.
	sb.WriteString(`UPDATE items
		SET data_source_id=NULL, job_id=NULL, modified_job_id=NULL, attribute_id=NULL,
			classification_id=NULL, original_id=NULL, original_location=NULL, intermediate_location=NULL,
			filename=NULL, timestamp=NULL, timespan=NULL, timeframe=NULL, time_offset=NULL, time_uncertainty=NULL,
			stored=0, modified=NULL, data_type=NULL, data_text=NULL, data_file=NULL, data_hash=NULL,
			metadata=NULL, longitude=NULL, latitude=NULL, altitude=NULL, coordinate_system=NULL,
			coordinate_uncertainty=NULL, `)
	if !preserveUserNotes {
		sb.WriteString("note=NULL, ")
	}
	sb.WriteString("starred=NULL, hidden=NULL, deleted=1 WHERE id IN ")

	// write the row IDs into the WHERE clause
	array, args := sqlArray(rowIDs)
	sb.WriteString(array)

	_, err := tx.ExecContext(ctx, sb.String(), args...)
	if err != nil {
		return fmt.Errorf("erasing item rows: %w", err)
	}

	return nil
}

func (tl *Timeline) deleteDataFiles(ctx context.Context, logger *zap.Logger, dataFilesToDelete []string) (int, error) {
	for _, dataFile := range dataFilesToDelete {
		if err := ctx.Err(); err != nil {
			return 0, err
		}

		dataFileFullPath := tl.FullPath(dataFile)
		err := os.Remove(dataFileFullPath)
		if errors.Is(err, fs.ErrNotExist) {
			logger.Warn("data file appears to have already been deleted",
				zap.String("data_file", dataFile),
				zap.Error(err))
		} else if err != nil {
			logger.Error("could not delete item data file",
				zap.String("data_file", dataFile),
				zap.Error(err))
			continue
		}

		// if parent dir is empty, delete it too (go up to 2 parent dirs = data source folder then month)
		var parentDir string
		for range 2 {
			parentDir = filepath.Dir(dataFileFullPath)

			isEmpty, _, err := directoryEmpty(parentDir, true)
			if err != nil {
				// this is optional but is nice for staying tidy; don't break here
				logger.Error("checking if parent dir is empty",
					zap.String("dir", parentDir),
					zap.Error(err))
				break
			}

			if isEmpty {
				logger.Debug("cleaning up empty parent directory",
					zap.String("parent_dir", parentDir),
					zap.Error(err))

				if err := os.Remove(parentDir); err != nil {
					logger.Error("removing empty parent directory",
						zap.String("dir", parentDir),
						zap.Error(err))
					break
				}
			}
		}
	}

	return len(dataFilesToDelete), nil
}
