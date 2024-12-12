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

// Package gmail implements a Gmail data source using its API,
// documented at https://developers.google.com/gmail/api/.
// TODO: WIP - Development of this package has stalled for now; use the email data source.
package gmail

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"time"

	"github.com/timelinize/timelinize/timeline"
	"go.uber.org/zap"
)

func init() {
	err := timeline.RegisterDataSource(timeline.DataSource{
		Name:           "gmail",
		Title:          "Gmail",
		NewAPIImporter: func() timeline.APIImporter { return new(Client) },
		// OAuth2: timeline.OAuth2{
		// 	ProviderID: "google",
		// 	Scopes:     []string{"https://www.googleapis.com/auth/gmail.readonly"},
		// },
		// RateLimit: timeline.RateLimit{
		// 	// the Gmail rate limit is actually pretty generous, so we set a sane limit
		// 	// that should at least prevent saturation of the local Internet pipe
		// 	// (see https://developers.google.com/gmail/api/v1/reference/quota)
		// 	RequestsPerHour: 60 * 60 * 2,
		// 	BurstSize:       10,
		// },
		// NewClient: func(ctx context.Context, acc timeline.Account, _ any) (timeline.Client, error) {
		// 	httpClient, err := acc.NewHTTPClient(ctx)
		// 	if err != nil {
		// 		return nil, err
		// 	}
		// 	return &Client{
		// 		HTTPClient: httpClient,
		// 	}, nil
		// },
	})
	if err != nil {
		timeline.Log.Fatal("registering data source", zap.Error(err))
	}
}

var (
	oauth2 = timeline.OAuth2{
		ProviderID: "google",
		Scopes:     []string{"https://www.googleapis.com/auth/gmail.readonly"},
	}

	// rateLimit = timeline.RateLimit{
	// 	// the Gmail rate limit is actually pretty generous, so we set a sane limit
	// 	// that should at least prevent saturation of the local Internet pipe
	// 	// (see https://developers.google.com/gmail/api/v1/reference/quota)
	// 	RequestsPerHour: 60 * 60 * 2,
	// 	BurstSize:       10,
	// }
)

// Client interacts with the Gmail API.
// It requires an OAuth2-authorized HTTP
// client in order to work properly.
type Client struct {
	HTTPClient *http.Client
}

// Authenticate peforms authentication with the remote service.
func (c *Client) Authenticate(ctx context.Context, acc timeline.Account, _ any) error {
	return acc.AuthorizeOAuth2(ctx, oauth2)
}

// APIImport conducts an import using the service's API.
// opt.Timeframe precision is day-level at best.
func (c *Client) APIImport(ctx context.Context, _ timeline.Account, _ timeline.ImportParams) error {
	// TODO: load any previous checkpoint

	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		threadsResp, err := c.getPageOfThreads(ctx)
		if err != nil {
			return fmt.Errorf("getting page of threads: %w", err)
		}

		// TODO: batch requests
		for _, th := range threadsResp.Threads {
			threadResp, err := c.getThread(ctx, th.ID)
			if err != nil {
				return fmt.Errorf("getting page of messages in thread: %w", err)
			}

			msgDecoded, err := base64.URLEncoding.DecodeString(threadResp.Messages[0].Payload.Body.Data)
			if err != nil {
				return fmt.Errorf("base64-decoding message: %w", err)
			}

			log.Printf("%+v", threadResp.Messages[0])
			log.Printf("%s", msgDecoded)
			break //nolint // TODO: WIP
		}

		break //nolint // TODO: WIP
	}

	return nil
}

func (c *Client) getPageOfThreads(ctx context.Context) (gmailThreads, error) {
	qs := url.Values{"q": {"-is:chats"}} // TODO.

	var threadsResp gmailThreads
	err := c.apiRequestWithRetry(ctx, fmt.Sprintf("%s/users/me/threads?%s", apiBase, qs.Encode()), &threadsResp)
	if err != nil {
		return gmailThreads{}, fmt.Errorf("getting thread: %w", err)
	}

	return threadsResp, nil
}

func (c *Client) getThread(ctx context.Context, threadID string) (gmailThread, error) {
	var threadResp gmailThread
	err := c.apiRequestWithRetry(ctx, fmt.Sprintf("%s/users/me/threads/%s", apiBase, threadID), &threadResp)
	if err != nil {
		return gmailThread{}, fmt.Errorf("getting thread: %w", err)
	}
	return threadResp, nil
}

func (c *Client) apiRequestWithRetry(ctx context.Context, endpoint string, decodeInto any) error {
	const maxRetries = 5
	const wait = 5 * time.Second
	var err error

	for range maxRetries {
		var req *http.Request
		req, err = http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return fmt.Errorf("creating request: %w", err)
		}

		var resp *http.Response
		resp, err = c.HTTPClient.Do(req)
		if err != nil {
			log.Printf("[ERROR][%s] Doing API request: %v - waiting and retrying...", "gmail", err)
			time.Sleep(wait)
			continue
		}
		// TODO: check HTTP status code

		err = json.NewDecoder(resp.Body).Decode(decodeInto)
		resp.Body.Close()
		if err != nil {
			log.Printf("[ERROR][%s] Decoding API response body: %v - waiting and retrying...", "gmail", err)
			time.Sleep(wait)
			continue
		}

		break
	}

	return err
}

type gmailThreads struct {
	Threads            []gmailThreadIndex `json:"threads"`
	NextPageToken      string             `json:"nextPageToken"`
	ResultSizeEstimate int                `json:"resultSizeEstimate"`
}

type gmailThreadIndex struct {
	ID        string `json:"id"`
	Snippet   string `json:"snippet"`
	HistoryID string `json:"historyId"`
}

type gmailThread struct {
	ID        string         `json:"id"`
	HistoryID string         `json:"historyId"`
	Messages  []gmailMessage `json:"messages"`
}

type gmailMessage struct {
	ID           string              `json:"id"`
	ThreadID     string              `json:"threadId"`
	LabelIDs     []string            `json:"labelIds"`
	Snippet      string              `json:"snippet"`
	HistoryID    string              `json:"historyId"`
	InternalDate string              `json:"internalDate"`
	Payload      gmailMessagePayload `json:"payload"`
	SizeEstimate int                 `json:"sizeEstimate"`
}

type gmailMessagePayload struct {
	PartID   string               `json:"partId"`
	MimeType string               `json:"mimeType"`
	Filename string               `json:"filename"`
	Headers  []gmailMessageHeader `json:"headers"`
	Body     gmailMessageBody     `json:"body"`
}

type gmailMessageHeader struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type gmailMessageBody struct {
	Size int    `json:"size"`
	Data string `json:"data"`
}

const apiBase = "https://www.googleapis.com/gmail/v1"
