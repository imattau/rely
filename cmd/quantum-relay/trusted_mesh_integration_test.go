package main

import (
	"context"
	"encoding/json"
	"fmt"
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

type noteAnnouncement struct {
	ID     string `json:"id"`
	Source string `json:"source"`
	PubKey string `json:"pubkey"`
	Round  int64  `json:"round"`
}

func TestTrustedMeshEventPropagation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	nodeA := newTrustRelayNode(t, ctx, "node-a", "", 10.0)
	nodeB := newTrustRelayNode(t, ctx, "node-b", nodeA.peerID, 10.0)

	if err := nodeA.peerMgr.Connect(nodeB.peerSrv.URL()); err != nil {
		t.Fatalf("connect node A -> node B: %v", err)
	}

	nodeB.peerSrv.ExpectConnected(t)

	reportedPubKey := "deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef"
	trustedReport := nostr.Event{
		CreatedAt: nostr.Now(),
		Kind:      1984,
		PubKey:    "trusted-reporter",
		Tags: nostr.Tags{
			{"p", reportedPubKey},
		},
		Content: "spam report",
	}
	if err := trustedReport.Sign(nostr.GeneratePrivateKey()); err != nil {
		t.Fatalf("sign trusted report: %v", err)
	}

	accepted, reason := sendEventAndReadOK(t, nodeA.relayURL, trustedReport)
	if !accepted {
		t.Fatalf("expected trusted report to be accepted, reason=%q", reason)
	}

	waitForCondition(t, 3*time.Second, func() bool {
		return nodeB.diff.GetReputation(reportedPubKey) < -0.5
	}, func() string {
		return fmt.Sprintf("nodeB reputation for %s = %v", reportedPubKey, nodeB.diff.GetReputation(reportedPubKey))
	})

	suppressed := nostr.Event{
		CreatedAt: nostr.Now(),
		Kind:      1,
		PubKey:    reportedPubKey,
		Content:   "should be rejected by reputation",
	}
	if err := suppressed.Sign(nostr.GeneratePrivateKey()); err != nil {
		t.Fatalf("sign suppressed event: %v", err)
	}

	accepted, reason = sendEventAndReadOK(t, nodeB.relayURL, suppressed)
	if accepted {
		t.Fatal("expected low-reputation event to be rejected")
	}
	if reason == "" {
		t.Fatal("expected rejection reason")
	}
	if _, ok := nodeB.store.Get(suppressed.ID); ok {
		t.Fatal("rejected event should not be stored")
	}
}

