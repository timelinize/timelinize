// Imports from individual WhatsApp conversation exports: https://faq.whatsapp.com/1180414079177245/
package whatsapp

import (
	"bufio"
	"context"
	"fmt"
	"strconv"
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
	// Standard WhatsApp export name
	if dirEntry.FileExists(chatPath) {
		return timeline.Recognition{Confidence: 0.8}, nil
	}

	// Also accept files whose name contains "whatsapp" and ends with .txt (common renames)
	name := strings.ToLower(dirEntry.Name())
	if strings.Contains(name, "whatsapp") && strings.HasSuffix(name, ".txt") {
		return timeline.Recognition{Confidence: 0.8}, nil
	}

	return timeline.Recognition{}, nil
}

// FileImport imports data from the file or folder.
func (i *Importer) FileImport(_ context.Context, dirEntry timeline.DirEntry, params timeline.ImportParams) error {
	// Try default path first, then fall back to the selected file if it's a WhatsApp export
	chatFilePath := chatPath

	file, err := dirEntry.FS.Open(chatFilePath)
	if err != nil {
		if dirEntry.Filename != "" && dirEntry.Filename != "." && !dirEntry.IsDir() {
			chatFilePath = dirEntry.Filename
			file, err = dirEntry.FS.Open(chatFilePath)
		}
	}
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

		h, ok := parseMessageHeader([]byte(messageLine))
		if !ok {
			// Ignore lines that don't look like messages
			continue
		}

		timestamp, err := parseTime(h.Date, h.Time)
		if err != nil {
			return err
		}

		content := strings.Split(messageLine[h.HeaderLen-1:], "\u200E")

		owner := timeline.Entity{
			Name: h.Name,
			Attributes: []timeline.Attribute{
				{
					Name:     "whatsapp_name",
					Value:    h.Name,
					Identity: true, // the data export only gives us this info for the owner, so it is the identity, but only for this user
				},
			},
		}

		var attachment *timeline.Item
		// If a line has an attachment the final \x200E separated column is always about that attachment
		if h.HasLRO {
			attachment = attachmentToItem(content[len(content)-1], timestamp, owner, dirEntry)
			content = content[:len(content)-1]
		}

		message := &timeline.Graph{
			Item: &timeline.Item{
				Classification: timeline.ClassMessage,
				Timestamp:      timestamp,
				Owner:          owner,
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
		if _, ok := recipients[h.Name]; !ok {
			recipients[h.Name] = owner
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

func parseTime(dateStr, timeStr string) (time.Time, error) {
	// Add seconds if missing
	if len(timeStr) == len("15:04") {
		timeStr += ":00"
	}

	yearAtEnd := true
	sep := dateStr[2:3]
	if _, err := strconv.Atoi(sep); err == nil {
		yearAtEnd = false
		sep = dateStr[4:5]
		if _, err := strconv.Atoi(sep); err == nil {
			return time.Time{}, fmt.Errorf("unable to convert '%s %s' into a time", dateStr, timeStr)
		}
	}

	var format string
	if yearAtEnd {
		format = fmt.Sprintf("02%s01%s2006 15:04:05", sep, sep)
	} else {
		format = fmt.Sprintf("2006%s01%s02 15:04:05", sep, sep)
	}

	timestamp, err := time.Parse(format, dateStr+" "+timeStr)
	if err != nil {
		return time.Time{}, fmt.Errorf("unable to convert '%s %s' into a time", dateStr, timeStr)
	}
	return timestamp, nil
}

const (
	chatPath     = "_chat.txt"
	attachPrefix = "<attached: "
	attachSuffix = ">"

	metadataFieldNumber = 1
)
