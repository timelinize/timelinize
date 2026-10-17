package line

import (
	"context"
	"io"
	"os"
	"testing"
	"time"

	"github.com/timelinize/timelinize/timeline"
)

func TestRecognize(t *testing.T) {
	tests := []struct {
		name      string
		filename  string
		wantConf  float64
		wantRecog bool
	}{
		{
			name:      "english one-on-one chat",
			filename:  "Chat history with Alice.txt",
			wantConf:  0.95,
			wantRecog: true,
		},
		{
			name:      "english group chat",
			filename:  "Chat history in GroupChat.txt",
			wantConf:  0.95,
			wantRecog: true,
		},
		{
			name:      "german group chat",
			filename:  "[LINE] Chat in GroupChat.txt",
			wantConf:  0.95,
			wantRecog: true,
		},
		{
			name:      "german one-on-one chat",
			filename:  "[LINE] Chat mit Solo.txt",
			wantConf:  0.95,
			wantRecog: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixtures := os.DirFS("testdata/fixtures")
			dirEntry := timeline.DirEntry{
				FS:       fixtures,
				Filename: tt.filename,
			}

			rec, err := FileImporter{}.Recognize(context.Background(), dirEntry, timeline.RecognizeParams{})
			if err != nil {
				t.Fatalf("Recognize() error: %v", err)
			}
			if tt.wantRecog && rec.Confidence != tt.wantConf {
				t.Errorf("Recognize() confidence = %v, want %v", rec.Confidence, tt.wantConf)
			}
			if !tt.wantRecog && rec.Confidence > 0 {
				t.Errorf("Recognize() confidence = %v, want 0", rec.Confidence)
			}
		})
	}
}

// collectGraphs runs FileImport on the given fixture and returns all graphs sent on the pipeline.
func collectGraphs(t *testing.T, filename string, opts *Options) []*timeline.Graph {
	t.Helper()

	fixtures := os.DirFS("testdata/fixtures")
	dirEntry := timeline.DirEntry{
		FS:       fixtures,
		Filename: filename,
	}

	pipeline := make(chan *timeline.Graph, 200)
	params := timeline.ImportParams{Pipeline: pipeline}
	if opts != nil {
		params.DataSourceOptions = opts
	}

	err := new(FileImporter).FileImport(context.Background(), dirEntry, params)
	if err != nil {
		t.Fatalf("FileImport(%q) error: %v", filename, err)
	}
	close(pipeline)

	var graphs []*timeline.Graph
	for g := range pipeline {
		graphs = append(graphs, g)
	}
	return graphs
}

// graphText returns the text content of a graph's item, or empty string if unavailable.
func graphText(t *testing.T, g *timeline.Graph) string {
	t.Helper()
	if g.Item == nil || g.Item.Content.Data == nil {
		return ""
	}
	r, err := g.Item.Content.Data(context.Background())
	if err != nil {
		t.Fatalf("reading item data: %v", err)
	}
	defer r.Close()
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("reading item data: %v", err)
	}
	return string(b)
}

func TestFileImportOneOnOne(t *testing.T) {
	graphs := collectGraphs(t, "Chat history with Alice.txt", nil)

	if len(graphs) == 0 {
		t.Fatal("expected messages, got none")
	}

	// All items should be messages (no system messages in a simple one-on-one fixture)
	// and each message must have at least one recipient relationship.
	senders := make(map[string]bool)
	for i, g := range graphs {
		if g.Item == nil {
			t.Fatalf("graph %d has nil item", i)
		}
		if g.Item.Classification.Name != timeline.ClassMessage.Name {
			continue // system/note messages won't have relationships
		}
		senders[g.Item.Owner.Name] = true

		if g.Item.Timestamp.IsZero() {
			t.Errorf("message %d has zero timestamp", i)
		}

		// Verify owner has a line_name identity attribute
		hasIdentity := false
		for _, attr := range g.Item.Owner.Attributes {
			if attr.Name == "line_name" && attr.Identity {
				hasIdentity = true
			}
		}
		if !hasIdentity {
			t.Errorf("message %d: owner %q missing line_name identity attribute", i, g.Item.Owner.Name)
		}

		// In a one-on-one chat, each message should be sent to exactly 1 recipient
		if len(g.Edges) != 1 {
			t.Errorf("message %d: expected 1 recipient edge, got %d", i, len(g.Edges))
		}
	}

	// Verify both participants appear
	if !senders["Alice"] {
		t.Error("expected Alice as a sender")
	}
	if !senders["Bob"] {
		t.Error("expected Bob as a sender")
	}

	// Verify multi-line message is correctly joined
	found := false
	for _, g := range graphs {
		if graphText(t, g) == "At the beach\nnear the lighthouse" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected multi-line message 'At the beach\\nnear the lighthouse'")
	}
}

