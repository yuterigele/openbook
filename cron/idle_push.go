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

// IdleSlotPusher 空闲时段主动推送器（PRD §11.3 P2 旗舰版：理发师空闲时段主动推送）
//
// 逻辑：
//   - 每天 11:00 / 17:00 各跑一次
//   - 扫今天剩余的时段，找出所有空档
//   - 给"30+ 天没来 + 最近来过"的休眠客推一条「今天 XX 时段有空，要不要来？」
//   - 节流：同一顾客同一时段当天只推一次（用 EventLog 当天记录判断）
//   - 多店：每家店独立推送
type IdleSlotPusher struct {
	scheduler *cron.Cron
	client    *wecom.Client
}

func NewIdleSlotPusher(client *wecom.Client) *IdleSlotPusher {
	return &IdleSlotPusher{
		scheduler: cron.New(cron.WithSeconds()),
		client:    client,
	}
}

func (p *IdleSlotPusher) Start(ctx context.Context) error {
	// 11:00 推下午空档；17:00 推晚间空档（标准 cron 6 段：秒 分 时 日 月 周）
	for _, spec := range []string{"0 0 11 * * *", "0 0 17 * * *"} {
		if _, err := p.scheduler.AddFunc(spec, p.scan); err != nil {
			return fmt.Errorf("注册 idle cron (%s) 失败: %w", spec, err)
		}
	}
	p.scheduler.Start()
	log.Printf("[cron] 启动空闲时段主动推送：每天 11:00 / 17:00 扫一次")
	return nil
}

func (p *IdleSlotPusher) Stop(ctx context.Context) error {
	if p.scheduler == nil {
		return nil
	}
	stops := p.scheduler.Stop()
	select {
	case <-stops.Done():
	case <-ctx.Done():
		return ctx.Err()
	}
	return nil
}

// pushRecord 记录今天给哪个顾客推过哪个时段（同一顾客同一天同一时段只推一次）
type pushRecord struct {
	customerID string
	shopID     string
	date       string
	time       string
}

// emptySlot 单一空档（理发师 + 时段）
type emptySlot struct {
	barberID   string
	barberName string
	time       string
}

func (p *IdleSlotPusher) scan() {
	if storage.DB == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	// 1. 取所有店铺
	var shops []storage.Shop
	if err := storage.DB.WithContext(ctx).Find(&shops).Error; err != nil {
		log.Printf("[idle] 加载店铺失败: %v", err)
		return
	}

	loc, _ := time.LoadLocation("Asia/Shanghai")
	if loc == nil {
		loc = time.FixedZone("CST", 8*3600)
	}
	now := time.Now().In(loc)
	today := now.Format("2006-01-02")

	for _, shop := range shops {
		if shop.WecomCorpID == "" {
			continue
		}
		p.pushForShop(ctx, &shop, today, now)
	}
}

