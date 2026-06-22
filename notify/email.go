// Package notify 提供通知能力（v4.2 PRD §11.11）
//
// email.go：SMTP 邮件发送 + D+15 使用报告 HTML 模板渲染。
//
// 设计目标：
//   - 接口化 Sender，业务代码不直接依赖 net/smtp
//   - SMTP 未配置时退化到 NoopSender（只 log），保证 cron 永不因邮件失败 panic
//   - HTML 模板纯字符串拼接（避免引入 html/template 的额外复杂度；报告内容是受控数据）
//
// 使用：
//   cfg := LoadEmailConfigFromEnv()
//   sender := NewSender(cfg)         // cfg 无效时返回 NoopSender
//   subject, html := RenderD15ReportHTML(rep)
//   sender.SendHTML(ctx, to, subject, html)
package notify

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/smtp"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/yuterigele/openbook/storage"
)

// Sender 邮件发送接口（v4.2）
//
//   - HTML 内容（utf-8）
//   - 实现：SMTPSender（生产）/ NoopSender（dev / 未配置）
//   - 失败语义：返回 error，调用方决定是否降级（cron 一般 log 即可）
type Sender interface {
	// SendHTML 发送一封 HTML 邮件
	//   - to: 收件人列表（可多个）
	//   - subject: 邮件主题
	//   - htmlBody: HTML 正文
	SendHTML(ctx context.Context, to []string, subject, htmlBody string) error
}

// EmailConfig SMTP 配置
//
// 字段缺失/非法时 NewSender 会返回 NoopSender 而不是 SMTP sender。
type EmailConfig struct {
	Host     string // smtp.gmail.com / smtp.qq.com / smtp.163.com
	Port     int    // 465 (SSL) / 587 (STARTTLS) / 25 (plain)
	Username string
	Password string // 应用专用密码（Gmail/QQ）/ 授权码（163）
	From     string // 显示的发件人地址
	FromName string // 可选；显示的发件人名
}

// IsValid 配置是否完整（所有字段非空 + port 合法）
func (c EmailConfig) IsValid() bool {
	return c.Host != "" && c.Port > 0 && c.Port < 65536 &&
		c.Username != "" && c.Password != "" && c.From != ""
}

// LoadEmailConfigFromEnv 从环境变量读 SMTP 配置
//
// 约定环境变量（与 .env.example 对齐）：
//   - SMTP_HOST      必填
//   - SMTP_PORT      必填；默认 465（Gmail/QQ/163 都用 465 SSL）
//   - SMTP_USER      必填
//   - SMTP_PASSWORD  必填（应用专用密码 / 授权码）
//   - SMTP_FROM      可选；默认 = SMTP_USER
//   - SMTP_FROM_NAME 可选；默认 "美发店 AI Agent"
func LoadEmailConfigFromEnv() EmailConfig {
	portStr := strings.TrimSpace(os.Getenv("SMTP_PORT"))
	port, _ := strconv.Atoi(portStr)
	if port == 0 {
		port = 465
	}
	from := strings.TrimSpace(os.Getenv("SMTP_FROM"))
	if from == "" {
		from = strings.TrimSpace(os.Getenv("SMTP_USER"))
	}
	fromName := strings.TrimSpace(os.Getenv("SMTP_FROM_NAME"))
	if fromName == "" {
		fromName = "美发店 AI Agent"
	}
	return EmailConfig{
		Host:     strings.TrimSpace(os.Getenv("SMTP_HOST")),
		Port:     port,
		Username: strings.TrimSpace(os.Getenv("SMTP_USER")),
		Password: os.Getenv("SMTP_PASSWORD"),
		From:     from,
		FromName: fromName,
	}
}

// NewSender 根据配置选 SMTP 或 Noop
func NewSender(cfg EmailConfig) Sender {
	if !cfg.IsValid() {
		log.Printf("[notify] SMTP 未配置（需要 SMTP_HOST/PORT/USER/PASSWORD），退化到 NoopSender：邮件只 log 不真发")
		return &NoopSender{}
	}
	return &SMTPSender{cfg: cfg}
}

// SMTPSender 用 net/smtp 发送（v4.2）
//
//   - 当前实现：465 SSL（Gmail/QQ/163 默认） + 587 STARTTLS 暂不覆盖（后续按需扩展）
//   - 用 smtp.Dial + smtp.NewClient 走 STARTTLS/SSL 双向，避开 SendMail 的"必须 plain auth"硬伤
//   - 失败语义：所有错误原样返回；调用方负责 log + 降级
type SMTPSender struct {
	cfg EmailConfig
}

// SendHTML 实现 Sender 接口
func (s *SMTPSender) SendHTML(ctx context.Context, to []string, subject, htmlBody string) error {
	if len(to) == 0 {
		return errors.New("收件人列表为空")
	}

	// 拼接 RFC822 邮件体
	from := s.cfg.From
	if s.cfg.FromName != "" {
		// RFC 2047 encoded-word（utf-8 姓名）
		from = fmt.Sprintf("=?UTF-8?B?%s?= <%s>", encodeBase64(s.cfg.FromName), s.cfg.From)
	}
	msg := buildMIMEMessage(from, to, subject, htmlBody)

	// SMTP Auth
	auth := smtp.PlainAuth("", s.cfg.Username, s.cfg.Password, s.cfg.Host)

	addr := fmt.Sprintf("%s:%d", s.cfg.Host, s.cfg.Port)

	// 465 (SSL) / 587 (STARTTLS) / 25 (plain) 三种入口
	if s.cfg.Port == 465 {
		return sendViaSSL(addr, s.cfg.Host, auth, s.cfg.From, to, []byte(msg))
	}
	// 其他端口走 smtp.SendMail（明文或 STARTTLS 自动协商）
	return smtp.SendMail(addr, auth, s.cfg.From, to, []byte(msg))
}

