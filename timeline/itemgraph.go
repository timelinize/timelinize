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
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"os"
	"strconv"
	"strings"
	"time"
)

// Graph is either an item or entity node with optional connections to other
// items and entities. Either an Item or Entity may be set, but not both.
// All Graph values should be pointers to ensure consistency.
// The usual weird/fun thing about representing graph data structures
// in memory is that a graph is a node, and a node is a graph. 🤓
type Graph struct {
	// The (root) node of this graph.
	// It is an error for both to be set.
	Item   *Item
	Entity *Entity

	// Edges contain pointers to other nodes on the graph
	// and are described by a relationship. There must be a
	// node on both ends of an edge. If this node has either
	// an Item or Entity specified, it can be on one end of
	// the edge as long as the edge specifies the other one
	// (either To or From). The edge may also specify both
	// To and From nodes regardless of this one.
	Edges []Relationship

	// TODO: experimental - let the processor decide whether to checkpoint
	Checkpoint any

	// state needed by processing pipeline
	err error
}

// Size returns the number of nodes in the graph. If this is
// an entity graph, the size is 1. If it's an item graph, the
// size is counted recursively across relationship edges.
func (g *Graph) Size() int {
	if g.Entity != nil {
		return 1
	}
	return g.ItemCount()
}

// ItemCount returns the number of items by traversing the graph.
func (g *Graph) ItemCount() int {
	return g.recursiveItemCount(make(map[*Graph]struct{}))
}

func (g *Graph) recursiveItemCount(visited map[*Graph]struct{}) int {
	if g == nil || g.Item == nil {
		return 0
	}

	// prevent infinite recursion
	if _, ok := visited[g]; ok {
		return 0
	}
	visited[g] = struct{}{}

	count := 1 // g.Item
	for _, edge := range g.Edges {
		count += edge.From.recursiveItemCount(visited)
		count += edge.To.recursiveItemCount(visited)
	}
	return count
}

// ToItem links an item to the node on this graph such that
// the edge goes from the graph node to the item.
func (g *Graph) ToItem(rel Relation, item *Item) {
	g.ToItemWithValue(rel, item, "")
}

// ToEntity links an entity to the node on this graph such that
// the edge goes from the graph node to the entity.
func (g *Graph) ToEntity(rel Relation, entity *Entity) {
	g.ToEntityWithValue(rel, entity, "")
}

// FromItem links the node on this graph to an item such that
// the edge goes from the item to the graph node.
func (g *Graph) FromItem(item *Item, rel Relation) {
	g.FromItemWithValue(item, rel, "")
}

// FromEntity links the node on this graph to an entity such that
// the edge goes from the entity to the graph node.
func (g *Graph) FromEntity(entity *Entity, rel Relation) {
	g.FromEntityWithValue(entity, rel, "")
}

// ToItemWithValue connects g to item with a relation that has the given value.
func (g *Graph) ToItemWithValue(rel Relation, item *Item, value any) {
	g.Edges = append(g.Edges, Relationship{
		Relation: rel,
		To:       &Graph{Item: item},
		Value:    value,
	})
}

// ToEntityWithValue connects g to entity with a relation that has the given value.
func (g *Graph) ToEntityWithValue(rel Relation, entity *Entity, value any) {
	g.Edges = append(g.Edges, Relationship{
		Relation: rel,
		To:       &Graph{Entity: entity},
		Value:    value,
	})
}

// FromItemWithValue connects item to g with a relation that has the given value.
func (g *Graph) FromItemWithValue(item *Item, rel Relation, value any) {
	g.Edges = append(g.Edges, Relationship{
		Relation: rel,
		From:     &Graph{Item: item},
		Value:    value,
	})
}

// FromEntityWithValue connects entity to g with a relation that has the given value.
func (g *Graph) FromEntityWithValue(entity *Entity, rel Relation, value any) {
	g.Edges = append(g.Edges, Relationship{
		Relation: rel,
		From:     &Graph{Entity: entity},
		Value:    value,
	})
}

func (g *Graph) String() string {
	if g.Item != nil {
		return "item:" + g.Item.String()
	}
	if g.Entity != nil {
		return "entity:" + g.Entity.String()
	}
	return "[graph]"
}

