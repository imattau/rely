package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/nbd-wtf/go-nostr"
	rely "github.com/pippellia-btc/rely/v2"
	"github.com/pippellia-btc/rely/v2/internal/consensus"
	"github.com/pippellia-btc/rely/v2/internal/p2p"
	"github.com/pippellia-btc/rely/v2/internal/quantum"
	"github.com/pippellia-btc/rely/v2/internal/spam"
	"github.com/pippellia-btc/rely/v2/internal/storage"
)

func localRelayURL(listen string) string {
	if len(listen) > 0 && listen[0] == ':' {
		return "ws://localhost" + listen
	}
	return "ws://" + listen
}

func defaultConfig() *Config {
	return &Config{
		Relay: RelayConfig{
			Listen:      ":8080",
			Name:        "Quantum Relay",
			Description: "Nostr relay with quantum walk propagation",
		},
		Peer: PeerConfig{
			Listen:     ":8081",
			PublicPort: 8443,
		},
		Quantum: QuantumConfig{
			Gamma:                0.5,
			FetchThreshold:       0.05,
			ConsensusTickMs:      500,
			QuantumTickMs:        1000,
			MaxConcurrentFetches: 32,
		},
		Spam: SpamConfig{
			ClientEventsPerSec: 10,
			PeerAnnouncePerSec: 100,
		},
		Storage: StorageConfig{
			Path: ":memory:",
		},
		Trust: TrustConfig{
			Enabled: false,
			Weight:  2.0,
		},
	}
}