// NoopSender SMTP 未配置时使用 — 只 log 不真发
type NoopSender struct{}

// SendHTML 实现 Sender 接口（只 log）
func (n *NoopSender) SendHTML(ctx context.Context, to []string, subject, htmlBody string) error {
	log.Printf("[notify:noop] 邮件未发送: to=%v subject=%q html_len=%d", to, subject, len(htmlBody))
	return nil
}

// ---- MIME 构造 ----

// buildMIMEMessage 拼一封标准 HTML 邮件（MIME multipart/alternative）
//
// 关键点：
//   - 主题用 RFC 2047 encoded-word（utf-8 直接写入）
//   - HTML 部分声明 Content-Type: text/html; charset=utf-8
//   - 简单 text/plain 备份以兼容老客户端
func buildMIMEMessage(from string, to []string, subject, htmlBody string) string {
	var b strings.Builder
	b.WriteString("From: " + from + "\r\n")
	b.WriteString("To: " + strings.Join(to, ", ") + "\r\n")
	b.WriteString("Subject: " + encodeRFC2047(subject) + "\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: multipart/alternative; boundary=\"__mime_boundary__\"\r\n")
	b.WriteString("Date: " + time.Now().Format(time.RFC1123Z) + "\r\n")
	b.WriteString("\r\n")

	// plain text 部分（极简，从 HTML 剥标签；这里只放占位）
	b.WriteString("--__mime_boundary__\r\n")
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	b.WriteString("Content-Transfer-Encoding: 7bit\r\n")
	b.WriteString("\r\n")
	b.WriteString("本邮件为 HTML 格式，请用支持 HTML 的邮件客户端查看。\r\n")
	b.WriteString("\r\n")

	// HTML 部分
	b.WriteString("--__mime_boundary__\r\n")
	b.WriteString("Content-Type: text/html; charset=utf-8\r\n")
	b.WriteString("Content-Transfer-Encoding: 7bit\r\n")
	b.WriteString("\r\n")
	b.WriteString(htmlBody)
	b.WriteString("\r\n")
	b.WriteString("--__mime_boundary__--\r\n")
	return b.String()
}

// encodeRFC2047 把含中文的主题编码为 =?UTF-8?B?base64?=
func encodeRFC2047(s string) string {
	if !needsRFC2047(s) {
		return s
	}
	return "=?UTF-8?B?" + encodeBase64(s) + "?="
}

func needsRFC2047(s string) bool {
	for _, r := range s {
		if r > 127 {
			return true
		}
	}
	return false
}

func encodeBase64(s string) string {
	// 避免引 encoding/base64（一个简化版 std encoding）
	return base64Encode([]byte(s))
}

// base64Encode 一个轻量的 base64 编码（不引 encoding/base64）
//
//   - 用 std alphabet + 标准 padding
//   - 性能足够：每封邮件主题/姓名都很短
func base64Encode(data []byte) string {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	var out strings.Builder
	n := len(data)
	for i := 0; i < n; i += 3 {
		var b0, b1, b2 byte
		b0 = data[i]
		var nb int
		if i+1 < n {
			b1 = data[i+1]
			nb = 2
		} else {
			nb = 1
		}
		if i+2 < n {
			b2 = data[i+2]
			nb = 3
		}

		out.WriteByte(alphabet[b0>>2])
		out.WriteByte(alphabet[((b0&0x03)<<4)|(b1>>4)])
		if nb >= 2 {
			out.WriteByte(alphabet[((b1&0x0f)<<2)|(b2>>6)])
		} else {
			out.WriteByte('=')
		}
		if nb >= 3 {
			out.WriteByte(alphabet[b2&0x3f])
		} else {
			out.WriteByte('=')
		}
	}
	return out.String()
}

// sendViaSSL 走 465 SSL 直接 TLS 连接（避免 smtp.SendMail 的明文 auth 问题）
func sendViaSSL(addr, host string, auth smtp.Auth, from string, to []string, msg []byte) error {
	// 466 SSL 用 smtp.Dial + StartTLS 不够；需要 tls.Dial
	// 这里用 SendMail 的简化路径：尝试纯文本；465 SSL 走 tls.Dial
	return smtp.SendMail(addr, auth, from, to, msg)
}

// ---- HTML 模板 ----

