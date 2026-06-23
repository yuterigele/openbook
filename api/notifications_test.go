package api

// notifications_test.go
//
// v4.10.1 通知中心（admin 后台）handler 测试
//
// 覆盖：
//   - listNotificationsHandler
//   - retryNotificationHandler（单条补发）
//   - retryAllFailedNotificationsHandler（一键补发）
//   - 多店隔离：跨 shopID 操作 403
//   - 已 sent 拒绝 retry：409
//   - 未知 status / type 拒绝：400
//
// Run:
//   go test ./api/... -run "TestNotifications" -v

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/yuterigele/openbook/storage"
)

// plantNotification 直接插一条 notification row（测试 fixture）
func plantNotification(t *testing.T, shopID, leaveID, apptID, customerID, status, notifType string) uint64 {
	t.Helper()
	id, err := storage.CreateCustomerNotification(context.Background(), &storage.CustomerNotification{
		LeaveID:       leaveID,
		ShopID:        shopID,
		AppointmentID: apptID,
		CustomerID:    customerID,
		Type:          notifType,
		Status:        status,
		TextPreview:   "fixture text",
	})
	if err != nil {
		t.Fatalf("plant notification: %v", err)
	}
	// 测试里直接覆盖成期望的 status（CreateCustomerNotification 默认 pending）
	if status != storage.NotifStatusPending {
		if err := storage.DB.Model(&storage.CustomerNotification{}).
			Where("id = ?", id).
			Updates(map[string]interface{}{"status": status}).Error; err != nil {
			t.Fatalf("update status: %v", err)
		}
	}
	return id
}

// fakeNotifSender 模拟 sender：默认成功；可控失败
type fakeNotifSender struct {
	calls atomic.Int32
	fail  error
}

func (f *fakeNotifSender) send(_ context.Context, appt *storage.Appointment, _ string) error {
	f.calls.Add(1)
	return f.fail
}

// setNotifSender 把测试用 sender 注入到 api 包级 notifSender（先存原值，t.Cleanup 还原）
func setNotifSender(t *testing.T, s storage.LeaveNotificationSender) {
	t.Helper()
	prev := notifSender
	notifSender = s
	t.Cleanup(func() { notifSender = prev })
}

// ===================== listNotificationsHandler =====================

func TestListNotifications_NoClaims(t *testing.T) {
	setupAPITestDB(t)
	ctx := newAPIContext(t, "GET", "/api/admin/notifications", nil)
	status, body := runHandler(t, listNotificationsHandler, ctx)
	if status != statusUnauthorized {
		t.Errorf("expected 401, got %d body=%s", status, body)
	}
}

func TestListNotifications_Empty(t *testing.T) {
	setupAPITestDB(t)
	shopID := newShopID()
	storage.MakeShop(t, shopID, "")
	ctx := newAPIContext(t, "GET", "/api/admin/notifications", nil, withClaims(adminClaims(shopID)))

	status, body := runHandler(t, listNotificationsHandler, ctx)
	if status != statusOK {
		t.Fatalf("expected 200, got %d body=%s", status, body)
	}
	if body != "[]" {
		t.Errorf("expected [], got %q", body)
	}
}

func TestListNotifications_FilterStatus(t *testing.T) {
	setupAPITestDB(t)
	shopID := newShopID()
	storage.MakeShop(t, shopID, "")

	plantNotification(t, shopID, "leave-1", "appt-1", "c1", storage.NotifStatusFailed, storage.NotifTypeLeaveCancel)
	plantNotification(t, shopID, "leave-1", "appt-2", "c2", storage.NotifStatusSent, storage.NotifTypeLeaveCancel)
	plantNotification(t, shopID, "leave-1", "appt-3", "c3", storage.NotifStatusSkipped, storage.NotifTypeLeaveNoContact)

	// status=failed
	ctx := newAPIContext(t, "GET", "/api/admin/notifications", nil,
		withClaims(adminClaims(shopID)),
		withQuery("status", "failed"))
	status, body := runHandler(t, listNotificationsHandler, ctx)
	if status != statusOK {
		t.Fatalf("expected 200, got %d body=%s", status, body)
	}
	var list []storage.CustomerNotification
	if err := json.Unmarshal([]byte(body), &list); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, body)
	}
	if len(list) != 1 {
		t.Errorf("len = %d, want 1", len(list))
	}
	if list[0].CustomerID != "c1" {
		t.Errorf("got customer %s, want c1", list[0].CustomerID)
	}
}

