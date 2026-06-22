package storage

import (
	"context"
	"errors"
	"fmt"
	"log"

	"gorm.io/gorm"
)

// permissions.go —— RBAC 权限矩阵（v4.7 增量）
//
// 设计要点：
//   - 细粒度 action 权限（15 个），不是粗粒度 role-only 判断
//   - 权限存 DB（role_permissions 表），不是 Go 常量，运营可在线调整
//   - role 字段（owner / staff / platform_admin）存 shop_admins，按 role 查 role_permissions 拿权限
//   - owner 默认全权限（本店）；staff 默认"看 + 业务操作"，不能"管理"（本店）
//   - platform_admin（v4.9）跨店全权限，看全平台所有数据
//
// 表结构：
//   CREATE TABLE role_permissions (
//     role        VARCHAR(32) NOT NULL,
//     permission  VARCHAR(64) NOT NULL,
//     PRIMARY KEY (role, permission)
//   )

// Permission 权限枚举（稳定字符串，前端 / DB 共享）
//
// 命名规则：动作:资源
const (
	// 看板
	PermViewDashboard = "view:dashboard"

	// 预约
	PermViewAppointments = "view:appointments"
	PermEditAppointments = "edit:appointments" // 标记完成 / 取消 / 创建（admin 手动补单）

	// 顾客
	PermViewCustomers = "view:customers"
	PermEditCustomers = "edit:customers" // 加减标签（v4.4）

	// 转人工
	PermViewHandoffs   = "view:handoffs"
	PermResolveHandoff = "resolve:handoff" // 标为已处理（v4.6）

	// 理发师
	PermViewBarbers       = "view:barbers"
	PermEditBarbers       = "edit:barbers"        // CRUD barber
	PermCreateBarberLeave = "create:barber_leave" // 提交请假

	// 事件日志
	PermViewEvents = "view:events"

	// 报表
	PermViewWeeklyReport   = "view:weekly_report"  // 单店周报
	PermViewChainDashboard = "view:chain_dashboard" // 跨店看板（v4.0）

	// 店铺设置
	PermEditShop = "edit:shop" // 营业时间 / 午休 / 节假日 / 时区

	// 服务目录
	PermViewServices = "view:services"
	PermEditServices = "edit:services" // CRUD + 批量导入 + 上下架

	// 订阅
	PermViewSubscription   = "view:subscription"
	PermManageSubscription = "manage:subscription" // 续费（v4.4）

	// 成员管理
	PermManageMembers = "manage:members" // 建 / 改 role / 重置密码 / 停用

	// 密码
	PermChangeOwnPassword = "change:own_password"
)

// AllPermissions 列出所有已知权限（init seed / API 校验用）
//
// 加新权限时务必同步加到这里 + db.go 的 defaultRolePermissions 矩阵
var AllPermissions = []string{
	PermViewDashboard,
	PermViewAppointments, PermEditAppointments,
	PermViewCustomers, PermEditCustomers,
	PermViewHandoffs, PermResolveHandoff,
	PermViewBarbers, PermEditBarbers, PermCreateBarberLeave,
	PermViewEvents,
	PermViewWeeklyReport, PermViewChainDashboard,
	PermEditShop,
	PermViewServices, PermEditServices,
	PermViewSubscription, PermManageSubscription,
	PermManageMembers,
	PermChangeOwnPassword,
}

// Role 角色枚举
const (
	RoleOwner         = "owner"          // 店主，全权限（本店）
	RoleStaff         = "staff"          // 店员，业务权限（本店）
	RolePlatformAdmin = "platform_admin" // 平台超管，跨店全权限（v4.9）
)

// AllRoles 所有已知 role
var AllRoles = []string{RoleOwner, RoleStaff, RolePlatformAdmin}

// IsPlatformAdmin 判断是否是平台超管（便捷判断，避免到处比对字符串）
func IsPlatformAdmin(role string) bool {
	return role == RolePlatformAdmin
}

// defaultRolePermissions 默认 role → 权限矩阵（initDB seed 用）
//
// 调整这里就等于调整系统默认权限。运营也可通过 /api/admin/roles/:role/permissions 在线改
// 持久化到 DB（重 InitDB 不会被覆盖——seed 仅在表为空时跑一次）
var defaultRolePermissions = map[string][]string{
	RoleOwner: AllPermissions, // owner 全权限
	RoleStaff: {
		// 看 + 业务操作
		PermViewDashboard,
		PermViewAppointments, PermEditAppointments,
		PermViewCustomers, PermEditCustomers,
		PermViewHandoffs, PermResolveHandoff,
		PermViewBarbers, PermCreateBarberLeave,
		PermViewEvents,
		PermViewServices,
		PermChangeOwnPassword,
		// 不含：view:weekly_report / view:chain_dashboard / edit:shop / edit:services
		//       / view:subscription / manage:subscription / manage:members
	},
	RolePlatformAdmin: AllPermissions, // 超管全权限（v4.9 新增）
}

