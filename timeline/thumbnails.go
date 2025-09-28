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
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"log"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	// TODO: I would like to just use "github.com/strukturag/libheif/go/heif"
	// as our AVIF decoder, since that is what we used earlier during development
	// but now I just get a distorted green mess:
	// https://x.com/mholt6/status/1864894439061381393
	"github.com/davidbyttow/govips/v2/vips"
	_ "github.com/gen2brain/avif" // register AVIF image decoder
	"go.n16f.net/thumbhash"
	"go.uber.org/zap"
	_ "golang.org/x/image/webp" // register WEBP image decoder
)

func init() {
	vips.LoggingSettings(nil, vips.LogLevelError)
	vips.Startup(nil) // Shutdown() is called in a sigtrap for clean shutdowns
}

/*
	TODO: A short preview/thumbnail of a video could be made with ffmpeg like this:

	$ ffmpeg -i input.mp4 -vf scale=320:-1 -quality 20 -ss 0 -t 3 ~/Downloads/output.webp

	This is an animated webp image of quite low quality, of the first 3 seconds of the video.
	Max width 320, scale height to preserve aspect ratio. The quality and size params can be
	increased but it will go slower. I think '-vcodec libwebp' is implied with the file ext
	(but then again, specifying -f webp also works if I want to pipe the output)


	TODO: To generate a video preview (webm):

	$ ffmpeg -i input.mp4 -vf scale=480:-1 -vcodec libvpx -acodec libvorbis -b:v 1M -ss 0 -t 3 ~/Downloads/output.webm

	Specifying the vcodec and acodec makes it faster. Tune scale and bitrate (-b:v) to
	satisfaction.
*/

type thumbnailJob struct {
	// must make sure there are no duplicate tasks in this slice,
	// but a slice is important because ordering is important for
	// checkpoints
	Tasks []thumbnailTask `json:"tasks,omitempty"`

	// infer tasks from the given import job
	TasksFromImportJob uint64 `json:"tasks_from_import_job,omitempty"`

	// if true, regenerate thumbnails even if they already exist
	// (thumbnails will always be regenerated if the item has
	// been updated since it was generated, even with this off)
	RegenerateAll bool `json:"regenerate_all"`
}

func (tj thumbnailJob) Run(job *ActiveJob, checkpoint []byte) error {
	// if the thumbnails to generate were explicitly enumerated, simply do those
	if len(tj.Tasks) > 0 {
		job.Logger().Info("generating thumbnails using predefined list", zap.Int("count", len(tj.Tasks)))

		var startIdx int
		if checkpoint != nil {
			if err := json.Unmarshal(checkpoint, &startIdx); err != nil {
				job.logger.Error("failed to resume from checkpoint", zap.Error(err))
			}
			job.Logger().Info("resuming from checkpoint",
				zap.Int("position", startIdx),
				zap.Int("total_count", len(tj.Tasks)))
		}
		return tj.processInBatches(job, tj.Tasks, startIdx, true)
	}

	if tj.TasksFromImportJob == 0 {
		return errors.New("no tasks; expecting either individual tasks listed, or an import job")
	}

	logger := job.Logger().With(zap.Uint64("import_job_id", tj.TasksFromImportJob))

	logger.Info("counting job size")

	job.Message("Estimating total job size")

	jobSize, err := tj.iteratePagesOfTasksFromImportJob(job, true)
	if err != nil {
		return fmt.Errorf("counting job size: %w", err)
	}

	job.SetTotal(jobSize)

	logger.Info("generating thumbnails for items from import job", zap.Int("count", jobSize))

	_, err = tj.iteratePagesOfTasksFromImportJob(job, false)
	return err
}

