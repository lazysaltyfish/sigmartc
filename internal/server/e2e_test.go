package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v3"
)

type e2eClient struct {
	t *testing.T

	ws   *websocket.Conn
	wsMu sync.Mutex
	pc   *webrtc.PeerConnection

	localTrack  *webrtc.TrackLocalStaticRTP
	payloadType uint8
	ssrc        uint32

	pendingMu sync.Mutex
	pending   []webrtc.ICECandidateInit

	connectedCh chan struct{}
	connectedMu sync.Once

	streamsMu       sync.Mutex
	streams         map[string]struct{}
	expectedStreams int
	streamsCh       chan struct{}
	streamsOnce     sync.Once
}

func newTestAPI(t *testing.T) *webrtc.API {
	t.Helper()
	m := &webrtc.MediaEngine{}
	if err := m.RegisterDefaultCodecs(); err != nil {
		t.Fatalf("failed to register codecs: %v", err)
	}
	return webrtc.NewAPI(webrtc.WithMediaEngine(m))
}

func newE2EClient(t *testing.T, serverURL, room, name string, api *webrtc.API, withTrack bool) (*e2eClient, error) {
	wsURL, err := buildWSURL(serverURL, room, name)
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

	client := &e2eClient{
		t:           t,
		ws:          ws,
		pc:          pc,
		connectedCh: make(chan struct{}),
		streams:     make(map[string]struct{}),
		streamsCh:   make(chan struct{}),
	}

	pc.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c == nil {
			return
		}
		client.send(map[string]any{
			"type":      "candidate",
			"candidate": c.ToJSON(),
		})
	})

	pc.OnICEConnectionStateChange(func(state webrtc.ICEConnectionState) {
		if state == webrtc.ICEConnectionStateConnected || state == webrtc.ICEConnectionStateCompleted {
			client.connectedMu.Do(func() {
				close(client.connectedCh)
			})
		}
	})

	pc.OnTrack(func(track *webrtc.TrackRemote, receiver *webrtc.RTPReceiver) {
		go func() {
			if _, _, err := track.ReadRTP(); err == nil {
				client.recordStream(track.StreamID())
			}
		}()
	})

	go client.readLoop()

	if withTrack {
		localTrack, err := webrtc.NewTrackLocalStaticRTP(
			webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus},
			fmt.Sprintf("%s-audio", name),
			name,
		)
		if err != nil {
			client.Close()
			return nil, err
		}
		sender, err := pc.AddTrack(localTrack)
		if err != nil {
			client.Close()
			return nil, err
		}
		go func() {
			rtcpBuf := make([]byte, 1500)
			for {
				if _, _, rtcpErr := sender.Read(rtcpBuf); rtcpErr != nil {
					return
				}
			}
		}()
		client.localTrack = localTrack
		client.payloadType = 111
		params := sender.GetParameters()
		if len(params.Codecs) > 0 {
			client.payloadType = uint8(params.Codecs[0].PayloadType)
		}
		if len(params.Encodings) > 0 && params.Encodings[0].SSRC != 0 {
			client.ssrc = uint32(params.Encodings[0].SSRC)
		}
	}

	if err := client.sendOffer(); err != nil {
		client.Close()
		return nil, err
	}

	return client, nil
}

func (c *e2eClient) sendOffer() error {
	offer, err := c.pc.CreateOffer(nil)
	if err != nil {
		return err
	}
	if err := c.pc.SetLocalDescription(offer); err != nil {
		return err
	}
	return c.send(map[string]any{
		"type": "offer",
		"sdp":  offer.SDP,
	})
}

func (c *e2eClient) send(payload map[string]any) error {
	c.wsMu.Lock()
	defer c.wsMu.Unlock()
	return c.ws.WriteJSON(payload)
}

