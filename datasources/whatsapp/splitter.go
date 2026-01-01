package whatsapp

import (
	"bufio"
	"bytes"
)

var lro = []byte{0xE2, 0x80, 0x8E} // U+200E (LRO)
const scanOffset = 4

type messageHeader struct {
	HeaderLen int
	HasLRO    bool
	Date      string
	Time      string
	Name      string
}

func chatSplit(data []byte, atEOF bool) (advance int, token []byte, err error) {
	// Start a few bytes in to avoid matching a message start at the very beginning of the buffer
	loc := findMessageStart(data, scanOffset)

	if loc == -1 {
		// We have the last message — return it
		if atEOF {
			return 0, data, bufio.ErrFinalToken
		}
		// We need more data to find a whole message
		return 0, nil, nil
	}

	message := data[:loc]
	return len(message), message, nil
}

// findMessageStart returns the index of the next message start at or after offset, or -1 if none.
func findMessageStart(data []byte, offset int) int {
	for i := offset; i < len(data); i++ {
		c := data[i]
		if c != '[' && (c < '0' || c > '9') && c != lro[0] {
			continue
		}
		if c == lro[0] && !bytes.HasPrefix(data[i:], lro) {
			continue
		}
		if isMessageStart(data[i:]) {
			return i
		}
	}
	return -1
}

func isMessageStart(b []byte) bool {
	_, ok := parseMessageHeader(b)
	return ok
}

func parseMessageHeader(b []byte) (messageHeader, bool) {
	origLen := len(b)

	var h messageHeader
	if bytes.HasPrefix(b, lro) {
		h.HasLRO = true
		b = b[len(lro):]
	}
	if len(b) < 10 {
		return messageHeader{}, false
	}

	// Standard WhatsApp export:
	// [YYYY/MM/DD, HH:MM(:SS)] Name: message
	if b[0] == '[' {
		endBracket := bytes.IndexByte(b, ']')
		if endBracket == -1 {
			return messageHeader{}, false
		}
		header := b[1:endBracket]
		parts := bytes.SplitN(header, []byte(", "), 2)
		if len(parts) != 2 || !isDate(parts[0]) || !isTime(parts[1]) {
			return messageHeader{}, false
		}

		nameStart := endBracket + 2 // "] "
		if nameStart >= len(b) || b[endBracket+1] != ' ' {
			return messageHeader{}, false
		}
		nameEndRel := bytes.Index(b[nameStart:], []byte(": "))
		if nameEndRel <= 0 {
			return messageHeader{}, false
		}
		nameEnd := nameStart + nameEndRel

		h.Date = string(parts[0])
		h.Time = string(parts[1])
		h.Name = string(b[nameStart:nameEnd])

		hLen := nameEnd + 2 // include ": "
		if h.HasLRO {
			hLen += len(lro)
		}
		// sanity
		if hLen > origLen {
			return messageHeader{}, false
		}
		h.HeaderLen = hLen
		return h, true
	}

	// Mobile export variant:
	// DD/MM/YYYY, HH:MM(:SS) - Name: message
	parts := bytes.SplitN(b, []byte(", "), 2)
	if len(parts) != 2 || !isDate(parts[0]) {
		return messageHeader{}, false
	}
	dateStr := parts[0]

	parts2 := bytes.SplitN(parts[1], []byte(" - "), 2)
	if len(parts2) != 2 || !isTime(parts2[0]) {
		return messageHeader{}, false
	}
	timeStr := parts2[0]
	nameAndMsg := parts2[1]

	nameEnd := bytes.Index(nameAndMsg, []byte(": "))
	if nameEnd <= 0 {
		return messageHeader{}, false
	}

	h.Date = string(dateStr)
	h.Time = string(timeStr)
	h.Name = string(nameAndMsg[:nameEnd])

	hLen := len(dateStr) + len(", ") + len(timeStr) + len(" - ") + nameEnd + 2 // ": "
	if h.HasLRO {
		hLen += len(lro)
	}
	if hLen > origLen {
		return messageHeader{}, false
	}
	h.HeaderLen = hLen
	return h, true
}

func skipLRO(b []byte) []byte {
	if bytes.HasPrefix(b, lro) {
		return b[len(lro):]
	}
	return b
}

func isDate(b []byte) bool {
	if len(b) != 10 {
		return false
	}
	sep4 := b[4]
	sep2 := b[2]

	// YYYY?MM?DD
	if sep4 == '-' || sep4 == '/' || sep4 == '.' {
		return isDigit(b[0]) && isDigit(b[1]) && isDigit(b[2]) && isDigit(b[3]) &&
			isDigit(b[5]) && isDigit(b[6]) && isDigit(b[8]) && isDigit(b[9]) &&
			b[7] == sep4
	}

	// DD?MM?YYYY
	if sep2 == '-' || sep2 == '/' || sep2 == '.' {
		return isDigit(b[0]) && isDigit(b[1]) &&
			isDigit(b[3]) && isDigit(b[4]) &&
			isDigit(b[6]) && isDigit(b[7]) && isDigit(b[8]) && isDigit(b[9]) &&
			b[2] == sep2 && b[5] == sep2
	}

	return false
}

func isTime(b []byte) bool {
	if len(b) != 5 && len(b) != 8 {
		return false
	}
	if !(isDigit(b[0]) && isDigit(b[1]) && b[2] == ':' && isDigit(b[3]) && isDigit(b[4])) {
		return false
	}
	if len(b) == 5 {
		return true
	}
	return b[5] == ':' && isDigit(b[6]) && isDigit(b[7])
}

func isDigit(c byte) bool {
	return c >= '0' && c <= '9'
}
