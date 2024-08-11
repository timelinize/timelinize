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
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/ttacon/libphonenumber"
	"go.uber.org/zap"
)

// TODO: update godoc
// Entity represents a person. All these fields go into the persons table,
// except for UserID which goes into person_identities and which maps that
// user ID to the person being described by name and birth information.
// Birth information is almost always added manually by users, since
// data sources don't usually provide this, and is used to distinguish
// different persons of the same name, and is totally optional.
type Entity struct {
	// database row IDs
	ID     int64  `json:"id,omitempty"`
	id     *int64 // temporary holding place for DB value
	typeID int64

	Type string  `json:"type,omitempty"`
	name *string // temporary holding place for DB value
	Name string  `json:"name,omitempty"`

	// To set a profile picture for this entity, set this field.
	NewPicture DataFunc `json:"-"`

	// Contains the path to the previously-set profile picture file
	// relative to the repo root. NOT FOR INPUT FROM DATA SOURCES.
	Picture *string `json:"picture,omitempty"`

	// Very optional extra information about this entity.
	Metadata Metadata `json:"metadata,omitempty"`

	Attributes []Attribute `json:"attributes,omitempty"`

	// Fields below are only for use with search or JSON serialization
	ImportID *int64    `json:"import_id,omitempty"`
	Stored   time.Time `json:"stored,omitempty"`
}

func (e Entity) String() string {
	return fmt.Sprintf("[name=%s picture=%v metadata=%v attributes=%v]",
		e.Name, e.Picture, e.Metadata, e.Attributes)
}

// IsEmpty returns true if the entity has no information.
// It is best to check this AFTER normalizing, which may
// delete data that looks non-empty but is actually empty.
func (e Entity) IsEmpty() bool {
	return e.ID == 0 &&
		e.Name == "" &&
		e.NewPicture == nil &&
		e.Picture == nil &&
		len(e.Metadata) == 0 &&
		len(e.Attributes) == 0
}

func (e Entity) dbName() *string {
	if strings.TrimSpace(e.Name) == "" {
		return nil
	}
	return &e.Name
}

// TODO: I'm not sure we're representing the new date attributes properly in the DB
// func (p Entity) birthDateUnix() *int64 {
// 	if p.BirthDate == nil {
// 		return nil
// 	}
// 	bdUnix := p.BirthDate.Unix()
// 	return &bdUnix
// }

// func (p Entity) deathDateUnix() *int64 {
// 	if p.DeathDate == nil {
// 		return nil
// 	}
// 	ddUnix := p.DeathDate.Unix()
// 	return &ddUnix
// }

func (p Entity) Attribute(name string) (Attribute, bool) {
	for _, attr := range p.Attributes {
		if attr.Name == name {
			return attr, true
		}
	}
	return Attribute{}, false
}

func (p Entity) AttributeValue(name string) any {
	for _, attr := range p.Attributes {
		if attr.Name == name {
			return attr.Value
		}
	}
	return ""
}

func (tl *Timeline) normalizeEntity(e *Entity) error {
	var identityAttr Attribute
	for i := 0; i < len(e.Attributes); i++ {
		// remove empty attributes
		if e.Attributes[i].Empty() {
			e.Attributes = append(e.Attributes[:i], e.Attributes[i+1:]...)
			i--
			continue
		}
		// ensure at most one identity attribute; multiple would be confusing/ambiguous and
		// could lead to confusion or worse (maybe uniqueness constraint violations)
		if e.Attributes[i].Identity {
			if identityAttr.Name != "" {
				return fmt.Errorf("multiple ambiguous identity attributes: at least %s=%s and %s=%s",
					identityAttr.Name, identityAttr.Value, e.Attributes[i].Name, e.Attributes[i].Value)
			}
			identityAttr = e.Attributes[i]
		}
		// normalize known attributes
		e.Attributes[i] = normalizeAttribute(e.Attributes[i])
	}

	// clean up name too; use regex to remove repeated spaces, then trim spaces on edges
	e.Name = strings.TrimSpace(multiSpaceRegex.ReplaceAllString(e.Name, " "))

	// oh, and ensure type is set (DB row requires entity type ID, not name)
	if e.Type == "" && e.typeID == 0 {
		e.Type = "person" // assume a person, I guess -- data sources don't often distinguish
	}
	if e.typeID == 0 {
		typeID, err := tl.entityTypeNameToID(e.Type)
		if err != nil {
			return fmt.Errorf("getting entity type ID: %v (entity_type=%s)", err, e.Type)
		}
		e.typeID = typeID
	}

	e.Metadata.Clean()

	return nil
}

