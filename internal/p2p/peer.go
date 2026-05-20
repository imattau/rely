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
	url       string
	conn      *websocket.Conn
	send      chan []byte
	done      chan struct{}
	closeOnce sync.Once
}

type PeerManager struct {
	mu           sync.RWMutex
	peers        map[string]*peer
	trustWeights map[string]float64
	onMessage    func(peerURL, msgType string, payload json.RawMessage)
}

func NewPeerManager(onMessage func(peerURL, msgType string, payload json.RawMessage)) *PeerManager {
	return &PeerManager{
		peers:        make(map[string]*peer),
		trustWeights: make(map[string]float64),
		onMessage:    onMessage,
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

// AddConnectedPeerForTest registers an in-memory peer without dialing a live websocket.
// It exists to keep higher-level tests sandbox-friendly.
func (pm *PeerManager) AddConnectedPeerForTest(url string) {
	p := &peer{
		url:  url,
		send: make(chan []byte, 256),
		done: make(chan struct{}),
	}

	pm.mu.Lock()
	pm.peers[url] = p
	pm.mu.Unlock()
}

func (pm *PeerManager) Broadcast(msgType string, payload interface{}) {
	env := marshalEnvelope(msgType, payload)
	if env == nil {
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

func (pm *PeerManager) BroadcastToTrusted(msgType string, payload interface{}) {
	env := marshalEnvelope(msgType, payload)
	if env == nil {
		return
	}

	pm.mu.RLock()
	defer pm.mu.RUnlock()

	for _, p := range pm.peers {
		if weight, ok := pm.trustWeights[p.url]; ok {
			if weight > 1.0 {
				select {
				case p.send <- env:
				default:
					log.Printf("p2p: send buffer full peer=%s", p.url)
				}
			}
		}
	}
}

func (pm *PeerManager) SetTrustWeight(url string, weight float64) {
	pm.mu.Lock()
	pm.trustWeights[url] = weight
	pm.mu.Unlock()
}

func (pm *PeerManager) TrustWeight(url string) float64 {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	if weight, ok := pm.trustWeights[url]; ok {
		return weight
	}
	return 1.0
}

func (pm *PeerManager) Disconnect(url string) {
	pm.mu.Lock()
	p, ok := pm.peers[url]
	if ok {
		delete(pm.peers, url)
	}
	pm.mu.Unlock()
	if ok {
		p.close()
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
		p.close()
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
				p.close()
				return
			}
		case <-ticker.C:
			_ = p.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := p.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				p.close()
				return
			}
		case <-p.done:
			return
		}
	}
}

func (p *peer) close() {
	p.closeOnce.Do(func() {
		close(p.done)
		if p.conn != nil {
			_ = p.conn.Close()
		}
	})
}

func marshalEnvelope(msgType string, payload interface{}) []byte {
	raw, err := json.Marshal(payload)
	if err != nil {
		log.Printf("p2p: failed to marshal payload: %v", err)
		return nil
	}

	env, err := json.Marshal(Envelope{Type: msgType, Payload: raw})
	if err != nil {
		log.Printf("p2p: failed to marshal envelope: %v", err)
		return nil
	}
	return env
}
