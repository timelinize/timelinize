// Imports from individual WhatsApp conversation exports: https://faq.whatsapp.com/1180414079177245/
package whatsapp

import (
	"bufio"
	"context"
	"fmt"
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

		var attachment *timeline.Item
		// If a line has an attachment the final \x200E separated column is always about that attachment
		if sub[1] == "\u200E" {
			attachment = attachmentToItem(content[len(content)-1], timestamp, owner, dirEntry)
			content = content[:len(content)-1]
		}

		var rootKey timeline.ItemRetrieval
		// Note: This isn't perfect; if someone renames a contact in their address book then the same message in a new export won't match.
		// We can't use a hash of the content of the message, as the key use-case here is to be able to add in "caption" text, if Meta fixes
		// the bug where caption text is omitted from files when exporting (as adding that omitted caption would alter the hash).
		rootKey.SetKey(fmt.Sprintf("whatsapp-%s-%s", timestamp.Format(time.DateTime), name))

		message := &timeline.Graph{
			Item: &timeline.Item{
				Classification: timeline.ClassMessage,
				Timestamp:      timestamp,
				Owner:          owner,
				Retrieval:      rootKey,
			},
		}

		// Note: WhatsApp chat exports currently (2025-06-03) totally omit text sent as a caption to an attachment, so these are never included in the data
		// However, for non-image files, this part of the line often includes the original filename instead, which we don't want to keep
		var messageText string
		if attachment == nil {
			messageText = strings.TrimSpace(content[0])

			if pollMessage, pollMeta, isPoll := extractPoll(content); isPoll {
				messageText = pollMessage
				message.Item.Metadata = pollMeta
			} else if locationMessage, locationMeta, isLocation := extractLocation(content); isLocation {
				messageText = locationMessage
				message.Item.Metadata = locationMeta
			}

			// Skip messages that have no content & no attachment
			if messageText == "" {
				continue
			}

			message.Item.Content = timeline.ItemData{
				Data: timeline.StringData(messageText),
			}
		} else {
			message.ToItem(timeline.RelAttachment, attachment)
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
