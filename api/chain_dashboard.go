package api

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/cloudwego/hertz/pkg/app"

	"github.com/yuterigele/openbook/storage"
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
	GeneratedAt      time.Time               `json:"generated_at"`
	Window           string                  `json:"window"` // v4.1: today|week|month（响应里回传，便于客户端核对）
	TotalShops       int                     `json:"total_shops"`
	ChainTotals      ChainTotals             `json:"chain_totals"`
	Shops            []storage.ShopAggregate `json:"shops"`
	TopShops         []ShopRank              `json:"top_shops"`          // 按 total DESC
	EventFunnelChain []EventStat             `json:"event_funnel_chain"` // 跨店事件漏斗（同窗口）
}

// ChainTotals 跨店合计（单窗口）
type ChainTotals struct {
	Window       string  `json:"window"` // "today" / "week" / "month"
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

// ValidChainDashboardWindows 跨店看板支持的查询窗口（v4.1）
//
//   - today：今日 00:00 到明日 00:00（Asia/Shanghai）
//   - week ：本周一 00:00 到下周一 00:00（每周一作为周分界）
//   - month：自然月，今天所在月份的 1 号 00:00 到下月 1 号 00:00
//
// 顺序稳定，便于后续扩展（按字典序排列）。
var ValidChainDashboardWindows = []string{"month", "today", "week"}

// DefaultChainDashboardWindow 默认窗口（month），便于 API 客户端省略 query
const DefaultChainDashboardWindow = "month"

// parseWindow 解析查询参数里的 window 字符串
//
//   - 空串 → DefaultChainDashboardWindow
//   - 合法值（today/week/month）→ 原值（trim 后小写）
//   - 其他值 → ""（让调用方决定是 400 还是 fallback 到默认）
func parseWindow(raw string) string {
	w := strings.ToLower(strings.TrimSpace(raw))
	if w == "" {
		return DefaultChainDashboardWindow
	}
	for _, v := range ValidChainDashboardWindows {
		if w == v {
			return w
		}
	}
	return ""
}

// resolveWindowBounds 给定 now + window 返回 [from, to) 半开区间
//
//   - today  : 当日 00:00（含）到次日 00:00（不含）
//   - week   : 本周一 00:00（含）到下周一 00:00（不含）
//   - month  : 当月 1 号 00:00（含）到次月 1 号 00:00（不含）
//
// 默认按 Asia/Shanghai 计算（中国大陆发廊场景；location 加载失败时兜底 +08:00 fixed zone）。
func resolveWindowBounds(now time.Time, window string) (time.Time, time.Time) {
	loc, _ := time.LoadLocation("Asia/Shanghai")
	if loc == nil {
		loc = time.FixedZone("CST", 8*3600)
	}
	now = now.In(loc)
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)

	switch window {
	case "today":
		return todayStart, todayStart.Add(24 * time.Hour)
	case "week":
		// time.Weekday(): Sunday=0, Monday=1, ..., Saturday=6
		// 想要"周一为周开始"，所以 Sunday 要回退 6 天，其他回退 (weekday-1) 天
		offset := int(now.Weekday()) - int(time.Monday)
		if offset < 0 {
			offset += 7
		}
		weekStart := todayStart.AddDate(0, 0, -offset)
		return weekStart, weekStart.AddDate(0, 0, 7)
	case "month":
		monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, loc)
		return monthStart, monthStart.AddDate(0, 1, 0)
	default:
		// 防御性 fallback：未知 window 退回 month（语义等价于调用方自己传 month）
		monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, loc)
		return monthStart, monthStart.AddDate(0, 1, 0)
	}
}

// chainDashboardHandler GET /api/admin/chain/dashboard?window=today|week|month
//
// Query 参数：
//   - window：可选；默认 month；非法值返回 400
//
// 鉴权策略（v4.10.1 修复）：
//   - 路由层用 auth.RequireRole(RolePlatformAdmin) 强约束，只放行 platform_admin
//   - 之前 v4.0 MVP 留的"任何已登录的 admin 都能看"是权限泄漏——单店 owner 能看全平台
//   - 现在已经修复：单店 owner 路由层就 403
//   - 路由层 RequireRole 已经覆盖"未登录 + 角色不对"两种情况，handler 内不再重复校验
func chainDashboardHandler(ctx context.Context, c *app.RequestContext) {
	if storage.DB == nil {
		c.JSON(http.StatusServiceUnavailable, map[string]string{"error": "db not initialized"})
		return
	}
	window := parseWindow(c.Query("window"))
	if window == "" {
		c.JSON(http.StatusBadRequest, map[string]string{
			"error": "invalid window; want one of: today, week, month",
		})
		return
	}
	resp := buildChainDashboard(ctx, window)
	c.JSON(http.StatusOK, resp)
}

// buildChainDashboard 跨店看板数据组装（v4.1：window 参数化）
//
//   - 单次调用：ListAllShops + 每个店 ShopAggregateByID + 跨店 chainEventFunnel
//   - 性能边界：N 个店 = N+2 次 SQL。当前目标 N=5~20 家店，性能足够；
//     后续店多到 100+ 时改成批量 appointments 查 + Go 端按 shop_id 分组聚合
//   - window：today/week/month，决定 ChainTotals、Shops[*].Stats、EventFunnelChain 的时间区间
func buildChainDashboard(ctx context.Context, window string) ChainDashboardResponse {
	now := time.Now()
	from, to := resolveWindowBounds(now, window)

	resp := ChainDashboardResponse{
		GeneratedAt: now,
		Window:      window,
	}

	shops := storage.ListAllShops(ctx)
	resp.TotalShops = len(shops)

	// 1. 指定窗口的跨店汇总
	totals := ChainTotals{Window: window}
	resp.Shops = make([]storage.ShopAggregate, 0, len(shops))
	resp.TopShops = make([]ShopRank, 0, len(shops))

	for _, s := range shops {
		stats, err := storage.ShopAggregateByID(ctx, s.ID, from, to)
		if err != nil {
			// 单店失败不阻塞整体 —— 留空 stats 继续
			stats = storage.ShopAggregateStats{}
		}
		resp.Shops = append(resp.Shops, storage.ShopAggregate{Shop: s, Stats: stats})

		totals.Total += stats.Total
		totals.Completed += stats.Completed
		totals.NoShow += stats.NoShow
		totals.Cancelled += stats.Cancelled
		totals.Active += stats.Active

		resp.TopShops = append(resp.TopShops, ShopRank{
			ShopID:   s.ID,
			ShopName: s.Name,
			Total:    stats.Total,
		})
	}
	closed := totals.NoShow + totals.Completed
	if closed > 0 {
		totals.NoShowRate = float64(totals.NoShow) / float64(closed)
		totals.CompleteRate = float64(totals.Completed) / float64(closed)
	}
	resp.ChainTotals = totals

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

	// 3. 跨店事件漏斗（同窗口，不按 shop_id 过滤）
	resp.EventFunnelChain = chainEventFunnel(ctx, from, to, 20)
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