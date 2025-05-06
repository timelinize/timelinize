# Beeper Integration for Timelinize

This merge request adds support for importing chat data from the Beeper Desktop application into Timelinize.

## Features

- Automatic detection of Beeper database location on macOS
- Extracts messages, contacts, and timestamps from Beeper chat history
- Integrates seamlessly with existing Timelinize UI
- Preserves message metadata (sender, timestamp, content)

## Technical Implementation

The integration works by:
1. Locating the Beeper SQLite database at `~/Library/Application Support/BeeperTexts/index.db`
2. Reading message data using SQL queries to extract relevant information
3. Converting Beeper's data model to Timelinize's expected format
4. Registering a new datasource in the Timelinize application

## Testing

Tests have been added to verify:
- Proper database connection
- Accurate message count reporting
- Data integrity during import

## User Instructions

To use this integration:

1. Install Beeper Desktop from https://www.beeper.com/
2. Set up your messaging accounts in Beeper Desktop
3. Use those accounts and have some conversations
4. Run Timelinize and select the "Beeper" data source
5. Timelinize will automatically find and import your Beeper messages

The integration automatically detects the Beeper database location, so no additional configuration is required.

## Screenshots

(Screenshots would be added in the actual GitHub PR) 