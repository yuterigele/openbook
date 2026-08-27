//go:build external_model_integration

package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"

	"github.com/yuterigele/openbook/chatmodel"
	"github.com/yuterigele/openbook/intent"
	legacybooking "github.com/yuterigele/openbook/internal/booking/legacy"
	"github.com/yuterigele/openbook/sdk/booking/v1alpha1"
	"github.com/yuterigele/openbook/storage"
)

// TestAgentApplicationRuntimeExecutesExternalModelCreate 验证真实模型能够驱动
// Agent 工具循环，并经 Application 适配器完成一次实际 SQLite 预约写入。
//
// 该测试必须显式使用 external_model_integration build tag，避免普通测试或
// CI 在没有显式 Tag 时产生外部模型调用和费用。测试只在显式 Tag 下读取
// .env；不会把凭据写入日志，没有选中提供商凭据时只安全跳过。
func TestAgentApplicationRuntimeExecutesExternalModelCreate(t *testing.T) {
	loadExternalModelEnv()
	provider := requireExternalProvider(t)
	if provider == "" {
		t.Skip("未配置 OPENBOOK_LLM_CHAIN 中的外部模型凭据")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	model, used, _, err := chatmodel.NewModelWithFallback[*schema.Message](ctx)
	if err != nil {
		t.Fatalf("初始化外部模型失败: %v", err)
	}
	if used == chatmodel.ProviderStub {
		t.Fatalf("外部模型初始化后意外进入 Stub 降级模式；选定提供商=%s", provider)
	}

	storage.SetupTestDB(t)
	shop := storage.MakeShop(t, "external-model-shop", "")
	barber := storage.MakeBarber(t, "external-model-barber", shop.ID, "Tony")
	customer := storage.MakeCustomer(t, "可信联调顾客", 0, 0)
	phone := "13800000001"
	if err := storage.DB.Model(&storage.Customer{}).Where("id = ?", customer.ID).Updates(map[string]any{
		"phone":            phone,
		"external_user_id": "external-model-customer",
	}).Error; err != nil {
		t.Fatalf("保存联调顾客身份失败: %v", err)
	}

	// 真实模型联调不验证短信供应商；取消当前进程中的该开关，避免测试
	// 因验证码外部依赖中断。原值在测试结束后恢复。
	unsetEnvForExternalModelTest(t, "SMS_VERIFICATION_ENABLED")
	// 本测试使用隔离 SQLite，且不初始化 Redis；明确关闭生产 Redis 强制门禁，
	// 避免仓库根目录 .env 的生产配置让写入在锁层安全失败。生产代码的门禁
	// 行为由 lock 包测试和真实部署验收覆盖。
	t.Setenv("APP_ENV", "development")
	t.Setenv("REDIS_REQUIRED", "0")

	application := legacybooking.NewApplication(func(ctx context.Context, trusted v1alpha1.ExecutionContext) (legacybooking.CustomerIdentity, error) {
		resolved, err := storage.GetCustomerByID(ctx, trusted.CustomerID)
		if err != nil {
			return legacybooking.CustomerIdentity{}, err
		}
		return legacybooking.CustomerIdentity{
			Name: resolved.Name, Phone: resolved.Phone,
			OpenID: resolved.WechatOpenID, ExternalUserID: resolved.ExternalUserID,
		}, nil
	})
	audited := &auditedApplication{delegate: application}
	agent, err := buildTypedWithModel(
		ctx, intent.NewClassifyTool(intent.NewClassifier()), model, used, audited,
	)
	if err != nil {
		t.Fatalf("组装外部模型 Agent 失败: %v", err)
	}

	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatalf("加载 Asia/Shanghai 失败: %v", err)
	}
	date := time.Now().In(loc).AddDate(0, 0, 1).Format("2006-01-02")
	trusted := v1alpha1.ExecutionContext{
		MerchantID: shop.ID, LocationID: shop.ID, CustomerID: customer.ID, PrincipalID: "external-model-test",
		Permissions: []v1alpha1.Permission{v1alpha1.PermissionBookingRead, v1alpha1.PermissionBookingWrite},
		TraceID:     "external-model-trace", IdempotencyKey: "external-model-idempotency",
	}

	runner := adk.NewRunner(ctx, adk.RunnerConfig{Agent: agent})
	prompt := fmt.Sprintf(
		"请直接帮我完成预约：%s 14:00，Tony 师傅，服务是剪发。顾客姓名是可信联调顾客，手机号是 %s。先查询空档，确认可约后再创建预约。",
		date, phone,
	)
	events := runner.Run(v1alpha1.WithExecutionContext(ctx, trusted), []adk.Message{schema.UserMessage(prompt)})

	var final string
	for {
		event, ok := events.Next()
		if !ok {
			break
		}
		if event.Err != nil {
			t.Fatalf("外部模型 Agent 执行失败: %v", event.Err)
		}
		if event.Output == nil || event.Output.MessageOutput == nil {
			continue
		}
		message, err := event.Output.MessageOutput.GetMessage()
		if err != nil {
			t.Fatalf("读取 Agent 事件失败: %v", err)
		}
		if event.Output.MessageOutput.Role == schema.Assistant && message != nil && message.Content != "" {
			final = message.Content
		}
	}
	if strings.TrimSpace(final) == "" {
		t.Fatal("外部模型 Agent 没有产生最终回复")
	}

	calls := audited.Calls()
	queryIndex := operationIndex(calls, v1alpha1.OperationQueryAvailability)
	createIndex := operationIndex(calls, v1alpha1.OperationCreateBooking)
	if queryIndex < 0 {
		t.Fatalf("外部模型没有先调用 query_schedule；调用序列=%v 参数=%v", operationsOf(calls), safeCallParameters(calls))
	}
	if createIndex < 0 {
		t.Fatalf("外部模型没有调用 create_appointment；调用序列=%v 参数=%v", operationsOf(calls), safeCallParameters(calls))
	}
	if queryIndex > createIndex {
		t.Fatalf("create_appointment 早于 query_schedule；调用序列=%v 参数=%v", operationsOf(calls), safeCallParameters(calls))
	}

	createCall := calls[createIndex]
	var parameters struct {
		BarberName string `json:"barber_name"`
		Date       string `json:"date"`
		Time       string `json:"time"`
	}
	if err := json.Unmarshal(createCall.Parameters, &parameters); err != nil {
		t.Fatalf("解析 create_appointment 参数失败: %v", err)
	}
	if parameters.BarberName == "" || parameters.Date == "" || parameters.Time == "" {
		t.Fatalf("create_appointment 缺少业务时间参数；参数=%v", safeCallParameters(calls))
	}
	if parameters.Date != date {
		t.Fatalf("模型提交了非请求日期：got=%q want=%q；参数=%v", parameters.Date, date, safeCallParameters(calls))
	}
	var rawParameters map[string]json.RawMessage
	if err := json.Unmarshal(createCall.Parameters, &rawParameters); err != nil {
		t.Fatalf("解析 create_appointment 原始参数失败: %v", err)
	}
	for _, forbidden := range []string{"merchant_id", "location_id", "customer_id", "principal_id", "permissions", "trace_id", "idempotency_key"} {
		if _, exists := rawParameters[forbidden]; exists {
			t.Fatalf("模型参数携带可信字段 %q: %s", forbidden, string(createCall.Parameters))
		}
	}

	var appointment storage.Appointment
	if err := storage.DB.Where("shop_id = ? AND barber_name = ? AND date = ? AND time = ?", shop.ID, parameters.BarberName, parameters.Date, parameters.Time).First(&appointment).Error; err != nil {
		t.Fatalf("读取外部模型创建的预约失败: %v；模型提交参数=%v", err, safeCallParameters(calls))
	}
	if appointment.BarberID != barber.ID || appointment.CustomerID != customer.ID || appointment.Customer != customer.Name {
		t.Fatalf("预约归属不正确: barber_id=%q customer_id=%q customer=%q", appointment.BarberID, appointment.CustomerID, appointment.Customer)
	}
	var persistedCustomer storage.Customer
	if err := storage.DB.Where("id = ?", appointment.CustomerID).First(&persistedCustomer).Error; err != nil {
		t.Fatalf("读取预约顾客失败: %v", err)
	}
	if persistedCustomer.Phone != phone {
		t.Fatalf("顾客手机号未使用可信资料: %q", persistedCustomer.Phone)
	}

	for _, got := range audited.Contexts() {
		if got.MerchantID != trusted.MerchantID || got.LocationID != trusted.LocationID || got.CustomerID != trusted.CustomerID || got.TraceID != trusted.TraceID || got.IdempotencyKey != trusted.IdempotencyKey {
			t.Fatalf("Application 收到的可信上下文错误: %+v", got)
		}
	}
}

