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

// Package nmea0183 implements a data source for NMEA 0183 logs (radios, marine electronics, etc).
// The official NMEA Standard is expensive ($7,500 for acadamic and testing purposes -- an absolute
// scam; $10k if you want to implement it! but only $1k if you're a member of the NMEA):
// https://www.nmea.org/nmea-0183.html
//
// Here are some free reference manuals that have the most important information, which I used
// to write this package:
// - https://receiverhelp.trimble.com/alloy-gnss/en-us/NMEA-0183messages_MessageOverview.html
// - https://www.sparkfun.com/datasheets/GPS/NMEA%20Reference%20Manual-Rev2.1-Dec07.pdf
package nmea0183

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"path"
	"strings"
	"time"

	"github.com/adrianmo/go-nmea"
	"github.com/timelinize/timelinize/datasources/googlelocation"
	"github.com/timelinize/timelinize/timeline"
	"go.uber.org/zap"
)

func init() {
	err := timeline.RegisterDataSource(timeline.DataSource{
		Name:            "nmea0183",
		Title:           "NMEA-0183",
		Icon:            "nmea.jpg",
		Description:     "Data output typically associated with marine electronics from a GPS receiver, radio, sonar, echo sounder, anemometer, gyrocompass, etc.",
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

	// The year representing the century any ambiguous dates are in.
	// If not set, the default is the current year. NMEA dates don't
	// have 4-digit years, so the first two digits are taken from
	// this reference year.
	ReferenceYear int `json:"reference_year,omitempty"`
}

// FileImporter implements the timeline.FileImporter interface.
type FileImporter struct{}

// Recognize returns whether the input is supported.
func (FileImporter) Recognize(_ context.Context, dirEntry timeline.DirEntry, _ timeline.RecognizeParams) (timeline.Recognition, error) {
	rec := timeline.Recognition{DirThreshold: .9}

	// we can import directories, but let the import planner figure that out; only recognize files
	if dirEntry.IsDir() {
		return rec, nil
	}

	// recognize by file extension
	switch strings.ToLower(path.Ext(dirEntry.Name())) {
	case ".nme", ".nmea":
		rec.Confidence = 1
	}

	return rec, nil
}

// FileImport imports data from the data source.
func (fi *FileImporter) FileImport(ctx context.Context, dirEntry timeline.DirEntry, params timeline.ImportParams) error {
	dsOpt := params.DataSourceOptions.(*Options)

	owner := timeline.Entity{ID: dsOpt.OwnerEntityID}

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
		ext := strings.ToLower(path.Ext(fpath))
		if ext != ".nme" && ext != ".nmea" {
			return nil
		}

		file, err := dirEntry.FS.Open(fpath)
		if err != nil {
			return err
		}
		defer file.Close()

		proc, err := NewProcessor(file, owner, params, dsOpt.Simplification, dsOpt.ReferenceYear, params.Log.Named("nmea_processor"))
		if err != nil {
			return err
		}

		for {
			item, err := proc.NextNMEAItem(ctx)
			if err != nil {
				return err
			}
			if item == nil {
				break
			}
			params.Pipeline <- &timeline.Graph{Item: item}
		}

		return nil
	})
}

// Processor can get the next NMEA datapoint. Call NewProcessor to make a valid instance.
type Processor struct {
	dec     *decoder
	opt     timeline.ImportParams
	locProc googlelocation.LocationSource
	owner   timeline.Entity
	refYear int
}

// NewProcessor returns a new file processor.
func NewProcessor(file io.Reader, owner timeline.Entity, opt timeline.ImportParams,
	simplification float64, refYear int, logger *zap.Logger) (*Processor, error) {
	if refYear >= 0 {
		refYear = time.Now().UTC().Year()
	}

	dec := &decoder{scanner: bufio.NewScanner(file), refYear: refYear, logger: logger}

	// some radios (like my Yaesu) produce \r-delimited (carriage-return ONLY) newlines,
	// which the default scanner does not support. Use custom split function.
	dec.scanner.Split(scanLines)

	// create location processor to clean up any noisy raw data
	locProc, err := googlelocation.NewLocationProcessor(dec, simplification)
	if err != nil {
		return nil, err
	}

	return &Processor{
		dec:     dec,
		opt:     opt,
		locProc: locProc,
		owner:   owner,
		refYear: refYear,
	}, nil
}

