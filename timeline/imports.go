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
	"encoding/json"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mholt/archives"
	"go.uber.org/zap"
)

type importJobCheckpoint struct {
	EstimatedSize        *int64          `json:"estimated_size"` // only set if currently in the estimating phase
	OuterIndex           int             `json:"outer_index"`
	InnerIndex           int             `json:"inner_index"`
	DataSourceCheckpoint json.RawMessage `json:"data_source_checkpoint,omitempty"`
}

type ImportJob struct {
	// accessed atomically (placed first to align on 64-bit word boundary, for 32-bit systems)
	// (reset each time the job is started)
	itemCount, newItemCount, updatedItemCount, skippedItemCount *int64
	newEntityCount                                              *int64

	job *ActiveJob

	Plan              ImportPlan        `json:"plan,omitempty"`
	ProcessingOptions ProcessingOptions `json:"processing_options,omitempty"`
	EstimateTotal     bool              `json:"estimate_total,omitempty"`
}

func (ij ImportJob) checkpoint(estimatedSize *int64, outer, inner int, ds any) error {
	var dsChkpt json.RawMessage
	if ds != nil {
		var err error
		dsChkpt, err = json.Marshal(ds)
		if err != nil {
			return fmt.Errorf("marshaling data source checkpoint %#v: %w", ds, err)
		}
	}
	return ij.job.Checkpoint(importJobCheckpoint{
		EstimatedSize:        estimatedSize,
		OuterIndex:           outer,
		InnerIndex:           inner,
		DataSourceCheckpoint: dsChkpt,
	})
}

// splitPathAtVolume splits an absolute filename into volume (or root)
// and filename components. On Windows, the "root" of a filepath is the
// volume (e.g. "C:"); on Unix-compatible systems, it is "/". Windows is
// insane. Anyway, this is useful for constructing a fs.FS that provides
// access to the whole file system. The returned rootOrVolume can be used
// as the root of an os.DirFS or similar FS (such as archives.DeepFS),
// and the relativePath is an fs.FS-compatible path that can be used to
// access the file within the FS.
//
// See https://github.com/golang/go/issues/44279.
func splitPathAtVolume(absFilename string) (rootOrVolume string, relativePath string, err error) {
	if !filepath.IsAbs(absFilename) {
		err = fmt.Errorf("filename is not absolute: %s", absFilename)
		return
	}
	rootOrVolume = "/" // assume non-Windows
	if vol := filepath.VolumeName(absFilename); vol != "" {
		rootOrVolume = vol // okay, it's actually Windows
	}
	relativePath, err = filepath.Rel(rootOrVolume, absFilename)
	if err != nil {
		return
	}
	relativePath = filepath.ToSlash(relativePath)
	return
}

