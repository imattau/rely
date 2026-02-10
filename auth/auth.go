package auth

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
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
	bytes := make([]byte, s.config.ChallengeBytes)
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

func (s *State) IsAuthed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pubkeys.Size() > 0
}

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
	CreatedAt time.Time
	Challenge string
	Relay     string
}

// Parse the authentication event from the JSON decoder.
func Parse(d *json.Decoder) (Request, error) {
	e := new(nostr.Event)
	if err := d.Decode(e); err != nil {
		return Request{}, ErrInvalidFormat
	}

	if e.Kind != Kind {
		return Request{}, ErrInvalidKind
	}

	challenge := findTag(e.Tags, "challenge")
	if challenge == "" {
		return Request{}, ErrInvalidChallenge
	}

	relay := findTag(e.Tags, "relay")
	if relay == "" {
		return Request{}, ErrInvalidRelay
	}

	if !e.CheckID() {
		return Request{}, ErrInvalidEventID
	}
	match, err := e.CheckSignature()
	if err != nil || !match {
		return Request{}, ErrInvalidEventSignature
	}

	return Request{
		CreatedAt: e.CreatedAt.Time(),
		Challenge: challenge,
		Relay:     relay,
	}, nil
}

// Validate returns the appropriate error if the auth is invalid, otherwise returns nil.
func (s *State) Validate(e Request) error {
	if time.Since(e.CreatedAt).Abs() > s.config.TimeTolerance {
		return ErrInvalidTimestamp
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.config.Domain == "" || normalizeURL(e.Relay) != s.config.Domain {
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

// normalizeURL removes the protocol scheme (e.g., "https://") if present,
// returning only the host and path (e.g., "example.com/abc").
func normalizeURL(url string) string {
	url = strings.TrimSpace(url)
	url = strings.TrimSuffix(url, "/")
	index := strings.Index(url, "://")
	if index != -1 {
		return url[index+3:]
	}
	return url
}
