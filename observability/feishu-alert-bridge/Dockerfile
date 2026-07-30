FROM golang:1.25-alpine AS builder

WORKDIR /src
COPY main.go ./
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/feishu-alert-bridge main.go

FROM alpine:3.20
RUN apk add --no-cache ca-certificates \
    && addgroup -S bridge \
    && adduser -S -G bridge -h /app bridge
WORKDIR /app
COPY --from=builder /out/feishu-alert-bridge ./feishu-alert-bridge
USER bridge
EXPOSE 8080
CMD ["/app/feishu-alert-bridge"]
