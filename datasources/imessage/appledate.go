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

package imessage

import (
	"fmt"
	"math"
	"strconv"
	"time"
)

// ParseAppleDate converts a date represented by a string of the decimal number of
// seconds since the Apple epoch to a Unix date. Example input: "-23919039.000000"
// TODO: This does seem to result in a timestamp offset by the local timezone (e.g. GMT -6 gets stored as 6 hours later than actual timestamp)
func ParseAppleDate(date string) (time.Time, error) {
	fractionalSeconds, err := strconv.ParseFloat(date, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("parsing string '%s' as float: %w", date, err)
	}
	sec, fraction := math.Modf(fractionalSeconds)
	return time.Unix(int64(sec)+timestampOffsetSeconds, int64(fraction*nanoToSec)), nil
}

// AppleSecondsToTime converts Apple timestamp in seconds to a normal timestamp.
func AppleSecondsToTime(appleSec int64) time.Time {
	return time.Unix(appleSec+timestampOffsetSeconds, 0)
}

// AppleNanoToTime converts Apple timestamp in nanoseconds to a normal timestamp.
func AppleNanoToTime(appleNano int64) time.Time {
	sec, nano := appleNano/nanoToSec, appleNano%nanoToSec
	return time.Unix(sec+timestampOffsetSeconds, nano)
}

const nanoToSec = 1e9

// Apple uses an epoch of Jan 1, 2001.
// This is the number of seconds that is after the Unix epoch.
const timestampOffsetSeconds = 978307200
