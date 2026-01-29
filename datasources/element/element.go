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

// Package element implements a data source for Element/Matrix chat exports.
package element

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"strings"
	"time"

	"github.com/timelinize/timelinize/timeline"
	"go.uber.org/zap"
)

func init() {
	err := timeline.RegisterDataSource(timeline.DataSource{
		Name:            "element_chat",
		Title:           "Element Chat",
		Icon:            "element.svg",
		Description:     "Element/Matrix chat room export from the Element client",
		NewFileImporter: func() timeline.FileImporter { return new(FileImporter) },
	})
	if err != nil {
		timeline.Log.Fatal("registering data source", zap.Error(err))
	}
}

// FileImporter implements the timeline.FileImporter interface.
type FileImporter struct{}

// Recognize returns whether the file is supported.
func (FileImporter) Recognize(_ context.Context, dirEntry timeline.DirEntry, _ timeline.RecognizeParams) (timeline.Recognition, error) {
	// Look for export.json file which is characteristic of Element exports
	f, err := dirEntry.FS.Open(ChatPath)
	if err != nil {
		return timeline.Recognition{}, nil
	}
	defer f.Close()

	// Try to parse the beginning to see if it looks like an Element export
	limitedReader := io.LimitReader(f, 200)
	content := make([]byte, 200)
	n, err := limitedReader.Read(content)
	if err != nil && err != io.EOF {
		return timeline.Recognition{}, nil
	}

	if !strings.Contains(string(content[:n]), `"room_name"`) {
		return timeline.Recognition{}, nil
	}

	return timeline.Recognition{Confidence: 1.0}, nil
}

// FileImport imports data from the Element export file.
func (fi *FileImporter) FileImport(ctx context.Context, dirEntry timeline.DirEntry, params timeline.ImportParams) error {
	f, err := dirEntry.FS.Open(ChatPath)
	if err != nil {
		return fmt.Errorf("opening %s: %w", ChatPath, err)
	}
	defer f.Close()

	var export elementExport
	if err := json.NewDecoder(f).Decode(&export); err != nil {
		return fmt.Errorf("decoding %s: %w", ChatPath, err)
	}

	// First pass: collect all unique participants
	participants := make(map[string]timeline.Entity)
	for _, msg := range export.Messages {
		if msg.Type == "m.room.message" && msg.Sender != "" {
			if _, exists := participants[msg.Sender]; !exists {
				// Extract display name from Matrix ID
				displayName := extractDisplayName(msg.Sender)

				participants[msg.Sender] = timeline.Entity{
					Name: displayName,
					Attributes: []timeline.Attribute{
						{
							Name:     "matrix_id",
							Value:    msg.Sender,
							Identity: true,
						},
					},
				}
			}
		}
	}

	// Second pass: process messages
	for _, msg := range export.Messages {
		if err := ctx.Err(); err != nil {
			return err
		}

		// Skip non-message
		if msg.Type != "m.room.message" {
			continue
		}

		// Parse timestamp (Matrix timestamps are in milliseconds)
		timestamp := time.UnixMilli(msg.OriginServerTs)

		// Get sender entity
		sender, exists := participants[msg.Sender]
		if !exists {
			// This shouldn't happen after our first pass, but just in case
			displayName := extractDisplayName(msg.Sender)
			sender = timeline.Entity{
				Name: displayName,
				Attributes: []timeline.Attribute{
					{
						Name:     "matrix_id",
						Value:    msg.Sender,
						Identity: true,
					},
				},
			}
		}

		// Handle different message types
		var messageText string
		var attachment *timeline.Item

		if msg.Content != nil {
			switch msg.Content.MsgType {
			case "m.text":
				messageText = msg.Content.Body
				// For HTML formatted messages, we could use FormattedBody, but plain text is safer for now
			case "m.image", "m.file", "m.video", "m.audio":
				// Create attachment item
				attachment = createAttachmentItem(msg, timestamp, sender, dirEntry)
				// Use body as message text (often a caption or filename)
				messageText = msg.Content.Body
			default:
				messageText = msg.Content.Body
			}

			// Skip messages with no text and no attachment
			if messageText == "" && attachment == nil {
				continue
			}

			// Create the message item
			item := &timeline.Item{
				ID:             msg.EventID,
				Classification: timeline.ClassMessage,
				Timestamp:      timestamp,
				Owner:          sender,
			}

			// Set content only if there's message text
			if messageText != "" {
				item.Content = timeline.ItemData{
					Data: timeline.StringData(messageText),
				}
			}

			// Add metadata
			metadata := timeline.Metadata{
				"matrix_event_id": msg.EventID,
				"matrix_room_id":  msg.RoomID,
				"room_name":       export.RoomName,
			}

			if msg.Content != nil && msg.Content.MsgType != "" {
				metadata["message_type"] = msg.Content.MsgType
			}

			if export.Topic != "" {
				metadata["room_topic"] = export.Topic
			}

			// Add formatted content if available
			if msg.Content != nil && msg.Content.FormattedBody != "" && msg.Content.Format != "" {
				metadata["formatted_body"] = msg.Content.FormattedBody
				metadata["format"] = msg.Content.Format
			}

			item.Metadata = metadata

			// Create graph with the item
			graph := &timeline.Graph{Item: item, Checkpoint: msg}

			// Add attachment relationship if present
			if attachment != nil {
				graph.ToItem(timeline.RelAttachment, attachment)
			}

			// Add relationships to all other participants in the room
			for participantID, participant := range participants {
				if participantID != msg.Sender {
					participantCopy := participant
					graph.ToEntity(timeline.RelSent, &participantCopy)
				}
			}

			// Send to pipeline
			params.Pipeline <- graph
		}
	}

	return nil
}

