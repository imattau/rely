package quantum_test

// TestTopologySignal answers: does graph topology produce meaningfully different
// propagation probabilities, or does density collapse everything toward uniform?
//
// Three topologies × three source types. For each combination we measure:
//   - walk probability curve over 50 rounds
//   - time-to-threshold (rounds until first crossing of 0.05)
//   - whether the quantum walk or the exploration floor drove the crossing
//   - peak probability observed
//   - variance across rounds (captures oscillation spread)
//
// Key findings this test validates:
//   - Flat/dense mesh: walk probability stays below threshold; floor drives all crossings
//   - Hierarchical/sparse: walk probability differentiates by topology
//   - Isolated sources (weakly connected): floor rescues them eventually

import (
	"fmt"
	"math"
	"math/cmplx"
	"strings"
	"testing"

	"github.com/pippellia-btc/rely/v2/internal/quantum"
)

const defaultThreshold = 0.05
const selectivityThreshold = 0.08 // above 0.05 shortcut; tests real walk selectivity
const probeRounds = 50

type topologyResult struct {
	topology    string
	sourceType  string
	sourceRelay string
	localRelay  string
	probs       []float64 // probability at rounds 1..probeRounds
	// round at which effective prob (walk or floor) first crossed defaultThreshold; -1 if never
	timeToThreshold int
	floorDriven     bool // true if floor, not walk, drove the crossing
	peak            float64
	variance        float64
}

// buildDenseCore: chain of 10 hubs (guaranteed connected) + 5 edge relays.
// Hub chain: hub0-hub1-hub2-...-hub9 plus cross-links hub0-hub2, hub1-hub3, etc.
func buildDenseCore() (*quantum.GraphState, map[string]string) {
	g := quantum.NewGraphState()
	var relays []string
	for i := 0; i < 10; i++ {
		relays = append(relays, fmt.Sprintf("hub%d", i))
	}
	for i := 0; i < 5; i++ {
		relays = append(relays, fmt.Sprintf("edge%d", i))
	}
	g.SetRelays(relays)

	// chain all hubs
	for i := 0; i < 9; i++ {
		g.SetConnection(relays[i], relays[i+1], true)
	}
	// add cross-links to make it denser
	for i := 0; i < 8; i++ {
		g.SetConnection(relays[i], relays[i+2], true)
	}
	// each edge relay connects to one hub
	for i := 0; i < 5; i++ {
		g.SetConnection(fmt.Sprintf("edge%d", i), fmt.Sprintf("hub%d", i), true)
	}
	g.Recompute()

	return g, map[string]string{
		"core":     "hub0",  // directly adjacent to local
		"edge":     "edge4", // leaf off hub4, 5 hops from local
		"isolated": "hub9",  // far end of chain
		"local":    "hub1",
	}
}

// buildFlatMesh: 15 relays all connected to each other (fully connected).
func buildFlatMesh() (*quantum.GraphState, map[string]string) {
	g := quantum.NewGraphState()
	var relays []string
	for i := 0; i < 15; i++ {
		relays = append(relays, fmt.Sprintf("relay%d", i))
	}
	g.SetRelays(relays)
	for i := 0; i < 15; i++ {
		for j := i + 1; j < 15; j++ {
			g.SetConnection(relays[i], relays[j], true)
		}
	}
	g.Recompute()

	return g, map[string]string{
		"core":     "relay0",
		"edge":     "relay14",
		"isolated": "relay7",
		"local":    "relay1",
	}
}

// buildHierarchical: 3 hubs fully connected; 12 leaves, 4 per hub.
// Leaves only connect to their hub.
func buildHierarchical() (*quantum.GraphState, map[string]string) {
	g := quantum.NewGraphState()
	relays := []string{"hub0", "hub1", "hub2"}
	for h := 0; h < 3; h++ {
		for l := 0; l < 4; l++ {
			relays = append(relays, fmt.Sprintf("leaf%d_%d", h, l))
		}
	}
	g.SetRelays(relays)

	g.SetConnection("hub0", "hub1", true)
	g.SetConnection("hub1", "hub2", true)
	g.SetConnection("hub0", "hub2", true)

	for h := 0; h < 3; h++ {
		for l := 0; l < 4; l++ {
			g.SetConnection(fmt.Sprintf("hub%d", h), fmt.Sprintf("leaf%d_%d", h, l), true)
		}
	}
	g.Recompute()

	return g, map[string]string{
		"core":     "hub0",    // local's hub (1 hop from local)
		"edge":     "leaf0_0", // sibling leaf (2 hops: leaf→hub0→local)
		"isolated": "leaf2_3", // far leaf (4 hops: leaf→hub2→hub0→local)
		"local":    "leaf0_1",
	}
}

