package main

import (
	"context"
	"encoding/json"
	"math"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/nbd-wtf/go-nostr"
	rely "github.com/pippellia-btc/rely/v2"
	"github.com/pippellia-btc/rely/v2/internal/consensus"
	"github.com/pippellia-btc/rely/v2/internal/p2p"
	"github.com/pippellia-btc/rely/v2/internal/storage"
)

func TestApplyTrustWeights(t *testing.T) {
	pm := p2p.NewPeerManager(nil)
	cfg := TrustConfig{
		Enabled: true,
		Weight:  3.0,
		Peers:   []string{"wss://trusted1.example.com", "wss://trusted2.example.com"},
	}

	applyTrustWeights(pm, cfg)

	if got := pm.TrustWeight(cfg.Peers[0]); got != 3.0 {
		t.Fatalf("expected trust weight 3.0, got %v", got)
	}
	if got := pm.TrustWeight(cfg.Peers[1]); got != 3.0 {
		t.Fatalf("expected trust weight 3.0, got %v", got)
	}
}

func TestHandlePeerMessageBlockPeer(t *testing.T) {
	pm := p2p.NewPeerManager(nil)

	senderURL := "wss://sender.example.com"
	targetURL := "wss://target.example.com"
	pm.AddConnectedPeerForTest(senderURL)
	pm.AddConnectedPeerForTest(targetURL)

	pm.SetTrustWeight(senderURL, 2.0)
	cfg := &Config{Trust: TrustConfig{Enabled: true}}

	payload, err := json.Marshal(targetURL)
	if err != nil {
		t.Fatalf("marshal target: %v", err)
	}
	handlePeerMessage(cfg, pm, consensus.NewDiffuser(nil, nil), nil, senderURL, "block_peer", payload)

	peers := pm.Peers()
	if len(peers) != 1 {
		t.Fatalf("expected 1 peer after block_peer, got %d", len(peers))
	}
	if peers[0] != senderURL {
		t.Fatalf("expected sender to remain connected, got %v", peers)
	}
}

func TestKind1984ReputationDelta(t *testing.T) {
	d := consensus.NewDiffuser(nil, nil)

	event := &nostr.Event{
		Tags: nostr.Tags{
			{"p", "deadbeef01"},
		},
	}

	applyKind1984Report(d, event, 2.0)
	applyKind1984Report(d, event, 2.0)

	got := d.GetReputation("deadbeef01")
	expected := -0.4
	if math.Abs(got-expected) > 1e-9 {
		t.Fatalf("expected %v, got %v", expected, got)
	}
}

func TestFetchEventFromRelay(t *testing.T) {
	store := storage.NewStore()
	relay := rely.NewRelay()
	relay.Reject.Connection.Clear()
	relay.Reject.Event.Clear()
	relay.On.Event = func(c rely.Client, event *nostr.Event) rely.EventResult {
		store.Save(*event)
		return rely.Success()
	}
	relay.On.Req = func(ctx context.Context, c rely.Client, id string, filters nostr.Filters) ([]nostr.Event, error) {
		_ = ctx
		_ = c
		_ = id
		return store.Query(filters), nil
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen relay: %v", err)
	}
	defer ln.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go relay.Start(ctx)
	go func() {
		_ = (&http.Server{Handler: relay}).Serve(ln)
	}()

	event := nostr.Event{
		CreatedAt: nostr.Now(),
		Kind:      1,
		PubKey:    "fetch-pubkey",
		Content:   "fetch me",
	}
	if err := event.Sign(nostr.GeneratePrivateKey()); err != nil {
		t.Fatalf("sign event: %v", err)
	}

	conn, _, err := websocket.DefaultDialer.Dial("ws://"+ln.Addr().String(), nil)
	if err != nil {
		t.Fatalf("dial source relay: %v", err)
	}
	defer conn.Close()

	payload, err := json.Marshal([]any{"EVENT", event})
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	if err := conn.WriteMessage(websocket.TextMessage, payload); err != nil {
		t.Fatalf("write event: %v", err)
	}
	if _, msg, err := conn.ReadMessage(); err != nil {
		t.Fatalf("read ok response: %v", err)
	} else {
		var resp []json.RawMessage
		if err := json.Unmarshal(msg, &resp); err != nil {
			t.Fatalf("unmarshal ok response: %v", err)
		}
		if len(resp) == 0 {
			t.Fatalf("unexpected ok response: %s", string(msg))
		}
		var label string
		if err := json.Unmarshal(resp[0], &label); err != nil || label != "OK" {
			t.Fatalf("unexpected response label: %s", string(msg))
		}
	}

	fetched, err := fetchEventFromRelay(context.Background(), "ws://"+ln.Addr().String(), event.ID)
	if err != nil {
		t.Fatalf("fetchEventFromRelay: %v", err)
	}
	if fetched == nil {
		t.Fatal("expected fetched event")
	}
	if fetched.ID != event.ID {
		t.Fatalf("fetched ID = %s, want %s", fetched.ID, event.ID)
	}
	if fetched.Content != event.Content {
		t.Fatalf("fetched content = %q, want %q", fetched.Content, event.Content)
	}
}

