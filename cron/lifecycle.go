package cron

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/cloudwego/eino-examples/quickstart/chatwitheino/storage"
	"github.com/cloudwego/eino-examples/quickstart/chatwitheino/wecom"
)

// LifecycleTrigger 续费漏斗节点触发器（PRD §8.2 续费动作链）
//
// D+3 / D+15 / D+25 按店铺 first_appointment 时间计算，到点写埋点 + 推微信消息。
type LifecycleTrigger struct {
	scheduler *cron.Cron
	client    *wecom.Client
}

// NewLifecycleTrigger 构造漏斗触发器
func NewLifecycleTrigger(client *wecom.Client) *LifecycleTrigger {
	return &LifecycleTrigger{
		scheduler: cron.New(cron.WithSeconds()),
		client:    client,
	}
}

// Start 每小时跑一次（cron 精度够用，避免分钟级重复触发）
func (l *LifecycleTrigger) Start(ctx context.Context) error {
	if _, err := l.scheduler.AddFunc("0 0 * * * *", l.scan); err != nil {
		return fmt.Errorf("注册 lifecycle cron 失败: %w", err)
	}
	l.scheduler.Start()
	log.Printf("[cron] 启动续费漏斗触发器：每小时扫一次 D+3 / D+15 / D+25 节点")
	return nil
}

// Stop 停止
func (l *LifecycleTrigger) Stop(ctx context.Context) error {
	if l.scheduler == nil {
		return nil
	}
	stops := l.scheduler.Stop()
	select {
	case <-stops.Done():
	case <-ctx.Done():
		return ctx.Err()
	}
	return nil
}

func (l *LifecycleTrigger) scan() {
	if storage.DB == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// D+3：恭喜完成第一次自动预约
	for _, shopID := range storage.FindShopsForLifecycle(ctx, 3, storage.EventD3Active) {
		l.trigger(ctx, shopID, storage.EventD3Active, "🎉 您已完成第一次 AI 自动预约！习惯它每天帮您省下的沟通时间吧～")
	}

	// D+15：使用报告
	for _, shopID := range storage.FindShopsForLifecycle(ctx, 15, storage.EventD15Active) {
		count := storage.CountShopEvents(ctx, shopID, storage.EventAppointmentCreated)
		l.trigger(ctx, shopID, storage.EventD15Active,
			fmt.Sprintf("📊 您已使用 AI 预约助手半个月，共处理 %d 笔预约。\n看看板了解详情 → 让员工给您开通后台账号。", count))
	}

	// D+25：年付优惠
	for _, shopID := range storage.FindShopsForLifecycle(ctx, 25, storage.EventD25RenewReminder) {
		l.trigger(ctx, shopID, storage.EventD25RenewReminder,
			"⏰ 您的首月体验即将到期。\n\n现在升级年付，立省 2 个月！\n👉 回复\"续费\"了解详情")
	}
}

func (l *LifecycleTrigger) trigger(ctx context.Context, shopID, eventType, message string) {
	// 写埋点
	storage.TrackEvent(ctx, shopID, eventType, "", nil)

	// 推送：MVP 阶段只发给店铺 owner（暂时用 shop_id 当 userID；接入商户后台后改成 owner UserID）
	if l.client == nil {
		return
	}
	if err := l.client.SendTextMessage(ctx, shopID, message); err != nil {
		log.Printf("[lifecycle] 推送给 shop=%s 失败: %v", shopID, err)
	}
	log.Printf("[lifecycle] shop=%s 触发 %s", shopID, eventType)
}