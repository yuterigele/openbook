#!/usr/bin/env bash
# scripts/build-linux.sh
#
# macOS / Linux 本地用：cross-compile 当前项目到 Linux amd64 / arm64
#
# 用法：
#   bash scripts/build-linux.sh                 # 默认 amd64
#   bash scripts/build-linux.sh arm64           # ARM 服务器
#   bash scripts/build-linux.sh amd64 myapp     # 自定义输出名
#
# 背景同 build-linux.ps1（Windows 版）：
#   - go build 默认产出当前平台
#   - 需要 GOOS=linux GOARCH=amd64/arm64
#   - CGO_ENABLED=0 纯 Go cross-compile
#   - ldflags="-s -w" 砍调试符号
#
# 上传配套：
#   scp chatwitheino-linux root@server:/home/www/wwwroot/agent.yuyuanyuan.cn/
#   # Linux/macOS 的 scp 默认走 binary mode，不用 -O

set -euo pipefail

ARCH="${1:-amd64}"
OUTPUT="${2:-chatwitheino-linux}"

if [[ "$ARCH" != "amd64" && "$ARCH" != "arm64" ]]; then
  echo "✗ 未知架构: $ARCH（支持 amd64 / arm64）" >&2
  exit 1
fi

echo ">>> 准备 cross-compile: GOOS=linux GOARCH=$ARCH"

export GOOS=linux
export GOARCH="$ARCH"
export CGO_ENABLED=0

echo ">>> 执行 go build..."
START=$(date +%s)
go build -ldflags="-s -w" -o "$OUTPUT" .
END=$(date +%s)
DURATION=$((END - START))

if [[ ! -f "$OUTPUT" ]]; then
  echo "✗ build 后找不到 $OUTPUT" >&2
  exit 1
fi

SIZE_MB=$(du -m "$OUTPUT" | cut -f1)

echo ""
echo "✓ build 完成"
echo "  文件: $(pwd)/$OUTPUT"
echo "  大小: ${SIZE_MB} MB"
echo "  耗时: ${DURATION}s"
echo ""
echo "  上传到服务器：" >&2
echo "    scp $OUTPUT root@<server>:/home/www/wwwroot/agent.yuyuanyuan.cn/" >&2
echo ""
echo "  部署机检查格式：" >&2
echo "    file $OUTPUT" >&2
echo "    # 期望输出: ELF 64-bit LSB executable, x86-64, version 1 (SYSV), statically linked" >&2