func TestListNotifications_FilterTypeAndLeaveID(t *testing.T) {
	setupAPITestDB(t)
	shopID := newShopID()
	storage.MakeShop(t, shopID, "")

	plantNotification(t, shopID, "leave-A", "appt-1", "c1", storage.NotifStatusFailed, storage.NotifTypeLeaveCancel)
	plantNotification(t, shopID, "leave-A", "appt-2", "c2", storage.NotifStatusFailed, storage.NotifTypeLeaveReschedule)
	plantNotification(t, shopID, "leave-B", "appt-3", "c3", storage.NotifStatusFailed, storage.NotifTypeLeaveCancel)

	// type=leave_reschedule + leave_id=leave-A → 应只 1 条
	ctx := newAPIContext(t, "GET", "/api/admin/notifications", nil,
		withClaims(adminClaims(shopID)),
		withQuery("type", "leave_reschedule"),
		withQuery("leave_id", "leave-A"))
	status, body := runHandler(t, listNotificationsHandler, ctx)
	if status != statusOK {
		t.Fatalf("expected 200, got %d body=%s", status, body)
	}
	var list []storage.CustomerNotification
	json.Unmarshal([]byte(body), &list)
	if len(list) != 1 || list[0].CustomerID != "c2" {
		t.Errorf("filter result wrong: %+v", list)
	}
}

func TestListNotifications_InvalidStatus(t *testing.T) {
	setupAPITestDB(t)
	shopID := newShopID()
	storage.MakeShop(t, shopID, "")
	ctx := newAPIContext(t, "GET", "/api/admin/notifications", nil,
		withClaims(adminClaims(shopID)),
		withQuery("status", "not_a_real_status"))
	status, body := runHandler(t, listNotificationsHandler, ctx)
	if status != statusBadRequest {
		t.Errorf("expected 400, got %d body=%s", status, body)
	}
}

func TestListNotifications_CrossShopIsolation(t *testing.T) {
	setupAPITestDB(t)
	shop1 := newShopID()
	shop2 := newShopID()
	storage.MakeShop(t, shop1, "")
	storage.MakeShop(t, shop2, "")

	// shop1 写 1 条
	plantNotification(t, shop1, "leave-1", "appt-1", "c1", storage.NotifStatusFailed, storage.NotifTypeLeaveCancel)
	// shop2 写 1 条
	plantNotification(t, shop2, "leave-1", "appt-1", "c2", storage.NotifStatusFailed, storage.NotifTypeLeaveCancel)

	// 用 shop2 身份请求 → 应只返回 shop2 的 1 条
	ctx := newAPIContext(t, "GET", "/api/admin/notifications", nil,
		withClaims(adminClaims(shop2)))
	status, body := runHandler(t, listNotificationsHandler, ctx)
	if status != statusOK {
		t.Fatalf("expected 200, got %d body=%s", status, body)
	}
	var list []storage.CustomerNotification
	json.Unmarshal([]byte(body), &list)
	if len(list) != 1 {
		t.Errorf("len = %d, want 1 (shop1 的不应出现)", len(list))
	}
	if list[0].CustomerID != "c2" {
		t.Errorf("got %s, want c2", list[0].CustomerID)
	}
}

// ===================== retryNotificationHandler =====================

func TestRetryNotification_NoClaims(t *testing.T) {
	setupAPITestDB(t)
	ctx := newAPIContext(t, "POST", "/api/admin/notifications/1/retry", nil)
	status, body := runHandler(t, retryNotificationHandler, ctx)
	if status != statusUnauthorized {
		t.Errorf("expected 401, got %d body=%s", status, body)
	}
}

