package storage

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// ---- Shop ----

// FindShopByCorpID 按企业微信 CorpID 找 Shop（回调时用）
func FindShopByCorpID(ctx context.Context, corpID string) (*Shop, error) {
	if DB == nil {
		return nil, errors.New("DB 未初始化")
	}
	var s Shop
	if err := DB.WithContext(ctx).Where("wecom_corp_id = ?", corpID).First(&s).Error; err != nil {
		return nil, err
	}
	return &s, nil
}

// GetShopByID 按 ID 查
func GetShopByID(ctx context.Context, id string) (*Shop, error) {
	if DB == nil {
		return nil, errors.New("DB 未初始化")
	}
	var s Shop
	if err := DB.WithContext(ctx).Where("id = ?", id).First(&s).Error; err != nil {
		return nil, err
	}
	return &s, nil
}

// ---- ShopAdmin ----

// FindAdminByUsername 按用户名查（登录时用）
func FindAdminByUsername(ctx context.Context, username string) (*ShopAdmin, error) {
	if DB == nil {
		return nil, errors.New("DB 未初始化")
	}
	var a ShopAdmin
	if err := DB.WithContext(ctx).Where("username = ?", username).First(&a).Error; err != nil {
		return nil, err
	}
	return &a, nil
}

// VerifyAdminPassword 校验密码
func VerifyAdminPassword(admin *ShopAdmin, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(admin.PasswordHash), []byte(password)) == nil
}

// FindShopAdminByID 按 id 查 ShopAdmin（v4.12.1 用于改自己密码时校验旧密码）
func FindShopAdminByID(ctx context.Context, adminID uint64) (*ShopAdmin, error) {
	if DB == nil {
		return nil, errors.New("DB 未初始化")
	}
	var a ShopAdmin
	if err := DB.WithContext(ctx).Where("id = ?", adminID).First(&a).Error; err != nil {
		return nil, err
	}
	return &a, nil
}

// MarkAdminLogin 更新最后登录时间
func MarkAdminLogin(ctx context.Context, adminID uint64) {
	if DB == nil {
		return
	}
	now := time.Now()
	DB.WithContext(ctx).Model(&ShopAdmin{}).Where("id = ?", adminID).Update("last_login_at", now)
}

// UpdateAdminPassword 改密码（商户后台 /settings 用）
func UpdateAdminPassword(ctx context.Context, adminID uint64, newPassword string) error {
	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	return DB.WithContext(ctx).Model(&ShopAdmin{}).Where("id = ?", adminID).
		Updates(map[string]interface{}{
			"password_hash": string(hash),
			"updated_at":    time.Now(),
		}).Error
}

// ---- Shop scoped helpers ----

// ListBarbersByShop 列出某店所有 active 理发师
func ListBarbersByShop(ctx context.Context, shopID string) ([]Barber, error) {
	if DB == nil {
		return nil, errors.New("DB 未初始化")
	}
	var out []Barber
	err := DB.WithContext(ctx).Where("shop_id = ? AND active = ?", shopID, true).
		Order("name asc").Find(&out).Error
	return out, err
}

// IsShopHoliday 判断某天是否为店铺休息日
//
// Shop.Holidays 字段格式：逗号分隔的 YYYY-MM-DD，例如 "2026-10-01,2026-10-02,2026-10-03"
func IsShopHoliday(shop *Shop, date string) bool {
	if shop == nil || shop.Holidays == "" {
		return false
	}
	for _, d := range strings.Split(shop.Holidays, ",") {
		if strings.TrimSpace(d) == date {
			return true
		}
	}
	return false
}

// AllShopHolidays 返回某店所有节假日（解析后的 map[date]bool）
func AllShopHolidays(shop *Shop) map[string]bool {
	out := make(map[string]bool)
	if shop == nil || shop.Holidays == "" {
		return out
	}
	for _, d := range strings.Split(shop.Holidays, ",") {
		d = strings.TrimSpace(d)
		if d != "" {
			out[d] = true
		}
	}
	return out
}

// ---- multi_store: shop group（v4.12.1） ----
//
// "shop group" = 主店 (ParentShopID="") + 它的所有分店 (ParentShopID=主店ID)
//   - 用于 multi_store feature gate：plan 限额 = group 内 shop 数
//   - 用于 listShopsHandler：返当前 group 所有店
//
// 设计：自引用字段 ParentShopID，比"按 corp_id 分组"更可靠（corp_id 可能空字符串）

// RootShopID 返回某 shop 的主店 ID（自己就是主店时返回自己）
//
//   - shop.ParentShopID == "" → 是主店，返回 shop.ID
//   - shop.ParentShopID != "" → 是分店，返回 ParentShopID
func RootShopID(ctx context.Context, shop Shop) string {
	if shop.ParentShopID == "" {
		return shop.ID
	}
	return shop.ParentShopID
}

