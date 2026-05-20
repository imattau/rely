package quantum

import (
	"math"
	"math/cmplx"
	"sync"
	"time"
)

const exactEigenLimit = 128

type GraphState struct {
	mu sync.RWMutex

	relays    []string
	idx       map[string]int
	neighbors map[int]map[int]struct{}
	n         int

	eigvals        []float64
	eigvecs        [][]float64
	recomputeTimer *time.Timer
}

func NewGraphState() *GraphState {
	return &GraphState{
		idx:       make(map[string]int),
		neighbors: make(map[int]map[int]struct{}),
	}
}

func (g *GraphState) SetRelays(urls []string) {
	g.mu.Lock()
	defer g.mu.Unlock()

	g.n = len(urls)
	g.relays = make([]string, g.n)
	g.idx = make(map[string]int, g.n)
	g.neighbors = make(map[int]map[int]struct{}, g.n)
	for i, u := range urls {
		g.relays[i] = u
		g.idx[u] = i
		g.neighbors[i] = make(map[int]struct{})
	}
	g.eigvals = nil
	g.eigvecs = nil
}

func (g *GraphState) SetConnection(a, b string, connected bool) {
	g.mu.Lock()
	defer g.mu.Unlock()

	ai, aok := g.idx[a]
	bi, bok := g.idx[b]
	if !aok || !bok || ai == bi {
		return
	}

	if connected {
		if g.neighbors[ai] == nil {
			g.neighbors[ai] = make(map[int]struct{})
		}
		if g.neighbors[bi] == nil {
			g.neighbors[bi] = make(map[int]struct{})
		}
		g.neighbors[ai][bi] = struct{}{}
		g.neighbors[bi][ai] = struct{}{}
		return
	}

	delete(g.neighbors[ai], bi)
	delete(g.neighbors[bi], ai)
}

func (g *GraphState) Recompute() {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.recomputeTimer != nil {
		_ = g.recomputeTimer.Stop()
	}

	if g.n == 0 {
		g.eigvals = nil
		g.eigvecs = nil
		return
	}

	// Dense eigendecomposition is still the best option for small relay sets.
	// For larger sets, we skip the dense factorization and fall back to sparse
	// matrix-vector propagation in Amplitude().
	if g.n > exactEigenLimit {
		g.eigvals = nil
		g.eigvecs = nil
		return
	}

	lap := make([][]float64, g.n)
	for i := range lap {
		lap[i] = make([]float64, g.n)
		for j := range g.neighbors[i] {
			lap[i][j] = -1
			lap[i][i]++
		}
	}

	eigvals, eigvecs := jacobiEigenSym(lap)
	g.eigvals = eigvals
	g.eigvecs = eigvecs
}

