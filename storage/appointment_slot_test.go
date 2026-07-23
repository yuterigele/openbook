package storage

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

func slotTestAppointment(status string) *Appointment {
	return &Appointment{
		ID: uuid.NewString(), ShopID: "shop-slot", BarberID: "barber-slot",
		BarberName: "Tony", CustomerID: uuid.NewString(), Customer: "顾客",
		Date: "2099-01-01", Time: "10:00", Service: "剪发", Status: status,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
}

func TestAppointmentActiveSlotUniqueAndReleasedOnCancel(t *testing.T) {
	SetupTestDB(t)
	first := slotTestAppointment("active")
	if err := DB.Create(first).Error; err != nil {
		t.Fatalf("create first: %v", err)
	}
	if err := DB.Create(slotTestAppointment("active")).Error; err == nil {
		t.Fatal("duplicate active slot should be rejected by unique index")
	}
	if err := DB.Model(&Appointment{}).Where("id = ?", first.ID).Updates(map[string]any{
		"status": "cancelled", "active_slot_key": nil,
	}).Error; err != nil {
		t.Fatalf("cancel first: %v", err)
	}
	if err := DB.Create(slotTestAppointment("active")).Error; err != nil {
		t.Fatalf("slot should be reusable after cancellation: %v", err)
	}
	if err := DB.Create(slotTestAppointment("cancelled")).Error; err != nil {
		t.Fatalf("historical cancelled rows may share a slot: %v", err)
	}
}

func TestUncancelAppointmentRejectsOccupiedSlot(t *testing.T) {
	SetupTestDB(t)
	cancelled := slotTestAppointment("cancelled")
	now := time.Now()
	cancelled.CancelledAt = &now
	if err := DB.Create(cancelled).Error; err != nil {
		t.Fatal(err)
	}
	if err := DB.Create(slotTestAppointment("active")).Error; err != nil {
		t.Fatal(err)
	}
	if err := UncancelAppointment(context.Background(), cancelled.ID); err == nil {
		t.Fatal("uncancel should fail while the slot is occupied")
	}
}

func TestBackfillAppointmentActiveSlotKeysDetectsHistoricalDuplicate(t *testing.T) {
	SetupTestDB(t)
	first := slotTestAppointment("active")
	if err := DB.Create(first).Error; err != nil {
		t.Fatal(err)
	}
	if err := DB.Model(&Appointment{}).Where("id = ?", first.ID).Update("active_slot_key", nil).Error; err != nil {
		t.Fatal(err)
	}
	if err := DB.Create(slotTestAppointment("active")).Error; err != nil {
		t.Fatal(err)
	}
	if err := BackfillAppointmentActiveSlotKeys(context.Background(), DB); err == nil {
		t.Fatal("backfill should reject existing duplicate active slots")
	}
}

func TestCreateAppointmentFullContextConcurrentSameSlot(t *testing.T) {
	SetupTestDB(t)
	shop := MakeShop(t, "shop-concurrent-slot", "")
	MakeBarber(t, "barber-concurrent-slot", shop.ID, "Tony")

	var wg sync.WaitGroup
	var successes int
	var mu sync.Mutex
	for i, phone := range []string{"13800000011", "13800000012"} {
		wg.Add(1)
		go func(n int, p string) {
			defer wg.Done()
			_, err := CreateAppointmentFullContext(context.Background(), shop.ID, "Tony", "顾客", p, "", "", "2099-01-02", "10:00", "剪发")
			if err == nil {
				mu.Lock()
				successes++
				mu.Unlock()
			}
		}(i, phone)
	}
	wg.Wait()
	if successes != 1 {
		t.Fatalf("successful creates = %d, want exactly 1", successes)
	}
	var count int64
	DB.Model(&Appointment{}).Where("barber_id = ? AND date = ? AND time = ? AND status = ?", "barber-concurrent-slot", "2099-01-02", "10:00", "active").Count(&count)
	if count != 1 {
		t.Fatalf("active appointment count = %d, want 1", count)
	}
}
