# AGENTS.md - Context & Instructions for AI Developers

## 1. Project Overview
**Name:** GhostTalk (Module: `sigmartc`)
**Type:** Anonymous, Low-Latency WebRTC Voice Chat (SFU)
**Core Philosophy:**
*   **Ephemeral:** No database. Rooms and users exist only in RAM. Rooms auto-destroy after 2 hours of inactivity.
*   **Privacy:** No user accounts. IPs are logged for moderation but not exposed to peers (SFU architecture).
*   **Performance:** Go backend (Pion) + Vanilla JS frontend. Optimized for high-bandwidth, low-RAM VPS environments.

## 2. Technical Architecture

### 2.1 Backend (Go)
*   **Entry Point:** `cmd/server/main.go`
*   **Core Logic:** `internal/server/`
*   **WebRTC Library:** `github.com/pion/webrtc/v3`
*   **Signaling:** `github.com/gorilla/websocket`
*   **Networking:**
    *   **TCP 8080 (Default):** HTTP (UI) + WebSocket (Signaling). Usually reverse-proxied via Nginx/Caddy to Port 443 (HTTPS).
    *   **UDP 50000:** Single-port WebRTC media traffic (RTP/RTCP).

### 2.2 Frontend (HTML/JS)
*   **Stack:** Vanilla ES6 JavaScript, CSS3 (No frameworks like React/Vue).
*   **Style:** Dark Mode ("Discord-like"), Responsive.
*   **Key Logic:** `web/static/js/app.js` handles Signaling, WebRTC negotiation, and client-side VAD (Visual Activity Detection).

## 3. Critical Implementation Details

### 3.1 Signaling Protocol (WebSocket)
**Endpoint:** `/ws?room={uuid}&name={nickname}`

**Messages (JSON):**
| Type | Direction | Payload | Description |
| :--- | :--- | :--- | :--- |
| `room_state` | S -> C | `{ self_id, peers: [] }` | Initial state on join. |
| `peer_join` | S -> C | `{ peer: { id, name } }` | Notification when a new user joins. |
| `peer_leave` | S -> C | `{ peer_id }` | Notification when a user disconnects. |
| `offer` | Bidirectional | `{ sdp }` | SDP Offer (Renegotiation). |
| `answer` | Bidirectional | `{ sdp }` | SDP Answer. |
| `candidate` | Bidirectional | `{ candidate }` | ICE Candidate. |
| `error` | S -> C | `{ message }` | e.g., "Room full". |

### 3.2 Media Forwarding (SFU)
*   **Model:** Simple SFU. The server receives an audio track from a publisher and creates a `TrackLocalStaticRTP` for every other subscriber in the room.
*   **Stream Identification (CRITICAL):**
    *   The backend **forces** the outgoing `StreamID` to be the **Sender's PeerID**.
    *   *Why?* This allows the frontend (`app.js`) to map a received `MediaStream` back to a specific user for UI rendering and VAD visualization without extra signaling.
    *   *Code Location:* `internal/server/handler.go` -> `addTrackToPeer`.

### 3.3 Room Lifecycle
*   **Creation:** Implicit. If a user connects to `/r/{uuid}` and it doesn't exist, it is created in RAM.
*   **Capacity:** Max **8 users** per room (Hardcoded check in `HandleWS`).
*   **Destruction:** A background ticker runs every 1 minute. If a room has 0 peers for > 2 hours, it is deleted.

## 4. Development & Operation

### 4.1 Build & Run
```bash
# Build
go build -o bin/sigmartc cmd/server/main.go

# Run
./bin/sigmartc -port 8080 -admin-key "my-secret-key"
```

### 4.2 Admin Interface
*   **URL:** `/admin?key=my-secret-key`
*   **Features:**
    *   `action=stats`: JSON stats (Room count, Memory usage).
    *   `action=logs`: View last 100 lines of `server.log`.
    *   `action=ban&ip={ip}`: Ban an IP address (Persisted to `banned_ips.json`).

### 4.3 Directory Structure
```
/
├── cmd/server/main.go       # Entry point
├── internal/
│   ├── logger/              # Structured logging (slog)
│   └── server/              # Room manager, Handler, WebRTC logic
├── web/
│   ├── static/              # CSS, JS assets
│   └── templates/           # HTML templates
├── DESIGN.md                # High-level design doc
├── server.log               # Runtime logs (JSON Lines)
└── banned_ips.json          # Persistent ban list
```

## 5. Coding Standards for AI

1.  **Go (Backend):**
    *   Strictly follow `gofmt`.
    *   Use `slog` for all logging.
    *   **Concurrency:** Use `sync.RWMutex` for `Room` and `RoomManager`. Never access Maps concurrently without a lock.
    *   **Error Handling:** Check all errors. Log critical failures.
    *   **Memory:** Be mindful of goroutine leaks. Ensure `defer` is used for unlocking and closing connections.

2.  **JavaScript (Frontend):**
    *   No build steps (No Webpack/Vite). Keep it raw ES6.
    *   Use `document.getElementById` or `querySelector`.
    *   **VAD Logic:** Keep `AudioContext` handling efficient. Use `requestAnimationFrame` for visual updates.

3.  **UI/UX:**
    *   Maintain the "Dark Mode" aesthetic.
    *   All icons must be SVG (No external font dependencies).
    *   **Feedback:** Users must clearly know when they are Muted, Speaking, or Disconnected.

## 6. Testing Strategy

Since this is a real-time networked application, automated unit tests are limited.

**Manual Verification Checklist:**
1.  **Connectivity:** Open two browser tabs. Join the same room URL.
2.  **Audio:** Verify audio flows both ways (speak in Tab A, hear in Tab B).
3.  **Mute:** Click Mute in Tab A. Verify "Red Mic" icon. Verify sound stops in Tab B.
4.  **VAD:** Speak in Tab A. Verify Avatar in Tab B pulses/glows.
5.  **Leave:** Click Hangup in Tab A. Verify Tab A redirects to Home. Verify Tab B sees "User Left" toast/removal.
6.  **Persistence:** Close all tabs. Wait 1 minute. Check `server.log` for cleanup events (if testing TTL).
7.  **Admin:** Access `/admin?key=...`. Ban Tab A's IP. Try to rejoin. Verify 403 Forbidden.

## 7. Future Roadmap (For AI Agents)
*   **Screen Sharing:** Add video track support to the SFU logic.
*   **Turn Server:** Integrate TURN credentials for users behind strict NATs.
*   **Chat:** Add a simple DataChannel text chat.
*   **Mobile UI:** Improve CSS for vertical mobile layouts.

## 8. Development Workflow (Human + AI)
1. Format Go code with `gofmt -w` before commits.
2. Run `go test ./...` (may be limited) and the manual verification checklist.
3. Use `go mod tidy` after dependency changes.
4. Keep docs in sync: `AGENTS.md`, `DESIGN.md`, `DEPLOY.md`, and `README.md`.
5. Verify the StreamID mapping (sender PeerID) remains intact in `internal/server/handler.go`.

## 9. Repository Standards
*   **Branching:** Use short-lived feature branches off `main` (e.g., `feat/vad-tuning`).
*   **Commits:** Prefer small, focused commits with clear messages.
*   **Generated/Runtime Data:** Never commit logs, binaries, or ban lists.
*   **Security:** Only trust `X-Forwarded-For` behind a known reverse proxy.
