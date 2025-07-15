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

	var checkpoint string
	if opt.Checkpoint != nil {
		err := json.Unmarshal(opt.Checkpoint, &checkpoint)
		if err != nil {
			return fmt.Errorf("decoding checkpoint: %w", err)
		}
	}

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
			if errors.Is(err, fs.ErrNotExist) {
				opt.Log.Warn("album metadata not found; maybe it is in another archive or this folder is not an album",
					zap.String("folder_path", thisAlbumFolderPath),
					zap.Error(err))
			} else {
				opt.Log.Error("could not open album metadata",
					zap.String("folder_path", thisAlbumFolderPath),
					zap.Error(err))
			}
		}

		// read album folder contents, then sort in what I think is the same way
		// Google does before truncating long filenames -- this is crucial to
		// matching up filenames correctly (metadata + media files)
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
			// make pauses more responsive
			if err := opt.Continue(); err != nil {
				return err
			}

			fpath := path.Join(thisAlbumFolderPath, dirEntry.Name())
			if checkpoint != "" {
				if fpath != checkpoint {
					continue // keep going until we find the checkpoint position
				}
				checkpoint = "" // at the checkpoint; clear it so we process all further items
			}
			if err := fimp.processAlbumItem(ctx, albumMeta, thisAlbumFolderPath, d, opt, dirEntry); err != nil {
				return fmt.Errorf("processing album item '%s': %w", fpath, err)
			}
		}
	}

	return nil
}

