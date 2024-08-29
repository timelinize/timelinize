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
	"encoding/binary"
	"fmt"
	"image"
	"math"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"

	"github.com/davidbyttow/govips/v2/vips"
	"github.com/galdor/go-thumbhash"
	_ "github.com/strukturag/libheif/go/heif" // register AVIF image decoder
	"go.uber.org/zap"
)

func init() {
	vips.LoggingSettings(nil, vips.LogLevelError)
	vips.Startup(nil) // Shutdown() is called in a sigtrap for clean shutdowns

	const cpuFraction = 3
	numThreads := runtime.NumCPU() / cpuFraction
	if numThreads == 0 {
		numThreads = 1
	}
	for range numThreads {
		go thumbnailWorker()
	}
}

// thumbnailTask represents a thumbnail that needs to be generated.
type thumbnailTask struct {
	ctx          context.Context
	tl           *Timeline     // the timeline of the item
	itemID       int64         // the item's row ID
	dataFile     string        // path to the data file (the data_file field of the item row), if known
	dataType     string        // media type (the data_type field of the item row), if known
	outputFormat ThumbnailType // whether to make an image or video thumbnail
	err          chan<- error  // the return value is sent here; never closed, to allow for reuse in batches
}

// TODO: Ideally, we'd also compute a thumbhash when we generate the thumbnail and store it
// with the item row.

// Channels in which to give tasks to the thumbnail generator workers.
// Foreground tasks are prioritized and should be used if a thumbnail
// is needed immediately, like when the user is loading a page that
// displays the thumbnail.
var thumbnailBackground, thumbnailForeground = make(chan thumbnailTask), make(chan thumbnailTask)

// thumbnailWorker loops forever and processes thumbnail tasks as they come in.
// It prioritizes foreground tasks over background tasks.
func thumbnailWorker() {
	for {
		select {
		// prioritize foreground thumbnails first (i.e. user is loading a page that needs this thumbnail right now)
		case highPriority := <-thumbnailForeground:
			generateThumbnail(highPriority)
		default:
			// no high-priority jobs, so accept whatever job comes in first
			select {
			case highPriority := <-thumbnailForeground:
				generateThumbnail(highPriority)
			case lowPriority := <-thumbnailBackground:
				generateThumbnail(lowPriority)
			}
		}
	}
}

/*
	TODO: A short preview/thumbnail of a video could be made with ffmpeg like this:

	$ ffmpeg -i input.mp4 -vf scale=320:-1 -quality 20 -ss 0 -t 3 ~/Downloads/output.webp

	This is an animated webp image of quite low quality, of the first 3 seconds of the video.
	Max width 320, scale height to preserve aspect ratio. The quality and size params can be
	increased but it will go slower. I think '-vcodec libwebp' is implied with the file ext.


	TODO: To generate a video preview (webm):

	$ ffmpeg -i input.mp4 -vf scale=480:-1 -vcodec libvpx -acodec libvorbis -b:v 1M -ss 0 -t 3 ~/Downloads/output.webm

	Specifying the vcodec and acodec makes it faster. Tune scale and bitrate (-b:v) to
	satisfaction.
*/

