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
	// ErrLegacyMigrationPlanBlocked 表示迁移报告含有未解决的阻断项。
	ErrLegacyMigrationPlanBlocked = errors.New("legacy booking migration plan is blocked")
	// ErrLegacyMigrationMappingConflict 表示重复执行时发现映射或目标记录不一致。
	ErrLegacyMigrationMappingConflict = errors.New("legacy booking migration mapping conflict")
)

// LegacyBookingMigrationRecord 保存 legacy ID 到 next ID 的版本化审计映射。
type LegacyBookingMigrationRecord struct {
	ID                  uint64    `gorm:"primaryKey;autoIncrement"`
	Version             string    `gorm:"size:64;not null;index"`
	LegacyAppointmentID string    `gorm:"size:64;not null;uniqueIndex"`
	BookingID           string    `gorm:"size:64;not null;uniqueIndex"`
	MerchantID          string    `gorm:"size:64;not null;index"`
	LocationID          string    `gorm:"size:64;not null;index"`
	Status              string    `gorm:"size:32;not null"`
	CreatedAt           time.Time `gorm:"not null"`
}

func (LegacyBookingMigrationRecord) TableName() string { return "booking_migration_records" }

// ExecuteLegacyBookingMigration 将完整迁移规划在一个事务中写入 next 表。
//
// 执行器只接受当前版本且 blocked=0 的完整规划。它不会创建实时预约事件，
// 避免历史导入触发顾客通知；映射审计记录和 Booking/Allocation 仍在同一事务内提交。
// 生产调用方必须先按迁移文档停止旧写路径，并使用数据库只读报告确认切换水位。
func ExecuteLegacyBookingMigration(ctx context.Context, db *gorm.DB, plan LegacyBookingMigrationPlan) error {
	if db == nil {
		return errors.New("迁移执行器数据库句柄为空")
	}
	if err := validateLegacyMigrationPlan(plan); err != nil {
		return err
	}
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		legacyIDs := make([]string, 0, len(plan.Rows))
		for _, row := range plan.Rows {
			legacyIDs = append(legacyIDs, row.LegacyAppointmentID)
		}
		var mappings []LegacyBookingMigrationRecord
		if len(legacyIDs) > 0 {
			if err := tx.WithContext(ctx).Where("legacy_appointment_id IN ?", legacyIDs).Find(&mappings).Error; err != nil {
				return fmt.Errorf("读取迁移映射失败: %w", err)
			}
		}
		mappingByLegacyID := make(map[string]LegacyBookingMigrationRecord, len(mappings))
		for _, mapping := range mappings {
			mappingByLegacyID[mapping.LegacyAppointmentID] = mapping
		}

		pending := make([]LegacyBookingMigrationRow, 0, len(plan.Rows))
		for _, row := range plan.Rows {
			mapping, exists := mappingByLegacyID[row.LegacyAppointmentID]
			if !exists {
				if err := rejectExistingTarget(tx, row); err != nil {
					return err
				}
				pending = append(pending, row)
				continue
			}
			if err := verifyExistingMigration(tx, mapping, row, plan.Version, plan.MerchantID); err != nil {
				return err
			}
		}

		if len(pending) == 0 {
			return nil
		}
		existing, err := loadMigrationTargetOccupancies(ctx, tx, plan.MerchantID, migrationLocationIDs(pending))
		if err != nil {
			return err
		}
		pendingOccupancies := make([]domain.Occupancy, 0, len(pending))
		for _, row := range pending {
			occupancy, err := migrationRowOccupancy(row)
			if err != nil {
				return err
			}
			occupied := append([]domain.Occupancy(nil), existing...)
			occupied = append(occupied, pendingOccupancies...)
			conflicts, err := domain.FindConflicts(occupancy, occupied)
			if err != nil {
				return fmt.Errorf("复核 legacy appointment %q 冲突失败: %w", row.LegacyAppointmentID, err)
			}
			if len(conflicts) > 0 {
				return fmt.Errorf("legacy appointment %q: %w", row.LegacyAppointmentID, ErrBookingConflict)
			}
			pendingOccupancies = append(pendingOccupancies, occupancy)
		}

		for _, row := range pending {
			if err := tx.WithContext(ctx).Create(&row.Booking).Error; err != nil {
				return fmt.Errorf("写入 booking %q 失败: %w", row.Booking.ID, err)
			}
			if err := tx.WithContext(ctx).Create(&row.Allocation).Error; err != nil {
				return fmt.Errorf("写入 booking allocation %q 失败: %w", row.Allocation.ID, err)
			}
			if err := tx.WithContext(ctx).Create(&LegacyBookingMigrationRecord{
				Version:             plan.Version,
				LegacyAppointmentID: row.LegacyAppointmentID,
				BookingID:           row.Booking.ID,
				MerchantID:          row.Booking.MerchantID,
				LocationID:          row.Booking.LocationID,
				Status:              "applied",
				CreatedAt:           time.Now(),
			}).Error; err != nil {
				return fmt.Errorf("写入迁移映射 %q 失败: %w", row.LegacyAppointmentID, err)
			}
		}
		return nil
	})
}

