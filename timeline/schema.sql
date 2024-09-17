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
	"id" INTEGER PRIMARY KEY, -- row ID, used only for DB consistency
	"name" TEXT NOT NULL UNIQUE   -- programming ID, e.g. "google_photos" as used in the application code
	"options" TEXT, -- configurable defaults to use when importing (to override their hard-coded defaults) (TODO: Not sure if they'll go here or in the settings table)
) STRICT;

-- An account contains credentials necessary for accessing a remote data source.
CREATE TABLE IF NOT EXISTS "accounts" (
	"id" INTEGER PRIMARY KEY,
	"data_source_id" INTEGER NOT NULL,
	"authorization" BLOB,
	FOREIGN KEY ("data_source_id") REFERENCES "data_sources"("id") ON UPDATE CASCADE ON DELETE CASCADE
) STRICT;

CREATE TABLE IF NOT EXISTS "jobs" (
	"id" INTEGER PRIMARY KEY,
	"action" TEXT NOT NULL, -- import, thumbnails, ...
	"configuration" TEXT, -- encoded as JSON
	"start" INTEGER NOT NULL DEFAULT (unixepoch()), -- timestamp in unix seconds UTC
	"end" INTEGER, -- timestamp in unix seconds UTC (whether stopped successfully or not)
	"status" TEXT NOT NULL, -- started, aborted, done, err (TODO: figure out the enum values)
	"hostname" TEXT, -- hostname of the machine the job started on
	"checkpoint" BLOB, -- state required for resuming an incomplete job
	-- if job is scheduled to run automatically at a certain interval, the following fields track that state
	"repeat" INTEGER, -- when this job is started, next job should be scheduled (inserted for future start) this many seconds from start time (not to be started if previous still running)
	"prev_job_id" INTEGER, -- the job before this one that scheduled this one, forming a chain
	FOREIGN KEY ("prev_job_id") REFERENCES "jobs"("id") ON UPDATE CASCADE ON DELETE SET NULL
) STRICT;

CREATE INDEX IF NOT EXISTS "idx_jobs_action" ON "jobs"("action");
CREATE INDEX IF NOT EXISTS "idx_jobs_start" ON "jobs"("start");
CREATE INDEX IF NOT EXISTS "idx_jobs_status" ON "jobs"("status");

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
	FOREIGN KEY ("job_id") REFERENCES "jobs"("id") ON UPDATE CASCADE
) STRICT;

CREATE INDEX IF NOT EXISTS "idx_entities_name" ON "entities"("name");
CREATE INDEX IF NOT EXISTS "idx_entities_birth_date" ON "entities"("birth_date");
CREATE INDEX IF NOT EXISTS "idx_entities_birth_place" ON "entities"("birth_place");
CREATE INDEX IF NOT EXISTS "idx_entities_hidden" ON "entities"("hidden");
CREATE INDEX IF NOT EXISTS "idx_entities_deleted" ON "entities"("deleted");

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

CREATE INDEX IF NOT EXISTS "idx_attributes_longitude1" ON "attributes"("longitude1");
CREATE INDEX IF NOT EXISTS "idx_attributes_latitude1" ON "attributes"("latitude1");
CREATE INDEX IF NOT EXISTS "idx_attributes_longitude2" ON "attributes"("longitude2");
CREATE INDEX IF NOT EXISTS "idx_attributes_latitude2" ON "attributes"("latitude2");