func generateThumbnail(task thumbnailTask) {
	if task.ctx == nil {
		task.ctx = context.Background()
	}

	// if the client already knows the item's data type and file path, we can skip a
	// DB query entirely when provided it to us; granted, we skip some media type
	// checks, but that's probably OK (the client does some basic vetting and
	// generation will fail anyways, nbd)
	if task.dataFile == "" || task.dataType == "" {
		results, err := task.tl.Search(task.ctx, ItemSearchParams{
			Repo:  task.tl.ID().String(),
			RowID: []int64{task.itemID},
		})
		if err != nil {
			task.err <- err
			return
		}
		if len(results.Items) == 0 {
			task.err <- fmt.Errorf("no item found with ID %d", task.itemID)
			return
		}
		if len(results.Items) > 1 {
			task.err <- fmt.Errorf("somehow, %d items were found having ID %d", len(results.Items), task.itemID)
			return
		}
		itemRow := results.Items[0]
		if itemRow.DataFile == nil {
			task.err <- fmt.Errorf("item %d does not have a data file recorded, so no thumbnail is possible", task.itemID)
			return
		}
		task.dataFile = *results.Items[0].DataFile
		task.dataType = *results.Items[0].DataType
	}

	if !qualifiesForThumbnail(&task.dataType) {
		task.err <- fmt.Errorf("media type of item %d does not support thumbnailing: %s", task.itemID, task.dataType)
		return
	}

	// form the item's data file's full path so we can load it, and the output filepath so we can save it
	inputFilename := filepath.Join(task.tl.repoDir, filepath.FromSlash(task.dataFile))
	outputFilename := task.tl.ThumbnailPath(task.itemID, task.outputFormat)

	// make sure the parent directory for the output file exists
	if err := os.MkdirAll(filepath.Dir(outputFilename), 0700); err != nil {
		task.err <- fmt.Errorf("item %d: making thumbnail directory tree for %s: %w", task.itemID, outputFilename, err)
		return
	}

	switch {
	case strings.HasPrefix(task.dataType, "image/"):
		inputImage, err := loadImageFromFile(inputFilename)
		if err != nil {
			task.err <- fmt.Errorf("opening source file from item %d: %s: %w", task.itemID, inputFilename, err)
			return
		}
		defer inputImage.Close()

		// scale down to a thumbnail size
		if err := resizeImage(inputImage, maxThumbnailDimension); err != nil {
			task.err <- fmt.Errorf("item %d: image %s: %w", task.itemID, inputFilename, err)
			return
		}

		// encode the resized image as the proper output format
		var imageBytes []byte
		switch strings.ToLower(thumbnailImgExt) {
		case extJpg, ".jpe", extJpeg:
			ep := vips.NewJpegExportParams()
			ep.StripMetadata = true // (note: this strips rotation info, which is needed if rotation is not applied manually)
			ep.Quality = 40
			ep.Interlace = true
			ep.SubsampleMode = vips.VipsForeignSubsampleAuto
			ep.TrellisQuant = true
			ep.QuantTable = 3
			imageBytes, _, err = inputImage.ExportJpeg(ep)
		case extAvif:
			// fun fact: AVIF supports animation, but I can't get ffmpeg to generate it faster than 0.0016x speed
			// (vips is fast enough for stills though, as long as we tune down the parameters sufficiently)
			ep := vips.NewAvifExportParams()
			ep.StripMetadata = true // (note: this strips rotation info, which is needed if rotation is not applied manually)
			ep.Quality = 30
			ep.Bitdepth = 10
			ep.Effort = 1
			imageBytes, _, err = inputImage.ExportAvif(ep)
		default:
			panic("unsupported thumbnail image type: " + thumbnailImgExt)
		}
		if err != nil {
			task.err <- fmt.Errorf("item %d: encoding thumbnail image of %s: %w", task.itemID, inputFilename, err)
			return
		}

		err = os.WriteFile(outputFilename, imageBytes, 0600)
		if err != nil {
			task.err <- fmt.Errorf("item %d: writing thumbnail image to %s: %w", task.itemID, outputFilename, err)
			return
		}

	case strings.HasPrefix(task.dataType, "video/"):
		var cmd *exec.Cmd
		if task.outputFormat == ImageThumbnail {
			cmd = exec.Command("ffmpeg",
				"-ss", "00:00:01.000",
				"-i", inputFilename,
				"-vf", fmt.Sprintf("scale=%d:-1", maxThumbnailDimension),
				"-vframes", "1",
				outputFilename,
			)
		} else if task.outputFormat == VideoThumbnail {
			cmd = exec.Command("ffmpeg",
				"-i", inputFilename,

				// important to scale down the video for fast encoding
				"-vf", "scale='min(480,iw)':-1",

				// libvpx is much faster than default encoder
				"-vcodec", "libvpx",
				"-acodec", "libvorbis",

				// bitrate, important quality determination
				"-b:v", "1M",

				// include only the first few seconds of video
				// TODO: Should this go before -i?
				"-ss", "0",
				"-t", "3",

				// we are already running concurrently, so limit to just 1 CPU thread
				"-threads", "1",

				outputFilename,
			)
		}
		cmd.Stderr = os.Stderr

		if err := cmd.Run(); err != nil {
			task.err <- fmt.Errorf("generating video thumbnail: %w", err)
		}

	default:
		task.err <- fmt.Errorf("not sure how to generate thumbnail for '%s' data type", task.dataType)
	}

	task.err <- nil
}

