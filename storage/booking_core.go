package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/yuterigele/openbook/internal/booking/domain"
	"github.com/yuterigele/openbook/lock"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	// ErrBookingConflict 表示员工或独占资源在候选区间内已被占用。
	ErrBookingConflict = errors.New("booking conflict")
	// ErrBookingIdempotencyConflict 表示同一幂等键对应了不同业务请求。
	ErrBookingIdempotencyConflict = errors.New("booking idempotency conflict")
	// ErrBookingOutcomeUnknown 表示事务提交后无法完成最终记录核验。
	ErrBookingOutcomeUnknown = errors.New("booking outcome unknown")
	// ErrBookingNotCancellable 表示预约当前状态不允许顾客取消。
	ErrBookingNotCancellable = errors.New("booking is not cancellable")
)

// BookingRecord 是通用预约内核的主记录，不替代旧 appointments 表。
type BookingRecord struct {
	ID             string    `gorm:"primaryKey;size:64"`
	MerchantID     string    `gorm:"size:64;not null;index;uniqueIndex:ux_booking_scope_key,priority:1"`
	LocationID     string    `gorm:"size:64;not null;index;uniqueIndex:ux_booking_scope_key,priority:2"`
	CustomerID     string    `gorm:"size:64;not null;index;uniqueIndex:ux_booking_scope_key,priority:3"`
	ServiceID      string    `gorm:"size:64;not null"`
	StaffID        string    `gorm:"size:64;not null;index"`
	StartAt        time.Time `gorm:"not null;index"`
	EndAt          time.Time `gorm:"not null;index"`
	Status         string    `gorm:"size:32;not null;index"`
	IdempotencyKey string    `gorm:"size:128;not null;uniqueIndex:ux_booking_scope_key,priority:4"`
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// BookingAllocationRecord 记录预约对员工或资源的占用。
type BookingAllocationRecord struct {
	ID         string `gorm:"primaryKey;size:64"`
	BookingID  string `gorm:"size:64;not null;index"`
	MerchantID string `gorm:"size:64;not null;index"`
	LocationID string `gorm:"size:64;not null;index"`
	StaffID    string `gorm:"size:64;index"`
	ResourceID string `gorm:"size:64;index"`
	CreatedAt  time.Time
}

// BookingOutboxRecord 与 BookingRecord 在同一事务内写入，供提交后异步发布。
type BookingOutboxRecord struct {
	ID          string `gorm:"primaryKey;size:64"`
	EventID     string `gorm:"size:64;not null;uniqueIndex"`
	BookingID   string `gorm:"size:64;not null;index"`
	MerchantID  string `gorm:"size:64;not null;index"`
	LocationID  string `gorm:"size:64;not null;index"`
	EventType   string `gorm:"size:64;not null"`
	Payload     string `gorm:"type:text;not null"`
	Status      string `gorm:"size:32;not null;index"`
	CreatedAt   time.Time
	PublishedAt *time.Time
}

// CreateBookingWithAllocations 在一个事务内重查冲突、写入预约、占用和 Outbox。
// 调用方应先用 domain.Booking.ValidateAllocations 校验服务资源需求。
func CreateBookingWithAllocations(ctx context.Context, booking domain.Booking, allocations []domain.Allocation) (*BookingRecord, error) {
	if DB == nil {
		return nil, errors.New("DB 未初始化")
	}
	if err := booking.Validate(); err != nil {
		return nil, err
	}
	if err := validatePersistedAllocations(booking, allocations); err != nil {
		return nil, err
	}

	var created BookingRecord
	idempotentReplay := false
	err := withBookingLocks(ctx, bookingLockKeys(booking, allocations), func(operationCtx context.Context) error {
		err := DB.WithContext(operationCtx).Transaction(func(tx *gorm.DB) error {
			var existing BookingRecord
			lookupErr := tx.WithContext(operationCtx).
				Where("merchant_id = ? AND location_id = ? AND customer_id = ? AND idempotency_key = ?", booking.MerchantID, booking.LocationID, booking.CustomerID, booking.IdempotencyKey).
				First(&existing).Error
			if lookupErr == nil {
				if !sameBookingRequest(existing, booking) {
					return ErrBookingIdempotencyConflict
				}
				created = existing
				idempotentReplay = true
				return nil
			}
			if !errors.Is(lookupErr, gorm.ErrRecordNotFound) {
				return lookupErr
			}

			occupied, err := lockedOccupiedBookings(operationCtx, tx, booking)
			if err != nil {
				return err
			}
			candidate, err := domain.NewOccupancy(booking, persistedResourceIDs(allocations))
			if err != nil {
				return err
			}
			conflicts, err := domain.FindConflicts(candidate, occupied)
			if err != nil {
				return err
			}
			if len(conflicts) > 0 {
				return ErrBookingConflict
			}

			created = bookingRecordFromDomain(booking)
			if err := tx.WithContext(operationCtx).Create(&created).Error; err != nil {
				return err
			}
			for _, allocation := range allocations {
				record := BookingAllocationRecord{
					ID: allocation.ID, BookingID: allocation.BookingID, MerchantID: allocation.MerchantID,
					LocationID: allocation.LocationID, StaffID: allocation.StaffID, ResourceID: allocation.ResourceID,
				}
				if err := tx.WithContext(operationCtx).Create(&record).Error; err != nil {
					return err
				}
			}
			payload, err := json.Marshal(map[string]string{
				"booking_id": created.ID, "merchant_id": created.MerchantID, "location_id": created.LocationID,
				"customer_id": created.CustomerID, "service_id": created.ServiceID, "staff_id": created.StaffID,
			})
			if err != nil {
				return err
			}
			return tx.WithContext(operationCtx).Create(&BookingOutboxRecord{
				ID: uuid.NewString(), EventID: uuid.NewString(), BookingID: created.ID,
				MerchantID: created.MerchantID, LocationID: created.LocationID, EventType: "booking.created",
				Payload: string(payload), Status: "pending", CreatedAt: time.Now(),
			}).Error
		})
		if err != nil {
			return err
		}
		if idempotentReplay {
			return nil
		}
		verified, err := GetBookingForCustomer(operationCtx, booking.MerchantID, booking.LocationID, booking.CustomerID, created.ID)
		if err != nil || !sameBookingRequest(*verified, booking) || verified.Status != string(domain.BookingPending) {
			return ErrBookingOutcomeUnknown
		}
		created = *verified
		return nil
	})
	if err != nil {
		return nil, err
	}
	if idempotentReplay {
		return &created, nil
	}
	return &created, nil
}

// GetBookingForCustomer 按商户、门店和顾客读取预约主记录，防止跨顾客访问。
func GetBookingForCustomer(ctx context.Context, merchantID, locationID, customerID, bookingID string) (*BookingRecord, error) {
	if DB == nil {
		return nil, errors.New("DB 未初始化")
	}
	var record BookingRecord
	err := DB.WithContext(ctx).Where("id = ? AND merchant_id = ? AND location_id = ? AND customer_id = ?", bookingID, merchantID, locationID, customerID).First(&record).Error
	if err != nil {
		return nil, err
	}
	return &record, nil
}

// ListBookingsForCustomer 读取顾客在当前门店的未来有效预约。
func ListBookingsForCustomer(ctx context.Context, merchantID, locationID, customerID string, now time.Time, limit int) ([]BookingRecord, error) {
	if DB == nil {
		return nil, errors.New("DB 未初始化")
	}
	if merchantID == "" || locationID == "" || customerID == "" || now.IsZero() || limit <= 0 {
		return nil, domain.ErrInvalidOutcome
	}
	records := make([]BookingRecord, 0)
	err := DB.WithContext(ctx).
		Where("merchant_id = ? AND location_id = ? AND customer_id = ? AND status IN ? AND end_at > ?", merchantID, locationID, customerID, []string{string(domain.BookingPending), string(domain.BookingConfirmed)}, now).
		Order("start_at ASC, id ASC").Limit(limit).Find(&records).Error
	if err != nil {
		return nil, err
	}
	return records, nil
}

// CancelBookingForCustomer 在事务内取消属于可信顾客的预约，并记录幂等结果。
func CancelBookingForCustomer(ctx context.Context, merchantID, locationID, customerID, bookingID, idempotencyKey string) (*BookingRecord, error) {
	if DB == nil {
		return nil, errors.New("DB 未初始化")
	}
	if merchantID == "" || locationID == "" || customerID == "" || bookingID == "" || idempotencyKey == "" {
		return nil, domain.ErrInvalidOutcome
	}
	now := time.Now()
	outcome, err := domain.NewPendingOutcome(uuid.NewString(), merchantID, locationID, customerID, "cancel_booking", idempotencyKey, now)
	if err != nil {
		return nil, err
	}
	outcomeRecord, err := CreatePendingOperationOutcome(ctx, outcome)
	if err != nil {
		if !errors.Is(err, ErrOperationOutcomeAlreadyExists) {
			return nil, err
		}
		return replayCancelledOutcome(ctx, merchantID, locationID, customerID, idempotencyKey)
	}
	lockKeys, err := loadBookingLockKeys(ctx, merchantID, locationID, customerID, bookingID)
	if err != nil {
		if transitionErr := transitionCancellationFailure(ctx, outcomeRecord.ID, err, now); transitionErr != nil {
			return nil, transitionErr
		}
		return nil, err
	}

	var cancelled BookingRecord
	err = withBookingLocks(ctx, lockKeys, func(operationCtx context.Context) error {
		err := DB.WithContext(operationCtx).Transaction(func(tx *gorm.DB) error {
			if err := tx.WithContext(operationCtx).Clauses(clause.Locking{Strength: "UPDATE"}).
				Where("id = ? AND merchant_id = ? AND location_id = ? AND customer_id = ?", bookingID, merchantID, locationID, customerID).
				First(&cancelled).Error; err != nil {
				return err
			}
			if cancelled.Status != string(domain.BookingPending) && cancelled.Status != string(domain.BookingConfirmed) {
				return ErrBookingNotCancellable
			}
			result := tx.WithContext(operationCtx).Model(&BookingRecord{}).
				Where("id = ? AND merchant_id = ? AND location_id = ? AND customer_id = ? AND status IN ?", bookingID, merchantID, locationID, customerID, []string{string(domain.BookingPending), string(domain.BookingConfirmed)}).
				Updates(map[string]any{"status": string(domain.BookingCancelled), "updated_at": time.Now()})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return ErrBookingNotCancellable
			}
			cancelled.Status = string(domain.BookingCancelled)
			payload, err := json.Marshal(map[string]string{
				"booking_id": bookingID, "merchant_id": merchantID, "location_id": locationID, "customer_id": customerID,
			})
			if err != nil {
				return err
			}
			return tx.WithContext(operationCtx).Create(&BookingOutboxRecord{
				ID: uuid.NewString(), EventID: uuid.NewString(), BookingID: bookingID,
				MerchantID: merchantID, LocationID: locationID, EventType: "booking.cancelled",
				Payload: string(payload), Status: "pending", CreatedAt: time.Now(),
			}).Error
		})
		if err != nil {
			return err
		}
		if _, err := TransitionOperationOutcome(operationCtx, outcomeRecord.ID, domain.OutcomePending, domain.OutcomeConfirmed, bookingID, time.Now()); err != nil {
			return ErrBookingOutcomeUnknown
		}
		verified, err := GetBookingForCustomer(operationCtx, merchantID, locationID, customerID, bookingID)
		if err != nil || verified.Status != string(domain.BookingCancelled) {
			return ErrBookingOutcomeUnknown
		}
		cancelled = *verified
		return nil
	})
	if err != nil {
		if errors.Is(err, ErrBookingOutcomeUnknown) || errors.Is(err, lock.ErrRedisUnavailable) {
			return nil, err
		}
		if transitionErr := transitionCancellationFailure(ctx, outcomeRecord.ID, err, now); transitionErr != nil {
			return nil, transitionErr
		}
		return nil, err
	}
	return &cancelled, nil
}

