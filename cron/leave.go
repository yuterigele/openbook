// Package cron 提供定时任务
//
// leave.go：P4 理发师请假过期扫描器
//
// 业务背景：
//   - 商户创建请假（CreateBarberLeave）时，leave row 状态 = "active"
//   - 顾客通知 / 预约取消 / 改派都在创建时一次性处理完
//   - end_at 过了之后，leave row 应该转 "expired" 状态（避免脏数据 + 让 UI 区分"已过期"）
//
// 为什么不"创建时就预计算状态"？
//   - 创建时只知道 StartAt / EndAt，状态会随时间变化
//   - 数据库存的是绝对时间（time.Time），cron 才是状态机的执行者
//
// 性能：
//   - 索引：status（idx_status）+ end_at（idx_end_at）
//   - 查询：WHERE status='active' AND end_at < ?
//   - 频率：每分钟一次（noshow 是每 5 分钟；leave 边界更敏感）
package cron

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/yuterigele/openbook/storage"
)

// LeaveExpirer 理发师请假过期扫描器
type LeaveExpirer struct {
	scheduler *cron.Cron
}

// NewLeaveExpirer 构造过期扫描器
func NewLeaveExpirer() *LeaveExpirer {
	return &LeaveExpirer{
		scheduler: cron.New(cron.WithSeconds()),
	}
}

// Start 启动扫描任务（每分钟一次）
func (l *LeaveExpirer) Start(ctx context.Context) error {
	if _, err := l.scheduler.AddFunc("0 * * * * *", l.scan); err != nil {
		return fmt.Errorf("注册 leave expirer cron 失败: %w", err)
	}
	l.scheduler.Start()
	log.Printf("[cron] 启动理发师请假过期扫描：每分钟扫一次，end_at < now 的 active leave 标记为 expired")
	return nil
}

// Stop 停止调度器
func (l *LeaveExpirer) Stop(ctx context.Context) error {
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

// scan 扫描所有 end_at < now 的 active leave，标记为 expired 并写埋点
//
// 为什么不发微信通知？
//   - 顾客通知在 CreateBarberLeave 时已经发完（取消/改派文案）
//   - expire 是后台状态机迁移，对顾客无感知
//   - 商户也不需要被通知"你的请假过期了"——他从列表里能看到状态变化
func (l *LeaveExpirer) scan() {
	if storage.DB == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	now := time.Now()
	expired, err := storage.ExpireOverdueLeaves(ctx, now)
	if err != nil {
		log.Printf("[leave-expirer] 扫描失败: %v", err)
		return
	}
	if expired > 0 {
		log.Printf("[leave-expirer] 已过期 %d 条请假", expired)
	}
}
