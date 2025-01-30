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

	checkpoint checkpoint

	wg       *sync.WaitGroup
	throttle chan struct{}
}

// Recognize returns whether the file is supported.
func (fi FileImporter) Recognize(_ context.Context, dirEntry timeline.DirEntry, _ timeline.RecognizeParams) (timeline.Recognition, error) {
	if rec := fi.recognizeLegacyTakeoutFormat(dirEntry); rec.Confidence > 0 {
		return rec, nil
	}
	if rec, err := fi.recognizeOnDevice2024iOSFormat(dirEntry); rec.Confidence > 0 || err != nil {
		return rec, err
	}
	if rec, err := fi.recognizeOnDevice2025AndroidFormat(dirEntry); rec.Confidence > 0 || err != nil {
		return rec, err
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

	// delegate decoding+processing to the detected format decoder
	if ok, err := fi.decodeOnDevice2025AndroidFormat(ctx, dirEntry, params); ok {
		return err
	}
	if ok, err := fi.decodeOnDevice2024iOSFormat(ctx, dirEntry, params); ok {
		return err
	}
	if ok, err := fi.decodeLegacyTakeoutFormat(ctx, dirEntry, params); ok {
		return err
	}

	return errors.New("location history format not detected")
}

type checkpoint struct {
	Legacy            *safePositionsMap `json:"legacy,omitempty"` // map of device tag to position/index
	FormatiOS2024     int               `json:"format_ios_2024,omitempty"`
	FormatAndroid2025 int               `json:"format_android_2025,omitempty"`
}

type decoder struct {
	*json.Decoder

	fi *FileImporter

	// if non-zero, the device we're supposed to look for
	deviceTag int64

	// for resuming from checkpoints and marking checkpoints
	fastForwardTo int
	position      int
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
		if err := ctx.Err(); err != nil {
			return nil, err
		}

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

					dec.fi.checkpoint.Legacy.Lock()
					dec.fastForwardTo = dec.fi.checkpoint.Legacy.Positions[dec.deviceTag]
					dec.fi.checkpoint.Legacy.Unlock()
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

						dec.fi.checkpoint.Legacy.Lock()
						fastForwardTo := dec.fi.checkpoint.Legacy.Positions[deviceTag]
						dec.fi.checkpoint.Legacy.Unlock()

						err := dec.fi.processFile(ctx, &decoder{
							fi:            dec.fi,
							deviceTag:     deviceTag,
							fastForwardTo: fastForwardTo,
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

		// fast-forward to checkpoint, if set; otherwise, mark current position for next checkpoint
		dec.position++
		if dec.fastForwardTo > 0 && dec.position < dec.fastForwardTo {
			continue
		}
		dec.fi.checkpoint.Legacy.Lock()
		dec.fi.checkpoint.Legacy.Positions[dec.deviceTag] = dec.position
		dec.fi.checkpoint.Legacy.Unlock()

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
			fi.opt.Pipeline <- &timeline.Graph{Item: item, Checkpoint: fi.checkpoint}
		}
	}

	return nil
}

// safePositionsMap makes it safe to give a map of each goroutine's position
// in the location history file to the processor to create a checkpoint, which
// happens concurrently with us; so it implements MarshalJSON() to obtains the
// same lock we use.
type safePositionsMap struct {
	sync.Mutex `json:"-"`
	Positions  map[int64]int `json:"positions,omitempty"` // used for unmarshaling/restoring the checkpoint, since we custom-marshal this struct
}

func (spm *safePositionsMap) MarshalJSON() ([]byte, error) {
	// can't marshal the whole struct itself since this method gets called and
	// deadlocks, so we marshal the positions map and then craft the JSON manually

	spm.Lock()
	mapBytes, err := json.Marshal(spm.Positions)
	spm.Unlock()
	if err != nil {
		return mapBytes, err
	}

	prefix, suffix := []byte(`{"positions":`), []byte("}")
	result := make([]byte, 0, len(prefix)+len(mapBytes)+len(suffix))

	result = append(result, prefix...)
	result = append(result, mapBytes...)
	result = append(result, suffix...)

	return result, nil
}

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
