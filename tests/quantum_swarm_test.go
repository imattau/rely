package tests

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	ws "github.com/gorilla/websocket"
	"github.com/nbd-wtf/go-nostr"
	rely "github.com/pippellia-btc/rely/v2"
	"github.com/pippellia-btc/rely/v2/internal/consensus"
	"github.com/pippellia-btc/rely/v2/internal/p2p"
	"github.com/pippellia-btc/rely/v2/internal/quantum"
	"github.com/pippellia-btc/rely/v2/internal/storage"
)

type noteAnnouncement struct {
	ID     string `json:"id"`
	Source string `json:"source"`
	PubKey string `json:"pubkey"`
	Round  int64  `json:"round"`
}

func TestQuantumSwarmPropagation(t *testing.T) {
	if os.Getenv("RUN_QUANTUM_SWARM_STRESS") == "" {
		t.Skip("set RUN_QUANTUM_SWARM_STRESS=1 to run the multi-relay quantum stress test")
	}

	const nodeCount = 3
	publisherCount := envInt(t, "QUANTUM_SWARM_PUBLISHERS", 8)
	eventsPerClient := envInt(t, "QUANTUM_SWARM_EVENTS_PER_CLIENT", 12)
	churnTicks := envInt(t, "QUANTUM_SWARM_CHURN_TICKS", 0)
	churnInterval := envDuration(t, "QUANTUM_SWARM_CHURN_INTERVAL", 50*time.Millisecond)
	totalEvents := publisherCount * eventsPerClient

	// Allocate relay and peer listeners up front so all graphs can be wired before startup.
	relayListeners := make([]net.Listener, nodeCount)
	relayAddrs := make([]string, nodeCount)
	peerListeners := make([]net.Listener, nodeCount)
	peerAddrs := make([]string, nodeCount)
	for i := 0; i < nodeCount; i++ {
		relayLn, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("relay listen %d: %v", i, err)
		}
		peerLn, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			_ = relayLn.Close()
			t.Fatalf("peer listen %d: %v", i, err)
		}
		relayListeners[i] = relayLn
		relayAddrs[i] = relayLn.Addr().String()
		peerListeners[i] = peerLn
		peerAddrs[i] = peerLn.Addr().String()
	}
	defer func() {
		for _, ln := range relayListeners {
			_ = ln.Close()
		}
		for _, ln := range peerListeners {
			_ = ln.Close()
		}
	}()

	nodes := make([]*quantumNode, nodeCount)
	for i := range nodes {
		nodes[i] = newQuantumNode(t, i, relayAddrs, peerAddrs)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	for _, n := range nodes {
		n.start(ctx)
	}
	for i, n := range nodes {
		n.serveRelay(relayListeners[i])
		n.servePeers(peerListeners[i])
	}
	for _, n := range nodes {
		n.connectPeers(t, peerAddrs)
	}

	churnDone := make(chan struct{})
	if churnTicks > 0 {
		go runTopologyChurn(churnDone, churnTicks, churnInterval, nodes)
	}
	defer close(churnDone)

	// Give the mesh a moment to settle.
	time.Sleep(200 * time.Millisecond)

	var wg sync.WaitGroup
	errCh := make(chan error, publisherCount)
	for i := 0; i < publisherCount; i++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			if err := publishBurst(relayAddrs[0], worker, eventsPerClient); err != nil {
				errCh <- err
			}
		}(i)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatalf("publish burst: %v", err)
	}

	waitFor(t, 5*time.Second, func() bool {
		required := totalEvents / 4
		return int(nodes[1].fetches.Load()) >= required && int(nodes[2].fetches.Load()) >= required
	}, func() string {
		return fmt.Sprintf(
			"fetches: n0=%d n1=%d n2=%d publishers=%d events_per_client=%d churn_ticks=%d churn_interval=%s",
			nodes[0].fetches.Load(),
			nodes[1].fetches.Load(),
			nodes[2].fetches.Load(),
			publisherCount,
			eventsPerClient,
			churnTicks,
			churnInterval,
		)
	})
}

type quantumNode struct {
	index    int
	addr     string
	peerAddr string
	relay    *rely.Relay
	store    *storage.Store
	graph    *quantum.GraphState
	diff     *consensus.Diffuser
	prop     *quantum.Propagator
	peers    *p2p.PeerManager
	fetches  atomic.Int32
}

func newQuantumNode(t *testing.T, index int, relayAddrs, peerAddrs []string) *quantumNode {
	t.Helper()

	store := storage.NewStore()
	graph := quantum.NewGraphState()
	graph.SetRelays(relayAddrs)
	for i := 0; i < len(relayAddrs); i++ {
		for j := i + 1; j < len(relayAddrs); j++ {
			graph.SetConnection(relayAddrs[i], relayAddrs[j], true)
		}
	}
	graph.Recompute()

	node := &quantumNode{
		index:    index,
		addr:     relayAddrs[index],
		peerAddr: peerAddrs[index],
		store:    store,
		graph:    graph,
	}

	node.peers = p2p.NewPeerManager(nil)

	node.diff = consensus.NewDiffuser(func(msgType string, payload interface{}) {
		node.peers.Broadcast(msgType, payload)
	}, func() {
		graph.ScheduleRecompute(100 * time.Millisecond)
	})

	node.prop = quantum.NewPropagator(graph, index, 0.01, func(noteID, sourceRelay string) {
		node.fetches.Add(1)
	})

	// Seed a little consensus churn so the diff path is exercised too.
	node.diff.SetReputation("stress", float64(index)/10.0)

	node.relay = rely.NewRelay()
	node.relay.Reject.Event.Clear()
	node.relay.Reject.Connection.Clear()
	node.relay.On.Event = func(c rely.Client, event *nostr.Event) rely.EventResult {
		node.store.Save(*event)
		// Keep the note born-round anchored at zero so propagation does not depend
		// on per-relay consensus skew in the stress harness.
		node.prop.AddNote(event.ID, node.addr, event.PubKey, 0)
		node.peers.Broadcast("note_announce", noteAnnouncement{
			ID:     event.ID,
			Source: node.addr,
			PubKey: event.PubKey,
			Round:  0,
		})
		return rely.Success()
	}
	node.relay.On.Req = func(ctx context.Context, c rely.Client, id string, filters nostr.Filters) ([]nostr.Event, error) {
		_ = ctx
		return node.store.Query(filters), nil
	}

	return node
}

