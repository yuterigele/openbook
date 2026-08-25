// Package legacy 将现有美发预约工具接入 v1alpha1 应用端口。
//
// 这是迁移期适配器：旧工具仍负责既有业务校验、锁和事务，新 Runtime
// 只通过本文件访问它们。适配器不自动重试写操作。
package legacy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/yuterigele/openbook/lock"
	"github.com/yuterigele/openbook/sdk/booking/v1alpha1"
	"github.com/yuterigele/openbook/sdk/toolkit"
	"github.com/yuterigele/openbook/storage"
	"github.com/yuterigele/openbook/tools"
)

// CustomerIdentity 是从可信顾客上下文解析出的旧工具身份字段。
// 旧工具仍需要手机号和消息身份来完成顾客档案关联；这些字段不能由模型参数提供。
type CustomerIdentity struct {
	Name           string
	Phone          string
	OpenID         string
	ExternalUserID string
}

// CustomerResolver 根据可信 ExecutionContext 解析顾客资料。
type CustomerResolver func(context.Context, v1alpha1.ExecutionContext) (CustomerIdentity, error)

type queryRunner func(context.Context, string) (string, error)
type createRunner func(context.Context, string) (string, error)
type readRunner func(context.Context, string) (string, error)

// Application 是迁移期旧工具应用适配器。
type Application struct {
	resolveCustomer CustomerResolver
	querySchedule   queryRunner
	createBooking   createRunner
	cancelBooking   createRunner
	getAppointment  readRunner
	listServices    readRunner
	listStaff       readRunner
	listMyBookings  readRunner
}

// NewApplication 创建接入现有工具实现的适配器。
func NewApplication(resolveCustomer CustomerResolver) *Application {
	return &Application{
		resolveCustomer: resolveCustomer,
		querySchedule: func(ctx context.Context, arguments string) (string, error) {
			return (&tools.QueryScheduleTool{}).InvokableRun(ctx, arguments)
		},
		createBooking: func(ctx context.Context, arguments string) (string, error) {
			return (&tools.CreateAppointmentTool{}).InvokableRun(ctx, arguments)
		},
		cancelBooking: func(ctx context.Context, arguments string) (string, error) {
			return (&tools.CancelAppointmentTool{}).InvokableRun(ctx, arguments)
		},
		getAppointment: func(ctx context.Context, arguments string) (string, error) {
			return (&tools.GetAppointmentTool{}).InvokableRun(ctx, arguments)
		},
		listServices: func(ctx context.Context, arguments string) (string, error) {
			return (&tools.ListServicesTool{}).InvokableRun(ctx, arguments)
		},
		listStaff: func(ctx context.Context, arguments string) (string, error) {
			return (&tools.ListBarbersTool{}).InvokableRun(ctx, arguments)
		},
		listMyBookings: func(ctx context.Context, arguments string) (string, error) {
			return (&tools.ListMyBookingsTool{}).InvokableRun(ctx, arguments)
		},
	}
}

// NewApplicationForTest 使用注入的旧工具运行器，便于不依赖数据库验证适配器边界。
func NewApplicationForTest(resolveCustomer CustomerResolver, query queryRunner, create createRunner) *Application {
	return &Application{
		resolveCustomer: resolveCustomer,
		querySchedule:   query,
		createBooking:   create,
	}
}

// Execute 实现 v1alpha1.Application。
func (a *Application) Execute(ctx context.Context, trusted v1alpha1.ExecutionContext, call v1alpha1.Call) (v1alpha1.Response, error) {
	response := v1alpha1.Response{Operation: call.Operation}
	if err := trusted.ValidateFor(call.Operation); err != nil {
		return withErrorResult(response, err)
	}
	if err := validateParameters(call.Parameters); err != nil {
		return withErrorResult(response, err)
	}

	switch call.Operation {
	case v1alpha1.OperationQueryAvailability:
		if a == nil || a.querySchedule == nil {
			return withErrorResult(response, v1alpha1.NewError(v1alpha1.ErrorCodeUnavailable, "query adapter is not configured"))
		}
		toolContext := tools.WithShopID(ctx, trusted.LocationID)
		output, err := a.querySchedule(toolContext, string(call.Parameters))
		if err != nil {
			return withLegacyError(response, err)
		}
		code := "availability.found"
		if strings.Contains(output, "没有可预约") {
			code = "availability.none"
		}
		return withToolResult(response, toolkit.NewOK(code, output, map[string]any{"message": output}))

	case v1alpha1.OperationListServices:
		return a.executeRead(ctx, trusted, call, response, a.listServices, "services.listed")

	case v1alpha1.OperationListStaff:
		return a.executeRead(ctx, trusted, call, response, a.listStaff, "staff.listed")

	case v1alpha1.OperationListMyBookings:
		return a.executeCustomerRead(ctx, trusted, call, response, a.listMyBookings, "booking.listed")

	case v1alpha1.OperationCreateBooking:
		return a.executeCreate(ctx, trusted, call, response)

	case v1alpha1.OperationCancelBooking:
		return a.executeCancel(ctx, trusted, call, response)

	case v1alpha1.OperationRescheduleBooking:
		return a.executeReschedule(ctx, trusted, call, response)
	default:
		return withErrorResult(response, v1alpha1.NewError(v1alpha1.ErrorCodeInvalidOperation, "legacy adapter only supports availability and create booking"))
	}
}

