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
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/ttacon/libphonenumber"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// Entity represents a person. All these fields go into the persons table,
// except for UserID which goes into person_identities and which maps that
// user ID to the person being described by name and birth information.
// Birth information is almost always added manually by users, since
// data sources don't usually provide this, and is used to distinguish
// different persons of the same name, and is totally optional.
// TODO: update godoc
type Entity struct {
	// database row IDs
	ID     uint64  `json:"id,omitempty"`
	id     *uint64 // temporary holding place for DB value
	typeID uint64

	Type string  `json:"type,omitempty"`
	name *string // temporary holding place for DB value
	Name string  `json:"name,omitempty"`

	// To set a profile picture for this entity, set this field.
	NewPicture DataFunc `json:"-"`

	// Very optional extra information about this entity.
	Metadata Metadata `json:"metadata,omitempty"`

	Attributes []Attribute `json:"attributes,omitempty"`

	// THE FOLLOWING FIELDS ARE NOT TO BE SET BY DATA SOURCES.

	// Contains the path to the previously-set profile picture file
	// relative to the repo root.
	Picture *string `json:"picture,omitempty"`

	// Fields below are only for use with search or JSON serialization
	JobID    *int64     `json:"job_id,omitempty"`
	Stored   time.Time  `json:"stored,omitempty"`
	Modified *time.Time `json:"modified,omitempty"`
}

