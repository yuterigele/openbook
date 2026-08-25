# 环境分层与发布约定

OpenBook 运行环境固定为 `development`、`staging`、`production`。三者使用同一份镜像和基础 Docker Compose 定义，差异只通过环境变量、独立基础设施以及仅开发环境加载的端口覆盖文件体现；禁止把生产凭据复制到开发或预发布环境。

| 环境 | 用途 | 企业微信回复 | 数据库/Redis | 日志 |
|---|---|---|---|---|
| development | 本机开发、功能调试 | `mock` | 本机 Compose 数据卷 | 可短时开启 `LLM_DEBUG_LOG` |
| staging | 回调、模型、回归验证 | 默认 `mock`；经批准才对测试客户 `real` | 独立实例与独立凭据 | 脱敏日志，不记录完整对话 |
| production | 对外服务 | `real` | 生产独立实例、备份与恢复演练 | 默认关闭 LLM 内容日志 |

## 配置方法

先复制基础模板，再应用目标环境覆盖项；真实密钥放入仅本机保存的 `.local` 文件。Docker Compose 从后面的 `--env-file` 覆盖前面的同名变量。

```powershell
# development
Copy-Item .env.example .env.dev.local
docker compose --env-file .env.dev.local -f docker-compose.yml -f docker-compose.dev-ports.yml up -d --build

# staging
Copy-Item .env.example .env.staging.local
Get-Content deploy/env/staging.override.env.example | Add-Content .env.staging.local
docker compose --env-file .env.staging.local -f docker-compose.yml up -d --build

# production：先在部署机的 Secret 管理系统或 .env.production.local 填入真实凭据。
Copy-Item .env.example .env.production.local
Get-Content deploy/env/production.override.env.example | Add-Content .env.production.local
docker compose --env-file .env.production.local -f docker-compose.yml up -d --build
```

生产与预发布均必须替换 `MYSQL_APP_PASSWORD`、`DEFAULT_*_PASSWORD`、`JWT_SECRET`，并配置独立的 MySQL、Redis、模型和企业微信凭据。不要使用开发数据卷或开发企微回调。

生产切流前执行 `go run ./cmd/openbook doctor`。当 `APP_ENV=production` 时，`doctor` 会阻断 Stub 模型、缺失 Redis 或企业微信凭据、非 `AGENT_REPLY_MODE=real`、示例管理员密码和缺失 `JWT_SECRET`；通过只代表配置门禁通过，不替代真实网络连通性、备份恢复和回滚演练。

## 发布前检查

1. development 使用 `docker compose --env-file <目标文件> -f docker-compose.yml -f docker-compose.dev-ports.yml config -q`；staging/production 使用 `docker compose --env-file <目标文件> -f docker-compose.yml config -q`。
2. `AGENT_REPLY_MODE=real` 仅允许生产或已批准的预发布验证。未认证企业主体也可以接入并使用企业微信能力，但会受人数上限、对外名片“未认证”标识及部分企业权益限制；应按当前企微后台已开通的能力和额度配置灰度范围。
3. `LLM_DEBUG_LOG=0`、`FEISHU_ALERT_TEST=0`。
4. `KF_INBOX_MAX_ATTEMPTS`、`KF_INBOX_LEASE_SECONDS` 和 `KF_INBOX_POLL_SECONDS` 已随 Compose 注入；后台“失败消息”页面可正常查询，测试消息重放后能离开 dead-letter。
5. HTTPS 反向代理只将企业微信回调与必要业务入口暴露公网；MySQL、Redis、Prometheus、Loki、Grafana、pprof 维持内网或本机访问。
6. 完成数据库备份、恢复演练和回滚镜像验证后再切换流量。

仓库 CI 会执行完整 Go 测试、`go vet`、前端 Vitest、development/production Compose 配置校验和可部署镜像构建。发布提交必须先通过这些门禁。
