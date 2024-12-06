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

// Package icloud implements a data source for Apple account data exports (iCloud backups, exported from your account).
package icloud

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"path/filepath"
	"strings"

	"github.com/mholt/archives"
	"github.com/timelinize/timelinize/datasources/media"
	"github.com/timelinize/timelinize/timeline"
	"go.uber.org/zap"
)

func init() {
	err := timeline.RegisterDataSource(timeline.DataSource{
		Name:            "icloud",
		Title:           "iCloud",
		Icon:            "icloud.svg",
		Description:     "An export of data from Apple iCloud.",
		NewOptions:      func() any { return new(Options) },
		NewFileImporter: func() timeline.FileImporter { return new(FileImporter) },
	})
	if err != nil {
		timeline.Log.Fatal("registering data source", zap.Error(err))
	}
}

// Options configures the data source.
type Options struct {
	// The ID of the owner entity. REQUIRED if entity is to be related in DB.
	OwnerEntityID int64 `json:"owner_entity_id"`

	// Whether to import recently deleted items.
	RecentlyDeleted bool `json:"recently_deleted"`
}

// FileImporter implements the timeline.FileImporter interface.
type FileImporter struct{}

const (
	icloudPhotosZip   = "iCloud Photos.zip"
	icloudContactsZip = "iCloud Contacts.zip"
	icloudInfoZip     = "Apple ID account and device information.zip"
)

// Recognize returns whether the file or folder is supported.
func (FileImporter) Recognize(_ context.Context, dirEntry timeline.DirEntry, opts timeline.RecognizeParams) (timeline.Recognition, error) {
	if dirEntry.Name() != icloudInfoZip &&
		dirEntry.Name() != icloudContactsZip &&
		dirEntry.Name() != icloudPhotosZip &&
		!strings.HasPrefix(dirEntry.Name(), "iCloud Photos Part ") {
		return timeline.Recognition{}, nil
	}
	return timeline.Recognition{Confidence: 1}, nil
}

