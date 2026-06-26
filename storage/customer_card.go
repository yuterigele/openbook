package storage

// customer_card.go —— 顾客卡实例操作（v4.15 储值 / 次卡模块）
//
// 核心操作：
//   - SellCardToCustomer     售卡（创建 CustomerCard + 写一条 recharge 流水）
//   - ConsumeCustomerCard    扣减（写一条 consume 流水，自动维护 depleted 状态）
//   - AdjustCustomerCard     手动调账（adjust_up / adjust_down，**Reason 必填**，保证追溯）
//   - ListCustomerCards      列某顾客的所有卡
//   - ListShopCustomerCards  列某店所有顾客卡（管理视图）
//   - ListCardTransactions   列某顾客卡的流水
//
// 设计要点：
//   - 多店隔离：所有方法必须传 shopID；customer_card 通过 (shop_id, customer_id) 联合查
//   - 防负数：consume / adjust_down 操作前断言余额 > 0；不允许"扣成负数"
//   - 自动 depleted：余额/次数归零时自动更新 customer_card.status = "depleted"
//   - 自动 expired：每次操作前 lazy check expires_at，过期则标 expired 拒操作
//   - 跨店预留：v4.15 跨店不共用，但 schema 已经用 shop_id 卡死；将来要做连锁共用
//     可加 chain_pool_id 字段，无需改动 customer_id 索引
//   - 事务：所有写操作用 gorm.Transaction 包起来，保证 CustomerCard + CardTransaction 同生共死

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// ErrCustomerCardNotFoundInShop 在指定店铺中未找到该顾客卡
var ErrCustomerCardNotFoundInShop = errors.New("customer card not found in this shop")

// ErrInsufficientBalance 余额不足（扣减/调减超出现有余额）
var ErrInsufficientBalance = errors.New("insufficient balance")

// ErrCustomerCardExpired 顾客卡已过期
var ErrCustomerCardExpired = errors.New("customer card expired")

// ErrCustomerCardNotActive 顾客卡不在 active 状态（已 depleted / archived / expired）
var ErrCustomerCardNotActive = errors.New("customer card not active")

// ErrReasonRequired 调账操作必须填理由
var ErrReasonRequired = errors.New("reason required for adjust operation")

// SellCardToCustomerParams 售卡入参
type SellCardToCustomerParams struct {
	ShopID     string
	CustomerID string
	CardID     string // 已存在的 card 产品
	OperatorID     uint64
	OperatorName   string
	Note       string // 顾客卡实例备注（"生日礼物"等），可选
}