func (a *Application) executeReschedule(ctx context.Context, trusted v1alpha1.ExecutionContext, call v1alpha1.Call, response v1alpha1.Response) (v1alpha1.Response, error) {
	if a == nil || a.getAppointment == nil || a.cancelBooking == nil || a.createBooking == nil {
		return withErrorResult(response, v1alpha1.NewError(v1alpha1.ErrorCodeUnavailable, "reschedule adapter is not configured"))
	}
	if a.resolveCustomer == nil {
		return withErrorResult(response, v1alpha1.NewError(v1alpha1.ErrorCodeInvalidContext, "customer resolver is required"))
	}
	identity, err := a.resolveCustomer(ctx, trusted)
	if err != nil {
		return withErrorResult(response, normalizeLegacyError(err))
	}
	if identity.Name == "" || identity.Phone == "" || (identity.OpenID == "" && identity.ExternalUserID == "") {
		return withErrorResult(response, v1alpha1.NewError(v1alpha1.ErrorCodeInvalidContext, "trusted customer identity is incomplete"))
	}

	var params struct {
		AppointmentID string `json:"appointment_id"`
		BarberName    string `json:"barber_name"`
		Date          string `json:"date"`
		Time          string `json:"time"`
		Service       string `json:"service"`
		PhoneCode     string `json:"phone_verification_code,omitempty"`
	}
	if err := json.Unmarshal(call.Parameters, &params); err != nil || params.AppointmentID == "" || params.BarberName == "" || params.Date == "" || params.Time == "" || params.Service == "" {
		return withErrorResult(response, v1alpha1.NewError(v1alpha1.ErrorCodeInvalidContext, "reschedule requires appointment_id, barber_name, date, time and service"))
	}

	toolContext := tools.WithShopID(ctx, trusted.LocationID)
	toolContext = tools.WithOpenID(toolContext, identity.OpenID)
	toolContext = tools.WithExternalUserID(toolContext, identity.ExternalUserID)
	appointmentArguments, _ := json.Marshal(map[string]string{"appointment_id": params.AppointmentID})
	if _, err := a.getAppointment(toolContext, string(appointmentArguments)); err != nil {
		return withLegacyError(response, err)
	}

	cancelArguments, _ := json.Marshal(map[string]string{
		"appointment_id": params.AppointmentID,
		"reason":         "顾客申请改约",
	})
	if _, err := a.cancelBooking(toolContext, string(cancelArguments)); err != nil {
		return withLegacyError(response, err)
	}
	if err := verifyLegacyCancellation(a.getAppointment, toolContext, params.AppointmentID); err != nil {
		return withErrorResult(response, err)
	}

	createArguments := map[string]string{
		"barber_name": params.BarberName,
		"customer":    identity.Name,
		"date":        params.Date,
		"time":        params.Time,
		"service":     params.Service,
		"phone":       identity.Phone,
	}
	if params.PhoneCode != "" {
		createArguments["phone_verification_code"] = params.PhoneCode
	}
	encodedCreateArguments, _ := json.Marshal(createArguments)
	output, err := a.createBooking(toolContext, string(encodedCreateArguments))
	if err != nil {
		return withLegacyError(response, err)
	}
	return withToolResult(response, toolkit.NewOK("booking.rescheduled", output, map[string]any{"message": output}))
}

