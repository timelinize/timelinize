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

package generic

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net/textproto"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/mholt/goexif2/exif"
)

func (fi fileItem) timestamp() time.Time {
	// first try EXIF; we can ignore errors, since they should
	// just mean there's no EXIF or valid timestamp in the file
	ts, err := fi.timestampFromExif()
	if err == nil {
		return ts
	}

	// see if file has MIME header with Date field (emails generally do)
	ts, err = fi.timestampFromDateHeader()
	if err == nil {
		return ts
	}

	// see if the file path has any timestamp
	ts, err = fi.timestampFromFilePath()
	if err == nil {
		return ts
	}

	// as a last resort, fall back to file modification date
	// TODO: Nooooo don't do this
	info, err := fi.dirEntry.Info()
	if err == nil {
		return info.ModTime()
	}
	return time.Time{}
}

func (fi fileItem) timestampFromExif() (time.Time, error) {
	// TODO: apparently, there are ways to get EXIF from video files:
	// https://superuser.com/questions/1036704/is-there-something-like-exif-for-video
	// (exiftool can help)

	file, err := fi.fsys.Open(fi.path)
	if err != nil {
		return time.Time{}, fmt.Errorf("unable to open file to attempt reading EXIF: %w", err)
	}
	defer file.Close()

	ex, err := exif.Decode(file)
	if err != nil {
		return time.Time{}, err
	}

	return ex.DateTime()
}

func (fi fileItem) timestampFromDateHeader() (time.Time, error) {
	file, err := fi.fsys.Open(fi.path)
	if err != nil {
		return time.Time{}, err
	}
	defer file.Close()

	// date header should probably be in the first kilobyte of file
	const kb = 1024
	bufr := bufio.NewReader(io.LimitReader(file, kb))
	tp := textproto.NewReader(bufr)
	header, err := tp.ReadMIMEHeader()
	if err != nil {
		return time.Time{}, err
	}

	date := header.Get("Date")
	if date == "" {
		return time.Time{}, errors.New("headers found, but no Date field")
	}

	return time.Parse(time.RFC1123Z, date)
}

func (fi fileItem) timestampFromFilePath() (time.Time, error) {
	return TimestampFromFilePath(fi.path)
}

