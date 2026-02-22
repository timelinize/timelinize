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

package generic

import (
	"fmt"
	"io/fs"

	"github.com/cozy/goexif2/exif"
	"github.com/timelinize/timelinize/timeline"
)

type fileItem struct {
	fsys     fs.FS
	path     string
	dirEntry fs.DirEntry
}

func (fi fileItem) location() timeline.Location {
	loc, _ := fi.locationFromExif()
	return loc
}

func (fi fileItem) locationFromExif() (timeline.Location, error) {
	file, err := fi.fsys.Open(fi.path)
	if err != nil {
		return timeline.Location{}, fmt.Errorf("unable to open file to attempt reading EXIF: %w", err)
	}
	defer file.Close()

	ex, err := exif.Decode(file)
	if err != nil {
		return timeline.Location{}, err
	}

	lat, lon, err := ex.LatLong()
	if err != nil {
		return timeline.Location{}, err
	}

	return timeline.Location{
		Latitude:  &lat,
		Longitude: &lon,
	}, nil
}
