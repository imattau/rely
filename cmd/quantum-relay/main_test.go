package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/nbd-wtf/go-nostr"
	rely "github.com/pippellia-btc/rely/v2"
	"github.com/pippellia-btc/rely/v2/internal/consensus"
	"github.com/pippellia-btc/rely/v2/internal/p2p"
	"github.com/pippellia-btc/rely/v2/internal/quantum"
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

func TestConfigFilePathFromEnv(t *testing.T) {
	t.Setenv("RELY_CONFIG", "/etc/rely/config.yaml")
	if got := configFilePath(); got != "/etc/rely/config.yaml" {
		t.Fatalf("expected env config path, got %q", got)
	}
}

func TestInboundPeerAllowed(t *testing.T) {
	t.Run("open mesh allows inbound peers", func(t *testing.T) {
		cfg := defaultConfig()
		cfg.Trust.Enabled = false
		if !inboundPeerAllowed(cfg, "wss://any.example.com") {
			t.Fatal("expected inbound peer to be allowed when trust is disabled")
		}
	})

	t.Run("restricted mesh rejects unknown peers", func(t *testing.T) {
		cfg := defaultConfig()
		cfg.Trust.Enabled = true
		cfg.Peers = []string{"wss://trusted.example.com"}
		if inboundPeerAllowed(cfg, "wss://other.example.com") {
			t.Fatal("expected inbound peer to be rejected when not in trust allowlist")
		}
	})

	t.Run("restricted mesh allows configured peers", func(t *testing.T) {
		cfg := defaultConfig()
		cfg.Trust.Enabled = true
		cfg.Peers = []string{"wss://trusted.example.com"}
		if !inboundPeerAllowed(cfg, "ws://trusted.example.com") {
			t.Fatal("expected inbound peer to be allowed when present in peers allowlist")
		}
	})
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

func TestApplyNIP09Deletion(t *testing.T) {
	store := storage.NewStore()
	owned := nostr.Event{ID: "owned", PubKey: "author", Kind: 1}
	foreign := nostr.Event{ID: "foreign", PubKey: "other", Kind: 1}
	store.Save(owned)
	store.Save(foreign)

	deleteEvent := &nostr.Event{
		Kind:   5,
		PubKey: "author",
		Tags: nostr.Tags{
			{"e", "owned"},
			{"e", "foreign"},
		},
	}

	applyNIP09Deletion(store, deleteEvent)

	if _, ok := store.Get("owned"); ok {
		t.Fatal("expected owned event to be deleted")
	}
	if _, ok := store.Get("foreign"); !ok {
		t.Fatal("expected foreign event to remain")
	}
}

func TestAuthRequiredRejectsUnauthedEvent(t *testing.T) {
	cfg := defaultConfig()
	cfg.Auth.Required = true

	store := storage.NewStore()
	relay := rely.NewRelay(rely.WithAuthURL("relay.example.com"))
	relay.Reject.Connection.Clear()
	relay.Reject.Event.Clear()
	relay.On.Connect = func(c rely.Client) {
		c.SendAuth()
	}
	relay.Reject.Event.Append(func(c rely.Client, event *nostr.Event) error {
		if cfg.Auth.Required && !c.IsAuthed() {
			return fmt.Errorf("auth-required: authentication needed to publish events")
		}
		return nil
	})
	relay.On.Event = func(c rely.Client, event *nostr.Event) rely.EventResult {
		store.Save(*event)
		return rely.Success()
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen relay: %v", err)
	}
	defer ln.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go relay.Start(ctx)
	go func() { _ = (&http.Server{Handler: relay}).Serve(ln) }()

	conn, _, err := websocket.DefaultDialer.Dial("ws://"+ln.Addr().String(), nil)
	if err != nil {
		t.Fatalf("dial relay: %v", err)
	}
	defer conn.Close()

	drainAuthChallenge(t, conn)

	event := nostr.Event{
		CreatedAt: nostr.Now(),
		Kind:      1,
		PubKey:    "unauth-pubkey",
		Content:   "blocked",
	}
	if err := event.Sign(nostr.GeneratePrivateKey()); err != nil {
		t.Fatalf("sign event: %v", err)
	}
	payload, err := json.Marshal([]any{"EVENT", event})
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	if err := conn.WriteMessage(websocket.TextMessage, payload); err != nil {
		t.Fatalf("write event: %v", err)
	}

	var label string
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, msg, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read ok response: %v", err)
	}
	var okParts []json.RawMessage
	if err := json.Unmarshal(msg, &okParts); err != nil {
		t.Fatalf("unmarshal ok response: %v", err)
	}
	if len(okParts) < 3 {
		t.Fatalf("unexpected OK response: %s", string(msg))
	}
	if err := json.Unmarshal(okParts[0], &label); err != nil || label != "OK" {
		t.Fatalf("expected OK response, got %s", string(msg))
	}
	var accepted bool
	if err := json.Unmarshal(okParts[2], &accepted); err != nil {
		t.Fatalf("parse accepted flag: %v", err)
	}
	if accepted {
		t.Fatal("expected unauthenticated event to be rejected")
	}
}