// iteratePagesOfTasksFromImportJob goes through pages of database results for the import job specified
// in the job config and, after figuring out which rows of items point to data files that need thumbnails,
// counts the number of tasks and, if precountMode is false, also generates and stores the thumbnails
// (and thumbhashes). If precountMode is true, only the counting will occur, and the total number of
// tasks to be expected will be returned. Note that this return value may be invalid if the import job
// is still running or if any items associated with the import job are changing while this job is running.
// If that happens, the final completion percentage may not be exactly 100%. This could be fixed if we
// do all the counting in a single DB lock and then keep the results in memory to then work on them, but
// I haven't seen a need for this yet, and current method uses a little less memory.
func (tj thumbnailJob) iteratePagesOfTasksFromImportJob(job *ActiveJob, precountMode bool) (int, error) {
	// the query that is used to page through rows.
	// get the items in reverse timestamp order since the
	// most recent items are most likely to be displayed by
	// various frontend pages, i.e. the user will likely
	// see those first (TODO: maybe this should be customizable)
	// (this query pages through results by timestamp, i.e.
	// the column we order by, which is efficient but if we
	// aren't careful we can skip rows: if we did timestamp < ?,
	// then if the LIMIT happened to end between rows with the
	// same timestamp, the next query would skip those; so we also
	// use row ID as the second sort column to avoid repeating or
	// skipping items within the same timestamp)
	const thumbnailJobQuery = `
		SELECT
			items.id, items.stored, items.modified, items.timestamp,
			items.data_id, items.data_type, items.data_file, jobs.ended
		FROM items
		LEFT JOIN jobs ON jobs.id = items.modified_job_id
		LEFT JOIN relationships ON relationships.to_item_id = items.id
		LEFT JOIN relations ON relations.id = relationships.relation_id
		WHERE (job_id=? OR modified_job_id=?)
			AND (items.data_file IS NOT NULL OR items.data_id IS NOT NULL)
			AND (items.data_type LIKE 'image/%' OR items.data_type LIKE 'video/%' OR items.data_type = 'application/pdf')
			AND relations.label IS NOT 'motion'
			AND (timestamp < ? OR ((timestamp = ? OR timestamp IS NULL) AND items.id > ?))
		ORDER BY items.timestamp DESC, items.id ASC
		LIMIT ?`
	const thumbnailJobPageSize = 1000

	var (
		maxInt64      int64 = math.MaxInt64
		lastTimestamp       = &maxInt64 // start at highest possible timestamp since we're sorting by timestamp descending (next page of results will have lower timestamps, not greater)
		lastItemID    int64             // start at 0, since we're sorting by ID ascending (useful for keeping our place in rows with the same timestamp)

		taskCount int // if precountMode enabled, we keep count of the number of tasks, and don't do actual work
	)

	for {
		log.Println("NEXT PAGE OF ITEMS...")
		var pageResults []thumbnailTask

		// prevent duplicates within a page; when we load a page, we do check
		// each row to see if a current thumbnail has already been generated, but
		// since we don't actually generate thumbnails until after doing all the
		// checks for a page (for efficiency), it's possible that multiple rows
		// on the page share a data file and they both add the same file to the
		// task queue; so we have to deduplicate those per-page, otherwise we not
		// only duplicate work, but our final count is wrong from the estimated
		// total -- by the time we get to the next page, the thumbnails for this
		// page have all been generated and stored in the DB, so we don't need
		// to de-duplicate in memory across all pages
		pageDataFiles, pageDataIDs := make(map[string]struct{}), make(map[int64]struct{})

		job.tl.dbMu.RLock()
		log.Println("QUERYING DB:", lastTimestamp, lastItemID, taskCount)
		rows, err := job.tl.db.QueryContext(job.ctx, thumbnailJobQuery,
			tj.TasksFromImportJob, tj.TasksFromImportJob,
			lastTimestamp, lastTimestamp, lastItemID, thumbnailJobPageSize)
		if err != nil {
			job.tl.dbMu.RUnlock()
			return 0, fmt.Errorf("failed querying page of database table: %w", err)
		}

		var hadRows bool
		for rows.Next() { // <-- TODO: THIS CAUSES THE EXCEPTION
			hadRows = true

			var rowID, stored int64
			var modified, timestamp, dataID, modJobEnded *int64
			var dataType, dataFile *string

			err := rows.Scan(&rowID, &stored, &modified, &timestamp, &dataID, &dataType, &dataFile, &modJobEnded)
			if err != nil {
				log.Println("ERROR A:", err)
				rows.Close()
				job.tl.dbMu.RUnlock()
				return 0, fmt.Errorf("failed to scan row from database page: %w", err)
			}

			// Keep the last item ID and timestamp for use by the next page.
			lastItemID = rowID
			lastTimestamp = timestamp

			// if the item qualifies for a thumbnail, make sure it's not a duplicate
			// on this page before adding it to the queue; but if it is a duplicate,
			// we should still count it, since
			if qualifiesForThumbnail(dataType) {
				if dataFile != nil {
					if _, ok := pageDataFiles[*dataFile]; !ok {
						pageDataFiles[*dataFile] = struct{}{}
						pageResults = append(pageResults, thumbnailTask{
							DataFile:     *dataFile,
							DataType:     *dataType,
							ThumbType:    thumbnailType(*dataType, false),
							itemStored:   stored,
							itemModified: modified,
							modJobEnded:  modJobEnded,
						})
					}
				} else if dataID != nil {
					if _, ok := pageDataIDs[*dataID]; !ok {
						pageDataIDs[*dataID] = struct{}{}
						pageResults = append(pageResults, thumbnailTask{
							DataID:       *dataID,
							DataType:     *dataType,
							ThumbType:    thumbnailType(*dataType, false),
							itemStored:   stored,
							itemModified: modified,
							modJobEnded:  modJobEnded,
						})
					}
				}
			}
		}
		rows.Close()
		job.tl.dbMu.RUnlock()
		log.Println("CLOSED ROWS, PAGE RESULTS:", len(pageResults), "TASKS SO FAR:", taskCount)
		if err = rows.Err(); err != nil {
			return 0, fmt.Errorf("iterating rows for researching thumbnails failed: %w", err)
		}

		if !hadRows {
			log.Println("ALL DONE")
			break // all done!
		}

		// based on lots of trial+error, this is the correct place to increment the count of tasks
		// (before pruning; since we consider the check to see if a thumbnail is actually needed
		// to be a unit of work itself; i.e. resuming an incomplete job will treat all the skipped
		// data files as progress, and that's OK since it was work to determine that the thumbnail
		// isn't needed)
		taskCount += len(pageResults)

		// if we are not supposed to regenerate all thumbnails, look up the existing thumbnail
		// for each item on this page and see if the item has been updated more recently than
		// the thumbnail; if so, we need to regenerate it anyway
		if !tj.RegenerateAll {
			// read-only tx, but should be faster than individual queries as we iterate items
			job.tl.thumbsMu.RLock()
			thumbsTx, err := job.tl.thumbs.BeginTx(job.ctx, nil)
			if err != nil {
				job.tl.thumbsMu.RUnlock()
				return 0, fmt.Errorf("starting tx for checking for existing thumbnails: %w", err)
			}

			// use classic loop style since we'll be deleting entries as we go
			for i := 0; i < len(pageResults); i++ {
				task := pageResults[i]

				// if we're not supposed to regenerate every thumbnail, see if thumbnail already exists; we might
				// still regenerate it if it's from before when the item was last stored or manually updated
				// (this query is optimized to use indexes, which is crucial to make the job faster, especially
				// the size estimation/precount phase)
				q := `
					SELECT generated FROM (
						SELECT generated FROM thumbnails WHERE data_file = ?
						UNION ALL
						SELECT generated FROM thumbnails WHERE item_data_id = ?
					)
					LIMIT 1`
				var thumbGenerated int64
				err := thumbsTx.QueryRowContext(job.ctx, q, task.DataFile, task.DataID).Scan(&thumbGenerated)
				if errors.Is(err, sql.ErrNoRows) {
					// no existing thumbnail; carry on
					continue
				} else if err != nil {
					// DB error; probably shouldn't continue
					thumbsTx.Rollback()
					job.tl.thumbsMu.RUnlock()
					return 0, fmt.Errorf("checking for existing thumbnail: %w", err)
				}

				// if the thumbnail was generated when or after the item was stored, and also after
				// it was modified (if modified at all), and also after any import job that modified
				// it completed, then we do not need to generate it, so we can remove it from the
				// task queue -- this is considered progress anyway
				if thumbGenerated >= task.itemStored &&
					(task.itemModified == nil || thumbGenerated >= *task.itemModified) &&
					(task.modJobEnded == nil || thumbGenerated >= *task.modJobEnded) {
					pageResults = slices.Delete(pageResults, i, i+1)

					// two possibilities: this could either be a thumbnail we already generated as part of this job,
					// or it was a duplicate data file that already had a thumbnail generated for it (as part of this
					// job, or otherwise) -- either way, we can't distinguish between them, so we need to count it
					// as progress to reach 100% completion; besides, this check/pruning is considered a unit of work
					if !precountMode {
						job.Progress(1)
					}

					// since we're deleting this element AFTER the work on this element has
					// been done, we need to decrement our index before the next iteration,
					// otherwise we skip an element, which in the past has led to us not
					// pruning that element when we should have, which led to >100% progress
					// at job completion; play demo: https://go.dev/play/p/rH5szSDC2SD
					// (see how only the last demo visits/prints every element)
					i--
				}
			}

			// rollback should be OK since we didn't mutate anything, should even be slightly more efficient than a commit?
			log.Println("ROLLING BACK")
			thumbsTx.Rollback()
			job.tl.thumbsMu.RUnlock()
			log.Println("DONE ROLLING BACK")
		}

		// if all we needed to do was count, we did that for this page, so move on to the next page
		if precountMode {
			continue
		}

		// nothing to do if there are no actionable results on this page
		if len(pageResults) == 0 {
			continue
		}

		// since we're not in precount mode, go ahead and actually generate the thumbnails+thumbhashes
		if err := tj.processInBatches(job, pageResults, 0, false); err != nil {
			return 0, fmt.Errorf("processing page of thumbnail tasks: %w", err)
		}
	}

	log.Println("THUMBNAIL JOB SIZE:", taskCount)

	return taskCount, nil
}

