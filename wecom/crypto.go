package wecom

import (
	"encoding/xml"
	"fmt"

	wxbiz "github.com/sbzhu/weworkapi_golang/wxbizmsgcrypt"
)

// MessageXML 企业微信消息 XML 结构（兼容企业应用、微信客服和外部联系人）
type MessageXML struct {
	XMLName      xml.Name `xml:"xml"`
	ToUserName   string   `xml:"ToUserName"`
	FromUserName string   `xml:"FromUserName"`
	CreateTime   int64    `xml:"CreateTime"`
	MsgType      string   `xml:"MsgType"`
	Content      string   `xml:"Content"`
	MsgId        int64    `xml:"MsgId"`
	AgentID      int      `xml:"AgentID"`
	// 微信客服事件字段
	Event    string `xml:"Event"`
	Token    string `xml:"Token"`
	OpenKfId string `xml:"OpenKfId"`
	// 外部联系人事件字段
	// Event=change_external_contact 时，ExternalUserID 表示外部联系人UserID
	// UserID 表示添加该联系人的企业成员UserID
	ExternalUserID string `xml:"ExternalUserID"`
	// ChangeType 用于区分事件子类型（如 add_external_contact、add_half_external_contact 等）
	ChangeType string `xml:"ChangeType"`
	// WelcomeCode 欢迎码，可用于发送欢迎语（仅 add_external_contact 事件时有值）
	WelcomeCode string `xml:"WelcomeCode"`
	// UserID 企业成员UserID（添加外部联系人的员工，仅 change_external_contact 事件时有值）
	UserID string `xml:"UserID"`
}

// Crypto 企业微信消息加解密（使用官方库）
type Crypto struct {
	wxcpt *wxbiz.WXBizMsgCrypt
}

// NewCrypto 创建加解密实例
func NewCrypto(token, encodingAESKey, corpID string) (*Crypto, error) {
	wxcpt := wxbiz.NewWXBizMsgCrypt(token, encodingAESKey, corpID, wxbiz.XmlType)
	if wxcpt == nil {
		return nil, fmt.Errorf("创建 WXBizMsgCrypt 失败")
	}
	return &Crypto{wxcpt: wxcpt}, nil
}

// VerifyURL 验证企业微信回调 URL
func (c *Crypto) VerifyURL(msgSignature, timestamp, nonce, echostr string) (string, error) {
	plaintext, cryptErr := c.wxcpt.VerifyURL(msgSignature, timestamp, nonce, echostr)
	if cryptErr != nil {
		return "", fmt.Errorf("VerifyURL 失败: %d %s", cryptErr.ErrCode, cryptErr.ErrMsg)
	}
	return string(plaintext), nil
}

// DecryptMsg 解密接收到的消息
func (c *Crypto) DecryptMsg(msgSignature, timestamp, nonce string, body []byte) (string, error) {
	plaintext, cryptErr := c.wxcpt.DecryptMsg(msgSignature, timestamp, nonce, body)
	if cryptErr != nil {
		return "", fmt.Errorf("DecryptMsg 失败: %d %s", cryptErr.ErrCode, cryptErr.ErrMsg)
	}
	return string(plaintext), nil
}

// EncryptMsg 加密回复消息
func (c *Crypto) EncryptMsg(plaintext, timestamp, nonce string) ([]byte, error) {
	encrypted, cryptErr := c.wxcpt.EncryptMsg(plaintext, timestamp, nonce)
	if cryptErr != nil {
		return nil, fmt.Errorf("EncryptMsg 失败: %d %s", cryptErr.ErrCode, cryptErr.ErrMsg)
	}
	return encrypted, nil
}

// ParseMessage 解析接收到的 XML 消息
func ParseMessage(xmlStr string) (*MessageXML, error) {
	var msg MessageXML
	if err := xml.Unmarshal([]byte(xmlStr), &msg); err != nil {
		return nil, fmt.Errorf("解析消息失败: %w", err)
	}
	return &msg, nil
}