-- This table links entities and attributes. An entity may have multiple attributes, and an attribute
-- may apply to multiple entities. If the attribute represents an entity's identity on a particular
-- data source, the data_source_id field will be filled out.
CREATE TABLE IF NOT EXISTS "entity_attributes" (
	"id" INTEGER PRIMARY KEY,
	"entity_id" INTEGER NOT NULL,
	"attribute_id" INTEGER NOT NULL,
	"data_source_id" INTEGER, -- if set, the attribute defines the entity's identity on this data source
	"job_id" INTEGER, -- the ID of the import that originated this row (linkage between entity and attribute)
	-- these next two fields explain how the row came into being, by inferring the association from another attribute
	"autolink_job_id" INTEGER,    -- if set, the import that motivated linking this attribute as the entity's ID on the data source
	"autolink_attribute_id" INTEGER, -- if set, the other attribute by which this attribute was automatically linked as the entity's ID on the data source
	-- TODO: rename to start/end as with relationships table?
	"timeframe_start" INTEGER, -- when the attribute started applying to the entity
	"timeframe_end" INTEGER,   -- when the attribute stopped applying to the entity
	FOREIGN KEY ("entity_id") REFERENCES "entities"("id") ON UPDATE CASCADE ON DELETE CASCADE,
	FOREIGN KEY ("attribute_id") REFERENCES "attributes"("id") ON UPDATE CASCADE ON DELETE CASCADE,
	FOREIGN KEY ("data_source_id") REFERENCES "data_sources"("id") ON UPDATE CASCADE ON DELETE CASCADE,
	FOREIGN KEY ("job_id") REFERENCES "jobs"("id") ON UPDATE CASCADE ON DELETE SET NULL,
	FOREIGN KEY ("autolink_job_id") REFERENCES "jobs"("id") ON UPDATE CASCADE ON DELETE SET NULL,
	FOREIGN KEY ("autolink_attribute_id") REFERENCES "attributes"("id") ON UPDATE CASCADE ON DELETE SET NULL,
	-- a person can have multiple attributes, and an attribute can be shared by multiple people (though rare;
	-- usually that's just for shared accounts, which have to be manually linked to the persons by the user)
	-- but a person can reuse the same attribute as identity on different data sources, so we need the third
	-- field data_source_id in this constraint, however sqlite treats NULLs as distinct (argh!!!) and since
	-- data_source_id is only populated when the attribute is also an identity, it may be NULL; so instead
	-- we use a trick and create a "partial index" below ("CREATE UNIQUE INDEX") that applies a separate
	-- unique constraint on only the first two fields if the last one is NULL.
	UNIQUE ("entity_id", "attribute_id", "data_source_id")
) STRICT;

-- A classification describes what kind of thing an item is, for example a text message, tweet, email, or location.
CREATE TABLE IF NOT EXISTS "classifications" (
	"id" INTEGER PRIMARY KEY,
	"standard" INTEGER NOT NULL DEFAULT 0, -- 1: hard-coded classification, 0: user-created
	"name" TEXT NOT NULL UNIQUE,  -- programming name, e.g. "message"
	"labels" TEXT NOT NULL, -- comma-separated human-friendly names, e.g. "Text message,SMS,MMS,iMessage,Texting"
	"description" TEXT NOT NULL
) STRICT;

-- An item is something imported from a specific data source.
CREATE TABLE IF NOT EXISTS "items" (
	"id" INTEGER PRIMARY KEY,
	"data_source_id" INTEGER,
	"job_id" INTEGER,
	"modified_job_id" INTEGER, -- the import that last modified this existing item
	"attribute_id" INTEGER, -- owner, creator, or originator attributed to this item
	"classification_id" INTEGER,
	"original_id" TEXT, -- ID provided by the data source
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
	"data_type" TEXT,  -- the MIME type (aka "media type") of the data
	"data_text" TEXT COLLATE NOCASE, -- item content, if text-encoded and not very long
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
	-- TODO: unique on these two hashes?
	"original_id_hash" BLOB, -- a hash of the data source and original ID of the item, also used for duplicate detection, optionally stored when item is deleted
	"initial_content_hash" BLOB, -- a hash computed during initial import, used for duplicate detection (remains same even if item is modified by user)
	"retrieval_key" BLOB, -- an optional opaque value that indicates this item may not be fully populated in a single import; not an ID but still a unique identifier
	"hidden" INTEGER,  -- if owner would like to forget about this item, don't show it in search results, etc. TODO: keep?
	"deleted" INTEGER, -- 1 = if the columns will be erased, they have been erased; >1 = a unix epoch timestamp after which the columns can be erased
	FOREIGN KEY ("data_source_id") REFERENCES "data_sources"("id") ON UPDATE CASCADE,
	FOREIGN KEY ("job_id") REFERENCES "jobs"("id") ON UPDATE CASCADE, --TODO: maybe add ON DELETE CASCADE someday, which would rely on a regular sweeping of the files (garbage collection)! or we could do SET NULL
	FOREIGN KEY ("modified_job_id") REFERENCES "jobs"("id") ON UPDATE CASCADE ON DELETE SET NULL, -- deleting that import won't undo the changes, however
	FOREIGN KEY ("attribute_id") REFERENCES "attributes"("id") ON UPDATE CASCADE,
	FOREIGN KEY ("classification_id") REFERENCES "classifications"("id") ON UPDATE CASCADE,
	-- TODO: UNIQUE("job_id", "intermediate_location") maybe? the only problem is I could see embedded items like album art violating this -- unless embedded items don't have an intermediate_location
	UNIQUE ("data_source_id", "original_id"),
	UNIQUE ("retrieval_key")
) STRICT;

-- TODO: figure out which of these are actually necessary (use EXPLAIN QUERY PLAN SELECT ...) -- (add a ton of data to a timeline with no indexes here, then perform some searches; then add indexes until they get fast)
CREATE INDEX IF NOT EXISTS "idx_items_filename" ON "items"("filename");
CREATE INDEX IF NOT EXISTS "idx_items_timestamp" ON "items"("timestamp");
CREATE INDEX IF NOT EXISTS "idx_items_timespan" ON "items"("timespan");
CREATE INDEX IF NOT EXISTS "idx_items_timeframe" ON "items"("timeframe");
CREATE INDEX IF NOT EXISTS "idx_items_time_offset" ON "items"("time_offset");
CREATE INDEX IF NOT EXISTS "idx_items_data_text" ON "items"("data_text" COLLATE NOCASE);
CREATE INDEX IF NOT EXISTS "idx_items_data_file" ON "items"("data_file" COLLATE NOCASE);
CREATE INDEX IF NOT EXISTS "idx_items_data_hash" ON "items"("data_hash");
CREATE INDEX IF NOT EXISTS "idx_items_longitude" ON "items"("longitude");
CREATE INDEX IF NOT EXISTS "idx_items_latitude" ON "items"("latitude");
CREATE INDEX IF NOT EXISTS "idx_items_altitude" ON "items"("altitude");
CREATE INDEX IF NOT EXISTS "idx_items_hidden" ON "items"("hidden");
CREATE INDEX IF NOT EXISTS "idx_items_deleted" ON "items"("deleted");
CREATE INDEX IF NOT EXISTS "idx_items_initial_hash" ON "items"("initial_hash");

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
	FOREIGN KEY ("to_attribute_id") REFERENCES "attributes"("id") ON UPDATE CASCADE ON DELETE CASCADE,
	-- TODO: are these constraints harmful given the timestamp bounds? SQLite has distinct nulls, should I just add start and end to the constraints? right now it's impossible to represent a re-marriage (to the same person), for example
	UNIQUE ("relation_id", "from_item_id", "to_item_id"),
	UNIQUE ("relation_id", "from_item_id", "to_attribute_id"),
	UNIQUE ("relation_id", "from_attribute_id", "to_item_id"),
	UNIQUE ("relation_id", "from_attribute_id", "to_attribute_id")
) STRICT;

