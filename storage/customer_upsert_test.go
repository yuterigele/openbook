package storage

// customer_upsert_test.go
//
// 覆盖 upsertCustomerInTx 的 4 个命中分支 + 边界，重点验证 v4.13.1 修复：
// 顾客纠正姓名时（"我上次说错了，我叫 XXX"），customer.Name 必须被同步更新，
// 否则 leave notify / admin 详情 / 黑名单判定全用错名字。
//
// 场景清单：
//  1. phone 命中：现有顾客有 phone → 同 phone 新预约传新名 → Name 更新 + openID 回填
//  2. openID 命中：现有顾客有 openID → 同 openID 新预约传新名 → Name 更新 + phone 回填
//  3. externalUserID 命中：现有顾客有 external_user_id → 同 external_user_id 新预约传新名 → Name 更新
//  4. name 兜底命中：现有顾客只有 name → 同名新预约传新 phone → name 不动（必然相等）+ phone 回填
//  5. 名字相同 no-op：现有 name="Alice"，新传 "Alice" → DB 不应触发 Updates（updated_at 不变）
//  6. name 空 → 入口直接拒绝（不动 DB）
//  7. 全 miss → 新建，customer.Name = 入参
//
// Run:
//   go test ./storage/... -v -run "TestUpsertCustomer"

import (
	"testing"
	"time"

	"gorm.io/gorm"
)

// runUpsert 包一层事务，模拟 CreateAppointmentFull 的调用方式
func runUpsert(t *testing.T, phone, openID, externalUserID, name string) *Customer {
	t.Helper()
	var c *Customer
	err := DB.Transaction(func(tx *gorm.DB) error {
		var err error
		c, err = upsertCustomerInTx(tx, phone, openID, externalUserID, name)
		return err
	})
	if err != nil {
		t.Fatalf("upsertCustomerInTx: %v", err)
	}
	return c
}

// reloadCustomer 从 DB 重新读，绕过缓存，看真实落库值
func reloadCustomer(t *testing.T, id string) *Customer {
	t.Helper()
	var c Customer
	if err := DB.Where("id = ?", id).First(&c).Error; err != nil {
		t.Fatalf("reload customer: %v", err)
	}
	return &c
}

// ===================== 1) phone 命中：新预约传新名 → Name 更新 + openID 回填 =====================

func TestUpsertCustomer_PhoneHit_UpdatesNameAndBackfillsOpenID(t *testing.T) {
	SetupTestDB(t)
	existing := MakeCustomer(t, "张三", 0, 0)
	existing.Phone = "13800000001"
	existing.WechatOpenID = "" // 空，留给回填
	if err := DB.Save(existing).Error; err != nil {
		t.Fatalf("save: %v", err)
	}

	// 同 phone 第二次预约，传新名字 + 新 openID
	got := runUpsert(t, "13800000001", "wx-new-uuid", "", "张三丰")

	if got.ID != existing.ID {
		t.Errorf("phone 命中应复用同一顾客 ID，got=%q want=%q", got.ID, existing.ID)
	}
	if got.Name != "张三丰" {
		t.Errorf("Name 未更新：got=%q want=%q", got.Name, "张三丰")
	}
	if got.WechatOpenID != "wx-new-uuid" {
		t.Errorf("WechatOpenID 未回填：got=%q", got.WechatOpenID)
	}

	// DB 落库验证（绕过内存）
	rel := reloadCustomer(t, existing.ID)
	if rel.Name != "张三丰" {
		t.Errorf("DB Name 未更新：got=%q want=%q", rel.Name, "张三丰")
	}
}

// ===================== 2) openID 命中：新预约传新名 → Name 更新 + phone 回填 =====================