func (e Entity) metadataString() (*string, error) {
	if len(e.Metadata) == 0 {
		return nil, nil
	}
	metaBytes, err := json.Marshal(e.Metadata)
	if err != nil {
		return nil, fmt.Errorf("encoding entity metadata as JSON: %v", err)
	}
	metaString := string(metaBytes)
	return &metaString, nil
}

type Attribute struct {
	// The name of the attribute. For attributes that define
	// a user's identity on a data source, this name *MUST*
	// be unique to the data source if it's possible for a
	// different person to have the same attribute+value, or
	// else collisions/confusion could arise in the processing
	// logic. (For example, "username" is not a good attribute
	// name.) Specifically, given a data source, attribute name,
	// and a specific value, we should be able to pinpoint
	// *which* person or people with certainty even if the value
	// alone is the same for different persons. If the value is
	// guaranteed to be unique even if the attribute name isn't,
	// then it is OK for the attribute name to be shared by data
	// sources (e.g. phone number or email address -- these
	// concepts span data sources, but they are globally unique
	// at any given point in time). See the Identifying field.
	//
	// Known global attributes will be recognized and/or standardized:
	//
	// - phone_number: Can be prefixed with region (default: "US"), e.g.: "US:123-456-7890"
	// - TODO: email address
	// - TODO: physical address
	// - TODO: gender
	Name string `json:"name"`

	// The value of the attribute.
	Value any `json:"value"`

	// Optional; an alternate value intended for display or as a description.
	AltValue string `json:"alt_value,omitempty"`

	// If true, this attribute+value combination can be used to
	// identify a person.
	Identifying bool `json:"identifying,omitempty"`

	// If true, this attribute defines a person's identity
	// on the data source associated with the current
	// processing. There should only be 1 identity attribute
	// per data source. When associated with an Item.Owner,
	// the item will be credited to this attribute in the DB.
	// Implies Identifying=true.
	Identity bool `json:"identity"`

	// If the attribute represents a place, put its location here.
	// For a point, use Latitude1 and Longitude1. For a rectangle,
	// use Lat1/Lon1 as the top-left corner and Lat2/Lon2 as the
	// bottom-right corner.
	Latitude1  *float64 `json:"latitude1,omitempty"`
	Longitude1 *float64 `json:"longitude1,omitempty"`
	Latitude2  *float64 `json:"latitude2,omitempty"`
	Longitude2 *float64 `json:"longitude2,omitempty"`

	// Optional metadata associated with this attribute.
	Metadata Metadata `json:"metadata,omitempty"`

	//////////////////////////////////////////////////////////////////////////
	// The fields below are NOT intended for use by data sources (importers).

	// NOT FOR USE BY DATA SOURCES.
	ID int64 `json:"id,omitempty"`

	// NOT FOR USE BY DATA SOURCES. For search results only.
	ItemCount int64 `json:"item_count,omitempty"`
}

func (a Attribute) Empty() bool {
	if strings.TrimSpace(a.Name) == "" {
		return true
	}
	return strings.TrimSpace(a.valueString()) == ""
}

func (a Attribute) valueForDB() any {
	switch val := a.Value.(type) {
	case *time.Time:
		if val == nil {
			return nil
		}
		return val.Unix()
	case time.Time:
		return val.Unix()
	default:
		return val
	}
}

func (a Attribute) valueString() string {
	switch val := a.Value.(type) {
	case nil:
		return ""
	case string:
		return val
	case *time.Time:
		// this case only exists because if the *time.Time is nil,
		// we get a panic for calling String() on a nil pointer
		// (because time.Time is a fmt.Stringer)
		if val == nil {
			return ""
		}
		return val.String()
	case fmt.Stringer:
		if val == nil {
			return ""
		}
		return val.String()
	case int:
		return strconv.Itoa(val)
	default:
		return fmt.Sprintf("%v", val)
	}
}