func drainAuthChallenge(t *testing.T, conn *websocket.Conn) {
	t.Helper()

	deadline := time.Now().Add(250 * time.Millisecond)
	for time.Now().Before(deadline) {
		_ = conn.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
		_, msg, err := conn.ReadMessage()
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				return
			}
			return
		}

		var parts []json.RawMessage
		if err := json.Unmarshal(msg, &parts); err != nil || len(parts) == 0 {
			continue
		}

		var label string
		if err := json.Unmarshal(parts[0], &label); err == nil && label == "AUTH" {
			return
		}
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
	fetcher := newQuantumFetcher("ws://"+localLn.Addr().String(), localStore, consensus.NewDiffuser(nil, nil), TrustConfig{}, 32)
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

func TestQuantumRelayLiveSourcePropagation(t *testing.T) {
	sourceCandidates := liveRelayCandidates(os.Getenv("RELAY_LIVE_SOURCE"))
	if len(sourceCandidates) == 0 {
		t.Skip("set RELAY_LIVE_SOURCE to a real relay URL or domain to run the live propagation smoke test")
	}

	localStore := storage.NewStore()
	localRelay := rely.NewRelay()
	localRelay.Reject.Connection.Clear()
	localRelay.Reject.Event.Clear()
	localRelay.On.Event = func(c rely.Client, event *nostr.Event) rely.EventResult {
		localStore.Save(*event)
		return rely.Success()
	}
	localRelay.On.Req = func(ctx context.Context, c rely.Client, id string, filters nostr.Filters) ([]nostr.Event, error) {
		_ = ctx
		_ = c
		_ = id
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

	fetcher := newQuantumFetcher("ws://"+localLn.Addr().String(), localStore, consensus.NewDiffuser(nil, nil), TrustConfig{}, 32)
	fetcher.relay = localRelay

	note := nostr.Event{
		CreatedAt: nostr.Now(),
		Kind:      1,
		PubKey:    "live-domain-pubkey",
		Content:   "live source propagation smoke test",
	}
	if err := note.Sign(nostr.GeneratePrivateKey()); err != nil {
		t.Fatalf("sign note: %v", err)
	}

	var sourceURL string
	var publishErrs []string
	for _, candidate := range sourceCandidates {
		if err := tryPublishToRelay(candidate, note); err == nil {
			sourceURL = candidate
			break
		} else {
			publishErrs = append(publishErrs, fmt.Sprintf("%s: %v", candidate, err))
		}
	}
	if sourceURL == "" {
		t.Fatalf("unable to publish to any live source candidate: %v; errors: %s", sourceCandidates, strings.Join(publishErrs, " | "))
	}

	fetcher.Fetch(note.ID, sourceURL)

	waitForCondition(t, 10*time.Second, func() bool {
		_, ok := localStore.Get(note.ID)
		return ok
	}, func() string {
		_, ok := localStore.Get(note.ID)
		return fmt.Sprintf("local store has note=%v", ok)
	})

	fetched, err := requestEventFromRelay(t, "ws://"+localLn.Addr().String(), note.ID)
	if err != nil {
		t.Fatalf("requestEventFromRelay: %v", err)
	}
	if fetched.ID != note.ID {
		t.Fatalf("fetched ID = %s, want %s", fetched.ID, note.ID)
	}
	if fetched.Content != note.Content {
		t.Fatalf("fetched content = %q, want %q", fetched.Content, note.Content)
	}
}

func TestQuantumRelayLiveSourcePeerPropagation(t *testing.T) {
	sourceCandidates := liveRelayCandidates(os.Getenv("RELAY_LIVE_SOURCE"))
	if len(sourceCandidates) == 0 {
		t.Skip("set RELAY_LIVE_SOURCE to a real relay URL or domain to run the live peer propagation smoke test")
	}

	cfg := defaultConfig()
	localStore := storage.NewStore()
	localRelay := rely.NewRelay()
	localRelay.Reject.Connection.Clear()
	localRelay.Reject.Event.Clear()
	localRelay.On.Event = func(c rely.Client, event *nostr.Event) rely.EventResult {
		localStore.Save(*event)
		return rely.Success()
	}
	localRelay.On.Req = func(ctx context.Context, c rely.Client, id string, filters nostr.Filters) ([]nostr.Event, error) {
		_ = ctx
		_ = c
		_ = id
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

	localURL := "ws://" + localLn.Addr().String()

	diffuser := consensus.NewDiffuser(nil, nil)
	graph := quantum.NewGraphState()
	fetcher := newQuantumFetcher(localURL, localStore, diffuser, cfg.Trust, 32)
	fetcher.relay = localRelay
	graph.SetRelays([]string{localURL})
	graph.Recompute()
	prop := quantum.NewPropagator(graph, graph.GetRelayIndex(localURL), 0.05, fetcher.Fetch)
	prop.SetReputationLookup(diffuser.GetReputation)

	peerCandidates := livePeerCandidates(os.Getenv("RELAY_LIVE_SOURCE"))
	if len(peerCandidates) == 0 {
		t.Skip("could not derive a peer endpoint candidate from RELAY_LIVE_SOURCE")
	}

	peerURL := peerCandidates[0]
	peerReady := make(chan struct{}, 1)
	announceSeen := make(chan struct{}, 1)
	var peerMgr *p2p.PeerManager
	peerMgr = p2p.NewPeerManager(func(peerURL, msgType string, payload json.RawMessage) {
		switch msgType {
		case "hello":
			select {
			case peerReady <- struct{}{}:
			default:
			}
			return
		case "note_announce":
			select {
			case announceSeen <- struct{}{}:
			default:
			}
		}

		handlePeerMessage(cfg, peerMgr, diffuser, prop, peerURL, msgType, payload)
	})
	peerMgr.Connect(peerURL)
	defer peerMgr.Disconnect(peerURL)

	waitForCondition(t, 10*time.Second, func() bool {
		select {
		case <-peerReady:
			return true
		default:
			return false
		}
	}, func() string {
		return fmt.Sprintf("waiting for peer hello from %s (wrong endpoint or peer server not running?)", peerURL)
	})

	done := make(chan struct{})
	defer close(done)
	go diffuser.Run(250*time.Millisecond, done)
	go func() {
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				prop.Tick(diffuser.GetRound(), 0.05)
			}
		}
	}()

	note := nostr.Event{
		CreatedAt: nostr.Now(),
		Kind:      1,
		PubKey:    "live-peer-pubkey",
		Content:   "live peer propagation smoke test",
	}
	if err := note.Sign(nostr.GeneratePrivateKey()); err != nil {
		t.Fatalf("sign note: %v", err)
	}

	var sourceURL string
	var publishErrs []string
	for _, candidate := range sourceCandidates {
		if err := tryPublishToRelay(candidate, note); err == nil {
			sourceURL = candidate
			break
		} else {
			publishErrs = append(publishErrs, fmt.Sprintf("%s: %v", candidate, err))
		}
	}
	if sourceURL == "" {
		t.Fatalf("unable to publish to any live source candidate: %v; errors: %s", sourceCandidates, strings.Join(publishErrs, " | "))
	}

	waitForCondition(t, 30*time.Second, func() bool {
		select {
		case <-announceSeen:
			return true
		default:
			return false
		}
	}, func() string {
		return fmt.Sprintf("waiting for note_announce note=%s peer=%s", note.ID, sourceURL)
	})

	waitForCondition(t, 10*time.Second, func() bool {
		_, ok := localStore.Get(note.ID)
		return ok
	}, func() string {
		_, ok := localStore.Get(note.ID)
		return fmt.Sprintf("local store has note=%v peer=%s", ok, peerURL)
	})

	fetched, err := requestEventFromRelay(t, localURL, note.ID)
	if err != nil {
		t.Fatalf("requestEventFromRelay: %v", err)
	}
	if fetched.ID != note.ID {
		t.Fatalf("fetched ID = %s, want %s", fetched.ID, note.ID)
	}
	if fetched.Content != note.Content {
		t.Fatalf("fetched content = %q, want %q", fetched.Content, note.Content)
	}
}