// extractDisplayName extracts a human-readable display name from a Matrix ID
func extractDisplayName(matrixID string) string {
	// Matrix IDs are in the format @username:homeserver.com
	// Extract the username part (without @ prefix)
	if len(matrixID) > 1 && matrixID[0] == '@' {
		if colonPos := strings.Index(matrixID, ":"); colonPos > 1 {
			username := matrixID[1:colonPos]
			return username
		}
	}
	// Fallback to the full Matrix ID if we can't parse it
	return matrixID
}

// createAttachmentItem creates a timeline item for media attachments
func createAttachmentItem(msg elementMessage, timestamp time.Time, owner timeline.Entity, dirEntry timeline.DirEntry) *timeline.Item {
	if msg.Content == nil {
		return nil
	}

	// Try to find the actual file in the appropriate directory
	// Element exports use different folders: images/, videos/, audio/, files/
	// Naming patterns: image-{timestamp}.jpg, video-{timestamp}.mp4,
	// Voice message-{timestamp}.ogg, file-{timestamp} (plus original filenames)
	var possibleFilenames []string

	// Format timestamp as it appears in filenames: "10-24-2025 at 2-20-32 PM"
	timeStr := timestamp.Format("1-2-2006 at 3-04-05 PM")

	// Element exports use consistent patterns based on content type
	switch msg.Content.MsgType {
	case "m.image":
		// Regular images are "image.jpg" -> "image-{timestamp}.jpg"
		possibleFilenames = append(possibleFilenames,
			fmt.Sprintf("images/image-%s.jpg", timeStr))
	case "m.video":
		// Videos use same pattern as images but in videos folder
		possibleFilenames = append(possibleFilenames,
			fmt.Sprintf("videos/video-%s.mp4", timeStr))
	case "m.audio":
		// Audio files are "Voice message-{timestamp}.ogg"
		possibleFilenames = append(possibleFilenames,
			fmt.Sprintf("audio/Voice message-%s.ogg", timeStr))
	case "m.file":
		// Generic files use same pattern as images but in files folder
		possibleFilenames = append(possibleFilenames,
			fmt.Sprintf("files/file-%s", timeStr))
	}

	// Also try descriptive filename pattern if body is not generic
	if msg.Content.Body != "" && msg.Content.Body != "image.jpg" && msg.Content.Body != "video.mp4" {
		// Use appropriate directory based on content type
		var dir string
		switch msg.Content.MsgType {
		case "m.image":
			dir = "images"
		case "m.video":
			dir = "videos"
		case "m.audio":
			dir = "audio"
		case "m.file":
			dir = "files"
		default:
			dir = "images" // fallback
		}

		// Try the full body text as filename
		possibleFilenames = append(possibleFilenames,
			fmt.Sprintf("%s/%s-%s", dir, msg.Content.Body, timeStr))

		// Try with file extension removed from body (for cases like "1000028804.jpg")
		if strings.Contains(msg.Content.Body, ".") {
			bodyWithoutExt := strings.TrimSuffix(msg.Content.Body, filepath.Ext(msg.Content.Body))
			possibleFilenames = append(possibleFilenames,
				fmt.Sprintf("%s/%s-%s", dir, bodyWithoutExt, timeStr))

			// Add appropriate extension back based on actual patterns seen in exports
			switch msg.Content.MsgType {
			case "m.image":
				possibleFilenames = append(possibleFilenames,
					fmt.Sprintf("%s/%s-%s.jpg", dir, bodyWithoutExt, timeStr))
				// Also try with original extension preserved
				originalExt := filepath.Ext(msg.Content.Body)
				if originalExt != "" {
					possibleFilenames = append(possibleFilenames,
						fmt.Sprintf("%s/%s-%s%s", dir, bodyWithoutExt, timeStr, originalExt))
				}
			case "m.video":
				possibleFilenames = append(possibleFilenames,
					fmt.Sprintf("%s/%s-%s.mp4", dir, bodyWithoutExt, timeStr))
			}
		}

		// Try truncated body text (Element may truncate long filenames)
		if len(msg.Content.Body) > 50 {
			// Try first part of body text - Element seems to cut at sentence boundaries
			truncated := msg.Content.Body

			// Look for natural break points first
			breakPoints := []string{". ", ".\n", "!", "?"}
			for _, bp := range breakPoints {
				if idx := strings.Index(truncated, bp); idx > 0 && idx < 50 {
					truncated = truncated[:idx]
					break
				}
			}

			// If no natural break point, truncate at 50 chars
			if len(truncated) > 50 {
				truncated = truncated[:50]
			}

			// Remove any trailing punctuation or whitespace
			truncated = strings.TrimRight(truncated, " .!?,:;-\n\r")

			possibleFilenames = append(possibleFilenames,
				fmt.Sprintf("%s/%s-%s", dir, truncated, timeStr))
		}

		// Special case: Try body text with common transformations Element makes
		cleanBody := msg.Content.Body
		// Replace newlines with spaces or remove them
		cleanBody = strings.ReplaceAll(cleanBody, "\n", " ")
		cleanBody = strings.ReplaceAll(cleanBody, "\r", " ")
		// Collapse multiple spaces
		for strings.Contains(cleanBody, "  ") {
			cleanBody = strings.ReplaceAll(cleanBody, "  ", " ")
		}
		cleanBody = strings.TrimSpace(cleanBody)

		if cleanBody != msg.Content.Body && cleanBody != "" {
			possibleFilenames = append(possibleFilenames,
				fmt.Sprintf("%s/%s-%s", dir, cleanBody, timeStr))
		}
	}

	// Also try the original filename if specified
	if msg.Content.Filename != "" {
		// Use appropriate directory based on content type
		var dir string
		switch msg.Content.MsgType {
		case "m.image":
			dir = "images"
		case "m.video":
			dir = "videos"
		case "m.audio":
			dir = "audio"
		case "m.file":
			dir = "files"
		}
		possibleFilenames = append(possibleFilenames,
			fmt.Sprintf("%s/%s", dir, msg.Content.Filename))
	}

	// Try some variations with different extensions
	for _, base := range possibleFilenames {
		candidates := []string{base}
		// Try without extension first, then with common extensions
		if !strings.Contains(base, ".") {
			switch msg.Content.MsgType {
			case "m.image":
				candidates = append(candidates, base+".jpg", base+".png", base+".jpeg")
			case "m.video":
				candidates = append(candidates, base+".mp4", base+".mov", base+".avi")
			case "m.audio":
				candidates = append(candidates, base+".ogg", base+".mp3", base+".wav")
			default:
				candidates = append(candidates, base+".jpg") // fallback
			}
		}

		// Also try with dot and space variations (Element sometimes modifies special chars)
		if strings.Contains(base, ".") && !strings.HasSuffix(base, ".jpg") && !strings.HasSuffix(base, ".png") && !strings.HasSuffix(base, ".mp4") && !strings.HasSuffix(base, ".ogg") {
			spaceVersion := strings.ReplaceAll(base, ".", " ")
			hyphenVersion := strings.ReplaceAll(base, ".", "-")
			candidates = append(candidates, spaceVersion, hyphenVersion)
		}

		for _, filename := range candidates {
			if timeline.FileExistsFS(dirEntry.FS, filename) {
				return createAttachmentItemWithFile(filename, timestamp, owner, msg, dirEntry.FS)
			}
		}
	}

	// If we can't find the file, create a placeholder attachment
	return createAttachmentItemWithFile("", timestamp, owner, msg, dirEntry.FS)
}

