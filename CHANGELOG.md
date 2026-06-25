# Changelog

本项目所有值得注意的改动都会记录在此文件。

格式参考 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/)，
版本号遵循 [Semantic Versioning](https://semver.org/lang/zh-CN/)（实际项目用 `vX.Y.Z` 业务版本号）。

---

## [v4.13.0] - 2026-06-25

v4.10.1 把 `/subscription` 锁给 `platform_admin` 但 handler 内部仍用 `shopFromClaims` —— 实际上
platform_admin 只能"管自己的 shop_id"（无意义）。v4.13.0 给 platform_admin 补齐真正的跨店管理能力：
列全平台店铺、改任意店铺套餐、看 audit log。前端新增「平台超管」nav 分区（仅 platform_admin 可见）。

投资人 demo 主操作屏：登录 platform_admin → 平台总览 / 店铺管理 / 套餐审计三屏流程。

### Added

- **后端 5 个 endpoint**（`api/admin_platform.go`）—— 全部 `RequireRole(RolePlatformAdmin)`
  - `GET  /api/admin/platform/stats` —— 平台 KPI（总店数 / 会员数 / 累计预约 / 月度收入估 / 7 天到期 / 冻结 / 套餐分布）
  - `GET  /api/admin/platform/shops` —— 全平台店铺列表（含 plan / 到期 / days_left / frozen / 成员数 / 累计预约 / 近 30 天活跃）
  - `GET  /api/admin/platform/shops/:id` —— 单店详情 + 订阅历史 + 成员列表
  - `PUT  /api/admin/platform/shops/:id/plan` —— 给某店开/改套餐（months 1-60，写 subscription + shop.plan + 取消旧 sub + 清 plan_active cache）
  - `GET  /api/admin/platform/audit?limit=100` —— 套餐变更审计日志（`event_type=plan_changed_by_admin`）
- **新 perm** `manage:platform` —— 只给 platform_admin，owner/staff 显式不列 → nav-item 自动隐藏 + 后端 403 双层防御
- **前端 Platform Admin 区**（`static/admin.html`）：
  - 新 nav 分区「平台超管」+ 3 个 nav-item（平台总览 / 店铺管理 / 套餐审计）
  - 平台总览：5 个 KPI 卡 + 套餐分布表（店铺数 / 月费 / 月小计 / 占比条）
  - 店铺管理：搜索 + plan / 状态过滤 + 表格 + 改套餐 modal + 详情 modal
  - 改套餐 modal：选 plan / 续费月数 / 备注，备注写入 audit log
  - 套餐审计：表格显示时间 / 店铺 / 原 plan / 新 plan / 月数 / 到期 / 操作人 / 备注 + 搜索
- **Audit log**：每次改套餐写 `event_logs`（event_type=plan_changed_by_admin），含 old/new plan、months、expires_at、admin_id、admin_username、note
- **测试** `api/admin_platform_test.go` —— 10 个用例覆盖 owner/staff 403、stats、shops list、shop detail、set plan（成功 + 400 各种 + 404）、audit 流

### Changed

- `storage/permissions.go`: 新增 `PermManagePlatform = "manage:platform"`，加入 `AllPermissions`，owner / staff 矩阵显式不列
- `static/admin.html`: 加 4 个 view 渲染函数 + 2 个 modal + nav-section-divider 样式 + 数据状态字段
- `api/api.go`: 注册 5 个新 platform 路由

### Fixed

- **platform_admin 改不了别店套餐**（v4.13.0 功能性 fix）—— 之前 `renewSubscriptionHandler` 用 `shopFromClaims`，platform_admin 实际只能改"自己 shop_id"（无意义）。Fix：走 `/api/admin/platform/shops/:id/plan`，从 path 参数拿 shop_id，platform_admin 真正能管全平台。

### Security

- 跨店改套餐严格 `RequireRole(RolePlatformAdmin)`，**单店 owner / staff 一律 403**（测试覆盖）
- 审计日志不可篡改：写到 `event_logs` 表，包含操作人 username + note（事后追责）
- 改套餐后立即 `auth.InvalidatePlanActiveCache(shopID)` —— 下次请求立刻看到新 plan，5 分钟缓存窗口缩短到 0

### 部署注意

**部署步骤**（v4.13.0 第一次部署）：

```bash
# 1) build + 部署后端
pwsh scripts/build-linux.ps1
scp -O chatwitheino-linux root@server:/home/www/wwwroot/agent.yuyuanyuan.cn/
# 2) 部署前端（带新 nav + 新 view + 新 modal）
scp -O static/admin.html root@server:/home/www/wwwroot/agent.yuyuanyuan.cn/static/
# 3) 重启
ssh root@server "systemctl restart chatwitheino"
# 4) 浏览器强刷
```

**DB 变化**：**无 schema 变化**（沿用 `subscriptions` / `shops` / `event_logs` / `admins` / `appointments` / `role_permissions` 表）。`role_permissions` 自动加新 perm `manage:platform`（platform_admin 通过 AllPermissions 拿到，owner / staff 不加）。

**验证**：

```bash
# platform_admin 拿得到 stats（200）
curl -H "Authorization: Bearer <platform_admin_token>" https://agent.yuyuanyuan.cn/api/admin/platform/stats

# 单店 owner 拿不到（403）
curl -H "Authorization: Bearer <owner_token>" https://agent.yuyuanyuan.cn/api/admin/platform/stats
# → {"error":"role 不允许（需 platform_admin）"}

# 验证新 perm 写进 role_permissions
ssh root@server "mysql chatwitheino -e 'SELECT * FROM role_permissions WHERE permission = \"manage:platform\"'"
# 期望：1 条（role=platform_admin）
```

### 投资人 Demo 路径（5 分钟流程）

1. **登录 platform_admin 账号** → 看到「平台超管」nav 分区（店主账号看不到）
2. **平台总览**：5 个 KPI + 套餐分布表（按 plan × 店铺数 × 月小计）
3. **店铺管理**：
   - 选一家 basic 店 → 点「改套餐」→ 选 flagship + 12 个月 + 备注"demo 升级" → 确认
   - 表格实时刷新：plan / 到期 / days_left 全部更新
4. **套餐审计**：
   - 看到刚改的那条记录（操作人 / 原 plan / 新 plan / 月数 / 到期 / 备注）
5. **回到「店铺管理」点「详情」** → 看订阅历史（之前 basic + 刚改的 flagship）+ 成员列表

### 留 v4.13.1 / v4.14

- 微信支付 + 续费 webhook（v4.13.1，platform_admin 仍可手动改套餐覆盖自动续费）
- 跨店事件漏斗 dashboard（v4.13.1+）
- 多 scope API key（v4.13.x）
- 分店 owner 自动建（v4.14）
- JS 测试基建（v4.14）

---

## [v4.12.1] - 2026-06-24

v4.12 plan 体系只列了 feature 没用 gate，v4.12.1 让 feature 真用起来：
data_export（CSV 导出）/ multi_store（建分店）/ api_access（API key）。
外加：改自己密码补旧密码验证（v4.12 安全 fix）+ W2 pilot 文档 + 邀请文案。

### Added

- **CSV 导出**（commit `28cac1e`，v4.12.1 第一块）—— `GET /api/admin/data/export?type=appointments&from=YYYY-MM-DD&to=YYYY-MM-DD&format=csv`
  - feature gate：basic 403 + `feature_required=data_export`，pro+ 200
  - UTF-8 BOM（Excel 兼容）+ 中文表头（日期/时间/理发师/客户/服务/状态/来源）+ status 中文映射
  - 默认区间：最近 30 天（缺 from/to）
  - admin.html 预约管理 view 顶部加 [导出 CSV] 按钮（`data-perm="view:plan"` 自动 hide）
- **multi_store gate**（commit `458f922`）—— `GET/POST /api/admin/shops`
  - Shop 加 `ParentShopID`（自引用，主店="" 分店=主店 id）
  - `CountShopsInGroup` / `ListShopsInGroup` / `CreateSubsidiaryShop` / `RootShopID` 4 个 helper
  - 基本 plan 限 1 店，旗舰限 5 店；建第 N+1 个 → 402 + `resource=shops`
  - 从分店建分店 → 403（必须主店账号）
  - 跨店隔离：shopB 看不到 shopA 分店
  - admin.html 加 "分店管理" nav view + 添加分店 modal
- **api_access**（commit `458f922`）—— `POST/GET /api/admin/api-keys` + `POST /api/admin/api-keys/:id/revoke`
  - 新表 `api_keys`（SHA256 hash，前 16 字符 prefix 用于展示；明文**只在创建时返一次**）
  - `auth.APIKeyAuth` + `auth.RequireAPIKeyScope(want)` 中间件
  - demo：`GET /api/external/appointments`（走 API key 鉴权 + `appointments:read` scope gate）
  - admin.html 套餐与升级 view 加 "API 访问" 卡片（basic 显示"仅旗舰版可用"，旗舰 [管理 API Key] 按钮 → 创建 / 列表 / 吊销）
- **W2 pilot 文档**（`PILOT-v4.12.md`）—— S9-S14 共 6 个新场景
  - S9 plan UI + 升级流程
  - S10 plan 过期冻结（关键路径，DB 改时间模拟）
  - S11 多店管理（basic 403 / flagship 建店 / 跨店隔离）
  - S12 CSV 导出
  - S13 API Key（生成 + external 调通 + 吊销）
  - S14 改自己密码
  - 邀请文案（贴群里）+ 反馈收集表

### Changed

- `storage/models.go`: Shop 加 `ParentShopID`（AutoMigrate 自动加列）
- `storage/models.go`: 新增 `APIKey` 结构 + `TableName()` 注册
- `storage/db.go` / `storage/testhelpers.go`: AutoMigrate 加 `&APIKey{}`
- `static/admin.html`:
  - 新 nav "分店管理" + 新 view `shops`
  - 新增 `applyElementPermVisibility`（非 nav-item 的 `[data-perm]` 元素也按 perm 隐藏）
  - 套餐与升级 view 加 API 访问卡片
  - 新增 modal：`subsidiaryCreate` / `apiKeyManage` / `apiKeyCreate`

### Fixed

- **改自己密码缺旧密码验证**（commit `458f922`，**v4.12.1 安全 fix**）
  - 之前 `changePasswordHandler` 只 BindAndValidate，不校验旧密码——**任何人有 JWT 就能改别人密码**
  - 现强制 `OldPassword` 非空 + bcrypt compare + 401 if wrong + 新密码 ≥ 6 位 + 新旧不可相同

### Security

- 同上（change-password 缺旧密码校验）。Token 泄漏场景下，原本能直接改任意账号密码。Fix 后必须知道旧密码。

### 部署注意

**1) 第一次部署 v4.12.1 必跑**：

