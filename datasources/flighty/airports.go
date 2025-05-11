// This file handles pulling information from the CC 4.0 BY-SA licenced airport location database available at:
// https://github.com/lxndrblz/Airports/tree/main
package flighty

import (
	"bytes"
	_ "embed"
	"encoding/csv"
	"fmt"
	"io"
	"strconv"

	"github.com/timelinize/timelinize/timeline"
)

//go:embed airports.csv
var airportData []byte

type AirportInfo struct {
	IATA     string
	Name     string
	Location timeline.Location
	Timezone string
}

// Parses the airport database into a map of IATA code to Airport information
// Remember to remove all references to it when you're done, so the Garbage Collector
// can remove it from memory.
func buildAirportDatabase() (map[string]AirportInfo, error) {
	db := make(map[string]AirportInfo)

	r := csv.NewReader(bytes.NewReader(airportData))

	headers, err := r.Read()
	if err != nil {
		return db, fmt.Errorf("unable to parse airport db CSV headers: %w", err)
	}

	headerMap := make(map[string]int)
	for i, h := range headers {
		headerMap[h] = i
	}

	for {
		record, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return db, fmt.Errorf("unable to parse airport db CSV: %w", err)
		}
		if len(record) < len(headers) {
			continue
		}

		lat, latErr := strconv.ParseFloat(record[headerMap[latHeader]], 64)
		lng, lngErr := strconv.ParseFloat(record[headerMap[lngHeader]], 64)
		if latErr != nil || lngErr != nil {
			continue
		}
		loc := timeline.Location{Latitude: &lat, Longitude: &lng}
		if alt, err := strconv.ParseFloat(record[headerMap[altHeader]], 64); err == nil {
			loc.Altitude = &alt
		}

		iata := record[headerMap[iataHeader]]
		db[iata] = AirportInfo{
			IATA:     iata,
			Name:     record[headerMap[nameHeader]],
			Location: loc,
			Timezone: record[headerMap[tzHeader]],
		}
	}

	return db, nil
}

const (
	iataHeader = "code"
	nameHeader = "name"
	latHeader  = "latitude"
	lngHeader  = "longitude"
	altHeader  = "elevation"
	tzHeader   = "time_zone"
)
