package cron

// kf_seen_cleanup.go
//
// 微信客服 msgid 去重表的 TTL 清理（v4.13.1）
//
// 背景：
//   - handleKfCallback 把每条处理过的 msgid 写到 storage.kf_seen_msg（防止重启后重复处理）
//   - 表会无限增长，必须有 TTL 清理
//   - TTL = 7 天（kfSeenTTL in storage/kf_sync_state.go）：覆盖任何 sync_msg 重试窗口 + 周末
//
// 设计：
//   - 每天 3:00 跑一次（业务低峰，不影响 sync_msg 实时处理）
//   - 调 storage.CleanupKfSeenMsgs() 删 seen_at < now-7d 的行
//   - 失败 silent（log warn）+ 次日重试，不阻塞主流程
//
// Run:
//   ./chatwitheino-linux &
//
// 启动日志：
//   [cron] 启动 KfSeenMsg cleanup: 每天 3:00 清理 7 天前的 msgid 去重记录

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/yuterigele/openbook/storage"
)

// KfSeenMsgCleaner 微信客服 msgid 去重表清理器
type KfSeenMsgCleaner struct {
	scheduler *cron.Cron
}

// NewKfSeenMsgCleaner 构造清理器
func NewKfSeenMsgCleaner() *KfSeenMsgCleaner {
	return &KfSeenMsgCleaner{
		scheduler: cron.New(cron.WithSeconds()),
	}
}

// Start 启动清理 cron（每天 3:00 跑一次）
//
// 标准 6 段 cron: "0 0 3 * * *" = 每天 3:00:00
// 选 3:00 是因为大部分理发店 9 点开门，3 点是绝对低峰
func (c *KfSeenMsgCleaner) Start(_ context.Context) error {
	if _, err := c.scheduler.AddFunc("0 0 3 * * *", c.scan); err != nil {
		return fmt.Errorf("注册 kf_seen_msg cleanup cron 失败: %w", err)
	}
	c.scheduler.Start()
	log.Printf("[cron] 启动 KfSeenMsg cleanup: 每天 3:00 清理 7 天前的 msgid 去重记录")
	return nil
}

// Stop 停止
func (c *KfSeenMsgCleaner) Stop(ctx context.Context) error {
	if c.scheduler == nil {
		return nil
	}
	stops := c.scheduler.Stop()
	select {
	case <-stops.Done():
	case <-ctx.Done():
		return ctx.Err()
	}
	return nil
}

// scan 清理逻辑：删 seen_at < now-7d 的行
//
//   - 失败 silent（log warn），不影响其他 cron
//   - DB 未初始化时直接跳过（测试 / 启动早期常见）
func (c *KfSeenMsgCleaner) scan() {
	if storage.DB == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	deleted, err := storage.CleanupKfSeenMsgs(ctx)
	if err != nil {
		log.Printf("[kf-cleanup] 清理 kf_seen_msg 失败（下次重试）: %v", err)
		return
	}
	if deleted > 0 {
		log.Printf("[kf-cleanup] 清理 kf_seen_msg: 删 %d 条 7 天前的去重记录", deleted)
	} else {
		log.Printf("[kf-cleanup] 清理 kf_seen_msg: 无过期记录（表干净）")
	}
}