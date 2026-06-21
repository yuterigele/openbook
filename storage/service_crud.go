package storage

// service_crud.go
//
// 服务目录 CRUD（v4.4 商户后台 — 服务管理）
//
// 设计要点：
//   - 多店隔离：所有方法必须传 shopID
//   - 软删除：IsActive=false 代替真删（保留历史预约的 service 名可追溯）
//   - 排序：按 sort_order ASC, id ASC
//   - 默认服务：seedDefaultServices 在 InitDB 后跑一次（如果该店还没有任何 service）
//
// 边界：
//   - name 必填、1-32 字、trim 后非空
//   - estimated_min 必须在 [1, 480] 区间
//   - price_range 长度上限 64

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// ErrServiceNameTaken 同店已存在同名 service（active 或 inactive 都算）
var ErrServiceNameTaken = errors.New("service name already taken")

// ErrServiceNotFoundInShop 在指定店铺中未找到该 service
var ErrServiceNotFoundInShop = errors.New("service not found in this shop")

// CreateService 创建服务项目
//
// 入参：shopID（必填）、name（必填，会 trim）、estimatedMin（>0，<480）、priceRange（选填，<=64）
// 返回：创建好的 *Service；ErrServiceNameTaken 如果重名；其他错误为 DB 异常
func CreateService(ctx context.Context, shopID, name string, estimatedMin int, priceRange string) (*Service, error) {
	if DB == nil {
		return nil, errors.New("DB 未初始化")
	}
	if shopID == "" {
		return nil, errors.New("shop_id 必填")
	}
	trimmed := trimServiceName(name)
	if trimmed == "" {
		return nil, errors.New("服务名称不能为空")
	}
	if len(trimmed) > 32 {
		return nil, fmt.Errorf("服务名称过长（最多 32 字），得到 %d 字", len(trimmed))
	}
	if estimatedMin <= 0 || estimatedMin > 480 {
		return nil, fmt.Errorf("预估时长必须在 1-480 分钟之间，得到 %d", estimatedMin)
	}
	if len(priceRange) > 64 {
		return nil, fmt.Errorf("价格区间过长（最多 64 字），得到 %d 字", len(priceRange))
	}

	// 预检查：同店同名
	var existing Service
	if err := DB.WithContext(ctx).
		Where("shop_id = ? AND name = ?", shopID, trimmed).
		First(&existing).Error; err == nil {
		return nil, fmt.Errorf("%w：%s", ErrServiceNameTaken, trimmed)
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}

	// 自动算 sort_order：当前 max+1
	var maxOrder int
	DB.WithContext(ctx).Model(&Service{}).
		Where("shop_id = ?", shopID).
		Select("COALESCE(MAX(sort_order), 0)").Scan(&maxOrder)

	now := time.Now()
	s := &Service{
		ID:           newServiceID(),
		ShopID:       shopID,
		Name:         trimmed,
		EstimatedMin: estimatedMin,
		PriceRange:   priceRange,
		IsActive:     true,
		SortOrder:    maxOrder + 10,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := DB.WithContext(ctx).Create(s).Error; err != nil {
		if isUniqueConstraintErr(err) {
			return nil, fmt.Errorf("%w：%s", ErrServiceNameTaken, trimmed)
		}
		return nil, err
	}
	return s, nil
}

// GetServiceInShop 按 ID 查 service，校验属于指定 shop
func GetServiceInShop(ctx context.Context, shopID, serviceID string) (*Service, error) {
	if DB == nil {
		return nil, errors.New("DB 未初始化")
	}
	if shopID == "" || serviceID == "" {
		return nil, errors.New("shop_id 和 service_id 必填")
	}
	var s Service
	if err := DB.WithContext(ctx).Where("id = ?", serviceID).First(&s).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrServiceNotFoundInShop
		}
		return nil, err
	}
	if s.ShopID != shopID {
		return nil, ErrServiceNotFoundInShop
	}
	return &s, nil
}

