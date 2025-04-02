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
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/maruel/natural"
	"github.com/timelinize/timelinize/datasources/media"
	"github.com/timelinize/timelinize/timeline"
	"go.uber.org/zap"
)

const googlePhotosPath = "Takeout/Google Photos"

func (fimp *FileImporter) listFromTakeoutArchive(ctx context.Context, opt timeline.ImportParams, dirEntry timeline.DirEntry) error {
	fimp.truncatedNames = make(map[string]int)

	albumFolders, err := fs.ReadDir(dirEntry.FS, dirEntry.Filename)
	if err != nil {
		return fmt.Errorf("getting album list from %s: %w", googlePhotosPath, err)
	}

	// We don't use Walk() because we need to control the order in which we read
	// the files. It's quite niche, but I ran into it with my very first import
	// test: filenames that are more than 47 characters, where the first 47 chars
	// are all the same, are ambiguous when it comes to pairing the media file and
	// the metadata sidecar file (.json), because Google truncates long filenames for
	// some reason without an obvious way to undo the truncation deterministically.
	// Before truncating, Google apparently sorts filenames in a folder by "natural
	// sort", but Walk uses lexical sort. So we read the dir listings ourselves and
	// sort album contents with a natural sort in order and remember truncated file
	// names we've seen in order to hopefully accurately link a JSON file to its
	// associated media file, and thus generate the same retrieval key for both
	// files. This is needed because we can't be guaranteed that the media file and
	// its sidecar will even be in the same archive/import; so the retrieval key
	// lets us import partial item data as we discover it, but it HAS to be the
	// same, and we use the filename for that, so we HAVE to reliably compute it.
	for _, albumFolder := range albumFolders {
		if err := ctx.Err(); err != nil {
			return err
		}

		thisAlbumFolderPath := path.Join(dirEntry.Filename, albumFolder.Name())

		albumMeta, err := fimp.readAlbumMetadata(dirEntry, thisAlbumFolderPath)
		if err != nil {
			opt.Log.Error("could not open album metadata (maybe it is in another archive?)", zap.Error(err))
		}

		// read album folder contents, then sort in what I think is the same way
		// Google does before truncating long filenames
		albumItems, err := fs.ReadDir(dirEntry.FS, thisAlbumFolderPath)
		if err != nil {
			return err
		}
		sort.Slice(albumItems, func(i, j int) bool {
			iName, jName := albumItems[i].Name(), albumItems[j].Name()
			iNameNoExt, jNameNoExt := strings.TrimSuffix(iName, path.Ext(iName)), strings.TrimSuffix(jName, path.Ext(jName))
			// first sort by length
			if len(iNameNoExt) != len(jNameNoExt) {
				return len(iNameNoExt) < len(jNameNoExt)
			}
			// then use natural sort; i.e. [a1, a20, a10] => [a1, a10, a20]
			return natural.Less(albumItems[i].Name(), albumItems[j].Name())
		})

		for _, d := range albumItems {
			if err := fimp.processAlbumItem(ctx, albumMeta, thisAlbumFolderPath, d, opt, dirEntry); err != nil {
				return fmt.Errorf("processing album item '%s': %w", path.Join(thisAlbumFolderPath, dirEntry.Name()), err)
			}
		}
	}

	return nil
}

