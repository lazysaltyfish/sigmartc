package server

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/pion/webrtc/v3"
	"sigmartc/internal/logger"
)

const (
	maxRoomPeers    = 10
	maxNicknameRune = 12
)

var upgrader = websocket.Upgrader{
	CheckOrigin: checkWSOrigin,
}

type Handler struct {
	RoomManager *RoomManager
	// Webrtc API with custom settings if needed
	WebRTCAPI *webrtc.API
}

func NewHandler(rm *RoomManager, api *webrtc.API) *Handler {
	if api == nil {
		m := &webrtc.MediaEngine{}
		if err := m.RegisterDefaultCodecs(); err != nil {
			panic(err)
		}
		// Add custom interceptors or settings here if needed (e.g. NACKs)
		api = webrtc.NewAPI(webrtc.WithMediaEngine(m))
	}

	return &Handler{
		RoomManager: rm,
		WebRTCAPI:   api,
	}
}

func (h *Handler) HandleWS(w http.ResponseWriter, r *http.Request) {
	roomUUID := strings.TrimSpace(r.URL.Query().Get("room"))
	nickname, err := normalizeNickname(r.URL.Query().Get("name"))
	if roomUUID == "" || err != nil {
		http.Error(w, "Invalid room or name", http.StatusBadRequest)
		return
	}

	ip := clientIP(r)

	if h.RoomManager.IsBanned(ip) {
		http.Error(w, "Banned", http.StatusForbidden)
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Error("WS Upgrade failed", "err", err)
		return
	}

	peerID := uuid.New().String()
	peer := &Peer{
		ID:       peerID,
		Name:     nickname,
		IP:       ip,
		Conn:     conn,
		JoinTime: time.Now(),
	}

	room := h.RoomManager.GetOrCreateRoom(roomUUID)

	// Check capacity
	room.Lock.Lock()
	if len(room.Peers) >= maxRoomPeers {
		room.Lock.Unlock()
		peer.WriteJSON(map[string]string{"type": "error", "message": "Room full"})
		conn.Close()
		return
	}
	room.Peers[peerID] = peer
	room.Lock.Unlock()

	logger.LogEvent("USER_JOIN", slog.String("uuid", roomUUID), slog.String("ip", ip), slog.String("name", nickname), slog.String("peer_id", peerID))

	// Cleanup on exit
	defer func() {
		// Unsubscribe this peer from all forwarders (so they stop sending to this peer)
		room.ForwardersMu.RLock()
		for _, forwarder := range room.Forwarders {
			forwarder.Unsubscribe(peerID)
		}
		room.ForwardersMu.RUnlock()

		// Stop and remove this peer's own forwarder if they were sending audio
		room.ForwardersMu.Lock()
		if forwarder, exists := room.Forwarders[peerID]; exists {
			forwarder.Stop()
			delete(room.Forwarders, peerID)
		}
		room.ForwardersMu.Unlock()

		room.Lock.Lock()
		delete(room.Peers, peerID)
		if len(room.Peers) == 0 {
			room.LastEmptyTime = time.Now()
		}
		room.Lock.Unlock()
		conn.Close()
		if peer.PC != nil {
			peer.PC.Close()
		}
		logger.LogEvent("USER_LEAVE", slog.String("uuid", roomUUID), slog.String("peer_id", peerID))

		// Notify others
		room.Broadcast(peerID, map[string]any{
			"type":    "peer_leave",
			"peer_id": peerID,
		})
	}()

	// Initial signaling state: Tell the user their ID and current room peers
	h.sendRoomState(room, peer)

	// WebRTC Setup
	if err := h.setupWebRTC(room, peer); err != nil {
		peer.WriteJSON(map[string]string{"type": "error", "message": "WebRTC setup failed"})
		return
	}
	h.addExistingTracks(room, peer)

	// Signaling loop
	for {
		_, message, err := conn.ReadMessage()
		if err != nil {
			break
		}

		var msg map[string]interface{}
		if err := json.Unmarshal(message, &msg); err != nil {
			continue
		}

		h.handleSignalingMessage(room, peer, msg)
	}
}

func (h *Handler) sendRoomState(room *Room, peer *Peer) {
	room.Lock.RLock()
	peersInfo := make([]map[string]any, 0, len(room.Peers))
	for _, p := range room.Peers {
		peersInfo = append(peersInfo, map[string]any{
			"id":   p.ID,
			"name": p.Name,
		})
	}
	room.Lock.RUnlock()

	peer.WriteJSON(map[string]any{
		"type":    "room_state",
		"self_id": peer.ID,
		"peers":   peersInfo,
	})

	// Notify others about new peer
	room.Broadcast(peer.ID, map[string]any{
		"type": "peer_join",
		"peer": map[string]any{"id": peer.ID, "name": peer.Name},
	})
}

