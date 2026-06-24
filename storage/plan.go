package storage

// plan.go —— plan 元数据（v4.12 增量）
//
// 跟 storage/permissions.go 同模式：纯 Go 字典 + helper，运营在线调整走 storage 公开方法
// （暂未实装，v4.12 只读硬编码元数据）
//
// 4 档 plan（v4.12）：
//   - basic     ：99 元/月   1 店 / 3 barber / 无数据导出
//   - pro       ：299 元/月  1 店 / 10 barber / 数据导出（CSV）
//   - flagship  ：999 元/月  5 店 / 不限 barber / API access / 多店看板
//   - enterprise：定价谈   不限 / 不限 / 全功能 + 专属客服 + SLA
//
// 设计要点：
//   - Plan 字符串用 stable id（"basic" / "pro" / ...）— UI / DB / API 共享
//   - 价格用分（int）— 避免浮点；前端用元
//   - Limit 用 int（-1 = 不限）
//   - Feature 列表用 string slice（["data_export", "api_access", ...]）— UI 按这个渲染对比表
//   - 未来加 plan 时务必同时加这里 + 测（TestPlanRegistryComplete）
//   - 加新 feature 时：这里加 + 各 handler / middleware 加 gate

// PlanID 稳定字符串（前端 / DB / API 共享）
const (
	PlanBasic      = "basic"
	PlanPro        = "pro"
	PlanFlagship   = "flagship"
	PlanEnterprise = "enterprise"
)

// PlanFeature 能力名（"data_export" / "api_access" / "multi_store" / "priority_support"）
const (
	FeatureDataExport     = "data_export"
	FeatureAPIAccess      = "api_access"
	FeatureMultiStore     = "multi_store"
	FeaturePrioritySupport = "priority_support"
	FeatureCustomReport   = "custom_report"
	FeatureSLAGuarantee   = "sla_guarantee"
)

// PlanMeta 单档 plan 的元数据
type PlanMeta struct {
	ID         string   // basic / pro / flagship / enterprise
	Name       string   // 中文显示名（基础版 / 专业版 / 旗舰版 / 企业定制）
	PriceCents int      // 月价（分）。enterprise = 0（按需谈）
	Currency   string   // CNY / USD（v4.12 固定 CNY）
	MaxShops   int      // -1 = 不限
	MaxBarbers int      // 单店最多 barber 数。-1 = 不限
	Features   []string // FeatureXxx 常量列表
	Note       string   // 描述（UI 展示）
}

// PlanRegistry 所有 plan 的元数据（真理之源）
//
// 加新 plan：在这里加一行 + 跑 TestPlanRegistryComplete
var PlanRegistry = map[string]PlanMeta{
	PlanBasic: {
		ID:         PlanBasic,
		Name:       "基础版",
		PriceCents: 9900, // 99 元/月
		Currency:   "CNY",
		MaxShops:   1,
		MaxBarbers: 3,
		Features:   []string{},
		Note:       "适合小店试水。1 店 3 个理发师，无数据导出",
	},
	PlanPro: {
		ID:         PlanPro,
		Name:       "专业版",
		PriceCents: 29900, // 299 元/月
		Currency:   "CNY",
		MaxShops:   1,
		MaxBarbers: 10,
		Features:   []string{FeatureDataExport},
		Note:       "适合成长中的店。1 店 10 个理发师，CSV 数据导出",
	},
	PlanFlagship: {
		ID:         PlanFlagship,
		Name:       "旗舰版",
		PriceCents: 99900, // 999 元/月
		Currency:   "CNY",
		MaxShops:   5,
		MaxBarbers: -1, // 不限
		Features:   []string{FeatureDataExport, FeatureAPIAccess, FeatureMultiStore, FeatureCustomReport},
		Note:       "适合连锁店。最多 5 店，不限理发师数，API access + 定制报表",
	},
	PlanEnterprise: {
		ID:         PlanEnterprise,
		Name:       "企业定制",
		PriceCents: 0, // 按需谈
		Currency:   "CNY",
		MaxShops:   -1, // 不限
		MaxBarbers: -1, // 不限
		Features: []string{
			FeatureDataExport, FeatureAPIAccess, FeatureMultiStore,
			FeatureCustomReport, FeaturePrioritySupport, FeatureSLAGuarantee,
		},
		Note:       "适合大型连锁。联系商务谈价，含 SLA + 专属客服",
	},
}

// AllPlanIDs 所有已知 plan id（按价格升序）
//
// UI 用这个渲染 plan 对比页（按价格升序展示）
var AllPlanIDs = []string{PlanBasic, PlanPro, PlanFlagship, PlanEnterprise}

// IsValidPlanID 校验 plan id 已知
//
// renewSubscriptionHandler / Shop.Plan 写入 / UI 提交时都用这个兜底
func IsValidPlanID(planID string) bool {
	_, ok := PlanRegistry[planID]
	return ok
}

// GetPlan 取 plan 元数据（不存在返零值 + false）
func GetPlan(planID string) (PlanMeta, bool) {
	p, ok := PlanRegistry[planID]
	return p, ok
}

// HasFeature 检查 plan 是否含某 feature
//
// enterprise 默认全开；其他按 PlanRegistry.Features
func HasFeature(planID string, feature string) bool {
	p, ok := PlanRegistry[planID]
	if !ok {
		return false
	}
	for _, f := range p.Features {
		if f == feature {
			return true
		}
	}
	return false
}

// PlanLimitInt 取 plan 的限额（-1 = 不限）
//
// key: "shops" / "barbers"
// 不存在的 plan 返 0（"什么都不允许"，比不限更安全）
func PlanLimitInt(planID string, key string) int {
	p, ok := PlanRegistry[planID]
	if !ok {
		return 0
	}
	switch key {
	case "shops":
		return p.MaxShops
	case "barbers":
		return p.MaxBarbers
	}
	return 0
}

// DefaultPlanID 新店铺默认 plan（InitDB 时 shop.plan 没值 → basic）
const DefaultPlanID = PlanBasic