func (thumbnailJob) processInBatches(job *ActiveJob, tasks []thumbnailTask, startIdx int, checkpoints bool) error {
	if len(tasks) == 0 {
		job.Logger().Debug("no thumbnails to generate")
		return nil
	}

	// Run each task in a goroutine by batch; this has multiple advantages:
	// - Goroutines allow parallel computation, finishing the job faster.
	// - By waiting at the end of every batch, we know that all goroutines in the batch
	//   have finished, so we can checkpoint and thus resume correctly from that index,
	//   without having to worry about some goroutines that haven't finished yet.
	// - Batching in this way acts as a goroutine throttle, so we don't flood the CPU.
	//
	// Downside: Videos thumbnails often take much longer to generate than stills, so
	// it can appear that the whole job has stalled after the still thumbnails are
	// done. It would be interesting to see if we can find a way to at least keep the
	// status message current with which video is being processed, since many of the
	// stills will be done so quickly the user won't even see them. Also, smaller
	// batches mean better checkpoint and pause precision, but less concurrency (speed).
	// Since we don't use checkpoints with large import jobs (as the tasks are calculated
	// on-the-fly based on import ID), and we can resume a job by paging through the
	// database without a checkpoint, we prefer a larger batch size when checkpointing
	// isn't enabled. This should help keep the CPU busy longer while working on videos,
	// assuming mostly stills in the batch. But not too large, so that pausing doesn't
	// take forever.
	var wg sync.WaitGroup
	batchSize := 10
	if checkpoints {
		batchSize = 5
	}

	// all goroutines must be done before we return
	defer wg.Wait()

	for i := startIdx; i < len(tasks); i++ {
		// At the end of every batch, wait for all the goroutines to complete before
		// proceeding; and once they complete, that's a good time to checkpoint
		if i%batchSize == batchSize-1 {
			wg.Wait()

			if checkpoints {
				if err := job.Checkpoint(i); err != nil {
					job.Logger().Error("failed to save checkpoint",
						zap.Int("position", i),
						zap.Error(err))
				}
			}

			if err := job.Continue(); err != nil {
				return err
			}
		}

		task := tasks[i]
		task.tl = job.tl

		// proceed to spawn a new goroutine as part of this batch
		wg.Add(1)
		go func(job *ActiveJob, task thumbnailTask) {
			defer wg.Done()

			logger := job.Logger().With(
				zap.Int64("data_id", task.DataID),
				zap.String("data_file", task.DataFile),
				zap.String("data_type", task.DataType),
				zap.String("thumbnail_type", task.ThumbType),
			)

			// TODO: we're in a tight loop as part of a batch; how do we get it to
			// show for the user during longer tasks such that it isn't overwritten?
			job.Message(task.DataFile)

			thumb, thash, err := task.thumbnailAndThumbhash(job.Context(), false)
			if err != nil {
				// don't terminate the job if there's an error
				// TODO: but we should probably note somewhere in the job's row in the DB that this error happened... maybe?
				logger.Error("thumbnail/thumbhash generation failed", zap.Error(err))
			} else {
				if thumb.alreadyExisted {
					logger.Info("thumbnail already existed")
				} else {
					logger.Info("finished thumbnail", zap.Binary("thumb_hash", thash))
				}
			}

			job.Progress(1)
		}(job, task)
	}

	return nil
}

