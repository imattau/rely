// Package auth implements NIP-42 authentication for Nostr relays.
//
// This package provides authentication state management and validation for clients
// connecting to a relay. It supports multi-pubkey authentication per client and
// includes challenge generation, event validation, and relay URL canonicalization.
package auth

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/goccy/go-json"

	"github.com/nbd-wtf/go-nostr"
	"github.com/pippellia-btc/smallset"
)

const (
	Kind = 22242
)

var (
	ErrInvalidFormat         = errors.New(`an AUTH request must follow this format: ['AUTH', {event_JSON}]`)
	ErrInvalidTimestamp      = errors.New(`created_at must be within one minute from the current time`)
	ErrInvalidKind           = errors.New(`invalid AUTH kind`)
	ErrInvalidEventID        = errors.New(`invalid event ID`)
	ErrInvalidEventSignature = errors.New(`invalid event signature`)
	ErrInvalidChallenge      = errors.New(`invalid AUTH challenge`)
	ErrInvalidRelay          = errors.New(`invalid AUTH relay`)
	ErrTooManyAuthed         = errors.New("trying to auth with too many pubkeys")
	ErrInvalidRelayURL       = errors.New("invalid relay URL format")
)

// State manages the authentication state of a client.
// As per NIP-42, a client can be authenticated with one or more pubkeys.
type State struct {
	mu        sync.Mutex
	challenge string
	pubkeys   *smallset.Ordered[string]
	config    Config
}

func NewState(c Config) *State {
	return &State{
		pubkeys: smallset.New[string](c.MaxPubkeys),
		config:  c,
	}
}

// Reset resets the authentication state.
// Any previously authenticated pubkey is cleared, and a new challenge is generated and sent to the callback function,
// all while holding the lock.
func (s *State) Reset(fn func(challenge string)) {
	bytes := make([]byte, int(s.config.ChallengeBytes))
	rand.Read(bytes)
	challenge := hex.EncodeToString(bytes)

	s.mu.Lock()
	defer s.mu.Unlock()

	s.pubkeys.Clear()
	s.challenge = challenge
	fn(challenge)
}

// Pubkeys returns the currently authenticated pubkeys.
func (s *State) Pubkeys() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pubkeys.Items()
}

// IsAuthed returns true if there are any authenticated pubkeys.
func (s *State) IsAuthed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pubkeys.Size() > 0
}

// Add adds a new pubkey to the authentication state.
func (s *State) Add(pk string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.pubkeys.Size() == s.config.MaxPubkeys {
		return fmt.Errorf("%w: max is %d", ErrTooManyAuthed, s.config.MaxPubkeys)
	}
	s.pubkeys.Add(pk)
	return nil
}

// Request is a normalized struct representing an authentication event (kind 22242)
type Request struct {
	ID        string
	Pubkey    string
	CreatedAt time.Time
	Challenge string
	Relay     string
}

// Parse the authentication event from the JSON decoder.
// It performs event-specific validation, returning an error if invalid.
func Parse(d *json.Decoder) (Request, error) {
	e := new(nostr.Event)
	if err := d.Decode(e); err != nil {
		return Request{}, ErrInvalidFormat
	}

	r := Request{
		ID:        e.ID,
		Pubkey:    e.PubKey,
		CreatedAt: e.CreatedAt.Time(),
	}

	if e.Kind != Kind {
		return r, ErrInvalidKind
	}

	r.Challenge = findTag(e.Tags, "challenge")
	if r.Challenge == "" {
		return r, ErrInvalidChallenge
	}

	r.Relay = findTag(e.Tags, "relay")
	if r.Relay == "" {
		return r, ErrInvalidRelay
	}

	if !e.CheckID() {
		return r, ErrInvalidEventID
	}
	match, err := e.CheckSignature()
	if err != nil || !match {
		return r, ErrInvalidEventSignature
	}
	return r, nil
}

// Validate returns the appropriate error if the auth Request does not match the expected state.
func (s *State) Validate(e Request) error {
	if time.Since(e.CreatedAt).Abs() > s.config.TimeTolerance {
		return ErrInvalidTimestamp
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	canonical, err := CanonicalURL(e.Relay)
	if err != nil {
		return ErrInvalidRelay
	}

	if s.config.URL == "" || canonical != s.config.URL {
		return ErrInvalidRelay
	}
	if s.challenge == "" || e.Challenge != s.challenge {
		return ErrInvalidChallenge
	}
	return nil
}

func findTag(tags nostr.Tags, key string) string {
	for _, tag := range tags {
		if len(tag) >= 2 && tag[0] == key {
			return tag[1]
		}
	}
	return ""
}

// CanonicalURL returns the canonical form of a relay URL consisting of
// lowercase hostname and normalized path. Scheme and port are ignored as
// they are transport details, not part of the relay's identity.
//
// Examples:
//   - "wss://Example.com/relay" -> "example.com/relay"
//   - "ws://example.com:8080/relay/" -> "example.com/relay"
//   - "wss://example.com:443" -> "example.com"
//
// URLs with userinfo (e.g., "user@host") are rejected.
func CanonicalURL(rawURL string) (string, error) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return "", errors.New("empty url")
	}

	if !strings.Contains(rawURL, "://") {
		rawURL = "wss://" + rawURL
	}

	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrInvalidRelayURL, err)
	}

	if parsed.User != nil {
		// reject urls with user info
		return "", fmt.Errorf("%w: userinfo not allowed", ErrInvalidRelayURL)
	}

	host := parsed.Hostname()
	host = strings.ToLower(host)
	if host == "" {
		return "", fmt.Errorf("%w: missing host", ErrInvalidRelayURL)
	}

	path := parsed.Path
	path = strings.TrimSuffix(path, "/")
	return host + path, nil
}
