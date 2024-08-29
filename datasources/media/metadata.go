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

package media

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/abema/go-mp4"
	"github.com/dhowden/tag"
	"github.com/mholt/goexif2/exif"
	"github.com/mholt/goexif2/mknote"
	"github.com/mholt/goexif2/tiff"
	"github.com/timelinize/timelinize/timeline"
	"github.com/trimmer-io/go-xmp/xmp"
	"go.uber.org/zap"
)

func init() {
	exif.RegisterParsers(mknote.All...)
}

type exifWalkerFunc func(exif.FieldName, *tiff.Tag) error

func (w exifWalkerFunc) Walk(name exif.FieldName, tag *tiff.Tag) error {
	return w(name, tag)
}

// ExtractAllMetadata reads the file at the given path in the given file system and tries to extract any and all metadata
// it can find. The metadata will be used to set properties on the item including Timestamp, Location, and the Metadata map.
// If an embedded image is found, it will be returned.
func ExtractAllMetadata(logger *zap.Logger, fsys fs.FS, path string, item *timeline.Item, policy timeline.MetadataMergePolicy) (*tag.Picture, error) {
	logger = logger.With(zap.String("filepath", path))

	file, err := fsys.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	// prepare to read the file; seeking ability is required
	var fileSeeker io.ReadSeeker
	if seeker, ok := file.(io.ReadSeeker); ok {
		fileSeeker = seeker
	} else {
		// TODO: I have not tested this code yet, but:
		// since this isn't a seekable reader, we have to make it seekable by reading it
		// into memory; obviously this is problematic if the file is large, but even if
		// it is, we can hope that the metadata is at the beginning (sometimes it is at
		// the end; but usually it is at the beginning)...
		const maxBufferSize = 1024 * 1024 * 50

		buf := bufPool.Get().(*bytes.Buffer)
		buf.Reset()
		defer bufPool.Put(buf)

		lr := io.LimitReader(file, maxBufferSize)

		if _, err := io.Copy(buf, lr); err != nil {
			return nil, err
		}

		fileSeeker = bytes.NewReader(buf.Bytes())
	}

	// EXIF
	meta, err := extractEXIFMetadata(logger, fileSeeker, item)
	if err != nil {
		logger.Warn("processing EXIF metadata", zap.Error(err))
	}
	item.AddMetadata(meta, policy)
	if _, err = fileSeeker.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("could not rewind file after EXIF: %w", err)
	}

	// XMP
	meta, err = extractXMPMetadata(logger, fileSeeker)
	if err != nil {
		logger.Warn("processing XMP metadata", zap.Error(err))
	}
	item.AddMetadata(meta, policy)
	if _, err = fileSeeker.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("could not rewind file after XMP: %w", err)
	}

	// MP4
	meta, err = readMP4Metadata(logger, item, fileSeeker)
	if err != nil {
		return nil, fmt.Errorf("processing MP4 metadata: %w", err)
	}
	item.AddMetadata(meta, policy)
	if _, err = fileSeeker.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("could not rewind file after MP4: %w", err)
	}

	// ID3/Tags (Audio)
	meta, pic, err := readAudioMetadata(logger, fileSeeker)
	if err != nil {
		return nil, fmt.Errorf("processing tag metadata: %w", err)
	}
	item.AddMetadata(meta, policy)

	return pic, nil
}

