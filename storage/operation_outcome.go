package storage

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/yuterigele/openbook/internal/booking/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	// ErrOperationOutcomeAlreadyExists 表示同一租户、顾客、操作和幂等键已有结果记录。
	ErrOperationOutcomeAlreadyExists = errors.New("operation outcome already exists")
	// ErrOperationOutcomeStateChanged 表示并发对账已先更新了结果状态。
	ErrOperationOutcomeStateChanged = errors.New("operation outcome state changed")
)

// OperationOutcomeRecord 持久化一次写操作的可对账结果。
// 幂等唯一键同时包含租户、顾客和操作，避免不同门店或不同操作互相碰撞。
type OperationOutcomeRecord struct {
	ID             string    `gorm:"primaryKey;size:64"`
	MerchantID     string    `gorm:"size:64;not null;uniqueIndex:ux_operation_outcome_scope_key,priority:1"`
	LocationID     string    `gorm:"size:64;not null;uniqueIndex:ux_operation_outcome_scope_key,priority:2"`
	CustomerID     string    `gorm:"size:64;not null;uniqueIndex:ux_operation_outcome_scope_key,priority:3"`
	Operation      string    `gorm:"size:64;not null;uniqueIndex:ux_operation_outcome_scope_key,priority:4"`
	IdempotencyKey string    `gorm:"size:128;not null;uniqueIndex:ux_operation_outcome_scope_key,priority:5"`
	BookingID      string    `gorm:"size:64;index"`
	Status         string    `gorm:"size:32;not null;index:idx_operation_outcome_reconcile,priority:1"`
	AttemptCount   int       `gorm:"not null;default:0"`
	LastCheckedAt  time.Time `gorm:"not null;index:idx_operation_outcome_reconcile,priority:2"`
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// CreatePendingOperationOutcome 以唯一幂等键创建 pending 结果记录。
func CreatePendingOperationOutcome(ctx context.Context, outcome domain.OperationOutcome) (*OperationOutcomeRecord, error) {
	if DB == nil {
		return nil, errors.New("DB 未初始化")
	}
	if outcome.Status != domain.OutcomePending {
		return nil, fmt.Errorf("%w: initial outcome must be pending", domain.ErrInvalidOutcome)
	}
	if err := outcome.Validate(); err != nil {
		return nil, err
	}
	if _, err := GetOperationOutcomeByScopeAndKey(ctx, outcome.MerchantID, outcome.LocationID, outcome.CustomerID, outcome.Operation, outcome.IdempotencyKey); err == nil {
		return nil, ErrOperationOutcomeAlreadyExists
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}

	record := operationOutcomeRecordFromDomain(outcome)
	if err := DB.WithContext(ctx).Create(&record).Error; err != nil {
		if isOperationOutcomeDuplicate(err) {
			return nil, ErrOperationOutcomeAlreadyExists
		}
		return nil, err
	}
	return &record, nil
}

// GetOperationOutcomeByScopeAndKey 按完整可信作用域读取结果记录。
func GetOperationOutcomeByScopeAndKey(ctx context.Context, merchantID, locationID, customerID, operation, idempotencyKey string) (*OperationOutcomeRecord, error) {
	if DB == nil {
		return nil, errors.New("DB 未初始化")
	}
	var record OperationOutcomeRecord
	err := DB.WithContext(ctx).
		Where("merchant_id = ? AND location_id = ? AND customer_id = ? AND operation = ? AND idempotency_key = ?", merchantID, locationID, customerID, operation, idempotencyKey).
		First(&record).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, gorm.ErrRecordNotFound
	}
	if err != nil {
		return nil, err
	}
	return &record, nil
}

// TransitionOperationOutcome 使用期望状态做比较并更新结果，避免旧对账任务覆盖新状态。
func TransitionOperationOutcome(ctx context.Context, id string, expected, next domain.OutcomeStatus, bookingID string, checkedAt time.Time) (*OperationOutcomeRecord, error) {
	if DB == nil {
		return nil, errors.New("DB 未初始化")
	}
	if id == "" || expected == "" {
		return nil, domain.ErrInvalidOutcome
	}
	var updated OperationOutcomeRecord
	err := DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var record OperationOutcomeRecord
		if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", id).First(&record).Error; err != nil {
			return err
		}
		if record.Status != string(expected) {
			return ErrOperationOutcomeStateChanged
		}
		outcome, err := record.toDomain()
		if err != nil {
			return err
		}
		if err := outcome.Transition(next, bookingID, checkedAt); err != nil {
			return err
		}
		record.BookingID = outcome.BookingID
		record.Status = string(outcome.Status)
		record.AttemptCount = outcome.AttemptCount
		record.LastCheckedAt = outcome.LastCheckedAt
		updates := map[string]any{
			"booking_id":      record.BookingID,
			"status":          record.Status,
			"attempt_count":   record.AttemptCount,
			"last_checked_at": record.LastCheckedAt,
		}
		result := tx.WithContext(ctx).Model(&OperationOutcomeRecord{}).Where("id = ? AND status = ?", id, expected).Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrOperationOutcomeStateChanged
		}
		updated = record
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &updated, nil
}

