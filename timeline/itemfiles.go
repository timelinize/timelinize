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
	"hash"
	"io"
	"io/fs"
	mathrand "math/rand"
	"mime"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"go.uber.org/zap"
)

// downloadDataFile downloads the data file and hashes it. It attaches the
// results to the item.
func (p *processor) downloadDataFile(ctx context.Context, it *Item) error {
	if it == nil {
		return nil
	}
	h := newHash()
	dataFileSize, err := p.downloadAndHashDataFile(it, h)
	if err != nil {
		return err
	}
	it.dataFileSize = dataFileSize
	if dataFileSize > 0 {
		it.dataFileHash = h.Sum(nil)
	}
	it.makeContentHash() // update content hash now that we know the data file hash
	return nil
}

// finishDataFileProcessing adds the results of a data file to the DB. It takes care of
// duplicate files and only keeps empty files if the processor is configured to do so.
// It returns the size of the data file that was downloaded and, if the item was found
// to be a duplicate, the row ID of the existing row for this item.
func (p *processor) finishDataFileProcessing(ctx context.Context, tx *sql.Tx, it *Item) error {
	if it == nil || it.dataFileOut == nil {
		return nil
	}

	// Now that we have a hash of the file, perform one last check WITHOUT row/original IDs to ensure the checksum
	// will be used in the query, to see if this ITEM is a duplicate. The check we performed before processing the
	// file doesn't have the file hash yet because it hasn't downloaded the item. (Even if the item is distinct,
	// we will still check for a duplicate data file soon, to see if we should just point to the existing file.)
	//
	// Note the conditions: no error, a row ID was returned, and the row ID is NOT equal to the input row ID.
	// This happened once when I had a bug in my call to loadItemRow, where I passed in itemRowID and originalID.
	// The query would use the IDs directly and not even use the checksum, which sometimes resulted in
	// the query loading the same row as the input item, and then we'd remove it, resulting in the item not
	// being added at all, and any future query to store relationships to fail with FK violations (because
	// the item was removed, when it was actually the only instance of it).
	//
	// If, still, somehow, the only item we discover is the same one, don't delete it!
	//
	// First, copy the item so we can zero-out its original ID; then nilify the data source name if empty...
	itCopy := *it
	itCopy.ID = ""
	var dsName *string
	if p.params.DataSourceName != "" {
		dsName = &p.params.DataSourceName
	}
	if existingItemRow, err := p.tl.loadItemRow(ctx, tx, 0, &itCopy, dsName, p.params.ProcessingOptions.ItemUniqueConstraints, true); err == nil && existingItemRow.ID > 0 && existingItemRow.ID != it.row.ID {
		// ah, so with the file hash, we have now determined that we already have the item;
		// this is a duplicate ITEM, so delete the row and file we just created, and since this
		// is being done asynchronously, the duplicate item has already finished processing
		// (it should have been processed in a mutex'ed transaction, so completely done by now)
		// and we need to: clean up the duplicated file, update any references to this item ID
		// in the DB, and then delete this item row from the DB
		p.log.Info("after downloading data file, used file hash to determine that item is a duplicate; removing",
			zap.Int64("matched_item_row_id", existingItemRow.ID),
			zap.Int64("incoming_duplicate_item_row_id", it.row.ID),
			zap.Stringp("original_id", existingItemRow.OriginalID),
			zap.String("data_file_name", it.dataFileName),
			zap.Binary("data_file_hash", it.dataFileHash),
			zap.Int64("bytes_written", it.dataFileSize))

		// delete duplicate data file
		if err := os.Remove(it.dataFileOut.Name()); err != nil {
			return fmt.Errorf("deleting duplicate data file %s: %v", it.dataFileOut.Name(), err)
		}

		// update references to the newly-inserted row to refer to the existing row instead,
		// but if the resulting row already exists, it'll fail uniqueness constraints;
		// we could probably make this operation more robust, but for now we just assume that
		// any error is failing the uniqueness constraint and we can move along simply by
		// logging it at debug level, and then when we delete the item row the FK constraints
		// should kick in and delete the row we were trying to update or remove
		// (SQL has an "ON CONFLICT REPLACE" but it's a SQLite extension I guess)
		if _, err := tx.Exec(`UPDATE relationships SET from_item_id=? WHERE from_item_id=?`, existingItemRow.ID, it.row.ID); err != nil {
			p.log.Debug("after detecting duplicate item file, could not update relevant relationships (from->to)", zap.Error(
				fmt.Errorf("updating relationships referencing from_item_id %d to %d: %v", existingItemRow.ID, it.row.ID, err)))
		}
		if _, err := tx.Exec(`UPDATE relationships SET to_item_id=? WHERE to_item_id=?`, existingItemRow.ID, it.row.ID); err != nil {
			p.log.Debug("after detecting duplicate item file, could not update relevant relationships (to->from)",
				zap.Error(fmt.Errorf("updating relationships referencing to_item_id %d to %d: %v", existingItemRow.ID, it.row.ID, err)))
		}
		if _, err := tx.Exec(`UPDATE collection_items SET item_id=? WHERE item_id=?`, existingItemRow.ID, it.row.ID); err != nil {
			p.log.Debug("after detecting duplicate item file, could not update relevant collection items",
				zap.Error(fmt.Errorf("updating collection_items referencing item_id %d to %d: %v", existingItemRow.ID, it.row.ID, err)))
		}
		if _, err := tx.Exec(`UPDATE curation_elements SET item_id=? WHERE item_id=?`, existingItemRow.ID, it.row.ID); err != nil {
			p.log.Debug("after detecting duplicate item file, could not update relevant curation elements",
				zap.Error(fmt.Errorf("updating curation_elements referencing item_id %d to %d: %v", existingItemRow.ID, it.row.ID, err)))
		}
		if _, err := tx.Exec(`UPDATE tagged SET item_id=? WHERE item_id=?`, existingItemRow.ID, it.row.ID); err != nil {
			p.log.Debug("after detecting duplicate item file, could not update relevant tags",
				zap.Error(fmt.Errorf("updating tagged referencing item_id %d to %d: %v", existingItemRow.ID, it.row.ID, err)))
		}
		if _, err = tx.Exec(`DELETE FROM items WHERE id=?`, it.row.ID); err != nil {
			p.log.Debug("after detecting duplicate item file, could not delete item row",
				zap.Error(fmt.Errorf("deleting duplicate item row %d: %v", it.row.ID, err)))
		}

		return nil
	} else if err != nil {
		p.log.Error("could not query to determine item uniqueness after file download", zap.Error(err))
	}

	// don't keep empty files
	if it.dataFileSize == 0 {
		p.log.Warn("downloaded data file was empty; removing item",
			zap.Int64("item_row_id", it.row.ID),
			zap.String("item_original_id", it.ID),
			zap.String("data_file_name", it.dataFileName),
			zap.Int64("bytes_written", it.dataFileSize))

		// delete col value from DB first, because then it's easy to sweep for stray/dangling data
		// files if we can know the DB is the ultimate source of truth, because nothing points to them
		// TODO: LIMIT 1... (see https://github.com/mattn/go-sqlite3/pull/802)
		if _, err := tx.Exec(`UPDATE items SET data_file=NULL, data_hash=NULL WHERE id=?`, it.row.ID); err != nil {
			return fmt.Errorf("unlinking data file from item row: %v", err)
		}

		// delete the empty data file
		if err := os.Remove(it.dataFileOut.Name()); err != nil {
			return fmt.Errorf("deleting empty data file: %v", err)
		}

		return nil
	}

	// file is non-empty, so that's good - and item is distinct; but if the exact same file
	// (byte-for-byte) already exists, delete this copy and reuse the existing one - don't do
	// this for empty files, that would leave all empty files pointing to the same empty file,
	// which is kind of pointless IMO
	// (this is where it's important that it.row.DataFile is not a pointer to it.dataFileName,
	// because we end up changing the value of it.dataFileName in this method)
	if err := p.replaceWithExisting(tx, &it.dataFileName, it.dataFileHash, it.row.ID); err != nil {
		return fmt.Errorf("replacing data file with identical existing file: %v", err)
	}

	// save the file's name and hash to all items which use it, to confirm it was downloaded successfully
	// (if it.row.DataFile was a pointer to it.dataFileName, this is where the query would no-op because
	// we updated it.dataFileName's value to the existing file, but that would also change it.row.DataFile
	// to be the same because they point to the same value in memory!! yet we expect it.row.DataFile to
	// keep the duplicate filename so we can select the row(s) to update...)
	_, err := tx.Exec(`UPDATE items SET data_file=?, data_hash=? WHERE data_file=?`,
		it.dataFileName, it.dataFileHash, it.row.DataFile)
	if err != nil {
		p.log.Error("updating item's data file hash in DB failed; hash info will be incorrect or missing",
			zap.Error(err),
			zap.String("filename", it.dataFileOut.Name()),
			zap.Int64("row_id", it.row.ID),
		)
	}

	// update content hash to be correct, now that we have the data file hash
	_, err = tx.Exec(`UPDATE items SET initial_content_hash=? WHERE id=?`, it.contentHash, it.row.ID)
	if err != nil {
		p.log.Error("updating item's initial content hash failed; duplicate detection for this item will not function",
			zap.Error(err),
			zap.Binary("initial_content_hash", it.contentHash),
			zap.Int64("row_id", it.row.ID),
		)
	}

	return nil
}