func extractEXIFMetadata(logger *zap.Logger, file io.Reader, item *timeline.Item) (timeline.Metadata, error) {
	ex, err := exif.Decode(file)
	if err != nil && exif.IsCriticalError(err) {
		return nil, fmt.Errorf("decoding exif from file: %w", err)
	}

	// fill in timestamp
	if item.Timestamp.IsZero() {
		if ts, err := ex.DateTime(); err == nil {
			item.Timestamp = ts
		}
	}

	// fill in location coordinates
	if item.Location.Latitude == nil && item.Location.Longitude == nil {
		lat, lon, err := ex.LatLong()
		if err == nil {
			item.Location = timeline.Location{
				Latitude:  &lat,
				Longitude: &lon,
			}
		}
	}

	// fill in remaining EXIF data
	meta := make(timeline.Metadata)
	err = ex.Walk(exifWalkerFunc(func(name exif.FieldName, tag *tiff.Tag) error {
		// we don't skip timestamp and location fields even though we extract them
		// separately, because it doesn't hurt to keep the original data, and sometimes
		// the timestamp we extract is overwritten for another one, and we should
		// definitely keep the original in the metadata in that case
		// (specifically, we don't skip: DateTimeOriginal, DateTime, GPSLatitude, and GPSLongitude)

		switch tag.Format() {
		case tiff.IntVal:
			for i := range int(tag.Count) {
				key := splitCamelCaseIntoWords(string(name))
				if tag.Count > 1 {
					key += fmt.Sprintf(" %d", i+1)
				}
				meta[key], err = tag.Int(i)
				if err != nil {
					logger.Error("unable to get int from TIFF tag",
						zap.Error(err),
						zap.String("field_name", string(name)),
						zap.Int("index", i))
				}
			}

		case tiff.FloatVal:
			for i := range int(tag.Count) {
				key := splitCamelCaseIntoWords(string(name))
				if tag.Count > 1 {
					key += fmt.Sprintf(" %d", i+1)
				}
				meta[key], err = tag.Float(i)
				if err != nil {
					logger.Warn("unable to get float from TIFF tag",
						zap.Error(err),
						zap.String("field_name", string(name)),
						zap.Int("index", i))
				}
			}

		case tiff.RatVal:
			for i := range int(tag.Count) {
				key := splitCamelCaseIntoWords(string(name))
				if tag.Count > 1 {
					key += fmt.Sprintf(" %d", i+1)
				}
				val, err := tag.Rat(i)
				if err != nil {
					logger.Warn("unable to get rational from TIFF tag",
						zap.Error(err),
						zap.String("field_name", string(name)),
						zap.Int("index", i))
				} else {
					// additionally, set Altitude if that's what this field happens to be
					meta[key] = val
					if string(name) == "GPSAltitude" && item.Location.Altitude == nil {
						if valFloat, _ := val.Float64(); !math.IsInf(valFloat, 0) {
							item.Location.Altitude = &valFloat
						}
					}
				}
			}

		case tiff.StringVal:
			meta[splitCamelCaseIntoWords(string(name))], err = tag.StringVal()
			if err != nil {
				logger.Warn("unable to get string from TIFF tag",
					zap.Error(err),
					zap.String("field_name", string(name)))
			}

		case tiff.OtherVal:
			logger.Debug("encountered other EXIF field type",
				zap.String("name", string(name)),
				zap.Int("length", len(tag.Val)))

		case tiff.UndefVal:
			logger.Debug("encountered undefined EXIF field type",
				zap.String("name", string(name)),
				zap.Int("length", len(tag.Val)))
		}
		return nil
	}))

	return meta, err
}

func extractXMPMetadata(logger *zap.Logger, file io.Reader) (timeline.Metadata, error) {
	xmpPackets, err := xmp.ScanPackets(file)
	if err != nil {
		if errors.Is(err, io.EOF) {
			logger.Debug("no XMP metadata found", zap.Error(err))
			return nil, nil
		}
		return nil, err
	}

	meta := make(timeline.Metadata)

	for _, packet := range xmpPackets {
		var doc xmp.Document
		if err := xmp.Unmarshal(packet, &doc); err != nil {
			return nil, fmt.Errorf("unmarshaling XMP document: %w", err)
		}
		paths, err := doc.ListPaths()
		if err != nil {
			return nil, fmt.Errorf("listing XMP paths: %w", err)
		}
		for _, p := range paths {
			// fmt.Printf("%s = %s\n", p.Path, p.Value)
			switch p.Path {
			case "GCamera:MicroVideo", // before Android 11, Google used "MicroVideo"; now they use "MotionPhoto"
				"GCamera:MotionPhoto",
				"GCamera:MotionPhotoPresentationTimestampUs":
				meta["XMP "+string(p.Path)] = p.Value
			}
		}

		// nodes := doc.Nodes()
		// for nodes != nil {
		// 	for _, n := range nodes {
		// 		fmt.Println("NODE:", n.XMLName.Local, n.Value)
		// 		for _, attr := range n.Attr {
		// 			fmt.Println(attr.Name.Local, "=", attr.Value)
		// 		}
		// 		for _, n := range n.Nodes {
		// 			for _, attr := range n.Attr {
		// 				fmt.Println("\t", attr.Name.Local, "=", attr.Value)
		// 			}
		// 		}
		// 	}
		// }
	}

	return meta, nil
}