CREATE INDEX IF NOT EXISTS "idx_relationships_value" ON "relationships"("value");
CREATE INDEX IF NOT EXISTS "idx_relationships_start" ON "relationships"("start");
CREATE INDEX IF NOT EXISTS "idx_relationships_end" ON "relationships"("end");

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

-- A curation is a user-created document with rich text formatting (basically HTML;
-- including hrefs to other items/entities). It can also embed timeline data, which
-- embeddings are stored in the curation_elements table.
-- TODO: rename to 'stories'?
CREATE TABLE IF NOT EXISTS "curations" (
	"id" INTEGER PRIMARY KEY,
	"name" TEXT,
	"description" TEXT,
	"content" TEXT COLLATE NOCASE,
	"content_type" TEXT NOT NULL, -- MIME type; "text/html", "text/markdown", etc -- whatever form the content is in
	"modified" INTEGER NOT NULL DEFAULT (unixepoch())
) STRICT;

-- Elements of curations. An element can be rich text (HTML; including hrefs to other items/entities);
-- an embedded item, entity, or collection; or a query that loads a dynamic set of items, entities, or
-- collections. Rich text content can be provided together with embedded data as an annotation.
CREATE TABLE IF NOT EXISTS "curation_elements" (
	"id" INTEGER PRIMARY KEY,
	"curation_id" INTEGER NOT NULL,
	-- only one of the following 5 fields should be populated
	"item_id" INTEGER,       -- embedded item
	"entity_id" INTEGER,     -- embedded entity
	"query" TEXT,            -- embedded dynamic set; omits SELECT, e.g. "FROM items|entities WHERE ... ORDER BY ... LIMIT ..."
	"external" TEXT,         -- embedded data from other timeline/repo DB, of this format: "<repo_id>:<query>|item|entity:rowid"
	"note" TEXT,             -- optional annotation of embedded content
	FOREIGN KEY ("curation_id") REFERENCES "curations"("id") ON UPDATE CASCADE ON DELETE CASCADE,
	FOREIGN KEY ("item_id") REFERENCES "items"("id") ON UPDATE CASCADE ON DELETE SET NULL,
	FOREIGN KEY ("entity_id") REFERENCES "entities"("id") ON UPDATE CASCADE ON DELETE SET NULL
) STRICT;

