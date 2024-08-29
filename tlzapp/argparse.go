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
	"strconv"
	"strings"
)

// flagValPair associates a flag with its value.
type flagValPair struct {
	flag string
	val  interface{}
}

// flagValPairs parses args and associates flags with their values.
func flagValPairs(args []string) []flagValPair {
	var pairs []flagValPair

	var flag string
	for i := 0; i < len(args); i++ {
		arg := args[i]

		// first get the flag
		if isFlag(arg) {
			if flag != "" {
				// two flags in a row? previous one's value must be boolean!
				pairs = append(pairs, flagValPair{flag: flag, val: true})
			}
			flag = arg
			continue
		}

		// then get the flag's value, which should immediately follow the flag
		var val interface{}
		if len(arg) > 0 && arg[0] == '[' {
			// an array
			vals := []interface{}{}
			args[i] = args[i][1:]
			for j := i; j < len(args); j++ {
				elem := args[j]
				if elem[len(elem)-1] == ']' {
					vals = append(vals, autoType(elem[:len(elem)-1]))
					i = j // skip the array, now that we've consumed it
					break
				}
				vals = append(vals, autoType(elem))
			}
			val = vals
		} else {
			// a singular value
			val = autoType(args[i])
		}

		// save the flag and value pair
		pairs = append(pairs, flagValPair{flag: flag, val: val})
		flag = ""
	}

	if flag != "" {
		// the last argument must have been a boolean flag
		pairs = append(pairs, flagValPair{flag: flag, val: true})
	}

	return pairs
}

// isFlag returns whether s looks like a flag argument.
func isFlag(s string) bool {
	return len(s) > 2 && s[:2] == "--"
}

// autoType returns the value of str in its JSON type.
func autoType(str string) interface{} {
	s := strings.TrimSpace(strings.ToLower(str))
	if s == "true" {
		return true
	} else if s == "false" {
		return false
	}
	if num, err := strconv.Atoi(s); err == nil {
		return num
	}
	if dec, err := strconv.ParseFloat(s, 64); err == nil {
		return dec
	}
	return str
}