func (n *quantumNode) start(ctx context.Context) {
	n.relay.Start(ctx)
	go n.diff.Run(20*time.Millisecond, ctx.Done())
	go func() {
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				n.prop.Tick(n.diff.GetRound(), 0.5)
			}
		}
	}()
}

func (n *quantumNode) serveRelay(ln net.Listener) {
	server := &http.Server{Handler: n.relay}
	go func() {
		_ = server.Serve(ln)
	}()
}

func (n *quantumNode) servePeers(ln net.Listener) {
	upgrader := ws.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			conn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				return
			}
			go n.handlePeerConn(conn)
		}),
	}
	go func() {
		_ = server.Serve(ln)
	}()
}

func (n *quantumNode) handlePeerConn(conn *ws.Conn) {
	defer conn.Close()

	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			return
		}

		var env p2p.Envelope
		if err := json.Unmarshal(msg, &env); err != nil {
			continue
		}

		switch env.Type {
		case "consensus":
			var s consensus.State
			if err := json.Unmarshal(env.Payload, &s); err == nil {
				n.diff.Enqueue(&s)
			}
		case "note_announce":
			var ann noteAnnouncement
			if err := json.Unmarshal(env.Payload, &ann); err != nil {
				continue
			}
			if n.prop.HasNote(ann.ID) {
				continue
			}
			n.prop.AddNote(ann.ID, ann.Source, ann.PubKey, ann.Round)
			n.peers.Broadcast("note_announce", ann)
		}
	}
}

func (n *quantumNode) connectPeers(t *testing.T, addrs []string) {
	t.Helper()
	for _, addr := range addrs {
		if addr == n.peerAddr {
			continue
		}
		if err := n.peers.Connect("ws://" + addr); err != nil {
			t.Fatalf("connect peer %s -> %s: %v", n.peerAddr, addr, err)
		}
	}
}

func runTopologyChurn(done <-chan struct{}, ticks int, interval time.Duration, nodes []*quantumNode) {
	if ticks <= 0 {
		return
	}
	if interval <= 0 {
		interval = 50 * time.Millisecond
	}
	if len(nodes) < 3 {
		return
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	left := nodes[0].addr
	right := nodes[2].addr
	edgeUp := true
	for i := 0; i < ticks; i++ {
		select {
		case <-done:
			return
		case <-ticker.C:
		}

		edgeUp = !edgeUp
		for _, n := range nodes {
			n.graph.SetConnection(left, right, edgeUp)
			n.graph.Recompute()
		}
	}
}

func publishBurst(addr string, worker, eventsPerClient int) error {
	conn, _, err := ws.DefaultDialer.Dial("ws://"+addr, nil)
	if err != nil {
		return fmt.Errorf("dial %s: %w", addr, err)
	}
	defer conn.Close()

	for i := 0; i < eventsPerClient; i++ {
		ev := nostr.Event{
			CreatedAt: nostr.Now(),
			Kind:      1,
			PubKey:    fmt.Sprintf("pubkey-%d-%d", worker, i),
			Content:   fmt.Sprintf("quantum-stress-%d-%d", worker, i),
		}
		_ = ev.Sign(nostr.GeneratePrivateKey())

		msg, err := json.Marshal([]any{"EVENT", ev})
		if err != nil {
			return fmt.Errorf("marshal event: %w", err)
		}
		if err := conn.WriteMessage(ws.TextMessage, msg); err != nil {
			return fmt.Errorf("write event: %w", err)
		}

		// Drain the OK response so the relay can keep pushing.
		if _, _, err := conn.ReadMessage(); err != nil {
			return fmt.Errorf("read response: %w", err)
		}
	}
	return nil
}

func envInt(t *testing.T, name string, defaultValue int) int {
	t.Helper()

	raw := os.Getenv(name)
	if raw == "" {
		return defaultValue
	}

	value, err := strconv.Atoi(raw)
	if err != nil {
		t.Fatalf("parse %s=%q: %v", name, raw, err)
	}
	if value < 0 {
		t.Fatalf("%s must be non-negative, got %d", name, value)
	}
	return value
}

func envDuration(t *testing.T, name string, defaultValue time.Duration) time.Duration {
	t.Helper()

	raw := os.Getenv(name)
	if raw == "" {
		return defaultValue
	}

	value, err := time.ParseDuration(raw)
	if err != nil {
		t.Fatalf("parse %s=%q: %v", name, raw, err)
	}
	if value <= 0 {
		t.Fatalf("%s must be positive, got %s", name, value)
	}
	return value
}

func waitFor(t *testing.T, timeout time.Duration, cond func() bool, state func() string) {
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
