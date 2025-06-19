package flighty

import (
	"fmt"
	"reflect"
)

type fieldLookup func(string) string
type recordParser func([]string) fieldLookup

func checkFields(headers []string) (recordParser, error) {
	headerMap := make(map[string]int, len(headers))
	for headerIdx, header := range headers {
		headerMap[header] = headerIdx
	}

	// Ensure all the necessary columns are present
	v := reflect.ValueOf(flightyFields)
	for i := range v.NumField() {
		header := v.Field(i).String()
		if _, ok := headerMap[header]; !ok {
			return nil, fmt.Errorf("csv is missing the needed '%s' column", header)
		}
	}

	return func(record []string) fieldLookup {
		return func(header string) string {
			recIdx, ok := headerMap[header]
			if !ok {
				return ""
			}

			return record[recIdx]
		}
	}, nil
}

type flightyNeededFields struct {
	ID                 string
	Airline            string
	FlightNumber       string
	FromIATA           string
	DepartureTerminal  string
	DepartureGate      string
	ToIATA             string
	ArrivalTerminal    string
	ArrivalGate        string
	DivertedToIATA     string
	TakeOffScheduled   string
	TakeOffActual      string
	LandingScheduled   string
	LandingActual      string
	AircraftType       string
	AircraftTailNumber string
	AircraftSeatNumber string
	AircraftSeatType   string
	AircraftSeatCabin  string
	Reason             string
	Notes              string
}

// These define the CSV headers for the columns we want to extract.
// If Flighty changes its column names, this is where we'd edit the reference.
var flightyFields = flightyNeededFields{
	ID:                 "Flight Flighty ID",
	Airline:            "Airline",
	FlightNumber:       "Flight",
	FromIATA:           "From",
	DepartureTerminal:  "Dep Terminal",
	DepartureGate:      "Dep Gate",
	ToIATA:             "To",
	ArrivalTerminal:    "Arr Terminal",
	ArrivalGate:        "Arr Gate",
	DivertedToIATA:     "Diverted To",
	TakeOffScheduled:   "Take off (Scheduled)",
	TakeOffActual:      "Take off (Actual)",
	LandingScheduled:   "Landing (Scheduled)",
	LandingActual:      "Landing (Actual)",
	AircraftType:       "Aircraft Type Name",
	AircraftTailNumber: "Tail Number",
	AircraftSeatNumber: "Seat",
	AircraftSeatType:   "Seat Type",
	AircraftSeatCabin:  "Cabin Class",
	Reason:             "Flight Reason",
	Notes:              "Notes",
}
