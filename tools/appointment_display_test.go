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
		{"UUID", "8a13e9f0-7123-4e6c-9d8b-0123456789ab", "OB-8A13E9F0"},
		{"non UUID test ID", "appointment-123", "OB-APPOINTM"},
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
	if !strings.Contains(got, "📋 预约信息\n预约号：OB-8A13E9F0") {
		t.Fatalf("success message = %q, want plain-text heading and short number", got)
	}
	if strings.Contains(got, appointment.ID) || strings.Contains(got, "**") {
		t.Errorf("success message exposed internal ID or Markdown: %q", got)
	}
}

func TestAppointmentReferencePrefix(t *testing.T) {
	for _, tt := range []struct {
		ref        string
		wantPrefix string
		wantOK     bool
	}{
		{"OB-A4F0", "a4f0", true},
		{"ob-A4F0E91B", "a4f0e91b", true},
		{"OB-A4", "", false},
		{"OB-A4F0-123", "", false},
		{"a4f0e91b-1234", "", false},
	} {
		got, ok := appointmentReferencePrefix(tt.ref)
		if got != tt.wantPrefix || ok != tt.wantOK {
			t.Errorf("appointmentReferencePrefix(%q) = (%q, %t), want (%q, %t)", tt.ref, got, ok, tt.wantPrefix, tt.wantOK)
		}
	}
}
