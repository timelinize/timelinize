package googlelocation

import (
	"encoding/json"
	"path"
	"strings"

	"github.com/timelinize/timelinize/timeline"
)

const (
	filenameFromLegacyTakeout     = "Records.json"
	filenameFromiOSDeviceContains = "location-history"
	filenameFromAndroidDevice     = "Timeline.json"
)

func (FileImporter) recognizeLegacyTakeoutFormat(dirEntry timeline.DirEntry) timeline.Recognition {
	if dirEntry.IsDir() {
		// see if it's a Takeout-structured location history (a folder with Records.json in it)
		if strings.Contains(dirEntry.Name(), "Location History") && dirEntry.FileExists(filenameFromLegacyTakeout) {
			return timeline.Recognition{Confidence: 1}
		}
	}
	return timeline.Recognition{}
}

func (FileImporter) recognizeOnDevice2024iOSFormat(dirEntry timeline.DirEntry) (timeline.Recognition, error) {
	// avoid opening all JSON files  (can be slow esp. in archives)... just possibly known ones
	if !filenameLooksLikeiOSOnDeviceFile(dirEntry.Name()) {
		return timeline.Recognition{}, nil
	}

	f, err := dirEntry.Open(".")
	if err != nil {
		return timeline.Recognition{}, err
	}
	defer f.Close()

	dec := json.NewDecoder(f)
	if token, err := dec.Token(); err == nil { // read what should be the opening delimiter
		if _, ok := token.(json.Delim); ok {
			var loc onDeviceLocationiOS2024
			if err := dec.Decode(&loc); err == nil {
				if !loc.StartTime.IsZero() && !loc.EndTime.IsZero() {
					return timeline.Recognition{Confidence: 1}, nil
				}
			}
		}
	}

	return timeline.Recognition{}, nil
}

func (FileImporter) recognizeOnDevice2025AndroidFormat(dirEntry timeline.DirEntry) (timeline.Recognition, error) {
	// avoid opening all JSON files  (can be slow esp. in archives)... just possibly known ones
	if dirEntry.Name() != filenameFromAndroidDevice {
		return timeline.Recognition{}, nil
	}

	f, err := dirEntry.Open(".")
	if err != nil {
		return timeline.Recognition{}, err
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
		token, err := dec.Token()
		if err != nil {
			return timeline.Recognition{}, nil
		}
		if !expected(token) {
			return timeline.Recognition{}, nil
		}
	}

	// see if the first entry "fits the bill," at least
	var loc semanticSegmentAndroid2025
	if err := dec.Decode(&loc); err == nil {
		if !loc.StartTime.IsZero() && !loc.EndTime.IsZero() {
			return timeline.Recognition{Confidence: 1}, nil
		}
	}

	return timeline.Recognition{}, nil
}

// I could see people renaming this file to be more descriptive (like I did, heh),
// but I don't want to try to open every JSON file we encounter, if possible...
// so we presume that the filename still has "location-history" in it, but it
// doesn't have to be exactly "location-history.json".
func filenameLooksLikeiOSOnDeviceFile(filename string) bool {
	return strings.Contains(filename, filenameFromiOSDeviceContains) && path.Ext(filename) == ".json"
}
