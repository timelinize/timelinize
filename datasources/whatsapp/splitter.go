package whatsapp

import (
	"bufio"
	"regexp"
)

var messageStartRegex = regexp.MustCompile(`(?s)(` + "\u200E" + `)?\[(\d{4}-\d{2}-\d{2}), (\d{2}:\d{2}:\d{2})\] ([^:]+): `)

func chatSplit(data []byte, atEOF bool) (advance int, token []byte, err error) {
	// +1: Make sure not to match a message start if it's right at the start of the provided data
	// +3: Go two longer so a first message with an attachment (starting \u200E) doesn't match either
	// This means loc[0] will be three lower than it should be
	loc := messageStartRegex.FindIndex(data[4:])

	if loc == nil {
		// We have the last message — return it
		if atEOF {
			return 0, data, bufio.ErrFinalToken
		}
		// We need more data to find a whole message
		return 0, nil, nil
	}

	message := data[0 : loc[0]+4]
	return len(message), message, nil
}
