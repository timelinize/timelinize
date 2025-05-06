package beeper

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/timelinize/timelinize/timeline"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest"
)

// TestRecognize tests that the Beeper data source correctly recognizes Beeper database files
func TestRecognize(t *testing.T) {
	// Create a temporary testing directory
	tempDir, err := os.MkdirTemp("", "beeper_test_*")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	// Create a mock Beeper database
	dbPath := filepath.Join(tempDir, "index.db")
	db, err := createMockBeeperDB(dbPath)
	require.NoError(t, err)
	db.Close()

	// Create a test DirEntry
	dirEntry := &mockDirEntry{
		name:     "index.db",
		fullPath: dbPath,
	}

	// Test recognition
	beeper := &Beeper{}
	result, err := beeper.Recognize(context.Background(), dirEntry, timeline.RecognizeParams{})
	require.NoError(t, err)
	assert.Equal(t, float64(1), result.Confidence, "Should recognize with 100% confidence")
}

// TestFileImport tests importing data from a mock Beeper database
func TestFileImport(t *testing.T) {
	// Create a temporary testing directory
	tempDir, err := os.MkdirTemp("", "beeper_test_*")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	// Create a mock Beeper database with test data
	dbPath := filepath.Join(tempDir, "index.db")
	db, err := createMockBeeperDB(dbPath)
	require.NoError(t, err)
	defer db.Close()

	// Add test data to the mock database
	insertTestData(t, db)

	// Create a test DirEntry
	dirEntry := &mockDirEntry{
		name:     "index.db",
		fullPath: dbPath,
	}

	// Create a test pipeline channel to capture outputs
	pipeline := make(chan *timeline.Graph, 100)
	entities := make([]*timeline.Entity, 0)

	// Create import params
	params := timeline.ImportParams{
		Pipeline: pipeline,
		SendEntity: func(entity *timeline.Entity) error {
			entities = append(entities, entity)
			return nil
		},
		Log: zap.NewNop(),
	}

	// Run the import in a goroutine
	done := make(chan struct{})
	go func() {
		beeper := &Beeper{}
		err := beeper.FileImport(context.Background(), dirEntry, params)
		require.NoError(t, err)
		close(pipeline)
		close(done)
	}()

	// Collect items from the pipeline
	var items []*timeline.Item
	for graph := range pipeline {
		if graph.Item != nil {
			items = append(items, graph.Item)
		}
	}

	<-done // Wait for the import to complete

	// Verify results
	assert.NotEmpty(t, items, "Should have imported some items")
	assert.NotEmpty(t, entities, "Should have imported some entities")

	// Verify that at least one message was imported
	var foundMessage bool
	for _, item := range items {
		if item.Classification == timeline.ClassMessage {
			foundMessage = true
			assert.NotEmpty(t, item.Content.Data, "Message should have content")
			assert.NotNil(t, item.Timestamp, "Message should have timestamp")
			assert.Contains(t, item.Metadata, "Source", "Message should have Source metadata")
			assert.Equal(t, "Beeper", item.Metadata["Source"], "Source should be Beeper")
		}
	}
	assert.True(t, foundMessage, "Should have found at least one message")

	// Verify that at least one thread entity was imported
	var foundThread bool
	for _, entity := range entities {
		for _, attr := range entity.Attributes {
			if attr.Name == "beeper_thread_id" {
				foundThread = true
				assert.NotEmpty(t, entity.Name, "Thread should have a name")
			}
		}
	}
	assert.True(t, foundThread, "Should have found at least one thread entity")
}

// TestWALMode tests that the importer can handle WAL mode databases
func TestWALMode(t *testing.T) {
	// Create a temporary testing directory
	tempDir, err := os.MkdirTemp("", "beeper_test_*")
	require.NoError(t, err)
	defer os.RemoveAll(tempDir)

	// Create a mock Beeper database with WAL mode
	dbPath := filepath.Join(tempDir, "index.db")
	db, err := createMockBeeperDBWithWAL(dbPath)
	require.NoError(t, err)
	defer db.Close()

	// Add test data to the mock database
	insertTestData(t, db)

	// Create a test DirEntry
	dirEntry := &mockDirEntry{
		name:     "index.db",
		fullPath: dbPath,
	}

	// Create a test temp directory for the import
	importTempDir, err := os.MkdirTemp("", "beeper_import_test_*")
	require.NoError(t, err)
	defer os.RemoveAll(importTempDir)

	// Test opening the database with our openDB function
	testDB, err := openDB(dbPath, importTempDir)
	require.NoError(t, err)
	defer testDB.Close()

	// Verify we can read from the database
	var count int
	err = testDB.QueryRow("SELECT COUNT(*) FROM mx_room_messages").Scan(&count)
	require.NoError(t, err)
	assert.Greater(t, count, 0, "Should be able to read data from the WAL mode database")
}

