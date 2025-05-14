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
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"io/fs"
	"path"
	"strings"
	"sync"
	"time"

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

// Recognize returns whether the input is supported.
func (FileImporter) Recognize(_ context.Context, dirEntry timeline.DirEntry, _ timeline.RecognizeParams) (timeline.Recognition, error) {
	rec := timeline.Recognition{DirThreshold: .9}

	// we can import directories, but let the import planner figure that out; only recognize files
	if dirEntry.IsDir() {
		return rec, nil
	}

	// skip unsupported file types
	switch strings.ToLower(path.Ext(dirEntry.Name())) {
	case ".vcf", ".vcard":
	default:
		return rec, nil
	}

	file, err := dirEntry.Open(".")
	if err != nil {
		return rec, err
	}
	defer file.Close()

	buf := bufPool.Get().([]byte)
	//nolint:gofmt,gosimple,staticcheck
	defer bufPool.Put(buf[:len(buf)]) // ensure that even if buf is resized (it's not), we don't put back a larger buffer (good practice) -- WOW the linters hate this one

	// read the first few bytes to see if it looks like a legit vcard; ignore empty or short files
	_, err = io.ReadFull(file, buf)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return rec, err
	}
	if string(buf) == beginVCard {
		rec.Confidence = 1
	}

	return rec, nil
}

// FileImport imports data from the given file/folder.
func (imp *FileImporter) FileImport(ctx context.Context, dirEntry timeline.DirEntry, params timeline.ImportParams) error {
	err := fs.WalkDir(dirEntry.FS, dirEntry.Filename, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if strings.HasPrefix(d.Name(), ".") {
			// skip hidden files & folders
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil // traverse into subdirectories
		}

		file, err := dirEntry.Open(".")
		if err != nil {
			return err
		}
		defer file.Close()

		dec := vcard.NewDecoder(file)
		for {
			card, err := dec.Decode()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return err
			}

			p := &timeline.Entity{
				Name:     strings.Trim(card.PreferredValue(vcard.FieldFormattedName), nameCutset),
				Metadata: make(timeline.Metadata),
			}
			if p.Name == "" {
				if name := card.Name(); name != nil {
					formattedName := join(" ", []string{
						name.GivenName,
						name.AdditionalName,
						name.FamilyName,
					})
					p.Name = strings.Trim(formattedName, nameCutset)
					p.Metadata["Honorific prefix"] = name.HonorificPrefix
					p.Metadata["Honorific suffix"] = name.HonorificSuffix
				}
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

			// for a picture, see if there's a sidecar file that matches the name (this is the case for Google Takeout exports)
			if p.Name != "" {
				sidecarFilename := path.Join(path.Dir(dirEntry.Filename), p.Name+".jpg")
				if timeline.FileExistsFS(dirEntry.FS, sidecarFilename) {
					p.NewPicture = func(_ context.Context) (io.ReadCloser, error) {
						return dirEntry.FS.Open(sidecarFilename)
					}
				}
			}
			// otherwise, try downloading the picture from the vCard; I have seen URLs and base64,
			// but URLs are often dead, and the vcard package loses some base64 values, for some reason
			if p.NewPicture == nil {
				photo := card.PreferredValue(vcard.FieldPhoto)
				if photo == "" {
					photo = card.PreferredValue(vcard.FieldLogo)
				}
				if strings.HasPrefix(strings.ToLower(photo), "http") {
					p.NewPicture = timeline.DownloadData(photo)
				} else if photo != "" {
					// assume base64 encoding I guess; the parser kind of loses the "tail" information... but we have seen base64 photos
					p.NewPicture = func(_ context.Context) (io.ReadCloser, error) {
						photoBytes, err := base64.StdEncoding.DecodeString(photo)
						return io.NopCloser(bytes.NewReader(photoBytes)), err
					}
				}
			}

			// the following fields are less common or useful, but still good to have if specified

			if nickname := card.PreferredValue(vcard.FieldNickname); nickname != "" {
				p.Attributes = append(p.Attributes, timeline.Attribute{
					Name:  "nickname",
					Value: nickname,
				})
			}

			for _, addr := range card.Addresses() {
				// TODO: store components in metadata? or maybe use address parsing service later if needed
				p.Attributes = append(p.Attributes, timeline.Attribute{
					Name: "address",
					Value: join(" ", []string{
						addr.PostOfficeBox,
						addr.StreetAddress,
						addr.ExtendedAddress,
						addr.Locality,
						addr.Region,
						addr.PostalCode,
						addr.Country,
					}),
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
				params.Pipeline <- &timeline.Graph{Entity: p}
			}
		}

		return nil
	})
	if err != nil {
		return err
	}

	return nil
}

// ParseBirthday parses bday in either "--MMDD" or "YYYYMMDD" format.
// If the former, the year is omitted, and as such, the Unix timestamp
// of the date will compute to be over 2000 years ago. If the date
// fails to parse, a nil time is returned.
func ParseBirthday(bday string) *time.Time {
	const fullBdayFormat = "20060102"
	if len(bday) == 6 && bday[:2] == "--" {
		monthDay, err := time.Parse("--0102", bday)
		if err == nil {
			return &monthDay
		}
	} else if len(bday) == len(fullBdayFormat) {
		fullDate, err := time.Parse(fullBdayFormat, bday)
		if err == nil {
			return &fullDate
		}
	}
	return nil
}

// join is like strings.Join, but only joins non-empty elements,
// and trims spaces around individual elements.
func join(sep string, elems []string) string {
	var sb strings.Builder
	for _, elem := range elems {
		if strings.TrimSpace(elem) == "" {
			continue
		}
		if sb.Len() > 0 {
			sb.WriteString(sep)
		}
		sb.WriteString(elem)
	}
	return sb.String()
}

const nameCutset = "<\"“”'>"

const beginVCard = "BEGIN:VCARD"

var bufPool = sync.Pool{
	New: func() any {
		return make([]byte, len(beginVCard))
	},
}