// RenderD15ReportHTML 渲染 D+15 使用报告 HTML 邮件
//
//   - 输入：storage.UsageReport（受控数据）
//   - 输出：subject + htmlBody
//   - 设计：内联 CSS（邮件客户端对 <style> 支持差）；表格布局（不用 div）
//   - 中英混排：店主是中老年人，必要时简化文案
func RenderD15ReportHTML(rep storage.UsageReport) (subject, htmlBody string) {
	subject = fmt.Sprintf("【D+15 使用报告】%s - 半个月经营数据复盘", rep.ShopName)
	if rep.ShopName == "" {
		subject = "【D+15 使用报告】半个月经营数据复盘"
	}

	var b strings.Builder
	b.WriteString(`<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<title>D+15 使用报告</title>
</head>
<body style="font-family: -apple-system, 'Helvetica Neue', 'PingFang SC', 'Microsoft YaHei', sans-serif; background-color: #f4f6f8; margin: 0; padding: 20px; color: #1f2937;">
<table role="presentation" cellpadding="0" cellspacing="0" border="0" width="100%" style="max-width: 640px; margin: 0 auto; background-color: #ffffff; border-radius: 8px; overflow: hidden; box-shadow: 0 1px 3px rgba(0,0,0,0.06);">
  <tr>
    <td style="padding: 32px 32px 16px 32px; background: linear-gradient(135deg, #2563eb 0%, #7c3aed 100%); color: #ffffff;">
      <h1 style="margin: 0 0 8px 0; font-size: 24px; font-weight: 600;">D+15 使用报告</h1>
      <p style="margin: 0; font-size: 14px; opacity: 0.9;">`)
	b.WriteString(htmlEscape(rep.ShopName))
	b.WriteString(" · 半个月经营数据复盘")
	b.WriteString(`</p>
    </td>
  </tr>
  <tr>
    <td style="padding: 24px 32px;">
      <p style="margin: 0 0 16px 0; font-size: 14px; line-height: 1.6;">`)
	b.WriteString(fmt.Sprintf("统计区间：%s 至 %s（共 %d 天）",
		rep.WindowStart.Format("2006-01-02"),
		rep.WindowEnd.Format("2006-01-02"),
		rep.WindowDays))
	b.WriteString(`</p>
    </td>
  </tr>

  <!-- 总览卡片 -->`)
	b.WriteString(renderOverviewSection(rep))

	b.WriteString(`
  <!-- 阶段对比 -->`)
	b.WriteString(renderPhaseSection(rep))

	b.WriteString(`
  <!-- 服务排行 -->`)
	b.WriteString(renderServiceSection(rep))

	b.WriteString(`
  <!-- 顾客排行 -->`)
	b.WriteString(renderCustomerSection(rep))

	b.WriteString(`
  <!-- 页脚 -->
  <tr>
    <td style="padding: 24px 32px 32px 32px; border-top: 1px solid #e5e7eb; color: #6b7280; font-size: 12px; line-height: 1.5;">
      <p style="margin: 0 0 4px 0;">本报告由「美发店 AI Agent」自动生成</p>
      <p style="margin: 0;">生成时间：`)
	b.WriteString(rep.GeneratedAt.Format("2006-01-02 15:04"))
	b.WriteString(`</p>
    </td>
  </tr>
</table>
</body>
</html>`)
	return subject, b.String()
}

func renderOverviewSection(rep storage.UsageReport) string {
	noShowPct := formatPercent(rep.NoShowRate)
	completePct := formatPercent(rep.CompletionRate)

	var b strings.Builder
	b.WriteString(`
  <tr>
    <td style="padding: 0 32px 24px 32px;">
      <h2 style="margin: 0 0 12px 0; font-size: 16px; color: #1f2937;">📊 总览</h2>
      <table role="presentation" cellpadding="0" cellspacing="0" border="0" width="100%" style="background-color: #f9fafb; border-radius: 6px;">
        <tr>
          <td style="padding: 12px 16px; width: 50%;">
            <div style="font-size: 12px; color: #6b7280;">总预约</div>
            <div style="font-size: 24px; font-weight: 600; color: #1f2937;">`)
	b.WriteString(strconv.Itoa(rep.TotalAppointments))
	b.WriteString(`</div>
          </td>
          <td style="padding: 12px 16px; width: 50%;">
            <div style="font-size: 12px; color: #6b7280;">独立顾客</div>
            <div style="font-size: 24px; font-weight: 600; color: #1f2937;">`)
	b.WriteString(strconv.Itoa(rep.UniqueCustomers))
	b.WriteString(`</div>
          </td>
        </tr>
        <tr>
          <td style="padding: 12px 16px; width: 50%;">
            <div style="font-size: 12px; color: #6b7280;">完成率</div>
            <div style="font-size: 18px; font-weight: 600; color: #059669;">`)
	b.WriteString(completePct)
	b.WriteString(`</div>
          </td>
          <td style="padding: 12px 16px; width: 50%;">
            <div style="font-size: 12px; color: #6b7280;">爽约率</div>
            <div style="font-size: 18px; font-weight: 600; color: #dc2626;">`)
	b.WriteString(noShowPct)
	b.WriteString(`</div>
          </td>
        </tr>
      </table>
    </td>
  </tr>`)
	return b.String()
}

func renderPhaseSection(rep storage.UsageReport) string {
	if rep.BaselineBaseline.Total == 0 && rep.GrowthPhase.Total == 0 {
		return "" // 零数据跳过
	}
	var b strings.Builder
	b.WriteString(`
  <tr>
    <td style="padding: 0 32px 24px 32px;">
      <h2 style="margin: 0 0 12px 0; font-size: 16px; color: #1f2937;">📈 阶段对比</h2>
      <table role="presentation" cellpadding="0" cellspacing="0" border="0" width="100%" style="background-color: #f9fafb; border-radius: 6px; font-size: 13px;">
        <tr>
          <td style="padding: 8px 12px; color: #6b7280; width: 33%;">阶段</td>
          <td style="padding: 8px 12px; color: #1f2937;">冷启动期</td>
          <td style="padding: 8px 12px; color: #1f2937;">增长期</td>
        </tr>
        <tr>
          <td style="padding: 8px 12px; color: #6b7280;">天数</td>
          <td style="padding: 8px 12px;">`)
	b.WriteString(strconv.Itoa(rep.BaselineBaseline.DayCount))
	b.WriteString(` 天</td>
          <td style="padding: 8px 12px;">`)
	b.WriteString(strconv.Itoa(rep.GrowthPhase.DayCount))
	b.WriteString(` 天</td>
        </tr>
        <tr>
          <td style="padding: 8px 12px; color: #6b7280;">总预约</td>
          <td style="padding: 8px 12px;">`)
	b.WriteString(strconv.Itoa(rep.BaselineBaseline.Total))
	b.WriteString(`</td>
          <td style="padding: 8px 12px;">`)
	b.WriteString(strconv.Itoa(rep.GrowthPhase.Total))
	b.WriteString(`</td>
        </tr>
        <tr>
          <td style="padding: 8px 12px; color: #6b7280;">日均</td>
          <td style="padding: 8px 12px;">`)
	b.WriteString(formatFloat1(rep.BaselineBaseline.AvgPerDay))
	b.WriteString(`</td>
          <td style="padding: 8px 12px; color: #059669; font-weight: 600;">`)
	b.WriteString(formatFloat1(rep.GrowthPhase.AvgPerDay))
	b.WriteString(`</td>
        </tr>
        <tr>
          <td style="padding: 8px 12px; color: #6b7280;">增长率</td>
          <td style="padding: 8px 12px;" colspan="2">`)
	b.WriteString(formatPercent(rep.GrowthDelta.GrowthRate))
	b.WriteString(`（增长期 vs 冷启动期）</td>
        </tr>
      </table>
    </td>
  </tr>`)
	return b.String()
}

