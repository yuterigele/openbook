package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type mysqlTarget struct {
	Host     string
	Port     string
	User     string
	Password string
	Database string
}

// RunBackup 使用 mysqldump 创建一致性备份；密码不会进入命令行参数或输出。
func RunBackup(writer io.Writer, args []string) int {
	return runBackup(writer, args, os.Getenv)
}

func runBackup(writer io.Writer, args []string, lookup func(string) string) int {
	flags := flag.NewFlagSet("backup", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	directory := flags.String("dir", "./backups", "备份目录")
	dryRun := flags.Bool("dry-run", false, "只显示目标，不执行 mysqldump")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		fmt.Fprintln(writer, "用法: openbook backup [-dir ./backups] [-dry-run]")
		return 2
	}
	target, err := mysqlTargetFromEnv(lookup)
	if err != nil {
		fmt.Fprintf(writer, "备份配置错误: %v\n", err)
		return 1
	}
	outputPath := filepath.Join(*directory, "openbook-"+time.Now().Format("20060102-150405")+".sql")
	if *dryRun {
		fmt.Fprintf(writer, "备份预览：%s:%s/%s -> %s（不会执行 mysqldump）\n", target.Host, target.Port, target.Database, filepath.Clean(outputPath))
		return 0
	}
	if err := os.MkdirAll(*directory, 0700); err != nil {
		fmt.Fprintf(writer, "创建备份目录失败: %v\n", err)
		return 1
	}
	file, err := os.OpenFile(outputPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		fmt.Fprintf(writer, "创建备份文件失败: %v\n", err)
		return 1
	}
	defer file.Close()
	command := exec.Command("mysqldump", "-h", target.Host, "-P", target.Port, "-u", target.User, "--single-transaction", "--routines", "--triggers", target.Database)
	command.Env = envWithMySQLPassword(target.Password)
	command.Stdout = file
	command.Stderr = io.Discard
	if err := command.Run(); err != nil {
		_ = file.Close()
		_ = os.Remove(outputPath)
		fmt.Fprintln(writer, "mysqldump 执行失败，未保留不完整备份文件")
		return 1
	}
	_ = file.Close()
	_ = os.Chmod(outputPath, 0600)
	fmt.Fprintf(writer, "备份完成: %s\n", filepath.Clean(outputPath))
	return 0
}

// RunRestore 从 SQL 文件恢复数据库；这是破坏性操作，必须显式确认。
func RunRestore(writer io.Writer, args []string) int {
	return runRestore(writer, args, os.Getenv)
}

func runRestore(writer io.Writer, args []string, lookup func(string) string) int {
	flags := flag.NewFlagSet("restore", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	filePath := flags.String("file", "", "SQL 备份文件")
	yes := flags.Bool("yes", false, "确认覆盖数据库")
	forceProduction := flags.Bool("force-prod", false, "允许在 APP_ENV=production 时恢复")
	dryRun := flags.Bool("dry-run", false, "只检查参数，不执行 mysql")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		fmt.Fprintln(writer, "用法: openbook restore -file backup.sql [-yes] [-force-prod] [-dry-run]")
		return 2
	}
	if strings.TrimSpace(*filePath) == "" {
		fmt.Fprintln(writer, "恢复必须指定 -file")
		return 2
	}
	if !*yes && !*dryRun {
		fmt.Fprintln(writer, "恢复是破坏性操作，请显式指定 -yes")
		return 2
	}
	if strings.EqualFold(lookup("APP_ENV"), "production") && !*forceProduction {
		fmt.Fprintln(writer, "生产环境恢复必须显式指定 -force-prod")
		return 1
	}
	cleanPath := filepath.Clean(*filePath)
	info, err := os.Stat(cleanPath)
	if err != nil {
		fmt.Fprintf(writer, "备份文件不可读: %v\n", err)
		return 1
	}
	if !info.Mode().IsRegular() {
		fmt.Fprintln(writer, "备份文件必须是普通文件")
		return 1
	}
	target, err := mysqlTargetFromEnv(lookup)
	if err != nil {
		fmt.Fprintf(writer, "恢复配置错误: %v\n", err)
		return 1
	}
	if *dryRun {
		fmt.Fprintf(writer, "恢复预览：%s -> %s:%s/%s（不会执行 mysql）\n", cleanPath, target.Host, target.Port, target.Database)
		return 0
	}
	input, err := os.Open(cleanPath)
	if err != nil {
		fmt.Fprintf(writer, "打开备份文件失败: %v\n", err)
		return 1
	}
	defer input.Close()
	command := exec.CommandContext(context.Background(), "mysql", "-h", target.Host, "-P", target.Port, "-u", target.User, target.Database)
	command.Env = envWithMySQLPassword(target.Password)
	command.Stdin = input
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	if err := command.Run(); err != nil {
		fmt.Fprintln(writer, "mysql 恢复失败，请检查数据库状态和备份文件")
		return 1
	}
	fmt.Fprintf(writer, "恢复完成: %s\n", cleanPath)
	return 0
}