func (fimp *FileImporter) processAlbumItem(ctx context.Context, albumMeta albumArchiveMetadata, folderPath string, d fs.DirEntry, opt timeline.ImportParams, dirEntry timeline.DirEntry) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// skip the album metadata
	// TODO: Also skip/use print-subscriptions.json, shared_album_comments.json, user-generated-memory-titles.json,... I guess? I haven't seen those though
	if d.Name() == albumMetadataFilename {
		return nil
	}

	// skip directories (there shouldn't be any in the first place... since Google Photos doesn't support sub-albums)
	if d.IsDir() {
		return nil
	}

	fpath := path.Join(folderPath, d.Name())
	f, err := dirEntry.FS.Open(fpath)
	if err != nil {
		return err
	}
	defer f.Close()

	var itemMeta mediaArchiveMetadata

	// this could be either the media file itself, or a metadata sidecar file; we
	// need the path to the media file, so start by assuming that's what this is
	mediaFilePath := fpath

	// if this is a JSON sidecar file, get the metadata it contains
	if path.Ext(fpath) == ".json" {
		err = json.NewDecoder(f).Decode(&itemMeta)
		if err != nil {
			return fmt.Errorf("decoding item metadata file %s: %w", fpath, err)
		}

		// I've heard that some JSON files in albums (other than the album metadata)
		// might be something else, so as a quick sanity check make sure it contained
		// what I presume is required info
		if itemMeta.Title == "" || itemMeta.URL == "" {
			return nil
		}

		// we don't totally trust the timstamp in the metadata file, but we'll
		// take it in case the actual media file doesn't contain any
		itemMeta.parsedPhotoTakenTime, err = itemMeta.timestamp()
		if err != nil && !errors.Is(err, errNoTimestamp) {
			return fmt.Errorf("parsing timestamp from item %s: %w", fpath, err)
		}

		// ensure item is within configured timeframe before continuing
		if !opt.Timeframe.Contains(itemMeta.parsedPhotoTakenTime) {
			opt.Log.Debug("item is outside timeframe", zap.String("filename", fpath))
			return nil
		}

		mediaFilePath = fimp.determineMediaFilenameInArchive(dirEntry, fpath, itemMeta)
		opt.Log.Debug("mapped sidecar to target media file",
			zap.String("sidecar_file", fpath),
			zap.String("target_file", mediaFilePath))
	} else {
		itemMeta.source = dirEntry
	}

	ig := fimp.makeItemGraph(mediaFilePath, itemMeta, albumMeta, opt)

	// tell the processor we prefer embedded metadata rather than sidecar info
	// (except: the sidecar file likely contains the full/original name of the file,
	// which may have been truncated after a certain length, so we prefer the filename
	// from the sidecar file)
	if path.Ext(fpath) != ".json" {
		ig.Item.Retrieval.PreferFields = []string{"data", "original_location", "intermediate_location", "timestamp", "timespan", "timeframe", "time_offset", "time_uncertainty", "location"}
	} else {
		ig.Item.Retrieval.PreferFields = []string{"filename"}
	}

	// if item has an "-edited" variant, relate it
	ext := path.Ext(mediaFilePath)
	editedPath := strings.TrimSuffix(mediaFilePath, ext) + "-edited" + ext
	if timeline.FileExistsFS(dirEntry.FS, path.Join(dirEntry.Filename, editedPath)) {
		mediaFilePath = editedPath
		edited := fimp.makeItemGraph(mediaFilePath, itemMeta, albumMeta, opt)
		ig.ToItem(timeline.RelEdit, edited.Item)
	}

	opt.Pipeline <- ig

	return nil
}