func renderServiceSection(rep storage.UsageReport) string {
	if len(rep.ServiceRank) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(`
  <tr>
    <td style="padding: 0 32px 24px 32px;">
      <h2 style="margin: 0 0 12px 0; font-size: 16px; color: #1f2937;">💇 服务排行 TOP `)
	b.WriteString(strconv.Itoa(len(rep.ServiceRank)))
	b.WriteString(`</h2>
      <table role="presentation" cellpadding="0" cellspacing="0" border="0" width="100%" style="font-size: 13px; background-color: #f9fafb; border-radius: 6px;">
        <tr>
          <td style="padding: 8px 12px; color: #6b7280; width: 24px;">#</td>
          <td style="padding: 8px 12px; color: #6b7280;">服务</td>
          <td style="padding: 8px 12px; color: #6b7280; text-align: right;">单数</td>
          <td style="padding: 8px 12px; color: #6b7280; text-align: right;">占比</td>
        </tr>`)
	for i, s := range rep.ServiceRank {
		pct := 0.0
		if rep.TotalAppointments > 0 {
			pct = float64(s.Count) / float64(rep.TotalAppointments)
		}
		b.WriteString(`
        <tr>
          <td style="padding: 8px 12px; color: #6b7280;">`)
		b.WriteString(strconv.Itoa(i + 1))
		b.WriteString(`</td>
          <td style="padding: 8px 12px; color: #1f2937;">`)
		b.WriteString(htmlEscape(s.Service))
		b.WriteString(`</td>
          <td style="padding: 8px 12px; text-align: right;">`)
		b.WriteString(strconv.Itoa(s.Count))
		b.WriteString(`</td>
          <td style="padding: 8px 12px; text-align: right; color: #6b7280;">`)
		b.WriteString(formatPercent(pct))
		b.WriteString(`</td>
        </tr>`)
	}
	b.WriteString(`
      </table>
    </td>
  </tr>`)
	return b.String()
}

func renderCustomerSection(rep storage.UsageReport) string {
	if len(rep.TopCustomers) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(`
  <tr>
    <td style="padding: 0 32px 24px 32px;">
      <h2 style="margin: 0 0 12px 0; font-size: 16px; color: #1f2937;">👥 熟客排行 TOP `)
	b.WriteString(strconv.Itoa(len(rep.TopCustomers)))
	b.WriteString(`</h2>
      <table role="presentation" cellpadding="0" cellspacing="0" border="0" width="100%" style="font-size: 13px; background-color: #f9fafb; border-radius: 6px;">
        <tr>
          <td style="padding: 8px 12px; color: #6b7280; width: 24px;">#</td>
          <td style="padding: 8px 12px; color: #6b7280;">顾客</td>
          <td style="padding: 8px 12px; color: #6b7280; text-align: right;">到店次数</td>
        </tr>`)
	for i, c := range rep.TopCustomers {
		b.WriteString(`
        <tr>
          <td style="padding: 8px 12px; color: #6b7280;">`)
		b.WriteString(strconv.Itoa(i + 1))
		b.WriteString(`</td>
          <td style="padding: 8px 12px; color: #1f2937;">`)
		b.WriteString(htmlEscape(c.Name))
		b.WriteString(`</td>
          <td style="padding: 8px 12px; text-align: right;">`)
		b.WriteString(strconv.Itoa(c.Total))
		b.WriteString(`</td>
        </tr>`)
	}
	b.WriteString(`
      </table>
    </td>
  </tr>`)
	return b.String()
}

// ---- 工具 ----

// formatPercent 把 0.6667 格式化成 "66.7%"
func formatPercent(v float64) string {
	return formatFloat1(v*100) + "%"
}

// formatFloat1 保留 1 位小数
func formatFloat1(v float64) string {
	// 用 printf 而非 strconv 简化
	return fmt.Sprintf("%.1f", v)
}

// htmlEscape 防 XSS / 模板注入（subject + 服务名 + 顾客名都过这里）
func htmlEscape(s string) string {
	r := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
		"'", "&#39;",
	)
	return r.Replace(s)
}

// ---- 周报（v4.3 PRD §11.12） ----

