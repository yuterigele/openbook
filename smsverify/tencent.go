package smsverify

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

type tencentSender struct {
	secretID, secretKey string
	appID, signName     string
	templateID, region  string
	endpoint            string
	client              *http.Client
	now                 func() time.Time
}

func newTencentSenderFromEnv() (*tencentSender, error) {
	s := &tencentSender{
		secretID: os.Getenv("TENCENT_SMS_SECRET_ID"), secretKey: os.Getenv("TENCENT_SMS_SECRET_KEY"),
		appID: os.Getenv("TENCENT_SMS_SDK_APP_ID"), signName: os.Getenv("TENCENT_SMS_SIGN_NAME"),
		templateID: os.Getenv("TENCENT_SMS_TEMPLATE_ID"), region: strings.TrimSpace(os.Getenv("TENCENT_SMS_REGION")),
		endpoint: "https://sms.tencentcloudapi.com", client: &http.Client{Timeout: 5 * time.Second}, now: time.Now,
	}
	if s.region == "" {
		s.region = "ap-guangzhou"
	}
	if s.secretID == "" || s.secretKey == "" || s.appID == "" || s.signName == "" || s.templateID == "" {
		return nil, fmt.Errorf("%w: 腾讯云短信配置不完整", ErrUnavailable)
	}
	return s, nil
}

func (s *tencentSender) SendCode(ctx context.Context, phone, code string) error {
	payload, err := json.Marshal(map[string]any{
		"PhoneNumberSet": []string{"+86" + phone}, "SmsSdkAppId": s.appID,
		"SignName": s.signName, "TemplateId": s.templateID, "TemplateParamSet": []string{code},
	})
	if err != nil {
		return err
	}
	timestamp := s.now().Unix()
	authorization := s.authorization(payload, timestamp)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Host", "sms.tencentcloudapi.com")
	req.Header.Set("X-TC-Action", "SendSms")
	req.Header.Set("X-TC-Version", "2021-01-11")
	req.Header.Set("X-TC-Region", s.region)
	req.Header.Set("X-TC-Timestamp", fmt.Sprint(timestamp))
	req.Header.Set("Authorization", authorization)
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	var result struct {
		Response struct {
			Error         *struct{ Code, Message string }
			SendStatusSet []struct{ Code, Message string }
		}
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return fmt.Errorf("腾讯云短信响应无法解析（HTTP %d）", resp.StatusCode)
	}
	if result.Response.Error != nil {
		return fmt.Errorf("腾讯云短信发送失败：%s", result.Response.Error.Code)
	}
	if len(result.Response.SendStatusSet) == 0 || result.Response.SendStatusSet[0].Code != "Ok" {
		code := "Unknown"
		if len(result.Response.SendStatusSet) > 0 {
			code = result.Response.SendStatusSet[0].Code
		}
		return fmt.Errorf("腾讯云短信发送失败：%s", code)
	}
	return nil
}

func (s *tencentSender) authorization(payload []byte, timestamp int64) string {
	date := time.Unix(timestamp, 0).UTC().Format("2006-01-02")
	canonicalHeaders := "content-type:application/json; charset=utf-8\nhost:sms.tencentcloudapi.com\nx-tc-action:sendsms\n"
	signedHeaders := "content-type;host;x-tc-action"
	canonicalRequest := "POST\n/\n\n" + canonicalHeaders + "\n" + signedHeaders + "\n" + sha256Hex(payload)
	credentialScope := date + "/sms/tc3_request"
	stringToSign := "TC3-HMAC-SHA256\n" + fmt.Sprint(timestamp) + "\n" + credentialScope + "\n" + sha256Hex([]byte(canonicalRequest))
	secretDate := hmacSHA256([]byte("TC3"+s.secretKey), date)
	secretService := hmacSHA256(secretDate, "sms")
	secretSigning := hmacSHA256(secretService, "tc3_request")
	signature := hex.EncodeToString(hmacSHA256(secretSigning, stringToSign))
	return "TC3-HMAC-SHA256 Credential=" + s.secretID + "/" + credentialScope + ", SignedHeaders=" + signedHeaders + ", Signature=" + signature
}

func hmacSHA256(key []byte, value string) []byte {
	h := hmac.New(sha256.New, key)
	_, _ = h.Write([]byte(value))
	return h.Sum(nil)
}

func sha256Hex(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}
