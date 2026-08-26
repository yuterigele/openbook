package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/yuterigele/openbook/chatmodel"
	"github.com/yuterigele/openbook/storage"
)

// RunMigrate 执行服务启动期的幂等数据库初始化和回填。
// 细粒度历史步骤仍可使用 cmd/migrate 的 -only 参数。
func RunMigrate(writer io.Writer, args []string) int {
	flags := flag.NewFlagSet("migrate", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	dryRun := flags.Bool("dry-run", false, "只显示迁移范围，不连接数据库")
	legacyReport := flags.Bool("legacy-report", false, "只读生成旧预约迁移规划报告")
	merchantID := flags.String("merchant-id", "", "旧预约迁移使用的显式商户 ID")
	locationID := flags.String("location-id", "", "只报告指定旧门店 ID")
	reportFile := flags.String("report-file", "", "将迁移规划 JSON 写入新文件")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		fmt.Fprintln(writer, "用法: openbook migrate [-dry-run] [-legacy-report -merchant-id <id> [-location-id <id>] [-report-file <path>]")
		return 2
	}
	if *legacyReport {
		if !*dryRun {
			fmt.Fprintln(writer, "-legacy-report 必须同时指定 -dry-run")
			return 2
		}
		if *merchantID == "" {
			fmt.Fprintln(writer, "-legacy-report 必须指定 -merchant-id")
			return 2
		}
		return runLegacyMigrationReport(writer, *merchantID, *locationID, *reportFile)
	}
	if *merchantID != "" || *locationID != "" || *reportFile != "" {
		fmt.Fprintln(writer, "-merchant-id、-location-id 和 -report-file 只能与 -legacy-report 一起使用")
		return 2
	}
	if *dryRun {
		fmt.Fprintln(writer, "迁移预览（不会连接或修改数据库）：")
		fmt.Fprintln(writer, "- AutoMigrate 通用预约、顾客、权限和 Outbox 表")
		fmt.Fprintln(writer, "- 补齐默认店铺、管理员、角色权限和顾客档案")
		return 0
	}
	chatmodel.LoadEnv()
	if _, err := storage.InitDB(context.Background()); err != nil {
		fmt.Fprintf(writer, "迁移失败: %v\n", err)
		return 1
	}
	defer closeOpenBookDB()
	fmt.Fprintln(writer, "迁移完成：数据库结构和启动期幂等回填已执行。")
	return 0
}

func runLegacyMigrationReport(writer io.Writer, merchantID, locationID, reportFile string) int {
	db, err := storage.OpenReadOnlyDB(context.Background())
	if err != nil {
		fmt.Fprintf(writer, "迁移只读规划失败: %v\n", err)
		return 1
	}
	defer func() {
		if sqlDB, closeErr := db.DB(); closeErr == nil && sqlDB != nil {
			_ = sqlDB.Close()
		}
	}()

	inputs, err := storage.LoadLegacyBookingMigrationInputs(context.Background(), db, merchantID, locationID)
	if err != nil {
		fmt.Fprintf(writer, "迁移只读规划失败: %v\n", err)
		return 1
	}
	plan, err := storage.PlanLegacyAppointmentMigration(inputs.Appointments, inputs.Services, inputs.Barbers, storage.LegacyBookingMigrationOptions{
		MerchantID:              merchantID,
		LocationID:              locationID,
		ExistingBookings:        inputs.ExistingBookings,
		ExistingBookingIDs:      inputs.ExistingBookingIDs,
		ExistingIdempotencyKeys: inputs.ExistingIdempotencyKeys,
	})
	if err != nil {
		fmt.Fprintf(writer, "迁移只读规划失败: %v\n", err)
		return 1
	}
	payload, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		fmt.Fprintf(writer, "迁移报告序列化失败: %v\n", err)
		return 1
	}
	payload = append(payload, '\n')
	if reportFile != "" {
		file, err := os.OpenFile(reportFile, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			fmt.Fprintf(writer, "迁移报告写入失败: %v\n", err)
			return 1
		}
		if _, err := file.Write(payload); err != nil {
			_ = file.Close()
			_ = os.Remove(reportFile)
			fmt.Fprintf(writer, "迁移报告写入失败: %v\n", err)
			return 1
		}
		if err := file.Close(); err != nil {
			fmt.Fprintf(writer, "迁移报告关闭失败: %v\n", err)
			return 1
		}
		fmt.Fprintf(writer, "迁移只读规划完成：总数=%d，可迁移=%d，阻断=%d，报告=%s\n", plan.Total, plan.Ready, plan.Blocked, reportFile)
	} else {
		_, _ = writer.Write(payload)
	}
	if plan.Blocked > 0 {
		return 1
	}
	return 0
}

// RunSeed 安装或清理带 [DEMO] 前缀的演示数据。
func RunSeed(writer io.Writer, args []string) int {
	flags := flag.NewFlagSet("seed", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	shopOnly := flags.Bool("shop-only", false, "只创建演示店铺、员工和服务目录")
	skipAppointments := flags.Bool("skip-appointments", false, "跳过演示预约")
	clean := flags.Bool("clean", false, "清理 [DEMO] 演示店铺")
	dryRun := flags.Bool("dry-run", false, "只显示操作，不连接数据库")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		fmt.Fprintln(writer, "用法: openbook seed [-shop-only] [-skip-appointments] [-clean] [-dry-run]")
		return 2
	}
	if *dryRun {
		if *clean {
			fmt.Fprintln(writer, "种子预览：清理所有 [DEMO] 演示店铺（不会连接或修改数据库）。")
		} else {
			fmt.Fprintf(writer, "种子预览：创建演示数据（shop_only=%t, skip_appointments=%t；不会连接或修改数据库）。\n", *shopOnly, *skipAppointments)
		}
		return 0
	}
	chatmodel.LoadEnv()
	if _, err := storage.InitDB(context.Background()); err != nil {
		fmt.Fprintf(writer, "种子初始化失败: %v\n", err)
		return 1
	}
	defer closeOpenBookDB()
	ctx := context.Background()
	if *clean {
		count, err := storage.CleanDemoShops(ctx)
		if err != nil {
			fmt.Fprintf(writer, "清理演示数据失败: %v\n", err)
			return 1
		}
		fmt.Fprintf(writer, "已清理 %d 家 [DEMO] 店铺。\n", count)
		return 0
	}
	stats, err := storage.SeedDemoData(ctx, storage.DemoSeedOptions{ShopOnly: *shopOnly, SkipAppointments: *skipAppointments})
	if err != nil {
		fmt.Fprintf(writer, "生成演示数据失败: %v\n", err)
		return 1
	}
	fmt.Fprintf(writer, "演示数据完成：店铺=%d 员工=%d 顾客=%d 预约=%d。\n", stats.Shops, stats.Barbers, stats.Customers, stats.Appointments)
	return 0
}

func closeOpenBookDB() {
	if storage.DB == nil {
		return
	}
	if sqlDB, err := storage.DB.DB(); err == nil && sqlDB != nil {
		_ = sqlDB.Close()
	}
	storage.DB = nil
}
