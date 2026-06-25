# DEPLOY_DEMO.md — 投资人 demo 部署说明（v4.13.1）

> 写给：项目负责人  
> 场景：企业微信未认证 / 接待人员接管，agent 发不出消息（95001）  
> 目标：**demo 时不被企业微信卡脖子**——agent 推理 + 工具调用 + DB 写入全跑通，回复写到 event_logs 给 admin 后台演示

---

## 一句话总结

启动服务前加一行 `AGENT_REPLY_MODE=mock` 环境变量，agent 收到的所有"回复"**不发企业微信**，写到 `event_logs` 表（event_type=`demo_reply`）+ log。**业务流完整跑通，只是最后一步"发微信"换成"落库"**。

---

## 部署步骤（demo 模式）

### 1. 编译（已经在 master 上了）

```bash
cd /home/www/wwwroot/agent.yuyuanyuan.cn/
git pull  # 拉 v4.13.1（含 commit 626dd13 + 之前的修复）
pwsh scripts/build-linux.ps1
```

### 2. 编辑 systemd service 文件

```bash
vim /etc/systemd/system/chatwitheino.service
```

找到 `Environment=` 那一段，加：

```
Environment="AGENT_REPLY_MODE=mock"
```

或者 `systemctl edit` 加 override：

```bash
systemctl edit chatwitheino
# 写入：
[Service]
Environment="AGENT_REPLY_MODE=mock"
```

### 3. 重启 + 验证启动日志

```bash
systemctl daemon-reload
systemctl restart chatwitheino

# 看启动日志，应该有：
# [wecom] ⚠️ AGENT_REPLY_MODE=mock 已启用：所有回复不发给企业微信，写到 event_logs
# [cron] 启动 KfSeenMsg cleanup: 每天 3:00 清理 7 天前的 msgid 去重记录
journalctl -u chatwitheino -f
```

**看到 `⚠️ AGENT_REPLY_MODE=mock 已启用`** 就对了。

### 4. 验证

让真实顾客发一条消息（或自己模拟一条回调），看：

```bash
# 日志应该看到（注意 [demo-reply] 标记）：
[demo-reply] to=wm4_xxx openKfID=wk4_xxx shop=default reply="好的，那明天 6 月 26 日 09:30，请问您想约 Tony 还是 Kevin 师傅呢？..."

# Admin 后台 → 事件流 → 过滤 event_type=demo_reply
# 应该看到这条 mock 回复的完整记录
```

DB 验证：

```sql
SELECT event_type, JSON_EXTRACT(meta, '$.to_user') AS to_user, 
       JSON_EXTRACT(meta, '$.reply') AS reply, created_at
FROM event_logs 
WHERE event_type = 'demo_reply' 
ORDER BY id DESC LIMIT 10;
```

应该看到顾客刚发的消息触发的 agent 回复。

---

## 完整业务流验证清单（demo 时用）

| 步骤 | 验证方式 |
|---|---|
| ① 顾客发消息 | 看 `[wecom] 处理消息: ...` 日志 |
| ② Agent 推理 | 看 `[wecom] Agent回复: ...` 日志 |
| ③ DB 写预约 | Admin 后台 → 预约列表能看到新预约 |
| ④ "回复"顾客 | 看 `[demo-reply] to=... reply=...` 日志 + event_logs 表 |
| ⑤ Cron 自愈 | 重启服务后 cursor / seen_msg 仍持久化（重启恢复 demo 流程无影响） |

**5 个都 ✅ 的话，demo 演示完整业务流**。

---

## 切回 real 模式（认证通过后）

```bash
systemctl edit chatwitheino
# 删除或注释：
# Environment="AGENT_REPLY_MODE=mock"
systemctl daemon-reload
systemctl restart chatwitheino

# 启动日志不再有 ⚠️ mock 警告
```

切回 real 后，agent 会真的调企业微信接口——如果 95001 还在就还会失败，需要：
1. **企业认证**：企业微信后台 → 我的企业 → 完成企业认证（要营业执照，1-3 工作日）
2. **接待人员配置**：客服管理 → 接待人员 → 改成空占位企微号

---

## 故障排查

### Q1: 启动看不到 ⚠️ mock 警告
- 检查 env：`systemctl show chatwitheino | grep AGENT_REPLY_MODE`
- 检查拼写：必须**精确** `mock`（大小写敏感）
- 任何拼错 / 未知值都 fallback 到 real（生产安全）

### Q2: 看不到 [demo-reply] 日志
- 检查是否进了 handleKfCallback：看 `[kf] 收到客服事件` 日志
- 如果没进，看企业微信回调是否配置正确（POST /wecom/callback）
- 如果进了但没 mock，看 DB 是否初始化：`storage.DB == nil` 时跳过写表

### Q3: 看不到 demo_reply event_logs
- 跑：`SELECT * FROM event_logs WHERE event_type='demo_reply' ORDER BY id DESC LIMIT 5`
- 0 条 → DB 没初始化或 handleKfCallback 没跑到 mock 分支

### Q4: 重启服务后客服消息没接住
- 现在 `kf_sync_state` + `kf_seen_msg` 已经持久化到 DB，重启不丢
- 验证：日志第二次启动时 cursor 应该不是空（`[kf] 使用cursor拉取: cursor="..." (首次=false)`）

### Q5: 投资人说"你这看着不像真发微信"
- 把 admin 后台事件流打开，给投资人看 `demo_reply` 记录——这是 agent 完整推理 + 决策 + 行动链路
- 强调：**业务核心**（推理 + 工具调用 + DB 写入 + 多轮对话）**完整跑通**，只是最后一步发消息在 demo 模式下落库

---

## v4.13.1 修复链（给团队同步用）

| Commit | 修什么 |
|---|---|
| `860085a` 系列 | 隐私：工具输出不暴露 leave reason |
| `c0eaf4e` | upsertCustomerInTx 同步更新 customer.Name（修了隐藏的 map[string]string GORM 反射 bug） |
| `8731392` | 去掉 SendKfTextMessage 失败后的死 fallback |
| `b136501` | **cursor + msgid seen 持久化到 DB**（修进程重启后历史消息重复 / 用户消息丢失） |
| `626dd13` | **AGENT_REPLY_MODE=mock demo 兜底 + cron 清理 7 天前去重记录** |

**5 个 commit 全部在 master，可直接 git pull**。

---

## 后续要做的（生产环境）

认证通过 + 接待人员改完后，可以做的事：

1. **去掉 AGENT_REPLY_MODE=mock**（回到 real 模式）
2. **加 admin 后台告警**：当 handleKfCallback 收到 95001 / 95018 / 95002 等常见 errcode，admin 后台弹红条
3. **监控 cursor 落后**：如果 `kf_sync_state` cursor 长期不更新，admin 后台告警（说明 sync_msg 卡了）
4. **扩展 kf_seen_msg TTL**：根据业务量调整（当前 7 天）

这些都不阻塞当前 demo，做不做都行。