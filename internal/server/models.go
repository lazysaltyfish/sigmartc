package server

import (
	"encoding/json"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pion/webrtc/v3"
	"sigmartc/internal/logger"
)

// Peer represents a connected user in a room.
type Peer struct {
	ID   string
	Name string
	IP   string

	Conn    *websocket.Conn
	WsMutex sync.Mutex

	PC          *webrtc.PeerConnection
	TrackRemote *webrtc.TrackRemote

	// OutTracks maps senderID to the local track used to forward that sender's audio to this peer
	OutTracks   map[string]*webrtc.TrackLocalStaticRTP
	OutTracksMu sync.RWMutex

	NegotiationMu         sync.Mutex
	NegotiationPending    bool
	NegotiationInProgress bool
	MakingOffer           bool

	PendingCandidatesMu sync.Mutex
	PendingCandidates   []webrtc.ICECandidateInit

	Muted    bool
	JoinTime time.Time
}

// TrackForwarder manages fan-out from one sender's TrackRemote to multiple receivers.
// It reads RTP packets once and writes them to all subscribers.
type TrackForwarder struct {
	SenderID    string
	TrackRemote *webrtc.TrackRemote

	mu          sync.RWMutex
	subscribers map[string]*webrtc.TrackLocalStaticRTP // receiverID -> localTrack

	done chan struct{}
}

// NewTrackForwarder creates a new forwarder for the given sender's track.
func NewTrackForwarder(senderID string, track *webrtc.TrackRemote) *TrackForwarder {
	return &TrackForwarder{
		SenderID:    senderID,
		TrackRemote: track,
		subscribers: make(map[string]*webrtc.TrackLocalStaticRTP),
		done:        make(chan struct{}),
	}
}

// Subscribe adds a receiver's local track to the forwarder.
func (f *TrackForwarder) Subscribe(receiverID string, localTrack *webrtc.TrackLocalStaticRTP) {
	f.mu.Lock()
	f.subscribers[receiverID] = localTrack
	f.mu.Unlock()
}

// Unsubscribe removes a receiver's local track from the forwarder.
func (f *TrackForwarder) Unsubscribe(receiverID string) {
	f.mu.Lock()
	delete(f.subscribers, receiverID)
	f.mu.Unlock()
}

// SubscriberCount returns the number of active subscribers.
func (f *TrackForwarder) SubscriberCount() int {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return len(f.subscribers)
}

// Start begins the forwarding loop. It reads from TrackRemote and writes to all subscribers.
// This method blocks until the track ends or Stop is called.
func (f *TrackForwarder) Start() {
	rtpBuf := make([]byte, 1500)
	for {
		select {
		case <-f.done:
			return
		default:
		}

		n, _, err := f.TrackRemote.Read(rtpBuf)
		if err != nil {
			return
		}

		f.mu.RLock()
		for _, localTrack := range f.subscribers {
			// Write to each subscriber, ignore individual write errors
			localTrack.Write(rtpBuf[:n])
		}
		f.mu.RUnlock()
	}
}

// Stop signals the forwarder to stop reading.
func (f *TrackForwarder) Stop() {
	select {
	case <-f.done:
		// Already closed
	default:
		close(f.done)
	}
}

// Room represents a voice chat session.
type Room struct {
	UUID  string
	Peers map[string]*Peer
	Lock  sync.RWMutex

	// Forwarders maps senderID to the forwarder handling that sender's audio
	Forwarders   map[string]*TrackForwarder
	ForwardersMu sync.RWMutex

	LastEmptyTime time.Time
	CreatedAt     time.Time
}

// RoomManager manages the lifecycle of rooms.
type RoomManager struct {
	Rooms       map[string]*Room
	BannedIPs   map[string]bool
	AdminKey    string
	BanListPath string
	Lock        sync.RWMutex
}

func NewRoomManager(adminKey string, banListPath string) *RoomManager {
	rm := &RoomManager{
		Rooms:       make(map[string]*Room),
		BannedIPs:   make(map[string]bool),
		AdminKey:    adminKey,
		BanListPath: banListPath,
	}
	rm.loadBanList()
	go rm.startCleanupTicker()
	return rm
}

func (rm *RoomManager) loadBanList() {
	data, err := os.ReadFile(rm.BanListPath)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Warn("Failed to read ban list", "err", err)
		}
		return
	}
	if err := json.Unmarshal(data, &rm.BannedIPs); err != nil {
		slog.Error("Failed to parse ban list", "err", err)
	}
}

func (rm *RoomManager) saveBanList() error {
	data, err := json.Marshal(rm.BannedIPs)
	if err != nil {
		return err
	}
	return os.WriteFile(rm.BanListPath, data, 0644)
}

func (rm *RoomManager) BanIP(ip string) {
	rm.Lock.Lock()
	rm.BannedIPs[ip] = true
	saveErr := rm.saveBanList()
	rm.Lock.Unlock()
	if saveErr != nil {
		slog.Error("Failed to save ban list", "err", saveErr)
	}
	logger.LogEvent("ADMIN_BAN", slog.String("ip", ip))
}

func (rm *RoomManager) IsBanned(ip string) bool {
	rm.Lock.RLock()
	defer rm.Lock.RUnlock()
	return rm.BannedIPs[ip]
}

func (rm *RoomManager) GetOrCreateRoom(uuid string) *Room {
	rm.Lock.Lock()
	defer rm.Lock.Unlock()

	room, exists := rm.Rooms[uuid]
	if exists {
		return room
	}

	room = &Room{
		UUID:          uuid,
		Peers:         make(map[string]*Peer),
		Forwarders:    make(map[string]*TrackForwarder),
		CreatedAt:     time.Now(),
		LastEmptyTime: time.Now(),
	}
	rm.Rooms[uuid] = room
	logger.LogEvent("ROOM_CREATE", slog.String("uuid", uuid))
	return room
}

func (rm *RoomManager) startCleanupTicker() {
	ticker := time.NewTicker(1 * time.Minute)
	for range ticker.C {
		rm.cleanup()
	}
}

func (rm *RoomManager) cleanup() {
	rm.Lock.Lock()
	defer rm.Lock.Unlock()

	now := time.Now()
	for uuid, room := range rm.Rooms {
		room.Lock.RLock()
		peerCount := len(room.Peers)
		lastEmpty := room.LastEmptyTime
		room.Lock.RUnlock()

		if peerCount == 0 && now.Sub(lastEmpty) > 2*time.Hour {
			delete(rm.Rooms, uuid)
			logger.LogEvent("ROOM_DESTROY", slog.String("uuid", uuid), slog.String("reason", "expired"))
		}
	}
}

func (r *Room) Broadcast(senderID string, msg any) {
	r.Lock.RLock()
	peers := make([]*Peer, 0, len(r.Peers))
	for id, peer := range r.Peers {
		if id == senderID {
			continue
		}
		peers = append(peers, peer)
	}
	r.Lock.RUnlock()

	for _, peer := range peers {
		peer.WriteJSON(msg)
	}
}

func (p *Peer) WriteJSON(v any) {
	p.WsMutex.Lock()
	defer p.WsMutex.Unlock()
	if p.Conn != nil {
		if err := p.Conn.WriteJSON(v); err != nil {
			slog.Warn("WS write failed", "peer_id", p.ID, "err", err)
		}
	}
}
