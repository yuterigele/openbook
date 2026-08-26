package storage

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/yuterigele/openbook/internal/booking/domain"
)

// LegacyBookingMigrationVersion 标识旧 appointments 到通用预约记录的映射规则。
const LegacyBookingMigrationVersion = "legacy-appointments-to-booking-v1"

const (
	legacyMigrationReady   = "ready"
	legacyMigrationBlocked = "blocked"
)

// LegacyBookingMigrationOptions 是一次迁移规划所需的显式上下文。
//
// MerchantID 不从旧 ShopID 推断。旧模型只有门店维度，若调用方没有提供
// 商户映射，规划直接失败，避免把门店错误地当成商户或跨租户写入。
type LegacyBookingMigrationOptions struct {
	MerchantID string
	LocationID string
	Location   *time.Location

	// ExistingBookings 用于在切换窗口内校验 next 表已有的占用冲突。
	ExistingBookings []domain.Occupancy
	// ExistingBookingIDs 和 ExistingIdempotencyKeys 用于阻止目标表主键或幂等键碰撞。
	ExistingBookingIDs      []string
	ExistingIdempotencyKeys []string
}

// LegacyBookingMigrationPlan 是只读迁移规划和审计映射报告。
//
// 规划器不会写入数据库。只有 State=ready 的行才允许后续执行器落库；
// blocked 行保留原因，调用方必须修复数据后重新规划，不能自动挑选历史赢家。
type LegacyBookingMigrationPlan struct {
	Version    string                      `json:"version"`
	MerchantID string                      `json:"merchant_id"`
	Total      int                         `json:"total"`
	Ready      int                         `json:"ready"`
	Blocked    int                         `json:"blocked"`
	Notes      []string                    `json:"notes"`
	Rows       []LegacyBookingMigrationRow `json:"rows"`
}

// LegacyBookingMigrationRow 保存一条 legacy ID 到 next ID 的映射及其状态。
type LegacyBookingMigrationRow struct {
	LegacyAppointmentID string                           `json:"legacy_appointment_id"`
	BookingID           string                           `json:"booking_id,omitempty"`
	IdempotencyKey      string                           `json:"idempotency_key,omitempty"`
	State               string                           `json:"state"`
	Reasons             []string                         `json:"reasons,omitempty"`
	Candidate           *LegacyBookingMigrationCandidate `json:"candidate,omitempty"`

	// Booking 和 Allocation 供后续事务执行器使用，不作为公开报告字段序列化。
	Booking    BookingRecord           `json:"-"`
	Allocation BookingAllocationRecord `json:"-"`
}

// LegacyBookingMigrationCandidate 是报告中可供人工复核的目标记录摘要。
type LegacyBookingMigrationCandidate struct {
	MerchantID string    `json:"merchant_id"`
	LocationID string    `json:"location_id"`
	CustomerID string    `json:"customer_id"`
	ServiceID  string    `json:"service_id"`
	StaffID    string    `json:"staff_id"`
	StartAt    time.Time `json:"start_at"`
	EndAt      time.Time `json:"end_at"`
	Status     string    `json:"status"`
	Allocation string    `json:"allocation_id"`
}

// ReadyRows 返回可供后续执行器使用的 ready 映射副本。
func (p LegacyBookingMigrationPlan) ReadyRows() []LegacyBookingMigrationRow {
	rows := make([]LegacyBookingMigrationRow, 0, p.Ready)
	for _, row := range p.Rows {
		if row.State == legacyMigrationReady {
			row.Reasons = append([]string(nil), row.Reasons...)
			if row.Candidate != nil {
				candidate := *row.Candidate
				row.Candidate = &candidate
			}
			rows = append(rows, row)
		}
	}
	return rows
}

