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

/*
The 2024/2025 on-device location histories have the following differences between Android and iOS:

1. 	On iOS, semanticSegments that have a timelinePath are all grouped at the end of the array.
	The chronological ordering is reset at the start of the group.
2. 	On iOS, timelinePath points are formatted like "geo:12.123456,-123.123456" but on Android
	they are formatted like "12.1234567°, -123.1234567°" (notice one more digit of precision).
3. 	On iOS, timelinePath points are only timestampped with a "durationMinutesOffsetFromStartTime"
	field (which is a string), whereas on Android, each point has its own complete timestamp.
*/

type onDeviceiOS2024Decoder struct {
	*json.Decoder

	// some objects have multiple location data points, so we "stick" the current
	// one until we've gotten through all the points on it
	multiLoc *onDeviceLocationiOS2024

	// set this to true when we have started the TimelinePath portion of the data
	doingPaths bool
}

func (dec *onDeviceiOS2024Decoder) NextLocation(_ context.Context) (*Location, error) {
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
			LatitudeE7:  int64(*coord.Latitude * float64(placesMult)),
			LongitudeE7: int64(*coord.Longitude * float64(placesMult)),
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
				LatitudeE7:  int64(*coord.Latitude * float64(placesMult)),
				LongitudeE7: int64(*coord.Longitude * float64(placesMult)),
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
		var newLoc *onDeviceLocationiOS2024
		if err := dec.Decode(&newLoc); err != nil {
			return nil, fmt.Errorf("decoding on-device location element: %w", err)
		}

		// there are different kinds of data points
		switch {
		case newLoc.Visit.TopCandidate.PlaceLocation != "":
			// this one is a single, self-contained location
			coord, err := newLoc.Visit.TopCandidate.PlaceLocation.parse()
			if err != nil {
				return nil, err
			}
			return &Location{
				Original:    newLoc,
				LatitudeE7:  int64(*coord.Latitude * float64(placesMult)),
				LongitudeE7: int64(*coord.Longitude * float64(placesMult)),
				Timestamp:   newLoc.StartTime,
				Timespan:    newLoc.EndTime,
			}, nil

		case newLoc.Activity.Start != "" && newLoc.Activity.End != "":
			// two locations; the next location will be this one's End value
			dec.multiLoc = newLoc

			coord, err := dec.multiLoc.Activity.Start.parse()
			if err != nil {
				return nil, err
			}
			return &Location{
				Original:    dec.multiLoc,
				LatitudeE7:  int64(*coord.Latitude * float64(placesMult)),
				LongitudeE7: int64(*coord.Longitude * float64(placesMult)),
				Timestamp:   dec.multiLoc.StartTime,
			}, nil

		case len(newLoc.TimelinePath) > 0:
			// many locations; pop the first one and process it
			dec.multiLoc = newLoc
			return handleTimelinePathPoint()
		}
	}

	return nil, nil
}

type onDeviceAndroid2025Decoder struct {
	*json.Decoder

	// some objects have multiple location data points (timelinePath) so
	// we "stick" it here and just chop off each one as we process them
	// TODO: group all points belonging to this segment, somehow (maybe a collection relation, like an item could be a Collection class that represents the path)
	currentSegment *semanticSegmentAndroid2025
}

func (dec *onDeviceAndroid2025Decoder) NextLocation(_ context.Context) (*Location, error) {
	handleTimelinePathSegment := func() (*Location, error) {
		vertex := dec.currentSegment.TimelinePath[0]

		coord, err := vertex.Point.parse()
		if err != nil {
			return nil, err
		}

		loc := &Location{
			Original:    dec.currentSegment,
			LatitudeE7:  int64(*coord.Latitude * float64(placesMult)),
			LongitudeE7: int64(*coord.Longitude * float64(placesMult)),
			Timestamp:   vertex.Time,
		}

		// pop off the point we just retrieved
		dec.currentSegment.TimelinePath = dec.currentSegment.TimelinePath[1:]

		// clean up the stickied segment when we're through the whole path
		if len(dec.currentSegment.TimelinePath) == 0 {
			// we can assume the last point in the path ended at
			// the end time of the segment containing this path
			if dec.currentSegment.EndTime.After(loc.Timestamp) {
				loc.Timespan = dec.currentSegment.EndTime
			}

			// finished with path, so clear stickied segment
			dec.currentSegment = nil
		}

		return loc, nil
	}

	// see if a data point is "stickied" that has more locations we need to get through
	if dec.currentSegment != nil {
		switch {
		case dec.currentSegment.Activity.End.LatLng != "":
			coord, err := dec.currentSegment.Activity.End.LatLng.parse()
			if err != nil {
				return nil, err
			}
			loc := &Location{
				Original:    dec.currentSegment,
				LatitudeE7:  int64(*coord.Latitude * float64(placesMult)),
				LongitudeE7: int64(*coord.Longitude * float64(placesMult)),
				Timestamp:   dec.currentSegment.EndTime,
			}
			dec.currentSegment = nil
			return loc, nil

		case len(dec.currentSegment.TimelinePath) > 0:
			return handleTimelinePathSegment()
		}
	}

	// decode next array element
	for dec.More() {
		var seg *semanticSegmentAndroid2025
		if err := dec.Decode(&seg); err != nil {
			return nil, fmt.Errorf("decoding Android 2025 on-device semanticSegment element: %w", err)
		}

		// shift the start and end times into their time zone, if specified
		const secPerMin, minsPerHour = 60, 60
		if seg.StartTimeTimezoneUTCOffsetMinutes != 0 {
			startTimeZone := time.FixedZone(fmt.Sprintf("UTC-%d", seg.StartTimeTimezoneUTCOffsetMinutes/minsPerHour), seg.StartTimeTimezoneUTCOffsetMinutes*secPerMin)
			seg.StartTime = seg.StartTime.In(startTimeZone)
		}
		if seg.EndTimeTimezoneUTCOffsetMinutes != 0 {
			endTimeZone := time.FixedZone(fmt.Sprintf("UTC-%d", seg.EndTimeTimezoneUTCOffsetMinutes/minsPerHour), seg.EndTimeTimezoneUTCOffsetMinutes*secPerMin)
			seg.EndTime = seg.EndTime.In(endTimeZone)
		}

		// there are different kinds of data points
		switch {
		case seg.Visit.TopCandidate.PlaceLocation.LatLng != "":
			// this one is a single, self-contained location
			coord, err := seg.Visit.TopCandidate.PlaceLocation.LatLng.parse()
			if err != nil {
				return nil, err
			}
			return &Location{
				Original:    seg,
				LatitudeE7:  int64(*coord.Latitude * float64(placesMult)),
				LongitudeE7: int64(*coord.Longitude * float64(placesMult)),
				Timestamp:   seg.StartTime,
			}, nil

		case seg.Activity.Start.LatLng != "" && seg.Activity.End.LatLng != "":
			// two locations; the next location will be this one's End value
			dec.currentSegment = seg

			coord, err := dec.currentSegment.Activity.Start.LatLng.parse()
			if err != nil {
				return nil, err
			}
			return &Location{
				Original:    dec.currentSegment,
				LatitudeE7:  int64(*coord.Latitude * float64(placesMult)),
				LongitudeE7: int64(*coord.Longitude * float64(placesMult)),
				Timestamp:   dec.currentSegment.StartTime,
			}, nil

		case len(seg.TimelinePath) > 0:
			// many locations; pop the first one and process it
			dec.currentSegment = seg
			return handleTimelinePathSegment()
		}
	}

	return nil, nil
}
