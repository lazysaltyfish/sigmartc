package server

import (
	"encoding/json"
	"errors"
	"fmt"
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
	wsWriteWait     = 5 * time.Second
	wsPongWait      = 60 * time.Second
	wsPingInterval  = 30 * time.Second
	iceRestartDelay = 5 * time.Second
	iceRestartMin   = 15 * time.Second
)

var upgrader = websocket.Upgrader{
	CheckOrigin: checkWSOrigin,
}

type Handler struct {
	RoomManager *RoomManager
	// Webrtc API with custom settings if needed
	WebRTCAPI *webrtc.API
	// Optional ICE config override (useful for tests).
	ICEConfig *webrtc.Configuration
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
		Done:     make(chan struct{}),
	}

	conn.SetReadDeadline(time.Now().Add(wsPongWait))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(wsPongWait))
		return nil
	})
	pingTicker := time.NewTicker(wsPingInterval)
	defer pingTicker.Stop()
	go func() {
		for {
			select {
			case <-peer.Done:
				return
			case <-pingTicker.C:
				peer.WsMutex.Lock()
				err := conn.WriteControl(websocket.PingMessage, []byte{}, time.Now().Add(wsWriteWait))
				peer.WsMutex.Unlock()
				if err != nil {
					slog.Warn("WS ping failed", "peer_id", peer.ID, "err", err)
					peer.SignalDone()
					_ = conn.Close()
					return
				}
			}
		}
	}()

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
		peer.SignalDone()
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
	if h.ICEConfig != nil {
		config = *h.ICEConfig
	}

	pc, err := h.WebRTCAPI.NewPeerConnection(config)
	if err != nil {
		slog.Error("Failed to create PeerConnection", "err", err)
		return err
	}
	peer.PC = pc

	pc.OnICEConnectionStateChange(func(state webrtc.ICEConnectionState) {
		slog.Info("ICE connection state changed", "peer_id", peer.ID, "state", state.String())
		switch state {
		case webrtc.ICEConnectionStateFailed:
			h.requestICERestart(peer)
		case webrtc.ICEConnectionStateDisconnected:
			go func() {
				select {
				case <-peer.Done:
					return
				case <-time.After(iceRestartDelay):
				}
				if peer.PC != nil && peer.PC.ICEConnectionState() == webrtc.ICEConnectionStateDisconnected {
					h.requestICERestart(peer)
				}
			}()
		}
	})
	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		slog.Info("Peer connection state changed", "peer_id", peer.ID, "state", state.String())
		if state == webrtc.PeerConnectionStateFailed {
			h.requestICERestart(peer)
		}
	})

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

		// Broadcast this new track to all other peers in the room
		h.broadcastTrack(room, peer, track)
	})
	return nil
}

func (h *Handler) addExistingTracks(room *Room, receiver *Peer) {
	type forwarderEntry struct {
		senderID  string
		forwarder *TrackForwarder
		track     *webrtc.TrackRemote
	}

	room.ForwardersMu.RLock()
	forwarders := make([]forwarderEntry, 0, len(room.Forwarders))
	for senderID, forwarder := range room.Forwarders {
		if senderID == receiver.ID || forwarder == nil || forwarder.TrackRemote == nil {
			continue
		}
		forwarders = append(forwarders, forwarderEntry{
			senderID:  senderID,
			forwarder: forwarder,
			track:     forwarder.TrackRemote,
		})
	}
	room.ForwardersMu.RUnlock()

	for _, entry := range forwarders {
		h.subscribeToForwarder(receiver, entry.senderID, entry.forwarder, entry.track)
	}
}

func (h *Handler) broadcastTrack(room *Room, sender *Peer, track *webrtc.TrackRemote) {
	// Create a forwarder for this sender's track
	forwarder := NewTrackForwarder(sender.ID, track)
	forwarder.onStop = func(err error) {
		room.ForwardersMu.Lock()
		current, exists := room.Forwarders[sender.ID]
		if exists && current == forwarder {
			delete(room.Forwarders, sender.ID)
		}
		room.ForwardersMu.Unlock()
	}

	var oldForwarder *TrackForwarder
	room.ForwardersMu.Lock()
	if existing, exists := room.Forwarders[sender.ID]; exists {
		oldForwarder = existing
	}
	room.Forwarders[sender.ID] = forwarder
	room.ForwardersMu.Unlock()
	if oldForwarder != nil && oldForwarder != forwarder {
		oldForwarder.Stop()
	}

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

	// Start forwarding immediately; no fixed sleep.
	go forwarder.Start()
}

