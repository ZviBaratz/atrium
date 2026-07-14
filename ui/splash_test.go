package ui

import (
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/ui/theme"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
	"github.com/stretchr/testify/require"
)

// splashTestPalette is a fixed truecolor palette (tokyo-night's hues) so the
// splash tests never depend on the global active theme.
func splashTestPalette() theme.Palette {
	return theme.Palette{
		Danger: lipgloss.Color("#f7768e"),
		Purple: lipgloss.Color("#bb9af7"),
		Accent: lipgloss.Color("#7aa2f7"),
		Cyan:   lipgloss.Color("#7dcfff"),
		Fg:     lipgloss.Color("#c0caf5"),
	}
}

// stripLines strips SGR and splits into visible lines.
func stripLines(s string) []string {
	return strings.Split(ansi.Strip(s), "\n")
}

// centeredClearing builds a single wordmark ellipse centered vertically in an
// h-row pane, so the field's focal center matches the pane center (keeping the
// round-vignette assertions valid).
func centeredClearing(h, halfW, halfH int) splashClearing {
	return splashClearing{wordHalfW: halfW, wordHalfH: halfH, wordCenterRow: (h - 1) / 2}
}

// overlayCenter drops fg onto the center of bg via the production overlayAt. A
// test-only convenience: the real render (renderSplashScene) positions the
// wordmark and message explicitly, so nothing outside tests needs centering.
func overlayCenter(bg, fg string) string {
	fgLines, fgWidth := splashLines(fg)
	bgLines, bgWidth := splashLines(bg)
	return overlayAt(bg, fg, (bgWidth-fgWidth)/2, (len(bgLines)-len(fgLines))/2)
}

// TestRenderSplashFieldDeterministic locks the pure-function contract: identical
// inputs must produce byte-identical output (so the field is snapshot-safe and
// the tick can drive it without hidden state).
func TestRenderSplashFieldDeterministic(t *testing.T) {
	pal := splashTestPalette()
	a := renderSplashField(80, 30, 5, pal, centeredClearing(30, 20, 4), splashDefaultVariant)
	b := renderSplashField(80, 30, 5, pal, centeredClearing(30, 20, 4), splashDefaultVariant)
	require.Equal(t, a, b, "same inputs must render identically")
	require.NotEmpty(t, a)
}

// TestRenderSplashFieldBounds is the view-bounds invariant: exactly h rows, each
// exactly w visible cells, across a spread of sizes (even/odd, wide/tall). This
// is what lets String() drop the field into the pane box without overflow (#251).
func TestRenderSplashFieldBounds(t *testing.T) {
	pal := splashTestPalette()
	sizes := [][2]int{{50, 18}, {66, 20}, {80, 30}, {120, 40}, {51, 19}, {73, 27}}
	for _, s := range sizes {
		w, h := s[0], s[1]
		field := renderSplashField(w, h, 3, pal, centeredClearing(h, w/4, h/6), splashDefaultVariant)
		lines := strings.Split(field, "\n")
		require.Lenf(t, lines, h, "%dx%d: line count", w, h)
		for i, l := range lines {
			require.Equalf(t, w, lipgloss.Width(l), "%dx%d: line %d width", w, h, i)
		}
	}
}

// TestRenderSplashFieldAnimates guards the drift wiring: advancing the frame must
// change the rendered field (otherwise the "slow drift" is dead).
func TestRenderSplashFieldAnimates(t *testing.T) {
	pal := splashTestPalette()
	f0 := renderSplashField(80, 30, 3, pal, centeredClearing(30, 20, 4), splashDefaultVariant)
	f1 := renderSplashField(80, 30, 4, pal, centeredClearing(30, 20, 4), splashDefaultVariant)
	require.NotEqual(t, f0, f1, "consecutive frames must differ")
}

// TestRenderSplashFieldVignetteCorners locks the edge vignette: the outermost
// rows and columns fade fully to blank at the pane border, so the full-bleed
// field softens into the edges instead of hard-clipping into a lit rectangle.
func TestRenderSplashFieldVignetteCorners(t *testing.T) {
	pal := splashTestPalette()
	w, h := 80, 30
	lines := stripLines(renderSplashField(w, h, 3, pal, centeredClearing(h, 20, 4), splashDefaultVariant))
	require.Len(t, lines, h)
	// The border rows fade to zero, so the first and last rows are entirely blank.
	require.Equal(t, strings.Repeat(" ", w), lines[0], "top row must be blank")
	require.Equal(t, strings.Repeat(" ", w), lines[h-1], "bottom row must be blank")
	// And each corner is blank regardless.
	for _, rc := range [][2]int{{0, 0}, {0, w - 1}, {h - 1, 0}, {h - 1, w - 1}} {
		require.Equalf(t, byte(' '), lines[rc[0]][rc[1]], "corner (%d,%d)", rc[0], rc[1])
	}
}