func (a *Application) executeCancel(ctx context.Context, trusted v1alpha1.ExecutionContext, call v1alpha1.Call, response v1alpha1.Response) (v1alpha1.Response, error) {
	if a == nil || a.cancelBooking == nil {
		return withErrorResult(response, v1alpha1.NewError(v1alpha1.ErrorCodeUnavailable, "cancel adapter is not configured"))
	}
	if a.resolveCustomer == nil {
		return withErrorResult(response, v1alpha1.NewError(v1alpha1.ErrorCodeInvalidContext, "customer resolver is required"))
	}
	identity, err := a.resolveCustomer(ctx, trusted)
	if err != nil {
		return withErrorResult(response, normalizeLegacyError(err))
	}
	if identity.OpenID == "" && identity.ExternalUserID == "" {
		return withErrorResult(response, v1alpha1.NewError(v1alpha1.ErrorCodeInvalidContext, "trusted customer identity is incomplete"))
	}

	toolContext := tools.WithShopID(ctx, trusted.LocationID)
	toolContext = tools.WithOpenID(toolContext, identity.OpenID)
	toolContext = tools.WithExternalUserID(toolContext, identity.ExternalUserID)
	output, err := a.cancelBooking(toolContext, string(call.Parameters))
	if err != nil {
		return withLegacyError(response, err)
	}
	if err := verifyLegacyCancellation(a.getAppointment, toolContext, appointmentIDFromParameters(call.Parameters)); err != nil {
		return withErrorResult(response, err)
	}
	return withToolResult(response, toolkit.NewOK("booking.cancelled", output, map[string]any{"message": output}))
}

func verifyLegacyCancellation(getAppointment readRunner, ctx context.Context, appointmentID string) error {
	if getAppointment == nil || appointmentID == "" {
		return v1alpha1.NewError(v1alpha1.ErrorCodeUnknown, "cancellation outcome is not confirmed")
	}
	arguments, _ := json.Marshal(map[string]string{"appointment_id": appointmentID})
	output, err := getAppointment(ctx, string(arguments))
	if err != nil {
		return v1alpha1.WrapError(v1alpha1.ErrorCodeUnknown, "cancellation outcome is not confirmed", err)
	}
	if !strings.Contains(output, "状态：cancelled") && !strings.Contains(output, "状态:cancelled") {
		return v1alpha1.NewError(v1alpha1.ErrorCodeUnknown, "cancellation outcome is not confirmed")
	}
	return nil
}

func appointmentIDFromParameters(parameters json.RawMessage) string {
	var params struct {
		AppointmentID string `json:"appointment_id"`
	}
	_ = json.Unmarshal(parameters, &params)
	return params.AppointmentID
}

func (a *Application) executeRead(ctx context.Context, trusted v1alpha1.ExecutionContext, call v1alpha1.Call, response v1alpha1.Response, runner readRunner, code string) (v1alpha1.Response, error) {
	if runner == nil {
		return withErrorResult(response, v1alpha1.NewError(v1alpha1.ErrorCodeUnavailable, "legacy read adapter is not configured"))
	}
	toolContext := tools.WithShopID(ctx, trusted.LocationID)
	output, err := runner(toolContext, string(call.Parameters))
	if err != nil {
		return withLegacyError(response, err)
	}
	return withToolResult(response, toolkit.NewOK(code, output, map[string]any{"message": output}))
}

func (a *Application) executeCustomerRead(ctx context.Context, trusted v1alpha1.ExecutionContext, call v1alpha1.Call, response v1alpha1.Response, runner readRunner, code string) (v1alpha1.Response, error) {
	if runner == nil {
		return withErrorResult(response, v1alpha1.NewError(v1alpha1.ErrorCodeUnavailable, "customer read adapter is not configured"))
	}
	if a.resolveCustomer == nil {
		return withErrorResult(response, v1alpha1.NewError(v1alpha1.ErrorCodeInvalidContext, "customer resolver is required"))
	}
	identity, err := a.resolveCustomer(ctx, trusted)
	if err != nil {
		return withErrorResult(response, normalizeLegacyError(err))
	}
	if identity.OpenID == "" && identity.ExternalUserID == "" {
		return withErrorResult(response, v1alpha1.NewError(v1alpha1.ErrorCodeInvalidContext, "trusted customer identity is incomplete"))
	}
	toolContext := tools.WithShopID(ctx, trusted.LocationID)
	toolContext = tools.WithOpenID(toolContext, identity.OpenID)
	toolContext = tools.WithExternalUserID(toolContext, identity.ExternalUserID)
	output, err := runner(toolContext, string(call.Parameters))
	if err != nil {
		return withLegacyError(response, err)
	}
	return withToolResult(response, toolkit.NewOK(code, output, map[string]any{"message": output}))
}

