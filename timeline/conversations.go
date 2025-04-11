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
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Conversation represents a conversation, or items that are sent from
// one entity to others.
type Conversation struct {
	Entities       []Entity  `json:"entities"`
	RecentMessages []ItemRow `json:"messages"`

	// the set of entities defines a unique conversation
	entities int64Slice
}

// hasAllEntities returns true if the conversation has all the entities in entityIDs.
func (c Conversation) hasAllEntities(entityIDs []int64) bool {
outer:
	for _, findID := range entityIDs {
		for _, ent := range c.Entities {
			if ent.ID == findID {
				continue outer
			}
		}
		return false
	}
	return true
}

// RecentConversations loads recent conversations from the DB. It honors a select number of ItemSearchParams fields.
func (tl *Timeline) RecentConversations(ctx context.Context, params ItemSearchParams) ([]*Conversation, error) {
	tl.dbMu.RLock()
	defer tl.dbMu.RUnlock()

	tx, err := tl.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	// start by loading the bare conversations, which gives us entities involved and at least one message for each
	convosMap, err := tl.loadRecentConversations(ctx, tx, params)
	if err != nil {
		return nil, err
	}

	// TODO: we might want to try to fill each convo preview with as many messages as a preview allows,
	// rather than only the messages we serendipitously discovered during our iterating

	// convert convo map to slice for returning
	conversations := make([]*Conversation, 0, len(convosMap))
	for _, c := range convosMap {
		conversations = append(conversations, c)
	}

	sort.Slice(conversations, func(i, j int) bool {
		if conversations[j].RecentMessages[0].Timestamp == nil || conversations[i].RecentMessages[0].Timestamp == nil {
			return false
		}
		return conversations[j].RecentMessages[0].Timestamp.Before(*conversations[i].RecentMessages[0].Timestamp)
	})

	// TODO: do we need to commit read-only transactions?
	return conversations, tx.Commit()
}

