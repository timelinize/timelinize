/*
	Timelinize
	Copyright (c) 2013 Matthew Holt

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

-- This database is only for storing thumbnails (small previews) of items in the
-- timeline. It is ancillary to the timeline, and deleting this database is not
-- harmful to the integrity of the timeline as it can be regenerated.

-- likely to perform faster for medium-large blobs: https://www.sqlite.org/intern-v-extern-blob.html
PRAGMA page_size = 16384;

-- consisting of a single row, the ID of the repo this belongs to
CREATE TABLE IF NOT EXISTS "repo_link" (
	"repo_id" TEXT PRIMARY KEY
);

CREATE TABLE IF NOT EXISTS "thumbnails" (
	"id" INTEGER PRIMARY KEY,
	
	-- only one of the following two columns should have a value:
	"data_file" TEXT UNIQUE COLLATE NOCASE, -- the data file in the repo this is a preview for (same as items.data_file in the main schema)
	"item_data_id" INTEGER UNIQUE, -- only used if the item does not have a data file (same as item_data.id in the main schema)

	"generated" INTEGER NOT NULL DEFAULT (unixepoch()), -- when the thumbnail was generated (timestamp in unix seconds UTC)
	"mime_type" TEXT NOT NULL, -- since we don't have file extensions to give us a hint, the content-type of this thumbnail/preview
	"content" BLOB NOT NULL-- the actual bytes of the thumbnail/preview
);

-- ensure this DB remains linked to only one timeline repo
CREATE TRIGGER IF NOT EXISTS only_one_linked_repo
	BEFORE INSERT ON repo_link
	FOR EACH ROW
	BEGIN
		SELECT RAISE(ABORT, 'Cannot link more than one timeline repository')
		WHERE (SELECT count() FROM repo_link) > 0;
	END;
