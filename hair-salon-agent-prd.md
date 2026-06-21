# 美发店智能预约助手 · 整体解决方案（PRD + 技术规格 v4.0）

> **作者**：Mavis（M3）| **日期**：2026-06-21 | **版本**：v4.0（P2 多店数据汇总 — `/api/admin/chain/dashboard` 跨店看板）
> **目标读者**：投资人 / 商务 BD / 后续接手的工程师（输入给 coding 工具用）
> **核心约束**：不含研发成本，仅含运营成本

> **v3.2 变更**：新增 §11.5 P3「退款/取消策略联动」—— 提前 2h 取消免爽约标记 / 累计晚退订 3 次自动黑名单 / 累计爽约 2 次自动黑名单。详见 §11.5。
> **v3.3 变更**：新增 §11.7 P4「理发师请假」—— 商户后台一键请假，自动取消 / 改派区间内未来预约，撤销规则限制在 startAt 之前。详见 §11.7。
> **v3.4 变更**：新增 §11.7.7 P4 工具侧集成 —— `create_appointment` 工具接入请假拦截，避免"预约成功→立即被请假处理取消"体验事故；新增 `IsBarberOnLeaveAt` / `ListBarberLeavesInRange` helpers + 18 个新单测。
> **v3.5 变更**：新增 §11.7.8 P4 cron 兜底 —— `LeaveExpirer` 每分钟扫描一次，把 `end_at < now` 的 active leave 自动转 `expired` 状态，避免脏数据 + 让 UI 准确区分"已过期"；新增 `EventBarberLeaveExpired` 事件类型 + 6 个 storage 单测 + 3 个 cron 单测。
> **v3.6 变更**：新增 §11.7.9 P4 query_schedule 视觉区分 —— `QueryScheduleBreakdown` storage helper 一次返回 available / leave blocks / booked count；query_schedule 渲染拆三段（可约 / 师傅请假 / 已约满），让 Agent 直接判断"换时间"还是"换师傅"；修复 toBarberLeaves 跨天请假判整天的 bug；+ 12 个新单测（storage 6 + tools 6）。
> **v3.6 增量**：新增 §11.7.10 P4 list_barbers 标记请假理发师 —— 选人阶段就把"今日请假"前置，让顾客不用先选师傅再被 reject；区分"正在请假（HH:MM-HH:MM）"和"即将请假（HH:MM 起）"两种文案；cancelled / expired leave 不显示；+ 8 个新单测。
> **v3.7 变更**：新增 §11.7.11 P4 改派策略升级 —— `findAlternateBarber` 从"按 name asc 取第一个空闲"改为三档分级：第一档 Skills 匹配（真会这门手艺）→ 第二档空 Skills 兜底（视作"全能"）→ 第三档任意 active 时段空闲（保底可用性）；+ 14 个新单测（`skillContains` 6 + `findAlternateBarber` 8）。
> **v3.8 变更**：新增 §11.8 P2 dashboard 事件漏斗 + 修 pre-existing SQL warning —— `eventFunnel` helper 把 18 个 event_type 按 today/week/month 三窗口聚合到 dashboard response；`idle_slot_push:DATE:CUST` 自动归一；修复 `customer_tags.go:132` 和 `idle_push.go:162` 引用不存在的 `shop_id` 列导致的 SQLite/MySQL warning；+ 14 个新单测（storage 5 + api 9）。
> **v3.9 变更**：新增 §11.9 MVP 第 5 项「转人工兜底」+ dashboard `HandoffPendingToday` 卡片 —— `HandoffToHumanTool` 在 Agent 解决不了顾客问题时写埋点 + 提示商户联系（伪 handoff，预留第三方客服对接）；Agent 指令新增 3 类允许场景 + 1 条严禁规则，避免没事就调；`DashboardResponse` 新增 `HandoffPendingToday` 字段（复用 `EventFunnelToday` 零额外 SQL）；+ 10 个新单测（tools 5 + api 5）。
> **v4.0 变更**：新增 §11.10 P2 多店数据汇总 —— `/api/admin/chain/dashboard` 跨店看板 endpoint，连锁品牌 owner 一次性看所有门店的 total / noshow rate / 各店明细 + Top 5 忙店 + 跨店事件漏斗；`storage.ListAllShops` + `storage.ShopAggregateByID` 跨店聚合 helper（口径与单店 `summarizeRange` 一致：date+time 解析后按时间戳精确过滤，22:00 算今天）；+ 16 个新单测（api 16）。

---

## 0. 一句话定位

**每天不到两块钱，雇一个 AI 预约助理。**

基于 Eino + DeepSeek + 企业微信，为中小美发店提供对话式智能预约。顾客加微信就能自助预约，理发师不用再接电话、用手记账本。

---

## 1. 项目概述

### 1.1 目标客户
- 中小美发店（1–5 名理发师），夫妻店优先
- 日均预约 5–30 单
- 当前痛点：靠电话/微信手工记、排班冲突、爽约率高

### 1.2 核心价值主张
1. **降本**：替代一名兼职前台，按 3500 元/月薪资算，单店年节省 ~4 万
2. **增收**：减少爽约（预估 -30%）+ 提升翻台率（按档期不冲突估算 +10%）
3. **省心**：7×24 自动应答，理发师专注手艺

### 1.3 三方角色
| 角色 | 谁 | 主要动作 |
|------|----|---------|
| C 端顾客 | 加了"XX 预约助手"微信的好友 | 自然语言发起/改约/取消预约 |
| B 端商户 | 美发店主/店长 | 在企业微信里看排班、收经营数据 |
| 平台 | 我们 | 跑 Agent 服务、维护订阅、对账 |

---

## 2. 痛点 ↔ 解决方案

| 痛点 | 现状 | 解决方案 |
|------|------|---------|
| 反复确认时间 | 顾客打电话、店主记纸条 | Agent 对话式预约，自动确认档期 |
| 手工排班冲突 | 两人同时段被约 | Agent 实时检测冲突 + 自动改期推荐 |
| 爽约/忘约 | 没有提醒 | 预约前 2h 微信自动提醒 |
| 服务被打断 | 理发中接电话 | 顾客全程跟 Agent 对话，不打扰师傅 |
| 没数据 | 凭感觉经营 | 经营数据看板（人次、热门时段、爽约率） |

---

## 3. 技术架构

### 3.1 整体架构图（数据流）

```
┌─────────────────┐
│ C 端顾客（微信） │
└────────┬────────┘
         │ ① 加好友 → 收发消息
         ▼
┌─────────────────────────────┐
│  企业微信（自建应用 + 上下游） │
│  - 接收消息回调              │
│  - 发送客服消息              │
└────────┬────────────────────┘
         │ ② 回调 HTTP（加密）
         ▼
┌─────────────────────────────────────┐
│  Eino Agent 核心服务（Go）          │
│  ┌──────────┐  ┌──────────────┐    │
│  │ 意图识别 │→│ 工作流编排    │    │
│  └──────────┘  └──────┬───────┘    │
│                      ↓             │
│  ┌──────────┐  ┌──────────────┐    │
│  │ 工具调用 │←│ LLM（DeepSeek│    │
│  └────┬─────┘  └──────────────┘    │
└───────┼────────────────────────────┘
        │ ③ 读写
        ▼
┌──────────────────────────────┐
│  数据层                       │
│  - MySQL：预约、理发师、顾客  │
│  - Redis：会话、分布式锁     │
└──────────────────────────────┘
```

### 3.2 核心组件选型

| 层级 | 组件 | 选型 | 说明 |
|------|------|------|------|
| 接入层 | 微信生态 | 企业微信（自建应用 + 上下游） | 官方合规、可发客服消息 |
| Agent 框架 | Eino | `github.com/cloudwego/eino` | 字节开源，Go 原生，适合工作流编排 |
| 大模型 | DeepSeek Chat API | `deepseek-chat` | 性价比高，中文能力强 |
| 关系数据库 | MySQL 8.0 | 阿里云 RDS | 预约/理发师/顾客/账单 |
| 缓存 | Redis 7 | 阿里云 Redis | 会话状态、分布式锁（防并发预约冲突） |
| 服务器 | ECS 4C8G | 阿里云 | 初期单实例，QPS < 50 |
| 部署 | Docker + systemd | - | 单机部署，不上 K8s |

### 3.3 关键工程决策

| 决策点 | 选择 | 理由 |
|--------|------|------|
| Agent 框架 | Eino（不用 LangChain） | 你已有 5 年 Go 经验；Eino 中文文档好；性能高 |
| 模型替换预案 | 抽象 LLM 接口，备选 Qwen/GLM | 防 DeepSeek 涨价/限流 |
| 并发预约 | Redis SETNX 分布式锁 | 防同一时段被两人同时约 |
| 时间处理 | 全部存 UTC + 商户时区字段 | 多店多城市友好 |
| 消息去重 | 用企业微信 MsgId 唯一索引 | 微信回调会重试 |

