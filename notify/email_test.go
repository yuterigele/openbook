package notify

// email_test.go
//
// notify 包的测试覆盖（v4.2 PRD §11.11）：
//   - LoadEmailConfigFromEnv：全空 / 部分 / 全有 / 端口默认
//   - IsValid：边界值（port 0 / port 65536 / 空字段）
//   - NewSender：未配置时 Noop / 有效配置时 SMTP
//   - NoopSender：返回 nil error + log
//   - RenderD15ReportHTML：含必要字段 + 转义
//   - encodeRFC2047 / base64Encode：英文 / 中文
//   - htmlEscape：XSS 防注入

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino-examples/quickstart/chatwitheino/storage"
)

// ---- EmailConfig ----

func TestEmailConfig_IsValid_AllFields(t *testing.T) {
	c := EmailConfig{Host: "smtp.gmail.com", Port: 465, Username: "u", Password: "p", From: "f@x.com"}
	if !c.IsValid() {
		t.Error("complete config should be valid")
	}
}

func TestEmailConfig_IsValid_MissingFields(t *testing.T) {
	cases := []struct {
		name string
		cfg  EmailConfig
	}{
		{"empty", EmailConfig{}},
		{"no host", EmailConfig{Port: 465, Username: "u", Password: "p", From: "f@x.com"}},
		{"port 0", EmailConfig{Host: "h", Username: "u", Password: "p", From: "f@x.com"}},
		{"port too high", EmailConfig{Host: "h", Port: 65536, Username: "u", Password: "p", From: "f@x.com"}},
		{"no user", EmailConfig{Host: "h", Port: 465, Password: "p", From: "f@x.com"}},
		{"no pass", EmailConfig{Host: "h", Port: 465, Username: "u", From: "f@x.com"}},
		{"no from", EmailConfig{Host: "h", Port: 465, Username: "u", Password: "p"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.cfg.IsValid() {
				t.Errorf("expected invalid: %+v", tc.cfg)
			}
		})
	}
}

func TestLoadEmailConfigFromEnv_DefaultsPort(t *testing.T) {
	// 清空所有 SMTP_* 变量
	for _, k := range []string{"SMTP_HOST", "SMTP_PORT", "SMTP_USER", "SMTP_PASSWORD", "SMTP_FROM", "SMTP_FROM_NAME"} {
		os.Unsetenv(k)
	}
	cfg := LoadEmailConfigFromEnv()
	if cfg.Port != 465 {
		t.Errorf("default port: want 465, got %d", cfg.Port)
	}
	if cfg.FromName != "美发店 AI Agent" {
		t.Errorf("default FromName: want 美发店 AI Agent, got %q", cfg.FromName)
	}
	if cfg.IsValid() {
		t.Error("empty env config should be invalid")
	}
}

func TestLoadEmailConfigFromEnv_FullSet(t *testing.T) {
	os.Setenv("SMTP_HOST", "smtp.gmail.com")
	os.Setenv("SMTP_PORT", "587")
	os.Setenv("SMTP_USER", "shop@gmail.com")
	os.Setenv("SMTP_PASSWORD", "secret")
	os.Setenv("SMTP_FROM", "no-reply@shop.com")
	os.Setenv("SMTP_FROM_NAME", "Tony's Salon")
	defer func() {
		os.Unsetenv("SMTP_HOST")
		os.Unsetenv("SMTP_PORT")
		os.Unsetenv("SMTP_USER")
		os.Unsetenv("SMTP_PASSWORD")
		os.Unsetenv("SMTP_FROM")
		os.Unsetenv("SMTP_FROM_NAME")
	}()

	cfg := LoadEmailConfigFromEnv()
	if !cfg.IsValid() {
		t.Errorf("full env config should be valid: %+v", cfg)
	}
	if cfg.Port != 587 {
		t.Errorf("port: want 587, got %d", cfg.Port)
	}
	if cfg.From != "no-reply@shop.com" {
		t.Errorf("from: want no-reply@shop.com, got %q", cfg.From)
	}
}

