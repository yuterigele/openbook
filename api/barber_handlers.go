package api

// barber_handlers.go
//
// P5 理发师管理 handler（PRD §11.2 商户后台 — 理发师管理）
//
// 设计要点：
//   - 4 个 endpoint：
//     * GET    /api/admin/barbers            列出本店所有 barber（含 inactive 默认开）
//     * POST   /api/admin/barbers            新建 barber（body: name, skills）
//     * DELETE /api/admin/barbers/:id        软删除（有未来 active 预约时 409 拒绝）
//     * POST   /api/admin/barbers/:id/activate 重新激活（误删恢复）
//   - shopID 一律从 JWT claims 取，杜绝 URL 注入
//   - 返回错误用统一 map[string]string{"error": ...}
//
// 与 P4 leave handler 的关系：
//   - leave handler 已经在 api.go 里，本文件只放 CRUD 不放 leave
//   - 共享 AuthClaims / shopFromClaims / currentOperator 等 helper
//
// 错误码约定：
//   - 400 输入校验失败（空 name / 超长）
//   - 401 没有 session
//   - 403 跨店操作（理论上 shopID 从 claims 取后不会发生，留作兜底）
//   - 404 barber 不存在
//   - 409 冲突（同名 / 有未来预约）
//   - 500 其它 DB 异常

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/cloudwego/hertz/pkg/app"

	"github.com/cloudwego/eino-examples/quickstart/chatwitheino/storage"
)

// ---- list ----

// listBarbersHandler 列出本店所有 barber
//
// GET /api/admin/barbers?include_inactive=true
//   - include_inactive 不传或 "false"：只列 active（用于"今天能服务的师傅"）
//   - include_inactive=true：包含 inactive（用于后台完整管理视图）
//
// 响应：[]Barber
func listBarbersHandler(ctx context.Context, c *app.RequestContext) {
	shopID := shopFromClaims(c)
	if shopID == "" {
		c.JSON(http.StatusUnauthorized, map[string]string{"error": "no shop in session"})
		return
	}
	includeInactive := false
	if v, err := strconv.ParseBool(c.Query("include_inactive")); err == nil {
		includeInactive = v
	}
	bs, err := storage.ListAllBarbersByShop(ctx, shopID, includeInactive)
	if err != nil {
		c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if bs == nil {
		bs = []storage.Barber{}
	}
	c.JSON(http.StatusOK, bs)
}

// ---- create ----

// createBarberHandler 新建 barber
//
// POST /api/admin/barbers
// Body: { name: "Tony", skills: "剪发,染发" }
// 响应：{ id, name, skills, active, ... }
func createBarberHandler(ctx context.Context, c *app.RequestContext) {
	shopID := shopFromClaims(c)
	if shopID == "" {
		c.JSON(http.StatusUnauthorized, map[string]string{"error": "no shop in session"})
		return
	}
	var req struct {
		Name   string `json:"name"`
		Skills string `json:"skills"`
	}
	if err := c.BindAndValidate(&req); err != nil {
		c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if req.Name == "" {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "name 必填"})
		return
	}

	b, err := storage.CreateBarber(ctx, shopID, req.Name, req.Skills)
	if err != nil {
		switch {
		case errors.Is(err, storage.ErrBarberNameTaken):
			c.JSON(http.StatusConflict, map[string]string{"error": err.Error()})
		default:
			// 长度超限 / DB 异常统一 400/500
			if isValidationErr(err) {
				c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
			} else {
				c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
			}
		}
		return
	}
	c.JSON(http.StatusOK, b)
}

// ---- soft delete ----

// softDeleteBarberHandler 软删除 barber（有未来 active 预约时 409 拒绝）
//
// DELETE /api/admin/barbers/:id
// 响应：{ status: "deleted", name: "Tony" }
//
// 拒绝条件：
//   - 跨店（404，伪装成"不存在"，不泄漏存在性）
//   - 有未来 active 预约（409）
func softDeleteBarberHandler(ctx context.Context, c *app.RequestContext) {
	shopID := shopFromClaims(c)
	if shopID == "" {
		c.JSON(http.StatusUnauthorized, map[string]string{"error": "no shop in session"})
		return
	}
	barberID := c.Param("id")
	if barberID == "" {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "barber id required"})
		return
	}
	// 先拿一下名字（错误信息更友好）
	b, getErr := storage.GetBarberInShop(ctx, shopID, barberID)
	if errors.Is(getErr, storage.ErrBarberNotFoundInShop) {
		c.JSON(http.StatusNotFound, map[string]string{"error": "理发师不存在"})
		return
	}
	if getErr != nil {
		c.JSON(http.StatusInternalServerError, map[string]string{"error": getErr.Error()})
		return
	}
	if err := storage.SoftDeleteBarber(ctx, shopID, barberID); err != nil {
		switch {
		case errors.Is(err, storage.ErrBarberNotFoundInShop):
			c.JSON(http.StatusNotFound, map[string]string{"error": "理发师不存在"})
		case isFutureApptErr(err):
			c.JSON(http.StatusConflict, map[string]string{"error": err.Error()})
		default:
			c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		}
		return
	}
	c.JSON(http.StatusOK, map[string]any{
		"status": "deleted",
		"name":   b.Name,
	})
}

// ---- activate ----

// activateBarberHandler 重新激活一个 inactive 的 barber（误删恢复）
//
// POST /api/admin/barbers/:id/activate
// 响应：{ status: "activated", name: "Tony" }
func activateBarberHandler(ctx context.Context, c *app.RequestContext) {
	shopID := shopFromClaims(c)
	if shopID == "" {
		c.JSON(http.StatusUnauthorized, map[string]string{"error": "no shop in session"})
		return
	}
	barberID := c.Param("id")
	if barberID == "" {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "barber id required"})
		return
	}
	b, getErr := storage.GetBarberInShop(ctx, shopID, barberID)
	if errors.Is(getErr, storage.ErrBarberNotFoundInShop) {
		c.JSON(http.StatusNotFound, map[string]string{"error": "理发师不存在"})
		return
	}
	if getErr != nil {
		c.JSON(http.StatusInternalServerError, map[string]string{"error": getErr.Error()})
		return
	}
	if err := storage.ActivateBarber(ctx, shopID, barberID); err != nil {
		if errors.Is(err, storage.ErrBarberNotFoundInShop) {
			c.JSON(http.StatusNotFound, map[string]string{"error": "理发师不存在"})
			return
		}
		c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, map[string]any{
		"status": "activated",
		"name":   b.Name,
	})
}

// ---- error classification helpers ----

// isValidationErr 判断是否为输入校验类错误（超长 / 空字符串）
//
// CreateBarber 的"必填""过长"用 errors.New 或 fmt.Errorf，message 含中文。
// 简单判断：包含"不能为空"或"过长"。
func isValidationErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "不能为空") ||
		strings.Contains(msg, "过长") ||
		strings.Contains(msg, "必填")
}

// isFutureApptErr 判断是否为"有未来预约"的错误
func isFutureApptErr(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "未来")
}