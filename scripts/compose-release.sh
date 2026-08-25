#!/usr/bin/env bash
# 使用不可变镜像执行 Compose 发布或回滚。
#
# 用法：
#   bash scripts/compose-release.sh deploy <new-image> [previous-image]
#   bash scripts/compose-release.sh rollback <previous-image>
#
# 默认读取 .env.production.local。生产环境应通过 COMPOSE_ENV_FILE 指定仅本机可读的配置，
# 并用 OPENBOOK_IMAGE 传入 registry digest 或已在部署机缓存的版本 Tag。

set -euo pipefail

usage() {
  echo "用法：" >&2
  echo "  $0 deploy <new-image> [previous-image]" >&2
  echo "  $0 rollback <previous-image>" >&2
  exit 2
}

if [[ $# -lt 2 || $# -gt 3 ]]; then
  usage
fi

ACTION="$1"
IMAGE_REF="$2"
PREVIOUS_REF="${3:-${OPENBOOK_PREVIOUS_IMAGE:-}}"
ENV_FILE="${COMPOSE_ENV_FILE:-.env.production.local}"
HEALTH_URL="${OPENBOOK_HEALTH_URL:-http://127.0.0.1:38080/}"
HEALTH_ATTEMPTS="${OPENBOOK_HEALTH_ATTEMPTS:-30}"
HEALTH_INTERVAL_SECONDS="${OPENBOOK_HEALTH_INTERVAL_SECONDS:-2}"
PULL_IMAGE="${OPENBOOK_PULL_IMAGE:-1}"

if [[ "$ACTION" != "deploy" && "$ACTION" != "rollback" ]]; then
  usage
fi
if [[ ! -f "$ENV_FILE" ]]; then
  echo "✗ 找不到 Compose 配置：$ENV_FILE" >&2
  exit 1
fi
if [[ "$IMAGE_REF" == -* || "$IMAGE_REF" == *$'\n'* || "$IMAGE_REF" == *$'\r'* ]]; then
  echo "✗ 镜像引用包含非法内容" >&2
  exit 1
fi

COMPOSE=(docker compose --env-file "$ENV_FILE" -f docker-compose.yml -f docker-compose.production.yml)

wait_for_health() {
  local attempt
  for ((attempt = 1; attempt <= HEALTH_ATTEMPTS; attempt++)); do
    if curl --fail --silent --show-error --max-time 5 "$HEALTH_URL" >/dev/null; then
      echo "✓ 应用健康检查通过：$HEALTH_URL"
      return 0
    fi
    sleep "$HEALTH_INTERVAL_SECONDS"
  done
  echo "✗ 应用健康检查超时：$HEALTH_URL" >&2
  "${COMPOSE[@]}" ps >&2 || true
  return 1
}

start_with_image() {
  local image="$1"
  echo ">>> 使用镜像启动 app：$image"
  OPENBOOK_IMAGE="$image" "${COMPOSE[@]}" up -d --no-build mysql redis db-bootstrap app
}

if [[ "$ACTION" == "deploy" && "$PULL_IMAGE" == "1" ]]; then
  echo ">>> 拉取目标镜像"
  OPENBOOK_IMAGE="$IMAGE_REF" "${COMPOSE[@]}" pull app
fi

if [[ "$ACTION" == "deploy" ]]; then
  start_with_image "$IMAGE_REF"
  if wait_for_health; then
    echo "✓ 发布完成：$IMAGE_REF"
    exit 0
  fi
  if [[ -z "$PREVIOUS_REF" ]]; then
    echo "✗ 新镜像不健康，未提供 previous-image，保持现场供排查" >&2
    exit 1
  fi
  echo "⚠️ 新镜像不健康，开始回滚：$PREVIOUS_REF" >&2
  start_with_image "$PREVIOUS_REF"
  wait_for_health
  echo "✓ 已回滚到：$PREVIOUS_REF" >&2
  exit 1
fi

start_with_image "$IMAGE_REF"
wait_for_health
echo "✓ 回滚完成：$IMAGE_REF"
