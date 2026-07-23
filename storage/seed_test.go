package storage

import (
	"context"
	"testing"
)

func TestSeedDefaultData_CreatesDemoData(t *testing.T) {
	SetupTestDB(t)
	t.Setenv("DEFAULT_SHOP_ID", "demo-shop")
	t.Setenv("DEFAULT_SHOP_NAME", "Demo Shop")
	t.Setenv("DEFAULT_ADMIN_USERNAME", "demo-admin")
	t.Setenv("DEFAULT_ADMIN_PASSWORD", "demo-password")
	t.Setenv("DEFAULT_PLATFORM_ADMIN_USERNAME", "demo-platform")
	t.Setenv("DEFAULT_PLATFORM_ADMIN_PASSWORD", "demo-platform-password")

	if err := SeedDefaultData(context.Background()); err != nil {
		t.Fatalf("SeedDefaultData: %v", err)
	}

	var shop Shop
	if err := DB.First(&shop, "id = ?", "demo-shop").Error; err != nil {
		t.Fatalf("default shop was not created: %v", err)
	}

	var barberCount int64
	if err := DB.Model(&Barber{}).Where("shop_id = ? AND active = ?", shop.ID, true).Count(&barberCount).Error; err != nil {
		t.Fatalf("count demo barbers: %v", err)
	}
	if barberCount != 2 {
		t.Fatalf("demo barber count = %d, want 2", barberCount)
	}

	var serviceCount int64
	if err := DB.Model(&Service{}).Where("shop_id = ? AND is_active = ?", shop.ID, true).Count(&serviceCount).Error; err != nil {
		t.Fatalf("count demo services: %v", err)
	}
	if serviceCount == 0 {
		t.Fatal("demo services were not created")
	}
}
