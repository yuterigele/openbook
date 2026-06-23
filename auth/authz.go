package auth

// authz.go —— RBAC 授权中间件（v4.7）
//
// 跟 jwt.go 的区别：
//   - jwt.go 负责"你是谁"（authentication）
//   - authz.go 负责"你能做什么"（authorization）
//
// 用法：
//
//	protected.GET("/services", RequirePerm(PermViewServices), listServicesHandler)
//	protected.POST("/services", RequirePerm(PermEditServices), createServiceHandler)
//
// 行为：
//   - 401：未登录（无 claims）
//   - 403：已登录但无权限（admin 被停用、role 没这个 perm）
//   - 放行：claims 有效 + 有权限
//
// 性能：每次请求 1 次 DB 查询（role_permissions 表小，<1ms）
//   - 后续可加 sync.Map + 5min TTL 缓存（暂未实现，admin 数量 < 100 时不必要）

import (
	"context"
	"net/http"
	"strings"

	"github.com/cloudwego/hertz/pkg/app"
)

// RequirePerm 鉴权中间件：要求当前 admin 拥有指定 permission
//
// 失败响应：
//   - 无 claims / 未登录        → 401 unauthorized
//   - admin 已被停用             → 403 forbidden
//   - role 缺该 perm             → 403 forbidden（返回 permission 字段方便前端定位）
//   - 权限检查 DB 出错           → 500 internal error
//
// 成功：c.Next(ctx) 继续
//
// ────────────────────────────────────────────────────────────────────────
// 特殊：platform_admin 角色 → 直接放行（不论接口要求什么 perm）
// ────────────────────────────────────────────────────────────────────────
//
// 设计原因（v4.9 新增）：
//   - platform_admin 是"跨店超管"，看全平台所有数据
//   - 权限矩阵虽然默认是 AllPermissions，但跨店接口的设计常常需要绕过 perm 检查
//     直接查询全平台数据（例：listServicesHandler 根据 role 走不同 SQL）
//   - 如果不在中间件层短路，每个跨店 handler 都要写一遍 `if IsPlatformAdmin(c)`
//     既冗余又容易漏（新加 handler 忘了判 → 超管被自己权限挡）
//
// 实现位置选择：
//   - 这里（中间件层）而非 handler 层：避免漏写
//   - 用 role 字符串硬比对而非查权限矩阵：避免每次进 DB 查（超管操作频次低，但 DB 调用）
//
// 注意：
//   - 超管仍走 authChain 校验登录态（未登录 → 401），不会绕过登录
//   - 超管绕过的是"具体 perm 检查"，不是"鉴权"本身
//   - 后续要新加 role（比如 'data_analyst'）也走这里加判断
func RequirePerm(perm string) app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		cl := GetClaims(c)
		if cl == nil || cl.AdminID == 0 {
			c.JSON(http.StatusUnauthorized, map[string]string{"error": "未登录"})
			c.Abort()
			return
		}
		// v4.9: 超管直接放行（不论接口要求什么 perm）
		if cl.Role == "platform_admin" {
			c.Next(ctx)
			return
		}
		ok, err := HasPermission(ctx, cl.AdminID, perm)
		if err != nil {
			c.JSON(http.StatusInternalServerError, map[string]string{"error": "权限检查失败: " + err.Error()})
			c.Abort()
			return
		}
		if !ok {
			c.JSON(http.StatusForbidden, map[string]string{
				"error":      "无权限: " + perm,
				"permission": perm,
			})
			c.Abort()
			return
		}
		c.Next(ctx)
	}
}

// RequireRole 鉴权中间件：要求当前 admin 的 role 在白名单里（v4.10.1）
//
// 跟 RequirePerm 的区别：
//   - RequirePerm：粒度细，按 permission 检查（每条接口具体要啥 perm）
//   - RequireRole：粒度粗，按 role 检查（整个 role 级别才允许）
//
// 适用场景：
//   - 跨店/平台级接口（多店看板、跨店周报、平台统计）——"只有 platform_admin 才能进"
//   - 任何 role=白名单的 admin 直接放行（不论 perm 矩阵里有啥）
//
// 行为：
//   - 无 claims            → 401 unauthorized
//   - role 不在白名单      → 403 forbidden（返回 required_role 方便前端定位）
//   - role 命中            → c.Next(ctx)
//
// 设计原因：
//   - 之前 v4.0 MVP 留的"任何 admin 都能看多店看板"是权限泄漏
//   - 用 RequireRole 比 RequirePerm 更直接：多店接口**只**给 platform_admin
//   - 配合权限矩阵的 owner 显式列（不含 view:chain_dashboard），双层防御
//     即使权限矩阵配错，handler 层也挡
//
// 注意：
//   - 跟 RequirePerm 一样，超管（platform_admin）放行逻辑在 RequirePerm 里就有
//     但 RequireRole 是直接比对 role，**不**走 RequirePerm，所以这里**不再**短路超管
//   - 调用方传白名单：RequireRole("platform_admin") 或 RequireRole("owner", "platform_admin")
func RequireRole(allowedRoles ...string) app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		cl := GetClaims(c)
		if cl == nil || cl.AdminID == 0 {
			c.JSON(http.StatusUnauthorized, map[string]string{"error": "未登录"})
			c.Abort()
			return
		}
		// 命中白名单
		for _, r := range allowedRoles {
			if cl.Role == r {
				c.Next(ctx)
				return
			}
		}
		// 未命中
		required := strings.Join(allowedRoles, ",")
		c.JSON(http.StatusForbidden, map[string]string{
			"error":         "无权限: 需要 role 之一: " + required,
			"required_role": required,
			"actual_role":   cl.Role,
		})
		c.Abort()
	}
}

// HasPermission 检查 admin 是否有 permission（中间件 + handler 都能用）
//
// 这是一个 alias，转发到 storage 层的具体实现。
// 不在 auth 包里直接调 DB，避免 auth 包依赖 storage（保持职责单一）。
// 真正的实现在 api/admin_features_v46.go 里通过 storage 包调用。
// （注：这里用 interface{} 占位，下面会注入真正的实现）
var hasPermissionFunc func(ctx context.Context, adminID uint64, perm string) (bool, error)

// HasPermission 公共入口（由 api 包在 init 时注入 storage.AdminHasPermission）
func HasPermission(ctx context.Context, adminID uint64, perm string) (bool, error) {
	if hasPermissionFunc == nil {
		return false, nil // 兜底：未注入 = 拒绝
	}
	return hasPermissionFunc(ctx, adminID, perm)
}

// SetHasPermissionFunc 注入真正的检查函数（api 包在 init 时调用）
//
// 用 setter 而非 import storage 是为了：
//  1. auth 包不依赖 storage（auth 是更低层的库）
//  2. 单元测试时容易 mock
func SetHasPermissionFunc(fn func(ctx context.Context, adminID uint64, perm string) (bool, error)) {
	hasPermissionFunc = fn
}
