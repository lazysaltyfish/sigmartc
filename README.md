# GhostTalk (sigmartc)

GhostTalk is an anonymous, low-latency WebRTC voice chat using a simple SFU design.
Rooms are ephemeral (in-memory only), with no user accounts or database.

## Quick Start (Local)

Requirements:
- Go 1.25.5 (per `go.mod`).

Build and run:

```bash
go build -o bin/sigmartc cmd/server/main.go
./bin/sigmartc -port 8080 -admin-key "my-secret-key" -rtc-udp-port 50000
```

Open `http://localhost:8080` in two browser tabs to test audio.
Share a room link like `http://localhost:8080/r/<room-id>`.

## Docker

Build the image:

```bash
docker build -t sigmartc .
```

Run (host networking recommended):

```bash
docker run -d --name sigmartc \
  --restart unless-stopped \
  --network host \
  -e ADMIN_KEY=my-secret-key \
  -e PORT=8080 \
  -e RTC_UDP_PORT=50000 \
  -v sigmartc_data:/data \
  sigmartc
```

Remote deployment via SSH is documented in `DEPLOY.md` and `scripts/deploy_ssh.sh`.

## Usage

1. Open the site and enter a nickname.
2. Click Join to enter the room.
3. Use the mute and hangup controls as needed.
4. Copy the invite link and share it with others.

## Admin

Admin panel: `/admin?key=<admin-key>`

Actions:
- `action=stats` for JSON stats
- `action=logs` for recent logs
- `action=ban&ip=<ip>` to ban an IP (POST only)

## Configuration

Command-line flags:
- `-port` (default `8080`)
- `-admin-key` (default `change-me-123`)
- `-rtc-udp-port` (default `50000`)

Docker environment variables:
- `PORT`, `ADMIN_KEY`, `RTC_UDP_PORT`
- `DATA_DIR` (default `/data`)

## Ports and Firewall

- TCP `8080` (HTTP + WebSocket)
- UDP `50000` (WebRTC media)

Ensure UDP `50000` is open if clients are remote.

## Data Files

Runtime data files:
- `server.log` (JSON lines)
- `banned_ips.json` (persistent ban list)

In Docker, these live under the `/data` volume.

## Troubleshooting

- If audio fails, confirm microphone permissions and UDP `50000` access.
- If running behind a reverse proxy, ensure WebSocket upgrade headers and
  a trusted `X-Forwarded-For` are configured.
