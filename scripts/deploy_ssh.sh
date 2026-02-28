#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'USAGE'
Usage: deploy_ssh.sh <user@host> [remote_dir]

Environment variables:
  ADMIN_KEY      Admin key passed to the server (default: change-me-123)
  PORT           HTTP port for the server (default: 8080)
  NETWORK_MODE   host|bridge (default: bridge)
  UDP_PORT       WebRTC UDP port (container + publish when NETWORK_MODE=bridge) (default: 50000)
  HTTP_BIND_HOST Host IP for HTTP port binding when NETWORK_MODE=bridge (default: 127.0.0.1)
  TURN_SERVER    TURN server URL (e.g., turn:1.2.3.4:3478)
  TURN_USER      TURN server username
  TURN_PASS      TURN server password
  IMAGE_NAME     Docker image name (default: sigmartc)
  CONTAINER_NAME Docker container name (default: sigmartc)
  SSH_OPTS       Extra options for ssh (e.g. "-i ~/.ssh/id_rsa")

Examples:
  ADMIN_KEY=secret PORT=8080 ./scripts/deploy_ssh.sh root@example.com
  NETWORK_MODE=bridge UDP_PORT=50000 ./scripts/deploy_ssh.sh ubuntu@1.2.3.4 /opt/sigmartc
  NETWORK_MODE=bridge HTTP_BIND_HOST=0.0.0.0 ./scripts/deploy_ssh.sh ubuntu@1.2.3.4 /opt/sigmartc
  TURN_SERVER=turn:1.2.3.4:3478 TURN_USER=user TURN_PASS=pass ./scripts/deploy_ssh.sh ubuntu@1.2.3.4
USAGE
}

if [[ "${1:-}" == "-h" || "${1:-}" == "--help" ]]; then
  usage
  exit 0
fi

TARGET="${1:-}"
REMOTE_DIR="${2:-/opt/sigmartc}"

if [[ -z "$TARGET" ]]; then
  usage
  exit 1
fi

ADMIN_KEY="${ADMIN_KEY:-change-me-123}"
PORT="${PORT:-8080}"
NETWORK_MODE="${NETWORK_MODE:-bridge}"
UDP_PORT="${UDP_PORT:-50000}"
HTTP_BIND_HOST="${HTTP_BIND_HOST:-127.0.0.1}"
TURN_SERVER="${TURN_SERVER:-}"
TURN_USER="${TURN_USER:-}"
TURN_PASS="${TURN_PASS:-}"
IMAGE_NAME="${IMAGE_NAME:-sigmartc}"
CONTAINER_NAME="${CONTAINER_NAME:-sigmartc}"
SSH_OPTS="${SSH_OPTS:-}"

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
REMOTE_SRC="$REMOTE_DIR/src"
REMOTE_DATA="$REMOTE_DIR/data"

ssh $SSH_OPTS "$TARGET" "rm -rf $(printf '%q' "$REMOTE_SRC") && mkdir -p $(printf '%q' "$REMOTE_SRC") $(printf '%q' "$REMOTE_DATA")"

cd "$ROOT_DIR"

tar \
  --exclude .git \
  --exclude bin \
  --exclude node_modules \
  --exclude test-results \
  --exclude playwright-report \
  --exclude server.log \
  --exclude banned_ips.json \
  --exclude task_plan.md \
  --exclude findings.md \
  --exclude progress.md \
  -czf - . \
  | ssh $SSH_OPTS "$TARGET" "tar -xzf - -C $(printf '%q' "$REMOTE_SRC")"

ssh $SSH_OPTS "$TARGET" bash -s -- \
  "$IMAGE_NAME" "$REMOTE_SRC" <<'EOF'
set -euo pipefail
image="$1"
context_dir="$2"

VERSION=$(date +%s)
docker build --build-arg VERSION="$VERSION" -t "$image" "$context_dir"
EOF

ssh $SSH_OPTS "$TARGET" bash -s -- \
  "$IMAGE_NAME" "$CONTAINER_NAME" "$PORT" "$ADMIN_KEY" "$NETWORK_MODE" "$UDP_PORT" "$HTTP_BIND_HOST" "$REMOTE_DATA" "$TURN_SERVER" "$TURN_USER" "$TURN_PASS" <<'EOF'
set -euo pipefail
image="$1"
container="$2"
port="$3"
admin_key="$4"
network_mode="$5"
udp_port="$6"
http_bind_host="${7-}"
data_dir="${8-}"
turn_server="${9-}"
turn_user="${10-}"
turn_pass="${11-}"
if [[ -z "$data_dir" ]]; then
  data_dir="$http_bind_host"
  http_bind_host=""
fi

docker rm -f "$container" >/dev/null 2>&1 || true

run_args=(
  --restart unless-stopped
  -d
  --name "$container"
  -e PORT="$port"
  -e ADMIN_KEY="$admin_key"
  -e RTC_UDP_PORT="$udp_port"
  -e DATA_DIR=/data
  -v "$data_dir:/data"
)

if [[ -n "$turn_server" ]]; then
  run_args+=(-e TURN_SERVER="$turn_server")
fi
if [[ -n "$turn_user" ]]; then
  run_args+=(-e TURN_USER="$turn_user")
fi
if [[ -n "$turn_pass" ]]; then
  run_args+=(-e TURN_PASS="$turn_pass")
fi

if [[ "$network_mode" == "host" ]]; then
  run_args+=(--network host)
else
  if [[ -n "$http_bind_host" ]]; then
    run_args+=(-p "${http_bind_host}:${port}:${port}")
  else
    run_args+=(-p "${port}:${port}")
  fi
  run_args+=(-p "${udp_port}:${udp_port}/udp")
fi

docker run "${run_args[@]}" "$image"
EOF

printf 'Deployment complete: %s\n' "$TARGET"
