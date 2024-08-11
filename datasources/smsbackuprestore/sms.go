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

package smsbackuprestore

import (
	"encoding/xml"
	"time"

	"github.com/timelinize/timelinize/timeline"
)

// Smses was generated 2019-07-10 using an export from
// SMS Backup & Restore v10.05.602 (previous versions
// have a bug with emoji encodings).
type Smses struct {
	XMLName    xml.Name `xml:"smses"`
	Text       string   `xml:",chardata"`
	Count      int      `xml:"count,attr"`
	Type       string   `xml:"type,attr"`
	BackupSet  string   `xml:"backup_set,attr"`  // UUID
	BackupDate int64    `xml:"backup_date,attr"` // unix timestamp in milliseconds
	SMS        []SMS    `xml:"sms"`
	MMS        []MMS    `xml:"mms"`
}

// CommonSMSandMMSFields are the fields that both
// SMS and MMS share in common.
type CommonSMSandMMSFields struct {
	Text         string `xml:",chardata"`
	Address      string `xml:"address,attr"` // the phone number (or email address) -- I've seen some *weird* stuff in this field, don't rely on it
	Date         int64  `xml:"date,attr"`    // unix timestamp in milliseconds
	Read         int    `xml:"read,attr"`
	Locked       int    `xml:"locked,attr"`
	DateSent     int64  `xml:"date_sent,attr"` // unix timestamp in (SMS: milliseconds, MMS: seconds)
	SubID        int    `xml:"sub_id,attr"`
	ReadableDate string `xml:"readable_date,attr"` // format: "Oct 20, 2017 12:35:30 PM"
	ContactName  string `xml:"contact_name,attr"`  // might be "(Unknown)"
}

// within returns true if c.Date is within the given timeframe (it ignores item ID frames).
func (c CommonSMSandMMSFields) within(tf timeline.Timeframe) bool {
	// ts := time.Unix(0, c.Date*int64(time.Millisecond))
	ts := time.UnixMilli(c.Date)
	return tf.Contains(ts)
}

// SMS represents a simple text message.
type SMS struct {
	CommonSMSandMMSFields
	Protocol      int    `xml:"protocol,attr"`
	Type          int    `xml:"type,attr"` // 1 = received, 2 = sent, 3 = draft, 4 = outbox, 5 = failed, 6 = queued
	Subject       string `xml:"subject,attr"`
	Body          string `xml:"body,attr"`
	Toa           string `xml:"toa,attr"`
	ScToa         string `xml:"sc_toa,attr"`
	ServiceCenter string `xml:"service_center,attr"`
	Status        int    `xml:"status,attr"` // -1 = none, 0 = complete, 32 = pending, 64 = failed
}

// people returns what is known about the sender and receiver.
func (s SMS) people(dsOpt Options) (sender, receiver timeline.Entity) {
	// fill the person from the item
	itemOwner := timeline.Entity{
		Attributes: []timeline.Attribute{
			{
				Name:     timeline.AttributePhoneNumber,
				Value:    s.Address,
				Identity: true,
			},
		},
	}
	if s.ContactName != "(Unknown)" {
		itemOwner.Name = s.ContactName
	}

	timelineOwner := timeline.Entity{
		Attributes: []timeline.Attribute{
			{
				Name:     timeline.AttributePhoneNumber,
				Value:    dsOpt.OwnerPhoneNumber,
				Identity: true,
			},
		},
	}

	// return in the proper order
	if s.Type == smsTypeSent {
		return timelineOwner, itemOwner
	}
	return itemOwner, timelineOwner
}

func (s SMS) metadata() timeline.Metadata {
	var msgType string
	switch s.Type {
	case smsTypeDraft:
		msgType = "draft"
	case smsTypeFailed:
		msgType = "failed"
	case smsTypeOutbox:
		msgType = "outbox"
	case smsTypeQueued:
		msgType = "queued"
	case smsTypeReceived:
		msgType = "received"
	case smsTypeSent:
		msgType = "sent"
	}

	var status string
	switch s.Status {
	case smsStatusPending:
		status = "pending"
	case smsStatusComplete:
		status = "complete"
	case smsStatusFailed:
		status = "failed"
	}

	var readStatus string
	switch s.Read {
	case read:
		readStatus = "read"
	case unread:
		readStatus = "unread"
	}

	sc := s.ServiceCenter
	if sc == "null" {
		sc = ""
	}

	subj := s.Subject
	if subj == "null" {
		subj = ""
	}

	// ensure any/interface type so that nil check can succeed,
	// instead of having non-nil interface having nil value
	subID := any(&s.SubID)
	if s.SubID == -1 {
		subID = nil
	}

	return timeline.Metadata{
		"Type":           msgType,
		"Subject":        subj,
		"Read":           readStatus,
		"Service center": sc,
		"Sub ID":         subID,
		"Status":         status,
	}
}
