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

package timeline

import (
	"testing"
	"time"
)

// TODO: Both these tests need fixing; the ContainsItem one especially needs more test cases

func TestTimeframeContains(t *testing.T) {
	for i, tc := range []struct {
		timeframe Timeframe
		input     time.Time
		expect    bool
	}{
		{
			timeframe: Timeframe{},
			input:     time.Now(),
			expect:    true,
		},
		{
			timeframe: Timeframe{
				Since: ptr(time.Date(2022, 1, 3, 0, 0, 0, 0, time.UTC)),
				Until: ptr(time.Date(2022, 1, 5, 0, 0, 0, 0, time.UTC)),
			},
			input:  time.Date(2022, 1, 4, 0, 0, 0, 0, time.UTC),
			expect: true,
		},
		{
			timeframe: Timeframe{
				Since: ptr(time.Date(2022, 1, 3, 0, 0, 0, 0, time.UTC)),
				Until: ptr(time.Date(2022, 1, 5, 0, 0, 0, 0, time.UTC)),
			},
			input:  time.Time{},
			expect: true,
		},
		{
			timeframe: Timeframe{
				Since: ptr(time.Date(2022, 1, 3, 0, 0, 0, 0, time.UTC)),
			},
			input:  time.Time{},
			expect: true,
		},
		{
			timeframe: Timeframe{
				Since: ptr(time.Date(2022, 1, 3, 0, 0, 0, 0, time.UTC)),
			},
			input:  time.Date(2022, 1, 6, 0, 0, 0, 0, time.UTC),
			expect: true,
		},
		{
			timeframe: Timeframe{
				Until: ptr(time.Date(2022, 1, 5, 0, 0, 0, 0, time.UTC)),
			},
			input:  time.Time{},
			expect: true,
		},
		{
			timeframe: Timeframe{
				Until: ptr(time.Date(2022, 1, 5, 0, 0, 0, 0, time.UTC)),
			},
			input:  time.Date(2022, 1, 6, 0, 0, 0, 0, time.UTC),
			expect: false,
		},
	} {
		actual := tc.timeframe.Contains(tc.input)
		if actual != tc.expect {
			t.Errorf("Test %d: Expected %v, got %v (input=%s timeframe=%s)",
				i, tc.expect, actual, tc.input, tc.timeframe)
		}
	}
}

func TestTimeframeContainsItem(t *testing.T) {
	for i, tc := range []struct {
		timeframe Timeframe
		input     *Item
		strict    bool
		expect    bool
	}{
		{
			timeframe: Timeframe{},
			input:     &Item{Timestamp: time.Now()},
			expect:    true,
		},
		{
			timeframe: Timeframe{
				Since: ptr(time.Date(2022, 1, 3, 0, 0, 0, 0, time.UTC)),
				Until: ptr(time.Date(2022, 1, 5, 0, 0, 0, 0, time.UTC)),
			},
			input:  &Item{Timestamp: time.Date(2022, 1, 4, 0, 0, 0, 0, time.UTC)},
			expect: true,
		},
		{
			timeframe: Timeframe{
				Since: ptr(time.Date(2022, 1, 3, 0, 0, 0, 0, time.UTC)),
				Until: ptr(time.Date(2022, 1, 5, 0, 0, 0, 0, time.UTC)),
			},
			input:  &Item{Timestamp: time.Time{}},
			expect: true,
		},
		{
			timeframe: Timeframe{
				Since: ptr(time.Date(2022, 1, 3, 0, 0, 0, 0, time.UTC)),
			},
			input:  &Item{Timestamp: time.Time{}},
			expect: true,
		},
		{
			timeframe: Timeframe{
				Since: ptr(time.Date(2022, 1, 3, 0, 0, 0, 0, time.UTC)),
			},
			input:  &Item{Timestamp: time.Date(2022, 1, 6, 0, 0, 0, 0, time.UTC)},
			expect: true,
		},
		{
			timeframe: Timeframe{
				Until: ptr(time.Date(2022, 1, 5, 0, 0, 0, 0, time.UTC)),
			},
			input:  &Item{Timestamp: time.Time{}},
			expect: true,
		},
		{
			timeframe: Timeframe{
				Until: ptr(time.Date(2022, 1, 5, 0, 0, 0, 0, time.UTC)),
			},
			input:  &Item{Timestamp: time.Date(2022, 1, 6, 0, 0, 0, 0, time.UTC)},
			expect: false,
		},
	} {
		actual := tc.timeframe.ContainsItem(tc.input, tc.strict)
		if actual != tc.expect {
			t.Errorf("Test %d: Expected %v, got %v (input=%+v timeframe=%+v strict=%t)",
				i, tc.expect, actual, tc.input, tc.timeframe, tc.strict)
		}
	}
}

func ptr(t time.Time) *time.Time { return &t }