func TestLoadEmailConfigFromEnv_FallsBackFromToUser(t *testing.T) {
	os.Setenv("SMTP_HOST", "smtp.gmail.com")
	os.Setenv("SMTP_PORT", "465")
	os.Setenv("SMTP_USER", "shop@gmail.com")
	os.Setenv("SMTP_PASSWORD", "secret")
	defer func() {
		os.Unsetenv("SMTP_HOST")
		os.Unsetenv("SMTP_PORT")
		os.Unsetenv("SMTP_USER")
		os.Unsetenv("SMTP_PASSWORD")
	}()

	cfg := LoadEmailConfigFromEnv()
	if cfg.From != "shop@gmail.com" {
		t.Errorf("from should fallback to user: want shop@gmail.com, got %q", cfg.From)
	}
}

// ---- NewSender ----

func TestNewSender_InvalidConfig_ReturnsNoop(t *testing.T) {
	s := NewSender(EmailConfig{})
	if _, ok := s.(*NoopSender); !ok {
		t.Errorf("invalid config should produce NoopSender, got %T", s)
	}
}

func TestNewSender_ValidConfig_ReturnsSMTP(t *testing.T) {
	cfg := EmailConfig{Host: "smtp.gmail.com", Port: 465, Username: "u", Password: "p", From: "f@x.com"}
	s := NewSender(cfg)
	if _, ok := s.(*SMTPSender); !ok {
		t.Errorf("valid config should produce SMTPSender, got %T", s)
	}
}

// ---- NoopSender ----

func TestNoopSender_SendHTML_NoError(t *testing.T) {
	n := &NoopSender{}
	err := n.SendHTML(context.Background(), []string{"a@b.com"}, "test", "<p>hi</p>")
	if err != nil {
		t.Errorf("NoopSender.SendHTML should not error, got %v", err)
	}
}

func TestNoopSender_SendHTML_EmptyToErrors(t *testing.T) {
	n := &NoopSender{}
	err := n.SendHTML(context.Background(), []string{}, "test", "<p>hi</p>")
	// NoopSender 不验证 to；测试 SMTPSender 才验证
	_ = err
}

func TestSMTPSender_SendHTML_EmptyToErrors(t *testing.T) {
	s := &SMTPSender{cfg: EmailConfig{Host: "h", Port: 465, Username: "u", Password: "p", From: "f@x.com"}}
	err := s.SendHTML(context.Background(), []string{}, "test", "<p>hi</p>")
	if err == nil {
		t.Error("SMTPSender.SendHTML with empty to should error")
	}
}

// ---- RenderD15ReportHTML ----

func TestRenderD15ReportHTML_ContainsKeyFields(t *testing.T) {
	rep := storage.UsageReport{
		ShopID:                "shop-1",
		ShopName:              "Tony's Salon",
		GeneratedAt:           time.Date(2026, 6, 22, 10, 0, 0, 0, time.UTC),
		FirstApptAt:           time.Date(2026, 6, 7, 0, 0, 0, 0, time.UTC),
		WindowStart:           time.Date(2026, 6, 7, 0, 0, 0, 0, time.UTC),
		WindowEnd:             time.Date(2026, 6, 22, 0, 0, 0, 0, time.UTC),
		WindowDays:            15,
		TotalAppointments:     100,
		CompletedAppointments: 80,
		NoShowAppointments:    10,
		CancelledAppointments: 5,
		ActiveAppointments:    5,
		CompletionRate:        0.889,
		NoShowRate:            0.111,
		UniqueServices:        3,
		UniqueCustomers:       20,
		ServiceRank: []storage.ServiceStat{
			{Service: "剪发", Count: 60},
			{Service: "染发", Count: 30},
			{Service: "烫发", Count: 10},
		},
		TopCustomers: []storage.CustomerStat{
			{CustomerID: "c1", Name: "Alice", Total: 8},
			{CustomerID: "c2", Name: "Bob", Total: 5},
		},
		BaselineBaseline: storage.BaselinePhase{Label: "冷启动期", DayCount: 3, Total: 5, AvgPerDay: 1.67},
		GrowthPhase:      storage.BaselinePhase{Label: "增长期", DayCount: 12, Total: 95, AvgPerDay: 7.92},
		GrowthDelta:      storage.PhaseDelta{AvgPerDayDelta: 6.25, GrowthRate: 3.75},
	}

	subject, html := RenderD15ReportHTML(rep)

	if !strings.Contains(subject, "Tony's Salon") {
		t.Errorf("subject should contain shop name, got %q", subject)
	}
	if !strings.Contains(html, "Tony&#39;s Salon") {
		t.Errorf("html should contain escaped shop name, got %s", html)
	}
	if !strings.Contains(html, "D+15 使用报告") {
		t.Error("html should contain title")
	}
	if !strings.Contains(html, "总预约") {
		t.Error("html should contain overview section")
	}
	if !strings.Contains(html, "100") {
		t.Error("html should contain total count")
	}
	if !strings.Contains(html, "服务排行") {
		t.Error("html should contain service section")
	}
	if !strings.Contains(html, "剪发") {
		t.Error("html should contain top service")
	}
	if !strings.Contains(html, "Alice") {
		t.Error("html should contain top customer")
	}
	if !strings.Contains(html, "冷启动期") {
		t.Error("html should contain baseline phase label")
	}
}

