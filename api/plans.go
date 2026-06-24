package api

// plans.go —— 商户看自己 plan 元数据 + 当前订阅状态（v4.12 增量）
//
// GET /api/admin/plans
//   - perm: view:subscription（v4.10.1 收紧：owner 有，staff 没）
//   - 返：当前 shop 的 plan + expires_at + 倒计时 + 4 档 plan 对比
//
// 设计：
//   - 不返 platform_admin 才能看的信息（不在这里列所有店）
//   - 商户用这页面看自己 plan + 升级
//   - 升级走"联系商务"modal（v4.12 不接支付；v4.13 接入微信支付后改这个 modal）
//
// v4.13 扩展点：这里可以加 `payment_link` 字段（微信支付跳转 url），modal 改成
// "立即支付"按钮 + 跳转。

import (
	"context"
	"net/http"
	"time"

	"github.com/cloudwego/hertz/pkg/app"

	"github.com/yuterigele/openbook/storage"
)

// PlanView 4 档 plan 对比表的一行
type PlanView struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	PriceCents int      `json:"price_cents"`
	Currency   string   `json:"currency"`
	MaxShops   int      `json:"max_shops"`
	MaxBarbers int      `json:"max_barbers"`
	Features   []string `json:"features"`
	Note       string   `json:"note"`
}

// PlansResponse /api/admin/plans 响应
type PlansResponse struct {
	CurrentPlan string     `json:"current_plan"`
	CurrentName string     `json:"current_name"` // 中文名（冗余方便前端）
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
	DaysLeft    int        `json:"days_left"`     // > 0 = 还剩 N 天
	Frozen      bool       `json:"frozen"`        // true = 已 frozen（> 7 天宽限期）
	GraceDays   int        `json:"grace_days"`    // 0 = fresh；N = 宽限期内剩 N 天
	Plans       []PlanView `json:"plans"`         // 4 档对比
}

// plansHandler GET /api/admin/plans
//
// perm: view:subscription
func plansHandler(ctx context.Context, c *app.RequestContext) {
	shopID := shopFromClaims(c)
	if shopID == "" {
		c.JSON(http.StatusUnauthorized, map[string]string{"error": "no shop in session"})
		return
	}
	if storage.DB == nil {
		c.JSON(http.StatusServiceUnavailable, map[string]string{"error": "db not initialized"})
		return
	}

	// 1) 当前 shop 的 plan + expires_at
	shop, err := storage.GetShopByID(ctx, shopID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, map[string]string{"error": "读 shop 失败: " + err.Error()})
		return
	}

	// 2) 倒计时：取最新 sub 的 expires_at
	var expiresAt *time.Time
	var subDaysLeft int
	var sub storage.Subscription
	if err := storage.DB.WithContext(ctx).
		Where("shop_id = ? AND cancelled_at IS NULL", shopID).
		Order("expires_at DESC").
		First(&sub).Error; err == nil {
		t := sub.ExpiresAt
		expiresAt = &t
		// days_left: 0 = 今天到期；负数 = 已过 N 天
		diff := time.Until(t)
		subDaysLeft = int(diff.Hours() / 24)
		if diff.Hours() < 0 {
			// 已过期（含宽限期）
			subDaysLeft = 0
		}
	}

	// 3) frozen / grace days（用 plan_active middleware 同源函数）
	frozen, graceDays := storage.IsPlanExpired(ctx, shopID)

	// 4) 当前 plan 的中文名
	currentPlan := shop.Plan
	if !storage.IsValidPlanID(currentPlan) {
		currentPlan = storage.DefaultPlanID
	}
	meta, _ := storage.GetPlan(currentPlan)
	currentName := meta.Name
	if currentName == "" {
		currentName = currentPlan
	}

	// 5) 4 档 plan 对比表（按价格升序）
	planViews := make([]PlanView, 0, len(storage.AllPlanIDs))
	for _, id := range storage.AllPlanIDs {
		m, ok := storage.GetPlan(id)
		if !ok {
			continue
		}
		planViews = append(planViews, PlanView{
			ID:         m.ID,
			Name:       m.Name,
			PriceCents: m.PriceCents,
			Currency:   m.Currency,
			MaxShops:   m.MaxShops,
			MaxBarbers: m.MaxBarbers,
			Features:   m.Features,
			Note:       m.Note,
		})
	}

	c.JSON(http.StatusOK, PlansResponse{
		CurrentPlan: currentPlan,
		CurrentName: currentName,
		ExpiresAt:   expiresAt,
		DaysLeft:    subDaysLeft,
		Frozen:      frozen,
		GraceDays:   graceDays,
		Plans:       planViews,
	})
}
