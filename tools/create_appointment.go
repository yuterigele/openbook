package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"

	"github.com/yuterigele/openbook/lock"
	"github.com/yuterigele/openbook/storage"
)

// ctxKeyShopID 是上下文中门店 ID 的键。
type ctxKeyShopID struct{}

// ctxKeyOpenID 是上下文中微信 openID 的键。
type ctxKeyOpenID struct{}

// ctxKeyExternalUserID 是上下文中企业微信外部用户 ID 的键。
type ctxKeyExternalUserID struct{}

// WithShopID 将门店 ID 写入上下文，供 Agent 工具读取。
func WithShopID(ctx context.Context, shopID string) context.Context {
	return context.WithValue(ctx, ctxKeyShopID{}, shopID)
}

// ShopIDFromCtx 从上下文读取门店 ID；不存在时返回空字符串。
func ShopIDFromCtx(ctx context.Context) string {
	v, _ := ctx.Value(ctxKeyShopID{}).(string)
	return v
}

// WithOpenID 将微信 openID 写入上下文。
func WithOpenID(ctx context.Context, openID string) context.Context {
	return context.WithValue(ctx, ctxKeyOpenID{}, openID)
}

// OpenIDFromCtx 从上下文读取微信 openID；不存在时返回空字符串。
func OpenIDFromCtx(ctx context.Context) string {
	v, _ := ctx.Value(ctxKeyOpenID{}).(string)
	return v
}

// WithExternalUserID 将企业微信外部用户 ID 写入上下文。
func WithExternalUserID(ctx context.Context, externalUserID string) context.Context {
	return context.WithValue(ctx, ctxKeyExternalUserID{}, externalUserID)
}

// ExternalUserIDFromCtx 从上下文读取企业微信外部用户 ID；不存在时返回空字符串。
func ExternalUserIDFromCtx(ctx context.Context) string {
	v, _ := ctx.Value(ctxKeyExternalUserID{}).(string)
	return v
}

// ValidatePhone 严格校验中国大陆手机号。
// 手机号是顾客档案的稳定查重键。规则为 11 位数字且以 1 开头；不接受空值、
// 国际号码、座机或服务号码。返回值是可直接提示顾客的友好错误信息。
// 命令行修复工具也复用本函数，确保校验规则一致。
func ValidatePhone(phone string) error {
	if phone == "" {
		return fmt.Errorf("手机号必填，请顾客提供 11 位手机号（如 13812345678）")
	}
	if len(phone) != 11 {
		return fmt.Errorf("手机号必须是 11 位（当前 %d 位：「%s」）", len(phone), phone)
	}
	if phone[0] != '1' {
		return fmt.Errorf("手机号必须以 1 开头（当前：「%s」）", phone)
	}
	for i, c := range phone {
		if c < '0' || c > '9' {
			return fmt.Errorf("手机号必须全是数字（第 %d 位不是数字：「%s」）", i+1, phone)
		}
	}
	return nil
}

// CreateAppointmentTool 创建预约工具
type CreateAppointmentTool struct{}

// Info 返回工具信息
func (t *CreateAppointmentTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: "create_appointment",
		Desc: "创建预约；需理发师、顾客姓名、11 位手机号、日期和时间。\n" +
			"调用前必须先用 query_schedule 确认空闲；相对日期换算为 YYYY-MM-DD，勿编造手机号。\n" +
			"时段不可约、师傅请假、过去时间、22:00 后或节假日时按工具结果引导换时间或师傅。\n" +
			"节假日推荐日期前先调 list_shop_holidays，再用 query_schedule 验证。\n" +
			"成功后自然确认并告知预约号；失败时用友好话术，不展示工具错误。",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"barber_name": {
				Type: "string", Desc: "理发师姓名，例如：Tony、Kevin", Required: true,
			},
			"customer": {
				Type: "string", Desc: "顾客姓名", Required: true,
			},
			"phone": {
				Type: "string", Desc: "顾客手机号，11 位数字、1 开头（用于后续到店提醒/通知）。**必填**——顾客没主动给时主动问一次。", Required: true,
			},
			"date": {
				Type: "string", Desc: "预约日期，格式：YYYY-MM-DD，例如：2026-06-20", Required: true,
			},
			"time": {
				Type: "string", Desc: "预约时间，格式：HH:MM（24 小时制），例如：15:00、09:30。**注意：顾客说「3 点」默指 15:00 下午，凌晨预约本店不接**。", Required: true,
			},
			"service": {
				Type: "string", Desc: "服务项目，默认为'剪发'。如果顾客没指定，可以先调 list_services 让他/她选。", Required: false,
			},
		}),
	}, nil
}

