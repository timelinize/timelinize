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
	"encoding/csv"
	"os"
	"path/filepath"

	"github.com/timelinize/timelinize/timeline"
)

// Recognize returns true if the file is recognized as a contact list.
func (fimp *FileImporter) Recognize(_ context.Context, filenames []string) (timeline.Recognition, error) {
nextFile:
	for _, filename := range filenames {
		for _, delim := range []rune{',', '\t', ';'} {
			result, err := recognizeCSV(filename, delim)
			if err != nil {
				return result, err
			}
			if result.Confidence > 0 {
				continue nextFile
			}
		}
		return timeline.Recognition{}, nil
	}
	return timeline.Recognition{Confidence: 1.0}, nil
}

func recognizeCSV(filename string, delim rune) (timeline.Recognition, error) {
	file, err := os.Open(filepath.Clean(filename))
	if err != nil {
		return timeline.Recognition{}, err
	}
	defer file.Close()

	r := csv.NewReader(file)
	r.Comma = delim

	var fieldCount int
	for {
		row, err := r.Read()
		if err != nil {
			return timeline.Recognition{}, nil // most likely a syntax error, nbd
		}

		if fieldCount == 0 {
			// this must be the header row

			// should have at least two fields to be useful
			fieldCount = len(row)
			if fieldCount < minFields {
				return timeline.Recognition{}, nil
			}

			// if we don't recognize the minimum number of distinct fields, it's not a recognized contact list
			if len(bestColumnMapping(row)) < recognizeAtLeastFields {
				return timeline.Recognition{}, nil
			}
		} else {
			// we've already seen the header row;
			// field count should be equal to header row
			if len(row) != fieldCount {
				return timeline.Recognition{}, nil
			}
			break
		}
	}

	return timeline.Recognition{Confidence: 1.0}, nil
}

// bestColumnMapping returns the best mapping of canonical field name
// to column indices we could find.
func bestColumnMapping(headerRow []string) map[string][]int {
	var bestMapping map[string][]int
	for _, f := range formats {
		result := f.match(headerRow)
		// TODO: if a tiebreaker is needed, we might want to also consider the number of total columns matched (i.e. multiple email columns is better than one)
		if len(result) > len(bestMapping) {
			bestMapping = result
		}
	}
	return bestMapping
}

const (
	minFields              = 2 // file must have at least this many fields
	recognizeAtLeastFields = 2 // we must recognize at least this many field names
)
