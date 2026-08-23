package api

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/cloudwego/hertz/pkg/app"

	"github.com/yuterigele/openbook/auth"
	"github.com/yuterigele/openbook/storage"
)

func listFailedMessagesHandler(ctx context.Context, c *app.RequestContext) {
	claims := auth.GetClaims(c)
	if claims == nil {
		c.JSON(http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	shopID := claims.ShopID
	if claims.Role == storage.RolePlatformAdmin {
		shopID = strings.TrimSpace(c.Query("shop_id"))
	}
	status := strings.TrimSpace(c.Query("status"))
	if status != "" && status != storage.KfInboxRetry && status != storage.KfInboxDeadLetter && status != storage.KfInboxProcessing {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "status 只支持 retry、dead_letter 或 processing"})
		return
	}
	limit, _ := strconv.Atoi(c.Query("limit"))
	rows, err := storage.ListKfInboxMessages(ctx, shopID, status, limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, map[string]string{"error": "查询失败消息失败"})
		return
	}
	if rows == nil {
		rows = []storage.KfInboxMessage{}
	}
	c.JSON(http.StatusOK, map[string]any{"items": rows, "count": len(rows)})
}

func replayFailedMessageHandler(ctx context.Context, c *app.RequestContext) {
	claims := auth.GetClaims(c)
	if claims == nil {
		c.JSON(http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	msgID := strings.TrimSpace(c.Param("id"))
	if msgID == "" || len(msgID) > 64 {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "消息 ID 无效"})
		return
	}
	platform := claims.Role == storage.RolePlatformAdmin
	target, err := storage.GetKfInboxMessage(ctx, msgID, claims.ShopID, platform)
	if err != nil {
		c.JSON(http.StatusNotFound, map[string]string{"error": "失败消息不存在或无权访问"})
		return
	}
	if err := storage.ReplayKfInboxMessage(ctx, msgID, claims.ShopID, platform); err != nil {
		c.JSON(http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	if err := storage.WriteAudit(ctx, storage.AuditLog{
		ShopID: target.ShopID, ActorType: storage.AuditActorAdmin, ActorID: fmt.Sprintf("admin#%d", claims.AdminID),
		Action: "kf_inbox.replay", ResourceType: "kf_inbox_message", ResourceID: msgID,
		Outcome: storage.AuditOutcomeSuccess,
	}, map[string]any{"source": "admin"}); err != nil {
		log.Printf("[audit] failed-message replay audit failed msgid=%s: %v", msgID, err)
	}
	c.JSON(http.StatusAccepted, map[string]any{
		"message": "已进入重试队列，后台 worker 将自动处理", "msg_id": msgID,
	})
}
