package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiagnoseConfiguredDevelopmentEnvironment(t *testing.T) {
	env := map[string]string{
		"MYSQL_DSN":                   "configured",
		"REDIS_ADDR":                  "127.0.0.1:6379",
		"OPENBOOK_LLM_CHAIN":          "stub",
		"BOOKING_APPLICATION_RUNTIME": "1",
		"DEFAULT_ADMIN_PASSWORD":      "a-long-random-password",
		"WECOM_CORP_ID":               "corp",
		"WECOM_AGENT_ID":              "agent",
		"WECOM_SECRET":                "secret",
		"WECOM_TOKEN":                 "token",
		"WECOM_ENCODING_AES_KEY":      "aes-key",
	}
	checks := Diagnose(func(key string) string { return env[key] })
	for _, check := range checks {
		if check.Status == "FAIL" {
			t.Fatalf("configured environment should not fail: %+v", check)
		}
	}
}

func TestRunDoctorDoesNotPrintSecretValues(t *testing.T) {
	secret := "never-print-this-secret"
	env := map[string]string{
		"MYSQL_DSN":              "configured",
		"OPENBOOK_LLM_CHAIN":     "stub",
		"DEFAULT_ADMIN_PASSWORD": secret,
	}
	var output bytes.Buffer
	if code := RunDoctor(&output, func(key string) string { return env[key] }); code != 0 {
		t.Fatalf("stub doctor should have no blocking failures: %d", code)
	}
	if strings.Contains(output.String(), secret) {
		t.Fatalf("doctor output leaked a secret: %s", output.String())
	}
}

func TestRunDoctorRejectsUnsafeProductionConfiguration(t *testing.T) {
	env := map[string]string{
		"APP_ENV":                         "production",
		"MYSQL_DSN":                       "configured",
		"OPENBOOK_LLM_CHAIN":              "stub",
		"AGENT_REPLY_MODE":                "mock",
		"DEFAULT_ADMIN_PASSWORD":          "change-me-before-exposing",
		"DEFAULT_PLATFORM_ADMIN_PASSWORD": "",
		"JWT_SECRET":                      "",
	}
	var output bytes.Buffer
	if code := RunDoctor(&output, func(key string) string { return env[key] }); code != 1 {
		t.Fatalf("unsafe production configuration should fail: code=%d output=%s", code, output.String())
	}
	for _, expected := range []string{"生产环境必须配置 REDIS_ADDR", "生产环境不能使用 stub", "生产环境必须完整配置企业微信凭据", "生产环境必须将 AGENT_REPLY_MODE 设置为 real", "生产环境必须设置非示例默认管理员密码", "生产环境必须设置非示例 JWT_SECRET"} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("doctor output missing %q: %s", expected, output.String())
		}
	}
}

func TestRunDoctorAcceptsSafeProductionConfigurationWithoutPrintingSecrets(t *testing.T) {
	secrets := map[string]string{
		"APP_ENV":                         "production",
		"MYSQL_DSN":                       "user:db-secret@tcp(mysql.internal:3306)/booking",
		"REDIS_ADDR":                      "redis.internal:6379",
		"OPENBOOK_LLM_CHAIN":              "deepseek",
		"DEEPSEEK_API_KEY":                "model-secret",
		"AGENT_REPLY_MODE":                "real",
		"DEFAULT_ADMIN_PASSWORD":          "admin-random-value",
		"DEFAULT_PLATFORM_ADMIN_PASSWORD": "platform-random-value",
		"JWT_SECRET":                      "jwt-random-value",
		"WECOM_CORP_ID":                   "corp",
		"WECOM_AGENT_ID":                  "agent",
		"WECOM_SECRET":                    "wecom-secret",
		"WECOM_TOKEN":                     "wecom-token",
		"WECOM_ENCODING_AES_KEY":          "wecom-aes-key",
	}
	var output bytes.Buffer
	if code := RunDoctor(&output, func(key string) string { return secrets[key] }); code != 0 {
		t.Fatalf("safe production configuration should pass: code=%d output=%s", code, output.String())
	}
	for _, secret := range []string{"db-secret", "model-secret", "wecom-secret", "jwt-random-value"} {
		if strings.Contains(output.String(), secret) {
			t.Fatalf("doctor output leaked %q: %s", secret, output.String())
		}
	}
}

func TestRunVersion(t *testing.T) {
	originalVersion, originalCommit, originalBuildTime := version, commit, buildTime
	defer func() { version, commit, buildTime = originalVersion, originalCommit, originalBuildTime }()
	version, commit, buildTime = "v1.0.0-test", "abc123", "2026-08-25T00:00:00Z"
	var output bytes.Buffer
	RunVersion(&output)
	for _, expected := range []string{"version=v1.0.0-test", "commit=abc123", "build_time=2026-08-25T00:00:00Z"} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("version output missing %q: %s", expected, output.String())
		}
	}
}

