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

// Package kmlgx implements a data source for KML files with Google extensions (gx).
package kmlgx

import (
	"context"
	"encoding/xml"
	"errors"
	"io/fs"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/timelinize/timelinize/datasources/googlelocation"
	"github.com/timelinize/timelinize/timeline"
	"go.uber.org/zap"
	"golang.org/x/net/html/charset"
)

// This could be nearly identical to GPX importer except the XML structure is different.
// I decided to not stream this file because the gx:Track entries can be in any order,
// instead of each point clustered together. That's unlikely in practice but seems like
// a bad assumption to make for the sake of performance on files that are rarely huge.
// So we just swallow this thing whole.

func init() {
	err := timeline.RegisterDataSource(timeline.DataSource{
		Name:            "kml",
		Title:           "Keyhole (KML)",
		Icon:            "kml.svg",
		Description:     "A .kml file or folder of .kml files containing location tracks; requires Google extension namespace (gx)",
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

// Recognize returns whether the file or folder is supported.
func (FileImporter) Recognize(_ context.Context, dirEntry timeline.DirEntry, _ timeline.RecognizeParams) (timeline.Recognition, error) {
	rec := timeline.Recognition{DirThreshold: .9}

	// we can import directories, but let the import planner figure that out; only recognize files
	if dirEntry.IsDir() {
		return rec, nil
	}

	// recognize by file extension, then verify by decoding the file
	// (TODO: If a recognize FastMode is created, opening and decoding the file should not happen in FastMode)
	if strings.ToLower(path.Ext(dirEntry.Name())) == ".kml" {
		file, err := dirEntry.Open(".")
		if err != nil {
			return rec, err
		}
		defer file.Close()
		dec := xml.NewDecoder(file)
		dec.CharsetReader = charset.NewReaderLabel // handle non-UTF-8 encodings
		var doc document
		err = dec.Decode(&doc)
		if err != nil {
			return rec, nil
		}
		if strings.HasPrefix(doc.XMLNSGX, "http://www.google.com/kml/ext/2") {
			rec.Confidence = 1.0
		}
	}

	return rec, nil
}

// FileImport imports data from a folder or file.
func (fi *FileImporter) FileImport(ctx context.Context, dirEntry timeline.DirEntry, params timeline.ImportParams) error {
	dsOpt := params.DataSourceOptions.(*Options)

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
		if ext := strings.ToLower(path.Ext(d.Name())); ext != ".kml" {
			return nil
		}

		file, err := dirEntry.FS.Open(fpath)
		if err != nil {
			return err
		}
		defer file.Close()

		dec := xml.NewDecoder(file)
		dec.CharsetReader = charset.NewReaderLabel // handle non-UTF-8 encodings

		var doc document
		err = dec.Decode(&doc)
		if err != nil {
			return err
		}
		doc.i = -1

		if doc.XMLNSGX != "http://www.google.com/kml/ext/2.2" {
			// actually, we may not need this version specifically; other versions might work just as well
			return errors.New("KML document does not support Google extension 'gx' namespace")
		}
		if len(doc.Document.Placemark.Track.When) != len(doc.Document.Placemark.Track.Coord) {
			return errors.New("corrupt gx:Track data in Placemark: number of timestamps does not match number of coordinates")
		}

		// create location processor to clean up any noisy raw data
		locProc, err := googlelocation.NewLocationProcessor(&doc, dsOpt.LocationProcessingOptions)
		if err != nil {
			return err
		}

		// iterate each resulting location point and process it as an item
		for {
			l, err := locProc.NextLocation(ctx)
			if err != nil {
				return err
			}
			if l == nil {
				break
			}

			item := &timeline.Item{
				Classification: timeline.ClassLocation,
				Timestamp:      l.Timestamp,
				Timespan:       l.Timespan,
				Location:       l.Location(),
				Owner: timeline.Entity{
					ID: dsOpt.OwnerEntityID,
				},
				Metadata: l.Metadata,
			}

			if params.Timeframe.ContainsItem(item, false) {
				params.Pipeline <- &timeline.Graph{Item: item}
			}
		}

		return nil
	})
}

// NextLocation returns the next available point from the XML document.
func (d *document) NextLocation(ctx context.Context) (*googlelocation.Location, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	for {
		d.i++

		if d.i >= len(d.Document.Placemark.Track.Coord) {
			return nil, nil
		}

		const requiredFields = 3
		coordFields := strings.Fields(d.Document.Placemark.Track.Coord[d.i])
		if len(coordFields) < requiredFields {
			continue
		}
		lon, lat, alt := coordFields[0], coordFields[1], coordFields[2]
		ts := d.Document.Placemark.Track.When[d.i]

		lonE7, err := googlelocation.FloatStringToIntE7(lon)
		if err != nil {
			continue
		}
		latE7, err := googlelocation.FloatStringToIntE7(lat)
		if err != nil {
			continue
		}
		altNum, _ := strconv.ParseFloat(alt, 64) // TODO: not really anything we can do about this error, but we should probably log it, yeah?

		return &googlelocation.Location{
			Original: point{
				when:  ts,
				lat:   lat,
				lon:   lon,
				alt:   alt,
				latE7: latE7,
				lonE7: lonE7,
			},
			LatitudeE7:  latE7,
			LongitudeE7: lonE7,
			Altitude:    altNum,
			Timestamp:   ts,
		}, nil
	}
}

type point struct {
	when          time.Time
	lat, lon, alt string
	latE7, lonE7  int64
}

type document struct {
	XMLName   xml.Name `xml:"kml"`
	Text      string   `xml:",chardata"`
	XMLNS     string   `xml:"xmlns,attr"`
	XMLNSGX   string   `xml:"gx,attr"`
	XMLNSKML  string   `xml:"kml,attr"`
	XMLNSAtom string   `xml:"atom,attr"`
	Document  struct {
		Text      string `xml:",chardata"`
		Name      string `xml:"name"`
		Placemark struct {
			Text  string `xml:",chardata"`
			Track struct {
				Text  string      `xml:",chardata"`
				When  []time.Time `xml:"when"`
				Coord []string    `xml:"coord"`
			} `xml:"Track"`
		} `xml:"Placemark"`
	} `xml:"Document"`

	i int // our iterator through the track points
}
