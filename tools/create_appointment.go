package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
		Desc: "为顾客创建一个新的预约。需要提供理发师姓名、顾客姓名、日期、时间，可选择服务项目。" +
			"同一理发师的同一时段只能有一个预约；并发请求会被 Redis 锁挡掉。" +
			"如果理发师在所选时段请假（P4），会返回错误，需要换理发师或换时间。",
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
				Type: "string", Desc: "预约时间，格式：HH:MM，例如：15:00", Required: true,
			},
			"service": {
				Type: "string", Desc: "服务项目，默认为'剪发'", Required: false,
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
		return "", fmt.Errorf("解析参数失败: %v", err)
	}
	if params.BarberName == "" || params.Customer == "" || params.Date == "" || params.Time == "" {
		return "", fmt.Errorf("barber_name / customer / date / time 均不能为空")
	}
	if !storage.IsValidSlot(params.Time) {
		return "", fmt.Errorf("时间 %s 不在工作时段内（09:00-18:00，午休 12:00-13:30 不可预约）", params.Time)
	}

	// 查 barber 拿 ID（用于 Redis 锁 key）
	barber, err := storage.GetBarberByName(params.BarberName)
	if err != nil {
		return "", fmt.Errorf("理发师 %s 不存在", params.BarberName)
	}

	// 节假日拦截（PRD #6）
	shop, _ := storage.GetShopByID(ctx, barber.ShopID)
	if storage.IsShopHoliday(shop, params.Date) {
		return "", fmt.Errorf("%s 是店铺休息日，无法预约", params.Date)
	}

	// P4 理发师请假拦截（PRD §11.7.4）：
	//   - 在加锁之前检查，避免"预约成功→立即被请假处理流程取消"的体验事故
	//   - 时区按 Asia/Shanghai，与 IsValidSlot / FindAppointmentsInRange 保持一致
	loc, _ := time.LoadLocation("Asia/Shanghai")
	if loc == nil {
		loc = time.FixedZone("CST", 8*3600)
	}
	appointmentAt, parseErr := time.ParseInLocation("2006-01-02 15:04", params.Date+" "+params.Time, loc)
	if parseErr != nil {
		// 时段格式异常已被 IsValidSlot 过滤过；这里再兜一道
		return "", fmt.Errorf("时间格式无法解析: %s %s", params.Date, params.Time)
	}
	onLeave, leave, err := storage.IsBarberOnLeaveAt(ctx, barber.ID, appointmentAt)
	if err != nil {
		// DB 抖动不阻塞下单，但记一笔 log 便于排查（标准 log 包）
		fmt.Printf("[create_appointment] IsBarberOnLeaveAt query failed: %v\n", err)
	}
	if onLeave && leave != nil {
		return "", fmt.Errorf(
			"理发师 %s 在 %s 至 %s 期间请假（原因：%s），该时段无法预约。请选择其他理发师或换个时间。",
			params.BarberName,
			leave.StartAt.In(loc).Format("01-02 15:04"),
			leave.EndAt.In(loc).Format("01-02 15:04"),
			leave.Reason,
		)
	}

	// 加 Redis 分布式锁（PRD §3.3 防并发预约冲突）
	lockCtx, cancel := context.WithTimeout(ctx, 5*1e9) // 5s
	defer cancel()
	l, err := lock.AcquireAppointmentLock(lockCtx, barber.ID, params.Date, params.Time)
	if err != nil {
		return "", fmt.Errorf("时段 %s %s 正在被其他人预约，请稍后再试或换个时间", params.Date, params.Time)
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
			return "", fmt.Errorf("时段 %s %s 已被预约", params.Date, params.Time)
		}
		if errors.Is(err, storage.ErrBarberNotFound) {
			return "", fmt.Errorf("理发师 %s 不存在", params.BarberName)
		}
		return "", fmt.Errorf("创建预约失败: %v", err)
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