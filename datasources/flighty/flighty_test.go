package flighty_test

import (
	"context"
	"fmt"
	"io"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/timelinize/timelinize/datasources/flighty"
	"github.com/timelinize/timelinize/timeline"
)

type expectedDetails struct {
	collectionContent string
	collectionMeta    timeline.Metadata
	takeOffLocation   timeline.Location
	takeOffTime       time.Time
	landingLocation   timeline.Location
	landingTime       time.Time
	noteContent       string
	visits            []*timeline.Entity
}

type neededParts struct {
	note        bool
	takeOffItem bool
	landingItem bool
}

func TestFileImport(t *testing.T) {
	// Setup
	fixtures := os.DirFS("testdata/fixtures")
	dirEntry := timeline.DirEntry{
		FS:       fixtures,
		Filename: "FlightyExport-2025-05-07.csv",
	}

	// The buffer for messages is 100 here; significantly more lines than there are in the chat log
	pipeline := make(chan *timeline.Graph, 100)
	params := timeline.ImportParams{
		Pipeline:          pipeline,
		DataSourceOptions: new(flighty.Options),
	}

	// Run the import
	runErr := new(flighty.Importer).FileImport(context.Background(), dirEntry, params)
	if runErr != nil {
		t.Errorf("unable to import file: %v", runErr)
	}
	close(params.Pipeline)

	expected := []expectedDetails{
		{
			collectionContent: "Flight from _London Heathrow Airport_ to _Kempegowda International Airport_",
			collectionMeta: timeline.Metadata{
				"Flight aircraft type":      "Boeing 777",
				"Flight airline":            "BAW",
				"Flight from IATA":          "LHR",
				"Flight from name":          "London Heathrow Airport",
				"Flight Departure Terminal": "5",
				"Flight Departure Gate":     "17",
				"Flight number":             uint(119),
				"Flight to IATA":            "BLR",
				"Flight to name":            "Kempegowda International Airport",
				"Flight Arrival Terminal":   "N",
				"Flight Arrival Gate":       "A12",
			},
			takeOffLocation: newLocation(51.46773895, -0.4587800741571181, 83.0),
			landingLocation: newLocation(13.196697, 77.70758655918868, 2962.0),
			takeOffTime:     parseExampleTime(t, "2011-07-09 15:19:00 +0100 BST"),
			landingTime:     parseExampleTime(t, "2011-07-10 04:56:00 +0530 IST"),
			visits: []*timeline.Entity{
				newPlace(
					"London Heathrow Airport",
					"London Heathrow Airport (Terminal 5, Gate 17)",
					51.46773895, -0.4587800741571181, 83.0,
					"http://www.heathrowairport.com/",
				),
				newPlace(
					"Kempegowda International Airport",
					"Kempegowda International Airport (Terminal N, Gate A12)",
					13.196697, 77.70758655918868, 2962.0,
					"http://www.bengaluruairport.com/home/home.jspx",
				),
			},
		},
		{
			collectionContent: "Flight from _Cancun International Airport_ to _London Gatwick Airport_ (diverted to _London Heathrow Airport_)",
			collectionMeta: timeline.Metadata{
				"Flight aircraft tail number": "JA822J",
				"Flight aircraft type":        "Boeing 787-8",
				"Flight airline":              "TOM",
				"Flight diverted to IATA":     "LHR",
				"Flight diverted to name":     "London Heathrow Airport",
				"Flight Departure Terminal":   "N",
				"Flight from IATA":            "CUN",
				"Flight from name":            "Cancun International Airport",
				"Flight number":               uint(62),
				"Flight reason type":          "PERSONAL",
				"Flight seat cabin":           "FIRST",
				"Flight seat number":          "1A",
				"Flight seat type":            "AISLE",
				"Flight to IATA":              "LGW",
				"Flight to name":              "London Gatwick Airport",
			},
			takeOffLocation: newLocation(21.0407394, -86.8818149087711, 72.0),
			landingLocation: newLocation(51.46773895, -0.4587800741571181, 83.0),
			takeOffTime:     parseExampleTime(t, "2014-04-25 12:19:00 -0500 CDT"),
			landingTime:     parseExampleTime(t, "2014-04-25 13:46:00 +0100 BST"),
			noteContent:     "This is a note",
			visits: []*timeline.Entity{
				newPlace(
					"Cancun International Airport",
					"Cancun International Airport (Terminal N)",
					21.0407394, -86.8818149087711, 72.0,
					"http://www.asur.com.mx/asur/ingles/aeropuertos/cancun/cancun.asp",
				),
				newPlace(
					"London Heathrow Airport",
					"London Heathrow Airport",
					51.46773895, -0.4587800741571181, 83.0,
					"http://www.heathrowairport.com/",
				),
			},
		},
	}

	i := 0
	for message := range pipeline {
		if i >= len(expected) {
			i++
			continue
		}

		ex := expected[i]

		checkCollection(t, ex, message)

		needs := neededParts{
			note:        ex.noteContent != "",
			takeOffItem: true,
			landingItem: true,
		}

		var actualVisits []*timeline.Entity

		for _, edge := range message.Edges {
			switch edge.Relation {
			case timeline.RelAttachment: // Should be a note
				checkAttachment(t, edge, ex, &needs)
			case timeline.RelInCollection: // Should be the take off/landing locations
				checkInCollection(t, edge, ex, &needs)
			case timeline.RelVisit: // Should be a place
				actualVisits = append(actualVisits, edge.To.Entity)
			default:
				t.Fatalf("unknown related item to flight %d (%s)", i, edge.Label)
			}
		}

		if !reflect.DeepEqual(actualVisits, ex.visits) {
			t.Fatalf("flight %d had incorrect visits attached, want %+v items, got: %+v", i, ex.visits, actualVisits)
		}

		if needs.note {
			t.Fatalf("flight %d should have had a note, but didn't", i)
		}
		if needs.takeOffItem {
			t.Fatalf("flight %d should have had a take off location item in its collection, but didn't", i)
		}
		if needs.landingItem {
			t.Fatalf("flight %d should have had a landing location item in its collection, but didn't", i)
		}

		i++
	}

	if i != len(expected) {
		t.Fatalf("received %d messages instead of %d", i, len(expected))
	}
}

