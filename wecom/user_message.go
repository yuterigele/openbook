package wecom

import (
		"bytes"
		"context"
		"encoding/json"
		"fmt"
		"io"
		"log"
		"net/http"
	)

// SourceType 消息来源类型
type SourceType string

const (
	// SourceKf 微信客服来源
	SourceKf SourceType = "kf"
	// SourceExternal 外部联系人（客户联系）来源
	SourceExternal SourceType = "external"
)

// UserMessage 统一消息结构体
// 将不同来源的消息（微信客服、外部联系人）统一转换为标准格式，供 Agent 处理。
type UserMessage struct {
	// MsgType 消息类型，如 "text"
	MsgType string
	// Content 消息文本内容
	Content string
	// UserID 标识用户：kf 场景为 external_userid，外部联系人场景为 employee userid
	UserID string
	// ExternalUserID 外部联系人的 external_userid（仅 external 来源时有值）
	ExternalUserID string
	// SourceType 消息来源："kf" 或 "external"
	SourceType SourceType
	// OpenKfID 微信客服ID（仅 kf 来源时有值）
	OpenKfID string
	// EmployeeUserID 企业成员 UserID（仅 external 来源时有值，即接收消息的员工）
	EmployeeUserID string
}

// ReplyRequest 统一回复请求
// 用于将 Agent 的回复根据 SourceType 分发到不同的发送接口。
type ReplyRequest struct {
	// UserID 用户标识（kf 场景为 external_userid，external 场景为 employee userid）
	UserID string
	// ExternalUserID 外部联系人的 external_userid（仅 external 来源时有值）
	ExternalUserID string
	// Content 回复文本内容
	Content string
	// SourceType 消息来源类型
	SourceType SourceType
	// OpenKfID 微信客服ID（仅 kf 来源时有值）
	OpenKfID string
	// EmployeeUserID 企业成员 UserID（仅 external 来源时有值）
	EmployeeUserID string
}

// FromKfMsg 从微信客服消息构造 UserMessage
func FromKfMsg(kfMsg KfMsgItem) *UserMessage {
	return &UserMessage{
		MsgType:        kfMsg.MsgType,
		Content:        kfMsg.Text.Content,
		UserID:         kfMsg.ExternalUserid,
		ExternalUserID: kfMsg.ExternalUserid,
		SourceType:     SourceKf,
		OpenKfID:       kfMsg.OpenKfid,
	}
}

// FromExternalContactMsg 从外部联系人消息构造 UserMessage
// externalUserID: 外部联系人的 external_userid
// employeeUserID: 收到消息的企业成员 UserID
// content: 消息内容
func FromExternalContactMsg(externalUserID, employeeUserID, content string) *UserMessage {
	return &UserMessage{
		MsgType:        "text",
		Content:        content,
		UserID:         employeeUserID,
		ExternalUserID: externalUserID,
		SourceType:     SourceExternal,
		EmployeeUserID: employeeUserID,
	}
}

// SendReply 统一回复适配层
// 根据 ReplyRequest.SourceType 选择不同的回复方式：
//   - SourceKf: 调用微信客服 /cgi-bin/kf/send_msg 回复
//   - SourceExternal: 调用外部联系人 /cgi-bin/externalcontact/send_msg 回复
//
// 外部联系人回复限制说明：
//   - 外部联系人场景不能像微信客服那样直接调用 kf/send_msg
//   - 需要使用客户联系接口 /cgi-bin/externalcontact/send_msg 发送消息
//   - 该接口要求应用有"客户联系"相关权限（externalcontact）
//   - 首次添加好友时，推荐使用发送欢迎语接口 /cgi-bin/externalcontact/send_welcome_msg
//   - 若 API 版本或权限不满足，建议在企业微信管理后台检查应用权限配置
func (c *Client) SendReply(ctx context.Context, req *ReplyRequest) error {
	switch req.SourceType {
	case SourceKf:
		return c.SendKfTextMessage(ctx, req.UserID, req.OpenKfID, req.Content)

	case SourceExternal:
		return c.sendExternalContactTextMessage(ctx, req.ExternalUserID, req.EmployeeUserID, req.Content)

	default:
		return fmt.Errorf("不支持的消息来源类型: %s", req.SourceType)
	}
}