// RolePermission role → permission 多对多（v4.7）
type RolePermission struct {
	Role       string `gorm:"primaryKey;size:32" json:"role"`
	Permission string `gorm:"primaryKey;size:64" json:"permission"`
}

// TableName 显式声明（避免 GORM 复数歧义）
func (RolePermission) TableName() string { return "role_permissions" }

// GetRolePermissions 取某 role 的所有 permission
//
//   - DB 未初始化 → 返回空切片（不让 caller panic）
//   - role 在表里没有记录 → 返回空切片（没权限 = 拒绝）
//   - 排序：字典序，保证多次调用结果稳定（前端展示用）
func GetRolePermissions(ctx context.Context, role string) ([]string, error) {
	if DB == nil {
		return nil, nil
	}
	var rows []RolePermission
	if err := DB.WithContext(ctx).Where("role = ?", role).Order("permission asc").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Permission)
	}
	return out, nil
}

// SetRolePermissions 整组覆盖某 role 的权限（事务）
//
//   - 旧的全删，新的全 insert
//   - 自动用 defaultRolePermissions 校验：未知 perm 拒绝
//   - role 必须是 AllRoles 之一
func SetRolePermissions(ctx context.Context, role string, perms []string) error {
	if DB == nil {
		return errors.New("DB 未初始化")
	}
	if !isKnownRole(role) {
		return fmt.Errorf("未知 role: %s（允许值: %v）", role, AllRoles)
	}
	for _, p := range perms {
		if !isKnownPermission(p) {
			return fmt.Errorf("未知 permission: %s", p)
		}
	}
	return DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("role = ?", role).Delete(&RolePermission{}).Error; err != nil {
			return err
		}
		if len(perms) == 0 {
			return nil
		}
		rows := make([]RolePermission, 0, len(perms))
		for _, p := range perms {
			rows = append(rows, RolePermission{Role: role, Permission: p})
		}
		return tx.Create(&rows).Error
	})
}

// AdminHasPermission 查某 admin 是否有某权限
//
//   - adminID == 0 / nil 错误 → false（防呆）
//   - DB 未初始化 → false（拒绝 = 安全）
//   - 缓存：暂不缓存（admin 数量小，权限表小，每次查 < 1ms）
//   - 高频路径：可加 sync.Map cache（5min TTL）—— 暂未实现
func AdminHasPermission(ctx context.Context, adminID uint64, perm string) (bool, error) {
	if DB == nil || adminID == 0 {
		return false, nil
	}
	// 1) 取 admin 的 role
	var admin ShopAdmin
	if err := DB.WithContext(ctx).Where("id = ?", adminID).First(&admin).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, nil // admin 已被删 → 拒绝
		}
		return false, err
	}
	if admin.Status != "" && admin.Status != "active" {
		return false, nil // 已停用
	}
	// 2) 查 role 是否拥有该 perm
	var n int64
	if err := DB.WithContext(ctx).Model(&RolePermission{}).
		Where("role = ? AND permission = ?", admin.Role, perm).
		Count(&n).Error; err != nil {
		return false, err
	}
	return n > 0, nil
}

// isKnownRole 校验 role 是已知 role
func isKnownRole(role string) bool {
	for _, r := range AllRoles {
		if r == role {
			return true
		}
	}
	return false
}

// isKnownPermission 校验 permission 是已知 permission
func isKnownPermission(perm string) bool {
	for _, p := range AllPermissions {
		if p == perm {
			return true
		}
	}
	return false
}

// SeedDefaultRolePermissions seed 默认 role → permission 矩阵
//
//   - 仅在 role_permissions 表为空时跑（不会覆盖运营在线调整）
//   - InitDB 调用一次
func SeedDefaultRolePermissions(ctx context.Context) error {
	if DB == nil {
		return nil
	}
	var n int64
	if err := DB.WithContext(ctx).Model(&RolePermission{}).Count(&n).Error; err != nil {
		return err
	}
	if n > 0 {
		return nil // 已 seed 过
	}
	var rows []RolePermission
	for role, perms := range defaultRolePermissions {
		for _, p := range perms {
			rows = append(rows, RolePermission{Role: role, Permission: p})
		}
	}
	if err := DB.WithContext(ctx).Create(&rows).Error; err != nil {
		return err
	}
	log.Printf("[storage] seed role_permissions: %d 条（owner=%d staff=%d）",
		len(rows), len(defaultRolePermissions[RoleOwner]), len(defaultRolePermissions[RoleStaff]))
	return nil
}
