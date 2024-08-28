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

// Package facebook implements the Facebook service by supporting account
// export files and also the Graph API: https://developers.facebook.com/docs/graph-api
package facebook

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/timelinize/timelinize/timeline"
	"go.uber.org/zap"
)

func init() {
	err := timeline.RegisterDataSource(timeline.DataSource{
		Name:            "facebook",
		Title:           "Facebook",
		Icon:            "facebook.svg",
		NewFileImporter: func() timeline.FileImporter { return new(Archive) },
		NewAPIImporter:  func() timeline.APIImporter { return new(Client) },
	})
	if err != nil {
		timeline.Log.Fatal("registering data source", zap.Error(err))
	}
}

var (
	oauth2 = timeline.OAuth2{
		ProviderID: "facebook",
		Scopes: []string{
			"public_profile",
			"user_posts",
			"user_photos",
			"user_videos",
		},
	}

	rateLimit = timeline.RateLimit{
		RequestsPerHour: 200,
		BurstSize:       3,
	}
)

// Client implements the timeline.Client interface.
type Client struct {
	logger     *zap.Logger
	httpClient *http.Client
	checkpoint *checkpoint
}

func (c *Client) Authenticate(ctx context.Context, acc timeline.Account, dsOpt any) error {
	return acc.AuthorizeOAuth2(ctx, oauth2)
}

