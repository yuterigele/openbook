# 扩展 Tool 与 Channel

本文给已经能启动 OpenBook 的开发者，说明如何新增一个 Agent 工具或消息渠道。示例以当前仓库的 `sdk/toolkit`、`sdk/channel` 和 Eino `tool.InvokableTool` 为准；其中业务规则仍必须落在确定性的 Application、领域层或存储层，不能只写在 Prompt 里。

## 1. 先理解两条扩展边界

OpenBook 当前同时保留两层接口：

| 层 | 作用 | 主要代码 |
| --- | --- | --- |
| `sdk/toolkit` | Runtime 无关的工具白名单、权限、重试和结果 Envelope | `sdk/toolkit/registry.go`、`sdk/toolkit/result.go` |
| Eino Tool Catalog | 把 Eino 工具接入现有 Agent，并在调用前检查契约 | `internal/agent/tool_catalog.go`、`tools/` |

新增工具需要进入显式白名单，不能通过动态路径、Go `plugin`、模型自行发现或任意 SQL 暴露能力。新增渠道则由适配器负责验签、解密（如果有）、去重和供应商发送；`sdk/channel` 只承载已经验签后的最小入站消息。

当前 `sdk/booking/v1alpha1` 只接受 `AllOperations()` 中的操作名称。一个新工具如果只是已有操作的另一种参数/展示方式，可以复用对应操作；如果它代表真正的新业务动作，必须先扩展操作契约、错误语义、Application、测试和 [COMPATIBILITY.md](../../COMPATIBILITY.md)，不能用一个不相干的旧操作名称伪装兼容。

## 2. 新增 Tool：推荐流程

### 2.1 先写清楚工具契约

在写代码前回答以下问题：

1. 工具是只读还是写入预约状态？
2. 它对应哪个 `v1alpha1.Operation`？当前 Application 是否已实现？
3. 哪些是公开业务参数，哪些必须由 Host 注入？
4. 成功、参数不完整、无权访问、冲突、服务不可用和结果未知时分别返回什么？
5. 是否会产生副作用？如果会，幂等键、事务和并发保护在哪里完成？

工具参数只允许放业务字段。例如预约创建可以接收 `service_id`、`staff_id`、`start_at`，但不能接收以下可信字段：

```text
merchant_id location_id customer_id principal_id permissions trace_id idempotency_key
```

这些字段来自 `v1alpha1.ExecutionContext`，不能由模型或顾客消息提供。写工具必须使用 `booking:write` 和一次执行策略；只读工具使用 `booking:read`，通常最多两次执行。

### 2.2 生成最小骨架

```bash
go run ./cmd/openbook generate tool customer_note
```

命令会生成：

```text
tools/customer_note.go
tools/customer_note_test.go
```

生成器只接受小写字母开头的 lower snake case 标识符，已有文件会拒绝覆盖。生成的 Tool 返回“尚未实现”，不能直接注册到生产 Agent；先补齐参数校验、权限边界、确定性业务实现和测试。

### 2.3 现有 Eino Agent 的实现方式

当前主 Agent 中的工具实现 `tools/` 遵循 Eino 的两个方法：

```go
type CustomerNoteTool struct{}

func (*CustomerNoteTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: "customer_note",
		Desc: "读取当前顾客允许查看的备注",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{}),
	}, nil
}

func (*CustomerNoteTool) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	// 1. 只解析公开业务参数；拒绝可信身份字段。
	// 2. 从受信上下文取得门店/顾客身份。
	// 3. 调用 Application 或确定性的业务服务。
	// 4. 返回模型可读且不包含敏感信息的结果。
	return "...", nil
}
```

这段代码是结构示例，不应直接复制其中的业务结果。`Info().Name` 必须与注册名完全一致，`ParamsOneOf` 应列出真实参数和必填约束，`InvokableRun` 不能把数据库原始错误、手机号、Token 或内部 ID 无限制地交给模型。

### 2.4 加入显式 Tool Catalog

在 `internal/agent/tool_catalog.go` 的 `registrations` 中增加描述和实现。只读工具使用 `readDescriptor`，写工具使用 `writeDescriptor`：

```go
{
	readDescriptor(
		"customer_note",
		"读取当前顾客允许查看的备注",
		v1alpha1.OperationListMyBookings,
	),
	&tools.CustomerNoteTool{},
},
```

上面的操作名称只是示例。实际开发必须选择语义匹配的操作；没有匹配操作时先修改契约和 Application。注册时会检查：

