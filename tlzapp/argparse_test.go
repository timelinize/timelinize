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

package tlzapp

import (
	"reflect"
	"testing"
)

func TestArgParseFlagValuePairs(t *testing.T) {
	for i, test := range []struct {
		input  []string
		expect []flagValPair
	}{
		{
			input: []string{"--foo", "bar"},
			expect: []flagValPair{
				{flag: "--foo", val: "bar"},
			},
		},
		{
			input: []string{"--foo", "1"},
			expect: []flagValPair{
				{flag: "--foo", val: 1},
			},
		},
		{
			input: []string{"--foo", "true"},
			expect: []flagValPair{
				{flag: "--foo", val: true},
			},
		},
		{
			input: []string{"--foo"},
			expect: []flagValPair{
				{flag: "--foo", val: true},
			},
		},
		{
			input: []string{"--foo", "[val1", "val2", "val3]", "--bar", "42"},
			expect: []flagValPair{
				{flag: "--foo", val: []interface{}{"val1", "val2", "val3"}},
				{flag: "--bar", val: 42},
			},
		},
		{
			input: []string{"--foo", ""},
			expect: []flagValPair{
				{flag: "--foo", val: ""},
			},
		},
	} {
		if actual, expected := flagValPairs(test.input), test.expect; !reflect.DeepEqual(actual, expected) {
			t.Errorf("Test %d: Expected %+v, got %+v", i, expected, actual)
		}
	}
}
