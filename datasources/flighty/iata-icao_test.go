package flighty

import "testing"

func Test_buildAirportDatabase(t *testing.T) {
	db, err := buildAirportDatabase()
	if err != nil {
		t.Fatalf("the airport database must always be buildable: %v", err)
	}

	cases := []struct {
		IATA      string
		Name      string
		Latitude  float64
		Longitude float64
	}{
		// First in list
		{"AAN", "Al Ain International Airport", 24.2617, 55.6092},
		// Last in list
		{"GWE", "Thornhill Air Base", -19.4364, 29.8619},
		// Negative lat/long
		{"APW", "Faleolo International Airport", -13.83, -172.008},
		// Accented characters in name
		{"PSW", "Municipal José Figueiredo Airport", -20.7322, -46.6618},
	}

	for _, tc := range cases {
		ai, ok := db[tc.IATA]
		if !ok {
			t.Fatalf("airport with IATA code %s should exist, but doesn't", tc.IATA)
		}

		if ai.Name != tc.Name {
			t.Fatalf("airport %s should have name [%s], but had name [%s]", tc.IATA, tc.Name, ai.Name)
		}

		if ai.Latitude != tc.Latitude {
			t.Fatalf("airport %s should have latitude %f, but had name %f", tc.IATA, tc.Latitude, ai.Latitude)
		}

		if ai.Longitude != tc.Longitude {
			t.Fatalf("airport %s should have longitude %f, but had name %f", tc.IATA, tc.Longitude, ai.Longitude)
		}
	}
}