---

## 4. 商业模式与定价

### 4.1 版本定价

| 版本 | 首月体验价 | 续费月价 | 年付价 | 年付节省 | 目标客户 |
|------|-----------|----------|--------|----------|---------|
| 基础版 | 19.9 元 | 49 元/月 | 499 元/年 | 省 89 元 | 1–2 人小店 |
| 专业版 | 29.9 元 | 79 元/月 | 799 元/年 | 省 149 元 | 3–5 人店 |
| 旗舰版 | 39.9 元 | 129 元/月 | 1,299 元/年 | 省 249 元 | 多店连锁 |

> **价格锚点**：美团开店宝 ~580 元/年，微信小程序 SaaS ~1,200 元/年。我们定在 1/3 价位。

### 4.2 功能梯度

| 功能 | 基础版 | 专业版 | 旗舰版 |
|------|:------:|:------:|:------:|
| 对话式在线预约 | ✅ | ✅ | ✅ |
| 微信自动提醒 | ✅ | ✅ | ✅ |
| 单店排班 | ✅ | ✅ | ✅ |
| 多理发师排班 | — | ✅ | ✅ |
| 冲突检测 + 自动改期推荐 | — | ✅ | ✅ |
| 爽约管理 + 智能重排 | — | ✅ | ✅ |
| 经营数据看板 | — | ✅ | ✅ |
| 会员/熟客管理 | — | — | ✅ |
| 多店数据汇总 | — | — | ✅ |

---

## 5. 成本结构（运营视角，不含研发）

### 5.1 固定年成本

| 成本项 | 金额 | 备注 |
|--------|------|------|
| 企业微信认证费 | 300 元/年 | 必须，腾讯官方 |
| 云服务器（ECS 4C8G） | ~2,000 元/年 | ~170 元/月 |
| 域名 | ~50 元/年 | 可选，前期可省 |
| SSL 证书 | 0 元 | Let's Encrypt 免费 |
| **固定成本小计** | **~2,350 元/年** | |

### 5.2 变动成本（按用量）

| 成本项 | 计费方式 | 备注 |
|--------|---------|------|
| 企业微信外部联系人 | 前 2,000 人免费，超出 0.1 元/人/年 | 累计 |
| DeepSeek API 输入 | 1 元/百万 tokens | deepseek-chat |
| DeepSeek API 输出 | 2 元/百万 tokens | deepseek-chat |
| MySQL RDS | ~50 元/月起，随数据量微增 | 1GB 内基本免费额度 |

#### DeepSeek API 成本测算

- 单次预约对话平均消耗：输入 ~2,000 tokens + 输出 ~500 tokens
- **单次对话成本** = (2000/1M × 1) + (500/1M × 2) = **0.003 元/次**
- 单店月均 30 次预约 → 单店月 API 成本 ≈ **0.09 元**
- 100 店月成本 ≈ **9 元**，几乎可忽略

### 5.3 不同规模下的年度总成本

| 服务规模（活跃顾客数） | 固定成本 | 外部联系人费 | API + DB | **年度总成本** |
|----------------------|---------|-------------|----------|--------------|
| 500 人（~30 店） | 2,350 元 | 0 元 | ~100 元 | **~2,450 元** |
| 2,000 人（~100 店） | 2,350 元 | 0 元 | ~300 元 | **~2,650 元** |
| 5,000 人（~250 店） | 2,350 元 | 300 元 | ~800 元 | **~3,450 元** |
| 10,000 人（~500 店） | 2,350 元 | 800 元 | ~1,500 元 | **~4,650 元** |
| 50,000 人（~2,500 店） | 2,350 元 | 4,800 元 | ~7,000 元 | **~14,150 元** |

---

## 6. 利润分析（不含研发成本）

### 6.1 核心假设

| 指标 | 取值 | 依据 |
|------|------|------|
| 保守付费店均价 | 60 元/月 | 主推基础版 49 + 部分体验价拉低 |
| 中性付费店均价 | 75 元/月 | 基础+专业版混合 |
| 乐观付费店均价 | 90 元/月 | 含旗舰版渗透 |
| 月流失率 | 5% | 工具型 SaaS 行业基准 |
| 年付用户占比 | 10%（保守）/ 20%（中性）/ 30%（乐观） | 行业中等偏上 |
| 平均每店活跃顾客 | ~20 人 | 用于外部联系人费估算 |

### 6.2 三种规模下的年利润测算

#### 场景一：保守（50 家付费店，均价 60 元/月）

| 项目 | 金额（元） |
|------|-----------|
| **年营收** | 50 × 60 × 12 = **36,000** |
| 固定成本 | 2,350 |
| 变动成本（覆盖 ~1,000 顾客） | ~150 |
| **年度总成本** | **~2,500** |
| **年净利润** | **~33,500** |
| **净利润率** | **~93%** |

#### 场景二：中性（200 家付费店，均价 75 元/月）

| 项目 | 金额（元） |
|------|-----------|
| **年营收** | 200 × 75 × 12 = **180,000** |
| 固定成本 | 2,350 |
| 变动成本（覆盖 ~4,000 顾客） | ~1,000 |
| **年度总成本** | **~3,350** |
| **年净利润** | **~176,650** |
| **净利润率** | **~98%** |

#### 场景三：乐观（500 家付费店，均价 90 元/月）

| 项目 | 金额（元） |
|------|-----------|
| **年营收** | 500 × 90 × 12 = **540,000** |
| 固定成本 | 2,350 |
| 变动成本（覆盖 ~10,000 顾客） | ~3,250 |
| **年度总成本** | **~5,600** |
| **年净利润** | **~534,400** |
| **净利润率** | **~99%** |

### 6.3 回本周期

| 里程碑 | 预估时间 |
|--------|---------|
| 投入固定成本（认证 + 服务器 + 域名） | 第 0 月 |
| 达到 10 家付费店 | 第 2–3 月 |
| **累计回本（覆盖初期投入）** | **第 4–5 月** |
| 突破 50 家店 | 第 6–8 月 |
| 突破 200 家店 | 第 12–18 月 |

---

## 7. 关键运营指标（北极星 + 过程指标）

| 指标 | 目标值 | 类型 |
|------|--------|------|
| 首月体验 → 续费转化率 | ≥ 60% | 北极星 |
| 年付用户占比 | 20%+ | 收入质量 |
| 单店月均预约对话数 | ≥ 20 次 | 使用深度 |
| 月流失率 | < 5% | 健康度 |
| 获客成本（CAC） | 地推 ~50–100 元/店 | 效率 |
| 客户终身价值（LTV） | 500–1,000 元 | 商业模型 |
| LTV / CAC | ≥ 5 | 健康度 |

---

## 8. 冷启动与增长策略

### 8.1 获客渠道（低成本优先）

| 渠道 | 操作 | 预期 CAC |
|------|------|---------|
| **地面推广** | 美发店密集商圈，演示 10 分钟自动排班 | 50–100 元 |
| **老店转介绍** | 双方各得 1 个月 | < 30 元 |
| **抖音本地生活** | "AI 自动排班"对比短视频 | < 80 元 |
| **美发培训学校** | 免费给学生用，培养未来商户 | 0 元 |
| **美团/点评置换** | 互相导流 | 0 元 |

### 8.2 续费动作链（按天触发）

1. **D+3**：推送"恭喜完成第一次自动预约！"
2. **D+15**：生成使用报告（对比手写时期的变化）
3. **D+25**：提醒首月即将到期，推送年付优惠
4. **到期前 7 天**：筛选高频使用者，运营 1v1 维护
5. **到期当天**：默认续月，发送发票/账单

---

## 9. 风险与应对

| 风险 | 概率 | 影响 | 应对措施 |
|------|:----:|:----:|---------|
| 企业微信政策调整 | 中 | 高 | 保持对接人脉，备选方案 = 小程序 |
| 理发店续费意愿低 | 中 | 高 | 强化预约提醒、经营看板的不可替代性 |
| 竞品低价竞争 | 高 | 中 | 差异化在"对话式 AI"，不只是列表式预约 |
| DeepSeek API 涨价/限流 | 中 | 中 | 预留模型替换方案（Qwen/GLM/豆包） |
| 获客成本超预期 | 中 | 中 | 聚焦转介绍 + 地推，不烧信息流 |
| 微信回调漏单 | 低 | 高 | MsgId 唯一索引 + 主动对账 |

---

## 10. 项目里程碑

| 阶段 | 时间 | 核心目标 |
|------|------|---------|
| **MVP 验证** | W1–W3 | 跑通"顾客微信 → Agent → 写入预约"主链路 |
| **内测** | W4–W6 | 3–5 家免费种子用户，收集真实对话 |
| **正式上线** | W7–W8 | 启动首月 19.9 元体验价 |
| **扩张期** | M3–M6 | 区域深耕，迭代经营看板 |
| **规模化** | M6–M12 | 模式复制，开第二城 |

