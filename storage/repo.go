package storage

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// 保留旧 memory.go 的 API 形态，让 tools/ 和旧调用方尽量不动；
// 所有方法改为走 DB（DB 可能在 InitDB 后才非 nil；开发期或本地无 DB 时会直接报错）

// 全局默认时段（每天 09:00-18:00，午休 12:00-13:30 不可预约）
var DefaultSlots = []string{
	"09:00", "09:30", "10:00", "10:30", "11:00", "11:30",
	"13:30", "14:00", "14:30", "15:00", "15:30", "16:00",
	"16:30", "17:00", "17:30", "18:00",
}

var (
	ErrSlotTaken   = errors.New("时段已被预约")
	ErrBarberNotFound = errors.New("理发师不存在")
	ErrAppointmentNotFound = errors.New("预约不存在")
	ErrAlreadyCancelled = errors.New("预约已取消")
)

func mustDB() *gorm.DB {
	if DB == nil {
		panic("storage.DB 未初始化：请在 main 里调用 storage.InitDB")
	}
	return DB
}

// QueryAvailableSlots 查询理发师在指定日期的可预约时段
func QueryAvailableSlots(barberName, date string) []string {
	var barber Barber
	if err := mustDB().Where("name = ? AND active = ?", barberName, true).First(&barber).Error; err != nil {
		return nil
	}

	var booked []Appointment
	mustDB().Where("barber_id = ? AND date = ? AND status = ?", barber.ID, date, "active").
		Select("time").Find(&booked)

	bookedSet := make(map[string]bool, len(booked))
	for _, a := range booked {
		bookedSet[a.Time] = true
	}
	out := make([]string, 0, len(DefaultSlots))
	for _, slot := range DefaultSlots {
		if !bookedSet[slot] {
			out = append(out, slot)
		}
	}
	return out
}

// CreateAppointment 创建预约（事务内做唯一性兜底；并发锁在 tools 层加 Redis SETNX）
func CreateAppointment(barberName, customer, date, timeStr, service string) (*Appointment, error) {
	return CreateAppointmentWithShop("", barberName, customer, date, timeStr, service)
}

// CreateAppointmentWithShop 同上，但显式指定 shopID（用于多店场景）
//
//   - shopID 为空时使用 barber 默认所属店铺
//   - shopID 与 barber.ShopID 不一致时拒绝（防跨店预约错误）
func CreateAppointmentWithShop(shopID, barberName, customer, date, timeStr, service string) (*Appointment, error) {
	if service == "" {
		service = "剪发"
	}

	db := mustDB()
	var appt Appointment
	err := db.Transaction(func(tx *gorm.DB) error {
		var barber Barber
		if err := tx.Where("name = ? AND active = ?", barberName, true).First(&barber).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrBarberNotFound
			}
			return err
		}

		// shopID 校验
		if shopID == "" {
			shopID = barber.ShopID
		} else if shopID != barber.ShopID {
			return fmt.Errorf("理发师 %s 不属于店铺 %s", barberName, shopID)
		}

		// 黑名单拦截（PRD §4.2 旗舰版）
		if err := checkBlacklist(tx, customer, shopID); err != nil {
			return err
		}

		// 兜底唯一性
		var existing Appointment
		if err := tx.Where("barber_id = ? AND date = ? AND time = ? AND status = ?",
			barber.ID, date, timeStr, "active").First(&existing).Error; err == nil {
			return ErrSlotTaken
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}

		appt = Appointment{
			ID:         uuid.NewString(),
			ShopID:     shopID,
			BarberID:   barber.ID,
			BarberName: barber.Name,
			Customer:   customer,
			Date:       date,
			Time:       timeStr,
			Service:    service,
			Status:     "active",
			Source:     "wecom",
			CreatedAt:  time.Now(),
			UpdatedAt:  time.Now(),
		}
		return tx.Create(&appt).Error
	})
	if err != nil {
		return nil, err
	}
	return &appt, nil
}

// checkBlacklist 检查顾客是否在该店黑名单里
//
// 通过 Customer.Phone 匹配（最简单方案）；如果 phone 未填写，按 customer 名匹配（兜底）。
// 命中 blacklist 标签则拒绝预约。
func checkBlacklist(tx *gorm.DB, customer, shopID string) error {
	if isCustomerBlacklistedByTx(tx, customer, shopID) {
		return fmt.Errorf("很抱歉，您暂时无法预约本店铺")
	}
	return nil
}

// CancelAppointment 取消预约（兼容旧签名）
//
// 内部走 CancelAppointmentWithPolicy，source = "agent"。
// 新代码应直接调用 CancelAppointmentWithPolicy 以拿到 CancelResult（含 penalty / blacklist 副作用）。
//
// 带 WHERE status='active' 兜底，并发安全。
// 已经 cancelled / completed / noshow 的不能再 cancel。
func CancelAppointment(appointmentID string) error {
	_, err := CancelAppointmentWithPolicy(context.Background(), appointmentID, CancelSourceAgent, "")
	return err
}

// GetAppointment 获取预约
func GetAppointment(appointmentID string) (*Appointment, error) {
	var appt Appointment
	if err := mustDB().Where("id = ?", appointmentID).First(&appt).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrAppointmentNotFound
		}
		return nil, err
	}
	return &appt, nil
}

