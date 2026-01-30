package line_test

import (
	"context"
	"io"
	"os"
	"testing"
	"time"

	"github.com/timelinize/timelinize/datasources/line"
	"github.com/timelinize/timelinize/timeline"
)

func TestFileImport(t *testing.T) {
	fixtures := os.DirFS("testdata/fixtures")
	dirEntry := timeline.DirEntry{
		FS:       fixtures,
		Filename: "Chat history with Alice.txt",
	}

	pipeline := make(chan *timeline.Graph, 100)
	params := timeline.ImportParams{Pipeline: pipeline}

	runErr := new(line.Importer).FileImport(context.Background(), dirEntry, params)
	if runErr != nil {
		t.Errorf("unable to import file: %v", runErr)
	}
	close(pipeline)

	expected := []struct {
		owner string
		text  string
		index int
	}{
		{owner: "Alice", text: "[Photo]", index: 0},
		{owner: "Bob", text: "Nice picture!", index: 1},
		{owner: "Alice", text: "Thanks! Took it on my trip", index: 2},
		{owner: "Bob", text: "Where was this taken?", index: 3},
		{owner: "Alice", text: "At the beach\nnear the lighthouse", index: 4},
		{owner: "Bob", text: "[Sticker]", index: 5},
		{owner: "Alice", text: "😂", index: 6},
		{owner: "Alice", text: "Alice created an album.", index: 7},
		{owner: "Alice", text: "Alice added items to an album.", index: 8},
		{owner: "Bob", text: "Did you see the news today?", index: 9},
	}

	// Collect results and track participants
	var results []*timeline.Graph
	participants := make(map[string]bool)
	for graph := range pipeline {
		results = append(results, graph)
		if graph.Item != nil {
			participants[graph.Item.Owner.Name] = true
			// Also check ToEntity relationships
			for _, edge := range graph.Edges {
				if edge.To != nil && edge.To.Entity != nil {
					participants[edge.To.Entity.Name] = true
				}
			}
		}
	}

	// Check we got the expected number of messages
	if len(results) < len(expected) {
		t.Errorf("Expected at least %d messages, got %d", len(expected), len(results))
	}

	// Verify we have exactly 2 participants: Alice and Bob
	if len(participants) != 2 {
		t.Errorf("Expected 2 participants, got %d: %v", len(participants), participants)
	}
	if !participants["Alice"] {
		t.Error("Expected participant 'Alice' not found")
	}
	if !participants["Bob"] {
		t.Error("Expected participant 'Bob' not found")
	}

	// Validate specific messages
	for _, exp := range expected {
		if exp.index >= len(results) {
			t.Errorf("Expected message at index %d, but only got %d messages", exp.index, len(results))
			continue
		}

		msg := results[exp.index]
		if msg.Item == nil {
			t.Errorf("Message at index %d has no item", exp.index)
			continue
		}

		if msg.Item.Owner.Name != exp.owner {
			t.Errorf("Message %d: expected owner %q, got %q", exp.index, exp.owner, msg.Item.Owner.Name)
		}

		// Get message text
		if msg.Item.Content.Data != nil {
			reader, err := msg.Item.Content.Data(context.Background())
			if err != nil {
				t.Errorf("Message %d: unable to get data reader: %v", exp.index, err)
			} else {
				defer reader.Close()
				textBytes, err := io.ReadAll(reader)
				if err != nil {
					t.Errorf("Message %d: unable to read data: %v", exp.index, err)
				} else {
					text := string(textBytes)
					if text != exp.text {
						t.Errorf("Message %d: expected text %q, got %q", exp.index, exp.text, text)
					}
				}
			}
		}

		// Verify the owner has a line_name attribute
		found := false
		for _, attr := range msg.Item.Owner.Attributes {
			if attr.Name == "line_name" && attr.Identity {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Message %d: owner missing line_name identity attribute", exp.index)
		}
	}

	// Verify timestamps are reasonable
	for i, msg := range results {
		if msg.Item == nil {
			continue
		}
		if msg.Item.Timestamp.IsZero() {
			t.Errorf("Message %d has zero timestamp", i)
		}
		if msg.Item.Timestamp.Year() != 2019 {
			t.Errorf("Message %d has unexpected year: %d", i, msg.Item.Timestamp.Year())
		}
	}
}

func TestOnewayConversation(t *testing.T) {
	fixtures := os.DirFS("testdata/fixtures")
	dirEntry := timeline.DirEntry{
		FS:       fixtures,
		Filename: "[LINE] Chat mit Solo.txt",
	}

	pipeline := make(chan *timeline.Graph, 100)
	params := timeline.ImportParams{Pipeline: pipeline}

	// Run the import
	runErr := new(line.Importer).FileImport(context.Background(), dirEntry, params)
	if runErr != nil {
		t.Fatalf("unable to import file: %v", runErr)
	}
	close(pipeline)

	// Collect all unique participants
	participants := make(map[string]bool)
	var messageCount int
	for graph := range pipeline {
		messageCount++
		if graph.Item != nil {
			t.Logf("Message %d: Owner=%s", messageCount, graph.Item.Owner.Name)
			participants[graph.Item.Owner.Name] = true
			// Also check ToEntity relationships
			for i, edge := range graph.Edges {
				if edge.To != nil && edge.To.Entity != nil {
					t.Logf("  Edge %d: Relation=%s, To=%s", i, edge.Relation.Label, edge.To.Entity.Name)
					participants[edge.To.Entity.Name] = true
				}
			}
		}
	}

	t.Logf("Total messages: %d", messageCount)
	t.Logf("Participants found: %v", participants)

	// Should have exactly 2 participants: Bob and Solo
	// Solo is mentioned in the header "[LINE] Chat mit Solo"
	// Bob sent all the messages
	if len(participants) != 2 {
		t.Errorf("Expected 2 participants (Bob and Solo), got %d: %v", len(participants), participants)
	}

	if !participants["Bob"] {
		t.Error("Expected participant 'Bob' (sender) not found")
	}

	if !participants["Solo"] {
		t.Error("Expected participant 'Solo' (from header) not found")
	}
}

func TestGroupChat(t *testing.T) {
	fixtures := os.DirFS("testdata/fixtures")
	dirEntry := timeline.DirEntry{
		FS:       fixtures,
		Filename: "Chat history in GroupChat.txt",
	}

	pipeline := make(chan *timeline.Graph, 200)
	params := timeline.ImportParams{Pipeline: pipeline}

	// Run the import
	runErr := new(line.Importer).FileImport(context.Background(), dirEntry, params)
	if runErr != nil {
		t.Fatalf("unable to import file: %v", runErr)
	}
	close(pipeline)

	// Collect all unique participants and messages
	participants := make(map[string]bool)
	var messages []*timeline.Graph
	var systemMessages int

	for graph := range pipeline {
		messages = append(messages, graph)
		if graph.Item != nil {
			// System messages are classified as notes and have no owner
			if graph.Item.Classification.Name == "note" {
				systemMessages++
			} else if graph.Item.Owner.Name != "" {
				participants[graph.Item.Owner.Name] = true
			}

			// Track participants from relationships
			for _, edge := range graph.Edges {
				if edge.To != nil && edge.To.Entity != nil {
					participants[edge.To.Entity.Name] = true
				}
			}
		}
	}

	t.Logf("Total messages: %d", len(messages))
	t.Logf("System messages: %d", systemMessages)
	t.Logf("Participants found: %v", participants)

	// Verify known participants exist
	expectedParticipants := []string{"Alice", "Bob", "Charlie", "David", "Eve", "Unknown"}
	for _, name := range expectedParticipants {
		if !participants[name] {
			t.Errorf("Expected participant %q not found", name)
		}
	}

	// Verify system messages were captured (English: "left the group", "left the chat")
	if systemMessages == 0 {
		t.Error("Expected system messages (leave notifications) but found none")
	}

	// Verify message content samples
	foundMultilineMessage := false
	foundPhotoMessage := false
	foundStickerMessage := false

	for _, msg := range messages {
		if msg.Item == nil || msg.Item.Content.Data == nil {
			continue
		}

		reader, err := msg.Item.Content.Data(context.Background())
		if err != nil {
			continue
		}
		textBytes, _ := io.ReadAll(reader)
		reader.Close()
		text := string(textBytes)

		if msg.Item.Owner.Name == "Alice" && len(text) > 30 {
			foundMultilineMessage = true
		}
		if text == "[Photo]" {
			foundPhotoMessage = true
		}
		if text == "[Sticker]" {
			foundStickerMessage = true
		}
	}

	if !foundMultilineMessage {
		t.Error("Expected to find multi-line message")
	}
	if !foundPhotoMessage {
		t.Error("Expected to find photo placeholder messages")
	}
	if !foundStickerMessage {
		t.Error("Expected to find sticker placeholder messages")
	}

	// Verify timestamps are reasonable
	for i, msg := range messages {
		if msg.Item == nil {
			continue
		}
		if msg.Item.Timestamp.IsZero() {
			t.Errorf("Message %d has zero timestamp", i)
		}
		// Messages are from 2019-2022
		year := msg.Item.Timestamp.Year()
		if year < 2019 || year > 2022 {
			t.Errorf("Message %d has unexpected year: %d", i, year)
		}
	}
}

func TestGroupChatGerman(t *testing.T) {
	// Keep one test for German format
	fixtures := os.DirFS("testdata/fixtures")
	dirEntry := timeline.DirEntry{
		FS:       fixtures,
		Filename: "[LINE] Chat in GroupChat.txt",
	}

	pipeline := make(chan *timeline.Graph, 200)
	params := timeline.ImportParams{Pipeline: pipeline}

	// Run the import
	runErr := new(line.Importer).FileImport(context.Background(), dirEntry, params)
	if runErr != nil {
		t.Fatalf("unable to import file: %v", runErr)
	}
	close(pipeline)

	// Count messages and system messages
	var messageCount, systemMessages int
	for graph := range pipeline {
		messageCount++
		if graph.Item != nil && graph.Item.Classification.Name == "note" {
			systemMessages++
		}
	}

	t.Logf("Total messages: %d", messageCount)
	t.Logf("System messages: %d", systemMessages)

	// Verify system messages were captured
	if systemMessages != 5 {
		t.Errorf("Expected 5 system messages in German export, got %d", systemMessages)
	}
}

func TestIsDateLine(t *testing.T) {
	tests := []struct {
		line     string
		expected bool
	}{
		// German format
		{"Do., 29.8.2019", true},
		{"Di., 15.10.2019", true},
		{"Mi., 1.1.2020", true},
		{"Sa., 4.1.2020", true},
		{"Mo., 15.6.2020", true},
		{"Fr., 19.6.2020", true},
		{"So., 21.6.2020", true},
		// English format
		{"Mon, 8/19/2019", true},
		{"Tue, 8/20/2019", true},
		{"Fri, 8/16/2019", true},
		{"Wed, 11/27/2019", true},
		{"Sat, 10/31/2020", true},
		// Not date lines
		{"09:00\tAlice\tGood morning!", false},
		{"[LINE] Chat mit Alice", false},
		{"Chat history with Alice", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.line, func(t *testing.T) {
			result := line.IsDateLine(tt.line)
			if result != tt.expected {
				t.Errorf("isDateLine(%q) = %v, expected %v", tt.line, result, tt.expected)
			}
		})
	}
}

func TestParseTimestamp(t *testing.T) {
	tests := []struct {
		name     string
		date     string
		time     string
		expected time.Time
	}{
		// German format tests
		{
			name:     "German date format single digits",
			date:     "Do., 29.8.2019",
			time:     "06:44",
			expected: time.Date(2019, 8, 29, 6, 44, 0, 0, time.Local),
		},
		{
			name:     "German two-digit month and day",
			date:     "Di., 15.10.2019",
			time:     "20:35",
			expected: time.Date(2019, 10, 15, 20, 35, 0, 0, time.Local),
		},
		{
			name:     "German single digit day and month",
			date:     "Mi., 1.1.2020",
			time:     "09:31",
			expected: time.Date(2020, 1, 1, 9, 31, 0, 0, time.Local),
		},
		// English format tests
		{
			name:     "English date format",
			date:     "Mon, 8/19/2019",
			time:     "11:28",
			expected: time.Date(2019, 8, 19, 11, 28, 0, 0, time.Local),
		},
		{
			name:     "English single digit month and day",
			date:     "Fri, 8/16/2019",
			time:     "20:27",
			expected: time.Date(2019, 8, 16, 20, 27, 0, 0, time.Local),
		},
		{
			name:     "English two-digit month and day",
			date:     "Tue, 10/15/2019",
			time:     "14:07",
			expected: time.Date(2019, 10, 15, 14, 7, 0, 0, time.Local),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := line.ParseTimestamp(tt.date, tt.time, time.Local)
			if err != nil {
				t.Fatalf("parseTimestamp failed: %v", err)
			}

			if !result.Equal(tt.expected) {
				t.Errorf("Expected %v, got %v", tt.expected, result)
			}
		})
	}
}
