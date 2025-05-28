package airports_test

import (
	"testing"

	"github.com/timelinize/timelinize/internal/airports"
)

func Test_buildAirportDatabase(t *testing.T) {
	db, err := airports.BuildDB()
	if err != nil {
		t.Fatalf("the airport database must always be buildable: %v", err)
	}

	cases := []struct {
		IATA      string
		Name      string
		Latitude  float64
		Longitude float64
		Altitude  float64
		Timezone  string
	}{
		// First in list
		{"AAA", "Anaa", -17.3506654, -145.51111994065877, 36, "Pacific/Tahiti"},
		// Last in list
		{"ZZV", "Zanesville", 39.933334, -82.01667, 900, "America/New_York"},
		// Accented characters in name
		{"AEH", "Abéché", 13.8465726, 20.849645040165157, 1778, "Africa/Ndjamena"},
	}

	for _, tc := range cases {
		ai, ok := db.LookupIATA(tc.IATA)
		if !ok {
			t.Fatalf("airport with IATA code %s should exist, but doesn't", tc.IATA)
		}

		if ai.Name != tc.Name {
			t.Fatalf("airport %s should have name [%s], but had [%s]", tc.IATA, tc.Name, ai.Name)
		}

		if *ai.Location.Latitude != tc.Latitude {
			t.Fatalf("airport %s should have latitude %f, but had %f", tc.IATA, tc.Latitude, *ai.Location.Latitude)
		}

		if *ai.Location.Longitude != tc.Longitude {
			t.Fatalf("airport %s should have longitude %f, but had %f", tc.IATA, tc.Longitude, *ai.Location.Longitude)
		}

		if *ai.Location.Altitude != tc.Altitude {
			t.Fatalf("airport %s should have altitude %f, but had %f", tc.IATA, tc.Altitude, *ai.Location.Altitude)
		}

		if ai.Timezone != tc.Timezone {
			t.Fatalf("airport %s should have timezone %s, but had %s", tc.IATA, tc.Timezone, ai.Timezone)
		}
	}
}
