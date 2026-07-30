package theme

import "sync/atomic"

// The active theme is composed from two orthogonal axes: the color palette (a
// named registry theme) and the glyph set (a fidelity rung: nerd / plain / ascii).
// They are tracked separately so any palette can pair with any glyph set.
//
// Selection (curName/curGlyphSet, and every Set*) happens on the bubbletea loop and
// stays lock-free. The composed result is an atomic pointer because it is also READ
// off that loop: the tmux bar push runs as a tea.Cmd — i.e. on its own goroutine —
// and reads Current() to colour the status band (see session/tmux/barstyle.go). A
// plain pointer there is a data race the race detector flags. Load/Store is a single
// word on the render hot path, so this costs nothing measurable.
var (
	curName     = DefaultThemeName
	curGlyphSet = GlyphSetPlain // safe default: plain glyphs, never tofu on a bare terminal
	current     atomic.Pointer[Theme]
)

func init() { current.Store(compose()) }

// compose builds the active theme from the current palette + glyph-set selection.
// It copies the registry entry so it never mutates the shared palette theme.
func compose() *Theme {
	t := *Get(curName)
	t.Glyphs = glyphsFor(curGlyphSet)
	return &t
}

// Current returns the active theme. Never nil. Safe to call from any goroutine; the
// returned *Theme is immutable once composed, so a reader keeps a consistent
// snapshot even if the selection changes underneath it.
func Current() *Theme { return current.Load() }

// Set activates the named palette theme (falling back to the default for unknown
// names), preserving the current glyph-set selection, and returns a function that
// restores the previous selection. Startup ignores the return value; tests use it
// for cleanup:
//
//	defer theme.Set("unicode")()
func Set(name string) (restore func()) {
	prevName, prevSet := curName, curGlyphSet
	curName = name
	current.Store(compose())
	return func() { curName, curGlyphSet = prevName, prevSet; current.Store(compose()) }
}

// SetGlyphSet selects the glyph-fidelity rung (GlyphSetNerd / GlyphSetPlain /
// GlyphSetASCII), preserving the current palette, and returns a restore function.
// An unrecognized value resolves to the plain rung (see glyphsFor).
func SetGlyphSet(set string) (restore func()) {
	prevName, prevSet := curName, curGlyphSet
	curGlyphSet = set
	current.Store(compose())
	return func() { curName, curGlyphSet = prevName, prevSet; current.Store(compose()) }
}

// SetNerdFont selects between the Nerd-Font and plain rungs — the two-rung view of
// the fidelity ladder, kept for callers and tests that only distinguish vendor
// icons from safe Unicode. It preserves the palette and returns a restore function.
func SetNerdFont(on bool) (restore func()) {
	if on {
		return SetGlyphSet(GlyphSetNerd)
	}
	return SetGlyphSet(GlyphSetPlain)
}
