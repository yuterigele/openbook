package storage

// service_crud_test.go
//
// v4.4 服务目录 CRUD 的测试套件
//
// 覆盖：
//   - CreateService：成功 / 重名 / 字段校验
//   - ListServicesByShop：includeInactive 行为
//   - UpdateService：成功 / 跨店 / 同名检测
//   - Deactivate / Activate：幂等性
//   - 多店隔离：跨店读写

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func setupSvcTestDB(t *testing.T) {
	t.Helper()
	SetupTestDB(t)
}

func TestCreateService_Success(t *testing.T) {
	setupSvcTestDB(t)
	ctx := context.Background()
	s, err := CreateService(ctx, "test-shop-svc-1", "剪发", 30, "30-50")
	if err != nil {
		t.Fatalf("CreateService 失败: %v", err)
	}
	if s.ID == "" || s.ShopID != "test-shop-svc-1" || s.Name != "剪发" || s.EstimatedMin != 30 || !s.IsActive {
		t.Errorf("返回值异常: %+v", s)
	}
}

func TestCreateService_DuplicateName(t *testing.T) {
	setupSvcTestDB(t)
	ctx := context.Background()
	if _, err := CreateService(ctx, "test-shop-svc-dup", "烫发", 90, "180-380"); err != nil {
		t.Fatalf("首次创建失败: %v", err)
	}
	_, err := CreateService(ctx, "test-shop-svc-dup", "烫发", 90, "180-380")
	if !errors.Is(err, ErrServiceNameTaken) {
		t.Fatalf("第二次同名应该返回 ErrServiceNameTaken，实际: %v", err)
	}
}

func TestCreateService_ValidationErrors(t *testing.T) {
	setupSvcTestDB(t)
	ctx := context.Background()
	cases := []struct {
		label     string
		shopID    string
		svcName   string
		min       int
		price     string
		wantSubstr string
	}{
		{"empty name", "test-svc-empty", "", 30, "30", "不能为空"},
		{"name too long", "test-svc-long", strings.Repeat("剪", 33), 30, "30", "过长"},
		{"min too small", "test-svc-min0", "剪发", 0, "30", "1-480"},
		{"min too large", "test-svc-min999", "剪发", 999, "30", "1-480"},
		{"price too long", "test-svc-pricelong", "剪发", 30, strings.Repeat("1", 65), "过长"},
		{"empty shop", "", "剪发", 30, "30", "shop_id"},
	}
	for _, c := range cases {
		t.Run(c.label, func(t *testing.T) {
			_, err := CreateService(ctx, c.shopID, c.svcName, c.min, c.price)
			if err == nil {
				t.Fatalf("期望报错，实际通过")
			}
			if !strings.Contains(err.Error(), c.wantSubstr) {
				t.Errorf("错误信息不符合预期（want substring %q）: %v", c.wantSubstr, err)
			}
		})
	}
}

func TestListServicesByShop_OrderAndFilter(t *testing.T) {
	setupSvcTestDB(t)
	ctx := context.Background()
	shopID := "test-svc-list"

	// 建 3 个 active + 1 个 inactive
	for _, n := range []string{"A", "B", "C"} {
		if _, err := CreateService(ctx, shopID, n, 30, "10"); err != nil {
			t.Fatalf("seed %s 失败: %v", n, err)
		}
	}
	d, err := CreateService(ctx, shopID, "Inactive", 30, "0")
	if err != nil {
		t.Fatalf("seed Inactive 失败: %v", err)
	}
	if err := DeactivateService(ctx, shopID, d.ID); err != nil {
		t.Fatalf("Deactivate 失败: %v", err)
	}

	all, err := ListServicesByShop(ctx, shopID, true)
	if err != nil {
		t.Fatalf("List includeInactive=true 失败: %v", err)
	}
	if len(all) != 4 {
		t.Errorf("includeInactive=true 应返回 4 条，实际 %d", len(all))
	}
	active, err := ListServicesByShop(ctx, shopID, false)
	if err != nil {
		t.Fatalf("List includeInactive=false 失败: %v", err)
	}
	if len(active) != 3 {
		t.Errorf("includeInactive=false 应返回 3 条，实际 %d", len(active))
	}
	for _, s := range active {
		if !s.IsActive {
			t.Errorf("active 列表里出现了 inactive service: %+v", s)
		}
	}
	// 排序：sort_order asc, id asc
	for i := 1; i < len(all); i++ {
		if all[i].SortOrder < all[i-1].SortOrder {
			t.Errorf("排序错乱：%d (%d) 后跟 %d (%d)", i-1, all[i-1].SortOrder, i, all[i].SortOrder)
		}
	}
}

