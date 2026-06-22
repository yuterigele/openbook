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
	"github.com/yuterigele/openbook/api"
	"github.com/yuterigele/openbook/chatmodel"
	cronpkg "github.com/yuterigele/openbook/cron"
	lockpkg "github.com/yuterigele/openbook/lock"
	"github.com/yuterigele/openbook/mem"
	"github.com/yuterigele/openbook/msgops"
	"github.com/yuterigele/openbook/notify"
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
	cm, err := chatmodel.NewModel[M](ctx)
	if err != nil {
		log.Fatalf("failed to create chat model: %v", err)
	}

	agent, err := buildAgentTyped[M](ctx)
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

	// 注册商户后台 + API 路由（PRD §11.2）
	api.RegisterRoutes(srv.EnsureHertz(), api.AdminConfig{
		LegacyToken: os.Getenv("ADMIN_TOKEN"), // 兼容旧版；非空时作为 fallback

		// P4 理发师请假通知：构造一个 sender 把 (customerID, text) 映射到企业微信发送
		//
		// 缺失/未配置 wecom 时 sender = nil，leave row 仍会写，顾客不会收到微信通知（管理员需另行沟通）。
		NotifSender: buildLeaveNotificationSender(wecomClient),
	})

	log.Printf("starting server on http://localhost:%s", port)
	log.Printf("商户后台: http://localhost:%s/admin (默认 admin/admin123，首次登录后请改密码)", port)
	srv.Spin()
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

// buildLeaveNotificationSender 构造请假通知发送器（P4）
//
// 行为：
//   - wecomClient == nil：返回 nil（通知能力缺失，但不影响 leave row 写入）
//   - customerID 为空：no-op
//   - 查 customer 表取 external_userid / wechat_open_id（优先 external）→ 调 wecomClient.SendTextMessage
//   - 失败只 log，不返回 error 给上层（避免一个顾客失败影响整个 leave）
//
// 注意：这里直接用 storage.DB 是为了保持简单；不抽 repo 是不想为这个一次性通知路径多写一层。
func buildLeaveNotificationSender(wecomClient *wecom.Client) func(ctx context.Context, customerID, text string) error {
	if wecomClient == nil {
		return nil
	}
	return func(ctx context.Context, customerID, text string) error {
		if customerID == "" {
			return nil
		}
		var cust storage.Customer
		if err := storage.DB.WithContext(ctx).Where("id = ?", customerID).First(&cust).Error; err != nil {
			return fmt.Errorf("顾客 %s 不存在: %w", customerID, err)
		}
		target := cust.ExternalUserID
		if target == "" {
			target = cust.WechatOpenID
		}
		if target == "" {
			return fmt.Errorf("顾客 %s 无 external_userid / wechat_open_id", customerID)
		}
		return wecomClient.SendTextMessage(ctx, target, text)
	}
}