func (c *Client) APIImport(ctx context.Context, account timeline.Account,
	itemChan chan<- *timeline.Graph, opt timeline.ListingOptions) error {

	httpClient, err := account.NewHTTPClient(ctx, oauth2, rateLimit)
	if err != nil {
		return err
	}
	c.httpClient = httpClient
	c.checkpoint = &checkpoint{
		checkpointInfo: opt.Checkpoint.(checkpointInfo),
	}
	c.logger = opt.Log

	errChan := make(chan error)

	// TODO: events, comments (if possible), ...
	go func() {
		err := c.getFeed(ctx, itemChan, opt.Timeframe)
		errChan <- err
	}()
	go func() {
		err := c.getCollections(ctx, itemChan, opt.Timeframe)
		errChan <- err
	}()

	// read exactly 2 errors (or nils) because we
	// started 2 goroutines to do things
	var errs []string
	for i := 0; i < 2; i++ {
		err := <-errChan
		if err != nil {
			errs = append(errs, err.Error())
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("one or more errors: %s", strings.Join(errs, ", "))
	}

	return nil
}

// // ListItems lists the items on the Facebook account.
// func (c *Client) ListItems(ctx context.Context, itemChan chan<- *timeline.ItemGraph, opt timeline.ListingOptions) error {
// 	defer close(itemChan)

// 	if opt.Filename != "" {
// 		return fmt.Errorf("importing from a file is not supported")
// 	}

// 	// load any previous checkpoint
// 	c.checkpoint.load(opt.Checkpoint)

// 	errChan := make(chan error)

// 	// TODO: events, comments (if possible), ...
// 	go func() {
// 		err := c.getFeed(ctx, itemChan, opt.Timeframe)
// 		errChan <- err
// 	}()
// 	go func() {
// 		err := c.getCollections(ctx, itemChan, opt.Timeframe)
// 		errChan <- err
// 	}()

// 	// read exactly 2 errors (or nils) because we
// 	// started 2 goroutines to do things
// 	var errs []string
// 	for i := 0; i < 2; i++ {
// 		err := <-errChan
// 		if err != nil {
// 			errs = append(errs, err.Error())
// 		}
// 	}
// 	if len(errs) > 0 {
// 		return fmt.Errorf("one or more errors: %s", strings.Join(errs, ", "))
// 	}

// 	return nil
// }

func (c *Client) getFeed(ctx context.Context, itemChan chan<- *timeline.Graph, timeframe timeline.Timeframe) error {
	c.checkpoint.Lock()
	nextPageURL := c.checkpoint.ItemsNextPage
	c.checkpoint.Unlock()

	var err error

	for {
		select {
		case <-ctx.Done():
			return nil
		default:
			nextPageURL, err = c.getFeedNextPage(itemChan, nextPageURL, timeframe)
			if err != nil {
				return err
			}
			if nextPageURL == nil {
				return nil
			}

			// TODO: check this with race detector (same as below)
			c.checkpoint.Lock()
			c.checkpoint.ItemsNextPage = nextPageURL
			c.checkpoint.Unlock()
			itemChan <- &timeline.Graph{
				Checkpoint: c.checkpoint.checkpointInfo,
			}
		}
	}
}

func (c *Client) getFeedNextPage(itemChan chan<- *timeline.Graph,
	nextPageURL *string, timeframe timeline.Timeframe) (*string, error) {

	nextPageURLStr := ""
	if nextPageURL != nil {
		nextPageURLStr = *nextPageURL
	}

	// TODO: When requesting a page using since&until, the results
	// need to be ordered differently because they start at the since,
	// and if we go "next" it goes the wrong direction; furthermore,
	// their "order" method is broken: https://developers.facebook.com/support/bugs/2231843933505877/
	// - that all needs to be figured out before we do much more here
	// with regards to timeframes
	user, err := c.requestPage(nextPageURLStr, timeframe)
	if err != nil {
		return nil, fmt.Errorf("requesting next page: %v", err)
	}

	// TODO: Refactor this into smaller functions...

	for _, post := range user.Feed.Data {
		item := &timeline.Item{
			ID:        post.PostID,
			Timestamp: post.timestamp(),
			Location:  post.location(),
			Owner: timeline.Entity{
				Name: post.From.Name,
				Attributes: []timeline.Attribute{
					{
						Name:     "facebook_id",
						Value:    post.From.ID,
						Identity: true,
					},
				},
			},
			Content: timeline.ItemData{
				Data: timeline.StringData(post.Message),
			},
			Metadata: map[string]any{
				"Link":        post.Link,
				"Description": post.Description,
				"Name":        post.Name,
				"ParentID":    post.ParentID,
				"StatusType":  post.StatusType,
				"Type":        post.Type,
			},
		}

		ig := &timeline.Graph{Item: item}

		for _, att := range post.Attachments.Data {
			if att.Type == "album" {

				// add all the items to a collection
				coll := &timeline.Item{
					ID:             att.Target.ID,
					Classification: timeline.ClassCollection,
					Content: timeline.ItemData{
						Data: timeline.StringData(att.Title),
					},
					Owner: item.Owner,
				}

				for i, subatt := range att.Subattachments.Data {
					mediaID := subatt.Target.ID

					media, err := c.requestMedia(subatt.Type, mediaID)
					if err != nil {
						log.Printf("[ERROR] Getting media: %v", err)
						continue
					}
					mediaItem := media.item()

					ig.ToItemWithValue(timeline.RelInCollection, coll, i)

					ig.ToItem(timeline.RelAttachment, mediaItem)
				}
			}
		}

		itemChan <- ig
	}

	return user.Feed.Paging.Next, nil
}

func (c *Client) requestPage(nextPageURL string, timeframe timeline.Timeframe) (fbUser, error) {
	var user fbUser

	// if a URL for the "next" page was given, then we can just use that
	if nextPageURL != "" {
		err := c.apiRequestFullURL("GET", nextPageURL, nil, &user)
		if err != nil {
			return user, fmt.Errorf("requesting page using full URL: %s: %v", nextPageURL, err)
		}
		return user, nil
	}

	// otherwise, we'll need to craft our own URL to kick things off

	timeConstraint := fieldTimeConstraint(timeframe)
	nested := "{attachments,backdated_time,created_time,description,from,link,message,name,parent_id,place,status_type,type,with_tags}"

	v := url.Values{
		"fields": {"feed" + timeConstraint + nested},
		"order":  {"reverse_chronological"}, // TODO: see https://developers.facebook.com/support/bugs/2231843933505877/ (thankfully, reverse_chronological is their default for feed)
	}

	err := c.apiRequest("GET", "me?"+v.Encode(), nil, &user)
	return user, err
}

func (c *Client) requestMedia(mediaType, mediaID string) (*fbMedia, error) {
	if mediaType != "photo" && mediaType != "video" {
		return nil, fmt.Errorf("unknown media type: %s", mediaType)
	}

	fields := []string{"backdated_time", "created_time", "from", "id", "place", "updated_time"}
	switch mediaType {
	case "photo":
		fields = append(fields, "album", "images", "name", "name_tags")
	case "video":
		fields = append(fields, "description", "length", "source", "status", "title")
	}

	vals := url.Values{
		"fields": {strings.Join(fields, ",")},
	}
	endpoint := fmt.Sprintf("%s?%s", mediaID, vals.Encode())

	var media fbMedia
	err := c.apiRequest("GET", endpoint, nil, &media)
	if err != nil {
		return nil, err
	}
	media.fillFields(mediaType)

	return &media, nil
}

func (c *Client) getCollections(ctx context.Context, itemChan chan<- *timeline.Graph, timeframe timeline.Timeframe) error {
	c.checkpoint.Lock()
	nextPageURL := c.checkpoint.AlbumsNextPage
	c.checkpoint.Unlock()

	var err error
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
			nextPageURL, err = c.getCollectionsNextPage(itemChan, nextPageURL, timeframe)
			if err != nil {
				return err
			}
			if nextPageURL == nil {
				return nil
			}

			c.checkpoint.Lock()
			c.checkpoint.ItemsNextPage = nextPageURL
			c.checkpoint.Unlock()
			itemChan <- &timeline.Graph{
				Checkpoint: c.checkpoint.checkpointInfo,
			}
		}
	}
}

