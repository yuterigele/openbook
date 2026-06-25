package server

// reply_mode.go
//
// AGENT_REPLY_MODE 环境变量控制 sendReply 是否打真实企业微信
//
//   - "real" (默认): 正常发企业微信（生产）
//   - "mock":         不发企业微信，写 event_logs + log，用于 demo / 调试
//
//   - 默认 real（生产安全）
//   - main.go 启动时根据 env 设置（见 SetReplyMode）
//
// v4.13.1 加：投资人 demo 兜底场景——企业未认证 / 接待人员接管时，agent 推理 + 数据库写入
// 仍然全跑通，只是最后"发微信"这一步改成 log + DB 记录，demo 屏幕可以演示完整业务流。

import (
	"context"
	"log"
	"time"

	"github.com/yuterigele/openbook/storage"
)

// replyMode 控制 sendReply 行为
//   - "real"（默认）: 走真实企业微信
//   - "mock":          跳过企业微信，写 event_logs + log
//
// 用 package-level 变量（不是 Config 字段），因为 sendReply 是 package-level function
// 不依赖 Server 实例，注入 env-driven 全局开关更简洁。
var replyMode = "real"

// SetReplyMode 启动时由 main.go 根据 AGENT_REPLY_MODE env 设置
//
//   - main.go: cfg.ReplyMode = os.Getenv("AGENT_REPLY_MODE"); server.SetReplyMode(cfg.ReplyMode)
//   - 默认 "real"——任何 env 拼错或没设都不会触发 mock（生产安全）
func SetReplyMode(mode string) {
	if mode == "mock" {
		replyMode = "mock"
		log.Printf("[wecom] ⚠️ AGENT_REPLY_MODE=mock 已启用：所有回复不发给企业微信，写到 event_logs")
		return
	}
	replyMode = "real"
}

// IsMockReplyMode 查询当前模式
func IsMockReplyMode() bool {
	return replyMode == "mock"
}

// logDemoReply 把 demo 模式下的"回复"写到 event_logs + log
//
// 写 event_logs 表的好处：admin 后台事件流能直接看到 demo 期间的"回复历史"，
// 不需要额外页面就能演示完整链路（agent 推理 → 写预约 → "回复"顾客）。
func logDemoReply(ctx context.Context, shopID, fromUser, openKfID, reply string) {
	log.Printf("[demo-reply] to=%s openKfID=%s shop=%s reply=%q",
		fromUser, openKfID, shopID, reply)

	// 写 event_logs：admin 后台能直接看到这条 demo 回复
	if storage.DB == nil {
		return // 测试环境 DB 未初始化，log 一行就够
	}
	storage.TrackEvent(ctx, shopID, storage.EventDemoReply,
		"", // 无 appointment id
		map[string]any{
			"to_user":   fromUser,
			"open_kfid": openKfID,
			"reply":     reply,
			"timestamp": time.Now().Unix(),
		})
}