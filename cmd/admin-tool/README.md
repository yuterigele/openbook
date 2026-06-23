# cmd/admin-tool

商户后台运维 CLI（小工具）。

## 用途

每次加新权限（permission）后，**老店铺的 `role_permissions` 表不会自动更新**——
因为 `SeedDefaultRolePermissions` 设计成只在表为空时才跑（保护运营在线调整）。

用本工具手动补全缺失的权限（**只补缺失，不删任何现有记录**）。

## 用法

```bash
# 1) 增量补齐缺失的 role → permission（推荐）
go run ./cmd/admin-tool perms reconcile

# 2) 查看某 role 的所有权限
go run ./cmd/admin-tool perms list                # 全部 role
go run ./cmd/admin-tool perms list owner          # 单个 role
go run ./cmd/admin-tool perms list staff

# 3) 整组覆盖某 role 的权限（运营调整用，会替换所有现有 perm）
go run ./cmd/admin-tool perms set staff view:dashboard,view:customers,view:events

# 4) 查某 role 是否有某 perm
go run ./cmd/admin-tool perms check staff view:notifications
```

## reconcile 行为保证

- ✅ **只补缺失**：对比 `defaultRolePermissions` + `AllPermissions`，缺啥补啥
- ✅ **不删任何记录**：运营手动调过的（比如 staff 拿掉某个 perm）完整保留
- ✅ **不破坏运营加的自定义 perm**：reconcile 不重置为 default
- ✅ **幂等**：重复跑无副作用
- ✅ **安全**：`role_permissions` 表的 UNIQUE 约束兜底，重复插不会 panic

## 什么时候需要跑

- 加新 permission 后（v4.10.1 加了 `view:notifications` / `retry:notifications`，需要 reconcile 一次）
- 新店铺上线（其实 Seed 第一次会跑全，但 reconcile 也安全无副作用）
- 数据库迁移/恢复后

## 历史背景

- v4.7 引入 RBAC 时只写了 `SeedDefaultRolePermissions`（表空才跑）
- v4.10.1 加 notification 权限时第一次踩坑：老店铺 owner 没新权限
- 修法：写 `ReconcileRolePermissions` 作为补丁，但**不在启动路径**自动跑（避免每次启动都扫全表）
- 改成运维按需触发
