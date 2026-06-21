package cron

// weekly_report.go
//
// PRD §8.2 / §11.12 周报触发器（v4.3 / v4.5 增量）
//
// 业务背景：
//   - D+15 是"开店半个月发一次"的一次性报告
//   - 周报是"每周一 9:00"重复触发，覆盖开店任意时长，让店主持续看见经营数据
//   - 用途：续费前的"复购"动机 / 每周 1 次的高频反馈 / 跨店连锁 owner 跨店汇总
//
// 设计要点：
//   - 触发时间：每周一 9:00（标准 cron 6 段 = "0 0 9 * * 1"）
//   - 邮件发送器（notify.Sender）以 Setter 注入；未注入时 = NoopSender
//   - 失败语义：邮件失败不影响整体（log）；埋点失败 silent
//   - 频率：每周 1 次，本身就幂等（不需要外部去重表）
//   - 范围：
//     - reportTo 非空：每店一封单店周报
//     - chainReportTo 非空：额外发一封跨店汇总周报（v4.5 增量）
//     - 两个收件人完全独立，可单独配置（单店 / 连锁 owner 视角不同）

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/cloudwego/eino-examples/quickstart/chatwitheino/notify"
	"github.com/cloudwego/eino-examples/quickstart/chatwitheino/storage"
)

// WeeklyReporter 周报触发器（v4.3 / v4.5）
//
//   - 不直接依赖 wecom 包（v4.3 只发邮件；微信推送留 v4.4 增量）
//   - 邮件发送器（sender）以 Setter 注入；未注入时 = NoopSender
//   - 收件人：
//     - reportTo：单店周报（每店一封）；空时单店不发邮件
//     - chainReportTo：跨店汇总周报（连锁 owner 视角）；空时不发跨店邮件
type WeeklyReporter struct {
	scheduler     *cron.Cron
	sender        notify.Sender
	reportTo      []string
	chainReportTo []string
}

// NewWeeklyReporter 构造周报触发器
//
//   - sender 默认 NoopSender（未配置 SMTP 时不真发邮件）
func NewWeeklyReporter() *WeeklyReporter {
	return &WeeklyReporter{
		scheduler: cron.New(cron.WithSeconds()),
		sender:    &notify.NoopSender{},
	}
}

// SetSender 设置邮件发送器（v4.3 与 LifecycleTrigger 同模式）
func (r *WeeklyReporter) SetSender(s notify.Sender) {
	if s == nil {
		r.sender = &notify.NoopSender{}
		return
	}
	r.sender = s
}

// SetReportTo 设置单店周报邮件收件人
//
//   - 空时：单店周报不发邮件（仅写埋点 + 静默 no-op）
//   - 非空时：每店一封单店周报邮件
func (r *WeeklyReporter) SetReportTo(to []string) {
	r.reportTo = to
}

// SetChainReportTo 设置跨店汇总周报邮件收件人（v4.5 增量）
//
//   - 空时：跨店周报不发邮件（仅写埋点 + 静默 no-op）
//   - 非空时：额外发一封跨店汇总周报邮件（连锁 owner 视角）
//   - 与 reportTo 独立：可单独配置某一方
func (r *WeeklyReporter) SetChainReportTo(to []string) {
	r.chainReportTo = to
}

// Start 启动周报 cron（每周一 9:00）
func (r *WeeklyReporter) Start(ctx context.Context) error {
	// 标准 6 段 cron：秒 分 时 日 月 周
	// "0 0 9 * * 1" = 每周一 9:00:00
	if _, err := r.scheduler.AddFunc("0 0 9 * * 1", r.scan); err != nil {
		return fmt.Errorf("注册 weekly report cron 失败: %w", err)
	}
	r.scheduler.Start()
	log.Printf("[cron] 启动周报触发器：每周一 9:00 扫一次所有店铺")
	return nil
}

// Stop 停止
func (r *WeeklyReporter) Stop(ctx context.Context) error {
	if r.scheduler == nil {
		return nil
	}
	stops := r.scheduler.Stop()
	select {
	case <-stops.Done():
	case <-ctx.Done():
		return ctx.Err()
	}
	return nil
}

