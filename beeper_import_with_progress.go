//go:build tools
// +build tools

package main

import (
	"database/sql"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3" // SQLite driver
)

// ProgressBar represents a visual progress bar
type ProgressBar struct {
	total      int
	current    int
	width      int
	lastUpdate time.Time
	startTime  time.Time
}

// NewProgressBar creates a new progress bar
func NewProgressBar(total int) *ProgressBar {
	return &ProgressBar{
		total:      total,
		current:    0,
		width:      50,
		lastUpdate: time.Now(),
		startTime:  time.Now(),
	}
}

// Update updates the progress bar
func (p *ProgressBar) Update(current int) {
	p.current = current
	now := time.Now()

	// Only update the bar every 100ms to avoid flickering
	if now.Sub(p.lastUpdate) < 100*time.Millisecond {
		return
	}
	p.lastUpdate = now

	percent := float64(p.current) / float64(p.total)
	filled := int(percent * float64(p.width))

	// Create the progress bar
	bar := "["
	for i := 0; i < p.width; i++ {
		if i < filled {
			bar += "="
		} else if i == filled {
			bar += ">"
		} else {
			bar += " "
		}
	}
	bar += "]"

	// Calculate time remaining
	elapsed := now.Sub(p.startTime)
	var timeRemaining string
	if p.current > 0 {
		totalTime := elapsed.Seconds() / float64(p.current) * float64(p.total)
		remaining := totalTime - elapsed.Seconds()
		if remaining > 0 {
			timeRemaining = fmt.Sprintf(" ETA: %s", formatDuration(time.Duration(remaining)*time.Second))
		}
	}

	fmt.Printf("\r%s %3.0f%% (%d/%d)%s", bar, percent*100, p.current, p.total, timeRemaining)
}

// Complete marks the progress bar as complete
func (p *ProgressBar) Complete() {
	p.Update(p.total)
	fmt.Println()
}

// formatDuration formats a duration in a human-readable format
func formatDuration(d time.Duration) string {
	hours := int(d.Hours())
	minutes := int(d.Minutes()) % 60
	seconds := int(d.Seconds()) % 60

	if hours > 0 {
		return fmt.Sprintf("%dh %dm %ds", hours, minutes, seconds)
	} else if minutes > 0 {
		return fmt.Sprintf("%dm %ds", minutes, seconds)
	}
	return fmt.Sprintf("%ds", seconds)
}

func main() {
	fmt.Println("=== Timelinize + Beeper Automated Integration ===")

	// Step 1: Test the Beeper database
	if !runBeeperDatabaseTest() {
		fmt.Println("\n❌ Failed to validate Beeper database")
		os.Exit(1)
	}

	// Step 2: Run the automated integration
	if !runBeeperIntegration() {
		fmt.Println("\n❌ Failed to integrate Beeper with Timelinize")
		os.Exit(1)
	}

	// Step 3: Start the Timelinize UI
	fmt.Println("\n✅ Integration complete! Starting Timelinize UI...")
	startTimelinizeUI()
}

// runBeeperDatabaseTest tests the Beeper database
func runBeeperDatabaseTest() bool {
	fmt.Println("=== Beeper Database Test ===")

	// Find Beeper database
	beeperPath := findBeeperDatabase()
	if beeperPath == "" {
		fmt.Println("❌ ERROR: Beeper database not found")
		return false
	}

	fmt.Printf("✓ Found Beeper database at: %s\n", beeperPath)
	dbSize := getFileSize(beeperPath)
	fmt.Printf("Database size: %.2f GB\n", dbSize)

	// Test database contents
	if !testDatabaseContents(beeperPath) {
		fmt.Println("❌ WARNING: Could not verify Beeper database structure")
		return false
	} else {
		fmt.Println("✓ Beeper database contains expected tables")
	}

	return true
}