// NextNMEAItem gets the next item from the next NMEA sentence(s).
func (p *Processor) NextNMEAItem(ctx context.Context) (*timeline.Item, error) {
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

		// TODO: determine whether we need to put source metadata in here, or if we can do it when we read the file in the decoder below...
		// point := l.Original.(trkpt)

		// meta := timeline.Metadata{
		// 	"Velocity":   point.Speed, // same key as with Google Location History
		// 	"Satellites": point.Sat,
		// }
		// meta.Merge(l.Metadata, timeline.MetaMergeReplace)

		item := &timeline.Item{
			Classification: timeline.ClassLocation,
			Timestamp:      l.Timestamp,
			Timespan:       l.Timespan,
			Location:       l.Location(),
			Owner:          p.owner,
			Metadata:       l.Metadata, // TODO: or 'meta' from the commented code above?
		}

		if p.opt.Timeframe.ContainsItem(item, false) {
			return item, nil
		}
	}

	return nil, nil
}

// decoder wraps the file reader to get the next location from the file.
type decoder struct {
	scanner  *bufio.Scanner
	refYear  int
	lastDate nmea.Date
	logger   *zap.Logger
}

// NextLocation returns the next available point from the NMEA file.
func (d *decoder) NextLocation(ctx context.Context) (*googlelocation.Location, error) {
	for d.scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		sentence, err := nmea.Parse(d.scanner.Text())
		if err != nil {
			return nil, fmt.Errorf("parsing next line: %w", err)
		}

		loc := &googlelocation.Location{Original: sentence, Metadata: make(timeline.Metadata)}

		// TODO: add all data from each supported sentence type
		// TODO: the alternating RMC and GGA messages have the same points, or rather, IF they do, then combine them here otherwise they get filtered as duplicate by the location processor, but they have different metadata
		switch s := sentence.(type) {
		case nmea.RMC:
			loc.LatitudeE7 = int64(s.Latitude * placesMult)
			loc.LongitudeE7 = int64(s.Longitude * placesMult)
			loc.Timestamp = nmea.DateTime(d.refYear, s.Date, s.Time)
			d.lastDate = s.Date // remember this since GGA sentences don't include date...

			loc.Metadata["Velocity"] = s.Speed * metersPerSecondPerKnot
			loc.Metadata["Heading"] = s.Course

		case nmea.GGA:
			if !d.lastDate.Valid {
				// No date... it's possible this came before any RMC lines, which means we don't
				// know which date the time occurred on. We could try to be clever and read ahead
				// to find a date, but that's complex and error-prone, and even then, there's no
				// guarantee that a line in the future was the same date as this one. The processor
				// will end up rejecting this if it becomes part of a cluster because a cluster has
				// to have a start timestamp if it has an end timestamp. I guess just drop this
				// data point. I don't know a better way to handle this.
				d.logger.Warn("encountered GGA sentence before any sentence with a date, so we cannot make timestamp; dropping data point",
					zap.String("raw", s.Raw))
				continue
			}
			loc.LatitudeE7 = int64(s.Latitude * placesMult)
			loc.LongitudeE7 = int64(s.Longitude * placesMult)
			loc.Altitude = s.Altitude
			loc.Timestamp = nmea.DateTime(d.refYear, d.lastDate, s.Time)

			loc.Metadata["Satellites"] = s.NumSatellites
			loc.Metadata["GPS Quality"] = s.FixQuality

		case nmea.VTG, nmea.GSA:
			// make these sentences no-ops for now, until we decide to use them
			// TODO: VTG seems to be redundant with RMC? and probably should use last known location and timestamp data, I guess?
			// case nmea.VTG:
			// 	loc.Metadata["Velocity"] = s.GroundSpeedKnots * metersPerSecondPerKnot
			// 	loc.Metadata["Heading"] = s.MagneticTrack

		default:
			return nil, fmt.Errorf("unsupported NMEA sentence type: %#v", s)
		}

		return loc, nil
	}
	if err := d.scanner.Err(); err != nil {
		return nil, fmt.Errorf("scanner error: %w", err)
	}

	return nil, nil
}

// scanLines is a bufio.SplitFunc for Scanners that tolerates variable newlines,
// including carriage-return-only. https://stackoverflow.com/a/74962607/1048862
func scanLines(data []byte, atEOF bool) (advance int, token []byte, err error) {
	if atEOF && len(data) == 0 {
		return 0, nil, nil
	}
	if i := bytes.IndexAny(data, "\r\n"); i >= 0 {
		if data[i] == '\n' {
			// We have a line terminated by single newline.
			return i + 1, data[0:i], nil
		}
		// We have a line terminated by carriage return at the end of the buffer.
		if !atEOF && len(data) == i+1 {
			return 0, nil, nil
		}
		advance = i + 1
		if len(data) > i+1 && data[i+1] == '\n' {
			advance++
		}
		return advance, data[0:i], nil
	}
	// If we're at EOF, we have a final, non-terminated line. Return it.
	if atEOF {
		return len(data), data, nil
	}
	// Request more data.
	return 0, nil, nil
}

// 1 knot is this many m/s
const metersPerSecondPerKnot = 0.514444

const placesMult = 1e7
