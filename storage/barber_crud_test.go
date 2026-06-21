package storage

// barber_crud_test.go
//
// 理发师 CRUD 单测（P5 商户后台理发师管理）
//
// 覆盖：
//   1. CreateBarber happy path + ID 自动生成 + active=true
//   2. CreateBarber 同名 active  → ErrBarberNameTaken
//   3. CreateBarber 同名 inactive → ErrBarberNameTaken（防止绕过软删除恢复路径）
//   4. CreateBarber 空 name → error
//   5. CreateBarber 超长 name（>32） → error
//   6. CreateBarber 超长 skills（>256） → error
//   7. CreateBarber 自动 trim 前后空白
//   8. GetBarberInShop happy path
//   9. GetBarberInShop 跨店 → ErrBarberNotFoundInShop（不泄漏存在性）
//  10. ListAllBarbersByShop 默认只 active
//  11. ListAllBarbersByShop includeInactive=true 含 inactive
//  12. SoftDeleteBarber 无未来预约 → OK + active=false
//  13. SoftDeleteBarber 有未来 active 预约 → error
//  14. SoftDeleteBarber 跨店 → ErrBarberNotFoundInShop
//  15. SoftDeleteBarber 幂等（已 inactive 状态再调用不报错）
//  16. ActivateBarber OK
//  17. ActivateBarber 幂等（已 active 状态再调用不报错）
//  18. UpdateBarberSkills OK + 超长 → error
//
// 隔离：每个 test 用 fresh shop + fresh barber（除非显式复用）。
// Run:
//   go test ./storage/... -v -run "TestCreateBarber|TestGetBarberInShop|TestListAllBarbersByShop|TestSoftDeleteBarber|TestActivateBarber|TestUpdateBarberSkills"

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// ---- CreateBarber ----

func TestCreateBarber_HappyPath(t *testing.T) {
	shopID := uuidNewShop(t)
	b, err := CreateBarber(context.Background(), shopID, "Tony", "剪发,染发")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if b.ID == "" {
		t.Errorf("ID should be auto-generated")
	}
	if b.ShopID != shopID {
		t.Errorf("ShopID: got %q want %q", b.ShopID, shopID)
	}
	if b.Name != "Tony" {
		t.Errorf("Name: got %q want %q", b.Name, "Tony")
	}
	if !b.Active {
		t.Errorf("Active should default true")
	}
	if b.CreatedAt.IsZero() || b.UpdatedAt.IsZero() {
		t.Errorf("timestamps should be set")
	}
}

func TestCreateBarber_TrimsWhitespace(t *testing.T) {
	shopID := uuidNewShop(t)
	b, err := CreateBarber(context.Background(), shopID, "  Kevin  ", "剪发")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if b.Name != "Kevin" {
		t.Errorf("Name should be trimmed: got %q", b.Name)
	}
}

func TestCreateBarber_DuplicateActiveName(t *testing.T) {
	shopID := uuidNewShop(t)
	if _, err := CreateBarber(context.Background(), shopID, "Tony", ""); err != nil {
		t.Fatalf("first create: %v", err)
	}
	_, err := CreateBarber(context.Background(), shopID, "Tony", "")
	if !errors.Is(err, ErrBarberNameTaken) {
		t.Errorf("expected ErrBarberNameTaken, got %v", err)
	}
}

func TestCreateBarber_DuplicateInactiveName(t *testing.T) {
	shopID := uuidNewShop(t)
	b, err := CreateBarber(context.Background(), shopID, "Tony", "")
	if err != nil {
		t.Fatalf("first create: %v", err)
	}
	if err := SoftDeleteBarber(context.Background(), shopID, b.ID); err != nil {
		t.Fatalf("soft delete: %v", err)
	}
	_, err = CreateBarber(context.Background(), shopID, "Tony", "")
	if !errors.Is(err, ErrBarberNameTaken) {
		t.Errorf("inactive 同名也应该拒绝 (防止绕过恢复路径): got %v", err)
	}
}

func TestCreateBarber_EmptyName(t *testing.T) {
	shopID := uuidNewShop(t)
	_, err := CreateBarber(context.Background(), shopID, "", "")
	if err == nil {
		t.Errorf("expected error for empty name")
	}
	if errors.Is(err, ErrBarberNameTaken) {
		t.Errorf("should be validation error, not duplicate")
	}
}

func TestCreateBarber_NameTooLong(t *testing.T) {
	shopID := uuidNewShop(t)
	longName := strings.Repeat("x", 33)
	_, err := CreateBarber(context.Background(), shopID, longName, "")
	if err == nil {
		t.Errorf("expected error for name > 32 chars")
	}
}

func TestCreateBarber_SkillsTooLong(t *testing.T) {
	shopID := uuidNewShop(t)
	longSkills := strings.Repeat("x", 257)
	_, err := CreateBarber(context.Background(), shopID, "Tony", longSkills)
	if err == nil {
		t.Errorf("expected error for skills > 256 chars")
	}
}