func main() {
	cfg, err := LoadConfig(configFilePath())
	if err != nil {
		log.Printf("config load failed, using defaults: %v", err)
		cfg = defaultConfig()
	}

	store, closeStore, err := openConfiguredStore(cfg.Storage.Path)
	if err != nil {
		log.Printf("storage init failed: %v", err)
		os.Exit(1)
	}
	defer func() {
		if closeStore != nil {
			_ = closeStore()
		}
	}()
	spamDetector := spam.NewRateLimiter(cfg.Spam.ClientEventsPerSec, cfg.Spam.PeerAnnouncePerSec)

	relayURL := localRelayURL(cfg.Relay.Listen)

	graph := quantum.NewGraphState()
	graph.SetRelays(append([]string{relayURL}, cfg.Peers...))
	for _, peerURL := range cfg.Peers {
		graph.SetConnection(relayURL, peerURL, true)
	}
	graph.Recompute()

	var peerMgr *p2p.PeerManager
	var prop *quantum.Propagator
	diffuser := consensus.NewDiffuser(func(msgType string, payload interface{}) {
		if peerMgr != nil {
			peerMgr.Broadcast(msgType, payload)
		}
	}, func() {
		graph.ScheduleRecompute(250 * time.Millisecond)
	})

	peerMgr = p2p.NewPeerManager(func(peerURL, msgType string, payload json.RawMessage) {
		if !spamDetector.AllowPeer(peerURL) {
			log.Printf("peer rate limited peer=%s type=%s", peerURL, msgType)
			return
		}

		handlePeerMessage(cfg, peerMgr, diffuser, prop, peerURL, msgType, payload)
	})

	fetcher := newQuantumFetcher(relayURL, store, diffuser, cfg.Trust, cfg.Quantum.MaxConcurrentFetches)
	prop = quantum.NewPropagator(graph, graph.GetRelayIndex(relayURL), cfg.Quantum.FetchThreshold, fetcher.Fetch)

	applyTrustWeights(peerMgr, cfg.Trust)

	for _, peerURL := range cfg.Peers {
		if err := peerMgr.Connect(peerURL); err != nil {
			log.Printf("failed to connect to peer=%s: %v", peerURL, err)
		}
	}

	var relayOpts []rely.Option
	if cfg.Auth.Required {
		relayOpts = append(relayOpts, rely.WithAuthURL(relayURL))
	}
	r := rely.NewRelay(relayOpts...)
	fetcher.relay = r
	peerServer := newPeerServer(peerMgr)

	if cfg.Auth.Required {
		r.On.Connect = func(c rely.Client) {
			c.SendAuth()
		}
	}

	r.Reject.Event.Append(func(c rely.Client, event *nostr.Event) error {
		if cfg.Auth.Required && !c.IsAuthed() {
			return fmt.Errorf("auth-required: authentication needed to publish events")
		}

		if !spamDetector.AllowClient(c.UID()) {
			return fmt.Errorf("rate limited")
		}

		if diffuser.GetReputation(event.PubKey) < -0.5 {
			return fmt.Errorf("low reputation pubkey")
		}

		return nil
	})

	r.On.Event = func(c rely.Client, event *nostr.Event) rely.EventResult {
		store.Save(*event)
		applyNIP09Deletion(store, event)
		if event.Kind == 1984 {
			applyKind1984Report(diffuser, event, trustReportWeight(cfg.Trust))
		}
		prop.AddNote(event.ID, relayURL, event.PubKey, diffuser.GetRound())
		log.Printf("broadcasting note_announce note=%s source=%s round=%d", event.ID, relayURL, diffuser.GetRound())
		peerMgr.Broadcast("note_announce", map[string]any{
			"id":     event.ID,
			"source": relayURL,
			"pubkey": event.PubKey,
			"round":  diffuser.GetRound(),
		})
		return rely.Success()
	}

	r.On.Req = func(ctx context.Context, c rely.Client, id string, filters nostr.Filters) ([]nostr.Event, error) {
		_ = ctx
		return store.Query(filters), nil
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	done := ctx.Done()
	go diffuser.Run(cfg.Quantum.ConsensusTick(), done)

	go func() {
		ticker := time.NewTicker(cfg.Quantum.QuantumTick())
		defer ticker.Stop()

		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				prop.Tick(diffuser.GetRound(), cfg.Quantum.Gamma)
			}
		}
	}()

	log.Printf("starting quantum relay listen=%s", cfg.Relay.Listen)
	peerErr := make(chan error, 1)
	go func() {
		if cfg.Peer.Listen == "" {
			peerErr <- nil
			return
		}
		peerErr <- servePeerEndpoint(ctx, peerServer, cfg.Peer.Listen)
	}()
	if err := r.StartAndServe(ctx, cfg.Relay.Listen); err != nil {
		log.Printf("relay stopped: %v", err)
		os.Exit(1)
	}
	if err := <-peerErr; err != nil {
		log.Printf("peer endpoint stopped: %v", err)
		os.Exit(1)
	}
}

func configFilePath() string {
	if path := os.Getenv("RELY_CONFIG"); path != "" {
		return path
	}
	return "configs/config.yaml"
}

func applyNIP09Deletion(store storage.EventStore, event *nostr.Event) {
	if store == nil || event == nil || event.Kind != 5 {
		return
	}

	for _, tag := range event.Tags {
		if len(tag) < 2 || tag[0] != "e" {
			continue
		}

		targetID := tag[1]
		original, ok := store.Get(targetID)
		if !ok || original.PubKey != event.PubKey {
			continue
		}
		store.Delete(targetID)
	}
}

func newPeerServer(pm *p2p.PeerManager) http.Handler {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodGet || req.Header.Get("Upgrade") != "websocket" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		conn, err := upgrader.Upgrade(w, req, nil)
		if err != nil {
			log.Printf("peer upgrade failed: %v", err)
			return
		}

		log.Printf("accepted peer websocket url=%s", peerConnectionURL(req))
		pm.Accept(peerConnectionURL(req), conn)
	})
	return mux
}

func servePeerEndpoint(ctx context.Context, handler http.Handler, listen string) error {
	server := &http.Server{
		Addr:              listen,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Printf("serving peer endpoint address=%s", listen)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return server.Shutdown(shutdownCtx)
	case err := <-errCh:
		return err
	}
}

