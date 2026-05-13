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

package googlelocation

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/timelinize/timelinize/timeline"
)

type onDeviceLocationiOS2024 struct {
	EndTime   time.Time `json:"endTime"` // e.g. 2024-06-21T19:51:13.014-06:00
	StartTime time.Time `json:"startTime"`
	Activity  struct {
		Start          geoString           `json:"start"`
		End            geoString           `json:"end"`
		TopCandidate   topCandidateiOS2024 `json:"topCandidate"`
		DistanceMeters string              `json:"distanceMeters"`
	} `json:"activity,omitempty"`
	Visit struct {
		HierarchyLevel string              `json:"hierarchyLevel"`
		TopCandidate   topCandidateiOS2024 `json:"topCandidate"`
		Probability    string              `json:"probability"`
	} `json:"visit,omitempty"`
	TimelinePath []struct {
		Point                              geoString `json:"point"`
		DurationMinutesOffsetFromStartTime string    `json:"durationMinutesOffsetFromStartTime"`
	} `json:"timelinePath"`
}

func (l *onDeviceLocationiOS2024) toItem(result *Location, opt *Options) *timeline.Item {
	entity := timeline.Entity{ID: opt.OwnerEntityID}

	if opt.Device != "" {
		attr := timeline.Attribute{
			Name:  "google_location_device",
			Value: opt.Device,
		}
		entity.Attributes = []timeline.Attribute{attr}
	}

	meta := make(timeline.Metadata)

	switch {
	case l.Visit.TopCandidate.PlaceLocation != "":
		meta["Hierarchy level"] = l.Visit.HierarchyLevel
		meta["Visit probability"] = l.Visit.Probability
		meta["Visit probability"] = l.Visit.TopCandidate.Probability
		meta["Visit semantic type"] = l.Visit.TopCandidate.SemanticType
		meta["Visited place ID"] = l.Visit.TopCandidate.PlaceID
	case l.Activity.Start != "":
		meta["Distance"] = l.Activity.DistanceMeters
		if l.Activity.TopCandidate.Type != "unknown" || l.Activity.TopCandidate.Probability != "0.000000" {
			meta["Activity type"] = l.Activity.TopCandidate.Type
			meta["Activity probability"] = l.Activity.TopCandidate.Probability
		}
	}

	meta.Merge(result.Metadata, timeline.MetaMergeReplace)

	return &timeline.Item{
		Classification: timeline.ClassLocation,
		Timestamp:      result.Timestamp,
		Timespan:       result.Timespan,
		Location:       result.Location(),
		Owner:          entity,
		Metadata:       meta,
	}
}

type topCandidateiOS2024 struct {
	Type          string    `json:"type"`
	Probability   string    `json:"probability"`
	SemanticType  string    `json:"semanticType"`
	PlaceID       string    `json:"placeID"`
	PlaceLocation geoString `json:"placeLocation"`
}

type geoString string // EXAMPLE: "geo:30.123456,-105.987654"

func (g geoString) parse() (timeline.Location, error) {
	const prefix = "geo:"
	if !strings.HasPrefix(string(g), prefix) {
		return timeline.Location{}, errors.New("not a valid geo string: missing prefix")
	}
	latStr, lonStr, ok := strings.Cut(string(g[len(prefix):]), ",")
	if !ok {
		return timeline.Location{}, errors.New("not a valid geo string: missing comma separator")
	}
	lat, err := strconv.ParseFloat(latStr, 64)
	if err != nil {
		return timeline.Location{}, fmt.Errorf("not a valid geo string: bad latitude: %s: %w", latStr, err)
	}
	lon, err := strconv.ParseFloat(lonStr, 64)
	if err != nil {
		return timeline.Location{}, fmt.Errorf("not a valid geo string: bad longitude: %s: %w", lonStr, err)
	}
	return timeline.Location{
		Latitude:  &lat,
		Longitude: &lon,
	}, nil
}