// extractAudioAndVideoMetadata reads various kinds of audio and video tags/metadata from the specified file.
// For MP4 files, the metadata tree is traversed and any useful, interesting, or valuable data is extracted.
// For other files, ID3 tags and other kinds of metadata are traversed and extracted if known and if it may
// be of use or value.
//
// Creation timestamp and location data are added to the item directly as first-class data.
// If a picture (usually album art) was embedded, it will be returned so that it can be processed separately.
// All the other metadata is added directly to the item's metadata map according to the metadata merge policy.
//
// Relevant useful resources (as of January 2023):
// - https://cconcolato.github.io/mp4ra/
// - https://www.ftyps.com
// - https://observablehq.com/@benjamintoofer/iso-base-media-file-format
// - https://developer.apple.com/library/archive/documentation/QuickTime/QTFF/QTFFChap2/qtff2.html
// func extractAudioAndVideoMetadata(logger *zap.Logger, fsys fs.FS, path string, item *timeline.Item, policy timeline.MetadataMergePolicy) (*tag.Picture, error) {
// 	file, err := fsys.Open(path)
// 	if err != nil {
// 		return nil, err
// 	}
// 	defer file.Close()

// 	// prepare to read the file; seeking ability is required
// 	var fileSeeker io.ReadSeeker
// 	if seeker, ok := file.(io.ReadSeeker); ok {
// 		fileSeeker = seeker
// 	} else {
// 		// TODO: I have not tested this code yet, but:
// 		// since this isn't a seekable reader, we have to make it seekable by reading it
// 		// into memory; obviously this is problematic if the file is large, but even if
// 		// it is, we can hope that the metadata is at the beginning (sometimes it is at
// 		// the end; but usually it is at the beginning)...
// 		const maxBufferSize = 1024 * 1024 * 50

// 		buf := bufPool.Get().(*bytes.Buffer)
// 		buf.Reset()
// 		defer bufPool.Put(buf)

// 		r := io.LimitReader(file, maxBufferSize)

// 		if _, err := io.Copy(buf, r); err != nil {
// 			return nil, err
// 		}

// 		fileSeeker = bytes.NewReader(buf.Bytes())
// 	}

// 	mp4Meta, err := readMP4Metadata(logger, item, fileSeeker)
// 	if err != nil {
// 		return nil, fmt.Errorf("reading MP4 metadata: %w", err)
// 	}
// 	item.AddMetadata(mp4Meta, timeline.MetaMergeAppend)

// 	// reset to beginning of file; then try to read audio tags
// 	fileSeeker.Seek(0, io.SeekStart)

// 	// an EOF error is OK; just means there was no tag metadata
// 	tagMeta, pic, err := readAudioMetadata(fileSeeker)
// 	if err != nil && !errors.Is(err, io.EOF) {
// 		return nil, fmt.Errorf("reading tag metadata: %w", err)
// 	}
// 	item.AddMetadata(tagMeta, policy)

// 	return pic, nil
// }

