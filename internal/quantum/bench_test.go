package quantum_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/pippellia-btc/rely/v2/internal/quantum"
)

func BenchmarkPropagatorTick100Notes(b *testing.B)  { benchmarkPropagatorTick(b, 100, 10) }
func BenchmarkPropagatorTick1000Notes(b *testing.B) { benchmarkPropagatorTick(b, 1000, 10) }
func BenchmarkPropagatorTick5000Notes(b *testing.B) { benchmarkPropagatorTick(b, 5000, 10) }
func BenchmarkPropagatorTickSparse256Notes(b *testing.B) {
	benchmarkPropagatorTick(b, 1000, 256)
}
func BenchmarkPropagatorTickSharedSource(b *testing.B) {
	benchmarkPropagatorTickPattern(b, 1000, 64, 1, 1)
}

func BenchmarkPropagatorTickMixedSources(b *testing.B) {
	benchmarkPropagatorTickPattern(b, 1000, 64, 16, 8)
}

func BenchmarkGraphRecomputeSmall(b *testing.B)  { benchmarkGraphRecompute(b, 16, true) }
func BenchmarkGraphRecomputeMedium(b *testing.B) { benchmarkGraphRecompute(b, 64, true) }
func BenchmarkGraphRecomputeSparse(b *testing.B) { benchmarkGraphRecompute(b, 256, false) }
func BenchmarkGraphScheduleRecompute(b *testing.B) {
	g := quantum.NewGraphState()
	relays := makeRelaySet(64)
	g.SetRelays(relays)
	for i := 0; i < len(relays)-1; i++ {
		g.SetConnection(relays[i], relays[i+1], true)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		g.ScheduleRecompute(time.Hour)
	}
}

func BenchmarkQuantumChurn(b *testing.B) {
	g := quantum.NewGraphState()
	relays := makeRelaySet(32)
	g.SetRelays(relays)
	for i := 0; i < len(relays)-1; i++ {
		g.SetConnection(relays[i], relays[i+1], true)
	}
	g.Recompute()

	p := quantum.NewPropagator(g, 0, 2.0, func(_, _ string) {})
	for i := 0; i < 1000; i++ {
		p.AddNote(fmt.Sprintf("note%d", i), relays[len(relays)-1], "pk", 0)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		idx := i % (len(relays) - 1)
		g.SetConnection(relays[idx], relays[idx+1], i%2 == 0)
		g.Recompute()
		p.Tick(int64(i%32), 0.5)
	}
}

func benchmarkPropagatorTick(b *testing.B, noteCount, graphSize int) {
	benchmarkPropagatorTickPattern(b, noteCount, graphSize, 1, 1)
}

func benchmarkPropagatorTickPattern(b *testing.B, noteCount, graphSize, uniqueSources, uniqueRounds int) {
	g := quantum.NewGraphState()
	relays := makeRelaySet(graphSize)
	g.SetRelays(relays)
	for i := 0; i < len(relays)-1; i++ {
		g.SetConnection(relays[i], relays[i+1], true)
	}
	g.Recompute()

	p := quantum.NewPropagator(g, 0, 2.0, func(_, _ string) {})
	for i := 0; i < noteCount; i++ {
		source := relays[len(relays)-1]
		if uniqueSources > 1 {
			source = relays[i%uniqueSources]
		}
		bornRound := int64(0)
		if uniqueRounds > 1 {
			bornRound = int64(i % uniqueRounds)
		}
		p.AddNote(fmt.Sprintf("note%d", i), source, "pk", bornRound)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		p.Tick(10, 0.5)
	}
}

func benchmarkGraphRecompute(b *testing.B, size int, dense bool) {
	g := quantum.NewGraphState()
	relays := makeRelaySet(size)
	g.SetRelays(relays)

	if dense {
		for i := 0; i < len(relays); i++ {
			for j := i + 1; j < len(relays); j++ {
				if (i+j)%3 == 0 {
					g.SetConnection(relays[i], relays[j], true)
				}
			}
		}
	} else {
		for i := 0; i < len(relays)-1; i++ {
			g.SetConnection(relays[i], relays[i+1], true)
		}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		g.Recompute()
	}
}

func makeRelaySet(n int) []string {
	relays := make([]string, n)
	for i := range relays {
		relays[i] = fmt.Sprintf("relay%d", i)
	}
	return relays
}
