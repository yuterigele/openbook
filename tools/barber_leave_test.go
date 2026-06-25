package tools

// barber_leave_test.go
//
// v4.4 补全测试 — PRD §4.1 barber_leave 工具

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/yuterigele/openbook/storage"
)

func TestBarberLeave_InfoMentionsReason(t *testing.T) {
	c := &BarberLeaveTool{}
	info, err := c.Info(context.Background())
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if !strings.Contains(info.Desc, "请假") {
		t.Errorf("Info.Desc 应提到 '请假'，got %q", info.Desc)
	}
}

func TestBarberLeave_NoLeave(t *testing.T) {
	setupToolsTestDB(t)
	shop := storage.MakeShop(t, "bl-none", "")
	storage.MakeBarber(t, "b-1", shop.ID, "Tony")

	c := &BarberLeaveTool{}
	out, err := c.InvokableRun(
		WithShopID(context.Background(), shop.ID),
		`{"barber_name":"Tony","date":"2026-07-01"}`)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !strings.Contains(out, "没有请假") {
		t.Errorf("无请假应明确说: %s", out)
	}
}

func TestBarberLeave_HasLeave(t *testing.T) {
	setupToolsTestDB(t)
	shop := storage.MakeShop(t, "bl-has", "")
	storage.MakeBarber(t, "b-1", shop.ID, "Tony")
	// 当天 14:00-16:00 请假
	loc, _ := time.LoadLocation("Asia/Shanghai")
	date := "2026-07-01"
	start, _ := time.ParseInLocation("2006-01-02 15:04", date+" 14:00", loc)
	end, _ := time.ParseInLocation("2006-01-02 15:04", date+" 16:00", loc)
	storage.MakeBarberLeave(t, shop.ID, "b-1", start, end, storage.LeaveActionCancel)

	c := &BarberLeaveTool{}
	out, err := c.InvokableRun(
		WithShopID(context.Background(), shop.ID),
		`{"barber_name":"Tony","date":"`+date+`"}`)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !strings.Contains(out, "14:00-16:00") {
		t.Errorf("应显示请假区间: %s", out)
	}
	// v4.13.0 隐私保护：硬编码"师傅临时有事"，不暴露内部 Reason
	if !strings.Contains(out, "师傅临时有事") {
		t.Errorf("应显示脱敏原因: %s", out)
	}
	if strings.Contains(out, "test leave") {
		t.Errorf("不应暴露内部 Reason（隐私泄漏）: %s", out)
	}
	if !strings.Contains(out, "建议") {
		t.Errorf("应给建议: %s", out)
	}
}

// TestBarberLeave_NeverExposesSensitiveReason 隐私回归测试
// v4.13.0 加：哪怕商户填的 Reason 是"痔疮手术"这种敏感字眼，输出也不能泄漏
func TestBarberLeave_NeverExposesSensitiveReason(t *testing.T) {
	setupToolsTestDB(t)
	shop := storage.MakeShop(t, "bl-privacy", "")
	storage.MakeBarber(t, "b-1", shop.ID, "Tony")
	loc, _ := time.LoadLocation("Asia/Shanghai")
	date := "2026-07-01"
	start, _ := time.ParseInLocation("2006-01-02 15:04", date+" 09:00", loc)
	end, _ := time.ParseInLocation("2006-01-02 15:04", date+" 18:00", loc)
	// 直接建一条带敏感 Reason 的 leave（绕开 MakeBarberLeave 工具方法）
	storage.DB.Create(&storage.BarberLeave{
		ID:     "leave-sensitive",
		ShopID: shop.ID,
		BarberID: "b-1",
		BarberName: "Tony",
		StartAt: start,
		EndAt:   end,
		Reason:  "痔疮手术",  // 模拟商户填了敏感字眼
		// v4.13.0：CustomerFacingReason 字段已删除
		Action:  storage.LeaveActionCancel,
		Status:  storage.LeaveStatusActive,
		CreatedBy: "test",
		CreatedAt: time.Now(),
	})

	c := &BarberLeaveTool{}
	out, err := c.InvokableRun(
		WithShopID(context.Background(), shop.ID),
		`{"barber_name":"Tony","date":"`+date+`"}`)
	if err != nil {
		t.Fatalf("err: %v", err)
	}

	// 硬保证：这两个敏感字眼绝不能出现在输出里
	sensitiveWords := []string{"痔疮", "手术", "产检", "老婆", "陪老婆"}
	for _, w := range sensitiveWords {
		if strings.Contains(out, w) {
			t.Errorf("隐私泄漏：输出含敏感词 %q\n完整输出: %s", w, out)
		}
	}
	// 必须显示统一的脱敏文案
	if !strings.Contains(out, "师傅临时有事") {
		t.Errorf("应显示脱敏文案 '师傅临时有事'，got: %s", out)
	}
}

func TestBarberLeave_BarberNotFound(t *testing.T) {
	setupToolsTestDB(t)
	shop := storage.MakeShop(t, "bl-nf", "")

	c := &BarberLeaveTool{}
	_, err := c.InvokableRun(
		WithShopID(context.Background(), shop.ID),
		`{"barber_name":"不存在","date":"2026-07-01"}`)
	if err == nil {
		t.Fatal("不存在的师傅应报错")
	}
	if !strings.Contains(err.Error(), "不在店里") {
		t.Errorf("应给友好兜底话术: %v", err)
	}
}

func TestBarberLeave_EmptyBarberName(t *testing.T) {
	setupToolsTestDB(t)
	c := &BarberLeaveTool{}
	_, err := c.InvokableRun(context.Background(), `{"barber_name":""}`)
	if err == nil {
		t.Fatal("空 barber_name 应报错")
	}
}