// SellCardToCustomer 售卡
//
//   - 校验 Card 存在 + 同店 + active
//   - 校验 Customer 存在（同店或全平台？简单做：顾客存在即可，不强求"曾在本店有预约"）
//   - 创建 CustomerCard：
//       储值：balance = Face + Bonus（首次充值），initial_balance 同
//       次卡：remaining = TotalCount，initial_count 同
//   - expires_at = now + ValidDays（ValidDays=0 时 nil = 永久）
//   - 写一条 recharge 流水（delta = 初始余额/次数，balance_after = 初始余额/次数）
//
// 注意：售卡本身不接支付（v4.15 范围纯后台记账，现金/微信收款商户线下处理）
func SellCardToCustomer(ctx context.Context, p SellCardToCustomerParams) (*CustomerCard, error) {
	if DB == nil {
		return nil, errors.New("DB 未初始化")
	}
	if p.ShopID == "" || p.CustomerID == "" || p.CardID == "" {
		return nil, errors.New("shop_id / customer_id / card_id 必填")
	}
	if len(p.Note) > 256 {
		return nil, fmt.Errorf("备注过长（最多 256 字），得到 %d 字", len(p.Note))
	}

	// 校验 card
	card, err := GetCardInShop(ctx, p.ShopID, p.CardID)
	if err != nil {
		return nil, fmt.Errorf("卡产品不存在：%w", err)
	}
	if card.Status != CardStatusActive {
		return nil, fmt.Errorf("卡产品已下架，请先重新上架")
	}

	// 校验 customer 存在
	var cust Customer
	if err := DB.WithContext(ctx).Where("id = ?", p.CustomerID).First(&cust).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("顾客不存在：%s", p.CustomerID)
		}
		return nil, err
	}

	now := time.Now()
	cc := &CustomerCard{
		ID:                 uuid.NewString(),
		ShopID:             p.ShopID,
		CustomerID:         p.CustomerID,
		CardID:             card.ID,
		CardName:           card.Name,
		Type:               card.Type,
		PriceCents:         card.PriceCents,
		Status:             CustomerCardStatusActive,
		Note:               strings.TrimSpace(p.Note),
		PurchasedAt:        now,
		ServiceID:          card.ServiceID,
		ServiceName:        card.ServiceName,
		CreatedAt:          now,
		UpdatedAt:          now,
	}
	if card.Type == CardTypeStoredValue {
		initial := card.FaceValueCents + card.BonusCents
		cc.BalanceCents = initial
		cc.InitialBalanceCents = initial
	} else {
		cc.RemainingCount = card.TotalCount
		cc.InitialCount = card.TotalCount
	}
	if card.ValidDays > 0 {
		exp := now.AddDate(0, 0, card.ValidDays)
		cc.ExpiresAt = &exp
	}

	// 事务：插入 customer_card + 写 recharge 流水
	err = DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(cc).Error; err != nil {
			return err
		}
		// 写一条 recharge 流水
		txRow := CardTransaction{
			ID:             uuid.NewString(),
			ShopID:         p.ShopID,
			CustomerID:     p.CustomerID,
			CustomerCardID: cc.ID,
			Type:           CardTxRecharge,
			Delta:          initialDeltaForCard(card, cc),
			BalanceAfter:   initialBalanceForCard(card, cc),
			Reason:         "售卡",
			OperatorID:     p.OperatorID,
			OperatorName:   p.OperatorName,
			CreatedAt:      now,
		}
		if p.Note != "" {
			txRow.Reason = "售卡 · " + p.Note
		}
		return tx.Create(&txRow).Error
	})
	if err != nil {
		return nil, err
	}
	return cc, nil
}

// ConsumeCustomerCardParams 扣减入参
type ConsumeCustomerCardParams struct {
	ShopID        string
	CustomerCardID string
	// 储值：amount_cents（正数，扣减额度）
	// 次卡：amount_cents 忽略，永远扣 1 次
	AmountCents   int
	Reason        string    // "剪发 by 张师傅" / "染发" 等；可空
	AppointmentID string    // 关联预约，可空
	OperatorID    uint64
	OperatorName  string
}

