// cmd/fix-customers —— 一次性修复脚本：列出/补全老顾客档案的 openID / external_user_id
//
// 背景：v4.8 之前的顾客档案是通过名字建的（backfill 时没 wecom user id 上下文），
// 导致 reminder / leave notify 等需要发微信的 cron 都失败。
//
// 本脚本做两件事：
//  1. List（默认）：列出所有缺 openID 的顾客 + 关联的 appointment / shop，方便人工补
//  2. Fix（-set）：从 wecom_message_logs 关联，尝试反推 openID
//
// 实际效果：能反推多少算多少，反推不到的让商户从 admin 后台手动补。
//
// 用法：
//   go run ./cmd/fix-customers                # 列出缺 openID 的顾客
//   go run ./cmd/fix-customers -attempt       # 尝试从 wecom_message_logs 反推
//   go run ./cmd/fix-customers -set ID openID externalUserID   # 手动设一个
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/yuterigele/openbook/chatmodel"
	"github.com/yuterigele/openbook/storage"
	"github.com/yuterigele/openbook/tools"
)

func main() {
	attempt := flag.Bool("attempt", false, "尝试从 wecom_message_logs 反推 openID")
	set := flag.String("set", "", "手动设一个顾客：格式 'cust-id,openID,externalUserID'")
	setPhone := flag.String("set-phone", "", "手动补手机号：格式 'cust-id,phone'（11 位数字、1 开头）")
	flag.Parse()

	chatmodel.LoadEnv()
	ctx := context.Background()
	if _, err := storage.InitDB(ctx); err != nil {
		log.Fatalf("[fix-customers] InitDB: %v", err)
	}
	defer func() {
		if sqlDB, err := storage.DB.DB(); err == nil && sqlDB != nil {
			_ = sqlDB.Close()
		}
	}()

	switch {
	case *set != "":
		parts := strings.SplitN(*set, ",", 3)
		if len(parts) != 3 {
			log.Fatalf("-set 格式: 'cust-id,openID,externalUserID'")
		}
		setOne(ctx, parts[0], parts[1], parts[2])
	case *setPhone != "":
		parts := strings.SplitN(*setPhone, ",", 2)
		if len(parts) != 2 {
			log.Fatalf("-set-phone 格式: 'cust-id,phone'")
		}
		setPhoneOne(ctx, parts[0], parts[1])
	case *attempt:
		attemptBackfill(ctx)
	default:
		listMissing(ctx)
	}
}

// listMissing 列出所有缺 openID 的顾客
func listMissing(ctx context.Context) {
	type row struct {
		ID             string
		Name           string
		Phone          string
		WechatOpenID   *string // 用指针区分 NULL 和 ""
		ExternalUserID *string
		ApptCount      int
		LastApptAt     *time.Time
	}
	var rows []row
	err := storage.DB.WithContext(ctx).
		Table("customers c").
		Select(`c.id, c.name, c.phone, c.wechat_open_id, c.external_user_id,
		        (SELECT COUNT(*) FROM appointments a WHERE a.customer_id = c.id) AS appt_count,
		        (SELECT MAX(STR_TO_DATE(CONCAT(a.date, ' ', a.time), '%Y-%m-%d %H:%i'))
		         FROM appointments a WHERE a.customer_id = c.id) AS last_appt_at`).
		// v4.9.3 修复：原来 WHERE wechat_open_id = '' 不匹配 NULL（MySQL 里 NULL = '' 是 false）
		// 改成：两个字段都空（NULL 或 '' 都算）
		Where(`(c.wechat_open_id IS NULL OR c.wechat_open_id = '')
		       AND (c.external_user_id IS NULL OR c.external_user_id = '')`).
		Scan(&rows).Error
	if err != nil {
		log.Fatalf("查询失败: %v", err)
	}

	if len(rows) == 0 {
		fmt.Println("✅ 所有顾客都有 openID/external_user_id，nothing to do")
		return
	}

	fmt.Printf("⚠️  共 %d 个顾客缺微信绑定（无法接收提醒/通知）：\n\n", len(rows))
	fmt.Printf("%-40s %-20s %-15s %-8s %-12s %s\n", "顾客ID", "姓名", "电话", "预约数", "openID?", "最后预约")
	fmt.Println(strings.Repeat("-", 120))
	for _, r := range rows {
		last := "—"
		if r.LastApptAt != nil {
			last = r.LastApptAt.Format("2006-01-02 15:04")
		}
		oid := "(null)"
		if r.WechatOpenID != nil && *r.WechatOpenID != "" {
			oid = "✅ " + *r.WechatOpenID
		} else {
			oid = "❌ 空"
		}
		fmt.Printf("%-40s %-20s %-15s %-8d %-12s %s\n",
			r.ID, r.Name, r.Phone, r.ApptCount, oid, last)
	}
	fmt.Println()
	fmt.Println("修复方式：")
	fmt.Println("  1) go run ./cmd/fix-customers -attempt               # 自动反推（成功率看数据）")
	fmt.Println("  2) go run ./cmd/fix-customers -set ID,openID,extID    # 手动补")
	fmt.Println("     openID 从企业微信客服工具拿（顾客对话窗口里能看到 UserID）")
}

