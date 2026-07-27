# Benchmark Baseline

本文件记录 OpenBook 的可复现性能基线。当前项目仍处于可运行 Demo / MVP 阶段，未声称线上吞吐能力；所有压测结论均应附带机器配置、命令、并发和依赖版本。

## 已有微基准

### 敏感词匹配

目标：验证 51,345 词敏感词库在本地内存中的匹配开销。

```powershell
go test ./sensitive -run '^$' -bench 'BenchmarkCheck_(Hit|Miss)$' -benchmem -count 5
```

已记录基线（2026-07-13，详见 `docs/CHANGELOG.md`）：

| 场景 | 结果 |
| --- | --- |
| 命中 | 393.9 ns/op，80 B/op，1 alloc/op |
| 未命中 | 412.3 ns/op，0 B/op，0 alloc/op |

不同机器的绝对值不可直接横比；提交结果时应保留完整终端输出。

## 本地并发流量模拟（可直接量化 Token 节省）

`cmd/loadtest` 复用生产默认的两级限流参数（每顾客 `1 req/s`、burst `5`；全局 `100 req/s`、burst `200`），并发模拟入口流量。它不会启动 HTTP 服务、调用模型或连接 MySQL/Redis，因此不会产生模型费用或污染业务数据。

```powershell
# 分散用户攻击：10,000 个请求、100 并发、1,000 位顾客；每次调用估算输入 600、输出 200 Token
go run ./cmd/loadtest -requests 10000 -concurrency 100 -customers 1000 -input-tokens 600 -output-tokens 200 -input-cny-per-1k 0.001 -output-cny-per-1k 0.002

# 单人暴力：验证单顾客 burst=5 对模型调用/Token 上限的保护
go run ./cmd/loadtest -requests 10000 -concurrency 100 -customers 1 -input-tokens 600 -output-tokens 200 -input-cny-per-1k 0.001 -output-cny-per-1k 0.002

# 机器可读结果，便于保存到 CI 或报表
go run ./cmd/loadtest -requests 10000 -concurrency 100 -customers 1000 -json

# 验证超时不会永久占用 worker：100ms 模拟处理耗时会被 20ms 请求上限中断
go run ./cmd/loadtest -requests 1000 -concurrency 100 -customers 1000 -simulated-work 100ms -request-timeout 20ms
```

报告中的“基线”是假设每个进入请求都直接触发一次模型调用；“优化后”只对实际放行请求计 Token。节省率为 `(基线 Token - 优化后 Token) / 基线 Token`。输入/输出 Token 与单价分别通过 `input-tokens`、`output-tokens`、`input-cny-per-1k`、`output-cny-per-1k` 设置，必须替换为目标模型的实际监控均值和采购价格；它们不是线上账单，也不代表 HTTP、模型或数据库的端到端吞吐。

建议将两种攻击专项压测分开记录：分散用户攻击使用 `customers=1000`，单人暴力使用 `customers=1`。后者能直接展示每顾客 burst=5 对 Token 风险的抑制效果；两者都不是正常预约对话的到达节奏。

### 用真实 API Usage 校准估算

该模拟器不调用模型，因此不会伪造真实用量。用隔离的测试环境对目标模型跑一轮固定请求集，在运行前后抓取 `/metrics`，计算 `openbook_llm_prompt_tokens_total` 与 `openbook_llm_completion_tokens_total` 的增量，再除以成功模型调用数。将所得均值写回 `input-tokens` 与 `output-tokens`；同时将供应商当前输入/输出单价分别填入对应价格参数。保留“预估 Token、Usage 实测 Token、误差率”三列，才能持续校准成本结论。

每个模拟请求都受 `-request-timeout`（默认 `3s`）约束，超时会计入 `timed_out` 并释放 worker；`-simulated-work` 默认是 `0s`，只用于本地验证超时机制。将来接入真实 HTTP 目标时，下游请求也必须使用该请求 context，才能真正中止网络 I/O 或模型调用。

## 待完成的端到端压测

### 目的

验证 HTTP 接入、限流、worker pool 与数据库预约写入在 Demo 环境中的行为；LLM 使用 mock/stub，避免将外部模型延迟和费用混入服务端性能结论。

### 环境记录模板

| 项目 | 待填写 |
| --- | --- |
| 日期与 commit | |
| CPU / 内存 / 操作系统 | |
| Go 版本 | |
| MySQL / Redis 版本与部署方式 | |
| `OPENBOOK_POOL_SIZE` / queue | |
| LLM 模式 | mock 或 stub（不得用真实付费模型） |

### 场景与验收项

| 场景 | 并发 | 关注指标 |
| --- | ---: | --- |
| 查询档期 | 10 / 50 / 100 | RPS、P50/P95/P99、错误率 |
| 同师傅同一时段抢约 | 10 / 50 | 至多一条预约写入、冲突返回正确 |
| 限流 | 单用户突发 20 请求 | 前 5 个允许、超限请求不触发 LLM |
| Provider 不可用 | 10 | 服务可启动、返回友好话术、无业务误写入 |

### 执行前检查

1. 使用独立压测数据库和 Redis，禁止连接真实商户数据。
2. 固定 seed 数据、时区和预约时段，确保抢约场景可重复。
3. 每个场景先 warm-up 30 秒，再记录至少 60 秒结果。
4. 保存请求总数、成功数、错误分类、延迟分位和数据库最终记录数。

## RAG 方案边界

当前 RAG 面向单文档、低频问答：`compose.Workflow` 先分块，再由 LLM 对各 chunk 评分，取 Top-3 合成答案。此时不引入向量库的原因是：

1. 文档规模小，全文分块评分的部署成本更低，且无需维护 embedding、索引和重建链路。
2. 业务尚未进入多商家、多文档、高并发检索阶段，先验证回答质量和工具闭环更有价值。
3. 当 chunk 数量、文档规模或查询延迟达到阈值后，再演进为 embedding 粗召回 + 重排，或采用 pgvector。

这是一项当前规模下的工程取舍，不等同于“向量检索不需要”。后续应通过压测和真实对话数据定义迁移阈值。
