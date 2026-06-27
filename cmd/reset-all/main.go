// cmd/reset-all —— 一键重置 openbook 数据（v4.16.4）
//
// 用法：
//
//	# 全清（MySQL + Redis + wecom cursor + 商户账号）
//	go run ./cmd/reset-all -mode full -yes
//	go run ./cmd/reset-all -mode full -yes -backup-dir /backup
//
//	# 只清 Agent 相关（保留业务数据：顾客/预约/卡）
//	go run ./cmd/reset-all -mode agent -yes
//
//	# 清单个店（保留其他店）
//	go run ./cmd/reset-all -mode shop -shop-id default -yes
//
// 行为：
//  1. 强制先备份（mysqldump → backupDir/reset-all-YYYYMMDD-HHmm.sql），备份失败拒绝继续
//  2. 默认 dry-run，只打印计划；要真执行必须带 -yes
//  3. 三次确认（避免误操作）
//  4. 生产 MySQL（hostname 含 prod/production/aliyun/yuyuanyuan.cn）必须加 -force-prod
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"

	"github.com/yuterigele/openbook/chatmodel"
	"github.com/yuterigele/openbook/storage"
)

// realTables —— db.go AutoMigrate 的 19 张表，按依赖反序（先子后父）
var realTables = []string{
	"customer_notifications",
	"role_permissions",
	"card_transactions",
	"customer_cards",
	"cards",
	"appointments",
	"barber_leaves",
	"services",
	"api_keys",
	"event_logs",
	"reminder_logs",
	"subscriptions",
	"wecom_message_logs",
	"kf_seen_msgs",
	"kf_sync_states",
	"customers",
	"barbers",
	"shop_admins",
	"shops",
}

// agentOnlyTables —— 只清 wecom 客服消息体系（保留业务数据）
var agentOnlyTables = []string{
	"wecom_message_logs",
	"kf_seen_msgs",
	"kf_sync_states",
}

// shopScopedTables —— 按 shop_id 删的表（不带 shop_id 的不进）
//
// role_permissions 是全局 RBAC 映射（不带 shop_id），shop 模式不动
// shops 是父表，最后单独 DELETE WHERE id = ?
var shopScopedTables = []string{
	"customer_notifications",
	"card_transactions",
	"customer_cards",
	"cards",
	"appointments",
	"barber_leaves",
	"services",
	"api_keys",
	"event_logs",
	"reminder_logs",
	"subscriptions",
	"wecom_message_logs",
	"kf_seen_msgs",
	"kf_sync_states",
	"customers",
	"barbers",
	"shop_admins",
}

