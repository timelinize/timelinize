package whatsapp

import (
	"bufio"
	"regexp"
)

var messageStartRegex = regexp.MustCompile(`(?s)(\x200E)?\[(\d{4}-\d{2}-\d{2}), (\d{2}:\d{2}:\d{2})\] ([^:]+): `)

func chatSplit(data []byte, atEOF bool) (advance int, token []byte, err error) {
	// Make sure not to match a message start if it's right at the start of the provided data
	// This means loc[0] will be one lower than it should be
	loc := messageStartRegex.FindIndex(data[1:])

	if loc == nil {
		// We have the last message — return it
		if atEOF {
			return 0, data, bufio.ErrFinalToken
		}
		// We need more data to find a whole message
		return 0, nil, nil
	}

	// +1 of the offset above
	message := data[0 : loc[0]+1]

	return len(message), message, nil
}