---

## 11. 可执行工程清单（喂给 coding 工具）

> **当前状态**：基本功能跑通 ✅。下面是要按优先级补齐的清单。

### 11.1 P0 - 必修（影响上线）

- [ ] **企业微信回调签名验证 + 消息解密**（安全基线）
- [ ] **MsgId 幂等去重**（防微信重试导致重复预约）
- [ ] **Redis 分布式锁**（防同一时段并发预约冲突）
- [ ] **预约前 2h 提醒定时任务**（cron + 模板消息）
- [ ] **多理发师排班冲突检测算法**
- [ ] **退款/取消流程**

### 11.2 P1 - 重要（影响留存）

- [ ] **爽约标记 + 自动重排推荐**
- [ ] **经营看板 MVP**（人次、热门时段、爽约率）
- [ ] **年付订阅接入**（微信支付 / 手动对账二选一）
- [ ] **首月体验 → 续费转化漏斗埋点**
- [ ] **商户后台（Web）极简版**：看排班、改设置

### 11.3 P2 - 加分（差异化）

- [ ] **会员/熟客标签**（VIP 自动识别）
- [ ] **抖音/小红书图文报告自动生成**（裂变素材）
- [ ] **多店数据汇总**（连锁版本）
- [ ] **理发师空闲时段主动推送**（GMV 提升）

### 11.4 数据模型（核心实体）

```
Customer       — 顾客（WeChat OpenID、手机号、标签、累计消费）
Stylist        — 理发师（所属店、技能标签、工作时段）
Shop           — 店铺（名称、地址、营业时间、版本、到期日）
Appointment    — 预约（顾客ID、理发师ID、时段、服务、状态）
Subscription   — 订阅（店铺ID、版本、起止时间、自动续费标志）
Conversation   — 会话（顾客ID、最近N轮上下文、Redis TTL 30min）
ReminderLog    — 提醒日志（预约ID、发送时间、状态）
```

### 11.5 API 端点草图

| 方法 | 路径 | 用途 |
|------|------|------|
| POST | `/webhook/wecom/callback` | 接收企业微信消息回调 |
| POST | `/api/appointment/create` | 创建预约（Agent 调用） |
| GET  | `/api/appointment/available` | 查询空闲时段 |
| POST | `/api/appointment/cancel` | 取消预约 |
| GET  | `/api/shop/{id}/dashboard` | 经营看板数据 |
| POST | `/api/internal/cron/reminder` | 定时提醒任务回调 |

### 11.6 P3 — 退款/取消策略联动（v3.2 新增）

> **背景**：PRD §11.4 列了 Cancellation/Refund 但没硬性规则。P3 自定义策略：
> **提前 2h 取消免爽约标记；不足 2h 算"晚退订"；商户/系统取消豁免；累计触发阈值自动黑名单**。

#### 11.6.1 取消时机 → CancelType 映射

| 取消来源 | 时机 | CancelType | 是否 penalty | 是否计入 NoShowCount / LateCancelCount |
|---------|------|-----------|:----------:|------|
| Agent（顾客主动） | now ≥ appt_time | `after_due` | ❌ 拒绝取消 | — （应改走 mark_no_show） |
| Agent | appt_time - now < 2h | `late_cancel` | ✅ +1 late_cancel_count | late_cancel_count +1 |
| Agent | appt_time - now ≥ 2h | `early_cancel` | ❌ | — |
| Admin（商户后台） | 任意 | `admin_cancel` | ❌ | — |
| System（cron noshow scanner 清理） | 任意 | `system_cancel` | ❌ | — |

#### 11.6.2 黑名单自动触发

| 计数维度 | 阈值 | 触发动作 |
|---------|------|---------|
| `Customer.LateCancelCount` | ≥ 3 | 自动加 `BLACKLIST` 标签 + 写 `customer_blacklisted` 事件埋点 |
| `Customer.NoShowCount` | ≥ 2 | 自动加 `BLACKLIST` 标签 + 写 `customer_blacklisted` 事件埋点 |

> **为何晚退订阈值比爽约高？** 晚退订是"事出有因"（临时有事、孩子生病），宽容度更高；爽约是"完全没来"，性质更恶劣。

#### 11.6.3 数据模型变更

新增字段（AutoMigrate 自动加列）：

```go
// Appointment
type Appointment struct {
    ...
    CancelType   string     `gorm:"size:16;index"`           // early/late/after_due/admin/system
    CancelledAt  *time.Time `gorm:"index"`
    CancelReason string     `gorm:"size:256"`
}

// Customer
type Customer struct {
    ...
    LateCancelCount int `gorm:"default:0"`  // 新增：累计晚退订次数
}
```

#### 11.6.4 关键代码

- `storage/cancel_policy.go` — `CancelAppointmentWithPolicy(ctx, apptID, source, reason)` 策略核心
- `tools/cancel_appointment.go` — Agent 工具，把"晚退订警告"拼到回复里
- `api/api.go adminCancelHandler` — 商户后台，强制走 `source=admin` 豁免 penalty
- `storage/cancel_policy_test.go` — 9 个单测覆盖三种时机 + 黑名单触发 + 向后兼容

#### 11.6.5 配置项（后续可加）

当前策略硬编码在 `DefaultCancelPolicy`。后续可从 `.env` / Shop 表覆盖：

```bash
# .env.example (待补)
CANCEL_FREE_WINDOW=2h
LATE_CANCEL_BLACKLIST_THRESHOLD=3
NOSHOW_BLACKLIST_THRESHOLD=2
```

### 11.7 P4 — 理发师请假（v3.3 新增）

> **背景**：理发师临时有事（生病/家中有事/紧急出差），商户在后台点"请假"，系统自动处理该理发师在 [StartAt, EndAt] 区间内的所有未来预约。这是商户日常高频率操作（P3 的补集：P3 处理顾客主动取消，P4 处理商户主动让理发师下架）。

#### 11.7.1 业务场景

- **理发师请病假/家中有事/紧急出差** → 商户在后台点"请假" → 系统根据 Action 处理：

| Action | 行为 | 适用场景 |
|--------|------|---------|
| `cancel` | 全部未来预约直接取消 + 微信通知顾客"因 X 师傅请假被取消，请重新预约" | 理发师病假几天（顾客需另约时间）|
| `reschedule` | 自动找同档期其他 active 理发师改派；改派失败的兜底取消 + 通知 | 理发师临时 1-2 小时外出（短假改派）|

#### 11.7.2 数据模型

新表 `barber_leaves`（AutoMigrate 自动建）：

```go
type BarberLeave struct {
    ID         string    // 主键
    ShopID     string    // 多店隔离
    BarberID   string    // 哪理发师
    BarberName string    // 冗余，便于审计
    StartAt    time.Time // 请假起点（建议：开始时间）
    EndAt      time.Time // 请假止点（建议：自然恢复时间）
    Reason     string    // 病假/家中有事/紧急出差
    Action     string    // cancel / reschedule
    Status     string    // active / cancelled / expired
    AffectedCount int    // 受影响预约总数
    RescheduledCount int // 改派成功数
    CancelledCount int   // 取消数
    CreatedBy      string // 商户后台用户名
    CreatedAt  time.Time
    UpdatedAt  time.Time
}
```

**状态机**：
- `active` — 生效中（now < end_at）
- `cancelled` — 商户主动撤销（**仅当 now < start_at 时允许**，因为已开始 / 已改派的预约改不回去了）
- `expired` — 自然结束（查询时过滤即可，不必显式状态）

#### 11.7.3 关键代码

- `storage/barber_leave.go` — `CreateBarberLeave` / `CancelBarberLeave` / `ListBarberLeaves` / `ListActiveLeaves` / `FindAppointmentsInRange`
- `storage/models.go BarberLeave` — GORM 模型
- `api/api.go` — `createBarberLeaveHandler` / `cancelBarberLeaveHandler` / `listBarberLeavesHandler`（路由 `/api/admin/barber/:id/leave*`）
- `main.go` — `buildLeaveNotificationSender`（wecom 通知适配器）
- `static/admin.html` — 商户后台"请假管理"section（理发师列表 + 历史 + 请假弹窗）

#### 11.7.4 接口

| 方法 | 路径 | 用途 |
|------|------|------|
| POST | `/api/admin/barber/:id/leave` | 创建请假（Body: `start_at` / `end_at` / `reason` / `action`）|
| DELETE | `/api/admin/barber/:id/leave/:leaveID` | 撤销请假（仅当还没到 start_at）|
| GET | `/api/admin/barber/:id/leaves?limit=N` | 历史（默认 limit=50）|

#### 11.7.5 Penalty 联动

所有取消走 `CancelAppointmentWithPolicy(source="admin")` 路径 → **不计顾客 late_cancel / no_show**。这与 P3 的"商户手工取消"逻辑一致：商户/系统原因不能让顾客承担爽约 penalty。

