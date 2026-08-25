// Package v1alpha1 定义 Agent Runtime 与预约应用之间的早期契约。
//
// v1alpha1 只冻结操作名称、可信执行上下文和错误语义。请求参数与业务
// DTO 仍属于不稳定范围，不能把领域实体直接作为公共接口暴露。
package v1alpha1

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// Operation 是 Runtime 可以请求预约应用执行的操作名称。
type Operation string

const (
	OperationListServices      Operation = "list_services"
	OperationListStaff         Operation = "list_staff"
	OperationQueryAvailability Operation = "query_availability"
	OperationQueryStaffLeave   Operation = "query_staff_leave"
	OperationListShopHolidays  Operation = "list_shop_holidays"
	OperationSensitiveCheck    Operation = "sensitive_check"
	OperationClassifyIntent    Operation = "classify_intent"
	OperationCreateBooking     Operation = "create_booking"
	OperationListMyBookings    Operation = "list_my_bookings"
	OperationCancelBooking     Operation = "cancel_booking"
	OperationRescheduleBooking Operation = "reschedule_booking"
	OperationHandoffToHuman    Operation = "handoff_to_human"
)

// AllOperations 返回当前契约支持的操作名称，调用方可以用它构建白名单。
func AllOperations() []Operation {
	return []Operation{
		OperationListServices,
		OperationListStaff,
		OperationQueryAvailability,
		OperationQueryStaffLeave,
		OperationListShopHolidays,
		OperationSensitiveCheck,
		OperationClassifyIntent,
		OperationCreateBooking,
		OperationListMyBookings,
		OperationCancelBooking,
		OperationRescheduleBooking,
		OperationHandoffToHuman,
	}
}

// IsValid 判断操作名称是否属于显式注册的预约应用能力。
func (o Operation) IsValid() bool {
	for _, known := range AllOperations() {
		if o == known {
			return true
		}
	}
	return false
}

// Permission 是应用层可以识别的最小权限名称。
type Permission string

const (
	PermissionBookingRead  Permission = "booking:read"
	PermissionBookingWrite Permission = "booking:write"
)

// ExecutionContext 是由 Host 从已验证的渠道身份构造的可信上下文。
//
// 模型参数、HTTP 请求体和顾客消息中的同名字段不能覆盖这些值。结构体
// 只作为应用层接口的输入，不应直接反序列化不可信 JSON 得到。
type ExecutionContext struct {
	MerchantID     string       `json:"merchant_id"`
	LocationID     string       `json:"location_id"`
	CustomerID     string       `json:"customer_id,omitempty"`
	PrincipalID    string       `json:"principal_id,omitempty"`
	Permissions    []Permission `json:"permissions,omitempty"`
	TraceID        string       `json:"trace_id"`
	IdempotencyKey string       `json:"idempotency_key,omitempty"`
}

// HasPermission 判断可信上下文是否包含指定权限。
func (c ExecutionContext) HasPermission(permission Permission) bool {
	for _, item := range c.Permissions {
		if item == permission {
			return true
		}
	}
	return false
}

// Validate 校验所有操作都需要的可信身份字段。
func (c ExecutionContext) Validate() error {
	switch {
	case c.MerchantID == "":
		return NewError(ErrorCodeInvalidContext, "merchant_id is required")
	case c.LocationID == "":
		return NewError(ErrorCodeInvalidContext, "location_id is required")
	case c.TraceID == "":
		return NewError(ErrorCodeInvalidContext, "trace_id is required")
	default:
		return nil
	}
}

// ValidateFor 校验指定操作额外需要的顾客身份和幂等键。
func (c ExecutionContext) ValidateFor(operation Operation) error {
	if !operation.IsValid() {
		return NewError(ErrorCodeInvalidOperation, fmt.Sprintf("unsupported operation %q", operation))
	}
	if err := c.Validate(); err != nil {
		return err
	}
	if requiresCustomer(operation) && c.CustomerID == "" {
		return NewError(ErrorCodeInvalidContext, "customer_id is required for customer operations")
	}
	if requiresIdempotencyKey(operation) && c.IdempotencyKey == "" {
		return NewError(ErrorCodeInvalidContext, "idempotency_key is required for write operations")
	}
	return nil
}

func requiresCustomer(operation Operation) bool {
	switch operation {
	case OperationCreateBooking, OperationListMyBookings, OperationCancelBooking, OperationRescheduleBooking:
		return true
	default:
		return false
	}
}

func requiresIdempotencyKey(operation Operation) bool {
	switch operation {
	case OperationCreateBooking, OperationCancelBooking, OperationRescheduleBooking:
		return true
	default:
		return false
	}
}

// Call 是 Runtime 发送给预约应用的调用外层结构。
// Parameters 只承载业务参数，不承载 merchant_id、location_id、customer_id
// 或权限等可信身份字段。
type Call struct {
	Operation  Operation       `json:"operation"`
	Parameters json.RawMessage `json:"parameters,omitempty"`
}

// Response 是预约应用返回给 Runtime 的调用外层结构。
// Data 的具体 DTO 在 v1alpha1 阶段仍可演进，调用方必须依据 Operation 解析。
type Response struct {
	Operation Operation       `json:"operation"`
	Data      json.RawMessage `json:"data,omitempty"`
}

// Application 是 Runtime 与预约应用之间的最小端口。
type Application interface {
	Execute(context.Context, ExecutionContext, Call) (Response, error)
}

// ErrorCode 是应用层对外稳定的错误语义。
type ErrorCode string

const (
	ErrorCodeInvalidContext   ErrorCode = "booking.invalid_context"
	ErrorCodeInvalidOperation ErrorCode = "booking.invalid_operation"
	ErrorCodeForbidden        ErrorCode = "booking.forbidden"
	ErrorCodeConflict         ErrorCode = "booking.conflict"
	ErrorCodeUnavailable      ErrorCode = "booking.unavailable"
	ErrorCodeMaintenance      ErrorCode = "booking.maintenance"
	ErrorCodeUnknown          ErrorCode = "booking.unknown"
)

// Error 是不暴露底层存储和第三方错误细节的稳定应用错误。
type Error struct {
	code    ErrorCode
	message string
	cause   error
}

// NewError 创建一个带稳定错误码的应用错误。
func NewError(code ErrorCode, message string) error {
	return &Error{code: code, message: message}
}

// WrapError 为日志保留底层错误，但 Error() 只返回稳定错误码和安全消息。
func WrapError(code ErrorCode, message string, cause error) error {
	return &Error{code: code, message: message, cause: cause}
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	if e.message == "" {
		return string(e.code)
	}
	return fmt.Sprintf("%s: %s", e.code, e.message)
}

func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

// CodeOf 从错误中提取稳定错误码，未知错误统一归类为 booking.unknown。
func CodeOf(err error) ErrorCode {
	var coded *Error
	if errors.As(err, &coded) && coded != nil && coded.code != "" {
		return coded.code
	}
	return ErrorCodeUnknown
}
