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
	"fmt"
	"strings"
	"time"
)

type EntitySearchParams struct {
	// The UUID of the open timeline to search.
	Repo string `json:"repo,omitempty"`

	RowID      []int64     `json:"row_id,omitempty"`
	Type       []string    `json:"type,omitempty"`
	JobID      []int64     `json:"job_id,omitempty"`
	Name       []string    `json:"name,omitempty"`
	Attributes []Attribute `json:"attributes,omitempty"`

	// If true, OR different fields instead of AND
	OrFields bool `json:"or_fields,omitempty"`

	// // query related items and people recursively to this many degrees
	// // (e.g. 0 is only the item, 1 adds direct relationships, etc...)
	// // TODO: not implemented (yet?) -- if we did, it'd probably only make sense to get relationships to other entities...
	// Relationships int `json:"relationships,omitempty"`

	// Supported columns: name (default), id, birth_date, item_count (can be slow), attribute
	OrderBy string `json:"order_by,omitempty"`

	// Default sort direction is based on order by:
	// id, name, birth_date, attribute: ASC
	// item_count: DESC
	// (overridden if doing birth_date proximity search)
	Sort SortDir `json:"sort,omitempty"`

	Limit int `json:"limit,omitempty"`
}

func (tl *Timeline) SearchEntities(ctx context.Context, params EntitySearchParams) ([]Entity, error) {
	if params.Limit == 0 {
		params.Limit = 100
	}

	// get the DB query string and associated arguments
	q, args, err := tl.prepareEntitySearchQuery(params)
	if err != nil {
		return nil, err
	}

	// lock DB for entirety of this operation, as it may involve many queries
	tl.dbMu.RLock()
	defer tl.dbMu.RUnlock()

	// run query and scan results
	rows, err := tl.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("querying db for people: %w", err)
	}
	defer rows.Close()

	// as we iterate, each entity may appear across multiple rows, one per attribute
	// but as we've sorted by entity ID, we know they will appear together, and we
	// build its aggregate entity until we encounter a new entity ID
	var results []Entity
	var aggregate Entity

	for rows.Next() {
		var ent Entity
		var identDS, autolinkImportID, autolinkAttributeID *int64 // TODO: we don't use these... yet
		var nattr nullableAttribute
		var itemCount, stored int64

		var dests = []any{
			&ent.ID, &ent.typeID, &ent.Type, &ent.JobID, &stored, &ent.name,
			&ent.Picture, &identDS, &autolinkImportID, &autolinkAttributeID,
			&nattr.ID, &nattr.Name, &nattr.Value,
			&nattr.Longitude, &nattr.Latitude, &nattr.Altitude}

		err := rows.Scan(dests...)
		if err != nil {
			return nil, err
		}

		// convert nullable attribute to regular attribute
		attr := nattr.attribute()
		attr.ItemCount = itemCount

		// convert name and dates
		if ent.name != nil {
			ent.Name = *ent.name
		}
		ent.Stored = time.Unix(stored, 0)

		if identDS != nil {
			attr.Identity = true
		}

		// attach this attribute to this person
		ent.Attributes = append(ent.Attributes, attr)

		// if this row is the same person as the last, simply
		// append to the aggregate identities and go to next row
		if ent.ID == aggregate.ID {
			aggregate.Attributes = append(aggregate.Attributes, attr)
			continue
		}

		// at this point, it's a different person than previous row;
		// if the aggregate so far has any person, save it to results
		// and reset the aggregate to this new entity
		if aggregate.ID != 0 {
			results = append(results, aggregate)
			if len(results) >= params.Limit {
				break
			}
		}
		aggregate = ent
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating person rows: %w", err)
	}

	// add the last remaining aggregate value to the results
	if aggregate.ID != 0 && len(results) < params.Limit {
		results = append(results, aggregate)
	}

	return results, nil
}

