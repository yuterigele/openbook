# 平台安装排障与升级回滚

本文覆盖 Windows、macOS、Linux 的本地安装排障，以及使用 Compose 不可变镜像的升级和回滚。它补充 [环境分层与发布约定](environments.md)；生产环境仍必须使用独立凭据、备份、维护窗口和回滚演练。

## 1. 所有平台的安装前检查

OpenBook 需要：

- Go 1.25；
- Docker Engine 或 Docker Desktop，以及 Docker Compose v2；
- 修改 `web/` 时使用 Node.js 22；
- 生产部署使用 MySQL 8、Redis 7 和 HTTPS 反向代理。

先执行：

```text
go version
docker version
docker compose version
```

开发环境使用仓库根目录的 `.env`，可从 `.env.example` 复制；staging/production 使用仅部署机保存的 `.env.staging.local` 或 `.env.production.local`。真实 API Key、企业微信 Token/AES Key、数据库密码、JWT Secret 和顾客数据不得进入 Git。

所有平台都可以先跑无凭据 Demo：

```bash
docker compose -f docker-compose.demo.yml config -q
docker compose -f docker-compose.demo.yml up --build
```

Demo 只用于页面和安全降级体验，Stub 不调用业务工具、不写真实预约，也不代表真实模型或企业微信联调已经通过。

## 2. Windows

### 2.1 推荐准备

1. 安装 Docker Desktop，启用 WSL 2 backend，并确认 Docker Desktop 正在运行。
2. 安装 Go 1.25 和 Node.js 22 LTS。
3. 使用 PowerShell 执行 `.ps1` 和 Compose 命令；执行 `.sh` 脚本时使用 WSL 或 Git Bash，不要用 PowerShell 解释 Bash 语法。
4. 仓库路径尽量放在本地磁盘，避免把 Go 缓存和 Docker bind mount 放在权限受限的目录。

标准开发启动：

```powershell
Copy-Item .env.example .env
docker compose up -d mysql redis db-bootstrap
docker compose ps
go run .
```

如果要从 Windows 编译 Linux 服务器二进制：

```powershell
pwsh scripts/build-linux.ps1
pwsh scripts/build-linux.ps1 -Arch arm64 -Output openbook-linux-arm64
```

脚本会设置 `GOOS=linux`、`GOARCH` 和 `CGO_ENABLED=0`，输出文件不是 Windows `.exe`。上传到 Linux 后先执行 `file <binary>`，确认是目标架构的 ELF 文件，再按部署脚本操作。

### 2.2 常见问题

#### Docker 命令找不到或无法连接

```powershell
docker version
docker compose version
docker context ls
docker compose ps
```

如果 Client 有版本、Server 无版本，先启动 Docker Desktop；如果当前 Docker context 指向错误环境，切回正在运行 Docker Engine 的 context。不要为了绕过连接错误删除 Compose 数据卷。

#### 端口 38080、3306 或 6379 被占用

```powershell
Get-NetTCPConnection -State Listen -LocalPort 38080,3306,6379 -ErrorAction SilentlyContinue |
  Select-Object LocalPort,OwningProcess
Get-Process -Id <PID>
```

优先停止确认无关的进程，或只为开发环境修改 `.env`/Compose 端口覆盖；不要把 MySQL/Redis 端口暴露到公网。

#### Bash 脚本提示语法错误

在 WSL 或 Git Bash 中运行：

```bash
bash -n scripts/dx-smoke.sh
bash -n scripts/compose-release.sh
bash scripts/dx-smoke.sh
```

如果文件被编辑器转换了换行符，先检查 Git diff，再用支持 LF 的编辑器保存脚本；不要把 `.env` 内容粘贴到报错日志。

#### Go 测试出现 `Access is denied` 或缓存目录不可写

先确认当前目录和 Go 环境：

```powershell
Get-Location
go env GOCACHE GOPATH GOMODCACHE
```

如果默认缓存目录被其他进程锁定，可在当前 PowerShell 会话使用工作区内的可写缓存：

```powershell
$env:GOCACHE = (Join-Path (Get-Location) '.gocache')
go test -p 1 -vet=off ./... -count=1
```

