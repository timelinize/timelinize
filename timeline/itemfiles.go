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
	"io"
	"io/fs"
	weakrand "math/rand/v2"
	"mime"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"go.uber.org/zap"
)

// openUniqueCanonicalItemDataFile opens a file for saving the content of the given item. It
// ensures the filename is unique within its folder, but only according to the file system's
// case sensitivity. The transaction parameter is optional. If set, the DB will be consulted
// to ensure the chosen filename is case-insensitively unique, even if on a case-sensitive
// file system. That check uses the COLLATE NOCASE (i.e. do a case-insensitive lookup in
// the DB, to ensure the timeline repo will transfer from case-insensitive file systems to
// case-insensitive ones). It returns the file handle as well as the path to the file
// relative to the repo root, which can be stored in the data_file column.
func (tl *Timeline) openUniqueCanonicalItemDataFile(ctx context.Context, logger *zap.Logger, tx *sql.Tx, it *Item, dataSourceID string) (*os.File, string, error) {
	if dataSourceID == "" {
		return nil, "", errors.New("missing data source ID")
	}

	dir := tl.canonicalItemDataFileDir(it, dataSourceID)

	err := os.MkdirAll(tl.FullPath(dir), 0700)
	if err != nil {
		return nil, "", fmt.Errorf("making directory for data file: %w", err)
	}

	// find a unique filename for this item - we starrt with the desired file name based
	// on the info we have, but we may have to adjust it based on availability
	it.intendedDataFileName = tl.canonicalItemDataFileName(it)
	canonicalFilenameExt := path.Ext(it.intendedDataFileName)
	canonicalFilenameWithoutExt := strings.TrimSuffix(it.intendedDataFileName, canonicalFilenameExt)

	const randSuffixLen = 4

	// TODO: If this isn't enough iterations (for some reason!?) we can either increase the iteration count, or try an outer loop that extends the randSuffixLen to 5, even 6 chars.
	for i := range 10 {
		// build the filepath to try; only add randomness to the filename if the original name isn't available
		tryPath := path.Join(dir, canonicalFilenameWithoutExt)
		if i > 0 {
			tryPath += "__" + safeRandomString(randSuffixLen, true, nil) // same case == true for portability to case-insensitive file systems
		}
		tryPath += canonicalFilenameExt

		// see if the filename is available; create it with EXCLUSIVE so that we don't trample any existing
		// file, and instead we should get a special error that the file already exists if it's taken...
		// if it is taken we can try another filename, but if it doesn't, this syscall will immediately
		// claim it for us
		f, err := os.OpenFile(tl.FullPath(tryPath), os.O_CREATE|os.O_RDWR|os.O_EXCL, 0600)
		if errors.Is(err, fs.ErrExist) {
			continue // filename already taken; try another one
		}
		if err != nil {
			return nil, "", fmt.Errorf("creating data file: %w", err)
		}

		// also check with the database to see if filename is taken, case-insensitively (the column or index
		// should have COLLATE NOCASE; this is important to avoid file collisions when copying from a
		// case-sensitive FS to a case-insensitive FS!) -- if an item row has claim to it, then the file
		// is either still processing or was lost and needs to be reconstituted, but for now we should
		// not collide with it
		if tx != nil {
			var count int
			err = tx.QueryRowContext(ctx, `SELECT count() FROM items WHERE data_file=? LIMIT 1`, tryPath).Scan(&count)
			if err != nil {
				return nil, "", fmt.Errorf("checking DB for case-insensitive filename uniqueness: %w", err)
			}
			if count > 0 {
				// an existing item has claim to it, so let it keep that, it might still be processing
				logger.Warn("file did not exist on disk but is already claimed in database - will try to make filename unique", zap.String("filepath", tryPath))
				continue
			}
		}

		return f, tryPath, nil
	}

	return nil, "", fmt.Errorf("unable to find available filename for item: %s", it)
}

