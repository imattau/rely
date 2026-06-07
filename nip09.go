package rely

import (
	"errors"
	"fmt"

	"github.com/nbd-wtf/go-nostr"
)

var (
	ErrNotDeletionEvent        = errors.New("event is not a deletion request (kind 5)")
	ErrDeletionUnauthorized    = errors.New("deletion request must be signed by the same author as the target event")
	ErrDeletionTargetNotLinked = errors.New("deletion request does not reference the target event")
)

// IsDeletionEvent returns true if the event is a NIP-09 deletion request (kind 5).
func IsDeletionEvent(e *nostr.Event) bool {
	return e != nil && e.Kind == 5
}

// ValidateDeletionRequest checks if deleteEvent is a valid deletion request (kind 5)
// that is authorized to delete targetEvent.
func ValidateDeletionRequest(deleteEvent *nostr.Event, targetEvent *nostr.Event) error {
	if deleteEvent == nil || targetEvent == nil {
		return errors.New("nil event provided")
	}

	if deleteEvent.Kind != 5 {
		return ErrNotDeletionEvent
	}

	// Deletion requests must be signed by the same key as the target event.
	if deleteEvent.PubKey != targetEvent.PubKey {
		return ErrDeletionUnauthorized
	}

	// Check if deleteEvent references the target event's ID in 'e' tags.
	for _, tag := range deleteEvent.Tags {
		if len(tag) >= 2 && tag[0] == "e" && tag[1] == targetEvent.ID {
			return nil
		}
	}

	// For parameterized replaceable events (NIP-33), check 'a' tags.
	if targetEvent.Kind >= 30000 && targetEvent.Kind < 40000 {
		dTag := ""
		for _, tag := range targetEvent.Tags {
			if len(tag) >= 2 && tag[0] == "d" {
				dTag = tag[1]
				break
			}
		}
		expectedCoordinates := fmt.Sprintf("%d:%s:%s", targetEvent.Kind, targetEvent.PubKey, dTag)
		for _, tag := range deleteEvent.Tags {
			if len(tag) >= 2 && tag[0] == "a" && tag[1] == expectedCoordinates {
				return nil
			}
		}
	}

	return ErrDeletionTargetNotLinked
}