func normalizeAttribute(attr Attribute) Attribute {
	attr.Name = strings.TrimSpace(attr.Name)

	switch val := attr.Value.(type) {
	case string:
		// if this is a time value that was deserialized from JSON, convert it to a time type
		// (values coming directly from data sources should already be time type); we do this
		// because we conventionally store timestamps as unix timestamps in the DB, and the
		// values need to be time types in order for us to do that
		if parsedTime, err := time.Parse(time.RFC3339, val); err == nil {
			attr.Value = parsedTime
		} else {
			attr.Value = strings.TrimSpace(val)
		}
	}

	switch attr.Name {
	case AttributePhoneNumber:
		// TODO: region could be stored in metadata now
		region, num, ok := strings.Cut(attr.Value.(string), ":")
		if !ok {
			num = region
			region = ""
		}
		stdPhoneNum, err := NormalizePhoneNumber(num, region)
		if err == nil {
			attr.Value = stdPhoneNum
		}
	}

	return attr
}

// normalizePhoneNumber attempts to parse number and returns
// a standardized version in E164 format. If the number does
// not have an explicit region/country code, the country code
// for the region is used instead. The default region is US.
//
// We chose E164 because that's what Twilio uses.
func NormalizePhoneNumber(number, defaultRegion string) (string, error) {
	if defaultRegion == "" {
		defaultRegion = "US"
	}
	ph, err := libphonenumber.Parse(number, defaultRegion)
	if err != nil {
		return "", err
	}
	return libphonenumber.Format(ph, libphonenumber.E164), nil
}

// processEntityPicture downloads the entity's picture (if relevant), then stores it on
// disk. It does NOT update the database, but it does return the path to the picture file.
func (p *processor) processEntityPicture(ctx context.Context, e Entity) (string, error) {
	if e.ID <= 0 {
		return "", fmt.Errorf("missing or invalid row ID %d", e.ID)
	}
	if e.NewPicture == nil {
		return "", nil
	}

	r, err := e.NewPicture(ctx)
	if err != nil {
		return "", fmt.Errorf("opening profile picture reader: %v", err)
	}
	if r == nil {
		return "", nil // this could happen if the data source finds out there's no picture and no error
	}
	defer r.Close()

	buffered := bufio.NewReader(r)

	peekedBytes, err := buffered.Peek(512)
	if err != nil {
		return "", fmt.Errorf("could not peek profile picture to determine type: %v", err)
	}

	contentType := http.DetectContentType(peekedBytes)

	// use "/" separators here; the fullpath() method will adjust for OS path seperator
	// (we use the "%09d" formatter so file systems sort more conveniently, but it
	// also does not look like a date/time)
	pictureFile := path.Join(AssetsFolderName, "profile_pictures", fmt.Sprintf("entity_%09d", e.ID))
	disposition, _, _ := mime.ParseMediaType(contentType)
	switch disposition {
	case "image/png":
		pictureFile += ".png"
	case "image/webp":
		pictureFile += ".webp"
	case "image/gif":
		pictureFile += ".gif"
	case "image/jpeg", "":
		fallthrough
	default:
		pictureFile += ".jpg"
	}
	fullPath := p.tl.FullPath(pictureFile)

	// ensure parent dir exists, then open file for writing
	if err = os.MkdirAll(filepath.Dir(fullPath), 0700); err != nil {
		return "", err
	}

	w, err := os.OpenFile(fullPath, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0600)
	if err != nil {
		return "", err
	}
	defer w.Close()

	if _, err := io.Copy(w, buffered); err != nil {
		return "", err
	}

	if err := w.Sync(); err != nil {
		return "", err
	}

	return pictureFile, nil
}