改派不算取消：`appointment.barber_id` 更新 + 写 `EventAppointmentRescheduled` 埋点。

#### 11.7.6 单测覆盖

`storage/barber_leave_test.go` 17 + 12 = 29 个用例：

| 类别 | 用例 |
|------|------|
| CreateLeave cancel | 区间内全部取消 / 区间外不受影响 / 参数校验 / 理发师不存在 |
| CreateLeave reschedule | 有空闲理发师改派成功 / 全部占用兜底取消 / 本店无其他理发师兜底取消 |
| CancelLeave | start_at 前撤销成功 / start_at 后拒绝（ErrLeaveNotCancellable）/ 重复撤销失败 / 不存在 ID 失败 |
| List | Active 过滤 expired + cancelled / 按 start_at DESC |
| Sender 容错 | sender 全失败 → leave 仍创建成功 |
| ID 生成 | UUID 自动生成 + barber_name 冗余正确 |
| FindAppointmentsInRange | 区间精筛（区间外的不返回）|
| IsBarberOnLeaveAt | 区间内 / 边界（start/end 含端点）/ 区间前 / 区间后 / cancelled 不计 / 其他理发师不计 / 无请假 |
| ListBarberLeavesInRange | 仅返回相交区间 / 空区间 / 过滤 cancelled / 其他理发师不计 |

#### 11.7.7 工具侧集成（v3.4 增量，2026-06-21）

P4 在 storage / api 层完成后，顾客侧的 `create_appointment` 工具必须能感知请假，否则会出现"预约成功→被请假处理自动取消"的体验事故。

集成点：`tools/create_appointment.go`，在"店铺节假日校验"之后、"Redis 锁"之前加一道请假拦截。

调用方式：
```go
appointmentAt, _ := time.ParseInLocation("2006-01-02 15:04",
    params.Date+" "+params.Time, loc) // Asia/Shanghai
onLeave, leave, err := storage.IsBarberOnLeaveAt(ctx, barber.ID, appointmentAt)
if onLeave && leave != nil {
    return "", fmt.Errorf(
        "理发师 %s 在 %s 至 %s 期间请假（原因：%s），该时段无法预约。",
        params.BarberName, ..., leave.Reason)
}
```

错误文案设计：让 Agent 拿到错误后能直接告诉顾客"X师傅 Y 时段请假（原因：Z），请选其他师傅或换时间"，无需再调一次工具查具体请假区间。

工具描述同步更新：`create_appointment.Info.Desc` 加一句"如果理发师在所选时段请假（P4），会返回错误，需要换理发师或换时间"。

新增 storage helper：
- `IsBarberOnLeaveAt(ctx, barberID, at time.Time) (bool, *BarberLeave, error)` — 单点查询
- `ListBarberLeavesInRange(ctx, barberID, from, to)` — 区间列表（预留 query_schedule / list_barbers 后续接入）

测试覆盖（`tools/create_appointment_test.go` 6 用例）：
- Info.Desc 含"请假"关键字
- 无请假 → 预约成功
- 区间覆盖 → 拒绝（错误信息含理发师名 + 请假区间 + 原因；DB 无新增 active 预约）
- 区间前结束 → 允许
- cancelled leave → 允许
- 其他理发师请假 → 允许（不误伤）

放置位置说明：在 Redis 锁**之前**做这个检查是有意为之——
- 避免 lock TTL（10s）白白占用
- 失败时 Agent 立即收到清晰错误，不需要等到锁释放再 reject

#### 11.7.8 cron 兜底：LeaveExpirer（v3.5 增量，2026-06-21）

P4 的请假 row 创建后状态为 `active`，设计预期是 end_at 过了之后**自然**转为 `expired` —— 但这个状态迁移没有自动机制，靠人工去改既不可靠也无法扩展。所以引入一个 1 分钟粒度的 cron 兜底。

**为什么需要这个 cron？**
- 数据卫生：UI（query_schedule / list_barbers）要准确区分"已结束" vs "已撤销" vs "仍生效中"；如果 active 永远不收敛，列表会一直显示"今天 14:00-16:00 Tony 师傅请假"——但他其实已经回来上班了
- 商户视角：商户看历史请假记录时，"expired" 状态比 "active" 更能反映实际情况
- 状态机完整性：active → {cancelled, expired}，两个出口都应该被代码覆盖

**为什么不"创建时预判状态"？**
- 状态是相对当前时间的，活的状态机必须有执行者
- 数据库存的是绝对时间，cron 才是状态机的执行者（noshow / lifecycle / reminder 也是同款思路）

**实现要点：**

```go
// storage/barber_leave.go
func ExpireOverdueLeaves(ctx context.Context, now time.Time) (int, error) {
    // 1) 找出所有"将过期"的 leave
    var toExpire []BarberLeave
    DB.Where("status = ? AND end_at < ?", LeaveStatusActive, now).Find(&toExpire)
    // 2) 一次 UPDATE 全部标 expired（带 status=active 守卫防 cancel 抢）
    res := DB.Model(&BarberLeave{}).
        Where("status = ? AND end_at < ?", LeaveStatusActive, now).
        Updates(map[string]interface{}{"status": LeaveStatusExpired, "updated_at": now})
    // 3) 逐条写 barber_leave_expired 事件（best-effort）
    for i := range toExpire {
        TrackEvent(ctx, toExpire[i].ShopID, EventBarberLeaveExpired, toExpire[i].ID, ...)
    }
    return int(res.RowsAffected), nil
}
```

```go
// cron/leave.go
type LeaveExpirer struct{ scheduler *cron.Cron }
func (l *LeaveExpirer) Start(ctx context.Context) error {
    l.scheduler.AddFunc("0 * * * * *", l.scan)  // 每分钟
    l.scheduler.Start()
}
func (l *LeaveExpirer) scan() {
    expired, err := storage.ExpireOverdueLeaves(ctx, time.Now())
    if err != nil { log.Printf(...) ; return }
    if expired > 0 { log.Printf("[leave-expirer] 已过期 %d 条请假", expired) }
}
```

**关键设计决策：**

| 决策 | 选择 | 理由 |
|---|---|---|
| 频率 | 每分钟 1 次 | 比 noshow（5 分钟）更敏感：顾客/商户想立刻看到状态变化，但 SQL 很轻（status+end_at 双索引） |
| 边界 | `end_at < now` | end_at == now 仍算 active，下一分钟过期；语义清晰（end_at 当天最后一秒还在请假内） |
| 守卫 | `WHERE status='active' AND end_at < now` | 防与商户主动 `CancelBarberLeave` 抢；如果同时发生，update 影响 0 行，写埋点 0 次 |
| 通知 | 不发微信 | 顾客通知在 CreateBarberLeave 时一次发完；expire 是后台状态迁移，对顾客无感知 |
| wecom 依赖 | 不需要 | LeaveExpirer 不发任何微信，与 reminder/noshow/lifecycle 解耦；方便单独跑测试 / debug |
| 失败处理 | log 不 return error | 单次失败不应让 cron 退出；下分钟再试 |
| 事件 | `barber_leave_expired` 埋点 | 后续 dashboard 可统计"理发师请假频次 / 平均时长" |

**测试覆盖（`storage/barber_leave_test.go` 6 用例 + `cron/leave_test.go` 3 用例）：**
- ExpiresPastEndAt：2 已过期 + 1 未来 + 1 cancelled → 仅 2 转 expired
- NoOpWhenNothingPast：全部未来 → 0 expired
- Idempotent：连续跑两次 → 第二次 0
- Boundary：end_at == now 仍 active；now + 1ms → 转 expired
- WritesExpiredEvent：event type / shop_id / meta 正确
- DBNotInitialized：DB=nil → no-op（不 panic）
- cron/StartStop：能启动并立即停止
- cron/StopOnNilScheduler：nil 调度器 Stop 安全
- cron/ScanDBNotInitialized：DB=nil 时 scan 不 panic

**新增事件类型：**
- `EventBarberLeaveExpired = "barber_leave_expired"` —— cron 自然过期

**main.go 集成点：** 在 `if wecomClient != nil` 块的 idlePusher 之后，单独启动 LeaveExpirer（虽然 LeaveExpirer 不需要 wecom，但放在同一处方便看到所有 cron 集合）

#### 11.7.9 query_schedule 视觉区分请假占用（v3.6 增量，2026-06-21）

P4 前几个迭代（v3.4 工具拦截 / v3.5 cron 兜底）解决了"顾客成功预约到请假的 slot 后被自动取消"的体验事故，但 query_schedule 工具渲染时只把请假占用的 slot 静默掉 + 末尾加一句"已有请假"。对 Agent 来说这有个问题：

- Agent 看到 "14:00 没了"，无法判断"因为有人约了"还是"师傅请假"
- 两种情况的可执行建议不同：约满了只能换时间；请假了可以换师傅（推荐）+ 换时间