// thumbnailTask represents a thumbnail that needs to be generated.
type thumbnailTask struct {
	tl *Timeline

	// set only ONE of these, DataID if the content to be thumbnailed
	// is in the database, or dataFile if the content is in a file
	DataID   int64  `json:"data_id,omitempty"`
	DataFile string `json:"data_file,omitempty"`

	// the media type (data_type field of the item row), if known,
	// can avoid an extra DB query; needed to determine how to load
	// and process the media we are thumbnailing
	DataType string `json:"data_type,omitempty"`

	// whether to make an image or video thumbnail; images have to
	// have image thumbnails, but videos can have either
	ThumbType string `json:"thumb_type,omitempty"`

	// used only temporarily to determine if a thumbnail task should be kept
	itemStored   int64  // when the associated item was stored in the DB
	itemModified *int64 // when the associated item was last manually modified
	modJobEnded  *int64 // when the job that last modified associated item ended
}

// thumbnailAndThumbhash returns the thumbnail, even if this returns an error because thumbhash
// generation fails, the thumbnail is still usable in that case.
func (task thumbnailTask) thumbnailAndThumbhash(ctx context.Context, forceThumbhashGeneration bool) (Thumbnail, []byte, error) {
	if task.DataID > 0 && task.DataFile != "" {
		// is the content in the DB or a file?? can't be both
		panic("ambiguous thumbnail task given both dataID and dataFile")
	}
	thumb, err := task.generateAndStoreThumbnail(ctx)
	if err != nil {
		return Thumbnail{}, nil, fmt.Errorf("generating/storing thumbnail: %w", err)
	}
	if thumb.alreadyExisted && !forceThumbhashGeneration {
		return thumb, nil, nil
	}
	var thash []byte
	if strings.HasPrefix(thumb.MediaType, "image/") {
		if thash, err = task.generateAndStoreThumbhash(ctx, thumb.Content); err != nil {
			return thumb, thash, fmt.Errorf("generating/storing thumbhash: %w", err)
		}
	}
	return thumb, thash, nil
}

var thumbnailMapMu = newMapMutex()

func (task thumbnailTask) generateAndStoreThumbnail(ctx context.Context) (Thumbnail, error) {
	task.DataType = strings.ToLower(task.DataType)
	task.ThumbType = strings.ToLower(task.ThumbType)

	if !qualifiesForThumbnail(&task.DataType) {
		return Thumbnail{}, fmt.Errorf("media type does not support thumbnailing: %s (item_data_id=%d data_file='%s')", task.DataType, task.DataID, task.DataFile)
	}

	// sync generation to save resources: don't allow a thumbnail to be generated multiple times
	var lockKey any = task.DataFile
	if task.DataID != 0 {
		lockKey = task.DataID
	}
	thumbnailMapMu.Lock(lockKey)
	defer thumbnailMapMu.Unlock(lockKey)

	// see if it was already done
	if thumb, err := task.tl.loadThumbnail(ctx, task.DataID, task.DataFile, task.ThumbType); err == nil {
		// thumbnail already exists, which implies that there's a row in the DB with a thumbhash for it,
		// so copy that thumbhash to all other rows that use that data file
		thumb.alreadyExisted = true
		task.tl.dbMu.Lock()
		defer task.tl.dbMu.Unlock()
		var err error
		if task.DataID != 0 {
			_, err = task.tl.db.ExecContext(ctx, `UPDATE items SET thumb_hash=(SELECT thumb_hash FROM items WHERE data_id=? AND thumb_hash IS NOT NULL LIMIT 1) WHERE data_id=?`, task.DataID, task.DataID)
		} else {
			_, err = task.tl.db.ExecContext(ctx, `UPDATE items SET thumb_hash=(SELECT thumb_hash FROM items WHERE data_file=? AND thumb_hash IS NOT NULL LIMIT 1) WHERE data_file=?`, task.DataFile, task.DataFile)
		}
		return thumb, err
	}

	// if not, or if the other goroutine failed, we need to generate it

	var inputBuf []byte
	var inputFilename string

	if task.DataFile != "" {
		inputFilename = task.tl.FullPath(task.DataFile)
	} else if task.DataID > 0 {
		task.tl.dbMu.RLock()
		err := task.tl.db.QueryRowContext(ctx,
			`SELECT content FROM item_data WHERE id=? LIMIT 1`, task.DataID).Scan(&inputBuf)
		task.tl.dbMu.RUnlock()
		if err != nil {
			return Thumbnail{}, fmt.Errorf("querying item data content: %w", err)
		}
	}

	thumbnail, mimeType, err := task.generateThumbnail(ctx, inputFilename, inputBuf)
	if err != nil {
		return Thumbnail{}, fmt.Errorf("generating thumbnail for content: %w (item_data_id=%d data_file='%s')", err, task.DataID, inputFilename)
	}

	dataFileToInsert, dataIDToInsert := &task.DataFile, &task.DataID
	if task.DataFile == "" {
		dataFileToInsert = nil
	} else {
		dataIDToInsert = nil
	}
	now := time.Now()

	task.tl.thumbsMu.Lock()
	defer task.tl.thumbsMu.Unlock()

	_, err = task.tl.thumbs.ExecContext(ctx, `
		INSERT INTO thumbnails (data_file, item_data_id, mime_type, content)
		VALUES (?, ?, ?, ?)
		ON CONFLICT DO UPDATE
		SET generated=?, mime_type=?, content=?
		WHERE data_file IS ? AND item_data_id IS ?`,
		dataFileToInsert, dataIDToInsert, mimeType, thumbnail,
		now.Unix(), mimeType, thumbnail,
		dataFileToInsert, dataIDToInsert)
	if err != nil {
		return Thumbnail{}, fmt.Errorf("saving thumbnail to database: %w (item_data_id=%d data_file='%s')", err, task.DataID, task.DataFile)
	}

	return Thumbnail{
		Name:      fakeThumbnailFilename(task.DataID, task.DataFile, mimeType),
		MediaType: mimeType,
		ModTime:   now,
		Content:   thumbnail,
	}, nil
}

