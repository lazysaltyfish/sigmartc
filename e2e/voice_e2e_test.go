//go:build e2e
// +build e2e

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v3"

	"sigmartc/internal/server"
)

const (
	testClientCount     = 3
	minPacketsPerSender = 3
	sendInterval        = 20 * time.Millisecond
	sendDuration        = 10 * time.Second
	testTimeout         = 45 * time.Second
)

type peerInfo struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type wsMessage struct {
	Type      string          `json:"type"`
	SDP       string          `json:"sdp,omitempty"`
	Candidate json.RawMessage `json:"candidate,omitempty"`
	SelfID    string          `json:"self_id,omitempty"`
	Peers     []peerInfo      `json:"peers,omitempty"`
	Peer      peerInfo        `json:"peer,omitempty"`
	PeerID    string          `json:"peer_id,omitempty"`
	Message   string          `json:"message,omitempty"`
}

type testClient struct {
	name       string
	ws         *websocket.Conn
	wsMu       sync.Mutex
	pc         *webrtc.PeerConnection
	localTrack *webrtc.TrackLocalStaticRTP
	selfID     string

	roomStateOnce sync.Once
	roomStateCh   chan struct{}

	connectedOnce sync.Once
	connectedCh   chan struct{}

	signalingMu sync.Mutex

	recvMu     sync.Mutex
	recvCounts map[string]int

	pendingCandidates []webrtc.ICECandidateInit

	errMu sync.Mutex
	err   error

	closeOnce sync.Once
	closedCh  chan struct{}
}

func TestVoiceE2E_MultiClient(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping E2E test in short mode")
	}
	if os.Getenv("E2E") == "" {
		t.Skip("set E2E=1 to run end-to-end tests")
	}

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	baseURL, closeServer := startTestServer(t)
	defer closeServer()

	roomID := fmt.Sprintf("room-%d", time.Now().UnixNano())
	api, err := newWebRTCAPI()
	if err != nil {
		t.Fatalf("failed to init WebRTC API: %v", err)
	}

	clients := make([]*testClient, 0, testClientCount)
	for i := 0; i < testClientCount; i++ {
		clientName := fmt.Sprintf("client-%d", i+1)
		c, err := newTestClient(ctx, api, baseURL, roomID, clientName)
		if err != nil {
			t.Fatalf("client setup failed for %s: %v", clientName, err)
		}
		t.Cleanup(c.Close)
		clients = append(clients, c)
	}

	for _, c := range clients {
		if err := c.WaitForConnected(ctx); err != nil {
			t.Fatalf("client %s connection failed: %v", c.name, err)
		}
	}

	for _, c := range clients {
		if c.selfID == "" {
			t.Fatalf("client %s missing self ID", c.name)
		}
	}

	for _, sender := range clients {
		sendCtx, sendCancel := context.WithCancel(ctx)
		sender.StartSending(sendCtx, sendDuration)
		if err := waitForReceivesFromSender(ctx, clients, sender.selfID, minPacketsPerSender); err != nil {
			sendCancel()
			t.Fatalf("media exchange failed for %s: %v", sender.name, err)
		}
		sendCancel()
	}
}

func startTestServer(t *testing.T) (string, func()) {
	t.Helper()

	rm := server.NewRoomManager("test-admin", filepath.Join(t.TempDir(), "banned_ips.json"))
	api, err := newWebRTCAPI()
	if err != nil {
		t.Fatalf("failed to init WebRTC API: %v", err)
	}
	h := server.NewHandler(rm, api)

	mux := http.NewServeMux()
	mux.HandleFunc("/ws", h.HandleWS)

	srv := &http.Server{Handler: mux}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}

	go func() {
		_ = srv.Serve(listener)
	}()

	baseURL := "http://" + listener.Addr().String()
	return baseURL, func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}
}

func newWebRTCAPI() (*webrtc.API, error) {
	m := &webrtc.MediaEngine{}
	if err := m.RegisterDefaultCodecs(); err != nil {
		return nil, err
	}
	se := webrtc.SettingEngine{}
	se.SetNetworkTypes([]webrtc.NetworkType{webrtc.NetworkTypeUDP4})
	api := webrtc.NewAPI(webrtc.WithMediaEngine(m), webrtc.WithSettingEngine(se))
	return api, nil
}