所以本轮把"可约 / 师傅请假 / 已被预约"三类在渲染时分开：

**新的渲染格式：**

```
理发师 Tony 在 2026-06-22 的可预约时段：
  09:00, 09:30, 10:00, 10:30, 11:00, 11:30, 13:30, 16:30, 17:00, 17:30, 18:00
师傅请假占用：14:00-16:00（体检）
（这些时段是师傅临时请假，建议换时间或换其他理发师）
其余 2 个时段均已被预约。
```

三段语义：
- **可约** — 顶部"可预约时段"段，逗号分隔
- **师傅请假占用** — 单独一段，列请假区间和原因 + 建议"换时间或换其他理发师"
- **已预约** — 末尾一行"其余 N 个时段均已被预约"，不展开明细（避免长尾刷屏）

**storage 新 helper `QueryScheduleBreakdown`：**

```go
type ScheduleBreakdown struct {
    Available   []string     // 可约 slot
    LeaveBlocks []LeaveBlock // 区间 + 原因，按 start_at ASC
    BookedCount int          // 已预约数（不展开）
}

type LeaveBlock struct {
    StartHM string  // "14:00"
    EndHM   string  // "16:00"
    Reason  string
}

func QueryScheduleBreakdown(barberName, date string) ScheduleBreakdown
```

设计要点：
- 一次性返回三个维度，调用方不用再拼 SQL
- 已预约的 slot 数 (BookedCount) 不展开明细——大多数情况下明细列表会让回复过长，对 Agent 决策无价值
- **整天请假也走同一三段路径**（v3.6 设计决策）：
  - LeaveBlocks 包含 "00:00-00:00"（整天请假的典型存储）
  - Available 为空 → "当天没有可预约的时段"
  - LeaveBlocks 段照常渲染 + 建议
  - **不再走"整天请假专门文案"路径**——视觉一致，Agent 不用为"整天请假"和"部分请假"学两套文案
- LeaveBlocks 排序依赖 `ListBarberLeavesInRange` 已按 `start_at ASC`

**决策变更记录**：v3.6 早期版本曾为整天请假设计专门文案（"全天请假 + 原因 + 建议换人/换天"），后续与并发 agent 写的 `TestQuerySchedule_FullDayLeave_FallbackShowsLeaveNote` 冲突，**统一改成三段路径**——简洁性 > 文案差异化。已删除 `isFullDayLeave` / `toBarberLeaves` 函数。

**为什么用逗号分隔而不展开明细？**
- list_barbers（v3.6 §11.7.10 标记请假理发师）已经把"今天能不能约"前置在选人阶段
- query_schedule 主要用于"我已选定 Tony，看哪天有空"——此时重点是"哪些时段是请假/已约满"，不是"具体被谁约了"
- 视觉三段（可约 / 请假 / 已约满）让 Agent 5 秒内能给出可执行建议

**测试覆盖（`storage/repo_test.go` 6 用例 + `tools/query_schedule_test.go` 6 用例）：**
- storage: TestQueryScheduleBreakdown_Empty / PartialLeave_Booked / FullDayLeave_BlocksAll / CancelledLeave_NotCounted / UnknownBarber_ReturnsZeros / MultipleLeaves_PreservesOrder
- tools: TestQueryScheduleTool_InfoMentionsLeave / PartialLeave_SlotsFilteredAndLeaveNoteShown / FullDayLeave_FallbackShowsLeaveNote / CancelledLeave_NotCounted / OtherBarberLeave_NotAffected / HolidayOverridesLeave

**为什么不直接在 `query_schedule.go` 里拼 SQL？**
- storage helper 让 admin UI / future dashboard 也能直接复用（不需要为前端再开一个 API）
- 单测更聚焦（storage 测试 SQL 正确性，tools 测试文案正确性）

#### 11.7.10 list_barbers 标记请假理发师（v3.6 增量，2026-06-21）

§11.7.9 把 query_schedule 渲染分了三段（可约 / 师傅请假 / 已约满）——但这一步要顾客先选好理发师才能查到请假。如果顾客一开始就不知道"我应该约谁"，Agent 会默认推荐 list_barbers 列出来的师傅；选错了才发现"哦他今天请假"，对话来回多一轮，体验损失。

本轮把"今日是否请假"前置到 list_barbers：

**新文案格式：**
```
本店可预约的理发师：
  1. Tony（擅长：剪发，今日 14:00-16:00 请假（原因：体检））
  2. Kevin（擅长：剪发，今日 14:30 起请假（原因：私事））
  3. Mike（擅长：剪发）
```

**两种文案区分：**
- **当前正在请假**（`now ∈ [start_at, end_at]`）：`今日 HH:MM-HH:MM 请假（原因：xxx）`
- **即将请假**（`now < start_at`）：`今日 HH:MM 起请假（原因：xxx）`

文案区别很重要：
- "14:00-16:00" 告诉顾客**这个时间段都不能约**——给他换师傅 / 改时间的清晰边界
- "14:30 起" 告诉顾客**前半段还能约**——他可以赶在请假前来

**实现细节：**
- 复用 §11.7.7 的 `ListBarberLeavesInRange(barberID, dayStart, dayEnd)` 拿今天相交的 active leave
- cancelled / expired leave 已被底层 SQL 过滤掉（`status='active'` 守卫）
- 取第一条 leave（ListBarberLeavesInRange 已按 start_at ASC，多条取最早那条——大多数场景只有一条）
- 不在 list_barbers 文案里区分 leave.action（cancel / reschedule）——内部行为对顾客无意义

**为什么用 `今日` 前缀？**
- list_barbers 默认是"今天可约"的语境，不显式说日期顾客也能懂
- 后续如果支持"未来 N 天"维度（PRD §11.7 远期），可以扩展成"6/22 请假"等

**关键设计决策：**

| 决策 | 选择 | 理由 |
|---|---|---|
| 显示窗口 | 仅今日 | list_barbers 是"现在能约谁"的语义，看太远反而干扰 |
| 多条 leave | 取最早一条 | 大多数场景只有一条；多条时"接下来还有"会让文案变长，对顾客决策无增量价值 |
| leave.action | 不显示 | cancel / reschedule 是内部处理逻辑，顾客只需要"能不能约" |
| cancelled / expired | 不显示 | UI 一致性原则：只显示对顾客当下决策有用的信息 |

**测试覆盖（`tools/list_barbers_test.go` 8 用例）：**
- TestListBarbersTool_InfoMentionsLeave：Info 描述里提到请假
- TestListBarbers_NoLeave_NormalList：无请假 → 正常列表
- TestListBarbers_OngoingLeave_ShowsFullRange：正在请假 → HH:MM-HH:MM + 原因
- TestListBarbers_UpcomingLeave_ShowsStartOnly：即将请假 → HH:MM 起 + 原因
- TestListBarbers_CancelledLeave_NoTag：cancelled leave 不显示
- TestListBarbers_ExpiredLeave_NoTag：expired leave 不显示
- TestListBarbers_OtherBarberLeave_OnlyAffectsThatBarber：多理发师时只标记请假的
- TestListBarbers_NoBarbers_FallbackMessage：空店兜底文案

---

#### 11.7.11 改派策略升级：按 Barber.Skills 三档分级（v3.7 增量，2026-06-21）

P4 早期版本的 `findAlternateBarber`（v3.3）采用 MVP 简化策略："取本店铺所有 active 理发师 → 排除原 barber → 检查时段空闲 → 按 name ASC 选第一个可用"。问题在于：完全不看理发师会不会这门手艺，顾客预约"染发"被改派给只会"剪发"的 Bob，体验事故。

v3.7 升级为**三档分级匹配**：

```
┌──────────────────────────────────────────────────────────────────┐
│ Tier 1 — Skills 匹配（真会这门手艺）                              │
│   候选.Skills 包含 appt.Service，且时段空闲 → 返回                │
├──────────────────────────────────────────────────────────────────┤
│ Tier 2 — 空 Skills 兜底（视作"全能"）                            │
│   候选.Skills == ""（未填写），且时段空闲 → 返回                   │
│   区分"未标记技能"和"标记了但不匹配"——后者不能假装会              │
├──────────────────────────────────────────────────────────────────┤
│ Tier 3 — 任意 active（保底可用性）                                │
│   忽略 Skills 匹配，取任何 active 且时段空闲                      │
│   防止"全员匹配不上就一个都改派不出去"导致兜底取消                 │
└──────────────────────────────────────────────────────────────────┘
```

**真实场景示例：**
- 顾客预约"Tony 染发 15:00"，Tony 请假
- 店铺有：Kevin（剪发+染发）/ Bob（剪发）/ Mike（未填 Skills）
- Tier 1 命中 → 选 **Kevin**（真会染发）

