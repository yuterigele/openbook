package storage

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/yuterigele/openbook/internal/booking/domain"
	"github.com/yuterigele/openbook/sdk/events"
)

func TestCreateBookingWritesVersionedOutboxEvent(t *testing.T) {
	SetupTestDB(t)
	booking, allocations := validPersistedBooking(t, "booking-event", "create-event", "staff-1", "chair-1")
	if _, err := CreateBookingWithAllocations(context.Background(), booking, allocations); err != nil {
		t.Fatal(err)
	}

	var outbox BookingOutboxRecord
	if err := DB.Where("booking_id = ? AND event_type = ?", booking.ID, string(events.BookingCreated)).First(&outbox).Error; err != nil {
		t.Fatal(err)
	}
	envelope, err := events.Unmarshal([]byte(outbox.Payload))
	if err != nil {
		t.Fatalf("outbox payload should be a versioned event: %v", err)
	}
	if outbox.EventID != envelope.EventID || envelope.Type != events.BookingCreated || envelope.BookingID != booking.ID {
		t.Fatalf("outbox and envelope identity differ: outbox=%+v envelope=%+v", outbox, envelope)
	}
	var data events.BookingCreatedData
	if err := json.Unmarshal(envelope.Data, &data); err != nil {
		t.Fatal(err)
	}
	if data.CustomerID != booking.CustomerID || data.ServiceID != booking.ServiceID || data.StaffID != booking.StaffID {
		t.Fatalf("unexpected created event data: %+v", data)
	}
	if booking.Status != domain.BookingPending {
		t.Fatalf("unexpected booking status: %q", booking.Status)
	}
}