// downloadAndHashDataFile downloads the data file for the item, computing h along the way.
// It closes the file handles and returns the number of bytes copied.
//
// The item must not be nil, but it can have nil file handles without error; in that
// case this is a no-op. If only one file handle is nil, the other file is closed and
// an error is returned.
func (p *processor) downloadAndHashDataFile(it *Item, h hash.Hash) (int64, error) {
	if it == nil {
		return 0, fmt.Errorf("missing item for which to download file")
	}

	// make sure that the data files get closed even if only one is set
	// (just some defensive programming to prevent a leak)
	defer func() {
		if it.dataFileIn != nil {
			it.dataFileIn.Close()
		}
		if it.dataFileOut != nil {
			it.dataFileOut.Close()
		}
	}()

	// if there's no file to process, nbd; no-op; but if only one file
	// is open, then that's an error!
	if it.dataFileIn == nil && it.dataFileOut == nil {
		return 0, nil
	}
	if it.dataFileIn == nil {
		return 0, fmt.Errorf("%s: missing reader from which to download file (filename=%s original_location=%s intermediate_location=%s rowid=%d)", it.dataFileName, it.Content.Filename, it.OriginalLocation, it.IntermediateLocation, it.row.ID)
	}
	if it.dataFileOut == nil {
		return 0, fmt.Errorf("%s: missing writer with which to write file (filename=%s original_location=%s intermediate_location=%s rowid=%d)", it.dataFileName, it.Content.Filename, it.OriginalLocation, it.IntermediateLocation, it.row.ID)
	}

	// give the hasher a copy of the file bytes
	tr := io.TeeReader(it.dataFileIn, h)

	n, err := io.Copy(it.dataFileOut, tr)
	if err != nil {
		os.Remove(it.dataFileOut.Name())
		return n, fmt.Errorf("copying contents: %v", err)
	}

	// TODO: If n == 0, should we retry? (would need to call h.Reset() first) - to help handle sporadic I/O issues maybe

	// we can probably increase performance if we don't sync all the time, but that would be less reliable...
	if n > 0 {
		if err := it.dataFileOut.Sync(); err != nil {
			os.Remove(it.dataFileOut.Name())
			return n, fmt.Errorf("syncing file after downloading: %v", err)
		}
	}

	p.log.Debug("downloaded data file",
		zap.String("item_id", it.ID),
		zap.String("filename", it.dataFileOut.Name()),
		zap.Int64("size", n),
	)

	return n, nil
}

