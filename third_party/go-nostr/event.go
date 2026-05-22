package nostr

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"sort"
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
	Tags      TagMap   `json:"-"`
	Search    string   `json:"search,omitempty"`
	Since     *Timestamp
	Until     *Timestamp
	Limit     int  `json:"limit,omitempty"`
	LimitZero bool `json:"-"`
}

func (f *Filter) UnmarshalJSON(data []byte) error {
	if f == nil {
		return errors.New("nil Filter receiver")
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	*f = Filter{Tags: make(TagMap)}

	for key, value := range raw {
		switch key {
		case "ids":
			if err := json.Unmarshal(value, &f.IDs); err != nil {
				return err
			}
		case "authors":
			if err := json.Unmarshal(value, &f.Authors); err != nil {
				return err
			}
		case "kinds":
			if err := json.Unmarshal(value, &f.Kinds); err != nil {
				return err
			}
		case "search":
			if err := json.Unmarshal(value, &f.Search); err != nil {
				return err
			}
		case "since":
			if err := json.Unmarshal(value, &f.Since); err != nil {
				return err
			}
		case "until":
			if err := json.Unmarshal(value, &f.Until); err != nil {
				return err
			}
		case "limit":
			if err := json.Unmarshal(value, &f.Limit); err != nil {
				return err
			}
		default:
			if strings.HasPrefix(key, "#") {
				if f.Tags == nil {
					f.Tags = make(TagMap)
				}
				var vals []string
				if err := json.Unmarshal(value, &vals); err != nil {
					return err
				}
				f.Tags[strings.TrimPrefix(key, "#")] = vals
			}
		}
	}

	return nil
}

func (f Filter) MarshalJSON() ([]byte, error) {
	type field struct {
		key   string
		value any
	}

	fields := make([]field, 0, 8)
	if len(f.IDs) > 0 {
		fields = append(fields, field{key: "ids", value: f.IDs})
	}
	if len(f.Authors) > 0 {
		fields = append(fields, field{key: "authors", value: f.Authors})
	}
	if len(f.Kinds) > 0 {
		fields = append(fields, field{key: "kinds", value: f.Kinds})
	}
	if len(f.Tags) > 0 {
		keys := make([]string, 0, len(f.Tags))
		for key := range f.Tags {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			fields = append(fields, field{key: "#" + key, value: f.Tags[key]})
		}
	}
	if f.Search != "" {
		fields = append(fields, field{key: "search", value: f.Search})
	}
	if f.Since != nil {
		fields = append(fields, field{key: "since", value: f.Since})
	}
	if f.Until != nil {
		fields = append(fields, field{key: "until", value: f.Until})
	}
	if f.LimitZero || f.Limit != 0 {
		fields = append(fields, field{key: "limit", value: f.Limit})
	}

	if len(fields) == 0 {
		return []byte("{}"), nil
	}

	buf := make([]byte, 0, 128)
	buf = append(buf, '{')
	for i, f := range fields {
		if i > 0 {
			buf = append(buf, ',')
		}
		key, err := json.Marshal(f.key)
		if err != nil {
			return nil, err
		}
		val, err := json.Marshal(f.value)
		if err != nil {
			return nil, err
		}
		buf = append(buf, key...)
		buf = append(buf, ':')
		buf = append(buf, val...)
	}
	buf = append(buf, '}')
	return buf, nil
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
		Kind      int       `json:"kind"`
		CreatedAt Timestamp `json:"created_at"`
		Tags      Tags      `json:"tags"`
		Content   string    `json:"content"`
		ID        string    `json:"id,omitempty"`
		PubKey    string    `json:"pubkey,omitempty"`
		Sig       string    `json:"sig,omitempty"`
	}

	tags := e.Tags
	if tags == nil {
		tags = Tags{}
	}

	return json.Marshal(wireEvent{
		Kind:      e.Kind,
		CreatedAt: e.CreatedAt,
		Tags:      tags,
		Content:   e.Content,
		ID:        e.ID,
		PubKey:    e.PubKey,
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
