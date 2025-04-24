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

-- Generic key/value store for the repo itself (version, ID, etc).
CREATE TABLE IF NOT EXISTS "repo" (
	"key" TEXT PRIMARY KEY,
	"value"
) WITHOUT ROWID;

-- A data source is where data comes from, like a content provider, like a cloud photo service,
-- social media site, or exported archive format. This table primarily exists to provide data
-- consistency for rows that refer to a data source enforced by the DB itself, using more compact
-- IDs than the program's string names.
CREATE TABLE IF NOT EXISTS "data_sources" (
	"id" INTEGER PRIMARY KEY, -- row ID, used only for DB consistency, applies only to this DB
	"name" TEXT NOT NULL UNIQUE, -- programming ID, e.g. "google_photos" as used in the application code and across different timeline DBs
	"title" TEXT NOT NULL, -- human-readable name/label
	"description" TEXT,
	"media" BLOB, -- icon/image, square dimensions work best
	"media_type" TEXT, -- MIME type of the icon
	"standard" BOOLEAN NOT NULL DEFAULT false -- true for data sources that come hard-coded with the app
);

CREATE TABLE IF NOT EXISTS "jobs" (
	"id" INTEGER PRIMARY KEY,
	"type" TEXT NOT NULL, -- import, thumbnails, etc...
	"name" TEXT, -- an optional user-assigned name
	"configuration" TEXT, -- encoded as JSON
	"hash" BLOB, -- for preventing duplicate jobs; opaque to everything except the code creating the job
	"state" TEXT NOT NULL DEFAULT 'queued', -- queued, started, paused, aborted, succeeded, failed
	"hostname" TEXT, -- hostname of the machine the job was created and configured on
	"created" INTEGER NOT NULL DEFAULT (unixepoch()), -- timestamp job was stored/enqueued in unix milliseconds UTC
	"updated" INTEGER, -- timestamp of last DB sync (in unix milliseconds UTC)
	"start" INTEGER, -- timestamp job was actually started in unix milliseconds UTC *could be future, so not called "started")
	"ended" INTEGER, -- timestamp in unix milliseconds UTC (TODO: only when finalized, or paused too?)
	"message" TEXT, -- brief message describing current status to be shown to the user, changes less frequently than log emissions
	"total" INTEGER, -- total number of units to complete
	"progress" INTEGER, -- number of units completed towards the total count
	"checkpoint" BLOB, -- required state for resuming an incomplete job
	-- if job is scheduled to run automatically at a certain interval, the following fields track that state
	"repeat" INTEGER, -- when this job is started, next job should be scheduled (inserted for future start) this many seconds from start time (not to be started if previous still running)
	"parent_job_id" INTEGER, -- the job before this one that scheduled or created this one, forming a chain or linked list
	FOREIGN KEY ("parent_job_id") REFERENCES "jobs"("id") ON UPDATE CASCADE ON DELETE SET NULL
) STRICT;

-- TODO: Update comment; should the embedding just be stored in the items table now??
--
-- Embeddings enable "intelligent" search using ML models to derive semantics and meaning.
-- By finding other embeddings that are close (dot product or euclidean distance),
-- similarity searches are possible. This requires the sqlite-vec module.
CREATE TABLE IF NOT EXISTS "embeddings" (
	"id" INTEGER PRIMARY KEY,
	"generated" INTEGER NOT NULL DEFAULT (unixepoch()), -- when the embedding was generated (timestamp in unix seconds UTC)
	"embedding" BLOB -- TODO: could define as float[768] (unless STRICT) and then use `check(typeof(contents_embedding) == 'blob' AND vec_length(contents_embedding) == 768)`
) STRICT;

-- Entity type names are hard-coded (but their IDs are not).
CREATE TABLE IF NOT EXISTS "entity_types" (
	"id" INTEGER PRIMARY KEY,
	"name" TEXT NOT NULL UNIQUE
) STRICT;