func (c *Client) getCollectionsNextPage(itemChan chan<- *timeline.Graph,
	nextPageURL *string, timeframe timeline.Timeframe) (*string, error) {

	var page fbMediaPage
	var err error
	if nextPageURL == nil {
		// get first page
		timeConstraint := fieldTimeConstraint(timeframe)
		v := url.Values{
			"fields": {"created_time,id,name,photos" + timeConstraint + "{album,backdated_time,created_time,from,id,images,updated_time,place,source}"},
		}
		v = qsTimeConstraint(v, timeframe)
		endpoint := fmt.Sprintf("me/albums?%s", v.Encode())
		err = c.apiRequest("GET", endpoint, nil, &page)
	} else {
		// get subsequent pages
		err = c.apiRequestFullURL("GET", *nextPageURL, nil, &page)
	}
	if err != nil {
		return nil, fmt.Errorf("requesting next page: %v", err)
	}

	// iterate each album on this page
	for _, album := range page.Data {
		// make the collection object
		coll := &timeline.Item{
			ID:             album.MediaID,
			Classification: timeline.ClassCollection,
			Content: timeline.ItemData{
				Data: timeline.StringData(album.Name),
			},
			Owner: timeline.Entity{
				Name: album.From.Name,
				Attributes: []timeline.Attribute{
					{
						Name:     "facebook_id",
						Value:    album.From.ID,
						Identity: true,
					},
				},
			},
		}

		// add each photo to the collection, page by page
		if album.Photos != nil {
			var counter int

			for {
				for i := range album.Photos.Data {
					album.Photos.Data[i].fillFields("photo")
					item := album.Photos.Data[i].item()
					ig := &timeline.Graph{Item: item}
					ig.ToItemWithValue(timeline.RelInCollection, coll, counter)
					itemChan <- ig
					counter++
				}

				if album.Photos.Paging.Next == nil {
					break
				}

				// request next page
				var nextPage *fbMediaPage
				err := c.apiRequestFullURL("GET", *album.Photos.Paging.Next, nil, &nextPage)
				if err != nil {
					return nil, fmt.Errorf("requesting next page of photos in album: %v", err)
				}
				album.Photos = nextPage
			}
		}

	}

	return page.Paging.Next, nil
}

func (c *Client) apiRequest(method, endpoint string, reqBodyData, respInto any) error {
	return c.apiRequestFullURL(method, apiBase+endpoint, reqBodyData, respInto)
}

