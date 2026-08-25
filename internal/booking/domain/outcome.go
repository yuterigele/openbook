package domain

import (
	"errors"
	"fmt"
	"time"
)

var (
	// ErrInvalidOutcome 表示操作结果记录缺少幂等和租户字段。
	ErrInvalidOutcome = errors.New("invalid booking outcome")
	// ErrInvalidOutcomeTransition 表示结果状态不能按该方向迁移。
	ErrInvalidOutcomeTransition = errors.New("invalid booking outcome transition")
)

// OutcomeStatus 是写操作结果的持久化状态。
type OutcomeStatus string

const (
	OutcomePending    OutcomeStatus = "pending"
	OutcomeConfirmed  OutcomeStatus = "confirmed"
	OutcomeRejected   OutcomeStatus = "rejected"
	OutcomeUnknown    OutcomeStatus = "unknown"
	OutcomeNeedsHuman OutcomeStatus = "needs_human"
)

// OperationOutcome 记录一次带幂等键的写操作最终确认状态。
type OperationOutcome struct {
	ID             string
	MerchantID     string
	LocationID     string
	CustomerID     string
	Operation      string
	IdempotencyKey string
	BookingID      string
	Status         OutcomeStatus
	AttemptCount   int
	LastCheckedAt  time.Time
}

// NewPendingOutcome 创建尚未获得最终结果的操作记录。
func NewPendingOutcome(id, merchantID, locationID, customerID, operation, idempotencyKey string, now time.Time) (OperationOutcome, error) {
	result := OperationOutcome{
		ID:             id,
		MerchantID:     merchantID,
		LocationID:     locationID,
		CustomerID:     customerID,
		Operation:      operation,
		IdempotencyKey: idempotencyKey,
		Status:         OutcomePending,
		LastCheckedAt:  now,
	}
	if err := result.Validate(); err != nil {
		return OperationOutcome{}, err
	}
	return result, nil
}

// Validate 校验操作结果的租户、顾客和幂等身份。
func (o OperationOutcome) Validate() error {
	if err := validateScope(o.MerchantID, o.LocationID); err != nil {
		return err
	}
	if o.ID == "" || o.CustomerID == "" || o.Operation == "" || o.IdempotencyKey == "" {
		return ErrInvalidOutcome
	}
	if o.AttemptCount < 0 || !validOutcomeStatus(o.Status) {
		return ErrInvalidOutcome
	}
	if o.Status == OutcomeConfirmed && o.BookingID == "" {
		return fmt.Errorf("%w: confirmed outcome requires booking id", ErrInvalidOutcome)
	}
	return nil
}

// Transition 更新结果状态，并记录一次对账检查时间。
// 同状态迁移是幂等的；终态不能转回 pending 或 unknown。
func (o *OperationOutcome) Transition(next OutcomeStatus, bookingID string, checkedAt time.Time) error {
	if o == nil {
		return ErrInvalidOutcome
	}
	if err := o.Validate(); err != nil {
		return err
	}
	if !validOutcomeStatus(next) || !allowedOutcomeTransition(o.Status, next) {
		return fmt.Errorf("%w: %s -> %s", ErrInvalidOutcomeTransition, o.Status, next)
	}
	if next == OutcomeConfirmed && bookingID == "" && o.BookingID == "" {
		return fmt.Errorf("%w: confirmed outcome requires booking id", ErrInvalidOutcome)
	}
	o.Status = next
	if bookingID != "" {
		o.BookingID = bookingID
	}
	if !checkedAt.IsZero() {
		o.LastCheckedAt = checkedAt
	}
	o.AttemptCount++
	return o.Validate()
}

func validOutcomeStatus(status OutcomeStatus) bool {
	switch status {
	case OutcomePending, OutcomeConfirmed, OutcomeRejected, OutcomeUnknown, OutcomeNeedsHuman:
		return true
	default:
		return false
	}
}

func allowedOutcomeTransition(current, next OutcomeStatus) bool {
	if current == next {
		return true
	}
	switch current {
	case OutcomePending:
		return next == OutcomeConfirmed || next == OutcomeRejected || next == OutcomeUnknown
	case OutcomeUnknown:
		return next == OutcomeConfirmed || next == OutcomeRejected || next == OutcomeNeedsHuman
	default:
		return false
	}
}
