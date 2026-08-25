package main

import (
	"context"
	"flag"
	"fmt"
	"io"

	"github.com/yuterigele/openbook/chatmodel"
	"github.com/yuterigele/openbook/storage"
)

// RunMigrate 执行服务启动期的幂等数据库初始化和回填。
// 细粒度历史步骤仍可使用 cmd/migrate 的 -only 参数。
func RunMigrate(writer io.Writer, args []string) int {
	flags := flag.NewFlagSet("migrate", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	dryRun := flags.Bool("dry-run", false, "只显示迁移范围，不连接数据库")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		fmt.Fprintln(writer, "用法: openbook migrate [-dry-run]")
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
