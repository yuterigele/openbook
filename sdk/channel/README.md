# Channel SDK

`github.com/yuterigele/openbook/sdk/channel` 是渠道适配器与 Agent Host 之间的最小消息契约。

适配器负责验签、去重、解析渠道供应商的消息，然后构造 `InboundMessage`。Host 负责从已认证的会话构造 `v1alpha1.ExecutionContext`，再调用 `Handler`：

```go
message, err := channel.NewInbound(
    "wecom-msg-123",
    "wecom_shop-1_customer-1",
    channel.KindWeComCustomerService,
    "明天下午想剪发",
    time.Now(),
)
if err != nil {
    return err
}

reply, err := handler.Handle(ctx, message, trustedContext)
```

`InboundMessage` 不包含 `merchant_id`、`location_id`、`customer_id`、权限或幂等键。上述字段只能来自 Host 注入的可信上下文；渠道正文中出现同名字段也不能覆盖它们。

当前冻结的外层版本是 `channel.message.v1`，支持 `web_chat`、`wecom_customer_service` 和 `wecom_external_contact`。渠道供应商的签名回调、游标、发送 API 和重试机制仍属于适配器内部，不属于这个 SDK 契约。
