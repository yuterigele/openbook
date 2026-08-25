package storage

import (
	"context"
	"errors"
	"fmt"

	"github.com/yuterigele/openbook/internal/booking/domain"
	"github.com/yuterigele/openbook/lock"
)

// bookingLockKeys 返回本次预约涉及的员工和资源锁，并由锁包统一排序去重。
func bookingLockKeys(booking domain.Booking, allocations []domain.Allocation) []string {
	keys := []string{bookingLockKey(booking.MerchantID, booking.LocationID, "staff", booking.StaffID)}
	for _, allocation := range allocations {
		if allocation.ResourceID != "" {
			keys = append(keys, bookingLockKey(booking.MerchantID, booking.LocationID, "resource", allocation.ResourceID))
		}
	}
	return lock.OrderedLockKeys(keys)
}

func bookingRecordLockKeys(record BookingRecord, allocations []BookingAllocationRecord) []string {
	keys := []string{bookingLockKey(record.MerchantID, record.LocationID, "staff", record.StaffID)}
	for _, allocation := range allocations {
		if allocation.ResourceID != "" {
			keys = append(keys, bookingLockKey(record.MerchantID, record.LocationID, "resource", allocation.ResourceID))
		}
	}
	return lock.OrderedLockKeys(keys)
}

func bookingLockKey(merchantID, locationID, kind, id string) string {
	return fmt.Sprintf("lock:booking:%s:%s:%s:%s", merchantID, locationID, kind, id)
}

// withBookingLocks 在业务事务和提交后核验期间持有全部预约资源锁。
func withBookingLocks(ctx context.Context, keys []string, fn func(context.Context) error) error {
	set, err := lock.AcquireOrderedLocks(ctx, keys)
	if err != nil {
		return err
	}
	guardedCtx, cancel := set.GuardContext(ctx)
	operationErr := fn(guardedCtx)
	lockErr := set.Err()
	unlockErr := set.Unlock(context.Background())
	cancel()
	if lockErr != nil || unlockErr != nil {
		if operationErr == nil || errors.Is(operationErr, context.Canceled) || errors.Is(operationErr, context.DeadlineExceeded) {
			return ErrBookingOutcomeUnknown
		}
	}
	return operationErr
}
