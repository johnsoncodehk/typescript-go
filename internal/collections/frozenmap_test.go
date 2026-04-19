package collections_test

import (
	"maps"
	"slices"
	"testing"

	"github.com/microsoft/typescript-go/internal/collections"
)

func TestFrozenMapBasicLookup(t *testing.T) {
	t.Parallel()
	m := collections.NewFrozenMap(map[string]int{
		"alpha": 1,
		"beta":  2,
		"gamma": 3,
	})

	if v, ok := m.Get("alpha"); !ok || v != 1 {
		t.Errorf("Get(alpha) = (%d, %v), want (1, true)", v, ok)
	}
	if v, ok := m.Get("beta"); !ok || v != 2 {
		t.Errorf("Get(beta) = (%d, %v), want (2, true)", v, ok)
	}
	if v, ok := m.Get("gamma"); !ok || v != 3 {
		t.Errorf("Get(gamma) = (%d, %v), want (3, true)", v, ok)
	}
}

func TestFrozenMapMiss(t *testing.T) {
	t.Parallel()
	m := collections.NewFrozenMap(map[string]int{
		"alpha": 1,
	})

	if v, ok := m.Get("missing"); ok || v != 0 {
		t.Errorf("Get(missing) = (%d, %v), want (0, false)", v, ok)
	}
}

func TestFrozenMapGetOrZero(t *testing.T) {
	t.Parallel()
	m := collections.NewFrozenMap(map[string]int{
		"alpha": 1,
		"beta":  2,
	})

	if v := m.GetOrZero("alpha"); v != 1 {
		t.Errorf("GetOrZero(alpha) = %d, want 1", v)
	}
	if v := m.GetOrZero("missing"); v != 0 {
		t.Errorf("GetOrZero(missing) = %d, want 0", v)
	}
}

func TestFrozenMapHas(t *testing.T) {
	t.Parallel()
	m := collections.NewFrozenMap(map[string]int{
		"alpha": 1,
	})

	if !m.Has("alpha") {
		t.Error("Has(alpha) = false, want true")
	}
	if m.Has("missing") {
		t.Error("Has(missing) = true, want false")
	}
}

func TestFrozenMapLen(t *testing.T) {
	t.Parallel()
	m := collections.NewFrozenMap(map[string]int{
		"a": 1, "b": 2, "c": 3,
	})

	if m.Len() != 3 {
		t.Errorf("Len() = %d, want 3", m.Len())
	}
}

func TestFrozenMapEmpty(t *testing.T) {
	t.Parallel()
	m := collections.NewFrozenMap(map[string]int{})
	if m.Len() != 0 {
		t.Errorf("Len() = %d, want 0", m.Len())
	}
	if _, ok := m.Get("anything"); ok {
		t.Error("Get on empty map returned ok=true")
	}
}

func TestFrozenMapNil(t *testing.T) {
	t.Parallel()
	var m *collections.FrozenMap[int]
	if m.Len() != 0 {
		t.Errorf("nil Len() = %d, want 0", m.Len())
	}
	if _, ok := m.Get("anything"); ok {
		t.Error("Get on nil map returned ok=true")
	}
	if m.Has("anything") {
		t.Error("Has on nil map returned true")
	}
	// Verify iteration on nil map produces no entries.
	for range m.All() {
		t.Error("All on nil map yielded an entry")
	}
	for range m.Keys() {
		t.Error("Keys on nil map yielded a key")
	}
}

func TestFrozenMapAll(t *testing.T) {
	t.Parallel()
	input := map[string]int{
		"a": 1, "b": 2, "c": 3,
	}
	m := collections.NewFrozenMap(input)

	got := maps.Collect(m.All())

	if len(got) != len(input) {
		t.Errorf("All() returned %d entries, want %d", len(got), len(input))
	}
	for k, v := range input {
		if got[k] != v {
			t.Errorf("All() key %q = %d, want %d", k, got[k], v)
		}
	}

	// Verify early termination.
	count := 0
	for range m.All() {
		count++
		if count == 2 {
			break
		}
	}
	if count != 2 {
		t.Errorf("All() early break: got %d iterations, want 2", count)
	}
}

func TestFrozenMapKeys(t *testing.T) {
	t.Parallel()
	input := map[string]int{
		"x": 10, "y": 20, "z": 30,
	}
	m := collections.NewFrozenMap(input)

	keys := slices.Collect(m.Keys())
	if len(keys) != 3 {
		t.Errorf("Keys() returned %d keys, want 3", len(keys))
	}
	slices.Sort(keys)
	if keys[0] != "x" || keys[1] != "y" || keys[2] != "z" {
		t.Errorf("Keys() = %v, want [x y z]", keys)
	}

	// Verify early termination.
	count := 0
	for range m.Keys() {
		count++
		if count == 2 {
			break
		}
	}
	if count != 2 {
		t.Errorf("Keys() early break: got %d iterations, want 2", count)
	}
}

func TestFrozenMapLargeTable(t *testing.T) {
	t.Parallel()
	const n = 1000
	input := make(map[string]int, n)
	for i := range n {
		input["key_"+string(rune('A'+i%26))+string(rune('0'+i/26))] = i
	}
	m := collections.NewFrozenMap(input)

	if m.Len() != len(input) {
		t.Errorf("Len() = %d, want %d", m.Len(), len(input))
	}
	for k, v := range input {
		if got, ok := m.Get(k); !ok || got != v {
			t.Errorf("Get(%q) = (%d, %v), want (%d, true)", k, got, ok, v)
		}
	}
}

func BenchmarkFrozenMapGet(b *testing.B) {
	input := make(map[string]int, 100)
	keys := make([]string, 0, 100)
	for i := range 100 {
		k := "keyword_" + string(rune('a'+i%26)) + string(rune('a'+i/26))
		input[k] = i
		keys = append(keys, k)
	}

	b.Run("FrozenMap", func(b *testing.B) {
		m := collections.NewFrozenMap(input)
		b.ResetTimer()
		for i := range b.N {
			m.Get(keys[i%len(keys)])
		}
	})

	b.Run("GoMap", func(b *testing.B) {
		b.ResetTimer()
		for i := range b.N {
			_ = input[keys[i%len(keys)]]
		}
	})
}

func BenchmarkFrozenMapMiss(b *testing.B) {
	input := make(map[string]int, 100)
	for i := range 100 {
		k := "keyword_" + string(rune('a'+i%26)) + string(rune('a'+i/26))
		input[k] = i
	}
	misses := []string{"zzz", "missing", "nothere", "absent", "gone"}

	b.Run("FrozenMap", func(b *testing.B) {
		m := collections.NewFrozenMap(input)
		b.ResetTimer()
		for i := range b.N {
			m.Get(misses[i%len(misses)])
		}
	})

	b.Run("GoMap", func(b *testing.B) {
		b.ResetTimer()
		for i := range b.N {
			_ = input[misses[i%len(misses)]]
		}
	})
}