func thumbnailDir(cacheDir, repoID string) string {
	return path.Join(cacheDir, "thumbnails", repoID)
}

// ThumbnailPath returns the full path to the thumbnail for the item with the given ID.
// imageOrVideo should be either "image" or "video" to generate that type of thumbnail.
// An image item may only have an image thumbnail. A video item may have an image or a
// video thumbnail. The default type is an image thumbnail.
//
// TODO: what should we do about data files that are shared across multiple items? they're stored by item row ID, so maybe we are generating the same thumbnail multiple times? hmm...
func ThumbnailPath(cacheDir, repoID string, itemID int64, imageOrVideo ThumbnailType) string {
	ext := thumbnailImgExt
	if imageOrVideo == VideoThumbnail {
		ext = thumbnailVidExt
	}

	// subdivide into folders to avoid any one folder having too many files
	topFolder := strconv.FormatInt(itemID/100000+1, 10)
	subFolder := strconv.FormatInt(itemID/1000+1, 10)
	filename := strconv.FormatInt(itemID, 10) + ext

	return path.Join(thumbnailDir(cacheDir, repoID), topFolder, subFolder, filename)
}

// ThumbnailPath returns the path to the thumbnail for the item in this timeline with the given ID.
func (tl *Timeline) ThumbnailPath(itemID int64, imageOrVideo ThumbnailType) string {
	return ThumbnailPath(tl.cacheDir, tl.ID().String(), itemID, imageOrVideo)
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

// GenerateThumbnail generates a thumbnail for the given item. If the data file path is known, pass it in
// to avoid a database query. The thumbnail is saved directly to the configured cache directory. The task
// is given low priority and is suitable for background operations. Use GenerateThumbnailNow for
// high-priority thumbnails.
//
// This method does not block, but you can still get the result and wait until the thumbnail is ready if
// needed. If you need the error AND/OR you are waiting for the thumbnail to be available, make an errChan
// and be sure to read from it properly. If you do not need to block, errChan can be nil and any error
// will be logged instead.
func (tl *Timeline) GenerateThumbnail(ctx context.Context, itemID int64,
	dataFileIfKnown, dataTypeIfKnown string, outputFormat ThumbnailType, errChan chan error) {
	if errChan == nil {
		errChan = make(chan error)
		go func() {
			if err := <-errChan; err != nil {
				Log.Error("generating thumbnail failed",
					zap.Int64("item_id", itemID),
					zap.String("data_file", dataFileIfKnown),
					zap.String("output_type", string(outputFormat)),
					zap.Error(err))
			}
		}()
	}
	tl.generateThumbnail(ctx, itemID, dataFileIfKnown, dataTypeIfKnown, outputFormat, false, errChan)
}

// GenerateThumbnailNow is the same as GenerateThumbnail, except the task is given higher
// priority, which is useful when you need the thumbnail ASAP (e.g. rendering a page).
func (tl *Timeline) GenerateThumbnailNow(ctx context.Context, itemID int64,
	dataFileIfKnown, dataTypeIfKnown string, outputFormat ThumbnailType) error {
	err := make(chan error)
	tl.generateThumbnail(ctx, itemID, dataFileIfKnown, dataTypeIfKnown, outputFormat, true, err)
	return <-err
}

func (tl *Timeline) generateThumbnail(ctx context.Context, itemID int64, dataFileIfKnown, dataTypeIfKnown string, outputFormat ThumbnailType, urgent bool, err chan<- error) {
	if ctx == nil {
		ctx = tl.ctx
	}
	task := thumbnailTask{
		ctx:          ctx,
		tl:           tl,
		itemID:       itemID,
		dataFile:     dataFileIfKnown,
		dataType:     dataTypeIfKnown,
		outputFormat: outputFormat,
		err:          err,
	}
	if urgent {
		thumbnailForeground <- task
	} else {
		thumbnailBackground <- task
	}
}

// GeneratePreviewImage generates a higher quality preview image for the given item. The
// extension should be for a supported image format such as JPEG, PNG, WEBP, or AVIF.
// (JPEG or WEBP recommended.) As preview images are not cached, the image bytes are
// returned instead.
func (tl *Timeline) GeneratePreviewImage(itemRow ItemRow, ext string) ([]byte, error) {
	ext = strings.ToLower(ext)
	if ext != extJpeg && ext != extJpg && ext != extPng && ext != extWebp && ext != extAvif {
		return nil, fmt.Errorf("unsupported file extension/type: %s", ext)
	}

	inputFilePath := filepath.Join(tl.repoDir, filepath.FromSlash(*itemRow.DataFile))

	inputImage, err := loadImageFromFile(inputFilePath)
	if err != nil {
		return nil, fmt.Errorf("opening source file from item %d: %s: %w", itemRow.ID, inputFilePath, err)
	}
	defer inputImage.Close()

	if err := resizeImage(inputImage, maxPreviewImageDimension); err != nil {
		return nil, fmt.Errorf("item %d: image %s: %w", itemRow.ID, inputFilePath, err)
	}

	var imageBytes []byte
	switch ext {
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
		ep.Quality = 40
		ep.Effort = 1
		ep.Bitdepth = 10
		imageBytes, _, err = inputImage.ExportAvif(ep)
	}
	if err != nil {
		// I have seen "VipsJpeg: Corrupt JPEG data: N extraneous bytes before marker 0xdb" for some N,
		// even though my computer can show the image just fine. Not sure how to fix this.
		Log.Error("could not encode preview image, falling back to original image",
			zap.Int64("item_id", itemRow.ID),
			zap.String("filename", inputFilePath),
			zap.String("ext", ext),
			zap.Error(err))

		// we can try using built-in Go library? TODO: maybe try pure Go solution, at least for common file types like JPEG
		// we cam also just return the image as-is and hope the browser can handle it?
		return os.ReadFile(inputFilePath)
	}

	return imageBytes, nil
}

// regenerateAllThumbnails generates thumbnails for all qualifying items in the database.
func (tl *Timeline) regenerateAllThumbnails() error {
	tl.dbMu.RLock()
	rows, err := tl.db.QueryContext(tl.ctx, `SELECT id, data_type, data_file FROM items WHERE data_file IS NOT NULL`)
	if err != nil {
		return fmt.Errorf("querying items: %w", err)
	}

	type thumbInfo struct {
		rowID    int64
		dataType string
	}
	thumbnailsNeeded := make(map[string]thumbInfo) // map key is item ID; useful for deduplicating if shared by other items

	for rows.Next() {
		var rowID int64
		var dataType, dataFile *string

		err := rows.Scan(&rowID, &dataType, &dataFile)
		if err != nil {
			defer tl.dbMu.RUnlock()
			defer rows.Close()
			return fmt.Errorf("scanning item row for thumbnails: %w", err)
		}
		if dataFile != nil && dataType != nil && qualifiesForThumbnail(dataType) {
			thumbnailsNeeded[*dataFile] = thumbInfo{rowID, *dataType}
		}
	}
	rows.Close()
	tl.dbMu.RUnlock()
	if err = rows.Err(); err != nil {
		return fmt.Errorf("iterating rows: %w", err)
	}

	ctx := context.Background()
	for dataFile, info := range thumbnailsNeeded {
		thumbType := ImageThumbnail
		if strings.HasPrefix(info.dataType, "video/") {
			thumbType = VideoThumbnail
		}
		tl.GenerateThumbnail(ctx, info.rowID, dataFile, info.dataType, thumbType, nil)
	}

	return nil
}

// generateThumbnailsForImportedItems generates thumbnails for qualifying items
// that were a part of the import associated with this processor. It should be
// run after the import completes.
func (p *processor) generateThumbnailsForImportedItems() {
	p.tl.dbMu.RLock()
	rows, err := p.tl.db.QueryContext(p.tl.ctx, `SELECT id, data_type, data_file FROM items WHERE import_id=? AND data_file IS NOT NULL`, p.impRow.id)
	if err != nil {
		p.log.Error("unable to generate thumbnails from this import",
			zap.Int64("import_id", p.impRow.id),
			zap.Error(err))
		return
	}

	type thumbInfo struct {
		rowID    int64
		dataType string
	}
	thumbnailsNeeded := make(map[string]thumbInfo) // map key is item ID; useful for deduplicating if shared by other items

	for rows.Next() {
		var rowID int64
		var dataType, dataFile *string

		err := rows.Scan(&rowID, &dataType, &dataFile)
		if err != nil {
			p.log.Error("unable to scan row to generate thumbnails from this import",
				zap.Int64("import_id", p.impRow.id),
				zap.Error(err))
			defer p.tl.dbMu.RUnlock()
			defer rows.Close()
			return
		}
		if dataFile != nil && dataType != nil && qualifiesForThumbnail(dataType) {
			thumbnailsNeeded[*dataFile] = thumbInfo{rowID, *dataType}
		}
	}
	rows.Close()
	p.tl.dbMu.RUnlock()
	if err = rows.Err(); err != nil {
		p.log.Error("iterating rows for generating thumbnails failed",
			zap.Int64("import_id", p.impRow.id),
			zap.Error(err))
		return
	}

	p.log.Info("generating thumbnails for imported items", zap.Int("count", len(thumbnailsNeeded)))

	// now that we've closed our DB lock, we can take our
	// time generating thumbnails in the background
	errs := make(chan error)
	done := make(chan struct{})
	go func() {
		for range thumbnailsNeeded {
			// don't wait for a context cancellation here because
			// we always need to drain the channel in order to
			// unblock the parent goroutine
			if err := <-errs; err != nil {
				p.log.Error("unable to generate thumbnail",
					zap.Int64("import_id", p.impRow.id),
					zap.Error(err))
			}
		}
		close(done)
	}()
	for dataFile, info := range thumbnailsNeeded {
		format := ImageThumbnail
		if strings.HasPrefix(info.dataType, "video/") {
			format = VideoThumbnail
		}
		p.tl.GenerateThumbnail(p.tl.ctx, info.rowID, dataFile, info.dataType, format, errs)
	}
	<-done

	// from the thumbnails, we can easily generate thumbhashes
	p.generateThumbhashesForItemsThatNeedOne()
}

func (p *processor) generateThumbhashesForItemsThatNeedOne() {
	p.log.Info("generating thumbhashes for imported items", zap.Int64("import_id", p.impRow.id))
	defer p.log.Info("finished thumbhash generation routine", zap.Int64("import_id", p.impRow.id))

	p.tl.dbMu.RLock()
	rows, err := p.tl.db.QueryContext(p.tl.ctx,
		`SELECT id, data_type FROM items WHERE import_id=? AND data_file IS NOT NULL AND thumb_hash IS NULL`,
		p.impRow.id)
	if err != nil {
		p.log.Error("unable to generate thumbhashes for this import",
			zap.Int64("import_id", p.impRow.id),
			zap.Error(err))
		return
	}

	var thumbhashesNeeded []int64
	for rows.Next() {
		var rowID int64
		var dataType *string
		err := rows.Scan(&rowID, &dataType)
		if err != nil {
			p.log.Error("unable to scan row to generate thumbhashes from this import",
				zap.Int64("import_id", p.impRow.id),
				zap.Error(err))
			defer p.tl.dbMu.RUnlock()
			defer rows.Close()
			return
		}
		// TODO: we can probably generate thumbhashes for videos (and even PDFs; whatever we generate thumbnails for) but we need to use ffmpeg to get a single frame of a video, or a screenshot of a PDF, etc.
		if dataType != nil && !strings.HasPrefix(*dataType, "image/") && *dataType != imageGif {
			continue
		}
		thumbhashesNeeded = append(thumbhashesNeeded, rowID)
	}
	rows.Close()
	p.tl.dbMu.RUnlock()
	if err = rows.Err(); err != nil {
		p.log.Error("iterating rows for generating thumbhashes failed",
			zap.Int64("import_id", p.impRow.id),
			zap.Error(err))
		return
	}

	const numWorkers = 5
	var wg sync.WaitGroup

	thumbhashesPerWorker := len(thumbhashesNeeded)/numWorkers + 1

	for w := range numWorkers {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()

			// compute thumbhashes from thumbnails and store them in the DB in batches
			batch := make(map[int64][]byte) // map of row ID to thumbhash
			for i := thumbhashesPerWorker * w; i < thumbhashesPerWorker*(w+1) && i < len(thumbhashesNeeded); i++ {
				rowID := thumbhashesNeeded[i]

				thumbnailPath := p.tl.ThumbnailPath(rowID, ImageThumbnail)
				file, err := os.Open(thumbnailPath)
				if err != nil {
					p.log.Error("opening thumbnail to compute thumbhash failed",
						zap.Int64("import_id", p.impRow.id),
						zap.Int64("item_id", rowID),
						zap.String("thumbnail_path", thumbnailPath),
						zap.Error(err))
					continue
				}
				img, _, err := image.Decode(file)
				if err != nil {
					p.log.Error("decoding thumbnail for thumbhash computation failed",
						zap.Int64("import_id", p.impRow.id),
						zap.Int64("item_id", rowID),
						zap.String("thumbnail_path", thumbnailPath),
						zap.Error(err))
					continue
				}

				// thumbhash can recover the _approximate_ aspect ratio, but not
				// exactly, which makes sizing the image difficult on the UI because
				// replacing the thumbhash image with the real image would result in
				// a content jump because the images are different sizes! so we
				// prepend the thumbhash with the exact aspect ratio...
				aspectRatio := float32(img.Bounds().Dx()) / float32(img.Bounds().Dy())
				aspectRatioPre := float32ToByte(aspectRatio)

				batch[rowID] = append(aspectRatioPre, thumbhash.EncodeImage(img)...)

				// if batch is full, store into DB
				const batchSize = 100
				if len(batch) >= batchSize {
					err := p.thumbhashBatch(batch)
					if err != nil {
						p.log.Error("storing thumbhashes failed",
							zap.Int64("import_id", p.impRow.id),
							zap.Error(err))
						return
					}
					batch = make(map[int64][]byte)
				}
			}

			// store what remains in the batch
			if len(batch) > 0 {
				err := p.thumbhashBatch(batch)
				if err != nil {
					p.log.Error("storing remaining thumbhashes failed",
						zap.Int64("import_id", p.impRow.id),
						zap.Error(err))
					return
				}
			}
		}(w)
	}

	wg.Wait()
}

