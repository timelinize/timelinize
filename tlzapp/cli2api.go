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
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// makeForm parses args and encodes the data as urlencoded-data.
func makeForm(args []string) string {
	formVals := url.Values{}

	keyVals := flagValPairs(args)

	for _, pair := range keyVals {
		formVals.Add(sanitizeFlag(pair.flag), fmt.Sprintf("%v", pair.val))
	}

	return formVals.Encode()
}

// sanitizeFlag turns a flag string like "--foo-bar"
// into "foo_bar"; i.e. it strips the flag prefix
// and standardizes its format.
func sanitizeFlag(s string) string {
	// figure out how long the dash-prefix iss
	name := s
	switch {
	case strings.HasPrefix(s, "--"):
		name = s[2:]
	case strings.HasPrefix(s, "-"):
		name = s[1:]
	}
	return strings.ReplaceAll(name, "-", "_")
}

// makeJSON parses args and encodes the data as JSON.
func makeJSON(args []string) ([]byte, error) {
	if len(args) == 0 {
		return nil, nil
	}

	// special cases: if there is only one arg and it is not
	// a flag, it must be a simple JSON data type.
	if len(args) == 1 && !isFlag(args[0]) {
		return json.Marshal(autoType(args[0]))
	}

	// create the object we will populate based on flags
	var obj interface{}

	// match up each flag to its value
	keyVals := flagValPairs(args)

	for _, pair := range keyVals {
		// strip flag prefix and format flag as a JSON object key
		cleanFlag := sanitizeFlag(pair.flag)

		// split the key into parts so we can traverse the object;
		// object nesting is separated with dots
		flagParts := strings.Split(cleanFlag, ".")

		// for mixed parts that have both a key and an array index (such as
		// "foo[0]") split them as if it was "foo.[0]" -- this is just syntactic
		// sugar, because the former feels more natural and familiar
		for j := 0; j < len(flagParts); j++ {
			part := flagParts[j]
			if k := strings.Index(part, "["); k > 0 && part[len(part)-1] == ']' {
				keyPart := part[:k]
				arrPart := part[k:]
				newParts := []string{keyPart, arrPart}
				flagParts = append(flagParts[:j], append(newParts, flagParts[j+1:]...)...)
			}
		}

		// now traverse the flag, part by part, recursively,
		// constructing the object as we go
		var err error
		obj, err = traverse(obj, flagParts, pair.val)
		if err != nil {
			return nil, err
		}
	}

	return json.Marshal(obj)
}

// traverse recursively builds obj from the bottom-up. On the way down, it
// turns nil obj into either an array or a map, and on the way back up,
// it assigns values to the new structure according to the first flagPart.
func traverse(obj interface{}, flagParts []string, val interface{}) (interface{}, error) {
	if len(flagParts) == 0 {
		return val, nil
	}

	// part will either be an array index ("[0]") or an object key ("foo")
	part := flagParts[0]

	if len(part) > 1 && part[0] == '[' && part[len(part)-1] == ']' {
		// part is an array index

		idx, err := strconv.Atoi(part[1 : len(part)-1])
		if err != nil {
			return obj, fmt.Errorf("invalid array index %s", part)
		}
		if obj == nil {
			obj = make([]interface{}, idx+1)
		}
		if objArr, ok := obj.([]interface{}); ok {
			if len(objArr) <= idx {
				objArr = append(objArr, make([]interface{}, idx-len(objArr)+1)...)
			}
			objArr[idx], err = traverse(objArr[idx], flagParts[1:], val)
			if err != nil {
				return obj, err
			}
			return objArr, nil
		}
		return obj, fmt.Errorf("inconsistent structure: expected an array at %s but got %T", part, obj)
	}

	// part is an object key

	if obj == nil {
		obj = make(map[string]interface{})
	}
	if objMap, ok := obj.(map[string]interface{}); ok {
		var err error
		objMap[part], err = traverse(objMap[part], flagParts[1:], val)
		if err != nil {
			return obj, err
		}
		return objMap, nil
	}
	return obj, fmt.Errorf("inconsistent structure: expected a map at %s but got %T", part, obj)
}
