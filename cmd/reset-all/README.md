# cmd/reset-all —— 一键重置 openbook 数据

清 MySQL + Redis 数据，支持 3 种范围。**默认 dry-run**，必须 `-yes` 才执行。

## 用法

```bash
# 看计划（不删任何东西）
go run ./cmd/reset-all -mode full
go run ./cmd/reset-all -mode agent
go run ./cmd/reset-all -mode shop -shop-id default

# 真执行（带 -yes + 三次确认 + 自动备份）
go run ./cmd/reset-all -mode full -yes
go run ./cmd/reset-all -mode agent -yes
go run ./cmd/reset-all -mode shop -shop-id default -yes

# 生产环境必须加 -force-prod（hostname 含 prod / yuyuanyuan 会触发）
go run ./cmd/reset-all -mode full -yes -force-prod

# 备份目录（默认 ./backups）
go run ./cmd/reset-all -mode full -yes -backup-dir /var/backups

# 跳过备份（危险，仅紧急恢复场景）
go run ./cmd/reset-all -mode full -yes -skip-backup
```

## 3 种模式

| 模式 | 范围 | 影响 |
|---|---|---|
| **`full`** | MySQL 19 张表全清 + Redis appt:lock/kf-debounce +（可选）FLUSHDB | 商户/顾客/预约/卡/Agent 全没了，重启服务会自动建默认 admin 账号 |
| **`agent`** | 只清 3 张 wecom 客服相关表（wecom_message_logs / kf_seen_msgs / kf_sync_states）+ Redis kf-debounce | 业务数据保留，Agent 行为从游标位置重新开始 |
| **`shop`** | 按 shop_id 清 17 张表 + 删 shops 表该行 | 删一个店，其他店完全不动。role_permissions 全局不动 |

## 安全机制

1. **dry-run 默认**：不带 `-yes` 只打印计划，不删任何东西
2. **强制备份**：默认执行 `mysqldump --single-transaction --routines --triggers`，备份失败拒绝继续
3. **三次确认**：连输 3 次 yes 才执行（防误操作）
4. **生产保护**：检测 MySQL hostname 含 prod / yuyuanyuan.cn 必须 `-force-prod`
5. **降级**：Redis 不可用时只清 MySQL，不报错

## 备份恢复

```bash
# 看备份
ls -lh ./backups/

# 恢复
mysql -u chatwitheino -p chatwitheino < ./backups/reset-all-20260627-105739.sql
```

## 典型场景

| 场景 | 命令 |
|---|---|
| 演示给客户看，要全新数据 | `go run ./cmd/reset-all -mode full -yes` |
| Agent 行为有问题，想从头跑 | `go run ./cmd/reset-all -mode agent -yes` |
| 演示店多了，要清一个 | `go run ./cmd/reset-all -mode shop -shop-id <id> -yes` |
| 紧急：生产环境全清 | `go run ./cmd/reset-all -mode full -yes -force-prod -backup-dir /safe/path` |