// TestRenderSplashFieldFillsPane guards the full-bleed fix: on a wide pane the
// field must span most of the width — not sit as a disc inscribed to the shorter
// (here vertical) axis, which would only reach ~half the width. It measures the
// horizontal span of lit cells (rune-indexed, since ramp glyphs like · are
// multi-byte).
func TestRenderSplashFieldFillsPane(t *testing.T) {
	pal := splashTestPalette()
	w, h := 120, 30
	lines := stripLines(renderSplashField(w, h, 3, pal, centeredClearing(h, 20, 4), splashDefaultVariant))
	minCol, maxCol := w, -1
	for _, l := range lines {
		for col, r := range []rune(l) {
			if r != ' ' {
				if col < minCol {
					minCol = col
				}
				if col > maxCol {
					maxCol = col
				}
			}
		}
	}
	require.GreaterOrEqual(t, maxCol, 0, "field must render some glyphs")
	span := maxCol - minCol
	require.Greaterf(t, span, int(float64(w)*0.7),
		"field must fill most of the width (span=%d of w=%d)", span, w)
}

// TestRenderSplashFieldClearing verifies the center clearing is blank, so the
// composited wordmark+message always lands on emptiness (never over field glyphs).
func TestRenderSplashFieldClearing(t *testing.T) {
	pal := splashTestPalette()
	w, h := 80, 30
	chw, chh := 20, 4
	lines := stripLines(renderSplashField(w, h, 3, pal, centeredClearing(h, chw, chh), splashDefaultVariant))
	centerRow := (h - 1) / 2
	cx := (w - 1) / 2
	// Rune-index the row: the field now fills the whole width, so multi-byte
	// glyphs (·) before the clearing shift byte offsets off the column.
	row := []rune(lines[centerRow])
	// Along the center row the clearing spans |dx| < chw — those cells are blank.
	for col := cx - (chw - 1); col <= cx+(chw-1); col++ {
		require.Equalf(t, ' ', row[col],
			"clearing cell (%d,%d) must be blank", centerRow, col)
	}
}

// TestOverlayCenterComposites checks the fade-less compositor drops fg onto the
// center of the field while preserving the field's exact w×h bounds (the whole
// point of doing it before the #251 clamp).
func TestOverlayCenterComposites(t *testing.T) {
	pal := splashTestPalette()
	w, h := 60, 20
	field := renderSplashField(w, h, 3, pal, centeredClearing(h, 8, 3), splashDefaultVariant)
	fg := "ABCDEF"
	out := overlayCenter(field, fg)
	require.Contains(t, ansi.Strip(out), "ABCDEF", "fg must survive compositing")
	lines := strings.Split(out, "\n")
	require.Len(t, lines, h, "compositing must preserve height")
	for i, l := range lines {
		require.LessOrEqualf(t, lipgloss.Width(l), w, "composited line %d width", i)
	}
}

// fieldGlyphs are ramp glyphs that only the splash field emits — none appear in
// the wordmark art (box-drawing + ░) or the onboarding message — so their
// presence in a stripped render proves the field engaged, and their absence
// proves the plain fallback did.
const fieldGlyphs = "·:*"

// TestPreviewSplashStringBounds drives the real idle path end to end
// (UpdateContent(nil) → setSplashState → String) and locks the #251 box
// contract at the String level: exactly h rows, each no wider than w, across a
// spread of sizes — with the wordmark and full onboarding message both present.
func TestPreviewSplashStringBounds(t *testing.T) {
	const msg = "No agents running yet"
	for _, s := range [][2]int{{50, 18}, {66, 20}, {80, 30}, {120, 40}, {51, 19}} {
		w, h := s[0], s[1]
		p := NewPreviewPane()
		p.SetSize(w, h)
		p.SetSplashFrame(6)
		require.NoError(t, p.UpdateContent(nil))
		require.True(t, p.previewState.splash, "%dx%d: idle screen must set splash", w, h)

		out := p.String()
		lines := strings.Split(out, "\n")
		require.Lenf(t, lines, h, "%dx%d: line count", w, h)
		for i, l := range lines {
			require.LessOrEqualf(t, lipgloss.Width(l), w, "%dx%d: line %d width", w, h, i)
		}
		stripped := ansi.Strip(out)
		require.Containsf(t, stripped, msg, "%dx%d: onboarding message must survive", w, h)
		require.Containsf(t, stripped, "█", "%dx%d: wordmark must survive", w, h)
		require.Truef(t, strings.ContainsAny(stripped, fieldGlyphs),
			"%dx%d: splash field must render behind the wordmark", w, h)
	}
}