func (tl *Timeline) loadRecentConversations(ctx context.Context, tx *sql.Tx, params ItemSearchParams) (map[string]*Conversation, error) {
	// keyed by hash of entities, as a unique set of entities is what
	// distinguishes conversations from one another (TODO: or attributes rather than entities? i.e. specific phone numbers instead of people?)
	convosMap := make(map[string]*Conversation)

	tl.convertNamesToIDs(&params)

	// this search is O(n) so make sure it doesn't get too big
	if params.Limit > 1000 || params.Limit <= 0 {
		params.Limit = 30
	}

	// if item classification is unspecified (as opposed to empty), use default
	if params.Classification == nil {
		tl.cachesMu.RLock()
		params.Classification = []string{
			ClassMessage.Name,
			ClassEmail.Name,
			ClassSocial.Name,
		}
		tl.cachesMu.RUnlock()
	}

	const (
		rowLimit    = 1000
		previewSize = 3
		maxQueries  = 100
	)

	// make sure our state is preserved across different query pages
	currentConvo := new(Conversation) // aggregate, as convos with 3+ attributes involved span multiple rows
	var lastItemID int64              // to help us know when we've reached a new message

	// make paging more efficient than using OFFSET; this way we just use
	// the DB index to skip results with timestamps we've already traversed
	var untilUnixMs int64

	// worst case scenario is tens of thousands of items that pertain only to
	// a few conversations that doesn't fill our quota... avoid super long
	// operation by limiting how many queries we make
	var queries int

	saveConvoAndReset := func() bool {
		if len(currentConvo.RecentMessages) == 0 {
			return true // nothing to do, but keep going
		}

		// TODO: we have to filter in code if filtering by entities, because the query
		// treats them like "any" instead of "all" have to be in the conversation (I bet
		// we could use an INTERSECT query like we do for messages... maybe?)
		convoQualifies := len(params.EntityID) <= 1 || currentConvo.hasAllEntities((params.EntityID))

		if convoQualifies {
			hash := currentConvo.entities.hash()
			if conv, exists := convosMap[hash]; exists {
				for i := 0; i < len(currentConvo.RecentMessages) && len(conv.RecentMessages) < previewSize; i++ {
					conv.RecentMessages = append(conv.RecentMessages, currentConvo.RecentMessages[i])
				}
			} else {
				convosMap[hash] = currentConvo
			}
		}

		// reset aggregate value
		currentConvo = new(Conversation)

		// return false if we haven't filled our conversation limit yet
		return len(convosMap) < params.Limit
	}

	for len(convosMap) < params.Limit && queries < maxQueries {
		queries++

		whereClause := `WHERE relationships.to_attribute_id IS NOT NULL`
		var args []any

		// include only conversational items and relations; otherwise we get results like
		// media where people are depicted, or any other items that point to entities...
		whereClause += " AND (relationships.relation_id IN (?, ?) OR items.classification_id IN (?, ?, ?))"
		args = append(args,
			tl.relations[RelSent.Label],
			tl.relations[RelReply.Label],
			tl.classifications[ClassEmail.Name],
			tl.classifications[ClassMessage.Name],
			tl.classifications[ClassSocial.Name],
		)

		if len(params.classificationIDs) > 0 {
			whereClause += " AND ("
			for i, classID := range params.classificationIDs {
				if i > 0 {
					whereClause += " OR "
				}
				whereClause += "items.classification_id=?"
				args = append(args, classID)
			}
			whereClause += ")"
		}
		if len(params.AttributeID) > 0 {
			whereClause += " AND ("
			for i, attrID := range params.AttributeID {
				if i > 0 {
					whereClause += " OR "
				}
				whereClause += "items.attribute_id=?"
				args = append(args, attrID)
			}
			whereClause += ")"
		}
		if len(params.ToAttributeID) > 0 {
			whereClause += " AND ("
			for i, attrID := range params.ToAttributeID {
				if i > 0 {
					whereClause += " OR "
				}
				whereClause += "relationships.to_attribute_id=?"
				args = append(args, attrID)
			}
			whereClause += ")"
		}
		if len(params.EntityID) > 0 {
			whereClause += " AND ("
			for i, entityID := range params.EntityID {
				if i > 0 {
					whereClause += " OR "
				}
				whereClause += "from_ent.id=? OR to_ent.id=?"
				args = append(args, entityID, entityID)
			}
			whereClause += ")"
		}
		if len(params.DataText) > 0 {
			whereClause += " AND ("
			for i, txt := range params.DataText {
				if i > 0 {
					whereClause += " OR "
				}
				whereClause += "items.data_text LIKE '%' || ? || '%'"
				args = append(args, txt)
			}
			whereClause += ")"
		}
		if params.StartTimestamp != nil {
			whereClause += " AND items.timestamp >= ?"
			args = append(args, params.StartTimestamp.UnixMilli())
		}
		if params.EndTimestamp != nil && (untilUnixMs == 0 || untilUnixMs > params.EndTimestamp.UnixMilli()) {
			whereClause += andItemsTimestampLessThanArg
			args = append(args, params.EndTimestamp.UnixMilli())
		} else if untilUnixMs > 0 {
			whereClause += andItemsTimestampLessThanArg
			args = append(args, untilUnixMs)
		}

		args = append(args, rowLimit)

		//nolint:gosec
		q := `SELECT ` + itemDBColumns + `,
				relationships.to_attribute_id,
				from_attr.name, to_attr.name,
				from_attr.value, to_attr.value,
				from_ent.id, from_ent.name, from_ent.picture_file,
				to_ent.id, to_ent.name, to_ent.picture_file
			FROM extended_items AS items
			LEFT JOIN relationships ON relationships.from_item_id = items.id
			LEFT JOIN entity_attributes from_ea ON from_ea.attribute_id = items.attribute_id
			LEFT JOIN entity_attributes to_ea ON to_ea.attribute_id = relationships.to_attribute_id
			LEFT JOIN entities AS from_ent ON from_ent.id = from_ea.entity_id
			LEFT JOIN entities AS to_ent ON to_ent.id = to_ea.entity_id
			LEFT JOIN attributes AS from_attr ON from_attr.id = from_ea.attribute_id
			LEFT JOIN attributes AS to_attr ON to_attr.id = to_ea.attribute_id
			` + whereClause + `
			ORDER BY items.timestamp DESC
			LIMIT ?`

		rows, err := tx.QueryContext(ctx, q, args...)
		if err != nil {
			return nil, err
		}
		defer rows.Close() // FIXME: This is a bug since it's in a for loop; extract into own function

		var count int

		for rows.Next() {
			var fromAttr, toAttr nullableAttribute
			var fromEntity, toEntity Entity
			ir, err := scanItemRow(rows, []any{&toAttr.ID,
				&fromAttr.Name, &toAttr.Name,
				&fromAttr.Value, &toAttr.Value,
				&fromEntity.id, &fromEntity.name, &fromEntity.Picture,
				&toEntity.id, &toEntity.name, &toEntity.Picture})
			if err != nil {
				return nil, fmt.Errorf("loading recent conversations: %w", err)
			}
			count++

			if ir.ID != lastItemID {
				// this row is a new item, so what we have aggregated so far represents a single conversation
				if !saveConvoAndReset() {
					break
				}
			}

			// remember this item ID for the next row so we can know if we've moved to the next item
			lastItemID = ir.ID

			// append this message to the current conversation aggregate
			if len(currentConvo.RecentMessages) < previewSize {
				currentConvo.RecentMessages = append(currentConvo.RecentMessages, ir)
			}

			// move nullable ints into non-nullable fields that are read from (and which are easier and safer to work with)
			if fromEntity.id != nil {
				fromEntity.ID = *fromEntity.id
			}
			if toEntity.id != nil {
				toEntity.ID = *toEntity.id
			}

			// record the sender and receiver as part of this conversation
			if fromEntity.ID > 0 {
				currentConvo.entities.appendIfUnique(fromEntity.ID)
			}
			if toEntity.ID > 0 {
				currentConvo.entities.appendIfUnique(toEntity.ID)
			}

			// if entity not added to conversation yet, do so now
			// TODO: a more correct algorithm is to add the entity if it doesn't exist, but if it does, add the attribute to it if it isn't already
			var fromEntFound, toEntFound bool
			for _, ent := range currentConvo.Entities {
				if fromEntFound && toEntFound {
					break
				}
				if ent.ID == fromEntity.ID {
					fromEntFound = true
					continue
				}
				if ent.ID == toEntity.ID {
					toEntFound = true
					continue
				}
			}
			if !fromEntFound {
				if fromEntity.name != nil {
					fromEntity.Name = *fromEntity.name
				}
				fromEntity.Attributes = append(fromEntity.Attributes, fromAttr.attribute())
				currentConvo.Entities = append(currentConvo.Entities, fromEntity)
			}
			if !toEntFound {
				if toEntity.name != nil {
					toEntity.Name = *toEntity.name
				}
				toEntity.Attributes = append(toEntity.Attributes, toAttr.attribute())
				currentConvo.Entities = append(currentConvo.Entities, toEntity)
			}

			if ir.Timestamp != nil {
				untilUnixMs = ir.Timestamp.UnixMilli()
			}
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}

		if count < rowLimit {
			break // no more rows
		}
	}

	// save last aggregate
	saveConvoAndReset()

	return convosMap, nil
}