func (h *Handler) setupWebRTC(room *Room, peer *Peer) error {
	config := webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{
			{URLs: []string{"stun:stun.l.google.com:19302"}},
		},
	}

	pc, err := h.WebRTCAPI.NewPeerConnection(config)
	if err != nil {
		slog.Error("Failed to create PeerConnection", "err", err)
		return err
	}
	peer.PC = pc

	// Handle ICE Candidates
	pc.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c == nil {
			return
		}
		peer.WriteJSON(map[string]any{
			"type":      "candidate",
			"candidate": c.ToJSON(),
		})
	})

	// Handle incoming audio track
	pc.OnTrack(func(track *webrtc.TrackRemote, receiver *webrtc.RTPReceiver) {
		if track.Kind() != webrtc.RTPCodecTypeAudio {
			return
		}

		slog.Info("Received remote track", "peer", peer.Name, "id", track.ID())
		room.Lock.Lock()
		peer.TrackRemote = track
		room.Lock.Unlock()

		// Broadcast this new track to all other peers in the room
		h.broadcastTrack(room, peer, track)
	})
	return nil
}

func (h *Handler) addExistingTracks(room *Room, receiver *Peer) {
	type senderTrack struct {
		senderID string
		track    *webrtc.TrackRemote
	}

	room.Lock.RLock()
	var tracks []senderTrack
	for _, p := range room.Peers {
		if p.ID == receiver.ID || p.TrackRemote == nil {
			continue
		}
		tracks = append(tracks, senderTrack{senderID: p.ID, track: p.TrackRemote})
	}
	room.Lock.RUnlock()

	for _, t := range tracks {
		h.addTrackToPeer(receiver, t.senderID, t.track, room)
	}
}

func (h *Handler) broadcastTrack(room *Room, sender *Peer, track *webrtc.TrackRemote) {
	// Create a forwarder for this sender's track
	forwarder := NewTrackForwarder(sender.ID, track)

	room.ForwardersMu.Lock()
	room.Forwarders[sender.ID] = forwarder
	room.ForwardersMu.Unlock()

	// Add the track to all existing peers in the room
	room.Lock.RLock()
	receivers := make([]*Peer, 0, len(room.Peers))
	for _, receiver := range room.Peers {
		if receiver.ID == sender.ID {
			continue
		}
		receivers = append(receivers, receiver)
	}
	room.Lock.RUnlock()

	for _, receiver := range receivers {
		h.subscribeToForwarder(receiver, sender.ID, forwarder, track)
	}

	// Wait for negotiation to stabilize before starting the forwarder
	// This is done in a goroutine to not block the OnTrack callback
	go func() {
		// Give time for all receivers to set up their tracks
		time.Sleep(100 * time.Millisecond)
		forwarder.Start()
	}()
}

// subscribeToForwarder creates a local track for the receiver and subscribes it to the forwarder.
func (h *Handler) subscribeToForwarder(receiver *Peer, senderID string, forwarder *TrackForwarder, track *webrtc.TrackRemote) {
	if receiver.PC == nil {
		return
	}

	// Create a local track to push data to the receiver
	// Use senderID as the StreamID so the client can map it to a user
	localTrack, err := webrtc.NewTrackLocalStaticRTP(track.Codec().RTPCodecCapability, track.ID(), senderID)
	if err != nil {
		slog.Error("Failed to create local track", "err", err)
		return
	}

	_, err = receiver.PC.AddTrack(localTrack)
	if err != nil {
		slog.Error("Failed to add track to PC", "err", err)
		return
	}

	// Store the outgoing track for this receiver
	receiver.OutTracksMu.Lock()
	if receiver.OutTracks == nil {
		receiver.OutTracks = make(map[string]*webrtc.TrackLocalStaticRTP)
	}
	receiver.OutTracks[senderID] = localTrack
	receiver.OutTracksMu.Unlock()

	// Subscribe to the forwarder
	forwarder.Subscribe(receiver.ID, localTrack)

	// Trigger renegotiation
	h.sendNegotiationNeeded(receiver)
}

func (h *Handler) addTrackToPeer(receiver *Peer, senderID string, track *webrtc.TrackRemote, room *Room) {
	// Get the forwarder for this sender
	room.ForwardersMu.RLock()
	forwarder, exists := room.Forwarders[senderID]
	room.ForwardersMu.RUnlock()

	if !exists {
		// The sender doesn't have a forwarder yet, which means they haven't started sending.
		// This shouldn't happen in normal flow but handle it gracefully.
		slog.Warn("Forwarder not found for sender", "senderID", senderID)
		return
	}

	h.subscribeToForwarder(receiver, senderID, forwarder, track)
}

