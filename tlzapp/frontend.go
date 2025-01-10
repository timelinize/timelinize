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

package tlzapp

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/Masterminds/sprig/v3"
	"github.com/timelinize/timelinize/datasources/media"
	"github.com/timelinize/timelinize/timeline"
)

// serveFrontend serves resources to the UI in this URL format:
// / repo / {repoID} / data|assets|thumbnail|image|transcode|motion-photo|dl / {dataFilePath_or_thumbnailItemID.jpg_or_itemID}
func (s server) serveFrontend(w http.ResponseWriter, r *http.Request) error {
	if strings.HasPrefix(r.URL.Path, "/repo/") {
		// serve timeline item data files, assets, thumbnails, and more
		return s.handleRepoResource(w, r)
	}

	// otherwise, serve static site

	buf := bufPool.Get().(*bytes.Buffer)
	buf.Reset()
	defer bufPool.Put(buf)

	// shouldBuf determines whether to execute templates on this response,
	// since generally we will not want to execute for images or CSS, etc.
	shouldBuf := func(_ int, header http.Header) bool {
		ct := header.Get("Content-Type")
		for _, mt := range []string{
			"text/html",
			"text/plain",
			"text/markdown",
		} {
			if strings.Contains(ct, mt) {
				return true
			}
		}
		return false
	}

	rec := newResponseRecorder(w, buf, shouldBuf)

	// single-page application file server 😏
	// if any other page is being loaded, always give the SPA which will then load the actual page
	if !strings.HasPrefix(r.URL.Path, "/pages/") &&
		!strings.HasPrefix(r.URL.Path, "/resources/") {
		r.URL.Path = "/"
	}

	s.staticFiles.ServeHTTP(rec, r)
	if !rec.Buffered() {
		return nil
	}

	if err := executeTemplate(rec, r, s.app); err != nil {
		return err
	}

	rec.Header().Set("Content-Length", strconv.Itoa(buf.Len()))
	rec.Header().Del("Accept-Ranges") // we don't know ranges for dynamically-created content
	rec.Header().Del("Last-Modified") // useless for dynamic content since it's always changing

	// we don't know a way to quickly generate etag for dynamic content,
	// and weak etags still cause browsers to rely on it even after a
	// refresh, so disable them until we find a better way to do this
	rec.Header().Del("Etag")

	return rec.WriteResponse()
}

func (s server) handleRepoResource(w http.ResponseWriter, r *http.Request) error {
	const minParts, maxParts = 5, 6
	parts := strings.SplitN(r.URL.Path, "/", maxParts)
	if len(parts) < minParts {
		return Error{
			Err:        errors.New("insufficient path"),
			HTTPStatus: http.StatusBadRequest,
			Log:        "parsing repo resource from URI path",
			Message:    "Invalid resource path.",
		}
	}
	repoID := parts[2]
	resourceType := parts[3]

	tl, err := getOpenTimeline(repoID)
	if err != nil {
		return Error{
			Err:        err,
			HTTPStatus: http.StatusBadRequest,
			Log:        "getting open timeline by ID",
			Message:    "Unable to get open timeline with ID in URI.",
		}
	}

	switch resourceType {
	case timeline.DataFolderName:
		// data file; serve statically from timeline
		return s.serveDataFile(w, r, tl, strings.Join(parts[3:], "/"))

	case timeline.AssetsFolderName:
		// asset file (such as profile picture); serve statically from timeline
		tl.fileServer.ServeHTTP(w, r)
		return nil

	case "thumbnail":
		// item thumbnail; serve from cache or generate if needed
		return s.serveThumbnail(w, r, tl)

	case "image":
		// preview image; generate on-the-fly
		return s.servePreviewImage(w, r, tl, parts)

	case "transcode":
		// stream video data file in a format that can be played by the browser
		dataFile := strings.Join(parts[4:], "/")
		inputPath := tl.FullPath(dataFile)
		_, obfuscate := s.app.ObfuscationMode(tl.Timeline)
		return s.transcodeVideo(r.Context(), w, inputPath, nil, obfuscate)

	case "motion-photo":
		// given the data path of a photo, stream its motion photo in a format playable by the browser
		return s.motionPhoto(w, r, tl, parts)

	case "dl":
		// download the item as a file
		return s.downloadItem(w, r, tl, parts)

	default:
		return Error{
			Err:        errors.New("URI must be accessing data, asset such as profile picture, thumbnail, preview image, transcode, motion-photo, or dl"),
			HTTPStatus: http.StatusBadRequest,
			Log:        "determining requested resource",
			Message:    "Invalid URI; unable to determine requested resource.",
		}
	}
}

