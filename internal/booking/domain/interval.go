// Package domain 包含与框架和存储无关的预约领域规则。
package domain

import (
	"errors"
	"time"
)

var (
	// ErrInvalidInterval 表示区间端点无效或不是正向区间。
	ErrInvalidInterval = errors.New("invalid time interval")
	// ErrInvalidBuffer 表示缓冲时间为负数。
	ErrInvalidBuffer = errors.New("invalid interval buffer")
)

// Interval 使用半开区间 [StartAt, EndAt)。
// 结束时刻等于另一个区间开始时刻时，两者相邻但不冲突。
type Interval struct {
	StartAt time.Time
	EndAt   time.Time
}

// NewInterval 创建一个经过校验的预约时间区间。
func NewInterval(startAt, endAt time.Time) (Interval, error) {
	if startAt.IsZero() || endAt.IsZero() || !endAt.After(startAt) {
		return Interval{}, ErrInvalidInterval
	}
	return Interval{StartAt: startAt, EndAt: endAt}, nil
}

// NewServiceInterval 根据服务开始时间、服务时长和前后缓冲创建占用区间。
func NewServiceInterval(startAt time.Time, duration, before, after time.Duration) (Interval, error) {
	if startAt.IsZero() || duration <= 0 || before < 0 || after < 0 {
		if before < 0 || after < 0 {
			return Interval{}, ErrInvalidBuffer
		}
		return Interval{}, ErrInvalidInterval
	}
	return NewInterval(startAt.Add(-before), startAt.Add(duration+after))
}

// Buffered 返回在当前区间前后增加缓冲后的占用区间。
func (i Interval) Buffered(before, after time.Duration) (Interval, error) {
	if err := i.Validate(); err != nil {
		return Interval{}, err
	}
	if before < 0 || after < 0 {
		return Interval{}, ErrInvalidBuffer
	}
	return NewInterval(i.StartAt.Add(-before), i.EndAt.Add(after))
}

// Validate 校验区间自身的时间顺序。
func (i Interval) Validate() error {
	if i.StartAt.IsZero() || i.EndAt.IsZero() || !i.EndAt.After(i.StartAt) {
		return ErrInvalidInterval
	}
	return nil
}

// Duration 返回区间长度。
func (i Interval) Duration() time.Duration {
	return i.EndAt.Sub(i.StartAt)
}

// Overlaps 判断两个半开区间是否发生实际占用重叠。
func (i Interval) Overlaps(other Interval) bool {
	return i.StartAt.Before(other.EndAt) && other.StartAt.Before(i.EndAt)
}

// HasConflict 判断候选区间是否与任一已占用区间冲突。
func HasConflict(candidate Interval, occupied []Interval) bool {
	for _, existing := range occupied {
		if candidate.Overlaps(existing) {
			return true
		}
	}
	return false
}
