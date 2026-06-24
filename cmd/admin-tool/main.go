// cmd/admin-tool —— 商户后台运维 CLI（v4.10.1）
//
// 用法：
//   go run ./cmd/admin-tool perms reconcile         # 增量补齐缺失的 role → permission
//   go run ./cmd/admin-tool perms list [role]       # 列某 role 的权限（不传 role = 全部）
//   go run ./cmd/admin-tool perms set <role> <p1,p2,...>  # 整组覆盖某 role 的权限
//   go run ./cmd/admin-tool perms check <role> <perm>     # 查某 role 是否有某 perm
//
// 背景：v4.10.1 加了 view:notifications / retry:notifications 两个新权限，
// SeedDefaultRolePermissions 只在 role_permissions 表为空时才跑，
// 所以老店铺永远拿不到新权限——用 reconcile 子命令补齐（不动运营调整过的）。
//
// 测试性：
//   - runPerms* 接受 io.Writer + 返 error，不直接 os.Exit（main 负责退出码）
//   - initStorageFn 是 var，测试里可替换为 storage.SetupTestDB 注入
//   - 这样 main_test.go 不需要真连 MySQL
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/yuterigele/openbook/chatmodel"
	"github.com/yuterigele/openbook/storage"
)

// initStorageFn 是 initStorage 的实现（依赖注入点）。
//
//   - 生产：调 storage.InitDB 连 MySQL
//   - 测试：替换为 storage.SetupTestDB 走 in-memory sqlite
var initStorageFn = func(ctx context.Context) error {
	_, err := storage.InitDB(ctx)
	return err
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run 是 main 的可测核心。返回 exit code（0=成功，1=用户错误，2=系统错误）。
//
//   - args:  os.Args[1:]，由调用方负责切片
//   - out:   正常输出（perms list / set / reconcile 的结果）
//   - errOut: usage / 错误信息
func run(args []string, out, errOut io.Writer) int {
	if len(args) < 1 {
		usage(errOut)
		return 1
	}
	cmd := args[0]
	rest := args[1:]

	chatmodel.LoadEnv()
	ctx := context.Background()
	if err := initStorageFn(ctx); err != nil {
		fmt.Fprintf(errOut, "InitDB 失败: %v\n", err)
		return 2
	}

	switch cmd {
	case "perms":
		if err := runPerms(ctx, rest, out, errOut); err != nil {
			return 1
		}
		return 0
	default:
		fmt.Fprintf(errOut, "未知子命令: %s\n", cmd)
		usage(errOut)
		return 1
	}
}

func usage(w io.Writer) {
	fmt.Fprintf(w, `用法:
  admin-tool perms reconcile                       # 增量补齐缺失的 role → permission
  admin-tool perms list [role]                     # 列某 role 的权限
  admin-tool perms set <role> <p1,p2,...>          # 整组覆盖某 role 的权限
  admin-tool perms check <role> <perm>             # 查某 role 是否有某 perm
`)
}

// runPerms dispatch perms 子命令；返回 error 走 main 决定 exit code
func runPerms(ctx context.Context, args []string, out, errOut io.Writer) error {
	if len(args) < 1 {
		fmt.Fprintln(errOut, "perms 子命令需要参数: reconcile / list / set / check")
		return fmt.Errorf("missing subcommand")
	}
	switch args[0] {
	case "reconcile":
		return runPermsReconcile(ctx, out)
	case "list":
		role := ""
		if len(args) >= 2 {
			role = args[1]
		}
		return runPermsList(ctx, role, out)
	case "set":
		if len(args) < 3 {
			fmt.Fprintln(errOut, "用法: admin-tool perms set <role> <p1,p2,...>")
			return fmt.Errorf("usage: perms set <role> <p1,p2,...>")
		}
		perms := strings.Split(args[2], ",")
		return runPermsSet(ctx, args[1], perms, out)
	case "check":
		if len(args) < 3 {
			fmt.Fprintln(errOut, "用法: admin-tool perms check <role> <perm>")
			return fmt.Errorf("usage: perms check <role> <perm>")
		}
		return runPermsCheck(ctx, args[1], args[2], out)
	default:
		fmt.Fprintf(errOut, "未知 perms 子命令: %s\n", args[0])
		return fmt.Errorf("unknown subcommand: %s", args[0])
	}
}

// runPermsReconcile 增量补齐缺失的 role → permission（"只补缺失，不删任何"）
func runPermsReconcile(ctx context.Context, out io.Writer) error {
	res, err := storage.ReconcileRolePermissions(ctx)
	if err != nil {
		return fmt.Errorf("reconcile 失败: %w", err)
	}
	fmt.Fprintf(out, "✓ reconcile 完成\n")
	fmt.Fprintf(out, "  新增 %d 条（已有 %d 条保留）\n", res.Inserted, res.Skipped)
	if res.Inserted > 0 {
		fmt.Fprintln(out, "  新增明细：")
		for _, desc := range res.InsertedList {
			fmt.Fprintf(out, "    + %s\n", desc)
		}
	} else {
		fmt.Fprintln(out, "  （无缺失，无需补全）")
	}
	return nil
}

// runPermsList 列权限：role 为空时列所有 role
func runPermsList(ctx context.Context, role string, out io.Writer) error {
	if role != "" {
		perms, err := storage.GetRolePermissions(ctx, role)
		if err != nil {
			return fmt.Errorf("查询失败: %w", err)
		}
		fmt.Fprintf(out, "role=%s 权限（%d 条）：\n", role, len(perms))
		for _, p := range perms {
			fmt.Fprintf(out, "  - %s\n", p)
		}
		return nil
	}
	// 不传 role：列所有 role
	for _, r := range storage.AllRoles {
		perms, err := storage.GetRolePermissions(ctx, r)
		if err != nil {
			return fmt.Errorf("查询 %s 失败: %w", r, err)
		}
		fmt.Fprintf(out, "\nrole=%s 权限（%d 条）：\n", r, len(perms))
		for _, p := range perms {
			fmt.Fprintf(out, "  - %s\n", p)
		}
	}
	fmt.Fprintln(out)
	fmt.Fprintln(out, "（可对照 storage/permissions.go 的 DefaultRolePermissions 看是否齐全）")
	return nil
}

// runPermsSet 整组覆盖某 role 的权限（去重 + 排序后写入）
func runPermsSet(ctx context.Context, role string, perms []string, out io.Writer) error {
	uniq := make(map[string]bool)
	out2 := make([]string, 0, len(perms))
	for _, p := range perms {
		p = strings.TrimSpace(p)
		if p != "" && !uniq[p] {
			uniq[p] = true
			out2 = append(out2, p)
		}
	}
	sort.Strings(out2)

	fmt.Fprintf(out, "将覆盖 role=%s 的权限：%d 条\n", role, len(out2))
	for _, p := range out2 {
		fmt.Fprintf(out, "  - %s\n", p)
	}
	if err := storage.SetRolePermissions(ctx, role, out2); err != nil {
		return fmt.Errorf("set 失败: %w", err)
	}
	fmt.Fprintln(out, "✓ 完成")
	return nil
}

// runPermsCheck 查某 role 是否有某 perm；缺则返 error
func runPermsCheck(ctx context.Context, role, perm string, out io.Writer) error {
	perms, err := storage.GetRolePermissions(ctx, role)
	if err != nil {
		return fmt.Errorf("查询失败: %w", err)
	}
	for _, p := range perms {
		if p == perm {
			fmt.Fprintf(out, "✓ role=%s 拥有 perm=%s\n", role, perm)
			return nil
		}
	}
	fmt.Fprintf(out, "✗ role=%s 缺少 perm=%s\n", role, perm)
	return fmt.Errorf("role=%s 缺少 perm=%s", role, perm)
}