// semanticSegmentAndroid2025 contains the primary majority of on-device location history from Android devices starting in about 2025.
type semanticSegmentAndroid2025 struct {
	StartTime    time.Time `json:"startTime"`
	EndTime      time.Time `json:"endTime"`
	TimelinePath []struct {
		Point degreeString `json:"point"`
		Time  time.Time    `json:"time"`
	} `json:"timelinePath,omitempty"`
	StartTimeTimezoneUTCOffsetMinutes int `json:"startTimeTimezoneUtcOffsetMinutes,omitempty"`
	EndTimeTimezoneUTCOffsetMinutes   int `json:"endTimeTimezoneUtcOffsetMinutes,omitempty"`
	Visit                             struct {
		HierarchyLevel int                     `json:"hierarchyLevel"`
		Probability    float64                 `json:"probability"`
		TopCandidate   topCandidateAndroid2025 `json:"topCandidate"`
	} `json:"visit,omitempty"`
	Activity struct {
		Start struct {
			LatLng degreeString `json:"latLng"`
		} `json:"start"`
		End struct {
			LatLng degreeString `json:"latLng"`
		} `json:"end"`
		DistanceMeters float64                 `json:"distanceMeters"`
		Probability    float64                 `json:"probability"`
		TopCandidate   topCandidateAndroid2025 `json:"topCandidate"`
		Parking        struct {
			Location struct {
				LatLng degreeString `json:"latLng"`
			} `json:"location"`
			StartTime time.Time `json:"startTime"`
		} `json:"parking"`
	} `json:"activity,omitempty"`
	TimelineMemory struct {
		Trip struct {
			DistanceFromOriginKms int `json:"distanceFromOriginKms"`
			Destinations          []struct {
				Identifier struct {
					PlaceID string `json:"placeId"`
				} `json:"identifier"`
			} `json:"destinations"`
		} `json:"trip"`
	} `json:"timelineMemory,omitempty"`
}

// TODO: Use this?
//
//nolint:unused
type rawSignalAndroid2025 struct {
	Position struct {
		LatLng               degreeString `json:"LatLng"`
		AccuracyMeters       int          `json:"accuracyMeters"`
		AltitudeMeters       float64      `json:"altitudeMeters"`
		Source               string       `json:"source"`
		Timestamp            string       `json:"timestamp"`
		SpeedMetersPerSecond float64      `json:"speedMetersPerSecond"`
	} `json:"position,omitempty"`
	ActivityRecord struct {
		ProbableActivities []struct {
			Type       string  `json:"type"`
			Confidence float64 `json:"confidence"`
		} `json:"probableActivities"`
		Timestamp time.Time `json:"timestamp"`
	} `json:"activityRecord,omitempty"`
	WifiScan struct {
		DeliveryTime   time.Time `json:"deliveryTime"`
		DevicesRecords []struct {
			Mac     int64 `json:"mac"`
			RawRSSI int   `json:"rawRssi"`
		} `json:"devicesRecords"`
	} `json:"wifiScan,omitempty"`
}

// TODO: Use this?
//
//nolint:unused
type userLocationProfileAndroid2025 struct {
	FrequentPlaces []struct {
		PlaceID       string `json:"placeId"`
		PlaceLocation string `json:"placeLocation"`
		Label         string `json:"label"`
	} `json:"frequentPlaces"`
}

type topCandidateAndroid2025 struct {
	Type          string  `json:"type"`
	PlaceID       string  `json:"placeId"`
	SemanticType  string  `json:"semanticType"`
	Probability   float64 `json:"probability"`
	PlaceLocation struct {
		LatLng degreeString `json:"latLng"`
	} `json:"placeLocation"`
}

