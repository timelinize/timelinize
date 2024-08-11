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
	"log"
	"time"

	"github.com/timelinize/timelinize/timeline"
)

type fbPost struct {
	Attachments   fbPostAttachments `json:"attachments,omitempty"`
	BackdatedTime string            `json:"backdated_time,omitempty"`
	CreatedTime   string            `json:"created_time,omitempty"` // example format: "2018-12-22T19:10:30+0000"
	From          fbFrom            `json:"from,omitempty"`
	Link          string            `json:"link,omitempty"`
	Description   string            `json:"description,omitempty"`
	Message       string            `json:"message,omitempty"`
	Name          string            `json:"name,omitempty"`
	ParentID      string            `json:"parent_id,omitempty"`
	Place         *fbPlace          `json:"place,omitempty"`
	StatusType    string            `json:"status_type,omitempty"`
	Type          string            `json:"type,omitempty"`
	PostID        string            `json:"id,omitempty"`
}

func (p fbPost) timestamp() time.Time {
	if p.BackdatedTime != "" {
		return fbTimeToGoTime(p.BackdatedTime)
	}
	return fbTimeToGoTime(p.CreatedTime)
}

func (p fbPost) location() timeline.Location {
	if p.Place != nil {
		return timeline.Location{
			Latitude:  &p.Place.Location.Latitude,
			Longitude: &p.Place.Location.Longitude,
		}
	}
	return timeline.Location{}
}

type fbPostAttachments struct {
	Data []fbPostAttachmentData `json:"data"`
}

type fbPostAttachmentData struct {
	Media          fbPostAttachmentMedia  `json:"media,omitempty"`
	Target         fbPostAttachmentTarget `json:"target,omitempty"`
	Subattachments fbPostAttachments      `json:"subattachments,omitempty"`
	Title          string                 `json:"title,omitempty"`
	Type           string                 `json:"type,omitempty"`
	URL            string                 `json:"url,omitempty"`
}

type fbPostAttachmentMedia struct {
	Image fbPostAttachmentImage `json:"image,omitempty"`
}

type fbPostAttachmentImage struct {
	Height int    `json:"height,omitempty"`
	Src    string `json:"src,omitempty"`
	Width  int    `json:"width,omitempty"`
}

type fbPostAttachmentTarget struct {
	ID  string `json:"id,omitempty"`
	URL string `json:"url,omitempty"`
}

func fbTimeToGoTime(fbTime string) time.Time {
	if fbTime == "" {
		return time.Time{}
	}
	ts, err := time.Parse(fbTimeFormat, fbTime)
	if err != nil {
		log.Printf("[ERROR] Parsing timestamp from Facebook: '%s' is not in '%s' format",
			fbTime, fbTimeFormat)
	}
	return ts
}

const fbTimeFormat = "2006-01-02T15:04:05+0000"
