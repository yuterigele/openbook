package storage

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	AuditOutcomeSuccess = "success"
	AuditOutcomeFailure = "failure"

	AuditActorAgent  = "agent"
	AuditActorAdmin  = "admin"
	AuditActorSystem = "system"
)

type auditContextKey struct{}

type auditContext struct {
	TraceID   string
	SpanID    string
	ShopID    string
	ActorType string
	ActorID   string
}

func WithAuditShop(ctx context.Context, shopID string) context.Context {
	ac := auditContextFrom(ctx)
	ac.ShopID = strings.TrimSpace(shopID)
	return context.WithValue(ctx, auditContextKey{}, ac)
}

// EnsureTraceID 确保当前调用链拥有稳定的追踪标识。
func EnsureTraceID(ctx context.Context) context.Context {
	if TraceIDFromContext(ctx) != "" {
		return ctx
	}
	return WithTraceID(ctx, randomHex(16))
}

// WithSpanID 将当前技术阶段设为后续阶段的父节点。
func WithSpanID(ctx context.Context, spanID string) context.Context {
	ac := auditContextFrom(ctx)
	ac.SpanID = strings.TrimSpace(spanID)
	return context.WithValue(ctx, auditContextKey{}, ac)
}

// WithTraceID 注入由服务端生成或验证过的追踪标识。
func WithTraceID(ctx context.Context, traceID string) context.Context {
	ac := auditContextFrom(ctx)
	ac.TraceID = strings.TrimSpace(traceID)
	return context.WithValue(ctx, auditContextKey{}, ac)
}

// WithAuditActor 注入可信操作方。actorID 应为内部标识，不应使用手机号或微信外部标识。
func WithAuditActor(ctx context.Context, actorType, actorID string) context.Context {
	ac := auditContextFrom(ctx)
	ac.ActorType = strings.TrimSpace(actorType)
	ac.ActorID = strings.TrimSpace(actorID)
	return context.WithValue(ctx, auditContextKey{}, ac)
}

func TraceIDFromContext(ctx context.Context) string {
	return auditContextFrom(ctx).TraceID
}

func SpanIDFromContext(ctx context.Context) string    { return auditContextFrom(ctx).SpanID }
func AuditShopFromContext(ctx context.Context) string { return auditContextFrom(ctx).ShopID }

func auditContextFrom(ctx context.Context) auditContext {
	if ctx == nil {
		return auditContext{}
	}
	ac, _ := ctx.Value(auditContextKey{}).(auditContext)
	return ac
}

// WriteAuditInTx 将关键操作审计写入调用方事务。
func WriteAuditInTx(ctx context.Context, tx *gorm.DB, rec AuditLog, details map[string]any) error {
	ac := auditContextFrom(ctx)
	if rec.TraceID == "" {
		rec.TraceID = ac.TraceID
	}
	if rec.TraceID == "" {
		rec.TraceID = uuid.NewString()
	}
	if rec.ActorType == "" {
		rec.ActorType = ac.ActorType
	}
	if rec.ActorType == "" {
		rec.ActorType = AuditActorSystem
	}
	if rec.ActorID == "" {
		rec.ActorID = ac.ActorID
	}
	if details != nil {
		encoded, err := json.Marshal(details)
		if err != nil {
			return err
		}
		rec.Details = string(encoded)
	}
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = time.Now()
	}
	rec.CreatedAt = rec.CreatedAt.UTC().Truncate(time.Millisecond)
	var previous AuditLog
	err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("shop_id = ?", rec.ShopID).Order("id DESC").First(&previous).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	rec.PrevHash = previous.RecordHash
	rec.RecordHash = auditRecordHash(rec)
	if err := tx.WithContext(ctx).Create(&rec).Error; err != nil {
		return err
	}
	payload, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	return tx.WithContext(ctx).Create(&AuditOutbox{
		AuditLogID: rec.ID,
		Payload:    string(payload),
		Status:     "pending",
		CreatedAt:  rec.CreatedAt,
	}).Error
}

// WriteAudit 独立记录失败尝试等不依附于成功业务事务的审计事件。
func WriteAudit(ctx context.Context, rec AuditLog, details map[string]any) error {
	if DB == nil {
		return fmt.Errorf("DB 未初始化")
	}
	return DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return WriteAuditInTx(ctx, tx, rec, details)
	})
}