func peerConnectionURL(req *http.Request) string {
	scheme := "ws"
	if proto := strings.ToLower(req.Header.Get("X-Forwarded-Proto")); proto == "https" || proto == "wss" {
		scheme = "wss"
	}
	host := req.Host
	path := strings.TrimSuffix(req.URL.Path, "/")
	if path == "" || path == "/" {
		return fmt.Sprintf("%s://%s", scheme, host)
	}
	return fmt.Sprintf("%s://%s%s", scheme, host, path)
}

func applyTrustWeights(peerMgr *p2p.PeerManager, trust TrustConfig) {
	if peerMgr == nil || !trust.Enabled {
		return
	}

	for _, url := range trust.Peers {
		peerMgr.SetTrustWeight(url, trust.Weight)
	}
}

func trustReportWeight(trust TrustConfig) float64 {
	if !trust.Enabled {
		return 1
	}
	if trust.Weight <= 0 {
		return 1
	}
	return trust.Weight
}

func applyKind1984Report(diffuser *consensus.Diffuser, event *nostr.Event, weight float64) {
	if diffuser == nil || event == nil {
		return
	}
	if weight <= 0 {
		weight = 1
	}

	for _, tag := range event.Tags {
		if len(tag) < 2 || tag[0] != "p" {
			continue
		}

		reported := tag[1]
		current := diffuser.GetReputation(reported)
		delta := -0.1 * weight
		diffuser.SetReputation(reported, clamp(current+delta, -1, 1))
	}
}

func handlePeerMessage(cfg *Config, peerMgr *p2p.PeerManager, diffuser *consensus.Diffuser, prop *quantum.Propagator, peerURL, msgType string, payload json.RawMessage) {
	switch msgType {
	case "consensus":
		if diffuser == nil {
			return
		}
		var s consensus.State
		if err := json.Unmarshal(payload, &s); err == nil {
			weight := 1.0
			if peerMgr != nil {
				weight = peerMgr.TrustWeight(peerURL)
			}
			diffuser.Enqueue(&s, weight)
		}
	case "note_announce":
		if prop == nil {
			return
		}
		var ann struct {
			ID     string `json:"id"`
			Source string `json:"source"`
			PubKey string `json:"pubkey"`
			Round  int64  `json:"round"`
		}
		if err := json.Unmarshal(payload, &ann); err == nil {
			prop.AddNote(ann.ID, ann.Source, ann.PubKey, ann.Round)
		}
	case "block_peer":
		if cfg == nil || peerMgr == nil || !cfg.Trust.Enabled {
			return
		}
		if peerMgr.TrustWeight(peerURL) <= 1.0 {
			return
		}

		var target string
		if err := json.Unmarshal(payload, &target); err != nil || target == "" {
			return
		}

		log.Printf("trusted peer requested block peer=%s from=%s", target, peerURL)
		peerMgr.Disconnect(target)
		peerMgr.BroadcastToTrusted("block_peer", target)
	}
}

