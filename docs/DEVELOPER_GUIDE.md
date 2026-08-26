# OpenBook 开发者指南

这是一份面向第一次参与 OpenBook 开发的总览。它把“从 Clone 到第一次运行、调试和扩展”的路径串起来；业务规则和部署细节仍以链接到的专题文档为准。

## 1. 先判断你要走哪条路径

| 目标 | 推荐入口 | 说明 |
| --- | --- | --- |
| 只看完整预约 Demo | `docker-compose.demo.yml` | 不需要模型和企业微信凭据；Stub 不会调用工具或写预约 |
| 开发 Agent、工具或 Profile | `examples/starter-booking` | 最小可运行扩展示例，含内存/SQLite 测试和可选 MySQL 测试 |
| 联调真实存储 | 标准 Compose + `go run .` | 使用 MySQL、Redis 和本地进程，便于断点和日志排查 |
| 预发布或生产交付 | 环境分层文档 | 使用独立凭据、数据库、Redis 和回调，不能复用 Demo 数据 |

OpenBook 当前的公共早期契约是 `sdk/booking/v1alpha1`。它冻结操作名称、可信 `ExecutionContext`、权限和错误语义；业务请求/响应 DTO 仍可能演进，不能把 `v1alpha1` 当成已经冻结的长期公共 API。详见 [契约兼容说明](../COMPATIBILITY.md)。

## 2. 15 分钟 Quick Start

### 2.1 环境要求

- Go 1.25。
- Docker Desktop 或 Docker Engine，且 Docker Compose v2 可用。
- 修改 `web/` 时使用 Node.js 22。
- Windows 推荐 PowerShell；macOS/Linux 使用 Bash。

先确认工具可用：

```bash
go version
docker version
docker compose version
```

### 2.2 Clone 稳定代码

普通开发者应从稳定 `main` 或发布 Tag 开始，不要把内部 `codex/*` 分支当作安装入口：

```bash
git clone --branch main --depth 1 git@github.com:yuterigele/openbook.git
cd openbook
```

如果使用公开 HTTPS 地址，将 Clone 命令中的 SSH 地址替换为仓库的 HTTPS 地址即可。

### 2.3 启动无凭据 Demo

这是最快的本地体验路径：

```bash
docker compose -f docker-compose.demo.yml up --build
```

浏览器打开 `http://127.0.0.1:38080`；管理端是 `/admin`。Demo 固定使用 `OPENBOOK_LLM_CHAIN=stub` 和 `AGENT_REPLY_MODE=mock`，可以查看页面、演示数据和安全降级，但 Stub 不会调用预约工具，也不会写入真实预约。

检查容器状态和应用日志：

```bash
docker compose -f docker-compose.demo.yml ps
docker compose -f docker-compose.demo.yml logs --tail=100 app
```

停止 Demo 时，只有确认不再需要本地演示数据才使用：

```bash
docker compose -f docker-compose.demo.yml down -v
```

### 2.4 启动标准开发环境

标准路径使用本机未跟踪的 `.env`。先从示例复制，不要把真实凭据提交到仓库：

PowerShell：

```powershell
Copy-Item .env.example .env
docker compose up -d mysql redis db-bootstrap
docker compose ps
go run .
```

macOS/Linux：

```bash
cp .env.example .env
docker compose up -d mysql redis db-bootstrap
docker compose ps
go run .
```

如果复用 Compose 的 MySQL，业务账号和密码取 `.env` 中的 `MYSQL_APP_PASSWORD`。本地开发建议显式设置 `AGENT_REPLY_MODE=mock`，这样不会向真实企业微信发送消息。应用默认监听 `127.0.0.1:38080`；打开根路径即可检查 Web 页面。

第一次启动前也可以用 CLI 生成本地配置：

```bash
go run ./cmd/openbook init
go run ./cmd/openbook doctor
```

`init` 不会覆盖已有 `.env`；`doctor` 只报告配置存在性、长度和风险，不输出密钥、DSN 或 Token，也不替代真正的数据库/Redis 连通性检查。

## 3. 代码从哪里开始看

```text
渠道适配器 / Web Chat
        │  验签、去重、认证
        ▼
可信 ExecutionContext + InboundMessage
        │
        ▼
Agent Runtime / 显式 Tool Registry
        │
        ▼
v1alpha1.Application
        │
        ▼
预约领域规则 → MySQL 事务 / Redis 短锁 / Outbox
```

| 目录 | 负责内容 | 修改时先看 |
| --- | --- | --- |
| `main.go`、`server/` | 应用入口、HTTP、会话、限流、消息 Worker | [架构说明](架构说明.md) |
| `internal/agent/` | Agent 编排、模型循环和 Prompt | 工具白名单与失败降级测试 |
| `sdk/booking/v1alpha1/` | 操作、上下文、错误和 Application 端口 | [COMPATIBILITY.md](../COMPATIBILITY.md) |
| `sdk/toolkit/` | 工具元数据、权限、重试和结果 Envelope | [Tool/Channel 扩展教程](engineering/扩展Tool与Channel.md) |
| `sdk/channel/` | 已验签渠道消息与 Host Handler 的最小契约 | [Channel SDK README](../sdk/channel/README.md) |
| `sdk/profile/`、`profiles/` | 行业术语、服务、资源、时长和模板 | [新增行业 Profile](engineering/新增行业Profile.md) |
| `internal/booking/`、`storage/`、`lock/` | 预约规则、事务、租户隔离、锁和持久化 | [迁移规划](deployment/booking-migration.md) |
| `wecom/` | 企业微信验签、解密、路由和发送 | 不要把渠道字段直接当可信身份 |
| `cmd/openbook/`、`scripts/` | 初始化、迁移、生成、发布和排障脚本 | [CLI README](../cmd/openbook/README.md) |

