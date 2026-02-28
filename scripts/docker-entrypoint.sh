#!/bin/sh
set -eu

DATA_DIR="${DATA_DIR:-/data}"
PORT="${PORT:-8080}"
ADMIN_KEY="${ADMIN_KEY:-change-me-123}"
RTC_UDP_PORT="${RTC_UDP_PORT:-50000}"
TURN_SERVER="${TURN_SERVER:-}"
TURN_USER="${TURN_USER:-}"
TURN_PASS="${TURN_PASS:-}"

mkdir -p "$DATA_DIR"
ln -sf "$DATA_DIR/server.log" /app/server.log
ln -sf "$DATA_DIR/banned_ips.json" /app/banned_ips.json

args="/app/sigmartc -port $PORT -admin-key $ADMIN_KEY -rtc-udp-port $RTC_UDP_PORT"
if [ -n "$TURN_SERVER" ]; then
  args="$args -turn-server $TURN_SERVER"
fi
if [ -n "$TURN_USER" ]; then
  args="$args -turn-user $TURN_USER"
fi
if [ -n "$TURN_PASS" ]; then
  args="$args -turn-pass $TURN_PASS"
fi

exec $args
