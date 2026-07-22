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

// ctxKeyShopID 注入 ctx 的 shop_id key
type ctxKeyShopID struct{}

// ctxKeyOpenID 注入 ctx 的微信 openID（v4.8 给 CreateAppointmentFull 透传顾客档案用）
type ctxKeyOpenID struct{}

// ctxKeyExternalUserID 注入 ctx 的 external_user_id
//
// v4.9.3 加这个 key 的原因：
//   - reminder / leave notify cron 都靠 customers.external_user_id 反查 wecom ID 发送消息
//   - 之前只透传 openID，external_user_id 字段永远是空 → cron 全失败
//   - 加这个 key 后，server.go 在处理 wecom 消息时同时注入 openID + externalUserID
//   - 工具调 storage.CreateAppointmentFull 时透传，顾客档案完整
type ctxKeyExternalUserID struct{}

// WithShopID 把 shop_id 放进 ctx（Agent 工具从 ctx 拿）
func WithShopID(ctx context.Context, shopID string) context.Context {
	return context.WithValue(ctx, ctxKeyShopID{}, shopID)
}

// ShopIDFromCtx 从 ctx 取 shop_id（取不到返回 ""）
func ShopIDFromCtx(ctx context.Context) string {
	v, _ := ctx.Value(ctxKeyShopID{}).(string)
	return v
}

// WithOpenID 把微信 openID 放进 ctx（v4.8 +）
func WithOpenID(ctx context.Context, openID string) context.Context {
	return context.WithValue(ctx, ctxKeyOpenID{}, openID)
}

// OpenIDFromCtx 从 ctx 取微信 openID（取不到返回 ""）
func OpenIDFromCtx(ctx context.Context) string {
	v, _ := ctx.Value(ctxKeyOpenID{}).(string)
	return v
}

// WithExternalUserID 把 external_user_id 放进 ctx（v4.9.3 修 reminder cron 缺字段）
func WithExternalUserID(ctx context.Context, externalUserID string) context.Context {
	return context.WithValue(ctx, ctxKeyExternalUserID{}, externalUserID)
}

// ExternalUserIDFromCtx 从 ctx 取 external_user_id（取不到返回 ""）
func ExternalUserIDFromCtx(ctx context.Context) string {
	v, _ := ctx.Value(ctxKeyExternalUserID{}).(string)
	return v
}