```bash
# 后端 build + 部署（DB 自动 migrate：加 ParentShopID 列 + api_keys 表）
pwsh scripts/build-linux.ps1
scp -O chatwitheino-linux root@server:/home/www/wwwroot/agent.yuyuanyuan.cn/
scp -O static/admin.html root@server:/home/www/wwwroot/agent.yuyuanyuan.cn/static/
ssh root@server "systemctl restart chatwitheino"

# 验证 api_keys 表建好
ssh root@server "mysql chatwitheino -e 'SHOW TABLES LIKE \"api_keys\"'"
# 或 sqlite: sqlite3 /path/to/db 'SELECT name FROM sqlite_master WHERE type="table" AND name="api_keys"'

# 浏览器强刷
Ctrl+Shift+R
```

**2) 升级 modal 还是"联系商务"** —— 支付接入留 v4.13。

**3) 风险点**：

- v4.12.1 改了自己密码的前端 modal — **之前能改但缺校验**，现在强制要求旧密码——所有现有用户无影响（首次改密码时仍可正常改）。
- `POST /api/admin/api-keys` 返 plaintext **只这一次**，前端 prompt 显示明文 token + 强提示。**用户必须立即复制保存**——吊销后无法再查。
- API key last_used_at 暂不异步更新（v4.12.1 简化）—— 留 v4.13。
- 分店 owner 账号**不**自动创建——v4.12.1 仍需店主用现有"成员管理"手动建 admin（v4.13 自动）。

