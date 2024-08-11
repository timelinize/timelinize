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

package instagram

import (
	"context"
	"io"
	"io/fs"
	"path"
	"time"

	"github.com/timelinize/timelinize/datasources/facebook"
	"github.com/timelinize/timelinize/timeline"
)

type instaPostsIndex []instaPost

type instaPost struct {
	Media []instaMedia `json:"media"`

	// usually for posts with multiple media
	Title             string `json:"title,omitempty"`
	CreationTimestamp int64  `json:"creation_timestamp,omitempty"`

	filename string
}

// allText returns a concatenation of all the titles in the post.
func (p instaPost) allText() string {
	s := facebook.FixString(p.Title)
	for _, m := range p.Media {
		if m.Title != "" {
			if s != "" {
				s += "\n\n"
			}
			s += facebook.FixString(m.Title)
		}
	}
	return s
}

// timestamp returns the timestamp of the post, or if not set,
// the timestamp of the first media item.
func (p instaPost) timestamp() time.Time {
	if p.CreationTimestamp > 0 {
		return time.Unix(p.CreationTimestamp, 0)
	}
	return time.Unix(p.Media[0].CreationTimestamp, 0)
}

type instaMedia struct {
	URI               string         `json:"uri"`
	CreationTimestamp int64          `json:"creation_timestamp"`
	MediaMetadata     instaMediaMeta `json:"media_metadata"`
	Title             string         `json:"title"`
}

func (m instaMedia) timelineItem(fsys fs.FS, owner timeline.Entity) *timeline.Item {
	return &timeline.Item{
		Classification:       timeline.ClassSocial,
		Timestamp:            time.Unix(m.CreationTimestamp, 0),
		Location:             m.location(),
		Owner:                owner,
		IntermediateLocation: m.URI,
		Content: timeline.ItemData{
			Filename: path.Base(m.URI),
			Data: func(_ context.Context) (io.ReadCloser, error) {
				return fsys.Open(m.URI)
			},
		},
	}
}

func (m instaMedia) location() timeline.Location {
	var l timeline.Location
	if len(m.MediaMetadata.PhotoMetadata.ExifData) > 0 {
		l.Latitude = &m.MediaMetadata.PhotoMetadata.ExifData[0].Latitude
		l.Longitude = &m.MediaMetadata.PhotoMetadata.ExifData[0].Longitude
	} else if len(m.MediaMetadata.VideoMetadata.ExifData) > 0 {
		l.Latitude = &m.MediaMetadata.VideoMetadata.ExifData[0].Latitude
		l.Longitude = &m.MediaMetadata.VideoMetadata.ExifData[0].Longitude
	}
	return l
}

type instaMediaMeta struct {
	PhotoMetadata instaPhotoAndVideoMeta `json:"photo_metadata"`
	VideoMetadata instaPhotoAndVideoMeta `json:"video_metadata"`
}

type instaPhotoAndVideoMeta struct {
	ExifData []instaExifData `json:"exif_data"`
}

type instaExifData struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
}

type instaPersonalInformation struct {
	ProfileUser []struct {
		Title        string `json:"title"`
		MediaMapData struct {
			ProfilePhoto struct {
				URI               string `json:"uri"`
				CreationTimestamp int64  `json:"creation_timestamp"`
				Title             string `json:"title"`
			} `json:"Profile Photo"`
		} `json:"media_map_data"`
		StringMapData struct {
			Email          instaProfileData `json:"Email"`
			PhoneNumber    instaProfileData `json:"Phone Number"`
			PhoneConfirmed instaProfileData `json:"Phone Confirmed"`
			Username       instaProfileData `json:"Username"`
			Name           instaProfileData `json:"Name"`
			Bio            instaProfileData `json:"Bio"`
			Gender         instaProfileData `json:"Gender"`
			DateOfBirth    instaProfileData `json:"Date of birth"`
			Website        instaProfileData `json:"Website"`
			PrivateAccount instaProfileData `json:"Private Account"`
		} `json:"string_map_data"`
	} `json:"profile_user"`
}

type instaProfileData struct {
	Href      string `json:"href"`
	Value     string `json:"value"`
	Timestamp int64  `json:"timestamp"`
}

type instaStories struct {
	IgStories []struct {
		URI               string `json:"uri"`
		CreationTimestamp int64  `json:"creation_timestamp"`
		Title             string `json:"title"`
	} `json:"ig_stories"`
}
