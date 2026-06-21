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
	"time"

	clc "github.com/cloudwego/eino-ext/callbacks/cozeloop"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/schema"
	"github.com/coze-dev/cozeloop-go"

	adkstore "github.com/cloudwego/eino-examples/adk/common/store"
	"github.com/cloudwego/eino-examples/quickstart/chatwitheino/api"
	"github.com/cloudwego/eino-examples/quickstart/chatwitheino/chatmodel"
	cronpkg "github.com/cloudwego/eino-examples/quickstart/chatwitheino/cron"
	lockpkg "github.com/cloudwego/eino-examples/quickstart/chatwitheino/lock"
	"github.com/cloudwego/eino-examples/quickstart/chatwitheino/mem"
	"github.com/cloudwego/eino-examples/quickstart/chatwitheino/msgops"
	"github.com/cloudwego/eino-examples/quickstart/chatwitheino/server"
	"github.com/cloudwego/eino-examples/quickstart/chatwitheino/storage"
	"github.com/cloudwego/eino-examples/quickstart/chatwitheino/wecom"
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
