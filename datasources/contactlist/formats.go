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

package contactlist

import (
	"regexp"
	"strings"

	"github.com/timelinize/timelinize/timeline"
)

type format struct {
	name    string
	columns map[string][]stringMatcher
}

// match returns a mapping of canonical field names to the matched column
// indices of the header row.
func (f format) match(headerRow []string) map[string][]int {
	results := make(map[string][]int)

nextCol:
	for i, colName := range headerRow {
		for canonicalField, matchers := range f.columns {
			for _, matcher := range matchers {
				if matcher.MatchString(colName) {
					results[canonicalField] = append(results[canonicalField], i)
					continue nextCol
				}
			}
		}
	}

	return results
}

// TODO: there are a lot of columns I'm not accounting for yet that could be useful
var formats = []format{
	{
		name: "Google Contacts",
		columns: map[string][]stringMatcher{
			"full_name": {
				exact{"name"},
			},
			"birthdate": {
				exact{"birthday"},
			},
			"picture": {
				exact{"photo"},
			},
			timeline.AttributeGender: {
				exact{"gender"},
			},
			timeline.AttributeEmail: {
				regexp.MustCompile(`^E-mail \d+ - Value$`),
			},
			timeline.AttributePhoneNumber: {
				regexp.MustCompile(`^Phone \d+ - Value$`),
			},
		},
	},
	{
		name: "Generic",
		columns: map[string][]stringMatcher{
			"full_name": {
				exact{"name", "full name"},
			},
			"first_name": {
				exact{"first name", "given name"},
			},
			"last_name": {
				exact{"last name", "surname", "family name", "second name"},
			},
			"birthdate": {
				exact{"birthday", "birthdate", "birth date", "date of birth", "dob"},
			},
			timeline.AttributePhoneNumber: {
				exact{"phone", "phone number", "phone no", "telephone", "telephone number", "telephone no"},
			},
			timeline.AttributeEmail: {
				exact{"email", "email address", "electronic mail"},
			},
			timeline.AttributeGender: {
				exact{"gender", "sex"},
			},
			"picture": {
				exact{"photo", "picture", "photograph", "profile picture", "profile photo", "avatar"},
			},
		},
	},
}

type exact []string

// MatchString returns true if s is equal to any in the exact slice
// after being normalized (lowercase, spaces trimmed, parenthetical
// substrings removed, etc).
func (e exact) MatchString(s string) bool {
	clean := noise.ReplaceAllString(s, "")
	clean = strings.ToLower(strings.TrimSpace(clean))

	for _, val := range e {
		if val == clean {
			return true
		}
	}

	return false
}

// TODO: verify this matches (*), non-word chars, and multiple whitespace (e.g. after removing "-" from "A - B")
var noise = regexp.MustCompile(`\(.*\)|\W|_|\s{2,}`)

type stringMatcher interface {
	MatchString(input string) bool
}
