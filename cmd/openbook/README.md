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
go run ./cmd/openbook generate profile beauty
go run ./cmd/openbook generate tool customer_note
go run ./cmd/openbook generate channel my_channel
```

`version` 输出版本、提交和构建时间；发布构建可以通过 `-ldflags` 注入：

```bash
go build -ldflags "-X main.version=v1.0.0 -X main.commit=$(git rev-parse --short HEAD)" ./cmd/openbook
```

`doctor` 只检查环境变量是否存在、运行模式和是否使用示例占位密码，不输出 DSN、API Key、企业微信 Token 或 AES Key，也不会自动迁移数据库。`APP_ENV=production` 时还会阻断 Stub 模型、缺失 Redis/企业微信凭据、非 `real` 回复模式、弱管理员密码和缺失 JWT 密钥。检查结果分为：

- `PASS`：配置存在且满足当前检查；
- `WARN`：可以运行，但不适合直接作为生产配置；
- `FAIL`：缺少数据库或模型链等阻断配置。

数据库连接、Redis 可用性和 Compose 服务状态仍需在部署环境中单独验证；`doctor` 不把“配置存在”冒充为网络连接成功。

`init` 默认只创建不存在的 `.env`，写入 Stub/Mock 本地模式、MySQL/Redis 地址和随机管理员密码、平台管理员密码及 `JWT_SECRET`。已有文件不会覆盖；必须显式使用 `go run ./cmd/openbook init -force`。生成后仍需按部署环境修改数据库、模型和渠道配置，不能直接当作生产配置。

`migrate` 执行应用启动期的幂等 AutoMigrate、默认数据和顾客档案回填；先用 `-dry-run` 查看范围。需要历史版本的单步选择时继续使用 `go run ./cmd/migrate -only=...`。旧预约迁移报告另用 `go run ./cmd/openbook migrate -dry-run -legacy-report -merchant-id <id> [-location-id <id>] [-report-file ./migration-report.json]`，该模式只读查询旧表和 next 表，不执行 AutoMigrate、seed 或写入；阻断项大于 0 时以非零状态退出。报告文件必须是不存在的新文件。

`seed` 调用现有演示数据生成器，支持 `-shop-only`、`-skip-appointments`、`-clean` 和 `-dry-run`。清理只针对 `[DEMO]` 店铺，生产环境不要把它当作数据删除工具。

`backup` 调用 `mysqldump`，必须明确配置 `MYSQL_DSN` 或完整的 `MYSQL_HOST/PORT/USER/PASS/DB`，密码通过 `MYSQL_PWD` 传给子进程，不出现在命令行和日志中；先用 `-dry-run` 查看目标。`restore` 只接受普通 SQL 文件，默认拒绝执行，必须指定 `-yes`；`APP_ENV=production` 还必须指定 `-force-prod`。恢复前应确认备份文件、数据库目标和回滚窗口。

`generate` 只接受小写字母开头的标识符，生成 Profile、Tool 或 Channel 的最小实现与契约测试模板；已有文件一律拒绝覆盖。生成的 Tool 默认返回“尚未实现”，必须补齐服务端校验、权限声明和注册测试后才能加入生产白名单；渠道模板也不替代验签、去重和可信身份解析。
