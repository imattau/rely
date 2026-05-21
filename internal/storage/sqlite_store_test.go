package storage_test

import (
	"path/filepath"
	"testing"

	"github.com/nbd-wtf/go-nostr"
	"github.com/pippellia-btc/rely/v2/internal/storage"
)

func TestSQLiteStoreEventAndReputation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relay.db")

	s, err := storage.NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer s.Close()

	event := nostr.Event{ID: "evt-1", PubKey: "pk1", Kind: 1, Content: "hello"}
	s.Save(event)

	got, ok := s.Get("evt-1")
	if !ok {
		t.Fatal("expected event to be saved")
	}
	if got.ID != event.ID || got.Content != event.Content {
		t.Fatalf("unexpected event: %+v", got)
	}

	query := s.Query(nostr.Filters{{IDs: []string{"evt-1"}}})
	if len(query) != 1 || query[0].ID != event.ID {
		t.Fatalf("unexpected query result: %+v", query)
	}

	s.SetReputation("pk1", 0.7)
	if got := s.GetReputation("pk1"); got != 0.7 {
		t.Fatalf("reputation = %v, want 0.7", got)
	}

	all := s.AllReputation()
	if got := all["pk1"]; got != 0.7 {
		t.Fatalf("all reputation = %v, want 0.7", got)
	}

	s.MergeReputation(map[string]float64{"pk1": -1, "pk2": 0.4}, 0.5)
	if got := s.GetReputation("pk1"); got >= 0.7 {
		t.Fatalf("expected pk1 to move toward incoming score, got %v", got)
	}
	if got := s.GetReputation("pk2"); got == 0 {
		t.Fatalf("expected pk2 reputation to be inserted, got %v", got)
	}

	s.Delete("evt-1")
	if _, ok := s.Get("evt-1"); ok {
		t.Fatal("expected event to be deleted")
	}
}
