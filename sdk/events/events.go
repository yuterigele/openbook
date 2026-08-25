// Package events 定义可供 Outbox 消费者使用的版本化预约事件。
package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const SchemaVersion = "booking.event.v1"

// Type 是预约领域事件类型。
type Type string

const (
	BookingCreated     Type = "booking.created"
	BookingCancelled   Type = "booking.cancelled"
	BookingRescheduled Type = "booking.rescheduled"
)

// Envelope 是所有预约事件共享的外层结构。
type Envelope struct {
	SchemaVersion string          `json:"schema_version"`
	EventID       string          `json:"event_id"`
	Type          Type            `json:"type"`
	BookingID     string          `json:"booking_id"`
	MerchantID    string          `json:"merchant_id"`
	LocationID    string          `json:"location_id"`
	OccurredAt    time.Time       `json:"occurred_at"`
	Data          json.RawMessage `json:"data"`
}

// BookingCreatedData 是预约创建事件的数据部分。
type BookingCreatedData struct {
	CustomerID string `json:"customer_id"`
	ServiceID  string `json:"service_id"`
	StaffID    string `json:"staff_id"`
}

// BookingCancelledData 是预约取消事件的数据部分。
type BookingCancelledData struct {
	CustomerID string `json:"customer_id"`
}

// BookingRescheduledData 是预约改约事件的数据部分。
type BookingRescheduledData struct {
	OldBookingID string `json:"old_booking_id"`
	NewBookingID string `json:"new_booking_id"`
	CustomerID   string `json:"customer_id"`
}

// Subscriber 是 Outbox 发布器使用的最小事件消费接口。
// 消费者应按 EventID 做幂等处理，失败时返回错误以触发发布重试。
type Subscriber interface {
	Handle(context.Context, Envelope) error
}

// HandlerFunc 将函数适配为 Subscriber。
type HandlerFunc func(context.Context, Envelope) error

// Handle 实现 Subscriber。
func (handler HandlerFunc) Handle(ctx context.Context, event Envelope) error {
	if handler == nil {
		return errors.New("event handler is nil")
	}
	return handler(ctx, event)
}

// DecodeData 将事件数据解码到调用方声明的类型。
func (envelope Envelope) DecodeData(target any) error {
	if target == nil {
		return errors.New("event data target is required")
	}
	if err := json.Unmarshal(envelope.Data, target); err != nil {
		return fmt.Errorf("decode booking event data: %w", err)
	}
	return nil
}

// Marshal 创建并校验一个版本化事件。
func Marshal(eventID string, eventType Type, bookingID, merchantID, locationID string, data any, occurredAt time.Time) ([]byte, error) {
	if strings.TrimSpace(eventID) == "" || strings.TrimSpace(bookingID) == "" || strings.TrimSpace(merchantID) == "" || strings.TrimSpace(locationID) == "" || occurredAt.IsZero() {
		return nil, errors.New("event identity and occurred_at are required")
	}
	if eventType != BookingCreated && eventType != BookingCancelled && eventType != BookingRescheduled {
		return nil, fmt.Errorf("unsupported booking event type %q", eventType)
	}
	encoded, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("marshal event data: %w", err)
	}
	payload, err := json.Marshal(Envelope{
		SchemaVersion: SchemaVersion, EventID: eventID, Type: eventType,
		BookingID: bookingID, MerchantID: merchantID, LocationID: locationID,
		OccurredAt: occurredAt, Data: encoded,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal event envelope: %w", err)
	}
	return payload, nil
}

// Unmarshal 解析并校验一个版本化事件。
func Unmarshal(payload []byte) (Envelope, error) {
	var envelope Envelope
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return Envelope{}, err
	}
	if envelope.SchemaVersion != SchemaVersion || strings.TrimSpace(envelope.EventID) == "" || strings.TrimSpace(envelope.BookingID) == "" || strings.TrimSpace(envelope.MerchantID) == "" || strings.TrimSpace(envelope.LocationID) == "" || envelope.OccurredAt.IsZero() || len(envelope.Data) == 0 {
		return Envelope{}, errors.New("invalid booking event envelope")
	}
	if envelope.Type != BookingCreated && envelope.Type != BookingCancelled && envelope.Type != BookingRescheduled {
		return Envelope{}, fmt.Errorf("unsupported booking event type %q", envelope.Type)
	}
	return envelope, nil
}