func (ij ImportJob) Run(job *ActiveJob, checkpoint []byte) error {
	// TODO: are these racey?
	ij.job = job
	ij.itemCount = new(int64)
	ij.newItemCount = new(int64)
	ij.updatedItemCount = new(int64)
	ij.skippedItemCount = new(int64)
	ij.newEntityCount = new(int64)

	estimating := ij.EstimateTotal

	var chkpt importJobCheckpoint
	if checkpoint != nil {
		err := json.Unmarshal(checkpoint, &chkpt)
		if err != nil {
			return fmt.Errorf("decoding checkpoint: %w", err)
		}
		// in theory, resuming a job should have the same configuration as
		// before, so this may take us out of "estimating" mode if that had
		// already been completed, but I don't think it should ever put us
		// INTO "estimating" mode
		estimating = chkpt.EstimatedSize != nil
	}

	// two iterations: first to estimate size if enabled, then to actually import items
	for {
		// this should be cumulative across all the files
		var totalSizeEstimate *int64
		if estimating {
			if chkpt.EstimatedSize == nil {
				totalSizeEstimate = new(int64)
				job.Logger().Info("estimating size; this may take a bit")
			} else {
				totalSizeEstimate = chkpt.EstimatedSize
				job.Logger().Info("resuming size estimation; this may take a bit")
			}
			job.Message("Estimating total import size")
		}

		for i := chkpt.OuterIndex; i < len(ij.Plan.Files); i++ {
			if err := job.Context().Err(); err != nil {
				return err
			}

			fileImport := ij.Plan.Files[i]

			// load data source tidbits
			ds, ok := dataSources[fileImport.DataSourceName]
			if !ok {
				return fmt.Errorf("file import %d: unknown data source: %s", i, fileImport.DataSourceName)
			}
			dsRowID, ok := job.tl.dataSources[fileImport.DataSourceName]
			if !ok {
				return fmt.Errorf("file import %d: data source ID is unknown: %s", i, fileImport.DataSourceName)
			}
			dsOpt, err := ds.UnmarshalOptions(fileImport.DataSourceOptions)
			if err != nil {
				return err
			}

			logger := job.Logger().With(zap.String("data_source_name", ds.Name))

			// batchSize is a minimum, so multiplier speeds up larger batches
			const dlMultiplier = 2

			p := processor{
				estimatedCount:   totalSizeEstimate,
				ij:               ij,
				ds:               ds,
				dsRowID:          dsRowID,
				dsOpt:            dsOpt,
				tl:               job.Timeline(),
				log:              logger,
				progress:         logger.Named("progress"),
				batchMu:          new(sync.Mutex),
				downloadThrottle: make(chan struct{}, batchSize*workers*dlMultiplier),
			}

			// process each filename one at a time
			for j := chkpt.InnerIndex; j < len(fileImport.Filenames); j++ {
				if err := job.Context().Err(); err != nil {
					return err
				}

				filename := fileImport.Filenames[j]

				if !estimating {
					job.Message("Importing " + filename)
				}

				// create a new "outer" checkpoint when arriving at a new filename to be imported
				if err := ij.checkpoint(totalSizeEstimate, i, j, nil); err != nil {
					job.Logger().Error("checkpointing", zap.Error(err))
				}

				// check for job cancellation
				if err := job.Context().Err(); err != nil {
					return err
				}

				// Create the file system from which this file/dir will be accessed. It must be
				// a DeepFS since the filename might refer to a path inside an archive file.
				// Rooting the FS is nuanced. If the FS is rooted at the file or directory
				// itself, data sources that walk the FS (like if they support importing from a
				// directory of similar independent files) must root their walk at ".", and
				// then must check the filename passed into the WalkDirFunc for "." and replace
				// it with the real filename, if they want to access the file's actual name
				// (e.g. to check its file extension). It also forbids access to siblings, which
				// can be useful if sidecar files contain relevant data.
				//
				// One way to solve this is rooting the FS at the volume or real file system
				// root (e.g. "C:" or "/"), then walking starting from the full path to the
				// file. This provides important context about the file's location, which
				// some data sources may use (e.g. looking for a timestamp in the folder
				// hierarchy), and also always provides the actual filename (never "." since
				// a walk wouldn't start from "."); however, this makes other FS operations
				// tedious. For example, accessing a specific path in a subfolder of the
				// starting filename involves convoluted logic to determine if the starting
				// filename is a directory or file, and if a file, to call path.Dir() then
				// path.Join(), just to get the desired path. This is error-prone and repetitive.
				//
				// We can improve on this by rooting the FS not at the filename or at the volume
				// root, but somewhere in between: the file if it's a directory, or the parent
				// dir if it's a file. That way, if the filename is a file, fs.WalkDir(filename)
				// will walk only the file as expected, and with its true filename (not ".");
				// and if it's a dir, the whole directory will be walked. (The filename of the
				// top directory in that case will be ".", but when walking a directory, the name
				// of the top directory is seldom important; it can still be retrieved using
				// another method or variable anyway). This also makes Open()/Stat()/etc work
				// as expected when simply passing in a relative path, without any complex lexical
				// manipulation (i.e. passing in "a/b/c.txt" will open "/fs/root/a/b/c.txt" as
				// intended because the fs is rooted at "/fs/root" regardless if the starting
				// filename was "/fs/root" or "/fs/root/file.txt"). And because we root the FS
				// at the parent dir of a file, it grants access to siblings/sidecars if needed.
				// (We can't ALWAYS root the FS at the parent dir, because if the target is
				// a directory, then its parent dir is a *different* directory, making the
				// construction of subpaths more tedious to do correctly.)
				//
				// There are two main downsides I can see. One is that the filename associated
				// with this DirEntry will no longer be the full path from the volume root, thus
				// losing potentially valuable context as to the file's location. This can be
				// remedied by adding a field to the DirEntry that stores the FS root path, so
				// it can be referred to if needed. This has been done. The other downside is
				// that when walking the FS, one must remember to root the walk at the associated
				// Filename field in the DirEntry, and not "." (Filename will be "." in the case
				// of a directory, but for a file, the FS root will be its parent folder and
				// Filename will be the file's name in that directory, so "." would walk all
				// files adjacent to the Filename).

				// start by assuming the filename is a directory, in which case the FS is rooted
				// at the directory itself, and thus the filename within the FS is "."
				fsRoot, filenameInsideFS := filename, "."

				// if the filepath contains an archive (i.e. the path itself traverses into
				// an archive), we need to use DeepFS; otherwise, traversing into an archive
				// during import is (probably?) not desired, so use a "regular" file system
				var fsys fs.FS
				if archives.PathContainsArchive(filepath.ToSlash(fsRoot)) {
					fsys = &archives.DeepFS{Root: fsRoot, Context: job.Context()}
				} else {
					fsys, err = archives.FileSystem(job.ctx, fsRoot, nil)
					if err != nil {
						return fmt.Errorf("creating file system at %s: %w", fsRoot, err)
					}
				}

				// this stat can be slow-ish, depending on if we're in a large, compressed tar file,
				// and the file happens to be at the end, but we need to know if it's not a directory;
				// and since we reuse the DeepFS, the results get amortized for later
				info, err := fs.Stat(fsys, filenameInsideFS)
				if err != nil {
					return fmt.Errorf("could not stat file to import: %s: %w", filename, err)
				}
				if !info.IsDir() {
					// as explained above, if it's NOT a directory, we root the FS at the
					// parent folder and set the filename to the file's name in the dir.
					fsRoot, filenameInsideFS = filepath.Split(fsRoot)

					// since we changed the FS root, we have to update the fsys to reflect that
					switch f := fsys.(type) {
					case *archives.DeepFS:
						f.Root = fsRoot
					default:
						// recreate the file system, since we might have just changed a FileFS
						// into a DirFS, for example
						fsys, err = archives.FileSystem(job.ctx, fsRoot, nil)
						if err != nil {
							return fmt.Errorf("recreating file system at %s: %w", fsRoot, err)
						}
					}
				}

				dirEntry := DirEntry{
					DirEntry: fs.FileInfoToDirEntry(info),
					FS:       fsys,
					FSRoot:   fsRoot,
					Filename: filenameInsideFS,
				}

				if err := p.process(job.Context(), dirEntry, chkpt.DataSourceCheckpoint); err != nil {
					return fmt.Errorf("processing %s: %w", filename, err)
				}

				// the data source checkpoint is only applicable to the starting point (the filename we resumed from)
				chkpt.DataSourceCheckpoint = nil
			}
		}

		if estimating {
			// inform the UI of the new total count
			total := atomic.LoadInt64(totalSizeEstimate)
			job.SetTotal(int(total))
			job.FlushProgress()
			job.Logger().Info("done with size estimation", zap.Int64("estimated_size", total))

			// loop once more to import items (don't estimate again)
			estimating = false
			continue
		}

		// only import the data once
		break
	}
	if err := job.Context().Err(); err != nil {
		return err
	}

	job.Logger().Info("import complete; cleaning up")

	if err := ij.successCleanup(); err != nil {
		job.Logger().Error("cleaning up after import job", zap.Error(err))
	}

	ij.generateThumbnailsForImportedItems()
	ij.generateEmbeddingsForImportedItems()

	// TODO: I had a performance issue that was solved by running ANALYZE after a large import.
	// (`PRAGMA optimize` did not fix it.) Hence doing it here. Hopefully this is fine?
	Log.Debug("optimizing DB")
	job.tl.dbMu.RLock()
	_, err := job.tl.db.ExecContext(job.ctx, `ANALYZE`)
	job.tl.dbMu.RUnlock()
	if err != nil {
		Log.Error("analyzing database: %w", zap.Error(err))
	}

	return nil
}

