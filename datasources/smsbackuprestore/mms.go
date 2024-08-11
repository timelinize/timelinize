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
	"context"
	"encoding/base64"
	"io"
	"strings"

	"github.com/timelinize/timelinize/timeline"
)

// MMS represents a multimedia message.
type MMS struct {
	CommonSMSandMMSFields
	Rr         string    `xml:"rr,attr"`
	Sub        string    `xml:"sub,attr"`
	CtT        string    `xml:"ct_t,attr"`
	ReadStatus string    `xml:"read_status,attr"`
	Seen       string    `xml:"seen,attr"`
	MsgBox     int       `xml:"msg_box,attr"`
	SubCs      string    `xml:"sub_cs,attr"`
	RespSt     string    `xml:"resp_st,attr"`
	RetrSt     string    `xml:"retr_st,attr"`
	DTm        string    `xml:"d_tm,attr"`
	TextOnly   string    `xml:"text_only,attr"`
	Exp        string    `xml:"exp,attr"`
	MID        string    `xml:"m_id,attr"`
	St         string    `xml:"st,attr"`
	RetrTxtCs  string    `xml:"retr_txt_cs,attr"`
	RetrTxt    string    `xml:"retr_txt,attr"`
	Creator    string    `xml:"creator,attr"`
	MSize      string    `xml:"m_size,attr"`
	RptA       string    `xml:"rpt_a,attr"`
	CtCls      string    `xml:"ct_cls,attr"`
	Pri        string    `xml:"pri,attr"`
	TrID       string    `xml:"tr_id,attr"`
	RespTxt    string    `xml:"resp_txt,attr"`
	CtL        string    `xml:"ct_l,attr"`
	MCls       string    `xml:"m_cls,attr"`
	DRpt       string    `xml:"d_rpt,attr"`
	V          string    `xml:"v,attr"`
	MType      string    `xml:"m_type,attr"`
	Parts      Parts     `xml:"parts"`
	Addrs      Addresses `xml:"addrs"`
}

// people returns what is known about the sender and receivers.
// Unfortunately the export format does not currently give us
// good information for the contacts' names, especially groups.
// TODO: I've noticed that it even omits the owner from the group sometimes. That must be a bug in the app -- maybe we should always add the owner if not present (as sender if there's no sender)?
func (m MMS) people(dsOpt Options) (sender timeline.Entity, recipients []timeline.Entity) {
	addrToPerson := func(addr Address) timeline.Entity {
		// the processor will standardize phone numbers for us, but we do it here since we
		// need to compare phone numbers to try to determine who this is
		standardizedPhoneNum, err := timeline.NormalizePhoneNumber(addr.Address, dsOpt.DefaultRegion)
		if err != nil {
			// TODO: log this?
			// oh well; just take what we're given, I guess
			standardizedPhoneNum = addr.Address
		}

		p := timeline.Entity{
			Attributes: []timeline.Attribute{
				{
					Name:     timeline.AttributePhoneNumber,
					Value:    standardizedPhoneNum,
					Identity: true,
				},
			},
		}

		// Getting the name accurately in group texts is tricky or impossible, since order varies
		// or is downright wrong. For example, a group text with 5 participants might have only 1
		// contact name. Or even if everyone does have a contact name, the order of the addrs doesn't
		// match; so it's impossible to know unless we can use process of elimination. For now,
		// I think that means we can only assign the name if there's 2 participants (one of them
		// should obviously be the account owner). If we have individual (1-on-1) messages with
		// those other contacts, we'd already have them in our persons table anyway.
		if len(m.Addrs.Addr) == 2 &&
			m.ContactName != "" &&
			m.ContactName != "(Unknown)" &&
			standardizedPhoneNum != dsOpt.OwnerPhoneNumber {
			p.Name = m.ContactName
		}

		return p
	}

	owner := timeline.Entity{
		Attributes: []timeline.Attribute{
			{
				Name:     timeline.AttributePhoneNumber,
				Value:    dsOpt.OwnerPhoneNumber,
				Identity: true,
			},
		},
	}

	// I have seen some instances where all MMS addrs are the same phone number!
	// That is definitely a bug in the data source that we can't do anything about.
	// Fortunately, m.MsgBox tells us whether the message was sent or received.
	allAddrsSame := true
	for i := 1; i < len(m.Addrs.Addr); i++ {
		if m.Addrs.Addr[i].Address != m.Addrs.Addr[i-1].Address {
			allAddrsSame = false
			break
		}
	}
	if allAddrsSame {
		// don't trust the address types in this case; use MsgBox instead
		// to at least reasonably assume account owner's role
		if m.MsgBox == mmsMsgBoxReceived {
			recipients = appendIfUnique(recipients, owner)
		} else {
			sender = owner
		}
	}

	// get sender, since the input data can be bad and repeat addresses
	// in the list, we have to make sure the sender isn't also a receiver
	// (I have seen this before)
	if _, ok := sender.Attribute(timeline.AttributePhoneNumber); !ok {
		for _, addr := range m.Addrs.Addr {
			if addr.Type == mmsAddrTypeFrom {
				sender = addrToPerson(addr)
				break
			}
		}
	}

	// fill the recipients list
	for _, addr := range m.Addrs.Addr {
		// we already have the sender; skip
		if addr.Type == mmsAddrTypeFrom {
			continue
		}
		// make sure they're not also the sender (I have seen that in the
		// input data before when all addresses are the same; sigh)
		p := addrToPerson(addr)
		if p.AttributeValue(timeline.AttributePhoneNumber) != sender.AttributeValue(timeline.AttributePhoneNumber) {
			recipients = appendIfUnique(recipients, p)
		}
	}

	return
}

