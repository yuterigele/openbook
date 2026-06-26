package api

// members.go —— 成员管理 endpoint（v4.7 RBAC）
//
// 4 个 endpoint，全要 owner 权限（PermManageMembers）：
//   - GET    /api/admin/members              列本店所有 admin
//   - POST   /api/admin/members              owner 建 staff
//   - PUT    /api/admin/members/:id/role     owner 改 role（含自我保护 + last-owner 保护）
//   - POST   /api/admin/members/:id/reset-password  owner 重置 staff 密码
//   - DELETE /api/admin/members/:id          owner 停用（不能停自己）
//
// 设计要点：
//   - shop 隔离：所有操作都限在自己 shop_id
//   - 不能对自己做：改自己 role、停用自己
//   - last-owner 保护：本店只剩 1 个 owner 时，不能把它降级或停用
//   - 密码重置：bcrypt 哈希后存，明文密码不返回前端
//   - staff 也能调 GET /members（看同事列表）；其他操作一律 owner

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"golang.org/x/crypto/bcrypt"

	"github.com/yuterigele/openbook/auth"
	"github.com/yuterigele/openbook/storage"
)

// MemberItem 成员列表项（GET /members 用）
type MemberItem struct {
	ID          uint64     `json:"id"`
	ShopID      string     `json:"shop_id"`
	Username    string     `json:"username"`
	Role        string     `json:"role"`
	Status      string     `json:"status"`
	LastLoginAt *time.Time `json:"last_login_at,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

// CreateMemberRequest POST /members body
type CreateMemberRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Role     string `json:"role"` // 接受 "owner" 或 "staff"；owner 可建 owner（连锁集团多店场景）
}

// ChangeRoleRequest PUT /members/:id/role body
type ChangeRoleRequest struct {
	Role string `json:"role"`
}

// ResetPasswordRequest POST /members/:id/reset-password body
type ResetPasswordRequest struct {
	NewPassword string `json:"new_password"`
}

// ============================================================
// GET /api/admin/members —— 列本店所有 admin
// ============================================================

// listMembersHandler GET /api/admin/members
//
// 权限：PermManageMembers (owner-only)
//
// 注：staff 不应看到此入口，但前端已经在 staff 视图下隐藏 nav
// 后端再加一道防线
//
// v4.14 隐私修复：低权限（owner / staff）viewer 看不到 platform_admin 行。
//   平台超管是跨店全局账号，跟 owner 没有业务关联，没必要显示在店主成员列表里。
//   只 platform_admin 自己查看时才返 platform_admin 行。
func listMembersHandler(ctx context.Context, c *app.RequestContext) {
	shopID := shopFromClaims(c)
	if shopID == "" {
		c.JSON(http.StatusUnauthorized, map[string]string{"error": "no shop in session"})
		return
	}
	if storage.DB == nil {
		c.JSON(http.StatusServiceUnavailable, map[string]string{"error": "db not initialized"})
		return
	}
	// v4.14：拿当前 viewer 的 role，决定要不要过滤掉 platform_admin
	viewerIsPlatform := false
	if cl := auth.GetClaims(c); cl != nil {
		viewerIsPlatform = cl.Role == storage.RolePlatformAdmin
	}
	var rows []storage.ShopAdmin
	if err := storage.DB.WithContext(ctx).
		Where("shop_id = ?", shopID).
		Order("id asc").
		Find(&rows).Error; err != nil {
		c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	out := make([]MemberItem, 0, len(rows))
	for _, r := range rows {
		// v4.14 隐私：低权限 viewer 看不到 platform_admin 行
		if !viewerIsPlatform && r.Role == storage.RolePlatformAdmin {
			continue
		}
		out = append(out, MemberItem{
			ID:          r.ID,
			ShopID:      r.ShopID,
			Username:    r.Username,
			Role:        r.Role,
			Status:      effectiveStatus(r.Status),
			LastLoginAt: r.LastLoginAt,
			CreatedAt:   r.CreatedAt,
			UpdatedAt:   r.UpdatedAt,
		})
	}
	c.JSON(http.StatusOK, out)
}

// effectiveStatus 把空字符串视作 active（兼容历史数据）
func effectiveStatus(s string) string {
	if s == "" {
		return "active"
	}
	return s
}

// ============================================================
// POST /api/admin/members —— owner 建新 admin
// ============================================================

// createMemberHandler POST /api/admin/members
//
// 权限：PermManageMembers (owner-only)
//
// 规则：
//   - username 必填、3-32 字、唯一（DB uniqueIndex 自动校验）
//   - password 必填、≥ 6 位
//   - role 必须是 owner / staff 之一
//   - 不能创建跨店 admin（强制 shop_id = 当前）
//   - last-owner 不影响创建（可以再建一个 owner）
func createMemberHandler(ctx context.Context, c *app.RequestContext) {
	shopID := shopFromClaims(c)
	if shopID == "" {
		c.JSON(http.StatusUnauthorized, map[string]string{"error": "no shop in session"})
		return
	}
	var req CreateMemberRequest
	if err := c.BindAndValidate(&req); err != nil {
		c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	username := strings.TrimSpace(req.Username)
	password := req.Password
	role := strings.ToLower(strings.TrimSpace(req.Role))

	// 校验
	if l := len(username); l < 3 || l > 32 {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "用户名长度必须在 3-32 字"})
		return
	}
	if len(password) < 6 {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "密码至少 6 位"})
		return
	}
	if role != storage.RoleOwner && role != storage.RoleStaff {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "role 必须是 owner 或 staff"})
		return
	}
	if storage.DB == nil {
		c.JSON(http.StatusServiceUnavailable, map[string]string{"error": "db not initialized"})
		return
	}

	// 重复检查（先友好提示，避免 bcrypt 后才发现）
	var existing storage.ShopAdmin
	if err := storage.DB.WithContext(ctx).Where("username = ?", username).First(&existing).Error; err == nil {
		c.JSON(http.StatusConflict, map[string]string{"error": "用户名已存在"})
		return
	}

	// bcrypt 哈希
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	now := time.Now()
	admin := storage.ShopAdmin{
		ShopID:       shopID,
		Username:     username,
		PasswordHash: string(hash),
		Role:         role,
		Status:       "active",
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := storage.DB.WithContext(ctx).Create(&admin).Error; err != nil {
		c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, MemberItem{
		ID:        admin.ID,
		ShopID:    admin.ShopID,
		Username:  admin.Username,
		Role:      admin.Role,
		Status:    admin.Status,
		CreatedAt: admin.CreatedAt,
		UpdatedAt: admin.UpdatedAt,
	})
}

// ============================================================
// PUT /api/admin/members/:id/role —— 改 role
// ============================================================

// changeMemberRoleHandler PUT /api/admin/members/:id/role
//
// 权限：PermManageMembers (owner-only)
//
// 自我保护：
//   - 不能改自己 role（防误操作把自己降级后失去 owner 权限）
//   - last-owner 保护：当店只剩 1 个 active owner 时，不能把它降级
//
// 跨店保护：target admin 必须 shop_id == 当前
func changeMemberRoleHandler(ctx context.Context, c *app.RequestContext) {
	shopID := shopFromClaims(c)
	if shopID == "" {
		c.JSON(http.StatusUnauthorized, map[string]string{"error": "no shop in session"})
		return
	}
	cl := auth.GetClaims(c)
	if cl == nil {
		c.JSON(http.StatusUnauthorized, map[string]string{"error": "no session"})
		return
	}

	idStr := c.Param("id")
	targetID, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "member id 格式错误"})
		return
	}
	var req ChangeRoleRequest
	if err := c.BindAndValidate(&req); err != nil {
		c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	newRole := strings.ToLower(strings.TrimSpace(req.Role))
	if newRole != storage.RoleOwner && newRole != storage.RoleStaff {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "role 必须是 owner 或 staff"})
		return
	}
	if storage.DB == nil {
		c.JSON(http.StatusServiceUnavailable, map[string]string{"error": "db not initialized"})
		return
	}

	// 自我保护：不能改自己
	if targetID == cl.AdminID {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "不能修改自己的角色"})
		return
	}

	// 跨店保护
	var target storage.ShopAdmin
	if err := storage.DB.WithContext(ctx).Where("id = ?", targetID).First(&target).Error; err != nil {
		c.JSON(http.StatusNotFound, map[string]string{"error": "成员不存在"})
		return
	}
	if target.ShopID != shopID {
		c.JSON(http.StatusForbidden, map[string]string{"error": "无权操作其他店铺的成员"})
		return
	}

	// last-owner 保护：当前 role=owner → 新 role=staff，且本店是最后一个 active owner → 拒
	if target.Role == storage.RoleOwner && newRole == storage.RoleStaff {
		var n int64
		storage.DB.WithContext(ctx).Model(&storage.ShopAdmin{}).
			Where("shop_id = ? AND role = ? AND status != ? AND id <> ?",
				shopID, storage.RoleOwner, "disabled", targetID).
			Count(&n)
		if n == 0 {
			c.JSON(http.StatusBadRequest, map[string]string{
				"error": "本店最后一个 owner，不能降级；请先把其他人升为 owner",
			})
			return
		}
	}

	if err := storage.DB.WithContext(ctx).
		Model(&storage.ShopAdmin{}).
		Where("id = ?", targetID).
		Updates(map[string]interface{}{"role": newRole, "updated_at": time.Now()}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, map[string]string{"status": "updated"})
}

// ============================================================
// POST /api/admin/members/:id/reset-password —— owner 重置密码
// ============================================================

// resetMemberPasswordHandler POST /api/admin/members/:id/reset-password
//
// 权限：PermManageMembers (owner-only)
//
// 自我保护：不能重置自己（用 /change-password 改自己）
// 跨店保护：target shop_id == 当前
func resetMemberPasswordHandler(ctx context.Context, c *app.RequestContext) {
	shopID := shopFromClaims(c)
	if shopID == "" {
		c.JSON(http.StatusUnauthorized, map[string]string{"error": "no shop in session"})
		return
	}
	cl := auth.GetClaims(c)
	if cl == nil {
		c.JSON(http.StatusUnauthorized, map[string]string{"error": "no session"})
		return
	}
	idStr := c.Param("id")
	targetID, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "member id 格式错误"})
		return
	}
	var req ResetPasswordRequest
	if err := c.BindAndValidate(&req); err != nil {
		c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if len(req.NewPassword) < 6 {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "新密码至少 6 位"})
		return
	}
	if storage.DB == nil {
		c.JSON(http.StatusServiceUnavailable, map[string]string{"error": "db not initialized"})
		return
	}
	// 自我保护
	if targetID == cl.AdminID {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "不能重置自己的密码（请用修改密码功能）"})
		return
	}
	// 跨店保护
	var target storage.ShopAdmin
	if err := storage.DB.WithContext(ctx).Where("id = ?", targetID).First(&target).Error; err != nil {
		c.JSON(http.StatusNotFound, map[string]string{"error": "成员不存在"})
		return
	}
	if target.ShopID != shopID {
		c.JSON(http.StatusForbidden, map[string]string{"error": "无权操作其他店铺的成员"})
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.NewPassword), bcrypt.DefaultCost)
	if err != nil {
		c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if err := storage.DB.WithContext(ctx).Model(&storage.ShopAdmin{}).
		Where("id = ?", targetID).
		Updates(map[string]interface{}{
			"password_hash": string(hash),
			"updated_at":    time.Now(),
		}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, map[string]string{"status": "password_reset"})
}

// ============================================================
// DELETE /api/admin/members/:id —— 停用成员
// ============================================================

// disableMemberHandler DELETE /api/admin/members/:id
//
// 权限：PermManageMembers (owner-only)
//
// 行为：软停用（status='disabled'），不物理删除（保留审计线索）
// 自我保护：不能停用自己
// last-owner 保护：最后 1 个 active owner 不能停用
func disableMemberHandler(ctx context.Context, c *app.RequestContext) {
	shopID := shopFromClaims(c)
	if shopID == "" {
		c.JSON(http.StatusUnauthorized, map[string]string{"error": "no shop in session"})
		return
	}
	cl := auth.GetClaims(c)
	if cl == nil {
		c.JSON(http.StatusUnauthorized, map[string]string{"error": "no session"})
		return
	}
	idStr := c.Param("id")
	targetID, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "member id 格式错误"})
		return
	}
	if storage.DB == nil {
		c.JSON(http.StatusServiceUnavailable, map[string]string{"error": "db not initialized"})
		return
	}
	// 自我保护
	if targetID == cl.AdminID {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "不能停用自己"})
		return
	}
	// 跨店保护 + 查 target
	var target storage.ShopAdmin
	if err := storage.DB.WithContext(ctx).Where("id = ?", targetID).First(&target).Error; err != nil {
		c.JSON(http.StatusNotFound, map[string]string{"error": "成员不存在"})
		return
	}
	if target.ShopID != shopID {
		c.JSON(http.StatusForbidden, map[string]string{"error": "无权操作其他店铺的成员"})
		return
	}
	// 幂等：已停用的直接返回
	if target.Status == "disabled" {
		c.JSON(http.StatusOK, map[string]string{"status": "already_disabled"})
		return
	}
	// last-owner 保护
	if target.Role == storage.RoleOwner {
		var n int64
		storage.DB.WithContext(ctx).Model(&storage.ShopAdmin{}).
			Where("shop_id = ? AND role = ? AND status != ? AND id <> ?",
				shopID, storage.RoleOwner, "disabled", targetID).
			Count(&n)
		if n == 0 {
			c.JSON(http.StatusBadRequest, map[string]string{
				"error": "本店最后一个 owner，不能停用；请先把其他人升为 owner",
			})
			return
		}
	}

	if err := storage.DB.WithContext(ctx).Model(&storage.ShopAdmin{}).
		Where("id = ?", targetID).
		Updates(map[string]interface{}{
			"status":     "disabled",
			"updated_at": time.Now(),
		}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, map[string]string{"status": "disabled"})
}

// ============================================================
// GET /api/admin/roles —— 列角色 + 权限（owner 看，方便管理）
// ============================================================

// listRolesHandler GET /api/admin/roles
//
// 返回：所有 role 当前的权限（owner 看，方便"成员管理"页展示）
// 权限：PermManageMembers (owner-only)
func listRolesHandler(ctx context.Context, c *app.RequestContext) {
	if storage.DB == nil {
		c.JSON(http.StatusServiceUnavailable, map[string]string{"error": "db not initialized"})
		return
	}
	roles := make([]map[string]any, 0, len(storage.AllRoles))
	for _, role := range storage.AllRoles {
		perms, err := storage.GetRolePermissions(ctx, role)
		if err != nil {
			c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		if perms == nil {
			perms = []string{}
		}
		roles = append(roles, map[string]any{
			"role":        role,
			"permissions": perms,
		})
	}
	c.JSON(http.StatusOK, map[string]any{
		"roles":             roles,
		"all_permissions":  storage.AllPermissions,
	})
}

// ============================================================
// 内部 helpers
// ============================================================

// 静默吞错：未来加日志埋点
var _ = errors.New