这只是 Windows 环境排障手段；如果失败信息来自测试断言或编译错误，不能把它归因于缓存权限。

#### `.env` 改了但容器仍使用旧值

Compose 只会向 `environment:` 明确列出的变量注入值。修改配置后强制重新创建应用容器：

```powershell
docker compose up -d --force-recreate app
docker compose logs --tail=100 app
```

不要在日志中用 `docker inspect` 复制完整环境变量；只检查安全的开关、存在性和长度。

## 3. macOS

### 3.1 推荐准备

1. 安装 Docker Desktop、Go 1.25 和 Node.js 22。
2. Apple Silicon 机器先确认 `uname -m` 为 `arm64`；Intel 机器通常为 `x86_64`。
3. 优先使用 Docker Compose，不要因为本机是 arm64 就手工修改业务代码。发布镜像由 CI 负责多架构构建；本地遇到架构提示时先查看镜像是否包含目标架构。

标准开发启动：

```bash
cp .env.example .env
docker compose up -d mysql redis db-bootstrap
docker compose ps
go run .
```

macOS/Linux 编译 Linux 服务器二进制：

```bash
bash scripts/build-linux.sh amd64 openbook-linux-amd64
bash scripts/build-linux.sh arm64 openbook-linux-arm64
```

### 3.2 常见问题

#### Docker Desktop 没有启动或资源不足

```bash
docker version
docker compose ps
docker system df
```

在 Docker Desktop 中适当增加 CPU、内存和磁盘配额，再重试构建。清理镜像前先确认没有生产或其他项目依赖；不要用 `docker compose down -v` 处理普通启动问题，因为它会删除项目数据库卷。

#### Apple Silicon 上出现架构不匹配

```bash
uname -m
docker image inspect <image> --format '{{.Os}}/{{.Architecture}}'
```

优先使用已发布的多架构镜像。若只是本地临时验证，可在 Docker Desktop 配置兼容运行，但应在目标部署架构上重新做一次镜像和性能验收，不能把兼容层结果当作生产验收。

#### 端口冲突或进程未退出

```bash
lsof -nP -iTCP:38080 -sTCP:LISTEN
lsof -nP -iTCP:3306 -sTCP:LISTEN
lsof -nP -iTCP:6379 -sTCP:LISTEN
```

确认进程属于本项目后再停止；只修改开发端口时同步更新访问地址和 Compose 覆盖文件。

#### Shell 脚本权限或换行问题

```bash
git diff --check
bash -n scripts/build-linux.sh
bash -n scripts/compose-release.sh
```

脚本可直接用 `bash <script>` 执行，不需要把可执行权限变化混入无关提交。真实凭据继续放在未跟踪 `.local` 文件或 Secret 管理系统中。

## 4. Linux

### 4.1 推荐准备

1. 安装 Go 1.25、Docker Engine 和 Compose v2 plugin。
2. 将部署用户加入 Docker 组，或明确使用受控的 `sudo docker`；不要把 Docker Socket 暴露给公网服务。
3. 使用独立的生产 env 文件和数据卷；部署主机必须能访问镜像仓库、MySQL、Redis、备份存储和 HTTPS 反向代理。

检查 Docker 和脚本：

```bash
docker version
docker compose version
bash -n scripts/compose-release.sh
bash -n scripts/deploy.sh
```

如果采用源码/二进制部署，先在构建机运行 `scripts/build-linux.sh`；如果采用生产 Compose，使用已构建的不可变 `OPENBOOK_IMAGE`，不要在生产覆盖文件中启用源码 `build`。

### 4.2 常见问题

#### `permission denied` 访问 Docker Socket

```bash
id
docker ps
stat -c '%A %U %G' /var/run/docker.sock
```

将部署用户加入受控 Docker 组后重新登录，或按组织规范使用 `sudo`。不要通过给 Socket 或应用目录设置全局可写权限来“修复”。

#### app 启动但不健康

```bash
docker compose ps
docker compose logs --tail=200 mysql redis db-bootstrap app
curl --fail --max-time 5 http://127.0.0.1:38080/
```

