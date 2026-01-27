# Project: Anonymous WebRTC SFU Voice Chat (Project Name: "GhostTalk")

## 1. Project Overview
A lightweight, anonymous, web-based voice chat application designed for gaming. It uses a "Traffic Forwarding Center" (SFU) architecture to relay audio, protecting user IPs and saving bandwidth.

### Core Philosophy
*   **Simplicity:** No database, no user accounts, no installation.
*   **Performance:** High throughput, low latency, low memory footprint (Go + Pion).
*   **Privacy:** Temporary sessions, logs stored locally for admin safety but no permanent user tracking.

## 2. System Architecture

### 2.1 Technology Stack
*   **Backend:** Go (Golang)
    *   **WebRTC Engine:** `github.com/pion/webrtc/v3`
    *   **WebSockets:** `github.com/gorilla/websocket` (or standard `net/http`)
    *   **Logging:** `slog` (Structured Logging)
*   **Frontend:** Vanilla JavaScript (ES6+), HTML5, CSS3. No frameworks.
*   **Infrastructure:** Linux VPS.
    *   **Reverse Proxy:** Nginx/Caddy (Handles SSL/TLS termination).

### 2.2 Network Flow
1.  **Signaling (TCP/WSS):**
    *   User -> Caddy (TLS) -> Go App (Port 8080/Localhost).
    *   Handles: Room joining, SDP exchange (Offer/Answer), ICE Candidate exchange.
2.  **Media (UDP/RTP):**
    *   User -> Go App (Port 50000 UDP).
    *   Handles: Encrypted Opus Audio packets.
    *   *Note:* The application will use a Single Port strategy for WebRTC to simplify firewall rules.

## 3. Business Logic & Lifecycle

### 3.1 Room Management (The "Implicit" Model)
*   **Identification:** Rooms are identified by the path `/r/{room-id}` (e.g., `domain.com/r/abc123xyz`).
*   **Creation:**
    *   When a user connects via WebSocket to a UUID:
    *   **If Room exists:** Join immediately.
*   **If Room does not exist:** **Create Room** in RAM immediately.
*   **Constraints:**
*   **Max Capacity:** 10 users per room. If full, reject connection with an error message.
*   **Destruction (Cleanup):**
    *   A background `Ticker` runs every 1 minute.
    *   Check each room: `if Room.PeerCount == 0` AND `time.Since(Room.LastEmptyTime) > 2 hours`, then `delete(RoomMap, uuid)`.

### 3.2 User Identity
*   **Anonymous:** No login.
*   **Display Name:** User inputs a nickname upon entry (stored in RAM only).
*   **Identification:** Users are tracked internally by a random `PeerID` and their Connection `RemoteAddr` (IP).

## 4. Backend Implementation Details

### 4.1 Data Structures (In-Memory)

```go
type Peer struct {
    ID          string
    Name        string
    IP          string
    Conn        *websocket.Conn
    PC          *webrtc.PeerConnection
    TrackRemote *webrtc.TrackRemote // Audio received from this user
    TrackLocal  *webrtc.TrackLocalStaticRTP // Audio sent TO other users
    Muted       bool
    JoinTime    time.Time
}

type Room struct {
    UUID          string
    Peers         map[string]*Peer
    Lock          sync.RWMutex
    LastEmptyTime time.Time
}

// Global State
var Rooms = make(map[string]*Room)
var BannedIPs = make(map[string]time.Time) // Loaded from/Saved to disk
```

### 4.2 Logging & Persistence
*   **Format:** JSON Lines (for easy parsing by the Admin UI).
*   **File:** `server.log`
*   **Events to Log:**
    *   `ROOM_CREATE`: UUID, Time
    *   `USER_JOIN`: UUID, IP, Name, PeerID
    *   `USER_LEAVE`: UUID, IP, Duration
    *   `ADMIN_ACTION`: ActionType, TargetIP
*   **IP Banning:**
    *   `banned_ips.json` saved to disk on change.
    *   Middleware checks incoming IP against this list before upgrading WebSocket.

### 4.3 WebRTC Configuration (Pion)
*   **Audio Only:** No video constraints in SDP.
*   **Codec:** Opus (Default).
*   **Buffer:** minimal jitter buffer.
*   **NACKs:** Enabled (to handle packet loss).

## 5. API & Signaling Protocol (WebSocket)

**Endpoint:** `/ws?room={uuid}&name={nickname}`

**JSON Messages:**

1.  **Signal (SDP/ICE):**
    ```json
    { "type": "offer", "sdp": "..." }
    { "type": "answer", "sdp": "..." }
    { "type": "candidate", "candidate": { "candidate": "...", "sdpMid": "0", "sdpMLineIndex": 0 } }
    ```
2.  **Room State (Initial):**
    ```json
    {
      "type": "room_state",
      "self_id": "abc",
      "peers": [{ "id": "xyz", "name": "Tan" }]
    }
    ```
3.  **Peer Join/Leave:**
    ```json
    { "type": "peer_join", "peer": { "id": "xyz", "name": "Tan" } }
    { "type": "peer_leave", "peer_id": "xyz" }
    ```
4.  **Error:**
    ```json
    { "type": "error", "message": "Room full" }
    ```

## 6. Admin Panel

**Endpoint:** `/admin?key={SECRET_KEY}`

**Features:**
1.  **Dashboard:**
    *   Total Active Rooms.
    *   Total Online Users.
    *   System Memory Usage (Go Runtime).
2.  **Log Viewer:**
    *   Reads the last N lines of `server.log`.
    *   Parses JSON and renders a table (Time | IP | Event | Detail).
3.  **Action Menu:**
    *   "Ban IP" button next to logs.

## 7. Frontend Design (UI/UX)

**Aesthetic:** "Discord-Dark"
*   **Colors:** Dark Gray (`#36393f`), Darker Gray (`#2f3136`), Accent Blurple (`#5865F2`), Success Green (`#43b581`).
*   **Font:** PingFang SC / Microsoft YaHei (system sans-serif).

**Views:**

1.  **Landing / Join:**
    *   If URL has no UUID: "Generate Room" button.
    *   If URL has UUID: "Enter Nickname" input + "Join" button.
    *   Permission Check: Explicitly ask for Microphone permission before joining.

2.  **Room (Active):**
    *   **Sidebar:** List of users.
    *   **Main Stage:** Grid of circular Avatars.
        *   **State:** Default (Gray border).
        *   **Speaking:** Green glowing border.
        *   **Muted:** Red microphone icon overlay.
    *   **Bottom Bar:**
        *   Microphone Toggle (Mute/Unmute).
        *   Mixer toggle for mic gain and per-member volume.
        *   "Disconnect" button (Red).
        *   "Copy Invite Link" button.

**Feedback Mechanism:**
*   Client-side VAD highlights avatars when speaking.
*   Short join/leave sound cues confirm room membership changes.

## 8. Implementation Steps (For AI Developer)

1.  **Setup:** Initialize Go module, setup HTTP/WS handlers.
2.  **Signaling:** Implement basic Room/Peer struct and WebSocket message loop.
3.  **WebRTC Core:** Implement Pion SFU logic (Track subscription and broadcasting).
4.  **Client:** Build the HTML/JS to capture Mic and connect to WS.
5.  **Refinement:** Add "Implicit Room" logic and "Auto-Destroy" timer.
6.  **Admin & Logging:** Add JSON logging and the Admin HTML interface.
7.  **UI Polish:** Apply the Discord-like CSS.