-- An entity represents a person, place, or thing (i.e. nouns). The vast majority will be
-- persons, but some may be pets/animals, companies and organizations, political
-- entities, or landmarks/places. It is not uncommon for duplicate entities to appear in
-- this table through various data imports, because each data source has a different way
-- of identifying or describing users/entities. Manual curation or post-processing may be
-- required to reconcile duplicates.
CREATE TABLE IF NOT EXISTS "entities" (
	"id" INTEGER PRIMARY KEY,
	"type_id" INTEGER NOT NULL,
	"job_id" INTEGER, -- import that originally created this entity, if via a data import
	"stored" INTEGER NOT NULL DEFAULT (unixepoch()),
	"modified" INTEGER, -- timestamp when entity was locally/manually modified (may include attributes)
	"name" TEXT COLLATE NOCASE,
	"picture_file" TEXT,
	"metadata" TEXT, -- optional extra information, encoded as JSON (should almost never be used! use attributes instead)
	"hidden" INTEGER, -- if owner would like to forget about this person, don't show in search results, etc.
	"deleted" INTEGER, -- timestamp when item was moved to trash and can be purged after some amount of time after that
	FOREIGN KEY ("type_id") REFERENCES "entity_types"("id") ON UPDATE CASCADE,
	FOREIGN KEY ("job_id") REFERENCES "jobs"("id") ON UPDATE CASCADE ON DELETE SET NULL
) STRICT;

-- Attributes describe entities. An attribute has a name and a value. The name for a particular
-- kind of attribute should be consistent throughout because some attributes need special handling
-- or can be normalized in the code (e.g. phone numbers). A single attribute may describe one or
-- more entities; attributes that don't link to any entities (via the entity_attributes table)
-- should not exist (see the relevant trigger for this). An attribute may identify an entity,
-- for example, a phone number or email address is kind of a globally-unique identifier; or an
-- attribute named "twitter_id" for example would identify an entity on Twitter. An attribute
-- that is an identity for one entity may not be for another, so that is encoded in the
-- entity_attributes table instead. The special attribute named "_entity" acts as a pass-thru
-- attribute directly to the entity by ID; this is useful if there's no particular attribute
-- relevant or known, and instead we want to specify an entity directly instead.
-- TODO: For ancestors, their identifying attribute could be a FamilySearch record ID.
CREATE TABLE IF NOT EXISTS "attributes" (
	"id" INTEGER PRIMARY KEY,
	"name" TEXT NOT NULL,
	"value" ANY NOT NULL COLLATE NOCASE,
	"alt_value" TEXT, -- optional alternate value intended for display or as a description
	-- the coordinate values below are useful if the attribute value is, in fact, a location or area
	"longitude1" REAL, -- point, or top-left corner
	"latitude1" REAL,  -- point, or top-left corner
	"longitude2" REAL, -- bottom-right corner, if box
	"latitude2" REAL,  -- bottom-right corner, if box
	"metadata" TEXT,  -- optional extra info encoded as JSON
	UNIQUE ("name", "value")
) STRICT;

-- This table links entities and attributes. An entity may have multiple attributes, and an attribute
-- may apply to multiple entities. If the attribute represents an entity's identity on a particular
-- data source, the data_source_id field will be filled out.
CREATE TABLE IF NOT EXISTS "entity_attributes" (
	"id" INTEGER PRIMARY KEY,
	"entity_id" INTEGER NOT NULL,
	"attribute_id" INTEGER NOT NULL,
	"data_source_id" INTEGER, -- if set, the attribute defines the entity's identity on this data source (a row of a certain entity_id and attribute_id can be duplicated if they are identities on different data sources)
	"job_id" INTEGER, -- the ID of the import that originated this row (originated the linkage between entity and attribute)
	-- these next two fields explain how the row came into being, by inferring the association from another attribute
	"autolink_job_id" INTEGER,    -- if set, the import that motivated linking this attribute as the entity's ID on the data source
	"autolink_attribute_id" INTEGER, -- if set, the other attribute by which this attribute was automatically linked as the entity's ID on the data source
	"start" INTEGER, -- when the attribute started applying to the entity
	"end" INTEGER,   -- when the attribute stopped applyiing to the entity
	FOREIGN KEY ("entity_id") REFERENCES "entities"("id") ON UPDATE CASCADE ON DELETE CASCADE,
	FOREIGN KEY ("attribute_id") REFERENCES "attributes"("id") ON UPDATE CASCADE ON DELETE CASCADE,
	FOREIGN KEY ("data_source_id") REFERENCES "data_sources"("id") ON UPDATE CASCADE ON DELETE CASCADE,
	FOREIGN KEY ("job_id") REFERENCES "jobs"("id") ON UPDATE CASCADE ON DELETE SET NULL,
	FOREIGN KEY ("autolink_job_id") REFERENCES "jobs"("id") ON UPDATE CASCADE ON DELETE SET NULL,
	FOREIGN KEY ("autolink_attribute_id") REFERENCES "attributes"("id") ON UPDATE CASCADE ON DELETE SET NULL
) STRICT;

