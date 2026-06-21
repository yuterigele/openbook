package storage

// customer_tags_test.go
//
// Tests for the customer tag system:
//   - TagSet: pure logic for in-memory tag set (Has / Add / Remove / String)
//   - Customer.IsBlacklisted / IsVIP / IsFrequent: tag-derived predicates
//   - AddCustomerTag / RemoveCustomerTag: DB persistence (idempotent)
//
// Run:
//   go test ./storage/... -v -run "TestTagSet|TestCustomer_Is|TestAddCustomerTag|TestRemoveCustomerTag"

import (
	"strings"
	"testing"

	"github.com/google/uuid"
)

// ===================== TagSet (pure logic) =====================

func TestTagSet_EmptyAndNil(t *testing.T) {
	ts := NewTagSet("")
	if ts.Has("VIP") {
		t.Error("empty TagSet should not have VIP")
	}
	if ts.String() != "" {
		t.Errorf("empty TagSet String = %q, want \"\"", ts.String())
	}

	// nil receiver must not panic
	var nilTS *TagSet
	if nilTS.Has("anything") {
		t.Error("nil TagSet should not have anything")
	}
	if nilTS.String() != "" {
		t.Error("nil TagSet String should be empty")
	}
}

func TestTagSet_ParseCSV(t *testing.T) {
	ts := NewTagSet("VIP, FREQUENT , BLACKLIST")
	for _, want := range []string{"VIP", "FREQUENT", "BLACKLIST"} {
		if !ts.Has(want) {
			t.Errorf("missing %s after parse", want)
		}
	}
	if ts.Has("UNKNOWN") {
		t.Error("UNKNOWN should not be in set")
	}
}

func TestTagSet_AddRemove(t *testing.T) {
	ts := NewTagSet("")
	ts.Add("VIP")
	if !ts.Has("VIP") {
		t.Error("VIP missing after Add")
	}
	// Add same again is idempotent
	ts.Add("VIP")
	if got := strings.Count(ts.String(), ","); got != 0 {
		// single tag has no comma
		t.Errorf("duplicates in String(): %q", ts.String())
	}
	ts.Add("FREQUENT")
	if !ts.Has("FREQUENT") {
		t.Error("FREQUENT missing")
	}
	ts.Remove("VIP")
	if ts.Has("VIP") {
		t.Error("VIP still present after Remove")
	}
	if !ts.Has("FREQUENT") {
		t.Error("FREQUENT should remain after removing VIP")
	}
}

func TestTagSet_StringFormat(t *testing.T) {
	ts := NewTagSet("")
	ts.Add("BLACKLIST")
	ts.Add("VIP")
	got := ts.String()
	// Order is map iteration, so just check both tags present
	if !strings.Contains(got, "VIP") || !strings.Contains(got, "BLACKLIST") {
		t.Errorf("String = %q, want both VIP and BLACKLIST", got)
	}
	parts := strings.Split(got, ",")
	if len(parts) != 2 {
		t.Errorf("expected 2 tags, got %d in %q", len(parts), got)
	}
}

// ===================== Customer predicates =====================

func TestCustomer_Is_Predicates(t *testing.T) {
	cases := []struct {
		name          string
		tags          string
		isBlacklisted bool
		isVIP         bool
		isFrequent    bool
	}{
		{"no tags", "", false, false, false},
		{"only VIP", "VIP", false, true, false},
		{"only BLACKLIST", "BLACKLIST", true, false, false},
		{"VIP + FREQUENT", "VIP,FREQUENT", false, true, true},
		{"all three", "VIP,FREQUENT,BLACKLIST", true, true, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cust := &Customer{Tags: c.tags}
			if got := cust.IsBlacklisted(); got != c.isBlacklisted {
				t.Errorf("IsBlacklisted = %v, want %v", got, c.isBlacklisted)
			}
			if got := cust.IsVIP(); got != c.isVIP {
				t.Errorf("IsVIP = %v, want %v", got, c.isVIP)
			}
			if got := cust.IsFrequent(); got != c.isFrequent {
				t.Errorf("IsFrequent = %v, want %v", got, c.isFrequent)
			}
		})
	}
}

