package main

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// RunInit 生成本地最小开发配置；默认拒绝覆盖已有文件。
func RunInit(writer io.Writer, args []string) int {
	flags := flag.NewFlagSet("init", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	path := flags.String("path", ".env", "配置文件路径")
	force := flags.Bool("force", false, "显式覆盖已有配置文件")
	if err := flags.Parse(args); err != nil {
		fmt.Fprintf(writer, "init 参数错误: %v\n", err)
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(writer, "init 不接受位置参数")
		return 2
	}
	if err := InitializeEnvFile(*path, *force, rand.Reader); err != nil {
		fmt.Fprintf(writer, "init 失败: %v\n", err)
		return 1
	}
	fmt.Fprintf(writer, "已生成本地配置: %s\n", filepath.Clean(*path))
	fmt.Fprintln(writer, "随机密码和 JWT_SECRET 已写入文件；不会在终端输出，请妥善保管并按环境修改模型配置。")
	return 0
}

// InitializeEnvFile 写入最小开发配置。random 只用于测试注入，不保存到其他位置。
func InitializeEnvFile(path string, force bool, random io.Reader) error {
	if random == nil {
		return errors.New("random source is required")
	}
	if path == "" {
		return errors.New("config path is required")
	}
	content, err := minimalEnv(random)
	if err != nil {
		return err
	}
	if !force {
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			if errors.Is(err, os.ErrExist) {
				return fmt.Errorf("配置文件已存在，请使用 -force 明确覆盖: %s", filepath.Clean(path))
			}
			return err
		}
		defer file.Close()
		if _, err := file.WriteString(content); err != nil {
			return err
		}
		return file.Chmod(0600)
	}
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		return err
	}
	return os.Chmod(path, 0600)
}

func minimalEnv(random io.Reader) (string, error) {
	password, err := randomSecret(random)
	if err != nil {
		return "", err
	}
	adminPassword, err := randomSecret(random)
	if err != nil {
		return "", err
	}
	platformPassword, err := randomSecret(random)
	if err != nil {
		return "", err
	}
	jwtSecret, err := randomSecret(random)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf(`# OpenBook 本地最小配置，由 openbook init 生成。
APP_ENV=development
OPENBOOK_LLM_CHAIN=stub
AGENT_REPLY_MODE=mock
BOOKING_APPLICATION_RUNTIME=0

MYSQL_HOST=127.0.0.1
MYSQL_PORT=3306
MYSQL_USER=chatwitheino
MYSQL_PASS=chatwitheino
MYSQL_DB=chatwitheino
MYSQL_APP_PASSWORD=%s

REDIS_ADDR=127.0.0.1:6379
REDIS_REQUIRED=0

JWT_SECRET=%s
DEFAULT_SHOP_ID=default
DEFAULT_SHOP_NAME=默认理发店
DEFAULT_ADMIN_USERNAME=admin
DEFAULT_ADMIN_PASSWORD=%s
DEFAULT_PLATFORM_ADMIN_USERNAME=platform
DEFAULT_PLATFORM_ADMIN_PASSWORD=%s
`, password, jwtSecret, adminPassword, platformPassword), nil
}

func randomSecret(random io.Reader) (string, error) {
	buffer := make([]byte, 32)
	if _, err := io.ReadFull(random, buffer); err != nil {
		return "", fmt.Errorf("生成随机密钥失败: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buffer), nil
}
