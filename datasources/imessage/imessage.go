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

// Package imessage implements a data source for Apple Messages (iMessage), either
// directly from the iMessage folder on a Macbook, or an iPhone backup.
package imessage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/timelinize/timelinize/timeline"
	"go.uber.org/zap"
)

func init() {
	err := timeline.RegisterDataSource(timeline.DataSource{
		Name:            "imessage",
		Title:           "iMessage",
		Icon:            "imessage.svg", // TODO: get iMessage icon
		NewOptions:      func() any { return new(Options) },
		NewFileImporter: func() timeline.FileImporter { return new(FileImporter) },
	})
	if err != nil {
		timeline.Log.Fatal("registering data source", zap.Error(err))
	}
}

// FileImporter can import from the iMessage database.
type FileImporter struct{}

// Recognize returns whether this file or folder is supported.
func (FileImporter) Recognize(_ context.Context, filepaths []string) (timeline.Recognition, error) {
	for _, fpath := range filepaths {
		if chatDBPath(fpath) != "" {
			return timeline.Recognition{Confidence: .85}, nil
		}
	}
	return timeline.Recognition{}, nil
}

// FileImport imports data from the given file or folder.
func (fimp *FileImporter) FileImport(ctx context.Context, inputPaths []string, itemChan chan<- *timeline.Graph, opt timeline.ListingOptions) error {
	if len(inputPaths) != 1 {
		return errors.New("only 1 input path supported")
	}
	inputPath := inputPaths[0]

	// start by adding contacts (TODO: this maybe should be separate? assumes we're on a Mac with the AddressBook DB)
	if err := fimp.processContacts(ctx, itemChan, opt); err != nil {
		return err
	}

	// open messages DB and prepare importer
	db, err := sql.Open("sqlite3", chatDBPath(inputPath)+"?mode=ro")
	if err != nil {
		return fmt.Errorf("opening chat.db: %w", err)
	}
	defer db.Close()

	im := Importer{
		DB: db,
		FillOutAttachmentItem: func(_ context.Context, filename string, attachment *timeline.Item) {
			chatDBFolderPath := filepath.FromSlash(filepath.Dir(chatDBPath(inputPath)))
			relativeFilename := filepath.FromSlash(strings.TrimPrefix(filename, "~/Library/Messages/"))

			attachment.OriginalLocation = relativeFilename
			attachment.Content.Data = func(context.Context) (io.ReadCloser, error) {
				return os.Open(filepath.Join(filepath.Clean(chatDBFolderPath), filepath.Clean(relativeFilename)))
			}
		},
	}

	if err := im.ImportMessages(ctx, itemChan, opt); err != nil {
		return err
	}

	return nil
}

// Importer is a type that can import messages from an Apple Messages chat DB.
type Importer struct {
	// An open chat.db sqlite file.
	DB *sql.DB

	// A callback that uses a filename as provided by
	// the database to fill out the OriginalLocation,
	// IntermediateLocation, and Content.Data fields
	// on an attachment before it is added to the graph.
	FillOutAttachmentItem func(ctx context.Context, filename string, attachmentItem *timeline.Item)

	// Typically phone number; only used if
	// message row doesn't contain this info.
	DeviceID string
}

