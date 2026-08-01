package tools

import (
	"strings"
	"testing"

	"github.com/yuterigele/openbook/storage"
)

func TestAppointmentDisplayNumber(t *testing.T) {
	tests := []struct {
		name, id, want string
	}{
		{"UUID", "8a13e9f0-7123-4e6c-9d8b-0123456789ab", "OB-8A13"},
		{"non UUID test ID", "appointment-123", "OB-APPO"},
		{"empty", "", "OB-未知"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := appointmentDisplayNumber(tt.id); got != tt.want {
				t.Errorf("appointmentDisplayNumber(%q) = %q, want %q", tt.id, got, tt.want)
			}
		})
	}
}

func TestAppointmentSuccessMessage_HidesInternalIDAndMarkdown(t *testing.T) {
	appointment := storage.Appointment{ID: "8a13e9f0-7123-4e6c-9d8b-0123456789ab", BarberName: "Tony", Customer: "Alice", Date: "2099-01-01", Time: "10:00", Service: "剪发"}
	got := appointmentSuccessMessage(&appointment)
	if !strings.Contains(got, "📋 预约信息\n预约号：OB-8A13") {
		t.Fatalf("success message = %q, want plain-text heading and short number", got)
	}
	if strings.Contains(got, appointment.ID) || strings.Contains(got, "**") {
		t.Errorf("success message exposed internal ID or Markdown: %q", got)
	}
}
