# PILOT v4.12 — 套餐体系 + 多店 + API + 导出

> W2 内部 pilot：v4.12 + v4.12.1 新功能 6 个场景，重点验：
> - plan 冻结机制（frozen / 宽限期）
> - feature gate 实战（多店 / API / 导出）
> - 安全 fix（改密码必须校验旧密码）

W1 已完成：单元测试 0 红、生产 owner 矩阵泄漏修复、admin-tool 覆盖率 85.1%。
本轮 pilot **拉 2-3 个非开发同学**真实跑，重点看 UX / 文案 / 边界。

| 场景 | 核心验 | 预期耗时 |
|---|---|---|
| S9  plan UI + 升级流程 | owner 看自己 plan + 4 档对比 + 升级 modal | 10 min |
| S10 plan 过期冻结 | basic 店主 7 天宽限 / 之后 402 | 15 min（需 DB 改时间） |
| S11 多店管理 | flagship 店主建分店 + 跨店隔离 | 15 min |
| S12 CSV 导出 | basic 403 / pro+ 200 + Excel 打开 | 10 min |
| S13 API Key | 生成 + external endpoint 调通 | 15 min |
| S14 改自己密码 | 前端 modal + 必须旧密码 | 5 min |

---

## 前提准备

### 测试账号

| 角色 | 账号 | 权限 | 用途 |
|---|---|---|---|
| owner（basic 店主） | `owner-basic@x` | basic 全部 | 验 403 / 402 |
| owner（pro 店主） | `owner-pro@x` | pro 全部 | 验 CSV 导出 |
| owner（flagship 店主） | `owner-flagship@x` | flagship 全部 | 验多店 + API |
| staff | `staff@flagship` | staff | 验 perm 边界 |

生产没有这 4 个账号——请 dev 同学用 admin-tool 建好（v4.10.1 已就绪）：

```bash
ssh root@server
cd /home/www/wwwroot/agent.yuyuanyuan.cn
./admin-tool members create --shop=shop-X --username=owner-basic --password=testpass --role=owner
./admin-tool perms set --shop=shop-X --username=owner-basic --plan=basic
# 类似创建 pro / flagship
```

### 数据库准备（S10 用）

```bash
# 模拟某店主"过期 10 天" → frozen 状态
ssh root@server
mysql chatwitheino
> UPDATE subscriptions SET expires_at = NOW() - INTERVAL 10 DAY
    WHERE shop_id IN (SELECT id FROM shops WHERE plan='basic') LIMIT 1;
```

### 浏览器

- Chrome（开 DevTools 看 network / console）
- 强刷（Ctrl+Shift+R）

---

## 场景 9：plan UI + 升级流程 ⭐ v4.12 新功能

**目的**：验 plan 体系 owner 端 UI 完整性 + 文案清晰度

**步骤**：
1. 三种 plan 店主分别登录 admin.html
2. 点左侧 nav "套餐与升级"
3. 看：
   - 顶部 banner：fresh 不显示 / 宽限期橙 / frozen 红
   - "当前套餐" 卡：plan 名 + 到期日 + 状态 pill
   - "4 档对比"：basic / pro / flagship / enterprise，标"当前"
   - "API 访问" 卡片：basic 灰 / flagship 显示 [管理 API Key]
4. 点"升级到此套餐"按钮 → 看升级 modal 文案

**预期**：
- 文案清楚：plan 名 + 价格 + 限额 + features
- 升级 modal 显示"联系商务"（v4.12 不接支付，v4.13 接入微信支付）
- basic 看 API 卡片显示"仅旗舰版可用"
- flagship 看 API 卡片显示 [管理 API Key]

**失败标准**：
- 文案错 / 错位 / 显示 "undefined"
- 升级 modal 报错 / 空
- API 卡片不显示

---

## 场景 10：plan 过期冻结 ⭐ v4.12 新功能（关键路径）

**目的**：验冻结 + 宽限期逻辑（**核心商业规则**）

**步骤**：
1. 选一个 **basic** 店主登录 admin.html
2. 看到"宽限期内"banner（橙）+ "当前套餐"卡显示"宽限期剩 N 天"
3. 所有 admin 操作 **仍可用**（发预约 / 看报表 / 改成员）
4. 切到浏览器 DevTools → console：`fetch('/api/admin/appointments', {credentials:'same-origin'}).then(r=>r.status)` → 应 200
5. **DB 强制改过期时间为 30 天前**：
   ```bash
   mysql chatwitheino -e "UPDATE subscriptions SET expires_at = NOW() - INTERVAL 30 DAY WHERE shop_id='<basic 店 ID>'"
   ```
