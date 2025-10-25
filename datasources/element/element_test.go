package element_test

import (
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/timelinize/timelinize/datasources/element"
	"github.com/timelinize/timelinize/timeline"
)

func TestRecognize(t *testing.T) {
	// Test with a valid Element export
	validExport := `{
		"room_name": "Test Room",
		"room_creator": "@user:example.com",
		"topic": "Test topic",
		"export_date": "01/01/2025",
		"exported_by": "testuser",
		"messages": []
	}`

	testFS := &mockFS{
		files: map[string]string{
			element.ChatPath: validExport,
		},
	}

	dirEntry := timeline.DirEntry{
		FS: testFS,
	}

	importer := &element.FileImporter{}
	recognition, err := importer.Recognize(context.Background(), dirEntry, timeline.RecognizeParams{})

	if err != nil {
		t.Errorf("Recognition failed: %v", err)
	}

	if recognition.Confidence != 1.0 {
		t.Errorf("Expected confidence 1.0, got %f", recognition.Confidence)
	}
}

func TestRecognizeInvalidFile(t *testing.T) {
	// Test with invalid content
	testFS := &mockFS{
		files: map[string]string{
			element.ChatPath: `{"invalid": "content"}`,
		},
	}

	dirEntry := timeline.DirEntry{
		FS: testFS,
	}

	importer := &element.FileImporter{}
	recognition, err := importer.Recognize(context.Background(), dirEntry, timeline.RecognizeParams{})

	if err != nil {
		t.Errorf("Recognition should not error: %v", err)
	}

	if recognition.Confidence != 0.0 {
		t.Errorf("Expected confidence 0.0 for invalid file, got %f", recognition.Confidence)
	}
}