// TimestampFromFilePath finds all timestamps that can be found in any
// of the programmed formats in the file path. Overlapping timestamps
// are deduplicated, preferring the most specific values. Then one is
// chosen to be returned; partial timestamps (one date, another time)
// are combined if possible.
func TimestampFromFilePath(fpath string) (time.Time, error) {
	const (
		maxYearsAgo   = 300 // how many years in the past to allow a date
		maxYearsAhead = 1   // how many years in the future to allow a date
	)

	stdCanonicalPath := strings.ReplaceAll(fpath, "\\", "/")
	now := time.Now()

	type foundTimestamp struct {
		ts         time.Time
		start, end int
	}
	var tsFound []foundTimestamp

	// first try to find as many timestamps as we can
	for _, tsPattern := range timestampPatterns {
		for _, matchPos := range tsPattern.re.FindAllStringIndex(stdCanonicalPath, -1) {
			start, end := matchPos[0], matchPos[1]
			match := stdCanonicalPath[start:end]

			// TODO: Experimental; if matching a 4-digit year, because 4-digit numbers
			// TODO: might be common, it should be the whole path component...?
			// if len(match) <= 4 {

			// }

			// time formats are case-sensitive with regards to some
			// components like "AM" versus "am"... avoid this nuisance
			// by uppercasing everything
			match = strings.ToUpper(match)

			// a time format like "2006_1_2" can't parse "2021_10_3" (Oct. 3)
			// but works if not using underscores (maybe related to
			// https://github.com/golang/go/issues/11334)
			match = strings.ReplaceAll(match, "_", "-")
			format := strings.ReplaceAll(tsPattern.dateFormat, "_", "-")

			ts, err := time.ParseInLocation(format, match, time.Local)
			if err != nil {
				continue
			}

			// reject timestamp if it's a ridiculous amount in the past
			// or future; those are very most likely false positives
			if ts.Year() > 0 && (ts.Before(now.AddDate(-maxYearsAgo, 0, 0)) || ts.After(now.AddDate(maxYearsAhead, 0, 0))) {
				continue
			}

			tsFound = append(tsFound, foundTimestamp{
				ts:    ts,
				start: start,
				end:   end,
			})
		}
	}
	if len(tsFound) == 0 {
		return time.Time{}, fmt.Errorf("no timestamp found in file path: %s", fpath)
	}

	var candidateTimestamp []foundTimestamp

	// some timestamp formats overlap ("2006/1" is also in "2006/1/2",
	// but the latter is more specific), so find those which literally
	// overlap in the input string where they share a start or end
	// position OR one is wholly contained within another, so we can be
	// assured they are part of the same timestamp; then between those,
	// keep the more specific one
candidates:
	for i := 0; i < len(tsFound); i++ {
		// iTs is our candidate timestamp, we'll compare it to every other
		// timestamp and see if we need to weed it out
		iTs := tsFound[i]

		for j := 0; j < len(tsFound); j++ {
			if j == i {
				continue
			}
			jTs := tsFound[j] // jTs is the competitor

			// we're looking for joint overlap where start or end are equal
			// or if one is entirely contained within the other;
			// disjoint overlap exists if start OR end of one is between start AND end of other;
			// hard to say which one is right, but likely at least one is wrong...
			// for example:  "4 January 2022/3:59PM" has "2022/3" crossing into both, but is wrong
			if iTs.start == jTs.start || iTs.end == jTs.end || // same start or same end
				(iTs.start > jTs.start && iTs.end < jTs.end) || // jTs contains iTs
				(iTs.start < jTs.start && iTs.end > jTs.end) || // iTs contains jTs
				(iTs.start > jTs.start && iTs.start < jTs.end) || // iTs starts inside jTs (disjoint overlap)
				(iTs.end > jTs.start && iTs.end < jTs.end) { // iTs ends inside jTs (disjoint overlap)

				// if it was joint overlap, we can presume they are the same timestamp,
				// but likely have different components specified in them; keep the more
				// specific one, which SHOULD be the "later" or "higher" time value,
				// because more non-zero components add to the timestamp -- BUT I found
				// a counterexample: "1953/10-09-1953" has both Oct 9, 1953, and Oct 9,
				// 2019 ("10-09-19"), where the higher date is clearly wrong here, so I've
				// settled on always going with the longest substring match

				// if jTs is more specific let iTs drop
				if (jTs.end - jTs.start) > (iTs.end - iTs.start) {
					continue candidates
				}
			}
		}

		// if we got here, the inner loop didn't skip this candidate,
		// so we can presumably use it for next phase
		candidateTimestamp = append(candidateTimestamp, iTs)
	}

	// if we ended up skipping all timestamps because they were
	// all positioned confusingly, keep them all and simply try
	// sorting (returning none when we found some seems unwise)
	if len(candidateTimestamp) == 0 {
		candidateTimestamp = tsFound
	}

	// we may have found multiple timestamps, for example one that
	// contains a date and another which contains time; try to find
	// them and combine them
	var tsDate, tsTime time.Time
	for _, ts := range candidateTimestamp {
		if (!zeroDate(ts.ts) && zeroTime(ts.ts)) && ts.ts.After(tsDate) {
			tsDate = ts.ts
		}
		if (zeroDate(ts.ts) && !zeroTime(ts.ts)) && timeOfDayIsLater(ts.ts, tsTime) {
			tsTime = ts.ts
		}
	}
	// TODO: we should still try to combine separate year, month, and day timestamps... somehow...
	if !tsDate.IsZero() && !tsTime.IsZero() {
		year, month, day := tsDate.Date()
		return tsTime.AddDate(year, int(month)-1, day-1).Local(), nil
	}

	// TODO: a date like "1959" should maybe set a timespan, from the first to the last second of that year, rather than just second 0 of that year?

	// if more than one timestamp remains, I dunno how to prefer one
	// over another since we've already taken care of overlapping
	// timestamps... but here's what I've found works well on a small
	// sample so far (some of which are in the test cases):
	// - Prefer longest substring match (most specific)
	// - Prefer last one (filename is likely more correct than prior path components)
	sort.Slice(candidateTimestamp, func(i, j int) bool {
		iLen := candidateTimestamp[i].end - candidateTimestamp[i].start
		jLen := candidateTimestamp[j].end - candidateTimestamp[j].start
		if iLen != jLen {
			return iLen > jLen
		}
		return candidateTimestamp[i].start > candidateTimestamp[j].start
	})

	return candidateTimestamp[0].ts, nil
}

// zeroDate returns true if the date component of t is zero-valued
// (after parsing, which sets the year to 0 if no year was parsed; default
// time.Time structs set the year as 1 which is a little maddening).
func zeroDate(t time.Time) bool {
	return t.Year() == 0 && t.Month() == time.January && t.Day() == 1
}

// zeroTime returns true if the time component of t is zero-valued.
func zeroTime(t time.Time) bool {
	return t.Hour() == 0 && t.Minute() == 0 && t.Second() == 0
}

// timeOfDayIsLater returns true if t1 is at a later time of day than t2.
func timeOfDayIsLater(t1, t2 time.Time) bool {
	t1BeginOfDay := time.Date(t1.Year(), t1.Month(), t1.Day(), 0, 0, 0, 0, time.UTC)
	t2BeginOfDay := time.Date(t2.Year(), t2.Month(), t2.Day(), 0, 0, 0, 0, time.UTC)
	return t1.Sub(t1BeginOfDay) > t2.Sub(t2BeginOfDay)
}

// timestampPatterns maps Go time format strings to the regexp that matches them.
type timestampPattern struct {
	dateFormat string
	re         *regexp.Regexp
}

