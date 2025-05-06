package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/timelinize/timelinize/datasources/beeper"
	"github.com/timelinize/timelinize/timeline"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

func main() {
	// Define command line flags
	beeperPath := flag.String("beeper-path", "", "Path to Beeper index.db file (optional)")
	outputPath := flag.String("output", "", "Path to output directory (default: $HOME/.timelinize)")
	verbose := flag.Bool("verbose", false, "Enable verbose logging")
	flag.Parse()

	// Set up logging
	logConfig := zap.NewDevelopmentConfig()
	if *verbose {
		logConfig.Level = zap.NewAtomicLevelAt(zapcore.DebugLevel)
	} else {
		logConfig.Level = zap.NewAtomicLevelAt(zapcore.InfoLevel)
	}
	
	logger, err := logConfig.Build()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating logger: %v\n", err)
		os.Exit(1)
	}
	defer logger.Sync()

	// Find Beeper database path if not specified
	dbPath := *beeperPath
	if dbPath == "" {
		homedir, err := os.UserHomeDir()
		if err != nil {
			logger.Fatal("Error getting home directory", zap.Error(err))
		}

		// Look for Beeper database in default location
		defaultPath := filepath.Join(homedir, "Library", "Application Support", "BeeperTexts", "index.db")
		if fileExists(defaultPath) {
			dbPath = defaultPath
			logger.Info("Found Beeper database at default location", zap.String("path", dbPath))
		} else {
			logger.Fatal("Beeper database not found. Please specify path with --beeper-path")
		}
	}

	// Determine output path
	outDir := *outputPath
	if outDir == "" {
		homedir, err := os.UserHomeDir()
		if err != nil {
			logger.Fatal("Error getting home directory", zap.Error(err))
		}
		outDir = filepath.Join(homedir, ".timelinize")
	}

	// Create output directory if it doesn't exist
	if err := os.MkdirAll(outDir, 0755); err != nil {
		logger.Fatal("Error creating output directory", zap.String("dir", outDir), zap.Error(err))
	}

	// Create an in-memory database for testing
	logger.Info("Setting up timeline database")
	db, err := timeline.NewDB(filepath.Join(outDir, "beeper-import.db"))
	if err != nil {
		logger.Fatal("Error creating database", zap.Error(err))
	}
	defer db.Close()

	// Create a pipeline channel
	pipeline := make(chan *timeline.Graph, 1000)
	
	// Set up consumer to process and count items
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	
	// Count variables
	var messageCount, threadCount, userCount int
	
	// Start consumer goroutine
	done := make(chan struct{})
	go func() {
		defer close(done)
		
		for {
			select {
			case <-ctx.Done():
				return
			case graph, ok := <-pipeline:
				if !ok {
					return
				}
				
				// Process the item
				if graph.Item != nil {
					// Save to database
					if err := db.SaveItem(graph.Item); err != nil {
						logger.Error("Error saving item", zap.Error(err))
						continue
					}
					
					// Count by type
					if graph.Item.Classification == timeline.ClassMessage {
						messageCount++
						if messageCount%100 == 0 {
							logger.Info("Messages imported", zap.Int("count", messageCount))
						}
					}
				}
				
				// Process entity if present
				if graph.Entity != nil {
					if err := db.SaveEntity(graph.Entity); err != nil {
						logger.Error("Error saving entity", zap.Error(err))
						continue
					}
				}
			}
		}
	}()

	// Track entities separately
	var entities []*timeline.Entity
	saveEntity := func(entity *timeline.Entity) error {
		entities = append(entities, entity)
		
		// Count by type
		for _, attr := range entity.Attributes {
			if attr.Name == "beeper_thread_id" {
				threadCount++
				if threadCount%10 == 0 {
					logger.Info("Thread entities imported", zap.Int("count", threadCount))
				}
				break
			} else if attr.Name == "beeper_user_id" {
				userCount++
				if userCount%10 == 0 {
					logger.Info("User entities imported", zap.Int("count", userCount))
				}
				break
			}
		}
		
		return db.SaveEntity(entity)
	}

	// Create import params
	params := timeline.ImportParams{
		Pipeline:   pipeline,
		SendEntity: saveEntity,
		Log:        logger,
	}

	// Run the import
	logger.Info("Starting Beeper import", zap.String("path", dbPath))
	startTime := time.Now()
	
	dirEntry := &filePathEntry{path: dbPath}
	beeperImporter := &beeper.Beeper{}
	
	err = beeperImporter.FileImport(ctx, dirEntry, params)
	if err != nil {
		logger.Fatal("Import failed", zap.Error(err))
	}
	
	// Close the pipeline and wait for processing to finish
	close(pipeline)
	<-done
	
	// Display results
	elapsed := time.Since(startTime)
	logger.Info("Import completed",
		zap.Duration("time", elapsed),
		zap.Int("messages", messageCount),
		zap.Int("threads", threadCount),
		zap.Int("users", userCount),
		zap.String("database", filepath.Join(outDir, "beeper-import.db")),
	)
	
	// Print success message
	fmt.Printf("\nSuccessfully imported Beeper data:\n")
	fmt.Printf("  Messages: %d\n", messageCount)
	fmt.Printf("  Threads:  %d\n", threadCount)
	fmt.Printf("  Users:    %d\n", userCount)
	fmt.Printf("  Time:     %v\n", elapsed.Round(time.Second))
	fmt.Printf("  Database: %s\n", filepath.Join(outDir, "beeper-import.db"))
	fmt.Printf("\nYou can now open this database in Timelinize to view your data.\n")
}

// Helper types and functions

// filePathEntry implements the timeline.DirEntry interface for a file path
type filePathEntry struct {
	path string
}

func (f *filePathEntry) Name() string {
	return filepath.Base(f.path)
}

func (f *filePathEntry) FullPath() string {
	return f.path
}

// fileExists checks if a file exists
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
} 