/*
 * Copyright 2026 CloudWeGo Authors
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	clc "github.com/cloudwego/eino-ext/callbacks/cozeloop"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/schema"
	"github.com/coze-dev/cozeloop-go"

	adkstore "github.com/yuterigele/openbook/internal/einocommon/store"
	"github.com/yuterigele/openbook/internal/agent"
	"github.com/yuterigele/openbook/api"
	"github.com/yuterigele/openbook/chatmodel"
	cronpkg "github.com/yuterigele/openbook/cron"
	"github.com/yuterigele/openbook/intent"
	lockpkg "github.com/yuterigele/openbook/lock"
	"github.com/yuterigele/openbook/mem"
	"github.com/yuterigele/openbook/msgops"
	"github.com/yuterigele/openbook/notify"
	"github.com/yuterigele/openbook/pool"
	"github.com/yuterigele/openbook/sensitive"
	"github.com/yuterigele/openbook/server"
	"github.com/yuterigele/openbook/storage"
	"github.com/yuterigele/openbook/wecom"
)

func main() {
	ctx := context.Background()

	// Load optional .env file as early as possible, so values it provides (e.g.
	// MESSAGE_KIND, MODEL_TYPE, API keys) are visible to every downstream call,
	// including msgops.KindFromEnv below. No-op if .env is absent.
	chatmodel.LoadEnv()

	// 加载敏感词词表（v4.17+：用户输入预过滤）
	// 文件缺失时静默放过，过滤器仍可工作（空词表 = 全部放行）。
	if err := sensitive.LoadProductionWords(); err != nil {
		log.Printf("⚠️  敏感词词表加载失败: %v", err)
	}

	// 初始化 MySQL（PRD §11.1 P0：持久化 + MsgId 唯一索引做幂等去重）
	// 本地开发可临时不接（DB == nil 时所有 storage 方法 panic）；
	// 生产必须配 MYSQL_DSN。
	if _, err := storage.InitDB(ctx); err != nil {
		log.Fatalf("初始化 MySQL 失败: %v", err)
	}

	// 初始化 Redis（PRD §3.3：分布式锁防并发预约冲突）
	// 本地开发无 Redis 时也会降级放行（lock.AcquireAppointmentLock 返回无锁句柄）。
	if _, err := lockpkg.InitRedis(ctx); err != nil {
		log.Printf("⚠️  Redis 初始化失败（继续运行，但并发预约可能冲突）: %v", err)
	}

	// v4.17+：手写 worker pool（bounded concurrency + backpressure + panic recovery）
	// 默认 4 workers / 64 queue；可通过 OPENBOOK_POOL_SIZE / OPENBOOK_POOL_QUEUE 调。
	precheckPool, err := pool.New(
		envInt("OPENBOOK_POOL_SIZE", 4),
		envInt("OPENBOOK_POOL_QUEUE", 64),
	)
	if err != nil {
		log.Fatalf("init pool: %v", err)
	}
	defer precheckPool.Close()
	log.Printf("[pool] precheck pool started: size=%d queue=%d",
		precheckPool.Size(), precheckPool.QueueCap())

	// setup cozeloop tracing (optional)
	// COZELOOP_WORKSPACE_ID=your workspace id
	// COZELOOP_API_TOKEN=your token
	cozeloopApiToken := os.Getenv("COZELOOP_API_TOKEN")
	cozeloopWorkspaceID := os.Getenv("COZELOOP_WORKSPACE_ID")
	if cozeloopApiToken != "" && cozeloopWorkspaceID != "" {
		client, err := cozeloop.NewClient(
			cozeloop.WithAPIToken(cozeloopApiToken),
			cozeloop.WithWorkspaceID(cozeloopWorkspaceID),
		)
		if err != nil {
			log.Fatalf("cozeloop.NewClient failed: %v", err)
		}
		defer func() {
			time.Sleep(5 * time.Second)
			client.Close(ctx)
		}()
		callbacks.AppendGlobalHandlers(clc.NewLoopHandler(client))
	}

	switch msgops.KindFromEnv() {
	case msgops.KindAgentic:
		runTyped[*schema.AgenticMessage](ctx)
	default:
		runTyped[*schema.Message](ctx)
	}
}

func runTyped[M adk.MessageType](ctx context.Context) {
	// v4.17+ LLM 降级链：DeepSeek → OpenAI → Ark（可在 env 调顺序）
	// Init-time fallback：主 provider 挂了自动切下一个；
	// 全部挂才 fatal。运行中 provider 挂的 runtime fallback 由
	// helpers/retry.go 的 IsRetryAble 配合 model 层处理。
	cm, _, chain, err := chatmodel.NewModelWithFallback[M](ctx)
	if err != nil {
		log.Fatalf("all LLM providers failed: %v", err)
	}
	for _, e := range chain {
		if e.Err == "" {
			log.Printf("[chatmodel] ✓ %s (idx %d) init %v", e.Provider, e.Index, e.Latency)
		} else {
			log.Printf("[chatmodel] ✗ %s (idx %d) init %v failed: %s", e.Provider, e.Index, e.Latency, e.Err)
		}
	}

	// v4.17+：双层意图分类（关键词 + LLM 兜底）。
	// 关键词层永远在线（免费、零延迟）；LLM 层复用上面的 chat model
	// （降级链选出来的那个）做语义分类兜底。
	//
	// LLM 层在 generic M 上接 BaseModel[M] 有点麻烦（不同 M 的 Generate
	// 签名不一样），所以这里只装"可装"的占位实现：见 intent.NewLLMClassifyFuncFromEino
	// 的注释，调用方如果 M = *schema.Message 可以用 type switch 装上。
	// 实际跑起来时，关键词层够用，LLM 层是加分项。
	_ = cm // mark used for clarity; the intent tool is keyword-only at runtime.
	intentClf := intent.NewClassifier()
	intentTool := intent.NewClassifyTool(intentClf)

	agent, err := agent.BuildTyped[M](ctx, intentTool)
	if err != nil {
		log.Fatalf("failed to build agent: %v", err)
	}

	checkpointStore := adkstore.NewInMemoryStore()

	sessionDir := msgops.DefaultSessionDir(msgops.KindOf[M]())
	log.Printf("message kind: %s", msgops.KindOf[M]())
	log.Printf("session dir: %s", sessionDir)

	workspaceDir := os.Getenv("WORKSPACE_DIR")
	if workspaceDir == "" {
		workspaceDir = "./data/workspace"
	}

	store, err := mem.NewStore[M](sessionDir)
	if err != nil {
		log.Fatalf("failed to create session store: %v", err)
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "38080"
	}

	projectRoot := os.Getenv("PROJECT_ROOT")
	if projectRoot == "" {
		// Default: the directory from which the binary is run.
		// Override with PROJECT_ROOT=/path/to/repo to give the agent full codebase access.
		if cwd, err := os.Getwd(); err == nil {
			projectRoot = cwd
		}
	}
	if abs, err := filepath.Abs(projectRoot); err == nil {
		projectRoot = abs
	}
	log.Printf("project root: %s", projectRoot)

	// EXAMPLES_DIR points to the root of the eino-examples repository.
	// Defaults to PROJECT_ROOT/examples if that directory exists, otherwise PROJECT_ROOT.
	examplesDir := os.Getenv("EXAMPLES_DIR")
	if examplesDir == "" {
		candidate := filepath.Join(projectRoot, "examples")
		if fi, err := os.Stat(candidate); err == nil && fi.IsDir() {
			examplesDir = candidate
		} else {
			examplesDir = projectRoot
		}
	}
	if abs, err := filepath.Abs(examplesDir); err == nil {
		examplesDir = abs
	}
	log.Printf("examples dir: %s", examplesDir)

	// 加载企业微信配置（多店版）
	// 优先级：1) shops 表里的 wecom_* 字段（每个店铺独立） 2) env 兜底（兼容旧版）
	var wecomConfig *wecom.Config
	var wecomClient *wecom.Client // 兜底 cron 用
	wecomRouter := wecom.NewRouter()
	corpID := os.Getenv("WECOM_CORP_ID")
	if corpID != "" {
		agentIDStr := os.Getenv("WECOM_AGENT_ID")
		agentID, _ := strconv.Atoi(agentIDStr)
		wecomConfig = wecom.LoadConfigFromValues(
			corpID,
			os.Getenv("WECOM_SECRET"),
			os.Getenv("WECOM_TOKEN"),
			os.Getenv("WECOM_ENCODING_AES_KEY"),
			agentID,
		)
		wecomClient = wecom.NewClient(corpID, os.Getenv("WECOM_SECRET"), agentID)
		// router 兜底（旧部署兼容：单 corpID 进 router 当 default 店）
		_ = wecomRouter.SetFallback(wecom.Fallback{
			CorpID:         corpID,
			Token:          os.Getenv("WECOM_TOKEN"),
			EncodingAESKey: os.Getenv("WECOM_ENCODING_AES_KEY"),
			AgentID:        agentID,
			Secret:         os.Getenv("WECOM_SECRET"),
		})
		log.Printf("企业微信配置已加载: corpID=%s agentID=%d (router 已就位)", corpID, agentID)
	}

	// 多店：从 shops 表加载所有有 wecom 凭据的店铺进 router
	if err := wecomRouter.ReloadFromDB(); err != nil {
		log.Printf("⚠️  从 DB 加载 shops 到 router 失败: %v", err)
	} else {
		log.Printf("[wecom] router 已加载 %d 个店铺", wecomRouter.Count())
	}

	// 启动 cron（PRD §11.1 P0 预约前 2h 提醒 + §11.2 P1 爽约扫描 + 续费漏斗）
	if wecomClient != nil {
		reminder := cronpkg.NewReminder(wecomClient)
		if err := reminder.Start(ctx); err != nil {
			log.Printf("⚠️  启动 reminder cron 失败: %v", err)
		} else {
			defer func() {
				stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = reminder.Stop(stopCtx)
			}()
		}

		noshow := cronpkg.NewNoShowScanner(wecomClient)
		if err := noshow.Start(ctx); err != nil {
			log.Printf("⚠️  启动 noshow cron 失败: %v", err)
		} else {
			defer func() {
				stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = noshow.Stop(stopCtx)
			}()
		}

		lifecycle := cronpkg.NewLifecycleTrigger(wecomClient)

		// v4.2 PRD §11.11：D+15 使用报告邮件
		//   - SMTP 未配置时 sender = NoopSender（只 log 不发）
		//   - REPORT_TO 配置后 D+15 触发时同时发邮件 + 微信
		emailCfg := notify.LoadEmailConfigFromEnv()
		lifecycle.SetSender(notify.NewSender(emailCfg))

		// D+15 邮件收件人（逗号分隔）；空时只发微信
		reportTo := parseRecipients(os.Getenv("REPORT_TO"))
		if len(reportTo) > 0 {
			lifecycle.SetReportTo(reportTo)
			log.Printf("[lifecycle] D+15 报告邮件已启用，收件人 %d 人：%v", len(reportTo), reportTo)
		} else {
			log.Printf("[lifecycle] REPORT_TO 未配置，D+15 报告邮件跳过（仅发微信）")
		}

		if err := lifecycle.Start(ctx); err != nil {
			log.Printf("⚠️  启动 lifecycle cron 失败: %v", err)
		} else {
			defer func() {
				stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = lifecycle.Stop(stopCtx)
			}()
		}

		// 订阅到期通知（PRD #9）
		subNotifier := cronpkg.NewSubscriptionNotifier(wecomClient)
		if err := subNotifier.Start(ctx); err != nil {
			log.Printf("⚠️  启动 subscription cron 失败: %v", err)
		} else {
			defer func() {
				stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = subNotifier.Stop(stopCtx)
			}()
		}
		// 空闲时段主动推送 cron（PRD §11.3 P2 旗舰版：理发师空闲时段主动推送休眠客）
		idlePusher := cronpkg.NewIdleSlotPusher(wecomClient)
		if err := idlePusher.Start(ctx); err != nil {
			log.Printf("⚠️  启动 idle pusher cron 失败: %v", err)
		} else {
			defer func() {
				stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = idlePusher.Stop(stopCtx)
			}()
		}

		// 理发师请假过期扫描（PRD §11.7 P4 兜底：end_at < now 的 active leave 自动 expired）
		// 不依赖 wecom 客户端，所以单独 if 一层
		leaveExpirer := cronpkg.NewLeaveExpirer()
		if err := leaveExpirer.Start(ctx); err != nil {
			log.Printf("⚠️  启动 leave expirer cron 失败: %v", err)
		} else {
			defer func() {
				stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = leaveExpirer.Stop(stopCtx)
			}()
		}

		// v4.13.1：kf_seen_msg TTL 清理（每天 3:00 删 7 天前的去重记录）
		// 不依赖 wecom 客户端，独立启动
		kfSeenCleaner := cronpkg.NewKfSeenMsgCleaner()
		if err := kfSeenCleaner.Start(ctx); err != nil {
			log.Printf("⚠️  启动 kf_seen_msg cleanup cron 失败: %v", err)
		} else {
			defer func() {
				stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = kfSeenCleaner.Stop(stopCtx)
			}()
		}

		// 周报触发器（PRD §8.2 / §11.12 v4.3 + v4.5 跨店增量 — 每周一 9:00）
		//   - 复用同一份 SMTP 配（与 D+15 共用 sender）
		//   - 收件人两路独立：
		//     - REPORT_TO：单店逐店周报（与 D+15 共用）
		//     - CHAIN_REPORT_TO：跨店汇总周报（连锁 owner 视角，v4.5 增量）
		//   - 不依赖 wecom 客户端，单独 if 一层
		weeklyReporter := cronpkg.NewWeeklyReporter()
		weeklyReporter.SetSender(notify.NewSender(emailCfg))
		if len(reportTo) > 0 {
			weeklyReporter.SetReportTo(reportTo)
			log.Printf("[weekly] 单店周报邮件已启用，收件人 %d 人：%v", len(reportTo), reportTo)
		} else {
			log.Printf("[weekly] REPORT_TO 未配置，单店周报邮件跳过（仅写埋点）")
		}
		if chainReportTo := parseRecipients(os.Getenv("CHAIN_REPORT_TO")); len(chainReportTo) > 0 {
			weeklyReporter.SetChainReportTo(chainReportTo)
			log.Printf("[weekly] 跨店汇总周报邮件已启用，收件人 %d 人：%v", len(chainReportTo), chainReportTo)
		} else {
			log.Printf("[weekly] CHAIN_REPORT_TO 未配置，跨店周报邮件跳过（仅写埋点）")
		}
		if err := weeklyReporter.Start(ctx); err != nil {
			log.Printf("⚠️  启动 weekly reporter cron 失败: %v", err)
		} else {
			defer func() {
				stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = weeklyReporter.Stop(stopCtx)
			}()
		}
	}

	srv := server.New[M](server.Config[M]{
		Agent:           agent,
		ChatModel:       cm,
		CheckPointStore: checkpointStore,
		Store:           store,
		WorkspaceDir:    workspaceDir,
		ProjectRoot:     projectRoot,
		ExamplesDir:     examplesDir,
		Port:            port,
		WeComConfig:     wecomConfig,
		WeComRouter:     wecomRouter,
	})

	// v4.13.1：AGENT_REPLY_MODE 环境变量控制 sendReply 是否打真实企业微信
	//   - 真实发送（默认）：AGENT_REPLY_MODE 不设 或 =real
	//   - 跳过企业微信：   AGENT_REPLY_MODE=mock（demo 兜底 / 调试用，写 event_logs）
	server.SetReplyMode(os.Getenv("AGENT_REPLY_MODE"))

	// 注册商户后台 + API 路由（PRD §11.2）
	api.RegisterRoutes(srv.EnsureHertz(), api.AdminConfig{
		LegacyToken: os.Getenv("ADMIN_TOKEN"), // 兼容旧版；非空时作为 fallback

		// P4 理发师请假通知：构造一个 sender 把 (appt, text) 映射到企业微信发送
		//
		// v4.10：传 router + fallbackClient，sender 内部按 shopID 多店路由 + 通道降级 + 3 次重试
		// 缺失/未配置 wecom 时 sender = nil，leave row 仍会写，顾客不会收到微信通知（管理员需另行沟通）。
		NotifSender: buildLeaveNotificationSender(wecomRouter, wecomClient),
	})

	log.Printf("starting server on http://localhost:%s", port)
	log.Printf("商户后台: http://localhost:%s/admin (默认 admin/admin123，首次登录后请改密码)", port)
	srv.Spin()
}

// envInt returns os.Getenv(key) parsed as int, or fallback if unset / invalid.
func envInt(key string, fallback int) int {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		log.Printf("[env] %s=%q invalid, using fallback %d", key, v, fallback)
		return fallback
	}
	return n
}

// parseRecipients 解析 REPORT_TO 字符串（逗号分隔）成收件人列表
//
//   - 去掉每个地址的空白
//   - 过滤空串
//   - 不做邮箱格式校验（让 SMTP 失败时报错）
func parseRecipients(raw string) []string {
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// buildLeaveNotificationSender 构造请假通知发送器（P4，v4.10 多店 + 重试 + 通道降级）
//
// 历史 bug 链：
//   - v4.9.3 修：按 external_user_id / wechat_open_id 自动选 SendKfTextMessage / SendTextMessage（修 81013）
//   - v4.10 修：多店路由（之前全平台用 DefaultOpenKfID，A 店顾客发到 B 店 KF 会 93001900）
//   - v4.10 修：按 shopID 反查 client + openKfID
//   - v4.10 修：ChannelSelector 选通道，phone-only 顾客走 skipped 而非报 err
//   - v4.10 修：SendWithRetry 3 次指数退避，单次抖动不丢消息
//   - v4.10 修：返回 storage.ErrNoCustomerContact 让 storage 写 skipped row
//
// 行为：
//   - router 和 fallbackClient 都 nil：返回 nil（通知能力缺失，但不影响 leave row 写入）
//   - customerID 空 / 无联系方式：返回 storage.ErrNoCustomerContact
//   - 顾客所在 shop 未注册到 router 且 fallbackClient 也是 nil：返回 error
//   - 失败：sender 闭包返回 error，storage 写 failed row + 计数 attempt
//
// 注意：这里直接用 storage.DB 是为了保持简单；不抽 repo 是不想为这个一次性通知路径多写一层。
func buildLeaveNotificationSender(router *wecom.Router, fallbackClient *wecom.Client) storage.LeaveNotificationSender {
	if router == nil && fallbackClient == nil {
		return nil
	}
	return func(ctx context.Context, appt *storage.Appointment, text string) error {
		if appt.CustomerID == "" {
			return storage.ErrNoCustomerContact
		}

		// 1) 查 customer（拿联系方式）
		var cust storage.Customer
		if err := storage.DB.WithContext(ctx).Where("id = ?", appt.CustomerID).First(&cust).Error; err != nil {
			return fmt.Errorf("顾客 %s 不存在: %w", appt.CustomerID, err)
		}

		// 2) ChannelSelector 选通道
		decision := storage.SelectChannel(&cust)
		if !decision.HasContact {
			return storage.ErrNoCustomerContact
		}

		// 3) 按 shopID 找 client + openKfID（多店路由核心）
		client, openKfID, err := resolveWecomForShop(ctx, router, fallbackClient, appt.ShopID, decision.Channel)
		if err != nil {
			return err
		}

		// 4) 包 sender（带 SendWithRetry）
		var sender storage.WeComSender
		switch decision.Channel {
		case storage.NotifChannelWeComKF:
			kfID := openKfID
			if kfID == "" {
				// shop 没配 openKfID → fallback 到全局默认（仅单店部署兼容）
				kfID = wecom.DefaultOpenKfID
				log.Printf("[leave] 店铺 %s 未配置 open_kf_id，临时使用 DefaultOpenKfID（多店场景下应填正确的）", appt.ShopID)
			}
			sender = storage.WeComSenderFunc(func(ctx context.Context, target, content string) error {
				return client.SendKfTextMessage(ctx, target, kfID, content)
			})
		case storage.NotifChannelWeComApp:
			sender = storage.WeComSenderFunc(func(ctx context.Context, target, content string) error {
				return client.SendTextMessage(ctx, target, content)
			})
		case storage.NotifChannelSMS:
			// SMS 暂未实现（预留接口）；phone-only 顾客走 skipped（由 caller 处理）
			return fmt.Errorf("SMS 通道未实现（顾客 %s）", appt.CustomerID)
		default:
			return fmt.Errorf("未知通道 %q", decision.Channel)
		}

		// 5) 重试 + 发送
		return storage.SendWithRetry(ctx, sender, decision.Target, text, storage.SendOptions{
			MaxAttempts:    3,
			InitialBackoff: 200 * time.Millisecond,
			MaxBackoff:     1 * time.Second,
			Context:        ctx,
		})
	}
}

// resolveWecomForShop 按 shopID 找对应的 wecom client + openKfID（多店路由）
//
// 查找顺序：
//   1. router.LookupByShopID(shopID) → 找到就用该 client
//   2. 没找到 → fallback 到 fallbackClient（兼容单店 .env 部署）
//   3. 都 nil → 返回 error
//
// openKfID 从 storage.Shop.OpenKfID 字段查（多店场景下每个店铺独立配置）
func resolveWecomForShop(ctx context.Context, router *wecom.Router, fallbackClient *wecom.Client, shopID, channel string) (*wecom.Client, string, error) {
	var client *wecom.Client

	// 1) router 查
	if router != nil {
		if sc, ok := router.LookupByShopID(shopID); ok && sc != nil && sc.Client != nil {
			client = sc.Client
		}
	}

	// 2) fallback 兼容
	if client == nil {
		client = fallbackClient
	}

	if client == nil {
		return nil, "", fmt.Errorf("店铺 %s 未配置 wecom client（既不在 router 也无 fallback）", shopID)
	}

	// 3) openKfID 仅 KF 通道需要
	if channel != storage.NotifChannelWeComKF {
		return client, "", nil
	}

	// 从 shop 表读 openKfID
	var openKfID string
	if storage.DB != nil && shopID != "" {
		var shop storage.Shop
		if err := storage.DB.WithContext(ctx).Select("open_kf_id").Where("id = ?", shopID).First(&shop).Error; err == nil {
			openKfID = shop.OpenKfID
		}
		// 查不到不报错——可能 shop 行不存在；fallback 由 caller 处理
	}
	return client, openKfID, nil
}
