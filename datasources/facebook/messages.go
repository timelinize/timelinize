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
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strings"
	"time"

	"github.com/timelinize/timelinize/timeline"
)

func GetMessages(dsName string, dirEntry timeline.DirEntry, params timeline.ImportParams) error {
	// figure out which archive version we're working with
	messagesInboxPrefix := pre2024MessagesPrefix
	if _, err := fs.Stat(dirEntry.FS, messagesInboxPrefix); errors.Is(err, fs.ErrNotExist) {
		messagesInboxPrefix = year2024MessagesPrefix
	}

	for _, messageSubfolder := range []string{
		"inbox",
		"archived_threads",
	} {
		err := fs.WalkDir(dirEntry.FS, path.Join(messagesInboxPrefix, messageSubfolder), func(fpath string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			if path.Ext(d.Name()) != ".json" {
				return nil
			}

			file, err := dirEntry.FS.Open(fpath)
			if err != nil {
				return err
			}
			defer file.Close()

			var thread fbMessengerThread
			if err := json.NewDecoder(file).Decode(&thread); err != nil {
				return err
			}

			for _, msg := range thread.Messages {
				senderName := FixString(msg.SenderName)
				sender := timeline.Entity{
					Name: senderName,
					Attributes: []timeline.Attribute{
						{
							Name:     dsName + "_name",
							Value:    senderName,
							Identity: true,
						},
					},
				}
				msgText := FixString(msg.Content)
				msgTimestamp := time.UnixMilli(msg.TimestampMS)

				var attachments []*timeline.Item

				for _, photo := range msg.Photos {
					attached := &timeline.Item{
						Classification: timeline.ClassMessage,
						Owner:          sender,
					}
					photo.fillItem(attached, dirEntry, "", params.Log)
					if attached.Timestamp.IsZero() {
						attached.Timestamp = msgTimestamp
					}
					attachments = append(attachments, attached)
				}
				for _, video := range msg.Videos {
					attached := &timeline.Item{
						Classification: timeline.ClassMessage,
						Owner:          sender,
					}
					video.fillItem(attached, dirEntry, "", params.Log)
					if attached.Timestamp.IsZero() {
						attached.Timestamp = msgTimestamp
					}
					attachments = append(attachments, attached)
				}
				for _, gif := range msg.GIFs {
					attached := &timeline.Item{
						Classification: timeline.ClassMessage,
						Owner:          sender,
					}
					gif.fillItem(attached, dirEntry, "", params.Log)
					if attached.Timestamp.IsZero() {
						attached.Timestamp = msgTimestamp
					}
					attachments = append(attachments, attached)
				}
				for _, audio := range msg.AudioFiles {
					attached := &timeline.Item{
						Classification: timeline.ClassMessage,
						Owner:          sender,
					}
					audio.fillItem(attached, dirEntry, "", params.Log)
					if attached.Timestamp.IsZero() {
						attached.Timestamp = msgTimestamp
					}
					attachments = append(attachments, attached)
				}
				if msg.Sticker.URI != "" {
					attached := &timeline.Item{
						Classification: timeline.ClassMessage,
						Owner:          sender,
					}
					msg.Sticker.fillItem(attached, dirEntry, "", params.Log)
					if attached.Timestamp.IsZero() {
						attached.Timestamp = msgTimestamp
					}
					attachments = append(attachments, attached)
				}
				if msg.Share.Link != "" {
					if !strings.Contains(msgText, msg.Share.Link) {
						msgText += "\n\n" + msg.Share.Link
						if msg.Share.ShareText != "" {
							msgText += "\n" + FixString(msg.Share.ShareText)
						}
					} else if msg.Share.ShareText != "" {
						msgText += "\n\n" + FixString(msg.Share.ShareText)
					}
				}

				msgText = strings.TrimSpace(msgText)

				var item *timeline.Item
				switch {
				case msgText != "":
					item = &timeline.Item{
						Classification: timeline.ClassMessage,
						Timestamp:      msgTimestamp,
						Owner:          sender,
						Content: timeline.ItemData{
							Data: timeline.StringData(msgText),
						},
					}
				case len(attachments) > 0:
					item, attachments = attachments[0], attachments[1:]
				default:
					// found an empty message; I've seen this happen rarely,
					// like if a message IsUnsent; no content, so skip
					continue
				}

				ig := &timeline.Graph{Item: item}

				for _, attach := range attachments {
					ig.ToItem(timeline.RelAttachment, attach)
				}
				for _, recipient := range thread.sentTo(senderName, dsName) {
					ig.ToEntity(timeline.RelSent, recipient)
				}
				for _, reaction := range msg.Reactions {
					ig.FromEntityWithValue(&timeline.Entity{
						Name: reaction.Actor,
						Attributes: []timeline.Attribute{
							{
								Name:     dsName + "_name",
								Value:    reaction.Actor,
								Identity: true,
							},
						},
					}, timeline.RelReacted, FixString(reaction.Reaction))
				}

				params.Pipeline <- ig
			}

			return nil
		})
		// since we try different folders above, ignore NotExist since some just might not exist in the archive
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("walking messages: %w", err)
		}
	}

	return nil
}