func (s server) serveDataFile(w http.ResponseWriter, r *http.Request, tl openedTimeline, dataFile string) error {
	// but first, help out the MIME sniffer since we often know the correct data type
	// and are a little smarter than Go's built-in logic
	results, err := tl.Search(r.Context(), timeline.ItemSearchParams{
		DataFile:    []string{dataFile},
		Astructured: true,
		Limit:       1,
	})
	if err != nil {
		return err
	}
	if len(results.Items) < 1 {
		return Error{
			Err:        fmt.Errorf("no item found with data file: %s", dataFile),
			HTTPStatus: http.StatusNotFound,
			Log:        "finding item in database",
			Message:    "Data file not found in database.",
		}
	}
	if results.Items[0].DataType != nil {
		_, obfuscate := s.app.ObfuscationMode(tl.Timeline)
		if obfuscate && strings.HasPrefix(*results.Items[0].DataType, "video/") {
			return s.transcodeVideo(r.Context(), w, tl.FullPath(dataFile), nil, obfuscate)
		}
		w.Header().Set("Content-Type", *results.Items[0].DataType)
	}

	tl.fileServer.ServeHTTP(w, r)
	return nil
}

func (s server) servePreviewImage(w http.ResponseWriter, r *http.Request, tl openedTimeline, parts []string) error {
	filename := parts[4]
	ext := path.Ext(filename)
	if !strings.EqualFold(ext, ".jpg") &&
		!strings.EqualFold(ext, ".jpe") &&
		!strings.EqualFold(ext, ".jpeg") &&
		!strings.EqualFold(ext, ".png") &&
		!strings.EqualFold(ext, ".avif") &&
		!strings.EqualFold(ext, ".webp") {
		return fmt.Errorf("unsupported file extension: %s - only JPEG, PNG, AVIF, and WEBP formats are supported", ext)
	}

	itemIDStr := strings.TrimSuffix(filename, ext)
	itemID, err := strconv.ParseInt(itemIDStr, 10, 64)
	if err != nil || itemID < 0 {
		return fmt.Errorf("invalid item ID: %s", itemIDStr)
	}

	results, err := tl.Search(r.Context(), timeline.ItemSearchParams{
		Repo:  tl.ID().String(),
		RowID: []int64{itemID},
	})
	if err != nil {
		return err
	}
	if len(results.Items) == 0 {
		return fmt.Errorf("no item found with ID %d", itemID)
	}
	if len(results.Items) > 1 {
		return fmt.Errorf("somehow, %d items were found having ID %d", len(results.Items), itemID)
	}
	itemRow := results.Items[0].ItemRow
	if itemRow.DataFile == nil {
		return fmt.Errorf("item %d does not have a data file recorded, so no preview image is possible", itemID)
	}
	if itemRow.DataType != nil {
		switch *itemRow.DataType {
		case "image/gif", "image/x-icon":
			r.URL.Path = "/" + path.Join("repo", tl.ID().String(), *itemRow.DataFile)
			return s.serveDataFile(w, r, tl, *itemRow.DataFile)
		}
		if !strings.HasPrefix(*itemRow.DataType, "image/") && *itemRow.DataType != "application/pdf" {
			return fmt.Errorf("media type of item %d does not support preview image: %s", itemID, *itemRow.DataType)
		}
	}

	hash := hex.EncodeToString(itemRow.DataHash)
	if strings.Trim(r.Header.Get("If-None-Match"), "\"") == hash {
		w.WriteHeader(http.StatusNotModified)
		return nil
	}

	imageBytes, err := tl.GeneratePreviewImage(r.Context(), itemRow, ext)
	if err != nil {
		return err
	}
	w.Header().Set("Etag", `"`+hash+`"`)

	modTime := itemRow.Stored
	if itemRow.Modified != nil {
		modTime = *itemRow.Modified
	}

	http.ServeContent(w, r, filename, modTime, bytes.NewReader(imageBytes))

	return nil
}