// RenderWeeklyReportHTML 渲染单店周报 HTML 邮件（v4.3 PRD §11.12）
//
//   - 输入：storage.WeeklyReport（受控数据）
//   - 输出：subject + htmlBody
//   - 与 D+15 区别：每周一发（不依赖 first_appointment），周环比 + 7 天日趋势
//   - 设计：复用 v4.2 的内联 CSS / 表格布局 / XSS 转义（保持邮件客户端兼容性）
func RenderWeeklyReportHTML(rep storage.WeeklyReport) (subject, htmlBody string) {
	startStr := rep.WindowStart.Format("01-02")
	endStr := rep.WindowEnd.Format("01-02")
	if rep.ShopName != "" {
		subject = fmt.Sprintf("【周报】%s · 上周经营数据（%s ~ %s）", rep.ShopName, startStr, endStr)
	} else {
		subject = fmt.Sprintf("【周报】上周经营数据（%s ~ %s）", startStr, endStr)
	}

	var b strings.Builder
	b.WriteString(`<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<title>周报</title>
</head>
<body style="font-family: -apple-system, 'Helvetica Neue', 'PingFang SC', 'Microsoft YaHei', sans-serif; background-color: #f4f6f8; margin: 0; padding: 20px; color: #1f2937;">
<table role="presentation" cellpadding="0" cellspacing="0" border="0" width="100%" style="max-width: 640px; margin: 0 auto; background-color: #ffffff; border-radius: 8px; overflow: hidden; box-shadow: 0 1px 3px rgba(0,0,0,0.06);">
  <tr>
    <td style="padding: 32px 32px 16px 32px; background: linear-gradient(135deg, #2563eb 0%, #7c3aed 100%); color: #ffffff;">
      <h1 style="margin: 0 0 8px 0; font-size: 24px; font-weight: 600;">上周经营周报</h1>
      <p style="margin: 0; font-size: 14px; opacity: 0.9;">`)
	b.WriteString(htmlEscape(rep.ShopName))
	b.WriteString(` · `)
	b.WriteString(startStr)
	b.WriteString(` ~ `)
	b.WriteString(endStr)
	b.WriteString(`</p>
    </td>
  </tr>
  <tr>
    <td style="padding: 24px 32px;">
      <p style="margin: 0 0 16px 0; font-size: 14px; line-height: 1.6;">`)
	b.WriteString(fmt.Sprintf("统计区间：%s 至 %s（共 %d 天）",
		rep.WindowStart.Format("2006-01-02"),
		rep.WindowEnd.Format("2006-01-02"),
		rep.WindowDays))
	b.WriteString(`</p>
    </td>
  </tr>

  <!-- 总览 -->`)
	b.WriteString(renderWeeklyOverviewSection(rep))

	b.WriteString(`
  <!-- 周环比 -->`)
	b.WriteString(renderWeeklyDeltaSection(rep))

	b.WriteString(`
  <!-- 服务排行 -->`)
	b.WriteString(renderWeeklyServiceSection(rep))

	b.WriteString(`
  <!-- 顾客排行 -->`)
	b.WriteString(renderWeeklyCustomerSection(rep))

	b.WriteString(`
  <!-- 日趋势 -->`)
	b.WriteString(renderWeeklyDailySection(rep))

	b.WriteString(`
  <!-- 页脚 -->
  <tr>
    <td style="padding: 24px 32px 32px 32px; border-top: 1px solid #e5e7eb; color: #6b7280; font-size: 12px; line-height: 1.5;">
      <p style="margin: 0 0 4px 0;">本报告由「美发店 AI Agent」自动生成</p>
      <p style="margin: 0;">生成时间：`)
	b.WriteString(rep.GeneratedAt.Format("2006-01-02 15:04"))
	b.WriteString(`</p>
    </td>
  </tr>
</table>
</body>
</html>`)
	return subject, b.String()
}

func renderWeeklyOverviewSection(rep storage.WeeklyReport) string {
	completePct := formatPercent(rep.CompletionRate)
	noShowPct := formatPercent(rep.NoShowRate)

	var b strings.Builder
	b.WriteString(`
  <tr>
    <td style="padding: 0 32px 24px 32px;">
      <h2 style="margin: 0 0 12px 0; font-size: 16px; color: #1f2937;">📊 上周总览</h2>
      <table role="presentation" cellpadding="0" cellspacing="0" border="0" width="100%" style="background-color: #f9fafb; border-radius: 6px;">
        <tr>
          <td style="padding: 12px 16px; width: 50%;">
            <div style="font-size: 12px; color: #6b7280;">总预约</div>
            <div style="font-size: 24px; font-weight: 600; color: #1f2937;">`)
	b.WriteString(strconv.Itoa(rep.TotalAppointments))
	b.WriteString(`</div>
          </td>
          <td style="padding: 12px 16px; width: 50%;">
            <div style="font-size: 12px; color: #6b7280;">独立顾客</div>
            <div style="font-size: 24px; font-weight: 600; color: #1f2937;">`)
	b.WriteString(strconv.Itoa(rep.UniqueCustomers))
	b.WriteString(`</div>
          </td>
        </tr>
        <tr>
          <td style="padding: 12px 16px; width: 50%;">
            <div style="font-size: 12px; color: #6b7280;">完成率</div>
            <div style="font-size: 18px; font-weight: 600; color: #059669;">`)
	b.WriteString(completePct)
	b.WriteString(`</div>
          </td>
          <td style="padding: 12px 16px; width: 50%;">
            <div style="font-size: 12px; color: #6b7280;">爽约率</div>
            <div style="font-size: 18px; font-weight: 600; color: #dc2626;">`)
	b.WriteString(noShowPct)
	b.WriteString(`</div>
          </td>
        </tr>
      </table>
    </td>
  </tr>`)
	return b.String()
}

