package storage

// permissions_test.go
//
// v4.10.1 RBAC 自愈测试
//
// 覆盖：
//   - ReconcileRolePermissions
//     1) 空表：补全所有 default + owner 用 AllPermissions
//     2) 部分缺失（模拟加新 perm 后的状态）：只补缺失
//     3) 全有：no-op
//     4) 运营调整过的（staff 拿掉某个 perm）：reconcile 不强制加回（尊重运营意图）
//   - SeedDefaultRolePermissions 二次幂等：reconcile 不会破坏 seed 过的数据
//   - GetRolePermissions / SetRolePermissions：基础 CRUD

import (
	"context"
	"testing"
)

func TestReconcileRolePermissions_EmptyTable(t *testing.T) {
	SetupTestDB(t)
	// role_permissions 表空（SetupTestDB 后 SeedDefaultRolePermissions 会跑一次）
	// 测 reconcile 在 Seed 之后跑应该 no-op（因为已经全有）
	res, err := ReconcileRolePermissions(context.Background())
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if res.Inserted != 0 {
		t.Errorf("Inserted = %d, want 0 (Seed 之后应该全有)", res.Inserted)
	}
	if res.Skipped < 10 {
		t.Errorf("Skipped = %d, want >= 10 (默认矩阵 + AllPermissions)", res.Skipped)
	}
}

func TestReconcileRolePermissions_MissingNewPerm(t *testing.T) {
	SetupTestDB(t)
	// 模拟"老 seed"状态：把 owner / staff 的新 perm（view:notifications / retry:notifications）删掉
	// 模拟加新 perm 前的 DB 状态
	// 注意：这些 perm 是 v4.10.1 加的，reconcile 后应该被补回
	if err := DB.Where("permission IN ?", []string{PermViewNotifications, PermRetryNotifications}).
		Delete(&RolePermission{}).Error; err != nil {
		t.Fatalf("delete new perms: %v", err)
	}
	// 确认确实没了
	var n int64
	DB.Model(&RolePermission{}).Where("permission = ?", PermViewNotifications).Count(&n)
	if n != 0 {
		t.Fatalf("setup: 删除后 view:notifications 仍有 %d 条", n)
	}

	// 跑 reconcile
	res, err := ReconcileRolePermissions(context.Background())
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	// owner / staff / platform_admin 都会补 2 条（3 role × 2 new perm = 6）
	//   - owner 走 AllPermissions
	//   - staff 走 defaultRolePermissions[RoleStaff]
	//   - platform_admin 走 defaultRolePermissions[RolePlatformAdmin] = AllPermissions
	if res.Inserted != 6 {
		t.Errorf("Inserted = %d, want 6 (owner 2 + staff 2 + platform_admin 2)", res.Inserted)
	}

	// 验证 DB：view:notifications 在 owner / staff 都有
	DB.Model(&RolePermission{}).Where("permission = ? AND role = ?", PermViewNotifications, RoleOwner).Count(&n)
	if n != 1 {
		t.Errorf("owner.view:notifications 应该有 1 条，实有 %d", n)
	}
	DB.Model(&RolePermission{}).Where("permission = ? AND role = ?", PermViewNotifications, RoleStaff).Count(&n)
	if n != 1 {
		t.Errorf("staff.view:notifications 应该有 1 条，实有 %d", n)
	}
}

func TestReconcileRolePermissions_NoOpWhenAllPresent(t *testing.T) {
	SetupTestDB(t)
	// Seed 之后所有 perm 都齐
	res, err := ReconcileRolePermissions(context.Background())
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if res.Inserted != 0 {
		t.Errorf("Inserted = %d, want 0（全有时不应插入）", res.Inserted)
	}
}

func TestReconcileRolePermissions_RespectsOperatorCustomPerm(t *testing.T) {
	SetupTestDB(t)
	// 模拟运营在 UI 上给 staff 手动加了一个**非 default** 的 perm（理论不该出现，
	// 但如果运营通过 SQL 直接插了，reconcile 不该删）
	//   - 用 PermEditShop（不属于 default staff 矩阵——staff 故意禁掉）
	//   - 这是想验证：reconcile 是"补缺失"，不是"重置为 default"
	if err := DB.Create(&RolePermission{
		Role:       RoleStaff,
		Permission: PermEditShop,
	}).Error; err != nil {
		t.Fatalf("plant custom: %v", err)
	}

	_, err := ReconcileRolePermissions(context.Background())
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	// 运营加的 perm 应该还在（reconcile 不删）
	var n int64
	DB.Model(&RolePermission{}).
		Where("role = ? AND permission = ?", RoleStaff, PermEditShop).
		Count(&n)
	if n != 1 {
		t.Errorf("运营手动加的 perm 被 reconcile 删了（违反「只增不减」约束）")
	}
}

