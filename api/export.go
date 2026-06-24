package api

// export.go —— 商户数据导出（v4.12.1 feature gate 实战）
//
// GET /api/admin/data/export?type=appointments&from=YYYY-MM-DD&to=YYYY-MM-DD&format=csv
//   - perm: view:plan（owner-only，v4.12 plans UI 同源）
//   - feature: data_export（basic 禁，pro+ 允许——storage.HasFeature 检查）
//   - plan: active（frozen 店走 middleware 402，platform_admin 走 bypass）
//   - 跨店隔离：handler 走 shopFromClaims(c) 拿自己店 shopID
//
// v4.12.1 范围：
//   - 只支持 type=appointments
//   - 只支持 format=csv（v4.13 加 xlsx）
//   - 限本店主（不跨店）—— 跨店导出走 platform_admin 链路，留 v4.13
//
// 失败语义：
//   - 缺 perm  → 403（PermViewPlan 没有——staff 默认进不来）
//   - basic plan → 403 + feature_required 字段（前端可引导升级）
//   - frozen plan → 402（auth middleware 拦，handler 不进）
//   - 参数错 → 400
//   - 成功 → 200 + CSV + Content-Disposition

import (
	"context"
	"encoding/csv"
	"fmt"
	"net/http"
	"time"

	"github.com/cloudwego/hertz/pkg/app"

	"github.com/yuterigele/openbook/storage"
)

// dataExportHandler GET /api/admin/data/export
//
// perm: view:plan
// feature: data_export
func dataExportHandler(ctx context.Context, c *app.RequestContext) {
	shopID := shopFromClaims(c)
	if shopID == "" {
		c.JSON(http.StatusUnauthorized, map[string]string{"error": "no shop in session"})
		return
	}
	if storage.DB == nil {
		c.JSON(http.StatusServiceUnavailable, map[string]string{"error": "db not initialized"})
		return
	}

	// 1) 解析 type（v4.12.1 只支持 appointments）
	exportType := string(c.Query("type"))
	if exportType == "" {
		exportType = "appointments"
	}
	if exportType != "appointments" {
		c.JSON(http.StatusBadRequest, map[string]string{
			"error": "不支持的 type: " + exportType + "（v4.12.1 只支持 appointments）",
		})
		return
	}

	// 2) 解析 from / to
	fromRaw := string(c.Query("from"))
	toRaw := string(c.Query("to"))
	if fromRaw == "" || toRaw == "" {
		// 默认：最近 30 天
		loc, _ := time.LoadLocation("Asia/Shanghai")
		if loc == nil {
			loc = time.FixedZone("CST", 8*3600)
		}
		now := time.Now().In(loc)
		toRaw = now.Format("2006-01-02")
		fromRaw = now.AddDate(0, 0, -29).Format("2006-01-02")
	}
	if err := validateDateParam("from", fromRaw); err != nil {
		c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if err := validateDateParam("to", toRaw); err != nil {
		c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if fromRaw > toRaw {
		c.JSON(http.StatusBadRequest, map[string]string{"error": "from 不能晚于 to"})
		return
	}

	// 3) feature gate：basic 返 403 + feature_required（前端 plans UI 可引导升级）
	shop, err := storage.GetShopByID(ctx, shopID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, map[string]string{"error": "读 shop 失败: " + err.Error()})
		return
	}
	currentPlan := shop.Plan
	if !storage.IsValidPlanID(currentPlan) {
		currentPlan = storage.DefaultPlanID
	}
	if !storage.HasFeature(currentPlan, storage.FeatureDataExport) {
		c.JSON(http.StatusForbidden, map[string]string{
			"error":           "当前 plan 不支持数据导出，请升级到 Pro 或以上版本",
			"feature_required": storage.FeatureDataExport,
			"current_plan":    currentPlan,
		})
		return
	}

	// 4) 查预约（不限 status：active / cancelled / completed / noshow 都返）
	appts, err := storage.ListAppointmentsByShopRange(ctx, shopID, fromRaw, toRaw)
	if err != nil {
		c.JSON(http.StatusInternalServerError, map[string]string{"error": "查预约失败: " + err.Error()})
		return
	}

	// 5) 输出 CSV（加 BOM 防止 Excel 打开中文乱码）
	filename := fmt.Sprintf("appointments-%s-to-%s.csv", fromRaw, toRaw)
	c.Response.Header.Set("Content-Type", "text/csv; charset=utf-8")
	c.Response.Header.Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))

	// 先写 BOM（Excel 需要 UTF-8 BOM 才认中文）
	c.Write([]byte("\xEF\xBB\xBF"))

	w := csv.NewWriter(c)
	// 表头（中文，方便商户直接看）
	if err := w.Write([]string{"日期", "时间", "理发师", "客户", "服务", "状态", "来源"}); err != nil {
		// 已经写 header 了，没法回 500；记 log 即可
		return
	}
	for _, a := range appts {
		if err := w.Write([]string{
			a.Date,
			a.Time,
			a.BarberName,
			a.Customer,
			a.Service,
			appointmentStatusCN(a.Status),
			a.Source,
		}); err != nil {
			return
		}
	}
	w.Flush()
	if err := w.Error(); err != nil {
		// 流式输出已部分写入，无法回 500
		return
	}
}

// validateDateParam 校验 YYYY-MM-DD
func validateDateParam(name, raw string) error {
	if raw == "" {
		return fmt.Errorf("%s 不能为空", name)
	}
	if _, err := time.ParseInLocation("2006-01-02", raw, time.Local); err != nil {
		return fmt.Errorf("%s 格式错误，需 YYYY-MM-DD", name)
	}
	return nil
}

// appointmentStatusCN status → 中文
func appointmentStatusCN(status string) string {
	switch status {
	case "active":
		return "已预约"
	case "cancelled":
		return "已取消"
	case "completed":
		return "已完成"
	case "noshow":
		return "未到店"
	case "":
		return "已预约"
	default:
		return status
	}
}