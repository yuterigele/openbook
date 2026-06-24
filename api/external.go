package api

// external.go —— 给 API key 调用的外部接入端点（v4.12.1 api_access 实战 demo）
//
// 全部走 auth.APIKeyAuth 中间件 + auth.RequireAPIKeyScope
// 当前 demo：
//   GET /api/external/appointments?from=YYYY-MM-DD&to=YYYY-MM-DD
//     scope: appointments:read
//     返：JSON 数组（含 appointment 详情，跨店隔离——按 API key 的 shop_id 过滤）

import (
	"context"
	"net/http"
	"time"

	"github.com/cloudwego/hertz/pkg/app"

	"github.com/yuterigele/openbook/auth"
	"github.com/yuterigele/openbook/storage"
)

// ExternalAppointment 外部 API 返的 appointment JSON
//
// 字段少于内部 handler（避免泄漏客户 phone 等敏感信息）
type ExternalAppointment struct {
	ID         string `json:"id"`
	Date       string `json:"date"`
	Time       string `json:"time"`
	BarberName string `json:"barber_name"`
	Customer   string `json:"customer"`
	Service    string `json:"service"`
	Status     string `json:"status"`
}

// listExternalAppointmentsHandler GET /api/external/appointments
//
// auth: APIKeyAuth + RequireAPIKeyScope("appointments:read")
// 跨店隔离：API key 的 shop_id 自动从 claims 拿
func listExternalAppointmentsHandler(ctx context.Context, c *app.RequestContext) {
	shopID := ""
	if cl := auth.GetClaims(c); cl != nil {
		shopID = cl.ShopID
	}
	if shopID == "" {
		c.JSON(http.StatusUnauthorized, map[string]string{"error": "no shop in api key"})
		return
	}

	from := string(c.Query("from"))
	to := string(c.Query("to"))
	if from == "" || to == "" {
		// 默认最近 30 天
		now := time.Now()
		to = now.Format("2006-01-02")
		from = now.AddDate(0, 0, -29).Format("2006-01-02")
	}
	if err := validateDateParam("from", from); err != nil {
		c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if err := validateDateParam("to", to); err != nil {
		c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	appts, err := storage.ListAppointmentsByShopRange(ctx, shopID, from, to)
	if err != nil {
		c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	out := make([]ExternalAppointment, 0, len(appts))
	for _, a := range appts {
		out = append(out, ExternalAppointment{
			ID:         a.ID,
			Date:       a.Date,
			Time:       a.Time,
			BarberName: a.BarberName,
			Customer:   a.Customer,
			Service:    a.Service,
			Status:     a.Status,
		})
	}
	c.JSON(http.StatusOK, map[string]interface{}{
		"shop_id":  shopID,
		"from":     from,
		"to":       to,
		"total":    len(out),
		"items":    out,
	})
}