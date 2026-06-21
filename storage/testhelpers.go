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
	"log"
	"os"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
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
		&ShopAdmin{},
		&EventLog{},
	); err != nil {
		t.Fatalf("AutoMigrate: %v", err)
	}

	DB = db
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
func MakeCustomer(t *testing.T, name string, lateCancelCount, noShowCount int) *Customer {
	t.Helper()
	c := &Customer{
		ID:              uuid.NewString(),
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