// RescheduleBookingForCustomer 在同一事务内创建新预约并取消旧预约。
// 冲突、权限或任一步写入失败时，旧预约保持原状态。
func RescheduleBookingForCustomer(ctx context.Context, merchantID, locationID, customerID, oldBookingID, idempotencyKey string, replacement domain.Booking, allocations []domain.Allocation) (*BookingRecord, error) {
	if DB == nil {
		return nil, errors.New("DB 未初始化")
	}
	if merchantID == "" || locationID == "" || customerID == "" || oldBookingID == "" || idempotencyKey == "" || replacement.ID == oldBookingID {
		return nil, domain.ErrInvalidOutcome
	}
	if replacement.MerchantID != merchantID || replacement.LocationID != locationID || replacement.CustomerID != customerID {
		return nil, domain.ErrTenantMismatch
	}
	if err := replacement.Validate(); err != nil {
		return nil, err
	}
	if err := validatePersistedAllocations(replacement, allocations); err != nil {
		return nil, err
	}
	outcome, err := domain.NewPendingOutcome(uuid.NewString(), merchantID, locationID, customerID, "reschedule_booking", idempotencyKey, time.Now())
	if err != nil {
		return nil, err
	}
	outcomeRecord, err := CreatePendingOperationOutcome(ctx, outcome)
	if err != nil {
		if !errors.Is(err, ErrOperationOutcomeAlreadyExists) {
			return nil, err
		}
		return replayRescheduledOutcome(ctx, merchantID, locationID, customerID, idempotencyKey)
	}
	oldLockKeys, err := loadBookingLockKeys(ctx, merchantID, locationID, customerID, oldBookingID)
	if err != nil {
		if transitionErr := transitionCancellationFailure(ctx, outcomeRecord.ID, err, time.Now()); transitionErr != nil {
			return nil, transitionErr
		}
		return nil, err
	}
	lockKeys := append(oldLockKeys, bookingLockKeys(replacement, allocations)...)

	var created BookingRecord
	err = withBookingLocks(ctx, lockKeys, func(operationCtx context.Context) error {
		err := DB.WithContext(operationCtx).Transaction(func(tx *gorm.DB) error {
			var old BookingRecord
			if err := tx.WithContext(operationCtx).Clauses(clause.Locking{Strength: "UPDATE"}).
				Where("id = ? AND merchant_id = ? AND location_id = ? AND customer_id = ?", oldBookingID, merchantID, locationID, customerID).
				First(&old).Error; err != nil {
				return err
			}
			if old.Status != string(domain.BookingPending) && old.Status != string(domain.BookingConfirmed) {
				return ErrBookingNotCancellable
			}
			occupied, err := lockedOccupiedBookings(operationCtx, tx, replacement)
			if err != nil {
				return err
			}
			filtered := make([]domain.Occupancy, 0, len(occupied))
			for _, item := range occupied {
				if item.BookingID != oldBookingID {
					filtered = append(filtered, item)
				}
			}
			candidate, err := domain.NewOccupancy(replacement, persistedResourceIDs(allocations))
			if err != nil {
				return err
			}
			conflicts, err := domain.FindConflicts(candidate, filtered)
			if err != nil {
				return err
			}
			if len(conflicts) > 0 {
				return ErrBookingConflict
			}

			result := tx.WithContext(operationCtx).Model(&BookingRecord{}).
				Where("id = ? AND merchant_id = ? AND location_id = ? AND customer_id = ? AND status IN ?", oldBookingID, merchantID, locationID, customerID, []string{string(domain.BookingPending), string(domain.BookingConfirmed)}).
				Updates(map[string]any{"status": string(domain.BookingCancelled), "updated_at": time.Now()})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return ErrBookingNotCancellable
			}

			created = bookingRecordFromDomain(replacement)
			if err := tx.WithContext(operationCtx).Create(&created).Error; err != nil {
				return err
			}
			for _, allocation := range allocations {
				if err := tx.WithContext(operationCtx).Create(&BookingAllocationRecord{
					ID: allocation.ID, BookingID: allocation.BookingID, MerchantID: allocation.MerchantID,
					LocationID: allocation.LocationID, StaffID: allocation.StaffID, ResourceID: allocation.ResourceID,
				}).Error; err != nil {
					return err
				}
			}
			payload, err := json.Marshal(map[string]string{
				"old_booking_id": oldBookingID, "new_booking_id": created.ID, "merchant_id": merchantID,
				"location_id": locationID, "customer_id": customerID,
			})
			if err != nil {
				return err
			}
			return tx.WithContext(operationCtx).Create(&BookingOutboxRecord{
				ID: uuid.NewString(), EventID: uuid.NewString(), BookingID: created.ID,
				MerchantID: merchantID, LocationID: locationID, EventType: "booking.rescheduled",
				Payload: string(payload), Status: "pending", CreatedAt: time.Now(),
			}).Error
		})
		if err != nil {
			return err
		}
		if _, err := TransitionOperationOutcome(operationCtx, outcomeRecord.ID, domain.OutcomePending, domain.OutcomeConfirmed, created.ID, time.Now()); err != nil {
			return ErrBookingOutcomeUnknown
		}
		verified, err := GetBookingForCustomer(operationCtx, merchantID, locationID, customerID, created.ID)
		if err != nil || verified.Status != string(domain.BookingPending) {
			return ErrBookingOutcomeUnknown
		}
		old, err := GetBookingForCustomer(operationCtx, merchantID, locationID, customerID, oldBookingID)
		if err != nil || old.Status != string(domain.BookingCancelled) {
			return ErrBookingOutcomeUnknown
		}
		created = *verified
		return nil
	})
	if err != nil {
		if errors.Is(err, ErrBookingOutcomeUnknown) || errors.Is(err, lock.ErrRedisUnavailable) {
			return nil, err
		}
		if transitionErr := transitionCancellationFailure(ctx, outcomeRecord.ID, err, time.Now()); transitionErr != nil {
			return nil, transitionErr
		}
		return nil, err
	}
	return &created, nil
}