// Item represents an item on the timeline.
type Item struct {
	// The unique ID of the item assigned by the data source.
	ID string

	// Item classification, i.e. the kind of thing it represents.
	// This value adds semantic context to the item, which helps
	// programs know how to display, visualize, and even query
	// the item; and helps users know how to think about it.
	// For example, we display and think of chat messages very
	// differently than we do memos-to-self, social media posts,
	// and emails.
	Classification Classification

	// The timestamp when the item originated. If multiple
	// timestamps are available, prefer the timestamp when the
	// original item content was captured or created. For
	// example, a photograph is captured at one timestamp and
	// posted online at another; prefer the first timestamp
	// when it was originally captured.
	Timestamp time.Time

	// An optional ending timestamp to make this item span
	// time instead of being a point in time. If set, it must
	// be a time after Timestamp. This gives an item duration.
	Timespan time.Time

	// An optional ending timestamp indicating that the item's
	// actual timestamp is between Timestamp and Timeframe, but
	// it's not certain exactly when.
	Timeframe time.Time

	// Approximate error of the time values.
	TimeUncertainty time.Duration

	// The coordinates where the item originated.
	Location Location

	// The person who owns, created, or originated the item. At
	// least one attribute is required: an identifying attribute
	// like a user ID of the person on this data source.
	Owner Entity

	// If applicable, path to the item on the original data
	// source, including the filename. Note that this can take
	// on different formats depending on the data source; for
	// example, iPhone backups provide paths relative to a
	// domain, so the paths may be in the form "Domain:Path".
	OriginalLocation string

	// If applicable, path to the item relative to the root of the
	// import, including the filename.
	IntermediateLocation string

	// The actual content of the item.
	Content ItemData

	// Optional extra information about the item. Keys should
	// be human-readable and formatted as natural titles or
	// labels (e.g. "Description" instead of "desc") since
	// viewers will typically just regurgitate this info as-is.
	Metadata Metadata

	// Annotations can bring something to the user's
	// attention or at least be associated as metadata
	// for curators or reviewers of the item.
	// TODO: STILL WIP, EXPERIMENTAL.
	Annotations []Annotation

	// When an item cannot be fully conveyed at the same time, or
	// has to be given in pieces, telling the processor how to
	// retrieve the other existing part of the same item from the
	// DB can be helpful, since the processor's built-in "check
	// for existing item" logic assumes/requires complete items.
	// When set, a "retrieval key" is stored in the DB as opaque
	// bytes which the processor uses as a unique key to retrieve
	// the part of an item that already exists in the DB. In other
	// words, each part of the same item that is conveyed to the
	// processor must have the same retrieval key, and no other
	// item globally must use the same key at any time.
	Retrieval ItemRetrieval

	// Used for storing state during processing; either the
	// text content of the item, or the source from which
	// to read when creating the data file on disk. Data
	// sources should set Content instead; NOT these!
	dataText *string

	// state for processing pipeline phases
	row          ItemRow
	dataFileIn   io.ReadCloser
	dataFileOut  *os.File
	dataFileSize int64
	dataFileName string
	dataFileHash []byte // should only be set if dataFileSize > 0
	idHash       []byte
	contentHash  []byte
}

// ItemRetrieval dictates how to retrieve an existing item from the database.
// It is used when items may be given to the processor in pieces, as in, the
// whole item is not available all at once. Call SetKey() to set the key (it
// gets hashed, so it simply opaque bytes to the processor and DB), and if
// relevant, set PreferFields to control what gets updated or preferred when
// data already exists in the DB.
type ItemRetrieval struct {
	key []byte

	// Column names to prefer from the incoming item over whatever is stored in the DB;
	// overrides user's configured update policies. This is useful if multiple separate
	// parts of an item may overlap, but one part is more reliable or preferred over
	// another part. For example, in Google Takeout archives, some photo metadata is both
	// embedded as EXIF and available in a sidecar JSON file, but it's fairly well known
	// that the JSON file's metadata is less correct sometimes, for some reason. So even
	// though both have metadata, when importing from the actual image, we prefer that,
	// and this tells the processor to do so.
	PreferFields []string
}

// SetKey sets the retrieval key for this item. It should be a globally unique
// value; note that other data sources may collaborate on the same item if they
// present items with the same retrieval key (this can be either a bug or a
// feature, so set the key wisely).
func (ret *ItemRetrieval) SetKey(key string) {
	h := newHash()
	h.Write([]byte(key))
	ret.key = h.Sum(nil)
}

// idHash sets the hash derived from the data source and the original ID assigned
// by the data source, or nil if both values are not present.
func (it *Item) makeIDHash(dataSourceName *string) {
	if dataSourceName == nil || it.ID == "" {
		return
	}
	h := newHash()
	h.Write([]byte(*dataSourceName))
	h.Write([]byte(it.ID))
	it.idHash = h.Sum(nil)
}