// TODO: update this godoc related to latentID; and note that an empty latentID and nil error is a possible and valid return value here.
// processEntity ingests the incoming person into the timeline. The person's attributes are
// normalized, the person is looked up in the DB according to their identifying attributes
// first, then their ID if given, or their name (if enabled as ID); and if one match is found,
// the person's information and attributes are merged with the one in the database if this one
// has anything new to add; otherwise, it creates a new entity in the DB with the information
// provided. If multiple matches are found (a single identity attribute can technically map to
// multiple entities, however, I believe this has to be done manually for now), then the
// information for the person(s) is not updated (as that would make them look alike) but the
// attributes are added and linked at least to this one person. The row ID of the ID attribute
// that represents the input entity is returned. Note that a row ID of 0 can be returned if
// there is no identity attribute!
func (p *processor) processEntity(ctx context.Context, tx *sql.Tx, in Entity) (latentID, error) {
	// remove any empty attributes and standardize the entity
	if err := p.tl.normalizeEntity(&in); err != nil {
		return latentID{}, err
	}

	// don't save an empty entity (make sure we do this AFTER normalizing); it's totally
	// allowed for items to not have any particular owner specified
	if in.IsEmpty() {
		return latentID{}, nil
	}

	// try to find matching entities with given information; first try attributes because
	// they are more descriptive, then if no matches, use ID directly if provided, or if
	// not, then use name if only it is provided and set to act as an ID (not ideal though,
	// as names tend to change...) -- first try attributes by clearing name and ID from a
	// copy of the incoming entity
	inCopy := in
	inCopy.ID = 0
	entities, entityAutolinkAttrs, err := p.loadEntities(ctx, tx, &inCopy)
	if err != nil {
		return latentID{}, fmt.Errorf("searching entities by attributes: %v", err)
	}

	// then only if that didn't yield any results, try ID next
	if len(entities) == 0 && in.ID > 0 {
		entities, entityAutolinkAttrs, err = p.loadEntities(ctx, tx, &in)
		if err != nil {
			return latentID{}, fmt.Errorf("searching entities by ID: %v", err)
		}
	}

	// if our searches yielded 0 results, we can insert the entity; if 1, we can update it
	if len(entities) == 0 {
		// don't create a new entity if *only* an entity ID was provided (it must have been
		// erroneous?) with no other attributes, as that implies the entity should already
		// exist but it does not... creating a new one is wrong
		if in.ID > 0 && len(in.Attributes) == 0 {
			return latentID{}, fmt.Errorf("input entity had non-zero row ID (%d), but that entity was not found; and without attributes we do not create a new entity", in.ID)
		}

		// marshal metadata and convert it to a string pointer... awkward, but seems more reasonable than a blob...
		metadata, err := in.metadataString()
		if err != nil {
			return latentID{}, err
		}

		// if we could not find a person by any of those identities, add new person to DB
		err = tx.QueryRowContext(ctx, `INSERT INTO entities
				(type_id, import_id, name, metadata) VALUES (?, ?, ?, ?)
				RETURNING id`,
			in.typeID, p.impRow.id, in.dbName(), metadata).Scan(&in.ID)
		if err != nil {
			return latentID{}, fmt.Errorf("adding new person %+v: %v", in, err)
		}

		// now that we have the row ID, save profile picture
		pictureFile, err := p.processEntityPicture(ctx, in)
		if err != nil {
			p.log.Error("saving new entity's profile picture",
				zap.Int64("entity_id", in.ID),
				zap.Error(err))
		} else if pictureFile != "" {
			_, err = tx.Exec(`UPDATE entities SET picture_file=? WHERE id=?`, pictureFile, in.ID) // TODO: LIMIT 1, if ever implemented
			if err != nil {
				p.log.Error("updating new entity's profile picture in DB",
					zap.Int64("entity_id", in.ID),
					zap.String("picture_file", pictureFile),
					zap.Error(err))
			}
		}

		entities = append(entities, in)

		atomic.AddInt64(p.newEntityCount, 1)

	} else if len(entities) == 1 {
		// if the identity attribute(s) matched exactly 1 person, update their info in the DB
		// (if it matched more than 1 person, we don't update multiple of them, because we only
		// get 1 person at a time from data sources and we'd end up making the two look alike...
		// I can't think of a single data source that explicitly maps one of their accounts to
		// multiple persons and gives us that information... so we lose that granularity or
		// specificity from data sources, and we have to rely on the user to maintain that
		// information because only they'd know)

		entity := entities[0]

		// update entity with the latest info from the incoming entity
		var setClause string
		var args []any
		// TODO: I used to have "|| (len(in.Name) > len(entity.Name))" as a condition, hoping that it would add missing information (such as a last name to a first name) but when testing with Josh it replaced his name with "Synology NAS Diskstation..." gibberish... anyway...
		if entity.Name == "" && in.Name != "" {
			if setClause != "" {
				setClause += ", "
			}
			setClause += "name=?"
			args = append(args, in.Name)
		}
		if entity.Picture == nil && in.NewPicture != nil {
			// we don't update existing profile pictures at this time... but we can set the picture if one doesn't already exist
			in.ID = entity.ID
			pictureFile, err := p.processEntityPicture(ctx, in)
			if err != nil {
				p.log.Error("saving existing entity's new profile picture",
					zap.Int64("entity_id", entity.ID),
					zap.Error(err))
			} else if pictureFile != "" {
				if setClause != "" {
					setClause += ", "
				}
				setClause += "picture_file=?"
				args = append(args, pictureFile)
			}
		}
		setClause = strings.TrimSuffix(setClause, ", ")

		if len(args) > 0 {
			args = append(args, entity.ID)
			_, err := tx.ExecContext(ctx, `UPDATE entities SET `+setClause+` WHERE id=?`, args...)
			if err != nil {
				return latentID{}, fmt.Errorf("updating person %d (%+v): %v", entity.ID, in, err)
			}
		}
	}

	// now store each attribute+value combo if we don't have it yet,
	// and link it to the entity
	var identityAttributeID int64
	for _, attr := range in.Attributes {
		attrID, err := storeAttribute(ctx, tx, attr)
		if err != nil {
			return latentID{}, fmt.Errorf("%w (entity_name=%s)", err, in.Name)
		}

		// if this is the identity attribute, then its row ID is what we need to return so item can be associated with it;
		// and the data source (DS) ID will be added to its entity_attributes row to indicate their identity on this DS
		var linkedDataSourceID *int64
		if attr.Identity {
			identityAttributeID = attrID
			linkedDataSourceID = &p.dsRowID
		}

		for _, entity := range entities {
			var eaID int64
			var existingDataSourceID *int64

			q := `SELECT id, data_source_id FROM entity_attributes WHERE entity_id=? AND attribute_id=?`
			args := []any{entity.ID, attrID}

			// if this is an identity attribute, we need to select the row that portrays this identity;
			// otherwise, we need to be sure to not care about the data source, otherwise we'll end up
			// with essentially duplicate rows, causing repeated results in some queries (consider case
			// where initial row insertion has data_source_id set because it's an identity; then future
			// import has same entity ID and attribute ID, but data source is null because it's not an
			// identity on this import's data source; we need to select whatever row may exist because
			// it's not an identity to us)
			if attr.Identity {
				q += " AND (data_source_id=? OR data_source_id IS NULL)"
				args = append(args, linkedDataSourceID)
			}
			q += " LIMIT 1"

			err = tx.QueryRowContext(ctx, q, args...).Scan(&eaID, &existingDataSourceID)
			noRows := errors.Is(err, sql.ErrNoRows)
			if err != nil && !noRows {
				return latentID{}, fmt.Errorf("checking for existing link of entity to attribute: %v (entity_id=%d attribute_id=%d)", err, entity.ID, attrID)
			}

			// TODO: I want to revise the autolink fields. I am not sure how useful they are presently. What actual information do we (want to) gain?
			var autolinkImportID, autolinkAttrIDPtr *int64
			if autolinkAttrID, ok := entityAutolinkAttrs[entity.ID]; ok {
				autolinkAttrIDPtr = &autolinkAttrID
				autolinkImportID = &p.impRow.id
			}

			if noRows {
				// the entity and attribute are not yet related in the DB; insert

				_, err = tx.ExecContext(ctx,
					`INSERT INTO entity_attributes
						(entity_id, attribute_id, data_source_id, import_id, autolink_import_id, autolink_attribute_id)
					VALUES (?, ?, ?, ?, ?, ?)`,
					entity.ID, attrID, linkedDataSourceID, p.impRow.id, autolinkImportID, autolinkAttrIDPtr)
				if err != nil {
					return latentID{}, fmt.Errorf("linking entity %d to attribute %d: %v (data_source_id=%#v import_id=%d autolink_import_id=%#v autolink_attribute_id=%#v)",
						entity.ID, attrID, err, linkedDataSourceID, p.impRow.id, autolinkImportID, autolinkAttrIDPtr)
				}
			} else if eaID > 0 && existingDataSourceID == nil {
				// the entity and attribute are already related in the DB but not as an ID on any data source; update

				_, err = tx.ExecContext(ctx,
					`UPDATE entity_attributes SET data_source_id=?, import_id=?, autolink_import_id=?, autolink_attribute_id=? WHERE id=?`, // TODO: LIMIT 1 would be nice...
					linkedDataSourceID, p.impRow.id, autolinkImportID, autolinkAttrIDPtr, eaID)
				if err != nil {
					return latentID{}, fmt.Errorf("updating entity %d link to to attribute %d: %v", entity.ID, attrID, err)
				}
			}
		}
	}

	return latentID{
		entityID:    entities[0].ID,
		attributeID: identityAttributeID,
	}, nil
}

