package storage

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"strconv"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// DB 全局 *gorm.DB 句柄。InitDB 后所有 repo 方法都依赖它。
var DB *gorm.DB

// IsReady 返回 storage.DB 是否已初始化（v4.5 C1 工具降级用）
//
// 用途：tools 包在调用前可检查；DB 未就绪时返回友好错误而不是 panic。
// 注意：本函数只判断 DB != nil，**不**做连接活性检查（避免拖慢热路径）。
// 实际"DB 暂时连不上"由各 storage 调用的 err 反映。
func IsReady() bool {
	return DB != nil
}

// InitDB 根据环境变量连接 MySQL，做 AutoMigrate，并返回 *gorm.DB。
//
// 必填环境变量：
//   - MYSQL_DSN      例如：user:pass@tcp(127.0.0.1:3306)/chatwitheino?charset=utf8mb4&parseTime=True&loc=Local
//   - 或者：MYSQL_HOST / MYSQL_PORT / MYSQL_USER / MYSQL_PASS / MYSQL_DB
func InitDB(ctx context.Context) (*gorm.DB, error) {
	dsn := os.Getenv("MYSQL_DSN")
	if dsn == "" {
		host := getenv("MYSQL_HOST", "127.0.0.1")
		port := getenv("MYSQL_PORT", "3306")
		user := getenv("MYSQL_USER", "chatwitheino")
		pass := getenv("MYSQL_PASS", "chatwitheino")
		dbname := getenv("MYSQL_DB", "chatwitheino")
		dsn = fmt.Sprintf("%s:%s@tcp(%s:%s)/%s?charset=utf8mb4&parseTime=True&loc=Local",
			user, pass, host, port, dbname)
	}

	gormLogger := logger.New(
		log.New(os.Stdout, "[gorm] ", log.LstdFlags),
		logger.Config{
			SlowThreshold:             500 * time.Millisecond,
			LogLevel:                  logger.Warn,
			IgnoreRecordNotFoundError: true,
			Colorful:                  false,
		},
	)

	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{Logger: gormLogger})
	if err != nil {
		return nil, fmt.Errorf("连接 MySQL 失败: %w", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, err
	}
	sqlDB.SetMaxOpenConns(50)
	sqlDB.SetMaxIdleConns(10)
	sqlDB.SetConnMaxLifetime(30 * time.Minute)

	if err := db.WithContext(ctx).AutoMigrate(
		&Shop{},
		&Barber{},
		&Customer{},
		&Appointment{},
		&Subscription{},
		&WecomMessageLog{},
		&ReminderLog{},
		&EventLog{},
		&ShopAdmin{},
		&BarberLeave{}, // P4 理发师请假（2026-06-21）
		&Service{},     // v4.4 服务目录（2026-06-22）
		&RolePermission{}, // v4.7 RBAC：role → permission 映射表
		&CustomerNotification{}, // v4.10 leave notify 持久化（2026-06-23）
		&APIKey{},               // v4.12.1 api_access feature 实战
		&KfSyncState{},          // v4.13.1 微信客服 sync cursor 持久化
		&KfSeenMsg{},            // v4.13.1 微信客服 msgid 去重持久化
	); err != nil {
		return nil, fmt.Errorf("AutoMigrate 失败: %w", err)
	}

	// 跑完后列出所有已建的表，便于排查"表不存在"问题
	if tables, listErr := db.WithContext(ctx).Migrator().GetTables(); listErr == nil {
		log.Printf("[storage] 已建表 (%d): %v", len(tables), tables)
	}

	// 种子数据：从 .env 构建默认店铺 + 默认 admin（多店场景下也可走这个种子）
	if err := SeedDefaultData(ctx); err != nil {
		log.Printf("[storage] SeedDefaultData 警告: %v", err)
	}

	// v4.7 RBAC 老数据兜底：admin.role 为空/NULL 的统一填 owner
	//
	// 背景：role / status 列是 v4.7 这次 AutoMigrate 加的。GORM 加列时**不会**回填已有行，
	// 老 admin 在 DB 里 role='' → 登录后所有 RequirePerm 全 403（无权限）。
	// 这里幂等（只 update 空 role），每次启动都跑也没事，自愈型。
	if err := db.WithContext(ctx).Model(&ShopAdmin{}).
		Where("role = '' OR role IS NULL").
		Update("role", "owner").Error; err != nil {
		log.Printf("[storage] backfill ShopAdmin.role 警告: %v", err)
	}

	// v4.7 RBAC 默认 role → permission 映射（只在表空时跑，不覆盖运营在线调整）
	if err := SeedDefaultRolePermissions(ctx); err != nil {
		log.Printf("[storage] SeedDefaultRolePermissions 警告: %v", err)
	}

	// v4.13.0 自动 reconcile：每次启动把 DefaultRolePermissions 缺的 perm 补进 role_permissions
	//   - 修复 v4.10.1 那种"加新 perm 后老店铺拿不到"的 footgun（admin-tool perms reconcile 是手动补丁）
	//   - 幂等 + 只补缺失（Reconcile 内部用 INSERT IGNORE 兜底，不会覆盖运营调整）
	//   - 成本：3 个 role × 1 个 SELECT + 几个 INSERT（最多），启动 < 1ms，可忽略
	if res, err := ReconcileRolePermissions(ctx); err != nil {
		log.Printf("[storage] ReconcileRolePermissions 警告: %v", err)
	} else if res.Inserted > 0 {
		log.Printf("[storage] ReconcileRolePermissions: 补 %d 条 perm（已有 %d 条保留）", res.Inserted, res.Skipped)
		for _, desc := range res.InsertedList {
			log.Printf("[storage]   + %s", desc)
		}
	}

	// 兼容：仍保留 admin-tool perms reconcile 命令（运营手动跑也行）

	// v4.8 顾客档案自愈：appointments 有 customer_id 为空 + customer 名字非空的，
	// 按名字去 customers 表查（没有就建），回填 appointment.customer_id。
	// 老数据就是因为 CreateAppointment 不建顾客档案才漏的——这里兜底。
	if err := BackfillMissingCustomers(ctx); err != nil {
		log.Printf("[storage] BackfillMissingCustomers 警告: %v", err)
	}

	DB = db
	log.Printf("[storage] MySQL 初始化完成")
	return db, nil
}