// fakeThumbnailFilename generates a fake name for a thumbnail. Sometimes this
// can be useful in an HTTP context.
func fakeThumbnailFilename(itemDataID int64, dataFile, dataType string) string {
	fakeName := dataFile
	if itemDataID > 0 {
		fakeName = strconv.FormatInt(itemDataID, 10)
	}
	fakeName += ".thumb"
	_, after, found := strings.Cut(dataType, "/")
	if found {
		fakeName += "." + after
	}
	return fakeName
}

func (task thumbnailTask) generateThumbnail(ctx context.Context, inputFilename string, inputBuf []byte) ([]byte, string, error) {
	// throttle expensive operation
	cpuIntensiveThrottle <- struct{}{}
	defer func() { <-cpuIntensiveThrottle }()

	// in case task was cancelled while we waited for the throttle
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}

	var thumbnail []byte
	var mimeType string

	switch {
	case strings.HasPrefix(task.DataType, "image/") || task.DataType == "application/pdf":
		inputImage, err := loadImageVips(inputFilename, inputBuf)
		if err != nil {
			return nil, "", fmt.Errorf("opening source file: %w", err)
		}
		defer inputImage.Close()

		// scale down to a thumbnail size
		// TODO: Make the algorithm configurable
		if err := inputImage.Thumbnail(maxThumbnailDimension, maxThumbnailDimension, vips.InterestingNone); err != nil {
			return nil, "", fmt.Errorf("thumbnailing image: %w", err)
		}

		// 10-bit is HDR, but the underlying lib (libvips, and then libheif) doesn't support "10-bit colour depth" on Windows
		bitDepth := 10
		if runtime.GOOS == "windows" {
			bitDepth = 8
		}

		// encode the resized image as the proper output format
		switch task.ThumbType {
		case ImageJPEG:
			ep := vips.NewJpegExportParams()
			ep.StripMetadata = true // (note: this strips rotation info, which is needed if rotation is not applied manually)
			ep.Quality = 40
			ep.Interlace = true
			ep.SubsampleMode = vips.VipsForeignSubsampleAuto
			ep.TrellisQuant = true
			ep.QuantTable = 3
			thumbnail, _, err = inputImage.ExportJpeg(ep)
		case ImageAVIF:
			// fun fact: AVIF supports animation, but I can't get ffmpeg to generate it faster than 0.0016x speed
			// (vips is fast enough for stills though, as long as we tune down the parameters sufficiently)
			ep := vips.NewAvifExportParams()
			ep.StripMetadata = true // (note: this strips rotation info, which is needed if rotation is not applied manually)
			ep.Quality = 45
			ep.Bitdepth = bitDepth
			ep.Effort = 1
			thumbnail, _, err = inputImage.ExportAvif(ep)
		case ImageWebP:
			ep := vips.NewWebpExportParams()
			ep.StripMetadata = true // (note: this strips rotation info, which is needed if rotation is not applied manually)
			ep.Quality = 50
			thumbnail, _, err = inputImage.ExportWebp(ep)
		default:
			panic("unsupported thumbnail MIME type: " + task.ThumbType)
		}
		if err != nil {
			return nil, "", fmt.Errorf("encoding image thumbnail: %w", err)
		}
		mimeType = task.ThumbType

	case strings.HasPrefix(task.DataType, "video/"):
		// TODO: support inputBuf here... dunno if we can pipe it (ideal), or if we have to write a temporary file
		if inputFilename == "" {
			return nil, "", errors.New("TODO: not implemented: support for video items stored in DB")
		}

		var cmd *exec.Cmd
		switch {
		case strings.HasPrefix(task.ThumbType, "image/"):
			// I have found that smaller dimension and higher quality is a good tradeoff for keeping
			// file size small -- it obviously doesn't look great, but lower quality looks REALLY bad
			//nolint:gosec
			cmd = exec.CommandContext(ctx, "ffmpeg",
				"-i", inputFilename,
				"-vf", fmt.Sprintf("scale=%d:-1", maxVideoThumbnailDimension),
				"-quality", "80",
				"-ss", "0",
				"-t", "2", // how many seconds to animate
				"-f", "webp", // TODO: avif would be preferred... but can't be piped out I guess?
				"-", // pipe to stdout
			)
			mimeType = ImageWebP
			// This generates a still:
			// cmd = exec.Command("ffmpeg",
			// 	"-ss", "00:00:01.000",
			// 	"-i", inputFilename,
			// 	"-vf", fmt.Sprintf("scale=%d:-1", maxThumbnailDimension),
			// 	"-vframes", "1",
			// 	"-f", "webp", // TODO: avif would be preferred... but can't be piped out I guess?
			// 	"-", // pipe to stdout
			// )
		case strings.HasPrefix(task.ThumbType, "video/"):
			cmd = exec.CommandContext(ctx, "ffmpeg",
				// include only the first few seconds of video; start at the beginning
				// (putting this before -i is a fast, but inaccurate skip-to, but we know
				// the beginning will have a keyframe)
				"-ss", "0",

				"-i", inputFilename,

				// keep thumbnail video short; limit it to this many seconds
				// (TODO: Shorter videos will still look this long but won't be, and may not loop in some players; we can determine
				// video length pretty quickly with: `ffprobe -v error -show_entries format=duration -of default=noprint_wrappers=1:nokey=1 input.mp4`)
				"-t", "5",

				// important to scale down the video for fast encoding -- perhaps biggest impact on encoding speed
				"-vf", "scale='min(480,iw)':-1",

				// libvpx is much faster than default encoder
				"-vcodec", "libvpx",
				// "-acodec", "libvorbis", // if keeping audio, use this

				// strip audio, not necessary for thumbnail
				"-an",

				// bitrate, important quality determination
				"-b:v", "256k", // constant bitrate, default is 256k
				// "-crf", "40", // variable bitrate (slower encode), valid range for vpx is 4-63; higher number is lower quality

				// we are already running concurrently, but if there's CPU cores available...
				"-threads", "4",

				// when piping out (no output filename), we have to explicitly specify
				// the format since ffmpeg can't deduce it from a file extension
				"-f", "webm",

				// pipe to stdout
				"-",
			)
			mimeType = VideoWebM
		default:
			return nil, "", fmt.Errorf("task has no target media type: %+v (inputFilename=%s)", task, inputFilename)
		}

		// capture stdout, which is the thumbnail
		// (we don't pool this because the caller would need to read the bytes
		// after the buffer is returned to the pool, and copying the buffer
		// defeats the purpose)
		stdoutBuf := new(bytes.Buffer)
		cmd.Stdout = stdoutBuf
		cmd.Stderr = os.Stderr

		if err := cmd.Run(); err != nil {
			return nil, "", fmt.Errorf("generating video thumbnail: %w", err)
		}

		thumbnail = stdoutBuf.Bytes()

	default:
		return nil, "", fmt.Errorf("not sure how to generate thumbnail for '%s' data type", task.DataType)
	}

	return thumbnail, mimeType, nil
}

