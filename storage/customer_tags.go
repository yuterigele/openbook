package storage

import (
	"context"
	"strings"
	"time"

	"gorm.io/gorm"
)

// Tag 顾客标签枚举（PRD §4.2 旗舰版）
const (
	TagVIP       = "VIP"       // 重要客户，优先排班、专属客服
	TagFrequent  = "FREQUENT"  // 常客（累计到店 ≥ 5 次）
	TagBlacklist = "BLACKLIST" // 黑名单（拒绝服务）
	TagNew       = "NEW"       // 新客户（首次到店后标记）
)

// TagSet 标签集合（不重复）
type TagSet struct {
	tags map[string]bool
}

// NewTagSet 从字符串（逗号分隔）解析
func NewTagSet(s string) *TagSet {
	t := &TagSet{tags: make(map[string]bool)}
	for _, tag := range strings.Split(s, ",") {
		tag = strings.TrimSpace(tag)
		if tag != "" {
			t.tags[tag] = true
		}
	}
	return t
}

// Has 判断是否有某标签
func (t *TagSet) Has(tag string) bool {
	if t == nil {
		return false
	}
	return t.tags[tag]
}

// Add 加标签
func (t *TagSet) Add(tag string) {
	if t.tags == nil {
		t.tags = make(map[string]bool)
	}
	t.tags[tag] = true
}

// Remove 删标签
func (t *TagSet) Remove(tag string) {
	delete(t.tags, tag)
}

// String 序列化为逗号分隔字符串
func (t *TagSet) String() string {
	if t == nil || len(t.tags) == 0 {
		return ""
	}
	out := make([]string, 0, len(t.tags))
	for k := range t.tags {
		out = append(out, k)
	}
	return strings.Join(out, ",")
}

// IsBlacklisted 是否在黑名单
func (c *Customer) IsBlacklisted() bool {
	return NewTagSet(c.Tags).Has(TagBlacklist)
}

// IsVIP 是否 VIP
func (c *Customer) IsVIP() bool {
	return NewTagSet(c.Tags).Has(TagVIP)
}

// IsFrequent 是否常客（标签）
func (c *Customer) IsFrequent() bool {
	return NewTagSet(c.Tags).Has(TagFrequent)
}

// AddCustomerTag 给顾客加标签（持久化；标签已存在则跳过）
func AddCustomerTag(ctx context.Context, customerID, tag string) error {
	if DB == nil {
		return nil
	}
	var c Customer
	if err := DB.WithContext(ctx).Where("id = ?", customerID).First(&c).Error; err != nil {
		return err
	}
	ts := NewTagSet(c.Tags)
	if ts.Has(tag) {
		return nil
	}
	ts.Add(tag)
	return DB.WithContext(ctx).Model(&c).Updates(map[string]interface{}{
		"tags":       ts.String(),
		"updated_at": time.Now(),
	}).Error
}

// RemoveCustomerTag 删标签
func RemoveCustomerTag(ctx context.Context, customerID, tag string) error {
	if DB == nil {
		return nil
	}
	var c Customer
	if err := DB.WithContext(ctx).Where("id = ?", customerID).First(&c).Error; err != nil {
		return err
	}
	ts := NewTagSet(c.Tags)
	if !ts.Has(tag) {
		return nil
	}
	ts.Remove(tag)
	return DB.WithContext(ctx).Model(&c).Updates(map[string]interface{}{
		"tags":       ts.String(),
		"updated_at": time.Now(),
	}).Error
}

// 工具函数：checkBlacklist 由 repo.go 复用
//
// 设计决策：黑名单是按顾客维度（不是按店铺维度）—— 一个顾客在任何店都黑名单。
// Customer 模型没有 shop_id 字段，所以不能用 shopID 过滤。
// shopID 参数保留（兼容 call site），但不在 SQL 端使用。
func isCustomerBlacklistedByTx(tx *gorm.DB, customer, shopID string) bool {
	_ = shopID // 黑名单是跨店的；保留参数仅为兼容调用方
	if customer == "" {
		return false
	}
	var rows []Customer
	if err := tx.Where("tags LIKE ?", "%BLACKLIST%").Find(&rows).Error; err != nil {
		return false
	}
	for _, c := range rows {
		if (c.Phone != "" && c.Phone == customer) || (c.Name != "" && c.Name == customer) {
			return true
		}
	}
	return false
}