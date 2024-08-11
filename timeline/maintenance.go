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
	"time"

	"go.uber.org/zap"
)

// maintenanceLoop runs various operations on the timeline while it is open.
func (tl *Timeline) maintenanceLoop() {
	logger := Log.Named("maintenance")

	err := tl.deleteExpiredItems(tl.ctx, logger)
	if err != nil {
		logger.Error("problem deleting expired items at startup", zap.Error(err))
	}

	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-tl.ctx.Done():
			return
		case <-ticker.C:
			err := tl.deleteExpiredItems(tl.ctx, logger)
			if err != nil {
				logger.Error("problem deleting expired items", zap.Error(err))
			}
		}
	}
}

// deleteExpiredItems finds items marked as deleted that have passed their retention period
// and actually erases them.
func (tl *Timeline) deleteExpiredItems(ctx context.Context, logger *zap.Logger) error {
	tl.dbMu.Lock()
	defer tl.dbMu.Unlock()

	tx, err := tl.db.Begin()
	if err != nil {
		return fmt.Errorf("beginning transaction: %v", err)
	}
	defer tx.Rollback()

	// first identify which items are ready to be erased; we need their row
	// IDs and data files (we could do the erasure in a single UPDATE query,
	// but we do need to get their data files first so we can delete those after)
	rowIDsToEmpty, dataFilesToDelete, err := tl.findExpiredDeletedItems(tl.ctx, tx)
	if err != nil {
		return fmt.Errorf("finding expired deleted items: %v", err)
	}
	if len(rowIDsToEmpty) == 0 && len(dataFilesToDelete) == 0 {
		return nil // nothing to do
	}

	// clear out their rows
	err = tl.deleteDataInItemRows(tl.ctx, tx, rowIDsToEmpty, false)
	if err != nil {
		return fmt.Errorf("erasing deleted items (before deleting data files): %v", err)
	}

	// commit transaction so that the items in the DB are at least marked as
	// deleted; if deleting any of the data files fails, we'll log it, but
	// but there's no good way to roll back the transaction for only the items
	// of which the data file failed to delete (TODO: maybe we need a sweeper routine to clean up zombie/stray data files)
	// this way the DB remains the source of truth
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commiting transaction (no data files have been deleted yet): %v", err)
	}

	// now that the database shows the new truth, delete the data files to match
	numFilesDeleted, err := tl.deleteDataFiles(tl.ctx, logger, dataFilesToDelete)
	if err != nil {
		logger.Error("error when deleting data files of erased items (items have already been marked as deleted in DB)", zap.Error(err))
	}

	// delete thumbnails, if present
	for _, itemID := range rowIDsToEmpty {
		for _, format := range []ThumbnailType{ImageThumbnail, VideoThumbnail} {
			thumbPath := tl.ThumbnailPath(itemID, format)
			err = os.Remove(thumbPath)
			if err != nil && !errors.Is(err, fs.ErrNotExist) {
				Log.Error("unable to delete thumbnail file for erased item",
					zap.Int64("item_id", itemID),
					zap.String("thumbnail_file", thumbPath),
					zap.Error(err))
			}
		}
	}

	if len(rowIDsToEmpty) > 0 || numFilesDeleted > 0 {
		logger.Info("erased deleted items",
			zap.Int("count", len(rowIDsToEmpty)),
			zap.Int("deleted_data_files", numFilesDeleted))
	}

	return nil
}

func (tl *Timeline) findExpiredDeletedItems(ctx context.Context, tx *sql.Tx) (rowIDs []int64, dataFilesToDelete []string, err error) {
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
		return nil, nil, fmt.Errorf("querying for deleted items: %v", err)
	}
	defer rows.Close()

	// it's possible, though uncommon, for multiple items being deleted in this batch to
	// share the same data file, so tidy up duplicates while we go
	dataFilesMap := make(map[string]struct{})

	for rows.Next() {
		var id int64
		var dataFile *string
		var otherItemsUsingFile int
		if err := rows.Scan(&id, &dataFile, &otherItemsUsingFile); err != nil {
			return nil, nil, fmt.Errorf("scanning item row: %v", err)
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

func (tl *Timeline) deleteDataInItemRows(ctx context.Context, tx *sql.Tx, rowIDs []int64, preserveUserNotes bool) error {
	if len(rowIDs) == 0 {
		return nil
	}

	var sb strings.Builder

	// Empty all the columns except for id, deleted, and row hashes (and possibly note).
	// Set deleted to 1 to signify we have erased the row and completed the deletion.
	// Keep id unchanged to preserve relationships. (TODO: This could be configurable in the future.)
	// Keep the row hashes to remember the signature(s) of what was deleted.
	sb.WriteString(`UPDATE items
		SET data_source_id=NULL, import_id=NULL, modified_import_id=NULL, attribute_id=NULL,
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
		return fmt.Errorf("erasing item rows: %v", err)
	}

	return nil
}

func (tl *Timeline) deleteDataFiles(ctx context.Context, logger *zap.Logger, dataFilesToDelete []string) (int, error) {
	for _, dataFile := range dataFilesToDelete {
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
		parentDir := dataFileFullPath // incorrect for now, will be Dir()'ed at beginning of loop iteration
		for i := 0; i < 2; i++ {
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
