// cmd/migrate
//
// 一次性手动迁移脚本 —— 把 InitDB 里"只跑一次"的幂等迁移手动跑一遍。
//
// 背景：v4.7 / v4.8 / v4.9 几次改动里，InitDB 加了多个幂等 backfill：
//   - v4.7 RBAC: 加 role / status 列、seed role_permissions
//   - v4.8 Customer: appointments.customer_id 为空 → 按 name 建顾客 + 累计字段
//   - v4.9 platform_admin: seed role_permissions (含 platform_admin)
//   - 任何时候: seed 默认店铺 + admin / platform_admin 账号
//
// InitDB 在服务启动时自动跑，但有时候：
//   - 老库没重启过新版本，schema 缺表/缺列
//   - 不想重启服务，但又想立刻把这些 backfill 跑一遍
//   - dev / staging 环境想快速追上 master
//
// 用法：
//   go run ./cmd/migrate                       # 跑全部迁移
//   go run ./cmd/migrate -dry-run              # 只打印要做什么，不实际改 DB
//   go run ./cmd/migrate -only=schema,roleperm # 只跑指定步骤（逗号分隔）
//
// 环境变量：跟主服务一致（MYSQL_DSN 或 MYSQL_HOST/PORT/USER/PASS/DB）
//
// 幂等性：每一步都按"已存在则跳过"原则，重复跑 0 副作用。
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/yuterigele/openbook/storage"
)

// 步骤定义
type step struct {
	name string
	run  func(ctx context.Context, dryRun bool) error
}

func main() {
	dryRun := flag.Bool("dry-run", false, "只打印要做什么，不实际改 DB")
	onlyFlag := flag.String("only", "", "只跑指定步骤，逗号分隔（schema,roles,shopadmin,roleperm,platform,customers）")
	verbose := flag.Bool("v", true, "显示每步详情")
	flag.Parse()

	// 1) 连 DB（用 storage.InitDB 的环境变量解析逻辑）
	ctx := context.Background()
	if _, err := storage.InitDB(ctx); err != nil {
		log.Fatalf("[migrate] InitDB 失败: %v", err)
	}
	defer func() {
		if sqlDB, err := storage.DB.DB(); err == nil && sqlDB != nil {
			_ = sqlDB.Close()
		}
	}()

	fmt.Println("===============================================")
	fmt.Println(" openbook DB migrate (v4.7 → v4.9)")
	fmt.Println("===============================================")
	if *dryRun {
		fmt.Println("⚠️  DRY-RUN 模式：不会改 DB")
	}
	fmt.Println()

	steps := allSteps()
	only := map[string]bool{}
	if *onlyFlag != "" {
		for _, s := range strings.Split(*onlyFlag, ",") {
			only[strings.TrimSpace(s)] = true
		}
	}

	start := time.Now()
	ran := 0
	skipped := 0
	for _, s := range steps {
		if len(only) > 0 && !only[s.name] {
			skipped++
			continue
		}
		if *verbose {
			fmt.Printf("▶ [%s] ... ", s.name)
		}
		err := s.run(ctx, *dryRun)
		if err != nil {
			fmt.Printf("\n❌ [%s] 失败: %v\n", s.name, err)
			os.Exit(1)
		}
		if *verbose {
			fmt.Println("OK")
		}
		ran++
	}

	fmt.Println()
	fmt.Println("===============================================")
	fmt.Printf(" 完成: 跑了 %d 步，跳过 %d 步，耗时 %s\n",
		ran, skipped, time.Since(start).Round(time.Millisecond))
	if *dryRun {
		fmt.Println("（DRY-RUN：上面只是预览，没有真改 DB）")
	}
	fmt.Println("===============================================")
}

func allSteps() []step {
	return []step{
		{name: "schema", run: stepSchema},
		{name: "roles", run: stepShopAdminRoles},
		{name: "shopadmin", run: stepSeedShopAdmin},
		{name: "roleperm", run: stepRolePermissions},
		{name: "platform", run: stepPlatformAdmin},
		{name: "customers", run: stepCustomers},
	}
}

// =============================================================
// step 1: schema —— AutoMigrate（创建所有表 + 加缺失列）
// =============================================================
func stepSchema(ctx context.Context, dryRun bool) error {
	if dryRun {
		fmt.Print("(AutoMigrate 全部表) ... ")
		return nil
	}
	if err := storage.DB.WithContext(ctx).AutoMigrate(
		&storage.Shop{},
		&storage.Barber{},
		&storage.Customer{},
		&storage.Appointment{},
		&storage.Subscription{},
		&storage.WecomMessageLog{},
		&storage.ReminderLog{},
		&storage.EventLog{},
		&storage.ShopAdmin{},
		&storage.BarberLeave{},
		&storage.Service{},
		&storage.RolePermission{},
	); err != nil {
		return fmt.Errorf("AutoMigrate: %w", err)
	}
	return nil
}

// =============================================================
// step 2: ShopAdmin role 列兜底（v4.7 老数据）
//   - role 为空 / NULL → owner
//   - status 为空 / NULL → active
// =============================================================
func stepShopAdminRoles(ctx context.Context, dryRun bool) error {
	if dryRun {
		fmt.Print("(UPDATE shop_admins SET role='owner' WHERE role IS NULL) ... ")
		return nil
	}
	r1 := storage.DB.WithContext(ctx).Model(&storage.ShopAdmin{}).
		Where("role = '' OR role IS NULL").
		Update("role", "owner")
	if r1.Error != nil {
		return fmt.Errorf("backfill role: %w", r1.Error)
	}
	r2 := storage.DB.WithContext(ctx).Model(&storage.ShopAdmin{}).
		Where("status = '' OR status IS NULL").
		Update("status", "active")
	if r2.Error != nil {
		return fmt.Errorf("backfill status: %w", r2.Error)
	}
	if r1.RowsAffected > 0 || r2.RowsAffected > 0 {
		fmt.Printf("(backfill role=%d status=%d) ", r1.RowsAffected, r2.RowsAffected)
	}
	return nil
}