// TestQuantumFetchBroadcastToSubscriber verifies the full propagation path:
// quantumFetcher.Fetch → fetchEventFromRelay → store.Save → r.Broadcast →
// Dispatcher → subscribed client receives EVENT.
func TestQuantumFetchBroadcastToSubscriber(t *testing.T) {
	// --- source relay: holds the note to be fetched ---
	srcStore := storage.NewStore()
	srcRelay := rely.NewRelay()
	srcRelay.Reject.Connection.Clear()
	srcRelay.Reject.Event.Clear()
	srcRelay.On.Event = func(c rely.Client, event *nostr.Event) rely.EventResult {
		srcStore.Save(*event)
		return rely.Success()
	}
	srcRelay.On.Req = func(ctx context.Context, c rely.Client, id string, filters nostr.Filters) ([]nostr.Event, error) {
		return srcStore.Query(filters), nil
	}

	srcLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen source relay: %v", err)
	}
	defer srcLn.Close()

	srcCtx, srcCancel := context.WithCancel(context.Background())
	defer srcCancel()
	go srcRelay.Start(srcCtx)
	go func() { _ = (&http.Server{Handler: srcRelay}).Serve(srcLn) }()

	// publish a note to the source relay
	event := nostr.Event{
		CreatedAt: nostr.Now(),
		Kind:      1,
		PubKey:    "broadcast-test-pubkey",
		Content:   "propagated note",
	}
	if err := event.Sign(nostr.GeneratePrivateKey()); err != nil {
		t.Fatalf("sign: %v", err)
	}
	srcURL := "ws://" + srcLn.Addr().String()
	publishToRelay(t, srcURL, event)

	// --- local relay: where the subscriber connects ---
	localStore := storage.NewStore()
	localRelay := rely.NewRelay()
	localRelay.Reject.Connection.Clear()
	localRelay.Reject.Event.Clear()
	localRelay.On.Event = func(c rely.Client, e *nostr.Event) rely.EventResult {
		localStore.Save(*e)
		return rely.Success()
	}
	localRelay.On.Req = func(ctx context.Context, c rely.Client, id string, filters nostr.Filters) ([]nostr.Event, error) {
		return localStore.Query(filters), nil
	}

	localLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen local relay: %v", err)
	}
	defer localLn.Close()

	localCtx, localCancel := context.WithCancel(context.Background())
	defer localCancel()
	go localRelay.Start(localCtx)
	go func() { _ = (&http.Server{Handler: localRelay}).Serve(localLn) }()

	// give relays a moment to start
	time.Sleep(20 * time.Millisecond)

	// --- subscriber: connects to local relay with a matching REQ ---
	subConn, _, err := websocket.DefaultDialer.Dial("ws://"+localLn.Addr().String(), nil)
	if err != nil {
		t.Fatalf("dial local relay: %v", err)
	}
	defer subConn.Close()

	req, _ := json.Marshal([]any{"REQ", "sub1", nostr.Filter{Kinds: []int{1}}})
	if err := subConn.WriteMessage(websocket.TextMessage, req); err != nil {
		t.Fatalf("write REQ: %v", err)
	}

	// --- fetcher: wired to local relay ---
	fetcher := newQuantumFetcher("ws://"+localLn.Addr().String(), localStore, consensus.NewDiffuser(nil, nil), TrustConfig{})
	fetcher.relay = localRelay

	// trigger fetch from source relay
	fetcher.Fetch(event.ID, srcURL)

	// --- subscriber should receive the EVENT within 2 seconds ---
	subConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	received := false
	for !received {
		_, msg, err := subConn.ReadMessage()
		if err != nil {
			t.Fatalf("read from subscriber: %v — propagated note never delivered", err)
		}
		var parts []json.RawMessage
		if err := json.Unmarshal(msg, &parts); err != nil || len(parts) < 3 {
			continue
		}
		var label string
		if err := json.Unmarshal(parts[0], &label); err != nil || label != "EVENT" {
			continue
		}
		var got nostr.Event
		if err := json.Unmarshal(parts[2], &got); err != nil {
			continue
		}
		if got.ID == event.ID {
			received = true
		}
	}

	if !received {
		t.Error("subscriber did not receive propagated note")
	}
}

func publishToRelay(t *testing.T, url string, event nostr.Event) {
	t.Helper()
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("dial %s: %v", url, err)
	}
	defer conn.Close()

	payload, _ := json.Marshal([]any{"EVENT", event})
	if err := conn.WriteMessage(websocket.TextMessage, payload); err != nil {
		t.Fatalf("write EVENT: %v", err)
	}
	// drain OK response
	conn.SetReadDeadline(time.Now().Add(time.Second))
	conn.ReadMessage()
}
