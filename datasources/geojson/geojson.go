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

// Package geojson implements a data source for GeoJSON data (RFC 7946): https://geojson.org/
package geojson

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"path"
	"strings"
	"time"

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

// Options configures the data source.
type Options struct {
	// The ID of the owner entity. REQUIRED for linking entity in DB.
	// TODO: maybe an attribute ID instead, in case the data represents multiple people
	OwnerEntityID uint64 `json:"owner_entity_id"`

	// If true, coordinate arrays beyond 2 elements will attempt
	// to be decoded in non-spec-compliant ways, which is useful
	// if the source data is non-compliant. If the optional 3rd
	// element is too big for altitude, it will be tried as
	// Unix timestamp (seconds); and if a fourth element exists,
	// both third and fourth elements will be tried as timestamp
	// or altitude, depending on their magnitude. See #23.
	Lenient bool `json:"lenient,omitempty"`

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
	if strings.ToLower(path.Ext(dirEntry.Name())) == ".geojson" {
		rec.Confidence = 1
	}

	return rec, nil
}

// FileImport conducts an import of the data from a file.
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
		if ext := strings.ToLower(path.Ext(d.Name())); ext != ".geojson" {
			return nil
		}

		file, err := dirEntry.FS.Open(fpath)
		if err != nil {
			return err
		}
		defer file.Close()

		// create JSON decoder (wrapped to track some state as it decodes)
		jsonDec := &decoder{Decoder: json.NewDecoder(file), lenient: dsOpt.Lenient}

		// create location processor to clean up any noisy raw data
		locProc, err := googlelocation.NewLocationProcessor(jsonDec, dsOpt.LocationProcessingOptions)
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

			feature := l.Original.(feature)

			// use generic properties as metadata
			meta := timeline.Metadata(feature.Properties)
			meta = meta.HumanizeKeys()

			// fill in special-case, standardized metadata using
			// the same keys as with Google Location History
			meta["Velocity"] = feature.velocity
			meta["Heading"] = feature.heading

			// include any metadata added by location processor
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

			if params.Timeframe.ContainsItem(item, false) {
				params.Pipeline <- &timeline.Graph{Item: item}
			}
		}

		return nil
	})
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
			if errors.Is(err, io.EOF) {
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
			return nil, fmt.Errorf("invalid GeoJSON feature: %w", err)
		}

		// feature properties are basically arbitrary key-value pairs, but a few common
		// ones exist, such as time, altitude, etc; we extract what we can
		if err := dec.current.extractKnownProperties(); err != nil {
			return nil, fmt.Errorf("reading well-known properties of geojson feature: %w", err)
		}

		switch dec.current.Geometry.Type {
		case "Point":
			var coord position
			if err := json.Unmarshal(dec.current.Geometry.Coordinates, &coord); err != nil {
				return nil, fmt.Errorf("invalid Point coordinates: %w", err)
			}
			return coord.location(dec.current, dec.lenient)
		case "LineString", "MultiPoint":
			if err := json.Unmarshal(dec.current.Geometry.Coordinates, &dec.positions); err != nil {
				return nil, fmt.Errorf("invalid %s coordinates: %w", dec.current.Geometry.Type, err)
			}
		case "MultiLineString":
			var manyPositions [][]position
			if err := json.Unmarshal(dec.current.Geometry.Coordinates, &manyPositions); err != nil {
				return nil, fmt.Errorf("invalid MultiLineString coordinates: %w", err)
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
	Type       string         `json:"type"`
	Properties map[string]any `json:"properties,omitempty"`
	Geometry   struct {
		Type        string          `json:"type"`
		Coordinates json.RawMessage `json:"coordinates"`
	} `json:"geometry"`

	// certain values extracted (and removed) from "well-known" (obvious or common) keys in Properties
	time     time.Time
	altitude float64 // meters
	accuracy float64 // meters (higher values are less accurate; should probably be called "error" instead)
	velocity float64 // meters per second
	heading  float64 // degrees
}

func (f *feature) extractKnownProperties() error {
	// we use this to try to guess whether Unix timestamp may be in seconds or milliseconds
	const year2286ApproxUnixSec = 10000000000

	// time
	for _, propName := range []string{
		"time",
		"timestamp",
		"time_long",
		"datetime",
		"date_time",
	} {
		// stop trying once we've got a time value
		if !f.time.IsZero() {
			break
		}

		switch val := f.Properties[propName].(type) {
		case string:
			for _, format := range []string{
				time.RFC3339,
				time.RFC3339Nano,
				time.RFC850,
				time.RFC822,
				time.RFC822Z,
				time.RFC1123,
				time.RFC1123Z,
			} {
				t, err := time.Parse(format, val)
				if err == nil {
					f.time = t
					delete(f.Properties, propName)
					break
				}
			}
		case int:
			if val < year2286ApproxUnixSec {
				f.time = time.Unix(int64(val), 0)
				delete(f.Properties, propName)
			} else {
				f.time = time.UnixMilli(int64(val))
				delete(f.Properties, propName)
			}
		case int64:
			if val < year2286ApproxUnixSec {
				f.time = time.Unix(val, 0)
				delete(f.Properties, propName)
			} else {
				f.time = time.UnixMilli(val)
				delete(f.Properties, propName)
			}
		case float64:
			sec, dec := math.Modf(val)
			if sec < year2286ApproxUnixSec {
				f.time = time.Unix(int64(sec), int64(dec*(1e9)))
				delete(f.Properties, propName)
			} else {
				f.time = time.UnixMilli(int64(sec)) // we don't store more precise than milliseconds
				delete(f.Properties, propName)
			}
		case nil:
			// having a time property isn't mandatory, ignore
		default:
			return fmt.Errorf("unexpected type for time property %s: %T", propName, val)
		}
	}

	// altitude
	for _, propName := range []string{
		"altitude",
		"elevation",
		"height",
	} {
		// stop trying once we've got a value
		if f.altitude != 0 {
			break
		}
		switch val := f.Properties[propName].(type) {
		case int:
			f.altitude = float64(val)
			delete(f.Properties, propName)
		case int64:
			f.altitude = float64(val)
			delete(f.Properties, propName)
		case float64:
			f.altitude = val
			delete(f.Properties, propName)
		}
	}

	// accuracy
	for _, propName := range []string{
		"accuracy",
	} {
		// stop trying once we've got a value
		if f.accuracy != 0 {
			break
		}
		switch val := f.Properties[propName].(type) {
		case int:
			f.accuracy = float64(val)
			delete(f.Properties, propName)
		case int64:
			f.accuracy = float64(val)
			delete(f.Properties, propName)
		case float64:
			f.accuracy = val
			delete(f.Properties, propName)
		}
	}

	// heading
	for _, propName := range []string{
		"heading",
		"bearing",
		"direction",
	} {
		// stop trying once we've got a value
		if f.heading != 0 {
			break
		}
		switch val := f.Properties[propName].(type) {
		case int:
			f.heading = float64(val)
			delete(f.Properties, propName)
		case int64:
			f.heading = float64(val)
			delete(f.Properties, propName)
		case float64:
			f.heading = val
			delete(f.Properties, propName)
		}
	}

	// velocity
	for _, propName := range []string{
		"velocity",
		"speed",
	} {
		// stop trying once we've got a value
		if f.velocity != 0 {
			break
		}
		switch val := f.Properties[propName].(type) {
		case int:
			f.velocity = float64(val)
			delete(f.Properties, propName)
		case int64:
			f.velocity = float64(val)
			delete(f.Properties, propName)
		case float64:
			f.velocity = val
			delete(f.Properties, propName)
		}
	}

	return nil
}

// https://datatracker.ietf.org/doc/html/rfc7946#section-3.1.1
type position []float64

func (p position) location(feature feature, lenient bool) (*googlelocation.Location, error) {
	const minDimensions = 2
	if count := len(p); count < minDimensions {
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
	altitude, ts := feature.altitude, feature.time

	// the GeoJSON spec advises against supporting more than the optional 3rd element (altitude),
	// but we've seen messy data (https://github.com/timelinize/timelinize/issues/23) where the
	// third element is timestamp and even a fourth element is altitude sometimes (!!)...
	// that blatantly violates the spec, but we can maybe do some basic sanity checks to see
	// if we can assume those values
	if len(p) > minDimensions {
		if lenient {
			// non-spec-compliant
			var inferredAltitude float64
			var inferredTimestamp time.Time
			minAltitude, maxAltitude := -100.0, 20000.0 // meters

			// iterate remaining positions
			var remaining = len(p) - 2
			for i := range remaining {
				if p[2+i] > maxAltitude { // time can occur in any position
					inferredTimestamp = time.Unix(int64(p[2+i]), 0)
				} else if i == 0 && p[2+i] > minAltitude { // altitude must be in first position after coordinates
					inferredAltitude = p[2+i]
				}
				// remaining positions can be altitude, speed, signal quality, number of satellites, etc.
				// these are not easily detectable though.
			}

			if altitude == 0 && inferredAltitude != 0 {
				altitude = inferredAltitude
			}
			if ts.IsZero() && !inferredTimestamp.IsZero() {
				ts = inferredTimestamp
			}
		} else if altitude == 0 {
			// spec compliant; third element is optional but must be altitude in meters if present
			altitude = p[2]
		}
	}
	return &googlelocation.Location{
		Original:    feature,
		LatitudeE7:  latE7,
		LongitudeE7: lonE7,
		Altitude:    altitude,
		Uncertainty: feature.accuracy,
		Timestamp:   ts,
	}, nil
}