func (fimp *FileImporter) processAlbumItem(ctx context.Context, albumMeta albumArchiveMetadata, folderPath string, d fs.DirEntry, opt timeline.ImportParams, dirEntry timeline.DirEntry) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// skip the album metadata (it is consumed separately)
	// TODO: Also skip/use print-subscriptions.json, shared_album_comments.json, user-generated-memory-titles.json,... I guess? I haven't seen those though
	if d.Name() == albumMetadataFilename {
		return nil
	}

	// skip directories (there shouldn't be any in the first place... since Google Photos doesn't support sub-albums)
	if d.IsDir() {
		return nil
	}

	fpath := path.Join(folderPath, d.Name())

	// skip sidecar movie files ("live photos") because we'll connect them when
	// we process the actual photograph (hopefully they're in the same archive!)
	if media.IsSidecarVideo(dirEntry.FS, fpath) {
		return nil
	}

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

		mediaFilePath = fimp.determineMediaFilenameInArchive(fpath, itemMeta)
		opt.Log.Debug("mapped sidecar to target media file",
			zap.String("sidecar_file", fpath),
			zap.String("target_file", mediaFilePath))
	} else {
		itemMeta.source = dirEntry
	}

	ig := fimp.makeItemGraph(mediaFilePath, itemMeta, albumMeta, opt)

	// ensure item is within configured timeframe before continuing
	if !opt.Timeframe.Contains(ig.Item.Timestamp) {
		opt.Log.Debug("item is outside timeframe", zap.String("filename", fpath))
		return nil
	}

	// Between the JSON file and the actual media file, we typically prefer the
	// filename in the JSON file and everything else that overlaps in the media
	// file, since Google's metadata is known to be wrong sometimes (!?). However,
	// in a rare singular case of corrupted input, I have found non-nil timestamp
	// data that was completely wrong in the mvhd box of an MP4 file, captured on
	// an Android phone, with several other videos even that same hour that were
	// correct / not corrupted. The corrupted timestamp was 4165689599 (confirmed
	// via ffprobe), which apparently equates to 2036-01-01, but should have been
	// 2016-11-27. (The time was also truncated.) I can't explain the corruption.
	// I think in general, photos and videos from Google Takeout aren't from the
	// future, and probably aren't RIGHT at midnight on New Years (okay to be fair,
	// that's not so unlikely) -- maybe we can prefer the metadata timestamp in
	// those cases; though I'm not sure if this heuristic is reliable.
	if path.Ext(fpath) == ".json" {
		// metadata file should have good filename and metadata, but we prefer
		// the embedded timestamp if possible
		ig.Item.Retrieval.FieldUpdatePolicies = map[string]timeline.FieldUpdatePolicy{
			"filename":         timeline.UpdatePolicyOverwriteExisting,
			"metadata":         timeline.UpdatePolicyPreferIncoming, // applied per-key, so keys unique to this file will be kept
			"timestamp":        timeline.UpdatePolicyPreferExisting,
			"timespan":         timeline.UpdatePolicyPreferExisting,
			"timeframe":        timeline.UpdatePolicyPreferExisting,
			"time_offset":      timeline.UpdatePolicyPreferExisting,
			"time_uncertainty": timeline.UpdatePolicyPreferExisting,
			"latlon":           timeline.UpdatePolicyPreferExisting,
			"altitude":         timeline.UpdatePolicyPreferExisting,
		}
	} else {
		// always use the embedded timestamp, unless it looks like it is bad (I've encountered
		// several corrupt or very wrong embedded timestamps that actually cause UI bugs b/c
		// they're so wrong they can't be serialized to JSON) -- the processor will also try to
		// clear them, but in our case there are timestamps that generically "look valid", yet
		// we can know are invalid, and in those cases we can likely lean on the timestamp in
		// the JSON file, so we just need to adjust the update policy for timestamps based on
		// what we can infer about the timestamp
		tsUpdatePolicy := timeline.UpdatePolicyOverwriteExisting
		if isBadTimestamp(ig.Item.Timestamp) {
			tsUpdatePolicy = timeline.UpdatePolicyKeepExisting
			ig.Item.Timestamp = time.Time{}
		}
		ig.Item.Retrieval.FieldUpdatePolicies = map[string]timeline.FieldUpdatePolicy{
			"data":                  timeline.UpdatePolicyOverwriteExisting,
			"original_location":     timeline.UpdatePolicyOverwriteExisting,
			"intermediate_location": timeline.UpdatePolicyOverwriteExisting,
			"filename":              timeline.UpdatePolicyPreferExisting,
			"metadata":              timeline.UpdatePolicyPreferIncoming,
			"timestamp":             tsUpdatePolicy,
			"timespan":              tsUpdatePolicy,
			"timeframe":             tsUpdatePolicy,
			"time_offset":           tsUpdatePolicy,
			"time_uncertainty":      tsUpdatePolicy,
			"latlon":                timeline.UpdatePolicyPreferIncoming,
			"altitude":              timeline.UpdatePolicyPreferIncoming,
		}

		media.ConnectMotionPhoto(opt.Log, dirEntry, mediaFilePath, ig)
	}

	// if item has an "-edited" variant, relate it
	ext := path.Ext(mediaFilePath)
	editedPath := strings.TrimSuffix(mediaFilePath, ext) + "-edited" + ext
	if dirEntry.FileExists(editedPath) {
		mediaFilePath = editedPath
		edited := fimp.makeItemGraph(mediaFilePath, itemMeta, albumMeta, opt)
		ig.ToItem(timeline.RelEdit, edited.Item)
	}

	ig.Checkpoint = fpath

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
			// we do eventually read the sidecar file (the data file name in the repo
			// will still have this name even after the DB row gets updated, but that
			// name is internal-only for the most part)
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

	// set a timestamp if we only have the metadata file
	if item.Timestamp.IsZero() {
		item.Timestamp = itemMeta.parsedPhotoTakenTime
	}

	// the retrieval key is crucial so that we can store what data we have from an item
	// as we get it, without getting the whole item, even across different imports; it
	// consists of the data source name to avoid conflicts with other DSes, the name of
	// the archive (with the index part removed, of course, since a metadata file in
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
		ig.ToEntity(timeline.RelIncludes, &timeline.Entity{
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
		Composition struct {
			Type string `json:"type"`
		} `json:"composition"`
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
		// if a photo is in multiple albums/folders, this can be different between the two
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
func (fimp *FileImporter) determineMediaFilenameInArchive(jsonFilePath string, itemMeta mediaArchiveMetadata) string {
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

// isBadTimestamp tries to detect timestamps that are bad/corrupted, which would generally come from
// embedded metadata like EXIF or XMP, where either there is a parser bug or actual corruption. I have
// encountered both on my data sets, and I've encountered these specific situations.
// The processor will actually strip timestamps that are invalid (like, year is super out-of-range and
// can't be serialized by JSON), but in the case of a corrupt offset (TZ), it will only strip the offset;
// but in our case we can do better than that probably, since the sidecar json file usually has a valid
// and correct timestamp in the rare case the EXIF/XMP data is wrong. So we want to prefer the timestamp
// from the JSON when we detect a timestamp that the processor may still consider valid, but which we
// assume is probably wrong. For example: future year, exactly midnight on new years, or corrupted
// offset. In these cases, the timestamp from JSON should be preferred. In order to prefer the JSON
// timestamp, we need to clear any bad, embedded timestamp, since otherwise it will be preferred.
func isBadTimestamp(t time.Time) bool {
	futureYear := t.Year() > time.Now().Year()
	exactlyMidnightOnNewYears := t.Month() == time.January && t.Day() == 1 && t.Hour() == 0 && t.Minute() == 0 && t.Second() == 0

	const maxTimezoneOffsetSecFromUTC = 50400 // most distant time zone from UTC is apparently +-14 hours
	_, offsetSec := t.Zone()
	offsetCorrupted := offsetSec > maxTimezoneOffsetSecFromUTC || offsetSec < -maxTimezoneOffsetSecFromUTC

	return t.IsZero() || futureYear || exactlyMidnightOnNewYears || offsetCorrupted
}
