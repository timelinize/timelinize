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

package timeline

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ItemSearchParams describes a search for items.
//
// Fields with a slice/array type typically mean "any of these"
// (their elements are OR'ed together).
type ItemSearchParams struct {
	// The UUID of the open timeline to search.
	Repo string `json:"repo,omitempty"`

	// ML searches -- currently, only one of these can be set at a time
	// TODO: add a QueryImage field for image search
	SemanticText string `json:"semantic_text,omitempty"` // similar to the centroid of the vector formed from this phrase
	SimilarTo    int64  `json:"similar_to,omitempty"`    // similar to the centroid of the vector of the specified item (TODO: should it just be embedding ID?)

	RowID []int64 `json:"row_id,omitempty"`
	// AccountID  []int    `json:"account_id,omitempty"` // TODO: restore this, if useful
	DataSourceName []string `json:"data_source,omitempty"`
	JobID          []int64  `json:"job_id,omitempty"`
	AttributeID    []int64  `json:"attribute_id,omitempty"`
	EntityID       []uint64 `json:"entity_id,omitempty"`
	Classification []string `json:"classification,omitempty"`
	OriginalID     []string `jsson:"original_id,omitempty"`
	DataType       []string `json:"data_type,omitempty"`
	DataText       []string `json:"data_text,omitempty"`
	DataFile       []string `json:"data_file,omitempty"`

	// TODO: how do we effectively search metadata? maybe virtual columns? https://antonz.org/json-virtual-columns/
	// TODO: Well, this query was fast: `SELECT * FROM items WHERE items.metadata->>'$.Make' = 'Google' LIMIT 10`
	// Metadata    map[string][]any `json:"metadata,omitempty"`

	// TODO: a way to get items with EntityID, but also any relation FromEntityID or ToEntityID...
	ToAttributeID []int64 `json:"to_attribute_id,omitempty"`
	ToEntityID    []int64 `json:"to_entity_id,omitempty"`

	// filter by relationship to other items
	// TODO: We can probably wrap ToAttributeID, ToEntityID, and maybe Astructured into this?
	Relations []RelationParams `json:"relations,omitempty"`

	// bounding box searches
	StartTimestamp       *time.Time `json:"start_timestamp,omitempty"`
	EndTimestamp         *time.Time `json:"end_timestamp,omitempty"`
	MinLatitude          *float64   `json:"min_latitude,omitempty"`
	MaxLatitude          *float64   `json:"max_latitude,omitempty"`
	MinLongitude         *float64   `json:"min_longitude,omitempty"`
	MaxLongitude         *float64   `json:"max_longitude,omitempty"`
	Inclusive            bool       `json:"inclusive,omitempty"`              // if true, bounding box is <= and >= instead of < and >
	StrictStartTimestamp bool       `json:"strict_start_timestamp,omitempty"` // if true, item timestamp must be after start_timestamp (even if item timespan is after)
	StrictEndTimestamp   bool       `json:"strict_end_timestamp,omitempty"`   // if true, item timespan must be before end_timestamp (even if item timestamp is before)

	NoLocation bool `json:"no_location,omitempty"` // if true, require location columns to be NULL regardless of max/min lat/lon

	// proximity searches (location and time are mutually exclusive)
	Timestamp *time.Time `json:"timestamp,omitempty"`
	Latitude  *float64   `json:"latitude,omitempty"`
	Longitude *float64   `json:"longitude,omitempty"`

	// If true, OR different fields instead of AND
	OrFields bool `json:"or_fields,omitempty"`

	// How to order results. Default: usually timestamp,
	// but can be "smart" depending on search parameters.
	// This can also be "stored" to order by date added
	// to DB.
	OrderBy string `json:"order_by,omitempty"`

	Sort   SortDir `json:"sort,omitempty"`   // ignored if doing proximity search (unless it's SortNone, which breaks proximity searches)
	Limit  int     `json:"limit,omitempty"`  // number of rows to include (-1 for no limit); default 1000
	Offset int     `json:"offset,omitempty"` // number of rows to skip (can be slow if very large)
	Sample int     `json:"sample,omitempty"` // retrieve every Nth row

	// TODO: Matt's note: Pagination can be done more efficiently than
	// Offset or WithTotal (below) by using little tricks. For example,
	// if the exact count isn't required, set a large limit N (but still
	// a sensible order of magnitude) and report simply "N+" items; or
	// instead of offset, use WHERE to exclude items we've already paged
	// through using an indexed column (for example, if ordering by
	// timestamp, use WHERE to exclude items with timestamps that
	// would have appeared on previous pages; i.e. keep a cursor of
	// last item's timestamp from prev. page)

	// query related items and people recursively to this many degrees
	// (e.g. 0 is only the item, 1 adds direct relationships, etc...)
	Related int `json:"related,omitempty"`

	// By default (when this is false), search results will prioritize
	// the root nodes of item graphs, i.e. items that are not on the
	// end of a directed relation/edge, since this makes more sense
	// when viewing the timeline. If this is true, the ends of
	// directed relations are ignored, and related items may appear
	// in the "top level" of search results as well as nested in
	// "related".
	// TODO: Rename? Like only_root_items (that would also invert the value...)
	Astructured bool `json:"flat,omitempty"`

	// TODO: Experimental, for autocomplete... not sure if useful.
	PreferPrefix bool `json:"prefer_prefix,omitempty"`

	// In addition to results, return a total count that ignores offset and limit.
	// This can be very slow for broad queries on large DBs.
	// TODO: This can be very slow for large DBs on broad queries... maybe cache results? Or find a way to paginate without this (see comment above)
	WithTotal bool `json:"with_total,omitempty"`

	// Only return a total count that ignores offset and limit.
	OnlyTotal bool `json:"only_total,omitempty"`

	// If true, the only fields that will be selected from the DB are
	// latitude and longitude, and the search results will not be filled
	// with items; instead, the results will contain a GeoJSON document
	// with the coordinates, which can then be used directly by mapping
	// software to render point or heatmap information. This approach is
	// an order of magnitude faster (10x in my testing) for large data
	// sets (1M+ rows) to render a heatmap, where only coordinates are
	// needed. It avoids scanning the entire item row and allocating
	// multiple structs and performing various other computations.
	// Instead it efficiently builds a string containing only the
	// coordinate data. Non-spatial data will be excluded.
	GeoJSON           bool                 `json:"geojson,omitempty"`
	ObfuscatedGeoJSON []ObfuscatedLocation `json:"-"` // TODO: this is kind of a hack since it's the only data we encode before returning (for efficiency)

	// Include the size of the item content with the results.
	// For data files, this involves calling stat() on the file.
	WithSize bool `json:"with_size,omitempty"`

	// If true, include deleted items (that haven't been erased yet).
	Deleted bool `json:"deleted,omitempty"`

	// stores the converted names to row IDs
	classificationIDs []uint64
}

