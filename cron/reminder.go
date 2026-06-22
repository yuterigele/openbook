// Package cron 提供定时任务，目前包含：
//   - 预约前 2h 自动给顾客发微信提醒（PRD §11.1 P0）
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

// Reminder 提醒发送器（注入企业微信客户端）
type Reminder struct {
	scheduler *cron.Cron
	client    *wecom.Client
}

// NewReminder 构造提醒器（不启动）
func NewReminder(client *wecom.Client) *Reminder {
	return &Reminder{
		scheduler: cron.New(cron.WithSeconds()),
		client:    client,
	}
}

// Start 启动所有定时任务
func (r *Reminder) Start(ctx context.Context) error {
	// 每分钟跑一次（秒级 cron: "0 * * * * *"）
	if _, err := r.scheduler.AddFunc("0 * * * * *", r.scanAndRemind); err != nil {
		return fmt.Errorf("注册 cron 失败: %w", err)
	}
	r.scheduler.Start()
	log.Printf("[cron] 启动提醒任务：每分钟扫描预约前 2h 的预约")
	return nil
}

// Stop 停止调度器
func (r *Reminder) Stop(ctx context.Context) error {
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

func (r *Reminder) scanAndRemind() {
	if storage.DB == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	appts, err := storage.FindAppointmentsToRemind(ctx)
	if err != nil {
		log.Printf("[cron] 查询待提醒预约失败: %v", err)
		return
	}
	if len(appts) == 0 {
		return
	}
	log.Printf("[cron] 找到 %d 条待提醒预约", len(appts))

	for _, appt := range appts {
		if err := r.sendReminder(ctx, &appt); err != nil {
			log.Printf("[cron] 发送提醒失败 appt=%s: %v", appt.ID, err)
			_ = storage.MarkReminderSent(ctx, appt.ID, "pre_2h", "wecom", "failed", err.Error())
			continue
		}
		_ = storage.MarkReminderSent(ctx, appt.ID, "pre_2h", "wecom", "sent", "")
	}
}

func (r *Reminder) sendReminder(ctx context.Context, appt *storage.Appointment) error {
	if r.client == nil {
		return fmt.Errorf("wecom client 未配置，无法发送提醒")
	}
	// 找到顾客的 external_userid（提醒发给顾客）
	var cust storage.Customer
	if appt.CustomerID != "" {
		if err := storage.DB.WithContext(ctx).Where("id = ?", appt.CustomerID).First(&cust).Error; err != nil {
			log.Printf("[cron] 找不到顾客 %s，继续按外部ID发送", appt.CustomerID)
		}
	}
	target := cust.ExternalUserID
	if target == "" && cust.WechatOpenID != "" {
		target = cust.WechatOpenID
	}
	if target == "" {
		return fmt.Errorf("顾客 %s 无 external_userid，无法发提醒", appt.CustomerID)
	}

	// OpenKfID 优先从店铺配置里取，没有则用默认（兼容 MVP）
	openKfID := "" // 由 caller 在 NewReminder 之前把 open_kfid 放进 wecom.Config；这里先空着，走 SendTextMessage 兜底
	_ = openKfID

	text := fmt.Sprintf("⏰ 预约提醒\n您预约的 %s 理发师（%s %s）将在 2 小时后开始，记得准时到店哦～",
		appt.BarberName, appt.Date, appt.Time)

	// 优先走 KF 接口，没有 openKfID 时退到普通消息
	// 简化处理：调 SendTextMessage（企业应用消息），后续接入 KF 后切到 SendKfTextMessage
	return r.client.SendTextMessage(ctx, target, text)
}