// canonicalItemDataFileName returns the filename to be used in the
// timeline repository for the item's data file. It pulls from elements
// of the item including its filename and timestamp, if present.
// It ensures the filename is a safe path component, and will not
// generate different names that end up conflicting on case-insensitive
// file systems.
func (tl *Timeline) canonicalItemDataFileName(it *Item) string {
	// this is stupid, but I really don't want to see .jfif at the end of my JPEGs ever again
	extensionByType := func(mediaType string) string {
		if exts, err := mime.ExtensionsByType(mediaType); err == nil && len(exts) > 0 {
			sort.Slice(exts, func(a, _ int) bool {
				if exts[a] == ".jpg" || exts[a] == ".jpeg" {
					return true
				}
				return false
			})
			return exts[0]
		}
		return ""
	}

	// ideally, the filename is simply the one provided with the item
	var filename string
	if it.Content.Filename != "" {
		filename = tl.safePathComponent(it.Content.Filename)
	}

	// otherwise, try a filename based on the item's original ID
	if filename == "" && it.ID != "" {
		filename = "item_id_" + tl.safePathComponent(it.ID) + extensionByType(it.Content.MediaType)
	}

	// otherwise, try a filename based on the item's timestamp
	if filename == "" && !it.Timestamp.IsZero() {
		filename = "item_ts_" + it.Timestamp.Format("2006_01_02_150405") + extensionByType(it.Content.MediaType)
	}

	// otherwise, out of options; revert to a random string
	// since no deterministic filename is available
	if filename == "" {
		const randomNameLen = 24
		filename = safeRandomString(randomNameLen, true, nil) // same case == true for portability to case-insensitive file systems
		filename += extensionByType(it.Content.MediaType)
	}

	// shorten the name if needed (thanks for nothing, Windows)
	filename = tl.ensureDataFileNameShortEnough(filename)

	return filename
}

// canonicalItemDataFileDir returns the path to the directory for the given item
// relative to the timeline root, using forward slash as path separators (this
// is the form used in the data_file column of the DB).
func (tl *Timeline) canonicalItemDataFileDir(it *Item, dataSourceID string) string {
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
		tl.safePathComponent(dataSourceID))
}

// ensureDataFileNameShortEnough returns the filename that is guaranteed to be short
// enough for... yeah, you guessed it: Windows. Of course.
func (tl *Timeline) ensureDataFileNameShortEnough(filename string) string {
	// Windows max filename length is 255, but it's unclear exactly what the limit is; choose slightly less...
	// see https://www.fileside.app/blog/2023-03-17_windows-file-paths/
	// "Traditionally, a path on Windows could not exceed a total of 260 characters. Even today, this is
	// still the case for some apps, unless they have taken care to implement a workaround."
	const (
		maxWindowsFilenameLen = 250
		maxExtLen             = 20 // arbitrary and unlikely, but just in case
	)
	if len(filename) > maxWindowsFilenameLen {
		ext := path.Ext(filename)
		if len(ext) > maxExtLen {
			ext = ext[:maxExtLen]
		}
		filename = filename[:maxWindowsFilenameLen-len(ext)]
		filename += ext
	}
	return filename
}