func TestFileImportGroupChat(t *testing.T) {
	graphs := collectGraphs(t, "Chat history in GroupChat.txt", nil)

	if len(graphs) == 0 {
		t.Fatal("expected messages, got none")
	}

	senders := make(map[string]bool)
	var systemMessages int

	for _, g := range graphs {
		if g.Item == nil {
			continue
		}
		if g.Item.Classification.Name == timeline.ClassNote.Name {
			systemMessages++
			// System messages should have no recipient relationships
			if len(g.Edges) > 0 {
				t.Errorf("system message should have no edges, got %d", len(g.Edges))
			}
			continue
		}
		senders[g.Item.Owner.Name] = true

		// Group messages should be sent to multiple recipients (all other participants)
		if len(g.Edges) < 2 {
			text := graphText(t, g)
			t.Errorf("group message from %q (%q) expected >= 2 recipients, got %d",
				g.Item.Owner.Name, text, len(g.Edges))
		}
	}

	// Verify key participants appear
	for _, name := range []string{"Alice", "Bob", "Charlie", "David", "Eve"} {
		if !senders[name] {
			t.Errorf("expected %q as a sender", name)
		}
	}

	// System messages for "left the group/chat" events
	if systemMessages == 0 {
		t.Error("expected system messages (leave notifications)")
	}
}

func TestFileImportGerman(t *testing.T) {
	graphs := collectGraphs(t, "[LINE] Chat in GroupChat.txt", &Options{Language: LanguageGerman})

	if len(graphs) == 0 {
		t.Fatal("expected messages, got none")
	}

	var systemMessages int
	senders := make(map[string]bool)
	for _, g := range graphs {
		if g.Item == nil {
			continue
		}
		if g.Item.Classification.Name == timeline.ClassNote.Name {
			systemMessages++
			continue
		}
		senders[g.Item.Owner.Name] = true
	}

	// German fixture has system messages (hat die Gruppe verlassen, hat den Chat verlassen)
	if systemMessages != 5 {
		t.Errorf("expected 5 system messages in German export, got %d", systemMessages)
	}

	// Verify German participant names appear
	if !senders["Anna"] {
		t.Error("expected Anna as a sender")
	}
	if !senders["Bernd"] {
		t.Error("expected Bernd as a sender")
	}
}

func TestFileImportOneWayChat(t *testing.T) {
	// In a chat where only one person sends messages, the header participant
	// ("Solo") should still appear as a recipient.
	graphs := collectGraphs(t, "[LINE] Chat mit Solo.txt", nil)

	if len(graphs) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(graphs))
	}

	for i, g := range graphs {
		if g.Item.Owner.Name != "Bob" {
			t.Errorf("message %d: expected sender Bob, got %q", i, g.Item.Owner.Name)
		}
		// Bob sends to Solo (from header)
		if len(g.Edges) != 1 {
			t.Fatalf("message %d: expected 1 recipient, got %d", i, len(g.Edges))
		}
		if g.Edges[0].To == nil || g.Edges[0].To.Entity == nil || g.Edges[0].To.Entity.Name != "Solo" {
			t.Errorf("message %d: expected recipient Solo", i)
		}
	}
}

func TestParseTimestamp(t *testing.T) {
	tests := []struct {
		name     string
		date     string
		time     string
		expected time.Time
	}{
		{
			name:     "german single digit day and month",
			date:     "Do., 29.8.2019",
			time:     "06:44",
			expected: time.Date(2019, 8, 29, 6, 44, 0, 0, time.Local),
		},
		{
			name:     "german two-digit month and day",
			date:     "Di., 15.10.2019",
			time:     "20:35",
			expected: time.Date(2019, 10, 15, 20, 35, 0, 0, time.Local),
		},
		{
			name:     "english date format",
			date:     "Mon, 8/19/2019",
			time:     "11:28",
			expected: time.Date(2019, 8, 19, 11, 28, 0, 0, time.Local),
		},
		{
			name:     "english two-digit month and day",
			date:     "Tue, 10/15/2019",
			time:     "14:07",
			expected: time.Date(2019, 10, 15, 14, 7, 0, 0, time.Local),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := parseTimestamp(tt.date, tt.time, time.Local)
			if err != nil {
				t.Fatalf("parseTimestamp() error: %v", err)
			}
			if !result.Equal(tt.expected) {
				t.Errorf("parseTimestamp() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestIsDateLine(t *testing.T) {
	tests := []struct {
		line string
		want bool
	}{
		// German format
		{"Do., 29.8.2019", true},
		{"Di., 15.10.2019", true},
		{"Mi., 1.1.2020", true},
		// English format
		{"Mon, 8/19/2019", true},
		{"Fri, 8/16/2019", true},
		{"Wed, 11/27/2019", true},
		// Not date lines
		{"09:00\tAlice\tGood morning!", false},
		{"Chat history with Alice", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.line, func(t *testing.T) {
			if got := isDateLine(tt.line); got != tt.want {
				t.Errorf("isDateLine(%q) = %v, want %v", tt.line, got, tt.want)
			}
		})
	}
}