func (p *processor) loadEntities(ctx context.Context, tx *sql.Tx, in *Entity) ([]Entity, map[int64]int64, error) {
	// iterate all the attributes to find all the entities we can identify who
	// we already have in the database if they're not already
	// (notice that we left join attributes; this is because an entity may only have
	// a row but no attributes, especially if it's the timeline owner (ID 1) because
	// they may not have filled out a profile)
	q := `SELECT
			entities.id, entities.name, entities.picture_file,
			ea.attribute_id
		FROM entities
		LEFT JOIN entity_attributes AS ea ON ea.entity_id = entities.id
		LEFT JOIN attributes ON attributes.id = ea.attribute_id
		WHERE `

	var whereGroups []string
	var args []any

	// in some cases, the entity ID may be specified manually; load that entity directly by ID instead of trying to find it
	if in.ID > 0 {
		whereGroups = append(whereGroups, "entities.id=?")
		args = append(args, in.ID)
	} else {
		for _, attr := range in.Attributes {
			// skip non-identifying attributes as those are not likely to be unique enough
			if attr.Identifying || attr.Identity {
				whereGroups = append(whereGroups, `(attributes.name=? AND attributes.value=?)`)
				args = append(args, attr.Name, attr.Value)
			}
		}
	}

	// only perform query if there are any qualifiers; otherwise this query returns
	// all rows which is obviously wrong; if there's no ID or identifying attributes,
	// there's no way for us to know who this entity is anyway
	var entities []Entity
	entityAutolinkAttrs := make(map[int64]int64) // map of entity ID to attribute ID that linked it
	if len(whereGroups) > 0 {
		q += strings.Join(whereGroups, " OR ")
		q += " GROUP BY entities.id"

		// run the query to get the entity/entities (note there may be multiple; e.g. if multiple people
		// share an account or the account has attributes spanning nultiple positively-ID'ed people)
		rows, err := tx.QueryContext(ctx, q, args...)
		if err != nil {
			return nil, nil, fmt.Errorf("querying entity attributes: %v", err)
		}
		defer rows.Close()

		for rows.Next() {
			var ent Entity
			var name *string
			var attrID *int64
			err := rows.Scan(&ent.ID, &name, &ent.Picture, &attrID)
			if err != nil {
				return nil, nil, fmt.Errorf("scanning entity ID: %v", err)
			}
			if name != nil {
				ent.Name = *name
			}
			if in.ID == 0 && attrID != nil {
				entityAutolinkAttrs[ent.ID] = *attrID
			}
			entities = append(entities, ent)
		}
		if err := rows.Err(); err != nil {
			return nil, nil, fmt.Errorf("iterating entity rows: %v", err)
		}
	}

	return entities, entityAutolinkAttrs, nil
}