func (task thumbnailTask) generateAndStoreThumbhash(ctx context.Context, thumb []byte) ([]byte, error) {
	thash, err := generateThumbhashFromThumbnail(thumb)
	if err != nil {
		return nil, err
	}
	return thash, task.storeThumbhash(ctx, thash)
}

func (task thumbnailTask) storeThumbhash(ctx context.Context, thash []byte) error {
	task.tl.dbMu.Lock()
	defer task.tl.dbMu.Unlock()

	var err error
	if task.DataID != 0 {
		_, err = task.tl.db.ExecContext(ctx, `UPDATE items SET thumb_hash=? WHERE data_id=?`, thash, task.DataID)
	} else {
		_, err = task.tl.db.ExecContext(ctx, `UPDATE items SET thumb_hash=? WHERE data_file=?`, thash, task.DataFile)
	}
	return err
}

func generateThumbhashFromThumbnail(thumb []byte) ([]byte, error) {
	// throttle expensive operation
	cpuIntensiveThrottle <- struct{}{}
	defer func() { <-cpuIntensiveThrottle }()

	img, format, err := image.Decode(bytes.NewReader(thumb))
	if err != nil {
		return nil, fmt.Errorf("decoding thumbnail (format=%s) for thumbhash computation failed: %w", format, err)
	}

	return generateThumbhash(img), nil
}

// generateThumbhash returns the thumbhash of the decoded image prepended by the aspect ratio;
// the frontend will need to detach the aspect ratio (and can use it to size elements for a
// more graceful layout flow) before using the thumbhash.
func generateThumbhash(img image.Image) []byte {
	// thumbhash can recover the _approximate_ aspect ratio, but not
	// exactly, which makes sizing the image difficult on the UI because
	// replacing the thumbhash image with the real image would result in
	// a content jump because the images are different sizes! so we
	// prepend the thumbhash with the exact aspect ratio... and program
	// the frontend to split it... hey, it works...
	aspectRatio := float32(img.Bounds().Dx()) / float32(img.Bounds().Dy())

	return append(float32ToByte(aspectRatio), thumbhash.EncodeImage(img)...)
}

// Thumbnail returns a thumbnail and associated thumbhash for either the given itemDataID or the dataFile.
// If both do not exist, they are generated and stored before being returned.
func (tl *Timeline) Thumbnail(ctx context.Context, itemDataID int64, dataFile, dataType, thumbType string) (Thumbnail, []byte, error) {
	var forceThumbhashGeneration bool
	// first try loading existing thumbnail from DB
	thumb, err := tl.loadThumbnail(ctx, itemDataID, dataFile, thumbType)
	if err == nil {
		// found existing thumbnail! get thumbhash real quick, if it's an image
		if strings.HasPrefix(dataType, "image/") {
			var thash []byte
			tl.dbMu.RLock()
			err2 := tl.db.QueryRowContext(ctx, "SELECT thumb_hash FROM items WHERE (data_file=? OR data_id=?) AND thumb_hash IS NOT NULL LIMIT 1", dataFile, itemDataID).Scan(&thash)
			tl.dbMu.RUnlock()
			if err2 == nil {
				return thumb, thash, nil
			}
			// interesting! thumbnail exists, but thumbhash does not; pretend there was no result, and we'll regenerate
			err = err2
			forceThumbhashGeneration = true
		} else {
			return thumb, nil, nil
		}
	}
	if errors.Is(err, sql.ErrNoRows) {
		// no existing thumbnail; generate it and return it
		task := thumbnailTask{
			tl:        tl,
			DataID:    itemDataID,
			DataFile:  dataFile,
			DataType:  dataType,
			ThumbType: thumbType,
		}
		thumb, thash, err := task.thumbnailAndThumbhash(ctx, forceThumbhashGeneration)
		if err != nil {
			return Thumbnail{}, nil, fmt.Errorf("existing thumbnail (or thumbhash) not found, so tried generating one, but got error: %w", err)
		}
		return thumb, thash, nil
	}
	return Thumbnail{}, nil, err
}