func (s server) serveThumbnail(w http.ResponseWriter, r *http.Request, tl openedTimeline) error {
	var itemDataID int64
	itemDataIDStr := r.FormValue("data_id")
	if itemDataIDStr != "" {
		var err error
		itemDataID, err = strconv.ParseInt(itemDataIDStr, 10, 64)
		if err != nil || itemDataID < 0 {
			return fmt.Errorf("invalid item data ID: %s", itemDataIDStr)
		}
	}

	// determine thumbnail type; we have to be smart about this because sweet, special
	// Safari sends "video/*" for img tags (granted, at a lower q-factor/weight, but still...)
	accept, err := parseAccept(r.Header.Get("Accept"))
	if err != nil {
		return Error{
			Err:        err,
			HTTPStatus: http.StatusBadRequest,
			Log:        "parsing Accept header",
			Message:    "invalid syntax for Accept header",
		}
	}

	dataType := r.FormValue("data_type")

	// figure out what type of thumbnail to serve, taking into account
	// both server and client preferences; by default (if client has no
	// preference), server prefers an image thumbnail if it's an image,
	// or a video thumbnail if it's a video; but we can also honor
	// client preference (for example, videos can have animated image
	// thumbnails)
	ourPref := []string{"image/*", "video/*"}
	if strings.HasPrefix(dataType, "video/") {
		ourPref[0], ourPref[1] = ourPref[1], ourPref[0]
	}
	thumbType := timeline.ImageAVIF
	if clientPref := accept.preference(ourPref...); clientPref == "video/*" {
		thumbType = timeline.VideoWebM
	}

	// sigh, precious Safari and its <video><source> tag...
	if len(accept) == 1 && accept[0].mimeType == "*/*" {
		if r.Header.Get("Sec-Fetch-Dest") == "video" {
			thumbType = timeline.VideoWebM
		}
	}

	thumb, err := tl.Thumbnail(r.Context(), itemDataID, r.FormValue("data_file"), dataType, thumbType)
	if err != nil {
		return fmt.Errorf("unable to provide thumbnail: %w", err)
	}

	if thumb.MediaType != "" {
		w.Header().Set("Content-Type", thumb.MediaType)
	}

	thumbReader := bytes.NewReader(thumb.Content)
	_, obfuscate := s.app.ObfuscationMode(tl.Timeline)
	if obfuscate {
		if strings.HasPrefix(thumbType, "image/") {
			return Error{
				Err:        errors.New("obfuscation mode enabled: image thumbnail can just be thumbhash"),
				HTTPStatus: http.StatusNoContent,
			}
		} else if strings.HasPrefix(thumbType, "video/") {
			return s.transcodeVideo(r.Context(), w, "", thumbReader, obfuscate)
		}
	}

	http.ServeContent(w, r, thumb.Name, thumb.ModTime, bytes.NewReader(thumb.Content))
	return nil
}