func (ij ImportJob) successCleanup() error {
	// delete empty items from this import (items with no content and no meaningful relationships)
	if err := ij.deleteEmptyItems(); err != nil {
		return fmt.Errorf("deleting empty items: %w", err)
	}
	return nil
}

// deleteEmptyItems deletes items that have no content and no meaningful relationships,
// from the given import.
func (ij ImportJob) deleteEmptyItems() error {
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
	ij.job.tl.dbMu.RLock()
	rows, err := ij.job.tl.db.Query(`SELECT id FROM extended_items
		WHERE job_id=?
		AND (data_text IS NULL OR data_text='')
			AND (classification_name != ? OR metadata IS NULL)
			AND data_file IS NULL
			AND longitude IS NULL
			AND latitude IS NULL
			AND altitude IS NULL
			AND retrieval_key IS NULL
			AND id NOT IN (SELECT from_item_id FROM relationships WHERE to_item_id IS NOT NULL)`,
		ij.job.id, ClassBookmark.Name) // TODO: consider deleting regardless of relationships existing (remember the iMessage data source until we figured out why some referred-to rows were totally missing?)
	if err != nil {
		ij.job.tl.dbMu.RUnlock()
		return fmt.Errorf("querying empty items: %w", err)
	}

	var emptyItems []int64
	for rows.Next() {
		var rowID int64
		err := rows.Scan(&rowID)
		if err != nil {
			defer ij.job.tl.dbMu.RUnlock()
			defer rows.Close()
			return fmt.Errorf("scanning item: %w", err)
		}
		emptyItems = append(emptyItems, rowID)
	}
	rows.Close()
	ij.job.tl.dbMu.RUnlock()
	if err = rows.Err(); err != nil {
		return fmt.Errorf("iterating item rows: %w", err)
	}

	// nothing to do if no items were empty
	if len(emptyItems) == 0 {
		return nil
	}

	ij.job.Logger().Info("deleting empty items from this import", zap.Int("count", len(emptyItems)))

	retention := time.Duration(0)
	return ij.job.tl.deleteItemRows(ij.job.tl.ctx, emptyItems, false, &retention)
}

