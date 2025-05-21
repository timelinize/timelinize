package flighty

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"time"

	"github.com/timelinize/timelinize/internal/airports"
	"github.com/timelinize/timelinize/timeline"
	"go.uber.org/zap"
)

func init() {
	err := timeline.RegisterDataSource(timeline.DataSource{
		Name:            "flighty",
		Title:           "Flighty",
		Icon:            "flighty.png",
		Description:     "A flight log from the Flighty app",
		NewOptions:      func() any { return new(Options) },
		NewFileImporter: func() timeline.FileImporter { return new(Importer) },
	})
	if err != nil {
		timeline.Log.Fatal("registering data source", zap.Error(err))
	}
}

// Options configures the data source.
type Options struct {
	// The ID of the owner entity. REQUIRED if entity is to be related in DB.
	OwnerEntityID uint64 `json:"owner_entity_id"`
}

// Importer can import the data from a zip file or folder.
type Importer struct{}

var exportFilenameFormatRegex = regexp.MustCompile(`(?i)^FlightyExport-\d{4}-\d{2}-\d{2}.csv$`)

// Recognize returns whether the input is supported.
func (Importer) Recognize(_ context.Context, dirEntry timeline.DirEntry, _ timeline.RecognizeParams) (timeline.Recognition, error) {
	if !exportFilenameFormatRegex.MatchString(dirEntry.Name()) {
		return timeline.Recognition{}, nil
	}

	// TODO: Perhaps read the first line & check the headings?

	return timeline.Recognition{Confidence: 1}, nil
}

// FileImport imports data from the file or folder.
func (i *Importer) FileImport(_ context.Context, dirEntry timeline.DirEntry, params timeline.ImportParams) error {
	dsOpt := params.DataSourceOptions.(*Options)

	airportDB, err := airports.BuildDB()
	if err != nil {
		return fmt.Errorf("unable to load airport database: %w", err)
	}

	csvF, err := dirEntry.FS.Open(dirEntry.Filename)
	if err != nil {
		return fmt.Errorf("unable to open Flighty export: %w", err)
	}

	r := csv.NewReader(csvF)

	headers, err := r.Read()
	if err != nil {
		return fmt.Errorf("unable to read CSV headers: %w", err)
	}

	rowAccessor, err := checkFields(headers)
	if err != nil {
		return err
	}

	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("unable to parse flighty CSV: %w", err)
		}
		if len(rec) < len(headers) {
			continue
		}

		f, err := parseFlightDetails(rowAccessor(rec), airportDB)
		if err != nil {
			// TODO: How to flag that an item import failed?
			continue
		}

		owner := timeline.Entity{ID: dsOpt.OwnerEntityID}

		journey := &timeline.Graph{}

		meta := timeline.Metadata{
			"Flight from IATA": f.From.IATA,
			"Flight from name": f.From.Name,
			"Flight to IATA":   f.To.IATA,
			"Flight to name":   f.To.Name,

			"Flight airline": f.Airline,
			"Flight number":  f.Number,

			"Flight aircraft type":        f.Aircraft.Type,
			"Flight aircraft tail number": f.Aircraft.TailNumber,

			"Flight seat number": f.Aircraft.SeatNumber,
			"Flight seat type":   string(f.Aircraft.SeatType),
			"Flight seat cabin":  string(f.Aircraft.SeatCabin),
			"Flight reason type": string(f.ReasonType),
		}
		if f.WasDiverted() {
			meta["Flight diverted to IATA"] = f.DivertedTo.IATA
			meta["Flight diverted to name"] = f.DivertedTo.Name
		}

		meta.Clean()

		var collKey timeline.ItemRetrieval
		collKey.SetKey(f.ID)

		coll := &timeline.Item{
			ID:             f.ID,
			Classification: timeline.ClassCollection,
			Content: timeline.ItemData{
				Data:      timeline.StringData(f.Description()),
				MediaType: "text/markdown",
			},
			Timestamp: f.TakeOffTime,
			Timespan:  f.LandingTime,
			Owner:     owner,
			Metadata:  meta,
			Retrieval: collKey,
		}
		journey.Item = coll

		if f.Notes != "" {
			var noteKey timeline.ItemRetrieval
			noteKey.SetKey(f.ID + "-notes")

			note := &timeline.Item{
				Classification: timeline.ClassMessage,
				Timestamp:      f.LandingTime,
				Owner:          owner,
				Content: timeline.ItemData{
					Data: timeline.StringData(f.Notes),
				},
				Retrieval: noteKey,
			}
			journey.ToItem(timeline.RelAttachment, note)
		}

		var takeoffKey timeline.ItemRetrieval
		takeoffKey.SetKey(f.ID + "-takeoff")

		takeoff := &timeline.Item{
			Classification: timeline.ClassLocation,
			Location:       f.From.Location,
			Timestamp:      f.TakeOffTime,
			Owner:          owner,
			Retrieval:      takeoffKey,
		}
		journey.ToItemWithValue(timeline.RelInCollection, takeoff, "takeoff")

		var landingKey timeline.ItemRetrieval
		landingKey.SetKey(f.ID + "-landing")

		landing := &timeline.Item{
			Classification: timeline.ClassLocation,
			Timestamp:      f.LandingTime,
			Owner:          owner,
			Retrieval:      landingKey,
		}
		if f.WasDiverted() {
			landing.Location = f.DivertedTo.Location
		} else {
			landing.Location = f.To.Location
		}
		journey.ToItemWithValue(timeline.RelInCollection, landing, "landing")

		params.Pipeline <- journey
	}

	// Make sure the airport DB can be garbage collected
	airportDB = nil
	return nil
}

