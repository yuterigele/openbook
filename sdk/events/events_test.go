package events

import (
	"context"
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
	if err := envelope.DecodeData(&data); err != nil {
		t.Fatal(err)
	}
	if data.CustomerID != "customer-1" || data.ServiceID != "service-1" || data.StaffID != "staff-1" {
		t.Fatalf("unexpected event data: %+v", data)
	}
}

func TestSubscriberAndHandlerFunc(t *testing.T) {
	event, err := Unmarshal([]byte(`{"schema_version":"booking.event.v1","event_id":"event-1","type":"booking.cancelled","booking_id":"booking-1","merchant_id":"merchant-1","location_id":"location-1","occurred_at":"2026-08-29T15:00:00+08:00","data":{"customer_id":"customer-1"}}`))
	if err != nil {
		t.Fatal(err)
	}
	called := false
	var subscriber Subscriber = HandlerFunc(func(_ context.Context, got Envelope) error {
		called = got.EventID == event.EventID
		return nil
	})
	if err := subscriber.Handle(context.Background(), event); err != nil || !called {
		t.Fatalf("subscriber was not called: err=%v called=%v", err, called)
	}
	if err := (HandlerFunc)(nil).Handle(context.Background(), event); err == nil {
		t.Fatal("nil handler should be rejected")
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
