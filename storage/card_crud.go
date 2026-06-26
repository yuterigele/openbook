package storage

// card_crud.go —— 卡产品 CRUD（v4.15 储值 / 次卡模块）
//
//   - 商家在后台定义"卖什么卡"（产品级，类比 Service 目录）
//   - 储值卡：PriceCents（实付） + FaceValueCents（本金） + BonusCents（赠送）
//     例：2000 储值送 200 → PriceCents=200000, FaceValueCents=200000, BonusCents=20000
//     顾客首次充值后获得 (FaceValueCents + BonusCents) 的余额
//   - 次卡：PriceCents（实付） + ServiceID（关联服务） + TotalCount（次数）
//   - ValidDays=0 → 永久；>0 → 售出后 N 天到期
//   - 软下架（ArchiveCard / ActivateCard）：保留历史 CustomerCard 可查
//
// 多店隔离：所有方法必须传 shopID

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// ErrCardNameTaken 同店已存在同名卡产品（active 或 archived 都算）
var ErrCardNameTaken = errors.New("card name already taken")

// ErrCardNotFoundInShop 在指定店铺中未找到该卡产品
var ErrCardNotFoundInShop = errors.New("card not found in this shop")

// ErrCardHasActiveInstances 卡产品下还有 active 顾客卡，禁下架
var ErrCardHasActiveInstances = errors.New("card has active customer cards, cannot archive")

// CreateCardParams 创建卡产品入参
//
// 字段按 type 分支校验：
//   - stored_value: PriceCents>0, FaceValueCents>0, BonusCents>=0, ServiceID/ServiceName/TotalCount 忽略
//   - count:        PriceCents>0, ServiceID必填, TotalCount>0, FaceValueCents/BonusCents 忽略
type CreateCardParams struct {
	ShopID   string
	Name     string
	Type     string // CardTypeStoredValue / CardTypeCount
	Note     string
	PriceCents        int
	FaceValueCents    int // stored_value
	BonusCents        int // stored_value
	ServiceID         string // count
	ServiceName       string // count
	TotalCount        int    // count
	ValidDays         int
}