func (p *IdleSlotPusher) pushForShop(ctx context.Context, shop *storage.Shop, today string, now time.Time) {
	// 2. 扫今天剩余时段（now 之后）的所有 barber 空档
	cutoff := now.Add(2 * time.Hour) // 只推 2h 内的空档（避免推"今晚 9 点"打扰别人）
	dayEnd := time.Date(now.Year(), now.Month(), now.Day(), 21, 0, 0, 0, now.Location())
	if cutoff.After(dayEnd) {
		cutoff = dayEnd
	}

	// 加载店铺的所有 barber
	var barbers []storage.Barber
	if err := storage.DB.WithContext(ctx).Where("shop_id = ? AND active = ?", shop.ID, true).Find(&barbers).Error; err != nil {
		return
	}

	// 收集所有空档（每个 barber 各自查）—— 复用包级 emptySlot
	var slots []emptySlot
	for _, b := range barbers {
		booked := make(map[string]bool)
		var appts []storage.Appointment
		storage.DB.WithContext(ctx).
			Where("shop_id = ? AND barber_id = ? AND date = ? AND status = ?", shop.ID, b.ID, today, "active").
			Find(&appts)
		for _, a := range appts {
			booked[a.Time] = true
		}
		for _, slot := range storage.DefaultSlots {
			t, err := time.ParseInLocation("15:04", slot, now.Location())
			if err != nil {
				continue
			}
			slotTime := time.Date(now.Year(), now.Month(), now.Day(), t.Hour(), t.Minute(), 0, 0, now.Location())
			if slotTime.Before(now) || slotTime.After(cutoff) {
				continue
			}
			if !booked[slot] {
				slots = append(slots, emptySlot{
					barberID:   b.ID,
					barberName: b.Name,
					time:       slot,
				})
			}
		}
	}
	if len(slots) == 0 {
		return
	}
	log.Printf("[idle] shop=%s 今天有 %d 个空档待推", shop.ID, len(slots))

	// 3. 找"休眠客"：30+ 天没来 + 最近 60 天来过（避免打扰从未到店的人）
	//
	// 注意：Customer 模型没有 shop_id 字段（顾客跨店共享），所以不再用 shopID 过滤。
	var dormant []storage.Customer
	cutoffDate := now.AddDate(0, 0, -30).Format("2006-01-02")
	recentDate := now.AddDate(0, 0, -60).Format("2006-01-02")
	if err := storage.DB.WithContext(ctx).
		Where("last_visit_at IS NOT NULL").
		Where("DATE(last_visit_at) <= ?", cutoffDate).
		Where("DATE(last_visit_at) >= ?", recentDate).
		Where("tags NOT LIKE ?", "%BLACKLIST%").
		Limit(50). // 防止一次推太多
		Find(&dormant).Error; err != nil {
		log.Printf("[idle] 加载休眠客失败: %v", err)
		return
	}
	if len(dormant) == 0 {
		return
	}

	// 4. 给每个休眠客挑一个最合适的空档（VIP/常客优先推 Tony 老顾客）
	for _, c := range dormant {
		if c.ExternalUserID == "" && c.WechatOpenID == "" {
			continue
		}
		// 节流：今天已经推过这家店的，跳过
		hasPushed, _ := storage.HasShopEvent(ctx, shop.ID, storage.EventIdleSlotPush+":"+today+":"+c.ID)
		if hasPushed {
			continue
		}

		// 给顾客挑个空档：优先选他/她之前约过的 barber
		slot := p.pickSlotForCustomer(ctx, c, slots, shop.ID)
		if slot == nil {
			continue
		}

		// 推消息
		if err := p.pushToCustomer(ctx, c, shop, slot, today); err != nil {
			log.Printf("[idle] 推送给 %s 失败: %v", c.Name, err)
			continue
		}
		storage.TrackEvent(ctx, shop.ID, storage.EventIdleSlotPush+":"+today+":"+c.ID, "", map[string]any{
			"customer": c.Name,
			"barber":   slot.barberName,
			"time":     slot.time,
		})
		log.Printf("[idle] 推送成功: shop=%s customer=%s barber=%s time=%s", shop.ID, c.Name, slot.barberName, slot.time)
	}
}

// pickSlotForCustomer 给顾客挑空档
//
// 策略：
//   - 优先该顾客上次约过的 barber（识别"老顾客找老理发师"）
//   - 否则随机挑
func (p *IdleSlotPusher) pickSlotForCustomer(ctx context.Context, c storage.Customer, slots []emptySlot, shopID string) *emptySlot {
	if len(slots) == 0 {
		return nil
	}
	// 查该顾客最近一次成功完成的预约的 barber
	var last storage.Appointment
	err := storage.DB.WithContext(ctx).
		Where("customer_id = ? AND shop_id = ? AND status IN ('completed','active')", c.ID, shopID).
		Order("created_at DESC").
		First(&last).Error
	if err == nil && last.BarberID != "" {
		for i, s := range slots {
			if s.barberID == last.BarberID {
				return &slots[i]
			}
		}
	}
	// fallback：第一个
	return &slots[0]
}

// pushToCustomer 推消息（带个性化）
func (p *IdleSlotPusher) pushToCustomer(ctx context.Context, c storage.Customer, shop *storage.Shop, slot *emptySlot, today string) error {
	if p.client == nil {
		return fmt.Errorf("no wecom client")
	}
	target := c.ExternalUserID
	if target == "" {
		target = c.WechatOpenID
	}
	if target == "" {
		return fmt.Errorf("customer has no openid")
	}

	// 根据顾客标签定制文案
	tagSet := storage.NewTagSet(c.Tags)
	var greet string
	switch {
	case tagSet.Has(storage.TagVIP):
		greet = fmt.Sprintf("尊贵的 %s 您好", c.Name)
	case tagSet.Has(storage.TagFrequent):
		greet = fmt.Sprintf("%s 您好呀，又见面啦", c.Name)
	default:
		greet = fmt.Sprintf("%s 您好", c.Name)
	}

	text := fmt.Sprintf(
		"%s～\n\n%s 今天 %s 时段有空档，%s 师傅等您来 ☕\n\n回复\"预约\"即可一键预约，或直接说要哪个时段～",
		greet, shop.Name, slot.time, slot.barberName,
	)

	if err := p.client.SendTextMessage(ctx, target, text); err != nil {
		return err
	}
	_ = today
	return nil
}

// 辅助：复用 storage.DB 做上下文查询
var _ = gorm.ErrRecordNotFound