func assertPeerEndpointHandshake(t *testing.T, peerURL string) {
	t.Helper()

	conn, _, err := dialTestRelayWebsocket(peerURL)
	if err != nil {
		t.Fatalf("peer websocket handshake failed for %s: %v", peerURL, err)
	}
	defer conn.Close()
	t.Logf("peer websocket handshake succeeded for %s", peerURL)
}

func TestPeerEndpointBroadcastsNoteAnnounce(t *testing.T) {
	peerMgr := p2p.NewPeerManager(nil)
	server := newPeerServer(defaultConfig(), peerMgr)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen peer endpoint: %v", err)
	}
	defer ln.Close()

	srv := &http.Server{Handler: server}
	go func() { _ = srv.Serve(ln) }()
	defer func() { _ = srv.Close() }()

	conn, _, err := websocket.DefaultDialer.Dial("ws://"+ln.Addr().String(), nil)
	if err != nil {
		t.Fatalf("dial peer endpoint: %v", err)
	}
	defer conn.Close()

	waitForCondition(t, 2*time.Second, func() bool {
		return len(peerMgr.Peers()) == 1
	}, func() string {
		return fmt.Sprintf("waiting for peer registration, peers=%v", peerMgr.Peers())
	})

	peerMgr.Broadcast("note_announce", map[string]any{
		"id":     "abc",
		"source": "ws://source.example.com",
		"pubkey": "pubkey",
		"round":  7,
	})

	env := readPeerEnvelopeOfType(t, conn, "note_announce", 2*time.Second)
	_ = env
}

