# 新增行业 Profile

Profile 只描述行业差异，不实现顾客身份、权限、事务、锁、冲突判断或数据库访问。预约核心始终使用同一套 `Service`、`Staff`、`Resource`、`Booking` 和 `Allocation` 规则。

## 1. 创建定义包

在 `profiles/<id>/profile.go` 创建 `Definition()`，至少提供：

- 稳定的 `ID` 和展示名称；
- `Terms` 中的人员、服务和资源行业术语；
- 30 分钟或 15 分钟等起约粒度；
- 独占资源类型；
- 不同服务时长、前后缓冲和资源需求；
- 顾客必填字段、回复模板及模板变量。

资源需求必须表达真实占用。例如美容护理可以同时需要房间和床位，美甲光疗可以同时需要工位和光疗灯；不要只在提示词中描述这些约束。

## 2. 注册 Profile

在 `profiles/registry.go` 的 `NewReferenceRegistry` 中显式注册定义：

```go
for _, definition := range []profile.Definition{
    hair.Definition(),
    beauty.Definition(),
    nail.Definition(),
    fitness_coach.Definition(),
} {
    if err := registry.Register(definition); err != nil {
        return nil, err
    }
}
```

不要使用 Go `plugin`、动态路径加载或让 Profile 自己执行 SQL。显式注册能在启动和测试阶段发现重复 ID、未知资源和不安全模板。

## 3. 添加契约测试

在 `profiles/profile_contract_test.go` 至少覆盖：

1. Profile 能通过 `Definition.Validate`；
2. 服务 ID、资源类型 ID 和模板变量稳定且无重复；
3. 典型服务的时长与联合资源需求符合行业事实；
4. 必填顾客字段和回复模板完整；
5. 资源冲突交给领域层验证，而不是由 Profile 绕过应用服务处理。

运行：

```bash
go test ./profiles ./sdk/profile -count=1
go test ./... -count=1
```

## 4. 完成验收

新增 Profile 后，先用 `profiles.NewReferenceRegistry` 验证注册，再用同一套 Application 工具执行查询、创建、取消和改约场景。不得为新行业复制一套工具或存储实现；如果需要新的业务规则，应先评估是否属于通用领域模型。