func validateLegacyMigrationPlan(plan LegacyBookingMigrationPlan) error {
	if plan.Version != LegacyBookingMigrationVersion {
		return fmt.Errorf("unsupported legacy migration plan version %q", plan.Version)
	}
	if strings.TrimSpace(plan.MerchantID) == "" {
		return errors.New("迁移执行器需要显式 merchant ID")
	}
	if plan.Total != len(plan.Rows) || plan.Ready != len(plan.Rows) || plan.Blocked != 0 {
		return ErrLegacyMigrationPlanBlocked
	}
	seenLegacyIDs := make(map[string]struct{}, len(plan.Rows))
	seenBookingIDs := make(map[string]struct{}, len(plan.Rows))
	seenIdempotencyKeys := make(map[string]struct{}, len(plan.Rows))
	for _, row := range plan.Rows {
		if row.State != legacyMigrationReady || strings.TrimSpace(row.LegacyAppointmentID) == "" {
			return ErrLegacyMigrationPlanBlocked
		}
		if _, exists := seenLegacyIDs[row.LegacyAppointmentID]; exists {
			return fmt.Errorf("%w: duplicate legacy appointment ID %q", ErrLegacyMigrationMappingConflict, row.LegacyAppointmentID)
		}
		seenLegacyIDs[row.LegacyAppointmentID] = struct{}{}
		if _, exists := seenBookingIDs[row.Booking.ID]; exists {
			return fmt.Errorf("%w: duplicate booking ID %q", ErrLegacyMigrationMappingConflict, row.Booking.ID)
		}
		seenBookingIDs[row.Booking.ID] = struct{}{}
		if _, exists := seenIdempotencyKeys[row.Booking.IdempotencyKey]; exists {
			return fmt.Errorf("%w: duplicate idempotency key %q", ErrLegacyMigrationMappingConflict, row.Booking.IdempotencyKey)
		}
		seenIdempotencyKeys[row.Booking.IdempotencyKey] = struct{}{}
		if row.BookingID != row.Booking.ID || row.IdempotencyKey != row.Booking.IdempotencyKey || row.Booking.MerchantID != plan.MerchantID {
			return fmt.Errorf("%w: row %q identity does not match report", ErrLegacyMigrationMappingConflict, row.LegacyAppointmentID)
		}
		if _, err := migrationRowOccupancy(row); err != nil {
			return err
		}
	}
	return nil
}

func rejectExistingTarget(tx *gorm.DB, row LegacyBookingMigrationRow) error {
	var existing BookingRecord
	if err := tx.Where("id = ?", row.Booking.ID).First(&existing).Error; err == nil {
		return fmt.Errorf("%w: target booking ID %q exists without mapping", ErrLegacyMigrationMappingConflict, row.Booking.ID)
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return fmt.Errorf("检查目标 booking %q 失败: %w", row.Booking.ID, err)
	}
	if err := tx.Where("merchant_id = ? AND location_id = ? AND customer_id = ? AND idempotency_key = ?", row.Booking.MerchantID, row.Booking.LocationID, row.Booking.CustomerID, row.Booking.IdempotencyKey).First(&existing).Error; err == nil {
		return fmt.Errorf("%w: target idempotency key %q exists without mapping", ErrLegacyMigrationMappingConflict, row.Booking.IdempotencyKey)
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return fmt.Errorf("检查目标幂等键 %q 失败: %w", row.Booking.IdempotencyKey, err)
	}
	return nil
}

func verifyExistingMigration(tx *gorm.DB, mapping LegacyBookingMigrationRecord, row LegacyBookingMigrationRow, version, merchantID string) error {
	if mapping.Version != version || mapping.LegacyAppointmentID != row.LegacyAppointmentID || mapping.BookingID != row.Booking.ID || mapping.MerchantID != merchantID || mapping.LocationID != row.Booking.LocationID || mapping.Status != "applied" {
		return fmt.Errorf("%w: legacy appointment %q", ErrLegacyMigrationMappingConflict, row.LegacyAppointmentID)
	}
	var booking BookingRecord
	if err := tx.Where("id = ?", mapping.BookingID).First(&booking).Error; err != nil {
		return fmt.Errorf("%w: booking %q is missing", ErrLegacyMigrationMappingConflict, mapping.BookingID)
	}
	if !sameMigratedBooking(booking, row.Booking) {
		return fmt.Errorf("%w: booking %q differs from report", ErrLegacyMigrationMappingConflict, mapping.BookingID)
	}
	var allocation BookingAllocationRecord
	if err := tx.Where("id = ? AND booking_id = ? AND merchant_id = ? AND location_id = ? AND staff_id = ?", row.Allocation.ID, row.Booking.ID, row.Booking.MerchantID, row.Booking.LocationID, row.Booking.StaffID).First(&allocation).Error; err != nil {
		return fmt.Errorf("%w: allocation %q is missing or differs", ErrLegacyMigrationMappingConflict, row.Allocation.ID)
	}
	return nil
}