func TestPeerEndpointTrustGating(t *testing.T) {
	t.Run("accepts allowlisted inbound peers", func(t *testing.T) {
		peerMgr := p2p.NewPeerManager(nil)
		cfg := defaultConfig()
		cfg.Trust.Enabled = true

		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("listen peer endpoint: %v", err)
		}
		defer ln.Close()

		allowedPeerURL := "wss://" + ln.Addr().String()
		cfg.Peers = []string{allowedPeerURL}

		server := newPeerServer(cfg, peerMgr)
		srv := &http.Server{Handler: server}
		go func() { _ = srv.Serve(ln) }()
		defer func() { _ = srv.Close() }()

		conn, _, err := websocket.DefaultDialer.Dial("ws://"+ln.Addr().String(), nil)
		if err != nil {
			t.Fatalf("dial peer endpoint: %v", err)
		}
		defer conn.Close()

		waitForCondition(t, 2*time.Second, func() bool {
			return len(peerMgr.Peers()) == 1
		}, func() string {
			return fmt.Sprintf("waiting for allowlisted peer registration, peers=%v", peerMgr.Peers())
		})

		peerMgr.Broadcast("note_announce", map[string]any{
			"id":     "allowlisted",
			"source": "wss://source.example.com",
			"pubkey": "pubkey",
			"round":  11,
		})

		env := readPeerEnvelopeOfType(t, conn, "note_announce", 2*time.Second)
		_ = env
	})

	t.Run("rejects non-allowlisted inbound peers", func(t *testing.T) {
		peerMgr := p2p.NewPeerManager(nil)
		cfg := defaultConfig()
		cfg.Trust.Enabled = true
		cfg.Trust.Peers = []string{"wss://other.example.com"}

		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("listen peer endpoint: %v", err)
		}
		defer ln.Close()

		server := newPeerServer(cfg, peerMgr)
		srv := &http.Server{Handler: server}
		go func() { _ = srv.Serve(ln) }()
		defer func() { _ = srv.Close() }()

		conn, _, err := websocket.DefaultDialer.Dial("ws://"+ln.Addr().String(), nil)
		if err != nil {
			t.Fatalf("dial peer endpoint: %v", err)
		}
		defer conn.Close()

		waitForCondition(t, 2*time.Second, func() bool {
			return len(peerMgr.Peers()) == 0
		}, func() string {
			return fmt.Sprintf("waiting for rejected peer to stay unregistered, peers=%v", peerMgr.Peers())
		})

		conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		_, _, err = conn.ReadMessage()
		if err == nil {
			t.Fatal("expected rejected inbound peer connection to close")
		}
	})
}