// ===================== AddCustomerTag / RemoveCustomerTag (DB) =====================

func TestAddCustomerTag_NewTag(t *testing.T) {
	SetupTestDB(t)
	cust := MakeCustomer(t, "Alice", 0, 0)

	if err := AddCustomerTag(WithCtx(), cust.ID, "VIP"); err != nil {
		t.Fatalf("AddCustomerTag: %v", err)
	}
	var got Customer
	if err := DB.First(&got, "id = ?", cust.ID).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if !got.IsVIP() {
		t.Errorf("expected VIP after add, got tags=%q", got.Tags)
	}
}

func TestAddCustomerTag_Idempotent(t *testing.T) {
	SetupTestDB(t)
	cust := MakeCustomer(t, "Bob", 0, 0)

	if err := AddCustomerTag(WithCtx(), cust.ID, "VIP"); err != nil {
		t.Fatalf("first add: %v", err)
	}
	if err := AddCustomerTag(WithCtx(), cust.ID, "VIP"); err != nil {
		t.Fatalf("second add: %v", err)
	}
	var got Customer
	DB.First(&got, "id = ?", cust.ID)
	ts := NewTagSet(got.Tags)
	if strings.Count(got.Tags, "VIP") != 1 {
		t.Errorf("expected single VIP, got tags=%q", got.Tags)
	}
	if !ts.Has("VIP") {
		t.Errorf("VIP missing, got %q", got.Tags)
	}
}

func TestAddCustomerTag_MultipleDistinct(t *testing.T) {
	SetupTestDB(t)
	cust := MakeCustomer(t, "Carol", 0, 0)

	for _, tag := range []string{"VIP", "FREQUENT"} {
		if err := AddCustomerTag(WithCtx(), cust.ID, tag); err != nil {
			t.Fatalf("AddCustomerTag(%s): %v", tag, err)
		}
	}
	var got Customer
	DB.First(&got, "id = ?", cust.ID)
	if !got.IsVIP() || !got.IsFrequent() {
		t.Errorf("expected VIP+FREQUENT, got tags=%q", got.Tags)
	}
}

func TestAddCustomerTag_CustomerNotFound(t *testing.T) {
	SetupTestDB(t)
	err := AddCustomerTag(WithCtx(), uuid.NewString(), "VIP")
	if err == nil {
		t.Error("expected error for non-existent customer, got nil")
	}
}

func TestRemoveCustomerTag_Existing(t *testing.T) {
	SetupTestDB(t)
	cust := MakeCustomer(t, "Dave", 0, 0)

	// seed VIP+FREQUENT, then remove VIP
	_ = AddCustomerTag(WithCtx(), cust.ID, "VIP")
	_ = AddCustomerTag(WithCtx(), cust.ID, "FREQUENT")

	if err := RemoveCustomerTag(WithCtx(), cust.ID, "VIP"); err != nil {
		t.Fatalf("RemoveCustomerTag: %v", err)
	}
	var got Customer
	DB.First(&got, "id = ?", cust.ID)
	if got.IsVIP() {
		t.Errorf("VIP should be gone, got tags=%q", got.Tags)
	}
	if !got.IsFrequent() {
		t.Errorf("FREQUENT should remain, got tags=%q", got.Tags)
	}
}

func TestRemoveCustomerTag_NotPresent(t *testing.T) {
	SetupTestDB(t)
	cust := MakeCustomer(t, "Eve", 0, 0)

	// Removing a tag the customer doesn't have should be a no-op (no error)
	if err := RemoveCustomerTag(WithCtx(), cust.ID, "BLACKLIST"); err != nil {
		t.Errorf("RemoveCustomerTag of missing tag should be no-op, got error: %v", err)
	}
}

