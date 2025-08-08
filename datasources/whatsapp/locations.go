package whatsapp

import (
	"fmt"
	"strings"

	"github.com/timelinize/timelinize/timeline"
)

func extractLocation(content []string) (string, timeline.Metadata, bool) {
	if len(content) <= metadataFieldNumber {
		return "", nil, false
	}

	trimmedContent := strings.TrimSpace(content[metadataFieldNumber])
	meta := make(timeline.Metadata)
	if idx := strings.Index(trimmedContent, foursquarePrefix); idx != -1 {
		meta["Pin foursquare id"] = trimmedContent[idx+len(foursquarePrefix):]
	} else if idx := strings.Index(trimmedContent, googleMapsPrefix); idx != -1 {
		var lat float64
		var lng float64
		if _, err := fmt.Sscanf(trimmedContent[idx+len(googleMapsPrefix):], "%f,%f", &lat, &lng); err != nil {
			return "", nil, false
		}

		meta["Pin latitude"] = lat
		meta["Pin longitude"] = lng
	} else {
		return "", nil, false
	}

	return trimmedContent, meta, true
}

const (
	foursquarePrefix = "https://foursquare.com/v/"
	googleMapsPrefix = "https://maps.google.com/?q="
)
