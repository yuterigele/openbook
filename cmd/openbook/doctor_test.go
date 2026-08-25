package main

import (
	"bytes"
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