// contentHash sets the content-based hash for this item's equivalent row
// in the items table as long as the item's content is not empty. It is
// only valid for use during the import processing flow.
func (it *Item) makeContentHash() {
	// this is a slightly stricter and more nuanced check than HasContent
	if (it.dataText == nil || len(*it.dataText) == 0) &&
		len(it.dataFileHash) == 0 &&
		it.Location.IsEmpty() {
		return
	}
	h := newHash()
	if !it.Timestamp.IsZero() {
		_ = binary.Write(h, binary.LittleEndian, it.Timestamp.UnixMilli())
	}
	switch {
	case it.dataText != nil && len(*it.dataText) > 0:
		h.Write([]byte(*it.dataText))
	case len(it.dataFileHash) > 0:
		h.Write(it.dataFileHash)
	case !it.Location.IsEmpty():
		if it.Location.Latitude != nil {
			_ = binary.Write(h, binary.LittleEndian, *it.Location.Latitude)
		}
		if it.Location.Longitude != nil {
			_ = binary.Write(h, binary.LittleEndian, *it.Location.Longitude)
		}
		if it.Location.Altitude != nil {
			_ = binary.Write(h, binary.LittleEndian, *it.Location.Altitude)
		}
	}
	it.contentHash = h.Sum(nil)
}

func (it Item) String() string {
	return fmt.Sprintf("[id=%s class=%+v timestamp=%s timespan=%s orig_path=%s inter_path=%s location=%s content=%p meta=%v]",
		it.ID, it.Classification, it.Timestamp, it.Timespan, it.OriginalLocation, it.IntermediateLocation, it.Location, it.Content.Data, it.Metadata)
}

// HasContent returns true if the item has data or a location.
func (it *Item) HasContent() bool {
	return it.Content.Data != nil || !it.Location.IsEmpty()
}

// AddMetadata adds meta to the item's metadata with the given merge policy.
func (it *Item) AddMetadata(meta Metadata, policy MetadataMergePolicy) {
	if it.Metadata == nil {
		it.Metadata = meta
	} else {
		it.Metadata.Merge(meta, policy)
	}
}

// SetTimeframe sets the Timeframe field based on the Timestamp field.
// The precision of the timestamp is determined based on default/"zero"
// values. The timeframe is understood to be the maximum range of the
// highest precision present in the timestamp. For example, a timestamp
// with a non-default year and month, but with default day, minute, and
// second, will have a timeframe set to exactly 1 month later, thus
// encompassing the whole month. A zero/empty timestamp does nothing.
//
// TODO: is this true? should this be part of the processing pipeline?
// This method is not done automatically for every item by the processor
// because it may not be appropriate, depending on the data source.
// For example, some items (particularly, reminders or health updates, etc.)
// might have timestamps at precisely 00:00 (i.e. date-only) but do not
// span the whole day.
func (it *Item) SetTimeframe() {
	if it.Timestamp.IsZero() {
		return
	}

	year, month, day := it.Timestamp.Date()
	hour, min, sec := it.Timestamp.Clock()

	const emptyDate, emptyClock = 1, 0

	switch {
	case year != emptyDate && month == emptyDate && day == emptyDate &&
		hour == emptyClock && min == emptyClock && sec == emptyClock:
		it.Timeframe = it.Timestamp.AddDate(1, 0, 0)

	case year != emptyDate && month != emptyDate && day == emptyDate &&
		hour == emptyClock && min == emptyClock && sec == emptyClock:
		it.Timeframe = it.Timestamp.AddDate(0, 1, 0)

	case year != emptyDate && month != emptyDate && day != emptyDate &&
		hour == emptyClock && min == emptyClock && sec == emptyClock:
		it.Timeframe = it.Timestamp.AddDate(0, 0, 1)

	case year != emptyDate && month != emptyDate && day != emptyDate &&
		hour != emptyClock && min == emptyClock && sec == emptyClock:
		it.Timeframe = it.Timestamp.Add(1 * time.Hour)
	}
}

// timestampUnix returns the timestamp as Unix timestamp, if non-zero.
func (it Item) timestampUnix() *int64 {
	if it.Timestamp.IsZero() {
		return nil
	}
	unix := it.Timestamp.UnixMilli()
	return &unix
}

// timestampUnix returns the timespan as Unix timestamp, if non-zero.
func (it Item) timespanUnix() *int64 {
	if it.Timespan.IsZero() {
		return nil
	}
	unix := it.Timespan.UnixMilli()
	return &unix
}

// timeframeUnix returns the timeframe as Unix timestamp, if non-zero.
func (it Item) timeframeUnix() *int64 {
	if it.Timeframe.IsZero() {
		return nil
	}
	unix := it.Timeframe.UnixMilli()
	return &unix
}