// ListOperationOutcomesForReconciliation 只读列出需要对账的结果记录。
// unknown 结果立即进入候选；pending 只有超过等待窗口后才进入候选。
func ListOperationOutcomesForReconciliation(ctx context.Context, now time.Time, pendingTimeout time.Duration, limit int) ([]OperationOutcomeRecord, error) {
	if DB == nil {
		return nil, errors.New("DB 未初始化")
	}
	if now.IsZero() || pendingTimeout <= 0 || limit <= 0 {
		return nil, fmt.Errorf("%w: reconciliation query parameters are invalid", domain.ErrInvalidOutcome)
	}
	cutoff := now.Add(-pendingTimeout)
	records := make([]OperationOutcomeRecord, 0)
	err := DB.WithContext(ctx).
		Where("status = ? OR (status = ? AND last_checked_at <= ?)", domain.OutcomeUnknown, domain.OutcomePending, cutoff).
		Order("last_checked_at ASC, id ASC").
		Limit(limit).
		Find(&records).Error
	if err != nil {
		return nil, err
	}
	return records, nil
}

// OutcomeFinalObservation 是对账器从只读业务查询得到的最终观察结果。
type OutcomeFinalObservation struct {
	Status    domain.OutcomeStatus
	BookingID string
}

// OutcomeFinalReader 只能读取最终状态，不能创建、取消预约或获取业务写锁。
type OutcomeFinalReader func(context.Context, OperationOutcomeRecord) (OutcomeFinalObservation, error)

// OutcomeReconcilePolicy 控制 unknown 结果进入人工处理前的安全等待范围。
type OutcomeReconcilePolicy struct {
	MaxAttempts int
	MaxAge      time.Duration
}

// DefaultOutcomeReconcilePolicy 返回计划约定的默认对账窗口。
func DefaultOutcomeReconcilePolicy() OutcomeReconcilePolicy {
	return OutcomeReconcilePolicy{MaxAttempts: 3, MaxAge: 5 * time.Minute}
}

func (p OutcomeReconcilePolicy) validate() error {
	if p.MaxAttempts <= 0 || p.MaxAge <= 0 {
		return fmt.Errorf("%w: reconciliation policy is invalid", domain.ErrInvalidOutcome)
	}
	return nil
}

// ReconcileOperationOutcome 只读查询最终状态，再 CAS 更新结果记录。
// 业务预约本身不在此函数内写入，也不在此函数内获取预约排期锁。
func ReconcileOperationOutcome(ctx context.Context, id string, policy OutcomeReconcilePolicy, reader OutcomeFinalReader, now time.Time) (*OperationOutcomeRecord, error) {
	if reader == nil || now.IsZero() {
		return nil, fmt.Errorf("%w: reconciliation reader and time are required", domain.ErrInvalidOutcome)
	}
	if err := policy.validate(); err != nil {
		return nil, err
	}
	if DB == nil {
		return nil, errors.New("DB 未初始化")
	}
	var record OperationOutcomeRecord
	if err := DB.WithContext(ctx).Where("id = ?", id).First(&record).Error; err != nil {
		return nil, err
	}
	if record.Status == string(domain.OutcomeConfirmed) || record.Status == string(domain.OutcomeRejected) || record.Status == string(domain.OutcomeNeedsHuman) {
		return &record, nil
	}
	observation, err := reader(ctx, record)
	if err != nil {
		return nil, err
	}
	if observation.Status != domain.OutcomeConfirmed && observation.Status != domain.OutcomeRejected && observation.Status != domain.OutcomeUnknown {
		return nil, fmt.Errorf("%w: final observation status is invalid", domain.ErrInvalidOutcome)
	}
	next := observation.Status
	if observation.Status == domain.OutcomeUnknown && record.Status == string(domain.OutcomeUnknown) {
		attemptAtLimit := record.AttemptCount+1 >= policy.MaxAttempts
		ageAtLimit := !record.CreatedAt.IsZero() && now.Sub(record.CreatedAt) >= policy.MaxAge
		if attemptAtLimit || ageAtLimit {
			next = domain.OutcomeNeedsHuman
		}
	}
	return TransitionOperationOutcome(ctx, record.ID, domain.OutcomeStatus(record.Status), next, observation.BookingID, now)
}

func (r OperationOutcomeRecord) toDomain() (domain.OperationOutcome, error) {
	outcome := domain.OperationOutcome{
		ID:             r.ID,
		MerchantID:     r.MerchantID,
		LocationID:     r.LocationID,
		CustomerID:     r.CustomerID,
		Operation:      r.Operation,
		IdempotencyKey: r.IdempotencyKey,
		BookingID:      r.BookingID,
		Status:         domain.OutcomeStatus(r.Status),
		AttemptCount:   r.AttemptCount,
		LastCheckedAt:  r.LastCheckedAt,
	}
	if err := outcome.Validate(); err != nil {
		return domain.OperationOutcome{}, err
	}
	return outcome, nil
}

func operationOutcomeRecordFromDomain(outcome domain.OperationOutcome) OperationOutcomeRecord {
	checkedAt := outcome.LastCheckedAt
	if checkedAt.IsZero() {
		checkedAt = time.Now()
	}
	return OperationOutcomeRecord{
		ID:             outcome.ID,
		MerchantID:     outcome.MerchantID,
		LocationID:     outcome.LocationID,
		CustomerID:     outcome.CustomerID,
		Operation:      outcome.Operation,
		IdempotencyKey: outcome.IdempotencyKey,
		BookingID:      outcome.BookingID,
		Status:         string(outcome.Status),
		AttemptCount:   outcome.AttemptCount,
		LastCheckedAt:  checkedAt,
	}
}

func isOperationOutcomeDuplicate(err error) bool {
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "unique") || strings.Contains(message, "duplicate")
}
