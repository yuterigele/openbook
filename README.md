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

## 核心实现

| 主题 | 实现 |
|---|---|
| Agent 调用层 | 基于 `adk.MessageType` 泛型封装，支持聊天与工具循环两类消息边界 |
| 工具调用 | 白名单注册预约相关工具；工具层而非模型提示词负责业务校验 |
| 身份与越权防护 | 查询/取消预约以服务端注入的门店、消息身份和顾客归属三重约束 |
| 时间处理 | 运行时以 `Asia/Shanghai` 注入日期上下文，避免示例日期污染相对时间理解 |
| 一致性 | MySQL 持久化；Redis 分布式锁防止同一时段撞单 |
| 模型可靠性 | DeepSeek / OpenAI / Ark 客户端降级链；失败时退回不写库的 chat-only 响应 |
| 输入保护 | 敏感内容预检查、意图分类、限流与工具错误兜底 |
| 平台可观测性 | 仅平台超管可查看 Agent 任务/工具成功率、LLM Token 用量及 5 分钟阈值告警 |
| 本地交付 | Docker Compose 一键启动应用、MySQL、Redis；默认 mock 回复，不向微信客服会话发送消息 |

## 安全边界

顾客消息不拥有文件系统、Shell、数据库管理或店员操作能力。Agent 仅注册预约业务所需工具；预约查询与取消均忽略模型传入的身份字段，只使用服务端从微信客服会话或本地会话写入的身份上下文。

本地 Compose 默认仅监听 `127.0.0.1`，应用使用受限 MySQL 账号，不使用数据库 `root`。详细取舍与复盘见 [AI 工程复盘](docs/ai-engineering-notes.md)。

## 快速开始

### Docker（推荐）

```bash
cp .env.example .env
# 可选：在 .env 填写 OPENAI_API_KEY；默认 AGENT_REPLY_MODE=mock
docker compose up --build
```

打开 `http://127.0.0.1:38080` 体验本地聊天页，商户后台为 `http://127.0.0.1:38080/admin`。

容器说明与重置方式见 [容器 Demo](docs/CONTAINER_DEMO.md)。

### 本地运行

```bash
cp .env.example .env
go test ./tools ./internal/agent ./server
go run .
```

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
