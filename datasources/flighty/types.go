package flighty

import (
	"fmt"
	"time"
)

type Flight struct {
	ID string

	Airline string
	Number  uint

	From       AirportInfo
	To         AirportInfo
	DivertedTo *AirportInfo

	TakeOffTime time.Time
	LandingTime time.Time

	Aircraft Aircraft

	ReasonType ReasonType
	Notes      string
}

func (f Flight) WasDiverted() bool {
	return f.DivertedTo != nil
}

// Returns the 'To' Airport, or the 'DivertedTo' Airport, if one is present
func (f Flight) Destination() AirportInfo {
	if f.WasDiverted() {
		return *f.DivertedTo
	}
	return f.To
}

// Returns a short (English) markdown formatted description of the flight (from, to, diversions)
// TODO: How to handle localisation here?
func (f Flight) Description() string {
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