// sendExternalContactTextMessage 发送文本消息给外部联系人
// 使用企业微信"客户联系"接口发送单聊消息
// 文档：https://developer.work.weixin.qq.com/document/path/95145
//
// 权限要求：
//   - 应用需要配置"客户联系"功能
//   - 需要 externalcontact 相关权限
//   - sender (employeeUserID) 需要已配置可调用应用
//
// 参数：
//   - externalUserID: 外部联系人的 external_userid
//   - employeeUserID: 发送消息的企业成员 UserID（作为 sender）
//   - content: 消息文本内容
func (c *Client) sendExternalContactTextMessage(ctx context.Context, externalUserID, employeeUserID, content string) error {
		accessToken, err := c.GetAccessToken(ctx)
		if err != nil {
			return fmt.Errorf("获取 Access Token 失败: %w", err)
		}
	
		msg := map[string]interface{}{
			"chat_type":       "single",
			"external_userid": []string{externalUserID},
			"sender":          employeeUserID,
			"msgtype":         "text",
			"text": map[string]interface{}{
				"content": content,
			},
		}
	
		jsonData, err := json.Marshal(msg)
		if err != nil {
			return fmt.Errorf("序列化消息失败: %w", err)
		}
	
		log.Printf("[wecom] sendExternalContactTextMessage 请求: %s", string(jsonData))
	
		url := fmt.Sprintf("https://qyapi.weixin.qq.com/cgi-bin/externalcontact/send_msg?access_token=%s", accessToken)
		req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(jsonData))
		if err != nil {
			return fmt.Errorf("创建请求失败: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
	
		resp, err := c.httpClient.Do(req)
		if err != nil {
			return fmt.Errorf("发送外部联系人消息失败: %w", err)
		}
		defer resp.Body.Close()
	
		body, _ := io.ReadAll(resp.Body)
		log.Printf("[wecom] sendExternalContactTextMessage 响应: status=%d body=%s", resp.StatusCode, string(body))
	
		var result struct {
			Errcode int    `json:"errcode"`
			Errmsg  string `json:"errmsg"`
		}
	
		if err := json.Unmarshal(body, &result); err != nil {
			return fmt.Errorf("解析响应失败: %w", err)
		}
	
		if result.Errcode != 0 {
			return fmt.Errorf("发送外部联系人消息失败: %d %s", result.Errcode, result.Errmsg)
		}
	
		return nil
	}

// SendWelcomeMsg 发送欢迎语给新添加的外部联系人
// 使用企业微信 /cgi-bin/externalcontact/send_welcome_msg 接口
// 文档：https://developer.work.weixin.qq.com/document/path/95146
//
// 使用场景：当接收到 add_external_contact 事件后，可调用此方法发送欢迎语。
//
// 权限要求：
//   - 应用需要"客户联系"功能权限
//   - welcome_code 来自 add_external_contact 事件回调
//
// 参数：
//   - welcomeCode: 欢迎码，来自 add_external_contact 事件的 WelcomeCode 字段
//   - content: 欢迎语文本内容
func (c *Client) SendWelcomeMsg(ctx context.Context, welcomeCode, content string) error {
	accessToken, err := c.GetAccessToken(ctx)
	if err != nil {
		return fmt.Errorf("获取 Access Token 失败: %w", err)
	}

	msg := map[string]interface{}{
		"welcome_code": welcomeCode,
		"msgtype":      "text",
		"text": map[string]interface{}{
			"content": content,
		},
	}

	jsonData, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("序列化消息失败: %w", err)
	}

	url := fmt.Sprintf("https://qyapi.weixin.qq.com/cgi-bin/externalcontact/send_welcome_msg?access_token=%s", accessToken)
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("创建请求失败: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("发送欢迎语失败: %w", err)
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
		return fmt.Errorf("发送欢迎语失败: %d %s", result.Errcode, result.Errmsg)
	}

	return nil
}
