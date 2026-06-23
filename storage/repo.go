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
//
// 同时排除：
//   - 已被预约的时段（appt.status='active'）
//   - 落在 active 请假区间内的时段（PRD §11.7.9 P4 工具层请假感知）
//
// 时区：Asia/Shanghai（与 Appointment.Date/Time 一致）
//
// 返回的 slots 是经过两道过滤后的纯可预约时段；调用方若需要"哪些时段被请假占用"
// 的文案，请再调 `ListBarberLeavesInRange` 自行渲染。
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

	// 加载当天 active leave（区间相交即可捕获）
	loc, _ := time.LoadLocation("Asia/Shanghai")
	dayStart, derr := time.ParseInLocation("2006-01-02", date, loc)
	if derr != nil {
		// 日期格式异常时不做 leave 过滤（保持旧行为不崩）
		out := make([]string, 0, len(DefaultSlots))
		for _, slot := range DefaultSlots {
			if !bookedSet[slot] {
				out = append(out, slot)
			}
		}
		return out
	}
	dayEnd := dayStart.Add(24 * time.Hour)
	leaves, _ := ListBarberLeavesInRange(context.Background(), barber.ID, dayStart, dayEnd)

	// 计算哪些 slot 被 leave 覆盖
	onLeave := leaveCoveredSlots(leaves, date, loc)

	out := make([]string, 0, len(DefaultSlots))
	for _, slot := range DefaultSlots {
		if bookedSet[slot] || onLeave[slot] {
			continue
		}
		out = append(out, slot)
	}
	return out
}

// leaveCoveredSlots 把落在任何 leave [start_at, end_at] 区间内的 DefaultSlot 标为 true
//
// 用法：QueryAvailableSlots 在过滤 booked 之后再过滤 leave；
// 也可以单独给其他场景（如 admin UI 高亮）调用。
func leaveCoveredSlots(leaves []BarberLeave, date string, loc *time.Location) map[string]bool {
	out := make(map[string]bool)
	dayStart, err := time.ParseInLocation("2006-01-02", date, loc)
	if err != nil {
		return out
	}
	for _, l := range leaves {
		for _, slot := range DefaultSlots {
			slotAt, err := time.ParseInLocation("15:04", slot, loc)
			if err != nil {
				continue
			}
			slotAt = time.Date(dayStart.Year(), dayStart.Month(), dayStart.Day(),
				slotAt.Hour(), slotAt.Minute(), 0, 0, loc)
			// 区间 [l.StartAt, l.EndAt] 含端点（与 IsBarberOnLeaveAt 一致）
			if !slotAt.Before(l.StartAt) && !slotAt.After(l.EndAt) {
				out[slot] = true
			}
		}
	}
	return out
}

// ScheduleBreakdown 单日排班的分组视图（PRD §11.7.10 v3.6 query_schedule 用）
//
// 三个维度拆开，让 tools/query_schedule 渲染时能视觉区分"可约"vs"请假"vs"已约满"：
//   - Available   : 顾客可以约的 slot（已扣除 booked + leave）
//   - LeaveBlocks : 当天 active leave 的「起-止 + 原因」列表（用于文案拼"师傅请假"提示）
//   - BookedCount : 被预约占用的 slot 数（不展开明细，避免长尾刷屏；用 "其余 X 个时段已约满" 提示即可）
type ScheduleBreakdown struct {
	Available   []string
	LeaveBlocks []LeaveBlock
	BookedCount int
}

// LeaveBlock 一段请假区间（用于文案）
type LeaveBlock struct {
	StartHM string // "14:00"
	EndHM   string // "16:00"
	Reason  string
}

