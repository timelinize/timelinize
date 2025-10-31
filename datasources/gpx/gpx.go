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
	"strings"
	"time"

	"github.com/timelinize/timelinize/datasources/googlelocation"
	"github.com/timelinize/timelinize/timeline"
	"go.uber.org/zap"
	"golang.org/x/net/html/charset"
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
	OwnerEntityID uint64 `json:"owner_entity_id"`

	// Options specific to the location processor.
	googlelocation.LocationProcessingOptions
}

// FileImporter implements the timeline.FileImporter interface.
type FileImporter struct{}

// Recognize returns whether the file is supported.
func (FileImporter) Recognize(_ context.Context, dirEntry timeline.DirEntry, _ timeline.RecognizeParams) (timeline.Recognition, error) {
	rec := timeline.Recognition{DirThreshold: .9}

	// we can import directories, but let the import planner figure that out; only recognize files
	if dirEntry.IsDir() {
		return rec, nil
	}

	// recognize by file extension
	if strings.ToLower(path.Ext(dirEntry.Name())) == ".gpx" {
		rec.Confidence = 1
	}

	return rec, nil
}

// FileImport imports data from a file or folder.
func (fi *FileImporter) FileImport(ctx context.Context, dirEntry timeline.DirEntry, params timeline.ImportParams) error {
	dsOpt := params.DataSourceOptions.(*Options)

	owner := timeline.Entity{
		ID: dsOpt.OwnerEntityID,
	}

	return fs.WalkDir(dirEntry.FS, dirEntry.Filename, func(fpath string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if strings.HasPrefix(d.Name(), ".") {
			// skip hidden files & folders
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil // traverse into subdirectories
		}

		// skip unsupported file types
		if ext := strings.ToLower(path.Ext(d.Name())); ext != ".gpx" {
			return nil
		}

		file, err := dirEntry.FS.Open(fpath)
		if err != nil {
			return err
		}
		defer file.Close()

		proc, err := NewProcessor(file, owner, params, dsOpt.LocationProcessingOptions)
		if err != nil {
			return err
		}

		for {
			g, err := proc.NextGPXGraph(ctx)
			if err != nil {
				return err
			}
			if g == nil {
				break
			}
			params.Pipeline <- g
		}

		return nil
	})
}

// Processor can get the next GPX point. Call NewProcessor to make a valid instance.
type Processor struct {
	dec     *decoder
	opt     timeline.ImportParams
	locProc googlelocation.LocationSource
	owner   timeline.Entity
}

