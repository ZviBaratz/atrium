package ui

import (
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/ui/theme"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
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
	a := renderSplashField(80, 30, 5, pal, centeredClearing(30, 20, 4))
	b := renderSplashField(80, 30, 5, pal, centeredClearing(30, 20, 4))
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
		field := renderSplashField(w, h, 3, pal, centeredClearing(h, w/4, h/6))
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
	f0 := renderSplashField(80, 30, 3, pal, centeredClearing(30, 20, 4))
	f1 := renderSplashField(80, 30, 4, pal, centeredClearing(30, 20, 4))
	require.NotEqual(t, f0, f1, "consecutive frames must differ")
}

// TestRenderSplashFieldVignetteCorners locks the edge vignette: the outermost
// rows and columns fade fully to blank at the pane border, so the full-bleed
// field softens into the edges instead of hard-clipping into a lit rectangle.
func TestRenderSplashFieldVignetteCorners(t *testing.T) {
	pal := splashTestPalette()
	w, h := 80, 30
	lines := stripLines(renderSplashField(w, h, 3, pal, centeredClearing(h, 20, 4)))
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
	lines := stripLines(renderSplashField(w, h, 3, pal, centeredClearing(h, 20, 4)))
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
	lines := stripLines(renderSplashField(w, h, 3, pal, centeredClearing(h, chw, chh)))
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
	field := renderSplashField(w, h, 3, pal, centeredClearing(h, 8, 3))
	fg := "ABCDEF"
	out := overlayCenter(field, fg)
	require.Contains(t, ansi.Strip(out), "ABCDEF", "fg must survive compositing")
	lines := strings.Split(out, "\n")
	require.Len(t, lines, h, "compositing must preserve height")
	for i, l := range lines {
		require.LessOrEqualf(t, lipgloss.Width(l), w, "composited line %d width", i)
	}
}

// fieldGlyphs are ramp glyphs that only the ripple field emits — none appear in
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
			"%dx%d: ripple field must render behind the wordmark", w, h)
	}
}

// TestPreviewSplashFallbackBelowFloor guards the size gate: below the splashFits
// floor the idle screen must fall back to the plain centered wordmark — bounded,
// panic-free, and with no field glyphs — never a clipped ripple.
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
			out := renderSplashField(w, h, 2, pal, centeredClearing(h, maxInt(1, w/4), maxInt(1, h/4)))
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
		_ = renderSplashField(80, 30, i, pal, centeredClearing(30, 20, 4))
	}
}
