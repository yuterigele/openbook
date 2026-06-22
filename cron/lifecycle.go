package cron

// lifecycle.go
//
// LifecycleTrigger 续费漏斗节点触发器（PRD §8.2 续费动作链 + §11.11 D+15 使用报告）
//
// D+3 / D+15 / D+25 按店铺 first_appointment 时间计算，到点：
//   - 写埋点（PRD §11.2 漏斗）
//   - 推微信（可选，依赖 wecom client）
//   - D+15 额外生成使用报告 + 发送邮件（v4.2 新增）
//
// 设计要点：
//   - 邮件发送器（notify.Sender）以 Setter 注入；未注入/未配置时 = NoopSender（不阻塞微信推送）
//   - 失败语义：邮件/微信失败只 log，不阻塞埋点；埋点本身失败也只 silent
//   - 频率：每小时跑一次（cron 精度够用，避免分钟级重复触发）

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/yuterigele/openbook/notify"
	"github.com/yuterigele/openbook/storage"
	"github.com/yuterigele/openbook/wecom"
)

// LifecycleTrigger 续费漏斗节点触发器
//
// 字段：
//   - scheduler：robfig cron
//   - client  ：wecom 客户端（可空）
//   - sender  ：邮件发送器（v4.2 新增；默认 NoopSender = 不发）
//   - reportTo：邮件收件人列表（v4.2 新增；空则不发送）
type LifecycleTrigger struct {
	scheduler *cron.Cron
	client    *wecom.Client
	sender    notify.Sender
	reportTo  []string // D+15 报告收件人列表；空时只发微信
}

// NewLifecycleTrigger 构造漏斗触发器（v4.2 起邮件 sender 默认 NoopSender，需用 SetSender / SetReportTo 启用邮件）
func NewLifecycleTrigger(client *wecom.Client) *LifecycleTrigger {
	return &LifecycleTrigger{
		scheduler: cron.New(cron.WithSeconds()),
		client:    client,
		sender:    &notify.NoopSender{},
	}
}

// SetSender 设置邮件发送器（v4.2 新增）
//
//   - nil：恢复 NoopSender
//   - 非 nil：覆盖默认值
func (l *LifecycleTrigger) SetSender(s notify.Sender) {
	if s == nil {
		l.sender = &notify.NoopSender{}
		return
	}
	l.sender = s
}

// SetReportTo 设置 D+15 邮件收件人（v4.2 新增）
//
//   - 收件人为空时：D+15 只发微信、不发邮件（保持向后兼容）
//   - 收件人非空时：D+15 同时发微信 + 邮件（store owner 邮箱）
func (l *LifecycleTrigger) SetReportTo(to []string) {
	l.reportTo = to
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
		l.triggerWecom(ctx, shopID, "🎉 您已完成第一次 AI 自动预约！习惯它每天帮您省下的沟通时间吧～")
	}

	// D+15：使用报告（v4.2 升级：渲染完整报告 + 发邮件）
	for _, shopID := range storage.FindShopsForLifecycle(ctx, 15, storage.EventD15Active) {
		l.triggerD15Report(ctx, shopID)
	}

	// D+25：年付优惠
	for _, shopID := range storage.FindShopsForLifecycle(ctx, 25, storage.EventD25RenewReminder) {
		l.triggerWecom(ctx, shopID, "⏰ 您的首月体验即将到期。\n\n现在升级年付，立省 2 个月！\n👉 回复\"续费\"了解详情")
	}
}

// triggerWecom 写埋点 + 推微信（v3.x 既有路径）
func (l *LifecycleTrigger) triggerWecom(ctx context.Context, shopID, message string) {
	storage.TrackEvent(ctx, shopID, storage.EventD3Active, "", nil)
	if l.client == nil {
		return
	}
	if err := l.client.SendTextMessage(ctx, shopID, message); err != nil {
		log.Printf("[lifecycle] 推送给 shop=%s 失败: %v", shopID, err)
	}
	log.Printf("[lifecycle] shop=%s 触发微信通知", shopID)
}

// triggerD15Report D+15 完整路径：写埋点 + 推微信 + 渲染报告 + 发邮件
//
//   - 行为：先写埋点（保证幂等），再组装 report，再发邮件，再发微信
//   - 失败处理：邮件失败不影响微信；微信失败不影响整体；埋点失败 silent
//   - 邮件收件人为空时：只发微信（向后兼容）
//   - 收件人非空时：邮件 + 微信 双发
func (l *LifecycleTrigger) triggerD15Report(ctx context.Context, shopID string) {
	// 1. 写埋点（幂等键 = shop + event + 首次触发时间，FindShopsForLifecycle 已保证唯一）
	storage.TrackEvent(ctx, shopID, storage.EventD15Active, "", nil)

	// 2. 组装使用报告
	firstApptAt := l.findFirstApptAt(ctx, shopID)
	if firstApptAt.IsZero() {
		log.Printf("[lifecycle] shop=%s D+15 找不到 first_appointment，跳过报告", shopID)
		return
	}

	rep, err := storage.BuildD15UsageReport(ctx, shopID, firstApptAt, time.Now())
	if err != nil {
		log.Printf("[lifecycle] shop=%s 组装 D+15 报告失败: %v", shopID, err)
		return
	}

	// 3. 发送邮件（收件人为空时跳过）
	if len(l.reportTo) > 0 {
		subject, html := notify.RenderD15ReportHTML(rep)
		if err := l.sender.SendHTML(ctx, l.reportTo, subject, html); err != nil {
			log.Printf("[lifecycle] shop=%s D+15 邮件发送失败: %v", shopID, err)
		} else {
			log.Printf("[lifecycle] shop=%s D+15 报告邮件已发: to=%v", shopID, l.reportTo)
		}
	}

	// 4. 发微信短摘要（保持 v3.x 行为）
	shortMsg := fmt.Sprintf("📊 您已使用 AI 预约助手半个月，共处理 %d 笔预约。\n完成率 %s / 爽约率 %s\n%s",
		rep.TotalAppointments,
		formatPercent(rep.CompletionRate),
		formatPercent(rep.NoShowRate),
		"详细报告已发送邮件，请查收。",
	)
	if l.client != nil {
		if err := l.client.SendTextMessage(ctx, shopID, shortMsg); err != nil {
			log.Printf("[lifecycle] shop=%s D+15 微信推送失败: %v", shopID, err)
		}
	}
}

// findFirstApptAt 取该店 first_appointment 事件的时间（D+0）
//
//   - 数据源：event_logs 表 MIN(created_at) WHERE event_type=first_appointment AND shop_id=?
//   - 用 map[string]any + parseAnyTime 跨 driver 兼容（同 FindShopsForLifecycle 口径）
//   - 找不到时返回 zero time（调用方决定是否跳过）
func (l *LifecycleTrigger) findFirstApptAt(ctx context.Context, shopID string) time.Time {
	if storage.DB == nil {
		return time.Time{}
	}
	var rows []map[string]any
	if err := storage.DB.WithContext(ctx).
		Table("event_logs").
		Select("MIN(created_at) as first_at").
		Where("shop_id = ? AND event_type = ?", shopID, storage.EventFirstAppointment).
		Scan(&rows).Error; err != nil {
		return time.Time{}
	}
	if len(rows) == 0 {
		return time.Time{}
	}
	t, ok := storage.ParseAnyTime(rows[0]["first_at"])
	if !ok {
		return time.Time{}
	}
	return t
}

// formatPercent 把 0.6667 格式化成 "66.7%"（与 notify 包口径一致）
func formatPercent(v float64) string {
	return fmt.Sprintf("%.1f%%", v*100)
}