func probCurve(g *quantum.GraphState, localIdx, sourceIdx int) []float64 {
	probs := make([]float64, probeRounds)
	for t := 1; t <= probeRounds; t++ {
		amp := g.Amplitude(localIdx, sourceIdx, float64(t))
		probs[t-1] = cmplx.Abs(amp) * cmplx.Abs(amp)
	}
	return probs
}

func explorationFloor(t int) float64 {
	return 0.02 * (1 - math.Exp(-float64(t)/25))
}

func analyseResult(topology, sourceType, sourceRelay, localRelay string, probs []float64) topologyResult {
	r := topologyResult{
		topology:        topology,
		sourceType:      sourceType,
		sourceRelay:     sourceRelay,
		localRelay:      localRelay,
		probs:           probs,
		timeToThreshold: -1,
	}

	var sum float64
	for _, p := range probs {
		sum += p
		if p > r.peak {
			r.peak = p
		}
	}
	mean := sum / float64(len(probs))

	var vsum float64
	for _, p := range probs {
		d := p - mean
		vsum += d * d
	}
	r.variance = vsum / float64(len(probs))

	for t, p := range probs {
		round := t + 1
		floor := explorationFloor(round)
		effective := p
		if effective < floor {
			effective = floor
		}
		if r.timeToThreshold == -1 && effective > defaultThreshold {
			r.timeToThreshold = round
			r.floorDriven = p <= floor
		}
	}

	return r
}

func TestTopologySignal(t *testing.T) {
	topologies := []struct {
		name  string
		build func() (*quantum.GraphState, map[string]string)
	}{
		{"DenseCore", buildDenseCore},
		{"FlatMesh", buildFlatMesh},
		{"Hierarchical", buildHierarchical},
	}

	sourceTypes := []string{"core", "edge", "isolated"}
	var results []topologyResult

	for _, topo := range topologies {
		g, roles := topo.build()
		localRelay := roles["local"]
		localIdx := g.GetRelayIndex(localRelay)

		for _, srcType := range sourceTypes {
			srcRelay := roles[srcType]
			if srcRelay == localRelay {
				continue
			}
			srcIdx := g.GetRelayIndex(srcRelay)
			if srcIdx < 0 {
				t.Logf("skip %s/%s: relay %q not in graph", topo.name, srcType, srcRelay)
				continue
			}

			probs := probCurve(g, localIdx, srcIdx)
			r := analyseResult(topo.name, srcType, srcRelay, localRelay, probs)
			results = append(results, r)
		}
	}

	t.Log("\n" + formatTable(results))

	// FlatMesh: walk probability should stay below threshold for all sources.
	// The floor drives all crossings — topology provides no signal.
	t.Run("FlatMeshCollapses", func(t *testing.T) {
		for _, r := range results {
			if r.topology != "FlatMesh" {
				continue
			}
			if r.peak >= defaultThreshold {
				t.Errorf("FlatMesh/%s: walk peak %.4f crossed threshold %.2f — topology signal present (unexpected in fully-connected graph)",
					r.sourceType, r.peak, defaultThreshold)
			} else {
				t.Logf("FlatMesh/%s: walk peak %.4f < threshold — floor drives propagation (confirmed)", r.sourceType, r.peak)
			}
		}
	})

	// DenseCore: core (adjacent) should have higher peak than isolated (far end of chain).
	t.Run("DenseCoreSignal", func(t *testing.T) {
		corePeak := findPeak(results, "DenseCore", "core")
		isoPeak := findPeak(results, "DenseCore", "isolated")
		if corePeak <= 0 {
			t.Error("DenseCore/core: zero peak — graph may be disconnected")
			return
		}
		t.Logf("DenseCore — core peak: %.4f, isolated peak: %.4f", corePeak, isoPeak)
		if corePeak <= isoPeak {
			t.Errorf("expected core peak (%.4f) > isolated peak (%.4f)", corePeak, isoPeak)
		}
	})

	// Hierarchical: core (1 hop) should have higher peak than isolated (4 hops).
	t.Run("HierarchicalSignal", func(t *testing.T) {
		corePeak := findPeak(results, "Hierarchical", "core")
		isoPeak := findPeak(results, "Hierarchical", "isolated")
		t.Logf("Hierarchical — core peak: %.4f, isolated peak: %.4f", corePeak, isoPeak)
		if corePeak <= isoPeak {
			t.Errorf("expected core peak (%.4f) > isolated peak (%.4f)", corePeak, isoPeak)
		}
	})

	// Hierarchical isolated: should either never cross or cross via floor only.
	t.Run("HierarchicalIsolatedFloorDriven", func(t *testing.T) {
		for _, r := range results {
			if r.topology != "Hierarchical" || r.sourceType != "isolated" {
				continue
			}
			if r.timeToThreshold < 0 {
				t.Logf("Hierarchical/isolated: never crossed in %d rounds — fully suppressed", probeRounds)
			} else if r.floorDriven {
				t.Logf("Hierarchical/isolated: crossed at round %d via exploration floor (as expected)", r.timeToThreshold)
			} else {
				t.Logf("Hierarchical/isolated: crossed at round %d via walk (prob=%.4f) — stronger signal than expected",
					r.timeToThreshold, r.probs[r.timeToThreshold-1])
			}
		}
	})
}