func parseFlightDetails(row fieldLookup, airportDB map[string]airports.Info) (flight, error) {
	flightNumber, _ := strconv.ParseUint(row(flightyFields.FlightNumber), 10, 0)

	details := flight{
		ID: row(flightyFields.ID),

		Airline: row(flightyFields.Airline),
		Number:  uint(flightNumber),

		Aircraft: Aircraft{
			Type:       row(flightyFields.AircraftType),
			TailNumber: row(flightyFields.AircraftTailNumber),
			SeatNumber: row(flightyFields.AircraftSeatNumber),
			SeatType:   SeatType(row(flightyFields.AircraftSeatType)),
			SeatCabin:  CabinType(row(flightyFields.AircraftSeatCabin)),
		},

		ReasonType: ReasonType(row(flightyFields.Reason)),
		Notes:      row(flightyFields.Notes),
	}

	// Locations of airports
	if airport, err := extractAirport(airportDB, row, flightyFields.FromIATA); err != nil {
		return details, err
	} else {
		details.From = *airport
	}

	if airport, err := extractAirport(airportDB, row, flightyFields.ToIATA); err != nil {
		return details, err
	} else {
		details.To = *airport
	}

	if airport, err := extractAirport(airportDB, row, flightyFields.DivertedToIATA); err != nil {
		return details, err
	} else {
		details.DivertedTo = airport
	}

	// Times

	takeOffTime, err := parseAirportTime(row(flightyFields.TakeOffActual), row(flightyFields.TakeOffScheduled), details.From)
	if err != nil {
		return details, err
	}
	details.TakeOffTime = takeOffTime

	landingTime, err := parseAirportTime(row(flightyFields.LandingActual), row(flightyFields.LandingScheduled), details.Destination())
	if err != nil {
		return details, err
	}
	details.LandingTime = landingTime

	return details, nil
}

func extractAirport(airportDB airports.DB, row fieldLookup, field string) (*airports.Info, error) {
	iata := row(field)
	if iata == "" {
		return nil, nil
	}

	airport, ok := airportDB[iata]
	if !ok {
		return nil, fmt.Errorf("unknown airport IATA code in '%s' field: %s", field, iata)
	}
	return &airport, nil
}

func parseAirportTime(actual, scheduled string, airport airports.Info) (time.Time, error) {
	tz, err := time.LoadLocation(airport.Timezone)
	if err != nil {
		return time.Time{}, err
	}

	if t, err := time.ParseInLocation("2006-01-02T15:04", actual, tz); err == nil {
		return t, nil
	}

	if t, err := time.ParseInLocation("2006-01-02T15:04", scheduled, tz); err == nil {
		return t, nil
	}

	return time.Time{}, fmt.Errorf("unable to extract an actual or scheduled time")
}
