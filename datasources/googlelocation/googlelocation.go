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

// Package googlelocation implements a data source for  importing data from
// the Google Location History (aka Google Maps Timeline).
//
// I found this website very helpful as documentation of the Takeout format:
// https://locationhistoryformat.com/
package googlelocation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"path"
	"strconv"
	"strings"
	"sync"

	"github.com/timelinize/timelinize/timeline"
	"go.uber.org/zap"
)

func init() {
	err := timeline.RegisterDataSource(timeline.DataSource{
		Name:            "google_location",
		Title:           "Google Location History",
		Icon:            "googlelocation.svg",
		Description:     "A Google Takeout archive containing location history data.",
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
	OwnerEntityID int64 `json:"owner_entity_id"`

	// Set to a value 1-10 to enable path simplification.
	// 10 means very aggressive simplification (skip many
	// points, leave practically only clusters or endpoints)
	// and 1 means to only drop points on the straightest paths.
	// (My preferred is ~2 when scaled to between 1000 and 50000; i.e. about epsilon=6-7k)
	Simplification float64 `json:"simplification,omitempty"`

	// When importing location data that was stored only on-device,
	// any actual information about the device is not available
	// unless the user provides a name or ID manually.
	Device string `json:"device,omitempty"`

	// keyed by deviceTag from Settings.json
	devices map[int64]deviceSettings
}

// FileImporter implements the timeline.FileImporter interface.
type FileImporter struct {
	ctx   context.Context
	d     timeline.DirEntry
	opt   timeline.ImportParams
	dsOpt *Options

	// device affinity: if seenDevices is not nil, then each
	// DeviceTag will be treated as a separate path, and only
	// tags equaling deviceTag will be included
	seenDevices   map[int64]struct{}
	seenDevicesMu *sync.Mutex

	checkpoint checkpoint       // incoming checkpoint (from the DB)
	positions  safePositionsMap // for outgoing checkpoints (to the DB)

	wg       *sync.WaitGroup
	throttle chan struct{}
}

// Recognize returns whether the file is supported.
func (FileImporter) Recognize(_ context.Context, dirEntry timeline.DirEntry, _ timeline.RecognizeParams) (timeline.Recognition, error) {
	if dirEntry.IsDir() {
		// see if it's a Takeout-structured location history (a folder with Records.json in it)
		pathToTry := path.Join(dirEntry.Filename, "Records.json")
		if strings.Contains(dirEntry.Name(), "Location History") && timeline.FileExistsFS(dirEntry.FS, pathToTry) {
			return timeline.Recognition{Confidence: 1}, nil
		}
	} else if dirEntry.Name() == "location-history.json" { // avoid opening all JSON files (can be slow esp. in archives)
		//  check for the newer on-device-only location history file from Q2 2024.
		f, err := dirEntry.Open()
		if err != nil {
			return timeline.Recognition{}, err
		}
		defer f.Close()
		dec := json.NewDecoder(f)
		if token, err := dec.Token(); err == nil {
			if _, ok := token.(json.Delim); ok {
				var loc onDeviceLocation
				if err := dec.Decode(&loc); err == nil {
					if !loc.StartTime.IsZero() && !loc.EndTime.IsZero() {
						return timeline.Recognition{Confidence: 1}, nil
					}
				}
			}
		}
	}
	return timeline.Recognition{}, nil
}

// FileImport imports data from a file.
func (fi *FileImporter) FileImport(ctx context.Context, dirEntry timeline.DirEntry, params timeline.ImportParams) error {
	fi.dsOpt = params.DataSourceOptions.(*Options)
	fi.ctx = ctx
	fi.opt = params
	fi.d = dirEntry

	// verify input configuration
	if fi.dsOpt.Simplification < 0 || fi.dsOpt.Simplification > 10 {
		return fmt.Errorf("invalid simplification factor; must be in [1,10]: %f", fi.dsOpt.Simplification)
	}

	// load prior checkpoint, if set
	if params.Checkpoint != nil {
		err := json.Unmarshal(params.Checkpoint, &fi.checkpoint)
		if err != nil {
			return fmt.Errorf("decoding checkpoint: %w", err)
		}
	}

	// see if this is a (now-legacy) Takeout archive of location history
	if dirEntry.IsDir() {
		// try to load settings file; this helps us identify devices; however
		// this list is often incomplete, especially if user has removed them
		// from their Google account
		settings, err := loadSettingsFromTakeoutArchive(dirEntry)
		if err != nil && errors.Is(err, fs.ErrNotExist) {
			params.Log.Warn("no Settings.json file found; some information may be lacking")
		}

		// key device settings to their device tag for future storage in DB
		fi.dsOpt.devices = make(map[int64]deviceSettings)
		for _, dev := range settings.DeviceSettings {
			fi.dsOpt.devices[dev.DeviceTag] = dev
		}

		// The data looks much better when we only process one path
		// at a time (a path being the points belonging to a DeviceTag),
		// so in order to do this, we iterate the input multiple times
		// concurrently - once per device. In this main goroutine we
		// simply look for the first device and "claim" it. As iteration
		// continues, the first device tag after that which is different
		// and unclaimed is claimed for a new goroutine, and a new
		// goroutine is spawned to scan the dataset for just that device;
		// and this process continues until all discovered devices have
		// been claimed. We limit the number of max goroutines to prevent
		// unbounded memory growth.
		fi.seenDevices = make(map[int64]struct{})
		fi.seenDevicesMu = new(sync.Mutex)

		fi.positions = safePositionsMap{
			Mutex:     new(sync.Mutex),
			positions: make(map[int64]int),
		}

		const maxGoroutines = 128
		fi.wg = new(sync.WaitGroup)
		fi.throttle = make(chan struct{}, maxGoroutines)

		fi.wg.Add(1)
		err = fi.processFile(ctx, &decoder{fi: fi})
		if err != nil {
			return fmt.Errorf("top scan processing %s: %w", dirEntry.Filename, err)
		}
		fi.wg.Done()

		fi.wg.Wait()

		return nil
	}

	// otherwise, this is a location history file that is the newer
	// on-device location history export starting from Q2 2024

	f, err := dirEntry.Open()
	if err != nil {
		return err
	}
	defer f.Close()

	dec := json.NewDecoder(f)

	// consume opening '['
	if token, err := dec.Token(); err != nil {
		return err
	} else if tkn, ok := token.(json.Delim); !ok || tkn != '[' {
		return fmt.Errorf("unexpected opening token: %v", token)
	}

	onDevDec := &onDeviceDecoder{Decoder: dec}
	locProc, err := NewLocationProcessor(onDevDec, fi.dsOpt.Simplification)
	if err != nil {
		return err
	}

	var i int
	for {
		if err := fi.ctx.Err(); err != nil {
			return err
		}

		result, err := locProc.NextLocation(ctx)
		if err != nil {
			return err
		}
		if result == nil {
			break
		}

		// fast-forward to checkpoint, if set
		if fi.checkpoint.Format2024 > 0 && i < fi.checkpoint.Format2024 {
			i++
			continue
		}

		item := result.Original.(*onDeviceLocation).toItem(result, fi.dsOpt)

		if fi.opt.Timeframe.ContainsItem(item, false) {
			params.Pipeline <- &timeline.Graph{Item: item, Checkpoint: checkpoint{Format2024: i}}
		}

		i++
	}

	return nil
}

type checkpoint struct {
	Legacy     safePositionsMap `json:"legacy,omitempty"` // map of device tag to position/index
	Format2024 int              `json:"format_2024,omitempty"`
}

type decoder struct {
	*json.Decoder

	fi *FileImporter

	// if non-zero, the device we're supposed to look for
	deviceTag int64
}

// NextLocation decodes the next unique location; it returns nil, nil
// if no more locations are available. It skips duplicated or very
// similar adjacent locations. It also enforces device affinity, meaning
// that it will only get points for a specific device during its scan
// (if enabled). If the import looks like it's stalling for a long time,
// it is probably trying to find the next location data point with a
// certain deviceTag; each deviceTag found requires 1 scan through all
// the data, so it's O(N*n) where N is the number of deviceTags and n
// is the number of data points. This is not great, but I don't know a
// better way to do it. We do, at least, perform these scans in parallel,
// and the only cost of skipping points is decoding them in their goroutine.
// When a new device is discovered, a new goroutine is spawned to process it.
func (dec *decoder) NextLocation(ctx context.Context) (*Location, error) {
	for dec.More() {
		var newLoc *location
		if err := dec.Decode(&newLoc); err != nil {
			return nil, fmt.Errorf("decoding location element: %w", err)
		}

		// enforce device affinity: if enabled, only process points
		// associated with the given deviceTag
		dec.fi.seenDevicesMu.Lock()
		if dec.fi.seenDevices != nil {
			// see if the device for this data point is already claimed by a goroutine
			if _, claimed := dec.fi.seenDevices[newLoc.DeviceTag]; claimed {
				// a goroutine is working on this device; is it ours?
				if newLoc.DeviceTag != dec.deviceTag {
					// not ours; skip it
					dec.fi.seenDevicesMu.Unlock()
					continue
				}
				// it is ours, so we'll just go out of this block
			} else {
				// a new unclaimed device! who will get it?
				dec.fi.seenDevices[newLoc.DeviceTag] = struct{}{}

				if dec.deviceTag == 0 {
					// this goroutine has no assignment yet, so we'll claim this one
					dec.deviceTag = newLoc.DeviceTag
				} else {
					// we are assigned a different one, but we can start a new goroutine to work on this one

					dec.fi.throttle <- struct{}{}
					dec.fi.wg.Add(1)

					go func(deviceTag int64) {
						defer func() {
							<-dec.fi.throttle
							dec.fi.wg.Done()
						}()

						// assign the new goroutine this device tag
						err := dec.fi.processFile(ctx, &decoder{
							fi:        dec.fi,
							deviceTag: deviceTag,
						})
						if err != nil {
							dec.fi.opt.Log.Error("processing file for specific device",
								zap.Int64("device_tag", deviceTag),
								zap.Error(err))
						}
					}(newLoc.DeviceTag)

					dec.fi.seenDevicesMu.Unlock()
					continue
				}
			}
		}
		dec.fi.seenDevicesMu.Unlock()

		dec.fi.positions.Lock()
		dec.fi.positions.positions[dec.deviceTag]++

		// fast forward to checkpoint, if set
		if leftOffAt, ok := dec.fi.checkpoint.Legacy.positions[dec.deviceTag]; ok && dec.fi.positions.positions[dec.deviceTag] < leftOffAt {
			dec.fi.positions.Unlock()
			continue
		}
		dec.fi.positions.Unlock()

		return &Location{
			Original:    newLoc,
			LatitudeE7:  newLoc.LatitudeE7,
			LongitudeE7: newLoc.LongitudeE7,
			Altitude:    float64(newLoc.Altitude),
			Uncertainty: float64(newLoc.Accuracy),
			Timestamp:   newLoc.Timestamp,
		}, nil
	}

	return nil, nil
}

func (fi *FileImporter) processFile(ctx context.Context, dec *decoder) error {
	// we're not sure if the user gave the JSON file directly
	// or whether it's in the Takeout archive
	var file fs.File
	var err error
	for _, pathToTry := range []string{
		takeoutLocationHistoryPath2024,
		takeoutLocationHistoryPathPre2024,
	} {
		file, err = flexibleOpen(fi.d, path.Join(pathToTry, "Records.json"))
		if err == nil || !errors.Is(err, fs.ErrNotExist) {
			break
		}
	}
	if err != nil {
		return fmt.Errorf("locating data file: %w", err)
	}
	defer file.Close()

	dec.Decoder = json.NewDecoder(file)

	// read the following opening tokens:
	// 1. open brace '{'
	// 2. "locations" field name,
	// 3. the array value's opening bracket '['
	for range 3 {
		_, err := dec.Token()
		if err != nil {
			return fmt.Errorf("decoding opening token: %w", err)
		}
	}

	locProc, err := NewLocationProcessor(dec, fi.dsOpt.Simplification)
	if err != nil {
		return err
	}

	for {
		if err := fi.ctx.Err(); err != nil {
			return err
		}

		result, err := locProc.NextLocation(ctx)
		if err != nil {
			return err
		}
		if result == nil {
			break
		}

		l := result.Original.(*location)
		l.LatitudeE7 = result.LatitudeE7
		l.LongitudeE7 = result.LongitudeE7
		l.Timestamp = result.Timestamp
		l.timespan = result.Timespan
		if l.meta == nil {
			l.meta = make(timeline.Metadata)
		}
		l.meta.Merge(result.Metadata, timeline.MetaMergeReplace)

		item := l.toItem(fi.dsOpt)
		if fi.opt.Timeframe.ContainsItem(item, false) {
			fi.opt.Pipeline <- &timeline.Graph{Item: item, Checkpoint: checkpoint{Legacy: fi.positions}}
		}
	}

	return nil
}

// safePositionsMap makes it safe to give a map of each goroutine's position
// in the location history file to the processor to create a checkpoint, which
// happens concurrently with us; so it implements MarshalJSON() to obtains the
// same lock we use.
type safePositionsMap struct {
	*sync.Mutex
	positions map[int64]int
}

func (spm safePositionsMap) MarshalJSON() ([]byte, error) {
	spm.Lock()
	defer spm.Unlock()
	return json.Marshal(spm.positions)
}

// // timeDelta returns the difference between times a and b,
// // but always returns a positive duration (absolute value).
// func timeDelta(a, b time.Time) time.Duration {
// 	if a.After(b) {
// 		return a.Sub(b)
// 	}
// 	return b.Sub(a)
// }

// FloatToIntE7 converts a float into the equivalent integer value
// with the decimal point moved right 7 places by string manipulation
// so no loss of precision occurs.
func FloatToIntE7(coord float64) (int64, error) {
	return FloatStringToIntE7(strconv.FormatFloat(coord, 'f', -1, 64))
}

// FloatStringToIntE7 is the same thing as FloatToIntE7, but takes
// a string representation of a float as input.
func FloatStringToIntE7(coord string) (int64, error) {
	dotPos := strings.Index(coord, ".")
	endPos := dotPos + 1 + places
	if endPos >= len(coord) {
		coord += strings.Repeat("0", endPos-len(coord))
		endPos = len(coord)
	}
	reconstructed := coord[:dotPos] + coord[dotPos+1:endPos]

	return strconv.ParseInt(reconstructed, 10, 64)
}

// flexibleOpen tries opening the given file directly first; then if not found, it tries
// a "top dir open" (strips the first path component - the "top dir") in case the user
// selected the folder created by extracting the archive; then if not found it tries
// just the last path component in case the user navigated into the subfolder.
func flexibleOpen(d timeline.DirEntry, filename string) (file fs.File, err error) {
	// perhaps archive was extracted and the "Takeout" folder was selected
	file, err = d.TopDirOpen(filename)
	if errors.Is(err, fs.ErrNotExist) {
		// okay, maybe they just selected the Location History subfolder
		file, err = d.FS.Open(path.Join(d.Filename, path.Base(filename)))
	}
	return
}

// haversineDistanceEarth computes the great-circle distance in kilometers between two points on Earth.
// The latitude and longitude values must be integer degrees 1e7 times their actual values (to preserve precision).
// TODO: consider using Vincenty distance? but that is way more expensive
func haversineDistanceEarth(lat1E7, lon1E7, lat2E7, lon2E7 int64) float64 {
	lat1Fl, lon1Fl, lat2Fl, lon2Fl :=
		float64(lat1E7)/placesMult, float64(lon1E7)/placesMult,
		float64(lat2E7)/placesMult, float64(lon2E7)/placesMult

	phi1 := degreesToRadians(lat1Fl)
	phi2 := degreesToRadians(lat2Fl)
	lambda1 := degreesToRadians(lon1Fl)
	lambda2 := degreesToRadians(lon2Fl)

	return 2 * earthRadiusKm * math.Asin(math.Sqrt(haversin(phi2-phi1)+math.Cos(phi1)*math.Cos(phi2)*haversin(lambda2-lambda1)))
}

func haversin(theta float64) float64 {
	return 0.5 * (1 - math.Cos(theta)) //nolint:mnd
}

func degreesToRadians(d float64) float64 {
	return d * (math.Pi / 180) //nolint:mnd
}

const (
	earthRadiusMi = 3958
	earthRadiusKm = 6371
)

// The path within the Google Takeout archive of the location history records.
const (
	takeoutLocationHistoryPathPre2024 = "Location History"
	takeoutLocationHistoryPath2024    = "Location History (Timeline)"
)
