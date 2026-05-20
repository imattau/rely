package p2p

import (
	"encoding/json"
	"log"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

type Envelope struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

type peer struct {
	url  string
	conn *websocket.Conn
	send chan []byte
	done chan struct{}
}

type PeerManager struct {
	mu        sync.RWMutex
	peers     map[string]*peer
	onMessage func(peerURL, msgType string, payload json.RawMessage)
}

func NewPeerManager(onMessage func(peerURL, msgType string, payload json.RawMessage)) *PeerManager {
	return &PeerManager{
		peers:     make(map[string]*peer),
		onMessage: onMessage,
	}
}

func (pm *PeerManager) Connect(url string) error {
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		return err
	}

	p := &peer{
		url:  url,
		conn: conn,
		send: make(chan []byte, 256),
		done: make(chan struct{}),
	}

	pm.mu.Lock()
	pm.peers[url] = p
	pm.mu.Unlock()

	go pm.readLoop(p)
	go pm.writeLoop(p)
	return nil
}

func (pm *PeerManager) Broadcast(msgType string, payload interface{}) {
	raw, err := json.Marshal(payload)
	if err != nil {
		log.Printf("p2p: failed to marshal payload: %v", err)
		return
	}

	env, err := json.Marshal(Envelope{Type: msgType, Payload: raw})
	if err != nil {
		log.Printf("p2p: failed to marshal envelope: %v", err)
		return
	}

	pm.mu.RLock()
	defer pm.mu.RUnlock()

	for _, p := range pm.peers {
		select {
		case p.send <- env:
		default:
			log.Printf("p2p: send buffer full peer=%s", p.url)
		}
	}
}

func (pm *PeerManager) Peers() []string {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	out := make([]string, 0, len(pm.peers))
	for u := range pm.peers {
		out = append(out, u)
	}
	return out
}

func (pm *PeerManager) readLoop(p *peer) {
	defer func() {
		pm.mu.Lock()
		delete(pm.peers, p.url)
		pm.mu.Unlock()
		close(p.done)
		_ = p.conn.Close()
	}()

	for {
		_, msg, err := p.conn.ReadMessage()
		if err != nil {
			return
		}

		if pm.onMessage == nil {
			continue
		}

		var env Envelope
		if err := json.Unmarshal(msg, &env); err != nil {
			continue
		}

		pm.onMessage(p.url, env.Type, env.Payload)
	}
}

func (pm *PeerManager) writeLoop(p *peer) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case msg := <-p.send:
			_ = p.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := p.conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				_ = p.conn.Close()
				return
			}
		case <-ticker.C:
			_ = p.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := p.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				_ = p.conn.Close()
				return
			}
		case <-p.done:
			return
		}
	}
}
