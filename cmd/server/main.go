package main

import (
	"flag"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sigmartc/internal/logger"
	"sigmartc/internal/server"
	"strings"
	"syscall"
	"time"

	"github.com/pion/ice/v2"
	"github.com/pion/webrtc/v3"
)

var Version = "dev"
var BuildTime = "unknown"

func main() {
	port := flag.Int("port", 8080, "HTTP Port")
	adminKey := flag.String("admin-key", "change-me-123", "Admin panel secret key")
	rtcUDPPort := flag.Int("rtc-udp-port", 50000, "WebRTC ICE UDP port")
	turnServer := flag.String("turn-server", "", "TURN server URL (e.g., turn:your-server.com:3478)")
	turnUser := flag.String("turn-user", "", "TURN server username")
	turnPass := flag.String("turn-pass", "", "TURN server password")
	flag.Parse()

	// 1. Initialize Logger
	if err := logger.InitLogger("server.log"); err != nil {
		fmt.Printf("Failed to init logger: %v\n", err)
		os.Exit(1)
	}
	defer logger.Close()

	// 2. Initialize Core Logic
	rm := server.NewRoomManager(*adminKey, "banned_ips.json")

	// 3. Setup WebRTC API with ICE UDP mux
	udpMux, err := ice.NewMultiUDPMuxFromPort(*rtcUDPPort)
	if err != nil {
		slog.Error("Failed to create ICE UDP mux", "err", err, "port", *rtcUDPPort)
		os.Exit(1)
	}
	defer func() {
		if closeErr := udpMux.Close(); closeErr != nil {
			slog.Error("Failed to close ICE UDP mux", "err", closeErr)
		}
	}()

	m := &webrtc.MediaEngine{}
	if err := m.RegisterDefaultCodecs(); err != nil {
		slog.Error("Failed to register codecs", "err", err)
		os.Exit(1)
	}

	settings := webrtc.SettingEngine{}
	settings.SetICEUDPMux(udpMux)
	// ICE keepalive: send STUN binding indication every 8 seconds to maintain NAT mappings
	// This helps prevent disconnections when ISP NAT entries expire (typically 30-60s)
	settings.SetICETimeouts(8*time.Second, 30*time.Second, 5*time.Second)

	api := webrtc.NewAPI(
		webrtc.WithMediaEngine(m),
		webrtc.WithSettingEngine(settings),
	)

	slog.Info("ICE UDP mux enabled", "port", *rtcUDPPort)

	// Build ICE configuration
	iceConfig := &webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{
			{URLs: []string{"stun:stun.l.google.com:19302"}},
		},
	}
	if *turnServer != "" {
		iceConfig.ICEServers = append(iceConfig.ICEServers, webrtc.ICEServer{
			URLs:           []string{*turnServer},
			Username:       *turnUser,
			Credential:     *turnPass,
			CredentialType: webrtc.ICECredentialTypePassword,
		})
		slog.Info("TURN server configured", "server", *turnServer)
	}

	h := server.NewHandler(rm, api, iceConfig)

	// 4. Routing
	mux := http.NewServeMux()

	// API & Signaling
	mux.HandleFunc("/ws", h.HandleWS)
	mux.Handle("/admin", withSecurityHeaders(http.HandlerFunc(h.HandleAdmin)))

	// Dynamic config.js endpoint (must be before static file server)
	mux.HandleFunc("/static/js/config.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript")
		w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")

		// Always include STUN as fallback for NAT traversal
		fmt.Fprint(w, "window.ICE_CONFIG={iceServers:[{urls:'stun:stun.l.google.com:19302'}")

		// Add TURN server if configured
		if *turnServer != "" {
			fmt.Fprintf(w, ",{urls:'%s',username:'%s',credential:'%s'}", *turnServer, *turnUser, *turnPass)
		}

		fmt.Fprint(w, "]};")
	})

	// Frontend Static Files
	fs := http.FileServer(http.Dir("web/static"))
	mux.Handle("/static/", withSecurityHeaders(http.StripPrefix("/static/", fs)))

	// SPA Routing: All /r/* or / paths serve index.html
	mux.Handle("/", withSecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// If it's the root or a room path, serve the app
		if r.URL.Path == "/" || (len(r.URL.Path) > 3 && r.URL.Path[:3] == "/r/") {
			tmpl, err := template.ParseFiles("web/templates/index.html")
			if err != nil {
				http.Error(w, "Internal Server Error", http.StatusInternalServerError)
				slog.Error("Failed to parse template", "err", err)
				return
			}
			
			data := struct {
				Version  string
				BuildTime string
			}{
				Version:   Version,
				BuildTime: BuildTime,
			}

			if err := tmpl.Execute(w, data); err != nil {
				slog.Error("Failed to execute template", "err", err)
			}
			return
		}
		http.NotFound(w, r)
	})))

	// 5. Start Server
	serverAddr := fmt.Sprintf(":%d", *port)
	slog.Info("GhostTalk Server Starting", "port", *port)

	go func() {
		if err := http.ListenAndServe(serverAddr, mux); err != nil {
			slog.Error("Server failed", "err", err)
			os.Exit(1)
		}
	}()

	// Graceful Shutdown
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	slog.Info("Shutting down...")
}

func withSecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		setSecurityHeaders(w, r)
		next.ServeHTTP(w, r)
	})
}

func setSecurityHeaders(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Permissions-Policy", "microphone=(self)")
	w.Header().Set("Content-Security-Policy", buildCSP(r))
}

func buildCSP(r *http.Request) string {
	host := r.Host
	if xfwd := r.Header.Get("X-Forwarded-Host"); xfwd != "" {
		parts := strings.Split(xfwd, ",")
		host = strings.TrimSpace(parts[0])
	}
	host = strings.TrimSpace(host)
	connectSrc := "'self' stun: turn: turns:"
	if host != "" {
		connectSrc = fmt.Sprintf("'self' ws://%s wss://%s stun: turn: turns:", host, host)
	}
	return strings.Join([]string{
		"default-src 'self'",
		"base-uri 'self'",
		"frame-ancestors 'none'",
		"form-action 'self'",
		"script-src 'self'",
		"style-src 'self' 'unsafe-inline'",
		"img-src 'self' data:",
		"media-src 'self' blob:",
		"connect-src " + connectSrc,
	}, "; ")
}
