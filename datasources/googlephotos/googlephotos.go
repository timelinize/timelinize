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

// Package googlephotos implements a data source for Google Photos albums and
// exports, and also via its API documented at https://developers.google.com/photos/.
package googlephotos

import (
	"context"
	"path"

	"github.com/timelinize/timelinize/timeline"
	"go.uber.org/zap"
)

const dataSourceName = "google_photos"

func init() {
	err := timeline.RegisterDataSource(timeline.DataSource{
		Name:            dataSourceName,
		Title:           "Google Photos",
		Icon:            "googlephotos.svg",
		NewOptions:      func() any { return Options{} },
		NewFileImporter: func() timeline.FileImporter { return new(FileImporter) },
	})
	if err != nil {
		timeline.Log.Fatal("registering data source", zap.Error(err))
	}
}

// FileImporter imports data from a file or folder.
type FileImporter struct {
	filename       string
	truncatedNames map[string]int
}

// Recognize returns whether this file or folder is supported.
func (FileImporter) Recognize(_ context.Context, dirEntry timeline.DirEntry, _ timeline.RecognizeParams) (timeline.Recognition, error) {
	// prefer a Google Takeout archive with Google Photos data inside it
	if dirEntry.IsDir() && timeline.FileExistsFS(dirEntry.FS, path.Join(dirEntry.Filename, googlePhotosPath)) {
		return timeline.Recognition{Confidence: 1}, nil
	}
	return timeline.Recognition{}, nil
}

// FileImport imports data from a file/folder.
func (fimp *FileImporter) FileImport(ctx context.Context, dirEntry timeline.DirEntry, params timeline.ImportParams) error {
	fimp.filename = dirEntry.Name()

	if _, err := dirEntry.TopDirStat(googlePhotosPath); err == nil {
		return fimp.listFromTakeoutArchive(ctx, params, dirEntry)
	}

	if err := fimp.listFromAlbumFolder(ctx, params, dirEntry); err != nil {
		return err
	}

	return nil
}

// Options configures the data source.
type Options struct {
	// Get albums. This can add significant time to an import.
	Albums bool `json:"albums"`

	IncludeArchivedMedia bool `json:"include_archived_media"`
}

// recognizedExts is only used for file recognition, as a fallback.
var recognizedExts = map[string]struct{}{
	".jpg":  {},
	".jpe":  {},
	".jpeg": {},
	".mp4":  {},
	".mov":  {},
	".mpeg": {},
	".avi":  {},
	".wmv":  {},
	".png":  {},
	".raw":  {},
	".dng":  {},
	".nef":  {},
	".tiff": {},
	".gif":  {},
	".heic": {},
	".webp": {},
	".divx": {},
	".m4v":  {},
	".3gp":  {},
	".3g2":  {},
	".m2t":  {},
	".m2ts": {},
	".mts":  {},
	".mkv":  {},
	".asf":  {},
}
