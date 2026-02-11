package twindow

import (
	"slices"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/pippellia-btc/smallset"
)

func TestIndexAdd(t *testing.T) {
	tests := []struct {
		name                string
		interval            Interval[string]
		inCurrent, inFuture bool
	}{
		{
			name:      "invalid interval, not indexed",
			interval:  Interval[string]{since: 11, until: 10, id: "a"},
			inCurrent: false, inFuture: false,
		},
		{
			name:      "until is too much into the past, not indexed",
			interval:  Interval[string]{since: 11, until: 12, id: "b"},
			inCurrent: false, inFuture: false,
		},
		{
			name:      "indexed into current",
			interval:  Interval[string]{since: time.Now().Unix(), until: time.Now().Add(+10 * time.Second).Unix(), id: "c"},
			inCurrent: true, inFuture: false,
		},
		{
			name:      "indexed into future",
			interval:  Interval[string]{since: time.Now().Add(+1000 * time.Second).Unix(), until: time.Now().Add(+10_000 * time.Second).Unix(), id: "d"},
			inCurrent: false, inFuture: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			index := New[string](512)
			index.add(test.interval)

			inCurrent := index.current.Contains(test.interval)
			if inCurrent != test.inCurrent {
				t.Fatalf("expected %v, got %v; current %v", test.inCurrent, inCurrent, index.current)
			}

			inFuture := index.future.Contains(test.interval)
			if inFuture != test.inFuture {
				t.Fatalf("expected %v, got %v; future %v", test.inFuture, inFuture, index.future)
			}
		})
	}
}

func TestIndexAdvance(t *testing.T) {
	tests := []struct {
		name                 string
		current, future      []Interval[string]
		expectedC, expectedF []Interval[string]
	}{
		{
			name:    "removal from current",
			current: []Interval[string]{{until: time.Now().Unix() - 100, id: "a"}}, future: []Interval[string]{},
			expectedC: []Interval[string]{}, expectedF: []Interval[string]{},
		},
		{
			name:    "from future to current",
			current: []Interval[string]{}, future: []Interval[string]{{since: time.Now().Unix() + 1, until: End, id: "a"}},
			expectedC: []Interval[string]{{since: time.Now().Unix() + 1, until: End, id: "a"}}, expectedF: []Interval[string]{},
		},
		{
			name: "multiple",
			current: []Interval[string]{
				{until: time.Now().Unix() - 10, id: "a"}, // will be removed
				{until: time.Now().Unix() - 10, id: "b"}, // will be removed
				{until: time.Now().Unix() + 100, id: "c"},
			},
			future: []Interval[string]{
				{since: time.Now().Unix() + 1, until: End, id: "x"}, // will go to current
				{since: time.Now().Unix() + 1000, until: End, id: "y"},
			},
			expectedC: []Interval[string]{
				{until: time.Now().Unix() + 100, id: "c"},
				{since: time.Now().Unix() + 1, until: End, id: "x"},
			},
			expectedF: []Interval[string]{
				{since: time.Now().Unix() + 1000, until: End, id: "y"},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			index := Index[string]{
				radius:  10,
				current: smallset.NewCustomFrom(sortByUntil[string], test.current...),
				future:  smallset.NewCustomFrom(sortBySince[string], test.future...),
			}
			index.advance()

			current := index.current.Items()
			if !slices.Equal(current, test.expectedC) {
				t.Errorf("expected current %v, got %v", test.expectedC, current)
			}

			future := index.future.Items()
			if !slices.Equal(future, test.expectedF) {
				t.Errorf("expected future %v, got %v", test.expectedF, future)
			}
		})
	}
}

func BenchmarkIndexAdd(b *testing.B) {
	idx := New[string](512)
	filter := nostr.Filter{
		Kinds: []int{1},
		Since: func() *nostr.Timestamp { t := nostr.Now(); return &t }(),
	}

	b.ResetTimer()
	for i := range b.N {
		id := "sub" + string(rune(i%1000))
		idx.Add(id, filter)
	}
}

func BenchmarkIndexRemove(b *testing.B) {
	idx := New[string](512)
	filter := nostr.Filter{
		Kinds: []int{1},
		Since: func() *nostr.Timestamp { t := nostr.Now(); return &t }(),
	}

	for i := range 1000 {
		id := "sub" + string(rune(i))
		idx.Add(id, filter)
	}

	b.ResetTimer()
	for i := range b.N {
		id := "sub" + string(rune(i%1000))
		idx.Remove(id, filter)
	}
}

func BenchmarkIndexCandidates(b *testing.B) {
	idx := New[string](512)
	filter := nostr.Filter{
		Kinds: []int{1},
		Since: func() *nostr.Timestamp { t := nostr.Now(); return &t }(),
	}

	for i := range 1000 {
		id := "sub" + string(rune(i))
		idx.Add(id, filter)
	}

	b.ResetTimer()
	for range b.N {
		idx.Candidates(nostr.Now())
	}
}
