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
	"os"
	"path"
	"path/filepath"

	"github.com/mholt/archiver/v4"
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
type Options struct {
	ExpandArchives bool `json:"expand_archives"`
}

func init() {
	exif.RegisterParsers(mknote.All...)

	err := timeline.RegisterDataSource(timeline.DataSource{
		Name:            DataSourceID,
		Title:           DataSourceName,
		Icon:            "folder.svg",
		NewOptions:      func() any { return new(Options) },
		NewFileImporter: func() timeline.FileImporter { return new(Client) },
	})
	if err != nil {
		timeline.Log.Fatal("registering data source", zap.Error(err))
	}
}

// Client interacts with the file system to get items.
type Client struct{}

// Recognize returns whether the input file is recognized.
func (Client) Recognize(_ context.Context, _ []string) (timeline.Recognition, error) {
	return timeline.Recognition{Confidence: 1}, nil
}

// FileImport conducts an import of the data using this data source.
func (c *Client) FileImport(ctx context.Context, filenames []string, itemChan chan<- *timeline.Graph, opt timeline.ListingOptions) error {
	for _, filename := range filenames {
		if err := c.walk(ctx, filename, ".", itemChan, opt); err != nil {
			return err
		}
	}
	return nil
}

// walk processes the item at root, or the items within root, joined to pathInRoot, which is
// a relative path to root and must use the slash as separator.
func (c *Client) walk(ctx context.Context, root, pathInRoot string, itemChan chan<- *timeline.Graph, opt timeline.ListingOptions) error {
	filesOpt := opt.DataSourceOptions.(*Options)

	fsys, err := archiver.FileSystem(ctx, filepath.Join(root, pathInRoot))
	if err != nil {
		return err
	}
	_, isArchiveFS := fsys.(archiver.ArchiveFS)

	err = fs.WalkDir(fsys, ".", func(fpath string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}

		// if enabled, traverse into archives, but not archives within archives
		if filesOpt.ExpandArchives && !isArchiveFS {
			fullPath := filepath.Join(root, pathInRoot, fpath)
			file, err := os.Open(filepath.Clean(fullPath))
			if err != nil {
				return err
			}
			format, _, err := archiver.Identify(fullPath, file)
			file.Close()
			if err == nil && format != nil {
				// some files look like archives but aren't actually generic archives;
				// for example, Microsoft Office files (.docx, etc.) are just .zip
				// files with special contents; we don't want to traverse into those,
				// so only traverse those if the filename has the .zip extension
				zip, isZip := format.(archiver.Zip)
				if !isZip || path.Ext(fullPath) == zip.Name() {
					err = c.walk(ctx, root, fpath, itemChan, opt)
					if err != nil {
						return fmt.Errorf("traversing into archive file: %w", err)
					}
					return nil
				}
			}
		}

		fitem := fileItem{fsys: fsys, path: fpath, dirEntry: d}

		item := &timeline.Item{
			Timestamp:            fitem.timestamp(),
			Location:             fitem.location(),
			IntermediateLocation: fpath,
			Content: timeline.ItemData{
				Filename: filepath.Base(fpath),
				Data: func(_ context.Context) (io.ReadCloser, error) {
					return fsys.Open(fpath)
				},
			},
			// TODO: surely there is some metadata...?
		}

		itemChan <- &timeline.Graph{Item: item}

		return nil
	})
	if err != nil {
		return fmt.Errorf("walking dir: %w", err)
	}

	return nil
}