-- A tag is a simple descriptor on an item, put there by the user for organizational purposes.
CREATE TABLE IF NOT EXISTS "tags" (
	"id" INTEGER PRIMARY KEY,
	"label" TEXT NOT NULL UNIQUE,
	"color" TEXT
) STRICT;

-- The tags associated with items, entities, curations, or relationships.
-- The same tag may not be applied multiple times to the same entity.
CREATE TABLE IF NOT EXISTS "tagged" (
	"id" INTEGER PRIMARY KEY,
	"tag_id" INTEGER NOT NULL,
	-- only one of the following ID fields should be populated per row
	"item_id" INTEGER,
	"entity_id" INTEGER,
	"attribute_id" INTEGER,
	"curation_id" INTEGER,
	"relationship_id" INTEGER,
	"data" TEXT, -- could be used for rich relationships, e.g. the coordinates of a entity tagged in a photo, etc.
	-- TODO: a way to indicate if this tag came as part of an import from a data source?
	FOREIGN KEY ("tag_id") REFERENCES "tags"("id") ON UPDATE CASCADE ON DELETE CASCADE,
	FOREIGN KEY ("item_id") REFERENCES "items"("id") ON UPDATE CASCADE ON DELETE CASCADE,
	FOREIGN KEY ("entity_id") REFERENCES "entities"("id") ON UPDATE CASCADE ON DELETE CASCADE,
	FOREIGN KEY ("attribute_id") REFERENCES "attributes"("id") ON UPDATE CASCADE ON DELETE CASCADE,
	FOREIGN KEY ("curation_id") REFERENCES "curations"("id") ON UPDATE CASCADE ON DELETE CASCADE,
	FOREIGN KEY ("relationship_id") REFERENCES "relationships"("id") ON UPDATE CASCADE ON DELETE CASCADE,
	-- TODO: Gah, another case of distinct NULLs biting me, so I can't do this:
	-- UNIQUE("tag_id", "item_id", "entity_id", "curation_id", "relationship_id")
	UNIQUE("tag_id", "item_id"),
	UNIQUE("tag_id", "entity_id"),
	UNIQUE("tag_id", "attribute_id"),
	UNIQUE("tag_id", "curation_id"),
	UNIQUE("tag_id", "relationship_id")
) STRICT;