func storeAttribute(ctx context.Context, tx *sql.Tx, attr Attribute) (int64, error) {
	// prepare some values for DB insertion
	var metadata *string
	if len(attr.Metadata) > 0 {
		metaBytes, err := json.Marshal(attr.Metadata)
		if err != nil {
			return 0, fmt.Errorf("encoding attribute metadata as JSON: %v", err)
		}
		metaString := string(metaBytes)
		metadata = &metaString
	}
	var altValue *string
	if strings.TrimSpace(attr.AltValue) != "" {
		altValue = &attr.AltValue
	}

	// store the attribute+value if we don't have this combo yet (if we do, no ID is returned, sigh)
	var attrID int64
	err := tx.QueryRowContext(ctx,
		`INSERT OR IGNORE INTO attributes (name, value, alt_value, metadata) VALUES (?, ?, ?, ?) RETURNING id`,
		attr.Name, attr.valueForDB(), altValue, metadata).Scan(&attrID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("storing attribute '%+v': %v", attr, err)
	}

	if attrID == 0 {
		// attribute already existed; query to get its ID so we can associate it with the person(s)
		// TODO: if this tx is slow, there may be room for optimization here; if we can already know
		// which persons are already associated with this attribute, we don't have to do this step?
		err = tx.QueryRowContext(ctx,
			`SELECT id FROM attributes WHERE name=? AND value=? LIMIT 1`,
			attr.Name, attr.valueForDB()).Scan(&attrID)
		if err != nil {
			return 0, fmt.Errorf("retrieving attribute ID for attribute %+v: %v", attr, err)
		}
	}

	return attrID, nil
}

