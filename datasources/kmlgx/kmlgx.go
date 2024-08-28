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
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/mholt/archiver/v4"
	"github.com/timelinize/timelinize/datasources/googlelocation"
	"github.com/timelinize/timelinize/timeline"
	"go.uber.org/zap"
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

type Options struct {
	// The ID of the owner entity. REQUIRED for linking entity in DB.
	// TODO: maybe an attribute ID instead, in case the data represents multiple people
	OwnerEntityID int64 `json:"owner_entity_id"`

	Simplification float64 `json:"simplification,omitempty"`
}

// FileImporter implements the timeline.FileImporter interface.
type FileImporter struct{}

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
				} else {
					return nil
				}
			}

			totalCount++

			ext := strings.ToLower(filepath.Ext(fpath))
			if ext == ".kml" {
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

func (fi *FileImporter) FileImport(ctx context.Context, filenames []string, itemChan chan<- *timeline.Graph, opt timeline.ListingOptions) error {
	dsOpt := opt.DataSourceOptions.(*Options)

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
				} else {
					return nil
				}
			}
			if d.IsDir() {
				return nil // traverse into subdirectories
			}

			// skip unsupported file types
			ext := path.Ext(strings.ToLower(fpath))
			if ext != ".kml" {
				return nil
			}

			file, err := fsys.Open(fpath)
			if err != nil {
				return err
			}
			defer file.Close()

			var doc document
			err = xml.NewDecoder(file).Decode(&doc)
			if err != nil {
				return err
			}
			doc.i = -1

			if doc.XMLNSGX != "http://www.google.com/kml/ext/2.2" {
				// actually, we may not need this version specifically; other versions might work just as well
				return fmt.Errorf("KML document does not support Google extension 'gx' namespace")
			}
			if len(doc.Document.Placemark.Track.When) != len(doc.Document.Placemark.Track.Coord) {
				return fmt.Errorf("corrupt gx:Track data in Placemark: number of timestamps does not match number of coordinates")
			}

			// create location processor to clean up any noisy raw data
			locProc, err := googlelocation.NewLocationProcessor(&doc, dsOpt.Simplification)
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

				if opt.Timeframe.ContainsItem(item, false) {
					itemChan <- &timeline.Graph{Item: item}
				}
			}

			return nil
		})
		if err != nil {
			return err
		}
	}

	return nil
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

		coordFields := strings.Fields(d.Document.Placemark.Track.Coord[d.i])
		if len(coordFields) < 3 {
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