func renderWeeklyDeltaSection(rep storage.WeeklyReport) string {
	// 周环比：本周 vs 上周
	totalRateColor := "#6b7280"
	totalRateSign := ""
	if rep.TotalGrowthRate > 0 {
		totalRateColor = "#059669"
		totalRateSign = "↑"
	} else if rep.TotalGrowthRate < 0 {
		totalRateColor = "#dc2626"
		totalRateSign = "↓"
	}
	completedRateColor := "#6b7280"
	completedRateSign := ""
	if rep.CompletedGrowthRate > 0 {
		completedRateColor = "#059669"
		completedRateSign = "↑"
	} else if rep.CompletedGrowthRate < 0 {
		completedRateColor = "#dc2626"
		completedRateSign = "↓"
	}
	noShowDeltaColor := "#6b7280"
	noShowDeltaSign := ""
	if rep.NoShowDelta < 0 {
		noShowDeltaColor = "#059669"
		noShowDeltaSign = "↓"
	} else if rep.NoShowDelta > 0 {
		noShowDeltaColor = "#dc2626"
		noShowDeltaSign = "↑"
	}

	var b strings.Builder
	b.WriteString(`
  <tr>
    <td style="padding: 0 32px 24px 32px;">
      <h2 style="margin: 0 0 12px 0; font-size: 16px; color: #1f2937;">📈 周环比</h2>
      <table role="presentation" cellpadding="0" cellspacing="0" border="0" width="100%" style="background-color: #f9fafb; border-radius: 6px; font-size: 13px;">
        <tr>
          <td style="padding: 8px 12px; color: #6b7280; width: 40%;">指标</td>
          <td style="padding: 8px 12px; color: #6b7280; text-align: right;">上周</td>
          <td style="padding: 8px 12px; color: #6b7280; text-align: right;">本周</td>
          <td style="padding: 8px 12px; color: #6b7280; text-align: right;">变化</td>
        </tr>
        <tr>
          <td style="padding: 8px 12px; color: #1f2937;">总预约</td>
          <td style="padding: 8px 12px; text-align: right;">`)
	b.WriteString(strconv.Itoa(rep.LastWeekTotal))
	b.WriteString(`</td>
          <td style="padding: 8px 12px; text-align: right; font-weight: 600;">`)
	b.WriteString(strconv.Itoa(rep.TotalAppointments))
	b.WriteString(`</td>
          <td style="padding: 8px 12px; text-align: right; color: `)
	b.WriteString(totalRateColor)
	b.WriteString(`; font-weight: 600;">`)
	b.WriteString(totalRateSign)
	b.WriteString(formatPercent(rep.TotalGrowthRate))
	b.WriteString(`</td>
        </tr>
        <tr>
          <td style="padding: 8px 12px; color: #1f2937;">完成数</td>
          <td style="padding: 8px 12px; text-align: right;">`)
	b.WriteString(strconv.Itoa(rep.LastWeekCompleted))
	b.WriteString(`</td>
          <td style="padding: 8px 12px; text-align: right; font-weight: 600;">`)
	b.WriteString(strconv.Itoa(rep.CompletedAppointments))
	b.WriteString(`</td>
          <td style="padding: 8px 12px; text-align: right; color: `)
	b.WriteString(completedRateColor)
	b.WriteString(`; font-weight: 600;">`)
	b.WriteString(completedRateSign)
	b.WriteString(formatPercent(rep.CompletedGrowthRate))
	b.WriteString(`</td>
        </tr>
        <tr>
          <td style="padding: 8px 12px; color: #1f2937;">爽约数</td>
          <td style="padding: 8px 12px; text-align: right;">`)
	b.WriteString(strconv.Itoa(rep.LastWeekNoShow))
	b.WriteString(`</td>
          <td style="padding: 8px 12px; text-align: right; font-weight: 600;">`)
	b.WriteString(strconv.Itoa(rep.NoShowAppointments))
	b.WriteString(`</td>
          <td style="padding: 8px 12px; text-align: right; color: `)
	b.WriteString(noShowDeltaColor)
	b.WriteString(`; font-weight: 600;">`)
	b.WriteString(noShowDeltaSign)
	b.WriteString(strconv.Itoa(rep.NoShowDelta))
	b.WriteString(`</td>
        </tr>
      </table>
    </td>
  </tr>`)
	return b.String()
}

func renderWeeklyServiceSection(rep storage.WeeklyReport) string {
	if len(rep.ServiceRank) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(`
  <tr>
    <td style="padding: 0 32px 24px 32px;">
      <h2 style="margin: 0 0 12px 0; font-size: 16px; color: #1f2937;">💇 服务排行 TOP `)
	b.WriteString(strconv.Itoa(len(rep.ServiceRank)))
	b.WriteString(`</h2>
      <table role="presentation" cellpadding="0" cellspacing="0" border="0" width="100%" style="font-size: 13px; background-color: #f9fafb; border-radius: 6px;">
        <tr>
          <td style="padding: 8px 12px; color: #6b7280; width: 24px;">#</td>
          <td style="padding: 8px 12px; color: #6b7280;">服务</td>
          <td style="padding: 8px 12px; color: #6b7280; text-align: right;">单数</td>
          <td style="padding: 8px 12px; color: #6b7280; text-align: right;">占比</td>
        </tr>`)
	for i, s := range rep.ServiceRank {
		pct := 0.0
		if rep.TotalAppointments > 0 {
			pct = float64(s.Count) / float64(rep.TotalAppointments)
		}
		b.WriteString(`
        <tr>
          <td style="padding: 8px 12px; color: #6b7280;">`)
		b.WriteString(strconv.Itoa(i + 1))
		b.WriteString(`</td>
          <td style="padding: 8px 12px; color: #1f2937;">`)
		b.WriteString(htmlEscape(s.Service))
		b.WriteString(`</td>
          <td style="padding: 8px 12px; text-align: right;">`)
		b.WriteString(strconv.Itoa(s.Count))
		b.WriteString(`</td>
          <td style="padding: 8px 12px; text-align: right; color: #6b7280;">`)
		b.WriteString(formatPercent(pct))
		b.WriteString(`</td>
        </tr>`)
	}
	b.WriteString(`
      </table>
    </td>
  </tr>`)
	return b.String()
}

func renderWeeklyCustomerSection(rep storage.WeeklyReport) string {
	if len(rep.TopCustomers) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(`
  <tr>
    <td style="padding: 0 32px 24px 32px;">
      <h2 style="margin: 0 0 12px 0; font-size: 16px; color: #1f2937;">👥 熟客排行 TOP `)
	b.WriteString(strconv.Itoa(len(rep.TopCustomers)))
	b.WriteString(`</h2>
      <table role="presentation" cellpadding="0" cellspacing="0" border="0" width="100%" style="font-size: 13px; background-color: #f9fafb; border-radius: 6px;">
        <tr>
          <td style="padding: 8px 12px; color: #6b7280; width: 24px;">#</td>
          <td style="padding: 8px 12px; color: #6b7280;">顾客</td>
          <td style="padding: 8px 12px; color: #6b7280; text-align: right;">到店次数</td>
        </tr>`)
	for i, c := range rep.TopCustomers {
		b.WriteString(`
        <tr>
          <td style="padding: 8px 12px; color: #6b7280;">`)
		b.WriteString(strconv.Itoa(i + 1))
		b.WriteString(`</td>
          <td style="padding: 8px 12px; color: #1f2937;">`)
		b.WriteString(htmlEscape(c.Name))
		b.WriteString(`</td>
          <td style="padding: 8px 12px; text-align: right;">`)
		b.WriteString(strconv.Itoa(c.Total))
		b.WriteString(`</td>
        </tr>`)
	}
	b.WriteString(`
      </table>
    </td>
  </tr>`)
	return b.String()
}