// createAttachmentItemWithFile creates the actual attachment item
func createAttachmentItemWithFile(filename string, timestamp time.Time, owner timeline.Entity, msg elementMessage, filesystem fs.FS) *timeline.Item {

	item := &timeline.Item{
		ID:             msg.EventID + "-attachment",
		Classification: timeline.ClassMessage, // Could be ClassFile or ClassMedia in the future
		Timestamp:      timestamp,
		Owner:          owner,
	}

	// Set up metadata
	metadata := timeline.Metadata{
		"matrix_event_id": msg.EventID,
		"matrix_url":      msg.Content.URL,
		"message_type":    msg.Content.MsgType,
	}

	if msg.Content.Info != nil {
		if msg.Content.Info.MimeType != "" {
			metadata["mime_type"] = msg.Content.Info.MimeType
		}
		if msg.Content.Info.Size > 0 {
			metadata["file_size"] = msg.Content.Info.Size
		}
		if msg.Content.Info.Width > 0 {
			metadata["width"] = msg.Content.Info.Width
		}
		if msg.Content.Info.Height > 0 {
			metadata["height"] = msg.Content.Info.Height
		}
	}

	item.Metadata = metadata

	// Set up file content
	if filename != "" {
		// Remove directory prefix to get just the filename
		displayFilename := filename
		if idx := strings.LastIndex(filename, "/"); idx != -1 {
			displayFilename = filename[idx+1:]
		}
		item.Content = timeline.ItemData{
			Filename: displayFilename,
			Data: func(ctx context.Context) (io.ReadCloser, error) {
				return filesystem.Open(filename)
			},
		}
	} else {
		// Placeholder for missing file - use generic name based on timestamp and type
		placeholderFilename := msg.Content.Filename
		if placeholderFilename == "" || IsGenericImageName(msg.Content.Body) {
			// Create timestamp-based filename based on content type
			timeStr := timestamp.Format("1-2-2006 at 3-04-05 PM")
			switch msg.Content.MsgType {
			case "m.image":
				placeholderFilename = fmt.Sprintf("image-%s.jpg", timeStr)
			case "m.video":
				placeholderFilename = fmt.Sprintf("video-%s.mp4", timeStr)
			case "m.audio":
				placeholderFilename = fmt.Sprintf("Voice message-%s.ogg", timeStr)
			case "m.file":
				placeholderFilename = fmt.Sprintf("file-%s", timeStr)
			}
		}
		item.Content = timeline.ItemData{
			Filename: placeholderFilename,
			Data:     timeline.StringData(fmt.Sprintf("Missing attachment: %s", msg.Content.Body)),
		}
	}

	return item
}