6. **强刷**浏览器（Ctrl+Shift+R）
7. 顶部 banner 应变红"已冻结，请续费"
8. 试访问"会员管理" → 应提示 frozen（**不是**直接 403，而是 banner 引导）
9. 试任意 API（DevTools fetch）→ 应 402 + body 含 `"frozen":true`
10. **续费（恢复）**：模拟 platform_admin 续费：
    ```bash
    ssh root@server "./admin-tool subscription renew --shop=<basic 店 ID> --plan=basic"
    ```
11. 强刷 → banner 应消失 → 所有 admin 操作恢复

**预期**：
- 宽限期内 banner 橙，操作可用
- 冻结后 banner 红，所有 API 返 402
- 续费 1 次后 cache 清掉，**立即**恢复（不要等 5min）

**失败标准**：
- 冻结后还能改数据（漏拦）
- 续费后还 402（cache 没清）
- banner 文案错

---

## 场景 11：多店管理 ⭐ v4.12.1 新功能

**目的**：验 multi_store gate + 跨店隔离

**步骤**：
1. **basic 店主**登录 admin.html → 左侧 nav 看"分店管理"
2. 点开 → 看：
   - "当前 plan：basic；已有 1 / 1 家店"
   - [已达上限] 按钮（灰）
   - 顶部黄色 hint："已达当前 plan 的店铺限额"
3. DevTools fetch：`fetch('/api/admin/shops', {method:'POST', headers:{'Content-Type':'application/json'}, credentials:'same-origin', body:JSON.stringify({name:'分店甲'})}).then(r=>r.status)` → 应 403
4. **flagship 店主**登录 admin.html → 点"分店管理"
5. 看：当前 1 / 5 → 有 [+ 添加分店] 按钮
6. 点 [+] → 输入"分店甲（朝阳店）" + 地址 → 提交
7. 看列表新增一行 "分店甲（朝阳店）"，类型"分店"
8. 再建 4 个分店（flagship 限 5 含主店）→ 第 5 个 OK
9. 第 6 个 → DevTools fetch → 应 402 + body 含 `"resource":"shops"` + `"plan":"flagship"`
10. **跨店隔离**：把浏览器 cookie 清掉，用 **shopA** owner 登录
    - 调 `/api/admin/shops` 应只看到 shopA 自己 group
    - 调 `/api/admin/shops/<shopB 的 key ID>/revoke` 应 400 + "不存在或不属于此 shop"

**预期**：
- basic 看不到 [添加分店]（自动 hide by perm-gated），想绕过也 403
- flagship 流畅建 4 个分店，第 5 个 402
- 跨店 revoke 失败（storage WHERE shop_id=? 保证）

**失败标准**：
- basic 能建分店（gate 漏）
- flagship 第 5 个没拦（plan limit 漏）
- 跨店能看到对方分店（隔离漏）

---

## 场景 12：CSV 导出 ⭐ v4.12.1 新功能

**目的**：验 data_export feature gate

**步骤**：
1. **basic 店主**登录 → 左侧"预约管理"
2. 看右上 filterbar：[查询] [重置] [导出 CSV]（**应该没有**——perm-gated 隐藏）
3. DevTools 强绕过：`fetch('/api/admin/data/export?from=2026-06-01&to=2026-06-30', {credentials:'same-origin'}).then(r=>r.json())` → 应 `{error: "当前 plan 不支持数据导出，请升级到 Pro 或以上版本", feature_required: "data_export", current_plan: "basic"}` + status 403
4. **pro 店主**登录 → 预约管理 → 看 filterbar 有 [导出 CSV]
5. 点 [导出] → 浏览器下载 `appointments-YYYY-MM-DD-to-YYYY-MM-DD.csv`
6. 用 Excel 打开（**注意 UTF-8 BOM**，中文不乱码）
7. 看列：日期 / 时间 / 理发师 / 客户 / 服务 / 状态 / 来源
8. 默认区间：缺 from/to → 最近 30 天
9. 测边界：
   - `from=2026/06/01`（错格式）→ 400
   - `from=2026-06-30&to=2026-06-01`（晚于 to）→ 400
   - `type=customers`（不支持）→ 400

**预期**：
- basic 看不到按钮（auto-hide），绕过也 403
- pro 流畅下载，CSV 中文正常
- 错误信息清晰

