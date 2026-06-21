package wecom

import (
		"bytes"
		"context"
		"encoding/json"
		"fmt"
		"io"
		"log"
		"net/http"
		"sync"
		"time"
	)

const (
	// 获取 Access Token
	accessTokenURL = "https://qyapi.weixin.qq.com/cgi-bin/gettoken"
	// 发送消息
	sendMessageURL = "https://qyapi.weixin.qq.com/cgi-bin/message/send"
	// 获取回调消息
	callbackURL = "https://qyapi.weixin.qq.com/cgi-bin/callback/get"
)

// Client 企业微信客户端
type Client struct {
	corpID     string
	agentID    int
	secret     string
	httpClient *http.Client

	// Access Token 缓存
	accessToken     string
	accessTokenExp  time.Time
	accessTokenLock sync.RWMutex
}

// NewClient 创建企业微信客户端
func NewClient(corpID, secret string, agentID int) *Client {
	return &Client{
		corpID:  corpID,
		secret:  secret,
		agentID: agentID,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// GetAccessToken 获取 Access Token
func (c *Client) GetAccessToken(ctx context.Context) (string, error) {
	c.accessTokenLock.RLock()
	if c.accessToken != "" && time.Now().Before(c.accessTokenExp) {
		defer c.accessTokenLock.RUnlock()
		return c.accessToken, nil
	}
	c.accessTokenLock.RUnlock()

	c.accessTokenLock.Lock()
	defer c.accessTokenLock.Unlock()

	// 双重检查
	if c.accessToken != "" && time.Now().Before(c.accessTokenExp) {
		return c.accessToken, nil
	}

	url := fmt.Sprintf("%s?corpid=%s&corpsecret=%s", accessTokenURL, c.corpID, c.secret)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return "", fmt.Errorf("创建请求失败: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("请求 Access Token 失败: %w", err)
	}
	defer resp.Body.Close()

	var result struct {
		Errcode     int    `json:"errcode"`
		Errmsg      string `json:"errmsg"`
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("解析响应失败: %w", err)
	}

	if result.Errcode != 0 {
		return "", fmt.Errorf("获取 Access Token 失败: %d %s", result.Errcode, result.Errmsg)
	}

		c.accessToken = result.AccessToken
		c.accessTokenExp = time.Now().Add(time.Duration(result.ExpiresIn-300) * time.Second) // 提前5分钟过期

		log.Printf("[wecom] access_token: %s (expires_in=%d)", c.accessToken, result.ExpiresIn)

		return c.accessToken, nil
}

// SendTextMessage 发送文本消息
func (c *Client) SendTextMessage(ctx context.Context, userID, content string) error {
	accessToken, err := c.GetAccessToken(ctx)
	if err != nil {
		return fmt.Errorf("获取 Access Token 失败: %w", err)
	}

	msg := map[string]interface{}{
		"touser":  userID,
		"msgtype": "text",
		"agentid": c.agentID,
		"text": map[string]interface{}{
			"content": content,
		},
	}

	jsonData, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("序列化消息失败: %w", err)
	}

	url := fmt.Sprintf("%s?access_token=%s", sendMessageURL, accessToken)
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("创建请求失败: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("发送消息失败: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var result struct {
		Errcode int    `json:"errcode"`
		Errmsg  string `json:"errmsg"`
	}

	if err := json.Unmarshal(body, &result); err != nil {
		return fmt.Errorf("解析响应失败: %w", err)
	}

	if result.Errcode != 0 {
		return fmt.Errorf("发送消息失败: %d %s", result.Errcode, result.Errmsg)
	}

	return nil
}

// OpenKfID 微信客服ID（来自回调事件的OpenKfId字段，非固定值）
// 实际使用的 open_kfid 从企业微信回调事件中动态获取
const DefaultOpenKfID = "wk4_kOBgAAqc8MJamoIGE7PmZo1ZpMGQ"

// SendKfTextMessage 通过微信客服接口发送文本消息
// 文档：https://developer.work.weixin.qq.com/document/path/94677
func (c *Client) SendKfTextMessage(ctx context.Context, externalUserID, openKfID, content string) error {
	accessToken, err := c.GetAccessToken(ctx)
	if err != nil {
		return fmt.Errorf("获取 Access Token 失败: %w", err)
	}

	msg := map[string]interface{}{
		"touser":   externalUserID,
		"open_kfid": openKfID,
		"msgtype":  "text",
		"text": map[string]interface{}{
			"content": content,
		},
	}

	jsonData, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("序列化消息失败: %w", err)
	}

	url := fmt.Sprintf("https://qyapi.weixin.qq.com/cgi-bin/kf/send_msg?access_token=%s", accessToken)
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("创建请求失败: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("发送客服消息失败: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var result struct {
		Errcode int    `json:"errcode"`
		Errmsg  string `json:"errmsg"`
		Msgid   string `json:"msgid"`
	}

	if err := json.Unmarshal(body, &result); err != nil {
		return fmt.Errorf("解析响应失败: %w", err)
	}

	if result.Errcode != 0 {
		return fmt.Errorf("发送客服消息失败: %d %s", result.Errcode, result.Errmsg)
	}

	return nil
}

// KfMsgItem 微信客服消息
type KfMsgItem struct {
	Msgid          string `json:"msgid"`
	OpenKfid       string `json:"open_kfid"`
	ExternalUserid string `json:"external_userid"`
	SendTime       int64  `json:"send_time"`
	Origin         int    `json:"origin"`
	MsgType        string `json:"msgtype"`
	Text           *struct {
		Content string `json:"content"`
	} `json:"text,omitempty"`
}

// SyncKfMsgResult 拉取消息结果
type SyncKfMsgResult struct {
	ErrCode    int         `json:"errcode"`
	ErrMsg     string      `json:"errmsg"`
	NextCursor string      `json:"next_cursor"`
	HasMore    int         `json:"has_more"`
	MsgList    []KfMsgItem `json:"msg_list"`
}

// SyncMsg 拉取微信客服消息
// 文档：https://developer.work.weixin.qq.com/document/path/94670
func (c *Client) SyncMsg(ctx context.Context, cursor, token string, limit int) (*SyncKfMsgResult, error) {
	accessToken, err := c.GetAccessToken(ctx)
	if err != nil {
		return nil, fmt.Errorf("获取 Access Token 失败: %w", err)
	}

	body := map[string]interface{}{
		"open_kfid": DefaultOpenKfID,
		"limit":     limit,
	}
	if cursor != "" {
		body["cursor"] = cursor
	}
	if token != "" {
		body["token"] = token
	}

	jsonData, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("序列化请求失败: %w", err)
	}

	url := fmt.Sprintf("https://qyapi.weixin.qq.com/cgi-bin/kf/sync_msg?access_token=%s", accessToken)
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, fmt.Errorf("创建请求失败: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("拉取消息失败: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	var result SyncKfMsgResult
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("解析响应失败: %w (body=%s)", err, string(respBody))
	}

	if result.ErrCode != 0 {
		return nil, fmt.Errorf("拉取消息失败: %d %s", result.ErrCode, result.ErrMsg)
	}

	return &result, nil
}
// AddContactWayResult 创建「联系我」二维码的结果
type AddContactWayResult struct {
	ConfigID string `json:"config_id"`
	QrCode   string `json:"qr_code"`
}

// AddContactWay 创建「联系我」二维码
// 文档：https://developer.work.weixin.qq.com/document/path/92228
//
// 参数：
//   - userID: 企业成员 UserID（谁接收客户消息）
//   - state: 自定义 state 参数，回调时会原样返回
//   - isTemp: 是否临时会话（0=正式/1=临时）
//
// 返回二维码图片 URL，扫码后客户发消息会触发应用回调。
// 注意：员工个人生成的二维码不会触发回调，必须使用此 API 生成的二维码。
func (c *Client) AddContactWay(ctx context.Context, userID, state string, isTemp int) (*AddContactWayResult, error) {
	accessToken, err := c.GetAccessToken(ctx)
	if err != nil {
		return nil, fmt.Errorf("获取 Access Token 失败: %w", err)
	}

		body := map[string]interface{}{
			"type":        1,         // 1=单人 2=多人
			"scene":       2,         // 1=小程序 2=二维码
			"user":        []string{userID},
			"is_temp":     isTemp,
			"skip_verify": true,      // 无需验证，直接添加
			"state":       state,
		}

	jsonData, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("序列化请求失败: %w", err)
	}

	url := fmt.Sprintf("https://qyapi.weixin.qq.com/cgi-bin/externalcontact/add_contact_way?access_token=%s", accessToken)
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, fmt.Errorf("创建请求失败: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求失败: %w", err)
	}
	defer resp.Body.Close()

		respBody, _ := io.ReadAll(resp.Body)
		log.Printf("[wecom] add_contact_way 原始响应: %s", string(respBody))

		// 先解析到通用 map 以兼容不同字段名
		var raw map[string]interface{}
		if err := json.Unmarshal(respBody, &raw); err != nil {
			return nil, fmt.Errorf("解析响应失败: %w (body=%s)", err, string(respBody))
		}

		errcode := int(raw["errcode"].(float64))
		errmsg, _ := raw["errmsg"].(string)
		configID, _ := raw["config_id"].(string)

		if errcode != 0 {
			return nil, fmt.Errorf("创建联系我二维码失败: %d %s", errcode, errmsg)
		}

		// 兼容 qr_code 或 qrcode 两种字段名
		qrCode, _ := raw["qr_code"].(string)
		if qrCode == "" {
			qrCode, _ = raw["qrcode"].(string)
		}

		log.Printf("[wecom] 联系我二维码生成成功: configID=%s qrCode=%s", configID, qrCode)

		return &AddContactWayResult{
			ConfigID: configID,
			QrCode:   qrCode,
		}, nil
}