// PlanLegacyAppointmentMigration 将旧预约转换为通用预约的候选记录和审计报告。
//
// 规划只使用确定性字段：门店、服务名、员工 ID、顾客 ID、日期时间和状态。
// 服务名必须在同一门店唯一，顾客 ID 必须已存在于旧记录；资源关系在旧模型中
// 不存在，因此规划只生成员工 Allocation，不伪造资源占用。
func PlanLegacyAppointmentMigration(appointments []Appointment, services []Service, barbers []Barber, options LegacyBookingMigrationOptions) (LegacyBookingMigrationPlan, error) {
	if strings.TrimSpace(options.MerchantID) == "" {
		return LegacyBookingMigrationPlan{}, fmt.Errorf("legacy booking migration requires merchant ID")
	}
	location := options.Location
	if location == nil {
		var err error
		location, err = time.LoadLocation("Asia/Shanghai")
		if err != nil {
			return LegacyBookingMigrationPlan{}, fmt.Errorf("load migration timezone: %w", err)
		}
	}

	plan := LegacyBookingMigrationPlan{
		Version:    LegacyBookingMigrationVersion,
		MerchantID: options.MerchantID,
		Total:      len(appointments),
		Notes: []string{
			"旧 appointments 没有资源占用字段；规划器只生成员工 Allocation，不推断 ResourceID。",
			"规划器只读内存输入，不连接或修改数据库；落库前必须由独立执行器再次校验迁移报告。",
		},
		Rows: make([]LegacyBookingMigrationRow, 0, len(appointments)),
	}

	serviceIndex := make(map[string][]Service)
	for _, service := range services {
		key := legacyCatalogKey(service.ShopID, service.Name)
		serviceIndex[key] = append(serviceIndex[key], service)
	}
	barberIndex := make(map[string][]Barber)
	for _, barber := range barbers {
		key := legacyCatalogKey(barber.ShopID, barber.ID)
		barberIndex[key] = append(barberIndex[key], barber)
	}
	existingIDs := stringSet(options.ExistingBookingIDs)
	existingIdempotencyKeys := stringSet(options.ExistingIdempotencyKeys)
	orderedAppointments := append([]Appointment(nil), appointments...)
	sort.SliceStable(orderedAppointments, func(i, j int) bool {
		return orderedAppointments[i].ID < orderedAppointments[j].ID
	})

	type candidate struct {
		index     int
		occupancy domain.Occupancy
	}
	candidates := make([]candidate, 0, len(orderedAppointments))
	for index := range orderedAppointments {
		appointment := orderedAppointments[index]
		row := LegacyBookingMigrationRow{
			LegacyAppointmentID: appointment.ID,
			State:               legacyMigrationBlocked,
			BookingID:           migratedBookingID(appointment.ID),
			IdempotencyKey:      migratedIdempotencyKey(appointment.ID),
		}
		booking, allocation, occupancy, reasons := planLegacyAppointment(appointment, serviceIndex, barberIndex, options, location, existingIDs, existingIdempotencyKeys)
		row.Booking = booking
		row.Allocation = allocation
		row.Reasons = reasons
		if booking.ID != "" {
			row.Candidate = &LegacyBookingMigrationCandidate{
				MerchantID: booking.MerchantID, LocationID: booking.LocationID, CustomerID: booking.CustomerID,
				ServiceID: booking.ServiceID, StaffID: booking.StaffID, StartAt: booking.StartAt,
				EndAt: booking.EndAt, Status: booking.Status, Allocation: allocation.ID,
			}
		}
		if len(reasons) == 0 {
			row.State = legacyMigrationReady
			candidates = append(candidates, candidate{index: len(plan.Rows), occupancy: occupancy})
		}
		plan.Rows = append(plan.Rows, row)
	}

	legacyIDRows := make(map[string][]int)
	for index, row := range plan.Rows {
		if row.LegacyAppointmentID != "" {
			legacyIDRows[row.LegacyAppointmentID] = append(legacyIDRows[row.LegacyAppointmentID], index)
		}
	}
	for legacyID, indexes := range legacyIDRows {
		if len(indexes) < 2 {
			continue
		}
		for _, index := range indexes {
			plan.Rows[index].Reasons = appendUniqueReason(plan.Rows[index].Reasons, fmt.Sprintf("输入包含重复的 legacy appointment ID %q", legacyID))
		}
	}

	// 对同一门店的所有候选一起做冲突检查。冲突两边全部阻断，不能以输入顺序
	// 自动决定谁先迁移，避免把不确定的历史状态变成新的事实。
	for _, item := range candidates {
		occupied := make([]domain.Occupancy, 0, len(options.ExistingBookings)+len(candidates)-1)
		for _, existing := range options.ExistingBookings {
			if sameBookingScope(item.occupancy.MerchantID, item.occupancy.LocationID, existing.MerchantID, existing.LocationID) {
				occupied = append(occupied, existing)
			}
		}
		for _, other := range candidates {
			if other.index == item.index || !sameBookingScope(item.occupancy.MerchantID, item.occupancy.LocationID, other.occupancy.MerchantID, other.occupancy.LocationID) {
				continue
			}
			occupied = append(occupied, other.occupancy)
		}
		conflicts, err := domain.FindConflicts(item.occupancy, occupied)
		if err != nil {
			return LegacyBookingMigrationPlan{}, fmt.Errorf("validate legacy appointment %q conflicts: %w", plan.Rows[item.index].LegacyAppointmentID, err)
		}
		for _, conflict := range conflicts {
			plan.Rows[item.index].Reasons = appendUniqueReason(plan.Rows[item.index].Reasons, fmt.Sprintf("与目标预约 %q 存在%s冲突", conflict.BookingID, migrationConflictLabel(conflict.Kind)))
		}
	}

	for index := range plan.Rows {
		row := &plan.Rows[index]
		if len(row.Reasons) == 0 {
			row.State = legacyMigrationReady
			plan.Ready++
		} else {
			row.State = legacyMigrationBlocked
			plan.Blocked++
		}
		sort.Strings(row.Reasons)
	}
	return plan, nil
}

