package flighty

import (
	"context"
	"encoding/csv"
	"errors"
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
func (i *Importer) FileImport(ctx context.Context, dirEntry timeline.DirEntry, params timeline.ImportParams) error {
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
		if err := ctx.Err(); err != nil {
			return err
		}

		rec, err := r.Read()
		if errors.Is(err, io.EOF) {
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

			"Flight Departure Terminal": f.DepartureTerminal,
			"Flight Departure Gate":     f.DepartureGate,
			"Flight Arrival Terminal":   f.ArrivalTerminal,
			"Flight Arrival Gate":       f.ArrivalGate,

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
		}
		journey.Item = coll

		if f.Notes != "" {
			note := &timeline.Item{
				Classification: timeline.ClassMessage,
				Timestamp:      f.LandingTime,
				Owner:          owner,
				Content: timeline.ItemData{
					Data: timeline.StringData(f.Notes),
				},
			}
			journey.ToItem(timeline.RelAttachment, note)
		}

		takeOff := &timeline.Item{
			Classification: timeline.ClassLocation,
			Location:       f.From.Location,
			Timestamp:      f.TakeOffTime,
			Owner:          owner,
		}
		visitTakeOff := &timeline.Entity{
			Type: timeline.EntityPlace,
			Name: f.From.Name,
			Attributes: []timeline.Attribute{
				{
					Name:      "coordinate",
					Latitude:  f.From.Location.Latitude,
					Longitude: f.From.Location.Longitude,
					Altitude:  f.From.Location.Altitude,
					Identity:  true,
					Metadata: timeline.Metadata{
						"URL":        f.From.URL,
						"Exact name": exactPlaceName(f.From.Name, f.DepartureTerminal, f.DepartureGate),
					},
				},
			},
		}
		visitTakeOff.Attributes[0].Metadata.Clean()
		journey.ToItemWithValue(timeline.RelInCollection, takeOff, "takeoff")
		journey.ToEntity(timeline.RelVisit, visitTakeOff)

		var destination airports.Info
		if f.WasDiverted() {
			destination = *f.DivertedTo
		} else {
			destination = f.To
		}

		landing := &timeline.Item{
			Classification: timeline.ClassLocation,
			Location:       destination.Location,
			Timestamp:      f.LandingTime,
			Owner:          owner,
		}
		visitLanding := &timeline.Entity{
			Type: timeline.EntityPlace,
			Name: destination.Name,
			Attributes: []timeline.Attribute{
				{
					Name:      "coordinate",
					Latitude:  destination.Location.Latitude,
					Longitude: destination.Location.Longitude,
					Altitude:  destination.Location.Altitude,
					Identity:  true,
					Metadata: timeline.Metadata{
						"URL":        destination.URL,
						"Exact name": exactPlaceName(destination.Name, f.ArrivalTerminal, f.ArrivalGate),
					},
				},
			},
		}
		visitLanding.Attributes[0].Metadata.Clean()
		journey.ToItemWithValue(timeline.RelInCollection, landing, "landing")
		journey.ToEntity(timeline.RelVisit, visitLanding)

		params.Pipeline <- journey
	}

	return nil
}

func parseFlightDetails(row fieldLookup, airportDB map[string]airports.Info) (flight, error) {
	flightNumber, _ := strconv.ParseUint(row(flightyFields.FlightNumber), 10, 0)

	details := flight{
		ID: row(flightyFields.ID),

		Airline: row(flightyFields.Airline),
		Number:  uint(flightNumber),

		DepartureTerminal: row(flightyFields.DepartureTerminal),
		DepartureGate:     row(flightyFields.DepartureGate),
		ArrivalTerminal:   row(flightyFields.ArrivalTerminal),
		ArrivalGate:       row(flightyFields.ArrivalGate),

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
	airport, err := extractAirport(airportDB, row, flightyFields.FromIATA)
	if err != nil {
		return details, err
	}
	details.From = *airport

	airport, err = extractAirport(airportDB, row, flightyFields.ToIATA)
	if err != nil {
		return details, err
	}
	details.To = *airport

	airport, err = extractAirport(airportDB, row, flightyFields.DivertedToIATA)
	if err != nil {
		return details, err
	}
	details.DivertedTo = airport

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

	return time.Time{}, errors.New("unable to extract an actual or scheduled time")
}

func exactPlaceName(airportName, arrivalTerminal, arrivalGate string) string {
	if arrivalTerminal == "" && arrivalGate == "" {
		return airportName
	}

	out := airportName + " ("
	addSep := false
	if arrivalTerminal != "" {
		out += "Terminal " + arrivalTerminal
		addSep = true
	}
	if arrivalGate != "" {
		if addSep {
			out += ", "
		}
		out += "Gate " + arrivalGate
	}

	return out + ")"
}
