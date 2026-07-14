package whatsapp

import "strings"

// WhatsApp inserts placeholder text for content it doesn't export as real
// message data (a deleted message, media omitted from an export without
// media, etc.). Export variants that include the LRO marker (U+200E) let us
// drop these generically, since only the text before the first LRO in a
// message is ever kept (see FileImport). Some export locales (eg. German)
// omit that marker entirely, so these exact phrases are matched directly as
// a fallback for those.
var deletedMessagePlaceholders = []string{
	"Diese Nachricht wurde gelöscht.", // German: "This message was deleted."
}

var mediaOmittedPlaceholders = []string{
	"<Medien ausgeschlossen>", // German: "<Media omitted>"
}

var editedMessageSuffixes = []string{
	"<Diese Nachricht wurde bearbeitet.>", // German: "<This message was edited>"
}

// isPlaceholderOnlyMessage reports whether text is entirely one of
// WhatsApp's own placeholders rather than user-authored content.
func isPlaceholderOnlyMessage(text string) bool {
	for _, p := range deletedMessagePlaceholders {
		if text == p {
			return true
		}
	}
	for _, p := range mediaOmittedPlaceholders {
		if text == p {
			return true
		}
	}
	return false
}

// stripEditedSuffix removes a trailing "message was edited" marker that
// WhatsApp appends to the real message text, returning the text unchanged
// if no such marker is present.
func stripEditedSuffix(text string) string {
	for _, suffix := range editedMessageSuffixes {
		if trimmed, ok := strings.CutSuffix(text, suffix); ok {
			return strings.TrimSpace(trimmed)
		}
	}
	return text
}
