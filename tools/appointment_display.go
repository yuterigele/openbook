package tools

import (
	"context"
	"strings"
	"unicode"

	"github.com/yuterigele/openbook/storage"
)

// appointmentDisplayNumber returns the customer-facing reference for an
// appointment. The database ID remains the authoritative identifier used by
// tools and storage; it must not be included in customer-visible messages.
func appointmentDisplayNumber(appointmentID string) string {
	var b strings.Builder
	for _, r := range appointmentID {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(unicode.ToUpper(r))
			if b.Len() == 8 {
				break
			}
		}
	}
	if b.Len() == 0 {
		return "OB-未知"
	}
	return "OB-" + b.String()
}

// resolveCustomerAppointment accepts either an internal appointment ID from
// trusted conversation history or a customer-facing OB reference. References
// are always resolved after the caller's shop and customer identity are known.
func resolveCustomerAppointment(ctx context.Context, reference, shopID, customerID string) (*storage.Appointment, error) {
	reference = strings.TrimSpace(reference)
	if prefix, ok := appointmentReferencePrefix(reference); ok {
		return storage.GetAppointmentForCustomerPrefix(ctx, prefix, shopID, customerID)
	}
	return storage.GetAppointmentForCustomer(ctx, reference, shopID, customerID)
}

// appointmentReferencePrefix parses both legacy 4-character references and
// current 8-character references. Other values remain exact internal IDs.
func appointmentReferencePrefix(reference string) (string, bool) {
	if len(reference) < 3 || !strings.EqualFold(reference[:3], "OB-") {
		return "", false
	}
	prefix := reference[3:]
	if len(prefix) < 4 || len(prefix) > 8 {
		return "", false
	}
	for _, r := range prefix {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			return "", false
		}
	}
	return strings.ToLower(prefix), true
}
