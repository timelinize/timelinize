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

package googlephotos

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/timelinize/timelinize/timeline"
)

// listMediaItems is the structure of the results
// of calling mediaItems in the Google Photos API.
type listMediaItems struct {
	MediaItems    []mediaItem `json:"mediaItems"`
	NextPageToken string      `json:"nextPageToken"`
}

type mediaItem struct {
	MediaID         string           `json:"id"`
	ProductURL      string           `json:"productUrl"`
	BaseURL         string           `json:"baseUrl"`
	Description     string           `json:"description"`
	MIMEType        string           `json:"mimeType"`
	MediaMetadata   mediaMetadata    `json:"mediaMetadata"`
	ContributorInfo mediaContributor `json:"mediaContributor"`
	Filename        string           `json:"filename"`
}

func (m mediaItem) timelinerItem() *timeline.Item {
	meta, err := m.metadata()
	if err != nil {
		log.Printf("[ERROR] Creating metadata from media item: %v", err)
	}

	return &timeline.Item{
		ID:        m.MediaID,
		Timestamp: m.MediaMetadata.CreationTime,
		// Location: see https://issuetracker.google.com/issues/80379228 😭
		Owner: timeline.Entity{
			Name: m.ContributorInfo.DisplayName,
		},
		Content: timeline.ItemData{
			Filename:  m.Filename,
			MediaType: m.MIMEType,
			Data:      m.dataFileReader,
		},
		Metadata: meta,
	}
}

func (m mediaItem) metadata() (timeline.Metadata, error) {
	// TODO: Parse exif metadata... maybe add most important/useful
	// EXIF fields to the metadata struct directly?

	meta := timeline.Metadata{
		"Description": m.Description,
	}

	if m.MediaMetadata.Photo != nil {
		meta["Camera make"] = m.MediaMetadata.Photo.CameraMake
		meta["Camera model"] = m.MediaMetadata.Photo.CameraModel
		meta["Focal length"] = m.MediaMetadata.Photo.FocalLength
		meta["Aperture f-stop"] = m.MediaMetadata.Photo.ApertureFNumber
		meta["ISO equivalent"] = m.MediaMetadata.Photo.ISOEquivalent
		meta["Exposure time"] = m.MediaMetadata.Photo.ExposureTime
	} else if m.MediaMetadata.Video != nil {
		meta["Camera make"] = m.MediaMetadata.Video.CameraMake
		meta["Camera model"] = m.MediaMetadata.Video.CameraModel
		meta["FPS"] = m.MediaMetadata.Video.FPS
	}

	widthInt, err := strconv.Atoi(m.MediaMetadata.Width)
	if err != nil {
		return meta, fmt.Errorf("parsing width as int: %v (width=%s)",
			err, m.MediaMetadata.Width)
	}
	meta["Width"] = widthInt
	heightInt, err := strconv.Atoi(m.MediaMetadata.Height)
	if err != nil {
		return meta, fmt.Errorf("parsing height as int: %v (height=%s)",
			err, m.MediaMetadata.Height)
	}
	meta["Height"] = heightInt

	return meta, nil
}

func (m mediaItem) dataFileReader(ctx context.Context) (io.ReadCloser, error) {
	if m.MediaMetadata.Video != nil && m.MediaMetadata.Video.Status != "READY" {
		log.Printf("[INFO] Skipping video file because it is not ready (status=%s filename=%s)",
			m.MediaMetadata.Video.Status, m.Filename)
		return nil, nil
	}

	u := m.BaseURL

	// configure for the download of full file with almost-full exif data; see
	// https://developers.google.com/photos/library/guides/access-media-items#base-urls
	if m.MediaMetadata.Photo != nil {
		u += "=d"
	} else if m.MediaMetadata.Video != nil {
		u += "=dv"
	}

	const maxTries = 5
	var err error
	var resp *http.Response
	for i := 0; i < maxTries; i++ {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		// TODO: custom HTTP client? and honor context
		resp, err = http.Get(u)
		if err != nil {
			err = fmt.Errorf("getting media contents: %v", err)
			// TODO: proper logger
			// log.Printf("[ERROR] %s: %s: %v - retrying... (attempt %d/%d)", DataSourceID, u, err, i+1, maxTries)
			// TODO: honor context cancellation
			time.Sleep(30 * time.Second)
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

			// TODO: proper logger
			// log.Printf("[ERROR %s: %s: Bad response: %v - waiting and retrying... (attempt %d/%d)",
			// 	DataSourceID, u, err, i+1, maxTries)
			// TODO: honor context cancellation
			time.Sleep(15 * time.Second)
			continue
		}

		break
	}

	if resp == nil {
		return nil, err
	}
	return resp.Body, err
}

