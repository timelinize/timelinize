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

// Package media implements a data source for general media files (photos, videos, and audio)
// that may have come from a camera roll, photo library, etc.
package media

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"path"
	"path/filepath"
	"strings"
	"sync"

	"github.com/mholt/archiver/v4"
	"github.com/timelinize/timelinize/datasources/generic"
	"github.com/timelinize/timelinize/timeline"
	"go.uber.org/zap"
)

func init() {
	err := timeline.RegisterDataSource(timeline.DataSource{
		Name:            "media",
		Title:           "Media",
		Icon:            "media.png",
		NewOptions:      func() any { return new(Options) },
		NewFileImporter: func() timeline.FileImporter { return new(FileImporter) },
	})
	if err != nil {
		timeline.Log.Fatal("registering data source", zap.Error(err))
	}
}

type Options struct {
	// We will attempt to extract timestamps for each media item in this order:
	// embedded (EXIF/XMP), filepath (if enabled), and as a last resort, file
	// modification time (if enabled). If more than one timestamp is available, a
	// date range can be specified to help choose one. If specified, a timestamp
	// within this range will be preferred over one not in the range. If both
	// timestamps are either in or out of the range, then the range will be
	// ignored in the choice of timestamp.
	//
	// Items with chosen timestamps that are outside this range will still be
	// processed. To avoid importing items that don't have any timestamp in a
	// certain range, use the Timeframe in ListingOptions instead.
	DateRange timeline.Timeframe `json:"date_range,omitempty"`

	// If true, a timestamp found in the file path may be used.
	UseFilePathTime bool `json:"use_filepath_time,omitempty"`

	// If true, fall back to file modification time if no other timestamp is found.
	// This can be accurate in some cases, like exports/downloads from Google Photos
	// of media files like "creations" (gifs, etc), or Apple .MOV files that don't
	// have a timestamp embedded within them.
	UseFileModTime bool `json:"use_file_mod_time,omitempty"`

	// If true, a collection will be created for each folder with items in it.
	FolderIsAlbum bool `json:"folder_is_album,omitempty"`

	// The ID of the owner entity. REQUIRED if entity is to be related in DB.
	OwnerEntityID int64 `json:"owner_entity_id"`
}

// FileImporter can import the data from a file.
type FileImporter struct{}

func (FileImporter) Recognize(ctx context.Context, filenames []string) (timeline.Recognition, error) {
	var totalCount, matchCount int

	for _, filename := range filenames {
		fsys, err := archiver.FileSystem(ctx, filename)
		if err != nil {
			return timeline.Recognition{}, err
		}

		// TODO: using fs.WalkDir is too inefficient with the current implementation I have for archive files
		// (consider type switching on fsys, and if a dir, then use WalkDir; for archives use Extract and just
		// iterate the entries in an arbitrary order, since the order/structure doesn't matter to us)

		// // switch f := fsys.(type) {
		// // case archiver.DirFS:
		// // 	dir, err := os.Open(filename)
		// // 	if err != nil {
		// // 		return false, err
		// // 	}
		// // 	entries, err := dir.ReadDir(10000)
		// // 	if err != nil {
		// // 		return false, err
		// // 	}
		// // 	for _, entry := range entries {
		// // 		// album exports do not have subfolders AFAIK
		// // 		// (TODO: is there any harm in supporting subfolders, if we support all the file types?)
		// // 		if entry.IsDir() {
		// // 			return false, nil
		// // 		}

		// // 		ext := strings.ToLower(filepath.Ext(entry.Name()))
		// // 		if _, ok := recognizedExts[ext]; !ok {
		// // 			return false, nil
		// // 		}
		// // 	}
		// // }

		// if afs, ok := fsys.(archiver.ArchiveFS); ok {
		// 	afs.Context = ctx
		// }

		err = fs.WalkDir(fsys, ".", func(fpath string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if fpath == "." {
				fpath = path.Base(filename)
			}
			if strings.HasPrefix(path.Base(fpath), ".") {
				// skip hidden files; they are cruft
				if d.IsDir() {
					return fs.SkipDir
				} else {
					return nil
				}
			}
			if d.IsDir() {
				return nil // traverse into subdirectories
			}

			totalCount++

			// for regular files, file type must be recognized (relying on extension is hopefully good enough for now)
			if _, ok := ItemClassByExtension(fpath); ok {
				matchCount++
			}

			return nil
		})
		if err != nil {
			return timeline.Recognition{}, err
		}
	}

	if totalCount > 0 {
		return timeline.Recognition{Confidence: float64(matchCount) / float64(totalCount)}, nil
	}
	return timeline.Recognition{}, nil

	// // TODO: this is too slow, even with context timeout for some reason.
	// // alternatively, if the input is a single directory that contains only
	// // recognized media, then we might assume this is a Google Photos album export
	// errNotSupported := fmt.Errorf("file type not supported")
	// // switch fsys.(type) {
	// // case archiver.DirFS, archiver.ArchiveFS:
	// err = fs.WalkDir(fsys, ".", func(fpath string, d fs.DirEntry, err error) error {
	// 	if err != nil {
	// 		return err
	// 	}
	// 	if d.IsDir() {
	// 		return nil // traverse into subdirectories
	// 	}
	// 	if fpath == "." {
	// 		fpath = path.Base(filename)
	// 	}
	// 	if strings.HasPrefix(path.Base(fpath), ".") {
	// 		return nil // skip hidden files; they are cruft
	// 	}
	// 	// for regular files, file type must be recognized (relying on extension is hopefully good enough)
	// 	if _, ok := itemClassByExtension(fpath); !ok {
	// 		return fmt.Errorf("%s: %w", fpath, errNotSupported)
	// 	}
	// 	return nil
	// })
	// if errors.Is(err, errNotSupported) {
	// 	return false, nil
	// }
	// if err != nil {
	// 	return false, err
	// }
	// return true, nil
	// // }

}