func (fimp *FileImporter) makeItemGraph(mediaFilePath string, itemMeta mediaArchiveMetadata, albumMeta albumArchiveMetadata, opt timeline.ImportParams) *timeline.Graph {
	item := &timeline.Item{
		Classification: timeline.ClassMedia,
		// timestamp is not set here (we prefer timestamp embedded in file itself first, below)
		Location:             itemMeta.location(),
		IntermediateLocation: mediaFilePath,
		Content: timeline.ItemData{
			Filename: itemMeta.Title,
		},
		Metadata: timeline.Metadata{
			"Description":  itemMeta.Description,
			"Local folder": itemMeta.GooglePhotosOrigin.MobileUpload.DeviceFolder.LocalFolderName,
			"Device type":  itemMeta.GooglePhotosOrigin.MobileUpload.DeviceType,
			"Views":        itemMeta.ImageViews,
			"URL":          itemMeta.URL,
		},
	}
	if itemMeta.source.FS != nil {
		if item.Content.Filename == "" {
			// if we're on the actual media file itself, and not the metadata file,
			// we are still likely to get the filename (mostly) right if we use the
			// filename, AFAIK it only changes if it gets truncated for length, in
			// which case, the DB row will be updated to the correct filename when
			// we do eventually ready the sidecar file, even if the filename in the
			// repo won't be updated, oh well, that's less visible, as we almost
			// always use the filename in the DB row when showing the filename
			item.Content.Filename = path.Base(mediaFilePath)
		}
		item.Content.Data = func(_ context.Context) (io.ReadCloser, error) {
			return itemMeta.source.FS.Open(path.Join(itemMeta.source.Filename, mediaFilePath))
		}

		// add metadata contained in the image file itself; note that this overwrites any overlapping
		// metadata that has already been filled in -- except timestamp which is not in the Metadata
		// field; however, apparently (according to the PhotoStructure devs), the timestamp in the
		// actual photo file is often more accurate than any in a sidecar metadata file, so prefer
		// the embedded timestamp first, and if there isn't one, then use the sidecar data
		_, err := media.ExtractAllMetadata(opt.Log, itemMeta.source.FS, path.Join(itemMeta.source.Filename, mediaFilePath), item, timeline.MetaMergeReplaceEmpty)
		if err != nil {
			opt.Log.Warn("extracting metadata", zap.Error(err))
		}
	}

	// the retrieval key is crucial so that we can store what data we have from an item
	// as we get it, without getting the whole item, even across different imports; it
	// consists of the data source name to avoid conflicts with other DSes, the name of
	// the archive (with the index part remove, of course, since a metadata file in
	// -001.zip might have its media file in -002.zip, but they should have the same
	// retrieval key; this does rely on them not being renamed), and the expected path
	// of the media file within the archive (if we're on the media file, it's just that
	// path, but if we're on the sidecar JSON file, we have to construct it with heuristics
	// since Google's naming convention isn't documented)
	archiveName := fimp.archiveFilenameWithoutPositionPart()
	retKey := fmt.Sprintf("%s::%s::%s", dataSourceName, archiveName, mediaFilePath)
	item.Retrieval.SetKey(retKey)

	ig := &timeline.Graph{Item: item}

	// add to album/collection
	if albumMeta.Title != "" || albumMeta.Description != "" {
		// prefer title, but use description if that's all we have for some reason
		albumTitle := albumMeta.Title
		if albumTitle == "" {
			albumTitle = albumMeta.Description
			albumMeta.Description = ""
		}
		ig.ToItem(timeline.RelInCollection, &timeline.Item{
			Classification: timeline.ClassCollection,
			Content: timeline.ItemData{
				Data: timeline.StringData(albumTitle),
			},
			Owner: item.Owner,
			Metadata: timeline.Metadata{
				"Description": albumMeta.Description,
			},
		})
	}

	for _, person := range itemMeta.People {
		ig.ToEntity(timeline.RelDepicts, &timeline.Entity{
			Name: person.Name,
			Attributes: []timeline.Attribute{
				{
					Name:     "google_photos_name",
					Value:    person.Name,
					Identity: true,
				},
			},
		})
	}

	return ig
}

// archiveFilenameWithoutPositionPart returns the name of the archive without the
// positional index and without the extension. It assumes a Takeout archive filename
// that has not been renamed, for example: "takeout-20240516T230250Z-003.zip", this
// returns "takeout-20240516T230250Z". This is useful for identifying which export
// an archive belongs to. (The filename is not strictly parsed; it quite naively
// just uses the name up to the last dash, as long as whatever is before the last dash
// is the same for all archives in the group.)
func (fimp *FileImporter) archiveFilenameWithoutPositionPart() string {
	// For "/foo/takeout-20240516T230250Z-003.zip/Takeout/Google Photos", strip the
	// "Takeout/Google Photos" suffix to terminate the path at the root of the archive
	base := filepath.Base(strings.TrimSuffix(fimp.filename, googlePhotosPath))
	lastDashPos := strings.LastIndex(base, "-")
	if lastDashPos <= 0 {
		return base
	}
	return base[:lastDashPos]
}

