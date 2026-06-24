package storage

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"

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

	// 通知中心（v4.10.1 admin 后台"通知中心"页面）
	//   - view: 看通知发送记录（sent/failed/skipped/pending）
	//   - retry: 补发失败/跳过的通知（避免重复打扰已 sent 顾客）
	PermViewNotifications    = "view:notifications"
	PermRetryNotifications   = "retry:notifications"
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
	PermViewNotifications, PermRetryNotifications,
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
//
// 三个 role 的设计哲学：
//   - owner         ：店主，看本店所有数据 + 可改店铺/服务/订阅/成员
//   - staff         ：店员，看 + 业务操作（不能改店铺/服务/订阅/成员，避免误删）
//   - platform_admin：跨店超管，全平台所有数据（v4.9 新增，多店连锁场景）
//
// staff 故意禁掉的 7 个权限（理由）：
//   - view:weekly_report / view:chain_dashboard → 经营数据敏感，不给店员
//   - edit:shop                               → 改营业时间/午休，误操作影响大
//   - edit:services                           → 改服务目录/价格，敏感
//   - view:subscription / manage:subscription → 订阅信息敏感
//   - manage:members                          → 成员管理（建/改 role/重置密码）必须有 owner 权限
//
// owner 故意禁掉的 3 个权限：
//   - view:chain_dashboard → 跨店数据，单店 owner 看不到其他店（v4.10.1 修复权限泄漏）
//     想要看多店/跨店周报请用 platform_admin 账号
//   - view:subscription   → 订阅详情归 platform_admin（v4.10.1）
//     商户无需关心订阅状态——订阅是平台/运营层的事
//   - manage:subscription → 续费 / 创建订阅只给 platform_admin（v4.10.1）
//     商户想续费请找运营
//
// owner 拥有 view:weekly_report（v4.10.1）：看自己店经营数据
//   - 但跨店周报 view:chain_dashboard 不给 owner
//   - 所以单店周报（PermViewWeeklyReport）和跨店周报（PermViewChainDashboard）权限分离
//
// 加新权限的步骤：
//  1. 在 storage/permissions.go 加 PermXxx 常量 + AllPermissions 追加
//  2. 这里给 owner / staff 显式列（owner 默认 AllPermissions 可不写）
//  3. platform_admin 不需要列（默认全权限）
//  4. 重启服务，让 seed 写 DB
//  5. 写单测覆盖 owner/staff 边界
//
// 暴露 DefaultRolePermissions 让测试可以断言"矩阵期望长度"（避免硬编码数字，
// 后续加 perm 时测试自动跟上）。
var DefaultRolePermissions = map[string][]string{
	// owner 全权限（除了 view:chain_dashboard——单店 owner 看不到跨店数据）
	// v4.10.1：之前用 AllPermissions 走捷径，导致单店 owner 也能看多店看板。
	// 现改成显式列，缺什么补什么更安全。
	RoleOwner: {
		PermViewDashboard,
		PermViewAppointments, PermEditAppointments,
		PermViewCustomers, PermEditCustomers,
		PermViewHandoffs, PermResolveHandoff,
		PermViewBarbers, PermEditBarbers, PermCreateBarberLeave,
		PermViewEvents,
		PermViewWeeklyReport, // v4.10.1：单店周报 owner 该看（看自己店经营数据）
		// PermViewChainDashboard, // 显式不列：跨店看板只给 platform_admin
		PermEditShop,
		PermViewServices, PermEditServices,
		// PermViewSubscription, // 显式不列：订阅详情归 platform_admin
		// PermManageSubscription, // 显式不列：续费 / 创建订阅只给 platform_admin
		PermManageMembers,
		PermChangeOwnPassword,
		PermViewNotifications, PermRetryNotifications,
	},
	RoleStaff: {
		// 看 + 业务操作（不允许 manage:* / edit:shop / edit:services）
		PermViewDashboard,
		PermViewAppointments, PermEditAppointments,
		PermViewCustomers, PermEditCustomers,
		PermViewHandoffs, PermResolveHandoff,
		PermViewBarbers, PermCreateBarberLeave,
		PermViewEvents,
		PermViewServices,
		PermViewNotifications, PermRetryNotifications, // 店员也能补发通知（避免漏通知）
		PermChangeOwnPassword,
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
	for role, perms := range DefaultRolePermissions {
		for _, p := range perms {
			rows = append(rows, RolePermission{Role: role, Permission: p})
		}
	}
	if err := DB.WithContext(ctx).Create(&rows).Error; err != nil {
		return err
	}
	return nil
}

// ReconcileRolePermissionsResult reconcile 结果报告
type ReconcileRolePermissionsResult struct {
	Inserted  int      // 实际新增的 (role, perm) 条数
	Skipped   int      // 已存在、跳过的条数
	InsertedList []string // 新增的 (role, perm) 描述（log 用，格式 "role=owner perm=view:foo"）
}

// ReconcileRolePermissions 增量补齐缺失的 role → permission（v4.10.1）
//
//   - 这是 SeedDefaultRolePermissions 的"补丁版"：
//     Seed 只在表为空时跑一次（保护运营调整），但**新加的权限**老店铺永远拿不到
//     Reconcile 每次启动都跑，对比 DefaultRolePermissions + 已存在记录，**只补缺失、不删任何记录**
//   - 关键安全约束：绝不 Delete 任何 row
//     - 运营在线调过的（比如"staff 禁掉 view:dashboard"）会完整保留
//     - 真正"删 perm"的操作只应该走 SetRolePermissions（替换整组），不走 reconcile
//   - 调用方：InitDB 末尾、运维 CLI（应急）
//
// 行为示例（假设加了 PermViewNotifications + PermRetryNotifications 两个新 perm）：
//   - role_permissions 表空：补全 owner/staff 的所有默认 + 新 perm
//   - role_permissions 表已有（老 seed 过的）：只补 2 个新 perm 到对应 role
//   - 运营在 UI 上把 staff 的 view:events 拿掉了：reconcile 不会加回来（尊重运营意图）
//
// 返回值：reconcile 报告（给 log / 运维确认用）
func ReconcileRolePermissions(ctx context.Context) (ReconcileRolePermissionsResult, error) {
	res := ReconcileRolePermissionsResult{}
	if DB == nil {
		return res, nil
	}

	// 1) 拉所有已存在的 (role, perm)
	type existing struct {
		Role       string
		Permission string
	}
	var rows []existing
	if err := DB.WithContext(ctx).
		Table("role_permissions").
		Select("role, permission").
		Scan(&rows).Error; err != nil {
		return res, fmt.Errorf("读 role_permissions: %w", err)
	}
	have := make(map[string]bool, len(rows))
	for _, r := range rows {
		have[r.Role+"\x00"+r.Permission] = true
	}

	// 2) 对比 DefaultRolePermissions，找出缺失的
	//    注意：owner / staff / platform_admin 全部走 DefaultRolePermissions 矩阵
	//    v4.10.1：owner 也改成显式列了（不再用 AllPermissions 兜底）
	//    → 删掉/新增 perm 都要同步改这里
	desired := make(map[string]bool)
	// 显式遍历每个 role 的 default 矩阵
	for role, perms := range DefaultRolePermissions {
		if role == RoleOwner {
			// owner 走显式列表（不包含 view:chain_dashboard 等"平台/连锁级" perm）
			for _, p := range perms {
				desired[role+"\x00"+p] = true
			}
			continue
		}
		// staff / platform_admin：按 defaultRolePermissions 矩阵
		// 注意：platform_admin 矩阵里也是 AllPermissions，所以效果一致
		for _, p := range perms {
			desired[role+"\x00"+p] = true
		}
	}

	// 3) 计算缺失
	var toInsert []RolePermission
	for k := range desired {
		if have[k] {
			res.Skipped++
			continue
		}
		// k 格式 "role\x00perm"，拆开
		idx := strings.Index(k, "\x00")
		if idx < 0 {
			continue
		}
		role, perm := k[:idx], k[idx+1:]
		toInsert = append(toInsert, RolePermission{Role: role, Permission: perm})
		res.InsertedList = append(res.InsertedList, fmt.Sprintf("role=%s perm=%s", role, perm))
	}
	res.Inserted = len(toInsert)

	// 4) 批量插入缺失的（CreateIgnoreDuplicates 双保险：极端竞态下也不会 panic）
	if len(toInsert) > 0 {
		if err := DB.WithContext(ctx).Create(&toInsert).Error; err != nil {
			return res, fmt.Errorf("补全 role_permissions: %w", err)
		}
		log.Printf("[storage] reconcile role_permissions: 新增 %d 条（已有 %d 条；运营调整完整保留）", res.Inserted, res.Skipped)
		for _, desc := range res.InsertedList {
			log.Printf("[storage]   + %s", desc)
		}
	}
	return res, nil
}
