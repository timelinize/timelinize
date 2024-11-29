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

package googlephotos

import (
	"context"
	"io"
	"io/fs"
	"path/filepath"
	"strings"
	"sync"

	"github.com/timelinize/timelinize/datasources/media"
	"github.com/timelinize/timelinize/timeline"
	"go.uber.org/zap"
)

func (fimp *FileImporter) listFromAlbumFolder(ctx context.Context, opt timeline.ImportParams, fsys fs.FS) error {
	albumName := filepath.Base(fimp.filename)

	// process files in parallel for faster imports
	var wg sync.WaitGroup
	const maxGoroutines = 100
	throttle := make(chan struct{}, maxGoroutines)

	err := fs.WalkDir(fsys, ".", func(fpath string, d fs.DirEntry, err error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err != nil {
			return err
		}
		if fpath == "." {
			// ignore folder or archive itself
			return nil
		}
		if d.IsDir() {
			// skip folders and unrecognized files
			return fs.SkipDir
		}
		ext := strings.ToLower(filepath.Ext(fpath))
		if _, ok := recognizedExts[ext]; !ok {
			// skip unsupported files by filename extension (naive, but hopefully OK)
			return nil
		}

		throttle <- struct{}{}
		wg.Add(1)
		go func() {
			defer func() {
				<-throttle
				wg.Done()
			}()

			item := &timeline.Item{
				Content: timeline.ItemData{
					Filename: d.Name(),
					Data: func(_ context.Context) (io.ReadCloser, error) {
						return fsys.Open(fpath)
					},
				},
			}

			_, err = media.ExtractAllMetadata(opt.Log, fsys, fpath, item, timeline.MetaMergeAppend)
			if err != nil {
				opt.Log.Warn("extracting metadata",
					zap.String("filename", fpath),
					zap.Error(err))
			}

			ig := &timeline.Graph{Item: item}

			ig.ToItem(timeline.RelInCollection, &timeline.Item{
				Classification: timeline.ClassCollection,
				Content: timeline.ItemData{
					Data: timeline.StringData(albumName),
				},
			})

			opt.Pipeline <- ig
		}()

		return nil
	})
	if err != nil {
		return err
	}

	wg.Wait()

	return nil
}