func planLegacyAppointment(appointment Appointment, services map[string][]Service, barbers map[string][]Barber, options LegacyBookingMigrationOptions, location *time.Location, existingIDs, existingIdempotencyKeys map[string]struct{}) (BookingRecord, BookingAllocationRecord, domain.Occupancy, []string) {
	bookingID := migratedBookingID(appointment.ID)
	idempotencyKey := migratedIdempotencyKey(appointment.ID)
	reasons := make([]string, 0)
	if strings.TrimSpace(appointment.ID) == "" {
		reasons = append(reasons, "legacy appointment ID 为空")
	}
	if strings.TrimSpace(appointment.ShopID) == "" {
		reasons = append(reasons, "legacy shop ID 为空")
	}
	if options.LocationID != "" && options.LocationID != appointment.ShopID {
		reasons = append(reasons, fmt.Sprintf("legacy shop %q 不属于迁移门店 %q", appointment.ShopID, options.LocationID))
	}
	if strings.TrimSpace(appointment.CustomerID) == "" {
		reasons = append(reasons, "顾客 ID 为空，不能确定预约归属")
	}
	status, ok := legacyBookingStatus(appointment.Status)
	if !ok {
		reasons = append(reasons, fmt.Sprintf("不支持的 legacy 状态 %q，未自动猜测映射", appointment.Status))
	}

	serviceMatches := services[legacyCatalogKey(appointment.ShopID, appointment.Service)]
	if len(serviceMatches) == 0 {
		reasons = append(reasons, fmt.Sprintf("门店 %q 找不到唯一服务 %q", appointment.ShopID, appointment.Service))
	} else if len(serviceMatches) > 1 {
		reasons = append(reasons, fmt.Sprintf("门店 %q 的服务 %q 不唯一，未自动猜测 service ID", appointment.ShopID, appointment.Service))
	}
	barberMatches := barbers[legacyCatalogKey(appointment.ShopID, appointment.BarberID)]
	if len(barberMatches) == 0 {
		reasons = append(reasons, fmt.Sprintf("找不到属于门店 %q 的员工 %q", appointment.ShopID, appointment.BarberID))
	} else if len(barberMatches) > 1 {
		reasons = append(reasons, fmt.Sprintf("门店 %q 的员工 %q 不唯一", appointment.ShopID, appointment.BarberID))
	}

	startAt, err := time.ParseInLocation("2006-01-02 15:04", strings.TrimSpace(appointment.Date)+" "+strings.TrimSpace(appointment.Time), location)
	if err != nil {
		reasons = append(reasons, fmt.Sprintf("日期时间无效: %v", err))
	}
	if _, exists := existingIDs[bookingID]; exists {
		reasons = append(reasons, fmt.Sprintf("目标 booking ID %q 已存在", bookingID))
	}
	if _, exists := existingIdempotencyKeys[idempotencyKey]; exists {
		reasons = append(reasons, fmt.Sprintf("目标幂等键 %q 已存在", idempotencyKey))
	}
	if len(reasons) > 0 || len(serviceMatches) != 1 || len(barberMatches) != 1 || !ok || err != nil {
		return BookingRecord{}, BookingAllocationRecord{}, domain.Occupancy{}, reasons
	}

	service := serviceMatches[0]
	if service.EstimatedMin <= 0 {
		reasons = append(reasons, fmt.Sprintf("服务 %q 缺少有效时长，不能推导 EndAt", service.Name))
		return BookingRecord{}, BookingAllocationRecord{}, domain.Occupancy{}, reasons
	}
	barber := barberMatches[0]
	booking := BookingRecord{
		ID:             bookingID,
		MerchantID:     options.MerchantID,
		LocationID:     appointment.ShopID,
		CustomerID:     appointment.CustomerID,
		ServiceID:      service.ID,
		StaffID:        barber.ID,
		StartAt:        startAt,
		EndAt:          startAt.Add(time.Duration(service.EstimatedMin) * time.Minute),
		Status:         string(status),
		IdempotencyKey: idempotencyKey,
		CreatedAt:      appointment.CreatedAt,
		UpdatedAt:      appointment.UpdatedAt,
	}
	domainBooking := domain.Booking{
		ID: booking.ID, MerchantID: booking.MerchantID, LocationID: booking.LocationID,
		CustomerID: booking.CustomerID, ServiceID: booking.ServiceID, StaffID: booking.StaffID,
		StartAt: booking.StartAt, EndAt: booking.EndAt, Status: status, IdempotencyKey: booking.IdempotencyKey,
	}
	if err := domainBooking.Validate(); err != nil {
		reasons = append(reasons, fmt.Sprintf("通用预约校验失败: %v", err))
		return BookingRecord{}, BookingAllocationRecord{}, domain.Occupancy{}, reasons
	}
	allocation := BookingAllocationRecord{
		ID: migratedAllocationID(appointment.ID), BookingID: booking.ID,
		MerchantID: booking.MerchantID, LocationID: booking.LocationID, StaffID: booking.StaffID,
		CreatedAt: appointment.CreatedAt,
	}
	occupancy, err := domain.NewOccupancy(domainBooking, nil)
	if err != nil {
		return BookingRecord{}, BookingAllocationRecord{}, domain.Occupancy{}, []string{fmt.Sprintf("通用占用校验失败: %v", err)}
	}
	return booking, allocation, occupancy, nil
}