func (c *Client) apiRequestFullURL(method, fullURL string, reqBodyData, respInto any) error {
	// make the request body just once (so we can retry safely)
	var reqBodyBytes []byte
	var err error
	if reqBodyData != nil {
		reqBodyBytes, err = json.Marshal(reqBodyData)
		if err != nil {
			return err
		}
	}

	const maxTries = 5
	for i := 0; i < maxTries; i++ {
		var reqBody io.Reader
		if len(reqBodyBytes) > 0 {
			reqBody = bytes.NewReader(reqBodyBytes)
		}

		var req *http.Request
		req, err = http.NewRequest(method, fullURL, reqBody)
		if err != nil {
			return fmt.Errorf("making request to %s %s: %v", method, fullURL, err)
		}
		if reqBody != nil {
			req.Header.Set("Content-Type", "application/json")
		}

		var resp *http.Response
		resp, err = c.httpClient.Do(req)
		if err != nil {
			c.logger.Error("api request; waiting and retrying",
				zap.String("method", method),
				zap.String("url", fullURL),
				zap.Error(err),
				zap.Int("attempt", i+1),
				zap.Int("max_attempts", maxTries),
			)
			// TODO: honor context cancellation
			time.Sleep(30 * time.Second)
			continue
		}
		defer resp.Body.Close() // yes, this is in a loop, so just a few sockets might leak for a few minutes

		if resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests {
			bodyText, err2 := io.ReadAll(io.LimitReader(resp.Body, 1024*256))

			if err2 == nil {
				err = fmt.Errorf("HTTP %d: %s: >>> %s <<<", resp.StatusCode, resp.Status, bodyText)
			} else {
				err = fmt.Errorf("HTTP %d: %s", resp.StatusCode, resp.Status)
			}

			c.logger.Error("bad response",
				zap.String("method", method),
				zap.String("url", fullURL),
				zap.Error(err),
				zap.Int("attempt", i+1),
				zap.Int("max_attempts", maxTries),
			)
			time.Sleep(30 * time.Second)
			continue
		} else if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("HTTP %d: %s", resp.StatusCode, resp.Status)
		}

		err = json.NewDecoder(resp.Body).Decode(&respInto)
		if err != nil {
			return fmt.Errorf("decoding JSON: %v", err)
		}
		break
	}

	return err
}

// NOTE: for these timeConstraint functions... Facebook docs recommend either setting
// BOTH since and until or NEITHER "for consistent results" (but seems to work with
// just one anyways...?)
// see https://developers.facebook.com/docs/graph-api/using-graph-api#time
func fieldTimeConstraint(timeframe timeline.Timeframe) string {
	var s string
	if timeframe.Since != nil {
		s += fmt.Sprintf(".since(%d)", timeframe.Since.Unix())
	}
	if timeframe.Until != nil {
		s += fmt.Sprintf(".until(%d)", timeframe.Until.Unix())
	}
	return s
}
func qsTimeConstraint(v url.Values, timeframe timeline.Timeframe) url.Values {
	if timeframe.Since != nil {
		v.Set("since", fmt.Sprintf("%d", timeframe.Since.Unix()))
	}
	if timeframe.Until != nil {
		v.Set("until", fmt.Sprintf("%d", timeframe.Until.Unix()))
	}
	return v
}

type checkpointInfo struct {
	ItemsNextPage  *string
	AlbumsNextPage *string
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
// 		log.Printf("[ERROR][%s] Encoding checkpoint: %v", DataSourceID, err)
// 	}
// 	timeline.Checkpoint(ctx, gobBytes)
// }

// // save records the checkpoint. It is NOT thread-safe,
// // so calls to this must be protected by a mutex.
// func (ch *checkpointInfo) load(checkpointGob []byte) {
// 	if len(checkpointGob) == 0 {
// 		return
// 	}
// 	err := timeline.UnmarshalGob(checkpointGob, ch)
// 	if err != nil {
// 		log.Printf("[ERROR][%s] Decoding checkpoint: %v", DataSourceID, err)
// 	}
// }

type fbUser struct {
	Feed fbUserFeed `json:"feed"`
	ID   string     `json:"id"`
}

type fbUserFeed struct {
	Data   []fbPost `json:"data"`
	Paging fbPaging `json:"paging"`
}

type fbPaging struct {
	Previous *string    `json:"previous"`
	Next     *string    `json:"next"`
	Cursors  *fbCursors `json:"cursors,omitempty"`
}

type fbCursors struct {
	Before string `json:"before,omitempty"`
	After  string `json:"after,omitempty"`
}

type fbFrom struct {
	Name string `json:"name,omitempty"`
	ID   string `json:"id,omitempty"`
}

type fbPlace struct {
	Name     string     `json:"name,omitempty"`
	Location fbLocation `json:"location,omitempty"`
	ID       string     `json:"id,omitempty"`
}

type fbLocation struct {
	City      string  `json:"city,omitempty"`
	Country   string  `json:"country,omitempty"`
	Latitude  float64 `json:"latitude,omitempty"`
	LocatedIn string  `json:"located_in,omitempty"`
	Longitude float64 `json:"longitude,omitempty"`
	Name      string  `json:"name,omitempty"`
	Region    string  `json:"region,omitempty"`
	State     string  `json:"state,omitempty"`
	Street    string  `json:"street,omitempty"`
	Zip       string  `json:"zip,omitempty"`
}

const apiBase = "https://graph.facebook.com/v3.2/"
