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

// Package generic implements a data source that imports generic files from
// disk that aren't better supported by any other data source.
package generic

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"strings"

	"github.com/mholt/goexif2/exif"
	"github.com/mholt/goexif2/mknote"
	"github.com/timelinize/timelinize/timeline"
	"go.uber.org/zap"
)

// Data source name and ID.
const (
	DataSourceName = "Generic"
	DataSourceID   = "generic"
)

// Options configures the data source.
type Options struct{}

func init() {
	exif.RegisterParsers(mknote.All...)

	err := timeline.RegisterDataSource(timeline.DataSource{
		Name:            DataSourceID,
		Title:           DataSourceName,
		Icon:            "folder.svg",
		NewOptions:      func() any { return new(Options) },
		NewFileImporter: func() timeline.FileImporter { return new(FileImporter) },
	})
	if err != nil {
		timeline.Log.Fatal("registering data source", zap.Error(err))
	}
}

// FileImporter interacts with the file system to get items.
type FileImporter struct{}

// Recognize returns whether the input file is recognized.
func (FileImporter) Recognize(_ context.Context, _ timeline.DirEntry, _ timeline.RecognizeParams) (timeline.Recognition, error) {
	return timeline.Recognition{Confidence: 1}, nil
}

// FileImport conducts an import of the data using this data source.
func (c *FileImporter) FileImport(ctx context.Context, dirEntry timeline.DirEntry, params timeline.ImportParams) error {
	err := fs.WalkDir(dirEntry.FS, dirEntry.Filename, func(fpath string, d fs.DirEntry, err error) error {
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

		filename := d.Name()

		fitem := fileItem{fsys: dirEntry.FS, path: fpath, dirEntry: d}

		item := &timeline.Item{
			Timestamp:            fitem.timestamp(),
			Location:             fitem.location(),
			IntermediateLocation: fpath,
			Content: timeline.ItemData{
				Filename: filename,
				Data: func(_ context.Context) (io.ReadCloser, error) {
					return dirEntry.FS.Open(fpath)
				},
			},
			// TODO: surely there is some metadata...?
		}

		params.Pipeline <- &timeline.Graph{Item: item}

		return nil
	})
	if err != nil {
		return fmt.Errorf("walking dir: %w", err)
	}

	return nil
}
