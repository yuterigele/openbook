package storage

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/yuterigele/openbook/internal/booking/domain"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// LegacyBookingMigrationInputs 是只读报告模式从数据库加载的输入快照。
type LegacyBookingMigrationInputs struct {
	Appointments            []Appointment
	Services                []Service
	Barbers                 []Barber
	ExistingBookings        []domain.Occupancy
	ExistingBookingIDs      []string
	ExistingIdempotencyKeys []string
}

// OpenReadOnlyDB 打开迁移报告专用数据库句柄，不执行 AutoMigrate、seed 或回填。
// 调用方只能通过 LoadLegacyBookingMigrationInputs 执行 SELECT 查询；生产环境
// 应为该命令配置数据库只读账号，避免把“代码只读”误当成数据库权限。
func OpenReadOnlyDB(ctx context.Context) (*gorm.DB, error) {
	dsn := mysqlDSNFromEnv()
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Error)})
	if err != nil {
		return nil, errors.New("只读迁移报告连接 MySQL 失败")
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, errors.New("只读迁移报告获取 MySQL 连接失败")
	}
	sqlDB.SetMaxOpenConns(4)
	sqlDB.SetMaxIdleConns(2)
	sqlDB.SetConnMaxLifetime(10 * time.Minute)
	if err := sqlDB.PingContext(ctx); err != nil {
		_ = sqlDB.Close()
		return nil, errors.New("只读迁移报告无法连接 MySQL")
	}
	return db, nil
}

// LoadLegacyBookingMigrationInputs 从指定数据库加载迁移规划快照。
//
// 该函数只查询 appointments、services、barbers、booking_records 和
// booking_allocations，不修改任何表；merchantID 必须由调用方显式提供。
func LoadLegacyBookingMigrationInputs(ctx context.Context, db *gorm.DB, merchantID, locationID string) (LegacyBookingMigrationInputs, error) {
	if db == nil {
		return LegacyBookingMigrationInputs{}, errors.New("迁移报告数据库句柄为空")
	}
	if strings.TrimSpace(merchantID) == "" {
		return LegacyBookingMigrationInputs{}, errors.New("迁移报告需要显式 merchant ID")
	}

	var inputs LegacyBookingMigrationInputs
	appointmentQuery := db.WithContext(ctx).Order("id ASC")
	serviceQuery := db.WithContext(ctx).Order("id ASC")
	barberQuery := db.WithContext(ctx).Order("id ASC")
	if locationID != "" {
		appointmentQuery = appointmentQuery.Where("shop_id = ?", locationID)
		serviceQuery = serviceQuery.Where("shop_id = ?", locationID)
		barberQuery = barberQuery.Where("shop_id = ?", locationID)
	}
	if err := appointmentQuery.Find(&inputs.Appointments).Error; err != nil {
		return LegacyBookingMigrationInputs{}, fmt.Errorf("读取 appointments 失败: %w", err)
	}
	if err := serviceQuery.Find(&inputs.Services).Error; err != nil {
		return LegacyBookingMigrationInputs{}, fmt.Errorf("读取 services 失败: %w", err)
	}
	if err := barberQuery.Find(&inputs.Barbers).Error; err != nil {
		return LegacyBookingMigrationInputs{}, fmt.Errorf("读取 barbers 失败: %w", err)
	}

	var existing []BookingRecord
	targetQuery := db.WithContext(ctx).Where("merchant_id = ?", merchantID).Order("id ASC")
	if locationID != "" {
		targetQuery = targetQuery.Where("location_id = ?", locationID)
	}
	if err := targetQuery.Find(&existing).Error; err != nil {
		return LegacyBookingMigrationInputs{}, fmt.Errorf("读取 booking_records 失败: %w", err)
	}
	for _, record := range existing {
		inputs.ExistingBookingIDs = append(inputs.ExistingBookingIDs, record.ID)
		inputs.ExistingIdempotencyKeys = append(inputs.ExistingIdempotencyKeys, record.IdempotencyKey)
	}
	if len(existing) == 0 {
		return inputs, nil
	}

	bookingIDs := make([]string, 0, len(existing))
	existingByID := make(map[string]BookingRecord, len(existing))
	for _, record := range existing {
		bookingIDs = append(bookingIDs, record.ID)
		existingByID[record.ID] = record
	}
	var allocations []BookingAllocationRecord
	if err := db.WithContext(ctx).Where("booking_id IN ?", bookingIDs).Order("booking_id ASC, id ASC").Find(&allocations).Error; err != nil {
		return LegacyBookingMigrationInputs{}, fmt.Errorf("读取 booking_allocations 失败: %w", err)
	}
	resourceIDsByBooking := make(map[string][]string, len(existing))
	for _, allocation := range allocations {
		record, exists := existingByID[allocation.BookingID]
		if !exists || allocation.MerchantID != record.MerchantID || allocation.LocationID != record.LocationID {
			return LegacyBookingMigrationInputs{}, fmt.Errorf("校验 booking allocation %q 的租户归属失败", allocation.ID)
		}
		if allocation.StaffID != "" && allocation.StaffID != record.StaffID {
			return LegacyBookingMigrationInputs{}, fmt.Errorf("校验 booking allocation %q 的员工归属失败", allocation.ID)
		}
		if allocation.ResourceID != "" {
			resourceIDsByBooking[allocation.BookingID] = append(resourceIDsByBooking[allocation.BookingID], allocation.ResourceID)
		}
	}
	for _, record := range existing {
		status := domain.BookingStatus(record.Status)
		booking := domain.Booking{
			ID: record.ID, MerchantID: record.MerchantID, LocationID: record.LocationID,
			CustomerID: record.CustomerID, ServiceID: record.ServiceID, StaffID: record.StaffID,
			StartAt: record.StartAt, EndAt: record.EndAt, Status: status, IdempotencyKey: record.IdempotencyKey,
		}
		occupancy, err := domain.NewOccupancy(booking, resourceIDsByBooking[record.ID])
		if err != nil {
			return LegacyBookingMigrationInputs{}, fmt.Errorf("校验已有 booking %q 失败: %w", record.ID, err)
		}
		inputs.ExistingBookings = append(inputs.ExistingBookings, occupancy)
	}
	return inputs, nil
}

func mysqlDSNFromEnv() string {
	if dsn := os.Getenv("MYSQL_DSN"); dsn != "" {
		return dsn
	}
	host := getenv("MYSQL_HOST", "127.0.0.1")
	port := getenv("MYSQL_PORT", "3306")
	user := getenv("MYSQL_USER", "chatwitheino")
	pass := getenv("MYSQL_PASS", "chatwitheino")
	dbname := getenv("MYSQL_DB", "chatwitheino")
	return fmt.Sprintf("%s:%s@tcp(%s:%s)/%s?charset=utf8mb4&parseTime=True&loc=Local", user, pass, host, port, dbname)
}
