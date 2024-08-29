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

// Package gpx implements a data source for GPS Exchange Format (https://en.wikipedia.org/wiki/GPS_Exchange_Format).
package gpx

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/mholt/archiver/v4"
	"github.com/timelinize/timelinize/datasources/googlelocation"
	"github.com/timelinize/timelinize/timeline"
	"go.uber.org/zap"
)

func init() {
	err := timeline.RegisterDataSource(timeline.DataSource{
		Name:            "gpx",
		Title:           "GPS Exchange",
		Icon:            "gpx.svg",
		Description:     "A .gpx file or folder of .gpx files containing location tracks",
		NewOptions:      func() any { return new(Options) },
		NewFileImporter: func() timeline.FileImporter { return new(FileImporter) },
	})
	if err != nil {
		timeline.Log.Fatal("registering data source", zap.Error(err))
	}
}

// Options configures the data source.
type Options struct {
	// The ID of the owner entity. REQUIRED for linking entity in DB.
	// TODO: maybe an attribute ID instead, in case the data represents multiple people
	OwnerEntityID int64 `json:"owner_entity_id"`

	Simplification float64 `json:"simplification,omitempty"`
}

// FileImporter implements the timeline.FileImporter interface.
type FileImporter struct{}

// Recognize returns whether the file is supported.
func (FileImporter) Recognize(ctx context.Context, filenames []string) (timeline.Recognition, error) {
	var totalCount, matchCount int

	for _, filename := range filenames {
		fsys, err := archiver.FileSystem(ctx, filename)
		if err != nil {
			return timeline.Recognition{}, err
		}

		err = fs.WalkDir(fsys, ".", func(fpath string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if fpath == "." {
					return nil
				}
				return fs.SkipDir // don't walk subfolders; it's uncommon and slower
			}
			if fpath == "." {
				fpath = path.Base(filename)
			}
			if strings.HasPrefix(path.Base(fpath), ".") {
				// skip hidden files
				if d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}

			totalCount++

			ext := strings.ToLower(filepath.Ext(fpath))
			if ext == ".gpx" {
				matchCount++
			}

			return nil
		})
		if err != nil {
			return timeline.Recognition{}, err
		}
	}

	var confidence float64
	if totalCount > 0 {
		confidence = float64(matchCount) / float64(totalCount)
	}

	return timeline.Recognition{Confidence: confidence}, nil
}

// FileImport imports data from a file or folder.
func (fi *FileImporter) FileImport(ctx context.Context, filenames []string, itemChan chan<- *timeline.Graph, opt timeline.ListingOptions) error {
	dsOpt := opt.DataSourceOptions.(*Options)

	owner := timeline.Entity{
		ID: dsOpt.OwnerEntityID,
	}

	for _, filename := range filenames {
		fsys, err := archiver.FileSystem(ctx, filename)
		if err != nil {
			return err
		}

		err = fs.WalkDir(fsys, ".", func(fpath string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if fpath == "." {
				fpath = path.Base(filename)
			}
			if strings.HasPrefix(path.Base(fpath), ".") {
				// skip hidden files
				if d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			if d.IsDir() {
				return nil // traverse into subdirectories
			}

			// skip unsupported file types
			ext := path.Ext(strings.ToLower(fpath))
			if ext != ".gpx" {
				return nil
			}

			file, err := fsys.Open(fpath)
			if err != nil {
				return err
			}
			defer file.Close()

			proc, err := NewProcessor(file, owner, opt, dsOpt.Simplification)
			if err != nil {
				return err
			}

			for {
				item, err := proc.NextGPXItem(ctx)
				if err != nil {
					return err
				}
				if item == nil {
					break
				}
				itemChan <- &timeline.Graph{Item: item}
			}

			return nil
		})
		if err != nil {
			return err
		}
	}

	return nil
}

// Processor can get the next GPX point. Call NewProcessor to make a valid instance.
type Processor struct {
	dec     *decoder
	opt     timeline.ListingOptions
	locProc googlelocation.LocationSource
	owner   timeline.Entity
}

// NewProcessor returns a new GPX processor.
func NewProcessor(file io.Reader, owner timeline.Entity, opt timeline.ListingOptions, simplification float64) (*Processor, error) {
	// create XML decoder (wrapped to track some state as it decodes)
	xmlDec := &decoder{Decoder: xml.NewDecoder(file)}

	// create location processor to clean up any noisy raw data
	locProc, err := googlelocation.NewLocationProcessor(xmlDec, simplification)
	if err != nil {
		return nil, err
	}

	return &Processor{
		dec:     xmlDec,
		opt:     opt,
		locProc: locProc,
		owner:   owner,
	}, nil
}

// NextGPXItem returns the next GPX item.
func (p *Processor) NextGPXItem(ctx context.Context) (*timeline.Item, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		l, err := p.locProc.NextLocation(ctx)
		if err != nil {
			return nil, err
		}
		if l == nil {
			break
		}

		point := l.Original.(trkpt)

		meta := timeline.Metadata{
			"Velocity":   point.Speed, // same key as with Google Location History
			"Satellites": point.Sat,
		}
		meta.Merge(l.Metadata, timeline.MetaMergeReplace)

		item := &timeline.Item{
			Classification: timeline.ClassLocation,
			Timestamp:      l.Timestamp,
			Timespan:       l.Timespan,
			Location:       l.Location(),
			Owner:          p.owner,
			Metadata:       meta,
		}

		if p.opt.Timeframe.ContainsItem(item, false) {
			return item, nil
		}
	}

	return nil, nil
}

