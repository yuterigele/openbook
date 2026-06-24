#!/usr/bin/env bash
# scripts/deploy.sh
#
# 服务器端：替换 binary + 重启服务（systemd / nohup 两种模式自适配）
#
# 用法（在服务器上跑）：
#   bash scripts/deploy.sh /path/to/chatwitheino-linux
#   # 不传参数时默认用 /home/www/wwwroot/agent.yuyuanyuan.cn/chatwitheino-linux
#
# 行为：
#   1) 备份旧 binary（带时间戳）
#   2) 替换新 binary + chmod +x
#   3) 检测服务模式：
#      - systemd 有 chatwitheino.service → systemctl restart
#      - 否则 → 找运行中的 PID 优雅 kill + nohup 启动
#   4) 等 3 秒 + health check
#
# 注意：
#   - 上传 binary 前先在本地用 scripts/build-linux.sh 编
#   - 二进制必须 chmod +x
#   - 端口冲突检查：lsof -i :38080

set -euo pipefail

BIN_PATH="${1:-/home/www/wwwroot/agent.yuyuanyuan.cn/chatwitheino-linux}"
APP_DIR="$(dirname "$BIN_PATH")"
SERVICE_NAME="chatwitheino"
HEALTH_URL="http://127.0.0.1:38080/healthz"  # 假设健康检查端点；如无改用根路径

if [[ ! -f "$BIN_PATH" ]]; then
  echo "✗ 找不到 $BIN_PATH"
  echo "  请先在本地 build + scp 上传："
  echo "    pwsh scripts/build-linux.ps1        # 或 bash scripts/build-linux.sh"
  echo "    scp -O chatwitheino-linux root@server:$BIN_PATH"
  exit 1
fi

echo ">>> 1) 备份旧 binary"
if [[ -f "${BIN_PATH}.old" ]]; then
  rm -f "${BIN_PATH}.old"
fi
if [[ -f "$BIN_PATH.bak" ]]; then
  echo "  发现 .bak，可能是上次失败残留，保留作排查"
fi
cp "$BIN_PATH" "${BIN_PATH}.bak.$(date +%Y%m%d-%H%M%S)"
echo "  备份 → ${BIN_PATH}.bak.<timestamp>"

echo ">>> 2) 确保可执行"
chmod +x "$BIN_PATH"
ls -la "$BIN_PATH"

echo ">>> 3) 检测服务模式 + 重启"
if systemctl list-unit-files | grep -q "^${SERVICE_NAME}.service"; then
  echo "  检测到 systemd unit: ${SERVICE_NAME}.service"
  sudo systemctl restart "$SERVICE_NAME"
  sleep 2
  sudo systemctl status "$SERVICE_NAME" --no-pager | head -10
else
  echo "  未发现 systemd unit，走 nohup 模式"
  # 找现有 PID（按 binary 路径匹配）
  PID=$(pgrep -f "$BIN_PATH" || true)
  if [[ -n "$PID" ]]; then
    echo "  发现运行中 PID=$PID，优雅 kill (TERM → 等 5s → KILL)"
    kill -TERM "$PID"
    for i in 1 2 3 4 5; do
      if ! kill -0 "$PID" 2>/dev/null; then break; fi
      sleep 1
    done
    if kill -0 "$PID" 2>/dev/null; then
      echo "  5s 未退出，强制 KILL"
      kill -KILL "$PID"
    fi
  fi
  # 启动
  echo "  nohup 启动..."
  cd "$APP_DIR"
  nohup "$BIN_PATH" >> "$APP_DIR/app.log" 2>&1 &
  NEW_PID=$!
  echo "  新 PID=$NEW_PID"
  sleep 3
  if ! kill -0 "$NEW_PID" 2>/dev/null; then
    echo "✗ 启动失败，看 $APP_DIR/app.log 末尾："
    tail -20 "$APP_DIR/app.log" || true
    exit 1
  fi
fi

echo ">>> 4) Health check"
sleep 2
if curl -fsS --max-time 5 "$HEALTH_URL" >/dev/null 2>&1; then
  echo "✓ 健康检查通过: $HEALTH_URL"
elif curl -fsS --max-time 5 "http://127.0.0.1:38080/" >/dev/null 2>&1; then
  echo "✓ 根路径响应: http://127.0.0.1:38080/"
else
  echo "⚠ 健康检查失败，端口未响应"
  echo "  手动验证："
  echo "    ss -tlnp | grep 38080"
  echo "    tail -50 $APP_DIR/app.log"
  exit 2
fi

echo ""
echo "✓ 部署完成"
echo "  binary: $BIN_PATH"
echo "  备份:   ${BIN_PATH}.bak.<timestamp>"
