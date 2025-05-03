// Imports from individual WhatsApp conversation exports: https://faq.whatsapp.com/1180414079177245/
package whatsapp

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/timelinize/timelinize/timeline"
	"go.uber.org/zap"
)

func init() {
	err := timeline.RegisterDataSource(timeline.DataSource{
		Name:            "whatsapp",
		Title:           "WhatsApp",
		Icon:            "whatsapp.svg",
		Description:     "A WhatsApp Chat Export containing (most of the) information from a chat between a fixed set of people",
		NewFileImporter: func() timeline.FileImporter { return new(Importer) },
	})
	if err != nil {
		timeline.Log.Fatal("registering data source", zap.Error(err))
	}
}

// Importer can import the data from a zip file or folder.
type Importer struct{}

// Recognize returns whether the input is supported.
func (Importer) Recognize(_ context.Context, dirEntry timeline.DirEntry, _ timeline.RecognizeParams) (timeline.Recognition, error) {
	if dirEntry.FileExists(chatPath) {
		return timeline.Recognition{Confidence: 1}, nil
	}
	return timeline.Recognition{}, nil
}

// FileImport imports data from the file or folder.
func (i *Importer) FileImport(_ context.Context, dirEntry timeline.DirEntry, params timeline.ImportParams) error {
	file, err := dirEntry.FS.Open(chatPath)
	if err != nil {
		return fmt.Errorf("loading chat: %w", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Split(chatSplit)

	// We have to store all and emit later, because we have no way of knowing
	// who's in the conversation until we've parsed it
	var messages []*timeline.Graph
	recipients := make(map[string]timeline.Entity)

	for scanner.Scan() {
		messageLine := scanner.Text()

		sub := messageStartRegex.FindStringSubmatch(messageLine)

		timestamp, err := time.Parse(time.DateTime, sub[2]+" "+sub[3])
		if err != nil {
			return fmt.Errorf("unable to convert '%s %s' into a time", sub[2], sub[3])
		}

		content := strings.Split(messageLine[len(sub[0])-1:], "\u200E")
		name := sub[4]

		owner := timeline.Entity{
			Name: name,
			Attributes: []timeline.Attribute{
				{
					Name:     "WhatsApp Name",
					Value:    name,
					Identity: true, // the data export only gives us this info for the owner, so it is the identity, but only for this user
				},
			},
		}

		message := &timeline.Graph{
			Item: &timeline.Item{
				Classification: timeline.ClassMessage,
				Timestamp:      timestamp,
				Owner:          owner,
			},
		}

		hasAttachments := false
		// First item is always the message, subsequent are context notes & attachment declarations
		for _, part := range content[1:] {
			// We're dropping non-attachment context notes, which are announcements about changes in phone number, or the number of pages of a PDF

			if filename, isAttachment := strings.CutPrefix(part, attachPrefix); isAttachment {
				filename = strings.TrimSuffix(strings.TrimSpace(filename), attachSuffix)

				message.ToItem(timeline.RelAttachment, &timeline.Item{
					Classification: timeline.ClassMessage,
					Timestamp:      timestamp,
					Owner:          owner,
					Content: timeline.ItemData{
						Filename: filename,
						Data: func(_ context.Context) (io.ReadCloser, error) {
							return dirEntry.FS.Open(filename)
						},
					},
				})
				hasAttachments = true
			}
		}

		// Note: WhatsApp chat exports currently (2025-06-03) totally omit text sent as a caption to an attachment, so these are never included in the data
		// However, for non-image files, this part of the line often includes the original filename instead, which we don't want to keep
		if !hasAttachments {
			messageText := strings.TrimSpace(content[0])
			// Skip messages context-only messages
			if messageText == "" {
				// TODO: This includes context like "you deleted this message"; do we want to keep those?
				continue
			}

			message.Item.Content = timeline.ItemData{
				Data: timeline.StringData(messageText),
			}
		}

		// Record all recipients for messages we're keeping
		// We'll tag all messages with all recipients later
		if _, ok := recipients[name]; !ok {
			recipients[name] = owner
		}
		messages = append(messages, message)
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("unable to scan chats file: %w", err)
	}

	// Mark all messages as being sent to the non-sender entities encountered so far
	// Note: If there is a silent member of the group, there's no way to know they are present from WhatsApp's exports
	for _, message := range messages {
		for _, recipient := range recipients {
			if recipient.Name != message.Item.Owner.Name {
				message.ToEntity(timeline.RelSent, &recipient)
			}
		}
		params.Pipeline <- message
	}

	return nil
}

const (
	chatPath     = "_chat.txt"
	attachPrefix = "<attached: "
	attachSuffix = ">"
)
