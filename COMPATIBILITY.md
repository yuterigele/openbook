# OpenBook Agent 契约兼容说明

本文档记录 `sdk/booking/v1alpha1` 在底座抽取期间的兼容边界。它是计划文档要求的阶段 0 产物，不代表已经冻结公共 `sdk/booking/v1`。

## 当前范围

当前只冻结以下内容：

- 操作名称：`list_services`、`list_staff`、`query_availability`、`create_booking`、`list_my_bookings`、`cancel_booking`、`reschedule_booking`。
- `ExecutionContext` 的可信字段：`merchant_id`、`location_id`、`customer_id`、`principal_id`、`permissions`、`trace_id` 和写操作的 `idempotency_key`。
- 应用错误的稳定前缀：`booking.*`。
- 模型工具结果外层：`tool.result.v1` 的 `schema_version`、`status`、`code`、`summary`、`facts` 和 `suggested_actions`。
- 工具注册元数据：`sdk/toolkit.Registry` 只允许显式注册；每个工具必须声明预约操作、读写模式、权限、结果预算和 Handler。
- Profile 使用 `sdk/profile` 定义并通过显式 Registry 注册；Hair/Beauty 参考实现位于 `profiles/`。

业务请求和响应的内部字段仍是不稳定字段，阶段 2 的通用领域模型完成后才进行 v1 契约评审。

## 可信上下文规则

Host 必须从已验签、已认证的渠道会话构造 `ExecutionContext`。模型参数、顾客消息、HTTP 请求体和工具参数中出现的同名身份字段必须被忽略或拒绝；不得用它们覆盖商户、门店、顾客或权限。

创建、取消和改约必须有顾客身份和幂等键。读操作可以不带顾客身份，但应用层仍需按具体用例执行权限检查。`idempotency_key` 只能由 Host 或可信渠道生成/传入，不能由模型自行决定写入范围。

工具注册还必须满足以下执行边界：

- 只读工具使用 `booking:read`，默认最多执行两次；写工具使用 `booking:write`，注册时强制最多执行一次。
- 未注册、权限不足、上下文不完整或结果 Envelope 不合格的调用都会失败；返回模型前统一映射为安全结果，不暴露 Handler 的原始错误。
- Registry 只接收公开业务参数和 Host 注入的 `ExecutionContext`；Handler 不得从参数中读取或覆盖可信身份字段。

## 调整规则

- `v1alpha1` 可以增加可选业务字段，但必须同步更新测试 Fixture 和本文档。
- 不能删除、改名或复用已发布操作名称、错误码和工具结果外层字段的语义。
- 破坏性变更进入新的 Go 包路径（例如 `sdk/booking/v1` 或后续 `v2`），不通过增加 `version` 字段伪装兼容。
- 领域实体、GORM 模型、SQL 错误、完整顾客资料和内部 ID 不得作为模型工具结果直接暴露。
- 工具结果超出预算时必须分页、分段或返回摘要，不能静默截断半个时间区间或错误消息。

## 后续评审门槛

阶段 2 完成后，使用 Hair 与 Beauty 的真实预约场景走查 Runtime → Tool → Application → Domain 链路，确认业务 DTO、资源分配和错误映射，再决定是否提升为稳定的 `sdk/booking/v1`。
