#!/bin/bash
# Beeper Import Helper for Timelinize
# This script prepares your Beeper database for import into Timelinize

set -e

# Default values
BEEPER_DB="$HOME/Library/Application Support/BeeperTexts/index.db"
TIMELINIZE_BIN="./.bin/timelinize_darwin_arm64"
VERBOSE=false

# Usage information
usage() {
  echo "Usage: $0 [options]"
  echo "Options:"
  echo "  -h, --help             Show this help message"
  echo "  -b, --beeper-db PATH   Path to the Beeper database (default: $HOME/Library/Application Support/BeeperTexts/index.db)"
  echo "  -t, --timelinize PATH  Path to the Timelinize binary (default: ./.bin/timelinize_darwin_arm64)"
  echo "  -v, --verbose          Enable verbose output"
  exit 1
}

# Parse arguments
while [[ $# -gt 0 ]]; do
  key="$1"
  case $key in
    -h|--help)
      usage
      ;;
    -b|--beeper-db)
      BEEPER_DB="$2"
      shift
      shift
      ;;
    -t|--timelinize)
      TIMELINIZE_BIN="$2"
      shift
      shift
      ;;
    -v|--verbose)
      VERBOSE=true
      shift
      ;;
    *)
      echo "Unknown option: $1"
      usage
      ;;
  esac
done

# Check if Beeper database exists
if [ ! -f "$BEEPER_DB" ]; then
  echo "Error: Beeper database not found at $BEEPER_DB"
  echo "Please specify the correct path with --beeper-db option"
  exit 1
fi

# Check if Timelinize binary exists
if [ ! -f "$TIMELINIZE_BIN" ]; then
  echo "Error: Timelinize binary not found at $TIMELINIZE_BIN"
  echo "Please specify the correct path with --timelinize option"
  exit 1
fi

# Set verbosity flag
if [ "$VERBOSE" = true ]; then
  VERBOSE_FLAG="-logtostderr"
else
  VERBOSE_FLAG=""
fi

echo "Beeper Import Helper for Timelinize"
echo "=================================="
echo "Beeper database: $BEEPER_DB"
echo ""

# Copy the database to a temporary location that's easier to access
TEMP_DIR="$HOME/.timelinize/beeper-temp"
mkdir -p "$TEMP_DIR"

TEMP_DB="$TEMP_DIR/index.db"
TEMP_WAL="$TEMP_DIR/index.db-wal"
TEMP_SHM="$TEMP_DIR/index.db-shm"

echo "Copying Beeper database to temporary location..."
cp "$BEEPER_DB" "$TEMP_DB"

# Copy WAL and SHM files if they exist
if [ -f "${BEEPER_DB}-wal" ]; then
  cp "${BEEPER_DB}-wal" "$TEMP_WAL"
fi

if [ -f "${BEEPER_DB}-shm" ]; then
  cp "${BEEPER_DB}-shm" "$TEMP_SHM"
fi

echo "Database copied successfully to: $TEMP_DB"
echo ""

# Start Timelinize
echo "Starting Timelinize..."
if [ "$VERBOSE" = true ]; then
  "$TIMELINIZE_BIN" -logtostderr &
else
  "$TIMELINIZE_BIN" &
fi

TIMELINIZE_PID=$!

# Wait for Timelinize to start
echo "Waiting for Timelinize to start..."
sleep 5

echo ""
echo "====================================================="
echo "INSTRUCTIONS FOR IMPORTING BEEPER DATA INTO TIMELINIZE"
echo "====================================================="
echo ""
echo "Timelinize is now running. Please follow these steps to import your Beeper data:"
echo ""
echo "1. In your web browser, go to the Timelinize interface"
echo "   (It should have automatically opened, or visit http://localhost:12002/)"
echo ""
echo "2. In the left sidebar, click on 'Sources'"
echo ""
echo "3. Select 'Beeper' from the list of datasources"
echo ""
echo "4. Navigate to the temporary Beeper database we created for you at:"
echo "   $TEMP_DB"
echo ""
echo "5. Click 'Start Import' to begin the import process"
echo ""
echo "6. Wait for the import to complete - this may take several minutes depending on the size of your Beeper database"
echo ""
echo "7. Once complete, you can explore your Beeper data in the Timeline and Entities views"
echo ""
echo "Press Ctrl+C when you're finished to exit Timelinize"
echo ""

# Wait for user to terminate with Ctrl+C
wait $TIMELINIZE_PID

# Clean up
echo "Cleaning up..."
rm -rf "$TEMP_DIR"

echo "Done!" 