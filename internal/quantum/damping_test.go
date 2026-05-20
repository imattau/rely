package quantum_test

import (
	"math"
	"testing"

	"github.com/pippellia-btc/rely/v2/internal/quantum"
)

func TestDampingNeutralRep(t *testing.T) {
	f := quantum.ReputationFactor(0, 0.5, 2.0)
	if math.Abs(f-1.0) > 1e-9 {
		t.Errorf("neutral rep factor = %f, want 1.0", f)
	}
}

func TestDampingNegativeRep(t *testing.T) {
	f := quantum.ReputationFactor(-1.0, 0.5, 2.0)
	if f >= 1.0 {
		t.Errorf("negative rep should reduce factor, got %f", f)
	}
	if f < 0 {
		t.Errorf("factor must be non-negative, got %f", f)
	}
}

func TestDampingPositiveRep(t *testing.T) {
	f := quantum.ReputationFactor(0.8, 0.5, 2.0)
	if math.Abs(f-1.0) > 1e-9 {
		t.Errorf("positive rep factor = %f, want 1.0", f)
	}
}
