#!/bin/sh
set -eu

DATA_DIR="${DATA_DIR:-/data}"
PORT="${PORT:-8080}"
ADMIN_KEY="${ADMIN_KEY:-change-me-123}"
RTC_UDP_PORT="${RTC_UDP_PORT:-50000}"

mkdir -p "$DATA_DIR"
ln -sf "$DATA_DIR/server.log" /app/server.log
ln -sf "$DATA_DIR/banned_ips.json" /app/banned_ips.json

exec /app/sigmartc -port "$PORT" -admin-key "$ADMIN_KEY" -rtc-udp-port "$RTC_UDP_PORT"