func (fimp *FileImporter) readAlbumMetadata(d timeline.DirEntry, albumFolderPath string) (albumArchiveMetadata, error) {
	albumMetadataFilePath := path.Join(d.Filename, albumFolderPath, albumMetadataFilename)
	albumMetadataFile, err := d.FS.Open(albumMetadataFilePath)
	if err != nil {
		return albumArchiveMetadata{}, fmt.Errorf("opening metadata file %s: %w", albumMetadataFilename, err)
	}
	defer albumMetadataFile.Close()

	var albumMeta albumArchiveMetadata
	err = json.NewDecoder(albumMetadataFile).Decode(&albumMeta)
	if err != nil {
		return albumArchiveMetadata{}, fmt.Errorf("decoding album metadata file %s: %w", albumMetadataFilename, err)
	}

	return albumMeta, nil
}

const albumMetadataFilename = "metadata.json"

type albumArchiveMetadata struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	Access      string `json:"access"`
	Date        struct {
		Timestamp string `json:"timestamp"`
		Formatted string `json:"formatted"`
	} `json:"date"`
	GeoData struct {
		Latitude      float64 `json:"latitude"`
		Longitude     float64 `json:"longitude"`
		Altitude      float64 `json:"altitude"`
		LatitudeSpan  float64 `json:"latitudeSpan"`
		LongitudeSpan float64 `json:"longitudeSpan"`
	} `json:"geoData"`
	SharedAlbumComments []struct {
		Text         string `json:"text,omitempty"`
		CreationTime struct {
			Timestamp string `json:"timestamp"`
			Formatted string `json:"formatted"`
		} `json:"creationTime"`
		ContentOwnerName string `json:"contentOwnerName"`
		Liked            bool   `json:"liked,omitempty"`
	} `json:"sharedAlbumComments"`
}

type mediaArchiveMetadata struct {
	Title        string `json:"title"`
	Description  string `json:"description"`
	ImageViews   string `json:"imageViews"`
	CreationTime struct {
		Timestamp string `json:"timestamp"`
		Formatted string `json:"formatted"`
	} `json:"creationTime"`
	PhotoTakenTime struct {
		Timestamp string `json:"timestamp"`
		Formatted string `json:"formatted"`
	} `json:"photoTakenTime"`
	GeoData struct {
		Latitude      float64 `json:"latitude"`
		Longitude     float64 `json:"longitude"`
		Altitude      float64 `json:"altitude"`
		LatitudeSpan  float64 `json:"latitudeSpan"`
		LongitudeSpan float64 `json:"longitudeSpan"`
	} `json:"geoData"`
	GeoDataExif struct {
		Latitude      float64 `json:"latitude"`
		Longitude     float64 `json:"longitude"`
		Altitude      float64 `json:"altitude"`
		LatitudeSpan  float64 `json:"latitudeSpan"`
		LongitudeSpan float64 `json:"longitudeSpan"`
	} `json:"geoDataExif"`
	People []struct {
		Name string `json:"name"`
	} `json:"people"`
	URL                string `json:"url"`
	GooglePhotosOrigin struct {
		MobileUpload struct {
			DeviceFolder struct {
				LocalFolderName string `json:"localFolderName"`
			} `json:"deviceFolder"`
			DeviceType string `json:"deviceType"`
		} `json:"mobileUpload"`
	} `json:"googlePhotosOrigin"`
	PhotoLastModifiedTime struct {
		Timestamp string `json:"timestamp"`
		Formatted string `json:"formatted"`
	} `json:"photoLastModifiedTime"`

	parsedPhotoTakenTime time.Time
	source               timeline.DirEntry // the parent DirEntry (not representing the actual file itself; the one we're starting the import from)
}

func (m mediaArchiveMetadata) location() timeline.Location {
	loc := timeline.Location{}
	if m.GeoData.Latitude != 0 {
		loc.Latitude = &m.GeoData.Latitude
	}
	if m.GeoData.Longitude != 0 {
		loc.Longitude = &m.GeoData.Longitude
	}
	if m.GeoData.Altitude != 0 {
		loc.Altitude = &m.GeoData.Altitude
	}
	if loc.Latitude == nil && m.GeoDataExif.Latitude != 0 {
		loc.Latitude = &m.GeoDataExif.Latitude
	}
	if loc.Longitude == nil && m.GeoDataExif.Longitude != 0 {
		loc.Longitude = &m.GeoDataExif.Longitude
	}
	if loc.Altitude == nil && m.GeoDataExif.Altitude != 0 {
		loc.Altitude = &m.GeoDataExif.Altitude
	}
	return loc
}

