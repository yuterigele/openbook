# 多阶段构建：第一阶段编译二进制，第二阶段只拿运行时
FROM golang:1.24-alpine AS builder

WORKDIR /src

# 1. 先复制 go.mod/go.sum 单独下载依赖（利用 docker 缓存）
COPY go.mod go.sum ./
COPY ../.. /src/../ 2>/dev/null || true
RUN go mod download

# 2. 复制源码并编译
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/chatwitheino .

# ---- 运行时镜像 ----
FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata curl

WORKDIR /app
COPY --from=builder /out/chatwitheino /app/chatwitheino
COPY .env.example /app/.env.example

# 数据/会话目录
RUN mkdir -p /app/data/sessions /app/data/sessions_agentic /app/data/workspace

EXPOSE 38080

# 健康检查：访问首页是否 200
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 \
  CMD curl -fsS http://127.0.0.1:38080/ || exit 1

CMD ["/app/chatwitheino"]