// ConsumeCustomerCard 扣减（消费）
//
//   - 校验 customer_card 存在 + 同店 + active
//   - 校验未过期（lazy check）
//   - 储值：balance -= amount_cents，amount_cents 必须 > 0 且 <= 当前余额
//   - 次卡：remaining -= 1，remaining 必须 > 0
//   - 扣完余额/次数 = 0 → 自动 status = "depleted"
//   - 写一条 consume 流水
func ConsumeCustomerCard(ctx context.Context, p ConsumeCustomerCardParams) (*CustomerCard, error) {
	if DB == nil {
		return nil, errors.New("DB 未初始化")
	}
	if p.ShopID == "" || p.CustomerCardID == "" {
		return nil, errors.New("shop_id / customer_card_id 必填")
	}
	if len(p.Reason) > 256 {
		return nil, fmt.Errorf("reason 过长（最多 256 字），得到 %d 字", len(p.Reason))
	}

	cc, err := GetCustomerCardInShop(ctx, p.ShopID, p.CustomerCardID)
	if err != nil {
		return nil, err
	}
	// 状态校验：expired / depleted / archived 一律拒
	if err := assertCustomerCardActive(cc); err != nil {
		return nil, err
	}

	err = DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := time.Now()
		var delta, after int
		switch cc.Type {
		case CardTypeStoredValue:
			if p.AmountCents <= 0 {
				return fmt.Errorf("扣减金额必须 > 0，得到 %d", p.AmountCents)
			}
			if cc.BalanceCents < p.AmountCents {
				return fmt.Errorf("%w：当前余额 %d 分，扣减 %d 分",
					ErrInsufficientBalance, cc.BalanceCents, p.AmountCents)
			}
			delta = -p.AmountCents
			after = cc.BalanceCents - p.AmountCents
			if err := tx.Model(cc).Updates(map[string]interface{}{
				"balance_cents": after,
				"updated_at":    now,
			}).Error; err != nil {
				return err
			}
		case CardTypeCount:
			if cc.RemainingCount <= 0 {
				return fmt.Errorf("%w：剩余次数 0", ErrInsufficientBalance)
			}
			delta = -1
			after = cc.RemainingCount - 1
			if err := tx.Model(cc).Updates(map[string]interface{}{
				"remaining_count": after,
				"updated_at":      now,
			}).Error; err != nil {
				return err
			}
		default:
			return fmt.Errorf("未知卡类型：%s", cc.Type)
		}

		// 自动 depleted
		if after == 0 {
			if err := tx.Model(cc).Update("status", CustomerCardStatusDepleted).Error; err != nil {
				return err
			}
		}

		// 写 consume 流水
		txRow := CardTransaction{
			ID:             uuid.NewString(),
			ShopID:         p.ShopID,
			CustomerID:     cc.CustomerID,
			CustomerCardID: cc.ID,
			Type:           CardTxConsume,
			Delta:          delta,
			BalanceAfter:   after,
			Reason:         strings.TrimSpace(p.Reason),
			AppointmentID:  p.AppointmentID,
			OperatorID:     p.OperatorID,
			OperatorName:   p.OperatorName,
			CreatedAt:      now,
		}
		return tx.Create(&txRow).Error
	})
	if err != nil {
		return nil, err
	}
	// 重新读返回最新状态
	return GetCustomerCardInShop(ctx, p.ShopID, p.CustomerCardID)
}

// AdjustDirection 调账方向
type AdjustDirection string

const (
	AdjustUp   AdjustDirection = "up"   // 调增（退款补偿 / 赠送）
	AdjustDown AdjustDirection = "down" // 调减（数据修正 / 退卡冲账）
)

// AdjustCustomerCardParams 手动调账入参
//
// 强制要求：
//   - Reason 非空（v4.15 调账追溯要求）
//   - Direction 必须是 up / down
//   - 调减时 amount_cents <= 当前余额（防负数）
type AdjustCustomerCardParams struct {
	ShopID         string
	CustomerCardID string
	Direction      AdjustDirection // up / down
	AmountCents    int             // 调增/调减额度（正数；次卡为次数，整数）
	Reason         string          // 必填
	OperatorID     uint64
	OperatorName   string
}