按顺序确认 MySQL、Redis、`db-bootstrap` 和 app。若只修改了环境变量，先执行 `docker compose up -d --force-recreate app`；若迁移失败，保留日志和备份，不能通过删除数据卷重来。

#### MySQL 或 Redis 连接失败

先检查 Compose 解析结果和容器网络，不要输出完整 DSN 或密码：

```bash
docker compose --env-file .env.production.local -f docker-compose.yml -f docker-compose.production.yml config -q
docker compose ps
docker compose logs --tail=100 mysql redis
```

应用容器内使用 Compose 服务名连接，不要把宿主机 `localhost` 当成 MySQL/Redis 地址。 `doctor` 只做配置门禁，不代表网络连接已经成功。

#### systemd/二进制部署重启后没有恢复

如果使用 `scripts/deploy.sh`，先确认二进制架构、路径、端口和日志：

```bash
file /path/to/openbook-linux
ss -tlnp | grep 38080
tail -50 /path/to/app.log
```

该脚本会备份旧二进制并执行健康检查；健康检查失败时保留现场，先排查日志和端口，不要连续覆盖部署。

## 5. 通用诊断顺序

遇到“启动失败”时按这个顺序收集最小证据：

1. `docker compose config -q`：确认变量、文件和服务合并正确；
2. `docker compose ps`：确认最终运行的容器和健康状态；
3. `docker compose logs --tail=200 <service>`：确认最先失败的服务；
4. `curl --fail http://127.0.0.1:38080/`：验证应用 HTTP，不只看容器是否 running；
5. `go run ./cmd/openbook doctor`：检查安全配置门禁；
6. `go test ./... -count=1`：确认源码回归，必要时再运行 MySQL/Redis Tag 测试。

不要把以下现象混为一谈：

| 现象 | 可能范围 | 结论方式 |
| --- | --- | --- |
| Compose 解析失败 | 配置/环境文件 | 修配置后重新 `config -q` |
| 容器 unhealthy | 依赖、迁移、启动参数或应用 | 以最终容器和日志为准 |
| HTTP 不通但容器 running | 监听地址、端口映射或应用已退出重启 | 检查 `ps`、日志和端口 |
| 测试缓存权限失败 | 本机 Go 环境 | 换可写 `GOCACHE` 后重跑 |
| 业务测试断言失败 | 源码或夹具回归 | 不能用环境问题掩盖 |
| Redis 只读保护 | Redis 连续健康检查失败 | 查询可用，写操作应安全拒绝 |

## 6. 升级前检查

> 运行位置说明：下面的 `go run ./cmd/openbook ...` 只适用于同一版本的源码检出目录或源码 Release 压缩包目录。生产不可变镜像只包含 `/app/openbook` 服务进程，不包含 Go、`cmd/openbook` 或 `mysqldump`；只有 Compose 文件的 `/opt/openbook` 部署目录不能执行这些命令。
>
> 部署机只有 Compose 文件时，先将 `COMPOSE_ENV_FILE` 指向实际存在且仅部署机可读的 env 文件，再执行 Compose 检查和容器健康检查。`doctor`、数据库 dry-run 和应用 CLI 备份必须从同一版本源码构建的运维 CLI，或经评审单独分发的运维工具执行；不要把 `.env` 内容、密钥或备份 SQL 粘贴到聊天记录。

升级是“应用镜像 + 兼容数据库变更 + 配置”的组合，不是简单替换容器。生产前完成：

```bash
export COMPOSE_ENV_FILE=.env.production.local
set -a
. "$COMPOSE_ENV_FILE"
set +a
go run ./cmd/openbook doctor
docker compose --env-file "$COMPOSE_ENV_FILE" \
  -f docker-compose.yml -f docker-compose.production.yml config -q
go run ./cmd/openbook migrate -dry-run
```

同时确认：

- 已保存当前镜像引用或 digest，作为 `previous-image`；
- 已完成 MySQL 备份，并在隔离库恢复核验过；
- 已检查迁移是否向前兼容当前应用和回滚镜像；
- 已建立维护窗口和单写者边界，等待 Agent、消息 Worker 和事务完成；
- 已准备 HTTPS、企业微信、模型、MySQL、Redis 和监控配置的回退值；
- 已决定升级失败时是保留现场排查，还是按预案回滚。