type mediaMetadata struct {
	CreationTime time.Time      `json:"creationTime"`
	Width        string         `json:"width"`
	Height       string         `json:"height"`
	Photo        *photoMetadata `json:"photo,omitempty"`
	Video        *videoMetadata `json:"video,omitempty"`
}

type photoMetadata struct {
	CameraMake      string  `json:"cameraMake"`
	CameraModel     string  `json:"cameraModel"`
	FocalLength     float64 `json:"focalLength"`
	ApertureFNumber float64 `json:"apertureFNumber"`
	ISOEquivalent   int     `json:"isoEquivalent"`
	ExposureTime    string  `json:"exposureTime"` // TODO: Parse duration out of this...?
}

type videoMetadata struct {
	CameraMake  string  `json:"cameraMake"`
	CameraModel string  `json:"cameraModel"`
	FPS         float64 `json:"fps"`
	Status      string  `json:"status"`
}

type mediaContributor struct {
	ProfilePictureBaseURL string `json:"profilePictureBaseUrl"`
	DisplayName           string `json:"displayName"`
}

type listMediaItemsRequest struct {
	Filters   *listMediaItemsFilter `json:"filters,omitempty"`
	AlbumID   string                `json:"albumId,omitempty"`
	PageSize  int                   `json:"pageSize,omitempty"`
	PageToken string                `json:"pageToken,omitempty"`
}

type listMediaItemsFilter struct {
	DateFilter               listMediaItemsDateFilter      `json:"dateFilter"`
	IncludeArchivedMedia     bool                          `json:"includeArchivedMedia"`
	ExcludeNonAppCreatedData bool                          `json:"excludeNonAppCreatedData"`
	ContentFilter            listMediaItemsContentFilter   `json:"contentFilter"`
	MediaTypeFilter          listMediaItemsMediaTypeFilter `json:"mediaTypeFilter"`
}

type listMediaItemsDateFilter struct {
	Ranges []listMediaItemsFilterRange `json:"ranges,omitempty"`
	Dates  []filterDate                `json:"dates,omitempty"`
}

type listMediaItemsFilterRange struct {
	StartDate filterDate `json:"startDate"`
	EndDate   filterDate `json:"endDate"`
}

type filterDate struct {
	Month int `json:"month"`
	Day   int `json:"day"`
	Year  int `json:"year"`
}

type listMediaItemsContentFilter struct {
	ExcludedContentCategories []string `json:"excludedContentCategories,omitempty"`
	IncludedContentCategories []string `json:"includedContentCategories,omitempty"`
}

type listMediaItemsMediaTypeFilter struct {
	MediaTypes []string `json:"mediaTypes,omitempty"`
}

type listAlbums struct {
	Albums        []gpAlbum `json:"albums"`
	NextPageToken string    `json:"nextPageToken"`
}

type gpAlbum struct {
	ID                    string `json:"id"`
	Title                 string `json:"title,omitempty"`
	ProductURL            string `json:"productUrl"`
	MediaItemsCount       string `json:"mediaItemsCount"`
	CoverPhotoBaseURL     string `json:"coverPhotoBaseUrl"`
	CoverPhotoMediaItemID string `json:"coverPhotoMediaItemId"`
}