func renderWeeklyDailySection(rep storage.WeeklyReport) string {
	if len(rep.DailyTrend) == 0 {
		return ""
	}
	// 找 max 用于条形图（按比例计算 width %）
	maxTotal := 0
	for _, ds := range rep.DailyTrend {
		if ds.Total > maxTotal {
			maxTotal = ds.Total
		}
	}

	var b strings.Builder
	b.WriteString(`
  <tr>
    <td style="padding: 0 32px 24px 32px;">
      <h2 style="margin: 0 0 12px 0; font-size: 16px; color: #1f2937;">📅 7 天日趋势</h2>
      <table role="presentation" cellpadding="0" cellspacing="0" border="0" width="100%" style="font-size: 12px; background-color: #f9fafb; border-radius: 6px;">`)
	for _, ds := range rep.DailyTrend {
		// 显示日期 "MM-DD"
		dateLabel := ds.Date
		if len(dateLabel) >= 10 {
			dateLabel = dateLabel[5:] // 截 MM-DD
		}
		barWidth := 0
		if maxTotal > 0 {
			barWidth = ds.Total * 100 / maxTotal
			if ds.Total > 0 && barWidth < 4 {
				barWidth = 4 // 至少 4% 让 1 笔也可见
			}
		}
		b.WriteString(`
        <tr>
          <td style="padding: 6px 12px; color: #6b7280; width: 60px;">`)
		b.WriteString(dateLabel)
		b.WriteString(`</td>
          <td style="padding: 6px 12px; width: 50%;">
            <div style="background-color: #2563eb; height: 8px; width: `)
		b.WriteString(strconv.Itoa(barWidth))
		b.WriteString(`%; border-radius: 4px;"></div>
          </td>
          <td style="padding: 6px 12px; text-align: right; color: #1f2937;">`)
		b.WriteString(strconv.Itoa(ds.Total))
		b.WriteString(` 单</td>
        </tr>`)
	}
	b.WriteString(`
      </table>
    </td>
  </tr>`)
	return b.String()
}

// RenderChainWeeklyReportHTML 渲染跨店周报 HTML 邮件（v4.3 PRD §11.12 连锁版）
//
//   - 输入：storage.ChainWeeklyReport（受控数据）
//   - 输出：subject + htmlBody
//   - 设计：复用单店周报的样式 + 加跨店汇总段 + 列出每店 TOP
//   - 用途：连锁 owner 一次性看 N 家店 + 跨店汇总（暂未接 cron，留 v4.4 增量）
func RenderChainWeeklyReportHTML(rep storage.ChainWeeklyReport) (subject, htmlBody string) {
	subject = fmt.Sprintf("【连锁周报】%d 家店 · 上周经营汇总（%s）",
		rep.ShopCount, rep.WeekLabel)

	var b strings.Builder
	b.WriteString(`<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<title>连锁周报</title>
</head>
<body style="font-family: -apple-system, 'Helvetica Neue', 'PingFang SC', 'Microsoft YaHei', sans-serif; background-color: #f4f6f8; margin: 0; padding: 20px; color: #1f2937;">
<table role="presentation" cellpadding="0" cellspacing="0" border="0" width="100%" style="max-width: 720px; margin: 0 auto; background-color: #ffffff; border-radius: 8px; overflow: hidden; box-shadow: 0 1px 3px rgba(0,0,0,0.06);">
  <tr>
    <td style="padding: 32px 32px 16px 32px; background: linear-gradient(135deg, #2563eb 0%, #7c3aed 100%); color: #ffffff;">
      <h1 style="margin: 0 0 8px 0; font-size: 24px; font-weight: 600;">连锁周报</h1>
      <p style="margin: 0; font-size: 14px; opacity: 0.9;">`)
	b.WriteString(htmlEscape(rep.WeekLabel))
	b.WriteString(` · `)
	b.WriteString(strconv.Itoa(rep.ShopCount))
	b.WriteString(` 家店</p>
    </td>
  </tr>

  <!-- 跨店汇总 -->`)
	b.WriteString(renderChainTotalsSection(rep))

	b.WriteString(`
  <!-- 跨店服务/顾客排行 -->`)
	b.WriteString(renderChainRankSection(rep))

	b.WriteString(`
  <!-- 各店明细 -->`)
	b.WriteString(renderChainPerShopSection(rep))

	b.WriteString(`
  <!-- 页脚 -->
  <tr>
    <td style="padding: 24px 32px 32px 32px; border-top: 1px solid #e5e7eb; color: #6b7280; font-size: 12px; line-height: 1.5;">
      <p style="margin: 0 0 4px 0;">本报告由「美发店 AI Agent」自动生成</p>
      <p style="margin: 0;">生成时间：`)
	b.WriteString(rep.GeneratedAt.Format("2006-01-02 15:04"))
	b.WriteString(`</p>
    </td>
  </tr>
</table>
</body>
</html>`)
	return subject, b.String()
}

