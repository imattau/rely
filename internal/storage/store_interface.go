package storage

import "github.com/nbd-wtf/go-nostr"

// EventStore is the persistence contract used by the relay and quantum fetcher.
// Both the in-memory Store and the SQLiteStore satisfy this interface.
type EventStore interface {
	Save(e nostr.Event)
	Get(id string) (nostr.Event, bool)
	Query(filters nostr.Filters) []nostr.Event
	Delete(id string)
	SetReputation(pubkey string, score float64)
	GetReputation(pubkey string) float64
	AllReputation() map[string]float64
	MergeReputation(incoming map[string]float64, weight float64)
}
