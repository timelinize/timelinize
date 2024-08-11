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
	"fmt"
	"sort"
	"strconv"
	"strings"
)

type acceptHeader []mimeTypeQ

func parseAccept(accept string) (acceptHeader, error) {
	parts := strings.Split(accept, ",")
	pairs := make(acceptHeader, 0, len(parts))
	for _, part := range parts {
		mimePart, qPart, cut := strings.Cut(strings.TrimSpace(part), ";q=")
		if mimePart == "" {
			continue
		}
		pair := mimeTypeQ{
			mimeType: strings.TrimSpace(strings.ToLower(mimePart)),
			weight:   1,
		}
		if cut {
			weight, err := strconv.ParseFloat(qPart, 32)
			if err != nil {
				return nil, fmt.Errorf("bad q value '%s': %v", qPart, err)
			}
			pair.weight = float32(weight)
		}
		pairs = append(pairs, pair)
	}
	// sort by descending weight (q-factor)
	sort.SliceStable(pairs, func(i, j int) bool {
		return pairs[i].weight > pairs[j].weight
	})
	return pairs, nil
}

// preference returns the client's preference, given the possible MIME types passed in.
// The return value will always be one of the possibleTypes or an empty string if there
// are no matches. The order of possibleTypes doesn't matter, except that the first
// one that matches the client's highest preference will be selected in the case of
// a q-factor tie.
func (acc acceptHeader) preference(possibleTypes ...string) string {
	for _, a := range acc {
		for _, possible := range possibleTypes {
			if a.matches(possible) && a.weight > 0 {
				return possible
			}
		}
	}
	return ""
}

type mimeTypeQ struct {
	mimeType string
	weight   float32
}

func (m mimeTypeQ) matches(candidate string) bool {
	// fast path: everything matches
	if m.mimeType == "*/*" {
		return true
	}

	mime1, sub1, _ := strings.Cut(m.mimeType, "/")
	mime2, sub2, _ := strings.Cut(strings.TrimSpace(candidate), "/")

	// first check if main type matches
	// (main type cannot be * unless subtype is also *, which we handle above)
	if !strings.EqualFold(mime1, mime2) {
		return false
	}

	// then see if subtype matches
	if sub1 == "*" || sub2 == "*" || strings.EqualFold(sub1, sub2) {
		return true
	}

	return false
}
