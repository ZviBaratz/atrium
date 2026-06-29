package theme

// The active theme is composed from two orthogonal axes: the color palette (a
// named registry theme) and the glyph set (plain vs Nerd-Font). They are tracked
// separately so any palette can pair with either glyph set. Rendering is
// single-threaded on the bubbletea loop, so no locking is needed.
var (
	curName = DefaultThemeName
	curNerd = false // safe default: plain glyphs, never tofu on a bare terminal
	current = compose()
)

// compose builds the active theme from the current palette + glyph-set selection.
// It copies the registry entry so it never mutates the shared palette theme.
func compose() *Theme {
	t := *Get(curName)
	t.Glyphs = glyphsFor(curNerd)
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
	prevName, prevNerd := curName, curNerd
	curName = name
	current = compose()
	return func() { curName, curNerd = prevName, prevNerd; current = compose() }
}

// SetNerdFont selects the glyph set — vendor Nerd-Font icons when on, plain Unicode
// when off — preserving the current palette, and returns a restore function.
func SetNerdFont(on bool) (restore func()) {
	prevName, prevNerd := curName, curNerd
	curNerd = on
	current = compose()
	return func() { curName, curNerd = prevName, prevNerd; current = compose() }
}