如果 Kevin 也请假或时段被占：
- Tier 1 跳过 → Tier 2 命中 Mike（未填技能 = 全能）

如果 Mike 也忙：
- Tier 2 跳过 → Tier 3 命中 Bob（剪发不会染发，但总比取消好——后台通知可手动改）

**Skills 匹配规则：**
- `skillContains(skills, needle)` 精确匹配逗号分隔的单项
- `Skills="剪发,染发"` 包含 `"染发"` 和 `"剪发"`，**不含** `"染"`（子串不匹配）
- 自动 TrimSpace skills 侧单项，容忍 `"剪发, 染发"` 写法
- needle 为空时返回 false（避免空匹配全 true）
- needle 不做 TrimSpace（callers 应传 DB 里存的干净字面值）

**设计理由：**
- **为什么需要 Tier 2？** 现实里很多小店没系统化登记每位师傅的技能，Skills 字段为空不代表"不会"，更可能是"懒得填"。视作全能兜底比拒绝公平。
- **为什么需要 Tier 3？** 如果店铺就两个师傅，Tony 请假、Kevin 会这门手艺但时段被占，如果没 Tier 3 就直接走"兜底取消"，顾客体验更差。Tier 3 宁可"乱派"也不"取消"。
- **为什么同档内 name ASC？** 稳定可预测。后续可按评分/距离优化，但 name asc 是最朴素的"公平"指标。

**关键设计决策：**

| 决策 | 选择 | 理由 |
|---|---|---|
| Skills 匹配粒度 | 精确匹配单项 | 子串匹配（如"染"匹配"染发"）会假阳性 |
| 空 Skills 处理 | 视作 Tier 2 全能 | 现实里小店没系统登记；视作全能更友好 |
| 都不匹配怎么办 | Tier 3 任意 active | 保底可用性，避免全员"匹配不上就取消" |
| 同档排序 | name ASC | 稳定可预测；后续可按评分/距离优化 |
| Service 为空时 | 跳过 Tier 1，走 Tier 2 | skillContains 永远 false |
| needle TrimSpace | 不做 | callers 负责传干净字面值；避免意外匹配 |

**测试覆盖（`storage/barber_leave_test.go` +14 用例）：**

`skillContains` 纯函数（6 用例）：
- TestSkillContains_ExactMatch：`"剪发,染发"` 含 `"染发"` → true
- TestSkillContains_TrimSpace：容忍 skills 侧空格
- TestSkillContains_NoPartialMatch：子串不匹配（`"染"` 不命中）
- TestSkillContains_EmptyNeedleReturnsFalse：needle 空 → false
- TestSkillContains_EmptySkills：双空 → false
- TestSkillContains_SingleSkill：单项也能匹配

`findAlternateBarber` 三档分级（8 用例）：
- TestFindAlternateBarber_Tier1_SkillsMatch_PreferredOverEmpty：Tier 1 压制 Tier 2
- TestFindAlternateBarber_Tier2_EmptySkills_WhenNoMatch：Tier 1 不命中时 Tier 2 兜底
- TestFindAlternateBarber_Tier3_AnyActive_WhenNoMatch_NoEmpty：全员不匹配 + 无空 → Tier 3
- TestFindAlternateBarber_BusyExcluded_AcrossTiers：跨档级 busy 排除
- TestFindAlternateBarber_AllBusy_ReturnsFalse：全员忙 → 返回 false
- TestFindAlternateBarber_ExcludesOriginalBarber：不能选回原 barber
- TestFindAlternateBarber_NoOtherBarber_ReturnsFalse：无候选 → 返回 false
- TestFindAlternateBarber_Tier1_OrderByName：同档 name asc
- TestFindAlternateBarber_ServiceEmpty_AllTiersSkipped：Service 空 → Tier 2 兜底

---

### 11.8 P2 — dashboard 事件漏斗（v3.8 新增，2026-06-21）

> **背景**：PRD §11.2 P1「经营看板 MVP」目前只展示预约聚合（总 / 完成 / 爽约 / 取消 / 即将到店等），但 18 个埋点事件（`EventAppointmentCreated` / `EventBlacklisted` / `EventIdleSlotPush` / `EventBarberLeaveCreated` 等）只躺在 `event_logs` 表里，商户看不到。P2 v3.8 把这些事件按 today / week / month 三窗口聚合进 dashboard response，让商户一眼看清"今天创了多少预约 / 推了多少 idle 提醒 / 拉黑了多少人"。

#### 11.8.1 dashboard response 新增字段

```go
type EventStat struct {
    EventType string `json:"event_type"`
    Count     int    `json:"count"`
}

type DashboardResponse struct {
    // ... 原有字段
    EventFunnelToday []EventStat `json:"event_funnel_today"`
    EventFunnelWeek  []EventStat `json:"event_funnel_week"`
    EventFunnelMonth []EventStat `json:"event_funnel_month"`
}
```

每个窗口独立聚合（todayStart / weekStart / monthStart 为下界，now 为上界），按 count DESC 排序，截 top 20 防止 response 膨胀。

#### 11.8.2 eventFunnel helper（api/api.go）

```go
func eventFunnel(ctx context.Context, shopID string, since, until time.Time, limit int) []EventStat
```

实现要点：
- **粗筛**：`created_at` 落在 `[since-1d, until+1d]`，给跨天 / 边界预留 buffer
- **精确过滤**：Go 端用 `storage.ParseAnyTime`（`map[string]any` 中转）跨 sqlite / mysql 驱动解析 `created_at`（与 `FindShopsForLifecycle` 同样的策略，避免 driver 差异）
- **归一化**：`EventIdleSlotPush` 在存储层是 `idle_slot_push:DATE:CUSTID`（带 customer 维度的幂等键），按 `:` 切前缀，归一为 `idle_slot_push`（避免展开成 N 条）
- **排序**：count DESC；count 相同时按 `event_type` ASC 稳定排序
- **limit**：`limit <= 0` 时返回全部；`limit > len(out)` 时直接返回（不补零）

#### 11.8.3 修复 pre-existing SQL warning

`isCustomerBlacklistedByTx`（`storage/customer_tags.go`）和 `IdleSlotPusher.pushForShop`（`cron/idle_push.go`）都引用了 `shop_id` 列做过滤，但 `Customer` 模型没有 `shop_id` 字段（顾客跨店共享，黑名单是按顾客维度的）。SQLite 和 MySQL 都报 `no such column: shop_id` warning。

**修复决策**：去掉 `shop_id` 过滤；`shopID` 参数保留兼容 call site，但加 `_ = shopID` 显式标注「已不用」。
- **为什么不加 `shop_id` 列？** 设计上顾客是跨店共享的（一个 VIP 顾客在所有店都 VIP），加列会破坏这一不变量；后续如果要做"分店专属 VIP"，应该新开一张 `customer_shop_preference` 表。
- **为什么不改 call site？** 移除参数会牵动 `repo.go:checkBlacklist` 等多个文件；保留参数 + 显式 `_` 是最小风险做法。

#### 11.8.4 关键设计决策

| 决策 | 选择 | 理由 |
|---|---|---|
| 聚合粒度 | today/week/month 三窗口 | 商家日常关心"今天"+"这周"+"这个月"，再多窗口响应膨胀 |
| 时间字段处理 | Go 端 parseAnyTime | sqlite 返回 string，mysql 返回 time.Time，统一在 Go 解析 |
| idle_slot_push 归一 | 切 `:` 前缀 | 保留 storage 的幂等命名空间，dashboard 端聚合时不展开 |
| sort tiebreaker | event_type ASC | count 相同时给一个稳定排序，避免 limit 边界抖动 |
| 窗口缓冲 | ±1 天 | 跨天预约 + 时区边界预留 |
| 黑名单 shopID 处理 | 去掉 SQL 过滤 | Customer 模型本身没有 shop_id；用 `_ = shopID` 标注 |

#### 11.8.5 测试覆盖（+14 用例）

**storage（+5）** `customer_tags_test.go`：
- `TestIsCustomerBlacklistedByTx_PhoneMatch`：按手机号匹配黑名单
- `TestIsCustomerBlacklistedByTx_NameFallback`：手机号空时按 name 匹配
- `TestIsCustomerBlacklistedByTx_NoMatch`：陌生 phone 不命中
- `TestIsCustomerBlacklistedByTx_EmptyCustomerNoOp`：空 customer 短路返回 false
- `TestIsCustomerBlacklistedByTx_ShopIDAccepted`：**关键回归** —— 传 shopID 不再触发 shop_id 列警告