// InvokableRun 执行创建预约
func (t *CreateAppointmentTool) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	if lock.IsReadOnly() {
		return "", fmt.Errorf("预约服务暂时只读，暂不能创建预约，请稍后再试")
	}
	var params struct {
		BarberName string `json:"barber_name"`
		Customer   string `json:"customer"`
		Date       string `json:"date"`
		Time       string `json:"time"`
		Service    string `json:"service"`
		Phone      string `json:"phone"`
	}
	if err := json.Unmarshal([]byte(argumentsInJSON), &params); err != nil {
		return "", FriendlyError(ctx, err, "参数解析失败", "create_appointment.unmarshal")
	}
	if params.BarberName == "" || params.Customer == "" || params.Date == "" || params.Time == "" {
		return "", fmt.Errorf("barber_name / customer / date / time 均不能为空")
	}
	// v4.9.3 手机号严格验证（11 位数字、1 开头）
	//   - 工具不能凭空给顾客编手机号，没收到就拒绝，让 LLM 回去问顾客
	//   - 校验失败返回 error → LLM 看到后会主动跟顾客要
	if err := ValidatePhone(params.Phone); err != nil {
		return "", err
	}
	if err := EnsureDB("create_appointment"); err != nil {
		return "", err
	}
	if !storage.IsValidSlot(params.Time) {
		return "", fmt.Errorf("时段 %s 不在营业时间内（本店 9:00-18:00，午休 12:00-13:30 不可约）", params.Time)
	}

	// 解析 + 边界检查：过去时间 / 午休 / 太晚
	loc, _ := time.LoadLocation("Asia/Shanghai")
	if loc == nil {
		loc = time.FixedZone("CST", 8*3600)
	}
	appointmentAt, parseErr := time.ParseInLocation("2006-01-02 15:04", params.Date+" "+params.Time, loc)
	if parseErr != nil {
		return "", fmt.Errorf("时间格式无法解析: %s %s", params.Date, params.Time)
	}
	now := time.Now().In(loc)
	// 过去时间拒绝（5 分钟容差，避免边界抖动）
	if appointmentAt.Before(now.Add(-5 * time.Minute)) {
		return "", fmt.Errorf("时段 %s 已经过期；当前北京时间为 %s，请重新查询今天或未来日期的可约时段", params.Date+" "+params.Time, now.Format("2006-01-02 15:04"))
	}
	// 22:00 之后、6:00 之前 不接（早 6 点也可视为异常，防御性兜底）
	if params.Time >= "22:00" || params.Time < "06:00" {
		return "", fmt.Errorf("时段 %s 太晚了，本店最晚 22:00，请换个时间", params.Time)
	}

	// 查询理发师并取得用于预约保护的 ID。
	barber, err := storage.GetBarberByName(params.BarberName)
	if err != nil {
		return "", fmt.Errorf("师傅 %s 不在店里呢（本店现在有 Tony、Kevin 两位），换个试试？", params.BarberName)
	}

	// 拦截节假日预约。
	shop, _ := storage.GetShopByID(ctx, barber.ShopID)
	if storage.IsShopHoliday(shop, params.Date) {
		return "", fmt.Errorf("%s 是本店休息日，麻烦换个日期试试", params.Date)
	}

	// Agent 或模型供应商偶尔会在收到成功结果后重复调用工具。这不是新的预约意图；
	// 使用已验证手机号、完整时段和服务项目做精确幂等匹配，返回原预约结果。
	existing, err := storage.FindActiveAppointmentForCustomerSlot(
		ctx, barber.ShopID, barber.ID, params.Phone, params.Date, params.Time, params.Service,
	)
	if err != nil {
		return "", FriendlyError(ctx, err, "查询预约状态失败，请稍后重试", "create_appointment.idempotency_check")
	}
	if existing != nil {
		return appointmentSuccessMessage(existing), nil
	}

	// 在写入预约前检查理发师是否请假，时间语义遵循 Asia/Shanghai。
	onLeave, leave, err := storage.IsBarberOnLeaveAt(ctx, barber.ID, appointmentAt)
	if err != nil {
		// 数据库短暂异常不阻塞下单，但记录日志以便排查。
		fmt.Printf("[create_appointment] IsBarberOnLeaveAt query failed: %v\n", err)
	}
	if onLeave && leave != nil {
		// 保护隐私：始终显示“临时有事”，不暴露内部请假原因。
		// 将请假区间裁到预约当天，避免跨日信息造成歧义。
		dispStart, dispEnd := clipLeaveToDate(leave.StartAt, leave.EndAt, params.Date, loc)
		sameDay := isSameYMD(dispStart, dispEnd, loc)
		var startStr, endStr string
		if sameDay {
			startStr = dispStart.In(loc).Format("15:04")
			endStr = dispEnd.In(loc).Format("15:04")
		} else {
			startStr = dispStart.In(loc).Format("01-02 15:04")
			endStr = dispEnd.In(loc).Format("01-02 15:04")
		}
		return "", fmt.Errorf(
			"%s 师傅在 %s 至 %s 临时有事，要不要换 Kevin 师傅或换个时间？",
			params.BarberName,
			startStr,
			endStr,
		)
	}

	// 加 Redis 分布式锁（PRD §3.3 防并发预约冲突）
	lockCtx, cancel := context.WithTimeout(ctx, 5*1e9) // 5s
	defer cancel()
	l, err := lock.AcquireAppointmentLock(lockCtx, barber.ID, params.Date, params.Time)
	if err != nil {
		if errors.Is(err, lock.ErrRedisUnavailable) {
			return "", fmt.Errorf("预约保护服务暂不可用，请稍后再试")
		}
		return "", fmt.Errorf("时段 %s %s 刚被别人抢了，我帮你看下一个空档", params.Date, params.Time)
	}
	if l != nil {
		defer func() {
			unlockCtx, unlockCancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer unlockCancel()
			_ = l.Unlock(unlockCtx)
		}()
	}
	guardCtx, guardCancel := l.GuardContext(ctx)
	defer guardCancel()
	operationCtx, operationCancel := context.WithTimeout(guardCtx, 8*time.Second)
	defer operationCancel()

	appointment, err := storage.CreateAppointmentFullContext(
		operationCtx,
		ShopIDFromCtx(ctx),
		params.BarberName,
		params.Customer,
		params.Phone,               // 已通过手机号校验。
		OpenIDFromCtx(ctx),         // 透传微信 openID，用于自动建立顾客档案。
		ExternalUserIDFromCtx(ctx), // 透传企业微信外部用户 ID，用于消息通知。
		params.Date,
		params.Time,
		params.Service,
	)
	if err != nil {
		if lockErr := l.Err(); lockErr != nil {
			return "", fmt.Errorf("预约锁已失效，操作已安全回滚，请重试")
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return "", fmt.Errorf("预约处理超时，操作未确认成功，请重试")
		}
		if errors.Is(err, storage.ErrSlotTaken) {
			return "", fmt.Errorf("时段 %s %s 刚被别的顾客抢了，要不要换个时间？", params.Date, params.Time)
		}
		if errors.Is(err, storage.ErrBarberNotFound) {
			return "", fmt.Errorf("师傅 %s 不在店里呢，换个试试？", params.BarberName)
		}
		return "", fmt.Errorf("系统忙不过来了，请稍后再试")
	}
	// 写入成功不等于可以向顾客宣称成功。重新读取最终记录，确保事务提交后的
	// 门店归属、状态和关键字段都与本次请求一致；校验失败时让 Agent 安全降级，
	// 而不是生成“已预约”的幻觉回复。
	persisted, err := storage.GetAppointmentContext(operationCtx, appointment.ID)
	if err != nil || persisted.ShopID != appointment.ShopID || persisted.Status != "active" ||
		persisted.BarberName != params.BarberName || persisted.Date != params.Date || persisted.Time != params.Time {
		return "", fmt.Errorf("预约结果校验失败，请勿向顾客确认成功；请稍后查询预约状态")
	}

	// 埋点（PRD §11.2 续费漏斗）
	storage.TrackEvent(ctx, persisted.ShopID, storage.EventAppointmentCreated, persisted.ID, map[string]any{
		"barber_name": persisted.BarberName,
		"customer":    persisted.Customer,
		"date":        persisted.Date,
		"time":        persisted.Time,
	})
	// 该店铺首次预约 → 触发 D+N 漏斗起点
	if has, _ := storage.HasShopEvent(ctx, persisted.ShopID, storage.EventFirstAppointment); !has {
		storage.TrackEvent(ctx, persisted.ShopID, storage.EventFirstAppointment, persisted.ID, nil)
	}

	return appointmentSuccessMessage(persisted), nil
}

