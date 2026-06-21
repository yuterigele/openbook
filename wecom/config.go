package wecom

import (
	"fmt"
	"os"
)

// Config 企业微信配置
type Config struct {
	CorpID         string `json:"corp_id"`
	AgentID        int    `json:"agent_id"`
	Secret         string `json:"secret"`
	Token          string `json:"token"`
	EncodingAESKey string `json:"encoding_aes_key"`
	// KFLink 微信客服链接（从管理后台「微信客服 → 客服账号 → 获取链接」获取）
	// 客户扫码加好友后，欢迎语中引导客户点击此链接进入微信客服对话
	KFLink string `json:"kf_link"`
}

// LoadConfig 从环境变量加载配置
func LoadConfig() *Config {
	return &Config{
		CorpID:         getEnv("WECOM_CORP_ID", ""),
		AgentID:        getEnvInt("WECOM_AGENT_ID", 0),
		Secret:         getEnv("WECOM_SECRET", ""),
		Token:          getEnv("WECOM_TOKEN", ""),
		EncodingAESKey: getEnv("WECOM_ENCODING_AES_KEY", ""),
		KFLink:         getEnv("WECOM_KF_LINK", ""),
	}
}

// LoadConfigFromValues 从指定值加载配置
func LoadConfigFromValues(corpID, secret, token, encodingAESKey string, agentID int) *Config {
	return &Config{
		CorpID:         corpID,
		AgentID:        agentID,
		Secret:         secret,
		Token:          token,
		EncodingAESKey: encodingAESKey,
		KFLink:         os.Getenv("WECOM_KF_LINK"),
	}
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func getEnvInt(key string, defaultValue int) int {
	if value := os.Getenv(key); value != "" {
		var intValue int
		if _, err := fmt.Sscanf(value, "%d", &intValue); err == nil {
			return intValue
		}
	}
	return defaultValue
}