func (s server) motionPhoto(w http.ResponseWriter, r *http.Request, tl openedTimeline, parts []string) error {
	itemIDStr := parts[4]

	// if the client gave us a related motion item, we can be confident we can use that,
	// and we don't need to query the DB.
	// if the client only gave us the image's data file, we have to query the DB to know
	// for sure whether there's a related motion item (we can't distinguish between the
	// client not having related items, and there actually not being a related motion item).
	// the only really good way to know whether the image data file has an embedded motion
	// video is to try extracting it. (EXIF can hint but it can also lie)

	videoDataFile := r.FormValue("hint")    // data file of the related motion item -- confidently the motion picture
	imgDataFile := r.FormValue("data_file") // item's data file -- may or may not have a motion picture embedded in it

	// if client wasn't able to give us both file paths, we have to query the DB because we'll need
	// to verify that no related motion photo exists, and also get the image data file so we can
	// try extracting from it if no related motion item exists
	if videoDataFile == "" || imgDataFile == "" {
		itemID, err := strconv.ParseInt(itemIDStr, 10, 64)
		if err != nil {
			return Error{
				Err:        err,
				HTTPStatus: http.StatusBadRequest,
				Message:    "Invalid item ID",
			}
		}

		// TODO: see if we can enhance our Search API to find a specific kind of relation
		results, err := tl.Search(r.Context(), timeline.ItemSearchParams{
			RowID:   []int64{itemID},
			Related: 1,
			Limit:   1,
		})
		if err != nil {
			return err
		}
		if len(results.Items) == 0 {
			return Error{
				Err:        fmt.Errorf("no search results for item with ID %d", itemID),
				HTTPStatus: http.StatusNotFound,
				Message:    "Item not found",
			}
		}

		if results.Items[0].DataFile != nil {
			// we might still use the original image file if there's no related motion item
			imgDataFile = *results.Items[0].DataFile
		}
		for _, rel := range results.Items[0].Related {
			if rel.Label == media.RelMotionPhoto.Label && rel.ToItem != nil && rel.ToItem.DataFile != nil {
				// great, we found a related motion item!
				videoDataFile = *rel.ToItem.DataFile
			}
		}
	}

	// if we found/have a separate data file as the motion photo, make its full path now
	var inputFile string
	if videoDataFile != "" {
		inputFile = tl.FullPath(videoDataFile)
	}

	// no sidecar motion pic, see if it's embedded in the photo file
	if videoDataFile == "" {
		imgFilePath := tl.FullPath(imgDataFile)

		// get the bytes of just the video from within the image file
		videoBytes, err := media.ExtractVideoFromMotionPic(nil, imgFilePath)
		if err != nil || len(videoBytes) == 0 {
			// no motion photo (or unable to get it), nothing to do
			w.WriteHeader(http.StatusNotFound)
			return nil
		}

		// we can't pipe MP4 directly to ffmpeg due to the MP4 container
		// requiring seeking :( so we create a temporary file instead
		tempInput, err := os.CreateTemp("", "timelinize_vidconvert_input_*.mp4")
		if err != nil {
			return fmt.Errorf("creating temporary input file: %w", err)
		}
		inputFile = tempInput.Name()
		defer os.Remove(inputFile)

		// write the extracted video to the input file, then be sure to close it
		_, err = tempInput.Write(videoBytes)
		tempInput.Close()
		if err != nil {
			return fmt.Errorf("writing to temporary input file: %w", err)
		}
	}

	// Motion videos are often H.265/HEVC encoded, this is true of HEIC/HEIF images and most .MP sidecar files at least;
	// and browsers need transcoding to play them (as of March 2024)
	imgExt := path.Ext(strings.ToLower(imgDataFile))
	isGoogleMP := path.Ext(strings.ToLower(videoDataFile)) == ".mp" ||
		path.Ext(strings.ToLower(strings.TrimSuffix(imgDataFile, imgExt))) == ".mp"
	_, obfuscate := s.app.ObfuscationMode(tl.Timeline)
	if videoDataFile == "" || imgExt == ".heif" || imgExt == ".heic" || isGoogleMP || obfuscate {
		return s.transcodeVideo(r.Context(), w, inputFile, nil, obfuscate)
	}

	r.URL.Path = "/" + path.Join("repo", tl.ID().String(), videoDataFile)
	tl.fileServer.ServeHTTP(w, r)
	return nil
}