// ItemData represents the actual content (data) of an item.
// Depending on size and type, it might be stored in the database
// or as a file on disk.
type ItemData struct {
	// The filename to use for this item. This is used as a hint
	// or suggestion if the item is stored as a file on disk.
	//
	// If set, this should be the LAST component of a file path, i.e.
	// the name of the within its folder, not the whole path. If the
	// content originated as a file, prefer its original filename. If
	// the filename is not unique in its destination within the repo,
	// it will be made unique by modifying it. If this value is empty
	// and a filename is needed, a usable name will be generated.
	Filename string

	// The MIME type of the bytes read from the Data field. If not
	// set, it will be inferred by sniffing the data or using the
	// filename. (However, it is recommended to specify if possible.)
	//
	// It should adhere to the basic syntax defined by RFC 2046, that
	// is, "top-level-type/subtype", and use registered values when
	// possible. The "/subtype" may be omitted if a subtype is not
	// known, does not make sense, or if the top-level-type is an
	// invented value.
	//
	// As this field is crucial when deciding how to store, handle,
	// and view the Data field, it should only be set if the Data field
	// is set.
	//
	// Whereas the default MIME type is "application/octet-stream" for
	// the purposes of mail, in our case we assume a more sensible default
	// of "text/plain" because items in a timeline are not usually
	// arbitrary binary blobs or executable programs.
	MediaType string

	// A function that returns a way to read the item's data. The
	// returned ReadCloser will be closed when processing finishes.
	Data DataFunc
}

// hasPlainTextMediaType returns true fi the item is declared as having
// a plaintext media type, or in other words, a type which may qualify
// for being stored directly in the DB (if this function returns false,
// do not store the item content in the database; use a file instead).
// If no media type is specified, we default to assuming plaintext (true).
func (id ItemData) hasPlainTextMediaType() bool {
	mediaType, _, err := mime.ParseMediaType(id.MediaType)
	if err != nil {
		return true // assume plaintext
	}
	switch mediaType {
	case "", "text", "text/plain":
		return true
	}
	return false
}

// DataFunc is a function that returns an item's data. It must honor
// context cancellation if it does anything long-running or async.
type DataFunc func(context.Context) (io.ReadCloser, error)

// Metadata is a map of arbitrary extra information to associate
// with an item. Keys should be human-readable with natural language
// formatting, casing, and spacing when possible.
type Metadata map[string]any

// Clean removes keys with empty values (including numeric 0 and
// non-empty strings and byte slices containing only spaces) or
// keys that are the empty string. Floats are compared with some
// tolerance around zero. Boolean false is not considered empty.
// (TODO: Should false be considered empty?)
func (m Metadata) Clean() {
	for k, v := range m {
		if k == "" {
			delete(m, k)
			continue
		}
		if m.isEmpty(v) {
			delete(m, k)
		}
	}
}

// StringsToSpecificType applies StringToSpecificType() to all
// string values in the metadata map.
func (m Metadata) StringsToSpecificType() {
	for key, anyVal := range m {
		if strVal, ok := anyVal.(string); ok {
			m[key] = StringToSpecificType(strVal)
		}
	}
}

// StringToSpecificType converts a string value to its more specific
// type, e.g. "3" -> 3, "3.14" -> 3.14, "True" -> true, etc. If it
// can't find a working conversion, it returns the input string.
func StringToSpecificType(s string) any {
	if v, err := strconv.ParseInt(s, 10, 64); err == nil {
		return v
	}
	if v, err := strconv.ParseFloat(s, 64); err == nil {
		return v
	}

	// we could use ParseBool, but we'd risk converting other strings
	// like "t" or "no" to true/false which might not be desired
	if v := strings.ToLower(strings.TrimSpace(s)); v == "true" {
		return v
	}
	if v := strings.ToLower(strings.TrimSpace(s)); v == "false" {
		return v
	}

	return s
}

func (Metadata) isEmpty(v any) bool {
	if v == nil {
		return true
	} else if str, ok := v.(string); ok && strings.TrimSpace(str) == "" {
		return true
	} else if buf, ok := v.([]byte); ok && len(bytes.TrimSpace(buf)) == 0 {
		return true
	} else if t, ok := v.(time.Time); ok && t.IsZero() {
		return true
	} else if d, ok := v.(time.Duration); ok && d == 0 {
		return true
	} else if n, ok := v.(int); ok && n == 0 {
		return true
	} else if n, ok := v.(int8); ok && n == 0 {
		return true
	} else if n, ok := v.(int16); ok && n == 0 {
		return true
	} else if n, ok := v.(int32); ok && n == 0 {
		return true
	} else if n, ok := v.(int64); ok && n == 0 {
		return true
	} else if n, ok := v.(uint); ok && n == 0 {
		return true
	} else if n, ok := v.(uint8); ok && n == 0 {
		return true
	} else if n, ok := v.(uint16); ok && n == 0 {
		return true
	} else if n, ok := v.(uint32); ok && n == 0 {
		return true
	} else if n, ok := v.(uint64); ok && n == 0 {
		return true
	} else if n, ok := v.(float32); ok &&
		(n < 0.000001 && n > -0.000001) {
		return true
	} else if n, ok := v.(float64); ok &&
		(n < 0.00000000000001 && n > -0.00000000000001) {
		return true
	}
	return false
}

