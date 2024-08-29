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

package iphone

import (
	"context"
	"fmt"
	"io"
	"os"
	"path"

	"github.com/timelinize/timelinize/datasources/media"
	"github.com/timelinize/timelinize/timeline"
	"go.uber.org/zap"
)

func (fimp *FileImporter) cameraRoll(ctx context.Context) error {
	rows, err := fimp.manifest.QueryContext(ctx, "SELECT fileID, relativePath FROM Files WHERE domain='CameraRollDomain' AND relativePath LIKE 'Media/DCIM/%'")
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var fileID, relativePath *string
		err := rows.Scan(&fileID, &relativePath)
		if err != nil {
			return fmt.Errorf("scanning row: %w", err)
		}

		// I haven't seen this, but just to avoid a panic
		if fileID == nil || relativePath == nil {
			continue
		}

		fimp.processCameraRollFile(ctx, *fileID, *relativePath)
	}
	if err = rows.Err(); err != nil {
		return fmt.Errorf("scanning rows: %w", err)
	}

	return nil
}

func (fimp *FileImporter) processCameraRollFile(_ context.Context, fileID, relativePath string) {
	// skip unsupported file types
	class, supported := media.ItemClassByExtension(relativePath)
	if !supported {
		return
	}

	fsys := domainFS{fimp, cameraRollDomain}

	// skip sidecar movie files ("live photos") because we'll connect it when
	// we process the actual photograph
	if media.IsSidecarVideo(fsys, relativePath) {
		return
	}

	item := &timeline.Item{
		Classification:       class,
		OriginalLocation:     fmt.Sprintf("%s:%s", cameraRollDomain, relativePath),
		IntermediateLocation: fimp.fileIDToRelativePath(fileID),
		Content: timeline.ItemData{
			Filename: path.Base(relativePath),
			Data: func(ctx context.Context) (io.ReadCloser, error) {
				return fimp.openFile(ctx, cameraRollDomain, relativePath)
			},
		},
	}

	// get as much metadata as possible (ignore any embedded media, I guess?)
	_, err := media.ExtractAllMetadata(fimp.opt.Log, os.DirFS(fimp.root), fimp.fileIDToRelativePath(fileID), item, timeline.MetaMergeAppend)
	if err != nil {
		fimp.opt.Log.Warn("extracting metadata",
			zap.String("file", relativePath),
			zap.Error(err))
	}

	ig := &timeline.Graph{Item: item}

	media.ConnectMotionPhoto(fimp.opt.Log, fsys, relativePath, ig)

	fimp.itemChan <- ig
}