func (l *semanticSegmentAndroid2025) toItem(result *Location, opt *Options) *timeline.Item {
	entity := timeline.Entity{ID: opt.OwnerEntityID}

	if opt.Device != "" {
		attr := timeline.Attribute{
			Name:  "google_location_device",
			Value: opt.Device,
		}
		entity.Attributes = []timeline.Attribute{attr}
	}

	meta := make(timeline.Metadata)

	switch {
	case l.Visit.TopCandidate.PlaceLocation.LatLng != "":
		meta["Hierarchy level"] = l.Visit.HierarchyLevel
		meta["Visit probability"] = l.Visit.Probability
		meta["Visit probability"] = l.Visit.TopCandidate.Probability
		meta["Visit semantic type"] = l.Visit.TopCandidate.SemanticType
		meta["Visited place ID"] = l.Visit.TopCandidate.PlaceID

	case l.Activity.Start.LatLng != "":
		meta["Distance"] = l.Activity.DistanceMeters
		if l.Activity.TopCandidate.Type != "unknown" || l.Activity.TopCandidate.Probability != 0 {
			meta["Activity type"] = l.Activity.TopCandidate.Type
			meta["Activity probability"] = l.Activity.TopCandidate.Probability
		}
	}

	// if the result wasn't a cluster (which has a timespan), then we may know a timespan of
	// this point if it came with an EndTime.
	if result.Timespan.IsZero() && !l.EndTime.IsZero() {
		result.Timespan = l.EndTime
	}

	meta.Merge(result.Metadata, timeline.MetaMergeReplace)

	return &timeline.Item{
		Classification: timeline.ClassLocation,
		Timestamp:      result.Timestamp,
		Timespan:       result.Timespan,
		Location:       result.Location(),
		Owner:          entity,
		Metadata:       meta,
	}
}

type degreeString string // EXAMPLE: "31.1234567°, -73.1234567°"

func (d degreeString) parse() (timeline.Location, error) {
	str := strings.ReplaceAll(string(d), "°", "") // remove degree symbols
	latStr, lonStr, ok := strings.Cut(str, ",")   // split lat and lon (could still have spaces for now)
	if !ok {
		return timeline.Location{}, errors.New("not a valid degree string: missing comma separator")
	}
	lat, err := strconv.ParseFloat(strings.TrimSpace(latStr), 64) // parse longitude (w/o spaces)
	if err != nil {
		return timeline.Location{}, fmt.Errorf("not a valid degree string: bad latitude: %s: %w", latStr, err)
	}
	lon, err := strconv.ParseFloat(strings.TrimSpace(lonStr), 64) // parse longitude (w/o spaces)
	if err != nil {
		return timeline.Location{}, fmt.Errorf("not a valid degree string: bad longitude: %s: %w", lonStr, err)
	}
	return timeline.Location{
		Latitude:  &lat,
		Longitude: &lon,
	}, nil
}

///////////////////////////////

// Awesome unofficial documentation: https://locationhistoryformat.com/

// FINALLY! Official docs!
// https://developers.google.com/data-portability/schema-reference/location_history (Update: Since disappeared...)
// TODO: Add more fields from the official docs to the item metadata
type location struct {
	Accuracy int `json:"accuracy"` // meters; higher values are less accurate (should probably be called "error" instead)
	Activity []struct {
		Activity []struct {
			Confidence int    `json:"confidence"`
			Type       string `json:"type"`
		} `json:"activity"`
		Timestamp time.Time `json:"timestamp"`
	} `json:"activity"`
	Altitude         int   `json:"altitude"`    // meters
	DeviceTag        int64 `json:"deviceTag"`   // may correspond with a device in Settings.json
	Heading          int   `json:"heading"`     // degrees
	LatitudeE7       int64 `json:"latitudeE7"`  // latitude times 1e7
	LongitudeE7      int64 `json:"longitudeE7"` // longitude times 1e7
	LocationMetadata []struct {
		TimestampMs string `json:"timestampMs"`
		WifiScan    struct {
			AccessPoints []struct {
				MAC      string `json:"mac"`
				Strength int    `json:"strength"`
			} `json:"accessPoints"`
		} `json:"wifiScan"`
	} `json:"locationMetadata"`
	Platform         string    `json:"platform"`
	PlatformType     string    `json:"platformType"` // ANDROID, IOS, or UNKNOWN
	Source           string    `json:"source"`       // WIFI, CELL, GPS, or UNKNOWN (may also be lowercase sometimes)
	Timestamp        time.Time `json:"timestamp"`    // old exports used to call this timestampMs, in milliseconds
	Velocity         int       `json:"velocity"`     // meters/second
	VerticalAccuracy int       `json:"verticalAccuracy"`

	// Fields added in early 2024
	DeviceTimestamp time.Time `json:"deviceTimestamp"` // "Timestamp at which the device uploaded the location batch containing this record."
	ServerTimestamp time.Time `json:"serverTimestamp"` // "Timestamp at which the server received and created this record."
	BatteryCharging bool      `json:"batteryCharging"`
	FormFactor      string    `json:"formFactor"` // PHONE, TABLET, ...

	// added after processing (but before becoming an item)
	timespan time.Time
	meta     timeline.Metadata
}

