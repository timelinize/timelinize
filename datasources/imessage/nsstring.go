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

package imessage

import (
	"bytes"
	"errors"
	"fmt"
	"unicode/utf8"
)

// Here's a well-maintained and thorough rust implementation of imessage db parsing:
// https://github.com/ReagentX/imessage-exporter

// parseStreamTypedNSString extracts the plaintext string from a binary
// encoding of an NSAttributedString object, found in the attributedBody
// column of the iMessage chat database. Attributes (metadata?) are ignored
// currently, but it would be cool to parse those out as metadata.
func parseStreamTypedNSString(stream []byte) (string, error) {
	startIdx := bytes.Index(stream, nsStringStartPattern)
	if startIdx == -1 {
		return "", errors.New("no start pattern")
	}
	stream = stream[startIdx+len(nsStringStartPattern):]

	endIdx := bytes.Index(stream, nsStringEndPattern)
	if endIdx == -1 {
		return "", errors.New("no end pattern")
	}
	stream = stream[:endIdx]

	str := string(stream)

	// Determine prefix offset; start by assuming valid UTF-8
	offset := 1
	if !utf8.Valid(stream) {
		offset = 3
	}

	// Drop prefix characters
	// (TODO: would slicing, i.e. str[offset:] work?)
	// (see also: https://github.com/yakuter/nsattrparser/blob/main/nsattrparser.go -- we arrived at very similar code independently, possibly with LLM help)
	str, err := dropChars(offset, str)
	if err != nil {
		return "", err
	}

	return str, nil
}

// dropChars drops 'offset' characters from the front of a string.
func dropChars(offset int, str string) (string, error) {
	// Find the index of the specified character offset
	for range offset {
		_, size := utf8.DecodeRuneInString(str)
		if size == 0 {
			return "", fmt.Errorf("invalid prefix: '%s' (offset %d)", str, offset)
		}
		str = str[size:]
	}
	return str, nil
}

// nsStringStartPattern and nsStringEndPattern are the start and end patterns for extracting
// the plaintext string.
var (
	nsStringStartPattern = []byte{0x01, 0x2b}
	nsStringEndPattern   = []byte{0x86, 0x84}
)
