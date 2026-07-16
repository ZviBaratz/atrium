package splash

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
)

// splashTestPalette is a fixed truecolor palette (tokyo-night's hues) so the
// splash tests never depend on a live theme. The comments name the theme token
// each anchor maps from (see ui.splashPalette).
func splashTestPalette() Palette {
	return Palette{
		A0:        "#f7768e", // Danger
		A1:        "#bb9af7", // Purple
		A2:        "#7aa2f7", // Accent
		A3:        "#7dcfff", // Cyan
		Highlight: "#c0caf5", // Fg
	}
}

// testLumRange, when non-nil, overrides the variant's lumRange in the render
// shims below. It is the test-package replacement for the ui-side
// ATRIUM_SPLASH_LUMRANGE global that withLumRange used to drive: the engine
// reads no environment, so the shade tests thread the override through Options.
var testLumRange *float64

// renderSplashField wraps Render with the pre-extraction signature the tests are
// written against: an explicit focal row and the variant's shipped lumRange,
// unless withLumRange has pinned an override.
func renderSplashField(w, h, frame int, pal Palette, focalRow int, v Variant) string {
	return Render(w, h, frame, Options{Palette: pal, Variant: v, FocalRow: focalRow, LumRange: testLumRange})
}

// withLumRange pins the dev lumRange override for a test or benchmark and
// restores it after, mirroring withColorProfile. Benchmarks need it too, which
// is why the override rides a plain var rather than resolving once.
func withLumRange(tb testing.TB, r float64) {
	tb.Helper()
	prev := testLumRange
	rr := r
	testLumRange = &rr
	tb.Cleanup(func() { testLumRange = prev })
}

// splashTestVariants enumerates every variant for contract loops, with names
// for failure messages. Hand-maintained — TestSplashTestVariantsCoversEnum is
// what keeps it honest.
func splashTestVariants() map[string]Variant {
	return map[string]Variant{
		"rain":   Rain,
		"tunnel": Tunnel,
		"ripple": Ripple,
	}
}

// stripLines strips SGR and splits into visible lines.
func stripLines(s string) []string {
	return strings.Split(ansi.Strip(s), "\n")
}

// centeredFocalRow is the focal row for a pane whose wordmark sits at its
// vertical centre — which is what ui's splashScene builds, and what keeps the
// round-vignette assertions valid.
func centeredFocalRow(h int) int { return (h - 1) / 2 }

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

// forceBenchTrueColor pins the color profile for a benchmark so the SGR path
// runs, and asserts it took — a silent degrade would turn every number the
// benchmark reports into a measurement of the wrong code.
func forceBenchTrueColor(b *testing.B) {
	b.Helper()
	prof := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	b.Cleanup(func() { lipgloss.SetColorProfile(prof) })

	// The tunnel is the canary because it cannot be blank: every cell of its wall
	// is lit at every frame. A sparse field could emit no SGR at frame 1 for an
	// honest reason and fail this for the wrong one.
	out := renderSplashField(80, 30, 1, splashTestPalette(), centeredFocalRow(30), Tunnel)
	if !strings.Contains(out, "\x1b[38;2;") {
		b.Fatal("no truecolor SGR emitted: benchmark would measure the colorless path")
	}
}