func (l location) toItem(opt *Options) *timeline.Item {
	entity := timeline.Entity{ID: opt.OwnerEntityID}

	if l.DeviceTag != 0 {
		attr := timeline.Attribute{
			Name:     "google_location_device",
			Value:    strconv.FormatInt(l.DeviceTag, 10),
			Identity: true, // I think the DeviceTag is basically a unique ID, but I am not 100% sure
		}
		if device, ok := opt.devices[l.DeviceTag]; ok {
			attr.AltValue = device.DevicePrettyName
			attr.Metadata = timeline.Metadata{
				"Creation time":        device.DeviceCreationTime,
				"Platform":             device.PlatformType,
				"Android OS API level": device.AndroidOSLevel,
				"Manufacturer":         device.DeviceSpec.Manufacturer,
				"Brand":                device.DeviceSpec.Brand,
				"Product":              device.DeviceSpec.Product,
				"Device":               device.DeviceSpec.Device,
				"Model":                device.DeviceSpec.Model,
				"Low RAM":              device.DeviceSpec.IsLowRAM,
			}
		}
		entity.Attributes = []timeline.Attribute{attr}
	}

	return &timeline.Item{
		Timestamp:      l.Timestamp,
		Timespan:       l.timespan,
		Owner:          entity,
		Classification: timeline.ClassLocation,
		Location:       l.location(),
		Metadata:       l.metadata(),
	}
}

func (l location) location() timeline.Location {
	lat := float64(l.LatitudeE7) / placesMult
	lon := float64(l.LongitudeE7) / placesMult
	alt := float64(l.Altitude)
	unc := metersToApproxDegrees(float64(l.Accuracy))

	var loc timeline.Location
	if lat != 0 {
		loc.Latitude = &lat
	}
	if lon != 0 {
		loc.Longitude = &lon
	}
	if alt != 0 {
		loc.Altitude = &alt
	}
	if unc != 0 {
		loc.CoordinateUncertainty = &unc
	}
	return loc
}

// metersToApproxDegrees converts the number of meters to an approximate number
// of degrees.
func metersToApproxDegrees(m float64) float64 {
	// Ok, hear me out.
	//
	// We need to convert a vector offset in meters to the approximate
	// degrees lat/lon -- both you say? yes, approximately; we're talking
	// usually small amounts anyway and not right at the poles.
	//
	// A reverse haversine sounds complicated. But APPARENTLY, "the French
	// originally defined the meter so that 10^7 (1e7) meters would be the
	// distance along the Paris meridian from the equator to the north pole.
	// Thus, 10^7 / 90 = 111,111.1 meters equals one degree of latitude to
	// within the capabilities of French surveyors two centuries ago."
	// https://gis.stackexchange.com/questions/2951/algorithm-for-offsetting-a-latitude-longitude-by-some-amount-of-meters#comment3018_2964
	//
	// And apparently the error of this approximatation stays below 10m
	// until you get beyond 89.6 degrees latitude, which is well within
	// our bounds; and once you get that far you're basically just "at
	// the poles" for our purposes anyway.
	//
	// More context: https://gis.stackexchange.com/a/2964/5599
	const oneDegOfLatInParisIn1789 = 1e7 / 90
	return m / oneDegOfLatInParisIn1789
}