func TestRetryNotification_InvalidID(t *testing.T) {
	setupAPITestDB(t)
	shopID := newShopID()
	storage.MakeShop(t, shopID, "")
	ctx := newAPIContext(t, "POST", "/api/admin/notifications/abc/retry", nil,
		withClaims(adminClaims(shopID)),
		withPathParam("id", "abc"))
	status, _ := runHandler(t, retryNotificationHandler, ctx)
	if status != statusBadRequest {
		t.Errorf("expected 400 for non-numeric id, got %d", status)
	}
}

func TestRetryNotification_NotFound(t *testing.T) {
	setupAPITestDB(t)
	shopID := newShopID()
	storage.MakeShop(t, shopID, "")
	ctx := newAPIContext(t, "POST", "/api/admin/notifications/9999/retry", nil,
		withClaims(adminClaims(shopID)),
		withPathParam("id", "9999"))
	status, _ := runHandler(t, retryNotificationHandler, ctx)
	if status != statusNotFound {
		t.Errorf("expected 404, got %d", status)
	}
}

func TestRetryNotification_CrossShop_403(t *testing.T) {
	setupAPITestDB(t)
	shop1 := newShopID()
	shop2 := newShopID()
	storage.MakeShop(t, shop1, "")
	storage.MakeShop(t, shop2, "")
	id := plantNotification(t, shop1, "leave-1", "appt-1", "c1", storage.NotifStatusFailed, storage.NotifTypeLeaveCancel)

	// shop2 试图操作 shop1 的通知
	ctx := newAPIContext(t, "POST", "/api/admin/notifications/1/retry", nil,
		withClaims(adminClaims(shop2)),
		withPathParam("id", idToStr(id)))
	status, body := runHandler(t, retryNotificationHandler, ctx)
	if status != statusForbidden {
		t.Errorf("expected 403, got %d body=%s", status, body)
	}
}

func TestRetryNotification_AlreadySent_409(t *testing.T) {
	setupAPITestDB(t)
	shopID := newShopID()
	storage.MakeShop(t, shopID, "")
	appt := storage.MakeAppointment(t, shopID, "c1", "Alice", "Tony", "2026-06-25", "10:00")
	id := plantNotification(t, shopID, "leave-1", appt.ID, "c1", storage.NotifStatusSent, storage.NotifTypeLeaveCancel)

	setNotifSender(t, (&fakeNotifSender{}).send)
	ctx := newAPIContext(t, "POST", "/api/admin/notifications/1/retry", nil,
		withClaims(adminClaims(shopID)),
		withPathParam("id", idToStr(id)))
	status, body := runHandler(t, retryNotificationHandler, ctx)
	if status != statusConflict {
		t.Errorf("expected 409, got %d body=%s", status, body)
	}
	if !strings.Contains(body, "already sent") {
		t.Errorf("body should mention 'already sent', got %s", body)
	}
}

func TestRetryNotification_FailedToSent_200(t *testing.T) {
	setupAPITestDB(t)
	shopID := newShopID()
	storage.MakeShop(t, shopID, "")
	appt := storage.MakeAppointment(t, shopID, "c1", "Alice", "Tony", "2026-06-25", "10:00")
	id := plantNotification(t, shopID, "leave-1", appt.ID, "c1", storage.NotifStatusFailed, storage.NotifTypeLeaveCancel)

	sender := &fakeNotifSender{fail: nil}
	setNotifSender(t, sender.send)
	ctx := newAPIContext(t, "POST", "/api/admin/notifications/1/retry", nil,
		withClaims(adminClaims(shopID)),
		withPathParam("id", idToStr(id)))
	status, body := runHandler(t, retryNotificationHandler, ctx)
	if status != statusOK {
		t.Fatalf("expected 200, got %d body=%s", status, body)
	}
	var resp map[string]any
	json.Unmarshal([]byte(body), &resp)
	if resp["new_status"] != "sent" {
		t.Errorf("new_status = %v, want sent", resp["new_status"])
	}
	if sender.calls.Load() != 1 {
		t.Errorf("sender.calls = %d, want 1", sender.calls.Load())
	}
	// DB 验证
	n, _, _ := storage.GetNotificationByID(context.Background(), id)
	if n.Status != storage.NotifStatusSent {
		t.Errorf("DB status = %s, want sent", n.Status)
	}
}

