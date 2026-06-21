package storage

import (
	"time"
)

// 所有模型显式声明表名，避免 GORM 默认复数化规则（EventLog → event_log vs event_logs）的歧义。

func (Shop) TableName() string         { return "shops" }
func (Barber) TableName() string       { return "barbers" }
func (Customer) TableName() string     { return "customers" }
func (Appointment) TableName() string  { return "appointments" }
func (Subscription) TableName() string { return "subscriptions" }
func (WecomMessageLog) TableName() string { return "wecom_message_logs" }
func (ReminderLog) TableName() string  { return "reminder_logs" }
func (EventLog) TableName() string     { return "event_logs" }
func (BarberLeave) TableName() string  { return "barber_leaves" }
func (Service) TableName() string      { return "services" }

// Shop 店铺（对应 PRD §11.4 Shop）
//
// 多店支持：每条 Shop 独立 CorpID / AgentID / Secret，回调时按 CorpID 反查 Shop。
type Shop struct {
	ID            string    `gorm:"primaryKey;size:64" json:"id"`
	Name          string    `gorm:"size:128;not null" json:"name"`
	Address       string    `gorm:"size:256" json:"address"`
	Timezone      string    `gorm:"size:64;default:Asia/Shanghai" json:"timezone"`
	OpenHour      int       `gorm:"default:9" json:"open_hour"`   // 09:00
	CloseHour     int       `gorm:"default:18" json:"close_hour"` // 18:00
	LunchStart    int       `gorm:"default:12" json:"lunch_start"`
	LunchEnd      int       `gorm:"default:13" json:"lunch_end"`
	LunchEndMin   int       `gorm:"default:30" json:"lunch_end_min"`
	Plan          string    `gorm:"size:32;default:basic" json:"plan"`
	ExpiresAt     time.Time `json:"expires_at"`
	AutoRenew     bool      `gorm:"default:false" json:"auto_renew"`
	// Holidays 节假日日期列表（逗号分隔 YYYY-MM-DD）。节假日不排班、不算爽约。
	Holidays      string    `gorm:"size:512;default:" json:"holidays,omitempty"`
	// 企业微信对接字段
	WecomCorpID         string `gorm:"size:64;index" json:"wecom_corp_id"`
	WecomAgentID        int    `json:"wecom_agent_id"`
	WecomSecret         string `gorm:"size:128" json:"-"`
	WecomToken          string `gorm:"size:64" json:"-"`
	WecomEncodingAESKey string `gorm:"size:64" json:"-"`
	WecomKFLink         string `gorm:"size:512" json:"wecom_kf_link"`
	CreatedAt           time.Time `json:"created_at"`
	UpdatedAt           time.Time `json:"updated_at"`
}

