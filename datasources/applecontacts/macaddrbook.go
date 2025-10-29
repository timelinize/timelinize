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

package applecontacts

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/timelinize/timelinize/datasources/imessage"
	"github.com/timelinize/timelinize/timeline"
	"go.uber.org/zap"
)

func init() {
	err := timeline.RegisterDataSource(timeline.DataSource{
		Name:            "apple_contacts",
		Title:           "Apple Contacts",
		Icon:            "apple_contacts.png",
		NewFileImporter: func() timeline.FileImporter { return new(FileImporter) },
	})
	if err != nil {
		timeline.Log.Fatal("registering data source", zap.Error(err))
	}
}

// FileImporter can import from the Apple Contacts database.
type FileImporter struct{}

// Recognize returns whether this file or folder is supported.
func (FileImporter) Recognize(_ context.Context, dirEntry timeline.DirEntry, _ timeline.RecognizeParams) (timeline.Recognition, error) {
	// first see if the file is an AddressBook DB directly
	// fun fact, "abcddb" apparently means "Address Book CoreData Database"
	if dirEntry.Name() == addressBookFilename {
		return timeline.Recognition{Confidence: 1}, nil
	}
	if path.Ext(dirEntry.Name()) == ".abcddb" {
		return timeline.Recognition{Confidence: .85}, nil
	}

	// then see if the entry is a directory that contains addressbook data; the Contacts
	// app stores its data across multiple database files if there is more than one source
	// for the contacts (for example, another contact list connected from Google) and
	// there's also metadata files per-person (abcdp) or per-group (abcdg) it seems, though
	// I'm not sure what their purpose is
	var confidence float64
	if dirEntry.IsDir() && dirEntry.Name() == "AddressBook" {
		confidence += .1
	}
	if info, err := fs.Stat(dirEntry.FS, addressBookFilename); err == nil && !info.IsDir() {
		confidence += .7
	}
	if confidence > 0 {
		if info, err := fs.Stat(dirEntry.FS, "Metadata"); err == nil && info.IsDir() {
			confidence += .1
		}
		if info, err := fs.Stat(dirEntry.FS, "Sources"); err == nil && info.IsDir() {
			confidence += .1
		}
	}

	return timeline.Recognition{Confidence: confidence}, nil
}