var timestampPatterns = []timestampPattern{
	{dateFormat: "2006/1/2", re: regexp.MustCompile(`\d{4}/\d\d?/\d\d?`)},
	{dateFormat: "2006\\1\\2", re: regexp.MustCompile(`\d{4}\\d\d?\\d\d?`)},
	{dateFormat: "1-2-06", re: regexp.MustCompile(`\d\d?-\d\d?-\d\d`)},    // NOTE: this is ambiguous depending on locale! Could be 2-1-06 as well
	{dateFormat: "1-2-2006", re: regexp.MustCompile(`\d\d?-\d\d?-\d{4}`)}, // NOTE: this is ambiguous depending on locale! Could be 2-1-2006 as well
	{dateFormat: "2006-1-2", re: regexp.MustCompile(`\d{4}-\d\d?-\d\d?`)},
	{dateFormat: "2006_1_2", re: regexp.MustCompile(`\d{4}_\d\d?_\d\d?`)},
	{dateFormat: "2 January 2006", re: regexp.MustCompile(`\d\d? \w+ \d{4}`)},
	{dateFormat: "2 Jan 2006", re: regexp.MustCompile(`\d\d? \w{3} \d{4}`)},
	{dateFormat: "2 January 06", re: regexp.MustCompile(`\d\d?, \w+ \d{2}`)},
	{dateFormat: "January 2 2006", re: regexp.MustCompile(`\w+ \d\d? \d{4}`)},
	{dateFormat: "January 2, 2006", re: regexp.MustCompile(`\w+ \d\d?, \d{4}`)},
	{dateFormat: "Jan 2, 2006", re: regexp.MustCompile(`\w{3} \d\d?, \d{4}`)},
	{dateFormat: "Jan 2, 06", re: regexp.MustCompile(`\w{3} \d\d?, \d{2}`)},
	{dateFormat: "2006/January", re: regexp.MustCompile(`\d{4}/\w+`)},
	{dateFormat: "2006/2 January", re: regexp.MustCompile(`\d{4}/\d\d? \w+`)},
	{dateFormat: "2006/2 Jan", re: regexp.MustCompile(`\d{4}/\d\d? \w{3}`)},
	{dateFormat: "2 January", re: regexp.MustCompile(`\d\d? \w+`)},
	{dateFormat: "January 2", re: regexp.MustCompile(`\w+ \d\d?`)},
	{dateFormat: "January 2006", re: regexp.MustCompile(`\w+ \d{4}`)},
	{dateFormat: "January-2006", re: regexp.MustCompile(`\w+-\d{4}`)},
	{dateFormat: "January_2006", re: regexp.MustCompile(`\w+_\d{4}`)},
	{dateFormat: "Jan 2006", re: regexp.MustCompile(`\w{3} \d{4}`)},
	{dateFormat: "Jan_2006", re: regexp.MustCompile(`\w{3}_\d{4}`)},
	{dateFormat: "Jan-2006", re: regexp.MustCompile(`\w{3}-\d{4}`)},
	{dateFormat: "1-2006", re: regexp.MustCompile(`\d\d?-\d{4}`)},
	{dateFormat: "2006_January", re: regexp.MustCompile(`\d{4}_\w+`)},
	{dateFormat: "2006_Jan", re: regexp.MustCompile(`\d{4}_\w{3}`)},
	{dateFormat: "2006 January", re: regexp.MustCompile(`\d{4} \w+`)},
	{dateFormat: "2006 Jan", re: regexp.MustCompile(`\d{4} \w{3}`)},
	{dateFormat: "January", re: regexp.MustCompile(`\w+`)},
	{dateFormat: "Jan", re: regexp.MustCompile(`\w{3}`)},
	{dateFormat: "2006/1", re: regexp.MustCompile(`\d{4}/\d\d?`)},
	{dateFormat: "2006\\1", re: regexp.MustCompile(`\d{4}\\d\d?`)},
	{dateFormat: "2006-1", re: regexp.MustCompile(`\d{4}-\d\d?`)},
	{dateFormat: "2006", re: regexp.MustCompile(`\d{4}`)},
	// TODO: these next few formats have flaky tests... (UPDATE MAY 10, 2022, I think I got the flakiness gone by fixing the nested for loops above) sometimes it fails because it doesn't choose the one with PM..},
	{dateFormat: "15:04", re: regexp.MustCompile(`\d\d?:\d\d`)},
	{dateFormat: "3:04PM", re: regexp.MustCompile(`(?i)\d\d?:\d\d[AP]M`)},
	{dateFormat: "3:04 PM", re: regexp.MustCompile(`(?i)\d\d?:\d\d [AP]M`)},
	// TODO: would be nice to isolate these numbers somehow, i.e. surrounded by non-numbers or they are the whole component of a path..},
	{dateFormat: "20060102150405", re: regexp.MustCompile(`\d{14}`)},
	{dateFormat: "200601021504", re: regexp.MustCompile(`\d{12}`)},
	{dateFormat: "20060102", re: regexp.MustCompile(`\d{8}`)},
	{dateFormat: "20060102_1504", re: regexp.MustCompile(`\d{8}_\d{4}`)},
	{dateFormat: "20060102-1504", re: regexp.MustCompile(`\d{8}-\d{4}`)},
	{dateFormat: "2006-1-2_15-4-5", re: regexp.MustCompile(`\d{4}-\d\d?-\d\d?_\d\d?-\d\d?-\d\d?`)},
}