func sameMigratedBooking(existing BookingRecord, expected BookingRecord) bool {
	return existing.ID == expected.ID && existing.MerchantID == expected.MerchantID && existing.LocationID == expected.LocationID && existing.CustomerID == expected.CustomerID && existing.ServiceID == expected.ServiceID && existing.StaffID == expected.StaffID && existing.StartAt.Equal(expected.StartAt) && existing.EndAt.Equal(expected.EndAt) && existing.Status == expected.Status && existing.IdempotencyKey == expected.IdempotencyKey
}

func migrationRowOccupancy(row LegacyBookingMigrationRow) (domain.Occupancy, error) {
	booking := domain.Booking{
		ID: row.Booking.ID, MerchantID: row.Booking.MerchantID, LocationID: row.Booking.LocationID,
		CustomerID: row.Booking.CustomerID, ServiceID: row.Booking.ServiceID, StaffID: row.Booking.StaffID,
		StartAt: row.Booking.StartAt, EndAt: row.Booking.EndAt, Status: domain.BookingStatus(row.Booking.Status), IdempotencyKey: row.Booking.IdempotencyKey,
	}
	if err := booking.Validate(); err != nil {
		return domain.Occupancy{}, fmt.Errorf("迁移预约 %q 校验失败: %w", row.LegacyAppointmentID, err)
	}
	if row.Allocation.ID == "" || row.Allocation.BookingID != row.Booking.ID || row.Allocation.MerchantID != row.Booking.MerchantID || row.Allocation.LocationID != row.Booking.LocationID || row.Allocation.StaffID != row.Booking.StaffID || row.Allocation.ResourceID != "" {
		return domain.Occupancy{}, fmt.Errorf("迁移预约 %q 的员工 Allocation 不完整或包含未验证资源", row.LegacyAppointmentID)
	}
	return domain.NewOccupancy(booking, nil)
}

func migrationLocationIDs(rows []LegacyBookingMigrationRow) []string {
	seen := make(map[string]struct{}, len(rows))
	locations := make([]string, 0, len(rows))
	for _, row := range rows {
		if _, exists := seen[row.Booking.LocationID]; exists {
			continue
		}
		seen[row.Booking.LocationID] = struct{}{}
		locations = append(locations, row.Booking.LocationID)
	}
	return locations
}

func loadMigrationTargetOccupancies(ctx context.Context, tx *gorm.DB, merchantID string, locationIDs []string) ([]domain.Occupancy, error) {
	if len(locationIDs) == 0 {
		return nil, nil
	}
	var records []BookingRecord
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("merchant_id = ? AND location_id IN ?", merchantID, locationIDs).Order("id ASC").Find(&records).Error; err != nil {
		return nil, fmt.Errorf("锁定目标 booking 失败: %w", err)
	}
	if len(records) == 0 {
		return nil, nil
	}
	bookingIDs := make([]string, 0, len(records))
	for _, record := range records {
		bookingIDs = append(bookingIDs, record.ID)
	}
	var allocations []BookingAllocationRecord
	if err := tx.WithContext(ctx).Where("booking_id IN ?", bookingIDs).Order("booking_id ASC, id ASC").Find(&allocations).Error; err != nil {
		return nil, fmt.Errorf("读取目标 allocation 失败: %w", err)
	}
	resources := make(map[string][]string, len(records))
	for _, allocation := range allocations {
		if allocation.ResourceID != "" {
			resources[allocation.BookingID] = append(resources[allocation.BookingID], allocation.ResourceID)
		}
	}
	occupancies := make([]domain.Occupancy, 0, len(records))
	for _, record := range records {
		booking := domain.Booking{
			ID: record.ID, MerchantID: record.MerchantID, LocationID: record.LocationID,
			CustomerID: record.CustomerID, ServiceID: record.ServiceID, StaffID: record.StaffID,
			StartAt: record.StartAt, EndAt: record.EndAt, Status: domain.BookingStatus(record.Status), IdempotencyKey: record.IdempotencyKey,
		}
		occupancy, err := domain.NewOccupancy(booking, resources[record.ID])
		if err != nil {
			return nil, fmt.Errorf("校验目标 booking %q 失败: %w", record.ID, err)
		}
		occupancies = append(occupancies, occupancy)
	}
	return occupancies, nil
}
