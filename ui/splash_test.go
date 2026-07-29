package ui

import (
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/ui/theme"
	"github.com/ZviBaratz/fresco"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

// TestSplashPalettesAreCanonicalHex validates that every registered theme maps to
// a fresco.Palette of canonical hex anchors, via fresco.Palette.Validate (the
// opt-in check added in the fresco #15–#19 API cluster). Atrium's palettes are
// compile-time constants, so fresco never rejects them at runtime — a bad anchor
// would silently degrade to fresco's documented fallback on screen. This test is
// where that surfaces instead: a theme-author typo in a splash token (Danger,
// Purple, Accent, Cyan, or Fg) fails here at CI rather than shipping a miscoloured
// field. Validate is stricter than the renderer's parser on purpose, so it also
// flags shorthands the renderer would still paint.
func TestSplashPalettesAreCanonicalHex(t *testing.T) {
	names := theme.Names()
	require.NotEmpty(t, names, "expected at least one registered theme")
	for _, name := range names {
		th := theme.Get(name)
		require.NoErrorf(t, splashPalette(th.Palette).Validate(),
			"theme %q: every splash anchor must be canonical hex", name)
	}
}

// stripLines strips SGR and splits into visible lines.
func stripLines(s string) []string {
	return strings.Split(ansi.Strip(s), "\n")
}

// overlayCenter drops fg onto the center of bg via the production overlayAt. A
// test-only convenience: the real render (splashScene) positions the wordmark
// and message explicitly, so nothing outside tests needs centering.
func overlayCenter(bg, fg string) string {
	fgLines, fgWidth := splashLines(fg)
	bgLines, bgWidth := splashLines(bg)
	return overlayAt(bg, fg, (bgWidth-fgWidth)/2, (len(bgLines)-len(fgLines))/2)
}

// TestOverlayIsOpaque is the fact the splash's whole text policy rests on: the
// text does not need the field cleared out from under it. overlayAt writes each
// overlaid line's cells wholesale — spaces included — so the text always covers
// its own footprint whatever the field draws underneath.
//
// This is why no variant takes a clearing. The splash carried one for a long
// time, and its name oversold it: it never prevented bleed-through, it only
// opened a margin of quiet *around* the text. That margin was charm on a field
// that faded into it and a defect on one that didn't — a band of missing streams
// with nothing drawn to account for them — and V5 retired the fields it flattered.
// If overlayAt ever became a fading or transparent composite, the field would
// start showing through the message's spaces and this policy would need
// revisiting.
func TestOverlayIsOpaque(t *testing.T) {
	bg := strings.Join([]string{
		strings.Repeat("#", 20),
		strings.Repeat("#", 20),
	}, "\n")
	// A foreground whose interior is a space: if overlays were transparent, the
	// background's # would survive in the middle.
	got := ansi.Strip(overlayAt(bg, "A B", 5, 0))
	first := strings.Split(got, "\n")[0]
	require.Equal(t, "#####A B############", first,
		"overlayAt must write the overlaid line's spaces over the background, not through it")
}

// TestBannerIsSolid pins the other half of that fact. The banner fills with ░
// rather than spaces, so it is opaque across its whole box on every row — there
// are no letter gaps for a field to show through even in principle. If a future
// banner introduced spaces, the field would start rendering inside the wordmark's
// counters and the no-clearing policy would need revisiting.
func TestBannerIsSolid(t *testing.T) {
	banner := ansi.Strip(trimBlankLines(FallbackBanner()))
	for i, line := range strings.Split(banner, "\n") {
		require.NotContainsf(t, line, " ",
			"banner row %d contains a space; the wordmark is assumed solid "+
				"(see TestOverlayIsOpaque)", i)
	}
}

// TestOverlayCenterComposites checks the fade-less compositor drops fg onto the
// center of the field while preserving the field's exact w×h bounds (the whole
// point of doing it before the #251 clamp).
func TestOverlayCenterComposites(t *testing.T) {
	w, h := 60, 20
	field := fresco.Render(w, h, 3, fresco.Options{
		Palette:  fresco.Palette{A0: "#f7768e", A1: "#bb9af7", A2: "#7aa2f7", A3: "#7dcfff", Highlight: "#c0caf5"},
		Variant:  fresco.Rain,
		FocalRow: (h - 1) / 2,
	})
	fg := "ABCDEF"
	out := overlayCenter(field, fg)
	require.Contains(t, ansi.Strip(out), "ABCDEF", "fg must survive compositing")
	lines := strings.Split(out, "\n")
	require.Len(t, lines, h, "compositing must preserve height")
	for i, l := range lines {
		require.LessOrEqualf(t, lipgloss.Width(l), w, "composited line %d width", i)
	}
}

// fieldGlyphs are glyphs that only the splash field emits — none appear in the
// wordmark art (box-drawing + ░), the panel border, or the onboarding message — so
// their presence in a stripped render proves the field engaged, and their absence
// proves the plain fallback did.
//
// It is the punctuation tail of fresco's shared density ramp (" .·:;+=*oO0@"),
// deliberately not the whole ramp. The ramp's alphanumeric rungs (o, O, 0) are
// excluded because the onboarding message is prose — "No agents running yet"
// already contains an 'o', which would satisfy the probe with the field switched
// off entirely. '·' and '.' and ':' are excluded for the same reason one level out:
// '·' is Atrium's own separator glyph. What is left cannot be produced by anything
// but the field.
//
// This used to be "@" alone, on the stated grounds that the tunnel — what TestMain
// pins — had lumRange 1 and so drew every lit cell as one full-weight mark. fresco
// v1.3.0 retuned the tunnel to lumRange 0.75 so the wall takes the glyph ramp
// (o → O → 0 → @) and reads as a textured surface; measured, it now emits '@' on
// exactly 0% of cells at every size these tests render, and all three probes failed.
// That is the coupling the old comment warned about, arriving from upstream rather
// than from a re-pin.
//
// The set above is chosen to retire that coupling rather than re-tighten it: every
// shipped variant emits at least one of these, measured at both 50×18 and 80×30
// (tunnel ~22% of cells, ripple ~25%, galaxy ~47%, aurora ~14%, rain ~2%), so
// re-pinning TestMain no longer requires moving this constant.
const fieldGlyphs = ";+=*@"

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
// floor the idle screen must fall back to the plain centered placeholder —
// bounded, panic-free, and with no field glyphs — never a clipped field. The
// placeholder keeps the wordmark only where it fits; narrower than its 48 cols it
// is the message alone (see fallbackBlock), which is why this asserts the field is
// gone rather than that the wordmark is present.
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
			"%dx%d: below the floor must render the plain placeholder, not the field", w, h)
	}
}

