package storage

// plan_gate.go —— plan limit gate（v4.12 增量）
//
// 各 handler 调 CheckPlanLimit 校验当前 shop.plan 是否够用
//   - 超限 → 返 error，handler 决定怎么返 HTTP 状态
//
// 用法示例（api/handlers 里）：
//   if err := storage.CheckPlanLimit(ctx, shopID, "barbers", currentCount+1); err != nil {
//       c.JSON(http.StatusPaymentRequired, map[string]string{"error": err.Error()})
//       return
//   }
//
// 设计要点：
//   - CheckPlanLimit 是纯 DB 操作，调用方传"当前值"和"新值"
//   - 限 -1 = 不限（flagship barber 不限 / enterprise 都不限）
//   - 限 0 = 严格不允许（默认 fallback，plan 不在白名单时返 0）
//   - 不读 cache（plan 切换后立即生效，不优化这个）

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ErrPlanLimitExceeded 资源超 plan 限额
//
// handler 看到应该返 402 Payment Required（或 403，看产品决定）
var ErrPlanLimitExceeded = errors.New("plan limit exceeded")

// PlanLimitError 带具体资源 + 限额的 error
//
// 让 handler 能直接拼"已用 X / 限 Y 条，升级 plan"
type PlanLimitError struct {
	Resource   string // "barbers" / "shops"
	CurrentVal int    // 已用
	Limit      int    // 限额（-1 = 不限）
	Plan       string // 当前 plan
}

func (e *PlanLimitError) Error() string {
	if e.Limit < 0 {
		return fmt.Sprintf("plan %q 不限制 %s，但当前已是 %d", e.Plan, e.Resource, e.CurrentVal)
	}
	return fmt.Sprintf("plan %q 限 %d 个 %s，当前已有 %d，升级 plan 解锁", e.Plan, e.Limit, e.Resource, e.CurrentVal)
}

// IsPlanLimitExceeded 断言是 plan limit 错（handler 用这个分类返 HTTP）
func IsPlanLimitExceeded(err error) bool {
	var p *PlanLimitError
	return errors.As(err, &p)
}

// CheckPlanLimit 校验"加入 newCount 后"是否超 plan 限额
//
//   - currentVal：当前已用数（如已有 barber 数）
//   - newCount：本次要加的数（通常 1）
//   - 返 nil：未超
//   - 返 *PlanLimitError：超了
//
// 例：checkPlanLimit(ctx, shopID, "barbers", 3, 1) → basic plan 限 3，第 4 个 barber 应返错
func CheckPlanLimit(ctx context.Context, shopID string, resource string, currentVal int, newCount int) error {
	if newCount <= 0 {
		return nil // 加 0 / 减数 不会超
	}
	if shopID == "" {
		return errors.New("plan gate: shopID 空")
	}
	// 取当前 shop.plan
	var shop Shop
	if err := DB.WithContext(ctx).Where("id = ?", shopID).First(&shop).Error; err != nil {
		return fmt.Errorf("plan gate: 读 shop 失败: %w", err)
	}
	limit := PlanLimitInt(shop.Plan, resource)
	if limit < 0 {
		return nil // 不限
	}
	if currentVal+newCount > limit {
		return &PlanLimitError{
			Resource:   resource,
			CurrentVal: currentVal,
			Limit:      limit,
			Plan:       shop.Plan,
		}
	}
	return nil
}

// CountBarbersByShop 统计某 shop 的 barber 数（plan gate 用）
//
// 不读 inactive（active=false 算"已删除"，不占 plan 限额）
func CountBarbersByShop(ctx context.Context, shopID string) (int, error) {
	if DB == nil {
		return 0, errors.New("storage.DB 未初始化")
	}
	var n int64
	if err := DB.WithContext(ctx).Model(&Barber{}).
		Where("shop_id = ? AND active = ?", shopID, true).
		Count(&n).Error; err != nil {
		return 0, err
	}
	return int(n), nil
}

// IsPlanExpired 检查当前 shop 的订阅是否过期（v4.12 阶段 3 准备）
//
//   - 没过 expiresAt：fresh
//   - 过了 expiresAt + 宽限期：expired + 宽限期内（仍可用，但提醒续费）
//   - 过了 expiresAt + 宽限期：frozen（功能冻结）
//
// 宽限期：v4.12 固定 7 天
const PlanGracePeriod = 7 * 24 * time.Hour

// IsPlanExpired 返当前 shop 订阅状态
//
// 返回值：
//   - fresh: false, 0
//   - 宽限期内: false, daysLeft（负数 = 已过 N 天）
//   - frozen: true, 0
//
// v4.12 阶段 3 middleware 调这个判定返 402/403
func IsPlanExpired(ctx context.Context, shopID string) (frozen bool, daysLeft int) {
	if DB == nil {
		return false, 0
	}
	var sub Subscription
	err := DB.WithContext(ctx).
		Where("shop_id = ? AND cancelled_at IS NULL", shopID).
		Order("expires_at DESC").
		First(&sub).Error
	if err != nil {
		// 没找到 sub → 当作 fresh（避免 v4.12 之前的老店铺被误判 frozen）
		return false, 0
	}
	now := time.Now()
	if sub.ExpiresAt.After(now) {
		return false, 0 // fresh
	}
	// 已过 expires_at
	diff := now.Sub(sub.ExpiresAt)
	if diff > PlanGracePeriod {
		return true, 0 // frozen
	}
	// 宽限期内
	return false, -int(diff.Hours() / 24)
}