// runBeeperIntegration integrates Beeper with Timelinize programmatically
func runBeeperIntegration() bool {
	fmt.Println("\n=== Running Beeper Integration ===")

	// Find Beeper database
	beeperPath := findBeeperDatabase()
	if beeperPath == "" {
		fmt.Println("❌ ERROR: Beeper database not found")
		return false
	}

	// Create output directory
	homeDir, err := os.UserHomeDir()
	if err != nil {
		fmt.Printf("❌ ERROR: Failed to get home directory: %v\n", err)
		return false
	}

	outputDir := filepath.Join(homeDir, ".timelinize")
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		fmt.Printf("❌ ERROR: Failed to create output directory: %v\n", err)
		return false
	}

	// Use the import-beeper.sh script which is more reliable than trying to use command line args
	fmt.Println("Starting Beeper import using import-beeper.sh script...")

	// Create and run import command
	importScript := "./tools/import-beeper.sh"
	if !fileExists(importScript) {
		fmt.Printf("❌ ERROR: Import script not found at %s\n", importScript)
		return false
	}

	// Get message count to estimate progress
	db, err := sql.Open("sqlite3", fmt.Sprintf("file:%s?mode=ro", beeperPath))
	if err != nil {
		fmt.Printf("Error opening database: %v\n", err)
		return false
	}

	var totalMessages int
	err = db.QueryRow("SELECT COUNT(*) FROM mx_room_messages").Scan(&totalMessages)
	if err != nil {
		fmt.Printf("Error counting messages: %v\n", err)
		totalMessages = 1000000 // Fallback estimate
	}
	db.Close()

	// Create a progress bar
	fmt.Printf("Preparing to import approximately %d messages...\n", totalMessages)
	progressBar := NewProgressBar(totalMessages)

	// Create pipes for capturing output while still showing it to user
	r, w := io.Pipe()

	// Set up command with piped output
	cmd := exec.Command(importScript,
		"--beeper-db", beeperPath,
		"--verbose")

	cmd.Stdout = io.MultiWriter(os.Stdout, w)
	cmd.Stderr = io.MultiWriter(os.Stderr, w)

	// Start the command
	startTime := time.Now()
	if err := cmd.Start(); err != nil {
		fmt.Printf("❌ ERROR: Failed to start import: %v\n", err)
		return false
	}

	// Read output in background to track progress
	go func() {
		defer w.Close()
		buffer := make([]byte, 1024)
		processedMessages := 0

		for {
			n, err := r.Read(buffer)
			if err != nil {
				if err != io.EOF {
					fmt.Printf("Error reading output: %v\n", err)
				}
				break
			}

			output := string(buffer[:n])

			// Look for progress indicators in the output
			if strings.Contains(output, "Processed messages") {
				// Try to parse message count
				parts := strings.Split(output, "Processed messages")
				if len(parts) > 1 {
					countParts := strings.Split(parts[1], ")")
					if len(countParts) > 0 {
						fmt.Sscanf(countParts[0], " (%d", &processedMessages)
						progressBar.Update(processedMessages)
					}
				}
			}
		}
	}()

	// Wait for the command to complete
	err = cmd.Wait()
	elapsed := time.Since(startTime)

	if err != nil {
		fmt.Printf("\n❌ ERROR: Import failed: %v\n", err)
		return false
	}

	progressBar.Complete()
	fmt.Printf("\n✅ Import completed in %s\n", elapsed.Round(time.Second))
	fmt.Printf("Output database: %s\n", filepath.Join(outputDir, "beeper-import.db"))

	return true
}

// startTimelinizeUI starts the Timelinize UI
func startTimelinizeUI() {
	timelinizeBin := findTimelinizeBinary()
	if timelinizeBin == "" {
		fmt.Println("❌ ERROR: Timelinize binary not found")
		return
	}

	fmt.Println("Starting Timelinize UI...")
	cmd := exec.Command(timelinizeBin)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	// Start in the background
	if err := cmd.Start(); err != nil {
		fmt.Printf("❌ ERROR: Failed to start Timelinize UI: %v\n", err)
		return
	}

	fmt.Println("✅ Timelinize UI started successfully!")
	fmt.Println("You can now view your imported Beeper data in the Timeline and Entities views.")
	fmt.Println("\nTIP: Look for your Beeper data in the 'Timeline' view. You can filter by:")
	fmt.Println("  - Date range")
	fmt.Println("  - Message content")
	fmt.Println("  - Contact names")
}

// findTimelinizeBinary locates the Timelinize binary
func findTimelinizeBinary() string {
	// Check current directory first
	candidates := []string{
		"./.bin/timelinize_darwin_arm64",
		"./.bin/timelinize_darwin_amd64",
		"./.bin/timelinize",
		"./timelinize",
	}

	for _, candidate := range candidates {
		if fileExists(candidate) {
			return candidate
		}
	}

	return ""
}