**api（+9）** `dashboard_test.go`：
- `TestEventFunnel_EmptyDB`：空 DB 返回空
- `TestEventFunnel_GroupsByType`：多种事件正确按 event_type 聚合
- `TestEventFunnel_SortByCountDesc`：count DESC 排序
- `TestEventFunnel_LimitApplied`：limit 截断
- `TestEventFunnel_NormalizesIdleSlotPushPrefix`：`idle_slot_push:DATE:CUST` 归一
- `TestEventFunnel_FiltersByShopID`：按 shop 隔离
- `TestEventFunnel_FiltersByTimeRange`：跨窗口过滤
- `TestEventFunnel_DBNotInitialized`：DB nil 安全
- `TestBuildDashboard_IncludesEventFunnel`：integration —— dashboard response 三窗口均含 funnel

#### 11.8.6 后续可继续做的 P2 增量

1. **多店数据汇总**（PRD §11.3 连锁版本）：新增 `/api/dashboard/chain` endpoint 跨店聚合
2. **事件趋势图**：把 funnel 扩成时序（`event_funnel_30d_trend` 数组），商户看事件量随时间变化
3. **事件详情钻取**：在 dashboard 上点某个 event_type → 弹最近 N 条 ref_id + meta
4. **修 pre-existing `customer_tags.go:134` 老 warning 的同时，把 `late_cancel_count` / `no_show_count` 单独建索引**（目前全表扫，黑名单多时性能下降）

---

### 11.9 MVP 第 5 项 — Agent 转人工兜底（v3.9 新增，2026-06-21）

#### 11.9.1 业务背景

PRD §0 提到 MVP 五大能力：「**复杂问题转人工**」是其中一项。Agent 工具能力有边界 —— 投诉、退款、改价、礼品卡、跨店售后等场景没有对应工具；硬扛只会让顾客更恼火、留下"这 AI 不行"的印象。

**设计原则**：
- **诚实兜底**：工具能力外的需求，直接转人工，不假装能处理
- **可观测**：每一次转人工都写埋点，商户后台能看到"今天有几个还没处理"
- **防滥用**：Agent 指令里明确"不要没事就调"，只在 3 类场景才允许

#### 11.9.2 当前实现（伪 handoff）

| 模块 | 文件 | 职责 |
|---|---|---|
| 工具实现 | `tools/handoff_to_human.go` | 解析参数 + 写埋点 + 返回成功摘要 |
| 事件类型 | `storage/event_log.go:EventHandoffToHuman` | `handoff_to_human` 事件标识 |
| Agent 集成 | `agent.go:buildAgentTyped` | 工具注册 + 指令约束 |
| Dashboard 卡片 | `api/api.go:DashboardResponse.HandoffPendingToday` | 商户一眼看到"今天待处理 N 个" |
| 埋点查询 | `api/api.go:findHandoffCount` | 复用 `EventFunnelToday` 零额外 SQL |

**为什么不直接接企业微信客服会话？**
- 当前是 MVP，先把"埋点 + 商户可观测"跑通
- 工具签名稳定，后续对接微信客服 / udesk / 智齿等第三方时只改实现，不改 Agent 侧
- 后期商户在后台看到 `HandoffPendingToday > 0` 时，**主动**通过企业微信联系顾客，体验更可控

#### 11.9.3 工具参数

```json
{
  "customer": "Alice",                       // 可选，顾客姓名/标识
  "reason": "顾客要求找店长",                  // 必填，商户能看懂的一句话
  "last_user_message": "我要投诉 Tony 手法"   // 可选，顾客最后一条原文（截断到 200 字）
}
```

**工具返回（给 Agent 看，不是给顾客看）**：
> 已为顾客 "Alice" 发起人工转接（原因：顾客要求找店长）。请用自然语言告诉顾客已转人工，请稍候。

Agent 拿到后**自己润色**："好的，我帮您转给店员，请稍等"——**不要**把工具的 JSON 原样贴给顾客。

#### 11.9.4 Agent 指令约束

Agent 只能在这 3 类场景调用 `handoff_to_human`：

| 场景 | 例子 |
|---|---|
| ① 顾客明确要求找人工 | "叫老板来"、"我要投诉"、"转人工" |
| ② 业务超出 Agent 能力 | 投诉处理、退款、改价、礼品卡等**没有对应工具**的事 |
| ③ 连续 2 轮 Agent 都无法识别顾客意图 | 别再死磕，直接转 |

**严禁场景**：
- 顾客语气不好 / 抱怨排队久 → **不转**，用工具解决
- Agent 答不上来普通问题 → **不转**，引导顾客换个问法
- 怕麻烦 / 嫌烦 → **不转**，这是滥用

**约束位置**：`agent.go:buildAgentTyped` 的 instruction 段，注释清楚"严禁"和"允许"两栏。

#### 11.9.5 关键设计决策

| 决策 | 选择 | 理由 |
|---|---|---|
| 工具名字 | `handoff_to_human` | 业界通用（Intercom / Zendesk / Salesforce 都用 handoff），Agent 训练语料里多 |
| 必填字段 | `reason` | 商户在后台看到一条 handoff 事件，第一眼要能看懂"为什么转" |
| ref_id 来源 | customer 字段 / `unknown-<nano>` | 有 customer 就用，没有就生成 unknown- 兜底，避免多条埋点挤在同 ref_id |
| meta 截断 | `last_user_message` 限 200 字 | 防止个别顾客粘长文撑爆 event_log.Meta 字段 |
| 实际触发会话 | 暂不接 | MVP 阶段商户主动联系体验更可控；后期可加真转接 |
| 去重 | 工具不强制去重 | 商户后台看到多条同源会自然合并处理，工具侧复杂化不划算 |
| Dashboard 字段 | `HandoffPendingToday`（不是 week/month） | 商户最该看的是"今天的待处理"，长周期用 week/month funnel 已够 |
| Dashboard 计算 | 复用 `EventFunnelToday` 找值 | 零额外 SQL，纯 Go 端遍历 EventStat 切片 |
| fallback shopID | 无 ctx 时填 `default` | 避免埋点丢失；wecom 链路必带 shopID，default 是理论兜底 |

#### 11.9.6 测试覆盖（+10 用例）

**tools（+5）** `tools/handoff_to_human_test.go`：
- `TestHandoffToHumanTool_BasicSuccess`：正常路径 → 写埋点 + 返回成功文案
- `TestHandoffToHumanTool_EmptyReason_Errors`：缺 reason 必报错 + 不写埋点
- `TestHandoffToHumanTool_NoShopID_Fallback`：ctx 无 shopID → fallback `default`，不 panic
- `TestHandoffToHumanTool_NoCustomer_GeneratesRefID`：没 customer → ref_id 用 `unknown-<nano>` 兜底
- `TestHandoffToHumanTool_LongMessage_Truncated`：超长 last_user_message → 截断到 ~200 字

**api（+5）** `api/dashboard_test.go`：
- `TestFindHandoffCount_Found`：stats 里有 handoff → 返回正确 count
- `TestFindHandoffCount_NotFound`：stats 里无 handoff → 返回 0
- `TestFindHandoffCount_EmptyStats`：nil / 空 stats → 返回 0
- `TestBuildDashboard_HandoffPendingToday`：3 today + 1 other + 1 old（40 天前）→ `HandoffPendingToday=3`，old 不被计入
- `TestBuildDashboard_HandoffPendingToday_EmptyDB`：空 DB → 0

#### 11.9.7 后续可继续做（增量）

1. **真转接**：对接企业微信客服会话 API，Agent 调 handoff 后直接拉起人工客服
2. **HandoffPendingToday 点击钻取**：dashboard 卡片 → 弹最近 N 条 handoff 事件（ref_id + reason + last_user_message）
3. **Handoff SLA 监控**：超过 X 分钟未处理的 handoff 推送告警给商户
4. **Agent 自评**："是否真的解决不了"打分，连续 3 轮低分自动 handoff（替代固定 2 轮规则）

---

### 11.10 P2 — 多店数据汇总 / 连锁看板（v4.0 新增，2026-06-21）

#### 11.10.1 业务背景

PRD §11.3「多店数据汇总」是 P2 里的加分项 —— 现实里很多连锁品牌 owner 一个人盯 3-10 家店，逐店切 dashboard 效率低。新版 endpoint 一次性返回：
- **所有门店的总体经营指标**（total / noshow / completed）
- **每家店明细**（让 owner 一眼对比）
- **Top 5 忙店**（按总预约数排序，识别明星门店）
- **跨店事件漏斗**（看整个连锁的事件分布）

#### 11.10.2 Endpoint

```
GET /api/admin/chain/dashboard
```

鉴权：任何已登录的 admin 都能访问（`role != ""`），不限定 platform_admin。

**为什么不做 platform_admin 限定？**
- 当前 ShopAdmin 只有 owner / staff 两种角色，没有 platform_admin 概念
- 真实场景中连锁 owner 通常也是某家店的 owner（用 owner 账号登录就能看所有店）
- 后续要做细粒度控制：加 `platform_admin` 角色 + 限定 endpoint
- 文档里写明这一权衡，避免后续误以为"默认是 owner 限定"

#### 11.10.3 响应结构