func (tl *Timeline) loadThumbnail(ctx context.Context, dataID int64, dataFile, thumbType string) (Thumbnail, error) {
	var mimeType string
	var modTimeUnix int64
	var thumbnail []byte

	thumbType = strings.ToLower(thumbType)

	// the DB uses nullable fields; and if a dataFile is set,
	// query for the thumbnail exclusively by that, since we
	// don't want to confuse by also querying by item ID if
	// present, since that might not give intended results
	var itemDataIDToQuery *int64
	var dataFileToQuery *string
	if dataFile != "" {
		dataFileToQuery = &dataFile
	} else {
		// only set this if the item doesn't have a data
		// file, and its content is stored in the DB
		itemDataIDToQuery = &dataID
	}

	tl.thumbsMu.RLock()
	err := tl.thumbs.QueryRowContext(ctx,
		`SELECT generated, mime_type, content
			FROM thumbnails
			WHERE (item_data_id=? OR data_file=?) AND mime_type=?
			LIMIT 1`,
		itemDataIDToQuery, dataFileToQuery, thumbType).Scan(&modTimeUnix, &mimeType, &thumbnail)
	tl.thumbsMu.RUnlock()
	if err != nil {
		return Thumbnail{}, err
	}

	return Thumbnail{
		Name:      fakeThumbnailFilename(dataID, dataFile, mimeType),
		MediaType: mimeType,
		ModTime:   time.Unix(modTimeUnix, 0),
		Content:   thumbnail,
	}, nil
}

type Thumbnail struct {
	Name      string
	MediaType string
	ModTime   time.Time
	Content   []byte

	alreadyExisted bool
}

func qualifiesForThumbnail(mimeType *string) bool {
	return mimeType != nil &&
		(strings.HasPrefix(*mimeType, "image/") ||
			strings.HasPrefix(*mimeType, "video/") ||
			*mimeType == "application/pdf") &&
		// these next two are mostly because I don't know how to convert
		// icons and animated gifs to thumbnails, or if it's even helpful
		*mimeType != "image/x-icon" &&
		*mimeType != imageGif
}

// AssetImage returns the bytes of the image at the given asset path (relative
// to the repo root). If obfuscate is true, then the image will be downsized and
// blurred.
func (tl *Timeline) AssetImage(_ context.Context, assetPath string, obfuscate bool) ([]byte, error) {
	size := 480
	if obfuscate {
		size = 120
	}
	imageBytes, err := loadAndEncodeImage(tl.FullPath(assetPath), nil, ".jpg", size, obfuscate)
	if err != nil {
		return nil, fmt.Errorf("opening source file %s: %w", assetPath, err)
	}
	return imageBytes, nil
}

// GeneratePreviewImage generates a higher quality preview image for the given item. The
// extension should be for a supported image format such as JPEG, PNG, WEBP, or AVIF.
// (JPEG or WEBP recommended.) As preview images are not cached, the image bytes are
// returned instead.
func (tl *Timeline) GeneratePreviewImage(ctx context.Context, itemRow ItemRow, ext string, obfuscate bool) ([]byte, error) {
	ext = strings.ToLower(ext)
	if ext != extJpeg && ext != extJpg && ext != extPng && ext != extWebp && ext != extAvif {
		return nil, fmt.Errorf("unsupported file extension/type: %s", ext)
	}

	var inputFilePath string
	var inputBuf []byte
	if itemRow.DataFile != nil {
		inputFilePath = filepath.Join(tl.repoDir, filepath.FromSlash(*itemRow.DataFile))
	} else if itemRow.DataID != nil {
		tl.dbMu.RLock()
		err := tl.db.QueryRowContext(ctx,
			`SELECT content FROM item_data WHERE id=? LIMIT 1`, *itemRow.DataID).Scan(&inputBuf)
		tl.dbMu.RUnlock()
		if err != nil {
			return nil, fmt.Errorf("loading content from database: %w", err)
		}
	}

	imageBytes, err := loadAndEncodeImage(inputFilePath, inputBuf, ext, maxPreviewImageDimension, obfuscate)
	if err != nil {
		return nil, fmt.Errorf("opening source file from item %d: %s: %w", itemRow.ID, inputFilePath, err)
	}

	return imageBytes, nil
}

