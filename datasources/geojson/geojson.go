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

package geojson

import (
	"context"
	"encoding/json"
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

// NOTE: This is very similar to the kmlgx importer, except it's JSON.
// Almost all other code is nearly identical.

func init() {
	err := timeline.RegisterDataSource(timeline.DataSource{
		Name:            "geojson",
		Title:           "GeoJSON",
		Icon:            "geojson.svg",
		Description:     "GeoJSON files containing a collection of points",
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

	// If true, coordinate arrays beyond 2 elements will attempt
	// to be decoded in non-spec-compliant ways, which is useful
	// if the source data is non-compliant. If the optional 3rd
	// element is too big for altitude, it will be tried as
	// Unix timestamp (seconds); and if a fourth element exists,
	// both third and fourth elements will be tried as timestamp
	// or altitude, depending on their magnitude. See #23.
	Lenient bool `json:"lenient,omitempty"`
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
			if ext == ".geojson" {
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
			if ext != ".geojson" {
				return nil
			}

			file, err := fsys.Open(fpath)
			if err != nil {
				return err
			}
			defer file.Close()

			// create JSON decoder (wrapped to track some state as it decodes)
			jsonDec := &decoder{Decoder: json.NewDecoder(file), lenient: dsOpt.Lenient}

			// create location processor to clean up any noisy raw data
			locProc, err := googlelocation.NewLocationProcessor(jsonDec, dsOpt.Simplification)
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

				point := l.Original.(feature)

				meta := timeline.Metadata{
					"Provider": point.Properties.Provider,
					"Velocity": point.Properties.Speed,   // same key as with Google Location History
					"Heading":  point.Properties.Bearing, // same key as with Google Location History
				}
				meta.Merge(l.Metadata, timeline.MetaMergeReplace)

				item := &timeline.Item{
					Classification: timeline.ClassLocation,
					Timestamp:      l.Timestamp,
					Timespan:       l.Timespan,
					Location:       l.Location(),
					Owner: timeline.Entity{
						ID: dsOpt.OwnerEntityID,
					},
					Metadata: meta,
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

// decoder wraps the JSON decoder to get the next location from the document.
// It tracks nesting state so we can be sure we're in the right part of the structure.
type decoder struct {
	*json.Decoder
	foundFeatures bool
	lenient       bool

	// state to persist as we potentially decode locations in batches, depending on structure
	current   feature
	positions []position
}

// NextLocation returns the next available point from the JSON document.
func (dec *decoder) NextLocation(ctx context.Context) (*googlelocation.Location, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		// if we've already decoded a batch of positions, return the next one until the batch is emptied
		if len(dec.positions) > 0 {
			var pos position
			pos, dec.positions = dec.positions[0], dec.positions[1:]
			return pos.location(dec.current, dec.lenient)
		}

		// if we haven't gotten to the 'features' part of the structure yet, keep going
		if !dec.foundFeatures {
			t, err := dec.Token()
			if err == io.EOF {
				break
			}
			if err != nil {
				return nil, fmt.Errorf("decoding next JSON token: %w", err)
			}

			if val, ok := t.(string); ok && val == "features" {
				tkn, err := dec.Token()
				if err != nil {
					return nil, fmt.Errorf("decoding token after features token: %w", err)
				}
				if delim, ok := tkn.(json.Delim); ok && delim == '[' {
					dec.foundFeatures = true
				}
			}
			continue
		}

		if !dec.More() {
			break
		}

		// decode the next feature!

		dec.current = feature{} // reset it since I'm not sure if Decode writes the fields in-place or if it swaps out everything
		if err := dec.Decode(&dec.current); err != nil {
			return nil, fmt.Errorf("invalid GeoJSON feature: %v", err)
		}

		switch dec.current.Geometry.Type {
		case "Point":
			var coord position
			if err := json.Unmarshal(dec.current.Geometry.Coordinates, &coord); err != nil {
				return nil, fmt.Errorf("invalid Point coordinates: %v", err)
			}
			return coord.location(dec.current, dec.lenient)
		case "LineString", "MultiPoint":
			if err := json.Unmarshal(dec.current.Geometry.Coordinates, &dec.positions); err != nil {
				return nil, fmt.Errorf("invalid %s coordinates: %v", dec.current.Geometry.Type, err)
			}
		case "MultiLineString":
			var manyPositions [][]position
			if err := json.Unmarshal(dec.current.Geometry.Coordinates, &manyPositions); err != nil {
				return nil, fmt.Errorf("invalid MultiLineString coordinates: %v", err)
			}
			dec.positions = []position{}
			for _, positions := range manyPositions {
				dec.positions = append(dec.positions, positions...)
			}
		default:
			// skip unsupported types
			continue
		}
	}

	return nil, nil
}

// see https://datatracker.ietf.org/doc/html/rfc7946#section-3.1
type feature struct {
	Type       string `json:"type"`
	Properties struct {
		Time     time.Time `json:"time"`
		Provider string    `json:"provider"`
		TimeLong int64     `json:"time_long"`
		Accuracy float64   `json:"accuracy"`
		Altitude float64   `json:"altitude"`
		Bearing  float64   `json:"bearing"`
		Speed    float64   `json:"speed"`
	} `json:"properties,omitempty"`
	Geometry struct {
		Type        string          `json:"type"`
		Coordinates json.RawMessage `json:"coordinates"`
	} `json:"geometry"`
}

// https://datatracker.ietf.org/doc/html/rfc7946#section-3.1.1
type position []float64

func (p position) location(feature feature, lenient bool) (*googlelocation.Location, error) {
	if count := len(p); count < 2 {
		return nil, fmt.Errorf("expected at least two values for coordinate, got %d: %+v", count, p)
	}
	latE7, err := googlelocation.FloatToIntE7(p[1])
	if err != nil {
		return nil, err
	}
	lonE7, err := googlelocation.FloatToIntE7(p[0])
	if err != nil {
		return nil, err
	}
	altitude, ts := feature.Properties.Altitude, feature.Properties.Time
	if ts.IsZero() && feature.Properties.TimeLong != 0 {
		ts = time.UnixMilli(feature.Properties.TimeLong)
	}

	// the GeoJSON spec advises against supporting more than the optional 3rd element (altitude),
	// but we've seen messy data (https://github.com/timelinize/timelinize/issues/23) where the
	// third element is timestamp and even a fourth element is altitude sometimes (!!)...
	// that blatantly violates the spec, but we can maybe do some basic sanity checks to see
	// if we can assume those values
	if len(p) > 2 {
		if lenient {
			// non-spec-compliant
			var inferredAltitude float64
			var inferredTimestamp time.Time

			minAltitude, maxAltitude := -100.0, 20000.0 // meters
			if p[2] > maxAltitude {
				inferredTimestamp = time.Unix(int64(p[2]), 0)
			} else if p[2] > minAltitude {
				altitude = p[2]
			}

			if len(p) > 3 {
				if p[3] > maxAltitude {
					inferredTimestamp = time.Unix(int64(p[3]), 0)
				} else if p[3] > minAltitude {
					altitude = p[3]
				}
			}

			if altitude == 0 && inferredAltitude != 0 {
				altitude = inferredAltitude
			}
			if ts.IsZero() && !inferredTimestamp.IsZero() {
				ts = inferredTimestamp
			}
		} else {
			// spec compliant; third element is optional but must be altitude in meters if present
			if altitude == 0 {
				altitude = p[2]
			}
		}
	}
	return &googlelocation.Location{
		Original:    feature,
		LatitudeE7:  latE7,
		LongitudeE7: lonE7,
		Altitude:    altitude,
		Uncertainty: feature.Properties.Accuracy,
		Timestamp:   ts,
	}, nil
}
