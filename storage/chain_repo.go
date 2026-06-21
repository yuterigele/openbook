package storage

import (
	"context"
	"errors"
	"time"
)

// ---- Shop listing (chain dashboard 用) ----

// ListAllShops 返回所有 shop（按 id 排序）
//
//   - 当前实现：单库多店，跨店数据汇总时直接 ListAll
//   - 后续如果按 shop_id 分库，要改成 union 各分库的 ListByID
//   - DB 未初始化返回空切片（不报错，方便上层零成本处理）
func ListAllShops(ctx context.Context) []Shop {
	if DB == nil {
		return []Shop{}
	}
	var out []Shop
	if err := DB.WithContext(ctx).Order("id asc").Find(&out).Error; err != nil {
		return []Shop{}
	}
	return out
}

// ShopAggregateStats 单店在 [from, to) 时间窗的预约汇总
//
//   - 设计：与 buildDashboard 的 summarizeRange 保持同口径（按 date+time 解析后落在窗内）
//   - 业务场景：chain 看板展示单店指标
//   - 性能：每个店一次 SQL 查 appointments；后续如果店多可以批量 + Go 端按 shop_id 分组
type ShopAggregateStats struct {
	Total        int     `json:"total"`
	Completed    int     `json:"completed"`
	NoShow       int     `json:"noshow"`
	Cancelled    int     `json:"cancelled"`
	Active       int     `json:"active"`
	NoShowRate   float64 `json:"no_show_rate"`
	CompleteRate float64 `json:"complete_rate"`
}

// ShopAggregate 单店 + 其在 [from, to) 时间窗的 stats
type ShopAggregate struct {
	Shop   Shop              `json:"shop"`
	Stats  ShopAggregateStats `json:"stats"`
}

// ShopAggregateByID 查某店在 [from, to) 内的预约汇总
//
//   - 复用 dashboard.summarizeRange 的口径（date+time 解析后按时间戳精确过滤）
//   - 跨天预约按"实际发生时间"判定，22:00 算今天（Asia/Shanghai 0 点为日界）
//   - SQL 端按 date 范围粗筛（±1d buffer），Go 端按 ParseInLocation 精确过滤
func ShopAggregateByID(ctx context.Context, shopID string, from, to time.Time) (ShopAggregateStats, error) {
	if DB == nil {
		return ShopAggregateStats{}, errors.New("DB 未初始化")
	}
	loc, _ := time.LoadLocation("Asia/Shanghai")
	if loc == nil {
		loc = time.FixedZone("CST", 8*3600)
	}
	dateFrom := from.AddDate(0, 0, -1).Format("2006-01-02")
	dateTo := to.AddDate(0, 0, 1).Format("2006-01-02")
	var appts []Appointment
	if err := DB.WithContext(ctx).
		Where("shop_id = ? AND date >= ? AND date <= ?", shopID, dateFrom, dateTo).
		Find(&appts).Error; err != nil {
		return ShopAggregateStats{}, err
	}

	var s ShopAggregateStats
	for _, a := range appts {
		dt, err := time.ParseInLocation("2006-01-02 15:04", a.Date+" "+a.Time, loc)
		if err != nil {
			continue
		}
		if dt.Before(from) || !dt.Before(to) {
			continue
		}
		s.Total++
		switch a.Status {
		case "completed":
			s.Completed++
		case "noshow":
			s.NoShow++
		case "cancelled":
			s.Cancelled++
		case "active":
			s.Active++
		}
	}
	closed := s.NoShow + s.Completed
	if closed > 0 {
		s.NoShowRate = float64(s.NoShow) / float64(closed)
		s.CompleteRate = float64(s.Completed) / float64(closed)
	}
	return s, nil
}