// openUniqueCanonicalItemDataFile opens a file for saving the content of the given item. It
// ensures the filename is unique within its folder even for case-insensitive file systems
// when running in a case-sensitive file system. It returns the file handle as well as the
// path to the file relative to the repo root, which can be stored in the data_file column.
//
// NOTE: It is crucial that the item row is inserted or updated with the data file path/name
// in the same transaction as tx, otherwise it's theoretically possible (though unlikely) for
// a collision to occur, as the DB is the source of truth, and this function creates a file
// but does not update the DB, so it is expected that the filename is "claimed" in the DB in
// the transaction tx before tx is committed.
func (t *Timeline) openUniqueCanonicalItemDataFile(tx *sql.Tx, logger *zap.Logger, it *Item, dataSourceID string) (*os.File, string, error) {
	if dataSourceID == "" {
		return nil, "", fmt.Errorf("missing data source ID")
	}

	dir := t.canonicalItemDataFileDir(it, dataSourceID)

	err := os.MkdirAll(t.FullPath(dir), 0700)
	if err != nil {
		return nil, "", fmt.Errorf("making directory for data file: %v", err)
	}

	// find a unique filename for this item
	canonicalFilename := t.canonicalItemDataFileName(it, dataSourceID)
	canonicalFilenameExt := path.Ext(canonicalFilename)
	canonicalFilenameWithoutExt := strings.TrimSuffix(canonicalFilename, canonicalFilenameExt)

	for i := 0; i < 10; i++ {
		// build the filepath to try; only add randomness to the filename if the original name isn't available
		tryPath := path.Join(dir, canonicalFilenameWithoutExt)
		if i > 0 {
			tryPath += fmt.Sprintf("__%s", safeRandomString(4, true, nil)) // same case == true for portability to case-insensitive file systems
		}
		tryPath += canonicalFilenameExt

		// see if the filename is available; create it with EXCLUSIVE so that we don't truncate any existing
		// file, and instead we should get a special error that the file already exists if it's taken...
		// if it is taken we can try another filename, but if it doesn't, this syscall will immediately
		// claim it for us
		f, err := os.OpenFile(t.FullPath(tryPath), os.O_CREATE|os.O_RDWR|os.O_EXCL, 0600)
		if errors.Is(err, fs.ErrExist) {
			continue // filename already taken; try another one
		}
		if err != nil {
			return nil, "", fmt.Errorf("creating data file: %v", err)
		}

		// also check with the database to see if filename is taken, case-insensitively (the column or index
		// should have COLLATE NOCASE; this is important to avoid file collisions when copying from a
		// case-sensitive FS to a case-insensitive FS!) -- if an item row has claim to it, then the file
		// is either still processing or was lost and needs to be reconstituted, but for now we should
		// not collide with it
		var count int
		err = tx.QueryRow(`SELECT count() FROM items WHERE data_file=? LIMIT 1`, tryPath).Scan(&count)
		if err != nil {
			return nil, "", fmt.Errorf("checking DB for file uniqueness: %v", err)
		}
		if count > 0 {
			// an existing item has claim to it, so let
			logger.Warn("file did not exist on disk but is already claimed in database - will try to make filename unique", zap.String("filepath", tryPath))
			continue
		}

		return f, tryPath, nil
	}

	return nil, "", fmt.Errorf("unable to find available filename for item: %s", it)
}