func (a *Application) executeCreate(ctx context.Context, trusted v1alpha1.ExecutionContext, call v1alpha1.Call, response v1alpha1.Response) (v1alpha1.Response, error) {
	if a == nil || a.createBooking == nil {
		return withErrorResult(response, v1alpha1.NewError(v1alpha1.ErrorCodeUnavailable, "create adapter is not configured"))
	}
	if a.resolveCustomer == nil {
		return withErrorResult(response, v1alpha1.NewError(v1alpha1.ErrorCodeInvalidContext, "customer resolver is required"))
	}
	identity, err := a.resolveCustomer(ctx, trusted)
	if err != nil {
		return withErrorResult(response, normalizeLegacyError(err))
	}
	if identity.Name == "" || identity.Phone == "" {
		return withErrorResult(response, v1alpha1.NewError(v1alpha1.ErrorCodeInvalidContext, "trusted customer identity is incomplete"))
	}

	var params map[string]json.RawMessage
	if err := json.Unmarshal(call.Parameters, &params); err != nil {
		return withErrorResult(response, v1alpha1.NewError(v1alpha1.ErrorCodeInvalidContext, "booking parameters must be an object"))
	}
	// 顾客姓名和手机号由可信顾客资料覆盖；模型只能提供服务、师傅和时间等业务参数。
	params["customer"], _ = json.Marshal(identity.Name)
	params["phone"], _ = json.Marshal(identity.Phone)
	arguments, err := json.Marshal(params)
	if err != nil {
		return withErrorResult(response, v1alpha1.NewError(v1alpha1.ErrorCodeInvalidContext, "booking parameters are invalid"))
	}

	toolContext := tools.WithShopID(ctx, trusted.LocationID)
	toolContext = tools.WithOpenID(toolContext, identity.OpenID)
	toolContext = tools.WithExternalUserID(toolContext, identity.ExternalUserID)
	output, err := a.createBooking(toolContext, string(arguments))
	if err != nil {
		return withLegacyError(response, err)
	}
	return withToolResult(response, toolkit.NewOK("booking.created", output, map[string]any{"message": output}))
}

func validateParameters(parameters json.RawMessage) error {
	if len(parameters) == 0 || string(parameters) == "null" {
		return v1alpha1.NewError(v1alpha1.ErrorCodeInvalidContext, "business parameters are required")
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(parameters, &object); err != nil || object == nil {
		return v1alpha1.NewError(v1alpha1.ErrorCodeInvalidContext, "business parameters must be an object")
	}
	for _, forbidden := range []string{"merchant_id", "location_id", "customer_id", "principal_id", "permissions", "trace_id", "idempotency_key"} {
		if _, exists := object[forbidden]; exists {
			return v1alpha1.NewError(v1alpha1.ErrorCodeInvalidContext, fmt.Sprintf("trusted field %q is not accepted in business parameters", forbidden))
		}
	}
	return nil
}

func withToolResult(response v1alpha1.Response, result toolkit.Result) (v1alpha1.Response, error) {
	if err := result.Validate(toolkit.DefaultBudget); err != nil {
		return withErrorResult(response, v1alpha1.WrapError(v1alpha1.ErrorCodeUnavailable, "tool result contract validation failed", err))
	}
	payload, err := json.Marshal(result)
	if err != nil {
		return withErrorResult(response, v1alpha1.WrapError(v1alpha1.ErrorCodeUnavailable, "tool result serialization failed", err))
	}
	response.Data = payload
	return response, nil
}

func withLegacyError(response v1alpha1.Response, err error) (v1alpha1.Response, error) {
	return withErrorResult(response, normalizeLegacyError(err))
}

func withErrorResult(response v1alpha1.Response, err error) (v1alpha1.Response, error) {
	result := toolkit.FromError(err)
	payload, marshalErr := json.Marshal(result)
	if marshalErr == nil {
		response.Data = payload
	}
	return response, err
}

func normalizeLegacyError(err error) error {
	if err == nil {
		return nil
	}
	if v1alpha1.CodeOf(err) != v1alpha1.ErrorCodeUnknown {
		return err
	}
	switch {
	case errors.Is(err, storage.ErrSlotTaken), strings.Contains(err.Error(), "刚被别人抢"), strings.Contains(err.Error(), "刚被别的顾客抢"):
		return v1alpha1.WrapError(v1alpha1.ErrorCodeConflict, "booking slot is occupied", err)
	case lock.IsReadOnly(), strings.Contains(err.Error(), "只读"), strings.Contains(err.Error(), "升级"):
		return v1alpha1.WrapError(v1alpha1.ErrorCodeMaintenance, "booking service is in maintenance", err)
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return v1alpha1.WrapError(v1alpha1.ErrorCodeUnknown, "booking outcome is not confirmed", err)
	default:
		return v1alpha1.WrapError(v1alpha1.ErrorCodeUnavailable, "legacy booking tool failed", err)
	}
}

var _ v1alpha1.Application = (*Application)(nil)
