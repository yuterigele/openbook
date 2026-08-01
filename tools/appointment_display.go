package tools

import (
	"strings"
	"unicode"
)

// appointmentDisplayNumber returns the customer-facing reference for an
// appointment. The database ID remains the authoritative identifier used by
// tools and storage; it must not be included in customer-visible messages.
func appointmentDisplayNumber(appointmentID string) string {
	var b strings.Builder
	for _, r := range appointmentID {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(unicode.ToUpper(r))
			if b.Len() == 4 {
				break
			}
		}
	}
	if b.Len() == 0 {
		return "OB-未知"
	}
	return "OB-" + b.String()
}
