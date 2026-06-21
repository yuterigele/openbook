package api

import (
	"context"
	"net/http"
	"sort"
	"time"

	"github.com/cloudwego/eino-examples/quickstart/chatwitheino/auth"
	"github.com/cloudwego/eino-examples/quickstart/chatwitheino/storage"
	"github.com/cloudwego/hertz/pkg/app"
)

// ChainDashboardResponse 跨店看板响应（v4.0 PRD §11.10）
//
//   - 设计目标：连锁品牌 owner 一次性看所有门店表现，不用逐店切
//   - 与单店 DashboardResponse 的区别：
//     * 多了 Shops []ShopAggregate（每店明细）
//     * 多了 ChainTotals（所有店合计）
//     * TopShops（按 total 排序，limit 5）—— 帮 owner 一眼识别"哪几家店最忙"
//     * EventFunnelChain（跨店事件漏斗，不按 shop_id 过滤）
type ChainDashboardResponse struct {
	GeneratedAt      time.Time                 `json:"generated_at"`
	TotalShops       int                       `json:"total_shops"`
	ChainTotals      ChainTotals               `json:"chain_totals"`
	Shops            []storage.ShopAggregate   `json:"shops"`
	TopShops         []ShopRank                `json:"top_shops"`         // 按 total DESC
	EventFunnelChain []EventStat               `json:"event_funnel_chain"` // 跨店事件漏斗（月）
}

// ChainTotals 跨店合计（单窗口）
type ChainTotals struct {
	Window       string  `json:"window"`         // "today" / "week" / "month"
	Total        int     `json:"total"`
	Completed    int     `json:"completed"`
	NoShow       int     `json:"noshow"`
	Cancelled    int     `json:"cancelled"`
	Active       int     `json:"active"`
	NoShowRate   float64 `json:"no_show_rate"`
	CompleteRate float64 `json:"complete_rate"`
}

// ShopRank 单店排名（用于 TopShops）
type ShopRank struct {
	ShopID   string `json:"shop_id"`
	ShopName string `json:"shop_name"`
	Total    int    `json:"total"`
}

// chainDashboardHandler GET /api/admin/chain/dashboard
//
// 鉴权策略（v4.0 MVP）：
//   - 当前实现：任何已登录的 admin 都能访问（role != ""）
//   - 后续要做细粒度控制：要求 role="platform_admin"（待产品定义清晰后）
//   - 文档里写明这一权衡，避免后续误以为"默认是 owner 限定"
func chainDashboardHandler(ctx context.Context, c *app.RequestContext) {
	if storage.DB == nil {
		c.JSON(http.StatusServiceUnavailable, map[string]string{"error": "db not initialized"})
		return
	}
	cl := auth.GetClaims(c)
	if cl == nil || cl.Role == "" {
		c.JSON(http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	resp := buildChainDashboard(ctx)
	c.JSON(http.StatusOK, resp)
}

// buildChainDashboard 跨店看板数据组装
//
//   - 单次调用：ListAllShops + 每个店 ShopAggregateByID + 跨店 chainEventFunnel
//   - 性能边界：N 个店 = N+2 次 SQL。当前目标 N=5~20 家店，性能足够；
//     后续店多到 100+ 时改成批量 appointments 查 + Go 端按 shop_id 分组聚合
func buildChainDashboard(ctx context.Context) ChainDashboardResponse {
	loc, _ := time.LoadLocation("Asia/Shanghai")
	if loc == nil {
		loc = time.FixedZone("CST", 8*3600)
	}
	now := time.Now().In(loc)
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	monthStart := todayStart.AddDate(0, -1, 0)

	resp := ChainDashboardResponse{
		GeneratedAt: now,
	}

	shops := storage.ListAllShops(ctx)
	resp.TotalShops = len(shops)

	// 1. 每月窗口跨店汇总（商家最关心的"过去一个月整盘经营情况"）
	monthTotals := ChainTotals{Window: "month"}
	resp.Shops = make([]storage.ShopAggregate, 0, len(shops))
	resp.TopShops = make([]ShopRank, 0, len(shops))

	for _, s := range shops {
		stats, err := storage.ShopAggregateByID(ctx, s.ID, monthStart, now.Add(24*time.Hour))
		if err != nil {
			// 单店失败不阻塞整体 —— 留空 stats 继续
			stats = storage.ShopAggregateStats{}
		}
		resp.Shops = append(resp.Shops, storage.ShopAggregate{Shop: s, Stats: stats})

		monthTotals.Total += stats.Total
		monthTotals.Completed += stats.Completed
		monthTotals.NoShow += stats.NoShow
		monthTotals.Cancelled += stats.Cancelled
		monthTotals.Active += stats.Active

		resp.TopShops = append(resp.TopShops, ShopRank{
			ShopID:   s.ID,
			ShopName: s.Name,
			Total:    stats.Total,
		})
	}
	closed := monthTotals.NoShow + monthTotals.Completed
	if closed > 0 {
		monthTotals.NoShowRate = float64(monthTotals.NoShow) / float64(closed)
		monthTotals.CompleteRate = float64(monthTotals.Completed) / float64(closed)
	}
	resp.ChainTotals = monthTotals

	// 2. TopShops 按 total DESC 排序，limit 5
	sort.Slice(resp.TopShops, func(i, j int) bool {
		if resp.TopShops[i].Total != resp.TopShops[j].Total {
			return resp.TopShops[i].Total > resp.TopShops[j].Total
		}
		return resp.TopShops[i].ShopID < resp.TopShops[j].ShopID
	})
	if len(resp.TopShops) > 5 {
		resp.TopShops = resp.TopShops[:5]
	}

	// 3. 跨店事件漏斗（月窗口，不按 shop_id 过滤）
	resp.EventFunnelChain = chainEventFunnel(ctx, monthStart, now, 20)
	return resp
}

// chainEventFunnel 跨店事件漏斗（不按 shop_id 过滤）
//
//   - 与 eventFunnel 同口径：Go 端 ParseAnyTime 跨 sqlite/mysql 驱动兼容
//   - 区别：去掉 shop_id 过滤条件
//   - 用途：chain 看板展示"整个连锁"的事件分布
func chainEventFunnel(ctx context.Context, since, until time.Time, limit int) []EventStat {
	if storage.DB == nil {
		return nil
	}
	sinceBuf := since.AddDate(0, 0, -1)
	untilBuf := until.AddDate(0, 0, 1)
	var rows []map[string]any
	if err := storage.DB.WithContext(ctx).
		Table("event_logs").
		Select("event_type, created_at").
		Where("created_at >= ? AND created_at <= ?", sinceBuf, untilBuf).
		Scan(&rows).Error; err != nil {
		return nil
	}
	counts := make(map[string]int, len(rows))
	for _, r := range rows {
		et, _ := r["event_type"].(string)
		if et == "" {
			continue
		}
		// idle_slot_push:DATE:CUST 归一（同 eventFunnel）
		if i := indexOfByte(et, ':'); i > 0 {
			et = et[:i]
		}
		ts, ok := storage.ParseAnyTime(r["created_at"])
		if !ok || ts.Before(since) || !ts.Before(until) {
			continue
		}
		counts[et]++
	}
	out := make([]EventStat, 0, len(counts))
	for et, n := range counts {
		out = append(out, EventStat{EventType: et, Count: n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].EventType < out[j].EventType
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// indexOfByte 找 byte 首次出现位置（避免引 strings 包名冲突）
func indexOfByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}