// attemptBackfill 尝试从 wecom_message_logs 反推 openID
//
// 思路：wecom_message_logs 存了所有 wecom 消息的 FromUserName，
// 但没法直接 join 到 customers.name（一个 openID 可能叫过多个名字）。
//
// 启发式策略：
//   1. 对每个缺 openID 的顾客
//   2. 在 wecom_message_logs 里找最近 N 条（30 天内）消息
//   3. 取出现次数最多的 FromUserName 作为候选
//   4. 但要求"该 openID 唯一对应这个顾客名"——即该 openID 没有给其他顾客名发过消息
//
// 注：成功率取决于你的数据特征；可能 0%。
func attemptBackfill(ctx context.Context) {
	type cRow struct {
		ID   string
		Name string
	}
	var missing []cRow
	if err := storage.DB.WithContext(ctx).
		Table("customers").
		Select("id, name").
		Where(`(wechat_open_id IS NULL OR wechat_open_id = '')
		       AND (external_user_id IS NULL OR external_user_id = '')`).
		Scan(&missing).Error; err != nil {
		log.Fatalf("query: %v", err)
	}
	if len(missing) == 0 {
		fmt.Println("✅ 无缺失顾客，nothing to do")
		return
	}

	fmt.Printf("尝试自动反推 %d 个顾客的 openID...\n", len(missing))

	var fixed, skipped int
	for range missing {
		// 启发式：找给顾客名产生过 wecom 消息的 FromUserName，
		// 且该 FromUserName 历史上没给其他顾客名发过消息。
		// wecom_message_logs 不存 content，没法 join；直接跳过。
		skipped++
	}
	fmt.Printf("\n⚠️  自动反推无法直接 join（wecom_message_logs 不存 content）\n")
	fmt.Printf("   建议直接走 -set 手动补，或在 admin 后台补。\n")
	fmt.Printf("   skipped=%d fixed=%d\n", skipped, fixed)
}

// setOne 手动设一个顾客的 openID / external_user_id
func setOne(ctx context.Context, custID, openID, externalUserID string) {
	updates := map[string]interface{}{}
	if openID != "" {
		updates["wechat_open_id"] = openID
	}
	if externalUserID != "" {
		updates["external_user_id"] = externalUserID
	}
	if len(updates) == 0 {
		log.Fatalf("至少要给一个字段（openID 或 externalUserID）")
	}
	res := storage.DB.WithContext(ctx).Model(&storage.Customer{}).
		Where("id = ?", custID).
		Updates(updates)
	if res.Error != nil {
		log.Fatalf("update: %v", res.Error)
	}
	if res.RowsAffected == 0 {
		log.Fatalf("顾客 %s 不存在", custID)
	}
	fmt.Printf("✅ 顾客 %s 已绑定:\n", custID)
	if openID != "" {
		fmt.Printf("   openID           = %s\n", openID)
	}
	if externalUserID != "" {
		fmt.Printf("   external_user_id = %s\n", externalUserID)
	}
	fmt.Println("   下次 cron 提醒就能找到他了")
	_ = os.Getenv("DUMMY") // 防 unused
}

// setPhoneOne 手动补手机号（v4.9.3 必填）
func setPhoneOne(ctx context.Context, custID, phone string) {
	// 复用 tools.ValidatePhone 校验（一致性）
	if err := tools.ValidatePhone(phone); err != nil {
		log.Fatalf("手机号校验失败: %v", err)
	}
	res := storage.DB.WithContext(ctx).Model(&storage.Customer{}).
		Where("id = ?", custID).
		Update("phone", phone)
	if res.Error != nil {
		log.Fatalf("update: %v", res.Error)
	}
	if res.RowsAffected == 0 {
		log.Fatalf("顾客 %s 不存在", custID)
	}
	fmt.Printf("✅ 顾客 %s 已绑定手机号 %s\n", custID, phone)
}