func main() {
	mode := flag.String("mode", "", "重置范围：full | agent | shop（必填）")
	shopID := flag.String("shop-id", "", "shop 模式：要清的店铺 ID")
	yes := flag.Bool("yes", false, "确认执行（默认 dry-run）")
	forceProd := flag.Bool("force-prod", false, "允许在生产环境执行")
	backupDir := flag.String("backup-dir", "./backups", "备份目录")
	skipBackup := flag.Bool("skip-backup", false, "跳过备份（强烈不推荐）")
	flag.Parse()

	if *mode == "" {
		log.Fatal("必须指定 -mode（full / agent / shop）")
	}
	if *mode != "full" && *mode != "agent" && *mode != "shop" {
		log.Fatalf("-mode 必须是 full / agent / shop，当前：%s", *mode)
	}
	if *mode == "shop" && *shopID == "" {
		log.Fatal("shop 模式必须指定 -shop-id")
	}

	chatmodel.LoadEnv()
	ctx := context.Background()

	// 初始化 MySQL
	if _, err := storage.InitDB(ctx); err != nil {
		log.Fatalf("初始化 MySQL 失败: %v", err)
	}

	// 初始化 Redis（不强制）
	rdb, redisErr := initRedisClient(ctx)
	if redisErr != nil {
		log.Printf("⚠️  Redis 不可用（%v）—— 仅清 MySQL", redisErr)
	}

	// 生产环境检测
	prodDB := detectProd()
	if prodDB && !*forceProd {
		log.Fatalf("❌ 检测到生产 MySQL DSN，必须加 -force-prod 才允许执行")
	}

	// 打印计划
	fmt.Println("═══════════════════════════════════════════════════════════")
	fmt.Printf("🔧 重置模式: %s\n", *mode)
	if *mode == "shop" {
		fmt.Printf("   目标店铺: %s\n", *shopID)
	}
	if prodDB {
		fmt.Println("   ⚠️  生产环境（已通过 -force-prod）")
	}
	fmt.Printf("   Redis: %s\n", redisState(redisErr))
	fmt.Printf("   备份: %s\n", boolStr(*skipBackup, "跳过（危险）", *backupDir))
	fmt.Println("═══════════════════════════════════════════════════════════")

	switch *mode {
	case "full":
		fmt.Printf("\n将清空 %d 张表：\n", len(realTables))
		for _, t := range realTables {
			fmt.Printf("  • %s\n", t)
		}
	case "agent":
		fmt.Printf("\n将清空 %d 张 agent 相关表（业务数据保留）：\n", len(agentOnlyTables))
		for _, t := range agentOnlyTables {
			fmt.Printf("  • %s\n", t)
		}
	case "shop":
		fmt.Printf("\n将按 shop_id=%s 清 %d 张表：\n", *shopID, len(shopScopedTables))
		for _, t := range shopScopedTables {
			fmt.Printf("  • %s\n", t)
		}
		fmt.Printf("  • shops（DELETE WHERE id='%s'）\n", *shopID)
		fmt.Println("  注：role_permissions 是全局 RBAC 映射，shop 模式不动")
	}

	if rdb != nil {
		fmt.Println("\nRedis 将清的模式：")
		for _, p := range redisPatternsForMode(*mode, *shopID) {
			fmt.Printf("  • %s\n", p)
		}
		if *mode == "full" {
			fmt.Println("  • (full 模式额外问：是否 FLUSHDB 清整个 Redis DB)")
		}
	}

	// dry-run 早退
	if !*yes {
		fmt.Println()
		fmt.Println("═══════════════════════════════════════════════════════════")
		fmt.Println("🟡 DRY-RUN：未执行任何删除")
		fmt.Println("   加 -yes 才真执行")
		fmt.Println("═══════════════════════════════════════════════════════════")
		return
	}

	// 三次确认（防误操作）
	if !confirm(fmt.Sprintf("⚠️  真的要执行 %s 重置吗？", strings.ToUpper(*mode))) {
		fmt.Println("已取消。")
		return
	}
	if !confirm("⚠️  再确认一次：以上数据将被永久删除（备份后才能找回）") {
		fmt.Println("已取消。")
		return
	}
	if !confirm(fmt.Sprintf("⚠️  最后确认：执行 %s 重置？", strings.ToUpper(*mode))) {
		fmt.Println("已取消。")
		return
	}

	// 1. 备份
	if !*skipBackup {
		backupPath, err := backupMySQL(*backupDir)
		if err != nil {
			log.Fatalf("❌ 备份失败（拒绝继续，避免清完没备份）：%v", err)
		}
		fmt.Printf("✅ 备份完成: %s\n", backupPath)
	} else {
		fmt.Println("⚠️  跳过备份（-skip-backup）")
	}

	// 2. 清 MySQL
	if err := resetMySQL(ctx, *mode, *shopID); err != nil {
		log.Fatalf("❌ MySQL 重置失败: %v", err)
	}
	fmt.Println("✅ MySQL 重置完成")

	// 3. 清 Redis
	if rdb != nil {
		if err := resetRedis(ctx, rdb, *mode, *shopID); err != nil {
			log.Fatalf("❌ Redis 重置失败: %v", err)
		}
		fmt.Println("✅ Redis 重置完成")
	}

	fmt.Println()
	fmt.Println("═══════════════════════════════════════════════════════════")
	fmt.Printf("✅ %s 重置完成\n", strings.ToUpper(*mode))
	if *mode == "full" {
		fmt.Println("   重启 chatwitheino 服务会自动 AutoMigrate + 重建默认 admin 账号")
		fmt.Println("   （admin / .env 里的 DEFAULT_ADMIN_PASSWORD）")
	}
	fmt.Println("═══════════════════════════════════════════════════════════")
}

// confirm 读 stdin 一行，匹配 yes/y 返回 true
func confirm(msg string) bool {
	fmt.Printf("%s [yes/no]: ", msg)
	reader := bufio.NewReader(os.Stdin)
	line, _ := reader.ReadString('\n')
	line = strings.TrimSpace(strings.ToLower(line))
	return line == "yes" || line == "y"
}

// detectProd 检测 MySQL 是不是生产（基于 hostname）
func detectProd() bool {
	sqlDB, err := storage.DB.DB()
	if err != nil {
		return false
	}
	var hostname string
	if err := sqlDB.QueryRow("SELECT @@hostname").Scan(&hostname); err != nil {
		return false
	}
	lower := strings.ToLower(hostname)
	return strings.Contains(lower, "prod") ||
		strings.Contains(lower, "production") ||
		strings.Contains(lower, "aliyun") ||
		strings.Contains(lower, "yuyuanyuan") ||
		strings.Contains(lower, "agent.yuyuanyuan.cn")
}

