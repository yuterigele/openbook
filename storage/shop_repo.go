package storage

import (
	"context"
	"errors"
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