func (h *Handler) sendNegotiationNeeded(peer *Peer) {
	if peer.PC == nil {
		return
	}
	offer, err := peer.PC.CreateOffer(nil)
	if err != nil {
		return
	}
	if err = peer.PC.SetLocalDescription(offer); err != nil {
		return
	}
	peer.WriteJSON(map[string]any{
		"type": "offer",
		"sdp":  offer.SDP,
	})
}

func waitForNegotiationStable(pc *webrtc.PeerConnection, timeout time.Duration) bool {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()

	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	sawLocalOffer := false
	for {
		state := pc.SignalingState()
		if state == webrtc.SignalingStateHaveLocalOffer {
			sawLocalOffer = true
		}
		if sawLocalOffer && state == webrtc.SignalingStateStable && pc.RemoteDescription() != nil {
			return true
		}

		select {
		case <-deadline.C:
			return false
		case <-ticker.C:
		}
	}
}

func (h *Handler) handleSignalingMessage(room *Room, peer *Peer, msg map[string]any) {
	t, ok := msg["type"].(string)
	if !ok {
		return
	}
	if peer.PC == nil {
		return
	}

	switch t {
	case "offer":
		sdp, _ := msg["sdp"].(string)
		err := peer.PC.SetRemoteDescription(webrtc.SessionDescription{
			Type: webrtc.SDPTypeOffer,
			SDP:  sdp,
		})
		if err != nil {
			slog.Error("SetRemoteDescription failed", "err", err)
			return
		}
		answer, err := peer.PC.CreateAnswer(nil)
		if err != nil {
			return
		}
		if err = peer.PC.SetLocalDescription(answer); err != nil {
			return
		}
		peer.WriteJSON(map[string]any{
			"type": "answer",
			"sdp":  answer.SDP,
		})

	case "answer":
		sdp, _ := msg["sdp"].(string)
		peer.PC.SetRemoteDescription(webrtc.SessionDescription{
			Type: webrtc.SDPTypeAnswer,
			SDP:  sdp,
		})

	case "candidate":
		candidateData, _ := msg["candidate"].(map[string]any)
		candidateJSON, _ := json.Marshal(candidateData)
		var candidate webrtc.ICECandidateInit
		if err := json.Unmarshal(candidateJSON, &candidate); err != nil {
			return
		}
		peer.PC.AddICECandidate(candidate)
	}
}

func normalizeNickname(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if name == "" {
		return "", errors.New("missing name")
	}
	if utf8.RuneCountInString(name) > maxNicknameRune {
		return "", errors.New("name too long")
	}
	return name, nil
}

func clientIP(r *http.Request) string {
	remoteIP := parseRemoteIP(r.RemoteAddr)
	if remoteIP != nil && isTrustedProxy(remoteIP) {
		if realIP := strings.TrimSpace(r.Header.Get("X-Real-IP")); realIP != "" {
			if ip := net.ParseIP(realIP); ip != nil {
				return ip.String()
			}
		}
		if xff := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); xff != "" {
			parts := strings.Split(xff, ",")
			for _, part := range parts {
				candidate := strings.TrimSpace(part)
				if candidate == "" {
					continue
				}
				if ip := net.ParseIP(candidate); ip != nil {
					return ip.String()
				}
			}
		}
	}

	if remoteIP != nil {
		return remoteIP.String()
	}
	return r.RemoteAddr
}

func checkWSOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	originURL, err := url.Parse(origin)
	if err != nil {
		return false
	}
	if originURL.Host == "" {
		return false
	}

	reqHost := requestHost(r)
	originHost := stripPort(originURL.Host)
	if reqHost == "" || originHost == "" {
		return false
	}
	if !strings.EqualFold(reqHost, originHost) {
		return false
	}
	if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
		return strings.EqualFold(proto, originURL.Scheme)
	}
	return true
}

func requestHost(r *http.Request) string {
	if xfwd := r.Header.Get("X-Forwarded-Host"); xfwd != "" {
		parts := strings.Split(xfwd, ",")
		return stripPort(strings.TrimSpace(parts[0]))
	}
	return stripPort(r.Host)
}

func stripPort(host string) string {
	host = strings.TrimSpace(host)
	if host == "" {
		return ""
	}
	if strings.HasPrefix(host, "[") {
		if h, _, err := net.SplitHostPort(host); err == nil {
			return strings.Trim(h, "[]")
		}
		return strings.Trim(host, "[]")
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		return h
	}
	return host
}

func parseRemoteIP(remoteAddr string) net.IP {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err == nil {
		return net.ParseIP(host)
	}
	return net.ParseIP(remoteAddr)
}

func isTrustedProxy(ip net.IP) bool {
	if ip == nil {
		return false
	}
	return ip.IsLoopback() || ip.IsPrivate()
}