// ValidatePhone 严格校验中国大陆手机号（v4.9.3）
//
// 业务背景：手机号是顾客档案最稳的查重键
//   - 微信 openID 会随换设备/重装 app 变 → 不稳
//   - external_user_id 主要用于客服消息 → 不通用
//   - 姓名可能重 → 不可靠
//   - 手机号：11 位数字，1 开头，几乎终身不变 → 唯一稳的标识
//   - 所以预约链路必填手机号，写进 customers.phone，后续所有 cron（reminder /
//     leave notify / 营销推送）都靠 phone 反查 wecom ID
//
// 规则：11 位数字、1 开头
//   - 不接国际号码（+86 前缀）：工具不支持境外顾客，简化
//   - 不接座机 / 400 / 800：业务场景全是个人手机
//   - 拒绝空字符串：必填项，没收到让 LLM 回去问顾客
//
// 返回的 error 是 friendly 话术，LLM 看到后会直接转给顾客。
//
// 复用：cmd/fix-customers 也 import 这个函数，保证手动补 phone 和工具流程校验规则一致。
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
		Desc: "为顾客创建一个新的预约。需要提供理发师姓名、顾客姓名、手机号、日期、时间，可选择服务项目。\n" +
			"\n" +
			"【调用时机】\n" +
			"  - 顾客明确说「帮我约一下」「我要预约」、说出具体理发师/时间时；\n" +
			"  - 调用前**先调 query_schedule 确认该时段真空闲**（避免顾客告诉你一个时间，你不去查就盲下）；\n" +
			"  - 调用前**确认 date 参数**：顾客说「明天」就转成 today+1 的 YYYY-MM-DD；说「3 号」就转成 YYYY-MM-03。\n" +
			"  - 调用前**必填手机号**：顾客没主动给手机号时**主动问一次**（如「方便留个手机号吗，到店前我们提醒你~」），不要凭空编。\n" +
			"\n" +
			"【业务规则】\n" +
			"  - 同一理发师的同一时段只能有一个预约；并发请求会被 Redis 锁挡掉；\n" +
			"  - 如果理发师在所选时段请假（P4），会返回错误，需要换理发师或换时间；\n" +
			"  - 不接过去时间（已过时刻）、22:00 之后、节假日；\n" +
			"  - 手机号必须 11 位数字、1 开头（如 13812345678）。工具会严格校验。\n" +
			"\n" +
			"【回复要求】\n" +
			"  - 成功后用自然语气确认：「好的，已帮你约好 Tony 师傅 6 月 22 日 15:00，到店报名字就行~」；\n" +
			"  - 失败时**不要**把工具错误原文（如「ErrSlotTaken」）告诉顾客，翻译成场景化话术：\n" +
			"    * 时段被占 → 「这个时段刚被别的顾客抢了，我帮你看下一个空档」\n" +
			"    * 理发师请假 → 「Tony 师傅 X 点到 X 点请假了，要不要换 Kevin 师傅或换个时间？」\n" +
			"    * 节假日（v4.16.2 改）→ 先调 list_shop_holidays 拿本店完整节假日清单，再调 query_schedule 验证推荐日期可约，\n" +
			"      最后回「X 月 X 日是节假日休息日，本店 X 月 X 日 / X 月 X 日也能约，您看哪天方便？」\n" +
			"      **禁止**凭印象推「前后两天」（v4.16.1 事故：店设 7-1、7-2 休息，Agent 推 7-1 给顾客，实际 7-1 也是假期）。",
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

	// 查 barber 拿 ID（用于 Redis 锁 key）
	barber, err := storage.GetBarberByName(params.BarberName)
	if err != nil {
		return "", fmt.Errorf("师傅 %s 不在店里呢（本店现在有 Tony、Kevin 两位），换个试试？", params.BarberName)
	}

	// 节假日拦截（PRD #6）
	shop, _ := storage.GetShopByID(ctx, barber.ShopID)
	if storage.IsShopHoliday(shop, params.Date) {
		return "", fmt.Errorf("%s 是本店休息日，麻烦换个日期试试", params.Date)
	}

	// P4 理发师请假拦截（PRD §11.7.4）：
	//   - 在加锁之前检查，避免"预约成功→立即被请假处理流程取消"的体验事故
	//   - 时区按 Asia/Shanghai，与 IsValidSlot / FindAppointmentsInRange 保持一致
	onLeave, leave, err := storage.IsBarberOnLeaveAt(ctx, barber.ID, appointmentAt)
	if err != nil {
		// DB 抖动不阻塞下单，但记一笔 log 便于排查（标准 log 包）
		fmt.Printf("[create_appointment] IsBarberOnLeaveAt query failed: %v\n", err)
	}
	if onLeave && leave != nil {
		// v4.13.0 隐私保护：永远显示"临时有事"，不暴露 leave.Reason 内部原因
		//   之前会把"痔疮手术""陪老婆产检"等敏感字眼直接拼到错误消息里
		//   LLM 拿到后会复述给顾客 → 改 hardcode "临时有事"
		//
		// v4.13.6 区间裁剪：把 leave 区间裁到 params.Date 当天 [00:00, 23:59:59]，
		//   避免跨日 leave 拼出"06-27 11:15"这种尾巴，LLM 拿到会口语化成"明天上午"
		//   让只关心今天的顾客一脸懵。同日裁剪后只显示 HH:MM（不带日期）。
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
		params.Phone,               // v4.9.3: 手机号（已 ValidatePhone 校验）
		OpenIDFromCtx(ctx),         // v4.8: 透传微信 openID，让 storage 自动建顾客档案
		ExternalUserIDFromCtx(ctx), // v4.9.3: 透传 external_user_id，reminder cron 需要
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

	return fmt.Sprintf("预约创建成功！\n预约ID：%s\n理发师：%s\n顾客：%s\n日期：%s\n时间：%s\n服务：%s",
		persisted.ID,
		persisted.BarberName,
		persisted.Customer,
		persisted.Date,
		persisted.Time,
		persisted.Service,
	), nil
}

// clipLeaveToDate 把 leave 区间裁到 date 当天 [00:00:00, 23:59:59.999999999]
//
//   - v4.13.6：顾客问"今天 14:00" → 错误消息只该显示今天的区间；
//     否则 leave 跨日（如 10:15 今天 至 11:15 明天）会被 LLM 口语化成"明天上午"，
//     顾客只关心今天，看到会一脸懵。
//   - 调用方先用 isSameYMD 决定显示格式：同日显示 HH:MM，跨日才带 MM-DD。
func clipLeaveToDate(startAt, endAt time.Time, date string, loc *time.Location) (time.Time, time.Time) {
	if loc == nil {
		loc = time.FixedZone("CST", 8*3600)
	}
	dayStart, err := time.ParseInLocation("2006-01-02", date, loc)
	if err != nil {
		// date 解析失败兜底：原样返回，行为跟 v4.13.5 之前一致（不裁剪）
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

// isSameYMD 判断两个时间是否在 loc 时区的同一天
//
//   - 给 create_appointment 错误消息用：同日 → "10:15-18:00"，跨日 → "10:15 至次日 11:15"
//   - 用 In(loc) 后取 Y-M-D 比较，避开 time.Truncate 跨夏令时的坑
func isSameYMD(a, b time.Time, loc *time.Location) bool {
	if loc == nil {
		loc = time.FixedZone("CST", 8*3600)
	}
	return a.In(loc).Format("2006-01-02") == b.In(loc).Format("2006-01-02")
}