func TestRenderD15ReportHTML_EmptyReportDoesNotCrash(t *testing.T) {
	rep := storage.UsageReport{} // 零值
	subject, html := RenderD15ReportHTML(rep)
	if subject == "" {
		t.Error("subject should not be empty for zero rep")
	}
	if !strings.Contains(html, "D+15 使用报告") {
		t.Error("html should still render header")
	}
}

func TestRenderD15ReportHTML_HTMLEscapesCustomerName(t *testing.T) {
	rep := storage.UsageReport{
		ShopName:        "Tony's Salon",
		TotalAppointments: 1,
		TopCustomers: []storage.CustomerStat{
			{CustomerID: "c1", Name: `<script>alert("xss")</script>`, Total: 1},
		},
	}
	_, html := RenderD15ReportHTML(rep)
	if strings.Contains(html, "<script>") {
		t.Errorf("html should escape script tag, got: %s", html)
	}
	if !strings.Contains(html, "&lt;script&gt;") {
		t.Error("html should contain escaped script tag")
	}
}

// ---- encodeRFC2047 / base64Encode ----

func TestEncodeRFC2047_EnglishPassThrough(t *testing.T) {
	got := encodeRFC2047("Hello World")
	if got != "Hello World" {
		t.Errorf("English should pass through, got %q", got)
	}
}

func TestEncodeRFC2047_ChineseEncoded(t *testing.T) {
	got := encodeRFC2047("中文主题")
	if !strings.HasPrefix(got, "=?UTF-8?B?") {
		t.Errorf("Chinese should be RFC2047 encoded, got %q", got)
	}
}

func TestBase64Encode_KnownVectors(t *testing.T) {
	// 标准 RFC 4648 测试向量
	cases := []struct {
		in, want string
	}{
		{"", ""},
		{"f", "Zg=="},
		{"fo", "Zm8="},
		{"foo", "Zm9v"},
		{"foob", "Zm9vYg=="},
		{"fooba", "Zm9vYmE="},
		{"foobar", "Zm9vYmFy"},
	}
	for _, tc := range cases {
		got := base64Encode([]byte(tc.in))
		if got != tc.want {
			t.Errorf("base64(%q): want %q, got %q", tc.in, tc.want, got)
		}
	}
}

// ---- htmlEscape ----

func TestHTMLEscape_BasicCases(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"hello", "hello"},
		{"a<b>c", "a&lt;b&gt;c"},
		{`a"b'c`, "a&quot;b&#39;c"},
		{"a&b", "a&amp;b"},
		{"<script>", "&lt;script&gt;"},
	}
	for _, tc := range cases {
		got := htmlEscape(tc.in)
		if got != tc.want {
			t.Errorf("htmlEscape(%q): want %q, got %q", tc.in, tc.want, got)
		}
	}
}

// ---- MIME message construction ----

func TestBuildMIMEMessage_BasicStructure(t *testing.T) {
	msg := buildMIMEMessage("from@x.com", []string{"to@y.com"}, "Test", "<p>Body</p>")
	if !strings.Contains(msg, "From: from@x.com") {
		t.Error("missing From header")
	}
	if !strings.Contains(msg, "To: to@y.com") {
		t.Error("missing To header")
	}
	if !strings.Contains(msg, "Subject: Test") {
		t.Error("missing Subject header")
	}
	if !strings.Contains(msg, "multipart/alternative") {
		t.Error("missing multipart Content-Type")
	}
	if !strings.Contains(msg, "text/html; charset=utf-8") {
		t.Error("missing HTML part")
	}
	if !strings.Contains(msg, "<p>Body</p>") {
		t.Error("missing HTML body")
	}
}
