package json

import (
	stdjson "encoding/json"
	"io"
)

type RawMessage = stdjson.RawMessage
type Decoder = stdjson.Decoder
type Encoder = stdjson.Encoder
type Delim = stdjson.Delim
type Number = stdjson.Number
type Marshaler = stdjson.Marshaler
type Unmarshaler = stdjson.Unmarshaler

func Marshal(v any) ([]byte, error) { return stdjson.Marshal(v) }
func MarshalIndent(v any, prefix, indent string) ([]byte, error) {
	return stdjson.MarshalIndent(v, prefix, indent)
}
func Unmarshal(data []byte, v any) error { return stdjson.Unmarshal(data, v) }
func Valid(data []byte) bool             { return stdjson.Valid(data) }
func NewDecoder(r io.Reader) *Decoder    { return stdjson.NewDecoder(r) }
func NewEncoder(w io.Writer) *Encoder    { return stdjson.NewEncoder(w) }