// loadExternalModelEnv 兼容 go test 在包目录运行、而 .env 位于仓库根目录
// 的情况。显式 ENV_FILE 仍优先，环境变量不会被 .env 覆盖。
func loadExternalModelEnv() {
	if strings.TrimSpace(os.Getenv("ENV_FILE")) != "" {
		chatmodel.LoadEnv()
		return
	}
	directory, err := os.Getwd()
	if err != nil {
		chatmodel.LoadEnv()
		return
	}
	for {
		candidate := filepath.Join(directory, ".env")
		if _, statErr := os.Stat(candidate); statErr == nil {
			_ = os.Setenv("ENV_FILE", candidate)
			chatmodel.LoadEnv()
			_ = os.Unsetenv("ENV_FILE")
			return
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			break
		}
		directory = parent
	}
	chatmodel.LoadEnv()
}

// auditedApplication 只记录 Application 外层调用，业务结果仍由真实 legacy
// Application 产生，避免测试通过一个假的写入实现掩盖数据库路径问题。
type auditedApplication struct {
	mu       sync.Mutex
	delegate v1alpha1.Application
	calls    []v1alpha1.Call
	contexts []v1alpha1.ExecutionContext
}

func (a *auditedApplication) Execute(ctx context.Context, trusted v1alpha1.ExecutionContext, call v1alpha1.Call) (v1alpha1.Response, error) {
	a.mu.Lock()
	a.calls = append(a.calls, v1alpha1.Call{
		Operation:  call.Operation,
		Parameters: append(json.RawMessage(nil), call.Parameters...),
	})
	a.contexts = append(a.contexts, trusted)
	a.mu.Unlock()
	return a.delegate.Execute(ctx, trusted, call)
}