// =============================================================
// step 3: seed 默认店铺 + 默认 admin
//   - 已存在则跳过（按 shop.id / shop_admin.username 查重）
// =============================================================
func stepSeedShopAdmin(ctx context.Context, dryRun bool) error {
	if dryRun {
		fmt.Print("(seed 默认店铺 + admin 账号) ... ")
		return nil
	}
	// 复用 InitDB 的种子逻辑
	if err := storage.SeedDefaultData(ctx); err != nil {
		return fmt.Errorf("SeedDefaultData: %w", err)
	}
	return nil
}

// =============================================================
// step 4: role_permissions seed（v4.7 + v4.9）
//   - 注意：SeedDefaultRolePermissions 只在表空时跑
//   - 如果表已有数据但缺 platform_admin，需要手动补（见 step 4b）
// =============================================================
func stepRolePermissions(ctx context.Context, dryRun bool) error {
	if dryRun {
		fmt.Print("(seed role_permissions; 缺失补 platform_admin) ... ")
		return nil
	}
	// 4a) 标准 seed（仅表空时）
	if err := storage.SeedDefaultRolePermissions(ctx); err != nil {
		return fmt.Errorf("SeedDefaultRolePermissions: %w", err)
	}

	// 4b) 手动补 platform_admin 的权限（即使表非空也要补）
	//     用 INSERT IGNORE 重复跑不出事
	platformPerms := []string{
		"view:dashboard",
		"view:appointments", "edit:appointments",
		"view:customers", "edit:customers",
		"view:handoffs", "resolve:handoff",
		"view:barbers", "edit:barbers", "create:barber_leave",
		"view:events",
		"view:weekly_report", "view:chain_dashboard",
		"edit:shop",
		"view:services", "edit:services",
		"view:subscription", "manage:subscription",
		"manage:members",
		"change:own_password",
	}
	added := 0
	for _, p := range platformPerms {
		res := storage.DB.WithContext(ctx).Exec(
			"INSERT IGNORE INTO role_permissions (role, permission) VALUES (?, ?)",
			"platform_admin", p)
		if res.Error != nil {
			return fmt.Errorf("insert platform_admin perm %s: %w", p, res.Error)
		}
		added += int(res.RowsAffected)
	}
	if added > 0 {
		fmt.Printf("(补 platform_admin %d 条) ", added)
	}
	return nil
}

// =============================================================
// step 5: platform_admin 种子账号（v4.9）
//   - 用户名默认 platform，密码默认 platform123
//   - 已存在则跳过（按 username 查重）
// =============================================================
func stepPlatformAdmin(ctx context.Context, dryRun bool) error {
	if dryRun {
		fmt.Print("(seed platform_admin 账号) ... ")
		return nil
	}
	username := os.Getenv("DEFAULT_PLATFORM_ADMIN_USERNAME")
	if username == "" {
		username = "platform"
	}
	password := os.Getenv("DEFAULT_PLATFORM_ADMIN_PASSWORD")
	if password == "" {
		password = "platform123"
	}

	var existing storage.ShopAdmin
	err := storage.DB.WithContext(ctx).Where("username = ?", username).First(&existing).Error
	if err == nil {
		// 已存在
		if existing.Role != "platform_admin" {
			// 角色不对，顺手改成 platform_admin（用户的旧账号想升级成超管的场景）
			if err := storage.DB.WithContext(ctx).Model(&existing).Update("role", "platform_admin").Error; err != nil {
				return fmt.Errorf("升级 %s 为 platform_admin: %w", username, err)
			}
			fmt.Printf("(升级 %s 为 platform_admin) ", username)
		} else {
			fmt.Print("(已存在) ")
		}
		return nil
	}

	// 取默认 shop_id
	shopID := os.Getenv("DEFAULT_SHOP_ID")
	if shopID == "" {
		shopID = "default"
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("bcrypt: %w", err)
	}
	now := time.Now()
	admin := storage.ShopAdmin{
		ShopID:       shopID,
		Username:     username,
		PasswordHash: string(hash),
		Role:         "platform_admin",
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := storage.DB.WithContext(ctx).Create(&admin).Error; err != nil {
		return fmt.Errorf("create platform_admin: %w", err)
	}
	fmt.Printf("(新建 %s / 密码 %s) ", username, password)
	return nil
}

// =============================================================
// step 6: 顾客档案自愈（v4.8）
//   - appointments.customer_id 为空 → 按 name 建顾客 + 关联
//   - 累计 total_visits / no_show_count / last_visit_at
// =============================================================
func stepCustomers(ctx context.Context, dryRun bool) error {
	if dryRun {
		fmt.Print("(顾客档案自愈) ... ")
		return nil
	}
	if err := storage.BackfillMissingCustomers(ctx); err != nil {
		return fmt.Errorf("BackfillMissingCustomers: %w", err)
	}
	return nil
}

// =============================================================
// helper: bcrypt （直接用 golang.org/x/crypto/bcrypt；main 顶部已 import）
// =============================================================