func TestLocalTwoRelayPeerPropagation(t *testing.T) {
	storeA := storage.NewStore()
	relayA := rely.NewRelay()
	relayA.Reject.Connection.Clear()
	relayA.Reject.Event.Clear()
	relayA.On.Req = func(ctx context.Context, c rely.Client, id string, filters nostr.Filters) ([]nostr.Event, error) {
		_ = ctx
		_ = c
		_ = id
		return storeA.Query(filters), nil
	}

	storeB := storage.NewStore()
	relayB := rely.NewRelay()
	relayB.Reject.Connection.Clear()
	relayB.Reject.Event.Clear()
	relayB.On.Req = func(ctx context.Context, c rely.Client, id string, filters nostr.Filters) ([]nostr.Event, error) {
		_ = ctx
		_ = c
		_ = id
		return storeB.Query(filters), nil
	}

	relayALn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen relay A: %v", err)
	}
	defer relayALn.Close()

	relayBLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen relay B: %v", err)
	}
	defer relayBLn.Close()

	peerLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen peer endpoint: %v", err)
	}
	defer peerLn.Close()

	relayACtx, relayACancel := context.WithCancel(context.Background())
	defer relayACancel()
	go relayA.Start(relayACtx)
	go func() { _ = (&http.Server{Handler: relayA}).Serve(relayALn) }()

	relayBCtx, relayBCancel := context.WithCancel(context.Background())
	defer relayBCancel()
	go relayB.Start(relayBCtx)
	go func() { _ = (&http.Server{Handler: relayB}).Serve(relayBLn) }()

	relayAURL := "ws://" + relayALn.Addr().String()
	relayBURL := "ws://" + relayBLn.Addr().String()
	peerURL := "ws://" + peerLn.Addr().String()

	diffuserB := consensus.NewDiffuser(nil, nil)
	graphB := quantum.NewGraphState()
	graphB.SetRelays([]string{relayBURL})
	graphB.Recompute()
	fetcherB := newQuantumFetcher(relayBURL, storeB, diffuserB, TrustConfig{}, 32)
	fetcherB.relay = relayB
	propB := quantum.NewPropagator(graphB, graphB.GetRelayIndex(relayBURL), 0.05, fetcherB.Fetch)
	propB.SetReputationLookup(diffuserB.GetReputation)

	announceSeen := make(chan struct{}, 1)
	cfg := defaultConfig()
	cfg.Trust.Enabled = false
	var peerMgrB *p2p.PeerManager
	peerMgrB = p2p.NewPeerManager(func(peerURL, msgType string, payload json.RawMessage) {
		handlePeerMessage(cfg, peerMgrB, diffuserB, propB, peerURL, msgType, payload)
		if msgType == "note_announce" {
			select {
			case announceSeen <- struct{}{}:
			default:
			}
		}
	})
	peerServer := newPeerServer(cfg, peerMgrB)
	peerSrv := &http.Server{Handler: peerServer}
	go func() { _ = peerSrv.Serve(peerLn) }()
	defer func() { _ = peerSrv.Close() }()

	peerMgrA := p2p.NewPeerManager(nil)
	if err := peerMgrA.Connect(peerURL); err != nil {
		t.Fatalf("connect peer: %v", err)
	}
	defer peerMgrA.Disconnect(peerURL)

	waitForCondition(t, 5*time.Second, func() bool {
		return len(peerMgrB.Peers()) == 1
	}, func() string {
		return fmt.Sprintf("waiting for peer registration, peers=%v", peerMgrB.Peers())
	})

	note := nostr.Event{
		CreatedAt: nostr.Now(),
		Kind:      1,
		PubKey:    "relay-a-pubkey",
		Content:   "local two relay peer propagation smoke test",
	}
	if err := note.Sign(nostr.GeneratePrivateKey()); err != nil {
		t.Fatalf("sign note: %v", err)
	}

	relayA.On.Event = func(c rely.Client, event *nostr.Event) rely.EventResult {
		storeA.Save(*event)
		peerMgrA.Broadcast("note_announce", map[string]any{
			"id":     event.ID,
			"source": relayAURL,
			"pubkey": event.PubKey,
			"round":  1,
		})
		return rely.Success()
	}
	relayA.On.Req = func(ctx context.Context, c rely.Client, id string, filters nostr.Filters) ([]nostr.Event, error) {
		_ = ctx
		_ = c
		_ = id
		return storeA.Query(filters), nil
	}

	publishToRelay(t, relayAURL, note)

	waitForCondition(t, 5*time.Second, func() bool {
		select {
		case <-announceSeen:
			return true
		default:
			return false
		}
	}, func() string {
		return "waiting for local note_announce receipt"
	})

	propB.AddNote(note.ID, relayAURL, note.PubKey, 1)
	propB.Tick(2, 0.05)

	waitForCondition(t, 10*time.Second, func() bool {
		_, ok := storeB.Get(note.ID)
		return ok
	}, func() string {
		_, ok := storeB.Get(note.ID)
		return fmt.Sprintf("relay B store has note=%v", ok)
	})

	fetched, err := requestEventFromRelay(t, relayBURL, note.ID)
	if err != nil {
		t.Fatalf("requestEventFromRelay relay B: %v", err)
	}
	if fetched.ID != note.ID {
		t.Fatalf("fetched ID = %s, want %s", fetched.ID, note.ID)
	}
	if fetched.Content != note.Content {
		t.Fatalf("fetched content = %q, want %q", fetched.Content, note.Content)
	}
}

