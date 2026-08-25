package domain

import (
	"errors"
	"fmt"
	"sort"
	"time"
)

var (
	// ErrInvalidSlot 表示预约开始时间不符合起约粒度。
	ErrInvalidSlot = errors.New("invalid booking slot")
	// ErrTooSoon 表示预约时间早于最早提前时间。
	ErrTooSoon = errors.New("booking is too soon")
	// ErrTooFar 表示预约时间超过最远可约日期。
	ErrTooFar = errors.New("booking is too far")
)

// Occupancy 是参与排期冲突判断的员工和资源占用快照。
type Occupancy struct {
	BookingID   string
	MerchantID  string
	LocationID  string
	Interval    Interval
	Status      BookingStatus
	StaffID     string
	ResourceIDs []string
}

// NewOccupancy 从预约和已分配资源创建排期快照。
func NewOccupancy(booking Booking, resourceIDs []string) (Occupancy, error) {
	if err := booking.Validate(); err != nil {
		return Occupancy{}, err
	}
	interval, err := NewInterval(booking.StartAt, booking.EndAt)
	if err != nil {
		return Occupancy{}, err
	}
	if err := validateUniqueIDs(resourceIDs); err != nil {
		return Occupancy{}, err
	}
	return Occupancy{
		BookingID:   booking.ID,
		MerchantID:  booking.MerchantID,
		LocationID:  booking.LocationID,
		Interval:    interval,
		Status:      booking.Status,
		StaffID:     booking.StaffID,
		ResourceIDs: append([]string(nil), resourceIDs...),
	}, nil
}

// BlocksAvailability 表示该快照是否应阻止新的预约。
func (o Occupancy) BlocksAvailability() bool {
	return o.Status == BookingPending || o.Status == BookingConfirmed
}

// ConflictKind 标识冲突来自员工还是独占资源。
type ConflictKind string

const (
	ConflictStaff    ConflictKind = "staff"
	ConflictResource ConflictKind = "resource"
)

// Conflict 描述一个确定性排期冲突。
type Conflict struct {
	BookingID  string
	Kind       ConflictKind
	ResourceID string
}

// FindConflicts 在同一商户和门店内查找员工/资源冲突。
func FindConflicts(candidate Occupancy, occupied []Occupancy) ([]Conflict, error) {
	if err := candidate.Validate(); err != nil {
		return nil, err
	}
	if !candidate.BlocksAvailability() {
		return nil, nil
	}
	conflicts := make([]Conflict, 0)
	for _, existing := range occupied {
		if err := existing.Validate(); err != nil {
			return nil, err
		}
		if err := ensureSameScope(candidate.MerchantID, candidate.LocationID, existing.MerchantID, existing.LocationID); err != nil {
			return nil, err
		}
		if existing.BookingID == candidate.BookingID || !existing.BlocksAvailability() || !candidate.Interval.Overlaps(existing.Interval) {
			continue
		}
		if candidate.StaffID == existing.StaffID {
			conflicts = append(conflicts, Conflict{BookingID: existing.BookingID, Kind: ConflictStaff})
		}
		for _, resourceID := range candidate.ResourceIDs {
			if containsID(existing.ResourceIDs, resourceID) {
				conflicts = append(conflicts, Conflict{BookingID: existing.BookingID, Kind: ConflictResource, ResourceID: resourceID})
			}
		}
	}
	sort.Slice(conflicts, func(i, j int) bool {
		if conflicts[i].BookingID != conflicts[j].BookingID {
			return conflicts[i].BookingID < conflicts[j].BookingID
		}
		if conflicts[i].Kind != conflicts[j].Kind {
			return conflicts[i].Kind < conflicts[j].Kind
		}
		return conflicts[i].ResourceID < conflicts[j].ResourceID
	})
	return conflicts, nil
}

// ValidateStartAt 校验开始时间是否落在策略规定的起约粒度上。
func ValidateStartAt(startAt time.Time, policy BookingPolicy) error {
	if err := policy.Validate(); err != nil {
		return err
	}
	if startAt.IsZero() {
		return ErrInvalidSlot
	}
	local := startAt.In(policy.Location)
	if local.Second() != 0 || local.Nanosecond() != 0 || time.Duration(local.Minute())*time.Minute%policy.SlotGranularity != 0 {
		return ErrInvalidSlot
	}
	return nil
}

// ValidateBookingWindow 校验预约时间相对于当前时刻的提前量。
func ValidateBookingWindow(startAt, now time.Time, policy BookingPolicy) error {
	if err := ValidateStartAt(startAt, policy); err != nil {
		return err
	}
	if now.IsZero() {
		return fmt.Errorf("%w: current time is required", ErrInvalidEntity)
	}
	start := startAt.In(policy.Location)
	current := now.In(policy.Location)
	if start.Before(current.Add(policy.MinAdvance)) {
		return ErrTooSoon
	}
	if start.After(current.Add(policy.MaxAdvance)) {
		return ErrTooFar
	}
	return nil
}

func (o Occupancy) Validate() error {
	if err := validateScope(o.MerchantID, o.LocationID); err != nil {
		return err
	}
	if o.BookingID == "" || o.StaffID == "" || !validBookingStatus(o.Status) {
		return fmt.Errorf("%w: occupancy fields are invalid", ErrInvalidEntity)
	}
	if err := o.Interval.Validate(); err != nil {
		return err
	}
	return validateUniqueIDs(o.ResourceIDs)
}

func validateUniqueIDs(ids []string) error {
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if id == "" {
			return fmt.Errorf("%w: empty allocation id", ErrInvalidEntity)
		}
		if _, exists := seen[id]; exists {
			return fmt.Errorf("%w: duplicate allocation id %q", ErrInvalidEntity, id)
		}
		seen[id] = struct{}{}
	}
	return nil
}

func containsID(ids []string, target string) bool {
	for _, id := range ids {
		if id == target {
			return true
		}
	}
	return false
}
