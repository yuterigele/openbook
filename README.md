# 美业预约助手 · OpenBook

> **基于 DeepSeek API 的垂直行业智能 Agent 助手** · `2026 · 个人核心`
>
> 面向本地生活服务门店（理发 / 美容 / 家政等）的智能 Agent 助手。
>
> 顾客在微信里说一句话，AI 自动完成查档期、创建预约、改时间、改师傅、转人工；商家零成本上线。

[![Go](https://img.shields.io/badge/Go-1.25-00ADD8?logo=go&logoColor=white)](https://go.dev)
[![CloudWeGo Eino](https://img.shields.io/badge/CloudWeGo-Eino-3B82F6)](https://github.com/cloudwego/eino)
[![DeepSeek](https://img.shields.io/badge/LLM-DeepSeek-0066CC)](https://deepseek.com)
[![License](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](LICENSE)
[![Version](https://img.shields.io/badge/v4.17-Production_Ready-success)](.)

</div>

---

## 一句话

> 1 个 AI Agent 替代 1 个 3500 元/月的前台。让中小美业门店用 49 元/月，享受原本要花 5 万/年的 AI 能力。

---

## 真实对话

```
顾客：想约 Tony 老师明天下午 2 点剪头发

  AI：帮你查了下，Tony 老师 14:00 已有预约
      —— 改到 15:00，还是 Kevin 14:30？

顾客：Tony 可以，2 号吧

  AI：好的，已帮你约好 Tony 老师 2 号 14:00 剪发 ✅
      前 2 小时我会发消息提醒你
```

---

## 核心能力

| 能力 | 落地方式 |
|------|---------|
| Function Calling | 10 个工具（查档期 / 预约 / 改时间 / 改师傅 / 转人工 / ...） |
| RAG 知识库 | eino compose.Workflow，4 节点 pipeline（load → chunk → score → filter → answer） |
| 敏感词审核 | **51,345 词 / 6 大类**，JSON 词表热加载 |
| LLM 降级链 | DeepSeek → OpenAI → Ark 顺序 fallback，5xx/网络瞬时自动重试 |
| 意图识别 | 关键词白名单（position-based tie-break）+ LLM 分类兜底 |
| 并发控制 | 手写 worker pool（bounded + backpressure + panic recovery） |
| 状态一致性 | MySQL 持久化 + Redis 分布式锁（防撞单） |
| 流式响应 | hertz-contrib/sse + 15s keepalive 心跳 |

---

## 架构

```
顾客微信 → 企业微信客服 API → Pre-check（敏感词 + 意图,worker pool）
                                    ↓
                          eino ADK Agent
                       (DeepSeek primary + 降级链)
                                    ↓
            ┌──────┬──────┬──────┬──────┬──────┐
            │query │create│cancel│list  │handoff│
            │_sched│_appt │_appt │...   │_human │
            └──────┴──────┴──────┴──────┴──────┘
                                    ↓
                            MySQL + Redis
```

---

## 快速开始

```bash
git clone https://github.com/yuterigele/openbook.git
cd openbook

# 1. 配置 LLM（默认 DeepSeek，可在 .env 切 OpenAI / Ark）
cp .env.example .env
# 编辑 .env 填入 OPENAI_API_KEY / DEEPSEEK_API_KEY

# 2. 跑测试（30+ 用例，覆盖 5 大核心包）
go test ./sensitive/ ./chatmodel/ ./intent/ ./pool/ ./internal/agent/

# 3. 启动服务
go run .
# → 监听 :38080，访问 http://localhost:38080 看管理后台
```

更多见 [docs/DEPLOY_DEMO.md](docs/DEPLOY_DEMO.md)。

---

## 技术栈

- **语言**：Go 1.25
- **AI 框架**：CloudWeGo Eino（ADK + compose）
- **LLM 主力**：DeepSeek（中文最优性价比）
- **LLM 降级**：OpenAI / 字节 Ark
- **后端**：CloudWeGo Hertz + SSE
- **存储**：MySQL 8.0 + Redis 7.0
- **接入**：企业微信客服 API
- **前端测试**：vitest（web/）

---

## 文档

- [痛点总结](docs/痛点总结.md) — 行业 3 极分化 + 8 大痛点 + 8 个技术踩坑
- [技术实现方案](docs/技术实现方案.md) — 5 层防护 / LLM 降级 / Function Calling / 性能数据
- [CHANGELOG](docs/CHANGELOG.md) — 20+ 版本迭代
- [PRD](docs/hair-salon-agent-prd.md) — 完整产品需求
- [部署 / 演示](docs/DEPLOY_DEMO.md) — 上线 + 演示

---

## 版本

- **当前**：v4.17（生产就绪）
- **迭代**：20+ 版本（v4.10 → v4.17）
- **代码**：~15K Go（不含 vendored）

---

## 反馈

- 🐛 Bug：[Issue](https://github.com/yuterigele/openbook/issues)
- 📧 联系：[特日格乐](https://github.com/yuterigele)

---

## License

Apache 2.0 — 详见 [LICENSE](LICENSE)
