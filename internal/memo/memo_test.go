package memo

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// key is a two-field key, so the tests below can change one field at a time and
// prove the comparison is over the whole struct rather than whichever field a
// single-field key would have made unfalsifiable.
type key struct {
	content string
	width   int
}

func TestCache_RepeatedKeyComposesOnce(t *testing.T) {
	var c Cache[key]
	k := key{content: "hello", width: 10}

	for range 5 {
		require.Equal(t, "composed", c.Get(k, func() string { return "composed" }))
	}

	require.Equal(t, 1, c.Runs(), "five Gets on one key must compose once")
}

// The negative control for the test above, which would otherwise pass against a
// cache that never invalidates.
func TestCache_ChangedKeyRecomposes(t *testing.T) {
	var c Cache[key]
	n := 0
	compose := func() string { n++; return "out" }

	c.Get(key{content: "a", width: 10}, compose)
	c.Get(key{content: "b", width: 10}, compose) // content moved
	c.Get(key{content: "b", width: 11}, compose) // width moved

	require.Equal(t, 3, n, "each distinct key must compose")
	require.Equal(t, 3, c.Runs())
}

// A key that comes back around still recomposes: the cache holds ONE entry, not a
// map. Stated as a test because the call sites' hit rates depend on it — a
// tabbed window that alternates between two states hits nothing.
func TestCache_HoldsOneEntry(t *testing.T) {
	var c Cache[key]
	n := 0
	compose := func() string { n++; return "out" }

	a, b := key{content: "a"}, key{content: "b"}
	c.Get(a, compose)
	c.Get(b, compose)
	c.Get(a, compose)

	require.Equal(t, 3, n, "the earlier key is evicted, not remembered")
}

// The zero key is a real key, and it is the one the valid flag exists for: a fresh
// cache — and a Reset one — holds the zero K with an empty output, so without the
// flag the first Get for a zero-valued key would be served "" instead of composing.
//
// It is reachable. List.String builds panelKey{} for an unsized, empty list, and a
// component that returned "" there rather than rendering would be a blank panel
// nothing ever repainted.
func TestCache_ZeroKeyComposesWhenNothingIsCached(t *testing.T) {
	var c Cache[key]
	var zero key

	require.Equal(t, "composed", c.Get(zero, func() string { return "composed" }),
		"a fresh cache must compose for the zero key, not serve its empty entry")
	require.Equal(t, 1, c.Runs())

	c.Reset()
	require.Equal(t, "again", c.Get(zero, func() string { return "again" }),
		"and so must a Reset one")
}

func TestCache_ResetDropsTheEntryAndTheCount(t *testing.T) {
	var c Cache[key]
	k := key{content: "a"}
	n := 0
	compose := func() string { n++; return "out" }

	c.Get(k, compose)
	c.Get(k, compose)
	require.Equal(t, 1, n)
	require.Equal(t, 1, c.Runs())

	c.Reset()
	c.Get(k, compose)

	require.Equal(t, 2, n, "Reset must force the next Get to compose")
	require.Equal(t, 1, c.Runs(), "Reset must zero the run count too")
}

// With Enabled off every Get composes, which is what makes the frame-equivalence
// table in package app possible.
func TestCache_DisabledAlwaysComposes(t *testing.T) {
	defer SetEnabled(false)()

	var c Cache[key]
	k := key{content: "a"}
	n := 0
	compose := func() string { n++; return "out" }

	for range 4 {
		c.Get(k, compose)
	}

	require.Equal(t, 4, n, "a disabled cache must never serve a stored entry")
}

// Re-enabling serves the entry the disabled run stored, rather than a stale one
// from before it. Worth pinning: Get stores unconditionally, so a disabled window
// still keeps the cache current instead of leaving a value the model has moved past.
func TestCache_DisabledStillStoresSoReEnablingIsCurrent(t *testing.T) {
	var c Cache[key]

	c.Get(key{content: "old"}, func() string { return "old-out" })

	restore := SetEnabled(false)
	c.Get(key{content: "new"}, func() string { return "new-out" })
	restore()

	n := 0
	got := c.Get(key{content: "new"}, func() string { n++; return "recomposed" })

	require.Equal(t, "new-out", got)
	require.Zero(t, n, "the entry stored while disabled must still be a hit")
}