func TestFileImport(t *testing.T) {
	// Create test export data
	exportData := map[string]interface{}{
		"room_name":    "Test Room",
		"room_creator": "@creator:example.com",
		"topic":        "Test topic",
		"export_date":  "01/01/2025",
		"exported_by":  "testuser",
		"messages": []map[string]interface{}{
			{
				"type":   "m.room.message",
				"sender": "@alice:example.com",
				"content": map[string]interface{}{
					"msgtype": "m.text",
					"body":    "Hello, world!",
				},
				"room_id":          "!testroom:example.com",
				"origin_server_ts": int64(1640995200000), // 2022-01-01 00:00:00 UTC
				"event_id":         "$event1:example.com",
				"user_id":          "@alice:example.com",
			},
			{
				"type":   "m.room.message",
				"sender": "@bob:example.com",
				"content": map[string]interface{}{
					"msgtype": "m.text",
					"body":    "Hi there!",
				},
				"room_id":          "!testroom:example.com",
				"origin_server_ts": int64(1640995260000), // 2022-01-01 00:01:00 UTC
				"event_id":         "$event2:example.com",
				"user_id":          "@bob:example.com",
			},
			{
				"type":   "m.room.message",
				"sender": "@alice:example.com",
				"content": map[string]interface{}{
					"msgtype":  "m.image",
					"body":     "Test image",
					"filename": "test.jpg",
					"url":      "mxc://example.com/testimage",
					"info": map[string]interface{}{
						"h":        1080,
						"w":        1920,
						"mimetype": "image/jpeg",
						"size":     123456,
					},
				},
				"room_id":          "!testroom:example.com",
				"origin_server_ts": int64(1640995320000), // 2022-01-01 00:02:00 UTC
				"event_id":         "$event3:example.com",
				"user_id":          "@alice:example.com",
			},
		},
	}

	exportJSON, err := json.Marshal(exportData)
	if err != nil {
		t.Fatalf("Failed to marshal test data: %v", err)
	}

	testFS := &mockFS{
		files: map[string]string{
			element.ChatPath: string(exportJSON),
			"images/Test image-1-1-2022 at 12-02-00 AM": "fake image data",
		},
	}

	dirEntry := timeline.DirEntry{
		FS: testFS,
	}

	// Setup pipeline
	pipeline := make(chan *timeline.Graph, 10)
	params := timeline.ImportParams{
		Pipeline:          pipeline,
		DataSourceOptions: &element.Options{},
	}

	// Run import
	importer := &element.FileImporter{}
	go func() {
		defer close(pipeline)
		err := importer.FileImport(context.Background(), dirEntry, params)
		if err != nil {
			t.Errorf("FileImport failed: %v", err)
		}
	}()

	// Collect results
	var messages []*timeline.Graph
	var checkpoints []*timeline.Graph
	for graph := range pipeline {
		if graph.Checkpoint != nil {
			checkpoints = append(checkpoints, graph)
		} else {
			messages = append(messages, graph)
		}
	}

	// Verify we got 3 messages (including the image message)
	if len(messages) != 3 {
		t.Errorf("Expected 3 messages, got %d", len(messages))
	}

	// Verify first message
	if len(messages) > 0 {
		msg := messages[0]
		if msg.Item.Owner.Name != "alice" {
			t.Errorf("Expected first message from 'alice', got '%s'", msg.Item.Owner.Name)
		}
		expectedTime := time.UnixMilli(1640995200000)
		if !msg.Item.Timestamp.Equal(expectedTime) {
			t.Errorf("Expected timestamp %v, got %v", expectedTime, msg.Item.Timestamp)
		}
	}

	// Verify second message
	if len(messages) > 1 {
		msg := messages[1]
		if msg.Item.Owner.Name != "bob" {
			t.Errorf("Expected second message from 'bob', got '%s'", msg.Item.Owner.Name)
		}
	}

	// Verify third message (image message with attachment)
	if len(messages) > 2 {
		msg := messages[2]
		if msg.Item.Owner.Name != "alice" {
			t.Errorf("Expected third message from 'alice', got '%s'", msg.Item.Owner.Name)
		}

		// Check if there's an attachment relationship
		hasAttachment := false
		for _, edge := range msg.Edges {
			if edge.Relation == timeline.RelAttachment {
				hasAttachment = true
				attachmentItem := edge.To.Item
				if attachmentItem == nil {
					t.Errorf("Attachment item is nil")
				} else {
					// Verify attachment metadata
					if attachmentItem.Metadata["message_type"] != "m.image" {
						t.Errorf("Expected attachment message_type 'm.image', got '%v'", attachmentItem.Metadata["message_type"])
					}
					if attachmentItem.Metadata["mime_type"] != "image/jpeg" {
						t.Errorf("Expected mime_type 'image/jpeg', got '%v'", attachmentItem.Metadata["mime_type"])
					}
				}
				break
			}
		}
		if !hasAttachment {
			t.Errorf("Expected image message to have attachment relationship")
		}
	}

	// Verify we got at least one checkpoint
	if len(checkpoints) == 0 {
		t.Errorf("Expected at least one checkpoint")
	}
}