func float32ToByte(f float32) []byte {
	var buf [4]byte
	binary.BigEndian.PutUint32(buf[:], math.Float32bits(f))
	return buf[:]
}

func (p *processor) thumbhashBatch(batch map[int64][]byte) error {
	p.log.Info("storing thumbhashes for batch of imported items",
		zap.Int64("import_id", p.impRow.id),
		zap.Int("batch_size", len(batch)))

	p.tl.dbMu.Lock()
	defer p.tl.dbMu.Unlock()

	tx, err := p.tl.db.Begin()
	if err != nil {
		return fmt.Errorf("beginning transaction: %w", err)
	}
	defer tx.Rollback()

	for rowID, thumbhash := range batch {
		_, err = p.tl.db.Exec(`UPDATE items SET thumb_hash=? WHERE id=?`, thumbhash, rowID)
		if err != nil {
			return err
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing transaction: %w", err)
	}

	return nil
}

func loadImageFromFile(inputFilePath string) (*vips.ImageRef, error) {
	importParams := vips.NewImportParams()

	// I have seen "VipsJpeg: Corrupt JPEG data: N extraneous bytes before marker 0xdb" for some N,
	// even though my computer can show the image just fine. We can ignore these errors, apparently,
	// and I have found that it works ¯\_(ツ)_/¯
	importParams.FailOnError.Set(false)

	// if rotation info is encoded into EXIF metadata, this can orient the image properly for us
	// (or we can do a separate call to AutoRotate)
	importParams.AutoRotate.Set(true)

	return vips.LoadImageFromFile(inputFilePath, importParams)
}

func resizeImage(inputImage *vips.ImageRef, maxDimension int) error {
	meta := inputImage.Metadata()

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

// The file extensions designate the type (encoding) of the thumbnails.
const (
	thumbnailImgExt = extAvif
	thumbnailVidExt = ".webm"
)

// ThumbnailType describes a type of thumbnail (either image or video).
type ThumbnailType string

const (
	ImageThumbnail ThumbnailType = "image"
	VideoThumbnail ThumbnailType = "video"
)

// Maximum X and Y dimensions for generated images.
const (
	maxThumbnailDimension    = 720
	maxPreviewImageDimension = 1400
)
