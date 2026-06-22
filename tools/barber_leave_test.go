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
	if !strings.Contains(out, "test leave") {
		t.Errorf("应显示原因: %s", out)
	}
	if !strings.Contains(out, "建议") {
		t.Errorf("应给建议: %s", out)
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
