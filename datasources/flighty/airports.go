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
)

//go:embed airports.csv
var airportDB []byte

type airportInfo struct {
	Name      string
	Latitude  float64
	Longitude float64
	Timezone  string
}

// Parses the airport database into a map of IATA code to Airport information
// Remember to remove all references to it when you're done, so the Garbage Collector
// can remove it from memory.
func buildAirportDatabase() (map[string]airportInfo, error) {
	db := make(map[string]airportInfo)

	r := csv.NewReader(bytes.NewReader(airportDB))

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
		if len(record) <= len(headers) {
			continue
		}

		lat, latErr := strconv.ParseFloat(record[headerMap[latHeader]], 64)
		lng, lngErr := strconv.ParseFloat(record[headerMap[lngHeader]], 64)
		if latErr != nil || lngErr != nil {
			continue
		}

		db[record[headerMap[iataHeader]]] = airportInfo{
			Name:      record[headerMap[nameHeader]],
			Latitude:  lat,
			Longitude: lng,
			Timezone:  record[headerMap[tzHeader]],
		}
	}

	return db, nil
}

const (
	iataHeader = "code"
	nameHeader = "name"
	latHeader  = "latitude"
	lngHeader  = "longitude"
	tzHeader   = "time_zone"
)