// TestPreviewSplashFallbackBelowFloor guards the size gate: below the splashFits
// floor the idle screen must fall back to the plain centered wordmark — bounded,
// panic-free, and with no field glyphs — never a clipped field.
func TestPreviewSplashFallbackBelowFloor(t *testing.T) {
	for _, s := range [][2]int{{49, 18}, {50, 17}, {40, 12}, {49, 17}, {10, 4}} {
		w, h := s[0], s[1]
		p := NewPreviewPane()
		p.SetSize(w, h)
		p.SetSplashFrame(6)
		require.NoError(t, p.UpdateContent(nil))

		out := p.String()
		lines := strings.Split(out, "\n")
		require.LessOrEqualf(t, len(lines), h, "%dx%d: too many lines", w, h)
		for _, l := range lines {
			require.LessOrEqualf(t, lipgloss.Width(l), w, "%dx%d: line too wide", w, h)
		}
		require.Falsef(t, strings.ContainsAny(ansi.Strip(out), fieldGlyphs),
			"%dx%d: below the floor must render the plain wordmark, not the field", w, h)
	}
}

// TestRenderSplashFieldExtremes is the panic-safety net: degenerate sizes must
// return "" or a bounded block, never panic (the caller gates on splashFits, but
// the generator must be robust on its own).
func TestRenderSplashFieldExtremes(t *testing.T) {
	pal := splashTestPalette()
	sizes := [][2]int{{0, 0}, {1, 1}, {2, 2}, {1, 40}, {40, 1}, {50, 1}, {3, 3}, {51, 19}}
	for _, s := range sizes {
		w, h := s[0], s[1]
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("renderSplashField(%d,%d) panicked: %v", w, h, r)
				}
			}()
			out := renderSplashField(w, h, 2, pal, centeredClearing(h, maxInt(1, w/4), maxInt(1, h/4)), splashDefaultVariant)
			if out == "" {
				return
			}
			lines := strings.Split(out, "\n")
			require.LessOrEqualf(t, len(lines), h, "%dx%d: too many lines", w, h)
			for _, l := range lines {
				require.LessOrEqualf(t, lipgloss.Width(l), w, "%dx%d: line too wide", w, h)
			}
		}()
	}
}

// TestSplashLUTThemeAnchored locks the gradient to the theme: the ramp starts at
// the warm anchor (pink/Danger), ends at cyan, and the rim hue flows into the
// upper stops (so swapping cyan changes the colors). It asserts at the LUT level
// because lipgloss strips truecolor to plain text in a no-TTY test process, so
// the rendered field carries no color to compare.
func TestSplashLUTThemeAnchored(t *testing.T) {
	pal := splashTestPalette()
	lut := splashLUTFor(pal)
	require.Equal(t, pal.Danger, lut.colors[0], "core stop is the warm anchor")
	require.Equal(t, pal.Cyan, lut.colors[len(lut.colors)-1], "rim stop is theme cyan")

	other := pal
	other.Cyan = lipgloss.Color("#a6e3a1") // a distinctly different rim (catppuccin green)
	otherLUT := splashLUTFor(other)
	require.NotEqual(t, lut.colors, otherLUT.colors,
		"changing the rim hue must change the gradient")
	// The lower stops (warm→blue) are rim-independent; the upper stops must move.
	require.NotEqual(t, lut.colors[len(lut.colors)-4], otherLUT.colors[len(otherLUT.colors)-4],
		"the rim hue must reach the upper stops of the ramp")
}

// withColorProfile pins lipgloss's global color profile for one test and
// restores it afterward. The emitter's output is profile-dependent by
// construction, and a test binary's stdout is not a TTY — so the ambient
// profile is Ascii, and the SGR path never runs unless a test asks for it.
func withColorProfile(t *testing.T, prof termenv.Profile) {
	t.Helper()
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(prof)
	t.Cleanup(func() { lipgloss.SetColorProfile(prev) })
}