type SearchResults struct {
	// If enabled, the total count of items matching the search query
	// without regard for limit and offset.
	Total *int `json:"total,omitempty"`

	// The items of the search result.
	Items []*SearchResult `json:"items,omitempty"`

	// The search results in GeoJSON mode. A GeoJSON document
	// useful for rendering heatmaps or clusters.
	GeoJSON string `json:"geojson,omitempty"`
}

func (tl *Timeline) Search(ctx context.Context, params ItemSearchParams) (SearchResults, error) {
	// setting a natural language input has big implications, so make sure it's not just whitespace by accident
	params.SemanticText = strings.TrimSpace(params.SemanticText)

	// get the DB query string and associated arguments
	q, args, err := tl.prepareSearchQuery(ctx, params)
	if err != nil {
		return SearchResults{}, err
	}

	// lock DB for entirety of this operation, as it may involve many queries
	tl.dbMu.RLock()
	defer tl.dbMu.RUnlock()

	// open DB transaction to hopefully make it more efficient
	// (TODO: we don't currently commit this tx, because we didn't make changes - that's OK, right?)
	tx, err := tl.db.BeginTx(ctx, nil)
	if err != nil {
		return SearchResults{}, err
	}
	defer tx.Rollback()

	// in count-only mode, there's only a single row with a single field
	if params.OnlyTotal {
		var count int
		err := tx.QueryRowContext(ctx, q, args...).Scan(&count)
		if err != nil {
			return SearchResults{}, err
		}
		return SearchResults{Total: &count}, nil
	}

	// run query and scan results
	rows, err := tx.QueryContext(ctx, q, args...)
	if err != nil {
		return SearchResults{}, fmt.Errorf("querying db for items: %w", err)
	}

	// in GeoJSON mode, skip the expensive full-row scans etc; instead, efficiently
	// build a GeoJSON string directly -- it's very specific so we can do even better
	// than json.Marshal by using a strings builder.
	var sb strings.Builder
	if params.GeoJSON {
		// in testing, I found that Mapbox renders a single huge MultiPoint feature
		// way faster than many Point features
		sb.WriteString(`{"type":"Feature","geometry":{"type":"MultiPoint","coordinates":[`)
	}

	// for JSON serialization, always initialize so "0 results" is at least an empty list and not null
	results := make([]*SearchResult, 0)
	var count, totalCount int
	for rows.Next() { // fun fact, the first call to Next() is actually what runs the query
		// in GeoJSON mode, skip the usual full-row scan, and instead focus
		// on the coordinates which is much more efficient
		if params.GeoJSON {
			// read the coordinate data
			var rowID uint64
			var lat, lon *float64
			targets := []any{&rowID, &lat, &lon}
			if params.WithTotal {
				targets = append(targets, &totalCount)
			}
			err := rows.Scan(targets...)
			if err != nil {
				defer rows.Close()
				return SearchResults{}, err
			}

			// ensure it's valid, then obfuscate if enabled, then format as strings (waaaaay faster than reflection)
			if lat == nil || lon == nil {
				continue
			}
			for _, locob := range params.ObfuscatedGeoJSON {
				if locob.Contains(*lat, *lon) {
					newLat, newLon := locob.Obfuscate(*lat, *lon, rowID)
					lat = &newLat
					lon = &newLon
				}
			}
			latStr := strconv.FormatFloat(*lat, 'f', -1, 64)
			lonStr := strconv.FormatFloat(*lon, 'f', -1, 64)

			// write coordinate to the GeoJSON document
			if count > 0 {
				sb.WriteRune(',')
			}
			sb.WriteRune('[')
			sb.WriteString(lonStr)
			sb.WriteRune(',')
			sb.WriteString(latStr)
			sb.WriteRune(']')

			// continue with next row; skip all the expensive stuff
			count++
			continue
		}

		var re relatedEntity // entity is left-joined, so could be null
		var embeddingDistance float64
		var embeddingID *uint64 // just to know whether there are any embeddings for the item
		extraTargets := []any{&re.ID, &re.Name, &re.Picture, &re.Attribute.Name, &re.Attribute.Value, &re.Attribute.AltValue, &embeddingID}
		if params.WithTotal {
			extraTargets = append(extraTargets, &totalCount)
		}
		if params.SemanticText != "" || params.SimilarTo > 0 {
			extraTargets = append(extraTargets, &embeddingDistance)
		}
		itemRow, err := scanItemRow(rows, extraTargets)
		if err != nil {
			rows.Close()
			return SearchResults{}, err
		}
		sr := &SearchResult{RepoID: tl.id.String(), ItemRow: itemRow, HasEmbedding: embeddingID != nil, Distance: embeddingDistance}
		if re.ID != nil {
			sr.Entity = &re
		}
		results = append(results, sr)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return SearchResults{}, fmt.Errorf("iterating item rows: %w", err)
	}

	// in GeoJSON mode, we can be done early by wrapping up our document
	if params.GeoJSON {
		sb.WriteString(`]}}`)
		return SearchResults{GeoJSON: sb.String(), Total: &totalCount}, nil
	}

	// TODO: this needs tuning
	if params.SemanticText != "" {
		// filter results for relevance by passing them through the classifier...
		// this is kind of a hack, but it's a well-known difficult problem apparently,
		// for any KNN search, to only show relevant results
		itemFiles := make(map[uint64]string)
		for _, result := range results {
			if result.DataFile == nil || result.DataType == nil {
				continue
			}
			if !strings.HasPrefix(*result.DataType, "image/") {
				continue
			}
			itemFiles[result.ID] = tl.FullPath(*result.DataFile)
		}

		scores, err := classify(ctx, itemFiles, []string{params.SemanticText})
		if err != nil {
			return SearchResults{}, fmt.Errorf("classifying results: %w", err)
		}
		for _, sr := range results {
			if score, ok := scores[sr.ID]; ok {
				sr.Score = score
			}
		}
		// sort by score descending, then prefer items that weren't scored over scored items with a low score
		sort.Slice(results, func(i, j int) bool {
			_, iWasScored := scores[results[i].ID]
			_, jWasScored := scores[results[j].ID]
			return results[i].Score > results[j].Score || (jWasScored && !iWasScored)
		})
		// chop off results that are irrelevant, starting with the first result that was scored and has a score near zero
		for i, sr := range results {
			_, wasScored := scores[sr.ID]
			// TODO: Be smarter: find the range the top/best results score, and only cull results below a significant threshold difference
			if sr.Score <= 0.002 && wasScored {
				results = results[:i]
				break
			}
		}
	}

	// traverse relationships
	for _, sr := range results {
		err = tl.expandRelationships(ctx, tx, params.Related, sr)
		if err != nil {
			return SearchResults{}, err
		}
	}

	// include size information, if requested
	if params.WithSize {
		for _, sr := range results {
			if sr.DataText != nil {
				sr.Size = int64(len(*sr.DataText))
			}
			if sr.DataFile != nil {
				info, err := os.Stat(filepath.Join(tl.Dir(), *sr.DataFile))
				if err == nil {
					sr.Size = info.Size()
				}
			}
		}
	}

	return SearchResults{Total: &totalCount, Items: results}, nil
}