// replaceWithExisting checks to see if the checksum of a file already exists in the database for a
// file that is not the file with the given canonical path or row ID. It returns true if the file
// already exists and a replacement occurred; false otherwise (i.e. the file was unique).
// TODO:/NOTE: If changing a file name, all items with same data_hash must also be updated to use same file name
//
// TODO: The newly refactored processor does not use this yet, since we have deemed the detection of
// duplicate rows to be different from finding an existing row that represents an item, but we can
// potentially use this in a function that removes duplicate items/rows.
//
//nolint:unused
func (p *processor) replaceWithExisting(ctx context.Context, tx *sql.Tx, canonical *string, checksum []byte, itemRowID uint64) (bool, error) {
	if canonical == nil || *canonical == "" || len(checksum) == 0 {
		return false, errors.New("missing data filename and/or hash of contents")
	}

	var existingDatafile *string
	err := tx.QueryRowContext(ctx, `SELECT data_file FROM items WHERE data_hash = ? AND id != ? AND data_file != ? LIMIT 1`,
		checksum, itemRowID, *canonical).Scan(&existingDatafile)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil // file is unique; carry on
	}
	if err != nil {
		return false, fmt.Errorf("querying DB: %w", err)
	}

	// file is a duplicate! by the time this function returns (if successful),
	// *canonical should not exist anymore and should have the value of
	// *existingDatafile instead.

	p.log.Info("data file is a duplicate",
		zap.Uint64("row_id", itemRowID),
		zap.Stringp("duplicate_data_file", canonical),
		zap.Stringp("existing_data_file", existingDatafile),
		zap.Binary("checksum", checksum))

	if existingDatafile == nil {
		// ... that's weird, how's this possible? it has a hash but no file name recorded
		return false, fmt.Errorf("item with matching hash is missing data file name; hash: %x", checksum)
	}

	// TODO: maybe this all should be limited to only when integrity checks are enabled? how do we know that this download has the right version/contents?
	p.log.Debug("verifying existing file is still the same",
		zap.Uint64("row_id", itemRowID),
		zap.Stringp("existing_data_file", existingDatafile),
		zap.Binary("checksum", checksum))

	// ensure the existing file is still the same
	h := newHash()
	f, err := os.Open(p.tl.FullPath(*existingDatafile))
	if err != nil {
		// TODO: This error is happening often when (re-?)importing SMS backup & restore MMS data files ("no such file or directory")
		return false, fmt.Errorf("opening existing file: %w", err)
	}
	defer f.Close()

	_, err = io.Copy(h, f)
	if err != nil {
		return false, fmt.Errorf("checking file integrity: %w", err)
	}

	existingFileHash := h.Sum(nil)

	if !bytes.Equal(checksum, existingFileHash) {
		// the existing file was corrupted, so restore it with
		// what we just downloaded, which presumably succeeded
		// (by simply renaming the file on disk, we don't have
		// to update any entries in the DB)
		p.log.Warn("existing data file failed integrity check (checksum on disk changed; file corrupted or modified?) - replacing existing file with this one",
			zap.Uint64("row_id", itemRowID),
			zap.Stringp("data_file", existingDatafile),
			zap.Binary("expected_checksum", checksum),
			zap.Binary("actual_checksum", existingFileHash))
		err := os.Rename(p.tl.FullPath(*canonical), p.tl.FullPath(*existingDatafile))
		if err != nil {
			return false, fmt.Errorf("replacing modified data file: %w", err)
		}
	} else {
		// everything checks out; delete the newly-downloaded file
		// and use the existing file instead of duplicating it
		p.log.Debug("existing file passed integrity check; using it instead of newly-downloaded duplicate",
			zap.Uint64("row_id", itemRowID),
			zap.Stringp("existing_data_file", existingDatafile),
			zap.Binary("checksum", checksum))
		err = p.tl.deleteRepoFile(*canonical)
		if err != nil {
			return false, fmt.Errorf("removing duplicate data file: %w", err)
		}
	}

	p.log.Info("merged duplicate data files based on integrity check",
		zap.Uint64("row_id", itemRowID),
		zap.Stringp("duplicate_data_file", canonical),
		zap.Stringp("existing_data_file", existingDatafile),
		zap.Binary("checksum", checksum))

	*canonical = *existingDatafile

	return true, nil
}

// randomString returns a string of n random characters.
// It is not even remotely secure or a proper distribution.
// But it's good enough for some things. It elides certain
// confusing characters like I, l, 1, 0, O, etc. If sameCase
// is true, then uppercase letters are excluded.
func randomString(n int, sameCase bool, r weakrand.Source) string {
	if n <= 0 {
		return ""
	}
	dict := []rune("ABCDEFGHJKLMNPQRTUVWXYabcdefghijkmnopqrstuvwxyz23456789")
	if sameCase {
		dict = dict[22:] // skip uppercase letters
	}
	b := make([]rune, n)
	for i := range b {
		var rnd uint64
		if r == nil {
			rnd = weakrand.Uint64() //nolint:gosec
		} else {
			rnd = r.Uint64()
		}
		b[i] = dict[rnd%uint64(len(dict))]
	}
	return string(b)
}

// FullPath returns the full file system path for a data file, including the repo path.
// It converts forward slashes in the input to the file system path separator.
func (tl *Timeline) FullPath(canonicalDatafileName string) string {
	return filepath.Join(tl.repoDir, filepath.FromSlash(canonicalDatafileName))
}

func (*Timeline) safePathComponent(s string) string {
	s = safePathRE.ReplaceAllLiteralString(s, "")
	s = strings.ReplaceAll(s, "..", "")
	if s == "." {
		s = ""
	}
	return s
}

func safeRandomString(n int, sameCase bool, r weakrand.Source) string {
	var s string
	for range 10 {
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
