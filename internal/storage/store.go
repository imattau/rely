package storage

import (
	"sync"

	"github.com/nbd-wtf/go-nostr"
)

type Store struct {
	mu         sync.RWMutex
	events     map[string]nostr.Event
	reputation map[string]float64
}

func NewStore() *Store {
	return &Store{
		events:     make(map[string]nostr.Event),
		reputation: make(map[string]float64),
	}
}

func (s *Store) Save(e nostr.Event) {
	s.mu.Lock()
	s.events[e.ID] = e
	s.mu.Unlock()
}

func (s *Store) Get(id string) (nostr.Event, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.events[id]
	return e, ok
}

func (s *Store) Query(filters nostr.Filters) []nostr.Event {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]nostr.Event, 0, len(s.events))
	for _, e := range s.events {
		if filters.Match(&e) {
			out = append(out, e)
		}
	}
	return out
}

func (s *Store) SetReputation(pubkey string, score float64) {
	s.mu.Lock()
	s.reputation[pubkey] = clamp(score, -1, 1)
	s.mu.Unlock()
}

func (s *Store) GetReputation(pubkey string) float64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.reputation[pubkey]
}

func (s *Store) AllReputation() map[string]float64 {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make(map[string]float64, len(s.reputation))
	for k, v := range s.reputation {
		out[k] = v
	}
	return out
}

func (s *Store) MergeReputation(incoming map[string]float64, weight float64) {
	if weight < 0 {
		weight = 0
	}
	if weight > 1 {
		weight = 1
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	for k, v := range incoming {
		existing := s.reputation[k]
		merged := existing*(1-weight) + v*weight
		s.reputation[k] = clamp(merged, -1, 1)
	}
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
