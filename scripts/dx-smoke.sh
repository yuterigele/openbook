#!/usr/bin/env bash

set -euo pipefail

REPORT_DIR="${DX_SMOKE_REPORT_DIR:-artifacts/dx-smoke}"
mkdir -p "$REPORT_DIR"
LOG_FILE="$REPORT_DIR/starter.log"
REPORT_FILE="$REPORT_DIR/report.txt"
STARTER_DIR="$(mktemp -d)"
STARTER_BIN="$STARTER_DIR/openbook-starter"
STARTER_PID=""

now_seconds() {
  date +%s
}

cleanup() {
  status=$?
  if [[ -n "$STARTER_PID" ]] && kill -0 "$STARTER_PID" 2>/dev/null; then
    kill "$STARTER_PID" 2>/dev/null || true
    wait "$STARTER_PID" 2>/dev/null || true
  fi
  rm -rf "$STARTER_DIR"
  if [[ "$status" -ne 0 ]]; then
    echo "status=failed" >"$REPORT_FILE"
    echo "dx-smoke failed; starter log:" >&2
    tail -80 "$LOG_FILE" >&2 2>/dev/null || true
  fi
  exit "$status"
}
trap cleanup EXIT

echo "Building Starter binary..."
go build -o "$STARTER_BIN" ./examples/starter-booking

echo "Starting Starter..."
started_at="$(now_seconds)"
"$STARTER_BIN" >"$LOG_FILE" 2>&1 &
STARTER_PID=$!

services_response=""
for _ in $(seq 1 60); do
  if services_response="$(curl --fail --silent --show-error --max-time 2 \
    -X POST http://127.0.0.1:8088/chat \
    -H 'Content-Type: application/json' \
    --data '{"tool":"list_services","parameters":{}}')"; then
    break
  fi
  sleep 1
done
ready_at="$(now_seconds)"
test -n "$services_response"
printf '%s' "$services_response" | grep -q '"code":"services.listed"'

booking_payload='{"tool":"create_booking","parameters":{"service_id":"wash_and_trim","start_at":"2030-01-01T15:00:00+08:00"}}'
booking_response="$(curl --fail --silent --show-error \
  -X POST http://127.0.0.1:8088/chat \
  -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: dx-smoke-booking-1' \
  --data "$booking_payload")"
printf '%s' "$booking_response" | grep -q '"code":"booking.created"'
booking_id="$(printf '%s' "$booking_response" | sed -n 's/.*"booking_id":"\([^"]*\)".*/\1/p')"
test -n "$booking_id"

replayed_response="$(curl --fail --silent --show-error \
  -X POST http://127.0.0.1:8088/chat \
  -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: dx-smoke-booking-1' \
  --data "$booking_payload")"
replayed_id="$(printf '%s' "$replayed_response" | sed -n 's/.*"booking_id":"\([^"]*\)".*/\1/p')"
test "$booking_id" = "$replayed_id"

startup_seconds=$((ready_at - started_at))
{
  echo "status=passed"
  echo "starter_ready_seconds=$startup_seconds"
  echo "services_code=services.listed"
  echo "booking_code=booking.created"
  echo "idempotency=replay_same_booking_id"
} | tee "$REPORT_FILE"
