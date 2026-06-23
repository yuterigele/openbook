# cmd/migrate —— 一次性手动迁移脚本

把 InitDB 里"只跑一次"的幂等迁移手动跑一遍，**不依赖重启服务**。

## 适用场景

- 老库从没重启过新版本 → schema 缺表/缺列
- 不想重启服务，但又想立刻把这些 backfill 跑一遍
- dev / staging 环境想快速追上 master

## 用法

```bash
# 跑全部迁移（默认）
go run ./cmd/migrate

# 先看会做什么，不真改 DB
go run ./cmd/migrate -dry-run

# 只跑指定步骤（逗号分隔）
go run ./cmd/migrate -only=schema,roleperm
go run ./cmd/migrate -only=platform
go run ./cmd/migrate -only=customers
```

## 步骤列表

| 步骤 | 干啥 | 对应版本 |
|---|---|---|
| `schema` | AutoMigrate 所有表（缺列自动加） | — |
| `roles` | 老 `shop_admins.role=''` → owner；`status=''` → active | v4.7 |
| `shopadmin` | seed 默认店铺 + 默认 admin 账号 | v4.7 |
| `roleperm` | seed role_permissions；缺失补 platform_admin | v4.7 + v4.9 |
| `platform` | seed platform_admin 账号（默认 platform/platform123） | v4.9 |
| `customers` | appointments.customer_id='' → 按 name 建顾客 + 累计字段 | v4.8 |

## 环境变量

跟主服务一致。**脚本启动时会自动读 `.env` 文件**（与 main.go 走同一个 `chatmodel.LoadEnv()`），
所以你只要把 DSN / 密码写在仓库根目录的 `.env` 里就行，不用每次手动 export。

| 变量 | 说明 | 默认 |
|---|---|---|
| `MYSQL_DSN` | 完整 DSN，例如 `user:pass@tcp(host:3306)/dbname?...` | — |
| `MYSQL_HOST/PORT/USER/PASS/DB` | 分项配置 | 127.0.0.1:3306 / chatwitheino |
| `DEFAULT_ADMIN_USERNAME/PASSWORD` | 默认店主账号 | admin / admin123 |
| `DEFAULT_PLATFORM_ADMIN_USERNAME/PASSWORD` | 默认超管账号 | platform / platform123 |

**优先级：**shell 里 export 的 env > `.env` 文件里的值（同 `chatmodel.LoadEnv` 的行为）

## 幂等保证

每一步都按"已存在则跳过"原则实现，**重复跑 0 副作用**。

## 输出示例

```
===============================================
 openbook DB migrate (v4.7 → v4.9)
===============================================

▶ [schema] ... OK
▶ [roles] ... OK
▶ [shopadmin] ... OK
▶ [roleperm] (补 platform_admin 20 条) ... OK
▶ [platform] (新建 platform / 密码 platform123) ... OK
▶ [customers] ... OK

===============================================
 完成: 跑了 6 步，耗时 234ms
===============================================
```

## ⚠️ 注意事项

1. **不重启服务也能跑**，但**仍然建议在维护窗口跑**——`roleperm` 和 `customers` 步骤会写多行。
2. **超管账号权限很大**，上线前务必改默认密码（设置 `DEFAULT_PLATFORM_ADMIN_PASSWORD` env）。
3. **不要在生产库直接 -dry-run 之外的命令**，除非你 review 过每个步骤会做什么。