// subscribeToForwarder creates a local track for the receiver and subscribes it to the forwarder.
func (h *Handler) subscribeToForwarder(receiver *Peer, senderID string, forwarder *TrackForwarder, track *webrtc.TrackRemote) {
	if receiver.PC == nil {
		return
	}
	if receiver.ID == senderID {
		return
	}

	receiver.OutTracksMu.RLock()
	existingTrack := receiver.OutTracks[senderID]
	receiver.OutTracksMu.RUnlock()
	if existingTrack != nil {
		forwarder.Subscribe(receiver.ID, existingTrack)
		return
	}

	// Prevent duplicate outbound tracks for the same (receiver, sender) pair.
	// This can happen when addExistingTracks() and broadcastTrack() race for a newly joined peer.
	receiver.OutTracksMu.Lock()
	if existingTrack := receiver.OutTracks[senderID]; existingTrack != nil {
		receiver.OutTracksMu.Unlock()
		forwarder.Subscribe(receiver.ID, existingTrack)
		return
	}

	// Create a local track to push data to the receiver
	// Use senderID as the StreamID so the client can map it to a user
	trackID := fmt.Sprintf("%s-audio", senderID)
	localTrack, err := webrtc.NewTrackLocalStaticRTP(track.Codec().RTPCodecCapability, trackID, senderID)
	if err != nil {
		receiver.OutTracksMu.Unlock()
		slog.Error("Failed to create local track", "err", err)
		return
	}

	sender, err := receiver.PC.AddTrack(localTrack)
	if err != nil {
		receiver.OutTracksMu.Unlock()
		slog.Error("Failed to add track to PC", "err", err)
		return
	}

	// Store the outgoing track for this receiver before unlocking, so concurrent callers can reuse it.
	if receiver.OutTracks == nil {
		receiver.OutTracks = make(map[string]*webrtc.TrackLocalStaticRTP)
	}
	receiver.OutTracks[senderID] = localTrack
	receiver.OutTracksMu.Unlock()

	go func() {
		rtcpBuf := make([]byte, 1500)
		for {
			if _, _, rtcpErr := sender.Read(rtcpBuf); rtcpErr != nil {
				return
			}
		}
	}()

	// Subscribe to the forwarder
	forwarder.Subscribe(receiver.ID, localTrack)

	// Trigger renegotiation
	h.requestNegotiation(receiver)
}

func (h *Handler) requestNegotiation(peer *Peer) {
	h.requestNegotiationWithICE(peer, false)
}

func (h *Handler) requestICERestart(peer *Peer) {
	h.requestNegotiationWithICE(peer, true)
}

func (h *Handler) requestNegotiationWithICE(peer *Peer, iceRestart bool) {
	peer.NegotiationMu.Lock()
	if iceRestart {
		now := time.Now()
		if !peer.LastIceRestart.IsZero() && now.Sub(peer.LastIceRestart) < iceRestartMin {
			peer.NegotiationMu.Unlock()
			return
		}
		peer.LastIceRestart = now
		peer.IceRestartPending = true
	}
	peer.NegotiationPending = true
	if peer.NegotiationInProgress {
		peer.NegotiationMu.Unlock()
		return
	}
	peer.NegotiationInProgress = true
	peer.NegotiationMu.Unlock()

	go h.runNegotiation(peer)
}