// LoadConversation loads the conversation according to select search parameters.
func (tl *Timeline) LoadConversation(ctx context.Context, params ItemSearchParams) (SearchResults, error) {
	// for now, I don't think it makes sense to support both entities and attributes...
	// I mean, you either are looking up a conversation between people or a conversation
	// between phone numbers/email addresses/etc, right? but if the need arises we can
	// probably make it work -- our code does support both but I haven't tested both together
	if len(params.EntityID) > 0 && len(params.AttributeID) > 0 {
		return SearchResults{}, errors.New("lookup by both entity and attribute not currently supported")
	}

	tl.dbMu.RLock()
	defer tl.dbMu.RUnlock()

	tx, err := tl.db.Begin()
	if err != nil {
		return SearchResults{}, err
	}
	defer tx.Rollback()

	// start by loading the bare conversations, which gives us entities involved and at least one message for each
	results, err := tl.loadConversation(ctx, tx, params)
	if err != nil {
		return SearchResults{}, err
	}

	for _, sr := range results {
		err = tl.expandRelationships(ctx, tx, params.Related, sr)
		if err != nil {
			return SearchResults{}, err
		}
	}

	sr := SearchResults{
		Items: results,
	}

	// TODO: do we need to commit read-only transactions?
	return sr, tx.Commit()
}

