//go:build mysql_integration

package main

import (
	"context"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/yuterigele/openbook/sdk/booking/v1alpha1"
	"github.com/yuterigele/openbook/sdk/toolkit"
	"github.com/yuterigele/openbook/storage"
)

func TestStarterWebChatPersistsThroughMySQLCore(t *testing.T) {
	if os.Getenv("MYSQL_DSN") == "" && os.Getenv("MYSQL_HOST") == "" {
		t.Skip("设置 MYSQL_DSN 或 MYSQL_HOST 后运行 MySQL 集成测试")
	}
	db, err := storage.InitDB(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	defer func() { storage.DB = nil }()

	merchantID := "starter-mysql-" + uuid.NewString()
	locationID := "location-" + uuid.NewString()
	customerID := "customer-" + uuid.NewString()
	resolver := func(request *http.Request) (v1alpha1.ExecutionContext, error) {
		return v1alpha1.ExecutionContext{
			MerchantID: merchantID, LocationID: locationID, CustomerID: customerID,
			PrincipalID: "mysql-test", Permissions: []v1alpha1.Permission{
				v1alpha1.PermissionBookingRead, v1alpha1.PermissionBookingWrite,
			}, TraceID: "mysql-trace", IdempotencyKey: request.Header.Get("Idempotency-Key"),
		}, nil
	}
	application := sqliteStarterApplication{}
	registry, err := NewStarterRegistry(application, StarterProfile())
	if err != nil {
		t.Fatal(err)
	}
	startAt := time.Now().Add(48 * time.Hour).Format(time.RFC3339)
	result := postChat(t, NewChatHandler(registry, resolver), "create_booking", map[string]any{
		"service_id": "wash_and_trim", "start_at": startAt,
	}, "mysql-booking-"+uuid.NewString())
	if result.Result.Status != toolkit.StatusOK {
		t.Fatalf("mysql booking failed: %+v", result.Result)
	}
	var count int64
	if err := storage.DB.Model(&storage.BookingRecord{}).Where("merchant_id = ? AND location_id = ? AND customer_id = ?", merchantID, locationID, customerID).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("mysql booking count = %d, want 1", count)
	}
}