func TestAttachmentHandling(t *testing.T) {
	// Test specific attachment scenarios
	exportData := map[string]interface{}{
		"room_name": "Attachment Test",
		"messages": []map[string]interface{}{
			{
				"type":   "m.room.message",
				"sender": "@user:example.com",
				"content": map[string]interface{}{
					"msgtype":  "m.image",
					"body":     "Photo from vacation",
					"filename": "image.jpg",
					"url":      "mxc://example.com/abc123",
					"info": map[string]interface{}{
						"h":        2048,
						"w":        1536,
						"mimetype": "image/jpeg",
						"size":     567890,
					},
				},
				"room_id":          "!test:example.com",
				"origin_server_ts": int64(1640995200000),
				"event_id":         "$img1:example.com",
				"user_id":          "@user:example.com",
			},
			{
				"type":   "m.room.message",
				"sender": "@user:example.com",
				"content": map[string]interface{}{
					"msgtype":  "m.video",
					"body":     "Vacation video",
					"filename": "video.mp4",
					"url":      "mxc://example.com/def456",
					"info": map[string]interface{}{
						"mimetype": "video/mp4",
						"size":     234567,
					},
				},
				"room_id":          "!test:example.com",
				"origin_server_ts": int64(1640995260000),
				"event_id":         "$video1:example.com",
				"user_id":          "@user:example.com",
			},
		},
	}

	exportJSON, err := json.Marshal(exportData)
	if err != nil {
		t.Fatalf("Failed to marshal test data: %v", err)
	}

	testFS := &mockFS{
		files: map[string]string{
			element.ChatPath: string(exportJSON),
			// Include files that match Element's patterns
			"images/image-1-1-2022 at 1-00-00 AM.jpg":           "fake image data",
			"images/video-1-1-2022 at 1-01-00 AM.mp4":           "fake video data",
			"images/Photo from vacation-1-1-2022 at 1-00-00 AM": "fake descriptive image data",
		},
	}

	dirEntry := timeline.DirEntry{
		FS: testFS,
	}

	pipeline := make(chan *timeline.Graph, 10)
	params := timeline.ImportParams{
		Pipeline:          pipeline,
		DataSourceOptions: &element.Options{},
	}

	importer := &element.FileImporter{}
	go func() {
		defer close(pipeline)
		err := importer.FileImport(context.Background(), dirEntry, params)
		if err != nil {
			t.Errorf("FileImport failed: %v", err)
		}
	}()

	var messages []*timeline.Graph
	for graph := range pipeline {
		if graph.Checkpoint == nil {
			messages = append(messages, graph)
		}
	}

	// Should have 2 messages (both with attachments)
	if len(messages) != 2 {
		t.Errorf("Expected 2 messages, got %d", len(messages))
	}

	// Verify first message (image)
	if len(messages) > 0 {
		msg := messages[0]
		attachmentFound := false
		for _, edge := range msg.Edges {
			if edge.Relation == timeline.RelAttachment {
				attachmentFound = true
				attachment := edge.To.Item
				if attachment.Metadata["message_type"] != "m.image" {
					t.Errorf("Expected image attachment, got %v", attachment.Metadata["message_type"])
				}
				if attachment.Metadata["width"] != int64(1536) {
					t.Errorf("Expected width 1536, got %v", attachment.Metadata["width"])
				}
				if !strings.Contains(attachment.Content.Filename, "image-") {
					t.Errorf("Expected image filename pattern, got %v", attachment.Content.Filename)
				}
				break
			}
		}
		if !attachmentFound {
			t.Errorf("Expected image message to have attachment")
		}
	}

	// Verify second message (video)
	if len(messages) > 1 {
		msg := messages[1]
		attachmentFound := false
		for _, edge := range msg.Edges {
			if edge.Relation == timeline.RelAttachment {
				attachmentFound = true
				attachment := edge.To.Item
				if attachment.Metadata["message_type"] != "m.video" {
					t.Errorf("Expected video attachment, got %v", attachment.Metadata["message_type"])
				}
				if attachment.Metadata["mime_type"] != "video/mp4" {
					t.Errorf("Expected MP4 mime type, got %v", attachment.Metadata["mime_type"])
				}
				if !strings.Contains(attachment.Content.Filename, "video-") {
					t.Errorf("Expected video filename pattern, got %v", attachment.Content.Filename)
				}
				break
			}
		}
		if !attachmentFound {
			t.Errorf("Expected video message to have attachment")
		}
	}
}

