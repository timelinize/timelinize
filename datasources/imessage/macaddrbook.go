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

package imessage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/timelinize/timelinize/timeline"
	"go.uber.org/zap"
)

func (fimp *FileImporter) processContacts(ctx context.Context, itemChan chan<- *timeline.Graph, opt timeline.ListingOptions) error {
	// macOS Mavericks and above, I think

	// TODO: maybe the user should be able to pass in address book databases directly too
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return err
	}

	const addrBookFilename = "AddressBook-v22.abcddb"

	addrBookDir := filepath.Join(homeDir, "Library", "Application Support", "AddressBook")
	mainBook := filepath.Join(addrBookDir, addrBookFilename)

	allBooks := []string{mainBook}

	bookSources, err := os.ReadDir(filepath.Join(addrBookDir, "Sources"))
	if err != nil {
		return err
	}
	for _, entry := range bookSources {
		if !entry.IsDir() {
			continue
		}
		allBooks = append(allBooks, filepath.Join(addrBookDir, "Sources", entry.Name(), addrBookFilename))
	}

	for _, bookPath := range allBooks {
		if err := fimp.processAddressBook(ctx, itemChan, opt, bookPath); err != nil {
			opt.Log.Error("could not process address book",
				zap.String("db_path", bookPath),
				zap.Error(err))
		}
	}

	return nil
}

func (*FileImporter) processAddressBook(ctx context.Context, itemChan chan<- *timeline.Graph, opt timeline.ListingOptions, bookPath string) error {
	db, err := sql.Open("sqlite3", bookPath+"?mode=ro")
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("opening AddressBook DB at %s: %v", bookPath, err)
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
			return fmt.Errorf("scanning contact row: %v", err)
		}

		// if this row is a new record (contact), process the previous one we filled in and replace current
		if record.id != current.id {
			itemChan <- &timeline.Graph{
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
		return fmt.Errorf("scanning rows: %v", err)
	}

	// make sure to save the last contact we were filling in
	itemChan <- &timeline.Graph{
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