func TestInitializeEnvFileGeneratesLocalSecretsWithoutPrintingThem(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	var random bytes.Buffer
	random.Write(bytes.Repeat([]byte{0x42}, 128))
	if err := InitializeEnvFile(path, false, &random); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(content)
	for _, expected := range []string{"OPENBOOK_LLM_CHAIN=stub", "MYSQL_APP_PASSWORD=", "JWT_SECRET=", "DEFAULT_ADMIN_PASSWORD="} {
		if !strings.Contains(text, expected) {
			t.Fatalf("generated config missing %q: %s", expected, text)
		}
	}
	if strings.Contains(text, "change-me-before-exposing") || strings.Contains(text, "replace-with") {
		t.Fatalf("generated config contains a placeholder secret: %s", text)
	}
	var output bytes.Buffer
	if code := RunInit(&output, []string{"-path", path}); code != 1 || !strings.Contains(output.String(), "已存在") {
		t.Fatalf("existing config should be protected: code=%d output=%s", code, output.String())
	}
}

func TestRunMigrateAndSeedDryRunDoNotRequireDatabase(t *testing.T) {
	var output bytes.Buffer
	if code := RunMigrate(&output, []string{"-dry-run"}); code != 0 || !strings.Contains(output.String(), "不会连接") {
		t.Fatalf("migrate dry-run failed: code=%d output=%s", code, output.String())
	}
	output.Reset()
	if code := RunSeed(&output, []string{"-dry-run", "-shop-only"}); code != 0 || !strings.Contains(output.String(), "skip_appointments=false") {
		t.Fatalf("seed dry-run failed: code=%d output=%s", code, output.String())
	}
}

func TestRunMigrateLegacyReportRequiresExplicitSafeFlags(t *testing.T) {
	var output bytes.Buffer
	if code := RunMigrate(&output, []string{"-legacy-report"}); code != 2 || !strings.Contains(output.String(), "-dry-run") {
		t.Fatalf("legacy report should require dry-run: code=%d output=%s", code, output.String())
	}
	output.Reset()
	if code := RunMigrate(&output, []string{"-legacy-report", "-dry-run"}); code != 2 || !strings.Contains(output.String(), "-merchant-id") {
		t.Fatalf("legacy report should require merchant ID: code=%d output=%s", code, output.String())
	}
	output.Reset()
	if code := RunMigrate(&output, []string{"-dry-run", "-merchant-id", "merchant-1"}); code != 2 || !strings.Contains(output.String(), "-legacy-report") {
		t.Fatalf("legacy report flags should be explicit: code=%d output=%s", code, output.String())
	}
}

func TestBackupAndRestoreDryRunProtectSecretsAndDestructiveActions(t *testing.T) {
	env := map[string]string{
		"MYSQL_DSN": "user:super-secret@tcp(127.0.0.1:3306)/booking?parseTime=true",
		"APP_ENV":   "development",
	}
	lookup := func(key string) string { return env[key] }
	var output bytes.Buffer
	if code := runBackup(&output, []string{"-dry-run"}, lookup); code != 0 {
		t.Fatalf("backup dry-run failed: %d %s", code, output.String())
	}
	if strings.Contains(output.String(), "super-secret") {
		t.Fatalf("backup output leaked password: %s", output.String())
	}
	output.Reset()
	if code := runRestore(&output, []string{"-dry-run", "-file", filepath.Join(t.TempDir(), "backup.sql")}, lookup); code != 1 || !strings.Contains(output.String(), "不可读") {
		t.Fatalf("restore should reject missing file: %d %s", code, output.String())
	}
	file := filepath.Join(t.TempDir(), "backup.sql")
	if err := os.WriteFile(file, []byte("-- test backup\n"), 0600); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if code := runRestore(&output, []string{"-dry-run", "-file", file}, lookup); code != 0 || !strings.Contains(output.String(), "不会执行 mysql") {
		t.Fatalf("restore dry-run failed: %d %s", code, output.String())
	}
	output.Reset()
	if code := runRestore(&output, []string{"-file", file}, lookup); code != 2 || !strings.Contains(output.String(), "-yes") {
		t.Fatalf("restore should require confirmation: %d %s", code, output.String())
	}
}

func TestBackupAndRestoreRequireExplicitDatabaseConfiguration(t *testing.T) {
	lookup := func(string) string { return "" }
	var output bytes.Buffer
	if code := runBackup(&output, []string{"-dry-run"}, lookup); code != 1 || !strings.Contains(output.String(), "需要 MYSQL_DSN") {
		t.Fatalf("backup should reject implicit database defaults: %d %s", code, output.String())
	}
	file := filepath.Join(t.TempDir(), "backup.sql")
	if err := os.WriteFile(file, []byte("-- test backup\n"), 0600); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if code := runRestore(&output, []string{"-dry-run", "-file", file}, lookup); code != 1 || !strings.Contains(output.String(), "需要 MYSQL_DSN") {
		t.Fatalf("restore should reject implicit database defaults: %d %s", code, output.String())
	}
}