// readMP4Metadata reads as much useful and valuable metadata from an MP4 file as we can. It sets the
// timestamp and location directly onto the item (if found) and returns the rest of the metadata.
func readMP4Metadata(logger *zap.Logger, item *timeline.Item, fileSeeker io.ReadSeeker) (timeline.Metadata, error) {
	// prepare metadata container
	meta := make(timeline.Metadata)

	// Attempt to read MP4 metadata. This mp4 package is a low-level parser of atoms/boxes/tags
	// (I don't really know what they're called tbh) that can extract all of the important fields,
	// even unrecognized boxes as long as we know how to read them.
	_, err := mp4.ReadBoxStructure(fileSeeker, func(h *mp4.ReadHandle) (any, error) {
		if h.BoxInfo.IsSupportedType() && h.BoxInfo.Type.String() != "mdat" {
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, fmt.Errorf("reading payload from handle: %w", err)
			}

			switch b := box.(type) {
			case *mp4.Ftyp: // file type
				meta["Major Brand"] = string(b.MajorBrand[:])

				brands := make([]string, 0, len(b.CompatibleBrands))
				for _, brand := range b.CompatibleBrands {
					brands = append(brands, string(brand.CompatibleBrand[:]))
				}
				if len(brands) > 0 {
					meta["Compatible Brands"] = strings.Join(brands, ", ")
				}

			case *mp4.Mvhd: // movie header (overall declarations)
				if item.Timestamp.IsZero() {
					// (only difference between V0 and V1 is bit length of integer)
					if creationTime := b.GetCreationTime(); creationTime != 0 {
						item.Timestamp = isoIEC14496Timestamp(creationTime)
					}
				}
				if modifTime := b.GetModificationTime(); modifTime != 0 {
					meta["Modification Time"] = isoIEC14496Timestamp(modifTime)
				}
				meta["Duration"] = float64(b.GetDuration()) / float64(b.Timescale)
				meta["Rate"] = b.GetRate()
				meta["Volume"] = b.Volume

			case *mp4.Tkhd: // track header
				// just in case (for some reason) the mvhd box didn't have this info
				if item.Timestamp.IsZero() {
					if creationTime := b.GetCreationTime(); creationTime != 0 {
						item.Timestamp = isoIEC14496Timestamp(creationTime)
					}
				}

				if width := b.GetWidthInt(); width > 0 {
					meta[fmt.Sprintf("Track %d Width", b.TrackID)] = width
				}
				if height := b.GetHeightInt(); height > 0 {
					meta[fmt.Sprintf("Track %d Height", b.TrackID)] = height
				}
				meta[fmt.Sprintf("Track %d Flags", b.TrackID)] = fmt.Sprintf("%#x", b.GetFlags())
			}

			// traverse child nodes
			return h.Expand()
		} else if h.BoxInfo.Context.UnderUdta && h.BoxInfo.Type == [4]byte{'©', 'x', 'y', 'z'} {
			// Google cameras store location data in this box
			var buf bytes.Buffer
			_, err := h.ReadData(&buf)
			if err != nil {
				return nil, fmt.Errorf("reading ©xyz box data: %w", err)
			}
			loc, err := mp4XYZCoordsToLocation(buf.String())
			if err != nil {
				logger.Error("parsing location from ©xyz",
					zap.Error(err),
					zap.String("©xyz", buf.String()))
			} else {
				item.Location = loc
			}
		}

		return nil, nil
	})

	return meta, err
}

// readAudioMetadata reads tag metadata from the file and returns it, along with any embedded picture.
func readAudioMetadata(logger *zap.Logger, fileSeeker io.ReadSeeker) (timeline.Metadata, *tag.Picture, error) {
	m, err := tag.ReadFrom(fileSeeker)
	if err != nil {
		if errors.Is(err, tag.ErrNoTagsFound) {
			logger.Debug("no audio metadata found", zap.Error(err))
			return nil, nil, nil
		}
		return nil, nil, err
	}

	meta := make(timeline.Metadata)

	meta["Format"] = m.Format()
	if m.FileType() != "" {
		meta["File type"] = m.FileType()
	}
	meta["Title"] = m.Title()
	meta["Album"] = m.Album()
	meta["Artist"] = m.Artist()
	meta["Album Artist"] = m.AlbumArtist()
	meta["Composer"] = m.Composer()
	meta["Year"] = m.Year()
	meta["Genre"] = m.Genre()
	meta["Lyrics"] = m.Lyrics()
	meta["Comment"] = m.Comment()

	track, totalTracks := m.Track()
	if track > 0 {
		meta["Track"] = track
	} else if totalTracks > 0 {
		meta["Total Tracks"] = totalTracks
	}

	disc, totalDiscs := m.Disc()
	if disc > 0 {
		meta["Disc"] = disc
	} else if totalDiscs > 0 {
		meta["Total Discs"] = totalDiscs
	}

	// additional fields from https://exiftool.org/TagNames/ID3.html
	raw := m.Raw()
	meta["Copyright"] = raw["TCOP"]
	meta["Band"] = raw["TPE2"]
	meta["Conductor"] = raw["TPE3"]
	// TODO: there's dozens more, including time and date, language, URLs, etc.

	return meta, m.Picture(), nil
}