func appointmentSuccessMessage(appointment *storage.Appointment) string {
	return fmt.Sprintf("预约创建成功！\n📋 预约信息\n预约号：%s\n理发师：%s\n顾客：%s\n日期：%s\n时间：%s\n服务：%s",
		appointmentDisplayNumber(appointment.ID),
		appointment.BarberName,
		appointment.Customer,
		appointment.Date,
		appointment.Time,
		appointment.Service,
	)
}

// clipLeaveToDate 将请假区间裁到指定日期的当天范围内。
// 调用方用 isSameYMD 决定显示格式：同日显示时分，跨日才显示日期。
func clipLeaveToDate(startAt, endAt time.Time, date string, loc *time.Location) (time.Time, time.Time) {
	if loc == nil {
		loc = time.FixedZone("CST", 8*3600)
	}
	dayStart, err := time.ParseInLocation("2006-01-02", date, loc)
	if err != nil {
		// 日期解析失败时原样返回。
		return startAt, endAt
	}
	dayEnd := dayStart.Add(24 * time.Hour).Add(-time.Nanosecond)
	clippedStart := startAt
	if clippedStart.Before(dayStart) {
		clippedStart = dayStart
	}
	clippedEnd := endAt
	if clippedEnd.After(dayEnd) {
		clippedEnd = dayEnd
	}
	return clippedStart, clippedEnd
}

// isSameYMD 判断两个时间在指定时区是否为同一天。
// 预约错误消息中，同日只显示时分，跨日显示“次日”或日期。
func isSameYMD(a, b time.Time, loc *time.Location) bool {
	if loc == nil {
		loc = time.FixedZone("CST", 8*3600)
	}
	return a.In(loc).Format("2006-01-02") == b.In(loc).Format("2006-01-02")
}
