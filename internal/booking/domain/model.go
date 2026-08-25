package domain

import (
	"errors"
	"fmt"
	"time"
)

var (
	// ErrInvalidEntity 表示预约领域实体缺少必需字段或包含非法值。
	ErrInvalidEntity = errors.New("invalid booking entity")
	// ErrTenantMismatch 表示参与同一预约的对象不属于同一商户和门店。
	ErrTenantMismatch = errors.New("booking tenant mismatch")
	// ErrUnsupportedStatus 表示预约状态不在领域允许范围内。
	ErrUnsupportedStatus = errors.New("unsupported booking status")
)

// BookingStatus 是预约在领域层允许的状态。
type BookingStatus string

const (
	BookingPending   BookingStatus = "pending"
	BookingConfirmed BookingStatus = "confirmed"
	BookingCancelled BookingStatus = "cancelled"
	BookingCompleted BookingStatus = "completed"
)

// ResourceRequirement 描述一项服务需要占用的独占资源类型和数量。
type ResourceRequirement struct {
	Kind     string
	Quantity int
}

// Validate 校验资源需求。v1 只支持独占资源，因此数量必须为 1。
func (r ResourceRequirement) Validate() error {
	if r.Kind == "" || r.Quantity != 1 {
		return fmt.Errorf("%w: resource requirement must contain one exclusive resource", ErrInvalidEntity)
	}
	return nil
}

// Service 是可预约的服务定义。
type Service struct {
	ID                   string
	MerchantID           string
	LocationID           string
	Name                 string
	Duration             time.Duration
	BufferBefore         time.Duration
	BufferAfter          time.Duration
	AllowedStaffIDs      []string
	ResourceRequirements []ResourceRequirement
	Active               bool
}