func (imp *FileImporter) FileImport(ctx context.Context, filenames []string, itemChan chan<- *timeline.Graph, opt timeline.ListingOptions) error {
	dsOpt := opt.DataSourceOptions.(*Options)

	owner := timeline.Entity{ID: dsOpt.OwnerEntityID}

	for _, filename := range filenames {
		fsys, err := archiver.FileSystem(ctx, filename)
		if err != nil {
			return err
		}

		collections := make(map[string]*timeline.Item)
		var collectionsMu sync.Mutex

		// processing files in parallel can greatly speed up imports,
		// but use a throttle to avoid unbounded goroutines
		throttle := make(chan struct{}, 100)
		var wg sync.WaitGroup

		err = fs.WalkDir(fsys, ".", func(fpath string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if len(fpath) > 1 && strings.HasPrefix(path.Base(fpath), ".") {
				// skip hidden files; they are cruft
				if d.IsDir() {
					return fs.SkipDir
				} else {
					return nil
				}
			}
			if d.IsDir() {
				return nil
			}

			// get the extension and directory; but this is nuanced if it's a single file, which comes up as "." in the walk
			fname, itemPath := fpath, fpath
			if fpath == "." {
				if _, ok := fsys.(archiver.FileFS); ok {
					fname = filename
					itemPath = filepath.Base(filename)
				}
			}

			// if a single directory is being imported, handle that case too
			fpathDir := path.Dir(fname)
			if fpathDir == "." {
				if _, ok := fsys.(archiver.DirFS); ok {
					fpathDir = filepath.Base(filename)
				}
			}

			// TODO: a lot of this logic is duplicated by the iCloud importer, except:
			// the concurrency model is a little different; we don't discover items by
			// traversing a folder (they are listed in a CSV file); we don't assign
			// timestamps from folder paths or modtime; collection membership is different;
			// etc. Come to think of it, we might need to move a lot of this logic into
			// the Google Photos importer as well.

			// skip dot-files, as those are often unwanted cruft
			if strings.HasPrefix(path.Base(fname), ".") {
				return nil
			}

			class, supported := ItemClassByExtension(fname)
			if !supported {
				// skip unsupported files by filename extension (naive, but hopefully OK)
				opt.Log.Debug("skipping unrecognized file", zap.String("filename", fname))
				return nil
			}

			// if this is a sidecar video for a photograph (a motion picture or live photo),
			// skip it, since we'll come back to this when we get to the photograph file itself
			if fpath != "." { // (not really possible to be a sidecar if only importing a single file)
				if IsSidecarVideo(fsys, itemPath) {
					return nil
				}
			}

			throttle <- struct{}{}
			wg.Add(1)

			go func() {
				defer func() {
					<-throttle
					wg.Done()
				}()

				item := &timeline.Item{
					Classification:       class,
					Owner:                owner,
					IntermediateLocation: itemPath,
					Content: timeline.ItemData{
						Filename: d.Name(),
						Data: func(_ context.Context) (io.ReadCloser, error) {
							return fsys.Open(fpath)
						},
					},
				}

				// get as much metadata as possible
				pic, err := ExtractAllMetadata(opt.Log, fsys, fpath, item, timeline.MetaMergeAppend)
				if err != nil {
					opt.Log.Warn("extracting metadata",
						zap.String("file", fpath),
						zap.Error(err))
				}

				// try to extract time from filepath (media files are often organized by date)
				if dsOpt.UseFilePathTime {
					fpathTimestamp, err := generic.TimestampFromFilePath(fpath)
					if err == nil {
						// use the filepath timestamp if the embedded timestamp is missing OR
						// the embedded timestamp is not within the specified range but the
						// filepath timestamp IS within range
						if item.Timestamp.IsZero() ||
							(!dsOpt.DateRange.Contains(item.Timestamp) && dsOpt.DateRange.Contains(fpathTimestamp)) {
							item.Timestamp = fpathTimestamp
						}
					}
				}

				// as a last resort, use file modification time as a timestamp; I've seen this
				// be accurate for downloads from Google Photos where the timestamp is otherwise
				// missing: common with iPhone videos, gifs/animations, and other "creations"
				if dsOpt.UseFileModTime {
					info, err := d.Info()
					if err == nil {
						// use the modtime if the timestamp is still missing OR if
						// the preferred timestamp is not within the specified range
						// and this one is
						modTime := info.ModTime()
						if item.Timestamp.IsZero() ||
							(!dsOpt.DateRange.Contains(item.Timestamp) && dsOpt.DateRange.Contains(modTime)) {
							item.Timestamp = modTime
						}
					}
				}

				// Media items are often manually organized and data that was originally
				// not digital (like scanned photos) might not have an exact date or time;
				// thus we can set a timeframe for these items, so instead of an item
				// being on a certain day at precisely midnight 00:00, we can mark it as
				// during that day, without certainty as to what time.
				item.SetTimeframe()

				ig := &timeline.Graph{Item: item}

				// if picture is attached, create related item for that
				if pic != nil && len(pic.Data) > 0 {
					embeddedPicName := "embedded" // you have a better idea?
					if pic.Ext != "" {
						embeddedPicName += "." + pic.Ext
					}

					picItem := &timeline.Item{
						Classification:       timeline.ClassMedia,
						Owner:                owner,
						IntermediateLocation: path.Join(itemPath, embeddedPicName), // this works, I guess? Not sure if it should be same as parent file as that might be confusing?
						Content: timeline.ItemData{
							Filename:  embeddedPicName,
							MediaType: pic.MIMEType,
							Data:      timeline.ByteData(pic.Data),
						},
						Metadata: timeline.Metadata{
							"Description": pic.Description,
							"Type":        pic.Type,
						},
					}

					// TODO: UI needs to support this
					ig.ToItem(RelCoverArt, picItem)
				}

				ConnectMotionPhoto(opt.Log, fsys, itemPath, ig)

				// now assemble collection info

				collectionsMu.Lock()
				defer collectionsMu.Unlock()

				// if item is part of an album according to metadata, add it to the collection
				if albumStr, ok := item.Metadata["Album"].(string); ok && albumStr != "" {
					collName := albumStr
					if albumYear, ok := item.Metadata["Year"].(int); ok && albumYear > 0 {
						collName += fmt.Sprintf(" (%d)", albumYear)
					}
					coll, ok := collections[collName]
					if !ok {
						coll = &timeline.Item{
							Classification: timeline.ClassCollection,
							Content: timeline.ItemData{
								Data: timeline.StringData(collName),
							},
						}
					}

					// we can only be certain of the position of the track in the album if a track number
					// is available AND this is disc 1 (that is most common, so by default, we assume there
					// is just 1 disc, if unspecified)
					var position any
					if trackNum, ok := item.Metadata["Track"].(int); ok && trackNum > 0 {
						if disc, ok := item.Metadata["Disc"].(int); !ok || (disc == 0 || disc == 1) {
							position = trackNum
						}
					}

					ig.ToItemWithValue(timeline.RelInCollection, coll, position)

					collections[collName] = coll
				}

				// if enabled, add item to collection based on parent folder name
				if dsOpt.FolderIsAlbum {
					coll, ok := collections[fpathDir]
					if !ok {
						coll = &timeline.Item{
							Classification: timeline.ClassCollection,
							Content: timeline.ItemData{
								Data: timeline.StringData(fpathDir),
							},
							Owner: owner,
						}
					}
					ig.ToItem(timeline.RelInCollection, coll)
					collections[fpathDir] = coll
				}

				itemChan <- ig
			}()

			return nil
		})
		if err != nil {
			return err
		}

		wg.Wait()

		// TODO: process the collections too (we still need to upgrade this logic after the schema rewrite)

	}

	return nil
}

