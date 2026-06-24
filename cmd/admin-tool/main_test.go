package main

// main_test.go
//
// admin-tool CLI 单测（v4.10.1）
//
// 覆盖：
//   - run() 边界：空 args / 未知子命令 / InitDB 失败
//   - runPerms dispatch：缺子命令 / 未知子命令 / 缺参数
//   - runPermsReconcile：happy path + 二次幂等 + 补齐缺失 perm
//   - runPermsList：单 role / 全部 role
//   - runPermsSet：happy path + 未知 role / 缺参数
//   - runPermsCheck：有 / 没有 / 缺参数
//
// 测试技巧：
//   - 替换 initStorageFn 为 storage.SetupTestDB，避免连真 MySQL
//   - 用 bytes.Buffer 捕获 out / errOut，断言文案（"✓" / "role=" / "缺"）
//   - 每个 case 独立 SetupTestDB（t.Cleanup 会 reset DB=nil）

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"github.com/yuterigele/openbook/storage"
)

// withTestStorage 注入 test DB 替换 initStorageFn；返回 restore 闭包
func withTestStorage(t *testing.T) func() {
	t.Helper()
	prev := initStorageFn
	storage.SetupTestDB(t)
	initStorageFn = func(ctx context.Context) error {
		// SetupTestDB 已经在外面跑过；这里只确保 DB 不为 nil 即可
		_ = ctx
		if storage.DB == nil {
			return errDBNotInit
		}
		return nil
	}
	return func() { initStorageFn = prev }
}

var errDBNotInit = &testErr{"test DB not initialized"}

// testErr 实现 error 借口（避免 import errors 包装）
type testErr struct{ msg string }

func (e *testErr) Error() string { return e.msg }

// runCmd 跑 run() 并返回 (exitCode, stdout, stderr)
func runCmd(args ...string) (int, string, string) {
	var out, errOut bytes.Buffer
	code := run(args, &out, &errOut)
	return code, out.String(), errOut.String()
}

// ============================================================
// run() 边界
// ============================================================

func TestRun_NoArgs_ShowsUsage(t *testing.T) {
	code, _, errOut := runCmd()
	if code != 1 {
		t.Errorf("no args exit code = %d, want 1", code)
	}
	if !strings.Contains(errOut, "用法") {
		t.Errorf("errOut 应含 '用法', got: %q", errOut)
	}
}

func TestRun_UnknownSubcommand_ExitsOne(t *testing.T) {
	defer withTestStorage(t)()
	code, _, errOut := runCmd("bogus")
	if code != 1 {
		t.Errorf("unknown sub exit code = %d, want 1", code)
	}
	if !strings.Contains(errOut, "未知子命令") || !strings.Contains(errOut, "bogus") {
		t.Errorf("errOut 应含 '未知子命令 bogus', got: %q", errOut)
	}
}

func TestRun_InitDBFailure_ExitsTwo(t *testing.T) {
	prev := initStorageFn
	initStorageFn = func(ctx context.Context) error {
		return &testErr{"simulated init failure"}
	}
	defer func() { initStorageFn = prev }()

	code, _, errOut := runCmd("perms", "list")
	if code != 2 {
		t.Errorf("InitDB failure exit code = %d, want 2", code)
	}
	if !strings.Contains(errOut, "InitDB 失败") {
		t.Errorf("errOut 应含 'InitDB 失败', got: %q", errOut)
	}
}

// ============================================================
// runPerms dispatch 边界
// ============================================================

func TestRunPerms_MissingSubcommand_Errors(t *testing.T) {
	defer withTestStorage(t)()
	code, _, errOut := runCmd("perms")
	if code != 1 {
		t.Errorf("perms no-arg exit code = %d, want 1", code)
	}
	if !strings.Contains(errOut, "需要参数") {
		t.Errorf("errOut 应含 '需要参数', got: %q", errOut)
	}
}

func TestRunPerms_UnknownSubcommand_Errors(t *testing.T) {
	defer withTestStorage(t)()
	code, _, errOut := runCmd("perms", "wat")
	if code != 1 {
		t.Errorf("perms unknown exit code = %d, want 1", code)
	}
	if !strings.Contains(errOut, "未知 perms 子命令") {
		t.Errorf("errOut 应含 '未知 perms 子命令', got: %q", errOut)
	}
}

