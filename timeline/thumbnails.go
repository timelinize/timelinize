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
	"errors"
	"fmt"
	"image"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	// TODO: I would like to just use "github.com/strukturag/libheif/go/heif"
	// as our AVIF decoder, since that is what we used earlier during development
	// but now I just get a distorted green mess:
	// https://x.com/mholt6/status/1864894439061381393

	"github.com/davidbyttow/govips/v2/vips"
	"github.com/galdor/go-thumbhash"
	_ "github.com/gen2brain/avif" // register AVIF image decoder
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
}

func (tj thumbnailJob) Run(job *Job, checkpoint []byte) error {
	// TODO: Resume from checkpoint

	if tj.Tasks == nil {
		// TODO: create thumbnails for all qualifying items that need one;
		// iterate DB in chunks of 100 or 1000, maybe?

		return nil
	}

	var wg sync.WaitGroup
	goroutineThrottle := make(chan struct{}, 20)

	for _, task := range tj.Tasks {
		wg.Add(1)

		task.tl = job.tl
		// TODO: Should we just use the cpuIntensiveThrottle for these (and move those throttles out to here)?
		goroutineThrottle <- struct{}{}

		// TODO: It'd be nice if we could batch our DB operations, like we did before the jobs refactor
		go func(job *Job, task thumbnailTask) {
			defer wg.Done()

			_, err := task.thumbnailAndThumbhash(job.Context(), task.DataID, task.DataFile)
			if err != nil {
				// don't terminate the job if there's an error
				// TODO: but we should probably note somewhere in the job's
				// row in the DB that this error happened... maybe?
				job.logger.Error("thumbnail/thumbhash generation failed",
					zap.Int64("data_id", task.DataID),
					zap.String("data_file", task.DataFile),
					zap.String("data_type", task.DataType),
					zap.String("thumbnail_type", task.ThumbType),
					zap.Error(err))
			}

			if err := job.UpdateProgress(1); err != nil {
				job.logger.Error("updating job progress", zap.Error(err))
			}

			<-goroutineThrottle
		}(job, task)
	}

	wg.Wait()

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
}

// thumbnailAndThumbhash returns the thumbnail, even if this returns an error because thumbhash
// generation fails, the thumbnail is still usable in yhat case.
func (task thumbnailTask) thumbnailAndThumbhash(ctx context.Context, dataID int64, dataFile string) (Thumbnail, error) {
	if dataID > 0 && dataFile != "" {
		// is the content in the DB or a file?? can't be both
		panic("ambiguous thumbnail task given both dataID and dataFile")
	}
	thumb, err := task.generateAndStoreThumbnail(ctx, dataID, dataFile)
	if err != nil {
		return Thumbnail{}, fmt.Errorf("generating/storing thumbnail: %w", err)
	}
	if strings.HasPrefix(thumb.MediaType, "image/") {
		if err = task.generateAndStoreThumbhash(ctx, dataID, dataFile, thumb.Content); err != nil {
			return thumb, fmt.Errorf("generating/storing thumbhash: %w", err)
		}
	}
	return thumb, nil
}

