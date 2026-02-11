// Package twindow provides a time-windowed index for efficiently matching timestamps
// against subscription filters with time bounds (since/until).
//
// The Index organizes interval filters into two categories:
//   - current: filters that intersect the dynamic time window [now - radius, now + radius]
//   - future: filters that don't intersect the time window but will in the future
//
// The working assumption is that the vast majority of broadcasted events will have
// a CreatedAt inside this window. Thanks to this assumption, we can reduce the number
// of candidates, which dramatically improves speed and memory usage.
package twindow

import (
	"cmp"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/pippellia-btc/smallset"
)

const (
	Beginning int64 = -1 << 63
	End       int64 = 1<<63 - 1
)

// Index organizes interval filters into current and future time windows.
// Its methods are not safe for concurrent use.
type Index[ID cmp.Ordered] struct {
	radius      int64
	lastAdvance int64
	current     *smallset.Custom[Interval[ID]]
	future      *smallset.Custom[Interval[ID]]
}

// New returns an Index using the dynamic time window [now - radius, now + radius].
func New[ID cmp.Ordered](radius int64) *Index[ID] {
	return &Index[ID]{
		radius:  radius,
		current: smallset.NewCustom(sortByUntil[ID], 1024),
		future:  smallset.NewCustom(sortBySince[ID], 1024),
	}
}

// Interval represents the since and until fields of a nostr.Filter,
// as well as an associated ID (typically a subscription ID).
type Interval[ID cmp.Ordered] struct {
	id           ID
	since, until int64
}

// isInvalid returns whether the interval's bounds are inverted.
func (i Interval[ID]) isInvalid() bool {
	return i.since > i.until
}

// sortBySince is a comparison function that sorts Intervals by their since.
// If they have the same since, then we compare them by their unique id to avoid
// incorrectly deduplicating them.
func sortBySince[ID cmp.Ordered](i1, i2 Interval[ID]) int {
	if i1.since < i2.since {
		return -1
	}
	if i1.since > i2.since {
		return 1
	}
	return cmp.Compare(i1.id, i2.id)
}

// sortByUntil is a comparison function that sorts Intervals by their until.
// If they have the same until, then we compare them by their unique id to avoid
// incorrectly deduplicating them.
func sortByUntil[ID cmp.Ordered](i1, i2 Interval[ID]) int {
	if i1.until < i2.until {
		return -1
	}
	if i1.until > i2.until {
		return 1
	}
	return cmp.Compare(i1.id, i2.id)
}

// NewInterval creates an Interval from a nostr.Filter and associated ID.
func NewInterval[ID cmp.Ordered](id ID, f nostr.Filter) Interval[ID] {
	i := Interval[ID]{
		id:    id,
		since: Beginning,
		until: End,
	}
	if f.Since != nil {
		i.since = int64(*f.Since)
	}
	if f.Until != nil {
		i.until = int64(*f.Until)
	}
	return i
}

// Size returns the total number of interval filters in current and future.
func (idx *Index[ID]) Size() int {
	return idx.current.Size() + idx.future.Size()
}

// Add a filter and associated ID to the Index.
func (idx *Index[ID]) Add(id ID, f nostr.Filter) {
	interval := NewInterval(id, f)
	idx.add(interval)
}

func (idx *Index[ID]) add(interval Interval[ID]) {
	if interval.isInvalid() {
		return
	}

	now := time.Now().Unix()
	min := now - idx.radius
	max := now + idx.radius

	if interval.until < min {
		// assumption: it's unlikely that events this old will be broadcasted,
		// so we simply don't index this filter.
		return
	}

	if interval.since > max {
		idx.future.Add(interval)
	} else {
		idx.current.Add(interval)
	}
}

// Remove the nostr filter with the associated ID from the Index.
func (idx *Index[ID]) Remove(id ID, f nostr.Filter) {
	interval := NewInterval(id, f)
	idx.remove(interval)
}

func (idx *Index[ID]) remove(interval Interval[ID]) {
	if interval.isInvalid() {
		// the interval wasn't indexed, so there is nothing to remove
		return
	}

	idx.current.Remove(interval)
	idx.future.Remove(interval)
}

// Candidates returns the set of interval IDs that are likely to match the event with the provided creation time.
// It returns whether it found any candidates.
func (idx *Index[ID]) Candidates(createdAt nostr.Timestamp) (*smallset.Ordered[ID], bool) {
	idx.advance()
	now := time.Now().Unix()
	min := now - idx.radius
	max := now + idx.radius

	if int64(createdAt) < min || int64(createdAt) > max {
		// fast path that avoids returning candidates that will likely be false-positives
		return nil, false
	}

	ids := idx.currentIDs()
	if len(ids) == 0 {
		return nil, false
	}

	return smallset.NewFrom(ids...), true
}

// advance moves the time window forward, migrating intervals from future to current
// and removing expired intervals from current.
func (idx *Index[ID]) advance() {
	now := time.Now().Unix()
	if now == idx.lastAdvance {
		// advance only once per second, as this is the "resolution" of the unix time
		return
	}

	idx.lastAdvance = now
	min := now - idx.radius
	max := now + idx.radius

	// move intervals from future to current.
	for _, interval := range idx.future.Ascend() {
		if interval.since > max {
			break
		}

		idx.current.Add(interval)
	}

	idx.future.RemoveBefore(Interval[ID]{since: max + 1})
	idx.current.RemoveBefore(Interval[ID]{until: min + 1})
}

// currentIDs returns a slice of all IDs in the current time window.
func (idx *Index[ID]) currentIDs() []ID {
	ids := make([]ID, idx.current.Size())
	for i, interval := range idx.current.Ascend() {
		ids[i] = interval.id
	}
	return ids
}
