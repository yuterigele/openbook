package storage

// plan_test.go —— plan 元数据完整性测试（v4.12）
//
// 防 v4.7/v4.10.1 那类"加新 perm 不加矩阵"漂：加 plan / feature 时
// 这里要全 PASS 才算注册完。

import (
	"testing"
)

// TestPlanRegistryComplete —— 4 档 plan 必须全有，且字段合法
func TestPlanRegistryComplete(t *testing.T) {
	for _, id := range AllPlanIDs {
		p, ok := PlanRegistry[id]
		if !ok {
			t.Errorf("AllPlanIDs 含 %q 但 PlanRegistry 没注册", id)
			continue
		}
		if p.ID != id {
			t.Errorf("plan %q 的 ID 字段 = %q，不一致", id, p.ID)
		}
		if p.Name == "" {
			t.Errorf("plan %q Name 为空", id)
		}
		if p.Currency == "" {
			t.Errorf("plan %q Currency 为空", id)
		}
		if p.Note == "" {
			t.Errorf("plan %q Note 为空（UI 需要描述）", id)
		}
		// 价格：除 enterprise 外必须 > 0
		if id != PlanEnterprise && p.PriceCents <= 0 {
			t.Errorf("plan %q PriceCents = %d，期望 > 0（enterprise 除外）", id, p.PriceCents)
		}
		// 限额：>= -1（-1 = 不限）；不允许 0（"什么都不允许"语义太危险）
		if p.MaxShops < -1 || p.MaxShops == 0 {
			t.Errorf("plan %q MaxShops = %d，期望 >= 1 或 -1（不限）", id, p.MaxShops)
		}
		if p.MaxBarbers < -1 || p.MaxBarbers == 0 {
			t.Errorf("plan %q MaxBarbers = %d，期望 >= 1 或 -1（不限）", id, p.MaxBarbers)
		}
		// 限额应该随价格递增（basic 最少，flagship / enterprise 最多）
		// 简单的 sanity 检查
		if id == PlanBasic && p.MaxBarbers > 5 {
			t.Errorf("basic plan MaxBarbers = %d 太大（应该 < 5）", p.MaxBarbers)
		}
		if id == PlanFlagship && p.MaxBarbers != -1 {
			t.Errorf("flagship plan MaxBarbers = %d，期望 -1（不限）", p.MaxBarbers)
		}
	}
}

// TestPlanOrder —— AllPlanIDs 按价格升序（UI 渲染对比页用）
func TestPlanOrder(t *testing.T) {
	if len(AllPlanIDs) != len(PlanRegistry) {
		t.Errorf("AllPlanIDs 数量 %d != PlanRegistry 数量 %d", len(AllPlanIDs), len(PlanRegistry))
	}
	for i := 0; i < len(AllPlanIDs)-1; i++ {
		cur := PlanRegistry[AllPlanIDs[i]]
		next := PlanRegistry[AllPlanIDs[i+1]]
		// enterprise (0) 排最后不算；非 enterprise 应该价格升序
		if cur.ID == PlanEnterprise || next.ID == PlanEnterprise {
			continue
		}
		if cur.PriceCents >= next.PriceCents {
			t.Errorf("AllPlanIDs 价格不升序: %s(%d) >= %s(%d)",
				cur.ID, cur.PriceCents, next.ID, next.PriceCents)
		}
	}
}

// TestIsValidPlanID —— 白名单校验
func TestIsValidPlanID(t *testing.T) {
	cases := []struct {
		plan string
		want bool
	}{
		{PlanBasic, true},
		{PlanPro, true},
		{PlanFlagship, true},
		{PlanEnterprise, true},
		{"", false},
		{"foo", false},
		{"BASIC", false}, // 大小写敏感
		{"pro ", false},  // 空格敏感
	}
	for _, tc := range cases {
		if got := IsValidPlanID(tc.plan); got != tc.want {
			t.Errorf("IsValidPlanID(%q) = %v, want %v", tc.plan, got, tc.want)
		}
	}
}

// TestHasFeature —— 各 plan feature 检查
func TestHasFeature(t *testing.T) {
	cases := []struct {
		plan    string
		feature string
		want    bool
	}{
		// basic：无任何 feature
		{PlanBasic, FeatureDataExport, false},
		{PlanBasic, FeatureAPIAccess, false},
		{PlanBasic, FeatureMultiStore, false},
		// pro：data_export
		{PlanPro, FeatureDataExport, true},
		{PlanPro, FeatureAPIAccess, false},
		{PlanPro, FeatureMultiStore, false},
		// flagship：data + api + multi + custom_report
		{PlanFlagship, FeatureDataExport, true},
		{PlanFlagship, FeatureAPIAccess, true},
		{PlanFlagship, FeatureMultiStore, true},
		{PlanFlagship, FeatureCustomReport, true},
		{PlanFlagship, FeaturePrioritySupport, false},
		// enterprise：全开
		{PlanEnterprise, FeatureDataExport, true},
		{PlanEnterprise, FeatureAPIAccess, true},
		{PlanEnterprise, FeaturePrioritySupport, true},
		{PlanEnterprise, FeatureSLAGuarantee, true},
		// 不存在的 plan
		{"fake", FeatureDataExport, false},
	}
	for _, tc := range cases {
		if got := HasFeature(tc.plan, tc.feature); got != tc.want {
			t.Errorf("HasFeature(%q, %q) = %v, want %v", tc.plan, tc.feature, got, tc.want)
		}
	}
}

// TestPlanLimitInt —— 限额查询
func TestPlanLimitInt(t *testing.T) {
	cases := []struct {
		plan string
		key  string
		want int
	}{
		// shops
		{PlanBasic, "shops", 1},
		{PlanPro, "shops", 1},
		{PlanFlagship, "shops", 5},
		{PlanEnterprise, "shops", -1},
		// barbers
		{PlanBasic, "barbers", 3},
		{PlanPro, "barbers", 10},
		{PlanFlagship, "barbers", -1},
		{PlanEnterprise, "barbers", -1},
		// 未知 key
		{PlanBasic, "unknown", 0},
		// 未知 plan
		{"fake", "shops", 0},
		{"fake", "barbers", 0},
	}
	for _, tc := range cases {
		if got := PlanLimitInt(tc.plan, tc.key); got != tc.want {
			t.Errorf("PlanLimitInt(%q, %q) = %d, want %d", tc.plan, tc.key, got, tc.want)
		}
	}
}

// TestFeatureConstantsKnown —— 所有已知 feature 都在某 plan 里（防 dead code）
func TestFeatureConstantsKnown(t *testing.T) {
	knownFeatures := map[string]bool{
		FeatureDataExport:      false,
		FeatureAPIAccess:       false,
		FeatureMultiStore:      false,
		FeaturePrioritySupport: false,
		FeatureCustomReport:    false,
		FeatureSLAGuarantee:    false,
	}
	for _, p := range PlanRegistry {
		for _, f := range p.Features {
			if _, ok := knownFeatures[f]; ok {
				knownFeatures[f] = true
			} else {
				t.Errorf("plan %q 含未知 feature: %s（FeatureXxx 常量没声明？）", p.ID, f)
			}
		}
	}
	for f, found := range knownFeatures {
		if !found {
			t.Errorf("feature %s 没有任何 plan 用——可能 dead code", f)
		}
	}
}
