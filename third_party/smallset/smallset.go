package smallset

import "sort"
import "reflect"

type Ordered[T comparable] struct {
	items []T
	index map[T]struct{}
}

func New[T comparable](cap int) *Ordered[T] {
	return &Ordered[T]{
		items: make([]T, 0, cap),
		index: make(map[T]struct{}, cap),
	}
}

func NewFrom[T comparable](vals ...T) *Ordered[T] {
	s := New[T](len(vals))
	for _, v := range vals {
		s.Add(v)
	}
	return s
}

func (s *Ordered[T]) Add(v T) {
	if s.index == nil {
		s.index = make(map[T]struct{})
	}
	if _, ok := s.index[v]; ok {
		return
	}
	s.index[v] = struct{}{}
	s.items = append(s.items, v)
}

func (s *Ordered[T]) Remove(v T) {
	if _, ok := s.index[v]; !ok {
		return
	}
	delete(s.index, v)
	for i, cur := range s.items {
		if cur == v {
			s.items = append(s.items[:i], s.items[i+1:]...)
			return
		}
	}
}

func (s *Ordered[T]) Items() []T {
	out := make([]T, len(s.items))
	copy(out, s.items)
	return out
}

func (s *Ordered[T]) Size() int { return len(s.items) }

func (s *Ordered[T]) Clear() {
	s.items = nil
	s.index = make(map[T]struct{})
}

func (s *Ordered[T]) IsEmpty() bool { return len(s.items) == 0 }

func Merge[T comparable](sets ...*Ordered[T]) *Ordered[T] {
	out := New[T](0)
	for _, set := range sets {
		if set == nil {
			continue
		}
		for _, v := range set.items {
			out.Add(v)
		}
	}
	return out
}

type Custom[T any] struct {
	less  func(a, b T) int
	items []T
}

func NewCustom[T any](less func(a, b T) int, cap int) *Custom[T] {
	return &Custom[T]{less: less, items: make([]T, 0, cap)}
}

func NewCustomFrom[T any](less func(a, b T) int, vals ...T) *Custom[T] {
	s := NewCustom(less, len(vals))
	for _, v := range vals {
		s.Add(v)
	}
	return s
}

func (s *Custom[T]) Add(v T) {
	if s.less == nil {
		s.items = append(s.items, v)
		return
	}
	i := sort.Search(len(s.items), func(i int) bool { return s.less(s.items[i], v) >= 0 })
	if i < len(s.items) && s.less(s.items[i], v) == 0 {
		return
	}
	s.items = append(s.items, v)
	copy(s.items[i+1:], s.items[i:])
	s.items[i] = v
}

func (s *Custom[T]) Contains(v T) bool {
	for _, cur := range s.items {
		if reflect.DeepEqual(cur, v) {
			return true
		}
	}
	return false
}

func (s *Custom[T]) Remove(v T) {
	if s.less == nil {
		for i, cur := range s.items {
			if any(cur) == any(v) {
				s.items = append(s.items[:i], s.items[i+1:]...)
				return
			}
		}
		return
	}
	for i, cur := range s.items {
		if s.less(cur, v) == 0 {
			s.items = append(s.items[:i], s.items[i+1:]...)
			return
		}
	}
}

func (s *Custom[T]) RemoveBefore(v T) {
	if s.less == nil {
		return
	}
	i := 0
	for i < len(s.items) && s.less(s.items[i], v) < 0 {
		i++
	}
	s.items = s.items[i:]
}

func (s *Custom[T]) Ascend() []T {
	out := make([]T, len(s.items))
	copy(out, s.items)
	return out
}

func (s *Custom[T]) Size() int { return len(s.items) }

func (s *Custom[T]) Items() []T { return s.Ascend() }