func mysqlTargetFromEnv(lookup func(string) string) (mysqlTarget, error) {
	if lookup == nil {
		lookup = func(string) string { return "" }
	}
	if dsn := lookup("MYSQL_DSN"); dsn != "" {
		return parseMySQLDSN(dsn)
	}
	target := mysqlTarget{
		Host: lookup("MYSQL_HOST"), Port: lookup("MYSQL_PORT"), User: lookup("MYSQL_USER"),
		Password: lookup("MYSQL_PASS"), Database: lookup("MYSQL_DB"),
	}
	if target.Host == "" || target.User == "" || target.Password == "" || target.Database == "" {
		return target, errors.New("需要 MYSQL_DSN 或完整的 MYSQL_HOST/MYSQL_USER/MYSQL_PASS/MYSQL_DB")
	}
	if target.Port == "" {
		target.Port = "3306"
	}
	return target, nil
}

func parseMySQLDSN(dsn string) (mysqlTarget, error) {
	target := mysqlTarget{}
	at := strings.Index(dsn, "@tcp(")
	if at <= 0 {
		return target, errors.New("MYSQL_DSN 格式不支持")
	}
	credentials := dsn[:at]
	separator := strings.IndexByte(credentials, ':')
	if separator <= 0 {
		return target, errors.New("MYSQL_DSN 缺少用户或密码")
	}
	target.User, target.Password = credentials[:separator], credentials[separator+1:]
	hostStart := at + len("@tcp(")
	hostEndRelative := strings.IndexByte(dsn[hostStart:], ')')
	if hostEndRelative <= 0 {
		return target, errors.New("MYSQL_DSN 缺少 host")
	}
	hostPort := dsn[hostStart : hostStart+hostEndRelative]
	portSeparator := strings.LastIndexByte(hostPort, ':')
	if portSeparator <= 0 {
		return target, errors.New("MYSQL_DSN 缺少 port")
	}
	target.Host, target.Port = hostPort[:portSeparator], hostPort[portSeparator+1:]
	databaseStart := hostStart + hostEndRelative + 1
	if databaseStart >= len(dsn) || dsn[databaseStart] != '/' {
		return target, errors.New("MYSQL_DSN 缺少 database")
	}
	target.Database = dsn[databaseStart+1:]
	if query := strings.IndexByte(target.Database, '?'); query >= 0 {
		target.Database = target.Database[:query]
	}
	if target.Host == "" || target.Port == "" || target.Database == "" {
		return target, errors.New("MYSQL_DSN 缺少数据库连接字段")
	}
	return target, nil
}

func envWithMySQLPassword(password string) []string {
	env := os.Environ()
	for index, value := range env {
		if strings.HasPrefix(value, "MYSQL_PWD=") {
			env[index] = "MYSQL_PWD=" + password
			return env
		}
	}
	return append(env, "MYSQL_PWD="+password)
}
