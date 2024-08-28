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

// Package vcard implements a data source for vCard files.
package vcard

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"path"
	"strings"
	"time"

	"github.com/mholt/archiver/v4"
	"github.com/signal-golang/go-vcard"
	"github.com/timelinize/timelinize/timeline"
	"go.uber.org/zap"
)

func init() {
	err := timeline.RegisterDataSource(timeline.DataSource{
		Name:            "vcard",
		Title:           "vCard",
		Icon:            "vcard.svg",
		NewFileImporter: func() timeline.FileImporter { return new(FileImporter) },
	})
	if err != nil {
		timeline.Log.Fatal("registering data source", zap.Error(err))
	}
}

// FileImporter can import the data from a file.
type FileImporter struct{}

func (FileImporter) Recognize(ctx context.Context, filenames []string) (timeline.Recognition, error) {
	var totalCount, matchCount int

	for _, filename := range filenames {
		fsys, err := archiver.FileSystem(ctx, filename)
		if err != nil {
			return timeline.Recognition{}, err
		}

		err = fs.WalkDir(fsys, ".", func(fpath string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if fpath == "." {
				fpath = path.Base(filename)
			}
			if strings.HasPrefix(path.Base(fpath), ".") {
				// skip hidden files; they are cruft
				if d.IsDir() {
					return fs.SkipDir
				} else {
					return nil
				}
			}
			if d.IsDir() {
				return nil // traverse into subdirectories
			}

			totalCount++

			switch path.Ext(strings.ToLower(fpath)) {
			case ".vcf", ".vcard":
			default:
				return nil // skip non-vcard files
			}

			file, err := fsys.Open(fpath)
			if err != nil {
				return err
			}
			defer file.Close()

			// read the first few bytes to see if it looks like a legit vcard; ignore empty or short files
			buf := make([]byte, len(beginVCard))
			_, err = io.ReadFull(file, buf)
			if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
				return err
			}
			if string(buf) == beginVCard {
				matchCount++
			}

			return nil
		})
		if err != nil {
			return timeline.Recognition{}, err
		}
	}

	if totalCount > 0 {
		return timeline.Recognition{Confidence: float64(matchCount) / float64(totalCount)}, nil
	}
	return timeline.Recognition{}, nil
}

// ParseBirthday parses bday in either "--MMDD" or "YYYYMMDD" format.
// If the former, the year is omitted, and as such, the Unix timestamp
// of the date will compute to be over 2000 years ago. If the date
// fails to parse, a nil time is returned.
// TODO: move this into the timeline package? maybe if --MMDD format is somewhat standard...
func ParseBirthday(bday string) *time.Time {
	if len(bday) == 6 && bday[:2] == "--" {
		monthDay, err := time.Parse("--0102", bday)
		if err == nil {
			return &monthDay
		}
	} else if len(bday) == 8 {
		fullDate, err := time.Parse("20060102", bday)
		if err == nil {
			return &fullDate
		}
	}
	return nil
}