func (a *auditedApplication) Calls() []v1alpha1.Call {
	a.mu.Lock()
	defer a.mu.Unlock()
	result := make([]v1alpha1.Call, len(a.calls))
	copy(result, a.calls)
	return result
}

func (a *auditedApplication) Contexts() []v1alpha1.ExecutionContext {
	a.mu.Lock()
	defer a.mu.Unlock()
	result := make([]v1alpha1.ExecutionContext, len(a.contexts))
	copy(result, a.contexts)
	return result
}

func requireExternalProvider(t *testing.T) string {
	t.Helper()
	active := strings.TrimSpace(os.Getenv("OPENBOOK_LLM_CHAIN"))
	if active == "" {
		active = "deepseek,openai,ark"
	}
	configuredAnywhere := false
	for _, provider := range strings.Split(active, ",") {
		provider = strings.ToLower(strings.TrimSpace(provider))
		if provider == "" {
			continue
		}
		if providerConfigured(provider) {
			return provider
		}
	}
	for _, provider := range []string{"deepseek", "openai", "ark"} {
		if providerConfigured(provider) {
			configuredAnywhere = true
			break
		}
	}
	if configuredAnywhere {
		t.Fatalf("已配置外部模型凭据，但 OPENBOOK_LLM_CHAIN 中没有对应提供商")
	}
	return ""
}

func providerConfigured(provider string) bool {
	switch provider {
	case "deepseek":
		return strings.TrimSpace(os.Getenv("DEEPSEEK_API_KEY")) != ""
	case "openai":
		return strings.TrimSpace(os.Getenv("OPENAI_API_KEY")) != ""
	case "ark":
		return strings.TrimSpace(os.Getenv("ARK_API_KEY")) != ""
	default:
		return false
	}
}

func operationIndex(calls []v1alpha1.Call, operation v1alpha1.Operation) int {
	for index, call := range calls {
		if call.Operation == operation {
			return index
		}
	}
	return -1
}

func operationsOf(calls []v1alpha1.Call) []v1alpha1.Operation {
	result := make([]v1alpha1.Operation, 0, len(calls))
	for _, call := range calls {
		result = append(result, call.Operation)
	}
	return result
}

func safeCallParameters(calls []v1alpha1.Call) []string {
	result := make([]string, 0, len(calls))
	for _, call := range calls {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(call.Parameters, &fields); err != nil {
			result = append(result, string(call.Parameters))
			continue
		}
		for _, sensitive := range []string{"phone", "customer", "phone_verification_code"} {
			if _, exists := fields[sensitive]; exists {
				fields[sensitive] = json.RawMessage(`"<redacted>"`)
			}
		}
		encoded, err := json.Marshal(fields)
		if err != nil {
			result = append(result, "<unavailable>")
			continue
		}
		result = append(result, string(encoded))
	}
	return result
}

func unsetEnvForExternalModelTest(t *testing.T, name string) {
	t.Helper()
	old, existed := os.LookupEnv(name)
	if err := os.Unsetenv(name); err != nil {
		t.Fatalf("清理测试环境变量 %s 失败: %v", name, err)
	}
	t.Cleanup(func() {
		if existed {
			_ = os.Setenv(name, old)
			return
		}
		_ = os.Unsetenv(name)
	})
}