// QueryScheduleBreakdown 返回单日排班的分组视图
//
// 一次性返回 available + leave blocks + booked count，调用方不用再拼 SQL。
// 用途：query_schedule 工具让 Agent 知道"为什么 11:00-13:00 不能约"（是预约占的？还是师傅请假？）
//
// 返回规则：
//   - barber 不存在 / 不 active：返回零值（nil slices + 0 count），不报错——query_schedule
//     工具已经在外层判断过 barber 存在，这里只兜底
//   - date 格式错误：跳过 leave 过滤，Available = booked 之外的 slot；LeaveBlocks 为空
//   - 全天都没有 leave 时 LeaveBlocks 为空 slice（非 nil，json 友好）
func QueryScheduleBreakdown(barberName, date string) ScheduleBreakdown {
	var out ScheduleBreakdown
	var barber Barber
	if err := mustDB().Where("name = ? AND active = ?", barberName, true).First(&barber).Error; err != nil {
		return out
	}

	var booked []Appointment
	mustDB().Where("barber_id = ? AND date = ? AND status = ?", barber.ID, date, "active").
		Select("time").Find(&booked)
	bookedSet := make(map[string]bool, len(booked))
	for _, a := range booked {
		bookedSet[a.Time] = true
	}

	loc, _ := time.LoadLocation("Asia/Shanghai")
	dayStart, derr := time.ParseInLocation("2006-01-02", date, loc)
	if derr != nil {
		// 日期格式异常时退化为"只过滤 booked"模式
		out.Available = make([]string, 0, len(DefaultSlots))
		for _, slot := range DefaultSlots {
			if !bookedSet[slot] {
				out.Available = append(out.Available, slot)
			}
		}
		out.BookedCount = len(booked)
		return out
	}
	dayEnd := dayStart.Add(24 * time.Hour)
	leaves, _ := ListBarberLeavesInRange(context.Background(), barber.ID, dayStart, dayEnd)

	onLeave := leaveCoveredSlots(leaves, date, loc)

	out.Available = make([]string, 0, len(DefaultSlots))
	for _, slot := range DefaultSlots {
		if bookedSet[slot] || onLeave[slot] {
			continue
		}
		out.Available = append(out.Available, slot)
	}

	// 构造 LeaveBlocks（按 start_at ASC，ListBarberLeavesInRange 已排序）
	out.LeaveBlocks = make([]LeaveBlock, 0, len(leaves))
	for _, l := range leaves {
		out.LeaveBlocks = append(out.LeaveBlocks, LeaveBlock{
			StartHM: l.StartAt.In(loc).Format("15:04"),
			EndHM:   l.EndAt.In(loc).Format("15:04"),
			Reason:  strings.TrimSpace(l.Reason),
		})
	}

	out.BookedCount = len(booked)
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
	return CreateAppointmentFull(shopID, barberName, customer, "", "", "", date, timeStr, service)
}

