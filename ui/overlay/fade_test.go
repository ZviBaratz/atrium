package overlay

import (
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/ui/theme"
)

// Background content behind a modal is repainted in the active theme's faint
// colors, not hardcoded greys. Each input form a terminal can emit — truecolor,
// 256-color, and simple SGR — must be rewritten, and resets must survive.
func TestFade_RewritesAllColorFormsToThemeColors(t *testing.T) {
	orig := theme.Current().Name
	t.Cleanup(func() { theme.Set(orig) })
	theme.Set("tokyo-night") // FgFaint #414868, Bg #1a1b26

	bgLines := []string{
		"\x1b[38;2;255;0;0mtruecolor fg\x1b[0m",
		"\x1b[48;5;21m256 bg\x1b[0m",
		"\x1b[31msimple red\x1b[0m",
		strings.Repeat("x", 20),
	}
	out := PlaceOverlay(0, 0, "FG", strings.Join(bgLines, "\n"), false)

	wantFg := "\x1b[38;2;65;72;104m" // #414868
	wantBg := "\x1b[48;2;26;27;38m"  // #1a1b26
	if !strings.Contains(out, wantFg) {
		t.Errorf("faded output missing theme faint-fg sequence %q", wantFg)
	}
	if !strings.Contains(out, wantBg) {
		t.Errorf("faded output missing theme bg sequence %q", wantBg)
	}
	for _, stale := range []string{"\x1b[38;2;255;0;0m", "\x1b[48;5;21m", "\x1b[31m", "\x1b[38;5;240m", "\x1b[48;5;236m"} {
		if strings.Contains(out, stale) {
			t.Errorf("faded output still contains pre-fade or legacy-grey sequence %q", stale)
		}
	}
	if !strings.Contains(out, "\x1b[0m") {
		t.Error("reset sequences must survive the fade")
	}
}

// The fade follows the active theme: switching themes switches the fade colors.
func TestFade_FollowsActiveTheme(t *testing.T) {
	orig := theme.Current().Name
	t.Cleanup(func() { theme.Set(orig) })

	bg := "\x1b[31mcolored\x1b[0m\n" + strings.Repeat("x", 10)

	theme.Set("catppuccin-mocha") // FgFaint #45475a
	out := PlaceOverlay(0, 0, "F", bg, false)
	if want := "\x1b[38;2;69;71;90m"; !strings.Contains(out, want) {
		t.Errorf("catppuccin fade missing %q", want)
	}

	theme.Set("tokyo-night")
	out = PlaceOverlay(0, 0, "F", bg, false)
	if want := "\x1b[38;2;65;72;104m"; !strings.Contains(out, want) {
		t.Errorf("tokyo-night fade missing %q", want)
	}
}

// TestFade_PreservesResetsInEitherSpelling states the fade's contract about
// resets in a form that survives the Lip Gloss v2 migration (#393).
//
// The sibling tests above assert exact bytes, including that "\x1b[0m" appears in
// the output. That is true of Lip Gloss v1's output and will stop being true:
// measured against lipgloss v2.0.5, a rendered style terminates with the implicit
// reset "\x1b[m" — an empty parameter — not "\x1b[0m". Colour sequences are
// unaffected (v2 emits the same semicolon-separated "38;2;R;G;B" and "38;5;N"
// forms the fade's regexes already match), so this is the one shape that moves.
//
// The fade happens to handle both already, and for a reason worth pinning rather
// than rediscovering: simpleColorRegex requires at least one digit, so "\x1b[m"
// never matches it and is passed through untouched, while "\x1b[0m" matches and is
// preserved by the explicit reset guard. Two different mechanisms, one behaviour.
// This asserts the behaviour, so whichever spelling the renderer emits after the
// cut, a background reset still reaches the terminal — and the byte-exact tests
// above can be re-baselined without leaving the contract unguarded.
func TestFade_PreservesResetsInEitherSpelling(t *testing.T) {
	orig := theme.Current().Name
	t.Cleanup(func() { theme.Set(orig) })
	theme.Set("tokyo-night")

	for _, reset := range []string{"\x1b[0m", "\x1b[m"} {
		bg := "\x1b[31mcoloured" + reset + "\n" + strings.Repeat("x", 12)
		out := PlaceOverlay(0, 0, "F", bg, false)

		if !strings.Contains(out, reset) {
			t.Errorf("fade dropped the reset %q — background styling would bleed past its span", reset)
		}
		if strings.Contains(out, "\x1b[31m") {
			t.Errorf("fade left the pre-fade colour in place alongside reset %q", reset)
		}
	}
}