// HumanizeKeys transforms the keys in m to be more human-friendly.
// For example, it capitlizes the first character and replaces
// underscores with spaces.
func (m Metadata) HumanizeKeys() map[string]any {
	nk := map[string]any{}
	for key, val := range m {
		if len(key) == 0 {
			continue
		}
		normKey := strings.ToUpper(string(key[0])) + key[1:]
		normKey = strings.ReplaceAll(normKey, "_", " ")
		nk[normKey] = val
	}

	return nk
}

// MetadataMergePolicy is a type that specifies how to handle
// merging of metadata when there is a key conflict.
type MetadataMergePolicy int

const (
	// MetaMergeAppend keeps both values. It finds the next unused counter
	// and appends it to the key so that both values can be preserved; for
	// example, if "Foo" already exists, then it will be saved as "Foo 2".
	// If the number of counters is depleted, the new value will be skipped.
	// This is the default merge policy.
	MetaMergeAppend = iota

	// MetaMergeReplace replaces any existing value with the incoming one.
	MetaMergeReplace

	// MetaMergeReplaceEmpty only replaces the existing value with the
	// incoming one if the existing value is empty or the "zero value".
	MetaMergeReplaceEmpty

	// MetaMergeSkip will skip any incoming value if the key already exists.
	MetaMergeSkip
)

// Merge adds the incoming metadata to m according to the specified conflict policy.
func (m Metadata) Merge(incoming Metadata, policy MetadataMergePolicy) {
	if len(incoming) == 0 {
		return
	}
	for key, val := range incoming {
		if currentVal, ok := m[key]; ok {
			// nothing to do if the values are the same
			if val == currentVal {
				continue
			}

			switch policy {
			case MetaMergeAppend:
				for i := 2; i < 100; i++ {
					newKey := fmt.Sprintf("%s %d", key, i)
					if _, ok := m[newKey]; !ok {
						m[newKey] = val
						break
					}
				}
			case MetaMergeReplace:
				m[key] = val
			case MetaMergeReplaceEmpty:
				if m.isEmpty(currentVal) {
					m[key] = val
				}
			}

			// skip; don't overwrite existing value
			continue
		}
		m[key] = val
	}
}

// These are the standard relationships that Timelinize
// recognizes. Using these known relationships is not
// required, but it makes it easier to translate them to
// human-friendly phrases when visualizing the timeline.
var (
	// TODO: rename to RelAttaches? (and label to "attaches"?)
	RelAttachment   = Relation{Label: "attachment", Directed: true, Subordinating: true} // "<from_item> has attachment <to_item>", or "<to> is attached to <from>"
	RelSent         = Relation{Label: "sent", Directed: true}                            // "<from_item> was sent to <to_person>"
	RelCCed         = Relation{Label: "cc", Directed: true}                              // "<from_item> is carbon-copied to <to_person>"
	RelReply        = Relation{Label: "reply", Directed: true}                           // "<from_item> is reply to <to_item>"
	RelQuotes       = Relation{Label: "quotes", Directed: true}                          // "<from_item> quotes <to>", or "<to> is quoted by <from>"
	RelReacted      = Relation{Label: "reacted", Directed: true}                         // "<from_entity>" reacted to <to_item> with <value>"
	RelDepicts      = Relation{Label: "depicts", Directed: true}                         // flexible, but most common is: "<from_item> depicts <to_entity>"
	RelEdit         = Relation{Label: "edit", Directed: true}                            // "<to_item> is edit of <from_item>"
	RelInCollection = Relation{Label: "in_collection", Directed: true}                   // "<from_item> is in collection <to_item> at position <value>"
	// RelTranscript = Relation{Label: "transcript", Directed: true, Subordinating: true} // "<from_item> is transcribed by <to_item>"
)

