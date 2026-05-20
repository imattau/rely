package spam_test

import (
	"testing"

	"github.com/pippellia-btc/rely/v2/internal/spam"
)

func TestClientAllowed(t *testing.T) {
	rl := spam.NewRateLimiter(2, 100)
	if !rl.AllowClient("alice") {
		t.Fatal("first event should be allowed")
	}
	if !rl.AllowClient("alice") {
		t.Fatal("second event should be allowed (burst=2)")
	}
	if rl.AllowClient("alice") {
		t.Fatal("third event should be rate-limited")
	}
}

func TestPeerAllowed(t *testing.T) {
	rl := spam.NewRateLimiter(100, 2)
	if !rl.AllowPeer("peer1") {
		t.Fatal("first announce should be allowed")
	}
	if !rl.AllowPeer("peer1") {
		t.Fatal("second announce should be allowed")
	}
	if rl.AllowPeer("peer1") {
		t.Fatal("third announce should be rate-limited")
	}
}
