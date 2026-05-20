package quantum_test

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/pippellia-btc/rely/v2/internal/quantum"
)

func TestPropagatorFetchesAboveThreshold(t *testing.T) {
	g := quantum.NewGraphState()
	g.SetRelays([]string{"local"})
	g.Recompute()

	var fetched int32
	p := quantum.NewPropagator(g, 0, 0.5, func(noteID, sourceRelay string) {
		atomic.StoreInt32(&fetched, 1)
	})
	p.AddNote("note1", "local", "pubkey1", 0)
	p.Tick(0, 0)

	time.Sleep(50 * time.Millisecond)
	if atomic.LoadInt32(&fetched) == 0 {
		t.Error("expected fetch to be triggered")
	}
}

func TestPropagatorSkipsBelowThreshold(t *testing.T) {
	g := quantum.NewGraphState()
	g.SetRelays([]string{"local", "remote"})
	g.SetConnection("local", "remote", true)
	g.Recompute()

	var fetched int32
	p := quantum.NewPropagator(g, 0, 0.99, func(_, _ string) {
		atomic.StoreInt32(&fetched, 1)
	})
	p.AddNote("note2", "remote", "pk2", 0)
	p.Tick(0, 0)

	time.Sleep(50 * time.Millisecond)
	if atomic.LoadInt32(&fetched) != 0 {
		t.Error("should not fetch at t=0 from remote with threshold 0.99")
	}
}

func TestPropagatorPrunesOldNotes(t *testing.T) {
	g := quantum.NewGraphState()
	g.SetRelays([]string{"local"})
	g.Recompute()

	p := quantum.NewPropagator(g, 0, 0.5, nil)
	p.SetRetention(2, 5)

	p.AddNote("n1", "local", "pk1", 0)
	p.AddNote("n2", "local", "pk2", 1)
	p.AddNote("n3", "local", "pk3", 2)

	if p.ActiveCount() != 2 {
		t.Fatalf("active count = %d, want 2 after cap eviction", p.ActiveCount())
	}
	if p.HasNote("n1") {
		t.Fatal("oldest note should have been evicted")
	}
	if !p.HasNote("n2") || !p.HasNote("n3") {
		t.Fatal("newer notes should remain active")
	}

	p.Tick(10, 0)
	if p.ActiveCount() != 0 {
		t.Fatalf("active count = %d, want 0 after TTL pruning", p.ActiveCount())
	}
}