// AdjustCustomerCard 手动调账（v4.15 调账追溯）
//
//   - 校验 customer_card 存在 + 同店
//   - Reason 必填（追溯需求）
//   - 储值 / 次卡：分别增减 balance_cents / remaining_count
//   - 防负数：调减时 amount <= 当前余额
//   - 调增后余额为 0 是不可能的（除非原来是负数，但 v4.15 不允许负数），所以调增不会触发 depleted
//   - 调减把余额扣到 0 → 自动 depleted
//   - 写一条 adjust_up / adjust_down 流水（delta 带符号，balance_after 为最新值）
func AdjustCustomerCard(ctx context.Context, p AdjustCustomerCardParams) (*CustomerCard, error) {
	if DB == nil {
		return nil, errors.New("DB 未初始化")
	}
	if p.ShopID == "" || p.CustomerCardID == "" {
		return nil, errors.New("shop_id / customer_card_id 必填")
	}
	if p.Direction != AdjustUp && p.Direction != AdjustDown {
		return nil, fmt.Errorf("direction 必须是 up 或 down，得到 %q", p.Direction)
	}
	p.Reason = strings.TrimSpace(p.Reason)
	if p.Reason == "" {
		return nil, fmt.Errorf("%w（请说明调账原因，便于审计追溯）", ErrReasonRequired)
	}
	if p.AmountCents <= 0 {
		return nil, fmt.Errorf("调账金额必须 > 0，得到 %d", p.AmountCents)
	}
	if len(p.Reason) > 256 {
		return nil, fmt.Errorf("reason 过长（最多 256 字），得到 %d 字", len(p.Reason))
	}

	cc, err := GetCustomerCardInShop(ctx, p.ShopID, p.CustomerCardID)
	if err != nil {
		return nil, err
	}
	// 调账允许对 depleted / archived 调增（数据修正场景）
	// 但不允许对 expired 调（过期卡不该再动）；如果想救回，让商户先改 valid_days
	if cc.Status == CustomerCardStatusExpired {
		return nil, fmt.Errorf("%w：已过期的卡不能调账，请先调整 card 产品的 valid_days 后重新售卡", ErrCustomerCardExpired)
	}

	err = DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := time.Now()
		var delta, after int
		txType := CardTxAdjustUp
		if p.Direction == AdjustDown {
			txType = CardTxAdjustDown
		}
		switch cc.Type {
		case CardTypeStoredValue:
			switch p.Direction {
			case AdjustUp:
				delta = +p.AmountCents
				after = cc.BalanceCents + p.AmountCents
			case AdjustDown:
				if p.AmountCents > cc.BalanceCents {
					return fmt.Errorf("%w：当前余额 %d 分，调减 %d 分",
						ErrInsufficientBalance, cc.BalanceCents, p.AmountCents)
				}
				delta = -p.AmountCents
				after = cc.BalanceCents - p.AmountCents
			}
			updates := map[string]interface{}{
				"balance_cents": after,
				"updated_at":    now,
			}
			if after == 0 {
				updates["status"] = CustomerCardStatusDepleted
			}
			if err := tx.Model(cc).Updates(updates).Error; err != nil {
				return err
			}
		case CardTypeCount:
			switch p.Direction {
			case AdjustUp:
				delta = +p.AmountCents
				after = cc.RemainingCount + p.AmountCents
			case AdjustDown:
				if p.AmountCents > cc.RemainingCount {
					return fmt.Errorf("%w：剩余次数 %d，调减 %d",
						ErrInsufficientBalance, cc.RemainingCount, p.AmountCents)
				}
				delta = -p.AmountCents
				after = cc.RemainingCount - p.AmountCents
			}
			updates := map[string]interface{}{
				"remaining_count": after,
				"updated_at":      now,
			}
			if after == 0 {
				updates["status"] = CustomerCardStatusDepleted
			}
			if err := tx.Model(cc).Updates(updates).Error; err != nil {
				return err
			}
		default:
			return fmt.Errorf("未知卡类型：%s", cc.Type)
		}

		// 写调账流水
		return tx.Create(&CardTransaction{
			ID:             uuid.NewString(),
			ShopID:         p.ShopID,
			CustomerID:     cc.CustomerID,
			CustomerCardID: cc.ID,
			Type:           txType,
			Delta:          delta,
			BalanceAfter:   after,
			Reason:         p.Reason,
			OperatorID:     p.OperatorID,
			OperatorName:   p.OperatorName,
			CreatedAt:      now,
		}).Error
	})
	if err != nil {
		return nil, err
	}
	return GetCustomerCardInShop(ctx, p.ShopID, p.CustomerCardID)
}

// GetCustomerCardInShop 按 ID 查 customer_card，校验属于指定 shop
func GetCustomerCardInShop(ctx context.Context, shopID, customerCardID string) (*CustomerCard, error) {
	if DB == nil {
		return nil, errors.New("DB 未初始化")
	}
	if shopID == "" || customerCardID == "" {
		return nil, errors.New("shop_id 和 customer_card_id 必填")
	}
	var cc CustomerCard
	if err := DB.WithContext(ctx).Where("id = ?", customerCardID).First(&cc).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrCustomerCardNotFoundInShop
		}
		return nil, err
	}
	if cc.ShopID != shopID {
		return nil, ErrCustomerCardNotFoundInShop
	}
	return &cc, nil
}

