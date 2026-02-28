# GhostTalk (sigmartc)

GhostTalk is an anonymous, low-latency WebRTC voice chat using a simple SFU design.
Rooms are ephemeral (in-memory only), with no user accounts or database.

**Features:**
- Anonymous voice chat - no registration required
- SFU architecture - scalable, hides client IPs from each other
- TURN relay support - works behind strict NAT/firewalls
- Real-time network stats display
- Mobile-friendly responsive UI

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

With TURN server (required for clients behind strict NAT):

```bash
docker run -d --name sigmartc \
  --restart unless-stopped \
  --network host \
  -e ADMIN_KEY=my-secret-key \
  -e PORT=8080 \
  -e RTC_UDP_PORT=50000 \
  -e TURN_SERVER=turn:your-server.com:3478 \
  -e TURN_USER=username \
  -e TURN_PASS=password \
  -v sigmartc_data:/data \
  sigmartc
```

Remote deployment via SSH is documented in `DEPLOY.md` and `scripts/deploy_ssh.sh`.

## TURN Server Setup

For production, you need a TURN server to relay media for clients behind symmetric NAT.

### Quick Setup with Coturn

1. Create `turnserver.conf`:

```conf
listening-port=3478
min-port=49160
max-port=49200
external-ip=YOUR_PUBLIC_IP
listening-ip=0.0.0.0

lt-cred-mech
user=ghosttalk:YOUR_PASSWORD
realm=ghosttalk.local

fingerprint
no-multicast-peers
no-cli
log-file=stdout
simple-log
```

2. Run coturn:

```bash
docker run -d --name coturn --restart=always --network=host \
  -v $(pwd)/turnserver.conf:/etc/coturn/turnserver.conf \
  coturn/coturn
```

3. Configure GhostTalk with TURN credentials.

### Required Ports for TURN

| Port | Protocol | Purpose |
|------|----------|---------|
| 3478 | UDP+TCP | STUN/TURN signaling |
| 49160-49200 | UDP | Media relay |

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
- `-port` (default `8080`) - HTTP port
- `-admin-key` (default `change-me-123`) - Admin panel secret
- `-rtc-udp-port` (default `50000`) - WebRTC ICE UDP port
- `-turn-server` - TURN server URL (e.g., `turn:1.2.3.4:3478`)
- `-turn-user` - TURN username
- `-turn-pass` - TURN password

Docker environment variables:
- `PORT`, `ADMIN_KEY`, `RTC_UDP_PORT`
- `TURN_SERVER`, `TURN_USER`, `TURN_PASS`
- `DATA_DIR` (default `/data`)

## Ports and Firewall

| Port | Protocol | Purpose |
|------|----------|---------|
| 8080 | TCP | HTTP + WebSocket |
| 50000 | UDP | WebRTC media (server) |

Ensure these ports are open if clients are remote.

## Data Files

Runtime data files:
- `server.log` (JSON lines)
- `banned_ips.json` (persistent ban list)

In Docker, these live under the `/data` volume.

## Troubleshooting

- **Audio fails / ICE state "failed"**: Check TURN server is running and ports are open
- **Can't connect at all**: Confirm microphone permissions and firewall settings
- **Behind reverse proxy**: Ensure WebSocket upgrade headers and trusted `X-Forwarded-For`

## Architecture

```
┌─────────┐     WebSocket      ┌─────────────┐
│ Client  │◄──────────────────►│   Server    │
│ (Browser)│                   │  (Go/Pion)  │
└─────────┘                    └─────────────┘
     │                              │
     │ WebRTC (UDP 50000)           │
     │                              │
     └──────────────────────────────┘
              SFU Relay
```

- Server uses Pion WebRTC v3
- Single UDP port for all WebRTC traffic
- Tracks forwarded via `TrackLocalStaticRTP`

## Third-Party Assets

- UI sound effects (join/leave): Kenney "Interface Sounds" (CC0). See `web/static/audio/LICENSE-kenney-interface-sounds.txt`.
