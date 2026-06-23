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
package main

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/yuterigele/openbook/chatmodel"
	"github.com/yuterigele/openbook/storage"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}
	cmd := os.Args[1]
	args := os.Args[2:]

	chatmodel.LoadEnv()
	ctx := context.Background()
	if _, err := storage.InitDB(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "InitDB 失败: %v\n", err)
		os.Exit(1)
	}

	switch cmd {
	case "perms":
		runPerms(ctx, args)
	default:
		fmt.Fprintf(os.Stderr, "未知子命令: %s\n", cmd)
		usage()
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, `用法:
  admin-tool perms reconcile                       # 增量补齐缺失的 role → permission
  admin-tool perms list [role]                     # 列某 role 的权限
  admin-tool perms set <role> <p1,p2,...>          # 整组覆盖某 role 的权限
  admin-tool perms check <role> <perm>             # 查某 role 是否有某 perm
`)
}

func runPerms(ctx context.Context, args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "perms 子命令需要参数: reconcile / list / set / check")
		os.Exit(1)
	}
	sub := args[0]
	switch sub {
	case "reconcile":
		runPermsReconcile(ctx)
	case "list":
		role := ""
		if len(args) >= 2 {
			role = args[1]
		}
		runPermsList(ctx, role)
	case "set":
		if len(args) < 3 {
			fmt.Fprintln(os.Stderr, "用法: admin-tool perms set <role> <p1,p2,...>")
			os.Exit(1)
		}
		perms := strings.Split(args[2], ",")
		runPermsSet(ctx, args[1], perms)
	case "check":
		if len(args) < 3 {
			fmt.Fprintln(os.Stderr, "用法: admin-tool perms check <role> <perm>")
			os.Exit(1)
		}
		runPermsCheck(ctx, args[1], args[2])
	default:
		fmt.Fprintf(os.Stderr, "未知 perms 子命令: %s\n", sub)
		os.Exit(1)
	}
}

func runPermsReconcile(ctx context.Context) {
	res, err := storage.ReconcileRolePermissions(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "reconcile 失败: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("✓ reconcile 完成\n")
	fmt.Printf("  新增 %d 条（已有 %d 条保留）\n", res.Inserted, res.Skipped)
	if res.Inserted > 0 {
		fmt.Println("  新增明细：")
		for _, desc := range res.InsertedList {
			fmt.Printf("    + %s\n", desc)
		}
	} else {
		fmt.Println("  （无缺失，无需补全）")
	}
}

func runPermsList(ctx context.Context, role string) {
	if role != "" {
		perms, err := storage.GetRolePermissions(ctx, role)
		if err != nil {
			fmt.Fprintf(os.Stderr, "查询失败: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("role=%s 权限（%d 条）：\n", role, len(perms))
		for _, p := range perms {
			fmt.Printf("  - %s\n", p)
		}
		return
	}
	// 不传 role：列所有 role
	for _, r := range storage.AllRoles {
		perms, err := storage.GetRolePermissions(ctx, r)
		if err != nil {
			fmt.Fprintf(os.Stderr, "查询 %s 失败: %v\n", r, err)
			continue
		}
		fmt.Printf("\nrole=%s 权限（%d 条）：\n", r, len(perms))
		for _, p := range perms {
			fmt.Printf("  - %s\n", p)
		}
	}
	// 顺便提示：default 矩阵里 owner/staff 的预期条数
	fmt.Println()
	fmt.Println("（可对照 storage/permissions.go 的 defaultRolePermissions 看是否齐全）")
}

func runPermsSet(ctx context.Context, role string, perms []string) {
	// 排序 + 去重
	uniq := make(map[string]bool)
	out := make([]string, 0, len(perms))
	for _, p := range perms {
		p = strings.TrimSpace(p)
		if p != "" && !uniq[p] {
			uniq[p] = true
			out = append(out, p)
		}
	}
	sort.Strings(out)

	fmt.Printf("将覆盖 role=%s 的权限：%d 条\n", role, len(out))
	for _, p := range out {
		fmt.Printf("  - %s\n", p)
	}
	if err := storage.SetRolePermissions(ctx, role, out); err != nil {
		fmt.Fprintf(os.Stderr, "set 失败: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("✓ 完成")
}

func runPermsCheck(ctx context.Context, role, perm string) {
	perms, err := storage.GetRolePermissions(ctx, role)
	if err != nil {
		fmt.Fprintf(os.Stderr, "查询失败: %v\n", err)
		os.Exit(1)
	}
	for _, p := range perms {
		if p == perm {
			fmt.Printf("✓ role=%s 拥有 perm=%s\n", role, perm)
			return
		}
	}
	fmt.Printf("✗ role=%s 缺少 perm=%s\n", role, perm)
	os.Exit(1)
}