// appendIfUnique appends p to persons if p isn't found in persons already.
// This shouldn't be necessary, but I can't trust the data source to not
// duplicate addresses in the MMS address list (which I've already seen).
// TODO: I think the processor handles duplicate user IDs gracefully? But maybe still best to be tidy about things
func appendIfUnique(persons []timeline.Entity, p timeline.Entity) []timeline.Entity {
	for _, existing := range persons {
		if p.AttributeValue(timeline.AttributePhoneNumber) == existing.AttributeValue(timeline.AttributePhoneNumber) {
			return persons
		}
	}
	return append(persons, p)
}

func (m MMS) metadata() timeline.Metadata {
	var msgBox string
	switch m.MsgBox {
	case mmsMsgBoxDraft:
		msgBox = "draft"
	case mmsMsgBoxOutbox:
		msgBox = "outbox"
	case mmsMsgBoxSent:
		msgBox = "sent"
	case mmsMsgBoxReceived:
		msgBox = "received"
	}

	var readStatus string
	switch m.Read {
	case read:
		readStatus = "read"
	case unread:
		readStatus = "unread"
	}

	sub := m.Sub
	if sub == "null" || sub == "-1" {
		sub = ""
	}

	creator := m.Creator
	if creator == "null" || creator == "-1" {
		creator = ""
	}

	return timeline.Metadata{
		"Box":     msgBox,
		"Read":    readStatus,
		"Subject": sub,
		"Creator": creator,
	}
}

// Parts is the parts of an MMS.
type Parts struct {
	Text string `xml:",chardata"`
	Part []Part `xml:"part"`
}

// putTextPartFirst reorders the parts so that the (first,
// but almost certainly *only*) text part is first in the
// list. This is necessary because we prefer the text part
// to be the "main" item in the MMS, with media being only
// attachments, if there is a text part at all; and part
// order varies at every export. Following the item graph
// is more intuitive if the text content is first.
func (pa Parts) putTextPartFirst() {
	for i, p := range pa.Part {
		if p.isText() {
			pa.Part[0], pa.Part[i] = pa.Part[i], pa.Part[0]
			return
		}
	}
}

// Part is a part of an MMS.
type Part struct {
	Text        string `xml:",chardata"`
	Seq         int    `xml:"seq,attr"`
	ContentType string `xml:"ct,attr"`
	Name        string `xml:"name,attr"`
	Charset     string `xml:"chset,attr"`
	Cd          string `xml:"cd,attr"`
	Fn          string `xml:"fn,attr"`
	Cid         string `xml:"cid,attr"`
	Filename    string `xml:"cl,attr"`
	CttS        string `xml:"ctt_s,attr"`
	CttT        string `xml:"ctt_t,attr"`
	AttrText    string `xml:"text,attr"`
	Data        string `xml:"data,attr"`
}

// isText returns true if this part is a text media type.
func (p Part) isText() bool {
	return strings.HasPrefix(p.ContentType, "text/")
}

// data returns a reader into the part's data, whether text or media.
func (p Part) data() timeline.DataFunc {
	if p.isText() {
		return timeline.StringData(p.AttrText)
	}
	return func(_ context.Context) (io.ReadCloser, error) {
		sr := strings.NewReader(p.Data)
		bd := base64.NewDecoder(base64.StdEncoding, sr)
		return io.NopCloser(bd), nil
	}
}

// Addresses is the addresses the MMS was sent to.
type Addresses struct {
	Text string    `xml:",chardata"`
	Addr []Address `xml:"addr"`
}

// Address is a sender or recipient of the MMS.
type Address struct {
	Text    string `xml:",chardata"`
	Address string `xml:"address,attr"`
	Type    int    `xml:"type,attr"` // 151 = recipient, 137 = sender, 129 = bcc, 130 = cc
	Charset string `xml:"charset,attr"`
}