func TestTrustedMeshClientCanReadFetchedNote(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	storeA := storage.NewStore()
	storeB := storage.NewStore()
	var peerMgrA *p2p.PeerManager
	var relayAURL string
	var relayALn, relayBLn net.Listener

	relayA := rely.NewRelay()
	relayA.Reject.Connection.Clear()
	relayA.Reject.Event.Clear()
	relayA.On.Event = func(c rely.Client, event *nostr.Event) rely.EventResult {
		storeA.Save(*event)
		peerMgrA.Broadcast("note_announce", noteAnnouncement{
			ID:     event.ID,
			Source: "ws://" + relayALn.Addr().String(),
			PubKey: event.PubKey,
			Round:  0,
		})
		return rely.Success()
	}
	relayA.On.Req = func(ctx context.Context, c rely.Client, id string, filters nostr.Filters) ([]nostr.Event, error) {
		_ = ctx
		_ = c
		_ = id
		return storeA.Query(filters), nil
	}

	relayB := rely.NewRelay()
	relayB.Reject.Connection.Clear()
	relayB.Reject.Event.Clear()
	relayB.On.Event = func(c rely.Client, event *nostr.Event) rely.EventResult {
		storeB.Save(*event)
		return rely.Success()
	}
	relayB.On.Req = func(ctx context.Context, c rely.Client, id string, filters nostr.Filters) ([]nostr.Event, error) {
		_ = ctx
		_ = c
		_ = id
		return storeB.Query(filters), nil
	}

	var err error
	if relayALn, err = net.Listen("tcp", "127.0.0.1:0"); err != nil {
		t.Fatalf("listen relay A: %v", err)
	}
	defer relayALn.Close()
	if relayBLn, err = net.Listen("tcp", "127.0.0.1:0"); err != nil {
		t.Fatalf("listen relay B: %v", err)
	}
	defer relayBLn.Close()

	relayAURL = "ws://" + relayALn.Addr().String()
	relayBURL := "ws://" + relayBLn.Addr().String()

	peerMgrA = p2p.NewPeerManager(nil)
	peerServerB := newTrustPeerServer(t, "peer://relay-a", func(peerURL, msgType string, payload json.RawMessage) {
		_ = peerURL
		if msgType != "note_announce" {
			return
		}

		var ann noteAnnouncement
		if err := json.Unmarshal(payload, &ann); err != nil {
			return
		}

		fetched, err := fetchEventFromRelay(ctx, ann.Source, ann.ID)
		if err != nil || fetched == nil {
			return
		}
		storeB.Save(*fetched)
	})
	defer peerServerB.Close()

	if err := peerMgrA.Connect(peerServerB.URL()); err != nil {
		t.Fatalf("connect relay A peer to relay B peer server: %v", err)
	}
	peerServerB.ExpectConnected(t)

	go relayA.Start(ctx)
	go relayB.Start(ctx)
	go func() { _ = (&http.Server{Handler: relayA}).Serve(relayALn) }()
	go func() { _ = (&http.Server{Handler: relayB}).Serve(relayBLn) }()

	note := nostr.Event{
		CreatedAt: nostr.Now(),
		Kind:      1,
		PubKey:    "client-a-pubkey",
		Content:   "client A to relay A",
	}
	if err := note.Sign(nostr.GeneratePrivateKey()); err != nil {
		t.Fatalf("sign note: %v", err)
	}

	accepted, reason := sendEventAndReadOK(t, relayAURL, note)
	if !accepted {
		t.Fatalf("expected relay A to accept note, reason=%q", reason)
	}

	waitForCondition(t, 3*time.Second, func() bool {
		_, ok := storeB.Get(note.ID)
		return ok
	}, func() string {
		_, ok := storeB.Get(note.ID)
		return fmt.Sprintf("storeB has note=%v", ok)
	})

	fetched, err := requestEventFromRelay(t, relayBURL, note.ID)
	if err != nil {
		t.Fatalf("requestEventFromRelay: %v", err)
	}
	if fetched.ID != note.ID {
		t.Fatalf("client B fetched ID = %s, want %s", fetched.ID, note.ID)
	}
	if fetched.Content != note.Content {
		t.Fatalf("client B fetched content = %q, want %q", fetched.Content, note.Content)
	}
}

type trustRelayNode struct {
	name     string
	peerID   string
	relay    *rely.Relay
	store    *storage.Store
	diff     *consensus.Diffuser
	peerMgr  *p2p.PeerManager
	peerSrv  *trustPeerServer
	relayLn  net.Listener
	relayURL string
	ctx      context.Context
	cancel   context.CancelFunc
}

func newTrustRelayNode(t *testing.T, parent context.Context, name, trustedSenderID string, trustWeight float64) *trustRelayNode {
	t.Helper()

	ctx, cancel := context.WithCancel(parent)
	store := storage.NewStore()
	relay := rely.NewRelay()
	relay.Reject.Connection.Clear()
	relay.Reject.Event.Clear()

	node := &trustRelayNode{
		name:   name,
		peerID: "peer://" + name,
		relay:  relay,
		store:  store,
		ctx:    ctx,
		cancel: cancel,
	}

	var peerMgr *p2p.PeerManager
	diff := consensus.NewDiffuser(func(msgType string, payload interface{}) {
		if peerMgr != nil {
			peerMgr.Broadcast(msgType, payload)
		}
	}, nil)
	peerMgr = p2p.NewPeerManager(nil)
	peerSrv := newTrustPeerServer(t, trustedSenderID, func(peerURL, msgType string, payload json.RawMessage) {
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

	relay.On.Event = func(c rely.Client, event *nostr.Event) rely.EventResult {
		store.Save(*event)
		if event.Kind == 1984 {
			applyKind1984Report(diff, event, trustWeight)
		}
		return rely.Success()
	}
	relay.Reject.Event.Append(func(c rely.Client, event *nostr.Event) error {
		if diff.GetReputation(event.PubKey) < -0.5 {
			return fmt.Errorf("low reputation pubkey")
		}
		return nil
	})
	relay.On.Req = func(ctx context.Context, c rely.Client, id string, filters nostr.Filters) ([]nostr.Event, error) {
		_ = ctx
		return store.Query(filters), nil
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen relay %s: %v", name, err)
	}

	node.peerMgr = peerMgr
	node.diff = diff
	node.peerSrv = peerSrv
	node.relayLn = ln
	node.relayURL = "ws://" + ln.Addr().String()
	if trustedSenderID != "" {
		peerMgr.SetTrustWeight(trustedSenderID, trustWeight)
	}

	go relay.Start(ctx)
	go node.diff.Run(20*time.Millisecond, ctx.Done())
	go func() {
		server := &http.Server{Handler: relay}
		_ = server.Serve(ln)
	}()

	return node
}

func (n *trustRelayNode) Close() {
	n.cancel()
	if n.relayLn != nil {
		_ = n.relayLn.Close()
	}
	if n.peerSrv != nil {
		n.peerSrv.Close()
	}
}

type trustPeerServer struct {
	url       string
	connected chan struct{}
	server    *http.Server
	ln        net.Listener
}

func newTrustPeerServer(t *testing.T, senderID string, handle func(peerURL, msgType string, payload json.RawMessage)) *trustPeerServer {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen peer server: %v", err)
	}

	srv := &trustPeerServer{
		url:       "ws://" + ln.Addr().String(),
		connected: make(chan struct{}),
		ln:        ln,
	}

	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			conn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				return
			}
			select {
			case <-srv.connected:
			default:
				close(srv.connected)
			}
			go func() {
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
					handle(senderID, env.Type, env.Payload)
				}
			}()
		}),
	}
	srv.server = server
	go func() {
		_ = server.Serve(ln)
	}()
	return srv
}