func (l location) metadata() timeline.Metadata {
	meta := l.meta
	if meta == nil {
		meta = make(timeline.Metadata)
	}

	meta.Merge(timeline.Metadata{
		"Velocity":          l.Velocity,
		"Heading":           l.Heading,
		"Vertical accuracy": l.VerticalAccuracy,
		"Device tag":        l.DeviceTag,
		"Platform":          l.Platform,
		"Platform type":     l.PlatformType,
		"Source":            l.Source,
		"Server timestamp":  l.ServerTimestamp,
		"Device timestamp":  l.DeviceTimestamp,
		"Battery charging":  l.BatteryCharging,
		"Form factor":       l.FormFactor,
	}, timeline.MetaMergeSkip)

	// activities are often duplicated, so eliminate duplicates first... we lose the order, but oh well
	actsMap := make(map[string]int)
	for _, act1 := range l.Activity {
		for _, act2 := range act1.Activity {
			if conf, ok := actsMap[act2.Type]; !ok || act2.Confidence > conf {
				actsMap[act2.Type] = act2.Confidence
			}
		}
	}
	acts := make([]string, 0, len(actsMap))
	for activityType, activityConfidence := range actsMap {
		acts = append(acts, fmt.Sprintf("%s (%d%%)", activityType, activityConfidence))
	}
	if len(acts) > 0 {
		meta["Activities"] = strings.Join(acts, ", ")
	}

	var wifis []string
	for _, lm := range l.LocationMetadata {
		for _, ap := range lm.WifiScan.AccessPoints {
			wifis = append(wifis, fmt.Sprintf("[%s %d]", ap.MAC, ap.Strength))
		}
	}
	if len(wifis) > 0 {
		meta["WiFi APs"] = strings.Join(wifis, " ")
	}

	// TODO: if combining more than 1, maybe add to the metadata how many we combined here

	return meta
}

// TODO: are we going to use these functions?

// func (l location) primaryMovement() string {
// 	if len(l.Activity) == 0 {
// 		return ""
// 	}

// 	counts := make(map[string]int)
// 	confidences := make(map[string]int)
// 	for _, a := range l.Activity {
// 		for _, aa := range a.Activity {
// 			counts[aa.Type]++
// 			confidences[aa.Type] += aa.Confidence
// 		}
// 	}

// 	// turn confidence into average confidence,
// 	// (ensure all activities are represented),
// 	// and keep activities with high enough score
// 	var top []activity
// 	var hasOnFoot, hasWalking, hasRunning bool
// 	for _, a := range movementActivities {
// 		count := counts[a]
// 		if count == 0 {
// 			count = 1 // for the purposes of division
// 		}
// 		avg := confidences[a] / len(l.Activity)
// 		avgSeen := confidences[a] / count
// 		if avgSeen > 50 {
// 			switch a {
// 			case "ON_FOOT":
// 				hasOnFoot = true
// 			case "WALKING":
// 				hasWalking = true
// 			case "RUNNING":
// 				hasRunning = true
// 			}
// 			top = append(top, activity{Type: a, Confidence: avg})
// 		}
// 	}
// 	sort.Slice(top, func(i, j int) bool {
// 		return top[i].Confidence > top[j].Confidence
// 	})

// 	// consolidate ON_FOOT, WALKING, and RUNNING if more than one is present
// 	if hasOnFoot && (hasWalking || hasRunning) {
// 		for i := 0; i < len(top); i++ {
// 			if hasWalking && hasRunning &&
// 				(top[i].Type == "WALKING" || top[i].Type == "RUNNING") {
// 				// if both WALKING and RUNNING, prefer more general ON_FOOT
// 				top = append(top[:i], top[i+1:]...)
// 			} else if top[i].Type == "ON_FOOT" {
// 				// if only one of WALKING or RUNNING, prefer that over ON_FOOT
// 				top = append(top[:i], top[i+1:]...)
// 			}
// 		}
// 	}

// 	if len(top) > 0 {
// 		return top[0].Type
// 	}
// 	return ""
// }

// func (l location) hasActivity(act string) bool {
// 	for _, a := range l.Activity {
// 		for _, aa := range a.Activity {
// 			if aa.Type == act && aa.Confidence > 50 {
// 				return true
// 			}
// 		}
// 	}
// 	return false
// }

// type activities struct {
// 	TimestampMs string     `json:"timestampMs"`
// 	Activity    []activity `json:"activity"`
// }

// type activity struct {
// 	Type       string `json:"type"`
// 	Confidence int    `json:"confidence"`
// }

// // movementActivities is the list of activities we care about
// // for drawing relationships between two locations. For example,
// // we don't care about TILTING (sudden accelerometer adjustment,
// // like phone set down or person standing up), UNKNOWN, or STILL
// // (where there is no apparent movement detected).
// //
// // https://developers.google.com/android/reference/com/google/android/gms/location/DetectedActivity
// var movementActivities = []string{
// 	"WALKING",
// 	"RUNNING",
// 	"IN_VEHICLE",
// 	"ON_FOOT",
// 	"ON_BICYCLE",
// }
