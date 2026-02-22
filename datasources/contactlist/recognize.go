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
	"fmt"
	"path"
	"strings"

	"github.com/timelinize/timelinize/timeline"
)

// Recognize returns true if the file is recognized as a contact list,
// or a Google Takeout contact list folder that also contains pictures.
func (fimp *FileImporter) Recognize(ctx context.Context, dirEntry timeline.DirEntry, _ timeline.RecognizeParams) (timeline.Recognition, error) {
	pathInFS := "." // start by assuming we'll inspect the current file
	if dirEntry.IsDir() {
		// if a directory, specifically, a folder for a contact list in a Google Takeout archive,
		// recognize the whole folder so we can import those profile pictures as well
		parts := strings.Split(dirEntry.FullPath(), "/")
		if len(parts) >= 3 && parts[len(parts)-3] == "Takeout" && parts[len(parts)-2] == "Contacts" {
			if csvFilename := parts[len(parts)-1] + ".csv"; timeline.FileExistsFS(dirEntry.FS, path.Join(dirEntry.Filename, csvFilename)) {
				pathInFS = csvFilename
			} else {
				return timeline.Recognition{}, nil
			}
		} else {
			return timeline.Recognition{}, nil
		}
	} else {
		// only inspect file if it has a relevant extension
		switch strings.ToLower(path.Ext(dirEntry.Name())) {
		case ".csv", ".tsv":
		default:
			return timeline.Recognition{}, nil
		}
	}

	bestColMapping, bestDelim, err := bestColumnMappingAndDelim(ctx, dirEntry, pathInFS)
	if err != nil {
		return timeline.Recognition{}, err
	}

	if len(bestColMapping) > recognizeAtLeastFields && bestDelim != 0 {
		return timeline.Recognition{Confidence: 1.0}, nil
	}

	return timeline.Recognition{}, nil
}

// bestColumnMappingAndDelim determines the best mapping of columns, which is canonical field name -> matched column indices,
// along with the associated detected delimiter of the file.
func bestColumnMappingAndDelim(ctx context.Context, dirEntry timeline.DirEntry, pathInFS string) (map[string][]int, rune, error) {
	var bestDelim rune
	var bestMapping map[string][]int // best column mapping across all delimiters

	for _, delim := range []rune{',', '\t', ';'} {
		if err := ctx.Err(); err != nil {
			return nil, 0, err
		}
		colMapping, err := determineColumnMappingForDelimiter(dirEntry, pathInFS, delim)
		if err != nil {
			return nil, 0, fmt.Errorf("trying to determine column mapping with delimiter %s: %w", string(delim), err)
		}
		if len(colMapping) > len(bestMapping) {
			bestMapping = colMapping
			bestDelim = delim
		}
	}

	return bestMapping, bestDelim, nil
}

func determineColumnMappingForDelimiter(dirEntry timeline.DirEntry, pathInFS string, delim rune) (map[string][]int, error) {
	file, err := dirEntry.Open(pathInFS)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	r := csv.NewReader(file)
	r.Comma = delim

	var fieldCount int
	var columnMapping map[string][]int
	for {
		row, err := r.Read()
		if err != nil {
			return nil, nil // most likely a syntax error, nbd
		}

		if fieldCount == 0 {
			// this must be the header row

			// should have at least two fields to be useful
			fieldCount = len(row)
			if fieldCount < minFields {
				return nil, nil
			}

			// if we don't recognize the minimum number of distinct fields, it's not a recognized contact list
			columnMapping = bestColumnMappingForFields(row)
			if len(columnMapping) < recognizeAtLeastFields {
				return nil, nil
			}
		} else {
			// we've already seen the header row;
			// field count should be equal to header row
			if len(row) != fieldCount {
				return nil, nil
			}
			break
		}
	}

	return columnMapping, nil
}

// bestColumnMappingForFields returns the best mapping of canonical field name
// to column indices we could find.
func bestColumnMappingForFields(headerRow []string) map[string][]int {
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
