# 投资人 Demo AI 短片 Prompt 库（v4.13）

> 给 Runway / Sora / Kling / 即梦 / 通义万相 / 可灵 等 AI 视频生成工具用。
> 5 段短片（5-8s/段）拼你的录屏，剪映组装成 3-4 分钟投资人 demo。

---

## 产品名

**简法预约助手**（v4.13 起的品牌名）
- 旧名「美发预约助手」仅在代码历史里保留，外部材料统一用新名
- 副 slogan：「AI 帮美发店老板接住每个顾客」
- 一句话定位：「让老板少干活，让顾客少等待」

---

## 视觉风格指南（每段 prompt 都加这段）

```
现代扁平插画风格，柔和电影光影，暖色调（主色橙 #c26044 + 辅色橄榄绿 #788c5d），
中国美发店场景，16:9 构图，干净背景，主体居中偏左，景深浅。
参考风格：Notion 营销页插画 + Apple 人文系列 + 微信红包封面
```

> 想要 6 段 AI 视频看起来"是一家做的"，**这段必须原样放在每段 prompt 开头**。

---

## 5 段镜头

### 场景 1 — 老板焦虑（0:00-0:07）

**中文文案**（配音用）：
> "30 万家美发店老板的日常——"

**画面描述**：
- 角色：中国中年男性（40-50 岁），穿围裙，店里
- 道具：手里拿着手机，屏幕上塞满微信消息气泡（10+ 条）
- 表情：揉太阳穴，疲惫、皱眉
- 环境：背景是镜台 / 理发椅（虚化）

**AI Prompt**（英文，直接粘贴）：
```
A tired Chinese male hair salon owner in his 40s wearing a work apron, 
looking at his phone overwhelmed by WeChat chat message bubbles stacking up, 
rubbing his temple with a stressed expression, 
modern flat illustration style, soft cinematic lighting, 
warm orange and olive green palette, shallow depth of field, 
Chinese hair salon interior background softly blurred, 
16:9 composition, subject centered-left
```

---

### 场景 2 — 老板手动登记（0:07-0:15）

**中文文案**：
> "每天花两小时手动登记预约、改期、回微信、解释爽约。"

**画面描述**：
- 同角色
- 道具：面前本子上写满字、撕碎的小纸条堆在桌角、收银机、笔筒
- 动作：边写边看手机、叹口气
- 环境：收银台后面，挂钟显示 18:00（晚上）

**AI Prompt**：
```
A Chinese male hair salon owner in his 40s writing appointment notes 
in a messy paper notebook, surrounded by torn paper slips and a small 
calculator, glancing at his phone with a sigh, the wall clock shows 6 PM, 
warm interior lighting, 
modern flat illustration style, soft cinematic lighting, 
warm orange and olive green palette, 16:9 composition
```

---

### 场景 3 — 想象 / 未来（0:15-0:22）

**中文文案**：
> "如果 AI 帮他们接住呢？"

**画面描述**：
- 同角色，**穿围裙但表情轻松**
- 道具：手里端着咖啡杯，**旁边悬浮一个发光的对话界面**（半透明，显示顾客消息 + AI 自动回复）
- 表情：微笑、放松、眼神看屏幕
- 环境：吧台角落，柔和光

**AI Prompt**：
```
A Chinese male hair salon owner in his 40s, now wearing apron but with a 
calm and proud smile, holding a coffee cup, beside him a glowing semi-transparent 
chat interface showing customer messages and AI auto-replies, soft warm lighting, 
modern flat illustration style, soft cinematic lighting, 
warm orange and olive green palette with subtle blue glow from the AI screen, 
16:9 composition, peaceful atmosphere
```

---

### 场景 4 — 录屏 demo（0:22-2:55，约 2 分半）

**这段不用 AI 生成，用你的真实录屏。** 配音 / 操作念这版脚本：

| 时段 | 念稿 | 操作 |
|---|---|---|
| 0:22-0:30 | "**简法预约助手**——3 分钟给你看完全平台怎么管。" | 浏览器开 admin.html |
| 0:30-0:40 | "登录 platform_admin 账号，能看到全平台所有店铺。" | 输 platform / platform123，登录 |
| 0:40-1:10 | "**平台总览**——5 个 KPI。这块月度订阅收入估 ¥xxxx，是按 plan × 店数。" | 平台总览页，停 3 秒让数字入眼 |
| 1:10-1:20 | "点这个 KPI 卡，跳到店铺管理。" | 点「全平台店铺」KPI |
| 1:20-1:50 | "全平台 x 家店。这家 basic 店——" | 进店铺管理表格 |
| 1:50-2:20 | "改套餐——选 flagship、12 个月、备注 demo 升级。**注意**改完表格自动刷新。" | 改套餐 → 选 plan → 改月数 → 备注 → 确认 |
| 2:20-2:40 | "详情看订阅历史——之前 basic，现在旗舰。**成员列表**——店主 + 店员。" | 点详情，等加载 |
| 2:40-2:55 | "**套餐审计**——所有改套餐记录都在这，操作人 / 原 plan / 新 plan / 月数 / 备注，**可追溯**。" | 切到套餐审计页 |

