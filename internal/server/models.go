package server

import (
	"encoding/json"
	"errors"
	"io"
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

	PC *webrtc.PeerConnection

	// OutTracks maps senderID to the local track used to forward that sender's audio to this peer
	OutTracks   map[string]*webrtc.TrackLocalStaticRTP
	OutTracksMu sync.RWMutex

	NegotiationMu         sync.Mutex
	NegotiationPending    bool
	NegotiationInProgress bool
	MakingOffer           bool
	IceRestartPending     bool
	LastIceRestart        time.Time

	PendingCandidatesMu sync.Mutex
	PendingCandidates   []webrtc.ICECandidateInit

	Muted    bool
	JoinTime time.Time

	Done     chan struct{}
	doneOnce sync.Once
}

// TrackForwarder manages fan-out from one sender's TrackRemote to multiple receivers.
// It reads RTP packets once and writes them to all subscribers.
type TrackForwarder struct {
	SenderID    string
	TrackRemote *webrtc.TrackRemote

	mu          sync.RWMutex
	subscribers map[string]*webrtc.TrackLocalStaticRTP // receiverID -> localTrack
	writeErrAt  map[string]time.Time

	done     chan struct{}
	stopOnce sync.Once
	onStop   func(error)
}

// NewTrackForwarder creates a new forwarder for the given sender's track.
func NewTrackForwarder(senderID string, track *webrtc.TrackRemote) *TrackForwarder {
	return &TrackForwarder{
		SenderID:    senderID,
		TrackRemote: track,
		subscribers: make(map[string]*webrtc.TrackLocalStaticRTP),
		writeErrAt:  make(map[string]time.Time),
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
			f.stopWithError(err)
			return
		}

		type subscriberEntry struct {
			id    string
			track *webrtc.TrackLocalStaticRTP
		}
		f.mu.RLock()
		subscribers := make([]subscriberEntry, 0, len(f.subscribers))
		for receiverID, localTrack := range f.subscribers {
			subscribers = append(subscribers, subscriberEntry{id: receiverID, track: localTrack})
		}
		f.mu.RUnlock()

		for _, sub := range subscribers {
			if _, writeErr := sub.track.Write(rtpBuf[:n]); writeErr != nil {
				f.recordWriteError(sub.id, writeErr)
			}
		}
	}
}

// Stop signals the forwarder to stop reading.
func (f *TrackForwarder) Stop() {
	f.stopOnce.Do(func() {
		close(f.done)
	})
}

func (f *TrackForwarder) stopWithError(err error) {
	f.stopOnce.Do(func() {
		close(f.done)
		if err != nil {
			slog.Warn("Forwarder stopped", "sender_id", f.SenderID, "err", err)
		}
		if f.onStop != nil {
			f.onStop(err)
		}
	})
}

func (f *TrackForwarder) recordWriteError(receiverID string, err error) {
	now := time.Now()
	shouldLog := false
	removeSubscriber := errors.Is(err, io.ErrClosedPipe) || errors.Is(err, webrtc.ErrConnectionClosed)

	f.mu.Lock()
	last := f.writeErrAt[receiverID]
	if now.Sub(last) >= 5*time.Second {
		f.writeErrAt[receiverID] = now
		shouldLog = true
	}
	if removeSubscriber {
		delete(f.subscribers, receiverID)
		delete(f.writeErrAt, receiverID)
	}
	f.mu.Unlock()

	if shouldLog {
		slog.Warn("Failed to write RTP to subscriber", "sender_id", f.SenderID, "receiver_id", receiverID, "err", err)
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

func (p *Peer) SignalDone() {
	p.doneOnce.Do(func() {
		if p.Done != nil {
			close(p.Done)
		}
	})
}
