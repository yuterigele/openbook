//go:build mysql_integration

package agent

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"

	"github.com/yuterigele/openbook/chatmodel"
	"github.com/yuterigele/openbook/intent"
	legacybooking "github.com/yuterigele/openbook/internal/booking/legacy"
	"github.com/yuterigele/openbook/sdk/booking/v1alpha1"
	"github.com/yuterigele/openbook/storage"
)

func TestAgentApplicationRuntimeExecutesLegacyCreateMySQL(t *testing.T) {
	if os.Getenv("MYSQL_DSN") == "" && os.Getenv("MYSQL_HOST") == "" {
		t.Skip("设置 MYSQL_DSN 或 MYSQL_HOST 后运行 MySQL 集成测试")
	}
	db, err := storage.InitDB(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	defer func() { storage.DB = nil }()

	shopID := "agent-mysql-" + uuid.NewString()
	barberID := "barber-" + uuid.NewString()
	barberName := "Tony-" + uuid.NewString()[:8]
	customerID := "customer-" + uuid.NewString()
	phone := fmt.Sprintf("138%08d", time.Now().UnixNano()%100000000)
	if err := storage.DB.Create(&storage.Shop{
		ID: shopID, Name: "Agent MySQL 集成店", Plan: "basic", Timezone: "Asia/Shanghai",
		OpenHour: 9, CloseHour: 18, LunchStart: 12, LunchEnd: 13, LunchEndMin: 30,
		ExpiresAt: time.Now().AddDate(1, 0, 0),
	}).Error; err != nil {
		t.Fatalf("create shop: %v", err)
	}
	if err := storage.DB.Create(&storage.Barber{
		ID: barberID, ShopID: shopID, Name: barberName, Active: true,
	}).Error; err != nil {
		t.Fatalf("create barber: %v", err)
	}
	if err := storage.DB.Create(&storage.Customer{
		ID: customerID, Name: "可信 MySQL 顾客", Phone: phone,
		WechatOpenID: "wx-" + uuid.NewString(), ExternalUserID: "ext-" + uuid.NewString(),
	}).Error; err != nil {
		t.Fatalf("create customer: %v", err)
	}

	application := legacybooking.NewApplication(func(ctx context.Context, trusted v1alpha1.ExecutionContext) (legacybooking.CustomerIdentity, error) {
		customer, err := storage.GetCustomerByID(ctx, trusted.CustomerID)
		if err != nil {
			return legacybooking.CustomerIdentity{}, err
		}
		return legacybooking.CustomerIdentity{
			Name: customer.Name, Phone: customer.Phone,
			OpenID: customer.WechatOpenID, ExternalUserID: customer.ExternalUserID,
		}, nil
	})
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		loc = time.FixedZone("CST", 8*60*60)
	}
	date := time.Now().In(loc).AddDate(0, 0, 1).Format("2006-01-02")
	model := &scriptedLegacyCreateModel{date: date, barberName: barberName}
	agent, err := buildTypedWithModel(
		context.Background(), intent.NewClassifyTool(intent.NewClassifier()), model, chatmodel.ProviderStub, application,
	)
	if err != nil {
		t.Fatalf("agent construction failed: %v", err)
	}

	trusted := v1alpha1.ExecutionContext{
		MerchantID: shopID, LocationID: shopID, CustomerID: customerID, PrincipalID: "agent-mysql-test",
		Permissions: []v1alpha1.Permission{v1alpha1.PermissionBookingWrite},
		TraceID:     "agent-mysql-trace-" + uuid.NewString(), IdempotencyKey: "agent-mysql-idem-" + uuid.NewString(),
	}
	runner := adk.NewRunner(context.Background(), adk.RunnerConfig{Agent: agent})
	events := runner.Run(v1alpha1.WithExecutionContext(context.Background(), trusted), []adk.Message{schema.UserMessage("请帮我预约明天 14 点")})

	var final string
	toolEvents := 0
	for {
		event, ok := events.Next()
		if !ok {
			break
		}
		if event.Err != nil {
			t.Fatalf("agent execution failed: %v", event.Err)
		}
		if event.Output == nil || event.Output.MessageOutput == nil {
			continue
		}
		message, err := event.Output.MessageOutput.GetMessage()
		if err != nil {
			t.Fatalf("read agent event failed: %v", err)
		}
		if event.Output.MessageOutput.Role == schema.Tool {
			toolEvents++
		}
		if event.Output.MessageOutput.Role == schema.Assistant && message != nil && message.Content != "" {
			final = message.Content
		}
	}

	if toolEvents != 1 {
		t.Fatalf("tool events = %d, want 1", toolEvents)
	}
	if final != "真实预约链路已完成" {
		t.Fatalf("final response = %q", final)
	}

	var appointment storage.Appointment
	if err := storage.DB.Where("shop_id = ? AND date = ? AND time = ?", shopID, date, "14:00").First(&appointment).Error; err != nil {
		t.Fatalf("load created appointment: %v", err)
	}
	if appointment.CustomerID != customerID || appointment.Customer != "可信 MySQL 顾客" {
		t.Fatalf("appointment identity = customer_id:%q customer:%q, want trusted customer %q/%q", appointment.CustomerID, appointment.Customer, customerID, "可信 MySQL 顾客")
	}
}
