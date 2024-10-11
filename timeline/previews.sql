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

-- likely to perform faster for medium-large blobs: https://www.sqlite.org/intern-v-extern-blob.html
PRAGMA page_size = 16384;

-- consisting of a single row, the ID of the repo this belongs to
CREATE TABLE IF NOT EXISTS "repo_link" (
	"repo_id" TEXT PRIMARY KEY
);

CREATE TABLE IF NOT EXISTS "previews" (
	"data_file" TEXT UNIQUE COLLATE NOCASE, -- the data file in the repo this is a preview for (same as items.data_file in the main schema)
	"mime_type" TEXT, -- since we don't have file extensions to give us a hint, the content-type of the thumbnail/preview
	"content" BLOB    -- the actual bytes of the thumbnail/preview
);

-- ensure this DB remains linked to only one timeline repo
CREATE TRIGGER IF NOT EXISTS only_one_linked_repo
	BEFORE INSERT ON repo_link
	FOR EACH ROW
	BEGIN
		SELECT RAISE(ABORT, 'Cannot link more than one timeline repository')
		WHERE (SELECT count() FROM repo_link) > 0;
	END;