### Test

- `go test ./api/`: 23 个新 case
  - change_password: 6 (D)
  - shops: 9 (A')
  - api_keys + external: 8 (B')
- `go test ./...` — **0 回归**（仅 `TestE2E_S1_FirstAppointment` 预存 flaky hardcoded 2026-06-24 14:00 已过期）

### Metrics

- **新代码**: ~2200 行
- **新文件**: 9 个（`storage/api_keys.go` / `auth/api_key.go` / `api/shops.go` / `api/api_keys.go` / `api/external.go` + 4 个测试 + PILOT 文档）
- **修改文件**: 5 个（`api/api.go` / `storage/{db,models,shop_repo,testhelpers}.go` / `static/admin.html`）
- **新 commit**: 2 个（28cac1e + 458f922）

### 留 v4.13 / v4.12.2

- **支付集成**：升级 modal "联系商务" → 微信支付跳转 URL
- **续费 webhook**：微信支付回调 → 自动写 sub
- **API key last_used_at 异步更新**
- **API key 多个 scope**：customers:read / customers:write / reports:write 等
- **JS 测试基建**：admin.html 没前端测试基建（v4.12.1 没碰）
- **分店 owner 自动建**：v4.12.1 手动用 members API
- **启用接口**（disable → active）
- **platform_admin 跨店管理 UI**

---

## [v4.12] - 2026-06-24

W1 测试基建收尾 + plan 体系完整化 + 过期冻结 + plans UI + 老店铺矩阵自动迁移。
详见下面部署注意和 commit ref。

### Added

- **plan 体系完整化**（commit `661807e`）—— 4 档 plan 硬编码元数据 + renew handler 白名单校验
  - `basic` 99 元/月（1 店 3 barber） / `pro` 299 元/月（1 店 10 barber + data_export）
  - `flagship` 999 元/月（5 店不限 barber + api_access + multi_store + custom_report）
  - `enterprise` 按需谈（不限 + priority_support + sla_guarantee）
  - `storage/plan.go` 真理之源 + `PlanRegistry` map + 6 个 helper（IsValidPlanID/HasFeature/PlanLimitInt 等）
- **barber 数 limit gate**（commit `661807e`）—— createBarberHandler 调 `CheckPlanLimit`
  - basic 第 4 个 barber 返 402 Payment Required
  - 软删 barber 不算限额
- **plan 过期冻结 middleware**（commit `aca9fd8`）—— `auth.RequirePlanActive()`
  - 7 天宽限期（仍可用，UI banner 提示）
  - 冻结后返 402 + `frozen: true`
  - 5min sync.Map cache（避免每 request 1 次 DB）
  - renew handler 续费后自动清 cache
- **plans API + UI**（commit `da272f5` + `76ef2ae` + `6c0bff2`）—— owner 端"套餐与升级"页
  - `GET /api/admin/plans`（perm: view:plan）返 4 档对比 + 当前 plan + 倒计时 + grace_days
  - admin.html 新 view `plans`：当前 plan 卡 + 4 档对比 + 升级 modal（v4.13 留支付扩展点）
  - 顶部 banner：fresh 不显示 / 宽限期橙 / frozen 红
- **view:plan 新 perm**（commit `da272f5`）—— 区分"自己店 plan 元数据"和"订阅管理"
  - owner + platform_admin 有，staff 故意禁（plan 是经营决策）
  - 跟 v4.10.1 收紧的 view:subscription 区分
- **admin-tool perms migrate**（commit `268ecef`）—— 长期 fix 老店铺矩阵漂
  - 跟 reconcile 区别：reconcile 只补不删，migrate 先删 v4.10.1 收紧项再补 v4.12 缺失
  - 跑后列出"删除 + 新增 + perm 数 vs 矩阵长度"清单
  - 幂等可重跑
- **老店铺紧急修复 SQL**（`scripts/sql/fix-owner-perms-v4.12.sql`）
  - v4.10.1 部署时漏 migrate 老 DB——owner 矩阵还停在 v4.7 时代的 AllPermissions（含 3 个收紧项）
  - 这条 SQL 删 3 个收紧项 + 补 view:plan
  - **生产必跑**（commit `208279a` 写明但没人跑——这是教训）

### Changed

- **storage/permissions.go**：v4.10.1 改 storage 代码收紧 owner 矩阵（去掉 chain_dashboard / view:subscription / manage:subscription），但**没 migrate 老 DB**——commit `268ecef` 修了 deploy 流程
- **storage/permissions.go**：owner 矩阵加 view:plan（v4.12）

### Fixed

- **前端 nav 漂问题**（v4.10.1 / v4.11 三次漂过）—— 改用真 perm 矩阵驱动 nav 可见性（`applyRoleBasedNavVisibility` 走 `state.user.permissions`），删 ROLES_REQUIRED 字典
  - 后端 meHandler 返 permissions 字段，前端按 nav-item `data-perm` 属性匹配
  - 加 fallback（perms=[] 时退化全显示）防部署错位
- **staff nav 漏配 3 个 owner-only 菜单**（v4.11 commit `161f577`）—— 加 weekly / shop / services
- **v4.11 UI 错乱 + 数据未汉化**（v4.11 commit `934411b`）—— 补 CSS + 汉化 role + 加 admin_id 后缀
- **TestRBAC_Setup / TestCreateMember_DuplicateUsername** 2 个红测试（v4.11 commit `f4b7896`）
- **2 个 seed 噪音**（storage/permissions.go 的 log.Printf）
- **MakeAdminWithRole username 长度 footgun**（fail-fast）

### Security

- **生产 owner 矩阵泄漏修复**（commit `268ecef`）—— 老店铺 owner 不该有 view:chain_dashboard（v4.10.1 收紧）；前端显示后立刻 SQL 修

### 部署注意

**1) 第一次部署 v4.12 必跑**：

```bash
# A) 紧急 SQL（立刻修生产 owner 矩阵）
scp -O scripts/sql/fix-owner-perms-v4.12.sql root@server:/tmp/
ssh root@server "mysql chatwitheino < /tmp/fix-owner-perms-v4.12.sql"

# B) 后端 build + 部署
pwsh scripts/build-linux.ps1
scp -O chatwitheino-linux root@server:/home/www/wwwroot/agent.yuyuanyuan.cn/
scp -O static/admin.html root@server:/home/www/wwwroot/agent.yuyuanyuan.cn/static/
ssh root@server "systemctl restart chatwitheino"

# C) 跑 admin-tool migrate 验证（新版 binary 含 migrate 子命令）
ssh root@server "cd /home/www/wwwroot/agent.yuyuanyuan.cn && ./chatwitheino-linux --version 2>&1 || ./admin-tool perms migrate"

# D) 浏览器强刷
# Ctrl+Shift+R
```

**2) 部署前必查 production subs 表**：

