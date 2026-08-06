package api

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/cloudwego/hertz/pkg/app"

	"github.com/yuterigele/openbook/auth"
	"github.com/yuterigele/openbook/storage"
)

func listAuditsHandler(ctx context.Context, c *app.RequestContext) {
	claims := auth.GetClaims(c)
	if claims == nil {
		c.JSON(http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	shopID := claims.ShopID
	if claims.Role == storage.RolePlatformAdmin && strings.TrimSpace(c.Query("shop_id")) != "" {
		shopID = strings.TrimSpace(c.Query("shop_id"))
	}
	limit, _ := strconv.Atoi(c.Query("limit"))
	rows, err := storage.ListAuditLogs(ctx, storage.AuditQuery{
		ShopID: shopID, TraceID: c.Query("trace_id"), Action: c.Query("action"),
		ResourceType: c.Query("resource_type"), ResourceID: c.Query("resource_id"),
		Outcome: c.Query("outcome"), Limit: limit,
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, map[string]string{"error": "查询审计日志失败"})
		return
	}
	if rows == nil {
		rows = []storage.AuditLog{}
	}
	c.JSON(http.StatusOK, map[string]any{"items": rows, "count": len(rows)})
}

func verifyAuditsHandler(ctx context.Context, c *app.RequestContext) {
	claims := auth.GetClaims(c)
	if claims == nil {
		c.JSON(http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	shopID := claims.ShopID
	if claims.Role == storage.RolePlatformAdmin && strings.TrimSpace(c.Query("shop_id")) != "" {
		shopID = strings.TrimSpace(c.Query("shop_id"))
	}
	if err := storage.VerifyAuditChain(ctx, shopID); err != nil {
		c.JSON(http.StatusConflict, map[string]any{"valid": false, "error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, map[string]any{"valid": true, "shop_id": shopID})
}
