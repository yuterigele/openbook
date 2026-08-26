# 贡献指南

感谢参与 OpenBook。请先阅读 [README](README.md)、[环境分层与发布约定](docs/deployment/environments.md) 和 [契约兼容说明](COMPATIBILITY.md)，再开始修改。

## 本地准备

- Go 1.25；涉及 Web 前端时使用 Node.js 22。
- 需要真实存储验证时启动 Docker Compose 的 MySQL 和 Redis；没有外部模型或企业微信凭据时使用 Stub/Mock 模式。
- 真实密钥、数据库密码、企业微信 Token、AES Key、JWT Secret 和顾客数据只能放在本地未跟踪配置或密钥管理系统中。

## 提交前检查

Go 修改至少执行：

```bash
gofmt -w <changed-go-files>
go test ./... -count=1
go vet ./...
git diff --check
```

前端修改执行：

```bash
cd web
npm test
```

涉及容器、环境变量或启动流程时，还应执行 Compose 配置校验和与风险相称的健康检查。真实 MySQL/Redis、模型或企业微信测试如果没有可用环境，可以跳过，但 Pull Request 必须说明跳过原因，不能把跳过写成通过。

## 修改边界

- 业务规则放在确定性的 `tools/`、`internal/booking/` 或 `storage/` 层，不要只依赖 Prompt。
- Agent 只能调用显式注册的业务工具；不要给顾客消息开放 Shell、文件系统、任意 SQL 或管理端能力。
- 商户、门店、顾客和权限由服务端可信上下文注入；不要从模型参数或请求体读取同名身份字段。
- 创建、取消和改约必须保留事务、幂等、锁、归属校验和结果未知保护。
- 修改公开行为、环境变量、迁移或部署流程时，同步更新 README、`.env.example` 或 `docs/`。

## Pull Request

Pull Request 请说明：

1. 修改解决的问题和影响范围；
2. 新增或改变的 API、配置、数据库迁移和兼容性边界；
3. 已执行的测试、未执行的测试及其原因；
4. 是否需要手动部署、数据备份或回滚步骤。

保持提交小而聚焦，不要覆盖无关的工作区修改。涉及安全边界、数据迁移或写路径时，请补充成功、失败、越权、重复调用和边界输入测试。
