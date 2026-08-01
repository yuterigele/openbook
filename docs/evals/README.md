# Agent 评测集

`intent-v1.json` 是版本化、离线的意图路由基准。它不调用模型、业务工具、MySQL 或 Redis，因此可以稳定地作为关键词基线和回归门禁。

```powershell
go run ./cmd/agent-eval
go run ./cmd/agent-eval -json
# 零成本关键词基线的回归门禁（当前套件要求全部通过）
go run ./cmd/agent-eval -min-accuracy 1 -min-critical-accuracy 1
# 仅在明确配置 SMALL_MODEL_* 后调用轻量模型；会产生模型费用。
go run ./cmd/agent-eval -strategy small -json
```

输出包含总体准确率、关键样例准确率、分类/调用来源维度分数、总耗时和失败样例。`keyword` 是零成本确定性基线；`small` 会读取 `SMALL_MODEL_ENABLED`、`SMALL_MODEL_API_KEY`、`SMALL_MODEL_BASE_URL` 和 `SMALL_MODEL_NAME`，未配置时明确失败，不会回退到主模型。

`-min-accuracy` 和 `-min-critical-accuracy` 可将评测结果变成非零退出码的质量门禁；传负数（默认值）表示只报告、不拦截。仓库 CI 固定运行关键词基线，并要求总体与关键样例准确率均为 100%。

新增能力时不要覆盖既有样例；新增版本或新 case，并记录失败原因、修复提交与是否为关键安全场景。运行对比时保存两份 JSON，比较准确率、关键样例准确率、`by_source`、总耗时和模型用量指标。

下一阶段：用同一 JSON 测试集接入轻量分类模型与主模型，分别保存 JSON 结果，比较准确率、误路由率、P95、主模型调用比例和 Token 成本。端到端预约、工具参数、越权拒绝与并发一致性使用独立的隔离环境评测集，不能混入此离线路由分数。