func newTestClient(ctx context.Context, api *webrtc.API, baseURL, roomID, name string) (*testClient, error) {
	wsURL, err := buildWSURL(baseURL, roomID, name)
	if err != nil {
		return nil, err
	}

	ws, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		return nil, err
	}

	pc, err := api.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		_ = ws.Close()
		return nil, err
	}

	localTrack, err := webrtc.NewTrackLocalStaticRTP(
		webrtc.RTPCodecCapability{
			MimeType:  webrtc.MimeTypeOpus,
			ClockRate: 48000,
			Channels:  2,
		},
		"audio",
		"local-"+name,
	)
	if err != nil {
		_ = pc.Close()
		_ = ws.Close()
		return nil, err
	}

	sender, err := pc.AddTrack(localTrack)
	if err != nil {
		_ = pc.Close()
		_ = ws.Close()
		return nil, err
	}

	client := &testClient{
		name:        name,
		ws:          ws,
		pc:          pc,
		localTrack:  localTrack,
		roomStateCh: make(chan struct{}),
		connectedCh: make(chan struct{}),
		recvCounts:  make(map[string]int),
		closedCh:    make(chan struct{}),
	}

	go drainRTCP(sender)

	pc.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c == nil {
			return
		}
		if err := client.sendJSON(map[string]any{
			"type":      "candidate",
			"candidate": c.ToJSON(),
		}); err != nil {
			client.recordErr(fmt.Errorf("send candidate: %w", err))
		}
	})

	pc.OnTrack(func(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		client.handleTrack(track)
	})

	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		if state == webrtc.PeerConnectionStateConnected {
			client.connectedOnce.Do(func() {
				close(client.connectedCh)
			})
		}
	})

	go client.readLoop()

	if err := client.waitForRoomState(ctx); err != nil {
		client.Close()
		return nil, err
	}

	if err := client.sendOffer(); err != nil {
		client.Close()
		return nil, err
	}

	return client, nil
}

func buildWSURL(baseURL, roomID, name string) (string, error) {
	u, err := url.Parse(baseURL)
	if err != nil {
		return "", err
	}
	scheme := "ws"
	if u.Scheme == "https" {
		scheme = "wss"
	}
	wsURL := url.URL{
		Scheme:   scheme,
		Host:     u.Host,
		Path:     "/ws",
		RawQuery: "room=" + url.QueryEscape(roomID) + "&name=" + url.QueryEscape(name),
	}
	return wsURL.String(), nil
}

func (c *testClient) WaitForConnected(ctx context.Context) error {
	if err := c.Err(); err != nil {
		return err
	}
	select {
	case <-c.connectedCh:
		return c.Err()
	case <-ctx.Done():
		return fmt.Errorf("timeout waiting for connection: %w", ctx.Err())
	}
}

func (c *testClient) StartSending(ctx context.Context, duration time.Duration) {
	payload := []byte("e2e-audio")
	go func() {
		ticker := time.NewTicker(sendInterval)
		defer ticker.Stop()

		deadline := time.NewTimer(duration)
		defer deadline.Stop()

		var seq uint16
		var ts uint32
		for {
			select {
			case <-c.closedCh:
				return
			case <-ctx.Done():
				return
			case <-deadline.C:
				return
			case <-ticker.C:
				packet := &rtp.Packet{
					Header: rtp.Header{
						Version:        2,
						SequenceNumber: seq,
						Timestamp:      ts,
					},
					Payload: payload,
				}
				if err := c.localTrack.WriteRTP(packet); err != nil {
					c.recordErr(fmt.Errorf("write rtp: %w", err))
					return
				}
				seq++
				ts += 960
			}
		}
	}()
}

func (c *testClient) handleTrack(track *webrtc.TrackRemote) {
	senderID := track.StreamID()
	if senderID == "" {
		senderID = track.ID()
	}

	c.recvMu.Lock()
	if _, exists := c.recvCounts[senderID]; !exists {
		c.recvCounts[senderID] = 0
	}
	c.recvMu.Unlock()

	go func() {
		for {
			if _, _, err := track.ReadRTP(); err != nil {
				return
			}
			c.recvMu.Lock()
			c.recvCounts[senderID]++
			c.recvMu.Unlock()
		}
	}()
}

func (c *testClient) readLoop() {
	for {
		_, payload, err := c.ws.ReadMessage()
		if err != nil {
			select {
			case <-c.closedCh:
				return
			default:
				c.recordErr(fmt.Errorf("ws read: %w", err))
				return
			}
		}

		var msg wsMessage
		if err := json.Unmarshal(payload, &msg); err != nil {
			c.recordErr(fmt.Errorf("decode ws: %w", err))
			continue
		}

		switch msg.Type {
		case "room_state":
			c.selfID = msg.SelfID
			c.roomStateOnce.Do(func() {
				close(c.roomStateCh)
			})
		case "offer":
			if err := c.handleOffer(msg.SDP); err != nil {
				c.recordErr(fmt.Errorf("handle offer: %w", err))
			}
		case "answer":
			if err := c.handleAnswer(msg.SDP); err != nil {
				c.recordErr(fmt.Errorf("handle answer: %w", err))
			}
		case "candidate":
			if err := c.handleCandidate(msg.Candidate); err != nil {
				c.recordErr(fmt.Errorf("handle candidate: %w", err))
			}
		case "error":
			if msg.Message != "" {
				c.recordErr(fmt.Errorf("server error: %s", msg.Message))
			} else {
				c.recordErr(fmt.Errorf("server error message received"))
			}
		}
	}
}

