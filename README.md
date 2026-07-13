# OpenBook · 基于 DeepSeek API 的垂直行业智能 Agent 助手

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
| 敏感词审核 | **trie 字典树 + LLM 双保险**：Layer 1 前缀树（51,345 词 / 6 大类 / JSON 热加载 / **实测 0.4μs/op**）+ Layer 2 LLM 兜底（关键词未命中时调小模型判灰区语义违规） |
| 可观测性 | in-process `atomic.Int64` 计数器 + 结构化日志 + `/metrics` 端点（Prometheus 格式，不引第三方库） |
| LLM 降级链 | DeepSeek → OpenAI → Ark 顺序 fallback，5xx/网络瞬时自动重试；**全挂时降级到 chat-only stub**（不调 LLM / 不触发 tool / 不写库），服务继续启动 |
| Token 监控 | eino callback handler 提取 `Message.ResponseMeta.Usage` 累加到 `/metrics` 端点（prompt / completion / total tokens + calls / errored） |
| 防滥用限流 | per-customer token bucket（`golang.org/x/time/rate`），LRU cap 10K，burst 5 / sustained 1 msg/s；超限返回"请稍后再试"+ 0 LLM 调用 |
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
- **规划**：PostgreSQL 17 + pgvector（v5.x 商家知识库 RAG）
- **接入**：企业微信客服 API
- **前端测试**：vitest（web/）

---

## 演进路线：RAG 与向量检索

### 现状（v4.17）

- **单文档 RAG**（`answer_from_document` 工具）：用户上传 PDF/文档问问题
- 实现：eino compose.Workflow 4 节点，**LLM-as-judge 评分**（无向量库）
  - `load` 读文件 → `chunk` 按段落切 ~800 char → `score` 每个 chunk 调 LLM 打 0-10 分（MaxConcurrency=5） → `filter` 取 top-3（score ≥ 3） → `answer` LLM 综合
- **不直接上向量库的原因**：
  1. **规模小**：单文档场景，< 1 MB LLM-scoring 完全够用（~35K token / 查询，~12s 延迟，~0.08 元）
  2. **精度高**：LLM 全文理解 > embedding cosine similarity
  3. **部署轻**：0 额外基础设施

### 演进路径（按规模）

| 文档规模 | 方案 | 触发条件 |
|---|---|---|
| < 1 MB | LLM-scoring（现状） | 单文档场景，量小 |
| 1-10 MB | **Hybrid 检索**：embedding 粗筛 top-20 + LLM 重排 top-3 | chunk 切分超过 100 |
| > 10 MB / 商家知识库 | **纯向量库**：bge-m3 + pgvector | 累积到一定规模 |

### v5.x 规划：商家知识库 RAG

**场景**：顾客问"有啥项目 / 染发有啥区别 / 现在有什么优惠"——这类非结构化查询，`list_services` 工具查不到。

**方案**：
- **embedding 模型**：bge-m3（本地部署，中文 SOTA，支持稠密+稀疏+多向量）
- **向量库**：**pgvector**（复用现有 MySQL 集群，运维负担最小）
- **Hybrid 检索**：向量召回 top-20 + Cross-Encoder（bge-reranker-large）精排 top-3
- **多租户隔离**：`shop_id` 作为 metadata 过滤，每个商家知识库独立
- **实时更新**：单条 upsert + 重建索引，商家改价目表秒级生效

**为什么选 pgvector**：
- 已有 MySQL，加 PG 实例 + pgvector 扩展，**0 新增组件类型**
- 10M 向量内性能够用
- 支持 metadata 过滤（`WHERE shop_id = ?`）
- GIN 索引 / HNSW 索引都支持

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