func (s server) downloadItem(w http.ResponseWriter, r *http.Request, tl openedTimeline, parts []string) error {
	itemIDStr := parts[4]
	itemID, err := strconv.ParseInt(itemIDStr, 10, 64)
	if err != nil || itemID < 0 {
		return fmt.Errorf("invalid item ID: %s", itemIDStr)
	}

	results, err := tl.Search(r.Context(), timeline.ItemSearchParams{
		Repo:  tl.ID().String(),
		RowID: []int64{itemID},
	})
	if err != nil {
		return err
	}
	if len(results.Items) == 0 {
		return fmt.Errorf("no item found with ID %d", itemID)
	}
	if len(results.Items) > 1 {
		return fmt.Errorf("somehow, %d items were found having ID %d", len(results.Items), itemID)
	}
	itemRow := results.Items[0].ItemRow

	if itemRow.DataText != nil && itemRow.DataFile != nil {
		return Error{
			Err:        fmt.Errorf("ambiguous content: item row %d has both data_text and data_file", itemID),
			HTTPStatus: http.StatusConflict,
		}
	}

	// figure out the filename, modtime, and the content

	var filename string
	if itemRow.Filename != nil {
		filename = *itemRow.Filename
	} else {
		// no filename, so I guess we just name it by the item ID
		filename = fmt.Sprint("item", itemID)

		// if it has a class, maybe we can add some helpful hint
		if itemRow.Classification != nil {
			filename += fmt.Sprint("_", *itemRow.Classification)
		}

		// try to add a relevant file extension
		if itemRow.DataType != nil {
			// the MIME db can give us uncommon extensions (like .asc for
			// text/plain), so try our own mapping first
			mediaType, _, err := mime.ParseMediaType(*itemRow.DataType)
			if err != nil {
				// oh well, just make the best of it
				mediaType = *itemRow.DataType
			}
			switch mediaType {
			case "text/plain":
				filename += ".txt"
			default:
				exts, err := mime.ExtensionsByType(*itemRow.DataType)
				if err == nil && len(exts) > 0 {
					filename += exts[0]
				}
			}
		}
	}

	// TODO: or do we use the item's timestamp instead?
	modified := itemRow.Stored
	if itemRow.Modified != nil {
		modified = *itemRow.Modified
	}

	var content io.ReadSeeker
	switch {
	case itemRow.DataText != nil:
		content = bytes.NewReader([]byte(*itemRow.DataText))

	case itemRow.DataFile != nil:
		f, err := os.Open(tl.FullPath(*itemRow.DataFile))
		if err != nil {
			return err
		}
		defer f.Close()
		content = f

	case itemRow.Latitude != nil || itemRow.Longitude != nil || itemRow.Altitude != nil:
		type geometry struct {
			Type        string     `json:"type"`
			Coordinates []*float64 `json:"coordinates"`
		}
		type properties struct {
			RepoID                string          `json:"repo_id,omitempty"`
			ItemID                int64           `json:"item_id,omitempty"`
			DataSourceName        *string         `json:"data_source_name,omitempty"`
			Stored                time.Time       `json:"stored,omitempty"`
			Filename              *string         `json:"filename,omitempty"`
			Timestamp             *time.Time      `json:"timestamp,omitempty"`
			Timespan              *time.Time      `json:"timespan,omitempty"`
			Timeframe             *time.Time      `json:"timeframe,omitempty"`
			CoordinateSystem      *string         `json:"coordinate_system,omitempty"`
			CoordinateUncertainty *float64        `json:"coordinate_uncertainty,omitempty"`
			Metadata              json.RawMessage `json:"metadata,omitempty"`
		}
		type geoJSON struct {
			Type       string     `json:"type"`
			Geometry   geometry   `json:"geometry"`
			Properties properties `json:"properties"`
		}

		data := geoJSON{
			Type: "Feature",
			Geometry: geometry{
				Type:        "Point",
				Coordinates: []*float64{itemRow.Longitude, itemRow.Latitude},
			},
			Properties: properties{
				RepoID:                tl.ID().String(),
				ItemID:                itemRow.ID,
				DataSourceName:        itemRow.DataSourceName,
				Stored:                itemRow.Stored,
				Filename:              itemRow.Filename,
				Timestamp:             itemRow.Timestamp,
				Timespan:              itemRow.Timespan,
				Timeframe:             itemRow.Timeframe,
				CoordinateSystem:      itemRow.CoordinateSystem,
				CoordinateUncertainty: itemRow.CoordinateUncertainty,
				Metadata:              itemRow.Metadata,
			},
		}
		if itemRow.Altitude != nil {
			data.Geometry.Coordinates = append(data.Geometry.Coordinates, itemRow.Altitude)
		}

		jsonBytes, err := json.MarshalIndent(data, "", "\t")
		if err != nil {
			return err
		}
		content = bytes.NewReader(jsonBytes)
		ct := "application/json"
		itemRow.DataType = &ct
		filename = strings.TrimSuffix(filename, path.Ext(filename)) + ".geojson"
	}

	if itemRow.DataType != nil {
		w.Header().Set("Content-Type", *itemRow.DataType)
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename=%q`, filename))

	http.ServeContent(w, r, filename, modified, content)
	return nil
}

func (s server) transcodeVideo(ctx context.Context, w http.ResponseWriter, inputVideoFilePath string, inputVideoStream io.Reader, obfuscate bool) error {
	// TODO: potentially use Accept header to make this dynamic
	const format = "video/webm"
	w.Header().Set("Content-Type", format)
	// TODO: Safari sends "Range: bytes=0-1" before it requests the whole video, I think it expects a Content-Range header. can we use ServeContent to make it work on Safari?

	if err := s.app.Transcode(ctx, inputVideoFilePath, inputVideoStream, format, w, obfuscate); err != nil {
		return fmt.Errorf("video transcode error: %#w", err)
	}

	return nil
}

func executeTemplate(rr *responseRecorder, r *http.Request, app *App) error {
	var data any
	if dataFunc, ok := templateData[r.URL.Path]; ok && dataFunc != nil {
		var err error
		data, err = dataFunc(r)
		if err != nil {
			return err
		}
	}
	if err := executeTemplateInBuffer(r.URL.Path, rr.Buffer(), data, app); err != nil {
		// TODO: make it possible for templates to return errors with a specific status code
		return err
	}

	return nil
}

func executeTemplateInBuffer(tplName string, buf *bytes.Buffer, data any, app *App) error {
	tpl := newTemplate(tplName, app)

	_, err := tpl.Parse(buf.String())
	if err != nil {
		return err
	}

	buf.Reset() // reuse buffer for output

	return tpl.Execute(buf, data)
}

func newTemplate(tplName string, app *App) *template.Template {
	tpl := template.New(tplName).Option("missingkey=zero")

	// add sprig library
	tpl.Funcs(sprig.FuncMap())

	// add our own library
	tpl.Funcs(template.FuncMap{
		"N":       tplFuncIntIter,
		"now":     time.Now,
		"include": app.tplFuncInclude,
	})

	return tpl
}

func tplFuncIntIter(n int) []struct{} {
	return make([]struct{}, n)
}

// tplFuncInclude returns the contents of filename relative to the site root
// and renders it in place. Note that included files are NOT escaped, so you
// should only include trusted files. If it is not trusted, be sure to use
// escaping functions in your template.
func (a *App) tplFuncInclude(filename string) (string, error) {
	bodyBuf := bufPool.Get().(*bytes.Buffer)
	bodyBuf.Reset()
	defer bufPool.Put(bodyBuf)

	err := readFileToBuffer(http.FS(a.server.frontend), filename, bodyBuf)
	if err != nil {
		return "", err
	}

	err = executeTemplateInBuffer(filename, bodyBuf, nil, a)
	if err != nil {
		return "", err
	}

	return bodyBuf.String(), nil
}

func readFileToBuffer(root http.FileSystem, filename string, bodyBuf *bytes.Buffer) error {
	if root == nil {
		return errors.New("root file system not specified")
	}

	file, err := root.Open(filename)
	if err != nil {
		return err
	}
	defer file.Close()

	_, err = io.Copy(bodyBuf, file)
	return err
}

var bufPool = sync.Pool{
	New: func() any {
		return new(bytes.Buffer)
	},
}
