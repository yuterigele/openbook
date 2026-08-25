package events

import (
	"encoding/json"
	"testing"
	"time"
)

func TestMarshalAndUnmarshalBookingEvent(t *testing.T) {
	wantTime := time.Date(2026, 8, 29, 15, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	payload, err := Marshal("event-1", BookingCreated, "booking-1", "merchant-1", "location-1", BookingCreatedData{
		CustomerID: "customer-1", ServiceID: "service-1", StaffID: "staff-1",
	}, wantTime)
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := Unmarshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if envelope.SchemaVersion != SchemaVersion || envelope.Type != BookingCreated || envelope.BookingID != "booking-1" || !envelope.OccurredAt.Equal(wantTime) {
		t.Fatalf("unexpected envelope: %+v", envelope)
	}
	var data BookingCreatedData
	if err := json.Unmarshal(envelope.Data, &data); err != nil {
		t.Fatal(err)
	}
	if data.CustomerID != "customer-1" || data.ServiceID != "service-1" || data.StaffID != "staff-1" {
		t.Fatalf("unexpected event data: %+v", data)
	}
}

func TestMarshalRejectsUnsupportedOrIncompleteEvent(t *testing.T) {
	now := time.Date(2026, 8, 29, 15, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	if _, err := Marshal("", BookingCreated, "booking-1", "merchant-1", "location-1", nil, now); err == nil {
		t.Fatal("missing event id should be rejected")
	}
	if _, err := Marshal("event-1", Type("booking.unknown"), "booking-1", "merchant-1", "location-1", nil, now); err == nil {
		t.Fatal("unsupported event type should be rejected")
	}
}