// TODO: favorites? or maybe a more flexible albums/lists feature? what to call it... "scrapbooks" or "curations"?

func (tl *Timeline) convertNamesToIDs(params *ItemSearchParams) {
	tl.cachesMu.RLock()
	for _, className := range params.Classification {
		if className == "" {
			params.classificationIDs = append(params.classificationIDs, 0)
		} else {
			params.classificationIDs = append(params.classificationIDs, tl.classifications[className])
		}
	}
	tl.cachesMu.RUnlock()
}

func (tl *Timeline) prepareSearchQuery(ctx context.Context, params ItemSearchParams) (string, []any, error) {
	if (params.Latitude == nil && params.Longitude != nil) || (params.Latitude != nil && params.Longitude == nil) {
		return "", nil, errors.New("location proximity search must include both lat and lon coordinates")
	}
	if params.Timestamp != nil && (params.Latitude != nil || params.Longitude != nil) {
		return "", nil, errors.New("time proximity and location proximity are mutually exclusive")
	}
	const maxDegreesOfSeparation = 2
	if params.Related > maxDegreesOfSeparation {
		// arbitrary, but I suspect it's a good idea to limit this for performance reasons
		return "", nil, errors.New("max degrees of separation for relationships is 2")
	}
	if params.WithTotal && params.OnlyTotal {
		return "", nil, errors.New("cannot query results with total and only total at the same time")
	}

	tl.convertNamesToIDs(&params)

	// When viewing a timeline, it can make more intuitive sense
	// to only show "root" nodes of item graphs at the "top" of
	// the search results, so that items which are only attachments,
	// for example, don't appear in the timeline multiple times
	// (once as an attachment, and once as "attached to" - it's even
	// more confusing if it's an attachment but looks like its own
	// item). So by default, we omit items from top level of search
	// results which appear at the end of a directed relation/edge.
	// Only if Astructured is true do we allow attachments and other
	// such items to appear as their own items in the top level.
	// Note: We also do astructured if specific row IDs are being queried
	// since we still want to select those specific rows even if
	// they are, say, attachments of a message. Querying specific
	// rows has priority over relationships.
	rootItemsOnly := !params.Astructured && len(params.RowID) == 0

	// if searching by embeddings, we first select the items that could possibly
	// be in the results by filtering with other search parameters, then we do
	// distance calculations over that subset of the data, which is theoretically
	// faster (see https://github.com/asg017/sqlite-vec/issues/196#issuecomment-2643543058)
	var q string
	vectorSearch := params.SemanticText != "" || params.SimilarTo > 0
	if vectorSearch {
		q = "WITH search_results AS (\n"
	}

	// honor inclusivity for bounding-box searches
	lt, gt := "<", ">"
	if params.Inclusive {
		lt, gt = "<=", ">="
	}

	// TODO: use strings.Builder (also in RecentConversations())

	q += fmt.Sprintf("\t\tSELECT %s, entities.id, entities.name, entities.picture_file, attributes.name, attributes.value, attributes.alt_value, embeddings.id", itemDBColumns)
	if params.OnlyTotal {
		q = "\t\tSELECT count(DISTINCT items.id)"
	}
	if params.GeoJSON {
		// GeoJSON mode is intended to be more efficient; as such, only select coordinate data
		q = "\t\tSELECT items.id, items.latitude, items.longitude"
	}
	if params.WithTotal {
		q += ", count() over() AS total_count"
	}
	q += `
		FROM extended_items AS items
		LEFT JOIN attributes ON items.attribute_id = attributes.id
		LEFT JOIN entity_attributes ON attributes.id = entity_attributes.attribute_id
		LEFT JOIN entities ON entity_attributes.entity_id = entities.id
		LEFT JOIN embeddings ON embeddings.item_id = items.id`

	// TODO: It's possible that we could move all these (ToAttributeID, ToEntityID, rootItemsOnly) into RelationParams
	if len(params.ToAttributeID) > 0 || len(params.ToEntityID) > 0 {
		q += `
		JOIN relationships ON relationships.from_item_id = items.id`
	} else if rootItemsOnly || len(params.Relations) > 0 {
		q += `
		LEFT JOIN relationships ON relationships.to_item_id = items.id
		LEFT JOIN relations ON relations.id = relationships.relation_id`
	}

	// build the WHERE in terms of groups of OR's that are AND'ed together
	var args []any
	var clauseCount int
	and := func(ors func()) {
		clauseCount = 0
		if len(args) == 0 {
			q += "\n\t\tWHERE"
		} else {
			if params.OrFields {
				q += " OR"
			} else {
				q += " AND"
			}
		}
		q += " ("
		ors()
		q += ")"

		// if the clause turned out to be empty,
		// this is a poor-man's way of undoing it
		q = strings.TrimSuffix(q, " OR ()")
		q = strings.TrimSuffix(q, " AND ()")
		q = strings.TrimSuffix(q, "\n\t\tWHERE ()")
	}
	or := func(clause string, val any) {
		if clauseCount > 0 {
			q += " OR "
		}
		q += clause
		args = append(args, val)
		clauseCount++
	}

	and(func() {
		if params.RowID != nil && len(params.RowID) == 0 {
			or("items.id IS ?", nil)
		}
		for _, v := range params.RowID {
			or("items.id=?", v)
		}
	})
	and(func() {
		if params.DataSourceName != nil && len(params.DataSourceName) == 0 {
			or("data_source_name IS ?", nil)
		}
		for _, v := range params.DataSourceName {
			or("data_source_name=?", v)
		}
	})
	and(func() {
		if params.JobID != nil && len(params.JobID) == 0 {
			or("items.job_id IS ?", nil)
		}
		for _, v := range params.JobID {
			or("items.job_id=?", v)
		}
	})
	and(func() {
		if params.AttributeID != nil && len(params.AttributeID) == 0 {
			or("items.attribute_id IS ?", nil)
		}
		for _, v := range params.AttributeID {
			or("items.attribute_id=?", v)
		}
	})
	and(func() {
		if params.EntityID != nil && len(params.EntityID) == 0 {
			or("entities.id IS ?", nil)
		}
		for _, v := range params.EntityID {
			or("entities.id=?", v)
		}
	})
	and(func() {
		for _, v := range params.classificationIDs {
			if v == 0 {
				or("items.classification_id IS ?", nil)
			} else {
				or("items.classification_id=?", v)
			}
		}
	})
	and(func() {
		if params.OriginalID != nil && len(params.OriginalID) == 0 {
			or("items.original_id IS ?", nil)
		}
		for _, v := range params.OriginalID {
			or("items.original_id=?", v)
		}
	})
	and(func() {
		if params.DataType != nil && len(params.DataType) == 0 {
			or("items.data_type IS ?", nil)
		}
		for _, v := range params.DataType {
			if strings.HasSuffix(v, "/*") {
				// useful for searching for all "video/*" files, for example
				or("items.data_type LIKE ? || '%'", v[:len(v)-1])
			} else {
				or("items.data_type=?", v)
			}
		}
	})
	and(func() {
		if params.DataText != nil && len(params.DataText) == 0 {
			or("items.data_text IS ?", nil)
		}
		for _, v := range params.DataText {
			or("items.data_text LIKE '%' || ? || '%'", v)
		}
	})
	and(func() {
		if params.DataFile != nil && len(params.DataFile) == 0 {
			or("items.data_file IS ?", nil)
		}
		for _, v := range params.DataFile {
			or("items.data_file=?", v)
		}
	})

	and(func() {
		// TODO: these can probably be in the same 'AND' group like this, right?
		for _, v := range params.ToAttributeID {
			or("relationships.to_attribute_id=?", v)
		}
		for _, v := range params.ToEntityID {
			or("relationships.to_entity_id=?", v)
		}
	})

	// TODO: can do local-time-aware querying by doing: "items.timestamp + items.time_offset*1000" instead of just "items.timestamp" (I think)
	// TODO: Use BETWEEN maybe

	if params.StartTimestamp != nil {
		and(func() {
			or("items.timestamp + COALESCE(items.time_offset*1000, 0) "+gt+" ?", params.StartTimestamp.UTC().UnixMilli())
			if !params.StrictStartTimestamp {
				// if not strict, allow items to spill into the window even if the started before it
				or("items.timespan + COALESCE(items.time_offset*1000, 0) "+gt+" ?", params.StartTimestamp.UTC().UnixMilli())
			}
		})
	}
	if params.EndTimestamp != nil {
		and(func() {
			or("items.timestamp + COALESCE(items.time_offset*1000, 0) "+lt+" ?", params.EndTimestamp.UTC().UnixMilli())
		})
		if params.StrictEndTimestamp {
			// if strict, items' timespan must end before the EndTimestamp (item can't merely start before it)
			and(func() {
				or("items.timespan IS NULL OR items.timespan + COALESCE(items.time_offset*1000, 0) "+lt+" ?", params.EndTimestamp.UTC().UnixMilli())
			})
		}
	}
	if params.NoLocation {
		and(func() {
			or("items.latitude IS ?", nil)
		})
		and(func() {
			or("items.longitude IS ?", nil)
		})
		and(func() {
			or("items.altitude IS ?", nil)
		})
		and(func() {
			or("items.coordinate_system IS ?", nil)
		})
	} else {
		if params.MinLatitude != nil {
			and(func() {
				or("items.latitude "+gt+" ?", params.MinLatitude)
			})
		}
		if params.MaxLatitude != nil {
			and(func() {
				or("items.latitude "+lt+" ?", params.MaxLatitude)
			})
		}
		if params.MinLongitude != nil {
			and(func() {
				or("items.longitude "+gt+" ?", params.MinLongitude)
			})
		}
		if params.MaxLongitude != nil {
			and(func() {
				or("items.longitude "+lt+" ?", params.MaxLongitude)
			})
		}
	}

	// skip deleted items unless we are explicitly supposed to include them
	// (NOTE: we include them if the specific row IDs are requested)
	if !params.Deleted && len(params.RowID) == 0 {
		and(func() {
			or("items.deleted IS ?", nil)
		})
	}

	// always skip hidden items
	and(func() {
		or("items.hidden IS ?", nil)
	})

	// skip every so many items if sampling is enabled
	if params.Sample > 1 {
		and(func() {
			or("items.id % ? = 0", params.Sample)
		})
	}

	if rootItemsOnly {
		// select only items which are not dependent on other items, i.e., items
		// that are not at the end of a directed relation from another item
		and(func() {
			or("relations.directed != ?", 1)
			or("relations.subordinating = ?", 0)
			or("relationships.to_item_id IS ?", nil)
		})
	}

	// factor in relationships
	for _, relParam := range params.Relations {
		op := "IS"
		if relParam.Not {
			op = "IS NOT"
		}
		and(func() {
			if relParam.RelationLabel != "" {
				or("relations.label "+op+" ?", relParam.RelationLabel)
			}
		})
	}

	if !params.OnlyTotal {
		q += "\n\t\tGROUP BY items.id"
	}

	// don't put order in the temporary table, since we'll be ordering by vector distance
	if !vectorSearch {
		if params.Sort != SortNone {
			q += "\n\t\tORDER BY "

			// TODO: not sure if this is how autocomplete will work or be useful, but basically
			// this sorts by data text so that if it's a prefix, it's weighed higher in the
			// sort, and if it's a suffix then put it at the end; i.e. favor term at beginning
			// of words instead of end... I think that's what this does, at least
			if params.PreferPrefix && len(params.DataText) == 1 {
				q += `CASE
					WHEN items.data_text LIKE ? || '%' THEN 1
					WHEN items.data_text LIKE '%' || ? THEN 3
					ELSE 2
				END, `
				args = append(args, params.DataText[0], params.DataText[0])
			}

			// sort
			sortDir := strings.ToUpper(string(params.Sort))
			if sortDir == "" {
				// smart sort: sort ASC by default if only "minimum" side of bounding boxes are specified
				if (params.StartTimestamp != nil || params.MinLatitude != nil || params.MinLongitude != nil) &&
					params.EndTimestamp == nil && params.MaxLatitude == nil && params.MaxLongitude == nil {
					sortDir = string(SortAsc)
				} else {
					sortDir = string(SortDesc)
				}
			}
			if sortDir != string(SortAsc) && sortDir != string(SortDesc) {
				return "", nil, fmt.Errorf("invalid sort direction: %s", sortDir)
			}
			// location proximity search is only an approximation for simplicity
			// see https://stackoverflow.com/a/39298241/1048862
			switch {
			case params.Latitude != nil && params.Longitude != nil:
				// nearest to location; account for shortened distances at poles
				// math.Cos() takes radians, hence the conversion to radians inside the cosine
				cosLat2 := math.Pow(math.Cos(*params.Latitude*math.Pi/180.0), 2) //nolint:mnd
				sortDir = string(SortAsc)                                        // always sort ascending for nearest
				q += "((?-items.latitude) * (?-items.latitude)) + ((?-items.longitude) * (?-items.longitude) * ?), items.id " + sortDir
				args = append(args, params.Latitude, params.Latitude, params.Longitude, params.Longitude, cosLat2)

			case params.Timestamp != nil:
				// nearest to timestamp
				sortDir = string(SortAsc) // always sort ascending for nearest
				q += "abs(?-items.timestamp), items.id " + sortDir
				args = append(args, params.Timestamp.UnixMilli())

			case params.OrderBy == "stored":
				q += "items.stored " + sortDir

			default:
				// generic sort, which is timestamp and row ID
				q += fmt.Sprintf("items.timestamp %s, items.id %s", sortDir, sortDir)
			}
		}

		// limit
		if !params.OnlyTotal {
			if params.Limit == 0 {
				params.Limit = 1000
			}
			if params.Limit > 0 {
				q += "\n\t\tLIMIT ?"
				args = append(args, params.Limit)
			}
			if params.Offset > 0 {
				q += "\n\t\tOFFSET ?"
				args = append(args, params.Offset)
			}
		}
	}

	if vectorSearch {
		var targetVectorClause string

		if params.SimilarTo > 0 {
			targetVectorClause = "(SELECT embedding FROM embeddings JOIN items ON embeddings.item_id = items.id WHERE items.id=? LIMIT 1)"
			args = append(args, params.SimilarTo)
		} else if params.SemanticText != "" {
			// search relative to an arbitrary input (TODO: support image inputs too) - python server must be online
			if !pythonServerReady(ctx, false) {
				return "", nil, errors.New("python server not ready")
			}
			embedding, err := generateEmbedding(ctx, "text/plain", []byte(params.SemanticText), nil)
			if err != nil {
				return "", nil, err
			}
			targetVectorClause = "?"
			args = append(args, string(embedding))
		}

		q += fmt.Sprintf(`
)
SELECT
	search_results.*,
	vec_distance_l2(%s, embeddings.embedding) AS distance
FROM search_results
JOIN embeddings ON embeddings.item_id = search_results.id
ORDER BY distance`, targetVectorClause)
		if params.Limit == 0 {
			params.Limit = 100
		}
		if params.Limit > 0 {
			q += "\nLIMIT ?"
			args = append(args, params.Limit)
		}
		if params.Offset > 0 {
			q += "\nOFFSET ?"
			args = append(args, params.Offset)
		}
	}

	return q, args, nil
}

