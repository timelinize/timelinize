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

package facebook

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"path"
	"time"

	"github.com/timelinize/timelinize/timeline"
)

type fbMediaPage struct {
	Data   []fbMedia `json:"data"`
	Paging fbPaging  `json:"paging"`
}

// fbMedia is used for videos, photos, and albums.
type fbMedia struct {
	Album         fbAlbum       `json:"album,omitempty"`
	BackdatedTime string        `json:"backdated_time,omitempty"`
	CreatedTime   string        `json:"created_time,omitempty"`
	From          fbFrom        `json:"from,omitempty"`
	Images        []fbImage     `json:"images,omitempty"`
	UpdatedTime   string        `json:"updated_time,omitempty"`
	Description   string        `json:"description,omitempty"`
	Length        float64       `json:"length,omitempty"` // in seconds
	Message       string        `json:"message,omitempty"`
	Name          string        `json:"name,omitempty"`
	Place         *fbPlace      `json:"place,omitempty"`
	Photos        *fbMediaPage  `json:"photos,omitempty"`
	Source        string        `json:"source,omitempty"`
	Status        fbVideoStatus `json:"status,omitempty"`
	MediaID       string        `json:"id,omitempty"`

	// these fields added by us and used internally
	mediaType          string
	bestSourceURL      string
	bestSourceFilename string
}

func (m *fbMedia) item() *timeline.Item {
	return &timeline.Item{
		ID:        m.MediaID,
		Timestamp: m.timestamp(),
		Location:  m.location(),
		Owner: timeline.Entity{
			Name: m.From.Name,
			Attributes: []timeline.Attribute{
				{
					Name:     "facebook_id",
					Value:    m.From.ID,
					Identity: true,
				},
			},
		},
		Content: timeline.ItemData{
			Filename: m.bestSourceFilename,
			Data:     m.dataReader,
		},
		Metadata: map[string]any{
			"Name":        m.Name,
			"Description": m.Description,
		},
	}
}

func (m *fbMedia) fillFields(mediaType string) {
	m.mediaType = mediaType

	// get URL to actual media content; we'll need
	// it later, and by doing this now, we only have
	// to do it once
	switch mediaType {
	case "photo":
		_, _, m.bestSourceURL = m.getLargestImage()
	case "video":
		m.bestSourceURL = m.Source
	}
	if m.bestSourceURL != "" {
		sourceURL, err := url.Parse(m.bestSourceURL)
		if err != nil {
			// TODO: What to return in this case? return the error?
			log.Printf("[ERROR] Parsing media source URL to get filename: %v", err)
		}
		m.bestSourceFilename = path.Base(sourceURL.Path)
	}
}

func (m *fbMedia) timestamp() time.Time {
	if m.BackdatedTime != "" {
		return fbTimeToGoTime(m.BackdatedTime)
	}
	return fbTimeToGoTime(m.CreatedTime)
}

func (m *fbMedia) dataReader(ctx context.Context) (io.ReadCloser, error) {
	if m.bestSourceURL == "" {
		return nil, errors.New("no way to get data file: no best source URL")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.bestSourceURL, nil)
	if err != nil {
		return nil, err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("getting media contents: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, resp.Status)
	}

	return resp.Body, nil
}

func (m *fbMedia) getLargestImage() (height, width int, source string) {
	var largest int
	for _, im := range m.Images {
		size := im.Height * im.Width
		if size > largest {
			source = im.Source
			height = im.Height
			width = im.Width
			largest = size
		}
	}
	return
}

func (m *fbMedia) location() timeline.Location {
	if m.Place != nil {
		return timeline.Location{
			Latitude:  &m.Place.Location.Latitude,
			Longitude: &m.Place.Location.Longitude,
		}
	}
	return timeline.Location{}
}

type fbVideoStatus struct {
	VideoStatus string `json:"video_status,omitempty"`
}

type fbAlbum struct {
	CreatedTime string        `json:"created_time,omitempty"`
	Name        string        `json:"name,omitempty"`
	ID          string        `json:"id,omitempty"`
	Photos      []fbMediaPage `json:"photos,omitempty"`
}

type fbImage struct {
	Height int    `json:"height,omitempty"`
	Source string `json:"source,omitempty"`
	Width  int    `json:"width,omitempty"`
}

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