func boolStr(skip bool, skipMsg, normalMsg string) string {
	if skip {
		return skipMsg
	}
	return normalMsg
}

func redisState(err error) string {
	if err != nil {
		return "不可用（已跳过 Redis 清理）"
	}
	return "可用（会清理匹配 key）"
}

// initRedisClient 连接 Redis（不强制）
func initRedisClient(ctx context.Context) (*redis.Client, error) {
	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		addr = "127.0.0.1:6379"
	}
	password := os.Getenv("REDIS_PASSWORD")
	db := 0
	if d := os.Getenv("REDIS_DB"); d != "" {
		fmt.Sscanf(d, "%d", &db)
	}

	rdb := redis.NewClient(&redis.Options{
		Addr:        addr,
		Password:    password,
		DB:          db,
		DialTimeout: 3 * time.Second,
	})
	if err := rdb.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("连接 Redis %s 失败: %w", addr, err)
	}
	return rdb, nil
}

// backupMySQL 调 mysqldump 备份当前 DB
func backupMySQL(backupDir string) (string, error) {
	parts := parseDSN(currentDSN())
	if parts.db == "" {
		return "", fmt.Errorf("DSN 解析失败，找不到 dbname")
	}

	if err := os.MkdirAll(backupDir, 0755); err != nil {
		return "", fmt.Errorf("建备份目录失败: %w", err)
	}
	stamp := time.Now().Format("20060102-150405")
	backupPath := filepath.Join(backupDir, fmt.Sprintf("reset-all-%s.sql", stamp))

	cmd := exec.Command("mysqldump",
		"-h", parts.host,
		"-P", parts.port,
		"-u", parts.user,
		fmt.Sprintf("-p%s", parts.pass),
		"--single-transaction",
		"--routines",
		"--triggers",
		parts.db,
	)
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("mysqldump 失败: %v\n%s", err, exitErr.Stderr)
		}
		return "", fmt.Errorf("mysqldump 失败: %w", err)
	}

	if err := os.WriteFile(backupPath, out, 0644); err != nil {
		return "", fmt.Errorf("写备份文件失败: %w", err)
	}
	return backupPath, nil
}

// currentDSN 拿当前 MySQL DSN（跟 storage.InitDB 一致）
func currentDSN() string {
	if dsn := os.Getenv("MYSQL_DSN"); dsn != "" {
		return dsn
	}
	return fmt.Sprintf("%s:%s@tcp(%s:%s)/%s?charset=utf8mb4&parseTime=True&loc=Local",
		getenv("MYSQL_USER", "chatwitheino"),
		getenv("MYSQL_PASS", "chatwitheino"),
		getenv("MYSQL_HOST", "127.0.0.1"),
		getenv("MYSQL_PORT", "3306"),
		getenv("MYSQL_DB", "chatwitheino"),
	)
}

// dsnParts mysqldump 用的拆分 DSN
type dsnParts struct {
	user string
	pass string
	host string
	port string
	db   string
}

// parseDSN 拆 user:pass@tcp(host:port)/dbname?...
func parseDSN(dsn string) dsnParts {
	p := dsnParts{user: "root", host: "127.0.0.1", port: "3306"}
	atIdx := strings.Index(dsn, "@")
	if atIdx > 0 {
		creds := dsn[:atIdx]
		if ci := strings.Index(creds, ":"); ci > 0 {
			p.user = creds[:ci]
			p.pass = creds[ci+1:]
		}
	}
	tcpIdx := strings.Index(dsn, "tcp(")
	if tcpIdx > 0 {
		end := strings.Index(dsn[tcpIdx:], ")")
		if end > 0 {
			hp := dsn[tcpIdx+4 : tcpIdx+end]
			if ci := strings.Index(hp, ":"); ci > 0 {
				p.host = hp[:ci]
				p.port = hp[ci+1:]
			} else {
				p.host = hp
			}
		}
	}
	slashIdx := strings.Index(dsn, ")/")
	if slashIdx > 0 {
		rest := dsn[slashIdx+2:]
		if qi := strings.Index(rest, "?"); qi > 0 {
			p.db = rest[:qi]
		} else {
			p.db = rest
		}
	}
	return p
}

