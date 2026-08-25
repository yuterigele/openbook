//go:build mysql_integration && redis_integration

package storage

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/yuterigele/openbook/internal/booking/domain"
	"github.com/yuterigele/openbook/lock"
)

func TestMySQLRedisBookingConcurrencyRehearsal(t *testing.T) {
	if os.Getenv("MYSQL_DSN") == "" && os.Getenv("MYSQL_HOST") == "" {
		t.Skip("设置 MYSQL_DSN 或 MYSQL_HOST 后运行 MySQL/Redis 集成演练")
	}
	if os.Getenv("REDIS_ADDR") == "" {
		t.Setenv("REDIS_ADDR", "127.0.0.1:6379")
	}
	t.Setenv("APP_ENV", "staging")
	t.Setenv("REDIS_REQUIRED", "1")
	t.Setenv("APPOINTMENT_LOCK_TTL", "2s")

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	merchantID := "integration-merchant-" + uuid.NewString()
	locationID := "integration-location-" + uuid.NewString()
	db, err := InitDB(ctx)
	if err != nil {
		t.Fatal(err)
	}
	oldLockClient := lock.Client
	redisClient, err := lock.InitRedis(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = db.Where("merchant_id = ?", merchantID).Delete(&BookingOutboxRecord{}).Error
		_ = db.Where("merchant_id = ?", merchantID).Delete(&BookingAllocationRecord{}).Error
		_ = db.Where("merchant_id = ?", merchantID).Delete(&BookingRecord{}).Error
		_ = db.Where("merchant_id = ?", merchantID).Delete(&OperationOutcomeRecord{}).Error
		_ = redisClient.Close()
		lock.Client = oldLockClient
		if sqlDB, dbErr := db.DB(); dbErr == nil {
			_ = sqlDB.Close()
		}
		DB = nil
	})

	service := domain.Service{
		ID: "integration-service", MerchantID: merchantID, LocationID: locationID, Name: "集成测试服务",
		Duration: time.Hour, BufferBefore: 15 * time.Minute, BufferAfter: 15 * time.Minute,
		ResourceRequirements: []domain.ResourceRequirement{{Kind: "chair", Quantity: 1}}, Active: true,
	}
	staff := domain.Staff{ID: "integration-staff", MerchantID: merchantID, LocationID: locationID, Name: "集成测试员工", Active: true}
	startAt := time.Now().Add(48 * time.Hour).Truncate(time.Second)

	type result struct {
		booking *BookingRecord
		err     error
	}
	results := make(chan result, 8)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < cap(results); i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			customerID := "integration-customer-" + uuid.NewString()
			idempotencyKey := "integration-booking-" + uuid.NewString()
			booking, createErr := domain.NewBooking(
				"integration-booking-"+uuid.NewString(), merchantID, locationID, customerID, idempotencyKey,
				service, staff, startAt,
			)
			if createErr != nil {
				results <- result{err: createErr}
				return
			}
			allocations := []domain.Allocation{
				{ID: uuid.NewString(), BookingID: booking.ID, MerchantID: merchantID, LocationID: locationID, StaffID: staff.ID},
				{ID: uuid.NewString(), BookingID: booking.ID, MerchantID: merchantID, LocationID: locationID, ResourceID: "integration-chair"},
			}
			record, createErr := CreateBookingWithAllocations(ctx, booking, allocations)
			results <- result{booking: record, err: createErr}
		}(i)
	}
	close(start)
	wg.Wait()
	close(results)

	successes := 0
	conflicts := 0
	for item := range results {
		if item.err == nil && item.booking != nil {
			successes++
			continue
		}
		if errors.Is(item.err, ErrBookingConflict) {
			conflicts++
		}
	}
	if successes != 1 {
		t.Fatalf("successful concurrent creates = %d, want exactly 1 (conflicts=%d)", successes, conflicts)
	}

	var bookingCount, allocationCount, outboxCount int64
	if err := db.Model(&BookingRecord{}).Where("merchant_id = ? AND location_id = ?", merchantID, locationID).Count(&bookingCount).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&BookingAllocationRecord{}).Where("merchant_id = ? AND location_id = ?", merchantID, locationID).Count(&allocationCount).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&BookingOutboxRecord{}).Where("merchant_id = ? AND location_id = ?", merchantID, locationID).Count(&outboxCount).Error; err != nil {
		t.Fatal(err)
	}
	if bookingCount != 1 || allocationCount != 2 || outboxCount != 1 {
		t.Fatalf("persisted records = bookings:%d allocations:%d outbox:%d, want 1/2/1", bookingCount, allocationCount, outboxCount)
	}
}