func (c *testClient) sendOffer() error {
	c.signalingMu.Lock()
	defer c.signalingMu.Unlock()

	offer, err := c.pc.CreateOffer(nil)
	if err != nil {
		return err
	}
	if err := c.pc.SetLocalDescription(offer); err != nil {
		return err
	}
	return c.sendJSON(map[string]any{
		"type": "offer",
		"sdp":  offer.SDP,
	})
}

func (c *testClient) handleOffer(sdp string) error {
	c.signalingMu.Lock()
	defer c.signalingMu.Unlock()

	if err := c.pc.SetRemoteDescription(webrtc.SessionDescription{
		Type: webrtc.SDPTypeOffer,
		SDP:  sdp,
	}); err != nil {
		return err
	}
	answer, err := c.pc.CreateAnswer(nil)
	if err != nil {
		return err
	}
	if err := c.pc.SetLocalDescription(answer); err != nil {
		return err
	}
	if err := c.flushPendingCandidatesLocked(); err != nil {
		return err
	}
	return c.sendJSON(map[string]any{
		"type": "answer",
		"sdp":  answer.SDP,
	})
}

func (c *testClient) handleAnswer(sdp string) error {
	c.signalingMu.Lock()
	defer c.signalingMu.Unlock()

	if err := c.pc.SetRemoteDescription(webrtc.SessionDescription{
		Type: webrtc.SDPTypeAnswer,
		SDP:  sdp,
	}); err != nil {
		return err
	}
	return c.flushPendingCandidatesLocked()
}

func (c *testClient) handleCandidate(raw json.RawMessage) error {
	if len(raw) == 0 {
		return nil
	}
	var candidate webrtc.ICECandidateInit
	if err := json.Unmarshal(raw, &candidate); err != nil {
		return err
	}
	c.signalingMu.Lock()
	defer c.signalingMu.Unlock()

	if c.pc.RemoteDescription() == nil {
		c.pendingCandidates = append(c.pendingCandidates, candidate)
		return nil
	}
	return c.pc.AddICECandidate(candidate)
}

func (c *testClient) flushPendingCandidatesLocked() error {
	for _, candidate := range c.pendingCandidates {
		if err := c.pc.AddICECandidate(candidate); err != nil {
			return err
		}
	}
	c.pendingCandidates = nil
	return nil
}

func (c *testClient) waitForRoomState(ctx context.Context) error {
	select {
	case <-c.roomStateCh:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("timeout waiting for room_state: %w", ctx.Err())
	}
}

func (c *testClient) sendJSON(v any) error {
	c.wsMu.Lock()
	defer c.wsMu.Unlock()
	return c.ws.WriteJSON(v)
}

func (c *testClient) recordErr(err error) {
	if err == nil {
		return
	}
	c.errMu.Lock()
	defer c.errMu.Unlock()
	if c.err == nil {
		c.err = err
	}
}

func (c *testClient) Err() error {
	c.errMu.Lock()
	defer c.errMu.Unlock()
	return c.err
}

func (c *testClient) Close() {
	c.closeOnce.Do(func() {
		close(c.closedCh)
		if c.pc != nil {
			_ = c.pc.Close()
		}
		if c.ws != nil {
			_ = c.ws.Close()
		}
	})
}

func (c *testClient) receivedFrom(senderID string, minPackets int) bool {
	c.recvMu.Lock()
	defer c.recvMu.Unlock()
	return c.recvCounts[senderID] >= minPackets
}

func waitForReceivesFromSender(ctx context.Context, clients []*testClient, senderID string, minPackets int) error {
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()

	for {
		for _, c := range clients {
			if err := c.Err(); err != nil {
				return err
			}
		}

		allReady := true
		for _, c := range clients {
			if c.selfID == senderID {
				continue
			}
			if !c.receivedFrom(senderID, minPackets) {
				allReady = false
				break
			}
		}
		if allReady {
			return nil
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("timeout waiting for media from %s: %w", senderID, ctx.Err())
		case <-ticker.C:
		}
	}
}

func drainRTCP(sender *webrtc.RTPSender) {
	buf := make([]byte, 1500)
	for {
		if _, _, err := sender.Read(buf); err != nil {
			return
		}
	}
}