func (c *e2eClient) readLoop() {
	for {
		_, message, err := c.ws.ReadMessage()
		if err != nil {
			return
		}
		var msg map[string]any
		if err := json.Unmarshal(message, &msg); err != nil {
			continue
		}
		msgType, _ := msg["type"].(string)
		switch msgType {
		case "offer":
			sdp, _ := msg["sdp"].(string)
			if err := c.pc.SetRemoteDescription(webrtc.SessionDescription{
				Type: webrtc.SDPTypeOffer,
				SDP:  sdp,
			}); err != nil {
				c.t.Logf("SetRemoteDescription offer failed: %v", err)
				continue
			}
			c.flushPending()
			answer, err := c.pc.CreateAnswer(nil)
			if err != nil {
				c.t.Logf("CreateAnswer failed: %v", err)
				continue
			}
			if err := c.pc.SetLocalDescription(answer); err != nil {
				c.t.Logf("SetLocalDescription answer failed: %v", err)
				continue
			}
			_ = c.send(map[string]any{
				"type": "answer",
				"sdp":  answer.SDP,
			})
		case "answer":
			sdp, _ := msg["sdp"].(string)
			if err := c.pc.SetRemoteDescription(webrtc.SessionDescription{
				Type: webrtc.SDPTypeAnswer,
				SDP:  sdp,
			}); err != nil {
				c.t.Logf("SetRemoteDescription answer failed: %v", err)
				continue
			}
			c.flushPending()
		case "candidate":
			candidateData, _ := msg["candidate"].(map[string]any)
			candidateJSON, _ := json.Marshal(candidateData)
			var candidate webrtc.ICECandidateInit
			if err := json.Unmarshal(candidateJSON, &candidate); err != nil {
				continue
			}
			if c.pc.RemoteDescription() == nil {
				c.pendingMu.Lock()
				c.pending = append(c.pending, candidate)
				c.pendingMu.Unlock()
				continue
			}
			if err := c.pc.AddICECandidate(candidate); err != nil {
				c.t.Logf("AddICECandidate failed: %v", err)
			}
		}
	}
}

func (c *e2eClient) flushPending() {
	c.pendingMu.Lock()
	pending := c.pending
	c.pending = nil
	c.pendingMu.Unlock()
	for _, candidate := range pending {
		if err := c.pc.AddICECandidate(candidate); err != nil {
			c.t.Logf("AddICECandidate failed: %v", err)
		}
	}
}