func auditRecordHash(rec AuditLog) string {
	canonical := strings.Join([]string{
		rec.PrevHash, rec.TraceID, rec.ShopID, rec.ActorType, rec.ActorID,
		rec.Action, rec.ResourceType, rec.ResourceID, rec.Outcome, rec.ErrorCode,
		rec.Details, rec.CreatedAt.UTC().Format(time.RFC3339Nano),
	}, "\x1f")
	sum := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(sum[:])
}

func randomHex(bytesLen int) string {
	b := make([]byte, bytesLen)
	if _, err := rand.Read(b); err != nil {
		return strings.ReplaceAll(uuid.NewString(), "-", "")
	}
	return hex.EncodeToString(b)
}

// VerifyAuditChain 校验指定门店的审计哈希链。
func VerifyAuditChain(ctx context.Context, shopID string) error {
	var rows []AuditLog
	if err := DB.WithContext(ctx).Where("shop_id = ?", shopID).Order("id ASC").Find(&rows).Error; err != nil {
		return err
	}
	previous := ""
	for _, row := range rows {
		if row.PrevHash != previous || row.RecordHash != auditRecordHash(row) {
			return fmt.Errorf("审计链校验失败: audit_id=%d", row.ID)
		}
		previous = row.RecordHash
	}
	return nil
}

type AuditQuery struct {
	ShopID, TraceID, Action, ResourceType, ResourceID, Outcome string
	Limit                                                      int
}

func ListAuditLogs(ctx context.Context, q AuditQuery) ([]AuditLog, error) {
	if q.ShopID == "" {
		return nil, fmt.Errorf("shop_id 必填")
	}
	if q.Limit <= 0 || q.Limit > 500 {
		q.Limit = 100
	}
	db := DB.WithContext(ctx).Where("shop_id = ?", q.ShopID)
	for column, value := range map[string]string{"trace_id": q.TraceID, "action": q.Action, "resource_type": q.ResourceType, "resource_id": q.ResourceID, "outcome": q.Outcome} {
		if value != "" {
			db = db.Where(column+" = ?", value)
		}
	}
	var rows []AuditLog
	return rows, db.Order("id DESC").Limit(q.Limit).Find(&rows).Error
}

// RecordTraceSpan 尽力保存技术链路；失败不得改变业务结果。
func RecordTraceSpan(ctx context.Context, span TraceSpan, attributes map[string]any) {
	if DB == nil {
		return
	}
	span.TraceID = TraceIDFromContext(ctx)
	if span.TraceID == "" {
		return
	}
	if span.SpanID == "" {
		span.SpanID = randomHex(8)
	}
	if span.ParentID == "" {
		span.ParentID = SpanIDFromContext(ctx)
	}
	if span.ShopID == "" {
		span.ShopID = auditContextFrom(ctx).ShopID
	}
	if span.StartedAt.IsZero() {
		span.StartedAt = time.Now()
	}
	if span.EndedAt == nil {
		now := time.Now()
		span.EndedAt = &now
	}
	span.DurationMS = span.EndedAt.Sub(span.StartedAt).Milliseconds()
	if attributes != nil {
		if b, err := json.Marshal(attributes); err == nil {
			span.Attributes = string(b)
		}
	}
	_ = DB.WithContext(ctx).Create(&span).Error
}

func DeleteTraceSpansBefore(ctx context.Context, before time.Time) (int64, error) {
	result := DB.WithContext(ctx).Where("started_at < ?", before).Delete(&TraceSpan{})
	return result.RowsAffected, result.Error
}

// ListPendingAuditOutbox 返回待外部导出的审计消息；发布方成功后必须调用 MarkAuditOutboxPublished。
func ListPendingAuditOutbox(ctx context.Context, limit int) ([]AuditOutbox, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	var rows []AuditOutbox
	err := DB.WithContext(ctx).Where("status = ? AND (next_retry_at IS NULL OR next_retry_at <= ?)", "pending", time.Now()).
		Order("id ASC").Limit(limit).Find(&rows).Error
	return rows, err
}

func MarkAuditOutboxPublished(ctx context.Context, id uint64) error {
	now := time.Now()
	return DB.WithContext(ctx).Model(&AuditOutbox{}).Where("id = ? AND status = ?", id, "pending").
		Updates(map[string]any{"status": "published", "published_at": now}).Error
}

func MarkAuditOutboxRetry(ctx context.Context, id uint64, nextRetryAt time.Time) error {
	return DB.WithContext(ctx).Model(&AuditOutbox{}).Where("id = ? AND status = ?", id, "pending").
		Updates(map[string]any{"attempts": gorm.Expr("attempts + 1"), "next_retry_at": nextRetryAt}).Error
}