func replayCancelledOutcome(ctx context.Context, merchantID, locationID, customerID, idempotencyKey string) (*BookingRecord, error) {
	record, err := GetOperationOutcomeByScopeAndKey(ctx, merchantID, locationID, customerID, "cancel_booking", idempotencyKey)
	if err != nil {
		return nil, err
	}
	switch domain.OutcomeStatus(record.Status) {
	case domain.OutcomeConfirmed:
		return GetBookingForCustomer(ctx, merchantID, locationID, customerID, record.BookingID)
	case domain.OutcomeRejected:
		return nil, ErrBookingNotCancellable
	default:
		return nil, ErrBookingOutcomeUnknown
	}
}

func replayRescheduledOutcome(ctx context.Context, merchantID, locationID, customerID, idempotencyKey string) (*BookingRecord, error) {
	record, err := GetOperationOutcomeByScopeAndKey(ctx, merchantID, locationID, customerID, "reschedule_booking", idempotencyKey)
	if err != nil {
		return nil, err
	}
	switch domain.OutcomeStatus(record.Status) {
	case domain.OutcomeConfirmed:
		return GetBookingForCustomer(ctx, merchantID, locationID, customerID, record.BookingID)
	case domain.OutcomeRejected:
		return nil, ErrBookingConflict
	default:
		return nil, ErrBookingOutcomeUnknown
	}
}