// findBeeperDatabase locates the Beeper database on the local system
func findBeeperDatabase() string {
	// Check standard location first
	homeDir, err := os.UserHomeDir()
	if err != nil {
		fmt.Printf("Error getting home directory: %v\n", err)
		return ""
	}

	standardPath := filepath.Join(homeDir, "Library", "Application Support", "BeeperTexts", "index.db")
	if fileExists(standardPath) {
		return standardPath
	}

	// If not found in standard location, check alternative locations
	altPaths := []string{
		filepath.Join(homeDir, "Library", "Application Support", "Beeper", "index.db"),
		filepath.Join(homeDir, "Library", "Caches", "BeeperTexts", "index.db"),
	}

	for _, path := range altPaths {
		if fileExists(path) {
			return path
		}
	}

	return ""
}

// testDatabaseContents tests if the database contains the expected tables
func testDatabaseContents(dbPath string) bool {
	// Connect to database
	db, err := sql.Open("sqlite3", fmt.Sprintf("file:%s?mode=ro", dbPath))
	if err != nil {
		fmt.Printf("Error opening database: %v\n", err)
		return false
	}
	defer db.Close()

	// Check for message-related tables
	rows, err := db.Query("SELECT name FROM sqlite_master WHERE type='table'")
	if err != nil {
		fmt.Printf("Error querying tables: %v\n", err)
		return false
	}
	defer rows.Close()

	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			fmt.Printf("Error scanning table name: %v\n", err)
			return false
		}
		tables = append(tables, name)
		fmt.Printf("Found table: %s\n", name)
	}

	// Define potential message tables
	potentialMessageTables := []string{"messages", "mx_room_messages", "Events", "mx_events"}

	// Check each potential message table
	totalMessages := 0
	for _, tableName := range potentialMessageTables {
		if containsTable(tables, tableName) {
			fmt.Printf("\nChecking message count in table '%s'...\n", tableName)
			var count int
			err := db.QueryRow(fmt.Sprintf("SELECT COUNT(*) FROM %s", tableName)).Scan(&count)
			if err != nil {
				fmt.Printf("Error counting messages in %s: %v\n", tableName, err)
			} else {
				fmt.Printf("Found %d messages in table '%s'\n", count, tableName)
				totalMessages += count

				// If table has messages, check the table structure
				if count > 0 {
					checkTableStructure(db, tableName)
				}
			}
		}
	}

	// Check thread and user tables too
	for _, tableName := range []string{"threads", "users"} {
		if containsTable(tables, tableName) {
			var count int
			err := db.QueryRow(fmt.Sprintf("SELECT COUNT(*) FROM %s", tableName)).Scan(&count)
			if err != nil {
				fmt.Printf("Error counting rows in %s: %v\n", tableName, err)
			} else {
				fmt.Printf("Found %d rows in table '%s'\n", count, tableName)
			}
		}
	}

	fmt.Printf("\nTotal messages across all tables: %d\n", totalMessages)

	// Return whether any message tables were found
	return containsAnyTable(tables, potentialMessageTables)
}

// checkTableStructure examines the structure of a table
func checkTableStructure(db *sql.DB, tableName string) {
	// Get column information
	rows, err := db.Query(fmt.Sprintf("PRAGMA table_info(%s)", tableName))
	if err != nil {
		fmt.Printf("Error getting column info for %s: %v\n", tableName, err)
		return
	}
	defer rows.Close()

	fmt.Printf("Table '%s' structure:\n", tableName)
	for rows.Next() {
		var cid int
		var name, typ string
		var notnull, pk int
		var dfltValue interface{}
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dfltValue, &pk); err != nil {
			fmt.Printf("Error scanning column info: %v\n", err)
			return
		}
		fmt.Printf("  - Column: %s (Type: %s)\n", name, typ)
	}
}

// getFileSize returns the size of a file in GB
func getFileSize(path string) float64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}

	return float64(info.Size()) / (1024 * 1024 * 1024) // Convert bytes to GB
}

// containsTable checks if tables slice contains a specific table
func containsTable(tables []string, tableName string) bool {
	for _, t := range tables {
		if t == tableName {
			return true
		}
	}
	return false
}

// containsAnyTable checks if tables slice contains any of the potential tables
func containsAnyTable(tables []string, potentialTables []string) bool {
	for _, pt := range potentialTables {
		if containsTable(tables, pt) {
			return true
		}
	}
	return false
}

// fileExists checks if a file exists
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