// FileImport imports data from the given file or folder.
func (fimp *FileImporter) FileImport(ctx context.Context, dirEntry timeline.DirEntry, params timeline.ImportParams) error {
	// if the given dirEntry is an address book database
	if path.Ext(dirEntry.Name()) == addressBookFilename {
		return fimp.processAddressBook(ctx, dirEntry, params)
	}

	// otherwise, this must be an AddressBook folder; look for abcddb files.
	return fs.WalkDir(dirEntry.FS, dirEntry.Filename, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Name() == addressBookFilename {
			return fimp.processAddressBook(ctx, timeline.DirEntry{
				DirEntry: d,
				FS:       dirEntry.FS,
				FSRoot:   dirEntry.FSRoot,
				Filename: path,
			}, params)
		}
		return nil
	})
}
func (fimp *FileImporter) processAddressBook(ctx context.Context, dirEntry timeline.DirEntry, params timeline.ImportParams) error {
	db, err := sql.Open("sqlite3", dirEntry.FullPath()+"?mode=ro")
	if err != nil {
		return fmt.Errorf("opening AddressBook DB at %s: %w", dirEntry.FullPath(), err)
	}
	defer db.Close()

	// not all databases are the same, unfortunately (issue #153, toward the middle/end)
	// so we need to build the query so that only the columns that exist are selected
	// (mainly concerned with the ZABCDRECORD table)
	supportedRecordTableColumns := []string{
		"Z_PK",
		"ZFIRSTNAME",
		"ZMIDDLENAME",
		"ZMAIDENNAME",
		"ZLASTNAME",
		"ZBIRTHDAY",
		"ZBIRTHDAYYEARLESS",
		"ZBIRTHDAYYEAR",
		"ZTHUMBNAILIMAGEDATA",
	}
	recordTableColumns, err := imessage.GetColumnNames(ctx, db, "ZABCDRECORD")
	if err != nil {
		return fmt.Errorf("listing column names: %w", err)
	}

	var sb strings.Builder
	sb.WriteString("SELECT\n\t")
	var selectedCols []string //nolint:prealloc // linter false positive: we don't know how many we will be selecting

	for _, col := range supportedRecordTableColumns {
		if !slices.Contains(recordTableColumns, col) {
			params.Log.Warn("address book database does not have column in ZABCDRECORD table", zap.String("column", col))
			continue
		}
		if len(selectedCols) > 0 {
			sb.WriteString(", ")
		}
		if col == "ZBIRTHDAY" {
			// we cast the ZBIRTHDAY field because it is a timestamp type, which mattn/go-sqlite3 tries to
			// convert to time.Time, but here's the kicker: it's NOT a unix epoch timestamp! so the time
			// value is wrong -- we need to read it as an integer then create the time.Time ourselves
			sb.WriteString("cast(rec.ZBIRTHDAY AS INTEGER)")
		} else {
			sb.WriteString("rec.")
			sb.WriteString(col)
		}
		selectedCols = append(selectedCols, col)
	}

	// remainder of query
	sb.WriteString(`,
		phone.ZFULLNUMBER, email.ZADDRESS, web.ZURL,
		post.ZSTREET, post.ZCITY, post.ZSUBLOCALITY, post.ZSTATE,
		post.ZCOUNTRYNAME, post.ZCOUNTRYCODE, post.ZZIPCODE
	FROM ZABCDRECORD AS rec
	LEFT JOIN ZABCDPHONENUMBER AS phone ON phone.ZOWNER = rec.Z_PK
	LEFT JOIN ZABCDEMAILADDRESS AS email ON email.ZOWNER = rec.Z_PK
	LEFT JOIN ZABCDURLADDRESS AS web ON web.ZOWNER = rec.Z_PK
	LEFT JOIN ZABCDPOSTALADDRESS AS post ON post.ZOWNER = rec.Z_PK
	ORDER BY rec.Z_PK ASC`)

	rows, err := db.QueryContext(ctx, sb.String())
	if err != nil {
		return fmt.Errorf("querying contact records: %w", err)
	}
	defer rows.Close()

	var current contact
	for rows.Next() {
		var record contact
		var addr postalAddress
		var email, phone, webpage *string

		// because the select clause is dynamically generated, we also need to
		// dynamically generate the list of targets...
		const numJoinedColumns = 10
		targets := make([]any, 0, len(selectedCols)+numJoinedColumns)

		for _, col := range selectedCols {
			switch col {
			case "Z_PK":
				targets = append(targets, &record.id)
			case "ZFIRSTNAME":
				targets = append(targets, &record.name.firstName)
			case "ZMIDDLENAME":
				targets = append(targets, &record.name.midName)
			case "ZMAIDENNAME":
				targets = append(targets, &record.name.maidenName)
			case "ZLASTNAME":
				targets = append(targets, &record.name.lastName)
			case "ZBIRTHDAY":
				targets = append(targets, &record.birthday)
			case "ZBIRTHDAYYEARLESS":
				targets = append(targets, &record.birthdayYearless)
			case "ZBIRTHDAYYEAR":
				targets = append(targets, &record.birthdayYear)
			case "ZTHUMBNAILIMAGEDATA":
				targets = append(targets, &record.thumbnail)
			}
		}
		targets = append(targets,
			&phone, &email, &webpage,
			&addr.street, &addr.city, &addr.sublocal, &addr.state,
			&addr.country, &addr.countryCode, &addr.zip,
		)

		err := rows.Scan(targets...)
		if err != nil {
			return fmt.Errorf("scanning contact row: %w", err)
		}

		// if this row is a new record (contact), process the previous one we filled in and replace current
		if record.id != current.id {
			params.Pipeline <- &timeline.Graph{
				Entity: current.entity(),
			}
			current = record
		}

		if phone != nil {
			current.phones = append(current.phones, *phone)
		}
		if email != nil {
			current.emails = append(current.emails, *email)
		}
		if webpage != nil {
			current.webpages = append(current.webpages, *webpage)
		}
		if !addr.empty() {
			current.addresses = append(current.addresses, addr)
		}
	}
	if err = rows.Err(); err != nil {
		return fmt.Errorf("scanning rows: %w", err)
	}

	// make sure to save the last contact we were filling in
	params.Pipeline <- &timeline.Graph{
		Entity: current.entity(),
	}

	return nil
}

