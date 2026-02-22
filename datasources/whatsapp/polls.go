package whatsapp

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/timelinize/timelinize/timeline"
)

// We use `\p{Lu}+` to match the "POLL" and "OPTION" words in any locale (eg. OPCIÓN)
var pollRegex = regexp.MustCompile(`^\p{Lu}+:\r\n(.+)\r\n$`)
var optionRegex = regexp.MustCompile(`^\p{Lu}+: (.+) \((\d+) .+\)\r\n$`)

func extractPoll(content []string) (string, timeline.Metadata, bool) {
	if len(content) <= metadataFieldNumber {
		return "", nil, false
	}

	parts := pollRegex.FindStringSubmatch(content[metadataFieldNumber])
	if parts == nil {
		return "", nil, false
	}

	meta := make(timeline.Metadata)
	meta["Poll question"] = parts[1]

	var sb strings.Builder // custom message
	sb.WriteString(parts[1])

	for i, optLine := range content[2:] {
		parts := optionRegex.FindStringSubmatch(optLine)
		if parts == nil {
			return "", nil, false
		}

		sb.WriteString("\r\n- ")
		sb.WriteString(parts[1])
		sb.WriteString(" (☑︎ ")
		sb.WriteString(parts[2])
		sb.WriteRune(')')

		// No need to track error, it's extracted with `\d+`
		voteCount, _ := strconv.Atoi(parts[2])

		meta[fmt.Sprintf("Poll option %d", i+1)] = parts[1]
		meta[fmt.Sprintf("Poll votes %d", i+1)] = voteCount
	}

	return sb.String(), meta, true
}
