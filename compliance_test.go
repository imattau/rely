package rely

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip11"
)

func TestExpiredEvent(t *testing.T) {
	now := time.Now().Unix()

	tests := []struct {
		name    string
		tags    nostr.Tags
		wantErr error
	}{
		{
			name:    "no expiration tag",
			tags:    nostr.Tags{},
			wantErr: nil,
		},
		{
			name:    "future expiration",
			tags:    nostr.Tags{{"expiration", fmt.Sprintf("%d", now+3600)}},
			wantErr: nil,
		},
		{
			name:    "past expiration",
			tags:    nostr.Tags{{"expiration", fmt.Sprintf("%d", now-10)}},
			wantErr: ErrExpiredEvent,
		},
		{
			name:    "malformed expiration",
			tags:    nostr.Tags{{"expiration", "not-a-number"}},
			wantErr: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := &nostr.Event{
				Tags: tt.tags,
			}
			err := ExpiredEvent(nil, e)
			if err != tt.wantErr {
				t.Errorf("ExpiredEvent() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestDynamicNIP11Count(t *testing.T) {
	// 1. Without Count hook
	r := NewRelay()
	var doc nip11.RelayInformationDocument
	if err := json.Unmarshal(r.settings.Sys.info, &doc); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	has45 := false
	for _, n := range doc.SupportedNIPs {
		if val, ok := n.(float64); ok && int(val) == 45 {
			has45 = true
		}
		if val, ok := n.(int); ok && val == 45 {
			has45 = true
		}
	}
	if has45 {
		t.Error("expected NIP-11 SupportedNIPs to not contain NIP-45 by default")
	}

	// 2. With Count hook
	r2 := NewRelay()
	r2.On.Count = func(c Client, id string, filters nostr.Filters) (int64, bool, error) {
		return 0, false, nil
	}
	r2.validate()

	var doc2 nip11.RelayInformationDocument
	if err := json.Unmarshal(r2.settings.Sys.info, &doc2); err != nil {
		t.Fatalf("failed to unmarshal doc2: %v", err)
	}

	has45 = false
	for _, n := range doc2.SupportedNIPs {
		if val, ok := n.(float64); ok && int(val) == 45 {
			has45 = true
		}
		if val, ok := n.(int); ok && val == 45 {
			has45 = true
		}
	}
	if !has45 {
		t.Error("expected NIP-11 SupportedNIPs to contain NIP-45 when Count hook is set")
	}
}

func TestNIP09DeletionHelpers(t *testing.T) {
	pubkey1 := "pubkey1"
	pubkey2 := "pubkey2"

	targetEvent := &nostr.Event{
		ID:        "event1",
		PubKey:    pubkey1,
		Kind:      1,
		CreatedAt: nostr.Timestamp(time.Now().Unix()),
		Content:   "hello",
	}

	// Correct deletion event
	delEvent := &nostr.Event{
		ID:     "del1",
		PubKey: pubkey1,
		Kind:   5,
		Tags:   nostr.Tags{{"e", "event1"}},
	}

	if !IsDeletionEvent(delEvent) {
		t.Error("expected IsDeletionEvent to return true")
	}
	if IsDeletionEvent(targetEvent) {
		t.Error("expected IsDeletionEvent to return false")
	}

	if err := ValidateDeletionRequest(delEvent, targetEvent); err != nil {
		t.Errorf("expected ValidateDeletionRequest to pass, got: %v", err)
	}

	// Deletion unauthorized (wrong pubkey)
	badDelEvent := &nostr.Event{
		ID:     "del2",
		PubKey: pubkey2,
		Kind:   5,
		Tags:   nostr.Tags{{"e", "event1"}},
	}
	if err := ValidateDeletionRequest(badDelEvent, targetEvent); err != ErrDeletionUnauthorized {
		t.Errorf("expected ErrDeletionUnauthorized, got %v", err)
	}

	// Deletion target not linked
	unlinkedDelEvent := &nostr.Event{
		ID:     "del3",
		PubKey: pubkey1,
		Kind:   5,
		Tags:   nostr.Tags{{"e", "some_other_event"}},
	}
	if err := ValidateDeletionRequest(unlinkedDelEvent, targetEvent); err != ErrDeletionTargetNotLinked {
		t.Errorf("expected ErrDeletionTargetNotLinked, got %v", err)
	}

	// Parameterized replaceable event
	paramTargetEvent := &nostr.Event{
		ID:     "param1",
		PubKey: pubkey1,
		Kind:   30000,
		Tags:   nostr.Tags{{"d", "my-identifier"}},
	}
	paramDelEvent := &nostr.Event{
		ID:     "del4",
		PubKey: pubkey1,
		Kind:   5,
		Tags:   nostr.Tags{{"a", "30000:pubkey1:my-identifier"}},
	}
	if err := ValidateDeletionRequest(paramDelEvent, paramTargetEvent); err != nil {
		t.Errorf("expected parameterized deletion to pass, got: %v", err)
	}
}

func TestNIP22CreatedAtLimits(t *testing.T) {
	pastLimit := 30 * time.Minute
	futureLimit := 15 * time.Minute
	validator := RejectCreatedAtLimits(pastLimit, futureLimit)

	tests := []struct {
		name      string
		createdAt int64
		wantErr   error
	}{
		{
			name:      "exactly now",
			createdAt: time.Now().Unix(),
			wantErr:   nil,
		},
		{
			name:      "well within past limit",
			createdAt: time.Now().Add(-10 * time.Minute).Unix(),
			wantErr:   nil,
		},
		{
			name:      "well within future limit",
			createdAt: time.Now().Add(5 * time.Minute).Unix(),
			wantErr:   nil,
		},
		{
			name:      "too far in past",
			createdAt: time.Now().Add(-40 * time.Minute).Unix(),
			wantErr:   ErrCreatedAtLimits,
		},
		{
			name:      "too far in future",
			createdAt: time.Now().Add(20 * time.Minute).Unix(),
			wantErr:   ErrCreatedAtLimits,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := &nostr.Event{
				CreatedAt: nostr.Timestamp(tt.createdAt),
			}
			err := validator(nil, e)
			if err != tt.wantErr {
				t.Errorf("RejectCreatedAtLimits() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestNIP13PoW(t *testing.T) {
	// Let's test CountLeadingZeroBits directly
	tests := []struct {
		id       string
		wantBits int
	}{
		{"0000000000000000000000000000000000000000000000000000000000000000", 256},
		{"00000000001a0000000000000000000000000000000000000000000000000000", 43},
		{"f000000000000000000000000000000000000000000000000000000000000000", 0},
		{"7000000000000000000000000000000000000000000000000000000000000000", 1},
		{"3000000000000000000000000000000000000000000000000000000000000000", 2},
		{"1000000000000000000000000000000000000000000000000000000000000000", 3},
		{"0100000000000000000000000000000000000000000000000000000000000000", 7},
	}

	for _, tt := range tests {
		got := CountLeadingZeroBits(tt.id)
		if got != tt.wantBits {
			t.Errorf("CountLeadingZeroBits(%s) = %d, want %d", tt.id, got, tt.wantBits)
		}
	}

	// Test validator RejectMinDifficulty
	validator := RejectMinDifficulty(4)
	err := validator(nil, &nostr.Event{ID: "0000111111111111111111111111111111111111111111111111111111111111"})
	if err != nil {
		t.Errorf("expected no error for difficulty >= 4, got: %v", err)
	}

	err = validator(nil, &nostr.Event{ID: "1000000000000000000000000000000000000000000000000000000000000000"})
	if err != ErrDifficultyTooLow {
		t.Errorf("expected ErrDifficultyTooLow, got: %v", err)
	}
}

type testAccessClient struct {
	Client
	pubkeys []string
}

func (c *testAccessClient) Pubkeys() []string {
	return c.pubkeys
}

func TestNIP70ProtectedEvents(t *testing.T) {
	protectedEvent := &nostr.Event{
		PubKey: "author1",
		Tags:   nostr.Tags{{"-"}},
	}
	regularEvent := &nostr.Event{
		PubKey: "author1",
		Tags:   nostr.Tags{},
	}

	if !IsProtectedEvent(protectedEvent) {
		t.Error("expected IsProtectedEvent(protectedEvent) to be true")
	}
	if IsProtectedEvent(regularEvent) {
		t.Error("expected IsProtectedEvent(regularEvent) to be false")
	}

	clientSelf := &testAccessClient{pubkeys: []string{"author1"}}
	clientOther := &testAccessClient{pubkeys: []string{"author2"}}

	if !CanAccessEvent(clientSelf, protectedEvent) {
		t.Error("expected author to access their own protected event")
	}
	if CanAccessEvent(clientOther, protectedEvent) {
		t.Error("expected other client to be blocked from accessing protected event")
	}
	if !CanAccessEvent(clientOther, regularEvent) {
		t.Error("expected other client to access regular event")
	}
}