// GetBarberByName 根据姓名获取理发师
func GetBarberByName(name string) (*Barber, error) {
	var b Barber
	if err := mustDB().Where("name = ?", name).First(&b).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrBarberNotFound
		}
		return nil, err
	}
	return &b, nil
}

// FindOrCreateCustomer 根据 wechat open id 查找/创建顾客
func FindOrCreateCustomer(ctx context.Context, openID, externalUserID, name string) (*Customer, error) {
	db := mustDB().WithContext(ctx)
	var c Customer
	if openID != "" {
		if err := db.Where("wechat_open_id = ?", openID).First(&c).Error; err == nil {
			return &c, nil
		}
	}
	if externalUserID != "" {
		if err := db.Where("external_user_id = ?", externalUserID).First(&c).Error; err == nil {
			if openID != "" && c.WechatOpenID == "" {
				db.Model(&c).Update("wechat_open_id", openID)
				c.WechatOpenID = openID
			}
			return &c, nil
		}
	}
	// 新建
	c = Customer{
		ID:             uuid.NewString(),
		WechatOpenID:   openID,
		ExternalUserID: externalUserID,
		Name:           name,
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
	}
	if err := db.Create(&c).Error; err != nil {
		return nil, err
	}
	return &c, nil
}

// ListActiveBarbers 列出所有 active 理发师
func ListActiveBarbers(ctx context.Context) ([]Barber, error) {
	var out []Barber
	err := mustDB().WithContext(ctx).Where("active = ?", true).Order("name asc").Find(&out).Error
	return out, err
}

// FindAppointmentsToRemind 找出需要在 [from, to] 窗口内提醒的预约
//   - status=active
//   - 拼接 date+time 为本地时间（用店铺时区，这里简化用 Asia/Shanghai）
//   - 落在 [now+1m55s, now+2h5s] 范围内（容差 ±5 分钟）
//   - 还没在 ReminderLog 里发过 pre_2h 提醒
//
// 性能优化：先按 date 范围粗筛（today, today+1），避免扫全部 active 预约。
func FindAppointmentsToRemind(ctx context.Context) ([]Appointment, error) {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		loc = time.FixedZone("CST", 8*3600)
	}
	now := time.Now().In(loc)
	from := now.Add(115 * time.Minute)
	to := now.Add(125 * time.Minute)

	// 粗筛：只查今天 + 明天的 active 预约（2h 提醒窗口不可能跨 2 天）
	dateFrom := now.Format("2006-01-02")
	dateTo := now.AddDate(0, 0, 1).Format("2006-01-02")

	var appts []Appointment
	if err := mustDB().WithContext(ctx).
		Where("status = ? AND date >= ? AND date <= ?", "active", dateFrom, dateTo).
		Find(&appts).Error; err != nil {
		return nil, err
	}
	var out []Appointment
	for _, a := range appts {
		t, err := time.ParseInLocation("2006-01-02 15:04", a.Date+" "+a.Time, loc)
		if err != nil {
			continue
		}
		if !t.Before(from) && !t.After(to) {
			// 还要确认还没提醒过
			var existing ReminderLog
			err := mustDB().WithContext(ctx).
				Where("appointment_id = ? AND reminder_type = ?", a.ID, "pre_2h").
				First(&existing).Error
			if err == nil {
				continue // 已提醒过，跳过
			}
			out = append(out, a)
		}
	}
	return out, nil
}

// MarkReminderSent 写 ReminderLog
func MarkReminderSent(ctx context.Context, apptID, reminderType, channel, status, errMsg string) error {
	now := time.Now()
	rec := ReminderLog{
		AppointmentID: apptID,
		ReminderType:  reminderType,
		Channel:       channel,
		Status:        status,
		Error:         errMsg,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	if status == "sent" {
		rec.SentAt = &now
	}
	// INSERT IGNORE 语义：如果已存在就跳过
	return mustDB().WithContext(ctx).
		Where("appointment_id = ? AND reminder_type = ?", apptID, reminderType).
		Attrs(rec).
		Create(&rec).Error
}

// Init 兼容旧调用方；真实初始化由 InitDB 完成
func Init() {
	if DB == nil {
		logStorageNotInit()
	}
}

func logStorageNotInit() {
	fmt.Println("[storage] 提示：DB 未初始化，仓储方法将无法工作；请在 main 调用 storage.InitDB(ctx)")
}

// ---- 兼容旧 memory.go 的 Barber / Appointment 类型别名 ----
// tools/cancel_appointment.go 等还在用 storage.Appointment，这里保留
// 真实定义在 models.go

// ---- 工具函数 ----

// IsValidSlot 校验时段是否在 DefaultSlots 中
func IsValidSlot(t string) bool {
	for _, s := range DefaultSlots {
		if s == t {
			return true
		}
	}
	return false
}

// ParseDate 简单校验日期格式 YYYY-MM-DD
func ParseDate(d string) (time.Time, error) {
	return time.Parse("2006-01-02", strings.TrimSpace(d))
}

// GetToday / GetTomorrow 保留兼容
func GetToday() string     { return time.Now().Format("2006-01-02") }
func GetTomorrow() string  { return time.Now().AddDate(0, 0, 1).Format("2006-01-02") }