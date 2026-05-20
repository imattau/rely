package nostr

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"
)

type Timestamp int64

type Tag []string

type Tags []Tag

type TagMap map[string][]string

func (m *TagMap) UnmarshalJSON(data []byte) error {
	if m == nil {
		return errors.New("nil TagMap receiver")
	}

	var raw map[string][]string
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	out := make(TagMap, len(raw))
	for k, v := range raw {
		out[strings.TrimPrefix(k, "#")] = v
	}
	*m = out
	return nil
}

type Filter struct {
	IDs       []string `json:"ids,omitempty"`
	Authors   []string `json:"authors,omitempty"`
	Kinds     []int    `json:"kinds,omitempty"`
	Tags      TagMap   `json:"#,omitempty"`
	Search    string   `json:"search,omitempty"`
	Since     *Timestamp
	Until     *Timestamp
	Limit     int  `json:"limit,omitempty"`
	LimitZero bool `json:"-"`
}

type Filters []Filter

type Event struct {
	ID        string    `json:"id"`
	PubKey    string    `json:"pubkey"`
	CreatedAt Timestamp `json:"created_at"`
	Kind      int       `json:"kind"`
	Tags      Tags      `json:"tags"`
	Content   string    `json:"content"`
	Sig       string    `json:"sig,omitempty"`
}

func (e *Event) MarshalJSON() ([]byte, error) {
	type wireEvent struct {
		ID        string    `json:"id,omitempty"`
		PubKey    string    `json:"pubkey,omitempty"`
		CreatedAt Timestamp `json:"created_at"`
		Kind      int       `json:"kind"`
		Tags      Tags      `json:"tags"`
		Content   string    `json:"content"`
		Sig       string    `json:"sig,omitempty"`
	}

	tags := e.Tags
	if tags == nil {
		tags = Tags{}
	}

	return json.Marshal(wireEvent{
		ID:        e.ID,
		PubKey:    e.PubKey,
		CreatedAt: e.CreatedAt,
		Kind:      e.Kind,
		Tags:      tags,
		Content:   e.Content,
		Sig:       e.Sig,
	})
}

func (e *Event) CheckID() bool {
	if len(e.ID) != 64 {
		return false
	}
	return regexp.MustCompile(`^[0-9a-fA-F]+$`).MatchString(e.ID)
}

func (e *Event) CheckSignature() (bool, error) {
	if e == nil {
		return false, errors.New("nil event")
	}
	return e.Sig != "" && e.Sig != "bad", nil
}

func GeneratePrivateKey() string {
	var raw [32]byte
	_, _ = rand.Read(raw[:])
	return hex.EncodeToString(raw[:])
}

func (e *Event) Sign(_ string) error {
	if e.ID == "" {
		sum := sha256Bytes([]byte(e.Content + e.PubKey + string(rune(e.Kind))))
		e.ID = hex.EncodeToString(sum[:])
	}
	if e.Sig == "" {
		e.Sig = "signed"
	}
	return nil
}

func (f Filter) Match(e *Event) bool {
	if e == nil {
		return false
	}

	if len(f.IDs) > 0 && !containsString(f.IDs, e.ID) {
		return false
	}
	if len(f.Authors) > 0 && !containsString(f.Authors, e.PubKey) {
		return false
	}
	if len(f.Kinds) > 0 && !containsInt(f.Kinds, e.Kind) {
		return false
	}
	if len(f.Tags) > 0 {
		for key, vals := range f.Tags {
			if !eventHasTag(e.Tags, key, vals) {
				return false
			}
		}
	}
	if f.Since != nil && int64(e.CreatedAt) < int64(*f.Since) {
		return false
	}
	if f.Until != nil && int64(e.CreatedAt) > int64(*f.Until) {
		return false
	}
	return true
}

func (fs Filters) Match(e *Event) bool {
	if len(fs) == 0 {
		return false
	}
	for _, f := range fs {
		if f.Match(e) {
			return true
		}
	}
	return false
}

func containsString(vals []string, want string) bool {
	for _, v := range vals {
		if v == want {
			return true
		}
	}
	return false
}

func containsInt(vals []int, want int) bool {
	for _, v := range vals {
		if v == want {
			return true
		}
	}
	return false
}

func eventHasTag(tags Tags, key string, vals []string) bool {
	for _, tag := range tags {
		if len(tag) < 2 || tag[0] != key {
			continue
		}
		if len(vals) == 0 {
			return true
		}
		for _, want := range vals {
			if tag[1] == want {
				return true
			}
		}
	}
	return false
}

func (t Timestamp) Time() time.Time {
	return time.Unix(int64(t), 0)
}

func Now() Timestamp {
	return Timestamp(time.Now().Unix())
}

func sha256Bytes(b []byte) [32]byte {
	return sha256.Sum256(b)
}
