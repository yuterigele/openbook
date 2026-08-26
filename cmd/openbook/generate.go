package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
)

var generateNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

type generatedFile struct {
	path string
	data string
}

// RunGenerate 生成安全的扩展模板；已有文件一律不覆盖。
func RunGenerate(writer io.Writer, args []string) int {
	if writer == nil {
		writer = io.Discard
	}
	if len(args) != 2 {
		_, _ = fmt.Fprintln(writer, "用法: openbook generate <profile|tool|channel> <name>")
		return 2
	}
	kind, name := args[0], args[1]
	if !generateNamePattern.MatchString(name) {
		_, _ = fmt.Fprintf(writer, "名称必须匹配 %s\n", generateNamePattern.String())
		return 2
	}

	files, err := generatedFiles(kind, name)
	if err != nil {
		_, _ = fmt.Fprintf(writer, "生成失败: %v\n", err)
		return 2
	}
	if err := writeGeneratedFiles(files); err != nil {
		_, _ = fmt.Fprintf(writer, "生成失败: %v\n", err)
		return 1
	}
	for _, file := range files {
		_, _ = fmt.Fprintln(writer, "已生成:", filepath.ToSlash(file.path))
	}
	return 0
}

func generatedFiles(kind, name string) ([]generatedFile, error) {
	exported := exportedIdentifier(name)
	switch kind {
	case "profile":
		return []generatedFile{
			{path: filepath.Join("profiles", name, "profile.go"), data: profileTemplate(name)},
			{path: filepath.Join("profiles", name, "profile_test.go"), data: profileTestTemplate(name)},
		}, nil
	case "tool":
		return []generatedFile{
			{path: filepath.Join("tools", name+".go"), data: toolTemplate(name, exported)},
			{path: filepath.Join("tools", name+"_test.go"), data: toolTestTemplate(exported)},
		}, nil
	case "channel":
		return []generatedFile{
			{path: filepath.Join("sdk", "channel", name+".go"), data: channelTemplate(exported)},
			{path: filepath.Join("sdk", "channel", name+"_test.go"), data: channelTestTemplate(exported)},
		}, nil
	default:
		return nil, fmt.Errorf("不支持的类型 %q，可选 profile、tool、channel", kind)
	}
}

func writeGeneratedFiles(files []generatedFile) error {
	for _, file := range files {
		if _, err := os.Stat(file.path); err == nil {
			return fmt.Errorf("文件已存在，不覆盖: %s", filepath.ToSlash(file.path))
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("检查文件 %s: %w", filepath.ToSlash(file.path), err)
		}
	}
	for _, file := range files {
		if err := os.MkdirAll(filepath.Dir(file.path), 0o755); err != nil {
			return fmt.Errorf("创建目录 %s: %w", filepath.ToSlash(filepath.Dir(file.path)), err)
		}
		if err := writeGeneratedFile(file); err != nil {
			return err
		}
	}
	return nil
}

func writeGeneratedFile(file generatedFile) error {
	handle, err := os.OpenFile(file.path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return fmt.Errorf("写入文件 %s: %w", filepath.ToSlash(file.path), err)
	}
	if _, err := io.WriteString(handle, file.data); err != nil {
		_ = handle.Close()
		return fmt.Errorf("写入文件 %s: %w", filepath.ToSlash(file.path), err)
	}
	if err := handle.Close(); err != nil {
		return fmt.Errorf("关闭文件 %s: %w", filepath.ToSlash(file.path), err)
	}
	return nil
}

func exportedIdentifier(name string) string {
	parts := strings.Split(name, "_")
	for index, part := range parts {
		runes := []rune(part)
		if len(runes) == 0 {
			continue
		}
		runes[0] = unicode.ToUpper(runes[0])
		parts[index] = string(runes)
	}
	return strings.Join(parts, "")
}

func profileTemplate(name string) string {
	return fmt.Sprintf(`package %s

import (
	"time"

	"github.com/yuterigele/openbook/sdk/profile"
)

// Definition 返回该行业的最小 Profile；请按业务补充服务、资源和回复模板。
func Definition() profile.Definition {
	return profile.Definition{
		ID:          %q,
		DisplayName: "待配置行业",
		Terms: profile.Terms{
			Staff: "服务人员", Service: "服务项目", Resource: "服务资源",
		},
		StartInterval: 30 * time.Minute,
		Services: []profile.Service{{
			ID: "service_1", DisplayName: "待配置服务", Duration: 30 * time.Minute,
		}},
		RequiredCustomerFields: []string{"name"},
	}
}
`, name, name)
}

func profileTestTemplate(name string) string {
	return fmt.Sprintf(`package %s

import "testing"

func TestDefinitionIsValid(t *testing.T) {
	if err := Definition().Validate(); err != nil {
		t.Fatalf("generated profile must be valid before customization: %%v", err)
	}
}
`, name)
}

func toolTemplate(name, exported string) string {
	return fmt.Sprintf(`package tools

import (
	"context"
	"errors"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

// %sTool 是工具扩展模板；实现前不能注册到生产 Tool Catalog。
type %sTool struct{}

func (*%sTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: %q,
		Desc: "待实现的业务工具；完成服务端校验后再注册",
	}, nil
}

func (*%sTool) InvokableRun(context.Context, string, ...tool.Option) (string, error) {
	return "", errors.New("生成的工具尚未实现")
}
`, exported, exported, exported, name, exported)
}

func toolTestTemplate(exported string) string {
	return fmt.Sprintf(`package tools

import "testing"

func Test%sToolMetadata(t *testing.T) {
	info, err := (&%sTool{}).Info(nil)
	if err != nil || info == nil || info.Name == "" {
		t.Fatalf("generated tool metadata is invalid: %%v %%+v", err, info)
	}
}
`, exported, exported)
}

func channelTemplate(exported string) string {
	return fmt.Sprintf(`package channel

import "time"

// %sChannelAdapter 是渠道适配器模板；验签和去重完成后才能构造 InboundMessage。
type %sChannelAdapter struct{}

func (%sChannelAdapter) NewInboundMessage(messageID, sessionID, text string, receivedAt time.Time) (InboundMessage, error) {
	return NewInbound(messageID, sessionID, KindWebChat, text, receivedAt)
}
`, exported, exported, exported)
}

func channelTestTemplate(exported string) string {
	return fmt.Sprintf(`package channel

import (
	"testing"
	"time"
)

func Test%sChannelAdapterCreatesInboundMessage(t *testing.T) {
	message, err := (%sChannelAdapter{}).NewInboundMessage("message-1", "session-1", "你好", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if message.SchemaVersion != SchemaVersion || message.Kind != KindWebChat {
		t.Fatalf("unexpected generated message: %%+v", message)
	}
}
`, exported, exported)
}