// scan 周一 9:00 触发：拉所有店铺 → 逐店组装报告 → 发邮件
//
//   - 行为：ListAllShopIDs → 逐店 BuildWeeklyUsageReport → 渲染 + 发邮件
//   - 跨店增量（v4.5）：reportTo / chainReportTo 任一非空就发；两者独立
//   - 失败语义：单店失败不阻塞其他店；邮件失败不阻塞下一店；埋点失败 silent
func (r *WeeklyReporter) scan() {
	if storage.DB == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	shopIDs, err := storage.ListAllShopIDs(ctx)
	if err != nil {
		log.Printf("[weekly] 列店铺失败: %v", err)
		return
	}
	if len(shopIDs) == 0 {
		return
	}

	now := time.Now()

	// 跨店汇总（v4.5 增量）— chainReportTo 配置才发；与逐店完全独立
	if len(r.chainReportTo) > 0 {
		r.triggerChain(ctx, now)
	}

	// 单店逐店（v4.3）
	if len(r.reportTo) > 0 {
		for _, shopID := range shopIDs {
			r.triggerOne(ctx, shopID, now)
		}
	}
}

// triggerOne 单店完整路径：写埋点 + 组装报告 + 发邮件
//
//   - 写埋点（v4.3：每周一 9:00 一次，cron 频率已保证幂等）
//   - 组装 WeeklyReport
//   - 渲染 HTML + 发邮件（收件人为空时跳过）
func (r *WeeklyReporter) triggerOne(ctx context.Context, shopID string, now time.Time) {
	// 1. 写埋点（v4.3：每周一 9:00 一次，cron 频率已保证幂等）
	storage.TrackEvent(ctx, shopID, storage.EventWeeklyReport, shopID, map[string]any{
		"week_start": now.AddDate(0, 0, -storage.WeeklyReportWindowDays).Format("2006-01-02"),
		"recipients": len(r.reportTo),
	})

	// 2. 组装周报
	rep, err := storage.BuildWeeklyUsageReport(ctx, shopID, now)
	if err != nil {
		log.Printf("[weekly] shop=%s 组装周报失败: %v", shopID, err)
		return
	}

	// 3. 发邮件（收件人为空时跳过）
	if len(r.reportTo) > 0 {
		subject, html := notify.RenderWeeklyReportHTML(rep)
		if err := r.sender.SendHTML(ctx, r.reportTo, subject, html); err != nil {
			log.Printf("[weekly] shop=%s 邮件发送失败: %v", shopID, err)
		} else {
			log.Printf("[weekly] shop=%s 周报邮件已发: to=%v", shopID, r.reportTo)
		}
	} else {
		log.Printf("[weekly] shop=%s 周报已组装（收件人为空，不发邮件）: total=%d", shopID, rep.TotalAppointments)
	}
}

// triggerChain 跨店汇总周报（v4.5 PRD §11.12 连锁版）
//
//   - 行为：BuildChainWeeklyUsageReport → RenderChainWeeklyReportHTML → 发邮件
//   - 与 triggerOne 关系：完全独立，单独一封连锁 owner 视角的邮件
//   - 失败语义：邮件失败不 panic（log 即可）；埋点失败 silent
//   - 频率：与逐店同步（同 cron 触发 1 次），不需要额外 cron 入口
func (r *WeeklyReporter) triggerChain(ctx context.Context, now time.Time) {
	// 1. 写埋点（每周一 9:00 一次；用空 shopID 因为是跨店）
	storage.TrackEvent(ctx, "", storage.EventWeeklyReport, "chain", map[string]any{
		"week_start":  now.AddDate(0, 0, -storage.WeeklyReportWindowDays).Format("2006-01-02"),
		"recipients":  len(r.chainReportTo),
		"scope":       "chain",
	})

	// 2. 组装跨店周报
	rep, err := storage.BuildChainWeeklyUsageReport(ctx, now)
	if err != nil {
		log.Printf("[weekly] 跨店周报组装失败: %v", err)
		return
	}
	if rep.ShopCount == 0 {
		log.Printf("[weekly] 跨店周报：暂无店铺，跳过")
		return
	}

	// 3. 发邮件（chainReportTo 由 scan 层 guard，这里非空）
	subject, html := notify.RenderChainWeeklyReportHTML(rep)
	if err := r.sender.SendHTML(ctx, r.chainReportTo, subject, html); err != nil {
		log.Printf("[weekly] 跨店周报邮件发送失败: %v", err)
	} else {
		log.Printf("[weekly] 跨店周报邮件已发: shops=%d, total_appts=%d, to=%v",
			rep.ShopCount, rep.Total.TotalAppointments, r.chainReportTo)
	}
}