func publishToRelay(t *testing.T, url string, event nostr.Event) {
	t.Helper()
	ok, reason, err := publishEventAndReadOK(url, event)
	if err != nil {
		t.Fatalf("publish %s: %v", url, err)
	}
	if !ok {
		t.Fatalf("publish %s rejected: %s", url, reason)
	}
}

func publishEventAndReadOK(url string, event nostr.Event) (bool, string, error) {
	conn, resp, err := dialTestRelayWebsocket(url)
	if err != nil {
		if resp != nil {
			loc := resp.Header.Get("Location")
			if loc != "" {
				return false, "", fmt.Errorf("%w: http %s location=%s", err, resp.Status, loc)
			}
			return false, "", fmt.Errorf("%w: http %s", err, resp.Status)
		}
		return false, "", err
	}
	defer conn.Close()

	payload, _ := json.Marshal([]any{"EVENT", event})
	if err := conn.WriteMessage(websocket.TextMessage, payload); err != nil {
		return false, "", err
	}
	conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			return false, "", err
		}

		var parts []json.RawMessage
		if err := json.Unmarshal(msg, &parts); err != nil || len(parts) == 0 {
			continue
		}

		var label string
		if err := json.Unmarshal(parts[0], &label); err != nil {
			continue
		}
		switch label {
		case "OK":
			var accepted bool
			if len(parts) > 2 {
				_ = json.Unmarshal(parts[2], &accepted)
			}
			var reason string
			if len(parts) > 3 {
				_ = json.Unmarshal(parts[3], &reason)
			}
			return accepted, reason, nil
		case "AUTH":
			return false, "authentication required", nil
		case "NOTICE":
			var reason string
			if len(parts) > 1 {
				_ = json.Unmarshal(parts[1], &reason)
			}
			return false, reason, nil
		}
	}
}