// generateThumbnailsForImportedItems generates thumbnails for qualifying items
// that were a part of the import associated with this processor. It should be
// run after the import completes.
// TODO: What about generating thumbnails for... just anything that needs one
func (ij ImportJob) generateThumbnailsForImportedItems() {
	var job thumbnailJob

	// prevent duplicates
	var (
		dataIDs   = make(map[int64]struct{})
		dataFiles = make(map[string]struct{})
	)

	// get the items in reverse timestamp order since the
	// most recent items are most likely to be displayed by
	// various frontend pages, i.e. the user will likely
	// see thoes first
	ij.job.tl.dbMu.RLock()
	rows, err := ij.job.tl.db.QueryContext(ij.job.tl.ctx,
		`SELECT id, data_id, data_type, data_file
		FROM items
		WHERE job_id=? AND (data_file IS NOT NULL OR data_id IS NOT NULL)
		ORDER BY timestamp DESC`, ij.job.id)
	if err != nil {
		ij.job.tl.dbMu.RUnlock()
		ij.job.Logger().Error("unable to generate thumbnails from this import", zap.Error(err))
		return
	}

	for rows.Next() {
		var rowID int64
		var dataID *int64
		var dataType, dataFile *string

		err := rows.Scan(&rowID, &dataID, &dataType, &dataFile)
		if err != nil {
			defer rows.Close()
			ij.job.Logger().Error("unable to scan row to generate thumbnails from this import", zap.Error(err))
			ij.job.tl.dbMu.RUnlock()
			return
		}
		if qualifiesForThumbnail(dataType) {
			if dataID != nil {
				if _, seen := dataIDs[*dataID]; !seen {
					job.Tasks = append(job.Tasks, thumbnailTask{
						DataID:    *dataID,
						DataType:  *dataType,
						ThumbType: thumbnailType(*dataType, false),
					})
				}
			} else if dataFile != nil {
				if _, seen := dataFiles[*dataFile]; !seen {
					job.Tasks = append(job.Tasks, thumbnailTask{
						DataFile:  *dataFile,
						DataType:  *dataType,
						ThumbType: thumbnailType(*dataType, false),
					})
				}
			}
		}
	}
	rows.Close()
	ij.job.tl.dbMu.RUnlock()

	if err = rows.Err(); err != nil {
		ij.job.Logger().Error("iterating rows for generating thumbnails failed", zap.Error(err))
		return
	}

	if len(job.Tasks) == 0 {
		ij.job.Logger().Debug("no thumbnails to generate from this job")
		return
	}

	// // convert maps to a slice; ordering is important for checkpoints
	// job.Tasks = make([]thumbnailTask, len(dataIDs)+len(dataFiles))
	// var i int
	// for _, task := range dataIDs {
	// 	job.Tasks[i] = task
	// 	i++
	// }
	// for _, task := range dataFiles {
	// 	job.Tasks[i] = task
	// 	i++
	// }

	ij.job.Logger().Info("generating thumbnails for imported items", zap.Int("count", len(job.Tasks)))

	if _, err = ij.job.tl.CreateJob(job, time.Time{}, len(job.Tasks), 0, ij.job.id); err != nil {
		ij.job.Logger().Error("creating thumbnail job", zap.Error(err))
		return
	}
}