// ItemRow has the structure of an item's row in our DB.
type ItemRow struct {
	ID                   int64           `json:"id"`
	DataSourceID         *int64          `json:"data_source_id,omitempty"` // row ID, used only for insertion into the DB
	ImportID             *int64          `json:"import_id,omitempty"`
	ModifiedImportID     *int64          `json:"modified_import_id,omitempty"`
	AttributeID          *int64          `json:"attribute_id,omitempty"`
	ClassificationID     *int64          `json:"classification_id,omitempty"` // row ID, used only internally
	OriginalID           *string         `json:"original_id,omitempty"`
	OriginalLocation     *string         `json:"original_location,omitempty"`
	IntermediateLocation *string         `json:"intermediate_location,omitempty"`
	Filename             *string         `json:"filename,omitempty"`
	Timestamp            *time.Time      `json:"timestamp,omitempty"`
	Timespan             *time.Time      `json:"timespan,omitempty"`
	Timeframe            *time.Time      `json:"timeframe,omitempty"`
	TimeOffset           *int            `json:"time_offset,omitempty"`
	TimeUncertainty      *int64          `json:"time_uncertainty,omitempty"`
	Stored               time.Time       `json:"stored,omitempty"`
	Modified             *time.Time      `json:"modified,omitempty"`
	DataType             *string         `json:"data_type,omitempty"`
	DataText             *string         `json:"data_text,omitempty"`
	DataFile             *string         `json:"data_file,omitempty"` // must NOT be a pointer to an Item.dataFileName value (should be its own copy!)
	DataHash             []byte          `json:"data_hash,omitempty"` // BLAKE3 hash of the contents of DataFile
	Metadata             json.RawMessage `json:"metadata,omitempty"`  // JSON-encoded extra information
	Location
	Note               *string    `json:"note,omitempty"`
	Starred            *int       `json:"starred,omitempty"`
	ThumbHash          []byte     `json:"thumb_hash,omitempty"`
	OriginalIDHash     []byte     `json:"original_id_hash,omitempty"`
	InitialContentHash []byte     `json:"initial_content_hash,omitempty"`
	RetrievalKey       []byte     `json:"retrieval_key,omitempty"`
	Hidden             *bool      `json:"hidden,omitempty"`
	Deleted            *time.Time `json:"deleted,omitempty"`

	// From view "extended_items"
	DataSourceName *string `json:"data_source_name"`
	Classification *string `json:"classification"`
}

func (ir ItemRow) hasContent() bool {
	return ir.DataText != nil || ir.DataFile != nil || !ir.Location.IsEmpty()
}

func (ir ItemRow) timestampUnix() *int64 {
	if ir.Timestamp == nil {
		return nil
	}
	unix := ir.Timestamp.UnixMilli()
	return &unix
}

func (ir ItemRow) timespanUnix() *int64 {
	if ir.Timespan == nil {
		return nil
	}
	unix := ir.Timespan.UnixMilli()
	return &unix
}

func (ir ItemRow) timeframeUnix() *int64 {
	if ir.Timeframe == nil {
		return nil
	}
	unix := ir.Timeframe.UnixMilli()
	return &unix
}

type sqlScanner interface {
	Scan(dest ...any) error
}

// scanItemRow reads an item from row and returns the structured ItemRow.
// The item must have been queried to select all of itemDBColumns.
// It must be called inside a lock on the database (such as Timeline.dbMu).
// It scans columns defined by itemDBColumns.
func scanItemRow(row sqlScanner, targetsAfterItemCols []any) (ItemRow, error) {
	var ir ItemRow

	var metadata, className *string
	var ts, tspan, tframe, modified, deleted *int64 // will convert from Unix milli timestamp
	var stored int64                                // will convert from Unix milli timestamp

	itemTargets := []any{&ir.ID, &ir.DataSourceID, &ir.ImportID, &ir.ModifiedImportID, &ir.AttributeID,
		&ir.ClassificationID, &ir.OriginalID, &ir.OriginalLocation, &ir.IntermediateLocation, &ir.Filename,
		&ts, &tspan, &tframe, &ir.TimeOffset, &ir.TimeUncertainty, &stored, &modified,
		&ir.DataType, &ir.DataText, &ir.DataFile, &ir.DataHash,
		&metadata, &ir.Location.Longitude, &ir.Location.Latitude, &ir.Location.Altitude,
		&ir.Location.CoordinateSystem, &ir.Location.CoordinateUncertainty, &ir.Note, &ir.Starred,
		&ir.ThumbHash, &ir.OriginalIDHash, &ir.InitialContentHash,
		&ir.Hidden, &deleted,
		&ir.DataSourceName, &className}
	allTargets := append(itemTargets, targetsAfterItemCols...) //nolint:gocritic // I am explicitly self-documenting how the first batch of targets are for the item, then there's the rest

	err := row.Scan(allTargets...)
	if errors.Is(err, sql.ErrNoRows) {
		return ir, nil
	}
	if err != nil {
		return ir, fmt.Errorf("scanning item row: %w", err)
	}

	ir.Classification = className
	if ts != nil {
		tsVal := time.UnixMilli(*ts)
		ir.Timestamp = &tsVal
	}
	if tspan != nil {
		tspanVal := time.UnixMilli(*tspan)
		ir.Timespan = &tspanVal
	}
	if tframe != nil {
		tframeVal := time.UnixMilli(*tframe)
		ir.Timeframe = &tframeVal
	}
	if modified != nil {
		modVal := time.Unix(*modified, 0)
		ir.Modified = &modVal
	}
	if metadata != nil {
		ir.Metadata = json.RawMessage(*metadata)
	}
	if deleted != nil {
		delVal := time.Unix(*deleted, 0)
		ir.Deleted = &delVal
	}
	ir.Stored = time.Unix(stored, 0)

	return ir, nil
}