func (h *Handler) runNegotiation(peer *Peer) {
	defer func() {
		peer.NegotiationMu.Lock()
		peer.NegotiationInProgress = false
		peer.NegotiationMu.Unlock()
	}()

	for {
		select {
		case <-peer.Done:
			return
		default:
		}

		peer.NegotiationMu.Lock()
		pending := peer.NegotiationPending
		iceRestart := peer.IceRestartPending
		peer.NegotiationMu.Unlock()
		if !pending {
			return
		}

		pc := peer.PC
		if pc == nil {
			peer.NegotiationMu.Lock()
			peer.NegotiationPending = false
			peer.IceRestartPending = false
			peer.NegotiationMu.Unlock()
			return
		}
		if pc.ConnectionState() == webrtc.PeerConnectionStateClosed || pc.SignalingState() == webrtc.SignalingStateClosed {
			peer.NegotiationMu.Lock()
			peer.NegotiationPending = false
			peer.IceRestartPending = false
			peer.NegotiationMu.Unlock()
			return
		}

		if pc.SignalingState() != webrtc.SignalingStateStable || pc.RemoteDescription() == nil {
			time.Sleep(50 * time.Millisecond)
			continue
		}

		peer.NegotiationMu.Lock()
		peer.NegotiationPending = false
		peer.MakingOffer = true
		peer.NegotiationMu.Unlock()

		var opts *webrtc.OfferOptions
		if iceRestart {
			opts = &webrtc.OfferOptions{ICERestart: true}
		}
		offer, err := pc.CreateOffer(opts)
		if err == nil {
			err = pc.SetLocalDescription(offer)
		}

		peer.NegotiationMu.Lock()
		peer.MakingOffer = false
		if err != nil {
			peer.NegotiationPending = true
		} else {
			peer.IceRestartPending = false
		}
		peer.NegotiationMu.Unlock()

		if err != nil {
			slog.Warn("Failed to create offer", "peer_id", peer.ID, "err", err)
			time.Sleep(100 * time.Millisecond)
			continue
		}

		peer.WriteJSON(map[string]any{
			"type": "offer",
			"sdp":  offer.SDP,
		})
	}
}

func (h *Handler) flushPendingCandidates(peer *Peer) {
	peer.PendingCandidatesMu.Lock()
	pending := peer.PendingCandidates
	peer.PendingCandidates = nil
	peer.PendingCandidatesMu.Unlock()

	for _, candidate := range pending {
		if err := peer.PC.AddICECandidate(candidate); err != nil {
			slog.Warn("Failed to add pending ICE candidate", "peer_id", peer.ID, "err", err)
		}
	}
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
		state := peer.PC.SignalingState()
		peer.NegotiationMu.Lock()
		offerCollision := peer.MakingOffer || state == webrtc.SignalingStateHaveLocalOffer
		if offerCollision {
			peer.NegotiationPending = true
			peer.MakingOffer = false
		}
		peer.NegotiationMu.Unlock()
		if state == webrtc.SignalingStateHaveRemoteOffer {
			slog.Warn("Dropping offer while remote offer pending", "peer_id", peer.ID)
			return
		}
		if state == webrtc.SignalingStateHaveLocalOffer {
			if err := peer.PC.SetLocalDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeRollback}); err != nil {
				slog.Warn("Rollback failed", "peer_id", peer.ID, "err", err)
				return
			}
		}

		sdp, _ := msg["sdp"].(string)
		err := peer.PC.SetRemoteDescription(webrtc.SessionDescription{
			Type: webrtc.SDPTypeOffer,
			SDP:  sdp,
		})
		if err != nil {
			slog.Error("SetRemoteDescription failed", "err", err)
			return
		}
		h.flushPendingCandidates(peer)
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
		if offerCollision {
			h.requestNegotiation(peer)
		}

	case "answer":
		sdp, _ := msg["sdp"].(string)
		if err := peer.PC.SetRemoteDescription(webrtc.SessionDescription{
			Type: webrtc.SDPTypeAnswer,
			SDP:  sdp,
		}); err != nil {
			slog.Error("SetRemoteDescription failed", "err", err)
			return
		}
		h.flushPendingCandidates(peer)

	case "candidate":
		candidateData, _ := msg["candidate"].(map[string]any)
		candidateJSON, _ := json.Marshal(candidateData)
		var candidate webrtc.ICECandidateInit
		if err := json.Unmarshal(candidateJSON, &candidate); err != nil {
			return
		}
		if peer.PC.RemoteDescription() == nil {
			peer.PendingCandidatesMu.Lock()
			peer.PendingCandidates = append(peer.PendingCandidates, candidate)
			peer.PendingCandidatesMu.Unlock()
			return
		}
		if err := peer.PC.AddICECandidate(candidate); err != nil {
			slog.Warn("Failed to add ICE candidate", "peer_id", peer.ID, "err", err)
		}
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