func (task thumbnailTask) generateAndStoreThumbnail(ctx context.Context, dataID int64, dataFile string) (Thumbnail, error) {
	var inputBuf []byte
	var inputFilename string

	task.DataType = strings.ToLower(task.DataType)
	task.ThumbType = strings.ToLower(task.ThumbType)

	if dataFile != "" {
		inputFilename = task.tl.FullPath(dataFile)
	} else if dataID > 0 {
		task.tl.dbMu.RLock()
		err := task.tl.db.QueryRowContext(ctx,
			`SELECT content FROM item_data WHERE id=? LIMIT 1`, dataID).Scan(&inputBuf)
		task.tl.dbMu.RUnlock()
		if err != nil {
			return Thumbnail{}, fmt.Errorf("querying item data content: %w", err)
		}
	}

	if !qualifiesForThumbnail(&task.DataType) {
		return Thumbnail{}, fmt.Errorf("media type does not support thumbnailing: %s (item_data_id=%d data_file='%s')", task.DataType, dataID, dataFile)
	}

	thumbnail, mimeType, err := task.generateThumbnail(ctx, inputFilename, inputBuf)
	if err != nil {
		return Thumbnail{}, fmt.Errorf("generating thumbnail for content: %w (item_data_id=%d data_file='%s')", err, dataID, inputFilename)
	}

	dataFileToInsert, dataIDToInsert := &dataFile, &dataID
	if dataFile == "" {
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
		WHERE (data_file=? OR (data_file IS NULL AND ? IS NULL))
			AND (item_data_id=? OR (item_data_id IS NULL AND ? IS NULL))`,
		dataFileToInsert, dataIDToInsert, mimeType, thumbnail,
		now.Unix(), mimeType, thumbnail,
		dataFileToInsert, dataFileToInsert, dataIDToInsert, dataIDToInsert)
	if err != nil {
		return Thumbnail{}, fmt.Errorf("saving thumbnail to database: %w (item_data_id=%d data_file='%s')", err, dataID, dataFile)
	}

	return Thumbnail{
		Name:      fakeThumbnailFilename(dataID, dataFile, mimeType),
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

	var thumbnail []byte
	var mimeType string

	switch {
	case strings.HasPrefix(task.DataType, "image/"):
		inputImage, err := loadImageVips(inputFilename, inputBuf)
		if err != nil {
			return nil, "", fmt.Errorf("opening source file: %w", err)
		}
		defer inputImage.Close()

		// scale down to a thumbnail size
		if err := resizeImage(inputImage, maxThumbnailDimension); err != nil {
			return nil, "", fmt.Errorf("resizing image: %w", err)
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
			ep.Bitdepth = 10
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
			return nil, "", fmt.Errorf("TODO: not implemented: support for video items stored in DB")
		}

		var cmd *exec.Cmd
		if strings.HasPrefix(task.ThumbType, "image/") {
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
		} else if strings.HasPrefix(task.ThumbType, "video/") {
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

				// when piping out (no output filename), we have to explicitly specify
				// the format since ffmpeg can't deduce it from a file extension
				"-f", "webm",

				// pipe to stdout
				"-",
			)
			mimeType = VideoWebM
		} else {
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

func (task thumbnailTask) generateAndStoreThumbhash(ctx context.Context, dataID int64, dataFile string, thumb []byte) error {
	thash, err := task.generateThumbhash(thumb)
	if err != nil {
		return err
	}

	task.tl.dbMu.Lock()
	defer task.tl.dbMu.Unlock()

	if dataID != 0 {
		_, err = task.tl.db.ExecContext(ctx, `UPDATE items SET thumb_hash=? WHERE data_id=?`, thash, dataID)
	} else {
		_, err = task.tl.db.ExecContext(ctx, `UPDATE items SET thumb_hash=? WHERE data_file=?`, thash, dataFile)
	}
	return err
}

func (thumbnailTask) generateThumbhash(thumb []byte) ([]byte, error) {
	// throttle expensive operation
	cpuIntensiveThrottle <- struct{}{}
	defer func() { <-cpuIntensiveThrottle }()

	img, format, err := image.Decode(bytes.NewReader(thumb))
	if err != nil {
		return nil, fmt.Errorf("decoding thumbnail (format=%s) for thumbhash computation failed: %w", format, err)
	}

	// thumbhash can recover the _approximate_ aspect ratio, but not
	// exactly, which makes sizing the image difficult on the UI because
	// replacing the thumbhash image with the real image would result in
	// a content jump because the images are different sizes! so we
	// prepend the thumbhash with the exact aspect ratio... and program
	// the frontend to split it... hey, it works...
	aspectRatio := float32(img.Bounds().Dx()) / float32(img.Bounds().Dy())

	return append(float32ToByte(aspectRatio), thumbhash.EncodeImage(img)...), nil
}

// Thumbnail returns a thumbnail for either the given itemDataID or the dataFile, along with
// media type. If a thumbnail does not yet exist, one is generated and stored for future use.
func (tl *Timeline) Thumbnail(ctx context.Context, itemDataID int64, dataFile, dataType, thumbType string) (Thumbnail, error) {
	var mimeType string
	var modTimeUnix int64
	var thumbnail []byte

	dataType = strings.ToLower(dataType)
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
		itemDataIDToQuery = &itemDataID
	}

	// first try the fast path: get the thumbnail if it exists
	tl.thumbsMu.RLock()
	err := tl.thumbs.QueryRowContext(ctx,
		`SELECT generated, mime_type, content
			FROM thumbnails
			WHERE (item_data_id=? OR data_file=?) AND mime_type=?
			LIMIT 1`,
		itemDataIDToQuery, dataFileToQuery, thumbType).Scan(&modTimeUnix, &mimeType, &thumbnail)
	tl.thumbsMu.RUnlock()

	// found existing thumbnail!
	if err == nil {
		return Thumbnail{
			Name:      fakeThumbnailFilename(itemDataID, dataFile, mimeType),
			MediaType: mimeType,
			ModTime:   time.Unix(modTimeUnix, 0),
			Content:   thumbnail,
		}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		// error other than "no thumbnail found"
		return Thumbnail{}, err
	}

	// slower path: no thumbnail found (of the desired media type), so generate one

	task := thumbnailTask{
		tl:        tl,
		DataType:  dataType,
		ThumbType: thumbType,
	}

	thumb, err := task.thumbnailAndThumbhash(ctx, itemDataID, dataFile)
	if err != nil {
		return Thumbnail{}, err
	}

	return thumb, nil
}

type Thumbnail struct {
	Name      string
	MediaType string
	ModTime   time.Time
	Content   []byte
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

// GeneratePreviewImage generates a higher quality preview image for the given item. The
// extension should be for a supported image format such as JPEG, PNG, WEBP, or AVIF.
// (JPEG or WEBP recommended.) As preview images are not cached, the image bytes are
// returned instead.
func (tl *Timeline) GeneratePreviewImage(ctx context.Context, itemRow ItemRow, ext string) ([]byte, error) {
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

	inputImage, err := loadImageVips(inputFilePath, inputBuf)
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
		ep.Quality = 65
		ep.Effort = 1
		ep.Bitdepth = 10
		imageBytes, _, err = inputImage.ExportAvif(ep)
	}
	if err != nil {
		// I have seen "VipsJpeg: Corrupt JPEG data: N extraneous bytes before marker 0xdb" for some N,
		// even though my computer can show the image just fine. Not sure how to fix this, other than
		// configuring vips to continue on error (I think -- I know it fixed some errors)
		Log.Error("could not encode preview image, falling back to original image",
			zap.Int64("item_id", itemRow.ID),
			zap.String("filename", inputFilePath),
			zap.String("ext", ext),
			zap.Error(err))

		// I guess just try returning the full image as-is and hope the browser
		// can handle it
		// TODO: maybe try a std lib solution, even if slower

		if inputBuf != nil {
			return inputBuf, nil
		}
		return os.ReadFile(inputFilePath)
	}

	return imageBytes, nil
}

func thumbnailType(inputDataType string, onlyImage bool) string {
	if strings.HasPrefix(inputDataType, "image/") {
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