func (s *trustPeerServer) URL() string { return s.url }

func (s *trustPeerServer) ExpectConnected(t *testing.T) {
	t.Helper()

	select {
	case <-s.connected:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for trusted peer connection")
	}
}

func (s *trustPeerServer) Close() {
	if s.server != nil {
		_ = s.server.Close()
	}
	if s.ln != nil {
		_ = s.ln.Close()
	}
}

func sendEventAndReadOK(t *testing.T, addr string, event nostr.Event) (bool, string) {
	t.Helper()

	conn, _, err := websocket.DefaultDialer.Dial(addr, nil)
	if err != nil {
		t.Fatalf("dial relay %s: %v", addr, err)
	}
	defer conn.Close()

	payload, err := json.Marshal([]any{"EVENT", event})
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	if err := conn.WriteMessage(websocket.TextMessage, payload); err != nil {
		t.Fatalf("write event: %v", err)
	}

	_, msg, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read relay response: %v", err)
	}

	var resp []json.RawMessage
	if err := json.Unmarshal(msg, &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(resp) < 4 {
		t.Fatalf("unexpected response payload: %s", string(msg))
	}
	var label string
	if err := json.Unmarshal(resp[0], &label); err != nil {
		t.Fatalf("unmarshal response label: %v", err)
	}
	if label != "OK" {
		t.Fatalf("unexpected response label %q: %s", label, string(msg))
	}
	var accepted bool
	if err := json.Unmarshal(resp[2], &accepted); err != nil {
		t.Fatalf("unmarshal accepted flag: %v", err)
	}
	var reason string
	_ = json.Unmarshal(resp[3], &reason)
	return accepted, reason
}

func requestEventFromRelay(t *testing.T, addr, eventID string) (*nostr.Event, error) {
	t.Helper()

	conn, _, err := websocket.DefaultDialer.Dial(addr, nil)
	if err != nil {
		return nil, fmt.Errorf("dial relay %s: %w", addr, err)
	}
	defer conn.Close()

	reqID := eventID
	payload, err := json.Marshal([]any{"REQ", reqID, nostr.Filter{IDs: []string{eventID}, Limit: 1}})
	if err != nil {
		return nil, fmt.Errorf("marshal req: %w", err)
	}
	if err := conn.WriteMessage(websocket.TextMessage, payload); err != nil {
		return nil, fmt.Errorf("write req: %w", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for {
		if err := conn.SetReadDeadline(deadline); err != nil {
			return nil, fmt.Errorf("set read deadline: %w", err)
		}

		_, msg, err := conn.ReadMessage()
		if err != nil {
			return nil, fmt.Errorf("read req response: %w", err)
		}

		var frame []json.RawMessage
		if err := json.Unmarshal(msg, &frame); err != nil || len(frame) == 0 {
			continue
		}

		var label string
		if err := json.Unmarshal(frame[0], &label); err != nil {
			continue
		}

		switch label {
		case "EVENT":
			if len(frame) < 3 {
				continue
			}
			var id string
			if err := json.Unmarshal(frame[1], &id); err != nil || id != reqID {
				continue
			}
			var event nostr.Event
			if err := json.Unmarshal(frame[2], &event); err != nil {
				return nil, fmt.Errorf("unmarshal event: %w", err)
			}
			return &event, nil
		case "EOSE", "CLOSED", "NOTICE":
			return nil, fmt.Errorf("unexpected response label %s: %s", label, string(msg))
		}
	}
}