// seedShopFromEnv 从环境变量读企业微信 + 店铺配置，写入 shop 表（idempotent）
//
// 配置来源：
//   - DEFAULT_SHOP_ID     店铺 ID（默认 "default"）
//   - DEFAULT_SHOP_NAME   店铺名（默认 "默认理发店"）
//   - WECOM_CORP_ID / WECOM_AGENT_ID / WECOM_SECRET / WECOM_TOKEN / WECOM_ENCODING_AES_KEY / WECOM_KF_LINK
//   - DEFAULT_ADMIN_USERNAME / DEFAULT_ADMIN_PASSWORD（默认 admin / admin123）
//
// 修复（2026-06-20）：之前如果店铺已存在就直接 return，导致 admin 永远建不出来。
// 现在店铺存在也尝试建 admin（用 username 查重）。
func seedShopFromEnv(ctx context.Context, db *gorm.DB) error {
	shopID := getenv("DEFAULT_SHOP_ID", "default")
	now := time.Now()
	agentID, _ := strconv.Atoi(getenv("WECOM_AGENT_ID", "0"))

	// 1) 建店铺（已存在则跳过；但 wecom_* 字段空时回填 env）
	var existing Shop
	if err := db.WithContext(ctx).Where("id = ?", shopID).First(&existing).Error; err != nil {
		shop := Shop{
			ID:                shopID,
			Name:              getenv("DEFAULT_SHOP_NAME", "默认理发店"),
			// v4.14 修：地址不再走 .env 配置。Owner 在店铺设置 UI 里手填。
			//   之前默认 "请到 .env 配置" 提示很奇怪——顾客看不到，店主不填。
			Address:           "",
			Timezone:          "Asia/Shanghai",
			OpenHour:          9,
			CloseHour:         18,
			LunchStart:        12,
			LunchEnd:          13,
			LunchEndMin:       30,
			Plan:              "basic",
			ExpiresAt:         now.AddDate(1, 0, 0),
			WecomCorpID:       os.Getenv("WECOM_CORP_ID"),
			WecomAgentID:      agentID,
			WecomSecret:       os.Getenv("WECOM_SECRET"),
			WecomToken:        os.Getenv("WECOM_TOKEN"),
			WecomEncodingAESKey: os.Getenv("WECOM_ENCODING_AES_KEY"),
			WecomKFLink:       os.Getenv("WECOM_KF_LINK"),
			CreatedAt:         now,
			UpdatedAt:         now,
		}
		if createErr := db.WithContext(ctx).Create(&shop).Error; createErr != nil {
			return createErr
		}
		log.Printf("[storage] 种子店铺: %s (id=%s)", shop.Name, shop.ID)
	} else {
		log.Printf("[storage] 店铺 %s 已存在，检查 wecom_* 字段是否需要回填", shopID)
		// 老数据兜底：如果 wecom_* 字段空但 env 有值，回填
		needUpdate := false
		updates := map[string]interface{}{"updated_at": now}
		if existing.WecomCorpID == "" && os.Getenv("WECOM_CORP_ID") != "" {
			updates["wecom_corp_id"] = os.Getenv("WECOM_CORP_ID")
			updates["wecom_agent_id"] = agentID
			updates["wecom_secret"] = os.Getenv("WECOM_SECRET")
			updates["wecom_token"] = os.Getenv("WECOM_TOKEN")
			updates["wecom_encoding_aes_key"] = os.Getenv("WECOM_ENCODING_AES_KEY")
			if os.Getenv("WECOM_KF_LINK") != "" {
				updates["wecom_kf_link"] = os.Getenv("WECOM_KF_LINK")
			}
			needUpdate = true
		}
		if needUpdate {
			if err := db.WithContext(ctx).Model(&existing).Updates(updates).Error; err != nil {
				log.Printf("[storage] 回填 wecom_* 失败: %v", err)
			} else {
				log.Printf("[storage] 已回填店铺 %s 的 wecom_* 字段", shopID)
			}
		}
	}

	// 2) 建 admin（按 username 查重，已存在则跳过）—— 修复点：无论店铺新建还是已存在都要执行
	username := getenv("DEFAULT_ADMIN_USERNAME", "admin")
	password := getenv("DEFAULT_ADMIN_PASSWORD", "admin123")
	var existingAdmin ShopAdmin
	if err := db.WithContext(ctx).Where("username = ?", username).First(&existingAdmin).Error; err == nil {
		log.Printf("[storage] admin %s 已存在，跳过创建", username)
		return nil
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	admin := ShopAdmin{
		ShopID:       shopID,
		Username:     username,
		PasswordHash: string(hash),
		Role:         "owner",
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := db.WithContext(ctx).Create(&admin).Error; err != nil {
		return err
	}
	log.Printf("[storage] 种子 admin: %s / %s（请尽快改密码）", username, password)

	// 3) v4.9: 种子 platform_admin 超管账号（跨店看所有数据）
	//   - 默认用户名 platform / 密码 platform123
	//   - shop_id 仍指向默认店（防止某些按 shop_id 过滤的 SQL 把超管过滤掉；超管本身无视该字段）
	//   - 已存在则跳过
	platformUsername := getenv("DEFAULT_PLATFORM_ADMIN_USERNAME", "platform")
	platformPassword := getenv("DEFAULT_PLATFORM_ADMIN_PASSWORD", "platform123")
	var existingPlatform ShopAdmin
	if err := db.WithContext(ctx).Where("username = ?", platformUsername).First(&existingPlatform).Error; err != nil {
		phash, perr := bcrypt.GenerateFromPassword([]byte(platformPassword), bcrypt.DefaultCost)
		if perr != nil {
			return perr
		}
		padmin := ShopAdmin{
			ShopID:       shopID, // 留个默认值避免空指针；超管本身不限店
			Username:     platformUsername,
			PasswordHash: string(phash),
			Role:         "platform_admin",
			CreatedAt:    now,
			UpdatedAt:    now,
		}
		if err := db.WithContext(ctx).Create(&padmin).Error; err != nil {
			return err
		}
		log.Printf("[storage] 种子 platform_admin: %s / %s（v4.9 跨店超管，请尽快改密码）",
			platformUsername, platformPassword)
	}

	return nil
}

func seedBarbers(ctx context.Context, db *gorm.DB) error {
	shopID := getenv("DEFAULT_SHOP_ID", "default")
	defaults := []Barber{
		{ID: "1", Name: "Tony", ShopID: shopID, Skills: "剪发,染发", Active: true},
		{ID: "2", Name: "Kevin", ShopID: shopID, Skills: "剪发,烫发", Active: true},
	}
	for _, b := range defaults {
		var existing Barber
		if err := db.WithContext(ctx).Where("name = ?", b.Name).First(&existing).Error; err == nil {
			continue
		}
		if err := db.WithContext(ctx).Create(&b).Error; err != nil {
			return err
		}
		log.Printf("[storage] 种子理发师: %s (id=%s)", b.Name, b.ID)
	}
	return nil
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// uuidGenerate 实际调用 uuid.NewString（避免在多个文件里重复 import）
func uuidGenerate() string {
	return uuid.Must(uuid.NewRandom()).String()
}

// seedDefaultServices 给所有"还没有任何 service"的店铺建一组通用服务
//
// 默认 7 项：剪发 30min、烫发 90min、染发 90min、洗吹 30min、护理 60min、造型 45min、其他 30min
// 用 sort_order 10/20/30... 递增，便于后台手动调整顺序
func seedDefaultServices(ctx context.Context, db *gorm.DB) error {
	var shops []Shop
	if err := db.WithContext(ctx).Find(&shops).Error; err != nil {
		return err
	}
	defaults := []struct {
		Name         string
		EstimatedMin int
		PriceRange   string
		SortOrder    int
	}{
		{"剪发", 30, "30-50", 10},
		{"烫发", 90, "180-380", 20},
		{"染发", 90, "180-480", 30},
		{"洗吹", 30, "20-40", 40},
		{"护理", 60, "80-150", 50},
		{"造型", 45, "60-120", 60},
		{"其他", 30, "0-0", 70},
	}
	for _, shop := range shops {
		if CountServices(ctx, shop.ID) > 0 {
			continue
		}
		now := time.Now()
		for _, d := range defaults {
			s := Service{
				ID:           uuidNewString(),
				ShopID:       shop.ID,
				Name:         d.Name,
				EstimatedMin: d.EstimatedMin,
				PriceRange:   d.PriceRange,
				IsActive:     true,
				SortOrder:    d.SortOrder,
				CreatedAt:    now,
				UpdatedAt:    now,
			}
			if err := db.WithContext(ctx).Create(&s).Error; err != nil {
				log.Printf("[storage] 种子 service %q 失败 (shop=%s): %v", d.Name, shop.ID, err)
			}
		}
		log.Printf("[storage] 种子 service: shop=%s 建了 %d 项", shop.ID, len(defaults))
	}
	return nil
}

// uuidNewString 包内统一的 ID 生成器（uuid v4）
func uuidNewString() string {
	return uuidGenerate()
}

// SeedDefaultData 导出 InitDB 内部的所有 seed 步骤（cmd/migrate 用）
//
// 用途：手动迁移脚本可以单独跑这些 seed，不依赖重启服务。
// InitDB 启动时也会调（保持向后兼容）。
//
// 内容：
//   - seedShopFromEnv（默认店铺 + 默认 admin + 默认 platform_admin，v4.9 增量）
//   - seedBarbers（默认理发师）
//   - ensureBarberShopIDs（老 barber 数据兜底）
//   - seedDefaultServices（每个店没有 service 时建一组通用项）
//
// 幂等：每个 seed 内部都按"已存在则跳过"实现，重复调用 0 副作用。
func SeedDefaultData(ctx context.Context) error {
	db := DB
	if db == nil {
		return errors.New("DB 未初始化；请先调 InitDB")
	}
	if err := seedShopFromEnv(ctx, db); err != nil {
		log.Printf("[storage] seedShopFromEnv 警告: %v", err)
	}
	if err := seedBarbers(ctx, db); err != nil {
		log.Printf("[storage] seedBarbers 警告: %v", err)
	}
	if err := ensureBarberShopIDs(ctx); err != nil {
		log.Printf("[storage] ensureBarberShopIDs 警告: %v", err)
	}
	if err := seedDefaultServices(ctx, db); err != nil {
		log.Printf("[storage] seedDefaultServices 警告: %v", err)
	}
	return nil
}