// ImportMessages imports messages from the chat DB.
func (im Importer) ImportMessages(ctx context.Context, itemChan chan<- *timeline.Graph, opt timeline.ListingOptions) error {
	rows, err := im.DB.QueryContext(ctx,
		`SELECT
			m.ROWID, m.guid, m.text, m.attributedBody, m.service, m.date, m.date_read, m.date_delivered,
			m.is_from_me, m.is_spam, m.associated_message_guid, m.associated_message_type, m.destination_caller_id,
			m.reply_to_guid, m.thread_originator_guid,
			h.id, h.country, h.service,
			chat_h.id, chat_h.country, chat_h.service,
			a.guid, a.created_date, a.filename, a.mime_type, a.transfer_name
		FROM message AS m
		JOIN chat_message_join AS cmj    ON cmj.message_id = m.ROWID
		JOIN chat_handle_join  AS chj    ON chj.chat_id    = cmj.chat_id
		JOIN handle            AS chat_h ON chat_h.ROWID   = chj.handle_id  -- handle from chat join
		LEFT JOIN handle       AS h      ON h.ROWID        = m.handle_id    -- handle from message row
		LEFT JOIN message_attachment_join AS maj ON maj.message_id = m.ROWID
		LEFT JOIN attachment              AS a   ON a.ROWID        = maj.attachment_id
		ORDER BY m.ROWID ASC`)
	if err != nil {
		return fmt.Errorf("querying for message rows: %w", err)
	}
	defer rows.Close()

	// a single message's info may be spread across multiple rows thanks to our LEFT JOINs
	var currentMessage message

	finalizeMessage := func() {
		msgItem := &timeline.Item{
			ID:             currentMessage.guid,
			Classification: timeline.ClassMessage,
			Timestamp:      currentMessage.timestamp(),
			Owner:          currentMessage.sender(),
			Content:        currentMessage.content(),
			Metadata:       currentMessage.metadata(),
		}

		// if the message has no text, but has an attachment, make the (first) attachment
		// the primary content of the message, i.e. the main/parent item
		attachments := currentMessage.attachments(ctx, im, msgItem.Owner)
		if msgItem.Content.Data == nil && len(attachments) > 0 {
			// log.Println("ATTACHMENT AS MAIN:", msgItem.ID, attachments[0].ID)
			msgItem.Content = attachments[0].Content
			msgItem.OriginalLocation = attachments[0].OriginalLocation
			msgItem.IntermediateLocation = attachments[0].IntermediateLocation
			msgItem.Metadata.Merge(attachments[0].Metadata, timeline.MetaMergeReplace)
			msgItem.Metadata["Attachment ID"] = attachments[0].ID
			attachments = attachments[1:]
		}

		ig := &timeline.Graph{Item: msgItem}
		for _, attach := range attachments {
			ig.ToItem(timeline.RelAttachment, attach)
		}

		// skip empty messages, as far as we can tell; there's probably something to it,
		// but I've looked into it and I can't figure out what the purpose of the
		// message is from the database tables alone, at least -- I probably missed
		// something though
		if msgItem.Content.Data == nil && len(ig.Edges) == 0 {
			opt.Log.Debug("skipping empty message",
				zap.Int64("row_id", currentMessage.rowid),
				zap.String("guid", currentMessage.guid))
			return
		}

		// now that we've checked for actual content, go ahead and connect entities
		for _, recip := range currentMessage.sentTo() {
			ig.ToEntity(timeline.RelSent, recip)
		}

		// if this is a reply, make root of item graph the item this is replying to
		if currentMessage.replyToGUID != nil && currentMessage.threadOriginatorGUID != nil {
			ig.ToItem(timeline.RelReply, &timeline.Item{ID: *currentMessage.replyToGUID})
		}

		itemChan <- ig
	}

	for rows.Next() {
		var msg message
		var joinedH handle
		var attach attachment

		err := rows.Scan(
			&msg.rowid, &msg.guid, &msg.text, &msg.attributedBody, &msg.service, &msg.date, &msg.dateRead,
			&msg.dateDelivered, &msg.isFromMe, &msg.isSpam, &msg.associatedMessageGUID, &msg.associatedMessageType,
			&msg.destinationCallerID, &msg.replyToGUID, &msg.threadOriginatorGUID, &msg.handle.id,
			&msg.handle.country, &msg.handle.service, &joinedH.id, &joinedH.country, &joinedH.service,
			&attach.guid, &attach.createdDate, &attach.filename, &attach.mimeType, &attach.transferName)
		if err != nil {
			return fmt.Errorf("scanning row: %w", err)
		}
		if msg.rowid == 0 {
			continue // I've never seen this, but might as well be careful
		}

		// fill in the normalized ID (phone number, usually) of the device; prefer the entry in the DB, I guess,
		// but fall back to the device's telephony/commcenter information if needed
		if msg.destinationCallerID != nil {
			msg.normalizedCallerID = NormalizePhoneNumber(*msg.destinationCallerID, "")
		} else {
			msg.normalizedCallerID = im.DeviceID
		}

		// if message is a reaction handle it separately
		if msg.associatedMessageGUID != nil && msg.associatedMessageType != nil {
			reaction := messageReactions[*msg.associatedMessageType]
			reactor := msg.sender()

			_, associatedGUID, cut := strings.Cut(*msg.associatedMessageGUID, "/")
			if !cut {
				_, associatedGUID, cut = strings.Cut(*msg.associatedMessageGUID, ":")
			}
			if !cut {
				associatedGUID = *msg.associatedMessageGUID
			}

			// I found that not all of these associated message GUIDs are found in the DB. Shortly after I got my iPhone,
			// there was a brief period where I couldn't receive messages. My Macbook did, but none of the messages I sent
			// from my iPhone during that time ever appeared on my Macbook. Thus, reactions from my contact(s) to my
			// messages(s) have broken GUIDs. So here we do a quick check to see if the referenced row exists first.
			var count int
			err := im.DB.QueryRowContext(ctx, "SELECT count() FROM message WHERE guid=? LIMIT 1", associatedGUID).Scan(&count)
			if err == nil && count > 0 {
				reactionGraph := &timeline.Graph{
					Item: &timeline.Item{ID: associatedGUID},
				}
				reactionGraph.FromEntityWithValue(&reactor, timeline.RelReacted, reaction)
				itemChan <- reactionGraph
				continue
			}
		}

		// start of new message
		if msg.rowid != currentMessage.rowid {
			// finish previous one and process it
			if currentMessage.rowid > 0 {
				finalizeMessage()
			}

			// advance the row ID we're working on
			currentMessage = msg
		}

		// continuation (or still first row) of same message; add all joined data
		if attach.guid != nil {
			currentMessage.attached = append(currentMessage.attached, attach)
		}
		if joinedH.id != nil {
			currentMessage.participants = append(currentMessage.participants, joinedH)
		}
	}
	if err = rows.Err(); err != nil {
		return fmt.Errorf("scanning rows: %w", err)
	}

	// don't forget to process the last one too!
	if currentMessage.rowid > 0 {
		finalizeMessage()
	}

	return nil
}