// FileImport imports data from a file or folder.
func (fi *FileImporter) FileImport(ctx context.Context, dirEntry timeline.DirEntry, params timeline.ImportParams) error {
	switch dirEntry.Name() {
	case icloudInfoZip:
		// TODO: implement
	case icloudContactsZip:
		// TODO: implement
	case icloudPhotosZip:
		fallthrough
	default:
		if dirEntry.Name() == icloudPhotosZip || strings.HasPrefix(dirEntry.Name(), "iCloud Photos Part ") {
			topDir := strings.TrimSuffix(dirEntry.Name(), filepath.Ext(dirEntry.Name()))

			err := fi.importPhotos(ctx, topDir, dirEntry, params)
			if err != nil {
				return err
			}

			err = fi.importAlbumsAndMemories(ctx, topDir, dirEntry, params)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

const yes = "yes"

func (fi *FileImporter) importPhotos(ctx context.Context, fsysName string, d timeline.DirEntry, opt timeline.ImportParams) error {
	dsOpt := opt.DataSourceOptions.(*Options)
	owner := timeline.Entity{ID: dsOpt.OwnerEntityID}

	const likelyMaxNumberOfArchives = 1000
	for i := range likelyMaxNumberOfArchives {
		// TODO: parallelize this for faster imports
		done, err := func(i int) (bool, error) {
			filename := "Photo Details"
			if i > 0 {
				filename += fmt.Sprintf("-%d", i)
			}
			filename += ".csv"

			detailsFile, err := d.TopDirOpen(path.Join(fsysName, "Photos", filename))
			if errors.Is(err, fs.ErrNotExist) {
				return true, nil
			}
			if err != nil {
				return false, err
			}
			defer detailsFile.Close()

			csvr := csv.NewReader(detailsFile)

			// map of column names to their index
			cols := make(map[string]int)

			for {
				if ctx.Err() != nil {
					return false, ctx.Err()
				}
				row, err := csvr.Read()
				if errors.Is(err, io.EOF) {
					break
				}
				if err != nil {
					return false, err
				}
				const requiredFields = 2
				if len(row) < requiredFields {
					continue
				}
				if len(cols) == 0 {
					// map field names to their index
					for i, field := range row {
						cols[field] = i
					}
					continue
				}

				// TODO: note that a lot of this logic is borrowed from the media importer

				subfolder := "Photos"

				// skip deleted items if preferred
				if row[cols["deleted"]] == yes {
					if !dsOpt.RecentlyDeleted {
						continue
					}
					subfolder = "Recently Deleted"
				}

				imgName := row[cols["imgName"]]
				imgPath := path.Join(fsysName, subfolder, imgName)

				// since we may be operating in a sub-folder of the FS, be sure to account for that
				imgPath = path.Join(d.Filename, imgPath)

				// skip sidecar videos that are part of live photos (we'll connect it when we process the associated image file)
				if media.IsSidecarVideo(d.FS, imgPath) {
					continue
				}

				class, supported := media.ItemClassByExtension(imgName)
				if !supported {
					// skip unsupported files by filename extension (naive, but hopefully OK)
					opt.Log.Debug("skipping unrecognized file", zap.String("filename", imgPath))
					continue
				}

				item := &timeline.Item{
					Classification:       class,
					Owner:                owner,
					IntermediateLocation: imgPath,
					Content: timeline.ItemData{
						Filename: imgName,
						Data: func(_ context.Context) (io.ReadCloser, error) {
							return archives.TopDirOpen(d.FS, imgPath) // imgPath already prepended the DirEntry Filename
						},
					},
					Metadata: timeline.Metadata{
						"iCloud file checksum": row[cols["fileChecksum"]],
						"View count":           row[cols["viewCount"]],
						"iCloud import date":   row[cols["importDate"]],
					},
				}
				if row[cols["favorite"]] == yes {
					item.Metadata["Favorited"] = true
				}
				if row[cols["hidden"]] == yes {
					item.Metadata["Hidden"] = true
				}
				if row[cols["deleted"]] == yes {
					item.Metadata["Deleted"] = true
				}

				// get as much metadata as possible from the picture
				_, err = media.ExtractAllMetadata(opt.Log, d.FS, imgPath, item, timeline.MetaMergeAppend)
				if err != nil {
					opt.Log.Warn("extracting metadata",
						zap.String("file", imgPath),
						zap.Error(err))
				}

				ig := &timeline.Graph{Item: item}

				media.ConnectMotionPhoto(opt.Log, d.FS, imgPath, ig)

				opt.Pipeline <- ig
			}

			return false, nil
		}(i)
		if err != nil {
			return err
		}
		if done {
			break
		}
	}
	return nil
}

func (fi *FileImporter) importAlbumsAndMemories(ctx context.Context, fsysName string, d timeline.DirEntry, opt timeline.ImportParams) error {
	// albums
	entries, err := d.TopDirReadDir(path.Join(fsysName, "Albums"))
	if err != nil {
		return err
	}
	for _, entry := range entries {
		// this is an automatic album that isn't particularly interesting or relevant; skip it
		if entry.Name() == "RAW.csv" {
			continue
		}
		err := fi.importAlbumOrMemory(ctx, fsysName, d, path.Join(fsysName, "Albums", entry.Name()), opt)
		if err != nil {
			return err
		}
	}

	// memories
	entries, err = d.TopDirReadDir(path.Join(fsysName, "Memories"))
	if err != nil {
		return err
	}
	for _, entry := range entries {
		err := fi.importAlbumOrMemory(ctx, fsysName, d, path.Join(fsysName, "Memories", entry.Name()), opt)
		if err != nil {
			return err
		}
	}

	return nil
}

func (fi *FileImporter) importAlbumOrMemory(ctx context.Context, fsysName string, d timeline.DirEntry, albumPath string, opt timeline.ImportParams) error {
	dsOpt := opt.DataSourceOptions.(*Options)
	owner := timeline.Entity{ID: dsOpt.OwnerEntityID}

	albumFileExt := path.Ext(albumPath)
	albumName := strings.TrimSuffix(path.Base(albumPath), albumFileExt)

	coll := &timeline.Item{
		Classification: timeline.ClassCollection,
		Content: timeline.ItemData{
			Data: timeline.StringData(albumName),
		},
		Owner: owner,
	}

	albumListing, err := d.TopDirOpen(albumPath)
	if err != nil {
		return err
	}
	defer albumListing.Close()

	csvr := csv.NewReader(albumListing)

	// map of column names to their index
	cols := make(map[string]int)

	// album positional index
	var pos int

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		row, err := csvr.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		if len(row) < 1 {
			continue
		}
		if strings.TrimSpace(row[0]) == "" {
			continue
		}
		if len(cols) == 0 {
			// map field names to their index
			for i, field := range row {
				cols[field] = i
			}
			continue
		}

		imgName := row[cols["imgName"]]
		imgPath := path.Join(fsysName, "Photos", imgName)

		// since we may be operating in a sub-folder of the FS, be sure to account for that
		imgPath = path.Join(d.Filename, imgPath)

		class, supported := media.ItemClassByExtension(imgName)
		if !supported {
			// skip unsupported files by filename extension (naive, but hopefully OK)
			opt.Log.Debug("skipping unrecognized file", zap.String("filename", imgPath))
			continue
		}

		item := &timeline.Item{
			Classification:       class,
			Owner:                owner,
			IntermediateLocation: imgPath,
			Content: timeline.ItemData{
				Filename: imgName,
				Data: func(_ context.Context) (io.ReadCloser, error) {
					return archives.TopDirOpen(d.FS, imgPath) // imgPath already prepended the DirEntry Filename
				},
			},
		}

		// get as much metadata as possible from the picture
		_, err = media.ExtractAllMetadata(opt.Log, d.FS, imgPath, item, timeline.MetaMergeAppend)
		if err != nil {
			opt.Log.Warn("extracting metadata",
				zap.String("file", imgPath),
				zap.Error(err))
		}

		ig := &timeline.Graph{Item: item}
		ig.ToItemWithValue(timeline.RelInCollection, coll, pos)

		pos++

		opt.Pipeline <- ig
	}

	return nil
}