func (e Entity) String() string {
	return fmt.Sprintf("[id=%d type=%s name=%s picture=%v metadata=%v attributes=%v]",
		e.ID, e.Type, e.Name, e.Picture, e.Metadata, e.Attributes)
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

// Attribute gets the (first-)named attribute from the entity.
func (e Entity) Attribute(name string) (Attribute, bool) {
	for _, attr := range e.Attributes {
		if attr.Name == name {
			return attr, true
		}
	}
	return Attribute{}, false
}

// AttributeValue gets the value of the (first-)named attribute from the entity.
func (e Entity) AttributeValue(name string) any {
	for _, attr := range e.Attributes {
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
		e.Attributes[i] = normalizeAttribute(tl.ctx, e.Attributes[i])
	}

	// clean up name too; use regex to remove repeated spaces, then trim spaces on edges
	e.Name = strings.TrimSpace(multiSpaceRegex.ReplaceAllString(e.Name, " "))

	// oh, and ensure type is set (DB row requires entity type ID, not name)
	if e.Type == "" && e.typeID == 0 {
		e.Type = EntityPerson // assume a person, I guess -- data sources don't often distinguish
	}
	if e.typeID == 0 {
		typeID, err := tl.entityTypeNameToID(e.Type)
		if err != nil {
			return fmt.Errorf("getting entity type ID: %w (entity_type=%s)", err, e.Type)
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
		return nil, fmt.Errorf("encoding entity metadata as JSON: %w", err)
	}
	metaString := string(metaBytes)
	return &metaString, nil
}

const (
	EntityPerson   = "person"
	EntityCreature = "creature"
	EntityPlace    = "place"
)

// Attribute describes an entity.
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
	// At least lat/lon are required (for earth coordinates).
	Latitude  *float64 `json:"latitude,omitempty"`
	Longitude *float64 `json:"longitude,omitempty"`
	Altitude  *float64 `json:"altitude,omitempty"`

	// Optional metadata associated with this attribute.
	Metadata Metadata `json:"metadata,omitempty"`

	//////////////////////////////////////////////////////////////////////////
	// The fields below are NOT intended for use by data sources (importers).

	// NOT FOR USE BY DATA SOURCES.
	ID uint64 `json:"id,omitempty"`

	// NOT FOR USE BY DATA SOURCES. For search results only.
	ItemCount int64 `json:"item_count,omitempty"`

	// NOT FOR USE BY DATA SOURCES. For search results only.
	// This indicates which data source(s) the attribute identifies
	// an entity on.
	IdentityOn []string `json:"identity_on,omitempty"`
}

// Empty returns true if the name or value is empty or only whitespace.
func (a Attribute) Empty() bool {
	if strings.TrimSpace(a.Name) == "" {
		return true
	}
	return strings.TrimSpace(a.valueString()) == "" && a.Latitude == nil && a.Longitude == nil
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

func normalizeAttribute(ctx context.Context, attr Attribute) Attribute {
	attr.Name = strings.TrimSpace(attr.Name)

	if val, ok := attr.Value.(string); ok {
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

	if attr.Name == AttributePhoneNumber {
		// TODO: region could be stored in metadata now
		region, num, ok := strings.Cut(attr.Value.(string), ":")
		if !ok {
			num = region
			region = ""
		}
		stdPhoneNum, err := NormalizePhoneNumber(ctx, num, region)
		if err == nil {
			attr.Value = stdPhoneNum
		}
	}

	attr.Metadata.Clean()

	return attr
}

// NormalizePhoneNumber attempts to parse number and returns
// a standardized version in E164 format. If the number does
// not have an explicit region/country code, the country code
// for the region is used instead. If no country code is
// in the number, the country code from the system's locale
// region is assumed; otherwise the US region is assumed.
//
// We chose E164 because that's what Twilio uses.
func NormalizePhoneNumber(ctx context.Context, number, defaultRegion string) (string, error) {
	if defaultRegion == "" {
		if _, localeRegion, _, err := getSystemLocale(ctx); err == nil {
			defaultRegion = localeRegion
		} else {
			defaultRegion = "US"
		}
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
		return "", fmt.Errorf("opening profile picture reader: %w", err)
	}
	if r == nil {
		return "", nil // this could happen if the data source finds out there's no picture and no error
	}
	defer r.Close()

	buffered := bufio.NewReader(r)

	const bytesNeededToSniff = 512
	peekedBytes, err := buffered.Peek(bytesNeededToSniff)
	if err != nil {
		return "", fmt.Errorf("could not peek profile picture to determine type: %w", err)
	}

	contentType := http.DetectContentType(peekedBytes)

	// use "/" separators here; the fullpath() method will adjust for OS path separator
	// (we use the "%09d" formatter so file systems sort more conveniently, but it
	// also does not look like a date/time)
	pictureFile := path.Join(AssetsFolderName, "profile_pictures", fmt.Sprintf("entity_%09d", e.ID))
	disposition, _, _ := mime.ParseMediaType(contentType)
	switch disposition {
	case imagePNG:
		pictureFile += ".png"
	case ImageWebP:
		pictureFile += ".webp"
	case imageGif:
		pictureFile += ".gif"
	case ImageAVIF:
		pictureFile += ".avif"
	case ImageJPEG, "":
		fallthrough
	default:
		pictureFile += extJpg
	}
	fullPath := filepath.Clean(p.tl.FullPath(pictureFile))

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

// Exported consts may be used for thumbnails.
const (
	ImageWebP, extWebp         = "image/webp", ".webp"
	ImageJPEG, extJpg, extJpeg = "image/jpeg", ".jpg", ".jpeg"
	ImageAVIF, extAvif         = "image/avif", ".avif"
	imagePNG, extPng           = "image/png", ".png"
	imageGif, extGif           = "image/gif", ".gif"

	VideoWebM = "video/webm"
)

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
	entities, err := p.loadEntities(ctx, tx, &inCopy)
	if err != nil {
		return latentID{}, fmt.Errorf("searching entities by attributes: %w", err)
	}

	// then only if that didn't yield any results, try ID next
	if len(entities) == 0 && in.ID > 0 {
		entities, err = p.loadEntities(ctx, tx, &in)
		if err != nil {
			return latentID{}, fmt.Errorf("searching entities by ID: %w", err)
		}
	}

	// if our searches yielded 0 results, we can insert the entity; if 1, we can update it
	switch len(entities) {
	case 0:
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
				(type_id, job_id, name, metadata) VALUES (?, ?, ?, ?)
				RETURNING id`,
			in.typeID, p.ij.job.id, in.dbName(), metadata).Scan(&in.ID)
		if err != nil {
			return latentID{}, fmt.Errorf("adding new person %+v: %w", in, err)
		}

		// now that we have the row ID, save profile picture
		pictureFile, err := p.processEntityPicture(ctx, in)
		if err != nil {
			p.log.Error("saving new entity's profile picture",
				zap.Uint64("entity_id", in.ID),
				zap.Error(err))
		} else if pictureFile != "" {
			_, err = tx.ExecContext(ctx, `UPDATE entities SET picture_file=? WHERE id=?`, pictureFile, in.ID) // TODO: LIMIT 1, if ever implemented
			if err != nil {
				p.log.Error("updating new entity's profile picture in DB",
					zap.Uint64("entity_id", in.ID),
					zap.String("picture_file", pictureFile),
					zap.Error(err))
			}
		}

		entities = append(entities, in)

		atomic.AddInt64(p.ij.newEntityCount, 1)
	case 1:
		// if the identity attribute(s) matched exactly 1 entity, update its info in the DB
		// (if it matched more than 1 entity, we don't update multiple of them, because we only
		// get 1 entity at a time from data sources and we'd end up making the two look alike...
		// I can't think of a single data source that explicitly maps one of their accounts to
		// multiple entities and gives us that information... so we lose that granularity or
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
					zap.Uint64("entity_id", entity.ID),
					zap.Error(err))
			} else if pictureFile != "" {
				if setClause != "" {
					setClause += ", "
				}
				setClause += "picture_file=?"
				args = append(args, pictureFile)
				// notify the UI that a new profile picture can be displayed
				if entity.ID == ownerEntityID {
					p.log.Info("new owner picture", zap.String("picture_file", pictureFile))
				}
			}
		}
		setClause = strings.TrimSuffix(setClause, ", ")

		if len(args) > 0 {
			args = append(args, entity.ID)
			_, err := tx.ExecContext(ctx, `UPDATE entities SET `+setClause+` WHERE id=?`, args...) //nolint:gosec
			if err != nil {
				return latentID{}, fmt.Errorf("updating person %d (%+v): %w", entity.ID, in, err)
			}
		}

	default:
		// There's multiple entities with this attribute -- could either be
		// because there was not enough information before to combine them
		// together automatically, or they are genuinely separate entities
		// that share an attribute.
		//
		// Example of the former: two messaging datasets are imported, one
		// uses an old phone number for someone and the other uses the
		// person's newer phone number, but only the old one has the person's
		// name. Two entities are created since we have no way of knowing it
		// is the same person. Then later, a contact list is imported which
		// has both phone numbers in a single entity. In that case, the
		// incoming entity will match both entities in the database because
		// it looks up with both phone number attributes. Their names are
		// compatible since one is non-empty, and the other is empty; it
		// should be no harm to merge the two.
		//
		// Example of the latter: a husband and wife share an email address,
		// like JoeAndJane@example.com. Obviously, these entities should
		// not be combined, as one is named Jane and the other is named
		// Joe! But if one of the entities is essentially empty (unnamed),
		// there's no harm in combining since that existing empty entity
		// could be either of them.
		//
		// Anyway, it's generally not safe to always merge duplicate entities,
		// but it should be okay as long as their names are "compatible."
		// (If it WAS always safe to do so, entities could never share
		// identifying attributes, and we would have a different, less
		// powerful schema design.
		entityName, compatible := entityNamesCompatible(entities)
		if compatible {
			// not really sure which one to keep; but it doesn't really matter,
			// any information missing on the one to keep that exists on the
			// others (name, profile picture, etc.) will cascade over to it
			entityToKeep := entities[0]
			mergeEntities := entities[1:]

			if err := p.tl.mergeEntities(ctx, tx, entityToKeep, mergeEntities); err != nil {
				return latentID{}, fmt.Errorf("failed merging entities: %w", err)
			}
			if checkedLog := p.log.Check(zapcore.InfoLevel, "merged entities based on new information"); checkedLog != nil {
				entityIDs := make([]uint64, 0, len(mergeEntities))
				for _, ent := range mergeEntities {
					entityIDs = append(entityIDs, ent.ID)
				}
				checkedLog.Write(
					zap.String("unified_entity_name", entityName),
					zap.Uint64("kept_entity_id", entityToKeep.ID),
					zap.Uint64s("merged_entity_ids", entityIDs),
				)
			}
		}
	}

	// now store each attribute+value/coords combo if we don't have it yet,
	// and link it to the entity
	var identityAttributeID uint64
	for _, attr := range in.Attributes {
		attrID, err := storeAttribute(ctx, tx, attr)
		if err != nil {
			return latentID{}, fmt.Errorf("%w (entity_name=%s)", err, in.Name)
		}

		// if this is the identity attribute, then its row ID is what we need to return so item can be associated with it;
		// and the data source (DS) ID will be added to its entity_attributes row to indicate their identity on this DS
		var linkedDataSourceID *uint64
		if attr.Identity {
			identityAttributeID = attrID
			linkedDataSourceID = &p.dsRowID
		}

		for _, entity := range entities {
			var eaID uint64
			var existingDataSourceID *uint64

			q := `SELECT id, data_source_id FROM entity_attributes WHERE entity_id=? AND attribute_id=?`
			args := []any{entity.ID, attrID}

			// If this is an identity attribute, we need to select the row that portrays this identity
			// if there is one, or select the existing row that doesn't yet portray / know about this
			// identity... this is because an entity can have the same attribute be their identity on
			// multiple data sources (like phone number, or email address, are often used by multiple
			// data sources), and we want all of them to be represented. This can lead to duplicate
			// results in search queries, though, unless they use proper GROUP BY clauses to deduplicate
			// on the row ID of what is being searched. For example, search items should GROUP BY item
			// row ID and relationship row ID to avoid duplicate "sent to" relationships and duplicate
			// items in the search results.
			if attr.Identity {
				q += " AND (data_source_id=? OR data_source_id IS NULL)"
				args = append(args, linkedDataSourceID)
			}
			q += " LIMIT 1"

			err = tx.QueryRowContext(ctx, q, args...).Scan(&eaID, &existingDataSourceID)
			noRows := errors.Is(err, sql.ErrNoRows)
			if err != nil && !noRows {
				return latentID{}, fmt.Errorf("checking for existing link of entity to attribute: %w (entity_id=%d attribute_id=%d)", err, entity.ID, attrID)
			}

			if noRows {
				// the entity and attribute are not yet related in the DB; insert

				_, err = tx.ExecContext(ctx,
					`INSERT INTO entity_attributes
						(entity_id, attribute_id, data_source_id, job_id)
					VALUES (?, ?, ?, ?)`,
					entity.ID, attrID, linkedDataSourceID, p.ij.job.id)
				if err != nil {
					return latentID{}, fmt.Errorf("linking entity %d to attribute %d: %w (data_source_id=%#v job_id=%d)",
						entity.ID, attrID, err, linkedDataSourceID, p.ij.job.id)
				}
			} else if eaID > 0 && existingDataSourceID == nil {
				// the entity and attribute are already related in the DB but not as an ID on any data source; update

				_, err = tx.ExecContext(ctx,
					`UPDATE entity_attributes SET data_source_id=?, job_id=? WHERE id=?`, // TODO: LIMIT 1 would be nice...
					linkedDataSourceID, p.ij.job.id, eaID)
				if err != nil {
					return latentID{}, fmt.Errorf("updating entity %d link to attribute %d: %w", entity.ID, attrID, err)
				}
			}
		}
	}

	return latentID{
		entityID:    entities[0].ID,
		attributeID: identityAttributeID,
	}, nil
}

func (p *processor) loadEntities(ctx context.Context, tx *sql.Tx, in *Entity) ([]Entity, error) {
	var sb strings.Builder
	var args []any

	// we use DISTINCT here to avoid duplicate rows in the output,
	// since multiple attributes could match the same entity, for
	// example -- note that UNION which we perform when there are
	// multiple entities also de-duplicates, and we don't need
	// DISTINCT in that case; but to avoid complexity with building
	// the query, I tested query plans that use both DISTINCT and
	// UNION, and found that they were identical (the DB isn't
	// doing extra work when both DISTINCT and UNION are used).
	const selectClause = `
SELECT DISTINCT
	entities.id,
	entities.name,
	entities.picture_file,
	entities.metadata
FROM entities
`

	sb.WriteString(selectClause)

	if in.ID > 0 {
		// in some cases, the entity ID may be specified manually; load that entity directly by ID instead of trying to find it
		sb.WriteString("WHERE entities.id=? AND deleted IS NULL")
		args = append(args, in.ID)
	} else {
		if in.Type == EntityPlace {
			// place entities can be retrieved by name
			sb.WriteString("JOIN entity_types ON entity_types.id = entities.type_id\n")
			sb.WriteString("WHERE entity_types.name=? AND entities.name=?")
			args = append(args, EntityPlace, in.Name)
		}

		for _, attr := range in.Attributes {
			// skip non-identifying attributes as those are not likely to be unique enough
			if !attr.Identifying && !attr.Identity {
				continue
			}

			if len(args) > 0 {
				sb.WriteString("\n\nUNION\n\n-- match by attribute ")
				sb.WriteString(attr.Name)
				sb.WriteString(selectClause)
			}
			sb.WriteString(`JOIN entity_attributes AS ea ON ea.entity_id = entities.id
JOIN attributes ON attributes.id = ea.attribute_id
WHERE deleted IS NULL AND attributes.name=? AND (`)
			args = append(args, attr.Name)

			const decPrecisionAt, decPrecisionNear = 4, 3
			lowLonAt, highLonAt, lonDecAt := latLonBounds(attr.Longitude, decPrecisionAt)
			lowLatAt, highLatAt, latDecAt := latLonBounds(attr.Latitude, decPrecisionAt)
			lowLonNear, highLonNear, lonDecNear := latLonBounds(attr.Longitude, decPrecisionNear)
			lowLatNear, highLatNear, latDecNear := latLonBounds(attr.Latitude, decPrecisionNear)

			if attr.Value != nil {
				sb.WriteString("attributes.value=?")
				args = append(args, attr.valueForDB())
			}
			if attr.Latitude != nil && attr.Longitude != nil {
				if attr.Value != nil {
					sb.WriteString(" OR ")
				}
				sb.WriteString(`
(
	-- entity name is different, but coordinate is really close to same spot as an existing attribute
	entities.name!=?
	AND round(attributes.longitude, ?) BETWEEN ? AND ?
	AND round(attributes.latitude, ?) BETWEEN ? AND ?
)
OR (
	-- entity name is the same, and coordinate is somewhat close to same spot as an existing attribute
	entities.name=?
	AND round(attributes.longitude, ?) BETWEEN ? AND ?
	AND round(attributes.latitude, ?) BETWEEN ? AND ?
)`)
				args = append(args,
					in.Name,
					lonDecAt, lowLonAt, highLonAt,
					latDecAt, lowLatAt, highLatAt,
					in.Name,
					lonDecNear, lowLonNear, highLonNear,
					latDecNear, lowLatNear, highLatNear)
			}
			sb.WriteString(")")
		}
	}

	var entities []Entity

	// only perform query if there are any qualifiers; otherwise this query returns
	// all rows which is obviously wrong; if there's no ID or identifying attributes,
	// there's no way for us to know who this entity is anyway
	if len(args) > 0 {
		q := sb.String()

		// run the query to get the entity/entities (note there may be multiple; e.g. if multiple people
		// share an account or the account has attributes spanning nultiple positively-ID'ed people)
		rows, err := tx.QueryContext(ctx, q, args...)
		if err != nil {
			return nil, fmt.Errorf("querying entity attributes: %w", err)
		}
		defer rows.Close()

		for rows.Next() {
			var ent Entity
			var name *string
			var metadata *string
			err := rows.Scan(&ent.ID, &name, &ent.Picture, &metadata)
			if err != nil {
				return nil, fmt.Errorf("scanning entity ID: %w", err)
			}
			if name != nil {
				ent.Name = *name
			}
			if metadata != nil {
				err := json.Unmarshal([]byte(*metadata), &ent.Metadata)
				if err != nil {
					return nil, fmt.Errorf("unmarshaling entity metadata: %w", err)
				}
			}
			entities = append(entities, ent)
		}
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("iterating entity rows: %w", err)
		}
	}

	return entities, nil
}

func storeAttribute(ctx context.Context, tx *sql.Tx, attr Attribute) (uint64, error) {
	// prepare some values for DB insertion
	var metadata *string
	if len(attr.Metadata) > 0 {
		metaBytes, err := json.Marshal(attr.Metadata)
		if err != nil {
			return 0, fmt.Errorf("encoding attribute metadata as JSON: %w", err)
		}
		metaString := string(metaBytes)
		metadata = &metaString
	}
	var altValue *string
	if strings.TrimSpace(attr.AltValue) != "" {
		altValue = &attr.AltValue
	}

	const decPrecision = 3
	lowLon, highLon, lonDec := latLonBounds(attr.Longitude, decPrecision)
	lowLat, highLat, latDec := latLonBounds(attr.Latitude, decPrecision)

	var attrID uint64
	err := tx.QueryRowContext(ctx, `
		SELECT id
		FROM attributes
		WHERE name=? AND (
			value=?
			OR (
				round(longitude, ?) BETWEEN ? AND ?
				AND round(latitude, ?) BETWEEN ? AND ?
			)
		)
		LIMIT 1`, attr.Name, attr.valueForDB(),
		lonDec, lowLon, highLon,
		latDec, lowLat, highLat).Scan(&attrID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("looking up attribute '%+v': %w", attr, err)
	}

	if attrID == 0 {
		// store the attribute+value/coords since we don't have this combo yet
		err := tx.QueryRowContext(ctx, `INSERT INTO attributes
			(name, value, alt_value, longitude, latitude, altitude, metadata)
			VALUES (?, ?, ?, ?, ?, ?, ?)
			RETURNING id`,
			attr.Name, attr.valueForDB(), altValue,
			attr.Longitude, attr.Latitude, attr.Altitude, metadata).Scan(&attrID)
		if err != nil {
			return 0, fmt.Errorf("storing attribute '%+v': %w", attr, err)
		}
	}

	return attrID, nil
}

// MergeEntities combines the entities to merge into the entity to keep.
func (tl *Timeline) MergeEntities(ctx context.Context, entityIDToKeep uint64, entityIDsToMerge []uint64) error {
	// input verification / sanity checks, as well as loading entity information
	if entityIDToKeep <= 0 {
		return errors.New("entity to keep must have an ID greater than 0")
	}
	entitiesToMerge := make([]Entity, 0, len(entityIDsToMerge))
	seen := make(map[uint64]struct{}) // deduplication
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
		if id == ownerEntityID {
			// TODO: always keep entity 1 for now, since that's the repo owner... until we figure out a better solution
			entityIDToKeep, entityIDsToMerge[i], id = id, entityIDToKeep, entityIDToKeep
		}

		// load all the entities before we get a lock on the DB to do the changes
		entMerge, err := tl.LoadEntity(id)
		if err != nil {
			return fmt.Errorf("loading entity to merge: %w", err)
		}
		entitiesToMerge = append(entitiesToMerge, entMerge)
	}
	entKeep, err := tl.LoadEntity(entityIDToKeep)
	if err != nil {
		return fmt.Errorf("loading entity to keep: %w", err)
	}

	tx, err := tl.db.WritePool.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if err = tl.mergeEntities(ctx, tx, entKeep, entitiesToMerge); err != nil {
		return fmt.Errorf("merging entities: %w", err)
	}

	return tx.Commit()
}

func (tl *Timeline) mergeEntities(ctx context.Context, tx *sql.Tx, entKeep Entity, entitiesToMerge []Entity) error {
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
			entKeep.Name, entKeep.Picture, metadata, entKeep.ID)
		if err != nil {
			return fmt.Errorf("updating entity row: %w", err)
		}

		// replace entity IDs in the entity_attributes table, but if a row update will cause a
		// duplicate on entity_id, attribute_id, and data_source_id fields, delete the row instead
		if _, err := tx.ExecContext(ctx, `
			DELETE FROM entity_attributes AS ea
				WHERE entity_id=?
				AND EXISTS (
					SELECT 1
					FROM entity_attributes AS ea_old
					WHERE ea_old.entity_id=?
						AND ea_old.attribute_id = ea.attribute_id
						AND ea_old.data_source_id IS ea.data_source_id
				)
		`, entKeep.ID, entMerge.ID); err != nil {
			return fmt.Errorf("deleting rows that would violate unique constraints after imminent update in entity_attributes: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE entity_attributes SET entity_id=? WHERE entity_id=?`, entKeep.ID, entMerge.ID); err != nil {
			return fmt.Errorf("replacing entity ID in entity_attributes: %w", err)
		}

		// replace entity IDs elsewhere in the database (TODO: we might need to employ similar deletion logic above since we could cause duplicate rows, but these tables are manually populated so it's less likely to make duplicates here)
		if _, err := tx.ExecContext(ctx, `UPDATE tagged SET entity_id=? WHERE entity_id=?`, entKeep.ID, entMerge.ID); err != nil {
			return fmt.Errorf("replacing entity ID in tagged: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE story_elements SET entity_id=? WHERE entity_id=?`, entKeep.ID, entMerge.ID); err != nil {
			return fmt.Errorf("replacing entity ID in story_elements: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE notes SET entity_id=? WHERE entity_id=?`, entKeep.ID, entMerge.ID); err != nil {
			return fmt.Errorf("replacing entity ID in notes: %w", err)
		}

		// handle pass-through attribute for the entity being merged (start by seeing if there's one for the entity to keep)
		var passThruAttrIDKeep int64
		if err = tx.QueryRowContext(ctx, `SELECT id FROM attributes WHERE name=? AND value=? LIMIT 1`, passThruAttribute, entKeep.ID).Scan(&passThruAttrIDKeep); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("selecting pass-thru attribute for entity to keep: %w", err)
		}
		if passThruAttrIDKeep == 0 {
			// a pass-thru attribute doesn't exist for the entity to keep, so we can safely
			// just update the pass-thru attribute (if any) for the one to merge to point to
			// the one to keep
			if _, err := tx.ExecContext(ctx, `UPDATE attributes SET value=? WHERE name=? AND value=?`, entKeep.ID, passThruAttribute, entMerge.ID); err != nil {
				return fmt.Errorf("updating pass-thru attribute to point to entity to keep: %w", err)
			}
		} else {
			// this is a little more work: we can't just update an attribute because it would
			// violate uniqueness constraints; so update everything that points to the old
			// attribute to point to the new one instead
			var passThruAttrIDMerge int64
			if err = tx.QueryRowContext(ctx, `SELECT id FROM attributes WHERE name=? AND value=? LIMIT 1`, passThruAttribute, entMerge.ID).Scan(&passThruAttrIDMerge); err != nil && !errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("selecting pass-thru attribute for entity to merge: %w", err)
			}
			if passThruAttrIDMerge > 0 {
				if _, err := tx.ExecContext(ctx, `UPDATE items SET attribute_id=? WHERE attribute_id=?`, passThruAttrIDKeep, passThruAttrIDMerge); err != nil {
					return fmt.Errorf("updating attribute ID in items table: %w", err)
				}
				if _, err := tx.ExecContext(ctx, `UPDATE relationships SET from_attribute_id=? WHERE from_attribute_id=?`, passThruAttrIDKeep, passThruAttrIDMerge); err != nil {
					return fmt.Errorf("updating 'from' attribute ID in relationships table: %w", err)
				}
				if _, err := tx.ExecContext(ctx, `UPDATE relationships SET to_attribute_id=? WHERE to_attribute_id=?`, passThruAttrIDKeep, passThruAttrIDMerge); err != nil {
					return fmt.Errorf("updating 'to' attribute ID in relationships table: %w", err)
				}
				if _, err := tx.ExecContext(ctx, `DELETE FROM entity_attributes WHERE attribute_id=?`, passThruAttrIDMerge); err != nil {
					return fmt.Errorf("deleting row in entity_attributes table: %w (attribute_id=%d)", err, passThruAttrIDMerge)
				}
				if _, err := tx.ExecContext(ctx, `DELETE FROM attributes WHERE id=?`, passThruAttrIDMerge); err != nil {
					return fmt.Errorf("deleting pass-thru attribute for entity to merge: %w (attribute_id=%d)", err, passThruAttrIDMerge)
				}
			}
		}

		// clean up the entity to merge
		if _, err := tx.ExecContext(ctx, `DELETE FROM entities WHERE id=?`, entMerge.ID); err != nil {
			return fmt.Errorf("deleting from entities table: %w", err)
		}
		if oldPictureFile != nil {
			if err := tl.deleteRepoFile(*oldPictureFile); err != nil {
				return err
			}
		}
	}

	return nil
}

// entityNamesCompatible returns the singular name of these entities and true
// if they are compatible, meaning that all the entities with a non-empty Name
// have the same name.
func entityNamesCompatible(entities []Entity) (string, bool) {
	var entityName string
	for _, e := range entities {
		if e.Name == "" {
			continue
		}
		if entityName == "" {
			entityName = e.Name
		} else if !strings.EqualFold(e.Name, entityName) {
			return "", false
		}
	}
	return entityName, true
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
	itemID, entityID, attributeID uint64
}

// id returns either the item ID or the attribute ID, whichever is set.
func (l latentID) id() uint64 {
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
func (l *latentID) identifyingAttributeID(ctx context.Context, tx *sql.Tx) (uint64, error) {
	if l.attributeID > 0 {
		return l.attributeID, nil
	}
	if l.entityID == 0 {
		return 0, errors.New("no attribute ID or entity ID set")
	}

	attrID, err := storeLinkBetweenEntityAndNonIDAttribute(ctx, tx, l.entityID, Attribute{
		Name:     passThruAttribute,
		Value:    strconv.FormatUint(l.entityID, 10),
		Identity: true,
	})
	if err != nil {
		return 0, err
	}
	l.attributeID = attrID

	return l.attributeID, nil
}

// getSystemLocale returns the language, region, and encoding, if available.
func getSystemLocale(ctx context.Context) (language, region, encoding string, err error) {
	// Example of LANG: en_US.UTF-8
	if lang := os.Getenv("LANG"); lang != "" {
		underscorePos := strings.Index(lang, "_")
		if underscorePos < 0 {
			err = fmt.Errorf("no underscore divider between language and region in LANG=%s", lang)
			return
		}
		dotPos := strings.Index(lang, ".")
		if dotPos < 0 {
			err = fmt.Errorf("no dot divider between language and region in LANG=%s", lang)
		}
		language = lang[:underscorePos]
		region = lang[underscorePos+1 : dotPos]
		encoding = lang[dotPos+1:]
		return
	}
	if runtime.GOOS == "windows" {
		cmd := exec.CommandContext(ctx, "powershell", "Get-Culture | select -exp Name")
		var output []byte
		output, err = cmd.Output() // e.g. "en-US"
		if err != nil {
			return
		}
		parts := strings.Split(strings.TrimSpace(string(output)), "-")
		if len(parts) == 2 { //nolint:mnd
			language = parts[0]
			region = parts[1]
		}
		return
	}
	err = errors.New("unable to determine locale because LANG var is unset and/or platform unrecognized")
	return
}

var multiSpaceRegex = regexp.MustCompile(`\s{2,}`)

// passThruAttribute is the name of the special attribute that is used to identify
// a specific entity by its row ID and no other features (attributes) in particular.
const passThruAttribute = "_entity"

// Canonical attribute names.
const (
	AttributeEmail       = "email_address"
	AttributePhoneNumber = "phone_number"
	AttributeGender      = "gender"
)