func (c *e2eClient) waitConnected(ctx context.Context) error {
	select {
	case <-c.connectedCh:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *e2eClient) waitForRTP(ctx context.Context) error {
	return c.waitForStreams(ctx, 1)
}

func (c *e2eClient) waitForStreams(ctx context.Context, expected int) error {
	c.setExpectedStreams(expected)
	select {
	case <-c.streamsCh:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *e2eClient) setExpectedStreams(expected int) {
	c.streamsMu.Lock()
	c.expectedStreams = expected
	ready := expected > 0 && len(c.streams) >= expected
	c.streamsMu.Unlock()
	if ready {
		c.streamsOnce.Do(func() {
			close(c.streamsCh)
		})
	}
}

func (c *e2eClient) recordStream(streamID string) {
	c.streamsMu.Lock()
	c.streams[streamID] = struct{}{}
	ready := c.expectedStreams > 0 && len(c.streams) >= c.expectedStreams
	c.streamsMu.Unlock()
	if ready {
		c.streamsOnce.Do(func() {
			close(c.streamsCh)
		})
	}
}

func (c *e2eClient) sendRTPPackets(ctx context.Context, count int) error {
	if c.localTrack == nil {
		return fmt.Errorf("no local track configured")
	}
	seq := uint16(1)
	ts := uint32(0)
	ssrc := c.ssrc
	if ssrc == 0 {
		ssrc = uint32(time.Now().UnixNano())
	}
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()

	for i := 0; i < count; i++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
		packet := &rtp.Packet{
			Header: rtp.Header{
				Version:        2,
				PayloadType:    c.payloadType,
				SequenceNumber: seq,
				Timestamp:      ts,
				SSRC:           ssrc,
			},
			Payload: []byte{0x00, 0x01, 0x02, 0x03},
		}
		if err := c.localTrack.WriteRTP(packet); err != nil {
			return err
		}
		seq++
		ts += 960
	}
	return nil
}

func (c *e2eClient) Close() {
	if c.ws != nil {
		_ = c.ws.Close()
	}
	if c.pc != nil {
		_ = c.pc.Close()
	}
}

func buildWSURL(serverURL, room, name string) (string, error) {
	u, err := url.Parse(serverURL)
	if err != nil {
		return "", err
	}
	u.Scheme = "ws"
	u.Path = "/ws"
	query := u.Query()
	query.Set("room", room)
	query.Set("name", name)
	u.RawQuery = query.Encode()
	return u.String(), nil
}

func TestE2EMultiUserOnline(t *testing.T) {
	api := newTestAPI(t)
	rm := NewRoomManager("test-key", filepath.Join(t.TempDir(), "banned.json"))
	handler := NewHandler(rm, api)
	handler.ICEConfig = &webrtc.Configuration{}

	mux := http.NewServeMux()
	mux.HandleFunc("/ws", handler.HandleWS)

	// Force IPv4 to avoid environments where IPv6 loopback is restricted.
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	server := &httptest.Server{
		Listener: ln,
		Config:   &http.Server{Handler: mux},
	}
	server.Start()
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	publisher, err := newE2EClient(t, server.URL, "room-e2e", "publisher", api, true)
	if err != nil {
		t.Fatalf("failed to create publisher: %v", err)
	}
	defer publisher.Close()

	receiverA, err := newE2EClient(t, server.URL, "room-e2e", "receiver-a", api, false)
	if err != nil {
		t.Fatalf("failed to create receiver A: %v", err)
	}
	defer receiverA.Close()

	receiverB, err := newE2EClient(t, server.URL, "room-e2e", "receiver-b", api, false)
	if err != nil {
		t.Fatalf("failed to create receiver B: %v", err)
	}
	defer receiverB.Close()

	for _, client := range []*e2eClient{publisher, receiverA, receiverB} {
		if err := client.waitConnected(ctx); err != nil {
			t.Fatalf("client did not connect: %v", err)
		}
	}

	sendCtx, sendCancel := context.WithTimeout(ctx, 6*time.Second)
	defer sendCancel()
	go func() {
		_ = publisher.sendRTPPackets(sendCtx, 60)
	}()

	if err := receiverA.waitForRTP(ctx); err != nil {
		t.Fatalf("receiver A did not receive RTP: %v", err)
	}
	if err := receiverB.waitForRTP(ctx); err != nil {
		t.Fatalf("receiver B did not receive RTP: %v", err)
	}
}

func TestE2EMultiUserMesh(t *testing.T) {
	api := newTestAPI(t)
	rm := NewRoomManager("test-key", filepath.Join(t.TempDir(), "banned.json"))
	handler := NewHandler(rm, api)
	handler.ICEConfig = &webrtc.Configuration{}

	mux := http.NewServeMux()
	mux.HandleFunc("/ws", handler.HandleWS)

	// Force IPv4 to avoid environments where IPv6 loopback is restricted.
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	server := &httptest.Server{
		Listener: ln,
		Config:   &http.Server{Handler: mux},
	}
	server.Start()
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	names := []string{"alice", "bob", "carol"}
	clients := make([]*e2eClient, 0, len(names))
	for _, name := range names {
		client, err := newE2EClient(t, server.URL, "room-mesh", name, api, true)
		if err != nil {
			t.Fatalf("failed to create client %s: %v", name, err)
		}
		clients = append(clients, client)
	}
	for _, client := range clients {
		defer client.Close()
	}

	for _, client := range clients {
		if err := client.waitConnected(ctx); err != nil {
			t.Fatalf("client did not connect: %v", err)
		}
	}

	sendCtx, sendCancel := context.WithTimeout(ctx, 8*time.Second)
	defer sendCancel()
	for _, client := range clients {
		go func(c *e2eClient) {
			_ = c.sendRTPPackets(sendCtx, 80)
		}(client)
	}

	expected := len(clients) - 1
	for _, client := range clients {
		if err := client.waitForStreams(ctx, expected); err != nil {
			t.Fatalf("client did not receive %d streams: %v", expected, err)
		}
	}
}
