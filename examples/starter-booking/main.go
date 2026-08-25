package main

import (
	"log"
	"net/http"
	"os"

	"github.com/google/uuid"
	"github.com/yuterigele/openbook/sdk/booking/v1alpha1"
)

func main() {
	definition := StarterProfile()
	application, err := NewMemoryApplication(definition)
	if err != nil {
		log.Fatalf("创建 Starter Application 失败: %v", err)
	}
	registry, err := NewStarterRegistry(application, definition)
	if err != nil {
		log.Fatalf("注册 Starter 工具失败: %v", err)
	}

	address := getenv("STARTER_HTTP_ADDR", ":8088")
	log.Printf("OpenBook Starter 已启动: http://localhost%s/chat", address)
	if err := http.ListenAndServe(address, NewChatHandler(registry, starterContext)); err != nil {
		log.Fatal(err)
	}
}

// starterContext 是 Demo Host 的可信上下文装配点；生产接入应替换为真实认证解析器。
func starterContext(request *http.Request) (v1alpha1.ExecutionContext, error) {
	traceID := request.Header.Get("X-Request-ID")
	if traceID == "" {
		traceID = uuid.NewString()
	}
	idempotencyKey := request.Header.Get("Idempotency-Key")
	if idempotencyKey == "" {
		idempotencyKey = traceID
	}
	return v1alpha1.ExecutionContext{
		MerchantID:     getenv("STARTER_MERCHANT_ID", "starter-merchant"),
		LocationID:     getenv("STARTER_LOCATION_ID", "starter-location"),
		CustomerID:     getenv("STARTER_CUSTOMER_ID", "starter-customer"),
		PrincipalID:    getenv("STARTER_PRINCIPAL_ID", "starter-demo-user"),
		Permissions:    []v1alpha1.Permission{v1alpha1.PermissionBookingRead, v1alpha1.PermissionBookingWrite},
		TraceID:        traceID,
		IdempotencyKey: idempotencyKey,
	}, nil
}

func getenv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
