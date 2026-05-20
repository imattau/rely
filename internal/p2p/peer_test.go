package p2p

import (
	"encoding/json"
	"net"
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

	go pm.writeLoop(p)
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