// canonicalItemDataFileName returns the plain, canonical name of the
// data file for the item. Canonical data file names are relative to
// the base storage (repo) path (i.e. the folder of the DB file). This
// function does no improvising in case of a name missing from the item,
// nor does it do uniqueness checks. If the item does not have enough
// information to generate a deterministic file name, the returned path
// will end with a trailing slash (i.e. the path's last component empty).
// Things considered deterministic for filename construction include the
// item's filename, the item's original ID, and its timestamp.
// TODO: fix godoc (this returns only the name now, not the whole dir)
func (t *Timeline) canonicalItemDataFileName(it *Item, dataSourceID string) string {
	// ideally, the filename is simply the one provided with the item
	var filename string
	if fname := it.Content.Filename; fname != "" {
		filename = t.safePathComponent(fname)
	}

	// otherwise, try a filename based on the item's original ID
	if filename == "" {
		if it.ID != "" {
			filename = fmt.Sprintf("item_%s", it.ID)
			if exts, err := mime.ExtensionsByType(it.Content.MediaType); err == nil && len(exts) > 0 {
				filename += exts[0]
			}
		}
	}

	// otherwise, try a filename based on the item's timestamp
	ts := it.Timestamp
	if filename == "" && !ts.IsZero() {
		filename = ts.Format("2006_01_02_150405")
		if exts, err := mime.ExtensionsByType(it.Content.MediaType); err == nil && len(exts) > 0 {
			filename += exts[0]
		}
	}

	// otherwise, out of options; revert to a random string
	// since no deterministic filename is available
	if filename == "" {
		filename = safeRandomString(24, true, nil) // same case == true for portability to case-insensitive file systems
		if exts, err := mime.ExtensionsByType(it.Content.MediaType); err == nil && len(exts) > 0 {
			filename += exts[0]
		}
	}

	// shorten the name if needed (thanks for nothing, Windows)
	filename = t.ensureDataFileNameShortEnough(filename)

	return filename
}

