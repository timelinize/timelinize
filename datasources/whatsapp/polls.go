package whatsapp

import (
	"fmt"
	"regexp"

	"github.com/timelinize/timelinize/timeline"
)

// We use `\p{Lu}+` to match the "POLL" and "OPTION" words in any locale (eg. OPCIÓN)
const optionRegexStr = `\p{Lu}+: (.+) \((\d) .+\)`

var optionRegex = regexp.MustCompile(optionRegexStr)
var pollRegex = regexp.MustCompile(`\p{Lu}+:\r\n(.+)((\r\n` + optionRegexStr + `)+)`)

func extractPoll(content []string) (string, timeline.Metadata, bool) {
	if len(content) < 2 {
		return "", nil, false
	}

	parts := pollRegex.FindStringSubmatch(content[1])
	if parts == nil {
		return "", nil, false
	}

	meta := make(timeline.Metadata)
	meta["Poll Question"] = parts[1]
	customMessage := parts[1]
	for i, opt := range optionRegex.FindAllStringSubmatch(parts[2], -1) {
		meta[fmt.Sprintf("Poll Option %d", i+1)] = opt[1]
		meta[fmt.Sprintf("Poll Votes %d", i+1)] = opt[2]

		customMessage += "\r\n- " + opt[1] + " (☑︎ " + opt[2] + ")"
	}

	return customMessage, meta, true
}
