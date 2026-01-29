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

// Package contactlist implements a data source for contact lists.
package contactlist

import (
	"context"
	"encoding/base64"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"

	"github.com/timelinize/timelinize/datasources/vcard"
	"github.com/timelinize/timelinize/timeline"
	"go.uber.org/zap"
)

func init() {
	err := timeline.RegisterDataSource(timeline.DataSource{
		Name:            "contact_list",
		Title:           "Contact List",
		Icon:            "contact_list.svg",
		NewFileImporter: func() timeline.FileImporter { return new(FileImporter) },
	})
	if err != nil {
		timeline.Log.Fatal("registering data source", zap.Error(err))
	}
}

// FileImporter can import the data from a file.
type FileImporter struct{}

// FileImport imports data from a file.
func (fimp *FileImporter) FileImport(ctx context.Context, dirEntry timeline.DirEntry, params timeline.ImportParams) error {
	// start by assuming the dirEntry points directly to a CSV file
	pathInFS := "."

	// but if a directory was recognized, alter the path to refer to the CSV file within it
	if dirEntry.IsDir() {
		pathInFS = path.Base(dirEntry.Name()) + ".csv"
	}

	bestColumnMapping, bestDelim, err := bestColumnMappingAndDelim(ctx, dirEntry, ".")
	if err != nil {
		return err
	}

	// at least 2 fields should be required in order to be useful, right?
	// like an email by itself (or a name by itself) has no value I think...
	// especially since no items are attached from a contact list
	if len(bestColumnMapping) < recognizeAtLeastFields {
		return errors.New("insufficient header row")
	}

	file, err := dirEntry.Open(pathInFS)
	if err != nil {
		return fmt.Errorf("opening file: %w", err)
	}
	defer file.Close()

	r := csv.NewReader(file)
	r.ReuseRecord = true // with this enabled, DO NOT MODIFY THE SLICE RETURNED FROM Read()
	r.Comma = bestDelim

	var headerRow []string

	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		row, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("error reading next record: %w", err)
		}

		// header row
		if len(headerRow) == 0 {
			headerRow = make([]string, len(row))
			copy(headerRow, row)
			continue
		}

		// first, extract mappedValues from recognized columns in the row
		mappedValues := make(map[string][]string) // map of canonical field name -> associated value(s) from row
		for canonicalField, colIndices := range bestColumnMapping {
			for _, colIdx := range colIndices {
				mappedValues[canonicalField] = append(mappedValues[canonicalField], row[colIdx])
			}
		}

		// then, convert each field+values pair to something about the person
		p := new(timeline.Entity)

		var firstName, midName, lastName string

		for field, values := range mappedValues {
			for _, value := range values {
				value = strings.TrimSpace(value)
				if value == "" {
					// ignore empty values; especially if there are multiple matched columns
					// for a field (like Name, for some reason), don't overwrite a non-empty
					// first column with an empty second column
					continue
				}
				switch field {
				case "full_name":
					p.Name = value
				case "first_name":
					firstName = value
				case "middle_name":
					midName = value
				case "last_name":
					lastName = value
				case "birthdate":
					birthDate := vcard.ParseBirthday(value)
					if birthDate != nil {
						p.Attributes = append(p.Attributes, timeline.Attribute{
							Name:  "birth_date",
							Value: value,
						})
					}
				case "picture":
					if strings.HasPrefix(value, "http") {
						p.NewPicture = timeline.DownloadData(value)
					} else {
						picBytes, err := base64.RawStdEncoding.DecodeString(value)
						if err == nil {
							p.NewPicture = timeline.ByteData(picBytes)
						}
					}
				case timeline.AttributeGender:
					p.Attributes = append(p.Attributes, timeline.Attribute{
						Name:  field,
						Value: value,
					})
				case timeline.AttributeEmail,
					timeline.AttributePhoneNumber:
					p.Attributes = append(p.Attributes, timeline.Attribute{
						Name:        field,
						Value:       value,
						Identifying: true,
					})
				}
			}
		}

		// assemble name, if given in different fields
		if p.Name == "" {
			p.Name = firstName
			if midName != "" {
				if p.Name != "" {
					p.Name += " "
				}
				p.Name += midName
			}
			if lastName != "" {
				if p.Name != "" {
					p.Name += " "
				}
				p.Name += lastName
			}
		}

		// contact lists from Google Takeout have profile pictures as sidecar files,
		// named as a concatenation of their names, or their email address; we can read
		// those directly for much faster and more reliable imports, if this import is
		// acting on a directory rather than on a regular file
		if dirEntry.IsDir() {
			pfpPathByName := path.Join(dirEntry.Filename, p.Name) + ".jpg"
			if timeline.FileExistsFS(dirEntry.FS, pfpPathByName) {
				p.NewPicture = func(_ context.Context) (io.ReadCloser, error) {
					return dirEntry.FS.Open(pfpPathByName)
				}
			} else if attr, ok := p.Attribute(timeline.AttributeEmail); ok && attr.Value != nil {
				pfpPathByEmail := path.Join(dirEntry.Filename, attr.Value.(string)) + ".jpg"
				if timeline.FileExistsFS(dirEntry.FS, pfpPathByEmail) {
					p.NewPicture = func(_ context.Context) (io.ReadCloser, error) {
						return dirEntry.FS.Open(pfpPathByEmail)
					}
				}
			}
		}

		// I think it's pointless to process a person if there aren't at
		// least 2 data points about them because we can get single
		// data points from nearly any data source; the value of adding
		// a contact list is to get more information about a person to
		// infer more relationships automatically.
		if (p.NewPicture == nil && len(p.Metadata) == 0 && len(p.Attributes) == 0) || // only a name is kinda useless
			(p.Name == "" && p.NewPicture != nil && len(p.Attributes)+len(p.Metadata) == 0) {
			continue
		}

		// finally, send person for processing
		params.Pipeline <- &timeline.Graph{Entity: p}
	}

	return nil
}