// ScheduleRecompute debounces a recomputation so rapid topology changes collapse
// into a single update. A non-positive delay recomputes immediately.
func (g *GraphState) ScheduleRecompute(delay time.Duration) {
	if delay <= 0 {
		g.Recompute()
		return
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	if g.recomputeTimer == nil {
		g.recomputeTimer = time.AfterFunc(delay, func() {
			g.Recompute()
		})
		return
	}

	g.recomputeTimer.Reset(delay)
}

func (g *GraphState) Amplitude(i, s int, t float64) complex128 {
	g.mu.RLock()
	defer g.mu.RUnlock()

	if i < 0 || s < 0 || i >= g.n || s >= g.n {
		return 0
	}
	if g.eigvecs != nil {
		return g.amplitudeExactLocked(i, s, t)
	}
	return g.amplitudeSparseLocked(i, s, t)
}

func (g *GraphState) amplitudeExactLocked(i, s int, t float64) complex128 {
	var amp complex128
	for k := 0; k < g.n; k++ {
		phase := -g.eigvals[k] * t
		expFactor := complex(math.Cos(phase), math.Sin(phase))
		amp += complex(g.eigvecs[i][k], 0) * expFactor * cmplx.Conj(complex(g.eigvecs[s][k], 0))
	}
	return amp
}

func (g *GraphState) amplitudeSparseLocked(i, s int, t float64) complex128 {
	if t == 0 {
		if i == s {
			return 1
		}
		return 0
	}

	// Truncated Taylor expansion of exp(-iLt) applied to |s>.
	const maxTerms = 16
	accum := make([]complex128, g.n)
	accum[s] = 1
	term := make([]complex128, g.n)
	term[s] = 1
	coeff := complex(1, 0)
	for k := 1; k <= maxTerms; k++ {
		term = g.applyLaplacianSparseLocked(term)
		coeff *= complex(0, -t) / complex(float64(k), 0)
		for idx := 0; idx < g.n; idx++ {
			accum[idx] += coeff * term[idx]
		}
		if termMagnitude(term)*cmplx.Abs(coeff) < 1e-10 {
			break
		}
	}
	return accum[i]
}

func (g *GraphState) applyLaplacianSparseLocked(v []complex128) []complex128 {
	out := make([]complex128, g.n)
	for i := 0; i < g.n; i++ {
		neighbors := g.neighbors[i]
		if len(neighbors) == 0 {
			continue
		}

		sum := complex(float64(len(neighbors)), 0) * v[i]
		for j := range neighbors {
			sum -= v[j]
		}
		out[i] = sum
	}
	return out
}

func termMagnitude(v []complex128) float64 {
	max := 0.0
	for _, x := range v {
		if m := cmplx.Abs(x); m > max {
			max = m
		}
	}
	return max
}

func (g *GraphState) GetRelayIndex(url string) int {
	g.mu.RLock()
	defer g.mu.RUnlock()

	idx, ok := g.idx[url]
	if !ok {
		return -1
	}
	return idx
}

func (g *GraphState) N() int {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.n
}

func jacobiEigenSym(a [][]float64) ([]float64, [][]float64) {
	n := len(a)
	if n == 0 {
		return nil, nil
	}

	v := make([][]float64, n)
	for i := range v {
		v[i] = make([]float64, n)
		v[i][i] = 1
	}

	const maxIter = 64
	const eps = 1e-12

	for iter := 0; iter < maxIter; iter++ {
		p, q := 0, 1
		maxVal := 0.0
		for i := 0; i < n; i++ {
			for j := i + 1; j < n; j++ {
				val := math.Abs(a[i][j])
				if val > maxVal {
					maxVal = val
					p, q = i, j
				}
			}
		}

		if maxVal < eps {
			break
		}

		app := a[p][p]
		aqq := a[q][q]
		apq := a[p][q]
		if apq == 0 {
			continue
		}

		tau := (aqq - app) / (2 * apq)
		t := math.Copysign(1/(math.Abs(tau)+math.Sqrt(1+tau*tau)), tau)
		c := 1 / math.Sqrt(1+t*t)
		s := t * c

		for k := 0; k < n; k++ {
			if k == p || k == q {
				continue
			}

			akp := a[k][p]
			akq := a[k][q]
			a[k][p] = c*akp - s*akq
			a[p][k] = a[k][p]
			a[k][q] = c*akq + s*akp
			a[q][k] = a[k][q]
		}

		a[p][p] = c*c*app - 2*s*c*apq + s*s*aqq
		a[q][q] = s*s*app + 2*s*c*apq + c*c*aqq
		a[p][q] = 0
		a[q][p] = 0

		for k := 0; k < n; k++ {
			vkp := v[k][p]
			vkq := v[k][q]
			v[k][p] = c*vkp - s*vkq
			v[k][q] = s*vkp + c*vkq
		}
	}

	eigvals := make([]float64, n)
	for i := 0; i < n; i++ {
		eigvals[i] = a[i][i]
	}

	for i := 0; i < n-1; i++ {
		minIdx := i
		for j := i + 1; j < n; j++ {
			if eigvals[j] < eigvals[minIdx] {
				minIdx = j
			}
		}
		if minIdx != i {
			eigvals[i], eigvals[minIdx] = eigvals[minIdx], eigvals[i]
			for row := 0; row < n; row++ {
				v[row][i], v[row][minIdx] = v[row][minIdx], v[row][i]
			}
		}
	}

	return eigvals, v
}