## 4. 不可绕过的开发边界

### 4.1 身份只来自 Host

`merchant_id`、`location_id`、`customer_id`、`principal_id`、权限、`trace_id` 和写操作 `idempotency_key` 必须由已经完成认证/验签的 Host 注入 `ExecutionContext`。

以下位置出现同名字段时都不能覆盖可信上下文：

- 顾客消息正文；
- HTTP 请求体或模型工具参数；
- 渠道供应商的普通元数据；
- Profile 或 Tool 自己拼接的参数。

新渠道要先验签和去重，再构造 `sdk/channel.InboundMessage`；新工具要从 Handler 参数中的 `ExecutionContext` 读取身份。不要直接反序列化不可信 JSON 成 `ExecutionContext`。

### 4.2 工具必须显式注册

工具必须进入 `sdk/toolkit.Registry`，声明合法操作、读写模式、权限、重试策略和 Handler：

- 只读工具使用 `booking:read`，默认最多 2 次重试；
- 写工具使用 `booking:write`，最多执行 1 次；
- 未注册、权限不足、上下文不完整和非法结果都会被拒绝；
- 工具结果必须通过 `tool.result.v1` 校验，不能把数据库错误或密钥返回给模型。

预约创建、取消和改约必须保留幂等、事务、冲突检测和未知结果保护。模型失败或结果不可信时不能写库，也不能回复“操作成功”。

### 4.3 时间和数据边界

业务时间使用运行时的 `Asia/Shanghai` 上下文，区间使用半开区间 `[start_at,end_at)`。服务时长、前后缓冲、员工和联合资源冲突应由领域层校验，不要只写进 Prompt。

## 5. 第一次改代码的推荐顺序

1. 先复制并运行 `examples/starter-booking`，确认 Go、Compose 和测试环境正常。
2. 行业差异优先写成 Profile；不要先复制一套 Agent 或存储逻辑。
3. 需要新业务动作时，先确认 `v1alpha1.Operation` 是否已有对应操作；没有时先补契约、错误和测试，再写 Tool。
4. 渠道接入只负责验签、去重、解析和发送；业务身份与权限交给 Host。
5. 先写成功、非法参数、越权、冲突、重放和未知结果测试，再接入 Runtime。
6. 运行与风险相称的测试，更新公开行为对应的 README、`.env.example` 或 `docs/`。

快速生成扩展骨架：

```bash
go run ./cmd/openbook generate profile beauty_lab
go run ./cmd/openbook generate tool customer_note
go run ./cmd/openbook generate channel my_channel
```

生成器只创建不存在的文件，生成的 Tool 默认是“尚未实现”，不能直接加入生产白名单。完成扩展后执行：

```bash
gofmt -w <changed-go-files>
go test ./... -count=1
go vet ./...
git diff --check
```

## 6. 调试和验证入口

### 日志与 Compose

```bash
docker compose ps
docker compose logs -f app
docker compose logs --tail=100 mysql redis db-bootstrap
docker compose config -q
```

修改 `.env` 后，必须让 Compose 重新创建应用容器，确保变量重新注入：

```bash
docker compose up -d --force-recreate app
```

本地短时排查模型请求时可设置 `LLM_DEBUG_LOG=1`，并限制单条日志长度；排查完成后恢复为 `0`。不要把完整顾客对话、手机号、Token 或密钥复制到 Issue、PR 或日志附件。

### 测试分层

```bash
# 全量 Go 回归
go test ./... -count=1

# Agent 与消息入口
go test ./internal/agent ./server -count=1

# 预约归属、取消权限和关键 E2E
go test ./tools -run 'Test(GetAppointment|CancelAppointmentTool|E2E_S2_CancelAppointment)' -count=1

# 前端
cd web
npm test
```

离线意图评测和预约流程回归见 [评测集 README](evals/README.md)。真实 MySQL/Redis 联调需要 Docker 服务和对应 Tag；没有外部模型或企业微信凭据时，不要把 Stub/Mock 结果描述为真实渠道验收。

本地进程的 pprof 默认位于 `127.0.0.1:6060`；`/metrics` 用于 Prometheus 抓取。二者都不应暴露到公网。

## 7. 配置、提交和发布

- 配置入口：`.env.example`、[环境分层与发布约定](deployment/environments.md)、`go run ./cmd/openbook doctor`。
- 数据库迁移：先 `go run ./cmd/openbook migrate -dry-run`，再按维护窗口执行；旧预约迁移见[迁移规划](deployment/booking-migration.md)。
- 贡献规范：[CONTRIBUTING.md](../CONTRIBUTING.md)。
- 安全漏洞：[SECURITY.md](../SECURITY.md)。
- 生产发布：使用已构建的不可变镜像和 `scripts/compose-release.sh`，不要在生产覆盖文件中重新启用源码 `build`。
- 升级、回滚和三平台排障：[部署排障与升级回滚](deployment/platform-troubleshooting.md)。

提交前至少确认：没有秘密文件、`git diff --check` 通过、相关测试真实执行并记录结果。稳定分支应使用 `main` 或版本 Tag；阶段开发分支只用于协作，不作为普通开发者的长期依赖。

## 8. 继续阅读

- [新增行业 Profile](engineering/新增行业Profile.md)
- [扩展 Tool 与 Channel](engineering/扩展Tool与Channel.md)
- [Starter Booking](../examples/starter-booking/README.md)
- [环境分层与发布约定](deployment/environments.md)
- [旧预约迁移规划](deployment/booking-migration.md)
- [契约兼容说明](../COMPATIBILITY.md)
