package ui

import (
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/ui/theme"
	"github.com/ZviBaratz/fresco/v2"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

// pinSplashLumRange drives the package's own ATRIUM_SPLASH_LUMRANGE state directly,
// restoring it when the test ends.
//
// t.Setenv cannot reach this knob: splash_variants.go resolves the variable in
// init(), so by the time any test body runs the read has already happened and
// setting the variable afterwards changes nothing. That is the same constraint
// TestLumRangeEnvReachesRender answers by spawning a subprocess — correct there,
// because its subject IS the env plumbing. Here the subject is the ladder that
// consumes the resolved value, so writing the resolved value is both cheaper and
// more direct. The vars are package-private and this is the same package; nothing
// is added to production for it.
//
// The lock is not ceremony: splashLumRangeOverride reads both vars under
// splashSelMu, and -race runs this package.
func pinSplashLumRange(t *testing.T, v float64, set bool) {
	t.Helper()
	splashSelMu.Lock()
	prevVal, prevSet := splashLumRangeVal, splashLumRangeSet
	splashLumRangeVal, splashLumRangeSet = v, set
	splashSelMu.Unlock()
	t.Cleanup(func() {
		splashSelMu.Lock()
		defer splashSelMu.Unlock()
		splashLumRangeVal, splashLumRangeSet = prevVal, prevSet
	})
}

// On a light palette the splash must not use fresco's luminance channel. Its ramp
// walks L* from a near-black floor UP to the hue (fresco's shade.go), so on a light
// field the dim cells come out as the darkest ink on screen — the vignette edge
// inverts into a halo. lumRange 0 short-circuits that ramp entirely and puts all
// brightness back on glyph density, which is directionally correct on either
// polarity.
//
// This asserts the resolved LumRange rather than the rendered field, because the
// rendered field is fresco's business and the decision is Atrium's.
func TestSplashLumRangeIsZeroOnALightPalette(t *testing.T) {
	pinSplashLumRange(t, 0, false) // no dev override: this is the shipped path

	t.Cleanup(theme.Set("tokyo-night"))
	require.Nil(t, splashLumRange(fresco.Tunnel),
		"on a dark palette Atrium must not override the variant's shipped lumRange")

	t.Cleanup(theme.Set("tokyo-night-day"))
	light := splashLumRange(fresco.Tunnel)
	require.NotNil(t, light, "a light palette must pin lumRange")
	require.Equal(t, 0.0, *light, "lumRange 0 is the endpoint that skips the ramp")

	// Both light palettes, not just the one: the rung keys on IsLight, so a
	// hardcoded theme name would pass this while covering half the case.
	t.Cleanup(theme.Set("catppuccin-latte"))
	latte := splashLumRange(fresco.Tunnel)
	require.NotNil(t, latte, "every light palette takes the same rung")
	require.Equal(t, 0.0, *latte)
}

// Rain is exempt from the light rung, and this is the guard that says so out loud.
//
// Rain's brightness is entirely luminance (fresco ships it at lumRange 1), so
// moving it to density leaves it nowhere to go: measured at 120x40 on
// tokyo-night-day, lumRange 0 inks 95% of cells with an edge:core ratio of 83:100 —
// a solid pane, no vignette — where leaving it alone gives 31% and 16:45. The light
// rung would otherwise have promoted a documented dev-only footgun into the shipped
// path for one launch in five. See fresco#82.
//
// Every OTHER variant must still take the rung, or an over-broad exemption would
// pass a test that only checked rain.
func TestSplashLumRangeExemptsRainOnALightPalette(t *testing.T) {
	pinSplashLumRange(t, 0, false)
	t.Cleanup(theme.Set("tokyo-night-day"))

	require.Nil(t, splashLumRange(fresco.Rain),
		"rain must keep its shipped lumRange on a light palette: at 0 the pane fills solid")

	for _, v := range fresco.Variants() {
		if v == fresco.Rain {
			continue
		}
		got := splashLumRange(v)
		require.NotNilf(t, got, "%v must still take the light rung", v)
		require.Equalf(t, 0.0, *got, "%v must still take the light rung", v)
	}
}

// Under NO_COLOR the splash takes the same rung a light palette does, for the
// mirror-image reason: with colour stripped, a brightness channel that spends
// itself on colour spends it on nothing, and the field flattens to a uniform wash.
// lumRange 0 puts brightness back on glyph density, which survives monochrome.
//
// The palette is pinned DARK so that only Mono can be the cause — on a light one
// the rung would fire anyway and the assertion would prove nothing.
func TestSplashLumRangeIsZeroUnderMono(t *testing.T) {
	pinSplashLumRange(t, 0, false) // no dev override: this is the shipped path
	t.Cleanup(theme.Set("tokyo-night"))

	require.Nil(t, splashLumRange(fresco.Tunnel),
		"with colour on, a dark palette must not override the variant's shipped lumRange")

	t.Cleanup(theme.SetMono(true))
	got := splashLumRange(fresco.Tunnel)
	require.NotNil(t, got, "NO_COLOR must pin lumRange even on a dark palette")
	require.Equal(t, 0.0, *got)
}

// Rain keeps its exemption under Mono, for the reason Stage C measured on light:
// its brightness is entirely luminance, so lumRange 0 leaves it nowhere to go and
// the pane fills solid (95% of cells inked, edge:core 83:100 — no vignette). That
// is true of rain whatever stripped the colour. A flat field is the lesser harm
// against a solid one, and fresco#82 is where it gets fixed properly.
//
// This is a NEGATIVE CONTROL, not a failing-first test: rain returns nil on a dark
// palette whether or not the Mono rung exists, so it is green before the change and
// green after the correct one. It goes red for exactly one edit — writing the rung
// as `Mono() || IsLight(…) && variant != Rain`, which Go parses as
// `Mono || (IsLight && …)` and puts rain back on lumRange 0 whenever NO_COLOR is
// set. The goldens cannot see that either: with Mono false both forms are
// identical. It was written wrong first and watched go red.
//
// Every OTHER variant must still take the rung, or an over-broad exemption would
// pass a test that only checked rain.
func TestSplashLumRangeExemptsRainUnderMono(t *testing.T) {
	pinSplashLumRange(t, 0, false)
	t.Cleanup(theme.Set("tokyo-night"))
	t.Cleanup(theme.SetMono(true))

	require.Nil(t, splashLumRange(fresco.Rain),
		"rain must keep its shipped lumRange under NO_COLOR: at 0 the pane fills solid")

	for _, v := range fresco.Variants() {
		if v == fresco.Rain {
			continue
		}
		got := splashLumRange(v)
		require.NotNilf(t, got, "%v must still take the mono rung", v)
		require.Equalf(t, 0.0, *got, "%v must still take the mono rung", v)
	}
}

// The dev override still wins, rain included. It is the knob used to tune a variant
// by eye, so a palette-derived default that silently ignored it would make the
// tuning loop lie — and rain is precisely the variant whose exemption someone would
// want to sweep past by hand.
func TestSplashLumRangeOverrideBeatsThePaletteDefault(t *testing.T) {
	t.Cleanup(theme.Set("tokyo-night-day"))
	pinSplashLumRange(t, 0.75, true)

	got := splashLumRange(fresco.Tunnel)
	require.NotNil(t, got)
	require.Equal(t, 0.75, *got, "the explicit override must beat the light-palette default")

	rain := splashLumRange(fresco.Rain)
	require.NotNil(t, rain, "the override must reach rain too, exemption notwithstanding")
	require.Equal(t, 0.75, *rain)
}

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