// ListServicesByShop 列某店所有 service
//
// includeInactive=false：只列 active（用于"服务目录管理"展示默认视图）
// includeInactive=true：含 inactive（用于后台完整管理）
func ListServicesByShop(ctx context.Context, shopID string, includeInactive bool) ([]Service, error) {
	if DB == nil {
		return nil, errors.New("DB 未初始化")
	}
	if shopID == "" {
		return nil, errors.New("shop_id 必填")
	}
	q := DB.WithContext(ctx).Where("shop_id = ?", shopID)
	if !includeInactive {
		q = q.Where("is_active = ?", true)
	}
	var out []Service
	if err := q.Order("sort_order asc, id asc").Find(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

// UpdateService 更新服务（name / estimated_min / price_range / sort_order）
func UpdateService(ctx context.Context, shopID, serviceID, name string, estimatedMin int, priceRange string, sortOrder int) (*Service, error) {
	s, err := GetServiceInShop(ctx, shopID, serviceID)
	if err != nil {
		return nil, err
	}
	trimmed := trimServiceName(name)
	if trimmed == "" {
		return nil, errors.New("服务名称不能为空")
	}
	if len(trimmed) > 32 {
		return nil, fmt.Errorf("服务名称过长（最多 32 字），得到 %d 字", len(trimmed))
	}
	if estimatedMin <= 0 || estimatedMin > 480 {
		return nil, fmt.Errorf("预估时长必须在 1-480 分钟之间，得到 %d", estimatedMin)
	}
	if len(priceRange) > 64 {
		return nil, fmt.Errorf("价格区间过长（最多 64 字），得到 %d 字", len(priceRange))
	}
	// 同店其他同名检测
	var dup Service
	if err := DB.WithContext(ctx).
		Where("shop_id = ? AND name = ? AND id <> ?", shopID, trimmed, s.ID).
		First(&dup).Error; err == nil {
		return nil, fmt.Errorf("%w：%s", ErrServiceNameTaken, trimmed)
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}

	updates := map[string]interface{}{
		"name":          trimmed,
		"estimated_min": estimatedMin,
		"price_range":   priceRange,
		"sort_order":    sortOrder,
		"updated_at":    time.Now(),
	}
	if err := DB.WithContext(ctx).Model(s).Updates(updates).Error; err != nil {
		return nil, err
	}
	// 重新读
	return GetServiceInShop(ctx, shopID, serviceID)
}

// DeactivateService 软下架（IsActive=false）。幂等。
func DeactivateService(ctx context.Context, shopID, serviceID string) error {
	s, err := GetServiceInShop(ctx, shopID, serviceID)
	if err != nil {
		return err
	}
	if !s.IsActive {
		return nil
	}
	return DB.WithContext(ctx).Model(s).Updates(map[string]interface{}{
		"is_active":  false,
		"updated_at": time.Now(),
	}).Error
}

// ActivateService 重新上架
func ActivateService(ctx context.Context, shopID, serviceID string) error {
	s, err := GetServiceInShop(ctx, shopID, serviceID)
	if err != nil {
		return err
	}
	if s.IsActive {
		return nil
	}
	return DB.WithContext(ctx).Model(s).Updates(map[string]interface{}{
		"is_active":  true,
		"updated_at": time.Now(),
	}).Error
}

// ---- helpers ----

func trimServiceName(s string) string {
	return strings.TrimSpace(s)
}

// newServiceID 生成 service ID（UUID v4，与其他表风格一致）
func newServiceID() string {
	return uuid.NewString()
}

// CountServices 统计某店 service 数（用于"是否需要 seed"判断）
func CountServices(ctx context.Context, shopID string) int64 {
	if DB == nil {
		return 0
	}
	var n int64
	DB.WithContext(ctx).Model(&Service{}).Where("shop_id = ?", shopID).Count(&n)
	return n
}