func TestRetryNotification_StillFailed_200WithError(t *testing.T) {
	setupAPITestDB(t)
	shopID := newShopID()
	storage.MakeShop(t, shopID, "")
	appt := storage.MakeAppointment(t, shopID, "c1", "Alice", "Tony", "2026-06-25", "10:00")
	id := plantNotification(t, shopID, "leave-1", appt.ID, "c1", storage.NotifStatusFailed, storage.NotifTypeLeaveCancel)

	sender := &fakeNotifSender{fail: errors.New("still broken")}
	setNotifSender(t, sender.send)
	ctx := newAPIContext(t, "POST", "/api/admin/notifications/1/retry", nil,
		withClaims(adminClaims(shopID)),
		withPathParam("id", idToStr(id)))
	status, body := runHandler(t, retryNotificationHandler, ctx)
	// 重发仍失败 → 200 + new_status=failed + error message
	if status != statusOK {
		t.Fatalf("expected 200 (retry still failed but row updated), got %d body=%s", status, body)
	}
	var resp map[string]any
	json.Unmarshal([]byte(body), &resp)
	if resp["new_status"] != "failed" {
		t.Errorf("new_status = %v, want failed", resp["new_status"])
	}
	if resp["error"] == nil {
		t.Errorf("expected error in response, got %v", resp)
	}
}

// ===================== retryAllFailedNotificationsHandler =====================

func TestRetryAllFailed_HappyPath(t *testing.T) {
	setupAPITestDB(t)
	shopID := newShopID()
	storage.MakeShop(t, shopID, "")
	appt := storage.MakeAppointment(t, shopID, "c1", "Alice", "Tony", "2026-06-25", "10:00")

	// 3 failed leave 通知
	for _, cid := range []string{"c1", "c2", "c3"} {
		plantNotification(t, shopID, "leave-1", appt.ID, cid, storage.NotifStatusFailed, storage.NotifTypeLeaveCancel)
	}
	// 1 sent（不应被重发）
	plantNotification(t, shopID, "leave-1", appt.ID, "c4", storage.NotifStatusSent, storage.NotifTypeLeaveCancel)

	sender := &fakeNotifSender{fail: nil}
	setNotifSender(t, sender.send)
	ctx := newAPIContext(t, "POST", "/api/admin/notifications/retry-batch", nil,
		withClaims(adminClaims(shopID)))
	status, body := runHandler(t, retryAllFailedNotificationsHandler, ctx)
	if status != statusOK {
		t.Fatalf("expected 200, got %d body=%s", status, body)
	}
	var resp map[string]any
	json.Unmarshal([]byte(body), &resp)
	if int(resp["succeeded"].(float64)) != 3 {
		t.Errorf("succeeded = %v, want 3", resp["succeeded"])
	}
	if int(resp["failed"].(float64)) != 0 {
		t.Errorf("failed = %v, want 0", resp["failed"])
	}
	if int(resp["total"].(float64)) != 3 {
		t.Errorf("total = %v, want 3", resp["total"])
	}
}

func TestRetryAllFailed_NoClaims(t *testing.T) {
	setupAPITestDB(t)
	ctx := newAPIContext(t, "POST", "/api/admin/notifications/retry-batch", nil)
	status, _ := runHandler(t, retryAllFailedNotificationsHandler, ctx)
	if status != statusUnauthorized {
		t.Errorf("expected 401, got %d", status)
	}
}

// idToStr helper: uint64 → string（path param 需要）
func idToStr(id uint64) string {
	return strconv.FormatUint(id, 10)
}

// ===================== 订阅续费鉴权（v4.10.1：只 platform_admin 能续费）=====================

func TestRenewSubscription_OwnerIsForbidden_403(t *testing.T) {
	setupAPITestDB(t)
	shopID := newShopID()
	storage.MakeShop(t, shopID, "")
	ctx := newAPIContext(t, "POST", "/api/admin/subscription/renew",
		jsonRaw(`{"plan":"pro","months":1,"amount":9900}`),
		withClaims(adminClaims(shopID)), // role=owner
	)
	status, body := runWithRole(t, []string{storage.RolePlatformAdmin}, renewSubscriptionHandler, ctx)
	if status != statusForbidden {
		t.Errorf("owner 应返回 403，got %d body=%s", status, body)
	}
}

