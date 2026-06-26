package tools

// get_appointment_test.go
//
// 覆盖 v4.13.6 新工具的 4 类核心场景：
//   1. 正常查：返回完整字段（barber_name / date / time / service / status）
//   2. leave 改派后查：barber_name 已经是新师傅（v4.13.6 修这个场景的根因）
//   3. 不存在的 ID：友好错误
//   4. 取消过的预约：status=cancelled 带 cancel_reason
//   5. phone 字段**不**返回（隐私）
//   6. Info 描述里提到"改时间前必调"关键约束（防误删）

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/yuterigele/openbook/storage"
)

func TestGetAppointmentTool_InfoMentionsPreModify(t *testing.T) {
	info, err := (&GetAppointmentTool{}).Info(context.Background())
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if info.Name != "get_appointment" {
		t.Errorf("tool name should be 'get_appointment', got %q", info.Name)
	}
	// 关键约束不能漏（去掉 markdown ** 噪声再断言）
	desc := strings.ReplaceAll(info.Desc, "**", "")
	mustHave := []string{
		"改时间 / 取消前必调",           // 触发场景
		"history 里的 barber_name 可能是旧的", // 为什么
		"leave 改派后",                  // 真实场景
		"不返回 phone",                  // 隐私
	}
	for _, sub := range mustHave {
		if !strings.Contains(desc, sub) {
			t.Errorf("Info.Desc should mention %q (v4.13.6), got %q", sub, info.Desc)
		}
	}
}

func TestGetAppointment_HappyPath(t *testing.T) {
	setupToolsTestDB(t)
	shop := storage.MakeShop(t, "shop-1", "")
	storage.MakeBarber(t, "barber-Tony", shop.ID, "Tony")
	cust := storage.MakeCustomer(t, "Alice", 0, 0)
	appt := storage.MakeAppointment(t, shop.ID, cust.ID, "Alice", "Tony", "2026-06-26", "14:00")

	out, err := (&GetAppointmentTool{}).InvokableRun(
		context.Background(),
		`{"appointment_id":"`+appt.ID+`"}`,
	)
	if err != nil {
		t.Fatalf("InvokableRun: %v / %s", err, out)
	}

	// 必须含完整字段
	mustHave := []string{
		appt.ID,
		"Tony",
		"2026-06-26",
		"14:00",
		"active", // status
	}
	for _, sub := range mustHave {
		if !strings.Contains(out, sub) {
			t.Errorf("output should contain %q, got %q", sub, out)
		}
	}
	// 隐私：phone 不能出现（即使 alice 真的有 phone）
	if strings.Contains(out, cust.Phone) && cust.Phone != "" {
		t.Errorf("output should NOT contain phone (privacy), got %q", out)
	}
}

// v4.13.6 根因场景：leave 改派后 barber_name 已经从老王变成 Tony，
// Agent 调这个工具拿真实状态，决定怎么继续。
func TestGetAppointment_AfterLeaveReschedule_ReturnsNewBarber(t *testing.T) {
	setupToolsTestDB(t)
	shop := storage.MakeShop(t, "shop-1", "")
	storage.MakeBarber(t, "barber-Tony", shop.ID, "Tony")
	storage.MakeBarber(t, "barber-Kevin", shop.ID, "Kevin")
	cust := storage.MakeCustomer(t, "Alice", 0, 0)

	// 原本是 Tony 11:00
	appt := storage.MakeAppointment(t, shop.ID, cust.ID, "Alice", "Tony", "2026-06-26", "11:00")

	// 模拟 leave reschedule：把 barber 改成 Kevin
	if err := storage.DB.Model(appt).Updates(map[string]interface{}{
		"barber_id":   "barber-Kevin",
		"barber_name": "Kevin",
	}).Error; err != nil {
		t.Fatalf("simulate leave reschedule: %v", err)
	}

	out, err := (&GetAppointmentTool{}).InvokableRun(
		context.Background(),
		`{"appointment_id":"`+appt.ID+`"}`,
	)
	if err != nil {
		t.Fatalf("InvokableRun: %v / %s", err, out)
	}

	// 关键：barber_name 必须是新值 Kevin，不能是旧值 Tony
	if strings.Contains(out, "理发师：Tony") {
		t.Errorf("output should show CURRENT barber (Kevin), not stale (Tony), got %q", out)
	}
	if !strings.Contains(out, "理发师：Kevin") {
		t.Errorf("output should show '理发师：Kevin', got %q", out)
	}
}

func TestGetAppointment_NotFound_ReturnsError(t *testing.T) {
	setupToolsTestDB(t)
	_, err := (&GetAppointmentTool{}).InvokableRun(
		context.Background(),
		`{"appointment_id":"nonexistent-id"}`,
	)
	if err == nil {
		t.Fatalf("expected error for nonexistent appt, got nil")
	}
	if !strings.Contains(err.Error(), "找不到") {
		t.Errorf("error should be friendly '找不到', got %q", err.Error())
	}
}

func TestGetAppointment_EmptyID_ReturnsError(t *testing.T) {
	setupToolsTestDB(t)
	_, err := (&GetAppointmentTool{}).InvokableRun(
		context.Background(),
		`{"appointment_id":""}`,
	)
	if err == nil {
		t.Fatalf("expected error for empty id, got nil")
	}
}

func TestGetAppointment_Cancelled_ShowsReason(t *testing.T) {
	setupToolsTestDB(t)
	shop := storage.MakeShop(t, "shop-1", "")
	storage.MakeBarber(t, "barber-Tony", shop.ID, "Tony")
	cust := storage.MakeCustomer(t, "Alice", 0, 0)
	appt := storage.MakeAppointment(t, shop.ID, cust.ID, "Alice", "Tony", "2026-06-26", "14:00")

	// 模拟取消（admin 取消 + 原因）
	if err := storage.DB.Model(appt).Updates(map[string]interface{}{
		"status":        "cancelled",
		"cancel_reason": "理发师请假：临时有事",
		"updated_at":    time.Now(),
	}).Error; err != nil {
		t.Fatalf("simulate cancel: %v", err)
	}

	out, err := (&GetAppointmentTool{}).InvokableRun(
		context.Background(),
		`{"appointment_id":"`+appt.ID+`"}`,
	)
	if err != nil {
		t.Fatalf("InvokableRun: %v / %s", err, out)
	}

	// status + cancel_reason 都该出现
	if !strings.Contains(out, "cancelled") {
		t.Errorf("output should show status=cancelled, got %q", out)
	}
	if !strings.Contains(out, "取消原因") {
		t.Errorf("output should show '取消原因' section, got %q", out)
	}
	if !strings.Contains(out, "临时有事") {
		t.Errorf("output should include cancel reason text, got %q", out)
	}
}