func TestRunPermsSet_MissingArgs_Errors(t *testing.T) {
	defer withTestStorage(t)()
	code, _, errOut := runCmd("perms", "set", "owner") // 缺 perm 列表
	if code != 1 {
		t.Errorf("perms set no-perm exit code = %d, want 1", code)
	}
	if !strings.Contains(errOut, "用法: admin-tool perms set") {
		t.Errorf("errOut 应含 '用法: admin-tool perms set', got: %q", errOut)
	}
}

func TestRunPermsCheck_MissingArgs_Errors(t *testing.T) {
	defer withTestStorage(t)()
	code, _, errOut := runCmd("perms", "check", "owner") // 缺 perm
	if code != 1 {
		t.Errorf("perms check no-perm exit code = %d, want 1", code)
	}
	if !strings.Contains(errOut, "用法: admin-tool perms check") {
		t.Errorf("errOut 应含 '用法: admin-tool perms check', got: %q", errOut)
	}
}

// ============================================================
// runPermsReconcile
// ============================================================

func TestRunPermsReconcile_EmptyTable_InsertsDefaults(t *testing.T) {
	defer withTestStorage(t)()
	var out bytes.Buffer
	if err := runPermsReconcile(context.Background(), &out); err != nil {
		t.Fatalf("reconcile err: %v", err)
	}
	// seed 在 SetupTestDB 时已经跑过了，reconcile 应该看到全有 → Inserted=0
	if !strings.Contains(out.String(), "reconcile 完成") {
		t.Errorf("out 应含 'reconcile 完成', got: %q", out.String())
	}
	if !strings.Contains(out.String(), "新增 0 条") {
		t.Errorf("fresh DB reconcile 后 Inserted 应 = 0, got: %q", out.String())
	}
	if !strings.Contains(out.String(), "无需补全") {
		t.Errorf("无缺失时应打印 '无需补全', got: %q", out.String())
	}
}

func TestRunPermsReconcile_MissingNewPerm_PatchesThem(t *testing.T) {
	defer withTestStorage(t)()
	// 模拟"老店铺"状态：删掉 v4.10.1 加的两个新 perm
	if err := storage.DB.Where("permission IN ?",
		[]string{storage.PermViewNotifications, storage.PermRetryNotifications}).
		Delete(&storage.RolePermission{}).Error; err != nil {
		t.Fatalf("delete new perms: %v", err)
	}

	var out bytes.Buffer
	if err := runPermsReconcile(context.Background(), &out); err != nil {
		t.Fatalf("reconcile err: %v", err)
	}
	// owner / staff / platform_admin 都会补 2 个 → Inserted=6
	if !strings.Contains(out.String(), "新增 6 条") {
		t.Errorf("应补 6 条新 perm, got: %q", out.String())
	}
	// 验证 DB：owner 现在有 view:notifications
	var n int64
	storage.DB.Model(&storage.RolePermission{}).
		Where("role = ? AND permission = ?", storage.RoleOwner, storage.PermViewNotifications).
		Count(&n)
	if n != 1 {
		t.Errorf("reconcile 后 owner 缺 view:notifications, got count = %d", n)
	}
}

func TestRunPermsReconcile_Idempotent(t *testing.T) {
	defer withTestStorage(t)()
	// 跑两次 reconcile，第二次 Inserted=0
	if err := runPermsReconcile(context.Background(), io.Discard); err != nil {
		t.Fatalf("first reconcile: %v", err)
	}
	var out bytes.Buffer
	if err := runPermsReconcile(context.Background(), &out); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	if !strings.Contains(out.String(), "新增 0 条") {
		t.Errorf("二次 reconcile 应 no-op, got: %q", out.String())
	}
}

