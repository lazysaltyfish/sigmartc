# Deployment (SSH + Docker)

## Prereqs
- Remote host with Docker installed and running.
- SSH access to the host.
- Remote host has `bash` and `tar` available (common on most Linux distros).
- UDP `50000` reachable from clients (firewall/security group).
- (Recommended) TURN server for NAT traversal.

## Quick Deploy

From the project root:

```bash
ADMIN_KEY=your-secret \
TURN_SERVER=turn:your-turn-server:3478 \
TURN_USER=ghosttalk \
TURN_PASS=your-password \
NETWORK_MODE=host \
./scripts/deploy_ssh.sh user@host
```

## SSH deployment script

Basic deployment:

```bash
ADMIN_KEY=your-secret ./scripts/deploy_ssh.sh user@host
```

Full deployment with TURN:

```bash
ADMIN_KEY=your-secret \
PORT=8080 \
NETWORK_MODE=host \
UDP_PORT=50000 \
TURN_SERVER=turn:1.2.3.4:3478 \
TURN_USER=ghosttalk \
TURN_PASS=your-password \
./scripts/deploy_ssh.sh user@host /opt/sigmartc
```

Bridge mode with HTTP on all interfaces:

```bash
PORT=8080 NETWORK_MODE=bridge HTTP_BIND_HOST=0.0.0.0 \
  ./scripts/deploy_ssh.sh user@host /opt/sigmartc
```

### Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `ADMIN_KEY` | `change-me-123` | Admin panel secret key |
| `PORT` | `8080` | HTTP port |
| `NETWORK_MODE` | `bridge` | Docker network mode (`host` or `bridge`) |
| `UDP_PORT` | `50000` | WebRTC UDP port |
| `HTTP_BIND_HOST` | `127.0.0.1` | HTTP bind address (bridge mode only) |
| `TURN_SERVER` | - | TURN server URL (e.g., `turn:1.2.3.4:3478`) |
| `TURN_USER` | - | TURN username |
| `TURN_PASS` | - | TURN password |
| `IMAGE_NAME` | `sigmartc` | Docker image name |
| `CONTAINER_NAME` | `sigmartc` | Container name |
| `SSH_OPTS` | - | Extra SSH options (e.g., `-i ~/.ssh/id_rsa`) |

## TURN Server Setup (Recommended)

Clients behind symmetric NAT need TURN relay. Deploy coturn:

### 1. Create turnserver.conf

```conf
listening-port=3478
min-port=49160
max-port=49200
external-ip=YOUR_PUBLIC_IP
listening-ip=0.0.0.0

lt-cred-mech
user=ghosttalk:YOUR_SECURE_PASSWORD
realm=ghosttalk.local

fingerprint
no-multicast-peers
no-cli
no-tlsv1
no-tlsv1_1
log-file=stdout
simple-log
```

### 2. Run coturn

```bash
docker run -d --name coturn --restart=always --network=host \
  -v $(pwd)/turnserver.conf:/etc/coturn/turnserver.conf \
  coturn/coturn
```

### 3. Open Firewall Ports

| Port | Protocol | Purpose |
|------|----------|---------|
| 3478 | UDP+TCP | STUN/TURN |
| 49160-49200 | UDP | Media relay |

```bash
# ufw example
ufw allow 3478/tcp
ufw allow 3478/udp
ufw allow 49160:49200/udp
```

### 4. Configure GhostTalk

Pass TURN credentials when deploying GhostTalk.

## Local Docker build/run

```bash
docker build -t sigmartc .
```

Host networking (recommended):

```bash
docker run -d --name sigmartc \
  --restart unless-stopped \
  --network host \
  -e ADMIN_KEY=your-secret \
  -e PORT=8080 \
  -e RTC_UDP_PORT=50000 \
  -e TURN_SERVER=turn:1.2.3.4:3478 \
  -e TURN_USER=ghosttalk \
  -e TURN_PASS=your-password \
  -v sigmartc_data:/data \
  sigmartc
```

Bridge networking:

```bash
docker run -d --name sigmartc \
  --restart unless-stopped \
  -p 127.0.0.1:8080:8080 \
  -p 50000:50000/udp \
  -e ADMIN_KEY=your-secret \
  -e PORT=8080 \
  -e RTC_UDP_PORT=50000 \
  -e TURN_SERVER=turn:1.2.3.4:3478 \
  -e TURN_USER=ghosttalk \
  -e TURN_PASS=your-password \
  -v sigmartc_data:/data \
  sigmartc
```

## Reverse Proxy (Caddy)

Example Caddyfile:

```
voice.example.com {
    reverse_proxy 127.0.0.1:8080
}
```

Keep the app bound to localhost so `X-Forwarded-For` can only come from the proxy.

## Troubleshooting

| Issue | Check |
|-------|-------|
| ICE state "failed" | TURN server running? Ports open? Credentials correct? |
| WebSocket pending | Reverse proxy WebSocket config? |
| Can't hear audio | Microphone permission? UDP 50000 open? |
| Some users can't connect | They may need TURN - check if TURN is configured |