-- A classification describes what kind of thing an item is, for example a text message, tweet, email, or location.
CREATE TABLE IF NOT EXISTS "classifications" (
	"id" INTEGER PRIMARY KEY,
	"standard" INTEGER NOT NULL DEFAULT 0, -- 1: hard-coded classification, 0: user-created
	"name" TEXT NOT NULL UNIQUE,  -- programming name, e.g. "message"
	"labels" TEXT NOT NULL, -- comma-separated human-friendly names, e.g. "Text message,SMS,MMS,iMessage,Texting"
	"description" TEXT NOT NULL
) STRICT;

-- TODO: Not used currently (but is supported, and accessed in queries).
CREATE TABLE IF NOT EXISTS "item_data" (
	"id" INTEGER PRIMARY KEY,
	"content" BLOB NOT NULL UNIQUE
) STRICT;

-- An item is something imported from a specific data source.
CREATE TABLE IF NOT EXISTS "items" (
	"id" INTEGER PRIMARY KEY,
	"embedding_id" INTEGER, -- associated embedding that represents the content of this item according to ML model; TODO: we may need an item_embeddings table for multiple...
	"data_source_id" INTEGER,
	"job_id" INTEGER, -- the import job that originally inserted this item
	"modified_job_id" INTEGER, -- the import job that most recently modified this existing item
	"attribute_id" INTEGER, -- owner, creator, or originator attributed to this item
	"classification_id" INTEGER,
	"original_id" TEXT, -- ID provided by the data source
	-- "embedding" BLOB, -- TODO: experimental, inline embedding
	"original_location" TEXT,     -- path or location of the file/data on the original data source; should include filename if applicable
	"intermediate_location" TEXT, -- path or location of the file/data from the import dataset (e.g. after exporting from the data source); should include filename if application
	"filename" TEXT, -- name of the original file as named by the owner, if known
	"timestamp" INTEGER, -- unix epoch millisecond timestamp when item content was originally created (NOT when the database row was created)
	"timespan" INTEGER,  -- ending unix epoch ms timestamp if this item spans time (instead of being a single point in time); can be used in conjunction with timeframe to suggest duration
	"timeframe" INTEGER, -- ending unix epoch ms timestamp if this item takes place somewhere between timestamp and timeframe, but it's not certain exactly when
	"time_offset" INTEGER, -- offset of original timestamp/timespan/timeframe in seconds east of UTC/GMT (time zone)
	"time_uncertainty" INTEGER, -- if nonzero, time columns may be inaccurate on the order of this number of milliseconds, essentially sliding the times in a fuzzy interval
	"stored" INTEGER NOT NULL DEFAULT (unixepoch()), -- unix epoch second timestamp when row was created or last retrieved from source
	"modified" INTEGER, -- unix epoch second timestamp when item was manually modified (not via an import); if not null, then item is "not clean"
	"data_id" INTEGER, -- TODO: not used currently, but may be used to store binary data in the database directly
	"data_type" TEXT,  -- the MIME type (aka "media type") of the data
	"data_text" TEXT COLLATE NOCASE, -- item content, if text-encoded and (by default) not very long
	"data_file" TEXT COLLATE NOCASE, -- item filename, if non-text or not suitable for storage in DB (usually media), relative to repo root
	"data_hash" BLOB, -- BLAKE3 checksum of contents of the data file
	"metadata" TEXT,  -- optional extra information, encoded as JSON for flexibility
	"longitude" REAL, -- or equivalent X-coord for the coordinate system
	"latitude" REAL,  -- or equivalent Y-coord for the coordinate system
	"altitude" REAL,  -- or equivalent Z-coord for the coordinate system
	"coordinate_system" TEXT, -- reserved for future use to define which coordinate system is being used or on which world coordinates refer to
	"coordinate_uncertainty" REAL, -- if nonzero, lat/lon values may be inaccurate by this amount (same unit as coordinates)
	"note" TEXT,      -- optional user-added information
	"starred" INTEGER, -- like a bookmark; TODO: different numbers indicate different kinds of stars or something?
	"thumb_hash" BLOB, -- bytes of the ThumbHash that represent a visual preview of the item (https://evanw.github.io/thumbhash/ and https://github.com/evanw/thumbhash)
	"original_id_hash" BLOB, -- a hash of the data source and original ID of the item, also used for duplicate detection, optionally stored when item is deleted
	"initial_content_hash" BLOB, -- a hash computed during initial import, used for duplicate detection (remains same even if item is modified by user)
	"retrieval_key" BLOB UNIQUE, -- an optional opaque value that indicates this item may not be fully populated in a single import; not an ID but still a unique identifier
	"hidden" INTEGER,  -- if owner would like to forget about this item, don't show it in search results, etc. TODO: keep?
	"deleted" INTEGER, -- 1 = if the columns will be erased, they have been erased; >1 = a unix epoch timestamp after which the columns can be erased
	FOREIGN KEY ("embedding_id") REFERENCES "embeddings"("id") ON UPDATE CASCADE,
	FOREIGN KEY ("data_source_id") REFERENCES "data_sources"("id") ON UPDATE CASCADE,
	FOREIGN KEY ("job_id") REFERENCES "jobs"("id") ON UPDATE CASCADE, --TODO: maybe add ON DELETE CASCADE someday, which would rely on a regular sweeping of the files (garbage collection)! or we could do SET NULL
	FOREIGN KEY ("modified_job_id") REFERENCES "jobs"("id") ON UPDATE CASCADE ON DELETE SET NULL, -- deleting that import won't undo the changes, however
	FOREIGN KEY ("attribute_id") REFERENCES "attributes"("id") ON UPDATE CASCADE,
	FOREIGN KEY ("classification_id") REFERENCES "classifications"("id") ON UPDATE CASCADE,
	FOREIGN KEY ("data_id") REFERENCES "item_data"("id") ON UPDATE CASCADE ON DELETE SET NULL,
	UNIQUE ("data_source_id", "original_id")
) STRICT;

CREATE INDEX IF NOT EXISTS "idx_items_timestamp" ON "items"("timestamp");

-- Relationships may exist between and across items and entities. A row
-- in this table is an actual connection between items and/or entities.
CREATE TABLE IF NOT EXISTS "relationships" (
	"id" INTEGER PRIMARY KEY,
 	"relation_id" INTEGER NOT NULL,
	"value" ANY, -- optional; some relationships make sense if they contain a value (like a reaction: what was the reaction? or a collection element, what is its position?)
	-- exactly one of the from_ fields and one of the to_ fields should be non-NULL
	"from_item_id" INTEGER,
	"from_attribute_id" INTEGER,
	"to_item_id" INTEGER,
	"to_attribute_id" INTEGER,
	-- the next two fields optionally describe when the relationship started and/or ended as unix epoch timestamp in seconds
	"start" INTEGER,
	"end" INTEGER,
	"metadata" TEXT, -- optional extra info encoded as JSON
	FOREIGN KEY ("relation_id") REFERENCES "relations"("id") ON UPDATE CASCADE,
	FOREIGN KEY ("from_item_id") REFERENCES "items"("id") ON UPDATE CASCADE ON DELETE CASCADE,
	FOREIGN KEY ("to_item_id") REFERENCES "items"("id") ON UPDATE CASCADE ON DELETE CASCADE,
	FOREIGN KEY ("from_attribute_id") REFERENCES "attributes"("id") ON UPDATE CASCADE ON DELETE CASCADE,
	FOREIGN KEY ("to_attribute_id") REFERENCES "attributes"("id") ON UPDATE CASCADE ON DELETE CASCADE
) STRICT;

-- This speeds up inserts because we check for duplicate relationships in the app (it's complex with potential timeframes),
-- and this way only 1 index is necessary, not multiple on the various fields we check for uniqueness
CREATE INDEX IF NOT EXISTS "idx_relationships_from_item_id" ON "relationships"("from_item_id");

-- Relations define the way relationships connect. They are described by natural
-- language phrases such as "in reply to", "picture of", or "attached to"; or could
-- be words like "siblings", "coworkers", or "related". Relations will either be
-- directed (one way; to/from matter) or bidirectional (both ways), which depends
-- on the phrase and whatever makes sense if you were to put it into a sentence.
CREATE TABLE IF NOT EXISTS "relations" (
	"id" INTEGER PRIMARY KEY,
	"label" TEXT NOT NULL UNIQUE, -- natural phrase
	"directed" BOOLEAN NOT NULL DEFAULT true, -- false = bidirectional
	"subordinating" BOOLEAN NOT NULL DEFAULT false -- if true, item on the end of directed relation does not make sense on its own, e.g. attachment or motion picture
); -- not STRICT due to boolean type

-- A story is a user-created document with rich text formatting (basically HTML;
-- including hrefs to other items/entities). It can also embed timeline data, which
-- embeddings are stored in the story_elements table.
CREATE TABLE IF NOT EXISTS "stories" (
	"id" INTEGER PRIMARY KEY,
	"name" TEXT,
	"description" TEXT,
	"content" TEXT COLLATE NOCASE,
	"content_type" TEXT NOT NULL, -- MIME type; "text/html", "text/markdown", etc -- whatever form the content is in
	"modified" INTEGER NOT NULL DEFAULT (unixepoch())
) STRICT;

-- Elements of stories. An element can be rich text (HTML; including hrefs to other items/entities);
-- an embedded item, entity, or collection; or a query that loads a dynamic set of items, entities, or
-- collections. Rich text content can be provided together with embedded data as an annotation.
CREATE TABLE IF NOT EXISTS "story_elements" (
	"id" INTEGER PRIMARY KEY,
	"story_id" INTEGER NOT NULL,
	-- only one of the following 5 fields should be populated
	"item_id" INTEGER,       -- embedded item
	"entity_id" INTEGER,     -- embedded entity
	"query" TEXT,            -- embedded dynamic set; omits SELECT, e.g. "FROM items|entities WHERE ... ORDER BY ... LIMIT ..."
	"external" TEXT,         -- embedded data from other timeline/repo DB, of this format: "<repo_id>:<query>|item|entity:rowid"
	"note" TEXT,             -- optional annotation of embedded content
	FOREIGN KEY ("story_id") REFERENCES "stories"("id") ON UPDATE CASCADE ON DELETE CASCADE,
	FOREIGN KEY ("item_id") REFERENCES "items"("id") ON UPDATE CASCADE ON DELETE SET NULL,
	FOREIGN KEY ("entity_id") REFERENCES "entities"("id") ON UPDATE CASCADE ON DELETE SET NULL
) STRICT;

-- A tag is a simple descriptor on an item, put there by the user for organizational purposes.
CREATE TABLE IF NOT EXISTS "tags" (
	"id" INTEGER PRIMARY KEY,
	"label" TEXT NOT NULL UNIQUE,
	"color" TEXT
) STRICT;

-- The tags associated with items, entities, stories, or relationships.
-- The same tag may not be applied multiple times to the same entity.
CREATE TABLE IF NOT EXISTS "tagged" (
	"id" INTEGER PRIMARY KEY,
	"tag_id" INTEGER NOT NULL,
	-- only one of the following ID fields should be populated per row
	"item_id" INTEGER,
	"entity_id" INTEGER,
	"attribute_id" INTEGER,
	"story_id" INTEGER,
	"relationship_id" INTEGER,
	"data" TEXT, -- could be used for rich relationships, e.g. the coordinates of a entity tagged in a photo, etc.
	-- TODO: a way to indicate if this tag came as part of an import from a data source?
	FOREIGN KEY ("tag_id") REFERENCES "tags"("id") ON UPDATE CASCADE ON DELETE CASCADE,
	FOREIGN KEY ("item_id") REFERENCES "items"("id") ON UPDATE CASCADE ON DELETE CASCADE,
	FOREIGN KEY ("entity_id") REFERENCES "entities"("id") ON UPDATE CASCADE ON DELETE CASCADE,
	FOREIGN KEY ("attribute_id") REFERENCES "attributes"("id") ON UPDATE CASCADE ON DELETE CASCADE,
	FOREIGN KEY ("story_id") REFERENCES "stories"("id") ON UPDATE CASCADE ON DELETE CASCADE,
	FOREIGN KEY ("relationship_id") REFERENCES "relationships"("id") ON UPDATE CASCADE ON DELETE CASCADE
) STRICT;

-- TODO: I might get rid of this. This table would become user-created commentary on items/entities... but those could be items themselves...
CREATE TABLE IF NOT EXISTS "notes" (
	"id" INTEGER PRIMARY KEY,
	-- only one of the following ID fields should be populated per row
	"item_id" INTEGER,
	"entity_id" INTEGER,
	"content" TEXT, -- a user-friendly description that doesn't fit the DB structure
	"metadata" TEXT, -- optional metadata structured as JSON
	FOREIGN KEY ("item_id") REFERENCES "items"("id") ON UPDATE CASCADE ON DELETE CASCADE,
	FOREIGN KEY ("entity_id") REFERENCES "entities"("id") ON UPDATE CASCADE ON DELETE CASCADE
) STRICT;

-- A log of changes, notices, warnings, or errors pertaining  to elements of the timeline.
CREATE TABLE IF NOT EXISTS "logs" (
	"id" INTEGER PRIMARY KEY,
	"level" INTEGER, -- 0=update/modification 1=notice 2=error
	"message" TEXT,
	"metadata" TEXT, -- optional metadata structured as JSON
	"timestamp" INTEGER, -- unix timestamp milliseconds
	-- only one of the following ID fields should be populated per row
	"job_id" INTEGER,
	"item_id" INTEGER,
	"entity_id" INTEGER,
	"attribute_id" INTEGER,
	"relationship_id" INTEGER,
	"entity_attribute_id" INTEGER,
	FOREIGN KEY ("job_id") REFERENCES "jobs"("id") ON UPDATE CASCADE ON DELETE CASCADE,
	FOREIGN KEY ("item_id") REFERENCES "items"("id") ON UPDATE CASCADE ON DELETE CASCADE,
	FOREIGN KEY ("entity_id") REFERENCES "entities"("id") ON UPDATE CASCADE ON DELETE CASCADE,
	FOREIGN KEY ("attribute_id") REFERENCES "attributes"("id") ON UPDATE CASCADE ON DELETE CASCADE,
	FOREIGN KEY ("relationship_id") REFERENCES "relationships"("id") ON UPDATE CASCADE ON DELETE CASCADE,
	FOREIGN KEY ("entity_attribute_id") REFERENCES "entity_attributes"("id") ON UPDATE CASCADE ON DELETE CASCADE
) STRICT;

CREATE TABLE IF NOT EXISTS "settings" (
	"key" TEXT PRIMARY KEY,
	"title" TEXT,
	"description" TEXT,
	"type" TEXT,
	"value",
	"item_id" INTEGER,
	"attribute_id" INTEGER,
	"data_source_id" INTEGER, -- setting this implies this is a data source option, not merely a general setting that refers a data source
	FOREIGN KEY ("item_id") REFERENCES "items"("id") ON UPDATE CASCADE ON DELETE CASCADE,
	FOREIGN KEY ("attribute_id") REFERENCES "attributes"("id") ON UPDATE CASCADE ON DELETE CASCADE,
	FOREIGN KEY ("data_source_id") REFERENCES "data_sources"("id") ON UPDATE CASCADE ON DELETE CASCADE
);

-- TODO: this is convenient -- will probably keep this, because the db-based enums like data sources and classifications
-- don't get translated earlier; maybe we could, but I still need to think on that... if we do keep this,
-- I wonder if it'd be useful to loop in the attribute name and value as well? for item de-duplication in loadItemRow()....
CREATE VIEW IF NOT EXISTS "extended_items" AS
	SELECT
		items.*,
		data_sources.name AS data_source_name,
		data_sources.title AS data_source_title,
		classifications.name AS classification_name,
		-- TODO: verify these are useful fields and make the dashboard queries more efficient
		cast(strftime('%j', date(round(timestamp/1000), 'unixepoch')) AS INTEGER) AS day_of_year,
		cast(strftime('%W', date(round(timestamp/1000), 'unixepoch')) AS INTEGER) AS week_of_year,
		cast(strftime('%m', date(round(timestamp/1000), 'unixepoch')) AS INTEGER) AS month_of_year
	FROM items
	LEFT JOIN data_sources ON data_sources.id = items.data_source_id
	LEFT JOIN classifications ON classifications.id = items.classification_id;

-- Don't allow stray attributes; if for any reason no entity_attributes rows
-- point to a given attribute, always delete the attribute to clean up. The
-- only reason we have them separated into two tables is to allow multiple
-- entities to share an attribute, but it's pointless for an attribute to not
-- be associated with any entities.
CREATE TRIGGER IF NOT EXISTS prevent_stray_attributes
	AFTER DELETE ON entity_attributes
	FOR EACH ROW
	WHEN
		(SELECT count(1) FROM
			(SELECT id FROM entity_attributes WHERE attribute_id=OLD.attribute_id OR autolink_attribute_id=OLD.attribute_id LIMIT 1)) = 0
	BEGIN
		DELETE FROM attributes WHERE id=OLD.attribute_id;
	END;