package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"runtime"

	"sigmartc/internal/logger"
)

func (h *Handler) HandleAdmin(w http.ResponseWriter, r *http.Request) {
	key := r.URL.Query().Get("key")
	if key == "" || key != h.RoomManager.AdminKey {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	action := r.URL.Query().Get("action")
	switch action {
	case "stats":
		h.getStats(w)
	case "logs":
		h.getLogs(w)
	case "ban":
		ip := r.URL.Query().Get("ip")
		if ip != "" {
			h.RoomManager.BanIP(ip)
			fmt.Fprintf(w, "Banned %s", ip)
		}
	default:
		// Serve simple Admin HTML (Embedded for simplicity, or we could load from web/templates)
		h.serveAdminUI(w)
	}
}

func (h *Handler) getStats(w http.ResponseWriter) {
	h.RoomManager.Lock.RLock()
	roomCount := len(h.RoomManager.Rooms)
	userCount := 0
	for _, room := range h.RoomManager.Rooms {
		room.Lock.RLock()
		userCount += len(room.Peers)
		room.Lock.RUnlock()
	}
	h.RoomManager.Lock.RUnlock()

	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	stats := map[string]any{
		"rooms":           roomCount,
		"users":           userCount,
		"memory_alloc_mb": m.Alloc / 1024 / 1024,
		"goroutines":      runtime.NumGoroutine(),
	}
	json.NewEncoder(w).Encode(stats)
}

func (h *Handler) getLogs(w http.ResponseWriter) {
	lines := logger.GetRecentLogs(100)
	json.NewEncoder(w).Encode(lines)
}

func (h *Handler) serveAdminUI(w http.ResponseWriter) {
	// For a real project, we'd use a template. For this CLI-based rapid dev,
	// a compact embedded HTML is more reliable.
	fmt.Fprintf(w, `
	<html>
	<head><title>GhostTalk Admin</title><style>body{font-family:sans-serif;background:#222;color:#eee;padding:20px;}</style></head>
	<body>
		<h1>GhostTalk Stats</h1>
		<div id="stats">Loading...</div>
		<h2>Recent Logs</h2>
		<pre id="logs" style="background:#000;padding:10px;overflow:auto;max-height:400px;"></pre>
		<input id="banIp" placeholder="IP to ban"><button onclick="ban()">Ban</button>
		<script>
			const key = new URLSearchParams(window.location.search).get('key');
			fetch('/admin?action=stats&key='+key).then(r=>r.json()).then(d=>{
				document.getElementById('stats').innerText = JSON.stringify(d, null, 2);
			});
			fetch('/admin?action=logs&key='+key).then(r=>r.json()).then(d=>{
				document.getElementById('logs').innerText = d.join('\n');
			});
			function ban() {
				const ip = document.getElementById('banIp').value;
				fetch('/admin?action=ban&ip='+ip+'&key='+key).then(()=>location.reload());
			}
		</script>
	</body>
	</html>
	`)
}
