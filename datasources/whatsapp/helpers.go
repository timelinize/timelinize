package whatsapp

// extractMatchParts normalizes the regex capture groups for both bracketed and dash formats.
func extractMatchParts(sub []string) (dateStr, timeStr, name string) {
	// Bracketed form: groups 2=date, 3=time, 4=name
	if len(sub) >= 5 && sub[2] != "" {
		return sub[2], sub[3], sub[4]
	}
	// Dash form: groups 5=date, 6=time, 7=name
	if len(sub) >= 8 {
		return sub[5], sub[6], sub[7]
	}
	return "", "", ""
}