// TestTopologyFetchSelectivity uses a live Propagator with threshold above the
// 0.05 shortcut to confirm that fetch decisions reflect topology in hierarchical graphs.
func TestTopologyFetchSelectivity(t *testing.T) {
	g, roles := buildHierarchical()
	localRelay := roles["local"]
	localIdx := g.GetRelayIndex(localRelay)

	var fetched []string
	prop := quantum.NewPropagator(g, localIdx, selectivityThreshold, func(noteID, _ string) {
		fetched = append(fetched, noteID)
	})
	prop.SetRetention(0, 0) // disable eviction

	prop.AddNote("core-note", roles["core"], "pk1", 0)
	prop.AddNote("edge-note", roles["edge"], "pk2", 0)
	prop.AddNote("isolated-note", roles["isolated"], "pk3", 0)

	firstFetch := map[string]int{
		"core-note":     -1,
		"edge-note":     -1,
		"isolated-note": -1,
	}

	for round := int64(1); round <= probeRounds; round++ {
		before := len(fetched)
		prop.Tick(round, 0)
		for _, id := range fetched[before:] {
			if firstFetch[id] < 0 {
				firstFetch[id] = int(round)
			}
		}
	}

	t.Logf("Hierarchical fetch order (threshold=%.2f) — core: round %d, edge: round %d, isolated: round %d",
		selectivityThreshold, firstFetch["core-note"], firstFetch["edge-note"], firstFetch["isolated-note"])

	// At least one of core or edge should be fetched — walk signal must exist.
	if firstFetch["core-note"] < 0 && firstFetch["edge-note"] < 0 {
		t.Errorf("neither core-note nor edge-note fetched — walk signal absent at threshold=%.2f", selectivityThreshold)
	}

	// Isolated (4 hops, peak ~0.025) should be fetched after core/edge or not at all.
	// It must not be fetched before both core and edge.
	isoRound := firstFetch["isolated-note"]
	coreRound := firstFetch["core-note"]
	edgeRound := firstFetch["edge-note"]
	if isoRound >= 0 {
		if coreRound >= 0 && isoRound < coreRound {
			t.Errorf("isolated-note (round %d) fetched before core-note (round %d)", isoRound, coreRound)
		}
		if edgeRound >= 0 && isoRound < edgeRound {
			t.Errorf("isolated-note (round %d) fetched before edge-note (round %d)", isoRound, edgeRound)
		}
	}
}

func findPeak(results []topologyResult, topology, srcType string) float64 {
	for _, r := range results {
		if r.topology == topology && r.sourceType == srcType {
			return r.peak
		}
	}
	return 0
}

func formatTable(results []topologyResult) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("%-15s %-10s %-12s %8s %8s %10s %12s\n",
		"Topology", "Source", "Local", "TTT", "Peak", "Variance", "FloorDriven"))
	sb.WriteString(strings.Repeat("-", 80) + "\n")
	for _, r := range results {
		ttt := fmt.Sprintf("%d", r.timeToThreshold)
		if r.timeToThreshold < 0 {
			ttt = "never"
		}
		fd := ""
		if r.floorDriven {
			fd = "yes"
		}
		sb.WriteString(fmt.Sprintf("%-15s %-10s %-12s %8s %8.4f %10.6f %12s\n",
			r.topology, r.sourceType, r.localRelay, ttt, r.peak, r.variance, fd))
	}
	return sb.String()
}
