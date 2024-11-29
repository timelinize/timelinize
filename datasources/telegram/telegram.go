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

// Package telegram implements a data source for Telegram data exports.
package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/timelinize/timelinize/timeline"
	"go.uber.org/zap"
)

func init() {
	err := timeline.RegisterDataSource(timeline.DataSource{
		Name:            "telegram",
		Title:           "Telegram",
		Icon:            "telegram.svg",
		NewOptions:      func() any { return new(Options) },
		NewFileImporter: func() timeline.FileImporter { return new(FileImporter) },
	})
	if err != nil {
		timeline.Log.Fatal("registering data source", zap.Error(err))
	}
}

// FileImporter can import iPhone backups from a folder on disk.
type FileImporter struct {
}

// Recognize returns whether the input is supported.
func (FileImporter) Recognize(_ context.Context, dirEntry timeline.DirEntry, _ timeline.RecognizeParams) (timeline.Recognition, error) {
	f, err := dirEntry.FS.Open("result.json")
	if err != nil {
		return timeline.Recognition{}, nil
	}
	defer f.Close()

	var hdr desktopExportHeader
	if err = json.NewDecoder(f).Decode(&hdr); err != nil {
		return timeline.Recognition{}, nil
	}

	if hdr.About == "" || hdr.PersonalInformation.UserID == 0 {
		return timeline.Recognition{}, nil
	}

	return timeline.Recognition{Confidence: 1}, nil
}

type jsonToken struct {
	json.Token
	context string
}

func (jt jsonToken) String() string {
	switch v := jt.Token.(type) {
	case string:
		return v
	case int:
		return strconv.Itoa(v)
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(v)
	case json.Delim, nil:
		return ""
	}
	return fmt.Sprintf("%v", jt.Token)
}

type jsonPath []jsonToken

func (jp jsonPath) String() string {
	parts := make([]string, 0, len(jp))
	for _, p := range jp {
		val := p.String()
		if len(parts) == 0 || val != "" {
			parts = append(parts, val)
		}
	}
	return strings.Join(parts, ".")
}

