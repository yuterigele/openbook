package tools

// get_appointment_test.go 覆盖预约查询的正常、改派、不存在、取消和隐私场景。

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
	// 保留模型决策所需的调用约束，不携带历史版本或实现背景。
	desc := info.Desc
	mustHave := []string{
		"改时间或取消前必须调用",
		"以工具结果为准",
	}
	for _, sub := range mustHave {
		if !strings.Contains(desc, sub) {
			t.Errorf("Info.Desc should mention %q, got %q", sub, info.Desc)
		}
	}
	if strings.Contains(strings.ToLower(desc), "v4.") {
		t.Errorf("Info.Desc should not contain a version number, got %q", info.Desc)
	}
}

func TestGetAppointment_HappyPath(t *testing.T) {
	setupToolsTestDB(t)
	shop := storage.MakeShop(t, "shop-1", "")
	storage.MakeBarber(t, "barber-Tony", shop.ID, "Tony")
	cust := storage.MakeCustomer(t, "Alice", 0, 0)
	appt := storage.MakeAppointment(t, shop.ID, cust.ID, "Alice", "Tony", "2026-06-26", "14:00")

	ctx := WithOpenID(WithShopID(context.Background(), shop.ID), cust.WechatOpenID)
	out, err := (&GetAppointmentTool{}).InvokableRun(
		ctx,
		`{"appointment_id":"`+appt.ID+`"}`,
	)
	if err != nil {
		t.Fatalf("InvokableRun: %v / %s", err, out)
	}

	// 必须含完整字段
	mustHave := []string{
		appointmentDisplayNumber(appt.ID),
		"Tony",
		"2026-06-26",
		"14:00",
		"active", // status
	}
	if strings.Contains(out, appt.ID) {
		t.Errorf("output should NOT expose the internal appointment ID, got %q", out)
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

func TestGetAppointment_LegacyCustomerReference(t *testing.T) {
	setupToolsTestDB(t)
	shop := storage.MakeShop(t, "shop-1", "")
	cust := storage.MakeCustomer(t, "Alice", 0, 0)
	appt := storage.MakeAppointment(t, shop.ID, cust.ID, "Alice", "Tony", "2026-06-26", "14:00")
	const legacyID = "a4f0e91b-1234-4e6c-9d8b-0123456789ab"
	if err := storage.DB.Model(appt).Update("id", legacyID).Error; err != nil {
		t.Fatalf("set deterministic appointment ID: %v", err)
	}
	appt.ID = legacyID

	ctx := WithOpenID(WithShopID(context.Background(), shop.ID), cust.WechatOpenID)
	out, err := (&GetAppointmentTool{}).InvokableRun(ctx, `{"appointment_id":"OB-A4F0"}`)
	if err != nil {
		t.Fatalf("legacy customer reference should resolve: %v", err)
	}
	if !strings.Contains(out, appointmentDisplayNumber(appt.ID)) {
		t.Errorf("output should contain the current display reference, got %q", out)
	}
}

func TestGetAppointment_AmbiguousCustomerReferenceIsRejected(t *testing.T) {
	setupToolsTestDB(t)
	shop := storage.MakeShop(t, "shop-1", "")
	cust := storage.MakeCustomer(t, "Alice", 0, 0)
	first := storage.MakeAppointment(t, shop.ID, cust.ID, "Alice", "Tony", "2026-06-26", "14:00")
	second := storage.MakeAppointment(t, shop.ID, cust.ID, "Alice", "Tony", "2026-06-27", "14:00")
	for appt, id := range map[*storage.Appointment]string{
		first:  "a4f01111-1234-4e6c-9d8b-0123456789ab",
		second: "a4f02222-1234-4e6c-9d8b-0123456789ab",
	} {
		if err := storage.DB.Model(appt).Update("id", id).Error; err != nil {
			t.Fatalf("set deterministic appointment ID: %v", err)
		}
	}

	ctx := WithOpenID(WithShopID(context.Background(), shop.ID), cust.WechatOpenID)
	_, err := (&GetAppointmentTool{}).InvokableRun(ctx, `{"appointment_id":"OB-A4F0"}`)
	if err == nil {
		t.Fatal("ambiguous short reference must be rejected")
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

	ctx := WithOpenID(WithShopID(context.Background(), shop.ID), cust.WechatOpenID)
	out, err := (&GetAppointmentTool{}).InvokableRun(
		ctx,
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
	shop := storage.MakeShop(t, "shop-1", "")
	cust := storage.MakeCustomer(t, "Alice", 0, 0)
	ctx := WithOpenID(WithShopID(context.Background(), shop.ID), cust.WechatOpenID)
	_, err := (&GetAppointmentTool{}).InvokableRun(
		ctx,
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

	ctx := WithOpenID(WithShopID(context.Background(), shop.ID), cust.WechatOpenID)
	out, err := (&GetAppointmentTool{}).InvokableRun(
		ctx,
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

func TestGetAppointment_RejectsOtherCustomer(t *testing.T) {
	setupToolsTestDB(t)
	shop := storage.MakeShop(t, "shop-1", "")
	owner := storage.MakeCustomer(t, "Alice", 0, 0)
	attacker := storage.MakeCustomer(t, "Mallory", 0, 0)
	appt := storage.MakeAppointment(t, shop.ID, owner.ID, "Alice", "Tony", "2026-06-26", "14:00")

	ctx := WithOpenID(WithShopID(context.Background(), shop.ID), attacker.WechatOpenID)
	_, err := (&GetAppointmentTool{}).InvokableRun(ctx, `{"appointment_id":"`+appt.ID+`"}`)
	if err == nil {
		t.Fatal("other customer must not read the appointment")
	}
}