```sql
-- 已过期 < 7 天 = 宽限期内
SELECT shop_id, plan, expires_at, DATEDIFF(NOW(), expires_at) AS days_overdue
FROM subscriptions
WHERE cancelled_at IS NULL
  AND expires_at < NOW()
  AND expires_at > NOW() - INTERVAL 7 DAY;

-- 已过期 > 7 天 = frozen（部署后这些店 402）
SELECT shop_id, plan, expires_at, DATEDIFF(NOW(), expires_at) AS days_overdue
FROM subscriptions
WHERE cancelled_at IS NULL
  AND expires_at < NOW() - INTERVAL 7 DAY;
```

**3) 未来加 perm / 收紧时**：

- 改 `storage/permissions.go` 的 `DefaultRolePermissions` 矩阵
- **改 `cmd/admin-tool/main.go` 的 `runPermsMigrate` 函数**（如果是 owner 收紧，加到 `ownerRemove` 列表）
- 写 commit message 提醒"部署后跑 `admin-tool perms migrate`"
- **v4.12 教训**：v4.10.1 写了"部署注意"但**没**自动 migrate——这次有 `admin-tool perms migrate` 工具化，下次必走

**4) 风险点**：

- `createBarberHandler` 加 402 gate 后，已 > 3 barber 的 basic 店主**不能再加 barber**（v4.12 设计）——如有超限，部署前手动调 plan 或删多余 barber
- `auth.RequirePlanActive` middleware 部署后立即生效——`plans` 表里 frozen 店**所有 admin endpoint 都 402**

