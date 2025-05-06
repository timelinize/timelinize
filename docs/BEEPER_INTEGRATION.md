# Beeper Integration for Timelinize

This document explains how to use the Beeper integration for Timelinize to import your Beeper chat history.

## What is imported

The Beeper integration imports the following data from your Beeper application:

1. **Messages**: All chat messages from all your Beeper rooms/chats
2. **Threads**: Information about your chat rooms/conversations
3. **Users**: Information about contacts and users in your Beeper network

## How to import Beeper data

### Method 1: Using the GUI (Manual)

1. Start Timelinize:
   ```
   ./.bin/timelinize_darwin_arm64
   ```

2. In the Timelinize web UI, click on "Sources" in the left sidebar

3. Select "Beeper" from the list of datasources

4. Navigate to your Beeper database file:
   - Standard location: `~/Library/Application Support/BeeperTexts/index.db`
   - Or choose another location if you have Beeper installed elsewhere

5. Click "Start Import" to begin the import process

6. Wait for the import to complete - this may take several minutes depending on the size of your Beeper database

7. Once complete, you can explore your Beeper data in the Timeline and Entities views

### Method 2: Using the Automated Import Script

We've created a script to automate the Beeper import process:

```
./tools/import-beeper.sh
```

This script:
1. Automatically locates your Beeper database 
2. Creates a temporary config file for Timelinize
3. Runs Timelinize in auto-import mode
4. Waits for the import to complete
5. Creates an output database at `~/.timelinize/beeper-import.db`

#### Options

The script supports several options:

```
./tools/import-beeper.sh [options]

Options:
  -h, --help             Show this help message
  -o, --output DIR       Output directory (default: $HOME/.timelinize)
  -b, --beeper-db PATH   Path to the Beeper database
  -t, --timelinize PATH  Path to the Timelinize binary
  -v, --verbose          Enable verbose output
```

## Viewing Your Beeper Data

After importing, you can:

1. Use the "Timeline" view to see messages chronologically
2. Use the "Entities" view to see contacts and chat rooms
3. Use the "Search" view to find specific messages
4. Create visualizations and timelines of your messaging history

## Troubleshooting

If you encounter issues with the Beeper integration:

1. **Database not found**: Ensure your Beeper database exists at the expected location
2. **No messages imported**: Some newer versions of Beeper store messages in different tables; check if you're using a compatible version
3. **WAL mode issues**: If the import fails, try closing Beeper completely before importing
4. **Large database performance**: For very large Beeper databases (>1GB), the import may take a while, be patient

## Technical Details

The Beeper integration works by:

1. Locating and opening the Beeper SQLite database
2. Handling WAL mode if enabled
3. Extracting messages from the `mx_room_messages` table
4. Extracting thread/chat information from the `threads` table
5. Extracting user information from the `users` table
6. Converting the data into Timelinize's data model
7. Saving the data to a Timelinize database

## Future Improvements

Planned improvements for the Beeper integration:

1. Support for images and attachments
2. Better handling of chat apps (understanding which platform each chat is from)
3. Adding support for Beeper bridged platforms metadata
4. Performance optimizations for very large databases 