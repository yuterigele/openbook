package storage

// repo_test.go
//
// Pure-logic tests for the storage package (no DB required):
//   - IsValidSlot: DefaultSlots validation
//   - ParseDate:   YYYY-MM-DD parsing (with whitespace trimming)
//   - IsShopHoliday: single-day match
//   - AllShopHolidays: multi-day parse into map
//   - VerifyAdminPassword: bcrypt verification
//
// Run:
//   go test ./storage/... -v -run "TestIsValidSlot|TestParseDate|TestIsShopHoliday|TestAllShopHolidays|TestVerifyAdminPassword"

import (
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

// ===================== IsValidSlot =====================

func TestIsValidSlot_Valid(t *testing.T) {
	// DefaultSlots covers 09-18 with a lunch break 12-13:30.
	cases := []struct {
		t    string
		want bool
	}{
		{"09:00", true},
		{"10:30", true},
		{"11:30", true},
		{"12:00", false}, // lunch
		{"12:30", false},
		{"13:00", false},
		{"13:30", true},
		{"17:00", true},
		{"17:30", true},
		{"18:00", true},
		{"18:30", false}, // after close
		{"08:59", false}, // before open
		{"", false},
		{"25:00", false}, // invalid format
		{"9:00", false},  // missing leading zero
	}
	for _, c := range cases {
		t.Run(c.t, func(t *testing.T) {
			if got := IsValidSlot(c.t); got != c.want {
				t.Errorf("IsValidSlot(%q) = %v, want %v", c.t, got, c.want)
			}
		})
	}
}

// ===================== ParseDate =====================

func TestParseDate_OK(t *testing.T) {
	got, err := ParseDate("2026-06-21")
	if err != nil {
		t.Fatalf("ParseDate: %v", err)
	}
	if got.Year() != 2026 || got.Month() != 6 || got.Day() != 21 {
		t.Errorf("ParseDate got %v, want 2026-06-21", got.Format("2006-01-02"))
	}
}

func TestParseDate_TrimsWhitespace(t *testing.T) {
	got, err := ParseDate("  2026-06-21\n")
	if err != nil {
		t.Fatalf("ParseDate with whitespace: %v", err)
	}
	if got.Day() != 21 {
		t.Errorf("got day %d, want 21", got.Day())
	}
}

func TestParseDate_BadFormat(t *testing.T) {
	cases := []string{
		"2026/06/21",
		"21-06-2026",
		"not-a-date",
		"2026-13-01", // invalid month — time.Parse is strict
	}
	for _, c := range cases {
		t.Run(c, func(t *testing.T) {
			if _, err := ParseDate(c); err == nil {
				t.Errorf("ParseDate(%q) expected error, got nil", c)
			}
		})
	}
}

// ===================== IsShopHoliday =====================

func TestIsShopHoliday_SingleMatch(t *testing.T) {
	s := &Shop{Holidays: "2026-10-01,2026-10-02,2026-10-03"}

	if !IsShopHoliday(s, "2026-10-01") {
		t.Error("expected 2026-10-01 to be a holiday")
	}
	if !IsShopHoliday(s, "2026-10-03") {
		t.Error("expected 2026-10-03 to be a holiday")
	}
	if IsShopHoliday(s, "2026-10-04") {
		t.Error("expected 2026-10-04 to NOT be a holiday")
	}
}

func TestIsShopHoliday_NilShopOrEmpty(t *testing.T) {
	if IsShopHoliday(nil, "2026-10-01") {
		t.Error("nil shop should not be a holiday")
	}
	s := &Shop{Holidays: ""}
	if IsShopHoliday(s, "2026-10-01") {
		t.Error("empty holidays should not match")
	}
}

// ===================== AllShopHolidays =====================

func TestAllShopHolidays_ParsesAll(t *testing.T) {
	s := &Shop{Holidays: "2026-10-01, 2026-10-02 ,2026-10-03"}
	got := AllShopHolidays(s)

	want := map[string]bool{
		"2026-10-01": true,
		"2026-10-02": true,
		"2026-10-03": true,
	}
	if len(got) != len(want) {
		t.Errorf("got %d holidays, want %d", len(got), len(want))
	}
	for k := range want {
		if !got[k] {
			t.Errorf("missing %s", k)
		}
	}
}

func TestAllShopHolidays_NilOrEmpty(t *testing.T) {
	if got := AllShopHolidays(nil); len(got) != 0 {
		t.Errorf("nil shop: got %d holidays, want 0", len(got))
	}
	if got := AllShopHolidays(&Shop{}); len(got) != 0 {
		t.Errorf("empty shop: got %d holidays, want 0", len(got))
	}
}

// ===================== VerifyAdminPassword =====================

func TestVerifyAdminPassword_OK(t *testing.T) {
	hash, err := bcrypt.GenerateFromPassword([]byte("hunter2"), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("bcrypt: %v", err)
	}
	admin := &ShopAdmin{Username: "owner", PasswordHash: string(hash)}
	if !VerifyAdminPassword(admin, "hunter2") {
		t.Error("correct password should verify")
	}
}

func TestVerifyAdminPassword_WrongPassword(t *testing.T) {
	hash, err := bcrypt.GenerateFromPassword([]byte("hunter2"), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("bcrypt: %v", err)
	}
	admin := &ShopAdmin{Username: "owner", PasswordHash: string(hash)}
	if VerifyAdminPassword(admin, "wrong") {
		t.Error("wrong password should NOT verify")
	}
	if VerifyAdminPassword(admin, "") {
		t.Error("empty password should NOT verify")
	}
}

// ===================== string utility smoke =====================

func TestTrimSpaceUsedByParseDate(t *testing.T) {
	// documents that ParseDate trims whitespace
	in := "  2026-06-21  "
	trimmed := strings.TrimSpace(in)
	if trimmed != "2026-06-21" {
		t.Errorf("TrimSpace: got %q, want %q", trimmed, "2026-06-21")
	}
}