# OpenBook · 微信一句话预约的 Agent MVP

> 围绕“用户在微信说一句话完成预约”的场景，完成从意图识别、工具调用、业务落库到企业微信回复的 Agent 闭环。

[![Go](https://img.shields.io/badge/Go-1.25-00ADD8?logo=go&logoColor=white)](https://go.dev)
[![CloudWeGo Eino](https://img.shields.io/badge/CloudWeGo-Eino-3B82F6)](https://github.com/cloudwego/eino)
[![LLM](https://img.shields.io/badge/LLM-DeepSeek%20%2F%20OpenAI%20%2F%20Ark-0066CC)](.)
[![License](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](LICENSE)

## 项目定位

这是一个可运行的本地 Demo / MVP，不宣称生产就绪。它用美发预约验证 Agent 在真实业务中最容易出错的几个环节：相对日期理解、结构化工具调用、并发撞单、身份归属与失败兜底。

顾客无需安装企业微信，可使用普通微信通过两种入口完成查排班、预约、改约、取消、查询服务与转人工：

1. 添加店主或理发师的企业微信为好友，在原微信聊天窗口直接咨询；
2. 点击门店微信客服链接或扫描客服二维码，进入专用客服会话。

两条通道分别接入企业微信“客户联系”和“微信客服”能力，后端统一进入同一套 Agent、身份校验与预约业务流程。

```text
顾客：明天下午 2 点想约 Tony 剪发
Agent：识别日期与意图 → 查询可约时段 → 创建预约 → 写入 MySQL → 返回确认结果
```

## 项目结构

- `main.go`、`server/`：应用启动、HTTP 接口、会话处理、限流与回复流程。
- `internal/agent/`、`tools/`：Agent 编排及受限的预约业务工具。
- `storage/`、`lock/`：MySQL 持久化、Redis 锁、事务与租户/归属校验。
- `chatmodel/`、`intent/`、`sensitive/`：模型适配与降级、意图识别、输入保护。
- `wecom/`、`cron/`、`notify/`：企业微信、定时任务与通知。
- `api/`、`auth/`、`static/`、`web/`：商户后台、认证与前端页面。
- `docs/`：产品、容器、部署、基准和工程复盘。

## 核心实现

- Agent 仅可调用白名单预约工具；身份、门店和顾客归属由服务端上下文注入。
- MySQL 保存业务数据，Redis 锁防止同一时段并发撞单。
- 默认模型链为 DeepSeek → OpenAI → Ark；不可用时进入不调用工具、不写库的 Stub 模式。
- 运行时使用 `Asia/Shanghai` 日期上下文，并做敏感内容、输入信任、限流与工具错误保护。
- 平台超管可查看 Agent/工具成功率、Token 用量与阈值告警。

## 安全边界

顾客消息不拥有文件系统、Shell、任意 SQL 或商户后台能力。Agent 仅注册预约业务所需工具；查询、取消和改约均忽略模型传入的身份字段，只使用服务端从微信客服会话或本地会话写入的可信上下文，并校验门店隔离、资源归属和调用者权限。

模型失败或结果不可信时不得写库或宣称操作成功。真实密钥、企业微信凭据、数据库口令和 `JWT_SECRET` 只放本机 `.env` 或密钥管理系统，不得提交。

本地 Compose 的 HTTP、MySQL 和 Redis 默认仅监听 `127.0.0.1`；应用使用受限 MySQL 账号而非 `root`。这仍是本地 Demo，不是公网生产部署方案。详细取舍见 [AI 工程复盘](docs/ai-engineering-notes.md)。

## 快速开始

### Docker（推荐）

```bash
cp .env.example .env
# 至少填写一个模型提供商的凭据；默认优先 DeepSeek
# DEEPSEEK_API_KEY=...
# 修改 MYSQL_APP_PASSWORD 和 DEFAULT_*_PASSWORD
docker compose up --build
```

打开 `http://127.0.0.1:38080` 体验聊天页，商户后台为 `http://127.0.0.1:38080/admin`。Compose 默认 `AGENT_REPLY_MODE=mock`，回复只写入事件记录，不会发送到企业微信。

Compose 从宿主 `.env` 注入其 `environment:` 明确列出的变量；修改 `.env` 后执行 `docker compose up -d --force-recreate app`。容器说明、停止与重置方式见 [容器 Demo](docs/CONTAINER_DEMO.md)。

### 本地开发

```bash
# 先启动 MySQL、Redis 与专用业务账号
docker compose up -d mysql redis db-bootstrap
cp .env.example .env
# 按实际数据库更新 MYSQL_DSN 或 MYSQL_HOST/PORT/USER/PASS/DB
# 本地演示建议显式设置 AGENT_REPLY_MODE=mock
go run .
```

本地进程会直接读取 `.env`；必须确保数据库可连接。若复用 Compose 的数据库，业务账号为 `openbook`，密码取 `MYSQL_APP_PASSWORD`。

## 配置

完整配置项见 [`.env.example`](.env.example)。常用项如下：

- 模型：`OPENBOOK_LLM_CHAIN=deepseek,openai,ark` 控制顺序；分别配置 `DEEPSEEK_*`、`OPENAI_*`、`ARK_*`。设置 `OPENBOOK_LLM_CHAIN=stub` 可验证安全降级，不会调用模型、工具或写库。
- 数据：本地进程使用 `MYSQL_DSN` 或 `MYSQL_*`、`REDIS_*`；Compose 会覆盖应用的数据库地址并使用 `MYSQL_APP_PASSWORD` 创建受限账号。
- Agent：`AGENT_REPLY_MODE=mock` 禁止真实企微发送；`AGENT_MAX_EXECUTION_SECONDS`、`USER_INPUT_TRUST_THRESHOLD` 控制执行和输入保护。
- 管理端：修改 `DEFAULT_ADMIN_*`、`DEFAULT_PLATFORM_ADMIN_*` 和 `JWT_SECRET` 后再暴露服务。
- 企业微信：主要由 `shops` 表和商户后台管理；不要在 README、日志或仓库中记录真实凭据。

### 性能分析

本地 `go run .` 会在 `127.0.0.1:6060` 开启 pprof：`http://127.0.0.1:6060/debug/pprof/`。下载 `/debug/pprof/trace?seconds=5` 后可用 `go tool trace trace.out` 查看。不要将该端口暴露到公网。

Compose 将容器内 pprof 映射到宿主机 `127.0.0.1:6060`，可直接访问上述地址；端口不会暴露到公网。

## 测试重点

```bash
# 预约归属与取消越权回归
go test ./tools -run 'Test(GetAppointment|CancelAppointmentTool|E2E_S2_CancelAppointment)' -count=1

# Agent 与服务端编译/单测
go test ./internal/agent ./server -count=1
```

压测口径、样例请求和当前已知边界见 [benchmarks](docs/benchmarks.md)。

## 为什么暂不上向量库

预约的核心数据（理发师、服务、档期、预约状态）是强结构化且实时变化的数据，直接通过受约束业务工具查询，比向量检索更准确、可审计。当前 MVP 中，非结构化知识库不是成交闭环的瓶颈，因此不为“看起来像 AI”额外引入 embedding、索引同步与多租户召回复杂度。

当商家沉淀出大量价目、活动、服务说明等非结构化资料，并出现召回质量或查询延迟瓶颈时，再评估混合检索和向量库。完整判断过程见 [AI 工程复盘](docs/ai-engineering-notes.md#为什么当前不先上向量库)。

## 文档

- [AI 工程复盘](docs/ai-engineering-notes.md) — 相对时间、越权、提示注入、种子数据、容器化的复现与修复
- [容器 Demo](docs/CONTAINER_DEMO.md) — 本地 Docker 启动与安全边界
- [部署 Demo](docs/DEPLOY_DEMO.md) — systemd 裸机部署流程
- [benchmarks](docs/benchmarks.md) — 压测方案与记录模板
- [产品需求](docs/hair-salon-agent-prd.md) — 预约场景与业务规则
- [CHANGELOG](docs/CHANGELOG.md) — 20+ 次版本迭代记录

## 技术栈

- Go、CloudWeGo Eino（ADK / compose）、CloudWeGo Hertz
- DeepSeek、OpenAI、字节 Ark
- MySQL 8、Redis 7、Docker Compose、企业微信客服 API

## 迭代

- 20+ 版本迭代
- 重点演进：工具调用闭环、业务规则、身份隔离、时间处理、容器化与回归测试

## License

Apache 2.0，详见 [LICENSE](LICENSE)。