// decoder wraps the XML decoder to get the next location from the document.
// It tracks nesting state so we can be sure we're in the right part of the tree.
type decoder struct {
	*xml.Decoder
	stack        nesting
	metadataTime time.Time
}

// NextLocation returns the next available point from the XML document.
func (d *decoder) NextLocation(ctx context.Context) (*googlelocation.Location, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		tkn, err := d.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("decoding next XML token: %w", err)
		}

		// TODO: grab this from the top in <metadata>? (we grab <time> already)
		/*
			<name>GPS Logger 20230210-171505</name>
			<desc>Verbier to Chamonix</desc>
			<time>2023-04-01T17:16:10Z</time>
			<keywords>car</keywords>
			<bounds minlat="45.91689727" minlon="6.86775060" maxlat="46.10371473" maxlon="7.22880099" />


			ALSO SOME METADATA IN COMMENTS:

			<!-- Created with BasicAirData GPS Logger for Android - ver. 3.2.1 -->
			<!-- Track 58 = 4685 TrackPoints + 0 Placemarks -->

			<!-- Track Statistics (based on Total Time | Time in Movement): -->
			<!--  Distance = 66.1 km -->
			<!--  Duration = 01:23:09 | 01:15:34 -->
			<!--  Altitude Gap = -471 m -->
			<!--  Max Speed = 118 km/h -->
			<!--  Avg Speed = 47.7 | 52.5 km/h -->
			<!--  Direction = SW -->
			<!--  Activity = car -->
			<!--  Altitudes = Raw -->


			THE <trk> TAG ALSO HAS SOME INFO IN IT (Strava, ca. 2023):

			<name>Morning Ride</name>
			<type>cycling</type>
		*/

		switch elem := tkn.(type) {
		case xml.StartElement:
			if elem.Name.Local == "metadata" && d.stack.path() == "gpx" {
				var meta metadata
				if err := d.DecodeElement(&meta, &elem); err != nil {
					// TODO: maybe skip and go to next?
					return nil, fmt.Errorf("decoding XML element as metadata: %w", err)
				}
				ts, err := time.Parse(time.RFC3339, meta.Time)
				if err != nil {
					return nil, fmt.Errorf("parsing timestamp in metadata->time element: %w", err)
				}
				d.metadataTime = ts
				continue
			}
			if elem.Name.Local == "trkpt" && d.stack.path() == "gpx/trk/trkseg" {
				var point trkpt
				if err := d.DecodeElement(&point, &elem); err != nil {
					// TODO: maybe skip and go to next?
					return nil, fmt.Errorf("decoding XML element as track point: %w", err)
				}

				timestamp := point.Time
				if timestamp.IsZero() {
					timestamp = d.metadataTime
				}

				return &googlelocation.Location{
					Original:    point,
					LatitudeE7:  int64(point.Lat * placesMult),
					LongitudeE7: int64(point.Lon * placesMult),
					Altitude:    point.Ele,
					Timestamp:   timestamp,
				}, nil
			}

			d.stack = append(d.stack, elem.Name.Local)

		case xml.EndElement:
			if len(d.stack) == 0 {
				return nil, fmt.Errorf("encountered end tag without opening: %s", elem.Name.Local)
			}
			d.stack = d.stack[:len(d.stack)-1]
		}
	}

	return nil, nil
}

type nesting []string

func (n nesting) path() string {
	return strings.Join(n, "/")
}

type metadata struct {
	XMLName   xml.Name `xml:"metadata"`
	Text      string   `xml:",chardata"`
	Copyright struct {
		Text   string `xml:",chardata"`
		Author string `xml:"author,attr"`
	} `xml:"copyright"`
	Link struct {
		Chardata string `xml:",chardata"`
		Href     string `xml:"href,attr"`
		Text     string `xml:"text"`
	} `xml:"link"`
	Time string `xml:"time"`
}

type trkpt struct {
	XMLName xml.Name  `xml:"trkpt"`
	Text    string    `xml:",chardata"`
	Lat     float64   `xml:"lat,attr"`
	Lon     float64   `xml:"lon,attr"`
	Ele     float64   `xml:"ele"` // elevation
	Time    time.Time `xml:"time"`
	Speed   float64   `xml:"speed"` // velocity
	Sat     int       `xml:"sat"`   // number of satellites
}

const placesMult = 1e7
