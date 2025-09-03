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

package iphone

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/timelinize/timelinize/datasources/imessage"
	"github.com/timelinize/timelinize/timeline"
	"go.uber.org/zap"
)

func (fimp *FileImporter) addressBook(ctx context.Context) error {
	addressBookFileID, err := fimp.findFileID(ctx, homeDomain, relativePathContactsDB)
	if err != nil {
		return err
	}

	db, err := sql.Open("sqlite3", fimp.fileIDToPath(addressBookFileID)+"?mode=ro")
	if err != nil {
		return fmt.Errorf("opening address book: %w", err)
	}
	defer db.Close()

	rows, err := db.QueryContext(ctx,
		`SELECT
			p.ROWID, p.First, p.Last, p.Middle, p.Organization, p.Note,
			p.Birthday, p.Nickname, p.Prefix, p.Suffix, p.CreationDate,
			mv.property, mvl.value, mv.value,
			mvek.value, mve.value
		FROM ABPerson AS p
		LEFT JOIN ABMultiValue AS mv ON mv.record_id = p.ROWID
		LEFT JOIN ABMultiValueEntry AS mve ON mve.parent_id = mv.UID
		LEFT JOIN ABMultiValueEntryKey AS mvek ON mvek.rowid = mve.key
		LEFT JOIN ABMultiValueLabel AS mvl ON mvl.rowid = mv.label
		ORDER BY p.ROWID ASC`)
	if err != nil {
		return fmt.Errorf("querying address book for contacts: %w", err)
	}
	defer rows.Close()

	// a single contact may be spread across multiple rows thanks to our LEFT JOINs;
	// preserve the info/state for the contact as we iterate each row
	var currentContactID int64
	var entity *timeline.Entity
	var fields map[string]string // usually for address components, but could have other stuff too
	var creationDate time.Time   // when the most-recently-processing contact was added to the book

	finalizeEntity := func() {
		var sb strings.Builder
		if v := fields["street"]; v != "" {
			sb.WriteString(v) // street often contains the full address anyway
		}
		if v := fields["city"]; v != "" {
			if sb.Len() > 0 {
				sb.WriteRune('\n')
			}
			sb.WriteString(v)
		}
		if v := fields["state"]; v != "" {
			if sb.Len() > 0 {
				sb.WriteString(", ")
			}
			sb.WriteString(v)
		}
		if v := fields["zip"]; v != "" {
			if sb.Len() > 0 {
				sb.WriteString(" ")
			}
			sb.WriteString(v)
		}
		if v := fields["country"]; v != "" {
			if sb.Len() > 0 {
				sb.WriteString(", ")
			}
			sb.WriteString(v)
		}
		entity.Attributes = append(entity.Attributes, timeline.Attribute{
			Name:  "address",
			Value: sb.String(),
		})

		g := &timeline.Graph{Entity: entity}
		if !creationDate.IsZero() {
			g.FromEntityWithValue(fimp.owner, timeline.Relation{Label: "saved_contact"}, strconv.FormatInt(creationDate.Unix(), 10))
		}

		fimp.opt.Pipeline <- g
	}

	for rows.Next() {
		creationDate = time.Time{} // reset from previous iteration

		var rowID, creationDateAppleSec *int64
		var first, last, middle, org, note, birthday, nick, prefix, suffix, mvValue, mvLabel, fieldName, fieldValue *string
		var mvProperty *int

		err := rows.Scan(&rowID, &first, &last, &middle, &org, &note, &birthday, &nick, &prefix, &suffix, &creationDateAppleSec, &mvProperty, &mvLabel, &mvValue, &fieldName, &fieldValue)
		if err != nil {
			return fmt.Errorf("scanning row: %w", err)
		}

		// I haven't seen this, but just to avoid a panic
		if rowID == nil {
			continue
		}

		// convert timestamps
		if creationDateAppleSec != nil && *creationDateAppleSec != 0 {
			creationDate = imessage.CocoaSecondsToTime(*creationDateAppleSec)
		}

		// start of new contact
		if *rowID != currentContactID {
			// finish previous one and process it
			if entity != nil {
				finalizeEntity()
			}

			// advance the row ID we're working on
			currentContactID = *rowID

			// reset for next contact and start filling it in
			// (any info from the ABPerson table)
			entity = new(timeline.Entity)
			entity.Metadata = make(timeline.Metadata)
			fields = make(map[string]string)

			// build the name from its components
			// (the processor remove extra spaces)
			var sb strings.Builder
			if prefix != nil {
				sb.WriteString(*prefix)
			}
			if first != nil {
				sb.WriteRune(' ')
				sb.WriteString(*first)
			}
			if middle != nil {
				sb.WriteRune(' ')
				sb.WriteString(*middle)
			}
			if last != nil {
				sb.WriteRune(' ')
				sb.WriteString(*last)
			}
			if suffix != nil {
				sb.WriteRune(' ')
				sb.WriteString(*suffix)
			}
			entity.Name = sb.String()

			if birthday != nil {
				birthdate, err := imessage.ParseCocoaDate(*birthday)
				if err == nil {
					entity.Attributes = append(entity.Attributes, timeline.Attribute{
						Name:  "birth_date",
						Value: birthdate,
					})
				} else {
					fimp.opt.Log.Error("parsing date", zap.Error(err))
				}
			}

			// these I'm putting as metadata because I don't see it likely
			// that items would be attributed to these properties
			if note != nil {
				entity.Metadata["Note"] = *note
			}
			if nick != nil {
				entity.Metadata["Nickname"] = *nick // maybe this could be an attribute, I dunno
			}

			if org != nil {
				entity.Attributes = append(entity.Attributes, timeline.Attribute{
					Name:  "organization",
					Value: *org,
				})
			}
		}

		// continuation (or still first row) of same contact; add any joined data

		if mvProperty != nil && mvValue != nil {
			switch *mvProperty {
			case propertyPhoneNumber:
				entity.Attributes = append(entity.Attributes, timeline.Attribute{
					Name:        timeline.AttributePhoneNumber,
					Value:       *mvValue,
					Identifying: true,
				})
			case propertyEmailAddress:
				entity.Attributes = append(entity.Attributes, timeline.Attribute{
					Name:        timeline.AttributeEmail,
					Value:       *mvValue,
					Identifying: true,
				})
			case propertyWebsite:
				entity.Attributes = append(entity.Attributes, timeline.Attribute{
					Name:  "url",
					Value: *mvValue,
				})
			}
		}

		// this usually contains street address; but can have other random components too like username or service
		if fieldName != nil && fieldValue != nil {
			switch strings.ToLower(*fieldName) {
			case "street", "city", "state", "zip", "country":
				fields[*fieldName] = *fieldValue
			}
		}
	}
	if err = rows.Err(); err != nil {
		return fmt.Errorf("scanning rows: %w", err)
	}

	// don't forget to process the last one too!
	if entity != nil {
		finalizeEntity()
	}

	return nil
}

const (
	propertyPhoneNumber  = 3
	propertyEmailAddress = 4
	propertyWebsite      = 22
)