func transitionCancellationFailure(ctx context.Context, outcomeID string, cause error, checkedAt time.Time) error {
	if _, err := TransitionOperationOutcome(ctx, outcomeID, domain.OutcomePending, domain.OutcomeRejected, "", checkedAt); err != nil {
		return ErrBookingOutcomeUnknown
	}
	return cause
}

func lockedOccupiedBookings(ctx context.Context, tx *gorm.DB, candidate domain.Booking) ([]domain.Occupancy, error) {
	var records []BookingRecord
	err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("merchant_id = ? AND location_id = ? AND status IN ? AND start_at < ? AND end_at > ?",
			candidate.MerchantID, candidate.LocationID,
			[]string{string(domain.BookingPending), string(domain.BookingConfirmed)}, candidate.EndAt, candidate.StartAt).
		Order("id ASC").Find(&records).Error
	if err != nil {
		return nil, err
	}
	occupancies := make([]domain.Occupancy, 0, len(records))
	for _, record := range records {
		var allocations []BookingAllocationRecord
		if err := tx.WithContext(ctx).Where("booking_id = ? AND merchant_id = ? AND location_id = ?", record.ID, record.MerchantID, record.LocationID).Find(&allocations).Error; err != nil {
			return nil, err
		}
		resourceIDs := make([]string, 0, len(allocations))
		for _, allocation := range allocations {
			if allocation.ResourceID != "" {
				resourceIDs = append(resourceIDs, allocation.ResourceID)
			}
		}
		occupancy, err := domain.NewOccupancy(bookingRecordToDomain(record), resourceIDs)
		if err != nil {
			return nil, err
		}
		occupancies = append(occupancies, occupancy)
	}
	return occupancies, nil
}

