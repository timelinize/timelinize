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
	"testing"
	"time"
)

func TestTimestampFromFilePath(t *testing.T) {
	for i, tc := range []struct {
		filepath  string
		expect    time.Time
		shouldErr bool
	}{
		{
			filepath: "2020/02/3/example.txt",
			expect:   time.Date(2020, time.February, 3, 0, 0, 0, 0, time.Local),
		},
		{
			filepath: "2020\\02\\3\\example.txt",
			expect:   time.Date(2020, time.February, 3, 0, 0, 0, 0, time.Local),
		},
		{
			filepath: "text_files/2020/02/3/example.txt",
			expect:   time.Date(2020, time.February, 3, 0, 0, 0, 0, time.Local),
		},
		{
			filepath: "text_files\\2020\\02\\3\\example.txt",
			expect:   time.Date(2020, time.February, 3, 0, 0, 0, 0, time.Local),
		},
		{
			filepath: "text_files/2020-02-3/example.txt",
			expect:   time.Date(2020, time.February, 3, 0, 0, 0, 0, time.Local),
		},
		{
			filepath: "text_files/2020_02_22/example.txt",
			expect:   time.Date(2020, time.February, 22, 0, 0, 0, 0, time.Local),
		},
		{
			filepath: "text_files/2021_10_3/example.txt",
			expect:   time.Date(2021, time.October, 3, 0, 0, 0, 0, time.Local),
		},
		{
			filepath: "text_files/2021/11/example.txt",
			expect:   time.Date(2021, time.November, 1, 0, 0, 0, 0, time.Local),
		},
		{
			filepath: "2021\\9\\example.txt",
			expect:   time.Date(2021, time.September, 1, 0, 0, 0, 0, time.Local),
		},
		{
			filepath: "text_files/2021-11/example.txt",
			expect:   time.Date(2021, time.November, 1, 0, 0, 0, 0, time.Local),
		},
		{
			filepath: "text_files/January 2022/example.txt",
			expect:   time.Date(2022, time.January, 1, 0, 0, 0, 0, time.Local),
		},
		{
			filepath: "text_files/15 January 2022/example.txt",
			expect:   time.Date(2022, time.January, 15, 0, 0, 0, 0, time.Local),
		},
		{
			filepath: "text_files/5:15.txt",
			expect:   time.Date(0, time.January, 1, 5, 15, 0, 0, time.Local),
		},
		{
			filepath: "text_files/17:15.txt",
			expect:   time.Date(0, time.January, 1, 17, 15, 0, 0, time.Local),
		},
		{
			filepath: "text_files/5:15 AM.txt",
			expect:   time.Date(0, time.January, 1, 5, 15, 0, 0, time.Local),
		},
		{
			filepath: "text_files/5:15 PM.txt",
			expect:   time.Date(0, time.January, 1, 17, 15, 0, 0, time.Local),
		},
		{
			filepath: "text_files/5:15 pm.txt",
			expect:   time.Date(0, time.January, 1, 17, 15, 0, 0, time.Local),
		},
		{
			filepath: "text_files/5:15pm.txt",
			expect:   time.Date(0, time.January, 1, 17, 15, 0, 0, time.Local),
		},
		{
			// good test case because result should be aggregate
			// of two timestamps with different information, while
			// ignoring third, disjoint non-timestamp ("2022/3")
			// TODO: this test is flaky! sometimes it passes and sometimes it fails. must be relying on map ordering or something...
			filepath: "text_files/4 January 2022/3:59pm.txt",
			expect:   time.Date(2022, time.January, 4, 15, 59, 0, 0, time.Local),
		},
		{
			filepath: "20220107201355.txt",
			expect:   time.Date(2022, time.January, 07, 20, 13, 55, 0, time.Local),
		},
		{
			filepath: "202201072013.txt",
			expect:   time.Date(2022, time.January, 07, 20, 13, 0, 0, time.Local),
		},
		{
			// example that does not contain a date, but looks like it might
			filepath:  "1143917197208805380-2z-8NloU.jpg.txt",
			shouldErr: true,
		},
		{
			filepath: "March_2013.txt",
			expect:   time.Date(2013, time.March, 1, 0, 0, 0, 0, time.Local),
		},
		{
			filepath: "Mar_2013.txt",
			expect:   time.Date(2013, time.March, 1, 0, 0, 0, 0, time.Local),
		},
		{
			filepath: "2021_February.txt",
			expect:   time.Date(2021, time.February, 1, 0, 0, 0, 0, time.Local),
		},
		{
			filepath: "2021_Feb.txt",
			expect:   time.Date(2021, time.February, 1, 0, 0, 0, 0, time.Local),
		},
		{
			filepath: "2021-11-14_21-23-23.mp4",
			expect:   time.Date(2021, time.November, 14, 21, 23, 23, 0, time.Local),
		},
		{
			filepath: "1950s/1959/003.jpg",
			expect:   time.Date(1959, time.January, 1, 0, 0, 0, 0, time.Local),
		},
		{
			filepath: "1950s/1954/1954 (2).jpg",
			expect:   time.Date(1954, time.January, 1, 0, 0, 0, 0, time.Local),
		},
		{
			filepath: "1950s/1954/05 May/31 Mon-Fenelon/Wilson slides last row in the box0049 flip.jpg",
			expect:   time.Date(1954, time.May, 5, 0, 0, 0, 0, time.Local),
		},
		{
			filepath: "ST 05n-12-1965 negative.jpg",
			expect:   time.Date(1965, time.December, 1, 0, 0, 0, 0, time.Local),
		},
		{
			filepath: "1953/10-09-1953 Foo.jpg",
			expect:   time.Date(1953, time.October, 9, 0, 0, 0, 0, time.Local),
		},
		{
			filepath: "1958/06-19-2012.JPG", // mistakenly placed in a folder for year 1958
			expect:   time.Date(2012, time.June, 19, 0, 0, 0, 0, time.Local),
		},
		{
			filepath: "1957/03 March-Mark's BD-Roe visit-Fenelon/01 to 05 blank",
			expect:   time.Date(1957, time.March, 3, 0, 0, 0, 0, time.Local),
		},
		{
			filepath:  "289115678_10166502958880693_2765038754221272615_n.jpg",
			shouldErr: true,
		},
	} {
		fi := fileItem{path: tc.filepath}
		actual, err := fi.timestampFromFilePath()
		if err == nil && tc.shouldErr {
			t.Errorf("Test %d (%s): Expected error but didn't get one", i, tc.filepath)
		} else if err != nil && !tc.shouldErr {
			t.Errorf("Test %d (%s): Expected no error but got: %v", i, tc.filepath, err)
		}
		if !actual.Equal(tc.expect) {
			t.Errorf("Test %d (%s): expected %s but got %s", i, tc.filepath, tc.expect, actual)
		}
	}
}
