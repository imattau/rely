package p2p

import (
	"encoding/json"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestBroadcastAndReceive(t *testing.T) {
	var received int32

	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()

	pm := NewPeerManager(nil)
	p := &peer{
		url:  "mem://peer",
		conn: websocket.NewConn(clientConn, true),
		send: make(chan []byte, 256),
		done: make(chan struct{}),
	}

	pm.mu.Lock()
	pm.peers[p.url] = p
	pm.mu.Unlock()

	go pm.writeLoop(p, websocket.NewConn(clientConn, true), p.done)
	defer close(p.done)

	pm.Broadcast("ping", map[string]string{"hello": "world"})

	serverSide := websocket.NewConn(serverConn, false)
	_, msg, err := serverSide.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}

	var env Envelope
	if err := json.Unmarshal(msg, &env); err != nil {
		t.Fatal(err)
	}
	if env.Type == "ping" {
		atomic.AddInt32(&received, 1)
	}

	time.Sleep(10 * time.Millisecond)
	if atomic.LoadInt32(&received) != 1 {
		t.Errorf("expected 1 message received, got %d", atomic.LoadInt32(&received))
	}
}

func TestTrustWeightDefault(t *testing.T) {
	pm := NewPeerManager(nil)
	if got := pm.TrustWeight("wss://unknown.example.com"); got != 1.0 {
		t.Errorf("expected default trust weight 1.0, got %v", got)
	}
}

func TestSetAndGetTrustWeight(t *testing.T) {
	pm := NewPeerManager(nil)
	pm.SetTrustWeight("wss://peer.example.com", 2.5)
	if got := pm.TrustWeight("wss://peer.example.com"); got != 2.5 {
		t.Errorf("expected trust weight 2.5, got %v", got)
	}
}

func TestBroadcastToTrustedOnly(t *testing.T) {
	pm := NewPeerManager(nil)

	trusted := &peer{
		url:  "wss://trusted.example.com",
		send: make(chan []byte, 1),
		done: make(chan struct{}),
	}
	untrusted := &peer{
		url:  "wss://untrusted.example.com",
		send: make(chan []byte, 1),
		done: make(chan struct{}),
	}

	pm.mu.Lock()
	pm.peers[trusted.url] = trusted
	pm.peers[untrusted.url] = untrusted
	pm.mu.Unlock()

	pm.SetTrustWeight(trusted.url, 2.0)

	pm.BroadcastToTrusted("test", map[string]string{"hello": "world"})

	if got := len(trusted.send); got != 1 {
		t.Fatalf("trusted peer send buffer = %d, want 1", got)
	}
	if got := len(untrusted.send); got != 0 {
		t.Fatalf("untrusted peer send buffer = %d, want 0", got)
	}
}

func TestDisconnect(t *testing.T) {
	pm := NewPeerManager(nil)
	p := &peer{
		url:  "wss://peer.example.com",
		send: make(chan []byte, 1),
		done: make(chan struct{}),
	}

	pm.mu.Lock()
	pm.peers[p.url] = p
	pm.mu.Unlock()

	if len(pm.Peers()) != 1 {
		t.Fatalf("expected 1 peer before disconnect, got %d", len(pm.Peers()))
	}

	pm.Disconnect(p.url)

	if len(pm.Peers()) != 0 {
		t.Fatalf("expected 0 peers after disconnect, got %d", len(pm.Peers()))
	}
	select {
	case <-p.done:
	default:
		t.Fatal("expected peer done channel to be closed")
	}
}

func TestReconnectLoop(t *testing.T) {
	var accepts int32
	firstDone := make(chan struct{}, 1)
	readyForBroadcast := make(chan struct{}, 1)
	received := make(chan Envelope, 1)

	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}

		if atomic.AddInt32(&accepts, 1) == 1 {
			_ = conn.WriteMessage(websocket.TextMessage, []byte(`["ping",{"hello":"first"}]`))
			time.Sleep(75 * time.Millisecond)
			_ = conn.Close()
			firstDone <- struct{}{}
			return
		}

		readyForBroadcast <- struct{}{}
		_, msg, err := conn.ReadMessage()
		if err != nil {
			t.Errorf("second conn read: %v", err)
			return
		}

		var env Envelope
		if err := json.Unmarshal(msg, &env); err != nil {
			t.Errorf("unmarshal broadcast: %v", err)
			return
		}
		received <- env
		_ = conn.Close()
	})}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	go func() {
		_ = srv.Serve(ln)
	}()
	defer srv.Close()

	pm := NewPeerManager(nil)
	url := "ws://" + ln.Addr().String()
	if err := pm.Connect(url); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer pm.Disconnect(url)

	select {
	case <-firstDone:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for initial disconnect")
	}

	select {
	case <-readyForBroadcast:
	case <-time.After(4 * time.Second):
		t.Fatal("timed out waiting for reconnect")
	}

	pm.Broadcast("test", map[string]string{"hello": "world"})

	select {
	case env := <-received:
		if env.Type != "test" {
			t.Fatalf("unexpected envelope type %q", env.Type)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("timed out waiting for broadcast on reconnected peer")
	}
}
