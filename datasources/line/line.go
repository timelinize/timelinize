// Package line implements a data source for LINE chat exports. https://help.line.me/line/ios/?contentId=20007388
package line

import (
	"bufio"
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/timelinize/timelinize/timeline"
	"go.uber.org/zap"
)

func init() {
	err := timeline.RegisterDataSource(timeline.DataSource{
		Name:            "line",
		Title:           "LINE",
		Icon:            "line.svg",
		Description:     "A LINE Chat Export containing messages from a conversation. Note: LINE exports only include text, media content such as images and stickers are represented as placeholder text only.",
		NewFileImporter: func() timeline.FileImporter { return new(Importer) },
	})
	if err != nil {
		timeline.Log.Fatal("registering data source", zap.Error(err))
	}
}

// Importer can import the data from a LINE chat export file.
type Importer struct{}

// Recognize returns whether the input is supported.
func (Importer) Recognize(_ context.Context, dirEntry timeline.DirEntry, _ timeline.RecognizeParams) (timeline.Recognition, error) {
	// Check if it's a directory (only if we have the DirEntry set)
	if dirEntry.DirEntry != nil && dirEntry.IsDir() {
		return timeline.Recognition{}, nil
	}

	// LINE exports are single text files
	file, err := dirEntry.Open("")
	if err != nil {
		return timeline.Recognition{}, nil
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	if scanner.Scan() {
		firstLine := scanner.Text()
		// Strip BOM if present
		firstLine = strings.TrimPrefix(firstLine, "\ufeff")
		// Check for LINE header (German or English)
		if strings.Contains(firstLine, "[LINE] ") || strings.HasPrefix(firstLine, "Chat history ") {
			return timeline.Recognition{Confidence: 0.95}, nil
		}
	}

	return timeline.Recognition{}, nil
}

// FileImport imports data from the LINE chat export file.
func (i *Importer) FileImport(ctx context.Context, dirEntry timeline.DirEntry, params timeline.ImportParams) error {
	filename := dirEntry.Filename
	if filename == "" || filename == "." {
		return fmt.Errorf("no file specified")
	}

	file, err := dirEntry.FS.Open(filename)
	if err != nil {
		return fmt.Errorf("opening chat file: %w", err)
	}
	defer file.Close()

	// Get system timezone for timestamp interpretation
	systemTimezone := time.Local

	scanner := bufio.NewScanner(file)

	// Parse header to check if this is a group chat or one-on-one
	var isGroupChat bool
	var otherParticipantName string
	if scanner.Scan() {
		headerLine := scanner.Text()
		// Strip BOM if present
		headerLine = strings.TrimPrefix(headerLine, "\ufeff")
		isGroupChat, otherParticipantName = parseHeaderLine(headerLine)
	}

	// Skip remaining header lines
	// Line 2: Gespeichert um: <timestamp>  OR  Saved on: <timestamp>
	// Line 3: Empty line
	for i := 0; i < 2 && scanner.Scan(); i++ {
		// Skip these header lines
	}

	// We store all messages and emit later, because we need to know
	// all participants before we can establish relationships
	var messages []*timeline.Graph
	recipients := make(map[string]timeline.Entity)

	// Add the other participant from the header if it's a one-on-one chat
	// For group chats, we'll discover participants from the messages themselves
	if !isGroupChat && otherParticipantName != "" {
		recipients[otherParticipantName] = timeline.Entity{
			Name: otherParticipantName,
			Attributes: []timeline.Attribute{
				{
					Name:     "line_name",
					Value:    otherParticipantName,
					Identity: true,
				},
			},
		}
	}

	var currentDate string
	var currentMessage *parsedMessage

	for scanner.Scan() {
		line := scanner.Text()

		// Check if this is a date line
		if IsDateLine(line) {
			currentDate = line
			continue
		}

		// Try to parse as a system message first (single tab)
		if sysMsg, ok := parseSystemMessage(line, currentDate, systemTimezone); ok {
			// Process any pending message first
			if currentMessage != nil {
				message := createMessageGraph(currentMessage)
				if message != nil {
					messages = append(messages, message)
					if _, exists := recipients[currentMessage.sender]; !exists {
						recipients[currentMessage.sender] = currentMessage.owner
					}
				}
				currentMessage = nil
			}
			// Add system message directly
			messages = append(messages, &sysMsg)
			continue
		}

		// Check if this is a regular message line (two tabs)
		if msg, ok := parseMessageLine(line, currentDate, systemTimezone); ok {
			// If we had a previous message with accumulated content, process it
			if currentMessage != nil {
				message := createMessageGraph(currentMessage)
				if message != nil {
					messages = append(messages, message)
					if _, exists := recipients[currentMessage.sender]; !exists {
						recipients[currentMessage.sender] = currentMessage.owner
					}
				}
			}
			currentMessage = &msg
		} else if currentMessage != nil {
			// This is a continuation of the previous message (multi-line)
			if line != "" {
				currentMessage.content += "\n" + line
			}
		}
	}

	// Process the last message
	if currentMessage != nil {
		message := createMessageGraph(currentMessage)
		if message != nil {
			messages = append(messages, message)
			if _, exists := recipients[currentMessage.sender]; !exists {
				recipients[currentMessage.sender] = currentMessage.owner
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("scanning file: %w", err)
	}

	// Now link all messages to all recipients
	for _, message := range messages {
		for _, recipient := range recipients {
			// Don't send a message to yourself
			if recipient.Name != message.Item.Owner.Name {
				message.ToEntity(timeline.RelSent, &recipient)
			}
		}

		select {
		case params.Pipeline <- message:
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	return nil
}

// parsedMessage holds the parsed data from a LINE message.
type parsedMessage struct {
	timestamp time.Time
	sender    string
	content   string
	owner     timeline.Entity
}

// Date line pattern: German: <DayAbbrev>., DD.MM.YYYY  or  English: <DayAbbrev>, M/D/YYYY
// German examples: "Do., 29.8.2019", "Di., 15.10.2019", "Mi., 1.1.2020"
// English examples: "Mon, 8/19/2019", "Tue, 8/20/2019", "Fri, 8/16/2019"
var dateLineRegexGerman = regexp.MustCompile(`^[A-Za-zäöüÄÖÜßéèêàâçñ]+\.,\s+\d{1,2}\.\d{1,2}\.\d{4}$`)
var dateLineRegexEnglish = regexp.MustCompile(`^[A-Za-z]+,\s+\d{1,2}/\d{1,2}/\d{4}$`)

// IsDateLine checks if a line is a date header.
func IsDateLine(line string) bool {
	return dateLineRegexGerman.MatchString(line) || dateLineRegexEnglish.MatchString(line)
}

var messageLineRegex = regexp.MustCompile(`^(\d{1,2}:\d{2})\t(.+?)\t(.*)$`)

func parseMessageLine(line, currentDate string, tz *time.Location) (parsedMessage, bool) {
	matches := messageLineRegex.FindStringSubmatch(line)
	if matches == nil {
		return parsedMessage{}, false
	}

	timeStr := matches[1]
	sender := matches[2]
	content := matches[3]

	timestamp, err := ParseTimestamp(currentDate, timeStr, tz)
	if err != nil {
		// Log error but don't fail the entire import
		return parsedMessage{}, false
	}

	owner := timeline.Entity{
		Name: sender,
		Attributes: []timeline.Attribute{
			{
				Name:     "line_name",
				Value:    sender,
				Identity: true,
			},
		},
	}

	return parsedMessage{
		timestamp: timestamp,
		sender:    sender,
		content:   content,
		owner:     owner,
	}, true
}

// System message line pattern: HH:MM<TAB>System message text
// Example: "14:02\tJohn left the chat."
var systemMessageRegex = regexp.MustCompile(`^(\d{1,2}:\d{2})\t(.+)$`)

func parseSystemMessage(line, currentDate string, tz *time.Location) (timeline.Graph, bool) {
	matches := systemMessageRegex.FindStringSubmatch(line)
	if matches == nil {
		return timeline.Graph{}, false
	}

	timeStr := matches[1]
	systemText := matches[2]

	// System messages must contain certain keywords to be recognized
	// This prevents false positives from malformed regular messages
	if !isSystemMessage(systemText) {
		return timeline.Graph{}, false
	}

	// Parse the timestamp
	timestamp, err := ParseTimestamp(currentDate, timeStr, tz)
	if err != nil {
		return timeline.Graph{}, false
	}

	return timeline.Graph{
		Item: &timeline.Item{
			Classification: timeline.ClassNote,
			Timestamp:      timestamp,
			Content: timeline.ItemData{
				Data: timeline.StringData(systemText),
			},
		},
	}, true
}

// isSystemMessage checks if text is a system message based on known patterns.
func isSystemMessage(text string) bool {
	// German patterns
	if strings.Contains(text, " hat die Gruppe verlassen") ||
		strings.Contains(text, " hat den Chat verlassen") ||
		strings.Contains(text, " der Gruppe hinzugefügt") ||
		strings.Contains(text, " ist der Gruppe beigetreten") {
		return true
	}

	// English patterns - check for actual system events, not user actions
	if strings.Contains(text, " left the group") ||
		strings.Contains(text, " left the chat") ||
		strings.Contains(text, " joined the group") ||
		strings.Contains(text, " removed ") { // "User removed User2"
		return true
	}

	// Check for "added" in group context (adding members, not album items)
	// System message: "Elina added jacqueline"
	// User message: "Roro羅雅暄Linda added items to an album."
	if strings.Contains(text, " added ") &&
		!strings.Contains(text, " added items") &&
		!strings.Contains(text, " added to ") &&
		!strings.Contains(text, " created ") {
		return true
	}

	return false
}

// ParseTimestamp parses a LINE timestamp from date and time strings.
// German date format: <DayAbbrev>., DD.MM.YYYY (e.g., "Do., 29.8.2019")
// English date format: <DayAbbrev>, M/D/YYYY (e.g., "Mon, 8/19/2019")
// Time format: HH:MM (e.g., "06:44" or "20:27")
func ParseTimestamp(dateLine, timeStr string, tz *time.Location) (time.Time, error) {
	if dateLine == "" {
		return time.Time{}, fmt.Errorf("missing date")
	}

	// Extract date part after the day-of-week abbreviation
	parts := strings.SplitN(dateLine, ", ", 2)
	if len(parts) != 2 {
		return time.Time{}, fmt.Errorf("invalid date format: %s", dateLine)
	}
	dateStr := parts[1]

	// Combine date and time
	dateTimeStr := dateStr + " " + timeStr

	// Try German format first: "DD.MM.YYYY HH:MM"
	timestamp, err := time.ParseInLocation("2.1.2006 15:04", dateTimeStr, tz)
	if err == nil {
		return timestamp, nil
	}

	// Try English format: "M/D/YYYY HH:MM"
	timestamp, err = time.ParseInLocation("1/2/2006 15:04", dateTimeStr, tz)
	if err == nil {
		return timestamp, nil
	}

	return time.Time{}, fmt.Errorf("parsing timestamp '%s': %w", dateTimeStr, err)
}

// parseHeaderLine parses the LINE header to determine if it's a group chat and extract participant info.
// Header formats:
//   - German one-on-one: "[LINE] Chat mit <name>"
//   - German group: "[LINE] Chat in <group_name>"
//   - English one-on-one: "Chat history with <name>"
//   - English group: "Chat history in \"<group_name>\""
//
// Returns (isGroupChat bool, participantName string)
func parseHeaderLine(headerLine string) (bool, string) {
	// Check for group chat indicators
	if strings.Contains(headerLine, "Chat in ") || strings.Contains(headerLine, "Chat history in ") {
		return true, "" // It's a group chat, no single participant
	}

	// Try German one-on-one format: "[LINE] Chat mit <name>"
	if strings.HasPrefix(headerLine, "[LINE] Chat mit ") {
		return false, strings.TrimPrefix(headerLine, "[LINE] Chat mit ")
	}
	// Try English export one-on-one format: "Chat history with <name>"
	if strings.HasPrefix(headerLine, "Chat history with ") {
		return false, strings.TrimPrefix(headerLine, "Chat history with ")
	}

	// Unknown format, assume one-on-one and try to extract name
	if strings.HasPrefix(headerLine, "[LINE] ") || strings.HasPrefix(headerLine, "Chat history ") {
		parts := strings.Fields(headerLine)
		if len(parts) >= 3 {
			return false, strings.Join(parts[2:], " ")
		}
	}

	return false, ""
}

// createMessageGraph creates a timeline.Graph from a parsed message.
func createMessageGraph(msg *parsedMessage) *timeline.Graph {
	// Skip empty messages
	if strings.TrimSpace(msg.content) == "" {
		return nil
	}

	return &timeline.Graph{
		Item: &timeline.Item{
			Classification: timeline.ClassMessage,
			Timestamp:      msg.timestamp,
			Owner:          msg.owner,
			Content: timeline.ItemData{
				Data: timeline.StringData(msg.content),
			},
		},
	}
}
