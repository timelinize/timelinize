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

import "testing"

func TestMakeForm(t *testing.T) {
	for i, tc := range []struct {
		input    []string
		expected string
	}{
		{
			input:    []string{"--foo", "bar"},
			expected: "foo=bar",
		},
		{
			input:    []string{"--foo"},
			expected: "foo=true",
		},
		{
			input:    []string{"--foo", "1"},
			expected: "foo=1",
		},
		{
			input:    []string{"--foo", "bar", "--key", "val"},
			expected: "foo=bar&key=val",
		},
		{
			input:    []string{"--bool1", "--bool2"},
			expected: "bool1=true&bool2=true",
		},
	} {
		actual := makeForm(tc.input)
		if actual != tc.expected {
			t.Errorf("Test %d: Expected '%s' but got '%s'", i, tc.expected, actual)
		}
	}
}

func TestMakeJSON(t *testing.T) {
	for i, tc := range []struct {
		input     []string
		expected  string
		shouldErr bool
	}{
		{
			input:    []string{"--foo", "bar"},
			expected: `{"foo":"bar"}`,
		},
		{
			input:    []string{"--bool"},
			expected: `{"bool":true}`,
		},
		{
			input:    []string{"--top.sub", "val"},
			expected: `{"top":{"sub":"val"}}`,
		},
		{
			input:    []string{"--top", "1"},
			expected: `{"top":1}`,
		},
		{
			input:    []string{"--top.sub1.sub2", "val", "--float", "1.5"},
			expected: `{"float":1.5,"top":{"sub1":{"sub2":"val"}}}`,
		},
		{
			input:    []string{"--arr[0]", "val"},
			expected: `{"arr":["val"]}`,
		},
		{
			input:    []string{"--arr[1]", "val"},
			expected: `{"arr":[null,"val"]}`,
		},
		{
			input:    []string{"--arr[0].foo", "bar", "--arr[1].foo", "baz"},
			expected: `{"arr":[{"foo":"bar"},{"foo":"baz"}]}`,
		},
		{
			input:    []string{"--arr.[0].foo", "bar", "--arr.[1].foo", "baz"},
			expected: `{"arr":[{"foo":"bar"},{"foo":"baz"}]}`,
		},
		{
			input:    []string{"--foo-bar"},
			expected: `{"foo_bar":true}`,
		},
		{
			input:     []string{"--[0]", "foo", "--bar"},
			shouldErr: true,
		},
	} {
		actual, err := makeJSON(tc.input)
		if err == nil && tc.shouldErr {
			t.Errorf("Test %d: Should have errored, but did not", i)
		} else if err != nil && !tc.shouldErr {
			t.Errorf("Test %d: Should NOT have errored, but did: %v", i, err)
		}
		if string(actual) != tc.expected {
			t.Errorf("Test %d: %v\nExpected: '%s'\n     Got: '%s'", i, tc.input, tc.expected, actual)
		}
	}
}

func TestSanitizeFlag(t *testing.T) {
	want := "foo_bar"
	if got := sanitizeFlag("--foo-bar"); got != want {
		t.Errorf("Wanted '%s' but got '%s'", want, got)
	}

	want = "foo-bar.flub-dub"
	if got := sanitizeFlag("--foo-bar.flub-dub"); got != want {
		t.Errorf("Wanted '%s' but got '%s'", want, got)
	}

	want = "foo_bar.flub_dub"
	if got := sanitizeFlag("--foo_bar.flub_dub"); got != want {
		t.Errorf("Wanted '%s' but got '%s'", want, got)
	}
}