func checkCollection(t *testing.T, ex expectedDetails, message *timeline.Graph) {
	collContent, err := itemContentString(message.Item)
	if err == nil {
		if collContent != ex.collectionContent {
			t.Fatalf("incorrect collection content: wanted %s, but got %s", ex.collectionContent, collContent)
		}
	} else {
		t.Fatalf("unable to read content of item: %v", err)
	}

	if !reflect.DeepEqual(message.Item.Metadata, ex.collectionMeta) {
		t.Fatal("collection metadata incorrect")
	}

	if secsDiff := message.Item.Timestamp.Sub(ex.takeOffTime).Abs().Seconds(); secsDiff >= 1 {
		t.Fatalf("flight's timestamp is incorrect (off by %.1fs)", secsDiff)
	}
	if secsDiff := message.Item.Timespan.Sub(ex.landingTime).Abs().Seconds(); secsDiff >= 1 {
		t.Fatalf("flight's timespan is incorrect (off by %.1fs)", secsDiff)
	}
}

func itemContentString(item *timeline.Item) (string, error) {
	df, err := item.Content.Data(context.Background())
	if err != nil {
		return "", fmt.Errorf("unable to retrieve actual data from dataFunc: %w", err)
	}

	data, err := io.ReadAll(df)
	if err != nil {
		return "", fmt.Errorf("unable read data from dataFunc reader: %w", err)
	}

	return string(data), nil
}

func parseExampleTime(t *testing.T, timeStr string) time.Time {
	tt, err := time.Parse("2006-01-02 15:04:05 -0700 MST", timeStr)
	if err == nil {
		return tt
	}
	t.Errorf("unable to parse example time: %v", err)
	return time.Time{}
}

func newLocation(lat, lng, alt float64) timeline.Location {
	return timeline.Location{
		Latitude:  &lat,
		Longitude: &lng,
		Altitude:  &alt,
	}
}

func newPlace(airportName, exactName string, latitude, longitude, altitude float64, url string) *timeline.Entity {
	return &timeline.Entity{
		Type: timeline.EntityPlace,
		Name: airportName,
		Attributes: []timeline.Attribute{
			{
				Name:      "coordinate",
				Latitude:  &latitude,
				Longitude: &longitude,
				Altitude:  &altitude,
				Identity:  true,
				Metadata: timeline.Metadata{
					"URL":        url,
					"Exact name": exactName,
				},
			},
		},
	}
}

func checkAttachment(t *testing.T, edge timeline.Relationship, ex expectedDetails, needs *neededParts) {
	actualNote, err := itemContentString(edge.To.Item)
	if err != nil {
		t.Errorf("unable to parse content of note: %v", err)
	}

	if needs.note {
		if actualNote != ex.noteContent {
			t.Fatalf("flight's note has content '%s', but should have been '%s'", actualNote, ex.noteContent)
		}
		if secsDiff := edge.To.Item.Timestamp.Sub(ex.landingTime).Abs().Seconds(); secsDiff >= 1 {
			t.Fatalf("flight's note should be at the landing time, but isn't")
		}
		needs.note = false
	} else {
		t.Fatalf("flight has a note, but shouldn't have one (%s)", actualNote)
	}
}

func checkInCollection(t *testing.T, edge timeline.Relationship, ex expectedDetails, needs *neededParts) {
	switch edge.Value {
	case "takeoff":
		if !reflect.DeepEqual(edge.To.Item.Location, ex.takeOffLocation) {
			t.Fatalf("flight's take off location is incorrect")
		}

		if secsDiff := edge.To.Item.Timestamp.Sub(ex.takeOffTime).Abs().Seconds(); secsDiff >= 1 {
			t.Fatalf("flight's take off time is incorrect, want %v got %v (%.2fs diff)", ex.takeOffTime, edge.To.Item.Timestamp, secsDiff)
		}
		needs.takeOffItem = false
	case "landing":
		if !reflect.DeepEqual(edge.To.Item.Location, ex.landingLocation) {
			t.Fatalf("flight's landing location is incorrect")
		}

		if secsDiff := edge.To.Item.Timestamp.Sub(ex.landingTime).Abs().Seconds(); secsDiff >= 1 {
			t.Fatalf("flight's landing time is incorrect, want %v got %v (%.2fs diff)", ex.landingTime, edge.To.Item.Timestamp, secsDiff)
		}
		needs.landingItem = false
	default:
		t.Fatalf("unknown edge item '%s' in flight's collection (%+v)", edge.Value, edge.To.Item)
	}
}