// Validate 校验服务定义及其租户归属。
func (s Service) Validate() error {
	if err := validateScope(s.MerchantID, s.LocationID); err != nil {
		return err
	}
	if s.ID == "" || s.Name == "" || s.Duration <= 0 || s.BufferBefore < 0 || s.BufferAfter < 0 {
		return fmt.Errorf("%w: service fields are invalid", ErrInvalidEntity)
	}
	for _, requirement := range s.ResourceRequirements {
		if err := requirement.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// IntervalAt 根据服务时长和缓冲计算资源占用区间。
func (s Service) IntervalAt(startAt time.Time) (Interval, error) {
	if err := s.Validate(); err != nil {
		return Interval{}, err
	}
	return NewServiceInterval(startAt, s.Duration, s.BufferBefore, s.BufferAfter)
}

// Staff 是可执行服务的人员定义。
type Staff struct {
	ID         string
	MerchantID string
	LocationID string
	Name       string
	Active     bool
}

// Validate 校验员工定义及其租户归属。
func (s Staff) Validate() error {
	if err := validateScope(s.MerchantID, s.LocationID); err != nil {
		return err
	}
	if s.ID == "" || s.Name == "" {
		return fmt.Errorf("%w: staff fields are invalid", ErrInvalidEntity)
	}
	return nil
}

// Resource 是预约可能占用的独占资源。
type Resource struct {
	ID         string
	MerchantID string
	LocationID string
	Kind       string
	Name       string
	Active     bool
}

// Validate 校验资源定义及其租户归属。
func (r Resource) Validate() error {
	if err := validateScope(r.MerchantID, r.LocationID); err != nil {
		return err
	}
	if r.ID == "" || r.Kind == "" || r.Name == "" {
		return fmt.Errorf("%w: resource fields are invalid", ErrInvalidEntity)
	}
	return nil
}

// Allocation 记录一次预约对员工或资源的实际占用。
type Allocation struct {
	ID         string
	BookingID  string
	MerchantID string
	LocationID string
	StaffID    string
	ResourceID string
}

// Validate 校验占用记录至少关联一个员工或资源。
func (a Allocation) Validate() error {
	if err := validateScope(a.MerchantID, a.LocationID); err != nil {
		return err
	}
	if a.ID == "" || a.BookingID == "" || (a.StaffID == "" && a.ResourceID == "") {
		return fmt.Errorf("%w: allocation fields are invalid", ErrInvalidEntity)
	}
	return nil
}

// Booking 是预约主记录。时间使用真实区间，不把展示日期和时间作为事实来源。
type Booking struct {
	ID             string
	MerchantID     string
	LocationID     string
	CustomerID     string
	ServiceID      string
	StaffID        string
	StartAt        time.Time
	EndAt          time.Time
	Status         BookingStatus
	IdempotencyKey string
}

// NewBooking 根据已校验的服务、员工和顾客上下文创建待确认预约。
func NewBooking(id, merchantID, locationID, customerID, idempotencyKey string, service Service, staff Staff, startAt time.Time) (Booking, error) {
	if id == "" || customerID == "" || idempotencyKey == "" {
		return Booking{}, fmt.Errorf("%w: booking identity fields are required", ErrInvalidEntity)
	}
	if err := validateScope(merchantID, locationID); err != nil {
		return Booking{}, err
	}
	if err := service.Validate(); err != nil {
		return Booking{}, err
	}
	if err := staff.Validate(); err != nil {
		return Booking{}, err
	}
	if err := ensureSameScope(merchantID, locationID, service.MerchantID, service.LocationID); err != nil {
		return Booking{}, err
	}
	if err := ensureSameScope(merchantID, locationID, staff.MerchantID, staff.LocationID); err != nil {
		return Booking{}, err
	}
	if !serviceAllowsStaff(service, staff.ID) {
		return Booking{}, fmt.Errorf("%w: staff is not allowed to perform service", ErrInvalidEntity)
	}
	interval, err := service.IntervalAt(startAt)
	if err != nil {
		return Booking{}, err
	}
	return Booking{
		ID:             id,
		MerchantID:     merchantID,
		LocationID:     locationID,
		CustomerID:     customerID,
		ServiceID:      service.ID,
		StaffID:        staff.ID,
		StartAt:        interval.StartAt,
		EndAt:          interval.EndAt,
		Status:         BookingPending,
		IdempotencyKey: idempotencyKey,
	}, nil
}

// Validate 校验预约自身的状态、区间和租户字段。
func (b Booking) Validate() error {
	if err := validateScope(b.MerchantID, b.LocationID); err != nil {
		return err
	}
	if b.ID == "" || b.CustomerID == "" || b.ServiceID == "" || b.StaffID == "" || b.IdempotencyKey == "" {
		return fmt.Errorf("%w: booking fields are invalid", ErrInvalidEntity)
	}
	if !validBookingStatus(b.Status) {
		return fmt.Errorf("%w: %q", ErrUnsupportedStatus, b.Status)
	}
	interval, err := NewInterval(b.StartAt, b.EndAt)
	if err != nil {
		return err
	}
	if interval.Duration() <= 0 {
		return ErrInvalidInterval
	}
	return nil
}

// ValidateScope 确认预约、员工、服务和占用记录没有跨租户混用。
func (b Booking) ValidateScope(service Service, staff Staff, allocations []Allocation) error {
	if err := b.Validate(); err != nil {
		return err
	}
	if err := service.Validate(); err != nil {
		return err
	}
	if err := staff.Validate(); err != nil {
		return err
	}
	for _, item := range allocations {
		if err := item.Validate(); err != nil {
			return err
		}
		if err := ensureSameScope(b.MerchantID, b.LocationID, item.MerchantID, item.LocationID); err != nil {
			return err
		}
		if item.BookingID != b.ID {
			return fmt.Errorf("%w: allocation does not belong to booking", ErrInvalidEntity)
		}
	}
	if err := ensureSameScope(b.MerchantID, b.LocationID, service.MerchantID, service.LocationID); err != nil {
		return err
	}
	if err := ensureSameScope(b.MerchantID, b.LocationID, staff.MerchantID, staff.LocationID); err != nil {
		return err
	}
	if b.ServiceID != service.ID || b.StaffID != staff.ID || !serviceAllowsStaff(service, staff.ID) {
		return fmt.Errorf("%w: booking references do not match service and staff", ErrInvalidEntity)
	}
	return nil
}

// BookingPolicy 定义不依赖渠道的确定性预约策略。
type BookingPolicy struct {
	Location        *time.Location
	SlotGranularity time.Duration
	MinAdvance      time.Duration
	MaxAdvance      time.Duration
	CancelBefore    time.Duration
}

// Validate 校验策略的时间参数。
func (p BookingPolicy) Validate() error {
	if p.Location == nil || p.SlotGranularity <= 0 || p.MinAdvance < 0 || p.MaxAdvance <= 0 || p.MaxAdvance < p.MinAdvance || p.CancelBefore < 0 {
		return fmt.Errorf("%w: booking policy fields are invalid", ErrInvalidEntity)
	}
	if 24*time.Hour%p.SlotGranularity != 0 {
		return fmt.Errorf("%w: slot granularity must divide one day", ErrInvalidEntity)
	}
	return nil
}

func validateScope(merchantID, locationID string) error {
	if merchantID == "" || locationID == "" {
		return fmt.Errorf("%w: merchant and location are required", ErrInvalidEntity)
	}
	return nil
}

func ensureSameScope(merchantID, locationID, otherMerchantID, otherLocationID string) error {
	if merchantID != otherMerchantID || locationID != otherLocationID {
		return ErrTenantMismatch
	}
	return nil
}

func serviceAllowsStaff(service Service, staffID string) bool {
	if len(service.AllowedStaffIDs) == 0 {
		return true
	}
	for _, allowedID := range service.AllowedStaffIDs {
		if allowedID == staffID {
			return true
		}
	}
	return false
}

func validBookingStatus(status BookingStatus) bool {
	switch status {
	case BookingPending, BookingConfirmed, BookingCancelled, BookingCompleted:
		return true
	default:
		return false
	}
}