// ShopAdmin 商户后台账号（PRD §11.2 多店隔离）
//   - 每个 Admin 绑定一个 Shop（FK），登录后只能看自己的店
//   - 密码用 bcrypt 哈希存储
type ShopAdmin struct {
	ID           uint64    `gorm:"primaryKey;autoIncrement" json:"id"`
	ShopID       string    `gorm:"size:64;index;not null" json:"shop_id"`
	Username     string    `gorm:"size:64;uniqueIndex;not null" json:"username"`
	PasswordHash string    `gorm:"size:128;not null" json:"-"`
	Role         string    `gorm:"size:16;default:owner" json:"role"` // owner / staff
	LastLoginAt  *time.Time `json:"last_login_at,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// Barber 理发师（对应 PRD §11.4 Stylist）
type Barber struct {
	ID        string    `gorm:"primaryKey;size:64" json:"id"`
	ShopID    string    `gorm:"size:64;index" json:"shop_id"`
	Name      string    `gorm:"size:64;uniqueIndex;not null" json:"name"`
	Skills    string    `gorm:"size:256" json:"skills"`         // 逗号分隔
	Active    bool      `gorm:"default:true" json:"active"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Customer 顾客（对应 PRD §11.4 Customer）
type Customer struct {
	ID             string    `gorm:"primaryKey;size:64" json:"id"`
	WechatOpenID   string    `gorm:"size:128;uniqueIndex" json:"wechat_open_id"` // KF external_userid
	ExternalUserID string    `gorm:"size:128;index" json:"external_user_id"`      // 外部联系人 external_userid
	Phone          string    `gorm:"size:32;index" json:"phone"`
	Name           string    `gorm:"size:64" json:"name"`
	Tags           string    `gorm:"size:256" json:"tags"` // VIP / 黑名单 等
	TotalVisits    int       `gorm:"default:0" json:"total_visits"`
	NoShowCount    int       `gorm:"default:0" json:"no_show_count"`     // 爽约累计（用于黑名单判断）
	LateCancelCount int      `gorm:"default:0" json:"late_cancel_count"` // 晚退订累计（提前不足 free_window 取消；用于黑名单判断）
	LastVisitAt    *time.Time `json:"last_visit_at,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// Appointment 预约（对应 PRD §11.4 Appointment）
//   - 唯一索引 (barber_id, date, time, status) 在 active 状态下保证同一时段不重复
//   - 实际并发控制靠 Redis 锁，DB 唯一索引是兜底
type Appointment struct {
	ID         string    `gorm:"primaryKey;size:64" json:"id"`
	ShopID     string    `gorm:"size:64;index" json:"shop_id"`
	BarberID   string    `gorm:"size:64;index;not null" json:"barber_id"`
	BarberName string    `gorm:"size:64;index" json:"barber_name"`
	CustomerID string    `gorm:"size:64;index" json:"customer_id"`
	Customer   string    `gorm:"size:64" json:"customer"` // 冗余顾客姓名，避免 join
	Date       string    `gorm:"size:10;index;not null" json:"date"`
	Time       string    `gorm:"size:5;index;not null" json:"time"`  // HH:MM
	Service    string    `gorm:"size:64;default:剪发" json:"service"`
	Status     string    `gorm:"size:16;default:active;index" json:"status"` // active / cancelled / completed / noshow
	Source     string    `gorm:"size:16;default:wecom" json:"source"`       // wecom / web / manual
	// P3 取消策略联动（2026-06-21）
	//   - CancelType 记录本次取消的类型：early_cancel / late_cancel / after_due / admin / system / ""
	//   - CancelledAt 记录取消时间（独立字段便于查询/分析，updated_at 也包含但语义不清晰）
	//   - CancelReason 可选原因（商户后台填）
	CancelType   string     `gorm:"size:16;index" json:"cancel_type,omitempty"`
	CancelledAt  *time.Time `gorm:"index" json:"cancelled_at,omitempty"`
	CancelReason string     `gorm:"size:256" json:"cancel_reason,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

// Subscription 订阅（对应 PRD §11.4 Subscription）
type Subscription struct {
	ID         string     `gorm:"primaryKey;size:64" json:"id"`
	ShopID     string     `gorm:"size:64;index;not null" json:"shop_id"`
	Plan       string     `gorm:"size:32;not null" json:"plan"` // basic / pro / flagship
	StartedAt  time.Time  `json:"started_at"`
	ExpiresAt  time.Time  `gorm:"index" json:"expires_at"`
	AutoRenew  bool       `gorm:"default:false" json:"auto_renew"`
	CancelledAt *time.Time `json:"cancelled_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

// WecomMessageLog 企业微信消息回调去重表（PRD §11.1 MsgId 幂等去重）
//   - 用 MsgId 唯一索引做持久化去重，重启不丢
//   - 同时记录 OpenKfID / FromUserName 便于排错
type WecomMessageLog struct {
	ID            uint64    `gorm:"primaryKey;autoIncrement" json:"id"`
	MsgID         int64     `gorm:"uniqueIndex;not null" json:"msg_id"`
	MsgType       string    `gorm:"size:16" json:"msg_type"`
	Event         string    `gorm:"size:32" json:"event"`
	OpenKfID      string    `gorm:"size:64;index" json:"open_kf_id"`
	FromUserName  string    `gorm:"size:128;index" json:"from_user_name"`
	ToUserName    string    `gorm:"size:128" json:"to_user_name"`
	Processed     bool      `gorm:"default:true" json:"processed"`
	ReceivedAt    time.Time `gorm:"index" json:"received_at"`
	CreatedAt     time.Time `json:"created_at"`
}

// ReminderLog 提醒发送日志（对应 PRD §11.4 ReminderLog）
//   - 唯一索引 (appointment_id, reminder_type) 防重复发
type ReminderLog struct {
	ID            uint64    `gorm:"primaryKey;autoIncrement" json:"id"`
	AppointmentID string    `gorm:"size:64;uniqueIndex:idx_appt_reminder;not null" json:"appointment_id"`
	ReminderType  string    `gorm:"size:32;uniqueIndex:idx_appt_reminder" json:"reminder_type"` // pre_2h / noshow_followup / noshow_auto_remark
	Channel       string    `gorm:"size:16;default:wecom" json:"channel"`
	Status        string    `gorm:"size:16;default:pending" json:"status"` // pending / sent / failed
	Error         string    `gorm:"size:512" json:"error"`
	SentAt        *time.Time `json:"sent_at,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// EventLog 续费转化漏斗埋点（PRD §11.2）
//
// 关键节点（按 §8.2 续费动作链）：
//   - first_appointment     首次预约完成
//   - d3_active             D+3 推送"恭喜完成第一次自动预约"
//   - d15_active            D+15 推送使用报告
//   - d25_renew_reminder    D+25 提醒首月到期 + 年付优惠
//   - d7_expired_warning    到期前 7 天筛选高频使用者运营 1v1
//   - renewed               续费成功
//   - expired               到期未续
//   - cancelled             取消订阅
type EventLog struct {
	ID         uint64    `gorm:"primaryKey;autoIncrement" json:"id"`
	ShopID     string    `gorm:"size:64;index;not null" json:"shop_id"`
	CustomerID string    `gorm:"size:64;index" json:"customer_id"`
	EventType  string    `gorm:"size:32;index;not null" json:"event_type"`
	RefID      string    `gorm:"size:64" json:"ref_id"` // 关联 ID
	Meta       string    `gorm:"size:2048" json:"meta"`  // JSON 备注
	CreatedAt  time.Time `gorm:"index" json:"created_at"`
}

// BarberLeave 理发师请假记录（PRD §11.7 P4）
//
// 业务场景：理发师临时有事（生病/家里有事），商户在后台点"请假"。
// 系统根据 Action 把 [StartAt, EndAt] 区间内的未来预约：
//   - cancel     : 全部取消 + 通知顾客
//   - reschedule : 改派到同档期其他 active 理发师；改派失败的兜底取消 + 通知
//
// 状态机：
//   - active    : 生效中（当前时间在 [StartAt, EndAt] 内，或尚未到 EndAt）
//   - cancelled : 商户主动撤销（仅允许在 StartAt 之前撤销）
//   - expired   : 已经过 EndAt（cron 兜底标记，或简单查询时过滤）
//
// Penalty 联动（P3）：取消走 source="admin"，不计入顾客 late_cancel / no_show。
type BarberLeave struct {
	ID         string     `gorm:"primaryKey;size:64" json:"id"`
	ShopID     string     `gorm:"size:64;index;not null" json:"shop_id"`
	BarberID   string     `gorm:"size:64;index;not null" json:"barber_id"`
	BarberName string     `gorm:"size:64" json:"barber_name"` // 冗余，便于审计
	StartAt    time.Time  `gorm:"index;not null" json:"start_at"`
	EndAt      time.Time  `gorm:"index;not null" json:"end_at"`
	Reason     string     `gorm:"size:256" json:"reason"`           // 病假/家中有事/紧急出差
	Action     string     `gorm:"size:16;not null" json:"action"`   // cancel / reschedule
	Status     string     `gorm:"size:16;default:active;index" json:"status"`
	AffectedCount int      `gorm:"default:0" json:"affected_count"` // 影响的预约数（cancel 或 reschedule 总和）
	RescheduledCount int  `gorm:"default:0" json:"rescheduled_count"`
	CancelledCount int    `gorm:"default:0" json:"cancelled_count"`
	CreatedBy  string     `gorm:"size:64" json:"created_by"`         // 商户后台用户名
	CreatedAt  time.Time  `gorm:"index" json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

// Service 服务项目（PRD §11.4 v4.4 服务目录）
//
// 业务场景：商户在后台维护本店可提供的服务（剪发/烫发/染发/护理...），
// 每项服务有名字、预估时长、价格区间。后端 Agent 工具 list_services 用此表。
//
// 多店隔离：每个 Service 绑定一个 ShopID。
type Service struct {
	ID            string    `gorm:"primaryKey;size:64" json:"id"`
	ShopID        string    `gorm:"size:64;index;not null" json:"shop_id"`
	Name          string    `gorm:"size:64;not null" json:"name"`            // 剪发/烫发/染发/洗吹/护理/造型/其他
	EstimatedMin  int       `gorm:"default:30" json:"estimated_min"`          // 预估时长(分钟)
	PriceRange    string    `gorm:"size:64" json:"price_range"`              // 价格区间描述，如 "80-120"
	IsActive      bool      `gorm:"default:true;index" json:"is_active"`     // false = 已下架（保留历史）
	SortOrder     int       `gorm:"default:0" json:"sort_order"`             // 列表展示顺序，asc
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}