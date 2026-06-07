package rely

import (
	"slices"

	"github.com/nbd-wtf/go-nostr"
)

// IsProtectedEvent returns true if the event has a standard NIP-70 protected tag ["-"].
func IsProtectedEvent(e *nostr.Event) bool {
	if e == nil {
		return false
	}
	for _, tag := range e.Tags {
		if len(tag) == 1 && tag[0] == "-" {
			return true
		}
	}
	return false
}

// CanAccessEvent returns true if the client is authorized to access the event under NIP-70.
// Under NIP-70, a protected event can only be accessed by its author (pubkey).
func CanAccessEvent(c Client, e *nostr.Event) bool {
	if e == nil {
		return false
	}
	if !IsProtectedEvent(e) {
		return true
	}
	if c == nil {
		return false
	}
	return slices.Contains(c.Pubkeys(), e.PubKey)
}