// Helper functions and mocks

// mockDirEntry implements the timeline.DirEntry interface for testing
type mockDirEntry struct {
	name     string
	fullPath string
}

func (m *mockDirEntry) Name() string {
	return m.name
}

func (m *mockDirEntry) FullPath() string {
	return m.fullPath
}

// createMockBeeperDB creates a mock Beeper database with the tables needed for testing
func createMockBeeperDB(path string) (*sql.DB, error) {
	// Remove the file if it exists
	os.Remove(path)

	// Create a new database
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		return nil, err
	}

	// Create the tables
	_, err = db.Exec(`
		-- Create mx_room_messages table
		CREATE TABLE mx_room_messages (
			id INTEGER PRIMARY KEY,
			roomID TEXT NOT NULL,
			eventID TEXT NOT NULL,
			senderContactID TEXT NOT NULL,
			timestamp INTEGER NOT NULL,
			isEdited INTEGER NOT NULL,
			isDeleted INTEGER NOT NULL,
			isEncrypted INTEGER NOT NULL,
			type TEXT NOT NULL,
			isSentByMe INTEGER NOT NULL,
			message JSON NOT NULL,
			text_content TEXT,
			text_formattedContent TEXT,
			text_format TEXT
		);

		-- Create threads table
		CREATE TABLE threads (
			threadID VARCHAR PRIMARY KEY,
			accountID VARCHAR,
			thread JSON NOT NULL,
			timestamp INTEGER
		);

		-- Create users table
		CREATE TABLE users (
			userID VARCHAR PRIMARY KEY,
			accountID VARCHAR,
			user JSON NOT NULL
		);
	`)
	if err != nil {
		return nil, err
	}

	return db, nil
}

// createMockBeeperDBWithWAL creates a mock Beeper database in WAL mode
func createMockBeeperDBWithWAL(path string) (*sql.DB, error) {
	db, err := createMockBeeperDB(path)
	if err != nil {
		return nil, err
	}

	// Enable WAL mode
	_, err = db.Exec("PRAGMA journal_mode=WAL;")
	if err != nil {
		return nil, err
	}

	return db, nil
}

// insertTestData inserts test data into the mock database
func insertTestData(t *testing.T, db *sql.DB) {
	// Insert test mx_room_messages
	_, err := db.Exec(`
		INSERT INTO mx_room_messages (
			roomID, eventID, senderContactID, timestamp, isEdited, isDeleted, 
			isEncrypted, type, isSentByMe, message, text_content
		) VALUES (
			'!room1:example.com', 
			'$event1:example.com', 
			'@user1:example.com', 
			?, 
			0, 0, 0, 
			'm.room.message', 
			1, 
			'{"msgtype":"m.text","body":"Hello, world!"}',
			'Hello, world!'
		), (
			'!room2:example.com', 
			'$event2:example.com', 
			'@user2:example.com', 
			?, 
			0, 0, 0, 
			'm.room.message', 
			0, 
			'{"msgtype":"m.text","body":"Testing Beeper integration"}',
			'Testing Beeper integration'
		);
	`, time.Now().Unix()*1000, time.Now().Add(-1*time.Hour).Unix()*1000)
	require.NoError(t, err)

	// Insert test threads
	_, err = db.Exec(`
		INSERT INTO threads (threadID, accountID, thread, timestamp) VALUES 
		('!room1:example.com', '@user1:example.com', 
		 '{"name":"Test Room 1","topic":"Testing","is_direct":false}', ?),
		('!room2:example.com', '@user1:example.com', 
		 '{"name":"Test Room 2","topic":"More Testing","is_direct":true}', ?);
	`, time.Now().Unix()*1000, time.Now().Add(-2*time.Hour).Unix()*1000)
	require.NoError(t, err)

	// Insert test users
	_, err = db.Exec(`
		INSERT INTO users (userID, accountID, user) VALUES 
		('@user1:example.com', '@user1:example.com', 
		 '{"displayname":"Test User 1","avatar_url":"mxc://example.com/avatar1"}'),
		('@user2:example.com', '@user1:example.com', 
		 '{"displayname":"Test User 2","avatar_url":"mxc://example.com/avatar2"}');
	`)
	require.NoError(t, err)
}
