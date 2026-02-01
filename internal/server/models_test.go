package server

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/webrtc/v3"
)

func TestRoomManagerGetOrCreateRoom(t *testing.T) {
	rm := &RoomManager{
		Rooms:     make(map[string]*Room),
		BannedIPs: make(map[string]bool),
	}

	roomA := rm.GetOrCreateRoom("room-a")
	if roomA == nil {
		t.Fatal("expected room, got nil")
	}
	roomB := rm.GetOrCreateRoom("room-a")
	if roomA != roomB {
		t.Fatal("expected same room instance for same UUID")
	}
	if roomA.CreatedAt.IsZero() || roomA.LastEmptyTime.IsZero() {
		t.Fatal("expected room timestamps to be initialized")
	}
	if roomA.Forwarders == nil || roomA.Peers == nil {
		t.Fatal("expected room maps to be initialized")
	}
}

func TestRoomManagerCleanupRemovesExpiredEmptyRoom(t *testing.T) {
	rm := &RoomManager{
		Rooms:     make(map[string]*Room),
		BannedIPs: make(map[string]bool),
	}

	rm.Rooms["expired"] = &Room{
		UUID:          "expired",
		Peers:         make(map[string]*Peer),
		Forwarders:    make(map[string]*TrackForwarder),
		CreatedAt:     time.Now().Add(-3 * time.Hour),
		LastEmptyTime: time.Now().Add(-3 * time.Hour),
	}
	rm.Rooms["active"] = &Room{
		UUID:          "active",
		Peers:         map[string]*Peer{"peer": {ID: "peer"}},
		Forwarders:    make(map[string]*TrackForwarder),
		CreatedAt:     time.Now().Add(-1 * time.Hour),
		LastEmptyTime: time.Now().Add(-3 * time.Hour),
	}
	rm.Rooms["recent-empty"] = &Room{
		UUID:          "recent-empty",
		Peers:         make(map[string]*Peer),
		Forwarders:    make(map[string]*TrackForwarder),
		CreatedAt:     time.Now().Add(-30 * time.Minute),
		LastEmptyTime: time.Now().Add(-30 * time.Minute),
	}

	rm.cleanup()

	if _, exists := rm.Rooms["expired"]; exists {
		t.Fatal("expected expired room to be removed")
	}
	if _, exists := rm.Rooms["active"]; !exists {
		t.Fatal("expected active room to remain")
	}
	if _, exists := rm.Rooms["recent-empty"]; !exists {
		t.Fatal("expected recent empty room to remain")
	}
}

func TestBanIPPersistence(t *testing.T) {
	tmp := t.TempDir()
	banPath := filepath.Join(tmp, "banned.json")

	rm := &RoomManager{
		Rooms:       make(map[string]*Room),
		BannedIPs:   make(map[string]bool),
		BanListPath: banPath,
	}

	rm.BanIP("203.0.113.9")

	data, err := os.ReadFile(banPath)
	if err != nil {
		t.Fatalf("failed to read ban list: %v", err)
	}

	var stored map[string]bool
	if err := json.Unmarshal(data, &stored); err != nil {
		t.Fatalf("failed to parse ban list: %v", err)
	}
	if !stored["203.0.113.9"] {
		t.Fatal("expected banned IP to be persisted")
	}
}

func TestLoadBanList(t *testing.T) {
	tmp := t.TempDir()
	banPath := filepath.Join(tmp, "banned.json")
	if err := os.WriteFile(banPath, []byte(`{"198.51.100.7":true}`), 0644); err != nil {
		t.Fatalf("failed to write ban list: %v", err)
	}

	rm := &RoomManager{
		Rooms:       make(map[string]*Room),
		BannedIPs:   make(map[string]bool),
		BanListPath: banPath,
	}

	rm.loadBanList()

	if !rm.IsBanned("198.51.100.7") {
		t.Fatal("expected IP to be loaded from ban list")
	}
}

func TestTrackForwarderRecordWriteErrorRemovesSubscriberOnClosed(t *testing.T) {
	localTrack, err := webrtc.NewTrackLocalStaticRTP(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus},
		"track-id",
		"stream-id",
	)
	if err != nil {
		t.Fatalf("failed to create local track: %v", err)
	}

	forwarder := NewTrackForwarder("sender", nil)
	forwarder.Subscribe("receiver", localTrack)
	if forwarder.SubscriberCount() != 1 {
		t.Fatal("expected subscriber to be added")
	}

	forwarder.recordWriteError("receiver", webrtc.ErrConnectionClosed)
	if forwarder.SubscriberCount() != 0 {
		t.Fatal("expected subscriber to be removed after connection closed")
	}
}

func TestTrackForwarderRecordWriteErrorKeepsSubscriberOnGenericError(t *testing.T) {
	localTrack, err := webrtc.NewTrackLocalStaticRTP(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus},
		"track-id",
		"stream-id",
	)
	if err != nil {
		t.Fatalf("failed to create local track: %v", err)
	}

	forwarder := NewTrackForwarder("sender", nil)
	forwarder.Subscribe("receiver", localTrack)

	forwarder.recordWriteError("receiver", errors.New("write failed"))
	if forwarder.SubscriberCount() != 1 {
		t.Fatal("expected subscriber to remain on generic error")
	}
}

func TestTrackForwarderStopWithErrorCallsOnStopOnce(t *testing.T) {
	var calls int32
	forwarder := NewTrackForwarder("sender", nil)
	forwarder.onStop = func(err error) {
		atomic.AddInt32(&calls, 1)
	}

	forwarder.stopWithError(errors.New("boom"))
	forwarder.stopWithError(errors.New("boom again"))

	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("expected onStop to be called once, got %d", got)
	}
}
