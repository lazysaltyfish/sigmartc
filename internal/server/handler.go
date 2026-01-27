package server

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/pion/webrtc/v3"
	"sigmartc/internal/logger"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
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
	roomUUID := r.URL.Query().Get("room")
	nickname := r.URL.Query().Get("name")

	if roomUUID == "" || nickname == "" {
		http.Error(w, "Missing room or name", http.StatusBadRequest)
		return
	}

	ip := r.Header.Get("X-Forwarded-For")
	if ip == "" {
		ip = r.RemoteAddr
	}
	// Strip port if present
	if idx := strings.LastIndex(ip, ":"); idx != -1 {
		ip = ip[:idx]
	}

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
	if len(room.Peers) >= 8 {
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
	h.setupWebRTC(room, peer)
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
	defer room.Lock.RUnlock()

	peersInfo := []map[string]any{}
	for _, p := range room.Peers {
		peersInfo = append(peersInfo, map[string]any{
			"id":   p.ID,
			"name": p.Name,
		})
	}

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

func (h *Handler) setupWebRTC(room *Room, peer *Peer) {
	config := webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{
			{URLs: []string{"stun:stun.l.google.com:19302"}},
		},
	}

	pc, err := h.WebRTCAPI.NewPeerConnection(config)
	if err != nil {
		slog.Error("Failed to create PeerConnection", "err", err)
		return
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
		h.addTrackToPeer(receiver, t.senderID, t.track)
	}
}

func (h *Handler) broadcastTrack(room *Room, sender *Peer, track *webrtc.TrackRemote) {
	room.Lock.RLock()
	defer room.Lock.RUnlock()

	for _, receiver := range room.Peers {
		if receiver.ID == sender.ID {
			continue
		}
		h.addTrackToPeer(receiver, sender.ID, track)
	}
}

func (h *Handler) addTrackToPeer(receiver *Peer, senderID string, track *webrtc.TrackRemote) {
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

	// Trigger renegotiation
	h.sendNegotiationNeeded(receiver)

	// Forward packets only after renegotiation is complete to avoid early SSRC drops.
	go func() {
		if !waitForNegotiationStable(receiver.PC, 10*time.Second) {
			return
		}
		rtpBuf := make([]byte, 1500)
		for {
			n, _, err := track.Read(rtpBuf)
			if err != nil {
				return
			}
			if _, err = localTrack.Write(rtpBuf[:n]); err != nil {
				return
			}
		}
	}()
}

func (h *Handler) sendNegotiationNeeded(peer *Peer) {
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
		json.Unmarshal(candidateJSON, &candidate)
		peer.PC.AddICECandidate(candidate)
	}
}
