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

	Muted    bool
	JoinTime time.Time
}

// Room represents a voice chat session.
type Room struct {
	UUID  string
	Peers map[string]*Peer
	Lock  sync.RWMutex

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