// TestSplashAffixBracketsMatchRender pins the invariant the whole emitter rests
// on: writing a style's cached prefix, then the cells, then its suffix must
// produce exactly what Style.Render would have produced for those cells.
// Splitting the SGR bracket out of lipgloss is only safe if the bracket really
// is a pure wrapper — for every stop, on every profile, including the colorless
// one where it has to collapse to plain text.
func TestSplashAffixBracketsMatchRender(t *testing.T) {
	profiles := map[string]termenv.Profile{
		"truecolor": termenv.TrueColor,
		"ansi256":   termenv.ANSI256,
		"ansi":      termenv.ANSI,
		"ascii":     termenv.Ascii,
	}
	// Spaces and braille included: the emitter brackets whatever the ramp
	// produced, and lipgloss has space-sensitive paths for some attributes.
	contents := []string{"x", "▓▓▓", "   ", "⠿⠿", "·-=#"}
	for name, prof := range profiles {
		t.Run(name, func(t *testing.T) {
			withColorProfile(t, prof)
			// Built directly rather than via splashLUTFor: this asserts on the
			// affixes themselves; TestSplashLUTCacheTracksColorProfile covers
			// the cache's keying.
			lut := buildSplashLUT(splashTestPalette())
			require.Equal(t, len(lut.styles), lut.starIndex(),
				"the star sentinel must sit exactly one past the last gradient stop")
			for _, content := range contents {
				for i, st := range lut.styles {
					require.Equal(t, st.Render(content),
						lut.affix[i].prefix+content+lut.affix[i].suffix,
						"stop %d must bracket %q exactly as Render does", i, content)
				}
				require.Equal(t, lut.star.Render(content),
					lut.starAffix.prefix+content+lut.starAffix.suffix,
					"the star style must bracket %q exactly as Render does", content)
			}
			if prof == termenv.Ascii {
				require.Empty(t, lut.affix[1].prefix, "a colorless profile must degrade to plain")
				require.Empty(t, lut.affix[1].suffix, "a colorless profile must degrade to plain")
			}
		})
	}
}

// TestSplashLUTCacheTracksColorProfile guards the trap that baking the affixes
// introduces. Style.Render re-read the color profile on every call, so a LUT
// cached under one profile still rendered correctly under the next; the affixes
// are frozen at build time, so the cache has to key on the profile or it pins
// whichever one happened to build the entry. That matters most in tests: a test
// binary starts out colorless, so a stale entry would silently turn any later
// truecolor assertion into a test of plain text.
func TestSplashLUTCacheTracksColorProfile(t *testing.T) {
	pal := splashTestPalette()
	pal.Purple = lipgloss.Color("#123456") // a private cache entry, so other tests are unaffected

	withColorProfile(t, termenv.Ascii)
	require.Empty(t, splashLUTFor(pal).affix[1].prefix, "a colorless profile emits no SGR")

	withColorProfile(t, termenv.TrueColor)
	require.NotEmpty(t, splashLUTFor(pal).affix[1].prefix,
		"a LUT cached under Ascii must not pin the colorless path once the profile is truecolor")
}

// TestRenderSplashFieldColorByProfile pins the emitted bytes end-to-end: a
// truecolor terminal gets SGR-bracketed runs, a colorless one gets the very
// same glyphs with no escapes at all.
func TestRenderSplashFieldColorByProfile(t *testing.T) {
	pal := splashTestPalette()
	render := func() string {
		return renderSplashField(60, 20, 3, pal, centeredClearing(20, 20, 4), splashVariantLegacy)
	}

	withColorProfile(t, termenv.TrueColor)
	colored := render()
	require.Contains(t, colored, "\x1b[38;2;", "a truecolor profile must emit truecolor SGR")

	withColorProfile(t, termenv.Ascii)
	plain := render()
	require.NotContains(t, plain, "\x1b[", "a colorless profile must emit no escapes")

	require.Equal(t, plain, ansi.Strip(colored),
		"color must be the only difference — the glyphs are identical either way")
}

// TestSplashFitsExported pins the exported gate the screensaver entry uses —
// the same floor as the internal splashFits.
func TestSplashFitsExported(t *testing.T) {
	require.True(t, SplashFits(minSplashW, minSplashH))
	require.False(t, SplashFits(minSplashW-1, minSplashH))
	require.False(t, SplashFits(minSplashW, minSplashH-1))
}

// TestSplashScreensaverScene pins the full-window easter-egg scene: exact row
// count, rows within the pane width, deterministic over (size, frame), and no
// message line — the field flows uninterrupted below the wordmark instead of
// being blanked for guidance text nobody passed.
func TestSplashScreensaverScene(t *testing.T) {
	const w, h = 80, 30
	out := SplashScreensaver(w, h, 7)
	require.Equal(t, out, SplashScreensaver(w, h, 7), "same frame must render identically")

	lines := stripLines(out)
	require.Len(t, lines, h)
	for i, ln := range lines {
		require.LessOrEqual(t, lipgloss.Width(ln), w, "row %d overflows the window", i)
	}

	withMsg := splashScene(w, h, 7, "press n to start")
	require.NotContains(t, ansi.Strip(out), "press n", "the screensaver has no message line")
	require.NotEqual(t, out, withMsg,
		"dropping the message must also drop its clearing, not just its text")
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func BenchmarkRenderSplash(b *testing.B) {
	pal := splashTestPalette()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = renderSplashField(80, 30, i, pal, centeredClearing(30, 20, 4), splashDefaultVariant)
	}
}
