package quantum_test

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/pippellia-btc/rely/v2/internal/quantum"
)

func TestThreeRelayPropagation(t *testing.T) {
	g := quantum.NewGraphState()
	g.SetRelays([]string{"r0", "r1", "r2"})
	g.SetConnection("r0", "r1", true)
	g.SetConnection("r1", "r2", true)
	g.Recompute()

	var fetchCount int32
	p := quantum.NewPropagator(g, 1, 0.01, func(_, _ string) {
		atomic.AddInt32(&fetchCount, 1)
	})
	p.AddNote("note42", "r0", "pubkey", 0)

	triggered := false
	for round := int64(0); round < 200; round++ {
		p.Tick(round, 0)
		if atomic.LoadInt32(&fetchCount) > 0 {
			triggered = true
			break
		}
	}

	if !triggered {
		t.Error("expected fetch to trigger within 200 rounds on a 3-node path graph")
	}
}

func TestSparsePropagationRegression(t *testing.T) {
	g := quantum.NewGraphState()
	relays := []string{"r0", "r1", "r2", "r3", "r4", "r5"}
	g.SetRelays(relays)
	for i := 0; i < len(relays)-1; i++ {
		g.SetConnection(relays[i], relays[i+1], true)
	}
	g.Recompute()

	var fetchCount int32
	p := quantum.NewPropagator(g, 3, 0.01, func(_, _ string) {
		atomic.AddInt32(&fetchCount, 1)
	})
	p.SetRetention(128, 50)
	p.AddNote("regression-note", relays[0], "pubkey", 0)

	start := time.Now()
	for round := int64(0); round < 200; round++ {
		p.Tick(round, 0.25)
		if atomic.LoadInt32(&fetchCount) > 0 {
			return
		}
	}

	t.Fatalf("expected sparse propagation to trigger within 200 rounds, elapsed=%s", time.Since(start))
}