// disableSplash turns the animation off for one test and restores the previous
// setting afterwards. splashOn is process-wide state shared with every other test
// in this package (see the note above TestSplashSelectionConcurrent), so a test that
// left it off would silently blank the field for whatever ran next.
func disableSplash(t *testing.T) {
	t.Helper()
	prev := splashEnabled()
	SetSplashEnabled(false)
	t.Cleanup(func() { SetSplashEnabled(prev) })
}

// TestSplashEnabledDefaultsOn pins the initializer on splashOn. It is a bool, so
// the zero value is the *disabled* state: were the `= true` dropped, every launch
// that never reached SetSplashEnabled — and every test in this package — would
// render the plain wordmark, which reads as "the splash broke" rather than as a
// missing default.
func TestSplashEnabledDefaultsOn(t *testing.T) {
	require.True(t, splashEnabled(), "the splash must animate until something turns it off")
}

// TestSplashDisabledFallsBackToWordmark is the #316 contract on the two idle
// panes: with the animation off, a pane *well above* the size floor renders the
// plain centered placeholder — wordmark and message intact, no field.
//
// The size matters. TestPreviewSplashFallbackBelowFloor asserts the same absence
// below the floor, where the field is already gone for an unrelated reason, so a
// gate that never fired would still pass there. Every size here is one splashFits
// admits, which is the only place this can be proven.
func TestSplashDisabledFallsBackToWordmark(t *testing.T) {
	disableSplash(t)

	for _, s := range [][2]int{{50, 18}, {80, 30}, {120, 40}} {
		w, h := s[0], s[1]
		require.Truef(t, splashFits(w, h), "%dx%d must be above the floor for this test to mean anything", w, h)

		p := NewPreviewPane()
		p.SetSize(w, h)
		p.SetSplashFrame(6)
		require.NoError(t, p.UpdateContent(nil))
		require.Truef(t, p.previewState.splash, "%dx%d: the idle screen still flags the splash", w, h)

		out := p.String()
		lines := strings.Split(out, "\n")
		require.LessOrEqualf(t, len(lines), h, "%dx%d: too many lines", w, h)
		for i, l := range lines {
			require.LessOrEqualf(t, lipgloss.Width(l), w, "%dx%d: line %d width", w, h, i)
		}
		stripped := ansi.Strip(out)
		require.Falsef(t, strings.ContainsAny(stripped, fieldGlyphs),
			"%dx%d: a disabled splash must render the plain placeholder, not the field", w, h)
		require.Containsf(t, stripped, "No agents running yet", "%dx%d: the onboarding message must survive", w, h)
		require.Containsf(t, stripped, "█", "%dx%d: the wordmark must survive", w, h)
	}
}

// TestSplashDisabledLeavesTheScreensaverAnimating is the negative control for the
// gate's *placement*. splashScene is shared by the idle panes and by the
// screensaver, so the obvious simplification — one check inside splashScene —
// would pass every other test here while silently killing an easter egg that is
// an explicit keypress and out of the setting's scope (#316).
//
// Nothing else in the suite would notice, because nothing else renders the
// screensaver with the splash off. This is that test.
func TestSplashDisabledLeavesTheScreensaverAnimating(t *testing.T) {
	disableSplash(t)

	stripped := ansi.Strip(SplashScreensaver(80, 30, 7))
	require.True(t, strings.ContainsAny(stripped, fieldGlyphs),
		"the screensaver is an explicit keypress and must animate even with the idle splash off")
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
		"the screensaver and the guided empty state must not render identically")
}