---

### 场景 5 — 老板满意收尾（2:55-3:05）

**中文文案**：
> "30 秒看完，老板喝咖啡，AI 帮他搞定。"

**画面描述**：
- 同角色，靠在吧台边
- 道具：手机屏幕显示推送 "**已为 3 位顾客自动预约**"
- 表情：欣慰、满足、点个头
- 环境：店里轻松氛围，背景镜子里能看见空的理发椅

**AI Prompt**：
```
A Chinese male hair salon owner leaning casually at his counter, 
looking at his phone showing a WeChat notification "已为 3 位顾客自动预约", 
a satisfied and proud smile, mirror behind him reflects empty barber chairs, 
relaxed end-of-day atmosphere, 
modern flat illustration style, soft cinematic lighting, 
warm orange and olive green palette, golden hour lighting, 
16:9 composition
```

---

## 工具选择建议

| 工具 | 时长 | 风格控制 | 费用 | 推荐度 |
|---|---|---|---|---|
| **即梦 AI**（字节） | 5-10s/段 | 中等，中文 prompt 友好 | 免费额度多 | ⭐⭐⭐ 国产首选 |
| **可灵 AI**（快手） | 5-10s/段 | 好 | 免费额度 | ⭐⭐⭐ 国产次选 |
| Runway Gen-3 | 4-16s/段 | 很好（控制最细） | $12/月起 | ⭐⭐ 风格最稳但贵 |
| Sora | 5-20s/段 | 很好 | ChatGPT Plus | ⭐⭐ 质量高，限制多 |
| 通义万相（阿里） | 5s/段 | 中等 | 免费 | ⭐ 国内应急 |

**建议用即梦或可灵**——中文 prompt 理解准，国产场景素材多，免费额度够 5 段。

---

## 跑 AI 视频的实操技巧

1. **先跑场景 1 + 3 试水**（风格基调先定下来），满意后再批量跑
2. 每段 prompt 都把上面「视觉风格指南」原样贴开头——避免 5 段风格漂移
3. 角色一致性：5 段都是「40-50 岁中国男性、围裙」，**关键词完全一致**
4. 每段跑 2-3 个备选，挑最好的；不满意的局部重跑
5. 输出分辨率选 **1920×1080 / 16:9 / 24fps**（剪映里好处理）
6. 视频生成失败/质量差时：缩短 prompt 描述、加 "highly detailed, masterpiece" 关键词
7. 商业用注意：AI 视频版权问题，建议**在末尾加一行小字 "本视频含 AI 生成素材"**

---

## 剪映组装步骤（拿到 5 段 AI 视频 + 你的录屏后）

1. 新建 1920×1080 工程，帧率 30fps
2. 拖入时间线，顺序：场景1 → 场景2 → 场景3 → 录屏 → 场景5
3. 录屏插在场景 3 和场景 5 之间（约 0:22-2:55）
4. **配音**（二选一）：
   - **你真人念**（推荐，信服力 3 倍于 TTS）—— 录屏段也用你的声音
   - 用剪映「AI 配音」生成中文男声（中低音，选"磁性解说"）
5. **字幕**：剪映「自动字幕」→ 校对 → 字体选"思源黑体 Medium" + 黄色描边
6. **BGM**：剪映「免费音乐」搜"商务"或"科技"，音量调到 **10-15%**（不能盖过配音）
7. **转场**：段间加 0.3s 淡入淡出（不要花哨转场，投资人 demo 要"专业稳"）
8. **导出**：1080p / 30fps / H.264 / 比特率 8Mbps

---

## 投资人 demo 视频 Checklist

- [ ] 5 段 AI 视频生成完毕，风格统一
- [ ] 录屏录制完毕（按场景 4 脚本）
- [ ] 配音完成（真人 or TTS）
- [ ] 剪映组装 + 字幕 + BGM + 转场
- [ ] 末尾加 "本视频含 AI 生成素材" 小字
- [ ] 导出 mp4，文件大小控制在 **< 80MB**（微信 / 飞书方便转发）
- [ ] 找个朋友看一遍，反馈"哪段最无聊"
- [ ] YouTube unlisted 上传，拿链接

---

## 留 v4.14 / 后续

- 真实顾客视角视频（拿一家 pilot 店的真实对话录屏，**比 AI 场景强 10 倍**）
- 一页式 one-pager PDF（投资人主动来问时发，配合视频用）
- 把这个 prompt 库模板化，下次 v4.14 / v4.15 demo 复用

---

**附**：本仓库的 admin.html / index.html 里现在还写的是「美发预约助手」旧名。投资人 demo 跑通后再决定是否全局改名（影响 CHANGELOG、登录页、邮件模板等 4-5 处）。今天 demo 视频用「简法预约助手」即可。