// CreateCard 创建卡产品
//
// 校验：
//   - Name 必填，trim 后 1-32 字
//   - Type 必须是 stored_value 或 count
//   - 储值卡：PriceCents / FaceValueCents > 0；BonusCents >= 0；TotalValue = Face + Bonus >= Price（否则等于免费送钱）
//   - 次卡：PriceCents / TotalCount > 0；ServiceID 必填
//   - ValidDays >= 0（0 = 永久）
func CreateCard(ctx context.Context, p CreateCardParams) (*Card, error) {
	if DB == nil {
		return nil, errors.New("DB 未初始化")
	}
	if p.ShopID == "" {
		return nil, errors.New("shop_id 必填")
	}
	trimmed := strings.TrimSpace(p.Name)
	if trimmed == "" {
		return nil, errors.New("卡名称不能为空")
	}
	if len(trimmed) > 32 {
		return nil, fmt.Errorf("卡名称过长（最多 32 字），得到 %d 字", len(trimmed))
	}
	if p.Type != CardTypeStoredValue && p.Type != CardTypeCount {
		return nil, fmt.Errorf("type 必须是 stored_value 或 count，得到 %q", p.Type)
	}
	if p.PriceCents <= 0 {
		return nil, fmt.Errorf("实付金额必须 > 0，得到 %d", p.PriceCents)
	}
	if p.ValidDays < 0 {
		return nil, fmt.Errorf("valid_days 不能为负，得到 %d", p.ValidDays)
	}
	if len(p.Note) > 256 {
		return nil, fmt.Errorf("描述过长（最多 256 字），得到 %d 字", len(p.Note))
	}

	// 类型分支校验
	if p.Type == CardTypeStoredValue {
		if p.FaceValueCents <= 0 {
			return nil, fmt.Errorf("储值卡面值必须 > 0，得到 %d", p.FaceValueCents)
		}
		if p.BonusCents < 0 {
			return nil, fmt.Errorf("赠送金额不能为负，得到 %d", p.BonusCents)
		}
		if p.FaceValueCents+p.BonusCents < p.PriceCents {
			// ponytail: 业务上"实付 2000 拿到 1800 余额"等于反向收费，禁掉
			return nil, fmt.Errorf("实付 %d 分 > 总到账 %d 分，请检查面值/赠送配置",
				p.PriceCents, p.FaceValueCents+p.BonusCents)
		}
	} else { // count
		if p.ServiceID == "" {
			return nil, errors.New("次卡必须关联服务 (service_id)")
		}
		if p.TotalCount <= 0 {
			return nil, fmt.Errorf("次卡次数必须 > 0，得到 %d", p.TotalCount)
		}
		// 校验 service 确实属于本 shop（避免把别店的 service 挂到本店的卡上）
		var svc Service
		if err := DB.WithContext(ctx).Where("id = ? AND shop_id = ?", p.ServiceID, p.ShopID).First(&svc).Error; err != nil {
			return nil, fmt.Errorf("service_id %q 在本店不存在", p.ServiceID)
		}
	}

	// 预检查：同店同名
	var existing Card
	if err := DB.WithContext(ctx).
		Where("shop_id = ? AND name = ?", p.ShopID, trimmed).
		First(&existing).Error; err == nil {
		return nil, fmt.Errorf("%w：%s", ErrCardNameTaken, trimmed)
	}

	now := time.Now()
	c := &Card{
		ID:             uuid.NewString(),
		ShopID:         p.ShopID,
		Name:           trimmed,
		Type:           p.Type,
		Status:         CardStatusActive,
		Note:           strings.TrimSpace(p.Note),
		PriceCents:     p.PriceCents,
		FaceValueCents: p.FaceValueCents,
		BonusCents:     p.BonusCents,
		ServiceID:      p.ServiceID,
		ServiceName:    p.ServiceName,
		TotalCount:     p.TotalCount,
		ValidDays:      p.ValidDays,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	// ServiceName 兜底：从 service 表读最新名字（用户没填时）
	if p.Type == CardTypeCount && c.ServiceName == "" {
		_ = DB.WithContext(ctx).Model(&Service{}).
			Where("id = ?", p.ServiceID).
			Select("name").Scan(&c.ServiceName).Error
	}

	if err := DB.WithContext(ctx).Create(c).Error; err != nil {
		if isUniqueConstraintErr(err) {
			return nil, fmt.Errorf("%w：%s", ErrCardNameTaken, trimmed)
		}
		return nil, err
	}
	return c, nil
}

// GetCardInShop 按 ID 查 card，校验属于指定 shop
func GetCardInShop(ctx context.Context, shopID, cardID string) (*Card, error) {
	if DB == nil {
		return nil, errors.New("DB 未初始化")
	}
	if shopID == "" || cardID == "" {
		return nil, errors.New("shop_id 和 card_id 必填")
	}
	var c Card
	if err := DB.WithContext(ctx).Where("id = ?", cardID).First(&c).Error; err != nil {
		return nil, ErrCardNotFoundInShop
	}
	if c.ShopID != shopID {
		return nil, ErrCardNotFoundInShop
	}
	return &c, nil
}

// ListCardsByShop 列某店所有 card
//
// includeArchived=false：只列 active（默认视图）
// includeArchived=true：含 archived（管理视图）
// 排序：type ASC（储值在前）, name ASC
func ListCardsByShop(ctx context.Context, shopID string, includeArchived bool) ([]Card, error) {
	if DB == nil {
		return nil, errors.New("DB 未初始化")
	}
	if shopID == "" {
		return nil, errors.New("shop_id 必填")
	}
	q := DB.WithContext(ctx).Where("shop_id = ?", shopID)
	if !includeArchived {
		q = q.Where("status = ?", CardStatusActive)
	}
	var out []Card
	if err := q.Order("type asc, name asc").Find(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

// UpdateCardParams 更新卡产品入参（nil 字段 = 不改）
type UpdateCardParams struct {
	Name     *string
	Note     *string
	PriceCents *int
	FaceValueCents *int // stored_value
	BonusCents *int     // stored_value
	TotalCount *int     // count
	ValidDays *int
}

// UpdateCard 改 card（只能改非业务关键字段；type / service_id 不可改，避免历史 customer_card 数据混乱）
func UpdateCard(ctx context.Context, shopID, cardID string, p UpdateCardParams) (*Card, error) {
	c, err := GetCardInShop(ctx, shopID, cardID)
	if err != nil {
		return nil, err
	}

	updates := map[string]interface{}{"updated_at": time.Now()}

	if p.Name != nil {
		trimmed := strings.TrimSpace(*p.Name)
		if trimmed == "" {
			return nil, errors.New("卡名称不能为空")
		}
		if len(trimmed) > 32 {
			return nil, fmt.Errorf("卡名称过长（最多 32 字），得到 %d 字", len(trimmed))
		}
		// 同店其他同名检测
		var dup Card
		if err := DB.WithContext(ctx).
			Where("shop_id = ? AND name = ? AND id <> ?", shopID, trimmed, c.ID).
			First(&dup).Error; err == nil {
			return nil, fmt.Errorf("%w：%s", ErrCardNameTaken, trimmed)
		}
		updates["name"] = trimmed
	}
	if p.Note != nil {
		if len(*p.Note) > 256 {
			return nil, fmt.Errorf("描述过长（最多 256 字），得到 %d 字", len(*p.Note))
		}
		updates["note"] = strings.TrimSpace(*p.Note)
	}
	if p.PriceCents != nil {
		if *p.PriceCents <= 0 {
			return nil, fmt.Errorf("实付金额必须 > 0，得到 %d", *p.PriceCents)
		}
		updates["price_cents"] = *p.PriceCents
	}
	if p.ValidDays != nil {
		if *p.ValidDays < 0 {
			return nil, fmt.Errorf("valid_days 不能为负，得到 %d", *p.ValidDays)
		}
		updates["valid_days"] = *p.ValidDays
	}

	if c.Type == CardTypeStoredValue {
		if p.FaceValueCents != nil {
			if *p.FaceValueCents <= 0 {
				return nil, fmt.Errorf("储值卡面值必须 > 0，得到 %d", *p.FaceValueCents)
			}
			updates["face_value_cents"] = *p.FaceValueCents
		}
		if p.BonusCents != nil {
			if *p.BonusCents < 0 {
				return nil, fmt.Errorf("赠送金额不能为负，得到 %d", *p.BonusCents)
			}
			updates["bonus_cents"] = *p.BonusCents
		}
		// 重新校验：面值 + 赠送 >= 实付（用最新值）
		newFace := c.FaceValueCents
		if p.FaceValueCents != nil {
			newFace = *p.FaceValueCents
		}
		newBonus := c.BonusCents
		if p.BonusCents != nil {
			newBonus = *p.BonusCents
		}
		newPrice := c.PriceCents
		if p.PriceCents != nil {
			newPrice = *p.PriceCents
		}
		if newFace+newBonus < newPrice {
			return nil, fmt.Errorf("实付 %d 分 > 总到账 %d 分，请检查面值/赠送配置",
				newPrice, newFace+newBonus)
		}
	} else { // count
		if p.TotalCount != nil {
			if *p.TotalCount <= 0 {
				return nil, fmt.Errorf("次卡次数必须 > 0，得到 %d", *p.TotalCount)
			}
			updates["total_count"] = *p.TotalCount
		}
	}

	if len(updates) == 1 { // 只有 updated_at
		return c, nil
	}
	if err := DB.WithContext(ctx).Model(c).Updates(updates).Error; err != nil {
		if isUniqueConstraintErr(err) {
			return nil, fmt.Errorf("%w：%s", ErrCardNameTaken, updates["name"])
		}
		return nil, err
	}
	return GetCardInShop(ctx, shopID, cardID)
}

// ArchiveCard 软下架 card
//
// 兜底：如果还有 active 状态的 customer_card，禁下架（避免突然失效造成顾客投诉）
// 异常路径：商户需要先把顾客卡归档/耗尽才能下架产品
func ArchiveCard(ctx context.Context, shopID, cardID string) error {
	c, err := GetCardInShop(ctx, shopID, cardID)
	if err != nil {
		return err
	}
	if c.Status == CardStatusArchived {
		return nil // 幂等
	}
	var n int64
	if err := DB.WithContext(ctx).Model(&CustomerCard{}).
		Where("shop_id = ? AND card_id = ? AND status = ?", shopID, cardID, CustomerCardStatusActive).
		Count(&n).Error; err != nil {
		return err
	}
	if n > 0 {
		return fmt.Errorf("%w：仍有 %d 张未用完的顾客卡，请先归档/耗尽", ErrCardHasActiveInstances, n)
	}
	return DB.WithContext(ctx).Model(c).Updates(map[string]interface{}{
		"status":     CardStatusArchived,
		"updated_at": time.Now(),
	}).Error
}

// ActivateCard 重新上架
func ActivateCard(ctx context.Context, shopID, cardID string) error {
	c, err := GetCardInShop(ctx, shopID, cardID)
	if err != nil {
		return err
	}
	if c.Status == CardStatusActive {
		return nil
	}
	return DB.WithContext(ctx).Model(c).Updates(map[string]interface{}{
		"status":     CardStatusActive,
		"updated_at": time.Now(),
	}).Error
}

// CountCardsByShop 统计某店 card 数（plan gate / dashboard 用）
func CountCardsByShop(ctx context.Context, shopID string) int64 {
	if DB == nil {
		return 0
	}
	var n int64
	DB.WithContext(ctx).Model(&Card{}).
		Where("shop_id = ? AND status = ?", shopID, CardStatusActive).
		Count(&n)
	return n
}