// NewProcessor returns a new GPX processor.
func NewProcessor(file io.Reader, owner timeline.Entity, opt timeline.ImportParams, locOpt googlelocation.LocationProcessingOptions) (*Processor, error) {
	// create XML decoder (wrapped to track some state as it decodes)
	xmlDec := &decoder{Decoder: xml.NewDecoder(file)}
	xmlDec.CharsetReader = charset.NewReaderLabel // handle non-UTF-8 encodings

	// create location processor to clean up any noisy raw data
	locProc, err := googlelocation.NewLocationProcessor(xmlDec, locOpt)
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

// NextGPXGraph returns the next GPX graph (item or entity)
func (p *Processor) NextGPXGraph(ctx context.Context) (*timeline.Graph, error) {
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
		if l.Metadata == nil {
			l.Metadata = make(timeline.Metadata)
		}

		item := &timeline.Item{
			Classification: timeline.ClassLocation,
			Timestamp:      l.Timestamp,
			Timespan:       l.Timespan,
			Location:       l.Location(),
			Owner:          p.owner,
			Metadata:       l.Metadata,
		}

		g := &timeline.Graph{Item: item}

		makePlaceEntity := func(pt wpt) {
			// store named waypoints as place entities
			if pt.Name != "" {
				g.ToEntity(timeline.RelVisit, &timeline.Entity{
					Type: timeline.EntityPlace,
					Name: pt.Name,
					Attributes: []timeline.Attribute{
						{
							Name:      "coordinate",
							Latitude:  item.Location.Latitude,
							Longitude: item.Location.Longitude,
							Identity:  true, // TODO: I feel like this should be just `Identifying: true`, but that creates "_entity" attributes...
							Metadata: timeline.Metadata{
								"URL": pt.Link.Href,
							},
						},
					},
				})
			}
		}

		switch pt := l.Original.(type) {
		case trkpt:
			if _, ok := item.Metadata["Velocity"]; !ok {
				item.Metadata["Velocity"] = pt.Speed // use same key as with Google Location History
			}
			if _, ok := item.Metadata["Satellites"]; !ok {
				item.Metadata["Satellites"] = pt.Sat
			}
			item.Metadata = l.Metadata
		case wpt:
			makePlaceEntity(pt)
		}

		// in case a waypoint was clustered with other points,
		// iterate the points comprising it to see if there's any
		// place entities we can extract, so we don't skip them
		for _, cp := range l.ClusterPoints() {
			if pt, ok := cp.Original.(wpt); ok {
				makePlaceEntity(pt)
			}
		}

		if p.opt.Timeframe.ContainsItem(item, false) {
			return g, nil
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
	trkType      string
	lastTrkPt    trkpt
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

		const gpxRoot = "gpx"

		switch elem := tkn.(type) {
		case xml.StartElement:
			switch {
			// any case where we decode the element, we need to return or continue,
			// to avoid adding it to the stack, since by decoding it, we also decode
			// the closing tag

			case elem.Name.Local == "metadata" && d.stack.path() == gpxRoot:
				var meta metadata
				if err := d.DecodeElement(&meta, &elem); err != nil {
					// TODO: maybe skip and go to next?
					return nil, fmt.Errorf("decoding XML element as metadata: %w", err)
				}
				if meta.Time != "" {
					ts, err := time.Parse(time.RFC3339, meta.Time)
					if err != nil {
						return nil, fmt.Errorf("parsing timestamp in metadata->time element: %w", err)
					}
					d.metadataTime = ts
				}
				continue

			case elem.Name.Local == "wpt" && d.stack.path() == gpxRoot:
				var waypoint wpt
				if err := d.DecodeElement(&waypoint, &elem); err != nil {
					// TODO: maybe skip and go to next?
					return nil, fmt.Errorf("decoding XML element as waypoint: %w", err)
				}
				return &googlelocation.Location{
					Original:    waypoint,
					LatitudeE7:  int64(waypoint.Lat * placesMult),
					LongitudeE7: int64(waypoint.Lon * placesMult),
					Altitude:    waypoint.Ele,
					Timestamp:   waypoint.Time,
					Significant: true,
				}, nil

			case elem.Name.Local == "trk" && d.stack.path() == gpxRoot:
				// reset the track state, since it's a new track
				d.trkType = ""
				d.lastTrkPt = trkpt{}

			case elem.Name.Local == "type" && d.stack.path() == "gpx/trk":
				var trkType string
				if err := d.DecodeElement(&trkType, &elem); err != nil {
					return nil, fmt.Errorf("decoding XML element as track type: %w", err)
				}
				d.trkType = trkType
				continue

			case elem.Name.Local == "trkpt" && d.stack.path() == "gpx/trk/trkseg":
				var point trkpt
				if err := d.DecodeElement(&point, &elem); err != nil {
					// TODO: maybe skip and go to next?
					return nil, fmt.Errorf("decoding XML element as track point: %w", err)
				}

				timestamp := point.Time
				if timestamp.IsZero() {
					timestamp = d.metadataTime
				}

				// tell the location processor to reset track state
				isFirstInTrack := d.lastTrkPt.Lat == 0 && d.lastTrkPt.Lon == 0
				d.lastTrkPt = point

				return &googlelocation.Location{
					Original:    point,
					LatitudeE7:  int64(point.Lat * placesMult),
					LongitudeE7: int64(point.Lon * placesMult),
					Altitude:    point.Ele,
					Timestamp:   timestamp,
					Metadata: timeline.Metadata{
						"Activity type": d.trkType,
					},
					NewTrack: isFirstInTrack,
				}, nil
			}

			d.stack = append(d.stack, elem.Name.Local)

		case xml.EndElement:
			if len(d.stack) == 0 {
				return nil, fmt.Errorf("encountered unmatched closing tag: %s", elem.Name.Local)
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

// waypoint
type wpt struct {
	XMLName xml.Name  `xml:"wpt"`
	Text    string    `xml:",chardata"`
	Lat     float64   `xml:"lat,attr"`
	Lon     float64   `xml:"lon,attr"`
	Time    time.Time `xml:"time"`
	Ele     float64   `xml:"ele"`  // elevation
	Name    string    `xml:"name"` // place name!
	Link    struct {
		Text string `xml:",chardata"`
		Href string `xml:"href,attr"` // usually a foursquare link
	} `xml:"link"`
}

// track point
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
