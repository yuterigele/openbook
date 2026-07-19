# syntax=docker/dockerfile:1

FROM golang:1.25-alpine AS builder

WORKDIR /src

# Copy dependency manifests first so dependency downloads remain cached.
COPY go.mod go.sum ./
ARG GOPROXY=https://proxy.golang.org,direct
ENV GOPROXY=${GOPROXY}
RUN --mount=type=cache,target=/go/pkg/mod \
    for attempt in 1 2 3; do \
      go mod download && exit 0; \
      echo "go mod download failed (attempt ${attempt}/3), retrying..."; \
      sleep "${attempt}"; \
    done; \
    exit 1

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/openbook .

FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata curl \
    && addgroup -S openbook \
    && adduser -S -G openbook -h /app openbook

WORKDIR /app

COPY --from=builder /out/openbook ./openbook
COPY static ./static

RUN mkdir -p /app/data/sessions /app/data/sessions_agentic /app/data/workspace \
    && chown -R openbook:openbook /app

USER openbook

EXPOSE 38080

HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 \
  CMD curl --fail --silent http://127.0.0.1:38080/ >/dev/null || exit 1

CMD ["/app/openbook"]
