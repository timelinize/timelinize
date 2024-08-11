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

package contactlist

import (
	"context"
	"encoding/base64"
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/timelinize/timelinize/datasources/vcard"
	"github.com/timelinize/timelinize/timeline"
	"go.uber.org/zap"
)

func init() {
	err := timeline.RegisterDataSource(timeline.DataSource{
		Name:            "contactlist",
		Title:           "Contact List",
		Icon:            "contactlist.svg",
		NewFileImporter: func() timeline.FileImporter { return new(FileImporter) },
	})
	if err != nil {
		timeline.Log.Fatal("registering data source", zap.Error(err))
	}
}

// FileImporter can import the data from a file.
type FileImporter struct{}

func (imp *FileImporter) FileImport(ctx context.Context, filenames []string, itemChan chan<- *timeline.Graph, opt timeline.ListingOptions) error {
	for _, filename := range filenames {
		if err := imp.importFile(ctx, filename, itemChan, opt); err != nil {
			return err
		}
	}
	return nil
}

func (imp *FileImporter) importFile(ctx context.Context, filename string, itemChan chan<- *timeline.Graph, opt timeline.ListingOptions) error {
	file, err := os.Open(filename)
	if err != nil {
		return err
	}
	defer file.Close()

	r := csv.NewReader(file)
	r.ReuseRecord = true // with this enabled, DO NOT MODIFY THE SLICE RETURNED FROM Read()

	var headerRow []string
	var bestMapping map[string][]int // canonical field name -> matched column indices

	for {
		row, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}

		// header row
		if len(headerRow) == 0 {
			headerRow = make([]string, len(row))
			copy(headerRow, row)

			// detect best format or mapping of columns to known fields
			bestMapping = bestColumnMapping(headerRow)

			// TODO: at least 2 fields should be required in order to be useful, right?
			// like an email by itself (or a name by itself) has no value I think... esp.
			// since no items are attached from a contact list
			if len(bestMapping) < recognizeAtLeastFields {
				return fmt.Errorf("insufficient header row")
			}

			continue
		}

		// first, extract mappedValues from recognized columns in the row
		mappedValues := make(map[string][]string) // map of canonical field name -> associated value(s) from row
		for canonicalField, colIndices := range bestMapping {
			for _, colIdx := range colIndices {
				mappedValues[canonicalField] = append(mappedValues[canonicalField], row[colIdx])
			}
		}

		// then, convert each field+values pair to something about the person
		p := new(timeline.Entity)

		for field, values := range mappedValues {
			for _, value := range values {
				if strings.TrimSpace(value) == "" {
					// ignore empty values; especially if there are multiple matched columns
					// for a field (like Name, for some reason), don't overwrite a non-empty
					// first column with an empty second column
					continue
				}
				switch field {
				case "full_name":
					p.Name = value
				case "first_name":
					p.Name = value + " " + p.Name
				case "last_name":
					p.Name += " " + value
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
						p.NewPicture = timeline.DownloadData(ctx, value)
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

		// I think it's pointless to process person if there aren't at
		// least 2 data points about them because we can get single
		// data points from nearly any data source; the value of adding
		// a contact list is to get more information about a person to
		// infer more relationships automatically.
		if (p.NewPicture == nil && len(p.Metadata) == 0 && len(p.Attributes) == 0) || // only a name is kinda useless
			(p.Name == "" && p.NewPicture != nil && len(p.Attributes)+len(p.Metadata) == 0) {
			continue
		}

		// finally, send person for processing
		itemChan <- &timeline.Graph{Entity: p}
	}

	return nil
}