// ListCustomerCards 列某顾客在某店的所有卡
//
// status="" → 全部；否则过滤指定状态
// 排序：active 优先 + purchased_at DESC（新卡在前）
func ListCustomerCards(ctx context.Context, shopID, customerID, status string) ([]CustomerCard, error) {
	if DB == nil {
		return nil, errors.New("DB 未初始化")
	}
	if shopID == "" || customerID == "" {
		return nil, errors.New("shop_id 和 customer_id 必填")
	}
	q := DB.WithContext(ctx).Where("shop_id = ? AND customer_id = ?", shopID, customerID)
	if status != "" {
		q = q.Where("status = ?", status)
	}
	var out []CustomerCard
	// active 优先（用 ORDER BY (status='active') DESC），其次按购入时间倒序
	if err := q.Order("(status = 'active') DESC, purchased_at DESC").Find(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

// ListShopCustomerCards 列某店所有顾客卡（管理视图）
//
// 默认按 purchased_at DESC；limit 默认 200，max 500
func ListShopCustomerCards(ctx context.Context, shopID string, limit int) ([]CustomerCard, error) {
	if DB == nil {
		return nil, errors.New("DB 未初始化")
	}
	if shopID == "" {
		return nil, errors.New("shop_id 必填")
	}
	if limit <= 0 {
		limit = 200
	}
	if limit > 500 {
		limit = 500
	}
	var out []CustomerCard
	if err := DB.WithContext(ctx).
		Where("shop_id = ?", shopID).
		Order("purchased_at DESC").
		Limit(limit).
		Find(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

// ListCardTransactions 列某顾客卡的流水（按时间倒序）
func ListCardTransactions(ctx context.Context, shopID, customerCardID string, limit int) ([]CardTransaction, error) {
	if DB == nil {
		return nil, errors.New("DB 未初始化")
	}
	if shopID == "" || customerCardID == "" {
		return nil, errors.New("shop_id 和 customer_card_id 必填")
	}
	if limit <= 0 {
		limit = 200
	}
	if limit > 500 {
		limit = 500
	}
	var out []CardTransaction
	if err := DB.WithContext(ctx).
		Where("shop_id = ? AND customer_card_id = ?", shopID, customerCardID).
		Order("created_at DESC").
		Limit(limit).
		Find(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

// RefreshCustomerCardExpiry 扫描并把 expires_at < now 的 active 卡标为 expired（lazy + cron）
//
// 业务触发：每次 Get/List 时 lazy 检查；cron 也可调一次保证一致性
// 返回：本次标记为 expired 的卡数
func RefreshCustomerCardExpiry(ctx context.Context, shopID string) (int64, error) {
	if DB == nil {
		return 0, errors.New("DB 未初始化")
	}
	now := time.Now()
	q := DB.WithContext(ctx).Model(&CustomerCard{}).
		Where("status = ? AND expires_at IS NOT NULL AND expires_at < ?", CustomerCardStatusActive, now)
	if shopID != "" {
		q = q.Where("shop_id = ?", shopID)
	}
	res := q.Update("status", CustomerCardStatusExpired)
	return res.RowsAffected, res.Error
}

// ---- helpers ----

// initialDeltaForCard 售卡时 delta 值（正数；recharge 流水记"充入"）
func initialDeltaForCard(card *Card, cc *CustomerCard) int {
	if card.Type == CardTypeStoredValue {
		return cc.BalanceCents
	}
	return cc.RemainingCount
}

// initialBalanceForCard 售卡时 balance_after 值
func initialBalanceForCard(card *Card, cc *CustomerCard) int {
	return initialDeltaForCard(card, cc)
}

// assertCustomerCardActive 断言卡处于 active 状态（未过期 / 未耗尽 / 未归档）
//
// 注意：expired 状态检查放在 lazy check 之后（每次操作都先看 expires_at）
func assertCustomerCardActive(cc *CustomerCard) error {
	if cc.Status != CustomerCardStatusActive {
		return fmt.Errorf("%w：当前状态 %s", ErrCustomerCardNotActive, cc.Status)
	}
	// lazy expired check
	if cc.ExpiresAt != nil && cc.ExpiresAt.Before(time.Now()) {
		return ErrCustomerCardExpired
	}
	return nil
}