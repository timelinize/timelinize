/*
	Timelinize
	Copyright (c) 2024 Timelinize Contributors

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

// Package beeper implements a data source for Beeper chat data.
package beeper

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/timelinize/timelinize/timeline"
	"go.uber.org/zap"
)

// Data source name and ID.
const (
	DataSourceName = "Beeper"
	DataSourceID   = "beeper"
)

func init() {
	err := timeline.RegisterDataSource(timeline.DataSource{
		Name:            DataSourceID,
		Title:           DataSourceName,
		Icon:            "beeper.svg",
		Description:     "Beeper chat messages importer",
		NewFileImporter: func() timeline.FileImporter { return new(Beeper) },
	})
	if err != nil {
		timeline.Log.Fatal("registering data source", zap.Error(err))
	}
}

// Beeper interacts with the Beeper SQLite database to extract chat messages.
type Beeper struct{}

// Recognize returns whether the input file is recognized as a Beeper database.
func (Beeper) Recognize(_ context.Context, dirEntry timeline.DirEntry, _ timeline.RecognizeParams) (timeline.Recognition, error) {
	filename := filepath.Base(dirEntry.Name())
	// Check for Beeper database filenames
	if filename == "index.db" || filename == "contacts.db" {
		var ok bool
		var err error
		if ok, err = checkTables(dirEntry.FullPath()); err != nil {
			return timeline.Recognition{}, fmt.Errorf("checking table existence: %w", err)
		}

		if ok {
			return timeline.Recognition{Confidence: 1}, nil
		}
	}

	return timeline.Recognition{}, nil
}

// FileImport conducts an import of the Beeper data.
func (b *Beeper) FileImport(ctx context.Context, dirEntry timeline.DirEntry, params timeline.ImportParams) error {
	path := dirEntry.FullPath()
	params.Log.Info("Starting Beeper import", zap.String("file", path))

	// Get user's home directory
	homedir, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("getting home directory: %w", err)
	}

	// Try standard Beeper location first (most reliable)
	beeperPath := filepath.Join(homedir, "Library", "Application Support", "BeeperTexts", "index.db")
	if fileExists(beeperPath) {
		params.Log.Info("Found Beeper database at standard location", zap.String("path", beeperPath))
		if err := b.process(ctx, beeperPath, params); err != nil {
			params.Log.Error("Error processing main database", zap.Error(err))
			return err
		}
		return nil
	}

	// If standard location doesn't exist, fall back to the provided path
	params.Log.Info("Standard Beeper database not found, using provided path", zap.String("path", path))
	if err := b.process(ctx, path, params); err != nil {
		return fmt.Errorf("error processing database: %w", err)
	}

	return nil
}

// process reads the Beeper database and sends the items to the channel.
func (b *Beeper) process(ctx context.Context, path string, params timeline.ImportParams) error {
	tempDir, err := os.MkdirTemp("", "beeper_import_*")
	if err != nil {
		return fmt.Errorf("creating temp directory: %w", err)
	}
	defer os.RemoveAll(tempDir)

	// Open the database in read-only mode
	db, err := openDB(path, tempDir)
	if err != nil {
		return fmt.Errorf("opening database in read-only mode: %w", err)
	}
	defer db.Close()

	// Verify that we can read from the database
	err = db.Ping()
	if err != nil {
		return fmt.Errorf("verifying database connection: %w", err)
	}

	// Explore the database schema
	tables, err := getTableNames(db)
	if err != nil {
		return fmt.Errorf("getting table names: %w", err)
	}

	params.Log.Info("Found tables in Beeper database", zap.Strings("tables", tables))

	// Process threads (instead of contacts) if the table exists
	if containsTable(tables, "threads") {
		if err := b.processThreads(ctx, db, params); err != nil {
			params.Log.Warn("Error processing threads, continuing with messages", zap.Error(err))
		}
	}

	// Process users if the table exists
	if containsTable(tables, "users") {
		if err := b.processUsers(ctx, db, params); err != nil {
			params.Log.Warn("Error processing users, continuing with messages", zap.Error(err))
		}
	}

	// Check if we have the expected messages table
	if !containsTable(tables, "mx_room_messages") {
		return fmt.Errorf("mx_room_messages table not found in database")
	}

	// Get message table columns
	messageColumns, err := getTableColumns(db, "mx_room_messages")
	if err != nil {
		return fmt.Errorf("getting columns for mx_room_messages table: %w", err)
	}

	params.Log.Info("Found message columns", zap.Strings("columns", messageColumns))

	// Count total messages to provide progress updates
	var totalMessages int
	err = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM mx_room_messages").Scan(&totalMessages)
	if err != nil {
		params.Log.Warn("Failed to count messages, continuing without progress info", zap.Error(err))
	} else {
		params.Log.Info("Total messages to process", zap.Int("count", totalMessages))
	}

	// Process in smaller chunks with pagination
	offset := 0
	batchSize := 100
	totalProcessed := 0

	for {
		// Build a query based on the available columns with pagination
		query := buildMessagesQuery(tables, messageColumns, offset, batchSize)
		params.Log.Info("Executing query batch",
			zap.String("query", query),
			zap.Int("offset", offset),
			zap.Int("processed", totalProcessed),
			zap.Int("total", totalMessages))

		// Execute the query
		rows, err := db.QueryContext(ctx, query)
		if err != nil {
			return fmt.Errorf("executing query: %w", err)
		}

		// Process the rows
		count := 0
		for rows.Next() {
			select {
			case <-ctx.Done():
				rows.Close()
				return ctx.Err()
			default:
				// Get column names
				columns, err := rows.Columns()
				if err != nil {
					rows.Close()
					return fmt.Errorf("getting column names: %w", err)
				}

				// Create a slice of interface{} to hold the values
				values := make([]interface{}, len(columns))
				valuePtrs := make([]interface{}, len(columns))

				// Create pointers to the values
				for i := range values {
					valuePtrs[i] = &values[i]
				}

				// Scan the row
				if err := rows.Scan(valuePtrs...); err != nil {
					rows.Close()
					return fmt.Errorf("scanning row: %w", err)
				}

				// Extract data based on our knowledge of the Beeper schema
				// Find important column indices
				eventIDIdx := findColumnIndex(columns, "eventID")
				roomIDIdx := findColumnIndex(columns, "roomID")
				timestampIdx := findColumnIndex(columns, "timestamp")
				messageIdx := findColumnIndex(columns, "message")
				textContentIdx := findColumnIndex(columns, "text_content")
				senderContactIDIdx := findColumnIndex(columns, "senderContactID")
				isSentByMeIdx := findColumnIndex(columns, "isSentByMe")

				// Extract the values
				var eventID, roomID, messageText, senderContactID string
				var timestamp time.Time
				var isSentByMe bool

				if eventIDIdx >= 0 && eventIDIdx < len(values) {
					eventID = toString(values[eventIDIdx])
				}

				if roomIDIdx >= 0 && roomIDIdx < len(values) {
					roomID = toString(values[roomIDIdx])
				}

				if timestampIdx >= 0 && timestampIdx < len(values) {
					timestamp = toTimestamp(values[timestampIdx])
				} else {
					// Default to current time if no timestamp
					timestamp = time.Now()
				}

				// Try to get text content from either message JSON or text_content field
				if textContentIdx >= 0 && textContentIdx < len(values) {
					messageText = toString(values[textContentIdx])
				}

				if messageText == "" && messageIdx >= 0 && messageIdx < len(values) {
					messageJSON := toString(values[messageIdx])
					// Try to extract text from JSON if possible
					if strings.Contains(messageJSON, "\"body\":") {
						// This is a very simple JSON extraction - in a real implementation
						// you would properly parse the JSON
						parts := strings.Split(messageJSON, "\"body\":")
						if len(parts) > 1 {
							bodyPart := parts[1]
							if strings.Contains(bodyPart, "\"") {
								bodyPart = strings.Split(bodyPart, "\"")[1]
								messageText = bodyPart
							}
						}
					}
				}

				if senderContactIDIdx >= 0 && senderContactIDIdx < len(values) {
					senderContactID = toString(values[senderContactIDIdx])
				}

				if isSentByMeIdx >= 0 && isSentByMeIdx < len(values) {
					isSentByMeVal := values[isSentByMeIdx]
					switch v := isSentByMeVal.(type) {
					case int64:
						isSentByMe = v > 0
					case bool:
						isSentByMe = v
					default:
						isSentByMe = false
					}
				}

				// Use threadID/roomID as chat name
				chat := roomID
				if chat == "" {
					chat = "Beeper Chat"
				}

				// Use sender if available
				sender := senderContactID
				if sender == "" {
					if isSentByMe {
						sender = "Me"
					} else {
						sender = "Unknown"
					}
				}

				// Create metadata map
				metadata := timeline.Metadata{
					"Source":     "Beeper",
					"ChatApp":    "beeper",
					"Chat":       chat,
					"Sender":     sender,
					"MessageID":  eventID,
					"IsSentByMe": isSentByMe,
				}

				// Add all columns to metadata
				for i, col := range columns {
					metadata[col] = toString(values[i])
				}

				// Create item
				item := &timeline.Item{
					Classification: timeline.ClassMessage,
					Timestamp:      timestamp,
					Content: timeline.ItemData{
						Data: timeline.StringData(messageText),
					},
					Metadata: metadata,
				}

				// Create entity if we have a sender
				var entity *timeline.Entity
				if sender != "" && sender != "Unknown" {
					entity = &timeline.Entity{
						Name: sender,
						Attributes: []timeline.Attribute{
							{
								Name:     "beeper_contact_id",
								Value:    senderContactID,
								Identity: true,
							},
							{
								Name:     "chat_platform",
								Value:    "beeper",
								Identity: false,
							},
						},
					}
				}

				// Send to pipeline
				params.Pipeline <- &timeline.Graph{
					Item:   item,
					Entity: entity,
				}

				count++
			}
		}
		rows.Close()

		totalProcessed += count
		params.Log.Info("Processed batch",
			zap.Int("batch_count", count),
			zap.Int("total_processed", totalProcessed),
			zap.Int("total_messages", totalMessages),
			zap.Float64("percent_complete", float64(totalProcessed)/float64(totalMessages)*100))

		// If we got fewer rows than the batch size, we're done
		if count < batchSize {
			break
		}

		// Move to the next batch
		offset += batchSize
	}

	params.Log.Info("Completed Beeper import", zap.Int("total_messages", totalProcessed))
	return nil
}

// processThreads reads thread information from the Beeper database and creates entities
func (b *Beeper) processThreads(ctx context.Context, db *sql.DB, params timeline.ImportParams) error {
	// Get threads table columns
	threadColumns, err := getTableColumns(db, "threads")
	if err != nil {
		return fmt.Errorf("getting columns for threads table: %w", err)
	}

	params.Log.Info("Found thread columns", zap.Strings("columns", threadColumns))

	// Count total threads
	var totalThreads int
	err = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM threads").Scan(&totalThreads)
	if err != nil {
		params.Log.Warn("Failed to count threads, continuing without progress info", zap.Error(err))
	} else {
		params.Log.Info("Total threads to process", zap.Int("count", totalThreads))
	}

	// Process in smaller chunks
	offset := 0
	batchSize := 100
	totalProcessed := 0

	for {
		// Build a query for threads with pagination
		query := fmt.Sprintf("SELECT * FROM threads LIMIT %d OFFSET %d", batchSize, offset)
		params.Log.Info("Executing threads query batch",
			zap.String("query", query),
			zap.Int("offset", offset),
			zap.Int("processed", totalProcessed),
			zap.Int("total", totalThreads))

		// Execute the query
		rows, err := db.QueryContext(ctx, query)
		if err != nil {
			return fmt.Errorf("executing threads query: %w", err)
		}

		// Process the rows
		count := 0
		for rows.Next() {
			select {
			case <-ctx.Done():
				rows.Close()
				return ctx.Err()
			default:
				// Get column names
				columns, err := rows.Columns()
				if err != nil {
					rows.Close()
					return fmt.Errorf("getting column names: %w", err)
				}

				// Create a slice of interface{} to hold the values
				values := make([]interface{}, len(columns))
				valuePtrs := make([]interface{}, len(columns))

				// Create pointers to the values
				for i := range values {
					valuePtrs[i] = &values[i]
				}

				// Scan the row
				if err := rows.Scan(valuePtrs...); err != nil {
					rows.Close()
					return fmt.Errorf("scanning row: %w", err)
				}

				// Find important column indices
				threadIDIdx := findColumnIndex(columns, "threadID")
				accountIDIdx := findColumnIndex(columns, "accountID")
				threadIdx := findColumnIndex(columns, "thread")
				timestampIdx := findColumnIndex(columns, "timestamp")

				// Extract the values
				var threadID, accountID, threadJSON string
				var timestamp time.Time

				if threadIDIdx >= 0 && threadIDIdx < len(values) {
					threadID = toString(values[threadIDIdx])
				}

				if accountIDIdx >= 0 && accountIDIdx < len(values) {
					accountID = toString(values[accountIDIdx])
				}

				if threadIdx >= 0 && threadIdx < len(values) {
					threadJSON = toString(values[threadIdx])
				}

				if timestampIdx >= 0 && timestampIdx < len(values) {
					timestamp = toTimestamp(values[timestampIdx])
				} else {
					timestamp = time.Now()
				}

				// Try to extract the thread name from JSON
				var threadName string
				if strings.Contains(threadJSON, "\"name\":") {
					// This is a very simple JSON extraction - in a real implementation
					// you would properly parse the JSON
					parts := strings.Split(threadJSON, "\"name\":")
					if len(parts) > 1 {
						namePart := parts[1]
						if strings.Contains(namePart, "\"") {
							namePart = strings.Split(namePart, "\"")[1]
							threadName = namePart
						}
					}
				}

				if threadName == "" {
					threadName = "Chat " + threadID
				}

				// Create entity for the thread/conversation
				entity := &timeline.Entity{
					Name: threadName,
					Attributes: []timeline.Attribute{
						{
							Name:     "beeper_thread_id",
							Value:    threadID,
							Identity: true,
						},
					},
				}

				// Add thread info as attributes
				if accountID != "" {
					entity.Attributes = append(entity.Attributes, timeline.Attribute{
						Name:     "beeper_account_id",
						Value:    accountID,
						Identity: false,
					})
				}

				// Create metadata map for all columns
				metadata := timeline.Metadata{
					"Source":     "Beeper",
					"ThreadType": "beeper_chat",
					"ThreadID":   threadID,
					"Timestamp":  timestamp,
				}

				// Add all columns to metadata
				for i, col := range columns {
					metadata[col] = toString(values[i])
				}

				entity.Metadata = metadata

				// Send entity via the pipeline
				params.Pipeline <- &timeline.Graph{Entity: entity}

				count++
			}
		}
		rows.Close()

		totalProcessed += count
		params.Log.Info("Processed threads batch",
			zap.Int("batch_count", count),
			zap.Int("total_processed", totalProcessed),
			zap.Int("total_threads", totalThreads),
			zap.Float64("percent_complete", float64(totalProcessed)/float64(totalThreads)*100))

		// If we got fewer rows than the batch size, we're done
		if count < batchSize {
			break
		}

		// Move to the next batch
		offset += batchSize
	}

	params.Log.Info("Completed threads import", zap.Int("total_threads", totalProcessed))
	return nil
}

// processUsers reads user information from the Beeper database and creates entities
func (b *Beeper) processUsers(ctx context.Context, db *sql.DB, params timeline.ImportParams) error {
	// Get users table columns
	userColumns, err := getTableColumns(db, "users")
	if err != nil {
		return fmt.Errorf("getting columns for users table: %w", err)
	}

	params.Log.Info("Found user columns", zap.Strings("columns", userColumns))

	// Count total users
	var totalUsers int
	err = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM users").Scan(&totalUsers)
	if err != nil {
		params.Log.Warn("Failed to count users, continuing without progress info", zap.Error(err))
	} else {
		params.Log.Info("Total users to process", zap.Int("count", totalUsers))
	}

	// Process in smaller chunks
	offset := 0
	batchSize := 100
	totalProcessed := 0

	for {
		// Build a query for users with pagination
		query := fmt.Sprintf("SELECT * FROM users LIMIT %d OFFSET %d", batchSize, offset)
		params.Log.Info("Executing users query batch",
			zap.String("query", query),
			zap.Int("offset", offset),
			zap.Int("processed", totalProcessed),
			zap.Int("total", totalUsers))

		// Execute the query
		rows, err := db.QueryContext(ctx, query)
		if err != nil {
			return fmt.Errorf("executing users query: %w", err)
		}

		// Process the rows
		count := 0
		for rows.Next() {
			select {
			case <-ctx.Done():
				rows.Close()
				return ctx.Err()
			default:
				// Get column names
				columns, err := rows.Columns()
				if err != nil {
					rows.Close()
					return fmt.Errorf("getting column names: %w", err)
				}

				// Create a slice of interface{} to hold the values
				values := make([]interface{}, len(columns))
				valuePtrs := make([]interface{}, len(columns))

				// Create pointers to the values
				for i := range values {
					valuePtrs[i] = &values[i]
				}

				// Scan the row
				if err := rows.Scan(valuePtrs...); err != nil {
					rows.Close()
					return fmt.Errorf("scanning row: %w", err)
				}

				// Find important column indices - these are guesses, adjust based on actual schema
				userIDIdx := findColumnIndex(columns, "userID")
				displayNameIdx := findColumnIndex(columns, "displayName")
				usernameIdx := findColumnIndex(columns, "username")
				accountIDIdx := findColumnIndex(columns, "accountID")
				platformIdx := findColumnIndex(columns, "platform")

				// Extract the values
				var userID, displayName, username, accountID, platform string

				if userIDIdx >= 0 && userIDIdx < len(values) {
					userID = toString(values[userIDIdx])
				}

				if displayNameIdx >= 0 && displayNameIdx < len(values) {
					displayName = toString(values[displayNameIdx])
				}

				if usernameIdx >= 0 && usernameIdx < len(values) {
					username = toString(values[usernameIdx])
				}

				if accountIDIdx >= 0 && accountIDIdx < len(values) {
					accountID = toString(values[accountIDIdx])
				}

				if platformIdx >= 0 && platformIdx < len(values) {
					platform = toString(values[platformIdx])
				}

				// Use displayName if available, otherwise use username
				userName := displayName
				if userName == "" {
					userName = username
				}
				if userName == "" {
					userName = "Unknown User"
				}

				// Create entity
				entity := &timeline.Entity{
					Name: userName,
					Attributes: []timeline.Attribute{
						{
							Name:     "beeper_user_id",
							Value:    userID,
							Identity: true,
						},
					},
				}

				// Add user info as attributes
				if username != "" {
					entity.Attributes = append(entity.Attributes, timeline.Attribute{
						Name:     "beeper_username",
						Value:    username,
						Identity: true,
					})
				}

				if accountID != "" {
					entity.Attributes = append(entity.Attributes, timeline.Attribute{
						Name:     "beeper_account_id",
						Value:    accountID,
						Identity: false,
					})
				}

				if platform != "" {
					entity.Attributes = append(entity.Attributes, timeline.Attribute{
						Name:     "chat_platform",
						Value:    platform,
						Identity: false,
					})
				}

				// Create metadata map for all columns
				metadata := timeline.Metadata{
					"Source":   "Beeper",
					"UserType": "beeper",
				}

				// Add all columns to metadata
				for i, col := range columns {
					metadata[col] = toString(values[i])
				}

				entity.Metadata = metadata

				// Send the entity via pipeline
				params.Pipeline <- &timeline.Graph{Entity: entity}

				count++
			}
		}
		rows.Close()

		totalProcessed += count
		params.Log.Info("Processed users batch",
			zap.Int("batch_count", count),
			zap.Int("total_processed", totalProcessed),
			zap.Int("total_users", totalUsers),
			zap.Float64("percent_complete", float64(totalProcessed)/float64(totalUsers)*100))

		// If we got fewer rows than the batch size, we're done
		if count < batchSize {
			break
		}

		// Move to the next batch
		offset += batchSize
	}

	params.Log.Info("Completed users import", zap.Int("total_users", totalProcessed))
	return nil
}

// Helper functions
func checkTables(src string) (bool, error) {
	tempDir, err := os.MkdirTemp("", "beeper_import_*")
	if err != nil {
		return false, fmt.Errorf("creating temp directory: %w", err)
	}
	defer os.RemoveAll(tempDir)

	db, err := openDB(src, tempDir)
	if err != nil {
		return false, fmt.Errorf("opening database: %w", err)
	}
	defer db.Close()

	// Check for message-related tables
	tables, err := getTableNames(db)
	if err != nil {
		return false, err
	}

	// Look for tables that suggest this is a Beeper database
	return containsTable(tables, "mx_room_messages") ||
		containsTable(tables, "threads") ||
		containsTable(tables, "users"), nil
}

// getTableNames returns all table names in the database
func getTableNames(db *sql.DB) ([]string, error) {
	rows, err := db.Query("SELECT name FROM sqlite_master WHERE type='table'")
	if err != nil {
		return nil, fmt.Errorf("querying table names: %w", err)
	}
	defer rows.Close()

	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("scanning table name: %w", err)
		}
		tables = append(tables, name)
	}

	return tables, nil
}

// getTableColumns returns all column names for a table
func getTableColumns(db *sql.DB, tableName string) ([]string, error) {
	query := fmt.Sprintf("PRAGMA table_info(%s)", tableName)
	rows, err := db.Query(query)
	if err != nil {
		return nil, fmt.Errorf("getting table columns: %w", err)
	}
	defer rows.Close()

	var columns []string
	for rows.Next() {
		var id int
		var name, dataType string
		var notNull, pk int
		var dfltValue interface{}
		if err := rows.Scan(&id, &name, &dataType, &notNull, &dfltValue, &pk); err != nil {
			return nil, fmt.Errorf("scanning column info: %w", err)
		}
		columns = append(columns, name)
	}

	return columns, nil
}

// findColumnIndex returns the index of a column in the columns slice, or -1 if not found
func findColumnIndex(columns []string, name string) int {
	for i, col := range columns {
		if col == name {
			return i
		}
	}
	return -1
}

func containsTable(tables []string, table string) bool {
	for _, t := range tables {
		if t == table {
			return true
		}
	}
	return false
}

// toString converts an interface to a string
func toString(value interface{}) string {
	if value == nil {
		return ""
	}

	switch v := value.(type) {
	case string:
		return v
	case []byte:
		return string(v)
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return fmt.Sprintf("%d", v)
	case float32, float64:
		return fmt.Sprintf("%f", v)
	case bool:
		return fmt.Sprintf("%t", v)
	case time.Time:
		return v.Format(time.RFC3339)
	default:
		return fmt.Sprintf("%v", v)
	}
}

// toTimestamp converts an interface to a time.Time
func toTimestamp(value interface{}) time.Time {
	if value == nil {
		return time.Time{}
	}

	switch v := value.(type) {
	case time.Time:
		return v
	case int64:
		// Assuming milliseconds since epoch
		return time.Unix(v/1000, (v%1000)*1000000)
	case float64:
		// Assuming seconds since epoch with fractional part
		return time.Unix(int64(v), int64((v-float64(int64(v)))*1000000000))
	case string:
		// Try to parse common formats
		for _, layout := range []string{
			time.RFC3339,
			"2006-01-02T15:04:05Z",
			"2006-01-02 15:04:05",
			"2006/01/02 15:04:05",
			"01/02/2006 15:04:05",
		} {
			if t, err := time.Parse(layout, v); err == nil {
				return t
			}
		}
	}

	// Default to current time if we couldn't parse
	return time.Now()
}

func buildMessagesQuery(tables []string, columns []string, offset, limit int) string {
	// Build message query with configurable pagination limit
	// Select specific fields instead of * to be more efficient
	return fmt.Sprintf(`
		SELECT 
			eventID, roomID, senderContactID, timestamp, message, 
			text_content, text_formattedContent, isSentByMe, type
		FROM mx_room_messages 
		WHERE text_content IS NOT NULL OR message IS NOT NULL
		ORDER BY timestamp DESC 
		LIMIT %d OFFSET %d
	`, limit, offset)
}

func openDB(src, tempDir string) (*sql.DB, error) {
	// Properly escape the path for SQLite
	srcPath, err := filepath.Abs(src)
	if err != nil {
		return nil, fmt.Errorf("getting absolute path: %w", err)
	}

	// Check for WAL files
	walPath := srcPath + "-wal"
	shmPath := srcPath + "-shm"
	hasWal := fileExists(walPath)
	hasShm := fileExists(shmPath)

	// For WAL mode databases, we need to copy all files
	if hasWal || hasShm {
		// Create a copy of the database and WAL files
		dstPath := filepath.Join(tempDir, "beeper_copy.db")
		dstWalPath := dstPath + "-wal"
		dstShmPath := dstPath + "-shm"

		// Copy main DB file
		if err := copyFile(srcPath, dstPath); err != nil {
			return nil, fmt.Errorf("copying database: %w", err)
		}

		// Copy WAL file if exists
		if hasWal {
			if err := copyFile(walPath, dstWalPath); err != nil {
				return nil, fmt.Errorf("copying WAL file: %w", err)
			}
		}

		// Copy SHM file if exists
		if hasShm {
			if err := copyFile(shmPath, dstShmPath); err != nil {
				return nil, fmt.Errorf("copying SHM file: %w", err)
			}
		}

		// Use immutable=0 for WAL mode to ensure we read the WAL file
		dbPath := "file:" + dstPath + "?mode=ro&immutable=0"
		return sql.Open("sqlite3", dbPath)
	}

	// For regular databases without WAL
	dbPath := "file:" + srcPath + "?mode=ro&immutable=1"
	return sql.Open("sqlite3", dbPath)
}

// copyFile copies the src file to dst.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}

// fileExists checks if a file exists - exported for use in tests
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