- 名称是否为 lower snake case；
- 描述是否非空；
- 操作是否在白名单中；
- 读写模式和权限是否匹配；
- 写工具是否严格使用 `MaxAttempts=1`；
- Eino `Info().Name` 是否与 Descriptor 相同；
- 工具是否真的实现 `tool.InvokableTool`。

如果工具要走灰度 `v1alpha1.Application`，还要在 `applicationToolName` 中加入名称，并在 Application 的 `Execute` 中实现对应操作。这样 Agent Runtime 不会直接依赖旧工具的数据库实现。若工具只是旧链路暂时保留，则必须仍通过 Catalog 的上下文和权限包装器。

### 2.5 使用 Runtime 无关的 Registry

需要写 Starter、独立 Host 或测试夹具时，可以直接使用 `sdk/toolkit.Registry`。它的 Handler 接收可信上下文和原始业务参数：

```go
registry := toolkit.NewRegistry()
err := registry.Register(toolkit.ToolSpec{
	Name:        "list_services",
	Description: "列出当前行业可预约的服务项目",
	Operation:   v1alpha1.OperationListServices,
	Mode:        toolkit.ModeRead,
	Permission:  v1alpha1.PermissionBookingRead,
	Retry:       toolkit.ReadRetryPolicy(),
	Handler: func(ctx context.Context, trusted v1alpha1.ExecutionContext, parameters json.RawMessage) (toolkit.Result, error) {
		if err := trusted.ValidateFor(v1alpha1.OperationListServices); err != nil {
			return toolkit.FromError(err), err
		}
		// parameters 只解析服务筛选等公开业务参数。
		return toolkit.NewOK("services.listed", "服务列表已查询", map[string]any{
			"services": []any{},
		}), nil
	},
})
```

真实业务不应在 Handler 里绕过 Application 直接操作全局数据库。可以参考 [Starter Booking](../../examples/starter-booking/README.md) 的 `NewStarterRegistry` 和 `applicationHandler`：Handler 校验参数后把调用转发到 `v1alpha1.Application`，Application 再调用预约内核。

### 2.6 返回安全结果

模型消费的是 `tool.result.v1`，不是领域实体或 SQL 错误。结果至少要有：

```json
{
  "schema_version": "tool.result.v1",
  "status": "ok",
  "code": "services.listed",
  "summary": "服务列表已查询",
  "facts": {"services": []},
  "suggested_actions": []
}
```

使用 `toolkit.NewOK`、`toolkit.NewNeedsInput` 或 `toolkit.FromError` 生成结果，并让 Registry 执行 `Validate`。默认结果预算是最多 32 个字段、6000 个 Unicode 字符；时间区间使用 `toolkit.TimeFact`，必须包含 RFC3339 的 `start_at`、`end_at`、`timezone` 和面向顾客的 `display`。

错误处理应遵循以下映射：

| 情况 | 应返回 | 不应返回 |
| --- | --- | --- |
| 参数不完整 | `needs_input`，说明需要补什么 | “SQL syntax error” |
| 无权或跨顾客访问 | `forbidden` | 另一顾客的预约是否存在 |
| 时段冲突 | `conflict`，建议查询其他时段 | 让模型自行重试写入 |
| Redis/MySQL/模型暂不可用 | `unavailable` 或 `maintenance` | 假装已经预约成功 |
| 写入结果无法确认 | `unknown`，引导查询本人预约或转人工 | 再次自动提交同一副作用 |

## 3. 新增 Channel：推荐流程

### 3.1 适配器与 SDK 的职责

渠道适配器处理供应商协议；SDK 只处理跨渠道都需要的稳定外层：

```text
原始 Webhook
  → 验签/解密
  → message_id 去重和可靠接收
  → 解析 session_id、文本和接收时间
  → Host 根据认证会话构造 ExecutionContext
  → channel.NewInbound
  → Handler.Handle
  → 适配器调用供应商发送 API
```

`InboundMessage` 当前字段是 `schema_version`、`message_id`、`session_id`、`kind`、`text` 和 `received_at`。它明确不包含 `merchant_id`、`location_id`、`customer_id`、权限或幂等键。

### 3.2 生成渠道骨架

```bash
go run ./cmd/openbook generate channel my_channel
```

命令会生成：

```text
sdk/channel/my_channel.go
sdk/channel/my_channel_test.go
```

生成模板默认使用 `KindWebChat`，只演示怎样创建消息，不代表已经完成供应商验签、去重、认证、发送和重试。真实渠道要使用已有的 `Kind`，或先扩展 `Kind` 白名单及对应契约测试。

