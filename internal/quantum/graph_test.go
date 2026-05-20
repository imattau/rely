package quantum_test

import (
	"fmt"
	"math"
	"math/cmplx"
	"testing"

	"github.com/pippellia-btc/rely/v2/internal/quantum"
)

func TestAmplitudeAtT0(t *testing.T) {
	g := quantum.NewGraphState()
	g.SetRelays([]string{"relay0", "relay1"})
	g.SetConnection("relay0", "relay1", true)
	g.Recompute()

	amp := g.Amplitude(0, 0, 0)
	if math.Abs(cmplx.Abs(amp)-1.0) > 1e-9 {
		t.Errorf("|<0|U(0)|0>| = %f, want 1.0", cmplx.Abs(amp))
	}
}

func TestAmplitudeProbabilityNormalized(t *testing.T) {
	g := quantum.NewGraphState()
	g.SetRelays([]string{"r0", "r1", "r2"})
	g.SetConnection("r0", "r1", true)
	g.SetConnection("r1", "r2", true)
	g.Recompute()

	currentT := 1.5
	var total float64
	for i := 0; i < 3; i++ {
		a := g.Amplitude(i, 0, currentT)
		total += cmplx.Abs(a) * cmplx.Abs(a)
	}
	if math.Abs(total-1.0) > 1e-6 {
		t.Errorf("probability sum = %f, want ~1.0", total)
	}
}

func TestGetRelayIndex(t *testing.T) {
	g := quantum.NewGraphState()
	g.SetRelays([]string{"alpha", "beta"})
	g.Recompute()
	if g.GetRelayIndex("beta") != 1 {
		t.Error("wrong index for beta")
	}
	if g.GetRelayIndex("unknown") != -1 {
		t.Error("unknown relay should return -1")
	}
}

func TestSparseFallbackAtT0(t *testing.T) {
	g := quantum.NewGraphState()
	relays := make([]string, 129)
	for i := range relays {
		relays[i] = fmt.Sprintf("relay-%d", i)
	}
	g.SetRelays(relays)
	for i := 0; i < len(relays)-1; i++ {
		g.SetConnection(relays[i], relays[i+1], true)
	}
	g.Recompute()

	amp := g.Amplitude(0, 0, 0)
	if math.Abs(cmplx.Abs(amp)-1.0) > 1e-9 {
		t.Fatalf("sparse fallback amplitude = %f, want 1", cmplx.Abs(amp))
	}
}
