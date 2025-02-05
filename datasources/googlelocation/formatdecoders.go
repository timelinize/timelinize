package googlelocation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"sync"

	"github.com/timelinize/timelinize/timeline"
)

func (fi *FileImporter) decodeLegacyTakeoutFormat(ctx context.Context, dirEntry timeline.DirEntry, params timeline.ImportParams) (bool, error) {
	// see if this is a (now-legacy) Takeout archive of location history
	if !dirEntry.IsDir() {
		return false, nil
	}

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

	fi.checkpoint.Legacy = &safePositionsMap{Positions: make(map[int64]int)}

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

	const maxGoroutines = 128
	fi.wg = new(sync.WaitGroup)
	fi.throttle = make(chan struct{}, maxGoroutines)

	fi.wg.Add(1)
	err = fi.processFile(ctx, &decoder{fi: fi})
	if err != nil {
		return true, fmt.Errorf("top scan processing %s: %w", dirEntry.Filename, err)
	}
	fi.wg.Done()

	fi.wg.Wait()

	return true, nil
}

func (fi *FileImporter) decodeOnDevice2024iOSFormat(ctx context.Context, dirEntry timeline.DirEntry, params timeline.ImportParams) (bool, error) {
	if dirEntry.Name() != filenameFromiOSDevice {
		return false, nil
	}

	f, err := dirEntry.Open()
	if err != nil {
		return false, err
	}
	defer f.Close()

	dec := json.NewDecoder(f)

	// consume opening '[' (failure implies wrong format, not an actual error; try next format)
	if token, err := dec.Token(); err != nil {
		return false, nil
	} else if tkn, ok := token.(json.Delim); !ok || tkn != '[' {
		return false, nil
	}

	onDevDec := &onDeviceiOS2024Decoder{Decoder: dec}
	locProc, err := NewLocationProcessor(onDevDec, fi.dsOpt.Simplification)
	if err != nil {
		return true, err
	}

	var i int
	for {
		if err := fi.ctx.Err(); err != nil {
			return true, err
		}

		result, err := locProc.NextLocation(ctx)
		if err != nil {
			return true, err
		}
		if result == nil {
			break
		}

		// fast-forward to checkpoint, if set
		if fi.checkpoint.FormatiOS2024 > 0 && i < fi.checkpoint.FormatiOS2024 {
			i++
			continue
		}

		item := result.Original.(*onDeviceLocationiOS2024).toItem(result, fi.dsOpt)

		if fi.opt.Timeframe.ContainsItem(item, false) {
			params.Pipeline <- &timeline.Graph{Item: item, Checkpoint: checkpoint{FormatiOS2024: i}}
		}

		i++
	}

	return true, nil
}

func (fi *FileImporter) decodeOnDevice2025AndroidFormat(ctx context.Context, dirEntry timeline.DirEntry, params timeline.ImportParams) (bool, error) {
	if dirEntry.Name() != filenameFromAndroidDevice {
		return false, nil
	}

	f, err := dirEntry.Open()
	if err != nil {
		return false, err
	}
	defer f.Close()

	dec := json.NewDecoder(f)

	// consume the first few tokens until we get to the meat of the file
	expect := []func(json.Token) bool{
		func(t json.Token) bool {
			d, ok := t.(json.Delim)
			return ok && d == '{'
		},
		func(t json.Token) bool {
			s, ok := t.(string)
			return ok && s == "semanticSegments"
		},
		func(t json.Token) bool {
			d, ok := t.(json.Delim)
			return ok && d == '['
		},
	}
	for _, expected := range expect {
		// errors in here may just be an unsupported format; we can try next format
		token, err := dec.Token()
		if err != nil {
			return false, nil
		}
		if !expected(token) {
			return false, nil
		}
	}

	// we've arrived at the location data; from here, we can stream-decode the objects in the array
	onDevDec := &onDeviceAndroid2025Decoder{Decoder: dec}
	locProc, err := NewLocationProcessor(onDevDec, fi.dsOpt.Simplification)
	if err != nil {
		return true, err
	}

	var i int
	for {
		if err := fi.ctx.Err(); err != nil {
			return true, err
		}

		result, err := locProc.NextLocation(ctx)
		if err != nil {
			return true, err
		}
		if result == nil {
			break
		}

		// fast-forward to checkpoint, if set
		if fi.checkpoint.FormatAndroid2025 > 0 && i < fi.checkpoint.FormatAndroid2025 {
			i++
			continue
		}

		item := result.Original.(*semanticSegmentAndroid2025).toItem(result, fi.dsOpt)
		if fi.opt.Timeframe.ContainsItem(item, false) {
			params.Pipeline <- &timeline.Graph{Item: item, Checkpoint: checkpoint{FormatAndroid2025: i}}
		}

		i++
	}

	return true, nil
}