// TestRunPermsMigrate_OldOwnerMatrix 模拟"v4.10.1 部署后老店铺"——owner 含 chain_dashboard / view:subscription
// migrate 应：删这 3 个 + 补 v4.12 加的 view:plan
func TestRunPermsMigrate_OldOwnerMatrix(t *testing.T) {
	defer withTestStorage(t)()
	// 模拟 v4.10.1 部署后老店铺：owner 含这 3 个收紧项
	extraPerms := []string{
		"view:chain_dashboard",
		"view:subscription",
		"manage:subscription",
	}
	for _, p := range extraPerms {
		if err := storage.DB.Create(&storage.RolePermission{
			Role: storage.RoleOwner, Permission: p,
		}).Error; err != nil {
			t.Fatalf("create %s: %v", p, err)
		}
	}
	// 验证"老"矩阵：owner 应该有 20+3 条 perm（20 默认 + 3 收紧项）
	//   - 20 = v4.10.1 显式列 19 + v4.12 view:plan 1
	var n int64
	storage.DB.Model(&storage.RolePermission{}).Where("role = ?", storage.RoleOwner).Count(&n)
	wantOld := len(storage.DefaultRolePermissions[storage.RoleOwner]) + 3
	if n != int64(wantOld) {
		t.Fatalf("老 owner 矩阵 应 = %d, got %d", wantOld, n)
	}

	// 跑 migrate
	var out bytes.Buffer
	if err := runPermsMigrate(context.Background(), &out); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// 验证：owner 现在有 20 条（19 默认 + 1 view:plan）
	storage.DB.Model(&storage.RolePermission{}).Where("role = ?", storage.RoleOwner).Count(&n)
	want := len(storage.DefaultRolePermissions[storage.RoleOwner]) // 20
	if int(n) != want {
		t.Errorf("migrate 后 owner 应 = %d 条, got %d", want, n)
	}

	// 验证：chain_dashboard / view:subscription / manage:subscription 都没了
	for _, p := range extraPerms {
		var cnt int64
		storage.DB.Model(&storage.RolePermission{}).
			Where("role = ? AND permission = ?", storage.RoleOwner, p).Count(&cnt)
		if cnt != 0 {
			t.Errorf("migrate 后 owner 仍含 %s（应被删）", p)
		}
	}

	// 验证：view:plan 在 owner 矩阵里
	var planN int64
	storage.DB.Model(&storage.RolePermission{}).
		Where("role = ? AND permission = ?", storage.RoleOwner, storage.PermViewPlan).Count(&planN)
	if planN != 1 {
		t.Errorf("migrate 后 owner 缺 view:plan, got %d", planN)
	}

	// 验证：输出含"实际删除 3 条"
	if !strings.Contains(out.String(), "实际删除 owner 矩阵 3 条") {
		t.Errorf("输出应含'实际删除 owner 矩阵 3 条', got: %q", out.String())
	}
}

// TestRunPermsMigrate_Idempotent 二次跑 migrate 0 删除 0 新增
func TestRunPermsMigrate_Idempotent(t *testing.T) {
	defer withTestStorage(t)()
	// 第一次
	if err := runPermsMigrate(context.Background(), io.Discard); err != nil {
		t.Fatalf("first migrate: %v", err)
	}
	// 第二次
	var out bytes.Buffer
	if err := runPermsMigrate(context.Background(), &out); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	if !strings.Contains(out.String(), "实际删除 owner 矩阵 0 条") {
		t.Errorf("二次 migrate 应 0 删除, got: %q", out.String())
	}
	if !strings.Contains(out.String(), "新增 0 条") {
		t.Errorf("二次 migrate reconcile 应 0 新增, got: %q", out.String())
	}
}

// TestRunPermsMigrate_LeavesOperatorCustomPerm 验证 migrate 不删运营手动加的 perm
func TestRunPermsMigrate_LeavesOperatorCustomPerm(t *testing.T) {
	defer withTestStorage(t)()
	fakePerm := "custom:fake_perm"
	if err := storage.DB.Create(&storage.RolePermission{
		Role: storage.RoleOwner, Permission: fakePerm,
	}).Error; err != nil {
		t.Fatalf("create fake: %v", err)
	}

	if err := runPermsMigrate(context.Background(), io.Discard); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// 运营手动加的 perm 应保留
	var n int64
	storage.DB.Model(&storage.RolePermission{}).
		Where("role = ? AND permission = ?", storage.RoleOwner, fakePerm).Count(&n)
	if n != 1 {
		t.Errorf("migrate 不应删运营手动加的 perm %s, got count=%d", fakePerm, n)
	}
}

// ============================================================
// runPermsList
// ============================================================

func TestRunPermsList_AllRoles(t *testing.T) {
	defer withTestStorage(t)()
	var out bytes.Buffer
	if err := runPermsList(context.Background(), "", &out); err != nil {
		t.Fatalf("list: %v", err)
	}
	got := out.String()
	for _, r := range storage.AllRoles {
		if !strings.Contains(got, "role="+r) {
			t.Errorf("list all 应列 role=%s, got: %q", r, got)
		}
	}
}

