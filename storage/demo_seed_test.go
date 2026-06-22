package storage

// demo_seed_test.go
//
// v4.5 D3 demo 数据集 — 测试 idempotent + clean

import (
	"context"
	"testing"
)

func TestSeedDemoData_Idempotent(t *testing.T) {
	setupSvcTestDB(t)
	ctx := context.Background()

	// 第一次
	stats1, err := SeedDemoData(ctx, DemoSeedOptions{})
	if err != nil {
		t.Fatalf("第一次 seed 失败: %v", err)
	}
	if stats1.Shops == 0 {
		t.Fatal("应建 ≥1 家店")
	}

	// 第二次（idempotent）
	stats2, err := SeedDemoData(ctx, DemoSeedOptions{})
	if err != nil {
		t.Fatalf("第二次 seed 失败: %v", err)
	}
	if stats2.Shops != stats1.Shops {
		t.Errorf("idempotent 失败：第一次 %d 家，第二次 %d 家", stats1.Shops, stats2.Shops)
	}
}

func TestCleanDemoShops(t *testing.T) {
	setupSvcTestDB(t)
	ctx := context.Background()

	// 1) 先 seed
	_, err := SeedDemoData(ctx, DemoSeedOptions{})
	if err != nil {
		t.Fatalf("seed 失败: %v", err)
	}

	// 2) clean
	n, err := CleanDemoShops(ctx)
	if err != nil {
		t.Fatalf("clean 失败: %v", err)
	}
	if n == 0 {
		t.Errorf("应清掉 ≥1 家")
	}

	// 3) 验证：没 [DEMO] 店了
	var shops []Shop
	DB.Where("name LIKE ?", "[DEMO]%").Find(&shops)
	if len(shops) != 0 {
		t.Errorf("clean 后应有 0 家 [DEMO] 店，实际 %d", len(shops))
	}
}

func TestSeedDemoData_ShopOnly(t *testing.T) {
	setupSvcTestDB(t)
	ctx := context.Background()

	stats, err := SeedDemoData(ctx, DemoSeedOptions{ShopOnly: true})
	if err != nil {
		t.Fatalf("seed failed: %v", err)
	}
	if stats.Shops == 0 {
		t.Fatal("应建店")
	}
	if stats.Appointments != 0 {
		t.Errorf("shop-only 不应建预约，实际 %d", stats.Appointments)
	}

	// 验证：店有 service + barber
	var shop Shop
	DB.Where("name LIKE ?", "[DEMO]%").First(&shop)
	if shop.ID == "" {
		t.Fatal("店未建")
	}
	var svcs []Service
	DB.Where("shop_id = ?", shop.ID).Find(&svcs)
	if len(svcs) < 5 {
		t.Errorf("应 seed ≥5 项服务，实际 %d", len(svcs))
	}
}

func TestSeedDemoData_DoesNotAffectDefaultShop(t *testing.T) {
	setupSvcTestDB(t)
	ctx := context.Background()
	// 提前建一个非 DEMO 店
	MakeShop(t, "real-shop", "")

	_, err := SeedDemoData(ctx, DemoSeedOptions{})
	if err != nil {
		t.Fatalf("seed failed: %v", err)
	}

	// 验证：real-shop 还在
	var real Shop
	DB.Where("id = ?", "real-shop").First(&real)
	if real.ID == "" {
		t.Error("real-shop 不应被 demo 删掉")
	}
	if real.Name != "Test Shop real-shop" {
		t.Errorf("real-shop 数据不对: %+v", real)
	}
}
