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

func TestParseAccept(t *testing.T) {
	for i, tc := range []struct {
		input     string
		expect    acceptHeader
		shouldErr bool
	}{
		{
			input: "image/avif,image/webp,*/*",
			expect: acceptHeader{
				{mimeType: "image/avif", weight: 1},
				{mimeType: "image/webp", weight: 1},
				{mimeType: "*/*", weight: 1},
			},
		},
		{
			// safari's <img> tag, believe it or not
			input: "image/webp,image/avif,image/jxl,image/heic,image/heic-sequence,video/*;q=0.8,image/png,image/svg+xml,image/*;q=0.8,*/*;q=0.5",
			expect: acceptHeader{
				{mimeType: "image/webp", weight: 1},
				{mimeType: "image/avif", weight: 1},
				{mimeType: "image/jxl", weight: 1},
				{mimeType: "image/heic", weight: 1},
				{mimeType: "image/heic-sequence", weight: 1},
				{mimeType: "image/png", weight: 1},
				{mimeType: "image/svg+xml", weight: 1},
				{mimeType: "video/*", weight: 0.8},
				{mimeType: "image/*", weight: 0.8},
				{mimeType: "*/*", weight: 0.5},
			},
		},
		{
			// Chrome's <video> tag
			input: "video/webm,video/ogg,video/*;q=0.9,application/ogg;q=0.7,audio/*;q=0.6,*/*;q=0.5",
			expect: acceptHeader{
				{mimeType: "video/webm", weight: 1},
				{mimeType: "video/ogg", weight: 1},
				{mimeType: "video/*", weight: 0.9},
				{mimeType: "application/ogg", weight: 0.7},
				{mimeType: "audio/*", weight: 0.6},
				{mimeType: "*/*", weight: 0.5},
			},
		},
	} {
		actual, err := parseAccept(tc.input)
		if err != nil && !tc.shouldErr {
			t.Errorf("test %d: expected no error, but got one: %v", i, err)
		}
		if !actual.equal(tc.expect) {
			t.Errorf("test %d: expected %v but got %v", i, tc.expect, actual)
		}
	}
}

func (acc acceptHeader) equal(other acceptHeader) bool {
	return reflect.DeepEqual(acc, other)
}
