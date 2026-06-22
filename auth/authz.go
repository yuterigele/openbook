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

	"github.com/cloudwego/hertz/pkg/app"
)

// RequirePerm 鉴权中间件：要求当前 admin 拥有指定 permission
//
// 失败：
//   - 无 claims → 401 unauthorized
//   - admin 已被停用 → 403 forbidden
//   - role 缺该 perm → 403 forbidden
//
// 成功：c.Next(ctx) 继续
func RequirePerm(perm string) app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		cl := GetClaims(c)
		if cl == nil || cl.AdminID == 0 {
			c.JSON(http.StatusUnauthorized, map[string]string{"error": "未登录"})
			c.Abort()
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
