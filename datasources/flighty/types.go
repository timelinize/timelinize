package flighty

import (
	"fmt"
	"time"

	"github.com/timelinize/timelinize/internal/airports"
)

type flight struct {
	ID string

	Airline string
	Number  uint

	From       airports.Info
	To         airports.Info
	DivertedTo *airports.Info

	DepartureTerminal string
	DepartureGate     string
	ArrivalTerminal   string
	ArrivalGate       string

	TakeOffTime time.Time
	LandingTime time.Time

	Aircraft Aircraft

	ReasonType ReasonType
	Notes      string
}

func (f flight) WasDiverted() bool {
	return f.DivertedTo != nil
}

// Returns the 'To' Airport, or the 'DivertedTo' Airport, if one is present
func (f flight) Destination() airports.Info {
	if f.WasDiverted() {
		return *f.DivertedTo
	}
	return f.To
}

// Returns a short (English) markdown formatted description of the flight (from, to, diversions)
// TODO: How to handle localisation here?
func (f flight) Description() string {
	desc := fmt.Sprintf("Flight from _%s_ to _%s_", f.From.Name, f.To.Name)

	if f.WasDiverted() {
		desc += fmt.Sprintf(" (diverted to _%s_)", f.DivertedTo.Name)
	}

	return desc
}

type Aircraft struct {
	Type       string
	TailNumber string
	SeatNumber string
	SeatType   SeatType
	SeatCabin  CabinType
}

type SeatType string
type CabinType string
type ReasonType string

var (
	SeatAisle   SeatType = "AISLE"
	SeatMiddle  SeatType = "MIDDLE"
	SeatWindow  SeatType = "WINDOW"
	SeatJump    SeatType = "JUMPSEAT"
	SeatPilot   SeatType = "PILOT"
	SeatCaptain SeatType = "CAPTAIN"

	CabinEconomy        CabinType = "ECONOMY"
	CabinPremiumEconomy CabinType = "PREMIUM_ECONOMY"
	CabinBusiness       CabinType = "BUSINESS"
	CabinFirstClass     CabinType = "FIRST"
	CabinPrivate        CabinType = "PRIVATE"

	ReasonLeisure  ReasonType = "LEISURE"
	ReasonBusiness ReasonType = "BUSINESS"
	ReasonCrew     ReasonType = "CREW"
)
