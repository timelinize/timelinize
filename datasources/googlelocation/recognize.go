package googlelocation

import (
	"encoding/json"
	"path"
	"strings"

	"github.com/timelinize/timelinize/timeline"
)

const filenameFromLegacyTakeout = "Records.json"

// Apparently, filenames exported from Google Maps depend on locale (see issue #111).
// It would be nice if we don't have to open and read from every single JSON file we
// come across in a walk. So for now, we can start making a codex of international
// translations for "Timeline" and other known export filenames. Note: In #111 it
// was reported that the file might be named something different on different devices,
// like "Chronologie.json" on one device, and "Vos trajets.json" on another, despite
// both being set to French. This might be a futile effort, we'll see.
var (
	filenameFromiOSDeviceContains = []string{
		"location-history", // English (confirmed)
	}
	filenameFromAndroidDeviceContains = []string{
		"Timeline",       // English (confirmed)
		"Cronología",     // Spanish
		"Linha do tempo", // Portuguese
		"Chronologie",    // French (confirmed)
		"Vos trajets",    // French again! issue #111 (confirmed)
		"Zeitleiste",     // German
		"Tijdlijn",       // Dutch
		"الجدول الزمني",  // Arabic
		"时间线",            //nolint:gosmopolitan // Chinese
		"ไทม์ไลน์",       // Thai
		"समय",            // Hindi
		"タイムライン",         // Japanese
		"타임라인",           // Korean (confirmed)
		"Хронологія",     // Ukrainian
		"Хронология",     // Russian
	}
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
	// avoid opening all JSON files  (can be slow especially in archives)...
	if !filenameHasJSONExtAndContains(dirEntry.Name(), filenameFromiOSDeviceContains) {
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
	// avoid opening all JSON files (can be slow especially in archives)...
	if !filenameHasJSONExtAndContains(dirEntry.Name(), filenameFromAndroidDeviceContains) {
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

// filenameHasJSONExtAndContains returns true if filename has a ".json" extension
// and contains one of the strings in the list.
func filenameHasJSONExtAndContains(filename string, list []string) bool {
	if path.Ext(filename) != ".json" {
		return false
	}
	for _, s := range list {
		if strings.Contains(filename, s) {
			return true
		}
	}
	return false
}
