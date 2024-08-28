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

// Package googlephotos implements a data source for Google Photos albums and
// exports, and also via its API documented at https://developers.google.com/photos/.
package googlephotos

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/mholt/archiver/v4"
	"github.com/timelinize/timelinize/timeline"
	"go.uber.org/zap"
)

var (
	oauth2 = timeline.OAuth2{
		ProviderID: "google",
		Scopes:     []string{"https://www.googleapis.com/auth/photoslibrary.readonly"},
	}
	rateLimit = timeline.RateLimit{
		RequestsPerHour: 10000 / 24, // https://developers.google.com/photos/library/guides/api-limits-quotas
		BurstSize:       3,
	}
)

const dataSourceName = "google_photos"

func init() {
	err := timeline.RegisterDataSource(timeline.DataSource{
		Name:  dataSourceName,
		Title: "Google Photos",
		Icon:  "googlephotos.svg",
		// NewClient: func(ctx context.Context, acc timeline.Account, _ any) (timeline.Client, error) {
		// 	httpClient, err := acc.NewHTTPClient(ctx)
		// 	if err != nil {
		// 		return nil, err
		// 	}
		// 	return &Client{
		// 		HTTPClient: httpClient,
		// 		userID:     acc.User.UserID,
		// 		checkpoint: checkpointInfo{mu: new(sync.Mutex)},
		// 	}, nil
		// },
		NewOptions:      func() any { return Options{} },
		NewFileImporter: func() timeline.FileImporter { return new(FileImporter) },
		NewAPIImporter:  func() timeline.APIImporter { return new(APIImporter) },
	})
	if err != nil {
		timeline.Log.Fatal("registering data source", zap.Error(err))
	}
}

// // Client interacts with the Google Photos
// // API. It requires an OAuth2-authorized
// // HTTP client in order to work properly.
// type Client struct {
// 	HTTPClient           *http.Client
// 	IncludeArchivedMedia bool

// 	userID     string
// 	checkpoint checkpointInfo
// }

type FileImporter struct {
	filename       string
	truncatedNames map[string]int
}

func (FileImporter) Recognize(ctx context.Context, filenames []string) (timeline.Recognition, error) {
	for _, filename := range filenames {
		// prefer a Google Takeout archive with Google Photos data inside it
		fsys, err := archiver.FileSystem(ctx, filename)
		if err != nil {
			return timeline.Recognition{}, err
		}
		// TODO: this is also very slow
		_, err = archiver.TopDirStat(fsys, googlePhotosPath)
		if err == nil {
			return timeline.Recognition{Confidence: 1}, nil
		}

		// TODO: using fs.WalkDir is too inefficient with the current implementation I have for archive files
		// (consider type switching on fsys, and if a dir, then use WalkDir; for archives use Extract and just
		// iterate the entries in an arbitrary order, since the order/structure doesn't matter to us)

		// switch f := fsys.(type) {
		// case archiver.DirFS:
		// 	dir, err := os.Open(filename)
		// 	if err != nil {
		// 		return false, err
		// 	}
		// 	entries, err := dir.ReadDir(10000)
		// 	if err != nil {
		// 		return false, err
		// 	}
		// 	for _, entry := range entries {
		// 		// album exports do not have subfolders AFAIK
		// 		// (TODO: is there any harm in supporting subfolders, if we support all the file types?)
		// 		if entry.IsDir() {
		// 			return false, nil
		// 		}

		// 		ext := strings.ToLower(filepath.Ext(entry.Name()))
		// 		if _, ok := recognizedExts[ext]; !ok {
		// 			return false, nil
		// 		}
		// 	}
		// }

		if afs, ok := fsys.(archiver.ArchiveFS); ok {
			afs.Context = ctx
		}

		// TODO: this is too slow, even with context timeout for some reason.
		// // alternatively, if the input is a single directory that contains only
		// // recognized media, then we might assume this is a Google Photos album export
		// errNotSupported := fmt.Errorf("file type not supported")
		// switch fsys.(type) {
		// case archiver.DirFS, archiver.ArchiveFS:
		// 	err := fs.WalkDir(fsys, ".", func(path string, d fs.DirEntry, err error) error {
		// 		if err != nil {
		// 			return err
		// 		}
		// 		// ignore the actual folder (or archive file), since we'd traverse only inside it
		// 		if path == "." {
		// 			return nil
		// 		}
		// 		// album exports do not have subfolders AFAIK
		// 		// (TODO: is there any harm in supporting subfolders, if we support all the file types?)
		// 		if d.IsDir() {
		// 			return errNotSupported
		// 		}
		// 		// file type must be recognized (relying on extension is hopefully good enough)
		// 		ext := strings.ToLower(filepath.Ext(path))
		// 		if _, ok := recognizedExts[ext]; !ok {
		// 			return fmt.Errorf("%s: %w", path, errNotSupported)
		// 		}
		// 		return nil
		// 	})
		// 	if errors.Is(err, errNotSupported) {
		// 		return false, nil
		// 	}
		// 	if err != nil {
		// 		return false, err
		// 	}
		// 	return true, nil
		// }
	}

	return timeline.Recognition{}, nil
}