func legacyBookingStatus(status string) (domain.BookingStatus, bool) {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "active":
		return domain.BookingConfirmed, true
	case "cancelled":
		return domain.BookingCancelled, true
	case "completed":
		return domain.BookingCompleted, true
	default:
		return "", false
	}
}

func legacyCatalogKey(shopID, value string) string {
	return strings.TrimSpace(shopID) + "\x00" + strings.TrimSpace(value)
}

func sameBookingScope(merchantID, locationID, otherMerchantID, otherLocationID string) bool {
	return merchantID == otherMerchantID && locationID == otherLocationID
}

func stringSet(values []string) map[string]struct{} {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			set[value] = struct{}{}
		}
	}
	return set
}

func appendUniqueReason(reasons []string, reason string) []string {
	for _, existing := range reasons {
		if existing == reason {
			return reasons
		}
	}
	return append(reasons, reason)
}

func migrationConflictLabel(kind domain.ConflictKind) string {
	if kind == domain.ConflictResource {
		return "资源"
	}
	return "员工"
}

func migratedBookingID(legacyID string) string {
	candidate := "legacy-" + strings.TrimSpace(legacyID)
	if len(candidate) <= 64 {
		return candidate
	}
	sum := sha256.Sum256([]byte(candidate))
	return "legacy-" + hex.EncodeToString(sum[:])[:56]
}

func migratedAllocationID(legacyID string) string {
	candidate := migratedBookingID(legacyID) + "-staff"
	if len(candidate) <= 64 {
		return candidate
	}
	sum := sha256.Sum256([]byte("allocation:" + strings.TrimSpace(legacyID)))
	return "legacy-" + hex.EncodeToString(sum[:])[:56]
}

func migratedIdempotencyKey(legacyID string) string {
	return "legacy:appointment:" + strings.TrimSpace(legacyID)
}