func (tl *Timeline) loadConversation(ctx context.Context, tx *sql.Tx, params ItemSearchParams) ([]*SearchResult, error) {
	// if no entities or attributes specified, no conversation can be found
	if len(params.EntityID) == 0 && len(params.AttributeID) == 0 {
		return []*SearchResult{}, nil
	}

	q, args, err := tl.prepareConversationQuery(params)
	if err != nil {
		return nil, err
	}

	rows, err := tx.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []*SearchResult
	for rows.Next() {
		var re relatedEntity
		itemRow, err := scanItemRow(rows, []any{&re.ID, &re.Name, &re.Picture})
		if err != nil {
			return nil, fmt.Errorf("loading conversation: %w", err)
		}
		results = append(results, &SearchResult{RepoID: tl.id.String(), ItemRow: itemRow, Entity: &re})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// conversation messages are retrieved most-recent-first (timestamp DESC) but they'll
	// generally be rendered in ASC order because that's more natural/intuitive
	for i, j := 0, len(results)-1; i < j; i, j = i+1, j-1 {
		results[i], results[j] = results[j], results[i]
	}

	return results, nil
}

func (tl *Timeline) prepareConversationQuery(params ItemSearchParams) (string, []any, error) {
	tl.convertNamesToIDs(&params)

	if params.Limit > 1000 || params.Limit <= 0 {
		params.Limit = 100
	}

	sortDir := strings.ToUpper(string(params.Sort))
	if sortDir == "" {
		sortDir = string(SortDesc)
	}
	if sortDir != string(SortAsc) && sortDir != string(SortDesc) {
		return "", nil, fmt.Errorf("invalid sort direction: %s", sortDir)
	}

	// if item classification is unspecified (as opposed to empty), use default
	if params.Classification == nil {
		tl.cachesMu.RLock()
		params.Classification = []string{
			ClassMessage.Name,
			ClassEmail.Name,
			ClassSocial.Name,
		}
		tl.cachesMu.RUnlock()
	}

	var where string
	var args, whereArgs []any

	if len(params.classificationIDs) > 0 {
		where += " AND ("
		for i, classID := range params.classificationIDs {
			if i > 0 {
				where += " OR "
			}
			where += "items.classification_id=?"
			whereArgs = append(whereArgs, classID)
		}
		where += ")"
	}
	if len(params.DataSourceName) > 0 {
		where += " AND ("
		for i, dsn := range params.DataSourceName {
			if i > 0 {
				where += " OR "
			}
			where += "data_source_name=?"
			whereArgs = append(whereArgs, dsn)
		}
		where += ")"
	}
	if len(params.DataText) > 0 {
		where += " AND ("
		for i, txt := range params.DataText {
			if i > 0 {
				where += " OR "
			}
			where += "items.data_text LIKE '%' || ? || '%'"
			whereArgs = append(whereArgs, txt)
		}
		where += ")"
	}
	if params.StartTimestamp != nil {
		where += " AND items.timestamp >= ?"
		whereArgs = append(whereArgs, params.StartTimestamp.UnixMilli())
	}
	if params.EndTimestamp != nil {
		where += andItemsTimestampLessThanArg
		whereArgs = append(whereArgs, params.EndTimestamp.UnixMilli())
	}

	// clean up and prepend the actual "WHERE"
	if len(where) > 0 {
		where += " AND "
	}
	where = "WHERE " + strings.TrimPrefix(where, " AND ")

	// TODO: Not sure if this is needed (is it okay if "attachments" show up as part of a conversation?
	// seems unlikely that there would be such a thing, but):
	// To select only "root" items (items that are NOT at the end of a
	// directed relation from another item), join in the relation values
	// if needed, then use this WHERE clause:
	//  ...LEFT JOIN relationships ON relationships.to_item_id = items.id
	//     LEFT JOIN relations ON relations.id = relationships.relation_id
	// ... WHERE (relations.directed != 1 OR relations.subordinating = 0 OR relationships.to_item_id IS NULL)
	// (the WHERE clause can be AND'ed into an existing one)
	// (TODO: Not sure yet if a GROUP BY items.id is needed to avoid duplicates that have multiple relations)

	selects := make([]string, 0, len(params.EntityID))
	idsForExcept := make([]string, 0, len(params.EntityID))
	for _, entityID := range params.EntityID {
		idsForExcept = append(idsForExcept, strconv.FormatInt(entityID, 10))
		selects = append(selects, fmt.Sprintf(
			`SELECT %s, entities.id, entities.name, entities.picture_file
			FROM extended_items AS items
			JOIN relationships ON relationships.from_item_id = items.id
			JOIN entity_attributes from_ea ON from_ea.attribute_id = items.attribute_id
			JOIN entity_attributes to_ea   ON to_ea.attribute_id   = relationships.to_attribute_id
			JOIN entities ON from_ea.entity_id = entities.id
			%s
				(from_ea.entity_id=? OR to_ea.entity_id=?)`, itemDBColumns, where))
		args = append(args, append(whereArgs, entityID, entityID)...)
	}
	for _, attrID := range params.AttributeID {
		idsForExcept = append(idsForExcept, strconv.FormatInt(attrID, 10))
		// TODO: we'd have to add some joins here to get entity info...
		selects = append(selects, fmt.Sprintf(
			`SELECT %s, entities.id, entities.name, entities.picture_file
			FROM extended_items AS items
			JOIN relationships ON relationships.from_item_id = items.id
			%s
				AND (items.attribute_id=? OR relationships.to_attribute_id=?)
				AND (relations.directed != 1 OR relations.subordinating = 0 OR relationships.to_item_id IS NULL)`, itemDBColumns, where))
		args = append(args, append(whereArgs, attrID, attrID)...)
	}

	q := strings.Join(selects, "\nINTERSECT\n")

	exceptStr := strings.Join(idsForExcept, ", ")

	// append the EXCEPT clause, otherwise we get a superset of what we're querying
	// (like, messages from conversations from groups that include *at least* people instead of *only* these people)
	if len(params.EntityID) > 0 {
		q += fmt.Sprintf(`
		EXCEPT
		SELECT %s, entities.id, entities.name, entities.picture_file
			FROM extended_items AS items
			JOIN relationships ON relationships.from_item_id = items.id
			JOIN entity_attributes from_ea ON from_ea.attribute_id = items.attribute_id
			JOIN entity_attributes to_ea ON to_ea.attribute_id = relationships.to_attribute_id
			JOIN entities ON from_ea.entity_id = entities.id
			WHERE (from_ea.entity_id NOT IN (%s) OR to_ea.entity_id NOT IN (%s))`,
			itemDBColumns, exceptStr, exceptStr)
	}

	q += fmt.Sprintf("\nORDER BY items.timestamp %s LIMIT ?", sortDir)
	args = append(args, params.Limit)

	return q, args, nil
}

type int64Slice []int64

func (s int64Slice) hash() string {
	sort.Sort(s) // TODO: this doesn't seem to be working
	var sb strings.Builder
	for _, v := range s {
		sb.WriteString(strconv.FormatInt(v, 10))
		sb.WriteRune(',')
	}
	return sb.String()
}

func (s *int64Slice) appendIfUnique(v int64) {
	for _, elem := range *s {
		if elem == v {
			return
		}
	}
	*s = append(*s, v)
}

const andItemsTimestampLessThanArg = " AND items.timestamp < ?"

// Implement sort.Interface
func (s int64Slice) Len() int           { return len(s) }
func (s int64Slice) Less(i, j int) bool { return s[i] < s[j] }
func (s int64Slice) Swap(i, j int)      { s[i], s[j] = s[j], s[i] }