func TestUpsertCustomer_OpenIDHit_UpdatesNameAndBackfillsPhone(t *testing.T) {
	SetupTestDB(t)
	existing := MakeCustomer(t, "李四", 0, 0)
	existing.WechatOpenID = "wx-old"
	existing.Phone = "" // 空，留给回填
	if err := DB.Save(existing).Error; err != nil {
		t.Fatalf("save: %v", err)
	}

	// 同 openID 第二次预约，传新名字 + 新 phone（无 externalUserID）
	got := runUpsert(t, "13800000002", "wx-old", "", "李四哥")

	if got.ID != existing.ID {
		t.Errorf("openID 命中应复用同一顾客 ID，got=%q want=%q", got.ID, existing.ID)
	}
	if got.Name != "李四哥" {
		t.Errorf("Name 未更新：got=%q want=%q", got.Name, "李四哥")
	}
	if got.Phone != "13800000002" {
		t.Errorf("Phone 未回填：got=%q", got.Phone)
	}

	rel := reloadCustomer(t, existing.ID)
	if rel.Name != "李四哥" {
		t.Errorf("DB Name 未更新：got=%q want=%q", rel.Name, "李四哥")
	}
}

// ===================== 3) externalUserID 命中：新预约传新名 → Name 更新 =====================

func TestUpsertCustomer_ExternalUserIDHit_UpdatesName(t *testing.T) {
	SetupTestDB(t)
	existing := MakeCustomer(t, "王五", 0, 0)
	existing.ExternalUserID = "ext-old"
	if err := DB.Save(existing).Error; err != nil {
		t.Fatalf("save: %v", err)
	}

	// 同 externalUserID 第二次预约，传新名字（phone/openID 都空，强制走第 3 分支）
	got := runUpsert(t, "", "", "ext-old", "王五爷")

	if got.ID != existing.ID {
		t.Errorf("externalUserID 命中应复用同一顾客 ID，got=%q want=%q", got.ID, existing.ID)
	}
	if got.Name != "王五爷" {
		t.Errorf("Name 未更新：got=%q want=%q", got.Name, "王五爷")
	}

	rel := reloadCustomer(t, existing.ID)
	if rel.Name != "王五爷" {
		t.Errorf("DB Name 未更新：got=%q want=%q", rel.Name, "王五爷")
	}
}

// ===================== 4) name 兜底命中：name 不动 + phone 回填 =====================

func TestUpsertCustomer_NameHit_NoNameChange_BackfillsPhone(t *testing.T) {
	SetupTestDB(t)
	existing := MakeCustomer(t, "赵六", 0, 0)
	existing.Phone = "" // 空
	if err := DB.Save(existing).Error; err != nil {
		t.Fatalf("save: %v", err)
	}

	// 同名 + 新 phone 进来（phone/openID/externalUserID 都只有 phone）
	got := runUpsert(t, "13800000004", "", "", "赵六")

	if got.ID != existing.ID {
		t.Errorf("name 命中应复用同一顾客 ID，got=%q want=%q", got.ID, existing.ID)
	}
	if got.Name != "赵六" {
		t.Errorf("Name 不应变：got=%q", got.Name)
	}
	if got.Phone != "13800000004" {
		t.Errorf("Phone 未回填：got=%q", got.Phone)
	}
}

// ===================== 5) 名字相同 no-op：DB 不应被 Updates =====================
//
// upsertCustomerInTx 在 phone 命中但 name 未变时，应走 "len(updates)==0" 分支，
// 不调 tx.Updates(...)——所以 UpdatedAt 不会被刷。
//
// 验证方式：upsert 前后各读一次 UpdatedAt，相等即可。
// （不能直接设 early time，因为 GORM Save 会自动刷 UpdatedAt 到 now()）
func TestUpsertCustomer_NameUnchanged_NoOpUpdate(t *testing.T) {
	SetupTestDB(t)
	existing := MakeCustomer(t, "Alice", 0, 0)
	existing.Phone = "13800000005"
	if err := DB.Save(existing).Error; err != nil {
		t.Fatalf("save: %v", err)
	}

	before := reloadCustomer(t, existing.ID)
	// sleep 一点点确保如果 Updates 触发，UpdatedAt 会变
	time.Sleep(10 * time.Millisecond)

	// 同 phone、同 name（不纠正）
	got := runUpsert(t, "13800000005", "", "", "Alice")
	if got.ID != existing.ID {
		t.Fatalf("phone 命中应复用，got=%q", got.ID)
	}

	after := reloadCustomer(t, existing.ID)
	if !after.UpdatedAt.Equal(before.UpdatedAt) {
		t.Errorf("Name 未变时应 no-op（Updates 不应被调），UpdatedAt 被刷了：before=%v after=%v", before.UpdatedAt, after.UpdatedAt)
	}
	if after.Name != "Alice" {
		t.Errorf("Name 应保持 Alice，got=%q", after.Name)
	}
}