// canonicalItemDataFileDir returns the path to the directory for the given item
// relative to the timeline root, using forward slash as path separators (this
// is the form used in the data_file column of the DB).
func (t *Timeline) canonicalItemDataFileDir(it *Item, dataSourceID string) string {
	ts := it.Timestamp
	if ts.IsZero() {
		ts = time.Now()
	}

	if dataSourceID == "" {
		dataSourceID = "unknown"
	}

	// use "/" separators here and adjust for
	// OS path separator when accessing disk
	return path.Join(DataFolderName,
		fmt.Sprintf("%04d", ts.Year()),
		fmt.Sprintf("%02d", ts.Month()),
		t.safePathComponent(dataSourceID))
}

func (t *Timeline) ensureDataFileNameShortEnough(filename string) string {
	// Windows max path length is about 256, but it's unclear exactly what the limit is
	if len(filename) > 250 {
		ext := path.Ext(filename)
		if len(ext) > 20 { // arbitrary and unlikely, but just in case
			ext = ext[:20]
		}
		filename = filename[:250-len(ext)]
		filename += ext
	}
	return filename
}

// TODO:/NOTE: If changing a file name, all items with same data_hash must also be updated to use same file name
func (p *processor) replaceWithExisting(tx *sql.Tx, canonical *string, checksum []byte, itemRowID int64) error {
	if canonical == nil || *canonical == "" || len(checksum) == 0 {
		return fmt.Errorf("missing data filename and/or hash of contents")
	}

	var existingDatafile *string
	err := tx.QueryRow(`SELECT data_file FROM items WHERE data_hash = ? AND id != ? AND data_file != ? LIMIT 1`,
		checksum, itemRowID, *canonical).Scan(&existingDatafile)
	if err == sql.ErrNoRows {
		return nil // file is unique; carry on
	}
	if err != nil {
		return fmt.Errorf("querying DB: %v", err)
	}

	// file is a duplicate! by the time this function returns (if successful),
	// *canonical should not exist anymore and should have the value of
	// *existingDatafile instead.

	p.log.Info("data file is a duplicate",
		zap.Int64("row_id", itemRowID),
		zap.Stringp("duplicate_data_file", canonical),
		zap.Stringp("existing_data_file", existingDatafile),
		zap.Binary("checksum", checksum))

	if existingDatafile == nil {
		// ... that's weird, how's this possible? it has a hash but no file name recorded
		return fmt.Errorf("item with matching hash is missing data file name; hash: %x", checksum)
	}

	// TODO: maybe this all should be limited to only when integrity checks are enabled? how do we know that this download has the right version/contents?
	p.log.Debug("verifying existing file is still the same",
		zap.Int64("row_id", itemRowID),
		zap.Stringp("existing_data_file", existingDatafile),
		zap.Binary("checksum", checksum))

	// ensure the existing file is still the same
	h := newHash()
	f, err := os.Open(p.tl.FullPath(*existingDatafile))
	if err != nil {
		// TODO: This error is happening often when (re-?)importing SMS backup & restore MMS data files ("no such file or directory")
		return fmt.Errorf("opening existing file: %v", err)
	}
	defer f.Close()

	_, err = io.Copy(h, f)
	if err != nil {
		return fmt.Errorf("checking file integrity: %v", err)
	}

	existingFileHash := h.Sum(nil)

	if !bytes.Equal(checksum, existingFileHash) {
		// the existing file was corrupted, so restore it with
		// what we just downloaded, which presumably succeeded
		// (by simply renaming the file on disk, we don't have
		// to update any entries in the DB)
		p.log.Warn("existing data file failed integrity check (checksum on disk changed; file corrupted or modified?) - replacing existing file with this one",
			zap.Int64("row_id", itemRowID),
			zap.Stringp("data_file", existingDatafile),
			zap.Binary("expected_checksum", checksum),
			zap.Binary("actual_checksum", existingFileHash))
		err := os.Rename(p.tl.FullPath(*canonical), p.tl.FullPath(*existingDatafile))
		if err != nil {
			return fmt.Errorf("replacing modified data file: %v", err)
		}
	} else {
		// everything checks out; delete the newly-downloaded file
		// and use the existing file instead of duplicating it
		p.log.Debug("existing file passed integrity check; using it instead of newly-downloaded duplicate",
			zap.Int64("row_id", itemRowID),
			zap.Stringp("existing_data_file", existingDatafile),
			zap.Binary("checksum", checksum))
		err = os.Remove(p.tl.FullPath(*canonical))
		if err != nil {
			return fmt.Errorf("removing duplicate data file: %v", err)
		}
	}

	p.log.Info("merged duplicate data files based on integrity check",
		zap.Int64("row_id", itemRowID),
		zap.Stringp("duplicate_data_file", canonical),
		zap.Stringp("existing_data_file", existingDatafile),
		zap.Binary("checksum", checksum))

	*canonical = *existingDatafile

	return nil
}