type contact struct {
	name      contactName
	thumbnail []byte

	id, birthdayYear *int64
	birthday         *float64 // cocoa core data timestamp (the "Apple epoch")
	birthdayYearless *float64 // number of seconds into the year of the birthday, UTC time

	phones    []string
	emails    []string
	webpages  []string
	addresses []postalAddress
}

func (c contact) entity() *timeline.Entity {
	var ent timeline.Entity

	ent.Name = c.name.String()

	// prefer full birth date if it seems valid; use yearless if that's what we have
	// (the "yearless" birthday is the number of seconds into the year)
	// (apparently, values of an extreme magnitude indicate the full birth date is
	// unknown, and we should use the yearless birthday instead)
	if c.birthday != nil && *c.birthday > -1e9 {
		ent.Attributes = append(ent.Attributes, timeline.Attribute{
			Name:  "birth_date",
			Value: imessage.CocoaSecondsToTime(int64(*c.birthday)),
		})
	} else if c.birthdayYearless != nil {
		var date time.Time

		// we may still be able to attach the year... maybe?
		// apparently Apple uses year 1604 as a sentinel value to
		// indicate "unknown" -- why they don't just leave it NULL
		// is beyond me (https://stackoverflow.com/a/14023536)
		// so we have to watch out for that one
		const sentinelYearMeaningUnknown = 1604
		if c.birthdayYear != nil &&
			*c.birthdayYear > 0 &&
			*c.birthdayYear != sentinelYearMeaningUnknown {
			date = time.Date(int(*c.birthdayYear), time.January, 1, 0, 0, 0, 0, time.UTC)
		}
		ent.Attributes = append(ent.Attributes, timeline.Attribute{
			Name:  "birth_date",
			Value: date.Add(time.Duration(*c.birthdayYearless) * time.Second),
		})
	}

	for _, phone := range c.phones {
		ent.Attributes = append(ent.Attributes, timeline.Attribute{
			Name:        timeline.AttributePhoneNumber,
			Value:       phone,
			Identifying: true,
		})
	}
	for _, email := range c.emails {
		ent.Attributes = append(ent.Attributes, timeline.Attribute{
			Name:        timeline.AttributeEmail, // TODO: should we be using the normalized form that the DB gives us ("ZADDRESSNORMALIZED" column)?
			Value:       email,
			Identifying: true,
		})
	}
	for _, webpage := range c.webpages {
		ent.Attributes = append(ent.Attributes, timeline.Attribute{
			Name:  "url",
			Value: webpage,
		})
	}
	for _, addr := range c.addresses {
		ent.Attributes = append(ent.Attributes, timeline.Attribute{
			Name:  "address",
			Value: addr.String(),
		})
	}

	// 38 bytes is the length of the string representation of a GUID, which we can't use (AFAIK...)
	if len(c.thumbnail) > 0 && len(c.thumbnail) != 38 {
		// For some reason, the Apple AddressBook ZTHUMBNAILIMAGEDATA column
		// prefixes jpeg images with a 01 byte. Maybe this is a version number
		// or a representation of the media type/format.
		ent.NewPicture = timeline.ByteData(c.thumbnail[1:])
	}

	return &ent
}

type postalAddress struct {
	street, city, sublocal, state, country, countryCode, zip *string
}

func (pa postalAddress) empty() bool {
	return pa.street != nil || pa.city != nil || pa.sublocal != nil || pa.state != nil || pa.country != nil || pa.zip != nil
}

func (pa postalAddress) String() string {
	return concat(", ", []*string{pa.street, pa.city, pa.sublocal, pa.state, pa.country, pa.zip})
}

type contactName struct {
	firstName, midName, maidenName, lastName *string
}

func (cn contactName) String() string {
	return concat(" ", []*string{cn.firstName, cn.midName, cn.maidenName, cn.lastName})
}

func concat(sep string, vals []*string) string {
	var sb strings.Builder
	for _, val := range vals {
		if val == nil {
			continue
		}
		if s := strings.TrimSpace(*val); s != "" {
			if sb.Len() > 0 {
				sb.WriteString(sep)
			}
			sb.WriteString(s)
		}
	}
	return sb.String()
}

// currently, we only support this version of the database, but we can add support for others later...
// macOS Mavericks and above, I think
const addressBookFilename = "AddressBook-v22.abcddb"