func (tl *Timeline) expandRelationships(ctx context.Context, tx *sql.Tx, degrees int, sr *SearchResult) error {
	if degrees <= 0 {
		return nil
	}

	// notice how we're careful to avoid recursion while we have open rows
	// scanning happening, so that we don't step on other select queries
	err := tl.expandRelationshipSingle(ctx, tx, sr)
	if err != nil {
		return err
	}

	// now expand relationships recursively until degrees of separation
	// from the original search results are reached
	for _, rel := range sr.Related {
		if rel.FromItem != nil {
			err := tl.expandRelationships(ctx, tx, degrees-1, rel.FromItem)
			if err != nil {
				return err
			}
		}
		if rel.ToItem != nil {
			err := tl.expandRelationships(ctx, tx, degrees-1, rel.ToItem)
			if err != nil {
				return err
			}
		}
	}

	return nil
}

func (tl *Timeline) expandRelationshipSingle(ctx context.Context, tx *sql.Tx, sr *SearchResult) error {
	// TODO: limit here is arbitrary I think... ho hum
	rows, err := tx.QueryContext(ctx, `
		SELECT
			relationships.id,
			relations.directed,
			relations.label,
			relationships.value,
			relationships.start,
			relationships.end,
			relationships.metadata,
			relationships.from_item_id,
			relationships.to_item_id,
			from_entity.id,
			from_entity.name,
			from_entity.picture_file,
			from_attr.name,
			from_attr.value,
			from_attr.alt_value,
			to_entity.id,
			to_entity.name,
			to_entity.picture_file,
			to_attr.name,
			to_attr.value,
			to_attr.alt_value
		FROM relationships
		JOIN items ON items.id=?
		JOIN relations ON relations.id=relationships.relation_id
		LEFT JOIN attributes AS from_attr ON from_attr.id=relationships.from_attribute_id
		LEFT JOIN attributes AS to_attr ON to_attr.id=relationships.to_attribute_id
		LEFT JOIN entity_attributes AS from_ea   ON from_ea.attribute_id   = from_attr.id
		LEFT JOIN entity_attributes AS to_ea   ON to_ea.attribute_id   = to_attr.id
		LEFT JOIN entities AS from_entity   ON from_entity.id   = from_ea.entity_id
		LEFT JOIN entities AS to_entity   ON to_entity.id   = to_ea.entity_id
		WHERE (relationships.from_item_id=? OR relationships.to_item_id=?) AND items.hidden IS NULL
		GROUP BY relationships.id
		LIMIT 10`, sr.ItemRow.ID, sr.ItemRow.ID, sr.ItemRow.ID)
	if err != nil {
		return fmt.Errorf("querying db for relationships: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var rel Related
		var fromItemID, toItemID *uint64
		var relStart, relEnd *int64
		var fromEntity, toEntity relatedEntity
		var relMeta *string
		err := rows.Scan(&rel.RelationshipID, &rel.Directed, &rel.Label, &rel.Value, &relStart, &relEnd, &relMeta, &fromItemID, &toItemID,
			&fromEntity.ID, &fromEntity.Name, &fromEntity.Picture, &fromEntity.Attribute.Name, &fromEntity.Attribute.Value, &fromEntity.Attribute.AltValue,
			&toEntity.ID, &toEntity.Name, &toEntity.Picture, &toEntity.Attribute.Name, &toEntity.Attribute.Value, &toEntity.Attribute.AltValue)
		if err != nil {
			return err
		}

		if fromEntity.ID != nil {
			rel.FromEntity = &fromEntity
		}
		if toEntity.ID != nil {
			rel.ToEntity = &toEntity
		}
		if relStart != nil {
			startTime := time.Unix(*relStart, 0)
			rel.Start = &startTime
		}
		if relEnd != nil {
			endTime := time.Unix(*relEnd, 0)
			rel.End = &endTime
		}
		if relMeta != nil {
			rel.Metadata = json.RawMessage(*relMeta)
		}

		// expand items, we have to do this until our code can support
		// loading an item from among other columns as well; expand
		// only if the item is distinct from the parent/starting item
		if fromItemID != nil && *fromItemID != sr.ID {
			fromRel, err := tl.loadRelatedItem(ctx, tx, *fromItemID)
			if err != nil {
				return fmt.Errorf("loading from_item: %w", err)
			}
			rel.FromItem = fromRel
		}
		if toItemID != nil && *toItemID != sr.ID {
			toRel, err := tl.loadRelatedItem(ctx, tx, *toItemID)
			if err != nil {
				return fmt.Errorf("loading to_item: %w", err)
			}
			rel.ToItem = toRel
		}

		sr.Related = append(sr.Related, rel)
	}
	if err = rows.Err(); err != nil {
		return fmt.Errorf("scanning related item rows: %w", err)
	}

	return nil
}

func (tl *Timeline) loadRelatedItem(ctx context.Context, tx *sql.Tx, itemRowID uint64) (*SearchResult, error) {
	ir, err := tl.loadItemRow(ctx, tx, itemRowID, 0, nil, nil, nil, false)
	if err != nil {
		return nil, fmt.Errorf("loading related item row: %w", err)
	}
	// TODO: dunno if this safe (QueryRow during a rows.Scan, by the caller of this function)
	// TODO: if it would help increase performance, we could probably cache information about persons and person_identities...
	// TODO: maybe there's a view we could use for this instead?
	var p relatedEntity
	if ir.AttributeID != nil {
		err = tx.QueryRowContext(ctx, `
		SELECT entities.id, entities.name, entities.picture_file, attributes.name, attributes.value, attributes.alt_value, attributes.longitude, attributes.latitude, attributes.altitude
		FROM attributes, entities
		JOIN entity_attributes
			ON entity_attributes.entity_id = entities.id
			AND entity_attributes.attribute_id = attributes.id
		WHERE attributes.id=?`, ir.AttributeID).Scan(&p.ID, &p.Name, &p.Picture, &p.Attribute.Name, &p.Attribute.Value, &p.Attribute.AltValue, &p.Attribute.Longitude, &p.Attribute.Latitude, &p.Attribute.Altitude)
		if err != nil {
			return nil, fmt.Errorf("loading related entity: %w", err)
		}
	}
	sr := &SearchResult{RepoID: tl.id.String(), ItemRow: ir}
	if p.ID != nil {
		sr.Entity = &p
	}
	return sr, nil
}

type SearchResult struct {
	RepoID string `json:"repo_id,omitempty"`
	ItemRow
	HasEmbedding bool           `json:"has_embedding,omitempty"`
	Entity       *relatedEntity `json:"entity,omitempty"`
	Related      []Related      `json:"related,omitempty"`
	Size         int64          `json:"size,omitempty"`

	// from ML model
	Distance float64 `json:"distance,omitempty"`
	Score    float64 `json:"score,omitempty"`
}

// RelationParams describes a search using item relations.
type RelationParams struct {
	Not           bool   `json:"not,omitempty"` // if true, only match items that do NOT have this relation
	RelationLabel string `json:"relation_label,omitempty"`
}

// relatedEntity is a subset of Entity (so we need to make sure
// the JSON field names are the same), but with pointer field
// types because it is left-joined in queries which means they
// can be null.
type relatedEntity struct {
	ID        *uint64           `json:"id"`
	Name      *string           `json:"name,omitempty"`
	Picture   *string           `json:"picture,omitempty"`
	Attribute nullableAttribute `json:"attribute,omitempty"` // TODO: experimental
}

// nullableAttribute is like Attribute but with nullable fields
// so that it can be I/O for the database
type nullableAttribute struct {
	ID        *uint64  `json:"id,omitempty"`
	Name      *string  `json:"name,omitempty"`
	Value     *string  `json:"value,omitempty"`
	AltValue  *string  `json:"alt_value,omitempty"`
	Latitude  *float64 `json:"latitude,omitempty"`
	Longitude *float64 `json:"longitude,omitempty"`
	Altitude  *float64 `json:"altitude,omitempty"`
}

func (na nullableAttribute) attribute() Attribute {
	var a Attribute
	if na.ID != nil {
		a.ID = *na.ID
	}
	if na.Name != nil {
		a.Name = *na.Name
	}
	if na.Value != nil {
		a.Value = *na.Value
	}
	if na.AltValue != nil {
		a.AltValue = *na.AltValue
	}
	a.Longitude = na.Longitude
	a.Latitude = na.Latitude
	a.Altitude = na.Altitude
	return a
}

type Related struct {
	Relation
	RelationshipID int             `json:"relationship_id"`
	Value          *string         `json:"value,omitempty"`
	FromItem       *SearchResult   `json:"from_item,omitempty"`
	ToItem         *SearchResult   `json:"to_item,omitempty"`
	FromEntity     *relatedEntity  `json:"from_entity,omitempty"`
	ToEntity       *relatedEntity  `json:"to_entity,omitempty"`
	Start          *time.Time      `json:"start,omitempty"`
	End            *time.Time      `json:"end,omitempty"`
	Metadata       json.RawMessage `json:"metadata,omitempty"`
}

type SortDir string

const (
	SortNone SortDir = "none" // special value that means to not include a ORDER BY clause
	SortAsc  SortDir = "ASC"
	SortDesc SortDir = "DESC"
)
