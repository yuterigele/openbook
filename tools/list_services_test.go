package tools

// list_services_test.go
//
// v4.4 补全测试 — PRD §4.1 list_services 工具

import (
	"context"
	"strings"
	"testing"

	"github.com/cloudwego/eino-examples/quickstart/chatwitheino/storage"
)

func TestListServicesTool_InfoMentionsServices(t *testing.T) {
	c := &ListServicesTool{}
	info, err := c.Info(context.Background())
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if !strings.Contains(info.Desc, "服务") {
		t.Errorf("Info.Desc 应提到 '服务'，got %q", info.Desc)
	}
}

func TestListServices_EmptyShop(t *testing.T) {
	setupToolsTestDB(t)
	shop := storage.MakeShop(t, "svc-empty", "")

	ctx := WithShopID(context.Background(), shop.ID)
	c := &ListServicesTool{}
	out, err := c.InvokableRun(ctx, `{}`)
	if err != nil {
		t.Fatalf("空店应不报错: %v", err)
	}
	if !strings.Contains(out, "暂未配置") {
		t.Errorf("空店应返回友好提示: %s", out)
	}
}

func TestListServices_ActiveOnly(t *testing.T) {
	setupToolsTestDB(t)
	shop := storage.MakeShop(t, "svc-active", "")
	storage.CreateService(context.Background(), shop.ID, "剪发", 30, "30-50")
	storage.CreateService(context.Background(), shop.ID, "烫发", 90, "180-380")
	d, _ := storage.CreateService(context.Background(), shop.ID, "下架项", 30, "")
	storage.DeactivateService(context.Background(), shop.ID, d.ID)

	ctx := WithShopID(context.Background(), shop.ID)
	c := &ListServicesTool{}
	out, err := c.InvokableRun(ctx, `{}`)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !strings.Contains(out, "剪发") || !strings.Contains(out, "烫发") {
		t.Errorf("应列出 active 服务: %s", out)
	}
	if strings.Contains(out, "下架项") {
		t.Errorf("下架项不应出现: %s", out)
	}
	if !strings.Contains(out, "30-50") {
		t.Errorf("应显示价格区间: %s", out)
	}
}

func TestListServices_NoShopID_Fallback(t *testing.T) {
	setupToolsTestDB(t)
	shop := storage.MakeShop(t, "svc-fallback", "")
	storage.CreateService(context.Background(), shop.ID, "剪发", 30, "30-50")

	// 不注入 shopID，应走 fallback 拿第一家有服务的店
	c := &ListServicesTool{}
	out, err := c.InvokableRun(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("fallback err: %v", err)
	}
	if !strings.Contains(out, "剪发") {
		t.Errorf("fallback 应拿到服务: %s", out)
	}
}
