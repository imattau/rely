package consensus_test

import (
	"math"
	"testing"
	"time"

	"github.com/pippellia-btc/rely/v2/internal/consensus"
)

func TestAverageReputation(t *testing.T) {
	d := consensus.NewDiffuser(nil, nil)
	d.SetReputation("alice", 0.6)

	neighbour := &consensus.State{
		Rep: map[string]float64{"alice": -0.2},
	}
	d.MergeState(neighbour)

	rep := d.GetReputation("alice")
	if rep < 0.19 || rep > 0.21 {
		t.Errorf("merged rep = %f, want ~0.2", rep)
	}
}

func TestAverageRound(t *testing.T) {
	d := consensus.NewDiffuser(nil, nil)
	d.SetRound(10)
	neighbour := &consensus.State{Round: 14}
	d.MergeState(neighbour)
	if d.GetRound() != 12 {
		t.Errorf("merged round = %d, want 12", d.GetRound())
	}
}

func TestSnapshotReturnsDeltaOnly(t *testing.T) {
	d := consensus.NewDiffuser(nil, nil)
	d.SetReputation("alice", 0.4)

	first := d.Snapshot()
	if len(first.Rep) != 1 {
		t.Fatalf("first snapshot size = %d, want 1", len(first.Rep))
	}
	if _, ok := first.Rep["alice"]; !ok {
		t.Fatal("first snapshot should include alice")
	}

	second := d.Snapshot()
	if len(second.Rep) != 1 {
		t.Fatalf("snapshot should remain stable until a broadcast clears dirty state, got %d", len(second.Rep))
	}
}

func TestMergeStateWeighted(t *testing.T) {
	d := consensus.NewDiffuser(nil, nil)
	d.SetReputation("alice", 0.0)

	neighbour := &consensus.State{
		Round: 10,
		Rep:   map[string]float64{"alice": 1.0},
	}
	d.MergeState(neighbour, 2.0)

	got := d.GetReputation("alice")
	expected := 2.0 / 3.0
	if math.Abs(got-expected) > 1e-9 {
		t.Fatalf("expected %v, got %v", expected, got)
	}
}

func TestEnqueueWeighted(t *testing.T) {
	d := consensus.NewDiffuser(nil, nil)
	d.SetReputation("bob", 0.0)

	done := make(chan struct{})
	go d.Run(10*time.Millisecond, done)

	d.Enqueue(&consensus.State{
		Rep: map[string]float64{"bob": 0.9},
	}, 3.0)

	time.Sleep(50 * time.Millisecond)
	close(done)

	got := d.GetReputation("bob")
	expected := 0.675
	if math.Abs(got-expected) > 1e-9 {
		t.Fatalf("expected %v, got %v", expected, got)
	}
}