func TestCreateBarber_DifferentShopsSameName(t *testing.T) {
	// Barber.Name 是全局 uniqueIndex，所以即使跨店也不能重名
	// 这是 schema 约束；测试期望同名跨店创建会失败（unique 兜底）
	shop1 := uuidNewShop(t)
	shop2 := uuidNewShop(t)
	if _, err := CreateBarber(context.Background(), shop1, "Tony", ""); err != nil {
		t.Fatalf("shop1 create: %v", err)
	}
	_, err := CreateBarber(context.Background(), shop2, "Tony", "")
	if !errors.Is(err, ErrBarberNameTaken) {
		t.Errorf("expected ErrBarberNameTaken (全局 unique), got %v", err)
	}
}

// ---- GetBarberInShop ----

func TestGetBarberInShop_HappyPath(t *testing.T) {
	shopID := uuidNewShop(t)
	created, _ := CreateBarber(context.Background(), shopID, "Tony", "")
	got, err := GetBarberInShop(context.Background(), shopID, created.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.ID != created.ID || got.Name != "Tony" {
		t.Errorf("got %+v want %+v", got, created)
	}
}

func TestGetBarberInShop_CrossShopForbidden(t *testing.T) {
	shop1 := uuidNewShop(t)
	shop2 := uuidNewShop(t)
	b, _ := CreateBarber(context.Background(), shop1, "Tony", "")
	_, err := GetBarberInShop(context.Background(), shop2, b.ID)
	if !errors.Is(err, ErrBarberNotFoundInShop) {
		t.Errorf("expected ErrBarberNotFoundInShop (不泄漏存在性), got %v", err)
	}
}

func TestGetBarberInShop_NotFound(t *testing.T) {
	shopID := uuidNewShop(t)
	_, err := GetBarberInShop(context.Background(), shopID, "no-such-id")
	if !errors.Is(err, ErrBarberNotFoundInShop) {
		t.Errorf("expected ErrBarberNotFoundInShop, got %v", err)
	}
}

// ---- ListAllBarbersByShop ----

func TestListAllBarbersByShop_ActiveOnly(t *testing.T) {
	shopID := uuidNewShop(t)
	t1, _ := CreateBarber(context.Background(), shopID, "Tony", "")
	_, _ = CreateBarber(context.Background(), shopID, "Kevin", "")
	_, _ = CreateBarber(context.Background(), shopID, "Bob", "")
	if err := SoftDeleteBarber(context.Background(), shopID, t1.ID); err != nil {
		t.Fatalf("delete t1: %v", err)
	}

	out, err := ListAllBarbersByShop(context.Background(), shopID, false)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(out) != 2 {
		t.Errorf("expected 2 active barbers, got %d", len(out))
	}
	for _, b := range out {
		if !b.Active {
			t.Errorf("inactive barber leaked: %+v", b)
		}
	}
	// active desc, name asc：Bob → Kevin（按字母升序 B < K）
	if len(out) >= 2 {
		if out[0].Name != "Bob" {
			t.Errorf("expected name-asc Bob first: got %q", out[0].Name)
		}
		if out[1].Name != "Kevin" {
			t.Errorf("expected Kevin second: got %q", out[1].Name)
		}
	}
}

func TestListAllBarbersByShop_IncludeInactive(t *testing.T) {
	shopID := uuidNewShop(t)
	t1, _ := CreateBarber(context.Background(), shopID, "Tony", "")
	_, _ = CreateBarber(context.Background(), shopID, "Kevin", "")
	if err := SoftDeleteBarber(context.Background(), shopID, t1.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	out, err := ListAllBarbersByShop(context.Background(), shopID, true)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(out) != 2 {
		t.Errorf("expected 2 (including inactive), got %d", len(out))
	}
	// active desc 排序：active 优先
	if !out[0].Active {
		t.Errorf("active barber should come first")
	}
}

// ---- SoftDeleteBarber ----

func TestSoftDeleteBarber_NoFutureAppts_OK(t *testing.T) {
	shopID := uuidNewShop(t)
	b, _ := CreateBarber(context.Background(), shopID, "Tony", "")
	if err := SoftDeleteBarber(context.Background(), shopID, b.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	// 复查
	got, _ := GetBarberInShop(context.Background(), shopID, b.ID)
	if got.Active {
		t.Errorf("active should be false after delete")
	}
}

func TestSoftDeleteBarber_WithFutureAppt_Fails(t *testing.T) {
	shopID := uuidNewShop(t)
	// 用 MakeBarber：约定 ID="barber-Tony"，这样 MakeAppointment 里
	// "barber-" + "Tony" = "barber-Tony" 跟 barber ID 匹配上
	MakeBarber(t, "barber-Tony", shopID, "Tony")
	cust := MakeCustomer(t, "Alice", 0, 0)
	// 未来 active 预约：明天 14:00
	futureDate := time.Now().Add(24 * time.Hour).Format("2006-01-02")
	MakeAppointment(t, shopID, cust.ID, cust.Name, "Tony", futureDate, "14:00")

	err := SoftDeleteBarber(context.Background(), shopID, "barber-Tony")
	if err == nil {
		t.Fatalf("expected error when barber has future active appt, got nil")
	}
	if !strings.Contains(err.Error(), "未来") {
		t.Errorf("error msg should mention 未来预约, got: %v", err)
	}
}

func TestSoftDeleteBarber_WithPastAppt_OK(t *testing.T) {
	shopID := uuidNewShop(t)
	b, _ := CreateBarber(context.Background(), shopID, "Tony", "")
	cust := MakeCustomer(t, "Bob", 0, 0)
	// 过去 active 预约：昨天 10:00（虽然应该被爽约扫描处理掉，但模型上仍 active）
	pastDate := time.Now().Add(-24 * time.Hour).Format("2006-01-02")
	MakeAppointment(t, shopID, cust.ID, cust.Name, b.ID, pastDate, "10:00")

	if err := SoftDeleteBarber(context.Background(), shopID, b.ID); err != nil {
		t.Errorf("past appt should not block delete, got: %v", err)
	}
}

func TestSoftDeleteBarber_Idempotent(t *testing.T) {
	shopID := uuidNewShop(t)
	b, _ := CreateBarber(context.Background(), shopID, "Tony", "")
	if err := SoftDeleteBarber(context.Background(), shopID, b.ID); err != nil {
		t.Fatalf("first delete: %v", err)
	}
	if err := SoftDeleteBarber(context.Background(), shopID, b.ID); err != nil {
		t.Errorf("second delete should be no-op, got: %v", err)
	}
}

func TestSoftDeleteBarber_CrossShopForbidden(t *testing.T) {
	shop1 := uuidNewShop(t)
	shop2 := uuidNewShop(t)
	b, _ := CreateBarber(context.Background(), shop1, "Tony", "")
	err := SoftDeleteBarber(context.Background(), shop2, b.ID)
	if !errors.Is(err, ErrBarberNotFoundInShop) {
		t.Errorf("expected ErrBarberNotFoundInShop, got %v", err)
	}
}

// ---- ActivateBarber ----

func TestActivateBarber_AfterDelete_OK(t *testing.T) {
	shopID := uuidNewShop(t)
	b, _ := CreateBarber(context.Background(), shopID, "Tony", "")
	if err := SoftDeleteBarber(context.Background(), shopID, b.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := ActivateBarber(context.Background(), shopID, b.ID); err != nil {
		t.Fatalf("activate: %v", err)
	}
	got, _ := GetBarberInShop(context.Background(), shopID, b.ID)
	if !got.Active {
		t.Errorf("should be active after ActivateBarber")
	}
}

func TestActivateBarber_Idempotent(t *testing.T) {
	shopID := uuidNewShop(t)
	b, _ := CreateBarber(context.Background(), shopID, "Tony", "")
	if err := ActivateBarber(context.Background(), shopID, b.ID); err != nil {
		t.Errorf("already-active should be no-op, got: %v", err)
	}
}

// ---- UpdateBarberSkills ----

func TestUpdateBarberSkills_OK(t *testing.T) {
	shopID := uuidNewShop(t)
	b, _ := CreateBarber(context.Background(), shopID, "Tony", "剪发")
	if err := UpdateBarberSkills(context.Background(), shopID, b.ID, "剪发,染发,烫发"); err != nil {
		t.Fatalf("update: %v", err)
	}
	got, _ := GetBarberInShop(context.Background(), shopID, b.ID)
	if got.Skills != "剪发,染发,烫发" {
		t.Errorf("skills not updated: got %q", got.Skills)
	}
}

func TestUpdateBarberSkills_TooLong(t *testing.T) {
	shopID := uuidNewShop(t)
	b, _ := CreateBarber(context.Background(), shopID, "Tony", "")
	longSkills := strings.Repeat("x", 257)
	err := UpdateBarberSkills(context.Background(), shopID, b.ID, longSkills)
	if err == nil {
		t.Errorf("expected error for skills > 256 chars")
	}
}

// ---- helpers ----

// uuidNewShop 创建一个新 shop，返回 shopID
//
// 唯一 ID 用 uuid 前缀，避免与同 test 包的其它 test 撞 unique index。
// 注意：必须先调 SetupTestDB(t)，否则 MakeShop 内部 DB.Create 会 NPE。
func uuidNewShop(t *testing.T) string {
	t.Helper()
	SetupTestDB(t)
	id := "shop-" + uuid.NewString()
	MakeShop(t, id, "")
	return id
}