func TestRemoveCustomerTag_LastTag(t *testing.T) {
	SetupTestDB(t)
	cust := MakeCustomer(t, "Frank", 0, 0)

	_ = AddCustomerTag(WithCtx(), cust.ID, "VIP")
	if err := RemoveCustomerTag(WithCtx(), cust.ID, "VIP"); err != nil {
		t.Fatalf("RemoveCustomerTag: %v", err)
	}
	var got Customer
	DB.First(&got, "id = ?", cust.ID)
	if got.Tags != "" {
		t.Errorf("expected empty tags after removing only tag, got %q", got.Tags)
	}
}

func TestRemoveCustomerTag_CustomerNotFound(t *testing.T) {
	SetupTestDB(t)
	err := RemoveCustomerTag(WithCtx(), uuid.NewString(), "VIP")
	if err == nil {
		t.Error("expected error for non-existent customer, got nil")
	}
}

// ===================== isCustomerBlacklistedByTx (黑名单查询修复) =====================
//
// 回归测试：原先 `isCustomerBlacklistedByTx` 在 Customer 表上用了不存在的
// `shop_id` 列做过滤，SQLite/MySQL 都报 "no such column: shop_id" warning。
// v3.8 修复：黑名单是按顾客维度（跨店共享），去掉 shop_id 过滤。
//
// 这些测试不依赖事务（直接传 DB），但保持函数签名一致以验证修复。

func TestIsCustomerBlacklistedByTx_PhoneMatch(t *testing.T) {
	SetupTestDB(t)
	// 顾客 A 在黑名单里，有手机号
	black := MakeCustomer(t, "Bad", 0, 0)
	DB.Model(black).Updates(map[string]any{
		"phone": "13800000000",
		"tags":  TagBlacklist,
	})
	// 顾客 B 正常
	_ = MakeCustomer(t, "Good", 0, 0)

	if got := isCustomerBlacklistedByTx(DB, "13800000000", "shop-X"); !got {
		t.Error("按 phone 匹配黑名单应返回 true")
	}
}

func TestIsCustomerBlacklistedByTx_NameFallback(t *testing.T) {
	SetupTestDB(t)
	black := MakeCustomer(t, "李黑", 0, 0)
	DB.Model(black).Update("tags", TagBlacklist) // 没手机号，靠名字匹配

	if got := isCustomerBlacklistedByTx(DB, "李黑", "shop-Y"); !got {
		t.Error("按 name fallback 匹配黑名单应返回 true")
	}
}

func TestIsCustomerBlacklistedByTx_NoMatch(t *testing.T) {
	SetupTestDB(t)
	black := MakeCustomer(t, "李黑", 0, 0)
	DB.Model(black).Updates(map[string]any{
		"phone": "13800000000",
		"tags":  TagBlacklist,
	})

	if got := isCustomerBlacklistedByTx(DB, "13800001111", "shop-Z"); got {
		t.Error("陌生 phone 不应命中黑名单")
	}
}

func TestIsCustomerBlacklistedByTx_EmptyCustomerNoOp(t *testing.T) {
	SetupTestDB(t)
	// 空 customer 直接返回 false，不查 DB
	if got := isCustomerBlacklistedByTx(DB, "", "shop-1"); got {
		t.Error("空 customer 应返回 false（短路）")
	}
}

func TestIsCustomerBlacklistedByTx_ShopIDAccepted(t *testing.T) {
	SetupTestDB(t)
	// 关键回归：调用时传 shopID 不能 SQL 报错（之前会触发 shop_id 列警告）
	black := MakeCustomer(t, "Mallory", 0, 0)
	DB.Model(black).Updates(map[string]any{
		"phone": "13700000000",
		"tags":  TagBlacklist,
	})

	// 不应 panic，不应 error；正确返回 true
	got := isCustomerBlacklistedByTx(DB, "13700000000", "shop-连锁-A")
	if !got {
		t.Error("跨店查询黑名单应仍然命中（黑名单是全局的）")
	}
}