func (c *Client) SendTextMessageToParty(ctx context.Context, partyID, content string) error {
	accessToken, err := c.GetAccessToken(ctx)
	if err != nil {
		return fmt.Errorf("获取 Access Token 失败: %w", err)
	}

	msg := map[string]interface{}{
		"toparty":  partyID,
		"msgtype":  "text",
		"agentid":  c.agentID,
		"text": map[string]interface{}{
			"content": content,
		},
	}

	jsonData, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("序列化消息失败: %w", err)
	}

	url := fmt.Sprintf("%s?access_token=%s", sendMessageURL, accessToken)
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("创建请求失败: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("发送消息失败: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var result struct {
		Errcode int    `json:"errcode"`
		Errmsg  string `json:"errmsg"`
	}

	if err := json.Unmarshal(body, &result); err != nil {
		return fmt.Errorf("解析响应失败: %w", err)
	}

	if result.Errcode != 0 {
		return fmt.Errorf("发送消息失败: %d %s", result.Errcode, result.Errmsg)
	}

	return nil
}

// GetContactWay 查询「联系我」二维码详情
// 文档：https://developer.work.weixin.qq.com/document/path/92228
//
// 用于在 add_contact_way 未返回 qr_code 时，通过 config_id 补查二维码 URL。
func (c *Client) GetContactWay(ctx context.Context, configID string) (*AddContactWayResult, error) {
	accessToken, err := c.GetAccessToken(ctx)
	if err != nil {
		return nil, fmt.Errorf("获取 Access Token 失败: %w", err)
	}

	body := map[string]string{
		"config_id": configID,
	}
	jsonData, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("序列化请求失败: %w", err)
	}

	url := fmt.Sprintf("https://qyapi.weixin.qq.com/cgi-bin/externalcontact/get_contact_way?access_token=%s", accessToken)
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, fmt.Errorf("创建请求失败: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求失败: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	log.Printf("[wecom] get_contact_way 原始响应: %s", string(respBody))

	var raw struct {
		Errcode    int    `json:"errcode"`
		Errmsg     string `json:"errmsg"`
		ContactWay *struct {
			ConfigID string `json:"config_id"`
			QrCode   string `json:"qr_code"`
		} `json:"contact_way"`
	}
	if err := json.Unmarshal(respBody, &raw); err != nil {
		return nil, fmt.Errorf("解析响应失败: %w (body=%s)", err, string(respBody))
	}

	if raw.Errcode != 0 {
		return nil, fmt.Errorf("查询联系我二维码失败: %d %s", raw.Errcode, raw.Errmsg)
	}

	if raw.ContactWay == nil {
		return nil, fmt.Errorf("查询结果中 contact_way 为空")
	}

	log.Printf("[wecom] 查询联系我二维码成功: configID=%s qrCode=%s", raw.ContactWay.ConfigID, raw.ContactWay.QrCode)

	return &AddContactWayResult{
		ConfigID: raw.ContactWay.ConfigID,
		QrCode:   raw.ContactWay.QrCode,
	}, nil
}