### Test

- `go test ./storage/`: 6 个新（plan 字典 + 6 个边界）
- `go test ./api/`: 17 个新（plan gate 4 + plan expired 5 + plans API 4 + me 4）
- `go test ./cmd/admin-tool/`: 3 个新（migrate happy + 幂等 + 不删运营手动 perm）
- **0 个 frontend 测试**（项目里没 JS 测试基建——v4.12 没碰这个）

### Metrics

- **新代码**: ~2200 行（plan 体系 + middleware + API + UI + tests + CSS + SQL）
- **新文件**: 8 个（`storage/plan.go` / `storage/plan_gate.go` / `api/plans.go` / `scripts/sql/fix-owner-perms-v4.12.sql` + 4 个测试文件）
- **修改文件**: 12 个
- **新 commit**: 7 个（6 个 feat/fix + 1 个 hotfix）

### 留 v4.12.1 / v4.13

- **feature gate 实战**：plan 字典里**列了** `data_export` / `api_access` / `multi_store` / `custom_report` / `priority_support` / `sla_guarantee` feature，但 **handler 没真用** `HasFeature` 拦——v4.12.1 加 demo 端点
- **支付集成**（v4.13）：升级 modal 引导联系商务 → 接入微信支付
- **续费 webhook**（v4.13）
- **admin.html JS 测试基建**（v4.12.2 或之后）
- **启用接口**（disable → active，UI 反悔）
- **改自己密码 modal**（change:own_password 后端有前端没接）

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
