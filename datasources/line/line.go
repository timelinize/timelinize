// Package line implements a data source for LINE chat exports. https://help.line.me/line/ios/?contentId=20007388
package line

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/timelinize/timelinize/timeline"
	"go.uber.org/zap"
)

// Language represents supported chat export languages.
type Language string

const (
	LanguageAuto    Language = ""
	LanguageEnglish Language = "english"
	LanguageGerman  Language = "german"
)

// Options configures the LINE data source.
type Options struct {
	Language Language `json:"language,omitempty"`
}

// languageConfig holds all language-specific parsing patterns and strings.
type languageConfig struct {
	dateLineRegex        *regexp.Regexp
	dateLayout           string
	headerOneOnOne       string   // Primary prefix for one-on-one chats
	headerGroup          string   // Primary prefix for group chats
	systemLeftPattern    string   // Pattern for "left" messages
	systemJoinedPattern  string   // Pattern for "joined" messages
	systemAddedPattern   string   // Pattern for "added" messages
	systemRemovedPattern string   // Pattern for "removed" messages
	addedExclusions      []string // Patterns to exclude from "added" matches
	savedTimestampText   string
	messageLineRegex     *regexp.Regexp
	systemMessageRegex   *regexp.Regexp
}

var languageConfigs = map[Language]languageConfig{
	LanguageEnglish: {
		dateLineRegex:        regexp.MustCompile(`^[A-Za-z]+,\s+\d{1,2}/\d{1,2}/\d{4}$`),
		dateLayout:           "1/2/2006 15:04",
		headerOneOnOne:       "Chat history with ",
		headerGroup:          "Chat history in ",
		systemLeftPattern:    "left the ",
		systemJoinedPattern:  "joined the group",
		systemAddedPattern:   "added ",
		systemRemovedPattern: "removed ",
		addedExclusions: []string{
			" added items",
			" added to ",
			" created ",
		},
		savedTimestampText: "Saved on:",
		messageLineRegex:   regexp.MustCompile(`^(\d{1,2}:\d{2})\t(.+?)\t(.*)$`),
		systemMessageRegex: regexp.MustCompile(`^(\d{1,2}:\d{2})\t(.+)$`),
	},
	LanguageGerman: {
		dateLineRegex:        regexp.MustCompile(`^[A-Za-zäöüÄÖÜßéèêàâçñ]+\.,\s+\d{1,2}\.\d{1,2}\.\d{4}$`),
		dateLayout:           "2.1.2006 15:04",
		headerOneOnOne:       "[LINE] Chat mit ",
		headerGroup:          "[LINE] Chat in ",
		systemLeftPattern:    "hat die Gruppe verlassen",
		systemJoinedPattern:  "ist der Gruppe beigetreten",
		systemAddedPattern:   "der Gruppe hinzugefügt",
		systemRemovedPattern: "hat den Chat verlassen",
		addedExclusions: []string{
			" created ",
		},
		savedTimestampText: "Gespeichert um:",
		messageLineRegex:   regexp.MustCompile(`^(\d{1,2}:\d{2})\t(.+?)\t(.*)$`),
		systemMessageRegex: regexp.MustCompile(`^(\d{1,2}:\d{2})\t(.+)$`),
	},
}

// isDateLine checks if a line is a date header (auto-detects language).
func isDateLine(line string) bool {
	for _, cfg := range languageConfigs {
		if cfg.dateLineRegex.MatchString(line) {
			return true
		}
	}
	return false
}

func init() {
	err := timeline.RegisterDataSource(timeline.DataSource{
		Name:            "line",
		Title:           "LINE",
		Icon:            "line.svg",
		Description:     "A LINE Chat Export containing messages from a conversation. Note: LINE exports only include text, media content such as images and stickers are represented as placeholder text only.",
		NewOptions:      func() any { return new(Options) },
		NewFileImporter: func() timeline.FileImporter { return new(FileImporter) },
	})
	if err != nil {
		timeline.Log.Fatal("registering data source", zap.Error(err))
	}
}