**失败标准**：
- basic 看到按钮（perm hide 漏）
- CSV 中文乱码（BOM 漏）
- 边界没拦

---

## 场景 13：API Key ⭐ v4.12.1 新功能

**目的**：验 api_access feature + API key 全流程（生成 / 鉴权 / 吊销）

**步骤**：
1. **basic 店主**登录 → 套餐与升级 → API 卡片应显示"仅旗舰版可用"
2. **flagship 店主**登录 → 套餐与升级 → API 卡片 → 点 [管理 API Key]
3. modal 打开：列出当前 API key（应空）→ [+ 创建 API Key]
4. 点 [+] → 填"POS 系统" → 创建
5. ⚠ 弹窗："这是明文 token，只显示一次！请立即复制..."
6. **复制 token**（apikey_ 开头 64 字符）到记事本
7. 测试 token：
   ```bash
   # macOS / Linux
   curl -H "Authorization: Bearer <刚才复制的 token>" \
        "https://agent.yuyuanyuan.cn/api/external/appointments?from=2026-06-01&to=2026-06-30"
   ```
   - 应 200 + JSON `{shop_id, from, to, total, items: [...]}`
8. 故意**删最后 1 位** → 调 → 应 401 "invalid api key"
9. 故意**用错 scope**（v4.12.1 不开放，先不测，留 v4.13）
10. 回到 admin.html → [管理 API Key] → 列表显示这个 key，状态 active
11. 点 [吊销] → confirm → 列表状态变 revoked
12. **再用此 token 调 external** → 应 401

**预期**：
- 明文 token 只显示一次（prompt 弹窗）
- DB 不存明文（list 时不含 plaintext）
- 吊销立即生效

**失败标准**：
- 吊销后还能调（cache 漏清，或 SQL 漏 WHERE status）
- 列表里能看到完整 plaintext（泄漏）

---

## 场景 14：改自己密码 ⭐ v4.12.1 安全 fix

**目的**：验 v4.12.1 安全 fix（必须校验旧密码）

**步骤**：
1. 任意 owner 登录 admin.html
2. 右上头像 → ⚙ "修改密码" 按钮 → modal 打开
3. 测：
   - 缺旧密码 → 弹"请填写完整"
   - 旧密码错 → 弹"旧密码错误"
   - 新密码 < 6 位 → 弹"新密码至少 6 位"
   - 新旧密码相同 → 弹"新密码不能与旧密码相同"
4. 输入正确旧密码 + 合法新密码 → 提交 → "密码已修改，请用新密码重新登录"
5. 自动登出 → 用新密码重新登录 → 成功

**预期**：
- 所有错误前端后端一致拦截
- 改成功强制重登（token 可能泄漏场景下必备）

**失败标准**：
- 不输旧密码也能改（v4.12.1 fix 漏）

---

## 邀请文案（贴群里）

```
【内部测试】v4.12 + v4.12.1 pilot（套餐 / 多店 / API / 导出 / 密码）

新功能 5 大块 + 1 个安全 fix，需要 2-3 位非开发同学跑一遍，重点验 UX + 文案。

📋 文档：D:\golang\openbook\PILOT-v4.12.md（看 S9-S14 共 6 个场景）
⏱ 总耗时：~70 min
👤 需要：
- basic 店主账号（验 403 / 402）
- pro 店主账号（验 CSV 导出）
- flagship 店主账号（验多店 + API）

参与方式：
1. 看文档
2. 用 dev 同学给的测试账号跑
3. 记"通过 / 失败 / 截图 + 描述"
4. 反馈给 dev 同学

⚠ 注意：
- S10 涉及 DB 改时间，dev 同学先 backup
- S12 生成的明文 API token 一定保存好（吊销不能再查）
- 任何失败立刻反馈，别自己改代码
```

---

## 反馈收集表

| 场景 | 通过/失败 | 截图 | 描述 |
|---|---|---|---|
| S9 plan UI |  |  |  |
| S10 冻结 |  |  |  |
| S11 多店 |  |  |  |
| S12 CSV |  |  |  |
| S13 API |  |  |  |
| S14 密码 |  |  |  |

完成后把表发给 dev 同学，统一处理。

---

## 已知 v4.12.1 不覆盖

- 支付集成（v4.13）
- 续费 webhook（v4.13）
- JS 测试基建（v4.12.2+）
- 启用接口（disable → active）
- 分店 owner 自动创建（v4.12.1 手动用 members API）
- API key last_used_at 异步更新（v4.13）
- API key scope: customers:read / write（v4.13）