### 3.3 构造入站消息

适配器完成验签和去重后，构造 `InboundMessage`：

```go
message, err := channel.NewInbound(
	verified.MessageID,
	verified.SessionID,
	channel.KindWebChat,
	verified.Text,
	verified.ReceivedAt,
)
if err != nil {
	return err
}
```

`NewInbound` 会校验：

- `message_id` 和 `session_id` 非空且不超过 256 个字符；
- `kind` 属于显式白名单；
- 正文非空且不超过 8000 个 Unicode 字符；
- `received_at` 非零；
- 外层版本为 `channel.message.v1`。

供应商签名、原始 XML/JSON、游标、发送凭据和重试控制信息留在适配器或可靠 Inbox，不要塞入 SDK 消息对象。`message_id` 用于重放保护，`session_id` 用于会话串联，二者不是顾客身份。

### 3.4 注入可信上下文并调用 Handler

`Handler` 的实际接口是：

```go
type Handler interface {
	Handle(context.Context, InboundMessage, v1alpha1.ExecutionContext) (Reply, error)
}
```

最小 Host 调用形态如下：

```go
trusted, err := resolver.Resolve(ctx, verifiedSession)
if err != nil {
	return err
}
if err := trusted.Validate(); err != nil {
	return err
}

reply, err := handler.Handle(ctx, message, trusted)
if err != nil {
	return err
}
return sender.Send(ctx, verifiedSession, reply.Text)
```

这里的 `resolver`、`sender` 和 `verifiedSession` 是适配器/Host 的实现，不是 SDK 提供的类型。写预约时，Host 还必须构造可信 `IdempotencyKey`；不要让模型从消息正文中生成或覆盖它。

`channel.HandlerFunc` 会再次校验消息、上下文和回复。回复文本不能为空，且不能超过 6000 个 Unicode 字符。发送失败要由渠道可靠投递策略处理，不应把一次发送失败变成重复创建预约。

### 3.5 渠道安全检查清单

| 阶段 | 必须验证 | 失败行为 |
| --- | --- | --- |
| 请求入口 | 签名、时间窗口、解密和请求体大小 | 拒绝，不进入 Agent |
| 消息接收 | `message_id` 去重，必要时持久化 Inbox/cursor | 已处理消息返回幂等成功，不重复执行业务 |
| 会话解析 | 渠道会话与商户/门店/顾客的服务端映射 | 缺少可信身份则拒绝 |
| Agent 调用 | `ExecutionContext.ValidateFor` 和权限 | 不调用业务 Handler |
| 回复发送 | 6000 字符限制、供应商错误和重试 | 记录失败，按渠道策略重试 |
| 观测 | `trace_id`、message ID、session ID 和安全错误码 | 不记录 Token、AES Key、完整秘密 |

企业微信渠道应优先复用仓库已有的 `wecom/` 验签解密、门店路由和可靠消息入口；不要重新实现一套绕过 inbox、限流或顾客绑定的“简化回调”。

## 4. 必须补的测试

### Tool 测试

至少覆盖：

```text
正常业务参数 → 成功结果
缺少业务参数 → needs_input / 稳定错误
伪造 merchant_id/customer_id → 拒绝且不覆盖可信上下文
缺少权限 → forbidden
重复注册或错误元数据 → 启动/注册阶段失败
非法结果、敏感字段、超预算结果 → Registry 拒绝
写工具重复请求 → 由幂等/事务逻辑返回同一结果，不新增副作用
冲突、维护、未知结果 → 对应安全状态，不回复成功
```

### Channel 测试

至少覆盖：

```text
合法消息 → channel.message.v1
未知 kind、错误 schema、空正文、超长正文 → 拒绝
消息对象不包含可信身份字段
缺失商户/门店/trace_id → Handler 拒绝
正文中的 customer_id 等字段不能改变 trusted context
回复为空或超过 6000 字符 → 拒绝
重复 message_id → 不重复进入业务处理
```

运行相关测试：

```bash
go test ./sdk/toolkit ./sdk/channel ./internal/agent -count=1
go test ./examples/starter-booking -count=1
go test ./... -count=1
go vet ./...
git diff --check
```

新增公开行为、环境变量、迁移或部署步骤时，同步更新 [开发者指南](../DEVELOPER_GUIDE.md)、README 或部署文档。提交前确认测试是真实执行的；没有外部模型、企业微信或 MySQL/Redis 时，要在 PR 中明确说明跳过的联调范围。
