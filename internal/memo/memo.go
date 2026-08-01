// Package memo is the one-entry render memo Atrium's frame build is layered on.
//
// It exists because ~70% of a frame build is lipgloss re-measuring text (#565):
// every composition layer — pad, align, border, JoinVertical, JoinHorizontal,
// Place — calls getLines, which calls ansi.StringWidth on every line it touches,
// and a pane is wrapped by six of them on its way into the frame. Measured on a
// 14-session frame, that is ~26 full-frame-equivalent width passes per View().
//
// The way out is not to make a pass cheaper but to skip the layer. A Cache does
// that, and the safety argument is the one #561 made for zone.Scan: reusing the
// previous output is correct exactly when compose is a PURE FUNCTION of the key.
// So every call site keys on the strings it composes plus the handful of scalars
// its own body reads — never on model state, which is the shape that silently
// serves a stale frame when someone adds a field and forgets the key.
package memo

// Enabled switches memoization off process-wide. It is the seam that lets a test
// render the same model twice, once with the caches live and once without, and
// require the two frames byte-identical — the net that catches an input nobody
// enumerated in a key.
//
// A package var rather than a Config field: this is a test seam, not a setting
// (the diffContentFloor idiom in app/app_poll.go). It is read on the render path,
// which is main-thread only, so flip it around a synchronous render and restore
// it with the returned function; nothing in app or ui runs tests in parallel.
var Enabled = true

// SetEnabled sets Enabled and returns a function restoring the previous value,
// mirroring theme.Set so the flip is always paired with its undo:
//
//	defer memo.SetEnabled(false)()
func SetEnabled(on bool) (restore func()) {
	prev := Enabled
	Enabled = on
	return func() { Enabled = prev }
}

// Cache memoizes one string against one comparable key. The zero value is an
// empty cache, so it is usable as a struct field with no constructor.
//
// Not safe for concurrent use, deliberately: every user is a Bubble Tea View()
// call, which Atrium only ever makes from the update loop.
type Cache[K comparable] struct {
	key   K
	out   string
	valid bool
	runs  int
}

// Get returns the memoized output for k, calling compose only when k differs from
// the key the cached output was built for (or when nothing is cached yet).
//
// The stored key is compared with ==, so K must not contain a field whose equality
// is looser than the render's dependence on it. Slices and maps are not comparable
// and will not compile, which is the point: a key that cannot be compared exactly
// is a key that cannot prove the output still applies.
func (c *Cache[K]) Get(k K, compose func() string) string {
	if Enabled && c.valid && c.key == k {
		return c.out
	}
	out := compose()
	c.key, c.out, c.valid = k, out, true
	c.runs++
	return out
}

// Runs reports how many times compose has actually run since the last Reset. It is
// the counting seam the memo's own tests need: without it, "the second render was
// served from the cache" is asserted by a test that passes just as well against a
// renderer that never ran at all.
func (c *Cache[K]) Runs() int { return c.runs }

// Reset drops the cached entry and zeroes the run count. It is both the cold path
// for a benchmark — a cache silently turns its own benchmark into a measurement of
// itself — and the zero point for a test that counts composes.
func (c *Cache[K]) Reset() {
	var zero K
	c.key, c.out, c.valid, c.runs = zero, "", false, 0
}
