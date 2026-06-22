package cron

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/yuterigele/openbook/storage"
	"github.com/yuterigele/openbook/wecom"
)

// SubscriptionNotifier 订阅到期通知器（PRD #9）
//
//   - 每天扫一次，扫到 expires_at 在 [now, now+3d] 区间内且未发过提醒 → 推商户
//   - 扫到 expires_at < now 且未续费 → 发 expired 事件 + 推商户（"您的服务已到期"）
type SubscriptionNotifier struct {
	scheduler *cron.Cron
	client    *wecom.Client
}

func NewSubscriptionNotifier(client *wecom.Client) *SubscriptionNotifier {
	return &SubscriptionNotifier{
		scheduler: cron.New(cron.WithSeconds()),
		client:    client,
	}
}

func (s *SubscriptionNotifier) Start(ctx context.Context) error {
	// 每天 9:00 跑一次（标准 cron 6 段式）
	if _, err := s.scheduler.AddFunc("0 0 9 * * *", s.scan); err != nil {
		return fmt.Errorf("注册 subscription cron 失败: %w", err)
	}
	s.scheduler.Start()
	log.Printf("[cron] 启动订阅到期通知：每天 9:00 扫")
	return nil
}

func (s *SubscriptionNotifier) Stop(ctx context.Context) error {
	if s.scheduler == nil {
		return nil
	}
	stops := s.scheduler.Stop()
	select {
	case <-stops.Done():
	case <-ctx.Done():
		return ctx.Err()
	}
	return nil
}

func (s *SubscriptionNotifier) scan() {
	if storage.DB == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	now := time.Now()
	soon := now.Add(3 * 24 * time.Hour)

	var subs []storage.Subscription
	if err := storage.DB.WithContext(ctx).
		Where("cancelled_at IS NULL AND expires_at <= ?", soon).
		Order("expires_at asc").
		Find(&subs).Error; err != nil {
		log.Printf("[subscription] 扫描失败: %v", err)
		return
	}

	for _, sub := range subs {
		s.handle(ctx, &sub, now)
	}
}

func (s *SubscriptionNotifier) handle(ctx context.Context, sub *storage.Subscription, now time.Time) {
	shopID := sub.ShopID
	daysLeft := int(time.Until(sub.ExpiresAt).Hours() / 24)

	// 已过期
	if daysLeft < 0 {
		// 发 expired 事件（埋点）
		storage.TrackEvent(ctx, shopID, storage.EventExpired, sub.ID, map[string]any{
			"plan":      sub.Plan,
			"expired_at": sub.ExpiresAt,
		})
		s.notifyShop(ctx, shopID,
			fmt.Sprintf("⚠️ 您的 %s 服务已于 %s 到期。回复\"续费\"立即恢复服务。",
				sub.Plan, sub.ExpiresAt.Format("2006-01-02")))
		return
	}

	// 即将过期（3 天内）—— 只在 D-3 / D-1 推送，避免每天骚扰
	shouldNotify := false
	switch daysLeft {
	case 3:
		shouldNotify = true
	case 1:
		shouldNotify = true
	case 0:
		shouldNotify = true
	}
	if !shouldNotify {
		return
	}

	// 防重复推送
	has, _ := storage.HasShopEvent(ctx, shopID, storage.EventD7ExpiredWarning)
	if has && daysLeft > 1 {
		return
	}
	storage.TrackEvent(ctx, shopID, storage.EventD7ExpiredWarning, sub.ID, map[string]any{
		"plan":      sub.Plan,
		"days_left": daysLeft,
		"expires_at": sub.ExpiresAt,
	})

	s.notifyShop(ctx, shopID,
		fmt.Sprintf("⏰ 您的 %s 服务将在 %d 天后（%s）到期。\n\n回复\"续费\"或联系客服提前续期，避免服务中断。",
			sub.Plan, daysLeft, sub.ExpiresAt.Format("2006-01-02")))
}

// notifyShop 推送给店铺（暂时用 shopID 当 userID；多店上线后改成 owner UserID）
func (s *SubscriptionNotifier) notifyShop(ctx context.Context, shopID, message string) {
	if s.client == nil {
		log.Printf("[subscription] ⚠️ no wecom client, skip notify: %s", message)
		return
	}
	if err := s.client.SendTextMessage(ctx, shopID, message); err != nil {
		log.Printf("[subscription] 推送失败 shop=%s: %v", shopID, err)
		return
	}
	log.Printf("[subscription] 推送成功 shop=%s", shopID)
}