func TestRunPermsList_SpecificRole(t *testing.T) {
	defer withTestStorage(t)()
	var out bytes.Buffer
	if err := runPermsList(context.Background(), storage.RoleOwner, &out); err != nil {
		t.Fatalf("list: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "role=owner") {
		t.Errorf("应列 role=owner, got: %q", got)
	}
	// 至少含 owner 的一个 perm
	if !strings.Contains(got, "view:dashboard") {
		t.Errorf("owner 应有 view:dashboard, got: %q", got)
	}
}

func TestRunPermsList_UnknownRole_EmptyList(t *testing.T) {
	defer withTestStorage(t)()
	var out bytes.Buffer
	// storage.GetRolePermissions 对未知 role 返空切片 + nil err
	if err := runPermsList(context.Background(), "fake_role", &out); err != nil {
		t.Fatalf("list fake_role: %v", err)
	}
	if !strings.Contains(out.String(), "权限（0 条）") {
		t.Errorf("未知 role 应 0 条, got: %q", out.String())
	}
}

// ============================================================
// runPermsSet
// ============================================================

func TestRunPermsSet_HappyPath(t *testing.T) {
	defer withTestStorage(t)()
	var out bytes.Buffer
	if err := runPermsSet(context.Background(), storage.RoleStaff,
		[]string{storage.PermViewDashboard, storage.PermViewCustomers, "  ", storage.PermViewDashboard}, // 含重复和空白
		&out); err != nil {
		t.Fatalf("set: %v", err)
	}
	// 验证 DB：staff 现在只有 2 个 perm（去重 + 排序）
	perms, err := storage.GetRolePermissions(context.Background(), storage.RoleStaff)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(perms) != 2 {
		t.Errorf("staff 应该有 2 个 perm, got %d: %v", len(perms), perms)
	}
	if perms[0] != storage.PermViewCustomers || perms[1] != storage.PermViewDashboard {
		t.Errorf("set 后 staff perms = %v, want [view:customers view:dashboard]（字典序 c < d）", perms)
	}
}

func TestRunPermsSet_UnknownRole_Errors(t *testing.T) {
	defer withTestStorage(t)()
	var out bytes.Buffer
	err := runPermsSet(context.Background(), "fake_role",
		[]string{storage.PermViewDashboard}, &out)
	if err == nil {
		t.Error("未知 role 应返 error")
	}
}

// ============================================================
// runPermsCheck
// ============================================================

func TestRunPermsCheck_Has(t *testing.T) {
	defer withTestStorage(t)()
	var out bytes.Buffer
	if err := runPermsCheck(context.Background(), storage.RoleOwner, storage.PermViewDashboard, &out); err != nil {
		t.Fatalf("check: %v", err)
	}
	if !strings.Contains(out.String(), "✓ role=owner 拥有 perm=view:dashboard") {
		t.Errorf("应打印 '✓ role=owner 拥有', got: %q", out.String())
	}
}

func TestRunPermsCheck_Missing(t *testing.T) {
	defer withTestStorage(t)()
	var out bytes.Buffer
	err := runPermsCheck(context.Background(), storage.RoleStaff, storage.PermEditShop, &out)
	if err == nil {
		t.Error("staff 没有 PermEditShop 应返 error")
	}
	if !strings.Contains(out.String(), "✗ role=staff 缺少 perm=edit:shop") {
		t.Errorf("应打印 '✗ ... 缺少', got: %q", out.String())
	}
}

// ============================================================
// 端到端：run() 走完整路径
// ============================================================

func TestRun_EndToEnd_Reconcile(t *testing.T) {
	defer withTestStorage(t)()
	// 删新 perm 模拟"老店铺"
	storage.DB.Where("permission = ?", storage.PermRetryNotifications).
		Delete(&storage.RolePermission{})

	code, out, errOut := runCmd("perms", "reconcile")
	if code != 0 {
		t.Errorf("reconcile exit code = %d, want 0; errOut=%q", code, errOut)
	}
	if !strings.Contains(out, "reconcile 完成") {
		t.Errorf("out 应含 'reconcile 完成', got: %q", out)
	}
	if !strings.Contains(out, "新增") {
		t.Errorf("out 应有 '新增' 数字, got: %q", out)
	}
}