// IsGenericImageName checks if the body/filename is a generic name like "image.jpg", "video.mp4", etc.
func IsGenericImageName(name string) bool {
	if name == "" {
		return true
	}

	// Element exports use consistent generic patterns for all media types
	lowerName := strings.ToLower(name)
	return lowerName == "image.jpg" ||
		lowerName == "video.mp4" ||
		strings.HasPrefix(lowerName, "voice message") ||
		lowerName == "file"
}

// elementExport represents the structure of an Element chat export
type elementExport struct {
	RoomName    string           `json:"room_name"`
	RoomCreator string           `json:"room_creator"`
	Topic       string           `json:"topic"`
	ExportDate  string           `json:"export_date"`
	ExportedBy  string           `json:"exported_by"`
	Messages    []elementMessage `json:"messages"`
}

// elementMessage represents a single message in the export
type elementMessage struct {
	Type           string                 `json:"type"`
	Sender         string                 `json:"sender"`
	Content        *elementMessageContent `json:"content,omitempty"`
	RoomID         string                 `json:"room_id"`
	OriginServerTs int64                  `json:"origin_server_ts"`
	EventID        string                 `json:"event_id"`
	UserID         string                 `json:"user_id"`
	Age            int64                  `json:"age,omitempty"`
}

// elementMessageContent represents the content of a message
type elementMessageContent struct {
	MsgType       string                 `json:"msgtype"`
	Body          string                 `json:"body"`
	Format        string                 `json:"format,omitempty"`
	FormattedBody string                 `json:"formatted_body,omitempty"`
	Mentions      map[string]interface{} `json:"m.mentions,omitempty"`
	Filename      string                 `json:"filename,omitempty"`
	URL           string                 `json:"url,omitempty"`
	Info          *elementMediaInfo      `json:"info,omitempty"`
}

// elementMediaInfo represents media information for attachments
type elementMediaInfo struct {
	Height   int64  `json:"h,omitempty"`
	Width    int64  `json:"w,omitempty"`
	MimeType string `json:"mimetype,omitempty"`
	Size     int64  `json:"size,omitempty"`
}

const ChatPath = "export.json"
