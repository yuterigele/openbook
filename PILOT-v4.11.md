# PILOT v4.11 内部测试场景

> 给非开发同学的 8 个真实场景脚本。每个场景有明确的"通过/失败"标准。
> 配合 W1 测试基建（这次 commit `f4b7896`）做最后一道人工验证。
> 反馈渠道：[见末尾"反馈渠道"](#反馈渠道)

## 目的

v4.10.1 发了 27 个文件 / +4389 / -120，**功能封盘**状态。W1 修完了自动化测试能抓的，
但还有几类 bug 只有真用户能触发：

- 跨角色 + 跨店的边界组合（ownerA 误操作店 B 数据）
- Agent 对话质量（重复回复、答非所问、多条消息轰炸）
- 通知发送失败的实际场景（网络问题 / 微信接口限流）
- 移动端 UI 死角（小屏 / 微信内嵌浏览器）
- 业务逻辑的反直觉行为（last-owner 保护会不会误伤）

8 个场景覆盖：成员管理 / 请假 / Agent 对话 / 通知中心 / 跨店隔离 / 权限边界 / 周报 / 预约核心流程。

## 前置准备

### 测试账号

每家测试店需要准备：

| 角色 | 账号 | 权限 | 用途 |
|---|---|---|---|
| owner | `owner@shop-A` | 全权限（本店）| 主测账号 |
| staff | `staff@shop-A` | 看 + 业务操作 | 测"店主看得见/店员看不见"边界 |
| owner (店 B) | `owner@shop-B` | 全权限（本店）| 测跨店隔离 |

至少需要 2 家店（A + B）。如果只有 1 家店的环境，可以**临时**用 admin-tool 复制一个：
```bash
# 注意：这只是测环境
mysql -e "INSERT INTO shops (id, name, ...) SELECT 'shop-B-test', '测试 B 店', ... FROM shops WHERE id='default';"
```

### 微信测试顾客

至少 3 个真实微信号（或 WeChat 测试号）做"顾客"角色：
- 顾客甲：测预约 + 通知
- 顾客乙：测 Agent 对话
- 顾客丙：测请假通知（确保请假那个时段没冲突预约）

### 浏览器

- 主测用 Chrome 桌面版（开发工具能看 network / console）
- 至少 1 个 case 用微信内置浏览器测（见场景 4）

---

## 场景 1：新员工入职（成员管理 — **前端未实装，本场景暂不测**）

**⚠ 状态**：v4.7 写了后端 API（`api/members.go`：list / create / changeRole / resetPassword / disable）+ 完整单测，
**但 admin.html 14 个 nav-item 里没有"成员管理"**（已 grep 验证）。pilot 阶段前端没接通。

**pilot 期间用什么替代测**：
- 用 **`admin-tool` 工具** 或 **curl 直接打 API** 测后端（见场景 1.1）
- 不让运营同学点 UI（点了也没东西可点）

**何时启用 UI 测**：v4.11+ 把前端 nav-item + 成员管理页面补上之后。

---

### 场景 1.1：成员管理 API 烟囱测（admin-tool / curl）

**目的**：验证后端 RBAC + 跨店保护 OK（绕过 UI，纯 API 测）

**角色**：owner（已登录获取 JWT） + 有 DB / SSH 权限的工程师

**步骤 1-3**（建 staff）：

1. SSH 到服务器 / admin-tool，用 owner 调：
   ```bash
   curl -X POST https://agi.yuyuanyuan.cn/api/admin/members \
     -H "Authorization: Bearer $OWNER_JWT" \
     -H "Content-Type: application/json" \
     -d '{"username":"test-staff-1","password":"Test123456","role":"staff"}'
   ```
2. 期望：200，`"username":"test-staff-1"`，`role=staff`

**步骤 4-5**（staff 登录 + 测无权限）：

4. 用 `test-staff-1` 登录拿 JWT（POST `/api/auth/login`）
5. 调 `GET /api/admin/members` 用 staff JWT

**期望**：

- [ ] 建 staff 200
- [ ] staff 调 `/api/admin/members` 返 **403**（无 PermManageMembers）
- [ ] staff 调 `GET /api/admin/chain` 返 **403**（v4.10.1 收紧：staff 故意没 view:chain_dashboard）
- [ ] staff 调 `GET /api/admin/subscription` 返 **403**（v4.10.1 收紧）

**步骤 6**（跨店保护 — 已有跨店测试覆盖，这里 API 再 smoke 一下即可）：

6. ownerA 调 `PUT /api/admin/members/<ownerB_id>/role`

**期望**：

- [ ] 返 403 "无权操作其他店铺的成员"

**反直觉点**：

- 后端 5xx / 没拒绝 → RBAC 中间件漏挂
- staff 能调 → 权限矩阵漂了

---

## 场景 2：请假申请（核心功能 + leave notify）

**目的**：测 owner 替理发师请假 → 受影响顾客收到通知 → 取消请假

**角色**：owner（已登录）+ 顾客甲（微信里）

**前置**：

- 至少有 1 个 active 理发师（比如 `Tony`）
- 顾客甲有未来 24h 内的 active 预约

**步骤**：

1. 打开 admin → 左侧"预约" → 确认有 Tony 的 active 预约（顾客甲 + 另一家时间）
2. 左侧"理发师" → 选 Tony → "请假"
3. start_at：明天 14:00
4. end_at：明天 18:00
5. reason：`临时有事`
6. action：`取消受影响预约`（不是 `改期`）
7. 点"提交"

**期望**：

- [ ] 提示"已通知 X 位顾客"
- [ ] 顾客甲的微信收到客服消息："您的预约 [时间] 因理发师请假已取消..."
- [ ] 顾客甲的预约在 admin 里 status 变成 `cancelled`

**步骤 8**：

8. 切到 admin → "请假记录" → 找到刚才那条 → 点"取消请假"
9. 输入原因（选填）→ 确认

**期望**：

- [ ] 提示"已恢复 X 个预约"
- [ ] 顾客甲的预约在 admin 里 status 变回 `active`
- [ ] 顾客甲**不**应再收到新通知（取消是恢复，不是新事件）

**反直觉点**：

- 顾客甲收到**两条**通知（请假 + 取消）→ 通知去重 bug
- 取消后没恢复 → storage.CancelBarberLeave 漏逻辑
- 顾客甲收到通知里写错时间 → 时区 bug（v4.10 出现过的 footgun）

---

## 场景 3：Agent 对话质量（v4.10.1 改的）

**目的**：测 Agent 回复是否合并 / 不重复 / 答非所问

**角色**：顾客乙（微信里）+ owner（admin 旁观察）

**前置**：

- 顾客乙从没跟店聊过
- 选 3 个常见问题：
  - "你们几点关门？"
  - "明天 Tony 在吗？想约 3 点"
  - "剪发 + 染发多少钱？"

**步骤**：

1. 顾客乙发："你们几点关门？"
2. 等 5 秒

**期望**：

- [ ] 收到**一条**回复（不是拆成多条 / 不是重复发两次）
- [ ] 回答**包含营业时间**（不是泛泛"请看大众点评"）
- [ ] 末尾**没**重复问"还有什么可以帮您"之类的话（v4.10.1 加的 system prompt 应避免这种）

**步骤 3-6**：

3. 顾客乙发："明天 Tony 在吗？想约 3 点"
4. 等 10 秒

**期望**：

- [ ] 收到**一条**完整回复（不是 3 条拼接：Tony 状态 / 是否能约 / 确认信息）
- [ ] 回答**确认** Tony 明天是否在（不是套话）
- [ ] 涉及预约时**主动问**"约几点？"（不直接反问"是否预约"）

**步骤 7-9**：

7. 顾客乙连发 3 条：
   - "染发"
   - "想染深棕"
   - "Tony 老师有推荐吗"
8. 等 15 秒

**期望**：

- [ ] 顾客乙**看到 1 条**回复，**不是 3 条**（v4.10.1 persist 过滤了中间 chatter）
- [ ] 回复**结合** 3 条信息（不是只回答第 1 条）
- [ ] admin 端 chat history 也只**1 条** Agent 回复（v4.10.1 的过滤要在 storage 层体现）

**反直觉点**：

- 收到多条回复 → v4.10.1 persist 过滤没生效 / 漏加
- Agent 答非所问 → system prompt 没生效
- 中间 chatter 也存了 → storage.TrackEvent 漏过滤

---

## 场景 4：通知中心 + 失败补发（v4.10.1 新功能）

**目的**：测 admin 通知中心能不能看到失败 / 跳过的通知，一键补发

**角色**：owner（已登录）

**前置**：

- 至少制造 1 条**失败**的 notify（手动改 DB 模拟最快）：
  ```sql
  UPDATE customer_notifications
  SET status='failed', last_error='simulated network timeout'
  WHERE id=<某条 ID>;
  ```

**步骤**：

1. admin → 左侧"通知中心"（v4.10.1 新增的页面）
2. 看列表

**期望**：

- [ ] 看到刚才改成 failed 的那条
- [ ] 列表支持按 status 过滤（至少能看到 sent / failed / skipped / pending 四个 tab 或 dropdown）

**步骤 3-5**：

3. 选中那条 failed 的通知 → 点"补发"
4. 确认

**期望**：

- [ ] 弹 toast "已重新入队" / "补发成功"
- [ ] 几秒后状态从 `failed` → `retrying` → `sent`（或新失败记录，但**不会重复打扰已 sent** 顾客）
- [ ] 顾客甲的微信**只收到 1 条**通知（不是 2 条）—— 关键的去重

**反直觉点**：

- 补发后顾客收到 2 条 → 幂等去重 bug（v4.10.1 收过的最重要 bug）
- 补发按钮点了没反应 → 通知重试 RPC 路径有问题
- 通知中心页面 404 → 路由没注册

---

## 场景 5：跨店数据隔离（多租户核心）

**目的**：ownerA 操作时绝对看不到店 B 的任何数据

**角色**：owner（店 A，已登录）+ 在 admin-tool / DB 里预先建好的店 B 数据

**前置**：

- 店 A 和店 B 各有 1 个 owner
- 店 B 有 1 个 barber + 1 个 customer + 1 个 leave 记录
- 店 A 不知道店 B 的存在

**步骤 1-2**（成员列表 — **前端未实装，走 API**）：

1. 店 A 用 admin-tool / curl 调 `GET /api/admin/members`
2. 应该只看到店 A 的 admin

**期望**：

- [ ] 看不到店 B 的 owner
- [ ] URL 手改 `?shop_id=shop-B` 也无效（看 Network → 响应里没店 B 数据）

**步骤 3-6**：

3. 店 A 登录 → "理发师" → 列表
4. 试着手改 URL：`/api/admin/barbers/店B的barberID`（用浏览器 DevTools / curl 改请求）
5. 试着 DELETE 那个 barber

**期望**：

- [ ] DELETE 返 404（伪装不存在，不泄漏存在性）
- [ ] 即使知道店 B 的 barber ID，店 A 也**完全无法删除 / 改 / 看**

**步骤 7-8**：

7. 店 A 登录 → "请假" → 看列表
8. 验证店 A 看不到店 B 的 leave

**反直觉点**：

- 看到店 B 的 barber / customer / leave → **严重安全 bug**，必须立刻报告
- 店 A 删了店 B 的 barber → 跨店 DELETE 漏拦
- 返 500 而不是 404 / 403 → 错误处理漏了

---

## 场景 6：权限边界（v4.10.1 收紧）

**目的**：验证 v4.10.1 把 view:chain_dashboard / view:subscription / manage:subscription
从 owner 默认矩阵里拿掉后，UI 是否正确隐藏

**角色**：owner（已登录）+ platform_admin（如有）

**步骤**：

1. owner 登录 → 看左侧导航

**期望**：

- [ ] **看不到** "跨店看板"（v4.10.1：单店 owner 故意不展示）
- [ ] **看不到** "订阅" 菜单（v4.10.1：订阅归 platform_admin）
- [ ] 仍然看得到 "周报"（v4.10.1：单店周报 owner 该看自己店）
- [ ] **看不到** "成员管理" 菜单（**前端未实装**，见末尾"待补模块"）

**步骤 2**：

2. owner 手输 URL `/api/admin/chain-dashboard`

**期望**：

- [ ] 返 403（不是 200 / 不是 500）
- [ ] 错误信息不泄漏"这个 endpoint 是给 platform_admin 的"

**步骤 3**（如果环境有 platform_admin）：

3. platform_admin 登录 → 同样 URL

**期望**：

- [ ] 返 200，看到所有店数据

**反直觉点**：

- owner 看到 "跨店看板" 菜单 → 前端没跟 v4.10.1 权限收紧
- owner 调 chain-dashboard 返 200 → 后端权限检查漏了
- platform_admin 看不到 → 跨店 perm 配错了

---

## 场景 7：周报拆分（v4.10.1 结构调整）

**目的**：owner 看自己店周报 vs 跨店周报只 platform_admin 看

**角色**：owner（已登录）+ platform_admin（如有）

**步骤**：

1. owner 登录 → "周报" → 选"本周"

**期望**：

- [ ] 看到**自己店**的周报（预约数 / 营收 / 顾客数）
- [ ] **不**列出"全平台" / "其他店" 的数据

**步骤 2**：

2. owner 试着访问 URL：`/api/admin/weekly-report?shop_id=<店 B>`

**期望**：

- [ ] 返 403 / 数据为空（绝对不能看到店 B 数据）

**步骤 3**（如有 platform_admin）：

3. platform_admin 登录 → "跨店周报"

**期望**：

- [ ] 看到所有店汇总
- [ ] 支持按周 / 按月切换

**反直觉点**：

- owner 看到店 B 数据 → 跨店数据泄漏，**严重 bug**
- platform_admin 看不到跨店 → PermViewChainDashboard 没生效

---

## 场景 8：顾客预约 + 完成（核心流程）

**目的**：端到端走一遍"顾客预约 → admin 标记完成"

**角色**：顾客丙（微信里）+ staff 账号（用 staff 测，验证 staff 能调业务操作）

**前置**：

- staff 账号已登录
- 顾客丙从没预约过这家店

**步骤 1-3**（顾客丙）：

1. 微信里跟店 Agent 聊："想约明天下午 3 点剪发"
2. 按 Agent 提示提供姓名 / 手机号
3. 收到"预约成功"通知

**步骤 4-6**（staff）：

4. admin → "预约" → 列表
5. 找到顾客丙这条 → 点"标记完成"
6. 看预约状态

**期望**：

- [ ] 预约从 `active` → `completed`
- [ ] 顾客丙**不**应再收到提醒（已完成 = 不发）
- [ ] staff 能调（验证 staff 有 PermEditAppointments）

**反直觉点**：

- staff 不能调 → PermEditAppointments 配错了
- 顾客丙收到完成后的提醒 → Reminder 逻辑漏判 status
- admin 看不到这条预约 → list 漏过滤

---

## 反馈渠道

每发现 bug，**按以下模板**反馈（群里 / GitHub Issues 标 `internal-pilot`）：

```
**场景**：场景 X 名字
**角色**：owner / staff / 顾客
**步骤**：第 X 步
**期望**：xxx
**实际**：xxx
**截图**：粘贴（微信截图 / 浏览器截图）
**严重度**：P0 数据安全 / P1 功能 broken / P2 不便 / P3 优化
**复现**：必现 / 偶现（多少 % 概率）
**浏览器 / 微信版本**：Chrome 120 / 微信 8.0.42 ...
```

P0 立刻 @我，P1 当天处理，P2/P3 排到 W2-W3 末统一修。

## 时间表

- **W2（前 3 天）**：每个测试人挑 2-3 个场景试，记录反馈
- **W2（后 4 天）**：每天 15 分钟 review 反馈，按 P 排优先级
- **W3**：高 P 修完发 v4.10.2
- **W4**：低 P 收尾

## 我会观察的指标

- 每个场景的"通过"率（> 80% 才算这个场景稳定）
- P0 数量（应该 = 0，P0 出现就 hotfix）
- 跨店相关反馈（任何"店 B 数据可见"的反馈都按 P0 处理）
- Agent 对话相关的"重复 / 答非所问"频次（> 30% 触发 v4.11 修）

## 不要做的事

- **不要** 在测试环境做 SQL 真实删除（用 `status='disabled'` 模拟）
- **不要** 把测试账号密码发群里（用 1Password / Bitwarden 共享）
- **不要** 在客户店做这套测试（这是内部 pilot，**只能**在测试店 / 测试环境）

---

## 测试账号密码本

| 账号 | 密码 | 用途 | 备注 |
|---|---|---|---|
| `owner@shop-A` | 见 1Password | 主测 | owner 全权限 |
| `staff@shop-A` | 见 1Password | 测 staff 边界 | 业务操作 |
| `owner@shop-B` | 见 1Password | 测跨店 | 仅看不可改 |
| `platform_admin@` | 见 1Password | 测跨店 / 订阅 | 慎用，权限大 |

---

## 待补模块清单（v4.11 候选）

pilot 之前没发现，调研 admin.html 14 个 nav-item 之后整理：

| 模块 | 后端 | 前端 | 备注 |
|---|---|---|---|
| 成员管理（v4.7 RBAC）| ✅ 完整（5 个 handler + 跨店测试）| ❌ 未实装 | 最高优先级——v4.7 commit 就标记了，7 个版本没补；pilot 期间只能用 admin-tool/curl 测 |
| 修改密码（`/api/auth/change-password`）| ✅ | ⚠ 后端有，前端可能没接"改自己密码"页面 | 让 staff 改密码走 admin-tool 改的（v4.7 自助）|
| ~~跨店看板订阅~~| — | — | v4.10.1 收紧不归 owner，无需补 owner 端 |

**建议**：v4.11 第一波加一个 `nav-item data-view="members"` + 一个最小可用成员管理页面（建 / 改 role / 停用，复用现有 5 个 API）。前端代码量预估 ~150 行（参考 services / barbers 模块的写法）。

---

> 文档版本：v4.11 W1 测试基建对应 commit `f4b7896`，场景校正对应 commit `260301b`
> 下次更新：v4.10.2 发版时同步更新场景；v4.11 补"成员管理前端"后启用场景 1