// FileImporter can import the data from a LINE chat export file.
type FileImporter struct{}

// Recognize returns whether the input is supported.
func (FileImporter) Recognize(_ context.Context, dirEntry timeline.DirEntry, _ timeline.RecognizeParams) (timeline.Recognition, error) {
	// Check if it's a directory (only if we have the DirEntry set)
	if dirEntry.DirEntry != nil && dirEntry.IsDir() {
		return timeline.Recognition{}, nil
	}

	file, err := dirEntry.Open("")
	if err != nil {
		return timeline.Recognition{}, nil
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	if scanner.Scan() {
		firstLine := strings.TrimPrefix(scanner.Text(), "\ufeff")
		for _, cfg := range languageConfigs {
			if strings.HasPrefix(firstLine, cfg.headerOneOnOne) || strings.HasPrefix(firstLine, cfg.headerGroup) {
				return timeline.Recognition{Confidence: 0.95}, nil
			}
		}
	}

	return timeline.Recognition{}, nil
}

// FileImport imports data from the LINE chat export file.
func (fi *FileImporter) FileImport(ctx context.Context, dirEntry timeline.DirEntry, params timeline.ImportParams) error {
	filename := dirEntry.Filename
	if filename == "" || filename == "." {
		return errors.New("no file specified")
	}
	sourceFilename := path.Base(filename)

	file, err := dirEntry.FS.Open(filename)
	if err != nil {
		return fmt.Errorf("opening chat file: %w", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	var headerLine string
	if scanner.Scan() {
		headerLine = strings.TrimPrefix(scanner.Text(), "\ufeff")
	}

	// Read the second line which contains the saved timestamp text
	var savedTimestampLine string
	if scanner.Scan() {
		savedTimestampLine = scanner.Text()
	}

	lang := LanguageAuto
	if params.DataSourceOptions != nil {
		if opt, ok := params.DataSourceOptions.(*Options); ok && opt.Language != LanguageAuto {
			lang = opt.Language
		}
	}
	_, cfg := detectLanguageFromHeaderAndTimestamp(headerLine, savedTimestampLine, lang)

	// Skip blank line after saved timestamp
	scanner.Scan()

	// We store all messages and emit later, because we need to know
	// all participants before we can establish relationships
	var messages []*timeline.Graph
	recipients := make(map[string]timeline.Entity)

	// Add the other participant from the header if it's a one-on-one chat
	// For group chats, we'll discover participants from the messages themselves
	isGroupChat, otherParticipantName := parseHeaderLineLang(headerLine, cfg)
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
	systemTimezone := time.Local
	entryOrdinal := 0

	for scanner.Scan() {
		line := scanner.Text()

		// Check if this is a date line
		if cfg.dateLineRegex.MatchString(line) {
			currentDate = line
			continue
		}

		// Try to parse as a system message first (single tab)
		if sysMsg, ok := parseSystemMessageLang(line, currentDate, systemTimezone, cfg); ok {
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
			if sysMsg.Item != nil {
				entryOrdinal++
				sysMsg.Item.IntermediateLocation = filename
				sysMsg.Item.Content.Filename = sourceFilename
				sysMsg.Item.Retrieval.SetKey(systemMessageRetrievalKey(filename, entryOrdinal, sysMsg.Item.Timestamp, systemText(sysMsg.Item)))
			}
			messages = append(messages, &sysMsg)
			continue
		}

		// Check if this is a regular message line (two tabs)
		if msg, ok := parseMessageLine(line, currentDate, systemTimezone, cfg); ok {
			entryOrdinal++
			msg.ordinal = entryOrdinal
			msg.intermediateLocation = filename
			msg.sourceFilename = sourceFilename

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
		if message.Item != nil &&
			message.Item.Classification.Name == timeline.ClassMessage.Name &&
			message.Item.Owner.Name != "" {
			for _, recipient := range recipients {
				// Don't send a message to yourself
				if recipient.Name != message.Item.Owner.Name {
					message.ToEntity(timeline.RelSent, &recipient)
				}
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
	timestamp            time.Time
	sender               string
	content              string
	ordinal              int
	intermediateLocation string
	sourceFilename       string
	owner                timeline.Entity
}

func parseMessageLine(line, currentDate string, tz *time.Location, cfg languageConfig) (parsedMessage, bool) {
	matches := cfg.messageLineRegex.FindStringSubmatch(line)
	if matches == nil {
		return parsedMessage{}, false
	}

	timeStr := matches[1]
	sender := matches[2]
	content := matches[3]

	timestamp, err := parseTimestampLang(currentDate, timeStr, tz, cfg)
	if err != nil {
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

// parseHeaderLineLang parses the LINE header using the language config.
func parseHeaderLineLang(headerLine string, cfg languageConfig) (bool, string) {
	// Check for group chat header
	if strings.HasPrefix(headerLine, cfg.headerGroup) {
		return true, ""
	}

	// Check for one-on-one chat header
	if strings.HasPrefix(headerLine, cfg.headerOneOnOne) {
		return false, strings.TrimPrefix(headerLine, cfg.headerOneOnOne)
	}

	// Fallback: try to parse generic format
	if strings.HasPrefix(headerLine, "[LINE] ") {
		parts := strings.Fields(headerLine)
		if len(parts) >= 3 {
			return false, strings.Join(parts[2:], " ")
		}
	}
	return false, ""
}

func detectLanguageFromHeaderAndTimestamp(headerLine, savedTimestampLine string, forced Language) (Language, languageConfig) {
	if forced != LanguageAuto {
		if cfg, ok := languageConfigs[forced]; ok {
			return forced, cfg
		}
	}

	// Try to detect from saved timestamp line
	if savedTimestampLine != "" {
		for l, cfg := range languageConfigs {
			if strings.Contains(savedTimestampLine, cfg.savedTimestampText) {
				return l, cfg
			}
		}
	}

	// Fall back to header detection
	for l, cfg := range languageConfigs {
		if strings.HasPrefix(headerLine, cfg.headerOneOnOne) || strings.HasPrefix(headerLine, cfg.headerGroup) {
			return l, cfg
		}
	}
	return LanguageEnglish, languageConfigs[LanguageEnglish]
}

// parseSystemMessageLang checks if text is a system message based on known patterns for the language.
func parseSystemMessageLang(line, currentDate string, tz *time.Location, cfg languageConfig) (timeline.Graph, bool) {
	matches := cfg.systemMessageRegex.FindStringSubmatch(line)
	if matches == nil {
		return timeline.Graph{}, false
	}
	textTime := matches[1]
	systemText := matches[2]

	// Check if it matches any of the main system message patterns
	if cfg.systemLeftPattern != "" && strings.Contains(systemText, cfg.systemLeftPattern) {
		timestamp, err := parseTimestampLang(currentDate, textTime, tz, cfg)
		if err != nil {
			return timeline.Graph{}, false
		}
		return createSystemMessageGraph(systemText, timestamp), true
	}

	if cfg.systemJoinedPattern != "" && strings.Contains(systemText, cfg.systemJoinedPattern) {
		timestamp, err := parseTimestampLang(currentDate, textTime, tz, cfg)
		if err != nil {
			return timeline.Graph{}, false
		}
		return createSystemMessageGraph(systemText, timestamp), true
	}

	if cfg.systemRemovedPattern != "" && strings.Contains(systemText, cfg.systemRemovedPattern) {
		timestamp, err := parseTimestampLang(currentDate, textTime, tz, cfg)
		if err != nil {
			return timeline.Graph{}, false
		}
		return createSystemMessageGraph(systemText, timestamp), true
	}

	// Check added pattern with exclusions
	if cfg.systemAddedPattern != "" && strings.Contains(systemText, cfg.systemAddedPattern) {
		// Skip if it matches any exclusion
		shouldExclude := false
		for _, exclusion := range cfg.addedExclusions {
			if strings.Contains(systemText, exclusion) {
				shouldExclude = true
				break
			}
		}
		if !shouldExclude {
			timestamp, err := parseTimestampLang(currentDate, textTime, tz, cfg)
			if err != nil {
				return timeline.Graph{}, false
			}
			return createSystemMessageGraph(systemText, timestamp), true
		}
	}

	return timeline.Graph{}, false
}

// createSystemMessageGraph creates a system message graph.
func createSystemMessageGraph(systemText string, timestamp time.Time) timeline.Graph {
	return timeline.Graph{
		Item: &timeline.Item{
			Classification: timeline.ClassNote,
			Timestamp:      timestamp,
			Content: timeline.ItemData{
				Data: timeline.StringData(systemText),
			},
		},
	}
}

func systemMessageRetrievalKey(filename string, ordinal int, timestamp time.Time, content string) string {
	return fmt.Sprintf("line|system|%s|%d|%d|%s", filename, ordinal, timestamp.UnixMilli(), content)
}

func messageRetrievalKey(msg *parsedMessage) string {
	return fmt.Sprintf("line|message|%s|%d|%d|%s|%s", msg.intermediateLocation, msg.ordinal, msg.timestamp.UnixMilli(), msg.sender, msg.content)
}

func systemText(item *timeline.Item) string {
	if item == nil || item.Content.Data == nil {
		return ""
	}
	r, err := item.Content.Data(context.Background())
	if err != nil {
		return ""
	}
	defer r.Close()
	b, err := io.ReadAll(r)
	if err != nil {
		return ""
	}
	return string(b)
}

// parseTimestampLang parses a LINE timestamp using the language config.
func parseTimestampLang(dateLine, timeStr string, tz *time.Location, cfg languageConfig) (time.Time, error) {
	if dateLine == "" {
		return time.Time{}, errors.New("missing date")
	}
	parts := strings.SplitN(dateLine, ", ", 2)
	if len(parts) != 2 {
		return time.Time{}, fmt.Errorf("invalid date format: %s", dateLine)
	}
	dateStr := parts[1]
	dateTimeStr := dateStr + " " + timeStr

	timestamp, err := time.ParseInLocation(cfg.dateLayout, dateTimeStr, tz)
	if err != nil {
		return time.Time{}, fmt.Errorf("parsing timestamp '%s': %w", dateTimeStr, err)
	}
	return timestamp, nil
}

// parseTimestamp parses a LINE timestamp from date and time strings (auto-detects language).
func parseTimestamp(dateLine, timeStr string, tz *time.Location) (time.Time, error) {
	var lastErr error
	for _, cfg := range languageConfigs {
		ts, err := parseTimestampLang(dateLine, timeStr, tz, cfg)
		if err == nil {
			return ts, nil
		}
		lastErr = err
	}
	if lastErr != nil {
		return time.Time{}, lastErr
	}
	return time.Time{}, errors.New("parsing timestamp failed")
}

// createMessageGraph creates a timeline.Graph from a parsed message.
func createMessageGraph(msg *parsedMessage) *timeline.Graph {
	// Skip empty messages
	if strings.TrimSpace(msg.content) == "" {
		return nil
	}

	item := &timeline.Item{
		Classification:       timeline.ClassMessage,
		Timestamp:            msg.timestamp,
		IntermediateLocation: msg.intermediateLocation,
		Owner:                msg.owner,
		Content: timeline.ItemData{
			Filename: msg.sourceFilename,
			Data:     timeline.StringData(msg.content),
		},
	}
	item.Retrieval.SetKey(messageRetrievalKey(msg))

	return &timeline.Graph{
		Item: item,
	}
}
