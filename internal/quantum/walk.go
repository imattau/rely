package quantum

import (
	"math"
	"math/cmplx"
	"sync"
)

type activeNote struct {
	id          string
	sourceRelay string
	sourceIndex int
	pubKey      string
	bornRound   int64
}

type Propagator struct {
	graph      *GraphState
	localIndex int
	threshold  float64
	fetchFunc  func(noteID, sourceRelay string)

	mu           sync.RWMutex
	notes        []activeNote
	positions    map[string]int
	order        []string
	orderHead    int
	fetched      map[string]int64
	maxActive    int
	maxAgeRounds int64
	fetchedSweep int64
}

func NewPropagator(graph *GraphState, localIndex int, threshold float64, fetchFunc func(string, string)) *Propagator {
	return &Propagator{
		graph:        graph,
		localIndex:   localIndex,
		threshold:    threshold,
		fetchFunc:    fetchFunc,
		positions:    make(map[string]int),
		order:        make([]string, 0, 1024),
		fetched:      make(map[string]int64),
		maxActive:    10_000,
		maxAgeRounds: 600,
		fetchedSweep: 32,
	}
}

// SetRetention configures the active-note cap and round-based TTL.
// A non-positive maxActive disables the size cap.
// A non-positive maxAgeRounds disables TTL pruning.
func (p *Propagator) SetRetention(maxActive int, maxAgeRounds int64) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.maxActive = maxActive
	p.maxAgeRounds = maxAgeRounds
}

func (p *Propagator) ActiveCount() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.notes)
}

func (p *Propagator) HasNote(noteID string) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	_, ok := p.positions[noteID]
	return ok
}

func (p *Propagator) AddNote(noteID, sourceRelay, pubKey string, bornRound int64) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if _, ok := p.fetched[noteID]; ok {
		return
	}

	if idx, ok := p.positions[noteID]; ok {
		existing := &p.notes[idx]
		existing.sourceRelay = sourceRelay
		existing.sourceIndex = p.graph.GetRelayIndex(sourceRelay)
		existing.pubKey = pubKey
		existing.bornRound = bornRound
		return
	}

	note := activeNote{
		id:          noteID,
		sourceRelay: sourceRelay,
		sourceIndex: p.graph.GetRelayIndex(sourceRelay),
		pubKey:      pubKey,
		bornRound:   bornRound,
	}
	p.notes = append(p.notes, note)
	p.positions[noteID] = len(p.notes) - 1
	p.order = append(p.order, noteID)
	p.evictOverflowLocked()
}

func (p *Propagator) Tick(currentRound int64, gamma float64) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.pruneExpiredLocked(currentRound)
	sourceProbCache := make(map[tickCacheKey]float64, len(p.notes))

	for i := 0; i < len(p.notes); {
		note := p.notes[i]
		prob := p.probabilityForNoteLocked(note, currentRound, gamma, sourceProbCache)

		if prob > p.threshold {
			p.fetched[note.id] = currentRound
			p.removeAtLocked(i)

			if p.fetchFunc != nil {
				p.fetchFunc(note.id, note.sourceRelay)
			}
			continue
		}

		i++
	}

	if p.fetchedSweep > 0 && currentRound%p.fetchedSweep == 0 {
		p.pruneFetchedLocked(currentRound)
	}
}

type tickCacheKey struct {
	sourceIndex int
	bornRound   int64
}

func (p *Propagator) probabilityForNoteLocked(note activeNote, currentRound int64, gamma float64, cache map[tickCacheKey]float64) float64 {
	key := tickCacheKey{sourceIndex: note.sourceIndex, bornRound: note.bornRound}
	if prob, ok := cache[key]; ok {
		return prob
	}

	t := float64(currentRound - note.bornRound)
	if t < 0 {
		t = 0
	}

	srcIdx := note.sourceIndex
	if srcIdx < 0 {
		srcIdx = p.localIndex
	}

	amp := p.graph.Amplitude(p.localIndex, srcIdx, t)
	prob := cmplx.Abs(amp) * cmplx.Abs(amp)
	prob *= ReputationFactor(0, gamma, t)
	if t > 0 {
		if p.threshold <= 0.05 {
			prob = 1
		} else {
			exploration := 0.02 * (1 - math.Exp(-t/25))
			if math.IsNaN(prob) || prob < exploration {
				prob = exploration
			}
		}
	}

	cache[key] = prob
	return prob
}

func (p *Propagator) pruneExpiredLocked(currentRound int64) {
	if p.maxAgeRounds <= 0 {
		return
	}

	for i := 0; i < len(p.notes); {
		note := p.notes[i]
		if currentRound-note.bornRound > p.maxAgeRounds {
			p.removeAtLocked(i)
			continue
		}
		i++
	}
}

func (p *Propagator) pruneFetchedLocked(currentRound int64) {
	if p.maxAgeRounds <= 0 {
		return
	}
	for noteID, round := range p.fetched {
		if p.isExpiredFetchedLocked(round, currentRound) {
			delete(p.fetched, noteID)
		}
	}
}

func (p *Propagator) isExpiredFetchedLocked(fetchedRound, currentRound int64) bool {
	if p.maxAgeRounds <= 0 {
		return false
	}
	if currentRound == 0 {
		return false
	}
	return currentRound-fetchedRound > p.maxAgeRounds
}

func (p *Propagator) evictOverflowLocked() {
	if p.maxActive <= 0 {
		return
	}
	for len(p.positions) > p.maxActive {
		noteID := p.nextEvictionCandidateLocked()
		if noteID == "" {
			return
		}
		p.removeByIDLocked(noteID)
	}
}

func (p *Propagator) nextEvictionCandidateLocked() string {
	for p.orderHead < len(p.order) {
		noteID := p.order[p.orderHead]
		p.orderHead++
		if _, ok := p.positions[noteID]; ok {
			return noteID
		}
	}

	if p.orderHead > 1024 && p.orderHead*2 >= len(p.order) {
		p.order = append(p.order[:0], p.order[p.orderHead:]...)
		p.orderHead = 0
	}
	return ""
}

func (p *Propagator) removeByIDLocked(noteID string) {
	idx, ok := p.positions[noteID]
	if !ok {
		return
	}
	p.removeAtLocked(idx)
}

func (p *Propagator) removeAtLocked(idx int) {
	last := len(p.notes) - 1
	if idx < 0 || idx > last {
		return
	}

	noteID := p.notes[idx].id
	delete(p.positions, noteID)

	if idx != last {
		moved := p.notes[last]
		p.notes[idx] = moved
		p.positions[moved.id] = idx
	}

	p.notes[last] = activeNote{}
	p.notes = p.notes[:last]
}
