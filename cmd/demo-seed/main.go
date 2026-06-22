// demo-seed 工具
//
// 用法：
//   go run ./cmd/demo-seed                # 跑全套 demo 数据
//   go run ./cmd/demo-seed -shop-only     # 只建店 + 师傅
//   go run ./cmd/demo-seed -clean         # 清掉 demo 数据（保留 default 店）
//
// 设计原则：
//   - idempotent：重复跑会跳过已存在的（用 wecom_corp_id / barber.name 去重）
//   - 不影响生产：建 3-5 家 demo 店，店名前缀 [DEMO] 显眼
//   - 覆盖典型场景：VIP/常客/黑名单/新客 + 各种预约状态 + 请假 + 转人工
//
// 用途：
//   - 自己本地 demo（打开商户后台就有真实数据看）
//   - 给种子用户试用（不需从零开始）
//   - 端到端测试 / 演示文档截图

package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"time"

	"github.com/cloudwego/eino-examples/quickstart/chatwitheino/storage"
)

func main() {
	ctx := context.Background()

	// 解析参数
	shopOnly := flag.Bool("shop-only", false, "只建店 + 师傅 + 服务目录")
	clean := flag.Bool("clean", false, "清掉所有 [DEMO] 前缀的店")
	skipAppts := flag.Bool("skip-appointments", false, "跳过建预约（用来做小数据调试）")
	flag.Parse()

	// 初始化 DB（连真实 MySQL）
	if _, err := storage.InitDB(ctx); err != nil {
		log.Fatalf("InitDB 失败: %v", err)
	}

	if *clean {
		n, err := storage.CleanDemoShops(ctx)
		if err != nil {
			log.Fatalf("clean failed: %v", err)
		}
		fmt.Printf("✅ 清理了 %d 家 [DEMO] 店\n", n)
		return
	}

	fmt.Println("开始生成 demo 数据...")
	stats, err := storage.SeedDemoData(ctx, storage.DemoSeedOptions{
		ShopOnly:         *shopOnly,
		SkipAppointments: *skipAppts,
	})
	if err != nil {
		log.Fatalf("seed failed: %v", err)
	}

	fmt.Println("\n✅ Demo 数据生成完成：")
	fmt.Printf("  店铺:    %d 家（[DEMO] 前缀）\n", stats.Shops)
	fmt.Printf("  师傅:    %d 位\n", stats.Barbers)
	fmt.Printf("  顾客:    %d 位（含 VIP/常客/黑名单/新客）\n", stats.Customers)
	fmt.Printf("  预约:    %d 条（过去 4 周 + 未来 2 周）\n", stats.Appointments)
	fmt.Printf("  请假:    %d 条\n", stats.Leaves)
	fmt.Printf("  事件:    %d 条\n", stats.Events)
	fmt.Printf("  订阅:    %d 条（覆盖 3 个套餐）\n", stats.Subscriptions)
	fmt.Printf("\n  登录:   http://localhost:38080/admin\n")
	fmt.Printf("  用户名: 任意 [DEMO] 店 owner（看 shops 表）\n")
	fmt.Printf("  密码:   admin123\n")
	_ = time.Now // 时间戳占位
}
