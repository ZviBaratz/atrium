package theme

// The active theme is composed from two orthogonal axes: the color palette (a
// named registry theme) and the glyph set (a fidelity rung: nerd / plain / ascii).
// They are tracked separately so any palette can pair with any glyph set.
// Rendering is single-threaded on the bubbletea loop, so no locking is needed.
var (
	curName     = DefaultThemeName
	curGlyphSet = GlyphSetPlain // safe default: plain glyphs, never tofu on a bare terminal
	current     = compose()
)

// compose builds the active theme from the current palette + glyph-set selection.
// It copies the registry entry so it never mutates the shared palette theme.
func compose() *Theme {
	t := *Get(curName)
	t.Glyphs = glyphsFor(curGlyphSet)
	return &t
}

// Current returns the active theme. Never nil.
func Current() *Theme { return current }

// Set activates the named palette theme (falling back to the default for unknown
// names), preserving the current glyph-set selection, and returns a function that
// restores the previous selection. Startup ignores the return value; tests use it
// for cleanup:
//
//	defer theme.Set("unicode")()
func Set(name string) (restore func()) {
	prevName, prevSet := curName, curGlyphSet
	curName = name
	current = compose()
	return func() { curName, curGlyphSet = prevName, prevSet; current = compose() }
}

// SetGlyphSet selects the glyph-fidelity rung (GlyphSetNerd / GlyphSetPlain /
// GlyphSetASCII), preserving the current palette, and returns a restore function.
// An unrecognized value resolves to the plain rung (see glyphsFor).
func SetGlyphSet(set string) (restore func()) {
	prevName, prevSet := curName, curGlyphSet
	curGlyphSet = set
	current = compose()
	return func() { curName, curGlyphSet = prevName, prevSet; current = compose() }
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
