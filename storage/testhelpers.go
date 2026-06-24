package storage

// testhelpers.go
//
// Test helpers exposed for cross-package tests (e.g. the tools package needs
// SetupTestDB to set up a sqlite DB before exercising tool functions).
//
// These functions are NOT test-only files, so they're available at compile time
// to any package that imports storage. Each call creates a unique-named in-memory
// sqlite DB and binds it to the package-global DB variable.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log"
	"os"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// WithCtx returns a fresh background context (helper so call sites read cleanly).
func WithCtx() context.Context {
	return context.Background()
}

// SetupTestDB creates an isolated in-memory sqlite DB and binds it to the global DB variable.
//
// Same as setup_test.go's helper but exposed for cross-package test use. Idempotent:
// every call gives the test a fresh, isolated DB; t.Cleanup resets DB=nil.
//
// Required to be called before any storage function in tests (most of them are no-ops
// when DB is nil, but write paths will silently fail).
func SetupTestDB(t *testing.T) {
	t.Helper()

	dsn := "file:test-" + uuid.NewString() + "?mode=memory&cache=shared&_pragma=foreign_keys(1)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.New(log.New(os.Stdout, "[gorm-test] ", log.LstdFlags), logger.Config{
			LogLevel:                  logger.Warn,
			IgnoreRecordNotFoundError: true,
		}),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}

	if err := db.AutoMigrate(
		&Shop{},
		&Barber{},
		&Customer{},
		&Appointment{},
		&Subscription{}, // v4.4 续费测试需要
		&ShopAdmin{},
		&EventLog{},
		&BarberLeave{},
		&Service{}, // v4.4 服务目录
		&RolePermission{}, // v4.7 RBAC 权限表
		&CustomerNotification{}, // v4.10 leave notify 持久化
		&APIKey{},               // v4.12.1 api_access feature
	); err != nil {
		t.Fatalf("AutoMigrate: %v", err)
	}

	DB = db
	// v4.7 RBAC: 测试 DB 也需要 seed 默认 role-permission 映射
	// （生产 InitDB 会跑，测试 SetupTestDB 也要跑）
	if err := SeedDefaultRolePermissions(context.Background()); err != nil {
		t.Fatalf("SeedDefaultRolePermissions: %v", err)
	}

	t.Cleanup(func() {
		if DB != nil {
			if sqlDB, err := DB.DB(); err == nil && sqlDB != nil {
				_ = sqlDB.Close()
			}
		}
		DB = nil
	})
}

// MakeCustomer creates a customer with optional late-cancel and no-show counts.
//
// 注意：wechat_open_id 设置为 UUID（不依赖 name），避免同一 test 多次调用时
// 撞 unique index。
func MakeCustomer(t *testing.T, name string, lateCancelCount, noShowCount int) *Customer {
	t.Helper()
	c := &Customer{
		ID:              uuid.NewString(),
		WechatOpenID:    "wx-" + uuid.NewString(),
		Name:            name,
		LateCancelCount: lateCancelCount,
		NoShowCount:     noShowCount,
		CreatedAt:       time.Now(),
		UpdatedAt:       time.Now(),
	}
	if err := DB.Create(c).Error; err != nil {
		t.Fatalf("create customer: %v", err)
	}
	return c
}

// MakeAppointment creates an appointment with a future time relative to now.
func MakeAppointment(t *testing.T, shopID, customerID, customerName, barberName, apptDate, apptTime string) *Appointment {
	t.Helper()
	a := &Appointment{
		ID:         uuid.NewString(),
		ShopID:     shopID,
		BarberID:   "barber-" + barberName,
		BarberName: barberName,
		CustomerID: customerID,
		Customer:   customerName,
		Date:       apptDate,
		Time:       apptTime,
		Status:     "active",
		Source:     "test",
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}
	if err := DB.Create(a).Error; err != nil {
		t.Fatalf("create appointment: %v", err)
	}
	return a
}