旧预约迁移必须先生成 dry-run 报告，并确保 `blocked=0`；具体规则见[旧预约迁移规划](booking-migration.md)。

## 7. 不可变镜像升级与自动回滚

Linux/macOS 或 Windows 的 WSL/Git Bash 中执行生产发布脚本。脚本读取 `COMPOSE_ENV_FILE`，生产 Compose 使用 `docker-compose.production.yml`，只消费 `OPENBOOK_IMAGE`，不重新构建源码：

```bash
export COMPOSE_ENV_FILE=.env.production.local
export OPENBOOK_PULL_IMAGE=1
bash scripts/compose-release.sh deploy \
  ghcr.io/yuterigele/openbook:v1.0.0 \
  ghcr.io/yuterigele/openbook:v0.9.0
```

脚本流程是：拉取目标镜像 → 启动 MySQL/Redis/db-bootstrap/app → 轮询 HTTP 健康检查 → 成功结束；如果新镜像不健康且提供了 `previous-image`，会启动旧镜像并再次检查，然后以非零状态退出，要求发布系统记录这次失败发布。

部署机已经缓存镜像、需要离线演练时：

```bash
export OPENBOOK_PULL_IMAGE=0
bash scripts/compose-release.sh deploy \
  ghcr.io/yuterigele/openbook:v1.0.0 \
  ghcr.io/yuterigele/openbook:v0.9.0
```

健康检查地址和次数可通过 `OPENBOOK_HEALTH_URL`、`OPENBOOK_HEALTH_ATTEMPTS`、`OPENBOOK_HEALTH_INTERVAL_SECONDS` 调整；调整时要在发布记录中说明原因。

升级成功后核验：

```bash
docker compose --env-file .env.production.local \
  -f docker-compose.yml -f docker-compose.production.yml ps
curl --fail --max-time 5 http://127.0.0.1:38080/
docker compose --env-file .env.production.local logs --tail=200 app
```

再执行一次只读业务检查、关键指标检查和消息/Outbox 处理检查。不要只依据 `docker compose up` 的退出码判断发布完成。

## 8. 手工回滚与数据库边界

应用镜像不健康时可以单独回滚到上一个镜像：

```bash
export COMPOSE_ENV_FILE=.env.production.local
bash scripts/compose-release.sh rollback \
  ghcr.io/yuterigele/openbook:v0.9.0
```

回滚后仍要执行 `ps`、HTTP、日志和只读业务核验。脚本只回退应用镜像，不会逆向数据库迁移。

如果新版本已经写入新字段、新表或新数据，不能只切回旧镜像；必须根据迁移设计选择：

1. 使用已经验证过的向后兼容旧镜像继续服务；
2. 在维护窗口执行经过评审的反向迁移；或
3. 从备份恢复到隔离目标，再按数据核验和补录方案切换。

不要把 `docker compose down -v` 当作升级修复命令，它会删除 MySQL/Redis 数据卷。除非明确是在销毁本地 Demo 数据，否则不执行带 `-v` 的清理。

回滚记录至少保留：目标镜像、旧镜像、数据库 Schema 版本、备份位置、开始/结束时间、健康检查结果、错误日志摘要和是否发生数据写入。凭据和顾客原文不应进入记录。

## 9. 发布后问题报告模板

```text
环境：development / staging / production
平台：Windows / macOS / Linux，架构：amd64 / arm64
版本或镜像 digest：
上一个可用版本：
Compose 文件和配置文件名（不含秘密）：
docker compose config -q：通过 / 失败
docker compose ps：
HTTP 健康检查：通过 / 失败
最先失败的服务和日志摘要：
是否执行过迁移：是 / 否，dry-run 结果：
是否写入业务数据：是 / 否 / 未知
是否回滚：是 / 否，回滚后的核验：
```

只附脱敏日志、错误码和时间范围；不要附完整 `.env`、DSN、Token、AES Key、手机号或顾客对话。