func validatePersistedAllocations(booking domain.Booking, allocations []domain.Allocation) error {
	if len(allocations) == 0 {
		return fmt.Errorf("%w: booking requires allocations", domain.ErrInvalidEntity)
	}
	staffCount := 0
	seenResources := make(map[string]struct{})
	seenAllocations := make(map[string]struct{}, len(allocations))
	for _, allocation := range allocations {
		if err := allocation.Validate(); err != nil {
			return err
		}
		if allocation.BookingID != booking.ID || allocation.MerchantID != booking.MerchantID || allocation.LocationID != booking.LocationID {
			return domain.ErrTenantMismatch
		}
		if _, exists := seenAllocations[allocation.ID]; exists {
			return fmt.Errorf("%w: duplicate allocation id", domain.ErrInvalidEntity)
		}
		seenAllocations[allocation.ID] = struct{}{}
		if allocation.StaffID != "" {
			if allocation.ResourceID != "" || allocation.StaffID != booking.StaffID {
				return fmt.Errorf("%w: staff allocation does not match booking", domain.ErrInvalidEntity)
			}
			staffCount++
			continue
		}
		if _, exists := seenResources[allocation.ResourceID]; exists {
			return fmt.Errorf("%w: duplicate resource allocation", domain.ErrInvalidEntity)
		}
		seenResources[allocation.ResourceID] = struct{}{}
	}
	if staffCount != 1 {
		return fmt.Errorf("%w: booking must have one staff allocation", domain.ErrInvalidEntity)
	}
	return nil
}