// ItemClassByExtension uses the file extension to return a best-guess item classification.
// It returns false if no matching classification could be found for the file extension.
func ItemClassByExtension(filename string) (timeline.Classification, bool) {
	ext := strings.ToLower(path.Ext(filename))
	if _, ok := imageExts[ext]; ok {
		return timeline.ClassMedia, true
	}
	if _, ok := videoExts[ext]; ok {
		return timeline.ClassMedia, true
	}
	if _, ok := audioExts[ext]; ok {
		return timeline.ClassMedia, true
	}
	return timeline.Classification{}, false
}

// Extensions used for file recognition.
var (
	imageExts = map[string]struct{}{
		".bmp":  {},
		".cr2":  {}, // x-canon-cr2 or x-dcraw
		".crw":  {}, // x-canon-crw
		".dcr":  {}, // x-kodak-dcr
		".dng":  {}, // x-adobe-dng
		".erf":  {}, // x-epson-erf
		".gif":  {},
		".heic": {},
		".heif": {},
		".hif":  {}, // fujifilm's heif extension
		".jpeg": {},
		".jpe":  {},
		".jpg":  {},
		".k25":  {}, // x-kodak-x25
		".kdc":  {}, // x-kodak-kdc
		".nef":  {}, // x-nikon-nef
		".orf":  {}, // x-olympus-orf
		".pbm":  {},
		".pef":  {}, // x-pentax-pef
		".pgm":  {},
		".png":  {},
		".pnm":  {},
		".ppm":  {},
		".raf":  {}, // x-fuji-raf
		".raw":  {}, // x-panasonic-raw - most likely an image, but could be anything
		".sr2":  {}, // x-sony-sr2
		".srf":  {}, // x-sony-srf
		".svg":  {},
		".tiff": {},
		".webp": {},
	}

	videoExts = map[string]struct{}{
		".3g2":  {},
		".3gp":  {},
		".3gpp": {},
		".asf":  {},
		".avi":  {},
		".divx": {},
		".flv":  {},
		".m2t":  {},
		".m2ts": {},
		".m4v":  {},
		".mkv":  {},
		".mov":  {},
		".mp":   {}, // Google Photos / Pixel phones motion pictures... sigh
		".mp4":  {},
		".mpeg": {},
		".mpg":  {},
		".mts":  {},
		".vob":  {},
		".wmv":  {},
	}

	audioExts = map[string]struct{}{
		".aa":   {},
		".aac":  {},
		".aax":  {},
		".aiff": {},
		".alac": {},
		".au":   {},
		".flac": {},
		".m4a":  {},
		".m4b":  {},
		".m4p":  {},
		".mogg": {},
		".mp3":  {},
		".oga":  {},
		".ogg":  {},
		".wav":  {},
		".wma":  {},
	}
)

var (
	// "<to> is a motion photo (aka 'live photo' or video) of <from>"
	RelMotionPhoto = timeline.Relation{Label: "motion", Directed: true, Subordinating: true}

	// "<to> is the album art for <from>"
	RelCoverArt = timeline.Relation{Label: "cover_art", Directed: true}
)