func TestGetServiceInShop_ShopIsolation(t *testing.T) {
	setupSvcTestDB(t)
	ctx := context.Background()
	s, err := CreateService(ctx, "shop-A-iso", "染发", 90, "200")
	if err != nil {
		t.Fatalf("CreateService 失败: %v", err)
	}
	// 用其他 shop 查应 404
	if _, err := GetServiceInShop(ctx, "shop-B-iso", s.ID); !errors.Is(err, ErrServiceNotFoundInShop) {
		t.Errorf("跨店查询应返回 ErrServiceNotFoundInShop，实际: %v", err)
	}
	// 用本店查应成功
	got, err := GetServiceInShop(ctx, "shop-A-iso", s.ID)
	if err != nil {
		t.Errorf("本店查询应成功: %v", err)
	}
	if got.Name != "染发" {
		t.Errorf("查回的数据不对: %+v", got)
	}
}

func TestUpdateService_Success(t *testing.T) {
	setupSvcTestDB(t)
	ctx := context.Background()
	shopID := "test-svc-upd"
	s, err := CreateService(ctx, shopID, "原名", 30, "10")
	if err != nil {
		t.Fatalf("seed 失败: %v", err)
	}
	got, err := UpdateService(ctx, shopID, s.ID, "新名", 45, "20-30", 99)
	if err != nil {
		t.Fatalf("UpdateService 失败: %v", err)
	}
	if got.Name != "新名" || got.EstimatedMin != 45 || got.PriceRange != "20-30" || got.SortOrder != 99 {
		t.Errorf("更新后字段不对: %+v", got)
	}
}

func TestUpdateService_DuplicateName(t *testing.T) {
	setupSvcTestDB(t)
	ctx := context.Background()
	shopID := "test-svc-dup-upd"
	if _, err := CreateService(ctx, shopID, "甲", 30, ""); err != nil {
		t.Fatalf("seed 甲失败: %v", err)
	}
	bb, err := CreateService(ctx, shopID, "乙", 30, "")
	if err != nil {
		t.Fatalf("seed 乙失败: %v", err)
	}
	// 改 乙 → 甲（撞名）
	_, err = UpdateService(ctx, shopID, bb.ID, "甲", 30, "", 0)
	if !errors.Is(err, ErrServiceNameTaken) {
		t.Fatalf("改名为已存在应返回 ErrServiceNameTaken，实际: %v", err)
	}
}

func TestDeactivateActivate_Idempotent(t *testing.T) {
	setupSvcTestDB(t)
	ctx := context.Background()
	shopID := "test-svc-toggle"
	s, err := CreateService(ctx, shopID, "toggle", 30, "")
	if err != nil {
		t.Fatalf("seed 失败: %v", err)
	}
	// 第一次 deactivate
	if err := DeactivateService(ctx, shopID, s.ID); err != nil {
		t.Fatalf("首次 deactivate 失败: %v", err)
	}
	got, _ := GetServiceInShop(ctx, shopID, s.ID)
	if got.IsActive {
		t.Errorf("deactivate 后 is_active 应为 false")
	}
	// 第二次 deactivate 应幂等成功
	if err := DeactivateService(ctx, shopID, s.ID); err != nil {
		t.Errorf("重复 deactivate 应幂等，实际: %v", err)
	}
	// activate
	if err := ActivateService(ctx, shopID, s.ID); err != nil {
		t.Fatalf("activate 失败: %v", err)
	}
	got, _ = GetServiceInShop(ctx, shopID, s.ID)
	if !got.IsActive {
		t.Errorf("activate 后 is_active 应为 true")
	}
	// 重复 activate
	if err := ActivateService(ctx, shopID, s.ID); err != nil {
		t.Errorf("重复 activate 应幂等，实际: %v", err)
	}
}

func TestDeactivateService_NotFound(t *testing.T) {
	setupSvcTestDB(t)
	ctx := context.Background()
	if err := DeactivateService(ctx, "any-shop", "non-existent-id"); !errors.Is(err, ErrServiceNotFoundInShop) {
		t.Errorf("不存在的 service 应返回 ErrServiceNotFoundInShop，实际: %v", err)
	}
}

func TestCountServices(t *testing.T) {
	setupSvcTestDB(t)
	ctx := context.Background()
	shopID := "test-svc-count"
	if n := CountServices(ctx, shopID); n != 0 {
		t.Errorf("空店 service 数应为 0，实际 %d", n)
	}
	if _, err := CreateService(ctx, shopID, "x", 30, ""); err != nil {
		t.Fatalf("seed 失败: %v", err)
	}
	if _, err := CreateService(ctx, shopID, "y", 30, ""); err != nil {
		t.Fatalf("seed 失败: %v", err)
	}
	if n := CountServices(ctx, shopID); n != 2 {
		t.Errorf("建了 2 个 service，Count 应为 2，实际 %d", n)
	}
}
