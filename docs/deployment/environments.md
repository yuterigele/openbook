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

## 真实依赖并发演练

阶段性发布前可以在隔离的 MySQL/Redis 实例上运行真实写路径演练。测试使用随机商户和顾客 ID，结束时清理该商户的通用预约记录，不向企业微信或模型发送请求：

```powershell
$env:MYSQL_HOST = "127.0.0.1"
$env:MYSQL_PORT = "33060" # 使用 docker-compose.dev-ports.yml 时按实际映射调整
$env:MYSQL_USER = "openbook"
$env:MYSQL_DB = "chatwitheino"
$env:REDIS_ADDR = "127.0.0.1:6379"
# 预先在当前 PowerShell 会话安全注入 MYSQL_PASS，或改用 MYSQL_DSN；不要把密码写进脚本。
go test -tags="mysql_integration redis_integration" ./lock ./storage -run "Test(RedisLock|MySQLRedis)" -count=1
```

该命令覆盖 Redis 看门狗续租、锁被替换后的安全中止、释放后再次获取，以及多个请求争抢同一员工/资源时 MySQL 预约、Allocation 和 Outbox 的最终数量。依赖不可用时测试会跳过或直接报告连接错误；不能把跳过当作通过。

## 生产镜像升级与回滚

生产部署使用 `docker-compose.production.yml` 覆盖源码 `build`，只启动已准备好的 `OPENBOOK_IMAGE`。发布前先完成备份、恢复演练和数据库迁移兼容性检查，再执行：

```bash
export COMPOSE_ENV_FILE=.env.production.local
bash scripts/compose-release.sh deploy \
  registry.example.com/openbook:2026-08-26-c5b9065 \
  registry.example.com/openbook:previous
```

脚本会拉取目标镜像、启动 MySQL/Redis/初始化任务/app 并检查 HTTP 健康；新镜像不健康时自动尝试 previous-image，恢复健康后仍以非零状态退出，要求发布系统记录事故。也可以单独执行 `rollback <previous-image>`。设置 `OPENBOOK_PULL_IMAGE=0` 可用于部署机已缓存镜像的离线演练。

回滚只替换应用镜像，不会自动逆向数据库迁移。若新版本已经执行不可逆迁移，必须进入维护窗口，按备份恢复/反向迁移方案处理数据，并核对迁移报告后再切流；不能仅切回旧镜像并宣称完成回滚。

旧 `appointments` 到通用预约表的迁移边界和只读规划器见[旧预约迁移规划](booking-migration.md)。当前规划器不会连接或修改数据库；真实迁移执行器、生产回滚和反向迁移仍需单独评审。

## Release 镜像校验

推送形如 `v1.2.3` 的 Tag 会触发 `.github/workflows/release.yml`，构建 `linux/amd64` 和 `linux/arm64` 镜像，并将 Provenance、SBOM、Cosign 签名和镜像摘要作为 Release 产物。工作流只负责发布候选产物；真实生产切换仍需先完成备份、迁移兼容性检查和回滚窗口确认。

部署时优先使用 Release 产出的不可变 digest，而不是可变 Tag：

```dotenv
OPENBOOK_IMAGE=ghcr.io/yuterigele/openbook@sha256:<image-digest>
```

在受信任的发布机上核验镜像签名和摘要后再写入生产配置。下面的 `image-digest.txt` 和 `openbook-sbom.spdx.json.sha256` 是 Release 下载的文件，不要从日志或聊天记录复制摘要：

```bash
sha256sum -c openbook-sbom.spdx.json.sha256
cosign verify \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  --certificate-identity-regexp 'https://github.com/yuterigele/openbook/.github/workflows/release.yml@refs/tags/v.*' \
  "ghcr.io/yuterigele/openbook@$(cat image-digest.txt)"
docker buildx imagetools inspect "ghcr.io/yuterigele/openbook@$(cat image-digest.txt)"
```

如果签名、SBOM 摘要或多架构检查失败，不得继续执行 `compose-release.sh deploy`。`cosign verify` 的身份表达式必须与实际仓库和 Release 工作流保持一致；Fork 或组织迁移后要同步调整它。

## 安全扫描门槛

`.github/workflows/security.yml` 在 Pull Request、`main` 推送和每周定时任务中运行 `govulncheck`、Trivy 文件系统依赖扫描和 Gitleaks 历史秘密扫描；Release 工作流还会扫描最终多架构镜像。高危或严重漏洞、已提交秘密和扫描器错误都会阻断对应工作流。扫描器使用 `ignore-unfixed` 时只忽略尚无修复版本的依赖，不代表可以忽略已知可修复漏洞。

安全扫描通过不等于生产安全验收完成，仍需人工复核镜像来源、Cosign 身份、SBOM 摘要、配置密钥和迁移回滚窗口。