// MakeShop creates a shop record with the given ID and holidays.
func MakeShop(t *testing.T, id, holidays string) *Shop {
	t.Helper()
	s := &Shop{
		ID:        id,
		Name:      "Test Shop " + id,
		Holidays:  holidays,
		Plan:      "basic",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	if err := DB.Create(s).Error; err != nil {
		t.Fatalf("create shop: %v", err)
	}
	return s
}

// MakeBarber creates a barber record with the given ID and shop.
//
//   - id 会同时作为 PrimaryKey，方便测试断言
//   - active 默认 true
func MakeBarber(t *testing.T, id, shopID, name string) *Barber {
	t.Helper()
	b := &Barber{
		ID:        id,
		ShopID:    shopID,
		Name:      name,
		Active:    true,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	if err := DB.Create(b).Error; err != nil {
		t.Fatalf("create barber: %v", err)
	}
	return b
}

// MakeBarberLeave creates a leave record with the given params.
//
// startAt/endAt 直接用调用方传入的 time.Time（便于精确控制未来/过去）。
// status 默认 active。
func MakeBarberLeave(t *testing.T, shopID, barberID string, startAt, endAt time.Time, action string) *BarberLeave {
	t.Helper()
	l := &BarberLeave{
		ID:         uuid.NewString(),
		ShopID:     shopID,
		BarberID:   barberID,
		BarberName: "Test Barber " + barberID,
		StartAt:    startAt,
		EndAt:      endAt,
		Reason:     "test leave",
		Action:     action,
		Status:     LeaveStatusActive,
		CreatedBy:  "test_admin",
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}
	if err := DB.Create(l).Error; err != nil {
		t.Fatalf("create barber leave: %v", err)
	}
	return l
}

// MakeAdminWithRole creates a ShopAdmin with bcrypt password "testpass"（v4.7 RBAC 测试用）
//
//   - 默认 status=active
//   - 不会跟 MakeShop 的固定 ID 撞（ID 用 t.Name() 区分）
//   - PasswordHash 是真实 bcrypt hash，能用 VerifyAdminPassword 校验
//   - username 长度保护：api/members.go 限制 3-32 字符。t.Name() 经常超 32（特别是
//     "TestCreateMember_DuplicateUsername" 这种长方法名 + 嵌套调用），先 panic 早，
//     不让"撞长度校验"这种问题偷偷表现为"测试期望不符"
const (
	testAdminUsernameMin = 3
	testAdminUsernameMax = 32
)

// ShortTestUsername 拼一个 ≤ 32 字符、test 间基本唯一的 username
//
//   - 解决 "t.Name() 经常超 32 撞长度校验" 的 footgun
//   - 格式：prefix + "-" + sha256(t.Name())[:6 bytes 12 hex]
//   - prefix 限制 ≤ 19 字符（prefix(19) + "-"(1) + 12 hex = 32 正好）
//   - 唯一性：sha256 6 bytes = 48 bit，跨 test 碰撞概率极低（10^14 量级）
//   - debug 友好：t.Name() 会在 test failure log 里出现，便于排查
func ShortTestUsername(t *testing.T, prefix string) string {
	t.Helper()
	if len(prefix) > 19 {
		t.Fatalf("ShortTestUsername: prefix %q (%d 字符) 太长，需 ≤ 19", prefix, len(prefix))
	}
	h := sha256.Sum256([]byte(t.Name()))
	return prefix + "-" + hex.EncodeToString(h[:6])
}

func MakeAdminWithRole(t *testing.T, shopID, username, role string) *ShopAdmin {
	t.Helper()
	if n := len(username); n < testAdminUsernameMin || n > testAdminUsernameMax {
		t.Fatalf("MakeAdminWithRole: username 长度 %d 超出 [%d, %d]：%q（t.Name() 太长？用短常量或 hash 截断）",
			n, testAdminUsernameMin, testAdminUsernameMax, username)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte("testpass"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("bcrypt: %v", err)
	}
	a := &ShopAdmin{
		ShopID:       shopID,
		Username:     username,
		PasswordHash: string(hash),
		Role:         role,
		Status:       "active",
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}
	if err := DB.Create(a).Error; err != nil {
		t.Fatalf("create admin: %v", err)
	}
	return a
}