// FileImport imports data from the file.
func (fimp *FileImporter) FileImport(ctx context.Context, dirEntry timeline.DirEntry, params timeline.ImportParams) error {
	// dsOpt := opt.DataSourceOptions.(*Options)

	const jsonFilename = "result.json"
	f, err := dirEntry.FS.Open(jsonFilename)
	if err != nil {
		return err
	}
	defer func() { f.Close() }() // value of f might change between now and returning

	dec := json.NewDecoder(f)

	var nesting jsonPath
	var context string

	var seenChats bool
	var currentChatType string
	var ownerEntity, currentChatEntity timeline.Entity

	handler := func(scope string, dec *json.Decoder) error {
		switch scope {
		case ".personal_information":
			// only need it once
			if len(ownerEntity.Attributes) > 0 {
				return nil
			}

			var pi personalInformation
			if err = dec.Decode(&pi); err != nil {
				return err
			}

			ownerEntity = timeline.Entity{
				Name: pi.FirstName + " " + pi.LastName,
				Attributes: []timeline.Attribute{
					{
						Name:     "telegram_id",
						Value:    strconv.FormatInt(pi.UserID, 10),
						Identity: true,
					},
					{
						Name:        timeline.AttributePhoneNumber,
						Value:       pi.PhoneNumber,
						Identifying: true,
					},
				},
			}

			if seenChats {
				// now start over and look for chats and contacts specifically
				f, err = dirEntry.FS.Open(jsonFilename)
				if err != nil {
					return err
				}
				nesting = jsonPath{}
			}

			// TODO: hrm, any way we can do without this? maybe wrap Decode() or something?
			if context == objectValue {
				nesting = nesting[:len(nesting)-1]
				context = objectKey
			}

		case ".profile_pictures":
			params.Log.Debug("TODO: process profile pictures")

		case ".contacts.list":
			dec.Token() //nolint:errcheck // consume array opening '['

			for dec.More() {
				var c contact
				if err = dec.Decode(&c); err != nil {
					return err
				}

				if strings.HasPrefix(c.PhoneNumber, "001") {
					// I have no idea why there's an extra 00 here but it messes up the phone number parser
					c.PhoneNumber = strings.TrimPrefix(c.PhoneNumber, "00")
				}

				params.Pipeline <- &timeline.Graph{
					Entity: &timeline.Entity{
						Name: c.FirstName + " " + c.LastName,
						Attributes: []timeline.Attribute{
							{
								Name:        timeline.AttributePhoneNumber,
								Value:       c.PhoneNumber,
								Identifying: true,
							},
						},
					},
				}
			}

			dec.Token() //nolint:errcheck // consume closing ']'
			if context == objectValue {
				nesting = nesting[:len(nesting)-1]
				context = objectKey
			}

		case ".chats.list.type",
			".chats.list.id",
			".chats.list.messages":
			seenChats = true
			if len(ownerEntity.Attributes) == 0 {
				return nil // have to get owner account info first
			}

			if scope == ".chats.list.type" {
				next, err := dec.Token()
				if err != nil {
					return err
				}
				currentChatType = next.(string)

				nesting = nesting[:len(nesting)-1]
				context = objectKey
				return nil
			}
			if scope == ".chats.list.id" {
				next, err := dec.Token()
				if err != nil {
					return err
				}
				currentChatEntity.Attributes = []timeline.Attribute{
					{
						Name:     "telegram_id",
						Value:    strconv.FormatFloat(next.(float64), 'f', -1, 64),
						Identity: true,
					},
				}
				nesting = nesting[:len(nesting)-1]
				context = objectKey
				return nil
			}

			if currentChatType != "personal_chat" {
				params.Log.Warn("chat type not yet supported", zap.String("chat_type", currentChatType))
				return nil
			}

			dec.Token() //nolint:errcheck // consume opening '['

			for dec.More() {
				var m message
				if err = dec.Decode(&m); err != nil {
					return err
				}
				m.fromID = strings.TrimPrefix(m.FromID, "user")

				// TODO: I'm not sure what other types there are, yet
				if m.Type != "message" {
					params.Log.Warn("TODO: Types other than 'message' not yet supported", zap.String("type", m.Type))
					continue
				}

				var sb strings.Builder
				for _, t := range m.TextEntities {
					sb.WriteString(t.Text)
				}

				dateTime, err := time.Parse("2006-01-02T15:04:05", m.Date)
				if err != nil {
					return err
				}

				// figure out who the message is sending and receiving the message
				var from, to timeline.Entity
				if m.fromID == ownerEntity.AttributeValue("telegram_id") {
					from = ownerEntity
					to = currentChatEntity
				} else {
					currentChatEntity.Name = m.From
					from = currentChatEntity
					to = ownerEntity
				}

				it := &timeline.Item{
					// TODO: Telegram has message IDs, but they're autoincrement, and I don't trust that they're consistent across exports
					Classification: timeline.ClassMessage,
					Timestamp:      dateTime,
					Owner:          from,
					Content: timeline.ItemData{
						Data: timeline.StringData(sb.String()),
					},
				}

				ig := &timeline.Graph{Item: it}
				ig.ToEntity(timeline.RelSent, &to)

				params.Pipeline <- ig
			}

			dec.Token() //nolint:errcheck // consume closing ']'
			if context == objectValue {
				nesting = nesting[:len(nesting)-1]
				context = objectKey
			}
		}

		return nil
	}

	////

	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		context = ""
		if len(nesting) > 0 {
			above := nesting[len(nesting)-1]
			if v, ok := above.Token.(json.Delim); ok {
				switch v {
				case '{':
					context = objectKey
				case '[':
					context = "array"
				}
			} else if above.context == objectKey {
				context = objectValue
			}
		}

		if context != objectKey {
			// if not an object key, let handler get it
			if err := handler(nesting.String(), dec); err != nil {
				return err
			}
		}

		////////

		next, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			params.Log.Error("decoding JSON archive object",
				zap.String("filename", path.Join(dirEntry.Filename, jsonFilename)),
				zap.Error(err))
			return nil
		}
		tkn := jsonToken{Token: next, context: context}

		if v, ok := tkn.Token.(json.Delim); ok {
			switch v {
			case '{', '[':
				nesting = append(nesting, tkn)
			case '}', ']':
				nesting = nesting[:len(nesting)-1]
				if len(nesting) > 0 {
					priorContext := nesting[len(nesting)-1].context
					if priorContext != "array" && priorContext != objectValue {
						nesting = nesting[:len(nesting)-1]
					}
				}
			}
		} else if context == objectKey {
			nesting = append(nesting, tkn)
		} else if context == objectValue {
			nesting = nesting[:len(nesting)-1]
		}
	}

	return nil
}

// Options contains provider-specific options for using this data source.
type Options struct {
	// remember to use JSON tags
}

const (
	objectValue = "object_value"
	objectKey   = "object_key"
)