// used for selecting from the extended_items view, but "AS items"
const itemDBColumns = `items.id, items.data_source_id, items.import_id, items.modified_import_id, items.attribute_id, items.classification_id,
items.original_id, items.original_location, items.intermediate_location, items.filename,
items.timestamp, items.timespan, items.timeframe, items.time_offset, items.time_uncertainty, items.stored, items.modified,
items.data_type, items.data_text, items.data_file, items.data_hash, items.metadata,
items.longitude, items.latitude, items.altitude, items.coordinate_system, items.coordinate_uncertainty,
items.note, items.starred, items.thumb_hash, items.original_id_hash, items.initial_content_hash,
items.hidden, items.deleted, data_source_name, classification_name`

// Location represents a precise coordinate on a planetary body.
// By default, standard Earth GPS lon/lat coordinates are assumed.
// An alternate coordinate system may be defined, or coordinates
// may be omitted entirely if not applicable.
type Location struct {
	// The X, Y, and Z coordinates of the item. These field names
	// may have different meanings depending on the coordinate system.
	Longitude *float64 `json:"longitude,omitempty"` // degrees
	Latitude  *float64 `json:"latitude,omitempty"`  // degrees
	Altitude  *float64 `json:"altitude,omitempty"`  // meters

	// The name, ID, or code of the body on which the item originated,
	// or an alternate coordinate system to use with the lat/lon.
	// If the coordinate center is Earth, this field should be omitted, since
	// that is the default. If there is no satisfactory code or name of a body
	// (i.e. if item originates in deep space or between worlds), put an
	// applicable description of the position instead, for example: the
	// name of the spaceship ("UNSC Infinity") or an alternate/cosmic
	// coordinate system (e.g. "ecliptic" or "galactic").
	CoordinateSystem *string `json:"coordinate_system,omitempty"`

	// If approximate coordinate error is known, specify it here
	// in the same units as the coordinate values.
	CoordinateUncertainty *float64 `json:"coordinate_uncertainty,omitempty"`
}

// IsEmpty returns true if there is no x, y, or z coordinate.
func (l Location) IsEmpty() bool {
	return l.Latitude == nil && l.Longitude == nil && l.Altitude == nil
}

func (l Location) String() string {
	var s strings.Builder
	s.WriteRune('(')

	if l.Latitude != nil {
		s.WriteString(strconv.FormatFloat(*l.Latitude, 'f', -1, 64))
		s.WriteString(", ")
	} else {
		s.WriteString("?, ")
	}

	if l.Longitude != nil {
		s.WriteString(strconv.FormatFloat(*l.Longitude, 'f', -1, 64))
	} else {
		s.WriteRune('?')
	}

	if l.Altitude != nil {
		s.WriteString(strconv.FormatFloat(*l.Altitude, 'f', -1, 64))
	} else {
		s.WriteRune('?')
	}

	if l.CoordinateSystem != nil {
		s.WriteString(", ")
		s.WriteString(*l.CoordinateSystem)
	}

	s.WriteRune(')')

	return s.String()
}

// Relationship represents a relationship between
// two nodes on a graph; i.e. it describes an edge.
// Relationships (or "edges") are composed of a
// relation, which gives the edge a name or label;
// a direction, which declares whether the edge
// is goes only one way ("directed") or both ways
// ("bidirectional"); an optional value, which may
// add necessary information to the relationship,
// for example the content of an interaction (e.g.
// a reaction to a message); and two nodes, one on
// either end (To and From). Which node is on which
// end doesn't matter if the relation is not
// directed.
//
// Relationships that are given in the context of
// a Graph may omit either From or To if the graph
// has a node (either an item or entity), as that
// will be assumed to be the other end of the edge.
//
// Relationships should always be defined so that
// the "root" of a graph can be the node that has
// no directed relationships to it. In other words,
// the root item of a graph is the node that does not
// appear as the "To" from any other item. These root
// nodes will be preferred when viewing a timeline.
type Relationship struct {
	Relation
	Value      any
	From, To   *Graph
	Start, End *time.Time
	Metadata   Metadata
}

// rawRelationships represents a relationship in DB terms.
type rawRelationship struct {
	Relation
	value                       any
	fromItemID, fromAttributeID *int64
	toItemID, toAttributeID     *int64
	start, end                  *int64
	metadata                    json.RawMessage
}

