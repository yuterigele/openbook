# Starter Booking

这是一个最小可运行的预约 Agent 扩展示例，展示从 Web Chat 到工具白名单、Application 和预约内核的边界。

示例包含：

- `pet_grooming` 自定义行业 Profile；
- `list_services` 只读工具和 `create_booking` 写工具；
- 不从请求体读取 `customer_id`、商户、门店或权限；这些字段由 Host 的 `ContextResolver` 注入；
- 内存 Application 和 SQLite 预约内核测试；
- 可选的 MySQL 集成测试入口。

## 启动

在仓库根目录执行：

```bash
go run ./examples/starter-booking
```

默认监听 `http://localhost:8088/chat`。发送只读请求：

```bash
curl -X POST http://localhost:8088/chat \
  -H "Content-Type: application/json" \
  -d '{"tool":"list_services","parameters":{}}'
```

发送写请求时使用请求头提供幂等键；请求体只放业务参数：

```bash
curl -X POST http://localhost:8088/chat \
  -H "Content-Type: application/json" \
  -H "Idempotency-Key: demo-booking-1" \
  -d '{"tool":"create_booking","parameters":{"service_id":"wash_and_trim","start_at":"2026-08-29T15:00:00+08:00"}}'
```

## 验证

```bash
go test ./examples/starter-booking -count=1
go test -tags=mysql_integration ./examples/starter-booking -count=1
```

MySQL 测试使用 `MYSQL_DSN` 或现有 `MYSQL_HOST`、`MYSQL_PORT`、`MYSQL_USER`、`MYSQL_PASS`、`MYSQL_DB` 配置；没有 MySQL 时只运行不带 Tag 的内存/SQLite 测试。

复制这个目录时，优先替换 `StarterProfile` 和 Application 适配器，不要把身份校验、冲突判断或任意 SQL 放进 Profile 或模型工具参数。