// resetMySQL 按 mode 清表
func resetMySQL(ctx context.Context, mode, shopID string) error {
	switch mode {
	case "full":
		return truncateAll(ctx)
	case "agent":
		return truncateTables(ctx, agentOnlyTables)
	case "shop":
		return deleteByShop(ctx, shopID)
	default:
		return errors.New("未知 mode: " + mode)
	}
}

// truncateAll 关 FK check 后逐表 TRUNCATE
func truncateAll(ctx context.Context) error {
	return truncateTables(ctx, realTables)
}

func truncateTables(ctx context.Context, tables []string) error {
	if err := storage.DB.WithContext(ctx).Exec("SET FOREIGN_KEY_CHECKS=0").Error; err != nil {
		return fmt.Errorf("关 FK check 失败: %w", err)
	}
	for _, t := range tables {
		if err := storage.DB.WithContext(ctx).Exec("TRUNCATE TABLE " + t).Error; err != nil {
			return fmt.Errorf("清表 %s 失败: %w", t, err)
		}
		fmt.Printf("  🗑  TRUNCATE %s\n", t)
	}
	if err := storage.DB.WithContext(ctx).Exec("SET FOREIGN_KEY_CHECKS=1").Error; err != nil {
		return fmt.Errorf("开 FK check 失败: %w", err)
	}
	return nil
}

// deleteByShop 事务内按 shop_id 删所有表 + shops 表该行
func deleteByShop(ctx context.Context, shopID string) error {
	return storage.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, t := range shopScopedTables {
			res := tx.WithContext(ctx).Exec("DELETE FROM "+t+" WHERE shop_id = ?", shopID)
			if res.Error != nil {
				return fmt.Errorf("清表 %s 失败: %w", t, res.Error)
			}
			fmt.Printf("  🗑  DELETE FROM %s WHERE shop_id='%s' (%d 行)\n", t, shopID, res.RowsAffected)
		}
		res := tx.WithContext(ctx).Exec("DELETE FROM shops WHERE id = ?", shopID)
		if res.Error != nil {
			return fmt.Errorf("删 shops 表失败: %w", res.Error)
		}
		fmt.Printf("  🗑  DELETE FROM shops WHERE id='%s' (%d 行)\n", shopID, res.RowsAffected)
		return nil
	})
}

// resetRedis 按 mode 清 key
func resetRedis(ctx context.Context, rdb *redis.Client, mode, shopID string) error {
	patterns := redisPatternsForMode(mode, shopID)
	for _, pattern := range patterns {
		if err := scanAndDel(ctx, rdb, pattern); err != nil {
			return fmt.Errorf("清 %s 失败: %w", pattern, err)
		}
	}

	// full 模式额外问 FLUSHDB
	if mode == "full" {
		fmt.Println("\n💡 full 模式还可执行 FLUSHDB 清空整个 Redis DB（包括无关 key）")
		fmt.Println("   确认清整个 Redis DB？输入大写 FLUSHDB：")
		reader := bufio.NewReader(os.Stdin)
		line, _ := reader.ReadString('\n')
		if strings.TrimSpace(line) == "FLUSHDB" {
			if err := rdb.FlushDB(ctx).Err(); err != nil {
				return fmt.Errorf("FLUSHDB 失败: %w", err)
			}
			fmt.Println("  🗑  FLUSHDB")
		} else {
			fmt.Println("  跳过 FLUSHDB")
		}
	}
	return nil
}

// redisPatternsForMode 按 mode 返回要清的 key pattern 列表
func redisPatternsForMode(mode, shopID string) []string {
	switch mode {
	case "full":
		return []string{
			"appt:lock:*",
			"kf-debounce:*",
		}
	case "agent":
		return []string{
			"kf-debounce:*",
		}
	case "shop":
		return []string{
			fmt.Sprintf("kf-debounce:wecom_%s_*", shopID),
		}
	default:
		return nil
	}
}

// scanAndDel SCAN 找所有匹配 key，DEL 删（避免 KEYS 阻塞）
func scanAndDel(ctx context.Context, rdb *redis.Client, pattern string) error {
	var (
		cursor uint64
		total  int
	)
	for {
		keys, next, err := rdb.Scan(ctx, cursor, pattern, 100).Result()
		if err != nil {
			return err
		}
		if len(keys) > 0 {
			if err := rdb.Del(ctx, keys...).Err(); err != nil {
				return err
			}
			total += len(keys)
		}
		cursor = next
		if cursor == 0 {
			break
		}
	}
	fmt.Printf("  🗑  DEL %s (%d 个 key)\n", pattern, total)
	return nil
}

// getenv 读环境变量带默认值
func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}