func (rr rawRelationship) String() string {
	const n = "nil"
	fromItemID, fromAttributeID, toItemID, toAttributeID := n, n, n, n
	if rr.fromItemID != nil {
		fromItemID = strconv.FormatInt(*rr.fromItemID, 10)
	}
	if rr.fromAttributeID != nil {
		fromAttributeID = strconv.FormatInt(*rr.fromAttributeID, 10)
	}
	if rr.toItemID != nil {
		toItemID = strconv.FormatInt(*rr.toItemID, 10)
	}
	if rr.toAttributeID != nil {
		toAttributeID = strconv.FormatInt(*rr.toAttributeID, 10)
	}
	return fmt.Sprintf("[label=%s directed=%t value=%v fromItemID=%s fromAttributeID=%s toItemID=%s toAttributeID=%s start=%d end=%d]",
		rr.Relation.Label, rr.Relation.Directed, rr.value, fromItemID, fromAttributeID, toItemID, toAttributeID, rr.start, rr.end)
}

// Relation describes how two nodes in a graph are related.
// It's essentially an edge on a graph.
type Relation struct {
	// The simple snake_case representation of this relation.
	Label string `json:"label"`

	// If true, the from and to positions matter (not commutative)
	Directed bool `json:"directed"`

	// If true, the to_item is subordinate to the from_item,
	// meaning the to_item does not make sense on its own
	// without the from_item.
	// TODO: better name for this?
	Subordinating bool `json:"subordinating"`
}

// Annotation is a note or something that needs attention.
// TODO: still figuring these out
type Annotation struct {
	ReviewReasonID int64
	Freeform       string
	Metadata       any
}

// ReviewReason describes a reason a review might be needed.
// TODO: very experimental
type ReviewReason string

// Classification represents item classes. Classifying items is used to
// convey their semantic meaning or intent. For example, a text item
// could be any number of things: an email, a social media post, a
// chat message, a note to self, etc. Classifications help the UI
// know how to display the items and how to craft more semantically-
// relevant queries in the DB. Overly vague or generic classifications
// should be avoided if possible, and classes should be distinct from
// MIME types (Content-Type in HTTP; or data_type in the schema),
// which help programs know how to read/parse data, classes help programs
// know how to semantically interpret or display them.
type Classification struct {
	id          *int64
	Standard    bool     `json:"standard"`
	Name        string   `json:"name"`
	Labels      []string `json:"labels,omitempty"`
	Description string   `json:"description,omitempty"`
}

// TODO: should we have a test to ensure these don't change?
var classifications = []Classification{
	{
		Name:        "message",
		Labels:      []string{"Message", "Text message", "SMS", "MMS", "iMessage", "Texting", "Instant message", "IM", "Direct message", "DM", "Private message", "PM", "Chat message"},
		Description: "Communication sent instantly, directly, and/or privately on a service (cellular, social, or similar)",
	},
	{
		Name:        "email",
		Labels:      []string{"Email", "E-mail", "Electronic mail"},
		Description: "Electronic letter sent in a way analogous to physical mail",
	},
	{
		Name:        "social",
		Labels:      []string{"Social media"},
		Description: "Post on social media",
	},
	{
		Name:        "location",
		Labels:      []string{"Location", "Geolocation", "Coordinate"},
		Description: "Location of an entity",
	},
	{
		Name:        "media",
		Labels:      []string{"Media", "Photo", "Video", "Audio"},
		Description: "Photo, video, or audio files",
	},
	// {
	// 	Name:        "screen",
	// 	Labels:      []string{"Screenshot", "Screen capture", "Screencap", "Screen recording"},
	// 	Description: "Screenshot or screen recording",
	// },
	{
		Name:        "collection",
		Labels:      []string{"Collection", "Album", "Playlist"},
		Description: "A group of items",
	},
	{
		Name:        "note",
		Labels:      []string{"Note", "Text"},
		Description: "A brief record written down to assist the memory",
	},
	{
		Name:        "document",
		Labels:      []string{"Document", "Excel", "Word", "PDF", "PowerPoint"},
		Description: "A file that contains text, images, or other data",
	},
}

// Item classifications!
var (
	ClassMessage  = getClassification("message")
	ClassEmail    = getClassification("email")
	ClassSocial   = getClassification("social")
	ClassLocation = getClassification("location")
	ClassMedia    = getClassification("media")
	// ClassScreen = getClassification("screen") // TODO: screenshot...?
	ClassCollection = getClassification("collection")
	ClassNote       = getClassification("note")
	ClassDocument   = getClassification("document")
)

func getClassification(name string) Classification {
	for _, cl := range classifications {
		if cl.Name == name {
			return cl
		}
	}
	return Classification{}
}
