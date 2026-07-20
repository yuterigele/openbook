// Package cron 提供定时任务（PRD §11.1 P0 + §11.2 P1）
//
// 任务列表：
//   - 预约前 2h 提醒（reminder.go）
//   - 爽约扫描（noshow.go）     ← 新增 P1-1
//   - 续费漏斗 D+N 节点（lifecycle.go）  ← 新增 P1-3
package cron

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/robfig/cron/v3"
	"gorm.io/gorm"

	"github.com/yuterigele/openbook/storage"
	"github.com/yuterigele/openbook/wecom"
)

// NoShowScanner 爽约扫描器
type NoShowScanner struct {
	scheduler *cron.Cron
	client    *wecom.Client
}

// NewNoShowScanner 构造爽约扫描器
func NewNoShowScanner(client *wecom.Client) *NoShowScanner {
	return &NoShowScanner{
		scheduler: cron.New(cron.WithSeconds()),
		client:    client,
	}
}

// Start 启动所有任务（每 5 分钟扫一次）
func (n *NoShowScanner) Start(ctx context.Context) error {
	if _, err := n.scheduler.AddFunc("0 */5 * * * *", n.scan); err != nil {
		return fmt.Errorf("注册 noshow cron 失败: %w", err)
	}
	n.scheduler.Start()
	log.Printf("[cron] 启动爽约扫描：每 5 分钟扫一次，预约时间 +30min 未到 = noshow")
	return nil
}

// Stop 停止调度器
func (n *NoShowScanner) Stop(ctx context.Context) error {
	if n.scheduler == nil {
		return nil
	}
	stops := n.scheduler.Stop()
	select {
	case <-stops.Done():
	case <-ctx.Done():
		return ctx.Err()
	}
	return nil
}

// scan 扫描所有"过了预约时间 30 分钟还没标记完成/取消/爽约"的 active 预约
//
// 节假日跳过：店铺设为休息日不营业的日期不算爽约（避免误判）。
func (n *NoShowScanner) scan() {
	if storage.DB == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		loc = time.FixedZone("CST", 8*3600)
	}
	now := time.Now().In(loc)
	cutoff := now.Add(-30 * time.Minute)

	// 性能优化：只查昨天 + 今天 + 明天的 active 预约（30 分钟前的预约最多跨一天）
	yesterday := now.AddDate(0, 0, -1).Format("2006-01-02")
	tomorrow := now.AddDate(0, 0, 1).Format("2006-01-02")
	var appts []storage.Appointment
	if err := storage.DB.WithContext(ctx).
		Where("status = ? AND date >= ? AND date <= ?", "active", yesterday, tomorrow).
		Find(&appts).Error; err != nil {
		log.Printf("[noshow] 扫描失败: %v", err)
		return
	}

	// 缓存店铺节假日，避免每个 appt 都查一次 DB
	shopCache := make(map[string]*storage.Shop)
	getShop := func(id string) *storage.Shop {
		if s, ok := shopCache[id]; ok {
			return s
		}
		s, _ := storage.GetShopByID(ctx, id)
		shopCache[id] = s
		return s
	}

	for _, a := range appts {
		t, err := time.ParseInLocation("2006-01-02 15:04", a.Date+" "+a.Time, loc)
		if err != nil {
			continue
		}
		// 过了预约时间 30 分钟还没动静 → 标记 noshow
		if t.After(cutoff) {
			continue
		}
		// 节假日跳过
		if storage.IsShopHoliday(getShop(a.ShopID), a.Date) {
			log.Printf("[noshow] 跳过节假日 appt=%s date=%s", a.ID, a.Date)
			continue
		}
		if err := n.markNoShow(ctx, &a); err != nil {
			log.Printf("[noshow] 标记失败 appt=%s: %v", a.ID, err)
		}
	}
}

func (n *NoShowScanner) markNoShow(ctx context.Context, appt *storage.Appointment) error {
	now := time.Now()
	if err := storage.DB.WithContext(ctx).
		Model(&storage.Appointment{}).
		Where("id = ? AND status = ?", appt.ID, "active").
		Updates(map[string]interface{}{
			"status":          "noshow",
			"active_slot_key": nil,
			"updated_at":      now,
		}).Error; err != nil {
		return err
	}

	// 增加顾客 NoShowCount
	if appt.CustomerID != "" {
		storage.DB.WithContext(ctx).Model(&storage.Customer{}).
			Where("id = ?", appt.CustomerID).
			UpdateColumns(map[string]interface{}{
				"no_show_count": gorm.Expr("no_show_count + 1"),
				"updated_at":    now,
			})
	}

	// 埋点
	storage.TrackEvent(ctx, appt.ShopID, storage.EventAppointmentNoShow, appt.ID, map[string]any{
		"customer":    appt.Customer,
		"barber_name": appt.BarberName,
		"date":        appt.Date,
		"time":        appt.Time,
	})

	// 自动重排推荐：给顾客发一条引导消息（PRD §2 "爽约管理 + 智能重排"）
	// 注意：MVP 阶段仅提示，不主动给新时段（避免越权帮顾客做决定）
	if n.client != nil && appt.CustomerID != "" {
		var cust storage.Customer
		if err := storage.DB.WithContext(ctx).Where("id = ?", appt.CustomerID).First(&cust).Error; err == nil {
			target := cust.ExternalUserID
			if target == "" {
				target = cust.WechatOpenID
			}
			if target != "" {
				text := fmt.Sprintf(
					"亲爱的 %s，您 %s %s 的预约已过 30 分钟未到店，标记为爽约。\n\n"+
						"如有需要可随时跟我说\"重新预约\"，我帮您换个时间。",
					appt.Customer, appt.Date, appt.Time,
				)
				if err := n.client.SendTextMessage(ctx, target, text); err != nil {
					log.Printf("[noshow] 引导消息发送失败 customer=%s: %v", appt.CustomerID, err)
				}
			}
		}
	}

	log.Printf("[noshow] 已标记 appt=%s customer=%s date=%s %s", appt.ID, appt.Customer, appt.Date, appt.Time)
	return nil
}
