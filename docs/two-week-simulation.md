# 50 店两周模拟数据

`cmd/simulation-seed` 为隔离的测试 MySQL 生成可重放的业务数据；默认是 50 家门店连续 14 天、每店每天 12 条预约，即 8,400 条预约。它不启动 HTTP 服务，不调用 Agent、LLM、企业微信或 Redis。

## 使用

先启动专用测试环境，并确认 `.env` 的 `MYSQL_DSN`（或 `MYSQL_*`）不是生产库。建议设置 `AGENT_REPLY_MODE=mock` 与 `OPENBOOK_LLM_CHAIN=stub`。

```powershell
# 默认 50 店、14 天，结束日期按运行当天（Asia/Shanghai）计算
go run ./cmd/simulation-seed

# 固定日期，便于复现与比对
go run ./cmd/simulation-seed -run-id july-2026 -end 2026-07-25

# 调整数据密度；相同 run-id + 参数 + seed 会得到相同的业务分布
go run ./cmd/simulation-seed -run-id high-volume -shops 50 -days 14 -appointments-per-day 24 -seed 20260725

# 只清除指定运行生成的数据
go run ./cmd/simulation-seed -run-id july-2026 -clean
```

同一个 `run-id` 不允许重复生成，避免在不知情时重复叠加数据。清理逻辑只删除 ID 以 `sim-<run-id>-` 开头的门店、师傅、顾客和预约，不会触碰 `[DEMO]` 或正常门店数据。

## 数据口径

- 每店 3 位师傅、20 位顾客；师傅和顾客 ID 均包含运行标识，满足现有全局唯一约束。
- 历史预约以 `completed` 为主，含少量 `cancelled` 与 `noshow`；最后一个模拟日保留部分 `active` 预约，便于验证查询与撞单。
- 所有日期按 `Asia/Shanghai` 生成；`-end` 是模拟数据的最后一天。
- 该命令只准备数据。若要测 HTTP/限流或 Agent 入口，应另行通过 mock/stub 驱动请求并记录延迟、错误率及最终数据库状态。
