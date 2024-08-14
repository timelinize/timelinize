/*
Timelinize
Copyright (c) 2013 Matthew Holt

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as published
by the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program.  If not, see <https://www.gnu.org/licenses/>.
*/

package googlelocation

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"
)

type onDeviceDecoder struct {
	*json.Decoder

	// some objects have multiple location data points, so we "stick" the current
	// one until we've gotten through all the points on it
	multiLoc *onDeviceLocation

	// set this to true when we have started the TimelinePath portion of the data
	doingPaths bool
}

func (dec *onDeviceDecoder) NextLocation(ctx context.Context) (*Location, error) {
	handleTimelinePathPoint := func() (*Location, error) {
		vertex := dec.multiLoc.TimelinePath[0]

		coord, err := vertex.Point.parse()
		if err != nil {
			return nil, err
		}
		offsetMin, err := strconv.Atoi(vertex.DurationMinutesOffsetFromStartTime)
		if err != nil {
			return nil, err
		}

		loc := &Location{
			Original:    dec.multiLoc,
			LatitudeE7:  int64(*coord.Latitude * float64(1e7)),
			LongitudeE7: int64(*coord.Longitude * float64(1e7)),
			Timestamp:   dec.multiLoc.StartTime.Add(time.Duration(offsetMin) * time.Minute),
		}

		// if this is the first path point, then indicate that to the
		// location processor so that it resets its state and allows
		// going back in time -- but we only indicate this once,
		// when we actually step back in time to start the new part
		// of the stream that goes forward in time again
		if !dec.doingPaths {
			dec.doingPaths = true // this sentinel ensures we only reset once
			loc.ResetTrack = true
		}

		// pop off the point we just retrieved
		dec.multiLoc.TimelinePath = dec.multiLoc.TimelinePath[1:]
		if len(dec.multiLoc.TimelinePath) == 0 {
			dec.multiLoc = nil
		}

		return loc, nil
	}

	// see if a data point is "stickied" that has more locations we need to get through
	if dec.multiLoc != nil {
		switch {
		case dec.multiLoc.Activity.End != "":
			coord, err := dec.multiLoc.Activity.End.parse()
			if err != nil {
				return nil, err
			}
			loc := &Location{
				Original:    dec.multiLoc,
				LatitudeE7:  int64(*coord.Latitude * float64(1e7)),
				LongitudeE7: int64(*coord.Longitude * float64(1e7)),
				Timestamp:   dec.multiLoc.EndTime,
			}
			dec.multiLoc = nil
			return loc, nil

		case len(dec.multiLoc.TimelinePath) > 0:
			return handleTimelinePathPoint()
		}
	}

	// decode next array element
	for dec.More() {
		var new *onDeviceLocation
		if err := dec.Decode(&new); err != nil {
			return nil, fmt.Errorf("decoding on-device location element: %v", err)
		}

		// there are different kinds of data points
		switch {
		case new.Visit.TopCandidate.PlaceLocation != "":
			// this one is a single, self-contained location
			coord, err := new.Visit.TopCandidate.PlaceLocation.parse()
			if err != nil {
				return nil, err
			}
			return &Location{
				Original:    new,
				LatitudeE7:  int64(*coord.Latitude * float64(1e7)),
				LongitudeE7: int64(*coord.Longitude * float64(1e7)),
				Timestamp:   new.StartTime,
				Timespan:    new.EndTime,
			}, nil

		case new.Activity.Start != "" && new.Activity.End != "":
			// two locations; the next location will be this one's End value
			dec.multiLoc = new

			coord, err := dec.multiLoc.Activity.Start.parse()
			if err != nil {
				return nil, err
			}
			return &Location{
				Original:    dec.multiLoc,
				LatitudeE7:  int64(*coord.Latitude * float64(1e7)),
				LongitudeE7: int64(*coord.Longitude * float64(1e7)),
				Timestamp:   dec.multiLoc.StartTime,
			}, nil

		case len(new.TimelinePath) > 0:
			// many locations; pop the first one and process it
			dec.multiLoc = new
			return handleTimelinePathPoint()
		}
	}

	return nil, nil
}