func (tl *Timeline) prepareEntitySearchQuery(params EntitySearchParams) (string, []any, error) {
	// TODO: explain this query
	// (LEFT JOINs are done on the attributes and entity_attributes in case they opened a brand
	// new repo with no attributes about themselves, otherwise nothing will show up)
	q := `SELECT
		entities.id,
		entities.type_id,
		entity_types.name,
		entities.job_id,
		entities.stored,
		entities.name,
		entities.picture_file,
		all_entity_attributes.data_source_id,
		all_entity_attributes.autolink_job_id,
		all_entity_attributes.autolink_attribute_id,
		attributes.id,
		attributes.name,
		attributes.value,
		attributes.longitude,
		attributes.latitude,
		attributes.altitude
	FROM entities
	JOIN entity_types ON entity_types.id = entities.type_id
	LEFT JOIN entity_attributes AS all_entity_attributes ON all_entity_attributes.entity_id = entities.id
	LEFT JOIN entity_attributes AS specific_entity_attributes ON specific_entity_attributes.entity_id = entities.id
	LEFT JOIN attributes ON attributes.id = all_entity_attributes.attribute_id
	LEFT JOIN attributes AS specific_attributes ON specific_attributes.id = specific_entity_attributes.attribute_id
	`

	// build the WHERE in terms of groups of OR's that are AND'ed together
	var args []any
	var clauseCount int
	and := func(ors func()) {
		clauseCount = 0
		if len(args) == 0 {
			q += " WHERE"
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
		q = strings.TrimSuffix(q, " WHERE ()")
	}
	or := func(clause string, vals ...any) {
		if clauseCount > 0 {
			q += " OR "
		}
		q += clause
		args = append(args, vals...)
		clauseCount++
	}

	and(func() {
		for _, v := range params.RowID {
			or("entities.id=?", v)
		}
	})
	and(func() {
		for _, v := range params.Type {
			typeID, err := tl.entityTypeNameToID(v)
			if err == nil {
				or("entities.type_id=?", typeID)
			}
		}
	})
	and(func() {
		for _, v := range params.JobID {
			or("entities.job_id=?", v)
		}
	})
	and(func() {
		for _, v := range params.Name {
			or("entities.name LIKE '%'||?||'%'", v)
		}
	})
	// TODO: reinstate this functionality somehow, now that birthdate/place are attributes
	// and(func() {
	// 	for _, v := range params.BirthPlace {
	// 		or("entities.birth_place LIKE '%'||?||'%'", v)
	// 	}
	// })
	// if params.StartBirthDate != nil {
	// 	and(func() {
	// 		or("entities.birth_date >= ?", params.StartBirthDate.Unix())
	// 	})
	// }
	// if params.EndBirthDate != nil {
	// 	and(func() {
	// 		or("entities.birth_date <= ?", params.EndBirthDate.Unix())
	// 	})
	// }
	and(func() {
		for _, attr := range params.Attributes {
			switch {
			case attr.Name != "" && attr.Value == "":
				or("specific_attributes.name=?", attr.Name)
			case attr.Name == "" && attr.Value != "":
				or("specific_attributes.value LIKE '%'||?||'%'", attr.Value)
			case attr.Name != "" && attr.Value != "":
				or("specific_attributes.name=? AND specific_attributes.value LIKE '%'||?||'%'", attr.Name, attr.Value)
			}
		}
	})

	// remove duplicate rows, which happens when an entity has more than one attribute
	// (I think it's because we have 2 JOIN paths -- doesn't happen with just 1 JOIN path)
	// (if this turns out to be buggy, maybe we need conditional JOINs above, like with CASE...)
	q += "\nGROUP BY entities.id, attributes.id"

	// sort direction
	sortDir := strings.ToUpper(string(params.Sort))
	if sortDir == "" {
		sortDir = string(SortAsc)
	}
	if sortDir != string(SortAsc) && sortDir != string(SortDesc) {
		return "", nil, fmt.Errorf("invalid sort direction: %s", sortDir)
	}

	// TODO: restore this (it is now an attribute)
	// // birthdate proximity is a bit unique, in that we have to sort ASC (nearest first)
	// // otherwise we end up with nonsense results; and we have to order by birthday,
	// // otherwise our lookup isn't a proximity search at all
	// if params.BirthDate != nil {
	// 	// always sort ascending for nearest first; then make sure to order by birth_date
	// 	sortDir = string(SortAsc)
	// 	params.OrderBy = "birth_date"
	// }

	// ordering
	switch params.OrderBy {
	case "item_count":
		q += "\nORDER BY max(item_count) OVER (PARTITION BY entities.id) " + sortDir
	// TODO: restore this (it is now an attribute)
	// case "birth_date":
	// 	q += "\nORDER BY abs(? - entities.birth_date), entities.id " + sortDir
	// 	args = append(args, params.BirthDate.Unix())
	case "attribute":
		q += "\nORDER BY attribute.value " + sortDir
	case "id":
		q += "\nORDER BY entities.id " + sortDir
	case "name", "":
		q += "\nORDER BY entities.name " + sortDir
	}
	q += " NULLS LAST"

	// limit (this limits the total rows; keep in mind that a single person can take up several rows from multiple attributes)
	// if specific entities are being requested, no limit; otherwise make sure it doesn't go crazy
	if len(params.RowID) > 0 {
		params.Limit = 0 // allow on average this many attributes per entity; this is just an arbitrary multiplier for now...
	}
	if params.Limit > 0 {
		q += "\nLIMIT ?"
		args = append(args, params.Limit)
	}

	return q, args, nil
}
