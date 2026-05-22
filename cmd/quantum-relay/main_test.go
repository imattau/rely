package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net"
	"net/http"
	"os"
	"strings"
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

func TestConfigFilePathFromEnv(t *testing.T) {
	t.Setenv("RELY_CONFIG", "/etc/rely/config.yaml")
	if got := configFilePath(); got != "/etc/rely/config.yaml" {
		t.Fatalf("expected env config path, got %q", got)
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

	_, msg, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read auth challenge: %v", err)
	}
	var authParts []json.RawMessage
	if err := json.Unmarshal(msg, &authParts); err != nil {
		t.Fatalf("unmarshal auth challenge: %v", err)
	}
	if len(authParts) == 0 {
		t.Fatal("expected auth challenge response")
	}
	var label string
	if err := json.Unmarshal(authParts[0], &label); err != nil || label != "AUTH" {
		t.Fatalf("expected AUTH challenge, got %s", string(msg))
	}

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

	_, msg, err = conn.ReadMessage()
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

func TestPeerEndpointBroadcastsNoteAnnounce(t *testing.T) {
	peerMgr := p2p.NewPeerManager(nil)
	server := newPeerServer(peerMgr)

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

	peerMgr.Broadcast("note_announce", map[string]any{
		"id":     "abc",
		"source": "ws://source.example.com",
		"pubkey": "pubkey",
		"round":  7,
	})

	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, msg, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read broadcast: %v", err)
	}

	var env struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(msg, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	if env.Type != "note_announce" {
		t.Fatalf("unexpected envelope type %q", env.Type)
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