func (fimp *FileImporter) FileImport(ctx context.Context, filenames []string, itemChan chan<- *timeline.Graph, opt timeline.ListingOptions) error {

	for _, filename := range filenames {
		fimp.filename = filename

		fsys, err := archiver.FileSystem(ctx, filename)
		if err != nil {
			return err
		}
		_, err = archiver.TopDirStat(fsys, googlePhotosPath)
		if err == nil {
			return fimp.listFromTakeoutArchive(ctx, itemChan, opt, fsys)
		}

		err = fimp.listFromAlbumFolder(ctx, itemChan, opt, fsys)
		if err != nil {
			return err
		}
	}
	return nil
}

type APIImporter struct {
	itemChan   chan<- *timeline.Graph
	httpClient *http.Client
	checkpoint *checkpoint
	listOpt    timeline.ListingOptions
	dsOpt      Options
}

func (APIImporter) Authenticate(ctx context.Context, acc timeline.Account, dsOpt any) error {
	return acc.AuthorizeOAuth2(ctx, oauth2)
}

func (aimp *APIImporter) APIImport(ctx context.Context, acc timeline.Account, itemChan chan<- *timeline.Graph, opt timeline.ListingOptions) error {
	var err error
	aimp.httpClient, err = acc.NewHTTPClient(ctx, oauth2, rateLimit)
	if err != nil {
		return err
	}
	aimp.itemChan = itemChan
	aimp.listOpt = opt
	aimp.dsOpt = opt.DataSourceOptions.(Options)

	// TODO:
	// // load any previous checkpoint
	// aimp.checkpoint.load(opt.Checkpoint)

	// get items and collections
	errChan := make(chan error)
	chanCount := 1
	go func() {
		err := aimp.listItems(ctx)
		errChan <- err
	}()
	if aimp.dsOpt.Albums {
		chanCount++
		go func() {
			err := aimp.listCollections(ctx)
			errChan <- err
		}()
	}

	// read exactly chanCount error (or nil) values to ensure we
	// block precisely until the two listers are done
	var errs []string
	for i := 0; i < chanCount; i++ {
		err := <-errChan
		if err != nil {
			// TODO: use actual logger
			// log.Printf("[ERROR] %s/%s: a listing goroutine errored: %v", DataSourceID, c.userID, err)
			errs = append(errs, err.Error())
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("one or more errors: %s", strings.Join(errs, ", "))
	}

	return nil
}

func (aimp *APIImporter) listItems(ctx context.Context) error {
	aimp.checkpoint.Lock()
	pageToken := aimp.checkpoint.ItemsNextPage
	aimp.checkpoint.Unlock()

	for {
		select {
		case <-ctx.Done():
			return nil
		default:
			var err error
			pageToken, err = aimp.getItemsNextPage(pageToken)
			if err != nil {
				return fmt.Errorf("getting items on next page: %v", err)
			}
			if pageToken == "" {
				return nil
			}

			// TODO: check this with the race detector... also below
			aimp.checkpoint.Lock()
			aimp.checkpoint.ItemsNextPage = pageToken
			aimp.checkpoint.Unlock()
			aimp.itemChan <- &timeline.Graph{
				Checkpoint: aimp.checkpoint.checkpointInfo,
			}
		}
	}
}

func (aimp *APIImporter) getItemsNextPage(pageToken string) (string, error) {
	reqBody := listMediaItemsRequest{
		PageSize:  100,
		PageToken: pageToken,
	}
	if aimp.listOpt.Timeframe.Since != nil || aimp.listOpt.Timeframe.Until != nil {
		reqBody.Filters = &listMediaItemsFilter{
			DateFilter: listMediaItemsDateFilter{
				Ranges: []listMediaItemsFilterRange{dateRange(aimp.listOpt.Timeframe)},
			},
			IncludeArchivedMedia: aimp.dsOpt.IncludeArchivedMedia,
		}
	}

	page, err := aimp.pageOfMediaItems(reqBody)
	if err != nil {
		return "", fmt.Errorf("requesting next page: %v", err)
	}

	for _, m := range page.MediaItems {
		aimp.itemChan <- &timeline.Graph{Item: m.timelinerItem()}
	}

	return page.NextPageToken, nil
}

// listCollections lists media items by iterating each album. As
// of Jan. 2019, the Google Photos API does not allow searching
// media items with both an album ID and filters. Because this
// search is predicated on album ID, we cannot be constrained by
// a timeframe in this search.
//
// See https://developers.google.com/photos/library/reference/rest/v1/mediaItems/search.
func (aimp *APIImporter) listCollections(ctx context.Context) error {
	aimp.checkpoint.Lock()
	albumPageToken := aimp.checkpoint.AlbumsNextPage
	aimp.checkpoint.Unlock()

	for {
		select {
		case <-ctx.Done():
			return nil
		default:
			aimp.listOpt.Log.Debug("listing albums: next page", zap.String("page_token", albumPageToken))

			var err error
			albumPageToken, err = aimp.getAlbumsAndTheirItemsNextPage(albumPageToken)
			if err != nil {
				return err
			}
			if albumPageToken == "" {
				return nil
			}

			aimp.checkpoint.Lock()
			aimp.checkpoint.AlbumsNextPage = albumPageToken
			aimp.checkpoint.Unlock()
			aimp.itemChan <- &timeline.Graph{
				Checkpoint: aimp.checkpoint.checkpointInfo,
			}
		}
	}
}

func (aimp *APIImporter) getAlbumsAndTheirItemsNextPage(pageToken string) (string, error) {
	vals := url.Values{
		"pageToken": {pageToken},
		"pageSize":  {"50"},
	}

	var respBody listAlbums
	err := aimp.apiRequestWithRetry("GET", "/albums?"+vals.Encode(), nil, &respBody)
	if err != nil {
		return pageToken, err
	}

	for _, album := range respBody.Albums {
		aimp.listOpt.Log.Debug("listing items in album",
			zap.String("album_title", album.Title),
			zap.String("album_id", album.ID),
			zap.String("item_count", album.MediaItemsCount))

		err = aimp.getAlbumItems(album)
		if err != nil {
			return "", err
		}
	}

	return respBody.NextPageToken, nil
}

func (aimp *APIImporter) getAlbumItems(album gpAlbum) error {
	var albumItemsNextPage string
	var counter int

	coll := &timeline.Item{
		ID:             album.ID,
		Classification: timeline.ClassCollection,
		Content: timeline.ItemData{
			Data: timeline.StringData(album.Title),
		},
	}

	const pageSize = 100

	for {
		reqBody := listMediaItemsRequest{
			AlbumID:   album.ID,
			PageToken: albumItemsNextPage,
			PageSize:  pageSize,
		}

		aimp.listOpt.Log.Debug("getting next page of media items in album",
			zap.String("album_title", album.Title),
			zap.String("album_id", album.ID),
			zap.Int("page_size", pageSize),
			zap.String("page_token", albumItemsNextPage),
		)

		page, err := aimp.pageOfMediaItems(reqBody)
		if err != nil {
			return fmt.Errorf("listing album contents: %v", err)
		}

		// iterate each media item on this page of the album listing
		for _, it := range page.MediaItems {
			// since we cannot request items in an album and also filter
			// by timestamp, be sure to filter here; it means we still
			// have to iterate all items in all albums, but at least we
			// can just skip items that fall outside the timeframe...
			if !aimp.listOpt.Timeframe.Contains(it.MediaMetadata.CreationTime) {
				continue
			}

			// otherwise, add this item to the album
			ig := &timeline.Graph{Item: it.timelinerItem()}
			ig.ToItemWithValue(timeline.RelInCollection, coll, counter)

			counter++
		}

		if page.NextPageToken == "" {
			return nil
		}

		albumItemsNextPage = page.NextPageToken
	}
}

func (aimp *APIImporter) pageOfMediaItems(reqBody listMediaItemsRequest) (listMediaItems, error) {
	var respBody listMediaItems
	err := aimp.apiRequestWithRetry("POST", "/mediaItems:search", reqBody, &respBody)
	return respBody, err
}

func (aimp *APIImporter) apiRequestWithRetry(method, endpoint string, reqBodyData, respInto any) error {
	// do the request in a loop for controlled retries on error
	var err error
	const maxTries = 10
	for i := 0; i < maxTries; i++ {
		var resp *http.Response
		resp, err = aimp.apiRequest(method, endpoint, reqBodyData)
		if err != nil {
			// TODO: Proper logger
			// log.Printf("[ERROR] %s/%s: doing API request: >>> %v <<< - retrying... (attempt %d/%d)",
			// 	DataSourceID, aimp.userID, err, i+1, maxTries)
			// TODO: honor context cancellation
			time.Sleep(10 * time.Second)
			continue
		}

		if resp.StatusCode != http.StatusOK {
			bodyText, err2 := io.ReadAll(io.LimitReader(resp.Body, 1024*256))
			resp.Body.Close()

			if err2 == nil {
				err = fmt.Errorf("HTTP %d: %s: >>> %s <<<", resp.StatusCode, resp.Status, bodyText)
			} else {
				err = fmt.Errorf("HTTP %d: %s", resp.StatusCode, resp.Status)
			}

			// extra-long pause for rate limiting errors
			if resp.StatusCode == http.StatusTooManyRequests {
				// TODO: proper logger
				// log.Printf("[ERROR] %s/%s: rate limited: HTTP %d: %s: %s - retrying in 35 seconds... (attempt %d/%d)",
				// 	DataSourceID, aimp.userID, resp.StatusCode, resp.Status, bodyText, i+1, maxTries)
				// TODO: honor context cancellation
				time.Sleep(35 * time.Second)
				continue
			}

			// for any other error, wait a couple seconds and retry
			// TODO: proper logger
			// log.Printf("[ERROR] %s/%s: bad API response: %v - retrying... (attempt %d/%d)",
			// 	DataSourceID, aimp.userID, err, i+1, maxTries)
			// TODO: honor context cancellation
			time.Sleep(10 * time.Second)
			continue
		}

		// successful request; read the response body
		err = json.NewDecoder(resp.Body).Decode(&respInto)
		if err != nil {
			resp.Body.Close()
			err = fmt.Errorf("decoding JSON: %v", err)
			// TODO: proper logger
			// log.Printf("[ERROR] %s/%s: reading API response: %v - retrying... (attempt %d/%d)",
			// 	DataSourceID, aimp.userID, err, i+1, maxTries)
			// TODO: honor context cancellation
			time.Sleep(10 * time.Second)
			continue
		}

		// successful read; we're done here
		resp.Body.Close()
		break
	}

	return err
}

func (aimp *APIImporter) apiRequest(method, endpoint string, reqBodyData any) (*http.Response, error) {
	var reqBody io.Reader
	if reqBodyData != nil {
		reqBodyBytes, err := json.Marshal(reqBodyData)
		if err != nil {
			return nil, err
		}
		reqBody = bytes.NewReader(reqBodyBytes)
	}

	req, err := http.NewRequest(method, apiBase+endpoint, reqBody)
	if err != nil {
		return nil, err
	}
	if reqBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	return aimp.httpClient.Do(req)
}

func dateRange(timeframe timeline.Timeframe) listMediaItemsFilterRange {
	var start, end filterDate
	if timeframe.Since == nil {
		start = filterDate{
			Day:   1,
			Month: 1,
			Year:  1,
		}
	} else {
		since := timeframe.Since.Add(24 * time.Hour) // to account for day precision
		start = filterDate{
			Day:   since.Day(),
			Month: int(since.Month()),
			Year:  since.Year(),
		}
	}
	if timeframe.Until == nil {
		end = filterDate{
			Day:   31,
			Month: 12,
			Year:  9999,
		}
	} else {
		until := timeframe.Until.Add(-24 * time.Hour) // to account for day precision
		end = filterDate{
			Day:   until.Day(),
			Month: int(until.Month()),
			Year:  until.Year(),
		}
	}
	return listMediaItemsFilterRange{
		StartDate: start,
		EndDate:   end,
	}
}

type Options struct {
	// Get albums. This can add significant time to an import.
	Albums bool `json:"albums"`

	IncludeArchivedMedia bool `json:"include_archived_media"`
}

// Assuming checkpoints are short-lived (i.e. are resumed
// somewhat quickly, before the page tokens/cursors expire),
// we can just store the page tokens.
type checkpointInfo struct {
	ItemsNextPage  string
	AlbumsNextPage string
}

type checkpoint struct {
	checkpointInfo
	sync.Mutex
}

// // save records the checkpoint. It is NOT thread-safe,
// // so calls to this must be protected by a mutex.
// func (ch *checkpointInfo) save(ctx context.Context) {
// 	gobBytes, err := timeline.MarshalGob(ch)
// 	if err != nil {
// 		// TODO: proper logger
// 		// log.Printf("[ERROR] %s: encoding checkpoint: %v", DataSourceID, err)
// 	}
// 	timeline.Checkpoint(ctx, gobBytes)
// }

// // load decodes the checkpoint. It is NOT thread-safe,
// // so calls to this must be protected by a mutex.
// func (ch *checkpointInfo) load(checkpointGob []byte) {
// 	if len(checkpointGob) == 0 {
// 		return
// 	}
// 	err := timeline.UnmarshalGob(checkpointGob, ch)
// 	if err != nil {
// 		// TODO: proper logger
// 		// log.Printf("[ERROR] %s: decoding checkpoint: %v", DataSourceID, err)
// 	}
// }

// recognizedExts is only used for file recognition, as a fallback.
var recognizedExts = map[string]struct{}{
	".jpg":  {},
	".jpe":  {},
	".jpeg": {},
	".mp4":  {},
	".mov":  {},
	".mpeg": {},
	".avi":  {},
	".wmv":  {},
	".png":  {},
	".raw":  {},
	".dng":  {},
	".nef":  {},
	".tiff": {},
	".gif":  {},
	".heic": {},
	".webp": {},
	".divx": {},
	".m4v":  {},
	".3gp":  {},
	".3g2":  {},
	".m2t":  {},
	".m2ts": {},
	".mts":  {},
	".mkv":  {},
	".asf":  {},
}

const apiBase = "https://photoslibrary.googleapis.com/v1"
