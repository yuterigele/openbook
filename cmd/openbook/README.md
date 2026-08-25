# `openbook` 运维入口

当前提供以下运维命令：

```bash
go run ./cmd/openbook version
go run ./cmd/openbook doctor
go run ./cmd/openbook init
go run ./cmd/openbook migrate
go run ./cmd/openbook seed -shop-only
go run ./cmd/openbook backup -dir ./backups
go run ./cmd/openbook restore -file ./backups/openbook-YYYYMMDD-HHMMSS.sql -yes
```

`version` 输出版本、提交和构建时间；发布构建可以通过 `-ldflags` 注入：

```bash
go build -ldflags "-X main.version=v1.0.0 -X main.commit=$(git rev-parse --short HEAD)" ./cmd/openbook
```

`doctor` 只检查环境变量是否存在、运行模式和是否使用示例占位密码，不输出 DSN、API Key、企业微信 Token 或 AES Key，也不会自动迁移数据库。检查结果分为：

- `PASS`：配置存在且满足当前检查；
- `WARN`：可以运行，但不适合直接作为生产配置；
- `FAIL`：缺少数据库或模型链等阻断配置。

数据库连接、Redis 可用性和 Compose 服务状态仍需在部署环境中单独验证；`doctor` 不把“配置存在”冒充为网络连接成功。

`init` 默认只创建不存在的 `.env`，写入 Stub/Mock 本地模式、MySQL/Redis 地址和随机管理员密码、平台管理员密码及 `JWT_SECRET`。已有文件不会覆盖；必须显式使用 `go run ./cmd/openbook init -force`。生成后仍需按部署环境修改数据库、模型和渠道配置，不能直接当作生产配置。

`migrate` 执行应用启动期的幂等 AutoMigrate、默认数据和顾客档案回填；先用 `-dry-run` 查看范围。需要历史版本的单步选择时继续使用 `go run ./cmd/migrate -only=...`。

`seed` 调用现有演示数据生成器，支持 `-shop-only`、`-skip-appointments`、`-clean` 和 `-dry-run`。清理只针对 `[DEMO]` 店铺，生产环境不要把它当作数据删除工具。

`backup` 调用 `mysqldump`，密码通过 `MYSQL_PWD` 传给子进程，不出现在命令行和日志中；先用 `-dry-run` 查看目标。`restore` 默认拒绝执行，必须指定 `-yes`；`APP_ENV=production` 还必须指定 `-force-prod`。恢复前应确认备份文件、数据库目标和回滚窗口。