func TestGenericImageHandling(t *testing.T) {
	// Test generic image names that use timestamp-based filenames
	exportData := map[string]interface{}{
		"room_name": "Generic Image Test",
		"messages": []map[string]interface{}{
			{
				"type":   "m.room.message",
				"sender": "@user:example.com",
				"content": map[string]interface{}{
					"msgtype":  "m.image",
					"body":     "image.jpg",
					"filename": "image.jpg",
					"url":      "mxc://example.com/generic123",
					"info": map[string]interface{}{
						"h":        499,
						"w":        501,
						"mimetype": "image/jpeg",
						"size":     23843,
					},
				},
				"room_id":          "!test:example.com",
				"origin_server_ts": int64(1640995200000), // 2022-01-01 00:00:00 UTC
				"event_id":         "$generic1:example.com",
				"user_id":          "@user:example.com",
			},
		},
	}

	exportJSON, err := json.Marshal(exportData)
	if err != nil {
		t.Fatalf("Failed to marshal test data: %v", err)
	}

	testFS := &mockFS{
		files: map[string]string{
			element.ChatPath: string(exportJSON),
			// Generic image with timestamp-based filename
			"images/image-1-1-2022 at 1-00-00 AM.jpg": "fake generic image data",
		},
	}

	dirEntry := timeline.DirEntry{
		FS: testFS,
	}

	pipeline := make(chan *timeline.Graph, 10)
	params := timeline.ImportParams{
		Pipeline:          pipeline,
		DataSourceOptions: &element.Options{},
	}

	importer := &element.FileImporter{}
	go func() {
		defer close(pipeline)
		err := importer.FileImport(context.Background(), dirEntry, params)
		if err != nil {
			t.Errorf("FileImport failed: %v", err)
		}
	}()

	var messages []*timeline.Graph
	for graph := range pipeline {
		if graph.Checkpoint == nil {
			messages = append(messages, graph)
		}
	}

	// Should have 1 message with attachment
	if len(messages) != 1 {
		t.Errorf("Expected 1 message, got %d", len(messages))
	}

	if len(messages) > 0 {
		msg := messages[0]
		attachmentFound := false
		for _, edge := range msg.Edges {
			if edge.Relation == timeline.RelAttachment {
				attachmentFound = true
				attachment := edge.To.Item
				if attachment.Metadata["message_type"] != "m.image" {
					t.Errorf("Expected image attachment, got %v", attachment.Metadata["message_type"])
				}
				if attachment.Content.Filename != "image-1-1-2022 at 1-00-00 AM.jpg" {
					t.Errorf("Expected generic image filename 'image-1-1-2022 at 1-00-00 AM.jpg', got '%v'", attachment.Content.Filename)
				}
				break
			}
		}
		if !attachmentFound {
			t.Errorf("Expected generic image message to have attachment")
		}
	}
}

func TestIsGenericImageName(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		{"empty string", "", true},
		{"image.jpg", "image.jpg", true},
		{"Image.JPG", "Image.JPG", true},
		{"video.mp4", "video.mp4", true},
		{"VIDEO.MP4", "VIDEO.MP4", true},
		{"sticker.png", "sticker.png", true},
		{"STICKER.PNG", "STICKER.PNG", true},
		{"voice message", "Voice message", true},
		{"voice message-timestamp.ogg", "Voice message-2025-01-15.ogg", true},
		{"file", "file", true},
		{"FILE", "FILE", true},
		{"descriptive name", "My vacation photo", false},
		{"custom filename", "sunset_beach.jpg", false},
		{"document", "document.pdf", false},
		{"specific image", "birthday_party.jpg", false},
		{"photo.png", "photo.png", false},
		{"picture.jpeg", "picture.jpeg", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := element.IsGenericImageName(tt.input)
			if result != tt.expected {
				t.Errorf("IsGenericImageName(%q) = %v, expected %v", tt.input, result, tt.expected)
			}
		})
	}
}

// mockFS implements fs.FS for testing
type mockFS struct {
	files map[string]string
}

func (mfs *mockFS) Open(name string) (fs.File, error) {
	content, exists := mfs.files[name]
	if !exists {
		return nil, os.ErrNotExist
	}
	return &mockFile{content: []byte(content)}, nil
}

// mockFile implements fs.File for testing
type mockFile struct {
	content []byte
	pos     int64
}

func (mf *mockFile) Read(p []byte) (int, error) {
	if mf.pos >= int64(len(mf.content)) {
		return 0, io.EOF
	}
	n := copy(p, mf.content[mf.pos:])
	mf.pos += int64(n)
	if mf.pos >= int64(len(mf.content)) {
		return n, io.EOF
	}
	return n, nil
}

func (mf *mockFile) Close() error {
	return nil
}

func (mf *mockFile) Stat() (fs.FileInfo, error) {
	return &mockFileInfo{size: int64(len(mf.content))}, nil
}

// mockFileInfo implements fs.FileInfo for testing
type mockFileInfo struct {
	size int64
}

func (mfi *mockFileInfo) Name() string       { return "test" }
func (mfi *mockFileInfo) Size() int64        { return mfi.size }
func (mfi *mockFileInfo) Mode() fs.FileMode  { return 0644 }
func (mfi *mockFileInfo) ModTime() time.Time { return time.Now() }
func (mfi *mockFileInfo) IsDir() bool        { return false }
func (mfi *mockFileInfo) Sys() interface{}   { return nil }