func openConfiguredStore(path string) (storage.EventStore, func() error, error) {
	if path == "" || path == ":memory:" {
		return storage.NewStore(), func() error { return nil }, nil
	}

	sqliteStore, err := storage.NewSQLiteStore(path)
	if err != nil {
		return nil, nil, err
	}
	return sqliteStore, sqliteStore.Close, nil
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

type quantumFetcher struct {
	localRelayURL string
	store         storage.EventStore
	diffuser      *consensus.Diffuser
	trust         TrustConfig
	relay         *rely.Relay

	mu       sync.Mutex
	inFlight map[string]struct{}
	sem      chan struct{}
}

func newQuantumFetcher(localRelayURL string, store storage.EventStore, diffuser *consensus.Diffuser, trust TrustConfig, maxConcurrent int) *quantumFetcher {
	if maxConcurrent <= 0 {
		maxConcurrent = 32
	}
	return &quantumFetcher{
		localRelayURL: localRelayURL,
		store:         store,
		diffuser:      diffuser,
		trust:         trust,
		inFlight:      make(map[string]struct{}),
		sem:           make(chan struct{}, maxConcurrent),
	}
}

func (f *quantumFetcher) Fetch(noteID, sourceRelay string) {
	if f == nil || noteID == "" || sourceRelay == "" {
		return
	}
	if sourceRelay == f.localRelayURL {
		return
	}

	f.mu.Lock()
	if _, ok := f.inFlight[noteID]; ok {
		f.mu.Unlock()
		return
	}
	f.inFlight[noteID] = struct{}{}
	f.mu.Unlock()

	go func() {
		f.sem <- struct{}{}
		defer func() {
			<-f.sem
			f.mu.Lock()
			delete(f.inFlight, noteID)
			f.mu.Unlock()
		}()

		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()

		event, err := fetchEventFromRelay(ctx, sourceRelay, noteID)
		if err != nil {
			log.Printf("quantum fetch failed note=%s source=%s: %v", noteID, sourceRelay, err)
			return
		}
		if event == nil {
			return
		}

		f.store.Save(*event)
		if event.Kind == 1984 {
			applyKind1984Report(f.diffuser, event, trustReportWeight(f.trust))
		}
		if f.relay != nil {
			_ = f.relay.Broadcast(event)
		}
		log.Printf("quantum fetch complete note=%s source=%s kind=%d", noteID, sourceRelay, event.Kind)
	}()
}

func fetchEventFromRelay(ctx context.Context, relayURL, noteID string) (*nostr.Event, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	conn, _, err := dialRelayWebsocket(normalizeRelayURL(relayURL))
	if err != nil {
		return nil, fmt.Errorf("dial relay: %w", err)
	}
	defer conn.Close()

	reqID := noteID
	req := []any{"REQ", reqID, nostr.Filter{IDs: []string{noteID}, Limit: 1}}
	payload, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal req: %w", err)
	}
	if err := conn.WriteMessage(websocket.TextMessage, payload); err != nil {
		return nil, fmt.Errorf("send req: %w", err)
	}

	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(4 * time.Second)
	}
	if err := conn.SetReadDeadline(deadline); err != nil {
		return nil, fmt.Errorf("set read deadline: %w", err)
	}

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		_, msg, err := conn.ReadMessage()
		if err != nil {
			return nil, fmt.Errorf("read response: %w", err)
		}

		var frame []json.RawMessage
		if err := json.Unmarshal(msg, &frame); err != nil {
			continue
		}
		if len(frame) == 0 {
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
			var eventID string
			if err := json.Unmarshal(frame[1], &eventID); err != nil || eventID != reqID {
				continue
			}
			var event nostr.Event
			if err := json.Unmarshal(frame[2], &event); err != nil {
				continue
			}
			if event.ID != noteID {
				continue
			}
			return &event, nil
		case "EOSE", "CLOSED", "NOTICE":
			if len(frame) > 1 {
				var responseID string
				_ = json.Unmarshal(frame[1], &responseID)
				if responseID != "" && responseID != reqID {
					continue
				}
			}
			return nil, fmt.Errorf("source relay returned %s for note %s", label, noteID)
		}
	}
}

func normalizeRelayURL(relayURL string) string {
	if strings.HasPrefix(relayURL, "ws://") || strings.HasPrefix(relayURL, "wss://") {
		return relayURL
	}
	return "ws://" + relayURL
}

func dialRelayWebsocket(relayURL string) (*websocket.Conn, *http.Response, error) {
	dialer := websocket.Dialer{}
	headers := http.Header{}
	switch {
	case strings.HasPrefix(relayURL, "wss://"):
		headers.Set("Origin", "https://"+strings.TrimPrefix(relayURL, "wss://"))
	case strings.HasPrefix(relayURL, "ws://"):
		headers.Set("Origin", "http://"+strings.TrimPrefix(relayURL, "ws://"))
	case strings.HasPrefix(relayURL, "https://"):
		headers.Set("Origin", relayURL)
	case strings.HasPrefix(relayURL, "http://"):
		headers.Set("Origin", relayURL)
	}
	return dialer.Dial(relayURL, headers)
}