func persistedResourceIDs(allocations []domain.Allocation) []string {
	ids := make([]string, 0, len(allocations))
	for _, allocation := range allocations {
		if allocation.ResourceID != "" {
			ids = append(ids, allocation.ResourceID)
		}
	}
	return ids
}

func bookingRecordFromDomain(booking domain.Booking) BookingRecord {
	return BookingRecord{
		ID: booking.ID, MerchantID: booking.MerchantID, LocationID: booking.LocationID, CustomerID: booking.CustomerID,
		ServiceID: booking.ServiceID, StaffID: booking.StaffID, StartAt: booking.StartAt, EndAt: booking.EndAt,
		Status: string(booking.Status), IdempotencyKey: booking.IdempotencyKey,
	}
}

func bookingRecordToDomain(record BookingRecord) domain.Booking {
	return domain.Booking{
		ID: record.ID, MerchantID: record.MerchantID, LocationID: record.LocationID, CustomerID: record.CustomerID,
		ServiceID: record.ServiceID, StaffID: record.StaffID, StartAt: record.StartAt, EndAt: record.EndAt,
		Status: domain.BookingStatus(record.Status), IdempotencyKey: record.IdempotencyKey,
	}
}

func sameBookingRequest(record BookingRecord, booking domain.Booking) bool {
	return record.MerchantID == booking.MerchantID && record.LocationID == booking.LocationID &&
		record.CustomerID == booking.CustomerID && record.ServiceID == booking.ServiceID &&
		record.StaffID == booking.StaffID && record.StartAt.Equal(booking.StartAt) && record.EndAt.Equal(booking.EndAt)
}