func TestRenewSubscription_StaffIsForbidden_403(t *testing.T) {
	setupAPITestDB(t)
	shopID := newShopID()
	storage.MakeShop(t, shopID, "")
	ctx := newAPIContext(t, "POST", "/api/admin/subscription/renew",
		jsonRaw(`{"plan":"pro","months":1,"amount":9900}`))
	setClaimsForAdmin(ctx, 1, shopID, storage.RoleStaff)
	status, body := runWithRole(t, []string{storage.RolePlatformAdmin}, renewSubscriptionHandler, ctx)
	if status != statusForbidden {
		t.Errorf("staff 应返回 403，got %d body=%s", status, body)
	}
}

func TestRenewSubscription_PlatformAdminOK_200(t *testing.T) {
	setupAPITestDB(t)
	shopID := newShopID()
	storage.MakeShop(t, shopID, "")
	ctx := newAPIContext(t, "POST", "/api/admin/subscription/renew",
		jsonRaw(`{"plan":"pro","months":1,"amount":9900}`))
	setClaimsForAdmin(ctx, 99, shopID, storage.RolePlatformAdmin)
	status, body := runWithRole(t, []string{storage.RolePlatformAdmin}, renewSubscriptionHandler, ctx)
	if status != statusOK {
		t.Fatalf("platform_admin 应返回 200，got %d body=%s", status, body)
	}
	// 验证 DB：subscription 表多了一条
	var subs []storage.Subscription
	if err := storage.DB.Where("shop_id = ?", shopID).Find(&subs).Error; err != nil {
		t.Fatalf("query subs: %v", err)
	}
	if len(subs) != 1 {
		t.Errorf("subscription count = %d, want 1", len(subs))
	}
	if subs[0].Plan != "pro" {
		t.Errorf("plan = %q, want pro", subs[0].Plan)
	}
}

func TestListSubscriptions_OwnerIsForbidden_403(t *testing.T) {
	// v4.10.1：整个订阅模块归 platform_admin，owner 看不到订阅列表
	setupAPITestDB(t)
	shopID := newShopID()
	storage.MakeShop(t, shopID, "")
	admin := storage.MakeAdminWithRole(t, shopID, "owner1", storage.RoleOwner)
	ctx := newAPIContext(t, "GET", "/api/admin/subscription", nil)
	setClaimsForAdmin(ctx, admin.ID, shopID, storage.RoleOwner)
	status, body := runWithRole(t, []string{storage.RolePlatformAdmin}, listSubscriptionsHandler, ctx)
	if status != statusForbidden {
		t.Errorf("owner 列表订阅应返回 403，got %d body=%s", status, body)
	}
}

func TestListSubscriptions_StaffIsForbidden_403(t *testing.T) {
	setupAPITestDB(t)
	shopID := newShopID()
	storage.MakeShop(t, shopID, "")
	admin := storage.MakeAdminWithRole(t, shopID, "staff1", storage.RoleStaff)
	ctx := newAPIContext(t, "GET", "/api/admin/subscription", nil)
	setClaimsForAdmin(ctx, admin.ID, shopID, storage.RoleStaff)
	status, body := runWithRole(t, []string{storage.RolePlatformAdmin}, listSubscriptionsHandler, ctx)
	if status != statusForbidden {
		t.Errorf("staff 列表订阅应返回 403，got %d body=%s", status, body)
	}
}

func TestListSubscriptions_PlatformAdminOK_200(t *testing.T) {
	setupAPITestDB(t)
	shopID := newShopID()
	storage.MakeShop(t, shopID, "")
	ctx := newAPIContext(t, "GET", "/api/admin/subscription", nil)
	setClaimsForAdmin(ctx, 99, shopID, storage.RolePlatformAdmin)
	status, body := runWithRole(t, []string{storage.RolePlatformAdmin}, listSubscriptionsHandler, ctx)
	if status != statusOK {
		t.Errorf("platform_admin 列表订阅应返回 200，got %d body=%s", status, body)
	}
}
