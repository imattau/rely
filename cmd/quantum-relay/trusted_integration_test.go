package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pippellia-btc/rely/v2/internal/consensus"
	"github.com/pippellia-btc/rely/v2/internal/p2p"
)

func TestTrustedPeerIntegration(t *testing.T) {
	sender := newLivePeerServer(t)
	defer sender.Close()
	victim := newLivePeerServer(t)
	defer victim.Close()

	done := make(chan struct{})
	diff := consensus.NewDiffuser(nil, nil)
	diff.SetReputation("alice", 0.0)
	go diff.Run(10*time.Millisecond, done)
	defer close(done)

	var peerMgr *p2p.PeerManager
	peerMgr = p2p.NewPeerManager(func(peerURL, msgType string, payload json.RawMessage) {
		handlePeerMessage(
			&Config{Trust: TrustConfig{Enabled: true}},
			peerMgr,
			diff,
			nil,
			peerURL,
			msgType,
			payload,
		)
	})

	peerMgr.SetTrustWeight(sender.URL(), 2.0)
	if err := peerMgr.Connect(sender.URL()); err != nil {
		t.Fatalf("connect trusted sender: %v", err)
	}
	if err := peerMgr.Connect(victim.URL()); err != nil {
		t.Fatalf("connect victim: %v", err)
	}

	sender.WaitConnected(t)
	victim.WaitConnected(t)

	sendEnvelope := func(server *livePeerServer, msgType string, payload any) {
		t.Helper()
		server.Send(t, msgType, payload)
	}

	sendEnvelope(sender, "consensus", consensus.State{
		Round: 10,
		Rep:   map[string]float64{"alice": 1.0},
	})

	waitForCondition(t, 2*time.Second, func() bool {
		return diff.GetReputation("alice") > 0.6
	}, func() string {
		return fmt.Sprintf("reputation=%v", diff.GetReputation("alice"))
	})

	sendEnvelope(sender, "block_peer", victim.URL())

	waitForCondition(t, 2*time.Second, func() bool {
		return victim.Closed()
	}, func() string {
		return "victim connection still open"
	})

	if got := len(peerMgr.Peers()); got != 1 {
		t.Fatalf("expected 1 peer after block, got %d", got)
	}
	if peerMgr.Peers()[0] != sender.URL() {
		t.Fatalf("expected trusted sender to remain connected, peers=%v", peerMgr.Peers())
	}
}

type livePeerServer struct {
	t        *testing.T
	ln       net.Listener
	server   *http.Server
	url      string
	connCh   chan *websocket.Conn
	last     *websocket.Conn
	closed   chan struct{}
	upgrader websocket.Upgrader
}

func newLivePeerServer(t *testing.T) *livePeerServer {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	s := &livePeerServer{
		t:      t,
		ln:     ln,
		url:    "ws://" + ln.Addr().String(),
		connCh: make(chan *websocket.Conn, 1),
		closed: make(chan struct{}),
		upgrader: websocket.Upgrader{
			CheckOrigin: func(*http.Request) bool { return true },
		},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		conn, err := s.upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		select {
		case s.connCh <- conn:
		default:
		}
		go func() {
			defer close(s.closed)
			for {
				if _, _, err := conn.ReadMessage(); err != nil {
					_ = conn.Close()
					return
				}
			}
		}()
	})

	s.server = &http.Server{Handler: mux}
	go func() {
		_ = s.server.Serve(ln)
	}()
	return s
}

func (s *livePeerServer) URL() string { return s.url }

func (s *livePeerServer) WaitConnected(t *testing.T) *websocket.Conn {
	t.Helper()

	select {
	case conn := <-s.connCh:
		s.last = conn
		return conn
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for peer connection")
		return nil
	}
}

func (s *livePeerServer) Send(t *testing.T, msgType string, payload any) {
	t.Helper()

	if s.last == nil {
		s.last = s.WaitConnected(t)
	}

	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	msg, err := json.Marshal(p2p.Envelope{Type: msgType, Payload: raw})
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	if err := s.last.WriteMessage(websocket.TextMessage, msg); err != nil {
		t.Fatalf("write message: %v", err)
	}
}

func (s *livePeerServer) Closed() bool {
	select {
	case <-s.closed:
		return true
	default:
		return false
	}
}

func (s *livePeerServer) Close() {
	_ = s.server.Close()
	_ = s.ln.Close()
	if s.last != nil {
		_ = s.last.Close()
	}
}

func waitForCondition(t *testing.T, timeout time.Duration, cond func() bool, state func() string) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for condition: %s", state())
}
