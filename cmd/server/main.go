package main

import (
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sigmartc/internal/logger"
	"sigmartc/internal/server"
	"syscall"

	"github.com/pion/ice/v2"
	"github.com/pion/webrtc/v3"
)

func main() {
	port := flag.Int("port", 8080, "HTTP Port")
	adminKey := flag.String("admin-key", "change-me-123", "Admin panel secret key")
	rtcUDPPort := flag.Int("rtc-udp-port", 50000, "WebRTC ICE UDP port")
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

	api := webrtc.NewAPI(
		webrtc.WithMediaEngine(m),
		webrtc.WithSettingEngine(settings),
	)

	slog.Info("ICE UDP mux enabled", "port", *rtcUDPPort)

	h := server.NewHandler(rm, api)

	// 4. Routing
	mux := http.NewServeMux()

	// API & Signaling
	mux.HandleFunc("/ws", h.HandleWS)
	mux.HandleFunc("/admin", h.HandleAdmin)

	// Frontend Static Files
	fs := http.FileServer(http.Dir("web/static"))
	mux.Handle("/static/", http.StripPrefix("/static/", fs))

	// SPA Routing: All /r/* or / paths serve index.html
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// If it's the root or a room path, serve the app
		if r.URL.Path == "/" || (len(r.URL.Path) > 3 && r.URL.Path[:3] == "/r/") {
			http.ServeFile(w, r, "web/templates/index.html")
			return
		}
		http.NotFound(w, r)
	})

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