```json
{
  "generated_at": "2026-06-21T16:30:00+08:00",
  "total_shops": 3,
  "chain_totals": {
    "window": "month",
    "total": 8,
    "completed": 6,
    "noshow": 1,
    "cancelled": 1,
    "active": 0,
    "no_show_rate": 0.143,
    "complete_rate": 0.857
  },
  "shops": [
    {
      "shop": { "id": "shop-A", "name": "总店", ... },
      "stats": { "total": 2, "completed": 2, "noshow": 0, ... }
    },
    ...
  ],
  "top_shops": [
    { "shop_id": "shop-B", "shop_name": "分店B", "total": 5 },
    { "shop_id": "shop-A", "shop_name": "总店",   "total": 2 },
    { "shop_id": "shop-C", "shop_name": "分店C", "total": 1 }
  ],
  "event_funnel_chain": [
    { "event_type": "appointment_created", "count": 8 },
    { "event_type": "appointment_completed", "count": 6 },
    ...
  ]
}
```

**字段说明**：
- `chain_totals`：月窗口（30 天）跨店合计 —— 商家最关心的"过去一个月整盘"
- `shops`：每家店明细（与 `chain_totals` 同一窗口）
- `top_shops`：按 total DESC 排序，limit 5
- `event_funnel_chain`：跨店事件漏斗（月窗口），复用 `eventFunnel` 的归一逻辑（`idle_slot_push:DATE:CUST` → `idle_slot_push`）

#### 11.10.4 关键设计决策

| 决策 | 选择 | 理由 |
|---|---|---|
| 时间窗口 | 月（30 天） | 商家关心"近期整盘"，日/周波动大；单店 dashboard 已有 today/week/month 三档，chain 默认月更稳 |
| 性能边界 | N+2 次 SQL（N 个店 + 1 list + 1 event 跨店） | 当前目标 5-20 家店足够；100+ 时改成批量 appointments 查 + Go 端按 shop_id 分组 |
| TopShops limit | 5 | 老板看 dashboard 不希望滚屏；超过 5 就得 list 全部 → 后端排序后取前 5 |
| 鉴权 | 任何已登录 admin | 见 §11.10.2，权衡 |
| 数据源 | 直接 ListAllShops + ShopAggregateByID | 不引入新的"门店分组"概念，保持简单；后续分库要重构成 union |
| 事件漏斗跨店 | 不按 shop_id 过滤 | chain funnel 看的是"整个连锁"事件分布；单店 funnel 仍在 `/api/shop/:id/dashboard` |
| 排序 tiebreaker | shopID ASC | total 相同时给一个稳定排序，避免 limit 边界抖动 |

#### 11.10.5 关键代码

**storage**（`storage/chain_repo.go`）：
- `ListAllShops(ctx)` —— ListAll shops，DB nil 返回空切片（零成本）
- `ShopAggregateByID(ctx, shopID, from, to)` —— 单店 [from, to) 预约汇总，复用单店 dashboard 的 date+time 解析口径

**api**（`api/chain_dashboard.go`）：
- `chainDashboardHandler` —— HTTP handler，鉴权后调 buildChainDashboard
- `buildChainDashboard(ctx)` —— 串起来：ListAll → 逐店 ShopAggregateByID → ChainTotals 累加 → TopShops 排序 + limit 5 → chainEventFunnel
- `chainEventFunnel(ctx, since, until, limit)` —— 不按 shop_id 过滤的 eventFunnel

**路由注册**（`api/api.go:RegisterRoutes`）：
```go
// 任何已登录 admin 都能访问
protected.GET("/chain/dashboard", chainDashboardHandler)
```

#### 11.10.6 测试覆盖（+16 用例）

**storage（+4）**：
- `TestListAllShops_EmptyDB`：空 DB 返回空切片
- `TestListAllShops_MultipleShops`：3 家店按 id ASC 返回
- `TestShopAggregateByID_EmptyDB`：空数据全 0
- `TestShopAggregateByID_GroupsByStatus`：1+1+1+1 = total 4，分项对齐，闭单率计算正确
- `TestShopAggregateByID_FiltersByDateRange`：5 天前的不在 today 窗内
- `TestShopAggregateByID_ShopIsolation`：shop-A 1 + shop-B 2 严格隔离

**api（+12）**：
- `TestBuildChainDashboard_EmptyDB`：空 DB，TotalShops=0 / ChainTotals=0
- `TestBuildChainDashboard_SingleShop`：1 家店 2 单 → 链合计 = 单店
- `TestBuildChainDashboard_MultiShop`：3 家店 (2+5+1=8) → TopShops 按 DESC 排序 B/A/C
- `TestBuildChainDashboard_TopShops_Limit5`：8 家店 → 只返回 top 5
- `TestChainEventFunnel_GroupsAcrossShops`：shop-A 2 + shop-B 1 = appointment_created=3（跨店合计）
- `TestChainEventFunnel_ExcludesOldEvents`：40 天前不在月窗内
- `TestChainEventFunnel_NormalizesIdleSlotPush`：`idle_slot_push:DATE:CUST` 跨店归一
- `TestChainDashboardHandler_NoClaims_401`：未登录 → 401
- `TestChainDashboardHandler_HappyPath`：登录后返回正确 JSON 结构
- `TestChainDashboardHandler_DBNotInitialized`：DB nil → 503

#### 11.10.7 后续可继续做（P2 增量）

1. **platform_admin 角色限定**：加 `platform_admin` role + login handler 支持，给连锁总部单独账号
2. **时间窗口 query 参数**：`?window=today|week|month`，让 chain dashboard 也能选窗口
3. **跨店客户分析**：top customer（按 total_visits 排序，跨店），帮 owner 看"谁是我的 VIP 客户"
4. **跨店理发师排行**：合并所有店 barber_name，看"谁是最多单的师傅"
5. **批量聚合优化**：N+2 → 1（一次查所有 appointments，Go 端按 shop_id 分组），支持 100+ 店
6. **Dashboard UI 切换**：在 `static/admin.html` 加"切到 chain 看板"按钮（需 platform_admin 限定）

---

## 12. 总结

### 12.1 核心优势

1. **成本结构极轻**：年固定成本 ~2,350 元，边际成本接近零
2. **毛利率极高**：保守场景净利润率 93%，乐观场景 99%
3. **定价有竞争力**：19.9 元/月体验价是美团开店宝的 1/12，对夫妻店极有吸引力
4. **技术栈成熟**：Go + Eino + DeepSeek，你已有 5 年 Go 经验，开发效率高
5. **合规路径清晰**：企业微信官方生态，无封号风险

### 12.2 一句话总结

> **前期投入不到 3,000 块，跑通后年利润可达数万至数十万，毛利率超 90%。这是一个极低成本、高毛利、可复制的 SaaS 项目。**

### 12.3 下一步行动

1. 把本文档喂给 coding 工具，按 §11.1 P0 清单逐项实现
2. 同步联系 3–5 家种子美发店准备内测
3. 开通企业微信认证 + 申请 DeepSeek API key
4. 第 7–8 周启动首月 19.9 元体验价

---

*文档版本：v4.0 | 更新日期：2026-06-21*
*— Mavis（M3）整理*

**v3.5 增量**：新增 §11.7.8 P4 cron 兜底（`LeaveExpirer` 每分钟扫描 + `ExpireOverdueLeaves` storage helper + `EventBarberLeaveExpired` 事件 + 9 个新单测）。
**v3.6 增量**：新增 §11.7.9 P4 query_schedule 视觉区分（`QueryScheduleBreakdown` helper + 三段渲染 + 6 storage 单测 + 6 tools 单测）+ §11.7.10 list_barbers 标记今日请假理发师（8 tools 单测，共 20 个新单测）。
**v3.7 增量**：新增 §11.7.11 P4 改派策略升级（`findAlternateBarber` 三档分级 + 14 个新单测）。
**v3.8 增量**：新增 §11.8 P2 dashboard 事件漏斗（`eventFunnel` helper + today/week/month 三窗口 + 9 个 api 单测）+ 修 pre-existing `customer_tags.go:132` 和 `idle_push.go:162` 引用不存在 `shop_id` 列的 SQL warning（5 个 storage 单测）。
**v3.9 增量**：新增 §11.9 MVP 第 5 项「转人工兜底」（`HandoffToHumanTool` 写埋点 + 3 类允许场景约束 + `EventHandoffToHuman` 事件类型 + 5 个 tools 单测）+ `DashboardResponse.HandoffPendingToday` 卡片（复用 `EventFunnelToday` 零额外 SQL + 5 个 api 单测），共 +10 个新单测。
**v4.0 增量**：新增 §11.10 P2 多店数据汇总（`/api/admin/chain/dashboard` 跨店看板 endpoint + `storage.ListAllShops` + `storage.ShopAggregateByID` 跨店聚合 helper + `chainEventFunnel` 跨店事件漏斗 + 16 个 api 单测）。