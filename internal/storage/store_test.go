package storage_test

import (
	"testing"

	"github.com/nbd-wtf/go-nostr"
	"github.com/pippellia-btc/rely/v2/internal/storage"
)

func TestStoreAndQuery(t *testing.T) {
	s := storage.NewStore()
	e := nostr.Event{ID: "abc123", PubKey: "pk1", Kind: 1}
	s.Save(e)

	all := s.Query(nostr.Filters{{Kinds: []int{1}}})
	if len(all) != 1 || all[0].ID != "abc123" {
		t.Fatalf("unexpected query result: %v", all)
	}
}

func TestReputation(t *testing.T) {
	s := storage.NewStore()
	s.SetReputation("pk1", 0.8)
	if s.GetReputation("pk1") != 0.8 {
		t.Fatal("reputation mismatch")
	}
	if s.GetReputation("unknown") != 0 {
		t.Fatal("unknown pubkey should return 0")
	}
}

func TestStoreDelete(t *testing.T) {
	s := storage.NewStore()
	e := nostr.Event{ID: "abc123", PubKey: "pk1", Kind: 1}
	s.Save(e)

	if _, ok := s.Get("abc123"); !ok {
		t.Fatal("expected event before delete")
	}

	s.Delete("abc123")

	if _, ok := s.Get("abc123"); ok {
		t.Fatal("expected event to be removed")
	}
}
