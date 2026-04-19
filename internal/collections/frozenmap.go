package collections

import (
	"hash/maphash"
	"iter"
)

// FrozenMap is a compact, immutable hash map from strings to values of type V.
// It uses open addressing with linear probing for excellent cache locality,
// resulting in faster lookups and significantly lower memory usage compared
// to Go's built-in map for static data.
//
// Inspired by https://lemire.me/blog/2026/03/29/a-fast-immutable-map-in-go/
//
// A FrozenMap must be created via NewFrozenMap and cannot be modified after creation.
type FrozenMap[V any] struct {
	entries []frozenEntry[V]
	seed    maphash.Seed
	mask    int
	length  int
}

type frozenEntry[V any] struct {
	key   string
	value V
	hash  uint64 // 0 means empty slot
}

func nextPowerOf2(n int) int {
	n--
	n |= n >> 1
	n |= n >> 2
	n |= n >> 4
	n |= n >> 8
	n |= n >> 16
	n |= n >> 32
	n++
	return n
}

// NewFrozenMap creates a new FrozenMap from a Go map.
// The resulting FrozenMap is immutable and optimized for fast lookups.
func NewFrozenMap[V any](m map[string]V) *FrozenMap[V] {
	n := len(m)
	if n == 0 {
		return &FrozenMap[V]{}
	}

	tableSize := max(nextPowerOf2(n*2), 16)

	seed := maphash.MakeSeed()
	entries := make([]frozenEntry[V], tableSize)
	mask := tableSize - 1

	for k, v := range m {
		h := hashString(seed, k)
		idx := int(h) & mask
		for entries[idx].hash != 0 {
			idx = (idx + 1) & mask
		}
		entries[idx] = frozenEntry[V]{key: k, value: v, hash: h}
	}

	return &FrozenMap[V]{
		entries: entries,
		seed:    seed,
		mask:    mask,
		length:  n,
	}
}

// hashString hashes a string with the given seed, ensuring the result is never zero.
func hashString(seed maphash.Seed, s string) uint64 {
	h := maphash.String(seed, s)
	// Ensure non-zero so 0 can be used as empty sentinel.
	h |= 1
	return h
}

// Get looks up a key and returns its value and whether it was found.
func (m *FrozenMap[V]) Get(key string) (V, bool) {
	if m == nil || m.length == 0 {
		var zero V
		return zero, false
	}
	h := hashString(m.seed, key)
	idx := int(h) & m.mask
	for {
		e := &m.entries[idx]
		if e.hash == 0 {
			var zero V
			return zero, false
		}
		if e.hash == h && e.key == key {
			return e.value, true
		}
		idx = (idx + 1) & m.mask
	}
}

// GetOrZero looks up a key and returns its value, or the zero value of V if not found.
func (m *FrozenMap[V]) GetOrZero(key string) V {
	v, _ := m.Get(key)
	return v
}

// Has returns true if the key is present in the map.
func (m *FrozenMap[V]) Has(key string) bool {
	_, ok := m.Get(key)
	return ok
}

// Len returns the number of entries in the map.
func (m *FrozenMap[V]) Len() int {
	if m == nil {
		return 0
	}
	return m.length
}

// All returns an iterator over all key-value pairs in the map.
// The iteration order is not guaranteed.
func (m *FrozenMap[V]) All() iter.Seq2[string, V] {
	return func(yield func(string, V) bool) {
		if m == nil {
			return
		}
		for i := range m.entries {
			if m.entries[i].hash != 0 {
				if !yield(m.entries[i].key, m.entries[i].value) {
					return
				}
			}
		}
	}
}

// Keys returns an iterator over all keys in the map.
// The iteration order is not guaranteed.
func (m *FrozenMap[V]) Keys() iter.Seq[string] {
	return func(yield func(string) bool) {
		if m == nil {
			return
		}
		for i := range m.entries {
			if m.entries[i].hash != 0 {
				if !yield(m.entries[i].key) {
					return
				}
			}
		}
	}
}
