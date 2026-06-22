package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"

	"github.com/cloudwego/eino-examples/quickstart/chatwitheino/lock"
	"github.com/cloudwego/eino-examples/quickstart/chatwitheino/storage"
)

// ctxKeyShopID 注入 ctx 的 shop_id key
type ctxKeyShopID struct{}

// WithShopID 把 shop_id 放进 ctx（Agent 工具从 ctx 拿）
func WithShopID(ctx context.Context, shopID string) context.Context {
	return context.WithValue(ctx, ctxKeyShopID{}, shopID)
}

// ShopIDFromCtx 从 ctx 取 shop_id（取不到返回 ""）
func ShopIDFromCtx(ctx context.Context) string {
	v, _ := ctx.Value(ctxKeyShopID{}).(string)
	return v
}

// CreateAppointmentTool 创建预约工具
type CreateAppointmentTool struct{}

// Info 返回工具信息
func (t *CreateAppointmentTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: "create_appointment",
		Desc: "为顾客创建一个新的预约。需要提供理发师姓名、顾客姓名、日期、时间，可选择服务项目。\n" +
			"\n" +
			"【调用时机】\n" +
			"  - 顾客明确说「帮我约一下」「我要预约」、说出具体理发师/时间时；\n" +
			"  - 调用前**先调 query_schedule 确认该时段真空闲**（避免顾客告诉你一个时间，你不去查就盲下）；\n" +
			"  - 调用前**确认 date 参数**：顾客说「明天」就转成 today+1 的 YYYY-MM-DD；说「3 号」就转成 YYYY-MM-03。\n" +
			"\n" +
			"【业务规则】\n" +
			"  - 同一理发师的同一时段只能有一个预约；并发请求会被 Redis 锁挡掉；\n" +
			"  - 如果理发师在所选时段请假（P4），会返回错误，需要换理发师或换时间；\n" +
			"  - 不接过去时间（已过时刻）、22:00 之后、节假日。\n" +
			"\n" +
			"【回复要求】\n" +
			"  - 成功后用自然语气确认：「好的，已帮你约好 Tony 师傅 6 月 22 日 15:00，到店报名字就行~」；\n" +
			"  - 失败时**不要**把工具错误原文（如「ErrSlotTaken」）告诉顾客，翻译成场景化话术：\n" +
			"    * 时段被占 → 「这个时段刚被别的顾客抢了，我帮你看下一个空档」\n" +
			"    * 理发师请假 → 「Tony 师傅 X 点到 X 点请假了，要不要换 Kevin 师傅或换个时间？」\n" +
			"    * 节假日 → 「X 月 X 日是节假日休息日，可以约前后两天吗？」",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"barber_name": {
				Type: "string", Desc: "理发师姓名，例如：Tony、Kevin", Required: true,
			},
			"customer": {
				Type: "string", Desc: "顾客姓名", Required: true,
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
	}
	if err := json.Unmarshal([]byte(argumentsInJSON), &params); err != nil {
		return "", FriendlyError(ctx, err, "参数解析失败", "create_appointment.unmarshal")
	}
	if params.BarberName == "" || params.Customer == "" || params.Date == "" || params.Time == "" {
		return "", fmt.Errorf("barber_name / customer / date / time 均不能为空")
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
		return "", fmt.Errorf("时段 %s 已经过去了，麻烦换个未来的时间", params.Date+" "+params.Time)
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
		reason := strings.TrimSpace(leave.Reason)
		if reason == "" {
			reason = "临时有事"
		}
		return "", fmt.Errorf(
			"%s 师傅在 %s 至 %s 请假了（%s），要不要换 Kevin 师傅或换个时间？",
			params.BarberName,
			leave.StartAt.In(loc).Format("01-02 15:04"),
			leave.EndAt.In(loc).Format("01-02 15:04"),
			reason,
		)
	}

	// 加 Redis 分布式锁（PRD §3.3 防并发预约冲突）
	lockCtx, cancel := context.WithTimeout(ctx, 5*1e9) // 5s
	defer cancel()
	l, err := lock.AcquireAppointmentLock(lockCtx, barber.ID, params.Date, params.Time)
	if err != nil {
		return "", fmt.Errorf("时段 %s %s 刚被别人抢了，我帮你看下一个空档", params.Date, params.Time)
	}
	if l != nil {
		defer func() { _ = l.Unlock(context.Background()) }()
	}

	appointment, err := storage.CreateAppointmentWithShop(
		ShopIDFromCtx(ctx),
		params.BarberName,
		params.Customer,
		params.Date,
		params.Time,
		params.Service,
	)
	if err != nil {
		if errors.Is(err, storage.ErrSlotTaken) {
			return "", fmt.Errorf("时段 %s %s 刚被别的顾客抢了，要不要换个时间？", params.Date, params.Time)
		}
		if errors.Is(err, storage.ErrBarberNotFound) {
			return "", fmt.Errorf("师傅 %s 不在店里呢，换个试试？", params.BarberName)
		}
		return "", fmt.Errorf("系统忙不过来了，请稍后再试")
	}

	// 埋点（PRD §11.2 续费漏斗）
	storage.TrackEvent(ctx, appointment.ShopID, storage.EventAppointmentCreated, appointment.ID, map[string]any{
		"barber_name": appointment.BarberName,
		"customer":    appointment.Customer,
		"date":        appointment.Date,
		"time":        appointment.Time,
	})
	// 该店铺首次预约 → 触发 D+N 漏斗起点
	if has, _ := storage.HasShopEvent(ctx, appointment.ShopID, storage.EventFirstAppointment); !has {
		storage.TrackEvent(ctx, appointment.ShopID, storage.EventFirstAppointment, appointment.ID, nil)
	}

	return fmt.Sprintf("预约创建成功！\n预约ID：%s\n理发师：%s\n顾客：%s\n日期：%s\n时间：%s\n服务：%s",
		appointment.ID,
		appointment.BarberName,
		appointment.Customer,
		appointment.Date,
		appointment.Time,
		appointment.Service,
	), nil
}