// ===================== 6) name 空 → 入口直接拒绝（不动 DB） =====================

func TestUpsertCustomer_EmptyName_Rejected(t *testing.T) {
	SetupTestDB(t)
	// 入口就 reject，不会落库
	got, err := upsertCustomerInTx(DB, "13800000006", "", "", "")
	if err == nil {
		t.Fatalf("期望 name='' 时报错，但 got=%+v", got)
	}
	if got != nil {
		t.Errorf("err 时应返回 nil customer，got=%+v", got)
	}
}

// ===================== 7) 全 miss → 新建 =====================

func TestUpsertCustomer_AllMiss_CreatesNew(t *testing.T) {
	SetupTestDB(t)
	// 全 miss：用全新 phone + 全空 openID/externalUserID + 全新 name
	got := runUpsert(t, "13800000007", "", "", "新顾客")

	if got.Name != "新顾客" {
		t.Errorf("新建 Name 不对：got=%q", got.Name)
	}
	if got.Phone != "13800000007" {
		t.Errorf("新建 Phone 不对：got=%q", got.Phone)
	}
	if got.ID == "" {
		t.Errorf("新建应有 ID")
	}

	// reload 验证落库
	rel := reloadCustomer(t, got.ID)
	if rel.Name != "新顾客" {
		t.Errorf("DB 落库 Name 不对：got=%q", rel.Name)
	}
}

// ===================== 8) phone 命中 + 同名（首字段无变化）：仅 openID 回填 =====================
//
// 边角：现有顾客 phone + name 都有，新预约同 phone 同 name，但补了 openID
// → name 不应触发 Updates（no-op），只刷 openID
func TestUpsertCustomer_PhoneHit_SameName_OnlyUpdatesOpenID(t *testing.T) {
	SetupTestDB(t)
	existing := MakeCustomer(t, "Bob", 0, 0)
	existing.Phone = "13800000008"
	existing.WechatOpenID = "" // 空
	early := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	existing.UpdatedAt = early
	if err := DB.Save(existing).Error; err != nil {
		t.Fatalf("save: %v", err)
	}

	got := runUpsert(t, "13800000008", "wx-fresh", "", "Bob")
	if got.Name != "Bob" {
		t.Errorf("Name 不应变：got=%q", got.Name)
	}
	if got.WechatOpenID != "wx-fresh" {
		t.Errorf("WechatOpenID 未回填：got=%q", got.WechatOpenID)
	}

	// reload：openID 变了，updated_at 应被刷（因为有字段变化）
	rel := reloadCustomer(t, existing.ID)
	if rel.WechatOpenID != "wx-fresh" {
		t.Errorf("DB WechatOpenID 未更新：got=%q", rel.WechatOpenID)
	}
	if rel.UpdatedAt.Equal(early) {
		t.Errorf("openID 变化应刷 UpdatedAt，但仍是 %v", rel.UpdatedAt)
	}
}

// ===================== 9) 唯一约束不破：避免重复插入冲突 =====================
//
// 防回归：v4.13.1 改动不应该破坏 unique index 语义。
func TestUpsertCustomer_PhoneUniqueIndex_NoDuplicateInsert(t *testing.T) {
	SetupTestDB(t)
	_ = MakeCustomer(t, "Eve", 0, 0)

	// 第一次：全 miss，新建
	c1 := runUpsert(t, "13800000009", "", "", "Eve")
	if c1.Name != "Eve" {
		t.Fatalf("首次 Name 不对：%q", c1.Name)
	}

	// 第二次：phone 命中，应复用
	c2 := runUpsert(t, "13800000009", "", "", "Eve 小姐")
	if c2.ID != c1.ID {
		t.Errorf("phone 命中应复用：c1=%q c2=%q", c1.ID, c2.ID)
	}
	if c2.Name != "Eve 小姐" {
		t.Errorf("第二次 Name 应更新：got=%q", c2.Name)
	}
}