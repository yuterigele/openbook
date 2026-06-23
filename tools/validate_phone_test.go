package tools

import (
	"strings"
	"testing"
)

// TestValidatePhone 严格手机号校验（v4.9.3）
//
// 规则：11 位数字、1 开头
func TestValidatePhone_Valid(t *testing.T) {
	cases := []string{
		"13812345678",
		"15912345678",
		"18600001234",
		"10000000000", // 边缘：1 开头 + 全 0
	}
	for _, p := range cases {
		if err := ValidatePhone(p); err != nil {
			t.Errorf("ValidatePhone(%q) should pass, got %v", p, err)
		}
	}
}

func TestValidatePhone_Empty(t *testing.T) {
	err := ValidatePhone("")
	if err == nil {
		t.Fatal("空字符串应报错")
	}
	if !strings.Contains(err.Error(), "必填") {
		t.Errorf("错误应提到'必填'，got %v", err)
	}
}

func TestValidatePhone_TooShort(t *testing.T) {
	err := ValidatePhone("1381234567") // 10 位
	if err == nil {
		t.Fatal("10 位应报错")
	}
	if !strings.Contains(err.Error(), "11 位") {
		t.Errorf("错误应提到'11 位'，got %v", err)
	}
}

func TestValidatePhone_TooLong(t *testing.T) {
	err := ValidatePhone("138123456789") // 12 位
	if err == nil {
		t.Fatal("12 位应报错")
	}
	if !strings.Contains(err.Error(), "11 位") {
		t.Errorf("错误应提到'11 位'，got %v", err)
	}
}

func TestValidatePhone_NotStartingWith1(t *testing.T) {
	err := ValidatePhone("23812345678")
	if err == nil {
		t.Fatal("不以 1 开头应报错")
	}
	if !strings.Contains(err.Error(), "1 开头") {
		t.Errorf("错误应提到'1 开头'，got %v", err)
	}
}

func TestValidatePhone_HasLetter(t *testing.T) {
	err := ValidatePhone("1381234567a")
	if err == nil {
		t.Fatal("含字母应报错")
	}
	if !strings.Contains(err.Error(), "数字") {
		t.Errorf("错误应提到'数字'，got %v", err)
	}
}

func TestValidatePhone_HasSpace(t *testing.T) {
	err := ValidatePhone("138 2345678")
	if err == nil {
		t.Fatal("含空格应报错")
	}
}

func TestValidatePhone_HasPlusPrefix(t *testing.T) {
	// 简化：不接国际号码（+86 前缀）
	err := ValidatePhone("+8613812345678")
	if err == nil {
		t.Fatal("带 + 前缀应报错（13 位 + 不以 1 开头）")
	}
}