func (imp *FileImporter) FileImport(ctx context.Context, filenames []string, itemChan chan<- *timeline.Graph, opt timeline.ListingOptions) error {
	for _, filename := range filenames {
		fsys, err := archiver.FileSystem(ctx, filename)
		if err != nil {
			return err
		}

		err = fs.WalkDir(fsys, ".", func(fpath string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if fpath == "." {
				fpath = path.Base(filename)
			}
			if strings.HasPrefix(path.Base(fpath), ".") {
				// skip hidden files; they are cruft
				if d.IsDir() {
					return fs.SkipDir
				} else {
					return nil
				}
			}
			if d.IsDir() {
				return nil // traverse into subdirectories
			}

			file, err := fsys.Open(fpath)
			if err != nil {
				return err
			}
			defer file.Close()

			dec := vcard.NewDecoder(file)
			for {
				card, err := dec.Decode()
				if err == io.EOF {
					break
				}
				if err != nil {
					return err
				}

				p := &timeline.Entity{
					Name: strings.Trim(card.PreferredValue(vcard.FieldFormattedName), nameCutset),
				}
				if p.Name == "" {
					p.Name = strings.Trim(card.PreferredValue(vcard.FieldName), nameCutset)
				}

				if rawBday := card.PreferredValue(vcard.FieldBirthday); rawBday != "" {
					p.Attributes = append(p.Attributes, timeline.Attribute{
						Name:  "birth_date",
						Value: ParseBirthday(rawBday),
					})
				}

				for _, phone := range card.Values(vcard.FieldTelephone) {
					p.Attributes = append(p.Attributes, timeline.Attribute{
						Name:        timeline.AttributePhoneNumber,
						Value:       phone,
						Identifying: true,
					})
				}

				for _, email := range card.Values(vcard.FieldEmail) {
					if email == p.Name {
						p.Name = "" // sometimes the email or phone number is also in the Name field for some reason (old Google Contacts)
					}
					p.Attributes = append(p.Attributes, timeline.Attribute{
						Name:        timeline.AttributeEmail,
						Value:       email,
						Identifying: true,
					})
				}

				if gender := card.PreferredValue(vcard.FieldGender); gender != "" {
					p.Attributes = append(p.Attributes, timeline.Attribute{
						Name:  timeline.AttributeGender,
						Value: gender,
					})
				}

				photoURL := card.PreferredValue(vcard.FieldPhoto)
				if photoURL == "" {
					photoURL = card.PreferredValue(vcard.FieldLogo)
				}
				if photoURL != "" {
					p.NewPicture = timeline.DownloadData(ctx, photoURL)
				}

				// the following fields are less common or useful, but still good to have if specified

				if nickname := card.PreferredValue(vcard.FieldNickname); nickname != "" {
					p.Attributes = append(p.Attributes, timeline.Attribute{
						Name:  "nickname",
						Value: nickname,
					})
				}

				for _, address := range card.Values(vcard.FieldAddress) {
					p.Attributes = append(p.Attributes, timeline.Attribute{
						Name:  "address",
						Value: address,
					})
				}

				for _, url := range card.Values(vcard.FieldURL) {
					p.Attributes = append(p.Attributes, timeline.Attribute{
						Name:  "url",
						Value: url,
					})
				}

				if anniversary := card.PreferredValue(vcard.FieldAnniversary); anniversary != "" {
					p.Attributes = append(p.Attributes, timeline.Attribute{
						Name:  "anniversary",
						Value: anniversary,
					})
				}

				for _, title := range card.Values(vcard.FieldTitle) {
					p.Attributes = append(p.Attributes, timeline.Attribute{
						Name:  "title",
						Value: title,
					})
				}

				for _, role := range card.Values(vcard.FieldRole) {
					p.Attributes = append(p.Attributes, timeline.Attribute{
						Name:  "role",
						Value: role,
					})
				}

				for _, note := range card.Values(vcard.FieldNote) {
					p.Attributes = append(p.Attributes, timeline.Attribute{
						Name:  "note",
						Value: note,
					})
				}

				// vCard extension: https://www.rfc-editor.org/rfc/rfc6474.html#section-2.1
				if birthPlace := card.PreferredValue("BIRTHPLACE"); birthPlace != "" {
					p.Attributes = append(p.Attributes, timeline.Attribute{
						Name:  "birth_place",
						Value: birthPlace,
					})
				}
				if rawDeathDate := card.PreferredValue("DEATHDATE"); rawDeathDate != "" {
					p.Attributes = append(p.Attributes, timeline.Attribute{
						Name:  "death_date",
						Value: ParseBirthday(rawDeathDate),
					})
				}
				if deathPlace := card.PreferredValue("DEATHPLACE"); deathPlace != "" {
					p.Attributes = append(p.Attributes, timeline.Attribute{
						Name:  "death_place",
						Value: deathPlace,
					})
				}

				// if we have at least some useful data for the entity, process it
				if p.Name != "" || len(p.Attributes) > 0 {
					itemChan <- &timeline.Graph{Entity: p}
				}
			}

			return nil
		})
		if err != nil {
			return err
		}
	}

	return nil
}

const nameCutset = "<\"“”'>"

const beginVCard = "BEGIN:VCARD"
