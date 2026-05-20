package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"time"

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
		Quantum: QuantumConfig{
			Gamma:           0.5,
			FetchThreshold:  0.05,
			ConsensusTickMs: 500,
			QuantumTickMs:   1000,
		},
		Spam: SpamConfig{
			ClientEventsPerSec: 10,
			PeerAnnouncePerSec: 100,
		},
	}
}

func main() {
	cfg, err := LoadConfig("configs/config.yaml")
	if err != nil {
		log.Printf("config load failed, using defaults: %v", err)
		cfg = defaultConfig()
	}

	store := storage.NewStore()
	spamDetector := spam.NewRateLimiter(cfg.Spam.ClientEventsPerSec, cfg.Spam.PeerAnnouncePerSec)

	relayURL := localRelayURL(cfg.Relay.Listen)

	graph := quantum.NewGraphState()
	graph.SetRelays(append([]string{relayURL}, cfg.Peers...))
	for _, peerURL := range cfg.Peers {
		graph.SetConnection(relayURL, peerURL, true)
	}
	graph.Recompute()

	var peerMgr *p2p.PeerManager
	diffuser := consensus.NewDiffuser(func(msgType string, payload interface{}) {
		if peerMgr != nil {
			peerMgr.Broadcast(msgType, payload)
		}
	}, func() {
		graph.ScheduleRecompute(250 * time.Millisecond)
	})

	prop := quantum.NewPropagator(graph, graph.GetRelayIndex(relayURL), cfg.Quantum.FetchThreshold, func(noteID, sourceRelay string) {
		log.Printf("quantum fetch triggered note=%s from=%s", noteID, sourceRelay)
	})

	peerMgr = p2p.NewPeerManager(func(peerURL, msgType string, payload json.RawMessage) {
		if !spamDetector.AllowPeer(peerURL) {
			log.Printf("peer rate limited peer=%s type=%s", peerURL, msgType)
			return
		}

		switch msgType {
		case "consensus":
			var s consensus.State
			if err := json.Unmarshal(payload, &s); err == nil {
				diffuser.Enqueue(&s)
			}
		case "note_announce":
			var ann struct {
				ID     string `json:"id"`
				Source string `json:"source"`
				PubKey string `json:"pubkey"`
				Round  int64  `json:"round"`
			}
			if err := json.Unmarshal(payload, &ann); err == nil {
				prop.AddNote(ann.ID, ann.Source, ann.PubKey, ann.Round)
			}
		}
	})

	for _, peerURL := range cfg.Peers {
		if err := peerMgr.Connect(peerURL); err != nil {
			log.Printf("failed to connect to peer=%s: %v", peerURL, err)
		}
	}

	r := rely.NewRelay()

	r.Reject.Event.Append(func(c rely.Client, event *nostr.Event) error {
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
		prop.AddNote(event.ID, relayURL, event.PubKey, diffuser.GetRound())
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
	if err := r.StartAndServe(ctx, cfg.Relay.Listen); err != nil {
		log.Printf("relay stopped: %v", err)
		os.Exit(1)
	}
}