func (ij ImportJob) generateEmbeddingsForImportedItems() {
	ij.job.tl.dbMu.RLock()
	rows, err := ij.job.tl.db.QueryContext(ij.job.tl.ctx, `
		SELECT id, data_id, data_type, data_file
		FROM items
		WHERE job_id=?
			AND data_type IS NOT NULL
			AND (data_id IS NOT NULL OR data_text IS NOT NULL OR data_file IS NOT NULL)
		`, ij.job.id)
	if err != nil {
		ij.job.tl.dbMu.RUnlock()
		ij.job.Logger().Error("unable to generate embeddings from this import", zap.Error(err))
		return
	}

	var job embeddingJob
	var idMap = make(map[int64]struct{}) // prevent duplicates

	for rows.Next() {
		var rowID int64
		var dataID *int64
		var dataType, dataFile *string

		err := rows.Scan(&rowID, &dataID, &dataType, &dataFile)
		if err != nil {
			defer rows.Close()
			ij.job.Logger().Error("unable to scan row to generate embeddings from this import", zap.Error(err))
			ij.job.tl.dbMu.RUnlock()
			return
		}

		if !strings.HasPrefix(*dataType, "image/") && !strings.HasPrefix(*dataType, "text/") {
			continue
		}

		idMap[rowID] = struct{}{}
	}
	rows.Close()
	ij.job.tl.dbMu.RUnlock()

	if err = rows.Err(); err != nil {
		ij.job.Logger().Error("iterating rows for generating embeddings failed", zap.Error(err))
		return
	}

	if len(idMap) == 0 {
		ij.job.Logger().Debug("no embeddings to generate from this job")
		return
	}

	total := len(idMap)

	// now that our read lock on the DB is released, we can take our time generating embeddings in the background

	ij.job.Logger().Info("generating embeddings for imported items", zap.Int("count", total))

	// convert map to slice; it's more concise in the DB and ordering is important with checkpoints
	job.ItemIDs = make([]int64, len(idMap))
	var i int
	for id := range idMap {
		job.ItemIDs[i] = id
		i++
	}

	if _, err = ij.job.tl.CreateJob(job, time.Time{}, total, 0, ij.job.id); err != nil {
		ij.job.Logger().Error("creating embedding job", zap.Error(err))
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

// ImportPlan describes what will be imported and how.
type ImportPlan struct {
	Files []FileImport `json:"files,omitempty"` // map of data source name to list of files+DSopt pairs
}

// FileImport represents a list of files to import with a specific
// configuration from a data source.
type FileImport struct {
	// The name of the data source to use.
	DataSourceName string `json:"data_source_name"`

	// Configuration specific to the data source.
	DataSourceOptions json.RawMessage `json:"data_source_options,omitempty"`

	// The (OS-compatible) absolute filepaths to the files or directories
	// to be imported. These paths may refer to files within archive files,
	// so the archives.DeepFS type should be used to access them.
	Filenames []string `json:"filenames"`
}

// ProposedImportPlan is a suggested import plan, to be reviewed and
// tweaked by the user. It is similar to the ImportPlan type, but
// instead of grouping a list of files by specific data source
// configurations, each file is listed together with its results
// from data source recognition, to be shown to the user so they
// can tweak the results and approve a final import plan.
type ProposedImportPlan struct {
	Files []ProposedFileImport `json:"files,omitempty"`
}

type ProposedFileImport struct {
	Filename    string                  `json:"filename"`
	FileType    string                  `json:"file_type,omitempty"` // file, dir, or archive
	DataSources []DataSourceRecognition `json:"data_sources,omitempty"`
}

func (p ProposedFileImport) String() string { return p.Filename }
