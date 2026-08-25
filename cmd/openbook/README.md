# `openbook` 运维入口

当前提供两个不修改业务数据的命令：

```bash
go run ./cmd/openbook version
go run ./cmd/openbook doctor
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