// CountShopsInGroup 统计某 shop 所在 group 的 shop 数（含自己 + 分店）
//
// 用于 multi_store plan gate：plan 限额按 group 总数算
//   - 主店：1 + COUNT WHERE parent_shop_id = 自己
//   - 分店：1（自己） + 1（主店） + COUNT WHERE parent_shop_id = 主店
func CountShopsInGroup(ctx context.Context, shopID string) (int, error) {
	if DB == nil {
		return 0, errors.New("storage.DB 未初始化")
	}
	if shopID == "" {
		return 0, errors.New("shop_id 空")
	}
	// 取自己 → 看是不是主店 → 拿 root_id → COUNT
	shop, err := GetShopByID(ctx, shopID)
	if err != nil {
		return 0, err
	}
	rootID := RootShopID(ctx, *shop)
	// root 自己是 1 个；分店是 COUNT(*) WHERE parent_shop_id = rootID
	var subCount int64
	if err := DB.WithContext(ctx).Model(&Shop{}).
		Where("parent_shop_id = ?", rootID).
		Count(&subCount).Error; err != nil {
		return 0, err
	}
	return 1 + int(subCount), nil
}

// ListShopsInGroup 列出某 shop 所在 group 的所有店（含主店 + 分店，按 id 排序）
func ListShopsInGroup(ctx context.Context, shopID string) ([]Shop, error) {
	if DB == nil {
		return nil, errors.New("storage.DB 未初始化")
	}
	if shopID == "" {
		return nil, errors.New("shop_id 空")
	}
	shop, err := GetShopByID(ctx, shopID)
	if err != nil {
		return nil, err
	}
	rootID := RootShopID(ctx, *shop)
	var out []Shop
	if err := DB.WithContext(ctx).
		Where("id = ? OR parent_shop_id = ?", rootID, rootID).
		Order("id asc").
		Find(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

// CreateSubsidiaryShop 在主店下建一个分店（v4.12.1）
//
//   - parentID：主店 ID（必须是主店，即 ParentShopID=""）
//   - name：分店名
//   - 其余字段：复用主店的 timezone / open_hour / plan / wecom_*
//   - 不创建分店 owner 账号（v4.13+；v4.12.1 店主用现有 /api/admin/members 手动建）
//
// 返回新 shop 的 ID。
func CreateSubsidiaryShop(ctx context.Context, parentID, name, address string) (*Shop, error) {
	if DB == nil {
		return nil, errors.New("storage.DB 未初始化")
	}
	parent, err := GetShopByID(ctx, parentID)
	if err != nil {
		return nil, fmt.Errorf("主店不存在: %w", err)
	}
	if parent.ParentShopID != "" {
		return nil, errors.New("parentID 必须为主店，不能是分店")
	}
	// 生成唯一 ID：parent_id-sub-<random>
	// 用 uuid 短码
	suffix, err := uuidString()
	if err != nil {
		return nil, err
	}
	newShop := &Shop{
		ID:           parentID + "-sub-" + suffix,
		Name:         name,
		Address:      address,
		Timezone:     parent.Timezone,
		OpenHour:     parent.OpenHour,
		CloseHour:    parent.CloseHour,
		LunchStart:   parent.LunchStart,
		LunchEnd:     parent.LunchEnd,
		LunchEndMin:  parent.LunchEndMin,
		Plan:         parent.Plan, // 分店继承主店 plan
		ExpiresAt:    parent.ExpiresAt,
		ParentShopID: parentID,
		Holidays:     "",
		WecomCorpID:  parent.WecomCorpID, // 同 corp（多店共用客服）
		OpenKfID:     parent.OpenKfID,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}
	if err := DB.WithContext(ctx).Create(newShop).Error; err != nil {
		return nil, err
	}
	return newShop, nil
}

// uuidString 生成短 UUID 字符串（用于分店 ID 后缀）
//
//   - 用 crypto/rand 避免同一时间创建多分店时撞 ID
//   - 长度 6（36^6 ≈ 60 亿组合，足够分店区分）
func uuidString() (string, error) {
	const charset = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	for i := range b {
		b[i] = charset[int(b[i])%len(charset)]
	}
	return string(b), nil
}

// ---- 确保 Barber 表 shop_id 有值（旧数据兜底） ----
//
// 旧版 seedBarbers 把 Barber.ShopID 写死成 "default"。
// 这里兜底：如果某 barber 的 shop_id 为空，按名字 fallback 给唯一一家店（单店 demo 用）。
func ensureBarberShopIDs(ctx context.Context) error {
	if DB == nil {
		return nil
	}
	var count int64
	DB.WithContext(ctx).Model(&Barber{}).Where("shop_id IS NULL OR shop_id = ''").Count(&count)
	if count == 0 {
		return nil
	}
	// 找唯一一家 shop（demo 模式）
	var shop Shop
	if err := DB.WithContext(ctx).Order("created_at asc").First(&shop).Error; err != nil {
		return err
	}
	return DB.WithContext(ctx).Model(&Barber{}).
		Where("shop_id IS NULL OR shop_id = ''").
		Update("shop_id", shop.ID).Error
}

// 兼容旧调用的 export：init 时跑一次
// （ensureBarberShopIDs 由 InitDB 显式调用，无需包级 init）