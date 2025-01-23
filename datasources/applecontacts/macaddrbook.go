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
	"strings"
	"time"

	"github.com/timelinize/timelinize/timeline"
	"go.uber.org/zap"
)

func init() {
	err := timeline.RegisterDataSource(timeline.DataSource{
		Name:            "applecontacts",
		Title:           "Apple Contacts",
		Icon:            "applecontacts.png",
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
	if path.Ext(dirEntry.Name()) == addressBookFilename {
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
	if info, err := fs.Stat(dirEntry.FS, "Metadata"); err == nil && info.IsDir() {
		confidence += .1
	}
	if info, err := fs.Stat(dirEntry.FS, "Sources"); err == nil && info.IsDir() {
		confidence += .1
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
	return fs.WalkDir(dirEntry.FS, ".", func(path string, d fs.DirEntry, err error) error {
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
func (*FileImporter) processAddressBook(ctx context.Context, dirEntry timeline.DirEntry, params timeline.ImportParams) error {
	db, err := sql.Open("sqlite3", dirEntry.FullPath()+"?mode=ro")
	if err != nil {
		return fmt.Errorf("opening AddressBook DB at %s: %w", dirEntry.FullPath(), err)
	}
	defer db.Close()

	rows, err := db.QueryContext(ctx, `
		SELECT
			rec.Z_PK, rec.ZFIRSTNAME, rec.ZMIDDLENAME, rec.ZMAIDENNAME, rec.ZLASTNAME,
			rec.ZBIRTHDAY, rec.ZBIRTHDAYYEARLESS, rec.ZBIRTHDAYYEAR, rec.ZTHUMBNAILIMAGEDATA,
			phone.ZFULLNUMBER, email.ZADDRESS, web.ZURL,
			post.ZSTREET, post.ZCITY, post.ZSUBLOCALITY, post.ZSTATE, post.ZCOUNTRYNAME, post.ZCOUNTRYCODE, post.ZZIPCODE
		FROM ZABCDRECORD AS rec
		LEFT JOIN ZABCDPHONENUMBER AS phone ON phone.ZOWNER = rec.Z_PK
		LEFT JOIN ZABCDEMAILADDRESS AS email ON email.ZOWNER = rec.Z_PK
		LEFT JOIN ZABCDURLADDRESS AS web ON web.ZOWNER = rec.Z_PK
		LEFT JOIN ZABCDPOSTALADDRESS AS post ON post.ZOWNER = rec.Z_PK
		ORDER BY rec.Z_PK ASC
	`)
	if err != nil {
		return err
	}
	defer rows.Close()

	var current contact
	for rows.Next() {
		var record contact
		var addr postalAddress
		var email, phone, webpage *string

		err := rows.Scan(
			&record.id, &record.name.firstName, &record.name.midName, &record.name.maidenName, &record.name.lastName,
			&record.birthday, &record.birthdayYearless, &record.birthdayYear, &record.thumbnail,
			&phone, &email, &webpage,
			&addr.street, &addr.city, &addr.sublocal, &addr.state, &addr.country, &addr.countryCode, &addr.zip)
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
	birthday         *time.Time
	birthdayYearless *float64

	phones    []string
	emails    []string
	webpages  []string
	addresses []postalAddress
}

func (c contact) entity() *timeline.Entity {
	var ent timeline.Entity

	ent.Name = c.name.String()

	if c.birthdayYear != nil && c.birthday != nil && *c.birthdayYear > 1604 {
		ent.Attributes = append(ent.Attributes, timeline.Attribute{
			Name:  "birth_date",
			Value: c.birthday,
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