// mp4XYZCoordsToLocation parses the box called ©xyz which, at least on Google media,
// is formatted like "*data+50.1234-101.1234+000.000/" - I assume this is +X-Y+Z, i.e.
// +Lat-Lon+Alt. MUST CONTAIN NO MORE THAN 4 DIGITS AFTER THE DECIMAL (according to spec;
// our code will, of course, handle it just fine).
func mp4XYZCoordsToLocation(xyzRaw string) (timeline.Location, error) {
	matches := cXYZCoordsRegex.FindStringSubmatch(xyzRaw)
	const minMatches = 4
	if len(matches) < minMatches {
		return timeline.Location{}, fmt.Errorf("lat+lon not found in expected format in input string '%s'", xyzRaw)
	}

	latStr, lonStr := matches[1], matches[3]

	lat, err := strconv.ParseFloat(latStr, 64)
	if err != nil {
		return timeline.Location{}, fmt.Errorf("converting latitude from '%s': %w", latStr, err)
	}
	lon, err := strconv.ParseFloat(lonStr, 64)
	if err != nil {
		return timeline.Location{}, fmt.Errorf("converting longitude from '%s': %w", lonStr, err)
	}

	return timeline.Location{
		Latitude:  &lat,
		Longitude: &lon,
	}, nil
}

// splitCamelCaseIntoWords splits camel-cased strings into words by inserting
// spaces at the most sensible places. This algorithm isn't perfect as it doesn't
// use a dictionary, but it's pretty good for EXIF and most other use cases.
func splitCamelCaseIntoWords(s string) string {
	var sb strings.Builder
	for i, ch := range s {
		// whether current character is upper or lower cased
		u := upper(ch)
		l := lower(ch)

		// previous is upper, next is upper, next is lower
		// (defaults depend on if we're at beginning or end of string)
		pu, nu, nl := i == 0, i >= len(s)-1, i >= len(s)-1
		if i > 0 {
			pu = upper(rune(s[i-1]))
		}
		if i < len(s)-1 {
			nu = upper(rune(s[i+1]))
			nl = lower(rune(s[i+1]))
		}

		// insert a space before current char if:
		// - we are upper and previous is not upper,
		// - we are upper and next is not upper,
		// - we are neither upper nor lower (i.e. non-alpha), and next is also not upper or lower
		if i > 0 && ((u && !pu) || (u && !nu) || (!u && !l && !nu && !nl)) {
			sb.WriteRune(' ')
		}

		sb.WriteRune(ch)
	}
	return sb.String()
}

func upper(ch rune) bool {
	// ASCII only
	return ch >= 65 && ch <= 90
}

func lower(ch rune) bool {
	// ASCII only
	return ch >= 97 && ch <= 122
}

// isoIEC14496Timestamp converts the number of seconds since January 1, 1904 (as
// defined by ISO/IEC 14496-12 5th Edition [2015], page 23) to a normal time.Time
// value based on Unix epoch.
func isoIEC14496Timestamp(ts uint64) time.Time {
	if ts == isoIEC14496_12_5thEdition_2015EpochToUnixEpochSeconds {
		return time.Time{}
	}
	unixSec := ts - isoIEC14496_12_5thEdition_2015EpochToUnixEpochSeconds
	return time.Unix(int64(unixSec), 0) //nolint:gosec // This could technically overflow but I don't think the incoming timestamp is going to be THAT big
}

// The difference between January 1, 1904 (the epoch used by MP4 file metadata)
// and January 1, 1970 (the Unix epoch) in seconds.
const isoIEC14496_12_5thEdition_2015EpochToUnixEpochSeconds uint64 = 2082844800 //nolint // Yeah screw it, I'm using underscores for this one

// Regex to extract lat-lon data from the ©xyz field of MP4 metadata which
// takes the form: "*data+50.1234-101.1234+000.000/" in North America.
// And sometimes the +000.000 is missing (sometimes there's just a trailing slash).
var cXYZCoordsRegex = regexp.MustCompile(`((\+|-)\d+\.\d+)((\+|-)\d+\.\d+)`)

var bufPool = sync.Pool{
	New: func() any {
		return new(bytes.Buffer)
	},
}
