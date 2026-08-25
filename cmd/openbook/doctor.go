package main

import (
	"fmt"
	"io"
	"strings"
)

// DoctorCheck 是一次不泄露敏感值的配置检查结果。
type DoctorCheck struct {
	Name   string
	Status string
	Detail string
}

// Diagnose 根据环境变量生成安全诊断结果，不连接外部服务也不输出密钥。
func Diagnose(lookup func(string) string) []DoctorCheck {
	if lookup == nil {
		lookup = func(string) string { return "" }
	}
	production := strings.EqualFold(strings.TrimSpace(lookup("APP_ENV")), "production")
	hasDatabase := lookup("MYSQL_DSN") != "" || (lookup("MYSQL_HOST") != "" && lookup("MYSQL_USER") != "" && lookup("MYSQL_DB") != "")
	hasDatabasePassword := lookup("MYSQL_DSN") != "" || lookup("MYSQL_PASS") != ""
	modelChain := strings.TrimSpace(lookup("OPENBOOK_LLM_CHAIN"))
	modelConfigured := modelChain == "stub" || (strings.Contains(modelChain, "deepseek") && lookup("DEEPSEEK_API_KEY") != "") || (strings.Contains(modelChain, "openai") && lookup("OPENAI_API_KEY") != "") || (strings.Contains(modelChain, "ark") && lookup("ARK_API_KEY") != "")
	wecomConfigured := lookup("WECOM_CORP_ID") != "" && lookup("WECOM_AGENT_ID") != "" && lookup("WECOM_SECRET") != "" && lookup("WECOM_TOKEN") != "" && lookup("WECOM_ENCODING_AES_KEY") != ""
	adminPassword := lookup("DEFAULT_ADMIN_PASSWORD")
	platformAdminPassword := lookup("DEFAULT_PLATFORM_ADMIN_PASSWORD")
	jwtSecret := lookup("JWT_SECRET")
	replyMode := strings.TrimSpace(strings.ToLower(lookup("AGENT_REPLY_MODE")))

	checks := []DoctorCheck{
		{Name: "database", Status: "FAIL", Detail: "MYSQL_DSN 或 MYSQL_HOST/MYSQL_USER/MYSQL_DB 未配置"},
		{Name: "redis", Status: "WARN", Detail: "REDIS_ADDR 未配置；本地锁降级可能可用，生产环境需显式配置"},
		{Name: "model", Status: "FAIL", Detail: "OPENBOOK_LLM_CHAIN 未配置或对应模型凭据不存在"},
		{Name: "channel", Status: "WARN", Detail: "企业微信凭据未完整配置，仅适合本地或 Mock 模式"},
		{Name: "reply_mode", Status: "WARN", Detail: "AGENT_REPLY_MODE 未设置为 real；消息只适合本地或 Mock 模式"},
		{Name: "booking_runtime", Status: "WARN", Detail: "使用 legacy 工具路径；未启用 v1alpha1 Application 灰度"},
		{Name: "admin_password", Status: "WARN", Detail: "未检测到 DEFAULT_ADMIN_PASSWORD，请确认部署时已设置强密码"},
		{Name: "platform_admin_password", Status: "WARN", Detail: "未检测到 DEFAULT_PLATFORM_ADMIN_PASSWORD，请确认部署时已设置强密码"},
		{Name: "jwt_secret", Status: "WARN", Detail: "未检测到 JWT_SECRET，请确认部署时已设置会话签名密钥"},
	}
	if hasDatabase {
		if production && !hasDatabasePassword {
			checks[0] = DoctorCheck{Name: "database", Status: "FAIL", Detail: "生产环境数据库密码未配置"}
		} else {
			checks[0] = DoctorCheck{Name: "database", Status: "PASS", Detail: "数据库连接配置已提供"}
		}
	}
	if lookup("REDIS_ADDR") != "" {
		checks[1] = DoctorCheck{Name: "redis", Status: "PASS", Detail: "Redis 地址已提供"}
	} else if production {
		checks[1] = DoctorCheck{Name: "redis", Status: "FAIL", Detail: "生产环境必须配置 REDIS_ADDR"}
	}
	if modelConfigured {
		if production && modelChain == "stub" {
			checks[2] = DoctorCheck{Name: "model", Status: "FAIL", Detail: "生产环境不能使用 stub 模型链"}
		} else {
			checks[2] = DoctorCheck{Name: "model", Status: "PASS", Detail: "模型链配置已提供"}
		}
	} else if production {
		checks[2] = DoctorCheck{Name: "model", Status: "FAIL", Detail: "生产环境未配置可用的真实模型链"}
	}
	if wecomConfigured {
		checks[3] = DoctorCheck{Name: "channel", Status: "PASS", Detail: "企业微信核心凭据已完整提供"}
	} else if production {
		checks[3] = DoctorCheck{Name: "channel", Status: "FAIL", Detail: "生产环境必须完整配置企业微信凭据"}
	}
	if replyMode == "real" {
		checks[4] = DoctorCheck{Name: "reply_mode", Status: "PASS", Detail: "已启用 real 回复模式"}
	} else if production {
		checks[4] = DoctorCheck{Name: "reply_mode", Status: "FAIL", Detail: "生产环境必须将 AGENT_REPLY_MODE 设置为 real"}
	}
	if lookup("BOOKING_APPLICATION_RUNTIME") == "1" {
		checks[5] = DoctorCheck{Name: "booking_runtime", Status: "PASS", Detail: "已启用 v1alpha1 Application 灰度路径"}
	}
	if adminPassword != "" && !isWeakPlaceholder(adminPassword) {
		checks[6] = DoctorCheck{Name: "admin_password", Status: "PASS", Detail: "默认管理员密码已提供且不是示例占位值"}
	} else if production {
		checks[6] = DoctorCheck{Name: "admin_password", Status: "FAIL", Detail: "生产环境必须设置非示例默认管理员密码"}
	}
	if platformAdminPassword != "" && !isWeakPlaceholder(platformAdminPassword) {
		checks[7] = DoctorCheck{Name: "platform_admin_password", Status: "PASS", Detail: "平台管理员密码已提供且不是示例占位值"}
	} else if production {
		checks[7] = DoctorCheck{Name: "platform_admin_password", Status: "FAIL", Detail: "生产环境必须设置非示例平台管理员密码"}
	}
	if jwtSecret != "" && !isWeakPlaceholder(jwtSecret) {
		checks[8] = DoctorCheck{Name: "jwt_secret", Status: "PASS", Detail: "JWT_SECRET 已提供且不是示例占位值"}
	} else if production {
		checks[8] = DoctorCheck{Name: "jwt_secret", Status: "FAIL", Detail: "生产环境必须设置非示例 JWT_SECRET"}
	}
	return checks
}

// RunDoctor 输出诊断状态；只有 FAIL 才返回非零退出码。
func RunDoctor(writer io.Writer, lookup func(string) string) int {
	checks := Diagnose(lookup)
	failures := 0
	for _, check := range checks {
		fmt.Fprintf(writer, "[%s] %-16s %s\n", check.Status, check.Name, check.Detail)
		if check.Status == "FAIL" {
			failures++
		}
	}
	if failures > 0 {
		fmt.Fprintf(writer, "doctor: %d 个阻断项\n", failures)
		return 1
	}
	fmt.Fprintln(writer, "doctor: 未发现阻断项")
	return 0
}

// RunVersion 输出可由构建流水线通过 -ldflags 注入的版本信息。
func RunVersion(writer io.Writer) {
	fmt.Fprintf(writer, "version=%s\ncommit=%s\nbuild_time=%s\n", version, commit, buildTime)
}

func isWeakPlaceholder(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	return value == "admin123" || strings.Contains(value, "change-me") || strings.Contains(value, "replace-with") || strings.Contains(value, "your-")
}
