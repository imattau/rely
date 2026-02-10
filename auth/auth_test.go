package auth

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/pippellia-btc/smallset"
)

func TestParseRequest(t *testing.T) {
	tests := []struct {
		name     string
		event    *nostr.Event
		expected error
	}{
		{
			name:     "invalid kind",
			event:    Signed(nostr.Event{Kind: 69, ID: "abc", CreatedAt: nostr.Now()}),
			expected: ErrInvalidKind,
		},
		{
			name:     "no relay tag",
			event:    Signed(nostr.Event{Kind: 22242, CreatedAt: nostr.Now(), Tags: nostr.Tags{{"challenge", "challenge"}}}),
			expected: ErrInvalidRelay,
		},
		{
			name:     "no challenge tag",
			event:    Signed(nostr.Event{Kind: 22242, CreatedAt: nostr.Now(), Tags: nostr.Tags{{"relay", "example.com"}}}),
			expected: ErrInvalidChallenge,
		},
		{
			name:     "invalid ID",
			event:    &nostr.Event{Kind: 22242, CreatedAt: nostr.Now(), Tags: nostr.Tags{{"challenge", "challenge"}, {"relay", "example.com"}}},
			expected: ErrInvalidEventID,
		},
		{
			name:     "invalid signature",
			event:    WithBadSignature(nostr.Event{Kind: 22242, CreatedAt: nostr.Now(), Tags: nostr.Tags{{"relay", "example.com"}, {"challenge", "challenge"}}}),
			expected: ErrInvalidEventSignature,
		},
		{
			name:  "valid",
			event: Signed(nostr.Event{Kind: 22242, CreatedAt: nostr.Now(), Tags: nostr.Tags{{"relay", "example.com"}, {"challenge", "challenge"}}}),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var buf bytes.Buffer
			if err := json.NewEncoder(&buf).Encode(test.event); err != nil {
				t.Fatalf("failed to encode event: %v", err)
			}

			_, err := Parse(json.NewDecoder(&buf))
			if !errors.Is(err, test.expected) {
				t.Fatalf("expected error %v, got %v", test.expected, err)
			}
		})
	}
}

func TestValidateRequest(t *testing.T) {
	state := State{
		pubkeys:   smallset.New[string](10),
		challenge: "challenge",
		config: Config{
			Domain:        "example.com",
			TimeTolerance: time.Minute,
		},
	}

	tests := []struct {
		name     string
		request  Request
		expected error
	}{
		{
			name:     "too much into the past",
			request:  Request{CreatedAt: time.Now().Add(-2 * time.Minute), Challenge: "challenge", Relay: "example.com"},
			expected: ErrInvalidTimestamp,
		},
		{
			name:     "relay is different",
			request:  Request{CreatedAt: time.Now(), Challenge: "challenge", Relay: "example.com.evil.website"},
			expected: ErrInvalidRelay,
		},
		{
			name:     "challenge is different",
			request:  Request{CreatedAt: time.Now(), Challenge: "different", Relay: "example.com"},
			expected: ErrInvalidChallenge,
		},
		{
			name:     "valid",
			request:  Request{CreatedAt: time.Now(), Challenge: "challenge", Relay: "example.com"},
			expected: nil,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := state.Validate(test.request)
			if !errors.Is(err, test.expected) {
				t.Fatalf("expected error %v, got %v", test.expected, err)
			}
		})
	}
}

func Signed(e nostr.Event) *nostr.Event {
	sk := nostr.GeneratePrivateKey()
	e.Sign(sk)
	return &e
}

func WithBadSignature(e nostr.Event) *nostr.Event {
	ev := Signed(e)
	ev.Sig = "bad"
	return ev
}
