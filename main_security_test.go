package main

import (
	"strings"
	"testing"
)

func TestAdminAccessLogDoesNotPrintDefaultPassword(t *testing.T) {
	output := adminAccessLog("38080")
	if strings.Contains(output, "admin123") || strings.Contains(output, "platform123") {
		t.Fatalf("admin access log leaked a default password: %s", output)
	}
	for _, expected := range []string{"DEFAULT_ADMIN_USERNAME", "DEFAULT_ADMIN_PASSWORD", "密码不输出"} {
		if !strings.Contains(output, expected) {
			t.Fatalf("admin access log missing %q: %s", expected, output)
		}
	}
}