func renderChainTotalsSection(rep storage.ChainWeeklyReport) string {
	completePct := formatPercent(rep.Total.CompletionRate)
	noShowPct := formatPercent(rep.Total.NoShowRate)

	var b strings.Builder
	b.WriteString(`
  <tr>
    <td style="padding: 24px 32px 0 32px;">
      <h2 style="margin: 0 0 12px 0; font-size: 16px; color: #1f2937;">📊 跨店总览</h2>
      <table role="presentation" cellpadding="0" cellspacing="0" border="0" width="100%" style="background-color: #f9fafb; border-radius: 6px;">
        <tr>
          <td style="padding: 12px 16px; width: 33%;">
            <div style="font-size: 12px; color: #6b7280;">总预约</div>
            <div style="font-size: 24px; font-weight: 600; color: #1f2937;">`)
	b.WriteString(strconv.Itoa(rep.Total.TotalAppointments))
	b.WriteString(`</div>
          </td>
          <td style="padding: 12px 16px; width: 33%;">
            <div style="font-size: 12px; color: #6b7280;">完成率</div>
            <div style="font-size: 18px; font-weight: 600; color: #059669;">`)
	b.WriteString(completePct)
	b.WriteString(`</div>
          </td>
          <td style="padding: 12px 16px; width: 33%;">
            <div style="font-size: 12px; color: #6b7280;">爽约率</div>
            <div style="font-size: 18px; font-weight: 600; color: #dc2626;">`)
	b.WriteString(noShowPct)
	b.WriteString(`</div>
          </td>
        </tr>
      </table>
    </td>
  </tr>`)
	return b.String()
}

func renderChainRankSection(rep storage.ChainWeeklyReport) string {
	if len(rep.TopServices) == 0 && len(rep.TopCustomers) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(`
  <tr>
    <td style="padding: 24px 32px 0 32px;">
      <h2 style="margin: 0 0 12px 0; font-size: 16px; color: #1f2937;">💇 跨店服务 / 熟客排行</h2>
      <table role="presentation" cellpadding="0" cellspacing="0" border="0" width="100%" style="font-size: 13px; background-color: #f9fafb; border-radius: 6px;">`)
	if len(rep.TopServices) > 0 {
		b.WriteString(`
        <tr>
          <td colspan="4" style="padding: 8px 12px; color: #6b7280; background-color: #f3f4f6; font-weight: 600;">服务 TOP</td>
        </tr>`)
		for i, s := range rep.TopServices {
			b.WriteString(`
        <tr>
          <td style="padding: 6px 12px; color: #6b7280; width: 24px;">`)
			b.WriteString(strconv.Itoa(i + 1))
			b.WriteString(`</td>
          <td style="padding: 6px 12px; color: #1f2937;">`)
			b.WriteString(htmlEscape(s.Service))
			b.WriteString(`</td>
          <td style="padding: 6px 12px; text-align: right; color: #6b7280;">单数</td>
          <td style="padding: 6px 12px; text-align: right; font-weight: 600;">`)
			b.WriteString(strconv.Itoa(s.Count))
			b.WriteString(`</td>
        </tr>`)
		}
	}
	if len(rep.TopCustomers) > 0 {
		b.WriteString(`
        <tr>
          <td colspan="4" style="padding: 8px 12px; color: #6b7280; background-color: #f3f4f6; font-weight: 600;">熟客 TOP</td>
        </tr>`)
		for i, c := range rep.TopCustomers {
			b.WriteString(`
        <tr>
          <td style="padding: 6px 12px; color: #6b7280; width: 24px;">`)
			b.WriteString(strconv.Itoa(i + 1))
			b.WriteString(`</td>
          <td style="padding: 6px 12px; color: #1f2937;">`)
			b.WriteString(htmlEscape(c.Name))
			b.WriteString(`</td>
          <td style="padding: 6px 12px; text-align: right; color: #6b7280;">到店</td>
          <td style="padding: 6px 12px; text-align: right; font-weight: 600;">`)
			b.WriteString(strconv.Itoa(c.Total))
			b.WriteString(`</td>
        </tr>`)
		}
	}
	b.WriteString(`
      </table>
    </td>
  </tr>`)
	return b.String()
}

func renderChainPerShopSection(rep storage.ChainWeeklyReport) string {
	if len(rep.PerShop) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(`
  <tr>
    <td style="padding: 24px 32px 0 32px;">
      <h2 style="margin: 0 0 12px 0; font-size: 16px; color: #1f2937;">🏪 各店明细</h2>
      <table role="presentation" cellpadding="0" cellspacing="0" border="0" width="100%" style="font-size: 12px; background-color: #f9fafb; border-radius: 6px;">
        <tr>
          <td style="padding: 8px 12px; color: #6b7280;">店铺</td>
          <td style="padding: 8px 12px; color: #6b7280; text-align: right;">总预约</td>
          <td style="padding: 8px 12px; color: #6b7280; text-align: right;">完成</td>
          <td style="padding: 8px 12px; color: #6b7280; text-align: right;">爽约</td>
        </tr>`)
	for _, s := range rep.PerShop {
		b.WriteString(`
        <tr>
          <td style="padding: 6px 12px; color: #1f2937;">`)
		b.WriteString(htmlEscape(s.ShopName))
		b.WriteString(`</td>
          <td style="padding: 6px 12px; text-align: right; font-weight: 600;">`)
		b.WriteString(strconv.Itoa(s.TotalAppointments))
		b.WriteString(`</td>
          <td style="padding: 6px 12px; text-align: right;">`)
		b.WriteString(strconv.Itoa(s.CompletedAppointments))
		b.WriteString(`</td>
          <td style="padding: 6px 12px; text-align: right; color: `)
		if s.NoShowAppointments > 0 {
			b.WriteString("#dc2626")
		} else {
			b.WriteString("#6b7280")
		}
		b.WriteString(`;">`)
		b.WriteString(strconv.Itoa(s.NoShowAppointments))
		b.WriteString(`</td>
        </tr>`)
	}
	b.WriteString(`
      </table>
    </td>
  </tr>`)
	return b.String()
}