func (tl *Timeline) MergeEntities(ctx context.Context, entityIDToKeep int64, entityIDsToMerge []int64) error {
	// input verification / sanity checks, as well as loading entity information
	if entityIDToKeep <= 0 {
		return fmt.Errorf("entity to keep must have an ID greater than 0")
	}
	var entitiesToMerge []Entity
	seen := make(map[int64]struct{}) //deduplication
	for i, id := range entityIDsToMerge {
		if id <= 0 {
			return fmt.Errorf("entities to merge must have IDs greater than 0 (%d)", id)
		}
		if entityIDToKeep == id {
			return fmt.Errorf("cannot merge entity into itself (%d)", id)
		}
		if _, ok := seen[id]; ok {
			return fmt.Errorf("entity to merge specified more than once (%d)", id)
		}
		seen[id] = struct{}{}
		if id == 1 {
			// TODO: always keep entity 1 for now, since that's the repo owner... until we figure out a better solution
			entityIDToKeep, entityIDsToMerge[i], id = id, entityIDToKeep, entityIDToKeep
		}

		// load all the entities before we get a lock on the DB to do the changes
		entMerge, err := tl.LoadEntity(id)
		if err != nil {
			return fmt.Errorf("loading entity to merge: %v", err)
		}
		entitiesToMerge = append(entitiesToMerge, entMerge)
	}
	entKeep, err := tl.LoadEntity(entityIDToKeep)
	if err != nil {
		return fmt.Errorf("loading entity to keep: %v", err)
	}

	// lock DB and start transaction
	tl.dbMu.Lock()
	defer tl.dbMu.Unlock()

	tx, err := tl.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for _, entMerge := range entitiesToMerge {
		// bring over any information on the entity to merge that's missing on the entity to keep
		if entMerge.Name != "" && entKeep.Name == "" {
			entKeep.Name = entMerge.Name
		}

		// combine metadata (add only missing fields)
		entKeep.Metadata.Merge(entMerge.Metadata, MetaMergeSkip)
		metadata, err := entKeep.metadataString()
		if err != nil {
			return err
		}

		// if they both have a profile picture, we'll need to delete the one from the entity being merged
		var oldPictureFile *string
		if entMerge.Picture != nil {
			if entKeep.Picture == nil {
				entKeep.Picture = entMerge.Picture
			} else {
				oldPictureFile = entMerge.Picture
			}
		}

		// update the entity to keep with any info that was transferred over from the entity to merge
		_, err = tx.ExecContext(ctx, `UPDATE entities SET name=?, picture_file=?, metadata=? WHERE id=?`,
			entKeep.Name, entKeep.Picture, metadata, entityIDToKeep)
		if err != nil {
			return fmt.Errorf("updating entity row: %v", err)
		}

		// replace entity IDs in the database
		if _, err := tx.ExecContext(ctx, `UPDATE entity_attributes SET entity_id=? WHERE entity_id=?`, entityIDToKeep, entMerge.ID); err != nil {
			return fmt.Errorf("replacing entity ID in entity_attributes: %v", err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE tagged SET entity_id=? WHERE entity_id=?`, entityIDToKeep, entMerge.ID); err != nil {
			return fmt.Errorf("replacing entity ID in tagged: %v", err)
		}

		// handle pass-through attribute for the entity being merged (start by seeing if there's one for the entity to keep)
		var passThruAttrIDKeep int64
		if err = tx.QueryRowContext(ctx, `SELECT id FROM attributes WHERE name=? AND value=? LIMIT 1`, passThruAttribute, entityIDToKeep).Scan(&passThruAttrIDKeep); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("selecting pass-thru attribute for entity to keep: %v", err)
		}
		if passThruAttrIDKeep == 0 {
			// a pass-thru attribute doesn't exist for the entity to keep, so we can safely
			// just update the pass-thru attribute (if any) for the one to merge to point to
			// the one to keep
			if _, err := tx.ExecContext(ctx, `UPDATE attributes SET value=? WHERE name=? AND value=?`, entityIDToKeep, passThruAttribute, entMerge.ID); err != nil {
				return fmt.Errorf("updating pass-thru attribute to point to entity to keep: %v", err)
			}
		} else {
			// this is a little more work: we can't just update an attribute because it would
			// violate uniqueness constraints; so update everything that points to the old
			// attribute to point to the new one instead
			var passThruAttrIDMerge int64
			if err = tx.QueryRowContext(ctx, `SELECT id FROM attributes WHERE name=? AND value=? LIMIT 1`, passThruAttribute, entMerge.ID).Scan(&passThruAttrIDMerge); err != nil && !errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("selecting pass-thru attribute for entity to merge: %v", err)
			}
			if passThruAttrIDMerge > 0 {
				if _, err := tx.ExecContext(ctx, `UPDATE items SET attribute_id=? WHERE attribute_id=?`, passThruAttrIDKeep, passThruAttrIDMerge); err != nil {
					return fmt.Errorf("updating attribute ID in items table: %v", err)
				}
				if _, err := tx.ExecContext(ctx, `UPDATE relationships SET from_attribute_id=? WHERE from_attribute_id=?`, passThruAttrIDKeep, passThruAttrIDMerge); err != nil {
					return fmt.Errorf("updating 'from' attribute ID in relationships table: %v", err)
				}
				if _, err := tx.ExecContext(ctx, `UPDATE relationships SET to_attribute_id=? WHERE to_attribute_id=?`, passThruAttrIDKeep, passThruAttrIDMerge); err != nil {
					return fmt.Errorf("updating 'to' attribute ID in relationships table: %v", err)
				}
				if _, err := tx.ExecContext(ctx, `DELETE FROM entity_attributes WHERE attribute_id=?`, passThruAttrIDMerge); err != nil {
					return fmt.Errorf("deleting row in entity_attributes table: %v (attribute_id=%d)", err, passThruAttrIDMerge)
				}
				if _, err := tx.ExecContext(ctx, `DELETE FROM attributes WHERE id=?`, passThruAttrIDMerge); err != nil {
					return fmt.Errorf("deleting pass-thru attribute for entity to merge: %v (attribute_id=%d)", err, passThruAttrIDMerge)
				}
			}
		}

		// clean up the entity to merge
		if _, err := tx.ExecContext(ctx, `DELETE FROM entities WHERE id=?`, entMerge.ID); err != nil {
			return fmt.Errorf("deleting from entities table: %v", err)
		}
		if oldPictureFile != nil {
			if err := os.Remove(filepath.Join(tl.repoDir, *oldPictureFile)); err != nil {
				return err
			}
		}
	}

	return tx.Commit()
}

// latentID is a type that holds either the ID of an item
// or an identity attribute for an entity. If an entity
// was processed without an explicit identity attribute,
// this struct will hold the row ID of the entity instead,
// and if the ID of the identity attribute is later needed,
// call identifyingAttributeID() to create a pass-thru
// entity and get a usable attribute ID linked to the
// entity. For items, the itemID field can be accessed directly.
type latentID struct {
	itemID, entityID, attributeID int64
}

// id returns either the item ID or the attribute ID, whichever is set.
func (l latentID) id() int64 {
	if l.itemID > 0 {
		return l.itemID
	}
	if l.attributeID > 0 {
		return l.attributeID
	}
	return 0
}

// identifyingAttributeID returns either the known row ID of the
// attribute identifying the entity, or it will create a pass-thru
// attribute for the entity and return the new row ID.
func (l *latentID) identifyingAttributeID(ctx context.Context, tx *sql.Tx) (int64, error) {
	if l.attributeID > 0 {
		return l.attributeID, nil
	}
	if l.entityID == 0 {
		return 0, fmt.Errorf("no attribute ID or entity ID set")
	}

	attrID, err := storeLinkBetweenEntityAndNonIDAttribute(ctx, tx, l.entityID, Attribute{
		Name:     passThruAttribute,
		Value:    strconv.FormatInt(l.entityID, 10),
		Identity: true,
	})
	if err != nil {
		return 0, err
	}
	l.attributeID = attrID

	return l.attributeID, nil
}

var multiSpaceRegex = regexp.MustCompile(`\s{2,}`)

// passThruAttribute is the name of the special attribute that is used to identify
// a specific entity by its row ID and no other features (attributes) in particular.
const passThruAttribute = "_entity"

const (
	AttributeEmail       = "email_address"
	AttributePhoneNumber = "phone_number"
	AttributeGender      = "gender"
)