// randomString returns a string of n random characters.
// It is not even remotely secure or a proper distribution.
// But it's good enough for some things. It elides certain
// confusing characters like I, l, 1, 0, O, etc. If sameCase
// is true, then uppercase letters are excluded.
func randomString(n int, sameCase bool, r mathrand.Source) string {
	if n <= 0 {
		return ""
	}
	dict := []rune("ABCDEFGHJKLMNPQRTUVWXYabcdefghijkmnopqrstuvwxyz23456789")
	if sameCase {
		// TODO: maybe it should be ONLY uppercase letters...
		dict = dict[22:]
	}
	b := make([]rune, n)
	for i := range b {
		var rnd int64
		if r == nil {
			rnd = mathrand.Int63()
		} else {
			rnd = r.Int63()
		}
		b[i] = dict[rnd%int64(len(dict))]
	}
	return string(b)
}

// FullPath returns the full file system path for a data file, including the repo path.
// It converts forward slashes in the input to the file system path separator.
func (t *Timeline) FullPath(canonicalDatafileName string) string {
	return filepath.Join(t.repoDir, filepath.FromSlash(canonicalDatafileName))
}

func (t *Timeline) safePathComponent(s string) string {
	s = safePathRE.ReplaceAllLiteralString(s, "")
	s = strings.Replace(s, "..", "", -1)
	if s == "." {
		s = ""
	}
	return s
}

func safeRandomString(n int, sameCase bool, r mathrand.Source) string {
	var s string
	for i := 0; i < 10; i++ {
		s = randomString(n, sameCase, r)
		if !containsBlocklistedWord(s) {
			break
		}
	}
	return s
}

func containsBlocklistedWord(s string) bool {
	s = strings.ToLower(s)
	for _, word := range []string{
		"fuck",
		"shit",
		"poo",
		"butt",
		"cunt",
		"ass",
		"arse",
		"niga",
		"nigg",
		"hate",
		"kill",
		"die",
		"damn",
		"sex",
		"anal",
		"bitch",
		"cum",
		"peni",
		"vagin",
		"puss",
		"tit",
		"wtf",
		"wank",
		"ejac",
		"dick",
		"hor",
		"evil",
	} {
		if strings.Contains(s, word) {
			return true
		}
	}
	return false
}

// safePathRE matches any undesirable characters in a filepath.
// Note that this allows dots, so you'll have to strip ".." manually.
var safePathRE = regexp.MustCompile(`[^\w.-]`)
