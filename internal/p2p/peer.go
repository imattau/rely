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
	reconnect bool
	backoff   time.Duration
	connMu    sync.RWMutex
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
	if url == "" {
		return nil
	}

	p := &peer{
		url:       url,
		send:      make(chan []byte, 256),
		done:      make(chan struct{}),
		reconnect: true,
		backoff:   time.Second,
	}

	pm.mu.Lock()
	pm.peers[url] = p
	pm.mu.Unlock()

	go pm.reconnectLoop(p)
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
		p.connMu.Lock()
		p.reconnect = false
		p.connMu.Unlock()
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

func (pm *PeerManager) reconnectLoop(p *peer) {
	if p == nil {
		return
	}

	backoff := p.backoff
	if backoff <= 0 {
		backoff = time.Second
	}

	for {
		if p.isStopped() || !p.shouldReconnect() {
			return
		}

		conn, _, err := websocket.DefaultDialer.Dial(p.url, nil)
		if err != nil {
			log.Printf("p2p: dial failed peer=%s: %v", p.url, err)
			if !sleepWithStop(p.done, backoff) {
				return
			}
			backoff = nextBackoff(backoff)
			continue
		}

		p.setConn(conn)
		readSeen := false
		activity := make(chan struct{}, 1)
		connStop := make(chan struct{})
		var stopOnce sync.Once
		stopConn := func() {
			stopOnce.Do(func() {
				close(connStop)
				_ = conn.Close()
			})
		}

		go func() {
			pm.readLoop(p, conn, activity, connStop)
			stopConn()
		}()
		go func() {
			pm.writeLoop(p, conn, connStop)
			stopConn()
		}()

		for {
			select {
			case <-p.done:
				stopConn()
				return
			case <-activity:
				readSeen = true
				backoff = time.Second
			case <-connStop:
				if readSeen {
					backoff = time.Second
				} else {
					backoff = nextBackoff(backoff)
				}
				p.clearConn(conn)
				if !sleepWithStop(p.done, backoff) {
					return
				}
				goto reconnect
			}
		}

	reconnect:
		p.clearConn(conn)
	}
}

func (pm *PeerManager) readLoop(p *peer, conn *websocket.Conn, activity chan struct{}, stop <-chan struct{}) {
	for {
		select {
		case <-stop:
			return
		default:
		}

		_, msg, err := conn.ReadMessage()
		if err != nil {
			return
		}

		select {
		case activity <- struct{}{}:
		default:
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

func (pm *PeerManager) writeLoop(p *peer, conn *websocket.Conn, stop <-chan struct{}) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case msg := <-p.send:
			_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				return
			}
		case <-ticker.C:
			_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		case <-stop:
			return
		}
	}
}

func (p *peer) close() {
	p.closeOnce.Do(func() {
		close(p.done)
		p.connMu.Lock()
		if p.conn != nil {
			_ = p.conn.Close()
		}
		p.conn = nil
		p.connMu.Unlock()
	})
}

func (p *peer) setConn(conn *websocket.Conn) {
	p.connMu.Lock()
	p.conn = conn
	p.connMu.Unlock()
}

func (p *peer) clearConn(conn *websocket.Conn) {
	p.connMu.Lock()
	if p.conn == conn {
		p.conn = nil
	}
	p.connMu.Unlock()
}

func (p *peer) isStopped() bool {
	select {
	case <-p.done:
		return true
	default:
		return false
	}
}

func (p *peer) shouldReconnect() bool {
	p.connMu.RLock()
	defer p.connMu.RUnlock()
	return p.reconnect
}

func sleepWithStop(done <-chan struct{}, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-done:
		return false
	case <-timer.C:
		return true
	}
}

func nextBackoff(current time.Duration) time.Duration {
	if current <= 0 {
		return time.Second
	}
	next := current * 2
	if next > 60*time.Second {
		return 60 * time.Second
	}
	return next
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