-- TODO: Still figuring out how to structure this...
-- Structured annotations on individual imports, items, entities, or attributes
-- that may contain useful information when organizing a timeline.
CREATE TABLE IF NOT EXISTS "notes" (
	"id" INTEGER PRIMARY KEY,
	-- only one of the following ID fields should be populated per row
	"job_id" INTEGER,
	"item_id" INTEGER,
	"entity_id" INTEGER,
	"attribute_id" INTEGER,
	"relationship_id" INTEGER,
	"review_code_id" INTEGER, -- a review by the user is recommended/requested
	"freeform" TEXT, -- a user-friendly description that doesn't fit the DB structure
	"metadata" TEXT, -- optional metadata structured as JSON
	FOREIGN KEY ("job_id") REFERENCES "jobs"("id") ON UPDATE CASCADE ON DELETE CASCADE,
	FOREIGN KEY ("item_id") REFERENCES "items"("id") ON UPDATE CASCADE ON DELETE CASCADE,
	FOREIGN KEY ("entity_id") REFERENCES "entities"("id") ON UPDATE CASCADE ON DELETE CASCADE,
	FOREIGN KEY ("attribute_id") REFERENCES "attributes"("id") ON UPDATE CASCADE ON DELETE CASCADE,
	FOREIGN KEY ("relationship_id") REFERENCES "relationships"("id") ON UPDATE CASCADE ON DELETE CASCADE,
	FOREIGN KEY ("review_code_id") REFERENCES "review_codes"("id") ON UPDATE CASCADE ON DELETE CASCADE
) STRICT;

CREATE TABLE IF NOT EXISTS "review_codes" (
	"id" INTEGER PRIMARY KEY,
	"reason" TEXT NOT NULL
) STRICT;

-- TODO: Still WIP / might not use this...
CREATE TABLE IF NOT EXISTS "settings" (
	"key" TEXT PRIMARY KEY,
	"title" TEXT,
	"description" TEXT,
	"type" TEXT,
	"value",
	"item_id" INTEGER,
	"attribute_id" INTEGER,
	"data_source_id" INTEGER,
	FOREIGN KEY ("job_id") REFERENCES "jobs"("id") ON UPDATE CASCADE ON DELETE CASCADE,
	FOREIGN KEY ("item_id") REFERENCES "items"("id") ON UPDATE CASCADE ON DELETE CASCADE,
	FOREIGN KEY ("entity_id") REFERENCES "entities"("id") ON UPDATE CASCADE ON DELETE CASCADE,
	FOREIGN KEY ("attribute_id") REFERENCES "attributes"("id") ON UPDATE CASCADE ON DELETE CASCADE,
	FOREIGN KEY ("data_source_id") REFERENCES "data_sources"("id") ON UPDATE CASCADE ON DELETE CASCADE
) STRICT;

-- TODO: this is convenient -- will probably keep this, because the db-based enums like data sources and classifications
-- don't get translated earlier; maybe we could, but I still need to think on that... if we do keep this,
-- I wonder if it'd be useful to loop in the attribute name and value as well? for item de-duplication in loadItemRow()....
CREATE VIEW IF NOT EXISTS "extended_items" AS
	SELECT
		items.*,
		data_sources.name AS data_source_name,
		classifications.name AS classification_name,
		-- TODO: verify these are useful fields and make the dashboard queries more efficient
		cast(strftime('%j', date(round(timestamp/1000), 'unixepoch')) AS INTEGER) AS day_of_year,
		cast(strftime('%W', date(round(timestamp/1000), 'unixepoch')) AS INTEGER) AS week_of_year,
		cast(strftime('%m', date(round(timestamp/1000), 'unixepoch')) AS INTEGER) AS month_of_year
	FROM items
	LEFT JOIN data_sources ON data_sources.id = items.data_source_id
	LEFT JOIN classifications ON classifications.id = items.classification_id;

-- This view filters out items that are dependent on other items; technically,
-- it selects items that are NOT at the end of a directed relation from another
-- item. For example, this view skips items that are attachments of other items;
-- these items may not make much sense alone, without the context of the root
-- item, so this view omits those. However, it does include items that are at
-- the "to" end of a directed relation from an entity, because an item that is
-- "reacted" to by an entity still makes sense on its own.
-- We add "GROUP BY items.id" because an item that is the root of multiple other
-- items (e.g. a collection item) would appear as many times as it has relationships,
-- which is probably not desired.
CREATE VIEW IF NOT EXISTS "root_items" AS
	SELECT items.* FROM extended_items AS items
		LEFT JOIN relationships ON relationships.to_item_id = items.id
		LEFT JOIN relations ON relations.id = relationships.relation_id
		WHERE relations.directed != 1
			OR relations.subordinating = 0
			OR relationships.to_item_id IS NULL
		GROUP BY items.id;

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
