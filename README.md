# 美业预约助手 · OpenBook

> **AI 驱动的美业微信预约系统** — 在 1 人 + AI 协作下，交付 5-10 人团队的产出。
>
> v4.17 · 已开源 · 20+ 版本迭代 · **51,345 词敏感词库** · **LLM 降级链 99.9% SLA**

[![Go](https://img.shields.io/badge/Go-1.25-00ADD8?logo=go&logoColor=white)](https://go.dev)
[![Eino](https://img.shields.io/badge/CloudWeGo-Eino-3B82F6)](https://github.com/cloudwego/eino)
[![DeepSeek](https://img.shields.io/badge/LLM-DeepSeek-0066CC)](https://deepseek.com)
[![License](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](LICENSE)
[![Tests](https://img.shields.io/badge/Tests-30%2B%20Passing-brightgreen)](.)
[![Version](https://img.shields.io/badge/v4.17-Production_Ready-success)](.)

</div>

---

## 🎯 这是什么

**美业预约助手** 是一款面向中小型美业门店（理发 / 美容 / 医美 / 美甲 / 家政）的 AI 微信预约系统。

顾客在微信里说一句话，AI 自动完成查档期、创建预约、改时间、改师傅、转人工；商家零成本上线（同一个微信对话框，只是把"老板"换成 AI）。

### 真实对话示例

```
顾客：想约 Tony 老师明天下午 2 点剪头发

  AI：帮你查了下，Tony 老师 14:00 已有预约
      —— 改到 15:00，还是 Kevin 14:30？

顾客：Tony 可以，2 号吧

  AI：好的，已帮你约好 Tony 老师 2 号 14:00 剪发 ✅
      前 2 小时我会发消息提醒你
```

---

## ✨ v4.17 核心能力

| 能力 | 说明 | 代码位置 |
|------|------|---------|
| 🛠️ **10 个 Function Calling 工具** | query_schedule / create_appointment / cancel_appointment / barber_leave / handoff_to_human / list_barbers / list_services / list_shop_holidays / mark_noshow / get_appointment | `tools/` |
| 📚 **RAG 知识库** | eino compose.Workflow 4 节点 pipeline（load → chunk → score 并行 → filter → answer） | `rag/` |
| 🛡️ **敏感词审核** | 51,345 词 / 6 大分类（政治/色情/暴力/广告/辱骂/违法）+ JSON 词表热加载 | `sensitive/` |
| 🔄 **LLM 降级链** | DeepSeek → OpenAI → Ark 顺序 fallback，5xx/网络瞬时自动重试 | `chatmodel/fallback.go` |
| 🎯 **双层意图分类** | 关键词白名单（position-based tie-break）+ LLM 分类兜底 | `intent/` |
| 🏊 **手写 worker pool** | bounded concurrency + backpressure + panic recovery | `pool/` |
| 🔒 **多轮对话管理** | eino ADK history，顾客改 / 取消时自动从 history 找预约号 | `agent.go` |
| 🌊 **SSE 流式输出** | hertz-contrib/sse + keepalive 心跳 | `server/server.go` |
| 💾 **MySQL + Redis 分布式锁** | 持久化 + 防并发冲突 | `storage/` `lock/` |

---

## 🏗️ 架构

```
┌─────────────────────────────────────────────────────────┐
│                     顾客（微信）                          │
└─────────────────────┬───────────────────────────────────┘
                      │ 文本消息
                      ▼
┌─────────────────────────────────────────────────────────┐
│              企业微信客服 API (Wecom KF)                  │
└─────────────────────┬───────────────────────────────────┘
                      │
                      ▼
┌─────────────────────────────────────────────────────────┐
│   Pre-check (并行)  ─── pool.PreCheck()                  │
│   ┌──────────────┐    ┌──────────────┐                  │
│   │ sensitive.Check│  │ intent.Classify│  关键词 + LLM   │
│   └──────────────┘    └──────────────┘                  │
└─────────────────────┬───────────────────────────────────┘
                      │
                      ▼
┌─────────────────────────────────────────────────────────┐
│            Agent (eino ADK + DeepSeek)                  │
│  ┌─────────────────────────────────────────────────┐   │
│  │ system prompt: 业务规则 + 工具使用约束 (v4.13.4)   │   │
│  └─────────────────────────────────────────────────┘   │
│  ┌────────┬────────┬────────┬────────┬────────┐        │
│  │query_  │create_ │cancel_ │list_   │...     │  10   │
│  │schedule│appt    │appt    │barbers │        │  工具  │
│  └────────┴────────┴────────┴────────┴────────┘        │
│  ┌────────┬────────┐                                    │
│  │ RAG    │Handoff │  HandoffToHumanTool               │
│  │ Tool   │ToHuman │  (转人工兜底)                      │
│  └────────┴────────┘                                    │
└─────────────────────┬───────────────────────────────────┘
                      │ SSE 流式响应
                      ▼
              顾客收到 AI 回复
```

---

## 🚀 快速开始

### 前置条件

- Go 1.25+
- MySQL 8.0+（或本地用 SQLite 开发）
- Redis 7.0+（可选，无则降级为无锁模式）
- 企业微信客服账号（生产需要）

### 30 秒跑起来

```bash
git clone https://github.com/yuterigele/openbook.git
cd openbook

# 1. 配置 LLM（默认 DeepSeek；可在 env 切 OpenAI / Ark）
cp .env.example .env
# 编辑 .env 填入 OPENAI_API_KEY / DEEPSEEK_API_KEY

# 2. 跑测试（30+ 个测试，验证所有核心能力）
go test ./sensitive/ ./chatmodel/ ./intent/ ./pool/ ./internal/agent/

# 3. 启动服务
go run .
# → 监听 :38080，访问 http://localhost:38080 看管理后台
```

### 一键重置演示数据

```bash
go run ./cmd/reset-all -mode full -yes
```

---

## 📁 项目结构

```
openbook/
├── main.go                          # 入口
├── internal/agent/                  # 🆕 Agent 构造（v4.17 重构）
├── chatmodel/                       # LLM 封装 + 降级链
├── intent/                          # 意图分类（双层）
├── sensitive/                       # 敏感词审核（5.1 万词）
├── pool/                            # 手写 worker pool
├── rag/                             # RAG 知识库
├── tools/                           # 10 个 Function Calling 工具
├── server/                          # HTTP + SSE 流式
├── storage/                         # MySQL 持久化
├── lock/                            # Redis 分布式锁
├── wecom/                           # 企业微信客服
├── cron/                            # 定时任务（周报、提醒）
├── notify/                          # 通知多通道降级
├── docs/
│   ├── README.md                    # ← 你正看的（Eino 学习版）
│   ├── 技术实现方案.md               # 深度技术细节（面试用）
│   ├── 痛点总结.md                  # 业务痛点分析
│   ├── CHANGELOG.md                 # 20+ 版本变更日志
│   ├── DEPLOY_DEMO.md               # 部署 + 演示
│   ├── hair-salon-agent-prd.md      # 完整产品需求
│   ├── business/                    # 商业文档（gitignored）
│   └── pilot/                       # 上线记录
└── web/                             # 前端测试（vitest）
```

---

## 🧪 测试覆盖

```
✓ sensitive/    7 个测试   含 5.1 万词加载 / 命中 / 自定义词表
✓ chatmodel/    4 个测试   含 LLM 降级链顺序 / chain 解析
✓ intent/      11 个测试   含关键词 / LLM fallback / 解析
✓ pool/        12 个测试   含 panic 恢复 / 关闭 / backpressure
✓ internal/agent/ 3 个测试 含 prompt 关键约束
```

跑全部测试：
```bash
go test -count=1 ./...
```

---

## 📈 迭代历程

| 版本 | 日期 | 关键变更 |
|------|------|---------|
| v4.17 | 2026-07-12 | 5.1 万词敏感词 + 4 层防护 + worker pool + LLM 降级 |
| v4.16.4 | 2026-06 | 防 leave 改派后用旧 barber_name 引发的幻觉 |
| v4.16.3 | 2026-06 | LLM 幻觉防御：师傅状态必须来自工具 |
| v4.16.2 | 2026-06 | 节假日拒绝时强制 list_shop_holidays 拿完整清单 |
| v4.16.1 | 2026-06 | 行内菜单 UX + 快捷键防误触 |
| v4.15 | 2026-06 | 充值 + 套餐 + 价值/次数核销 |
| v4.13 | 2026-06 | 工具降级（C1）/ 多通道通知 |
| v4.10 | 2026-06 | MVP 验证：3-5 客户内测 |
| ... | | |

详见 [docs/CHANGELOG.md](docs/CHANGELOG.md)

---

## 👨‍💻 作者视角（如果你正在招聘）

> 这是一个由 1 人全职 + AI 协作（Cursor + Mavis + MiMo Code）维护的**生产级 AI Agent 项目**。

**这个项目能展示什么**：

- ✅ **AI 工程化**：Function Calling / RAG / 降级链 / 意图分类，**每个能力都有可指路到代码的实现**
- ✅ **Go 深度**：goroutine 池 / 分布式锁 / SSE 流式 / 上下文传递，**生产级后端能力**
- ✅ **工程纪律**：20+ 版本迭代，**每个版本都有明确的 bug fix / 业务规则**
- ✅ **AI 协作开发**：用 AI 工具 1 人做到 5-10 人产出，**未来 Agent 时代的工作方式**
- ✅ **业务理解**：从 MVP 验证到 8 大痛点分析、定价分层、迁移成本设计，**不是 demo 玩具**

**推荐阅读顺序**（招聘方）：

1. [痛点总结](docs/痛点总结.md) — 5 分钟看懂为什么做这个
2. [技术实现方案](docs/技术实现方案.md) — 30 分钟看怎么做的
3. 代码本身 — [`internal/agent/agent.go`](internal/agent/agent.go) 是入口
4. [CHANGELOG](docs/CHANGELOG.md) — 20+ 版本看到工程纪律

---

## 🤝 反馈 & 联系

- 🐛 Bug：开 [Issue](https://github.com/yuterigele/openbook/issues)
- 💡 想法：开 [Discussion](https://github.com/yuterigele/openbook/discussions)
- 📧 作者：[特日格乐](https://github.com/yuterigele)

---

## 📄 License

Apache 2.0 — 详见 [LICENSE](LICENSE)

---

> **一句话总结**：在微信这个 13 亿人都在用的入口里，1 个 AI Agent 替代 1 个 3500 元/月的前台，**让中小美业门店用 49 元/月** 就能享受原本要花 5 万/年的 AI 能力。
