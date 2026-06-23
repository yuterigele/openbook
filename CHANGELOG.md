# Changelog

本项目所有值得注意的改动都会记录在此文件。

格式参考 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/)，
版本号遵循 [Semantic Versioning](https://semver.org/lang/zh-CN/)（实际项目用 `vX.Y.Z` 业务版本号）。

---

## [v4.10.1] - 2026-06-23

### Added

- **admin 后台通知中心**（P0-3 闭环）：商户后台新增"通知中心"页面，列出 leave notify 发送记录（sent / failed / skipped / pending），支持：
  - 状态 / 类型 / 关联请假 ID 筛选
  - 失败行红色高亮
  - 单条补发按钮（已 sent 拒绝重发，避免重复打扰）
  - 一键补发所有失败通知
  - 侧栏失败数 badge 提醒
- **CustomerNotification 持久化**：所有 leave notify 落 `customer_notifications` 表（type=leave_cancel/leave_reschedule/leave_no_contact）
- **storage/notification.go 基础设施**：
  - `ListNotificationsForShop` / `GetNotificationByID` / `RetryNotification` / `RetryShopFailedNotifications`
  - `SendWithRetry` 通用重试（指数退避 200ms→400ms→800ms）
  - `ChannelSelector` 通道选择（external_user_id → wechat_open_id → phone）
  - `ParallelSender` 并发发送（5 worker）
- **storage/notification_test.go** (+826)：12 个测试
- **api/notifications_test.go** (+470)：15 个 handler 测试
- **RBAC 自愈机制**：
  - `auth.RequireRole(allowedRoles ...string)` 中间件（直接比对 claims.Role，不查 DB）
  - `storage.ReconcileRolePermissions` 增量补全函数（只补缺失，不删任何记录——保护运营调整）
  - `cmd/admin-tool` 运维 CLI：`perms reconcile / list / set / check` 子命令
  - `api/setup_test.go` 新增 `runWithRole` 测试 helper
- **平台/运营层模块收紧**：
  - 链看板 / 跨店周报 / 订阅全部归 `platform_admin` 角色
  - 周报拆分：单店周报（owner / staff 拒绝 / platform_admin 看自己店）vs 跨店周报（仅 platform_admin）
- **Agent 对话优化**：
  - `msgops.RoleOf` helper（区分 chatter assistant vs tool call assistant）
  - `server.shouldPersistIntermediate` 过滤中间步骤纯文本 chatter
  - `makeOnAgentEvents` 持久化时只保留 tool_call / tool_result + 用 lastContent 补最终回复
  - system prompt 加 3 条回复合并指引

### Changed

- **权限矩阵收紧**（v4.10.1）：
  - `owner` 从 `AllPermissions` 兜底改为显式列（21 条），方便审计
  - owner 去掉：`view:chain_dashboard` / `view:subscription` / `manage:subscription`（v4.10.1 收走）
  - owner 加回：`view:weekly_report`（拆分后 owner 能看自己店周报）
- **前端 nav-item 按 role 隐藏**：
  - 新增 `ROLES_REQUIRED` 字典 + `applyRoleBasedNavVisibility(role)` 函数
  - 隐藏：chain / subscription / chain-weekly（仅 platform_admin）
  - 可见：weekly（owner / platform_admin 都能看）
- **路由改用 RequireRole(platform_admin)**：
  - `GET /api/admin/chain/dashboard`
  - `GET /api/admin/weekly-report/chain`
  - `GET /api/admin/subscription`（list）
  - `POST /api/admin/subscription/renew`
- **leave notify 发送链路升级**（v4.9.3 之上叠加）：
  - 多店路由：`Router.LookupByShopID(shopID)` 避免 A 店顾客发到 B 店 KF
  - 按 shopID 多店路由 + 通道降级 + 3 次退避
  - 顾客无联系方式时返回 `ErrNoCustomerContact`，写 skipped row 不报错
  - `CustomerFacingReason` 隐私脱敏（避免暴露"痔疮手术"等敏感信息）

### Fixed

- **权限泄漏修复**（v4.0 MVP 留下的 P0 漏洞）：
  - 之前任何 admin 都能看多店看板（单店 owner 能看全平台所有店）→ 现在仅 platform_admin
  - 之前 owner 默认有 `view:subscription` / `manage:subscription` → 现在收走
- **Agent 重复/不相干回复**：
  - 根因：DeepAgent 一次 run 多个 assistant block 全 append 到 session history，下次 LLM 看到自己之前的 chatter 接着说
  - 修复：filter 纯文本 chatter，session history 只保留 tool_call + tool_result + 最终回复
- **leave notify 多店路由**：A 店顾客发到 B 店 KF（93001900）→ 按 `Shop.OpenKfID` 路由
- **storage/permissions.go reconcile** 修复 owner 误用 `AllPermissions`：之前 owner 走 AllPermissions 兜底，加新 perm 后老店铺拿不到，reconcile 用显式 default 矩阵解决

### Removed

- （无）

### Security

- **多店数据隔离加固**：所有跨店接口用 `RequireRole(platform_admin)` 强约束 + handler 不依赖 owner 权限
- **重复发送防护**：`RetryNotification` 拒绝重发已 sent 通知（409 + new_status）
- **session history 净化**：过滤中间 chatter 避免 LLM 看到自己说过的"过渡语"

### 部署注意

加新权限后老店铺需手动补全：
```bash
go run ./cmd/admin-tool perms reconcile
# 或 SQL：
INSERT OR IGNORE INTO role_permissions (role, permission) VALUES
  ('owner', 'view:notifications'), ('owner', 'retry:notifications'),
  ('staff', 'view:notifications'), ('staff', 'retry:notifications');
```

收紧权限后老店铺需手动删：
```sql
DELETE FROM role_permissions WHERE role = 'owner' AND permission IN
  ('manage:subscription', 'view:subscription');
-- view:weekly_report 如果之前删过，需要加回来
INSERT OR IGNORE INTO role_permissions (role, permission) VALUES
  ('owner', 'view:weekly_report');
```

---

## [v4.9.3] - 2026-06 中

### Fixed

- 请假通知 81013：`SendTextMessage` 错用自动回退到 KF 接口
- `fix-customers` 脚本查询排除 NULL 顾客
- `NULLS LAST` SQL 报错 + `fix-customers` 工具修复
- 透传 `external_user_id` + 修外部联系人路径完全没传 wecom ID
- 预约必填手机号 + 严格 11 位 + 开屏

### Docs

- `scripts/sql/comments.sql` 给字段 COMMENT 补全
- `scripts/sql/cleanup_no_openid.sql` 一键清理无 openID 顾客
- v4.7-v4.9 关键设计决策补到代码注释

---

## [v4.9.2] - 2026-06 初

### Changed

- agent 历史消息精简：默认 6 条 + 12k 字符预算（v4.9.1 是 10 条太多，导致 prompt 爆 token）

---

## [v4.9.1] - 2026-05 末

### Added

- `cmd/migrate` 一次性手动迁移脚本（自动读 `.env`）

### Fixed

- migrate 脚本自动读 `.env`

---

## [v4.9] - 2026-05

### Added

- `platform_admin` 超管角色
- 服务目录跨店展示

---

## [v4.8.1] - 2026-05

### Fixed

- 顾客详情 modal 一直 loading 骨架屏

---

## [v4.8] - 2026-05

### Fixed

- `CreateAppointment` 不建顾客档案 → admin 顾客列表 / 详情 404

---

## [v4.7] - 2026-04

### Added

- RBAC：15 个细粒度 permission + `role_permissions` 表 + `auth.RequirePerm` 中间件
- admin 角色区分：owner / staff / platform_admin

### Fixed

- 给 admin 加 `role` 字段 + `RequirePerm` 返 403，加 backfill 兜底

---

## [v4.6] - 2026-04

### Added

- 顾客详情 / 转人工已处理 / 服务批量导入

---

## [v4.5] - 2026-03

### Added

- 主体功能打磨：A1+A2+A5+B1+B3+C1+C2+D3

---

## [v4.4] - 2026-03

### Added

- 周报 cron + 服务目录 + 后台 5 个新模块（订阅 / 服务 / 顾客详情 / 转人工 / 多店看板）

---

## [v4.3] - 2026-02

### Added

- 跨店周报 cron 触发 + 完善 `.gitignore`

---

## [v4.2] - 2026-02

### Added

- D+15 使用报告邮件：storage report + notify/email + lifecycle 集成

---

## [v4.1] - 2026-01

### Added

- 跨店看板时间窗口切换：`?window=today|week|month` + 13 个新单测

---

## [v4.0] - 2026-01

### Added

- 跨店看板 `/api/admin/chain/dashboard` + 16 个新单测
- `wecom.Router` 多店路由

---

## [v3.9] - 2025-12

### Added

- 转人工兜底工具 + dashboard 待人工卡片

---

## [v3.8] - 2025-12

### Added

- dashboard 事件漏斗

### Fixed

- 去掉 pre-existing SQL warning

---

## [v3.7] - 2025-11

### Added

- 改派策略升级：`findAlternateBarber` 三档分级（Skills 匹配 → Skills 为空 → 兜底）

---

## [v3.5 + v3.6] - 2025-11

### Added

- `LeaveExpirer` cron：end_at < now 的 active leave 自动 expired
- `query_schedule` / `list_barbers` visual split

---

## [MVP] - 2025-08 之前

### Added

- v1.0：商业计划 + 技术方案 + 痛点总结
- v3.4：P4 barber leave（data layer / REST API / admin UI / tool integration）
- v3.x：理发师管理 / 多店路由 / 跨店看板 / 订阅体系 / D+15 邮件 / 续费漏斗埋点
- v2.x：MCP Agent + 企业微信回调 + 多店 Crypto 路由

---

[Unreleased]: https://github.com/yuterigele/openbook/compare/v4.10.1...HEAD
[v4.10.1]: https://github.com/yuterigele/openbook/compare/v4.9.3...v4.10.1
[v4.9.3]: https://github.com/yuterigele/openbook/compare/v4.9.2...v4.9.3
