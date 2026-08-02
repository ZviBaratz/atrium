package theme

import "sync/atomic"

// The active theme is composed from three orthogonal axes: the color palette (a
// named registry theme), the glyph set (a fidelity rung: nerd / plain / ascii), and
// the scheme (the terminal's detected background polarity, consulted only when the
// palette selection is AutoThemeName). They are tracked separately so any palette
// can pair with any glyph set, and so detection cannot move a palette the user
// named explicitly.
//
// Selection (curName/curGlyphSet, and every Set*) happens on the bubbletea loop and
// stays lock-free. The composed result is an atomic pointer because it is also READ
// off that loop: the tmux bar push runs as a tea.Cmd — i.e. on its own goroutine —
// and reads Current() to colour the status band (see session/tmux/barstyle.go). A
// plain pointer there is a data race the race detector flags. Load/Store is a single
// word on the render hot path, so this costs nothing measurable.
//
// curScheme is atomic for a third reason, neither of the above. Nothing reads it off
// the loop today — barStyleColours reaches Current() and Mono() and never this — so
// on the letter of the rule it could be a plain var like curName. But that safety is
// an invariant of the CALL SITE, not of the type, and CurrentScheme() is exported
// directly beside Current(), which promises any goroutine. A pair of neighbouring
// getters with opposite concurrency contracts is a footgun one word closes; `mono`
// went through this exact transition in review.
var (
	curName     = DefaultThemeName
	curGlyphSet = GlyphSetPlain // safe default: plain glyphs, never tofu on a bare terminal
	curScheme   atomic.Int32    // a Scheme; the zero value is SchemeUnknown, on purpose
	current     atomic.Pointer[Theme]
)

func init() { current.Store(compose()) }

// compose builds the active theme from the current palette + glyph-set + scheme
// selection. It copies the registry entry so it never mutates the shared palette
// theme.
//
// AutoThemeName is resolved here rather than being a registry entry, because Get
// must return a concrete eighteen-token palette and `auto` has none — an `auto`
// entry would have to hold a fiction, which the canonical-hex and contrast oracles
// would then dutifully validate. Resolving it here is also what makes AC#4
// structural: this is the only place curScheme is read TO SELECT A PALETTE, and the
// read sits behind the AutoThemeName branch, so a named theme cannot follow the
// terminal no matter what detection reports.
//
// Not the only read of curScheme, which is the distinction that matters if you are
// checking the claim: CurrentScheme() reads it too, and app/scheme.go's
// applyDetectedScheme calls that to answer "has the polarity changed since last
// time". That read chooses nothing — it decides whether to re-theme at all — so it
// cannot route a named palette anywhere. A future read that DOES pick a colour
// belongs in here, beside this branch, or AC#4 stops being structural.
func compose() *Theme {
	name := curName
	if name == AutoThemeName {
		name = DefaultThemeName
		if Scheme(curScheme.Load()) == SchemeLight {
			if twin, ok := lightTwin[name]; ok {
				name = twin
			}
		}
	}
	t := *Get(name)
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

// SetScheme records the terminal's detected background polarity and recomposes,
// preserving the palette and glyph-set selections, and returns a function that
// restores the previous scheme. It has no effect on what is rendered unless the
// palette selection is AutoThemeName.
//
// It restores only its own axis. Set and SetGlyphSet each snapshot and restore both
// of theirs, and adding a third to those two — or a palette to this one — is how a
// restore starts clobbering a sibling: a detected scheme is not a theme change's to
// undo.
func SetScheme(s Scheme) (restore func()) {
	prev := curScheme.Swap(int32(s))
	current.Store(compose())
	return func() { curScheme.Store(prev); current.Store(compose()) }
}

// CurrentScheme reports the scheme most recently recorded by SetScheme. Safe to
// call from any goroutine, like Current(); see the note on curScheme above.
func CurrentScheme() Scheme { return Scheme(curScheme.Load()) }

// SetNerdFont selects between the Nerd-Font and plain rungs — the two-rung view of
// the fidelity ladder, kept for callers and tests that only distinguish vendor
// icons from safe Unicode. It preserves the palette and returns a restore function.
func SetNerdFont(on bool) (restore func()) {
	if on {
		return SetGlyphSet(GlyphSetNerd)
	}
	return SetGlyphSet(GlyphSetPlain)
}
