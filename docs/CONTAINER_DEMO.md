# Container Demo

OpenBook 的容器化目标是可复现的本地 Demo，而不是生产集群部署。默认组合启动应用、MySQL 8 和 Redis 7，并以 mock 模式记录回复，不会向企业微信发送消息。

## 启动

1. 在项目根目录创建 `.env`，至少填写 `OPENAI_API_KEY`；没有 API Key 也能启动，但会进入 stub 降级模式，无法完成真实 Agent 工具调用。
2. 修改默认后台密码，以及 `MYSQL_APP_PASSWORD`。
3. 启动：

```powershell
docker compose up --build
```

访问 `http://localhost:38080`。首次启动会初始化数据库和示例店铺数据。

## 本地演示安全边界

- `AGENT_REPLY_MODE=mock` 是默认值；回复只写入事件记录，适合演示。
- Docker Compose 中的 HTTP、MySQL、Redis 端口只绑定到 `127.0.0.1`，不能直接用于公网环境。
- 应用容器使用专用 MySQL 账号；启动时由一次性 `db-bootstrap` 服务创建或校正其权限，已有数据卷也适用。该账号仅覆盖运行时读写和当前自动迁移所需的建表/索引操作，不使用 `root`。
- `.env.example` 只保留占位符，真实 API Key、企业微信凭据和 JWT 密钥只能放在本机 `.env` 或部署平台的 Secret 中。

## 停止与重置

```powershell
docker compose down
docker compose down -v
```

第二条命令会删除本地 MySQL 与应用卷，下一次启动会重新初始化 Demo 数据。

## 为什么暂不提供 K8s 清单

当前优先验证 Agent 业务闭环和容器化 Demo。进入真实试点后，再基于实际域名、TLS、Secret 管理、持久卷、数据库托管和观测体系编写 Kubernetes 部署清单，避免维护一套未经使用的 YAML。