func normalizeLiveRelayURL(raw string) string {
	if raw == "" {
		return ""
	}
	if strings.HasPrefix(raw, "http://") {
		return "ws://" + strings.TrimPrefix(raw, "http://")
	}
	if strings.HasPrefix(raw, "https://") {
		return "wss://" + strings.TrimPrefix(raw, "https://")
	}
	if strings.HasPrefix(raw, "ws://") || strings.HasPrefix(raw, "wss://") {
		return raw
	}
	return "wss://" + raw
}

func liveRelayCandidates(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	switch {
	case strings.HasPrefix(raw, "ws://"), strings.HasPrefix(raw, "wss://"):
		return []string{raw}
	case strings.HasPrefix(raw, "http://"):
		host := strings.TrimPrefix(raw, "http://")
		return []string{"ws://" + host, "wss://" + host}
	case strings.HasPrefix(raw, "https://"):
		host := strings.TrimPrefix(raw, "https://")
		return []string{"wss://" + host, "ws://" + host}
	default:
		return []string{"wss://" + raw, "ws://" + raw}
	}
}

func livePeerCandidates(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}

	normalized := normalizeLiveRelayURL(raw)
	u, err := url.Parse(normalized)
	if err != nil {
		return nil
	}
	host := u.Hostname()
	if host == "" {
		return nil
	}

	candidates := []string{
		"wss://" + net.JoinHostPort(host, "8443"),
		"ws://" + net.JoinHostPort(host, "8443"),
	}
	return candidates
}

func tryPublishToRelay(url string, event nostr.Event) error {
	ok, reason, err := publishEventAndReadOK(url, event)
	if err != nil {
		return err
	}
	if !ok {
		if reason == "" {
			reason = "rejected"
		}
		return fmt.Errorf("%s", reason)
	}
	return nil
}

func readPeerEnvelopeOfType(t *testing.T, conn *websocket.Conn, msgType string, timeout time.Duration) p2p.Envelope {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn.SetReadDeadline(deadline)
		_, msg, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read peer envelope: %v", err)
		}
		var env p2p.Envelope
		if err := json.Unmarshal(msg, &env); err != nil {
			continue
		}
		if env.Type == msgType {
			return env
		}
	}
	t.Fatalf("timeout waiting for peer envelope type %q", msgType)
	return p2p.Envelope{}
}

func dialTestRelayWebsocket(url string) (*websocket.Conn, *http.Response, error) {
	dialer := websocket.Dialer{}
	headers := http.Header{}
	switch {
	case strings.HasPrefix(url, "wss://"):
		headers.Set("Origin", "https://"+strings.TrimPrefix(url, "wss://"))
	case strings.HasPrefix(url, "ws://"):
		headers.Set("Origin", "http://"+strings.TrimPrefix(url, "ws://"))
	case strings.HasPrefix(url, "https://"):
		headers.Set("Origin", url)
	case strings.HasPrefix(url, "http://"):
		headers.Set("Origin", url)
	}
	return dialer.Dial(url, headers)
}
