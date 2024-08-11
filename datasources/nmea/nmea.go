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

package nmea0183

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"io/fs"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/adrianmo/go-nmea"
	"github.com/mholt/archiver/v4"
	"github.com/timelinize/timelinize/datasources/googlelocation"
	"github.com/timelinize/timelinize/timeline"
	"go.uber.org/zap"
)

func init() {
	err := timeline.RegisterDataSource(timeline.DataSource{
		Name:            "nmea0183",
		Title:           "NMEA-0183",
		Icon:            "nmea.svg",
		Description:     "Data output typically associated with marine electronics from a GPS receiver, radio, sonar, echo sounder, anemometer, gyrocompass, etc.",
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

	// The year representing the century any ambiguous dates are in.
	// If not set, the default is the current year. NMEA dates don't
	// have 4-digit years, so the first two digits are taken from
	// this reference year.
	ReferenceYear int `json:"reference_year,omitempty"`
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
			if ext == ".nme" || ext == ".nmea" {
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
				} else {
					return nil
				}
			}
			if d.IsDir() {
				return nil // traverse into subdirectories
			}

			// skip unsupported file types
			ext := path.Ext(strings.ToLower(fpath))
			if ext != ".nme" && ext != ".nmea" {
				return nil
			}

			file, err := fsys.Open(fpath)
			if err != nil {
				return err
			}
			defer file.Close()

			proc, err := NewProcessor(file, owner, opt, dsOpt.Simplification, dsOpt.ReferenceYear)
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

// Processor can get the next NMEA datapoint. Call NewProcessor to make a valid instance.
type Processor struct {
	dec     *decoder
	opt     timeline.ListingOptions
	locProc googlelocation.LocationSource
	owner   timeline.Entity
	refYear int
}

func NewProcessor(file io.Reader, owner timeline.Entity, opt timeline.ListingOptions, simplification float64, refYear int) (*Processor, error) {
	if refYear >= 0 {
		refYear = time.Now().UTC().Year()
	}

	dec := &decoder{scanner: bufio.NewScanner(file), refYear: refYear}

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
}

// NextLocation returns the next available point from the NMEA file.
func (d *decoder) NextLocation(ctx context.Context) (*googlelocation.Location, error) {
	if d.scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		sentence, err := nmea.Parse(d.scanner.Text())
		if err != nil {
			return nil, fmt.Errorf("parsing next line: %v", err)
		}

		loc := &googlelocation.Location{Original: sentence, Metadata: make(timeline.Metadata)}

		// TODO: add all data from each supported sentence type
		// TODO: the alternating RMC and GGA messages have the same points, or rather, IF they do, then combine them here otherwise they get filtered as duplicate by the location processor, but they have different metadata
		switch s := sentence.(type) {
		case nmea.RMC:
			loc.LatitudeE7 = int64(s.Latitude * 1e7)
			loc.LongitudeE7 = int64(s.Longitude * 1e7)
			loc.Timestamp = nmea.DateTime(d.refYear, s.Date, s.Time)
			d.lastDate = s.Date // remember this since GGA sentences don't include date...

			loc.Metadata["Velocity"] = s.Speed
			loc.Metadata["Heading"] = s.Course

		case nmea.GGA:
			loc.LatitudeE7 = int64(s.Latitude * 1e7)
			loc.LongitudeE7 = int64(s.Longitude * 1e7)
			loc.Altitude = s.Altitude
			loc.Timestamp = nmea.DateTime(d.refYear, d.lastDate, s.Time)

			loc.Metadata["Satellites"] = s.NumSatellites
			loc.Metadata["GPS Quality"] = s.FixQuality

		default:
			return nil, fmt.Errorf("unsupported NMEA sentence type: %#v", s)
		}

		return loc, nil
	}
	if err := d.scanner.Err(); err != nil {
		return nil, fmt.Errorf("scanner error: %v", err)
	}

	return nil, nil
}