func chatDBPath(inputPath string) string {
	info, err := os.Stat(inputPath)
	if err != nil {
		return ""
	}
	// To be 100% confident we should open chat.db and see if we can query it...
	if !info.IsDir() &&
		filepath.Base(inputPath) == "chat.db" &&
		timeline.FileExists(filepath.Join(filepath.Dir(inputPath), "Attachments")) {
		return inputPath
	} else if info.IsDir() &&
		timeline.FileExists(filepath.Join(inputPath, "Attachments")) &&
		timeline.FileExists(filepath.Join(inputPath, "chat.db")) {
		return filepath.Join(inputPath, "chat.db")
	}
	return ""
}

type message struct {
	rowid          int64
	guid           string
	text           *string
	attributedBody []byte // a rich text representation of the message; seems to be the newer way, as of iOS 16-ish
	service        *string
	date           *int64
	dateRead       *int64
	dateDelivered  *int64
	isFromMe       *int
	isSpam         *int

	// seems to be the device's phone number!
	destinationCallerID *string
	normalizedCallerID  string

	// message reactions
	associatedMessageGUID *string
	associatedMessageType *int

	replyToGUID          *string // always seems to be set, even if not an explicit reply via user gesture
	threadOriginatorGUID *string // I think this is only set when the user explicitly makes a thread (by replying)

	handle       handle
	participants []handle
	attached     []attachment
}

func (m message) timestamp() time.Time {
	if m.date == nil {
		return time.Time{}
	}
	return AppleNanoToTime(*m.date)
}

// fromMe returns true if the message was sent by the owner of the device.
func (m message) fromMe() bool {
	if m.isFromMe == nil {
		return false
	}
	return *m.isFromMe == 1
}

// senderID returns the normalized sender ID (phone number, or sometimes email).
func (m message) senderID() string {
	if m.fromMe() {
		return m.normalizedCallerID
	}
	return m.handle.normalizedID()
}

func (m message) sender() timeline.Entity {
	return entityWithID(m.senderID())
}

func (m message) sentTo() []*timeline.Entity {
	senderID := m.senderID()

	var ents []*timeline.Entity //nolint:prealloc // bug filed: https://github.com/alexkohler/prealloc/issues/30

	// if device is not the sender, it is at least a recipient!
	if !m.fromMe() {
		e := entityWithID(m.normalizedCallerID)
		ents = append(ents, &e)
	}

	// add participants, skipping the sender to avoid redundancy
	for _, p := range m.participants {
		pID := p.normalizedID()
		if pID == senderID {
			continue
		}
		e := p.entity()
		ents = append(ents, &e)
	}

	return ents
}