func loadAndEncodeImage(inputFilePath string, inputBuf []byte, desiredExtension string, maxDimension int, obfuscate bool) ([]byte, error) {
	inputImage, err := loadImageVips(inputFilePath, inputBuf)
	if err != nil {
		return nil, fmt.Errorf("loading image: %w", err)
	}
	defer inputImage.Close()

	if err := scaleDownImage(inputImage, maxDimension); err != nil {
		return nil, fmt.Errorf("resizing image to within %d: %w", maxDimension, err)
	}

	if obfuscate {
		// how much blur is needed depends on the size of the image; I have found that the square root
		// of the max dimension, fine-tuned by a coefficient is pretty good: for thumbnails of 120px,
		// this ends up being about 8-10; for larger preview images of 1400px, we get more like 30, which
		// is helpful for obscuruing sensitive features like faces (higher sigma = more blur)
		sigma := math.Sqrt(float64(maxDimension) * .9) //nolint:mnd
		if err := inputImage.GaussianBlur(sigma); err != nil {
			return nil, fmt.Errorf("applying guassian blur to image for obfuscation: %w", err)
		}
	}

	// apparently Windows does not support 10-bit color depth!?
	bitDepth := 10
	if runtime.GOOS == "windows" {
		bitDepth = 8
	}

	var imageBytes []byte
	switch desiredExtension {
	case extJpg, extJpeg:
		ep := vips.NewJpegExportParams()
		ep.StripMetadata = true // (note: this strips rotation info, which is needed if rotation is not applied manually)
		ep.Quality = 50
		ep.Interlace = true
		ep.SubsampleMode = vips.VipsForeignSubsampleAuto
		ep.TrellisQuant = true
		ep.QuantTable = 3
		imageBytes, _, err = inputImage.ExportJpeg(ep)
	case extPng:
		ep := vips.NewPngExportParams()
		ep.StripMetadata = true // (note: this strips rotation info, which is needed if rotation is not applied manually)
		ep.Quality = 50
		ep.Interlace = true
		imageBytes, _, err = inputImage.ExportPng(ep)
	case extWebp:
		ep := vips.NewWebpExportParams()
		ep.StripMetadata = true // (note: this strips rotation info, which is needed if rotation is not applied manually)
		ep.Quality = 50
		imageBytes, _, err = inputImage.ExportWebp(ep)
	case extAvif:
		ep := vips.NewAvifExportParams()
		ep.StripMetadata = true // (note: this strips rotation info, which is needed if rotation is not applied manually)
		ep.Quality = 65
		ep.Effort = 1
		ep.Bitdepth = bitDepth
		imageBytes, _, err = inputImage.ExportAvif(ep)
	}
	if err != nil {
		// don't attempt a fallback if obfuscation is enabled, so we don't leak the unobfuscated image
		if obfuscate {
			return nil, fmt.Errorf("unable to encode obfuscated preview image: %w -- use thumbhash instead", err)
		}

		// I have seen "VipsJpeg: Corrupt JPEG data: N extraneous bytes before marker 0xdb" for some N,
		// even though my computer can show the image just fine. Not sure how to fix this, other than
		// configuring vips to continue on error (I think -- I know it fixed some errors)
		Log.Error("could not encode preview image, falling back to original image",
			zap.String("filename", inputFilePath),
			zap.String("ext", desiredExtension),
			zap.Error(err))

		// I guess just try returning the full image as-is and hope the browser can handle it - TODO: maybe try a std lib solution, even if slower
		if inputBuf != nil {
			return inputBuf, nil
		}
		return os.ReadFile(inputFilePath)
	}

	return imageBytes, nil
}

func thumbnailType(inputDataType string, onlyImage bool) string {
	if strings.HasPrefix(inputDataType, "image/") || inputDataType == "application/pdf" {
		return ImageAVIF
	}
	if strings.HasPrefix(inputDataType, "video/") {
		if onlyImage {
			return ImageWebP // webp supports animation (avif does too, apparently, but I can't figure it out with ffmpeg)
		}
		return VideoWebM
	}
	return ""
}

func float32ToByte(f float32) []byte {
	var buf [4]byte
	binary.BigEndian.PutUint32(buf[:], math.Float32bits(f))
	return buf[:]
}

// loadImageVips loads an image for vips to work with from either a file path or
// a buffer of bytes directly; precisely one must be non-nil.
func loadImageVips(inputFilePath string, inputBytes []byte) (*vips.ImageRef, error) {
	if inputFilePath != "" && inputBytes != nil {
		panic("load image with vips: input cannot be both a filename and a buffer")
	}
	if inputFilePath == "" && inputBytes == nil {
		panic("load image with vips: input must be either a filename or a buffer")
	}

	importParams := vips.NewImportParams()

	// I have seen "VipsJpeg: Corrupt JPEG data: N extraneous bytes before marker 0xdb" for some N,
	// even though my computer can show the image just fine. We can ignore these errors, apparently,
	// and I have found that it works ¯\_(ツ)_/¯
	importParams.FailOnError.Set(false)

	// if rotation info is encoded into EXIF metadata, this can orient the image properly for us
	// (or we can do a separate call to AutoRotate)
	importParams.AutoRotate.Set(true)

	if inputFilePath != "" {
		return vips.LoadImageFromFile(inputFilePath, importParams)
	}
	return vips.LoadImageFromBuffer(inputBytes, importParams)
}

// scaleDownImage resizes the image to fit within the maxDimension
// if either side is larger than maxDimension; otherwise it does
// nothing to the image.
func scaleDownImage(inputImage *vips.ImageRef, maxDimension int) error {
	meta := inputImage.Metadata()

	// if image is already within constraints, no-op
	if meta.Width < maxDimension && meta.Height < maxDimension {
		return nil
	}

	var scale float64
	if meta.Width > meta.Height {
		scale = float64(maxDimension) / float64(meta.Width)
	} else {
		scale = float64(maxDimension) / float64(meta.Height)
	}

	err := inputImage.Resize(scale, vips.KernelAuto) // Nearest is fast, but Auto looks slightly better
	if err != nil {
		return fmt.Errorf("scaling image: %w", err)
	}

	// if AutoRotate was not set when loading the image, you could call AutoRotate
	// here to apply rotation info before EXIF metadata is stripped
	// if err := inputImage.AutoRotate(); err != nil {
	// 	return fmt.Errorf("rotating image: %v", err)
	// }

	return nil
}

// Maximum X and Y dimensions for generated images.
const (
	// larger than ordinary thumbnails because we generate embeddings with
	// them; higher size = more detail = better embeddings, in theory
	maxThumbnailDimension = 720

	// video thumbnails (animated webp) can be rather large, so quality
	// is less important to us
	maxVideoThumbnailDimension = 140

	// preview images are like full-screen images, so they can be a bit
	// bigger to preserve quality
	maxPreviewImageDimension = 1400
)