var errNoTimestamp = errors.New("no timestamp available")

// timestamp returns a timestamp derived from the metadata. It first
// prefers the PhotoTakenTime, then the CreationTime, then the
// PhotoLastModifiedTime. However, it has been reported by the
// PhotoStructure team that these timestamps can be wildly wrong,
// on the order of hours or days. Image metadata may be more reliable.
func (m mediaArchiveMetadata) timestamp() (time.Time, error) {
	ts := m.PhotoTakenTime.Timestamp
	if ts == "" {
		ts = m.CreationTime.Timestamp
	}
	if ts == "" {
		ts = m.PhotoLastModifiedTime.Timestamp
	}
	if ts == "" {
		return time.Time{}, errNoTimestamp
	}
	parsed, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return time.Time{}, err
	}
	return time.Unix(parsed, 0), nil
}

// determineMediaFilenameInArchive returns the path to the media file in the archive
// that is associated with the given JSON sidecar metadata filepath.
//
// Google Photos export truncates long filenames. This function uses a lexical approach
// with the help of some count state to assemble the image filename that can be used to
// read it in the archive.
func (fimp *FileImporter) determineMediaFilenameInArchive(dirEntry timeline.DirEntry, jsonFilePath string, itemMeta mediaArchiveMetadata) string {
	// target media file will be in the same directory
	dir := path.Dir(jsonFilePath)

	// the metadata contains the original filename; we use that to compute
	// what we hope is the filename in the archive based on... experience
	// (none of this is documented, but there's some writeups at TODO: link...)
	titleExt := path.Ext(itemMeta.Title)
	transformedTitle := strings.ReplaceAll(itemMeta.Title, "&", "_")
	transformedTitle = strings.ReplaceAll(transformedTitle, "?", "_")
	titleWithoutExt := strings.TrimSuffix(transformedTitle, titleExt)

	// Google truncates filenames longer than this (sans extension)
	const truncateAt = 47

	// if the filename is long enough, Google truncates it, so we need
	// to reconstruct it; this depends on the order we're reading the files,
	// because Google auto-increments a "uniqueness suffix" in the form of
	// "(N)" where N is how many times that truncated filename has already
	// appeared before this.
	if len(titleWithoutExt) > truncateAt {
		truncatedTitle := titleWithoutExt[:truncateAt]
		truncatedTitleWithDir := path.Join(dir, truncatedTitle)
		fullTruncatedName := truncatedTitleWithDir + titleExt

		// if the truncated filename already exists, then we need to count that as the first "hit"
		if fimp.truncatedNames[fullTruncatedName] == 0 && timeline.FileExistsFS(dirEntry.FS, path.Join(dirEntry.Filename, fullTruncatedName)) {
			fimp.truncatedNames[fullTruncatedName]++
		}

		// then count this "hit" for the name
		fimp.truncatedNames[fullTruncatedName]++

		// now read the count; it will be at least 1
		seenCount := fimp.truncatedNames[fullTruncatedName]

		// a uniqueness suffix is only inserted (before the extension) if the
		// truncated filename has not already been seen in our walk, so if this
		// is the first (or only) occurrence, just return the truncated filename
		if seenCount == 1 {
			return fullTruncatedName
		}

		// otherwise, insert the uniqueness suffix between the truncated filename and
		// the extension; use seenCount-1 because the first instance doesn't have a
		// "uniqueness suffix (N)", the second one has "(1)", third has "(2)", etc;
		// it's how many times we've *already* seen this name before this
		return fmt.Sprintf("%s(%d)%s", truncatedTitleWithDir, seenCount-1, titleExt)
	}

	// short filenames are great... so simple (I think)
	return path.Join(dir, itemMeta.Title)
}