func (m message) attachments(ctx context.Context, im Importer, sender timeline.Entity) []*timeline.Item {
	var items []*timeline.Item //nolint:prealloc // bug filed: https://github.com/alexkohler/prealloc/issues/30
	for _, a := range m.attached {
		if a.filename == nil {
			continue
		}

		var guid string
		if a.guid != nil {
			guid = *a.guid
		}
		var ts time.Time
		if a.createdDate != nil {
			ts = AppleSecondsToTime(*a.createdDate)
		}

		it := &timeline.Item{
			ID:             guid,
			Classification: timeline.ClassMessage,
			Timestamp:      ts,
			Owner:          sender,
			Content: timeline.ItemData{
				Filename: path.Base(*a.filename),
			},
		}
		if a.mimeType != nil {
			it.Content.MediaType = *a.mimeType
		}
		if im.FillOutAttachmentItem != nil {
			im.FillOutAttachmentItem(ctx, *a.filename, it)
		}

		items = append(items, it)
	}
	return items
}

func (m message) content() timeline.ItemData {
	var text string
	if m.text != nil {
		text = *m.text
	} else if len(m.attributedBody) > 0 {
		nsStringContent, err := parseStreamTypedNSString(m.attributedBody)
		if err != nil {
			// TODO: log this properly or something?
			log.Printf("[ERROR] failed parsing NSString streamtyped from attributedBody: %v (body=%v)", err, m.attributedBody)
			return timeline.ItemData{}
		}
		text = nsStringContent
	}

	// iMessage uses the Object Replacement Character (efbfbc) as the textual message for attachments
	text = strings.ReplaceAll(text, "\uFFFC", "")

	if strings.TrimSpace(text) == "" {
		return timeline.ItemData{}
	}

	return timeline.ItemData{Data: timeline.StringData(text)}
}

func (m message) metadata() timeline.Metadata {
	meta := make(timeline.Metadata)
	if m.service != nil {
		meta["Service"] = *m.service
	}
	if m.dateRead != nil {
		meta["Date read"] = AppleNanoToTime(*m.dateRead)
	}
	if m.dateDelivered != nil {
		meta["Date delivered"] = AppleNanoToTime(*m.dateDelivered)
	}
	return meta
}

type handle struct {
	id      *string
	country *string
	service *string
}

func (h handle) ID() string {
	if h.id == nil {
		return ""
	}
	return *h.id
}

func (h handle) normalizedID() string {
	var country string
	if h.country != nil {
		country = strings.ToUpper(*h.country)
	}
	return NormalizePhoneNumber(h.ID(), country)
}

func (h handle) entity() timeline.Entity {
	if h.id == nil {
		return timeline.Entity{}
	}
	return timeline.Entity{
		Attributes: []timeline.Attribute{
			{
				Name:     timeline.AttributePhoneNumber,
				Value:    *h.id,
				Identity: true,
			},
		},
	}
}

type attachment struct {
	guid         *string
	createdDate  *int64
	filename     *string
	mimeType     *string
	transferName *string // filename that was transmitted
}

// NormalizePhoneNumber tries to NormalizePhoneNumber the ID (phone number), and simply returns the input if it fails.
func NormalizePhoneNumber(id, country string) string {
	norm, err := timeline.NormalizePhoneNumber(id, country)
	if err != nil {
		return id // oh well
	}
	return norm
}

func entityWithID(id string) timeline.Entity {
	attribute := timeline.AttributePhoneNumber
	if strings.Contains(id, "@") {
		attribute = timeline.AttributeEmail
	}
	return timeline.Entity{
		Attributes: []timeline.Attribute{
			{
				Name:     attribute,
				Value:    id,
				Identity: true,
			},
		},
	}
}

// TODO: These but in the 3000s are for removing the reaction.
var messageReactions = map[int]string{
	2000: "\u2764\uFE0F", // red heart, but it appears black otherwise: ❤️
	2001: "👍",
	2002: "👎",
	2003: "😂",
	2004: "\u203C\uFE0F", // red double-exclamation, but it appears plain black otherwise: ‼️
	2005: "❓",
}

// Options configures the data source.
type Options struct{}