// CreateAppointmentFull 完整版：带 wecom openID/externalUserID + phone，事务里 upsert 顾客档案
//
// 调用链路（v4.8 / v4.9.3 关键链路）：
//
//	企业微信回调 (server.go:handleWeComMessageWithOpenKfID)
//	   ↓ 注入 ctx：WithShopID + WithOpenID + WithExternalUserID
//	tools/create_appointment.go:CreateAppointmentTool.InvokableRun
//	   ↓ ValidatePhone（11 位、1 开头） + 调 storage.CreateAppointmentFull
//	CreateAppointmentFull（事务）
//	   ↓ upsertCustomerInTx（按 phone → openID → external_user_id → name 顺序查重）
//	   ↓ tx.Create(&Appointment{CustomerID: cust.ID, ...})
//
// 关键设计点：
//   - 顾客档案必须在事务里查到/建好，appointment.customer_id 必须填上
//   - 否则 admin 顾客列表空、详情 404、reminder cron 找不到人
//   - phone 是 v4.9.3 必填项（工具层 ValidatePhone 校验），是查重最稳的键
//
// 历史 bug（修过的）：
//   - v4.8: CreateAppointmentWithShop 不建顾客档案，admin 顾客列表空
//     → 修法：拆出 CreateAppointmentFull，事务里 upsert 顾客
//   - v4.9.3: openID/externalUserID 漏透传，reminder cron 找不到人
//     → 修法：tools 加 WithOpenID / WithExternalUserID ctx 透传，server.go 注入
//   - v4.9.3: phone 没收集，老顾客档案无法补全
//     → 修法：工具必填 phone + ValidatePhone 严格校验
func CreateAppointmentFull(shopID, barberName, customer, phone, openID, externalUserID, date, timeStr, service string) (*Appointment, error) {
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

		// 顾客档案：优先按 phone / openID / externalUserID 查，没有再按名字查，最后再新建
		// v4.9.3: phone 排第一——最稳的查重键（wechat ID 可能换设备，名字可能重）
		cust, custErr := upsertCustomerInTx(tx, phone, openID, externalUserID, customer)
		if custErr != nil {
			return custErr
		}

		appt = Appointment{
			ID:         uuid.NewString(),
			ShopID:     shopID,
			BarberID:   barber.ID,
			BarberName: barber.Name,
			CustomerID: cust.ID,
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

// upsertCustomerInTx 在事务里查找/创建顾客档案
//
// 业务背景：
//   - 顾客档案是预约链路的核心，所有 cron（reminder / leave notify）都靠 customers 表反查 wecom ID
//   - 一个顾客可能有多个标识：手机号、微信 openID、企业微信 external_user_id、姓名
//   - 不同顾客可能重名；同一人可能换设备（openID 变）；同一人可能加多个微信
//   - 所以查重要按"最稳的标识"优先
//
// 查找顺序（命中即返回，不再继续）：
//  1. phone == phone（v4.9.3 最优先）
//     - 手机号最稳：换微信号/换姓名都不会影响
//     - 11 位数字、1 开头已在 tools.ValidatePhone 校验过
//     - 只要匹配 → 命中 → 自动回填缺的 openID / external_user_id
//  2. wechat_open_id == openID（且非空）
//     - 兜底：老顾客 backfill 时没 phone，但有 openID
//     - 命中后顺手回填 phone（新预约传进来的）
//  3. external_user_id == externalUserID（且非空）
//     - 企业微信外部联系人场景，比 openID 更稳（客服消息走的 external_user_id）
//  4. name == customer（兜底，匹配同名老顾客）
//     - 最后兜底：保证"老顾客没 openID 也没 phone 但预约过"也能命中
//     - 命中后把所有缺的字段都补上
//
// 全部 miss → 用 phone/openID/externalUserID/name 新建一条
//
// 顺带：命中任一分支但 phone / wechat_open_id / external_user_id 是空时，
// 会回填缺失字段（让后续匹配更准，避免重复建档案）。
//
// 重要：所有 SQL 在事务里（tx），并发安全。
// 重要：phone 没在事务里做 unique 检查，靠 MySQL index 兜底；并发极端情况下
//        可能出现两个顾客同 phone 的瞬间，第二个 INSERT 会失败（unique key 冲突），
//        由 caller 决定是否重试。
func upsertCustomerInTx(tx *gorm.DB, phone, openID, externalUserID, name string) (*Customer, error) {
	if name == "" {
		return nil, errors.New("顾客姓名不能为空")
	}
	var c Customer

	// 1) 按 phone 查（v4.9.3：手机号是最稳的查重键，优先级最高）
	//   - phone 唯一索引已经在 db.go AutoMigrate 加了（customers.phone index）
	//   - 用手机号匹配保证"同一人不同微信号/不同名"也能合并到一条档案
	if phone != "" {
		if err := tx.Where("phone = ?", phone).First(&c).Error; err == nil {
			// 命中 → 把缺的字段补上（让后续匹配更准）
			updates := map[string]string{}
			if c.WechatOpenID == "" && openID != "" {
				updates["wechat_open_id"] = openID
				c.WechatOpenID = openID
			}
			if c.ExternalUserID == "" && externalUserID != "" {
				updates["external_user_id"] = externalUserID
				c.ExternalUserID = externalUserID
			}
			if len(updates) > 0 {
				tx.Model(&c).Updates(updates)
			}
			return &c, nil
		}
	}

	// 2) 按 openID 查
	if openID != "" {
		if err := tx.Where("wechat_open_id = ?", openID).First(&c).Error; err == nil {
			// 顺便回填 phone / external_user_id
			updates := map[string]string{}
			if c.Phone == "" && phone != "" {
				updates["phone"] = phone
				c.Phone = phone
			}
			if c.ExternalUserID == "" && externalUserID != "" {
				updates["external_user_id"] = externalUserID
				c.ExternalUserID = externalUserID
			}
			if len(updates) > 0 {
				tx.Model(&c).Updates(updates)
			}
			return &c, nil
		}
	}

	// 3) 按 externalUserID 查
	if externalUserID != "" {
		if err := tx.Where("external_user_id = ?", externalUserID).First(&c).Error; err == nil {
			updates := map[string]string{}
			if c.Phone == "" && phone != "" {
				updates["phone"] = phone
				c.Phone = phone
			}
			if c.WechatOpenID == "" && openID != "" {
				updates["wechat_open_id"] = openID
				c.WechatOpenID = openID
			}
			if len(updates) > 0 {
				tx.Model(&c).Updates(updates)
			}
			return &c, nil
		}
	}

	// 4) 按 name 兜底（同名老顾客复用档案，不重复建）
	if err := tx.Where("name = ?", name).First(&c).Error; err == nil {
		// 顺便把缺的字段都补上
		updates := map[string]string{}
		if c.Phone == "" && phone != "" {
			updates["phone"] = phone
			c.Phone = phone
		}
		if c.WechatOpenID == "" && openID != "" {
			updates["wechat_open_id"] = openID
			c.WechatOpenID = openID
		}
		if c.ExternalUserID == "" && externalUserID != "" {
			updates["external_user_id"] = externalUserID
			c.ExternalUserID = externalUserID
		}
		if len(updates) > 0 {
			tx.Model(&c).Updates(updates)
		}
		return &c, nil
	}

	// 5) 全 miss，新建
	c = Customer{
		ID:             uuid.NewString(),
		Phone:          phone,
		WechatOpenID:   openID,
		ExternalUserID: externalUserID,
		Name:           name,
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
	}
	if err := tx.Create(&c).Error; err != nil {
		return nil, err
	}
	return &c, nil
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