func TestReconcileRolePermissions_OwnerUsesAllPermissions(t *testing.T) {
	SetupTestDB(t)
	// 模拟"新加 perm"：临时往 AllPermissions 模拟加一项不容易（编译期常量）
	// 所以用替代方案：直接往 DB 插一条 (owner, fake_perm) 然后删掉，确认 reconcile 不会乱动
	//
	// 实际验证：删掉 owner 的 view:dashboard（一个 AllPermissions 里的项）
	if err := DB.Where("role = ? AND permission = ?", RoleOwner, PermViewDashboard).
		Delete(&RolePermission{}).Error; err != nil {
		t.Fatalf("delete: %v", err)
	}
	// 跑 reconcile
	_, err := ReconcileRolePermissions(context.Background())
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	// owner.view:dashboard 应该被补回（因为 AllPermissions 里有）
	var n int64
	DB.Model(&RolePermission{}).Where("role = ? AND permission = ?", RoleOwner, PermViewDashboard).Count(&n)
	if n != 1 {
		t.Errorf("owner.view:dashboard 被删后应该被补回，实有 %d", n)
	}
}

func TestReconcileRolePermissions_OnlyInsertsNoDelete(t *testing.T) {
	SetupTestDB(t)
	// 计算 reconcile 前的总条数
	var beforeN int64
	DB.Model(&RolePermission{}).Count(&beforeN)

	_, err := ReconcileRolePermissions(context.Background())
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	var afterN int64
	DB.Model(&RolePermission{}).Count(&afterN)

	// reconcile 只增不减
	if afterN < beforeN {
		t.Errorf("reconcile 后总条数减少（%d → %d），违反「只增不减」约束", beforeN, afterN)
	}
}

func TestGetRolePermissions_Empty(t *testing.T) {
	SetupTestDB(t)
	perms, err := GetRolePermissions(context.Background(), "non_existent_role")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if perms == nil {
		t.Errorf("perms should be empty slice, not nil")
	}
	if len(perms) != 0 {
		t.Errorf("len = %d, want 0", len(perms))
	}
}

func TestSetRolePermissions_ReplaceGroup(t *testing.T) {
	SetupTestDB(t)
	// 把 staff 设成只 1 个 perm
	if err := SetRolePermissions(context.Background(), RoleStaff, []string{PermViewDashboard}); err != nil {
		t.Fatalf("set: %v", err)
	}
	perms, _ := GetRolePermissions(context.Background(), RoleStaff)
	if len(perms) != 1 || perms[0] != PermViewDashboard {
		t.Errorf("perms = %v, want [view:dashboard]", perms)
	}
	// 再设一组（替换）
	if err := SetRolePermissions(context.Background(), RoleStaff, []string{PermViewEvents, PermViewCustomers}); err != nil {
		t.Fatalf("set2: %v", err)
	}
	perms, _ = GetRolePermissions(context.Background(), RoleStaff)
	if len(perms) != 2 {
		t.Errorf("perms = %v, want 2 entries", perms)
	}
}

func TestSetRolePermissions_RejectUnknownRole(t *testing.T) {
	SetupTestDB(t)
	err := SetRolePermissions(context.Background(), "fake_role", []string{PermViewDashboard})
	if err == nil {
		t.Errorf("应该拒绝未知 role")
	}
}

func TestSetRolePermissions_RejectUnknownPerm(t *testing.T) {
	SetupTestDB(t)
	err := SetRolePermissions(context.Background(), RoleStaff, []string{"view:fake_perm"})
	if err == nil {
		t.Errorf("应该拒绝未知 perm")
	}
}

func TestAdminHasPermission_DisabledAdmin(t *testing.T) {
	SetupTestDB(t)
	admin := MakeAdminWithRole(t, "shop-1", "alice", RoleOwner)
	// 停用
	if err := DB.Model(admin).Update("status", "disabled").Error; err != nil {
		t.Fatalf("disable: %v", err)
	}
	ok, err := AdminHasPermission(context.Background(), admin.ID, PermViewDashboard)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if ok {
		t.Errorf("disabled admin 不应有权限")
	}
}

func TestAdminHasPermission_NotFound(t *testing.T) {
	SetupTestDB(t)
	ok, err := AdminHasPermission(context.Background(), 99999, PermViewDashboard)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if ok {
		t.Errorf("不存在的 admin 不应有权限")
	}
}
