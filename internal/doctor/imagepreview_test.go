package doctor

import (
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCheckImagePreviewReadsTheEnvironment(t *testing.T) {
	got := CheckImagePreview([]string{
		"TERM=xterm-256color",
		"TERM_PROGRAM=ghostty",
		"malformed",
		"TMUX=/tmp/tmux-1000/atrium,123,0",
		"TERM=xterm-kitty",
	}, config.ImagePreviewAuto, true)

	assert.Equal(t, "xterm-kitty", got.Term, "a later duplicate wins")
	assert.Equal(t, "ghostty", got.TermProgram)
	assert.True(t, got.InTmux)
	assert.True(t, got.Recognized, "the environment does name a supported terminal")
	assert.False(t, got.Eligible, "but tmux vetoes it under auto")
	assert.False(t, got.Confirmed, "doctor is one-shot; it cannot wait for the reply")
}

// An empty TMUX is not something tmux ever sets, so it must not read as one —
// the same rule CheckKeyboard follows.
func TestCheckImagePreviewIgnoresAnEmptyTmuxVariable(t *testing.T) {
	got := CheckImagePreview([]string{"TMUX=", "TERM=xterm-kitty"}, config.ImagePreviewAuto, true)
	assert.False(t, got.InTmux)
	assert.True(t, got.Eligible)
}

// The four preferences resolve differently against the same environment, and the
// pairs that matter are auto-vs-kitty inside tmux (the documented override) and
// auto on a terminal nobody recognises.
func TestCheckImagePreviewResolvesEveryPreference(t *testing.T) {
	kitty := []string{"TERM=xterm-kitty"}
	// Unrecognised but TRUECOLOR, so these rows turn on recognition alone. Bare
	// xterm-256color is not truecolor, and using it here made two cases pass for
	// a reason they did not name — which is how the missing colour veto stayed
	// invisible in this file.
	plain := []string{"TERM=xterm-256color", "COLORTERM=truecolor"}
	inTmux := []string{"TERM=xterm-kitty", "TMUX=/tmp/s,1,0"}

	for _, tc := range []struct {
		name    string
		environ []string
		pref    string
		want    bool
	}{
		{"auto on kitty", kitty, config.ImagePreviewAuto, true},
		{"auto on an unrecognised terminal", plain, config.ImagePreviewAuto, false},
		{"auto inside tmux", inTmux, config.ImagePreviewAuto, false},
		{"kitty inside tmux is the documented override", inTmux, config.ImagePreviewKitty, true},
		{"kitty on an unrecognised terminal", plain, config.ImagePreviewKitty, true},
		{"glyph on kitty", kitty, config.ImagePreviewGlyph, false},
		{"off on kitty", kitty, config.ImagePreviewOff, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, CheckImagePreview(tc.environ, tc.pref, true).Eligible)
		})
	}
}

// Ghostty is recognised by its own variables as well as by TERM_PROGRAM, because
// a shell that lost TERM_PROGRAM still has these.
func TestCheckImagePreviewRecognisesGhosttyVariables(t *testing.T) {
	assert.True(t, CheckImagePreview(
		[]string{"GHOSTTY_BIN_DIR=/usr/bin"}, config.ImagePreviewAuto, true).Recognized)
	assert.False(t, CheckImagePreview(
		[]string{"GHOSTTY_BIN_DIR="}, config.ImagePreviewAuto, true).Recognized,
		"an empty value is not a terminal")
}

// The section must say eligible and never say it works. An eligible rung that
// reads as confirmed sends a user whose terminal is on the list — and who still
// sees glyphs — looking for the wrong fault.
func TestRenderImagePreviewNeverClaimsConfirmation(t *testing.T) {
	out := RenderImagePreview(CheckImagePreview([]string{"TERM=xterm-kitty"}, config.ImagePreviewAuto, true))

	require.Contains(t, out, "eligible")
	require.Contains(t, out, "not confirmed here",
		"an unreachable rung must be named as unreachable, not omitted")
	for _, forbidden := range []string{"supported", "works", "confirmed —"} {
		assert.NotContains(t, out, forbidden,
			"doctor cannot establish the rung, so it must not word it as established")
	}
}

// Each refusal names its own reason. A single generic "not attempted" would send
// a user to change terminals when what they actually did was set glyph.
func TestRenderImagePreviewNamesWhyItWillNotTry(t *testing.T) {
	for _, tc := range []struct {
		name    string
		environ []string
		pref    string
		want    string
	}{
		{"glyph", []string{"TERM=xterm-kitty"}, config.ImagePreviewGlyph, "set to glyph"},
		{"off", []string{"TERM=xterm-kitty"}, config.ImagePreviewOff, "only copies it"},
		{"tmux", []string{"TERM=xterm-kitty", "TMUX=/tmp/s,1,0"}, config.ImagePreviewAuto, "inside tmux"},
		{"unrecognised", []string{"TERM=alacritty", "COLORTERM=truecolor"}, config.ImagePreviewAuto, "no terminal Atrium"},
		{"not truecolor", []string{"TERM=xterm-256color"}, config.ImagePreviewAuto, "24-bit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := RenderImagePreview(CheckImagePreview(tc.environ, tc.pref, true))
			assert.Contains(t, out, tc.want)
			assert.NotContains(t, out, "eligible —", "this environment is not eligible")
		})
	}
}

func TestRenderImagePreviewNamesUnsetVariables(t *testing.T) {
	out := RenderImagePreview(CheckImagePreview(nil, config.ImagePreviewAuto, true))
	assert.Equal(t, 2, strings.Count(out, "unset"), "TERM and TERM_PROGRAM, and nothing else")
}

// The section has to sit beside the others without looking foreign: same header
// shape, same two-space rows, newline-terminated with no trailing blank (main.go
// owns the separators).
func TestRenderImagePreviewMatchesTheSectionConvention(t *testing.T) {
	out := RenderImagePreview(CheckImagePreview(nil, config.ImagePreviewAuto, true))

	require.False(t, strings.HasPrefix(out, "\n"), "the blank separator is main.go's job")
	require.True(t, strings.HasSuffix(out, "\n"), "sections are newline-terminated")
	require.False(t, strings.HasSuffix(out, "\n\n"), "no trailing blank line")

	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	require.Equal(t, "Image preview:", lines[0],
		"header is a bare Title-case line ending in a colon, like Host capacity:")
	for _, l := range lines[1:] {
		require.True(t, strings.HasPrefix(l, "  "), "row %q must be indented two spaces", l)
	}
}

// NO_COLOR is a veto in app.kittyEligible, and doctor has to agree.
//
// Reporting "eligible" to a user on kitty with NO_COLOR set tells the exact
// person this section exists for — on the list, still seeing glyphs — the
// opposite of the truth, and sends them looking for a fault in their terminal.
func TestCheckImagePreviewHonoursNoColor(t *testing.T) {
	kittyMono := []string{"TERM=xterm-kitty", "NO_COLOR=1"}

	got := CheckImagePreview(kittyMono, config.ImagePreviewAuto, true)
	assert.True(t, got.Mono)
	assert.True(t, got.Recognized, "the terminal is still recognised")
	assert.False(t, got.Eligible, "but the foreground the image ID rides in is stripped")

	// The explicit opt-in must not override it either — it is a drawability veto,
	// not a preference.
	assert.False(t, CheckImagePreview(kittyMono, config.ImagePreviewKitty, true).Eligible)

	// The negative control: the same terminal without NO_COLOR.
	assert.True(t, CheckImagePreview([]string{"TERM=xterm-kitty"}, config.ImagePreviewAuto, true).Eligible)

	out := RenderImagePreview(CheckImagePreview(kittyMono, config.ImagePreviewAuto, true))
	assert.Contains(t, out, "NO_COLOR")
	assert.NotContains(t, out, "eligible —")
}

// The other two drawability vetoes, which this section shipped without.
//
// Both are exactly the surprise it exists to explain: a user on kitty — on the
// list, TERM correct — who still sees glyphs because the TUI refused before it
// transmitted. Reporting "eligible" there is worse than printing nothing, since
// it sends them to look for a fault in their terminal.
//
// Neither is overridable by image_preview: kitty. The opt-in means "this
// terminal is not on your list", never "draw it wrong".
func TestCheckImagePreviewHonoursTheDrawabilityVetoes(t *testing.T) {
	t.Run("a profile below truecolor", func(t *testing.T) {
		// The image ID rides in the cell's 24-bit foreground, so a downsampling
		// profile does not dim the picture — it rewrites the address.
		//
		// Ghostty named by TERM_PROGRAM, because that is the one recognised
		// environment whose colour depth is still open: a TERM containing "kitty"
		// or "ghostty" is truecolor to colorprofile on its own, so it could not
		// reach this veto and the case would be vacuous.
		downsampled := []string{"TERM=xterm-256color", "TERM_PROGRAM=ghostty"}
		truecolor := append(append([]string{}, downsampled...), "COLORTERM=truecolor")

		got := CheckImagePreview(downsampled, config.ImagePreviewAuto, true)
		require.False(t, got.TrueColor, "the fixture must actually fail the check")
		assert.True(t, got.Recognized, "the terminal is still recognised")
		assert.False(t, got.Eligible, "but the foreground the image ID rides in is downsampled")
		assert.False(t, CheckImagePreview(downsampled, config.ImagePreviewKitty, true).Eligible,
			"and the explicit opt-in does not override a drawability veto")

		// The negative control: the same terminal, declaring truecolor.
		assert.True(t, CheckImagePreview(truecolor, config.ImagePreviewAuto, true).Eligible)

		out := RenderImagePreview(got)
		assert.Contains(t, out, "24-bit")
		assert.NotContains(t, out, "eligible —")
	})

	t.Run("a placeholder that measures two columns", func(t *testing.T) {
		kitty := []string{"TERM=xterm-kitty"}

		got := CheckImagePreview(kitty, config.ImagePreviewAuto, false)
		assert.False(t, got.OneCell)
		assert.True(t, got.Recognized)
		assert.False(t, got.Eligible, "every row of the picture would overflow the box")
		assert.False(t, CheckImagePreview(kitty, config.ImagePreviewKitty, false).Eligible)

		// The negative control.
		assert.True(t, CheckImagePreview(kitty, config.ImagePreviewAuto, true).Eligible)

		out := RenderImagePreview(got)
		assert.Contains(t, out, "two columns")
		assert.NotContains(t, out, "eligible —")
	})
}

// The unrecognised-terminal arrow must not promise that trying is free.
//
// "an unsupported terminal simply never answers and the glyphs stay" is true of
// a terminal with no graphics protocol and FALSE of the ones this arrow is for.
// A terminal that implements kitty graphics but not Unicode placeholders answers
// the transmission, so the upgrade fires and the picture becomes cells it cannot
// draw — permanently, because placeholder support is not probeable and there is
// no reply to fall back on. The arrow has to name the way out instead.
func TestRenderImagePreviewDoesNotPromiseAFreeAttempt(t *testing.T) {
	out := RenderImagePreview(CheckImagePreview(
		[]string{"TERM=alacritty", "COLORTERM=truecolor"}, config.ImagePreviewAuto, true))

	require.Contains(t, out, "image_preview: kitty", "the override is still offered")
	assert.NotContains(t, out, "never answers",
		"a graphics-capable terminal without placeholders DOES answer")
	assert.Contains(t, out, "image_preview: glyph", "so the way back has to be named")
}

// The tmux branch must not tell the user to turn on allow-passthrough.
//
// Measured: with passthrough on, an unwrapped payload still draws no reply,
// because tmux forwards only what is inside its own DCS envelope — which Atrium
// does not emit. Printing it as an actionable arrow sends a user to change two
// settings for no change at all.
func TestRenderImagePreviewDoesNotPromiseTmuxPassthrough(t *testing.T) {
	out := RenderImagePreview(CheckImagePreview(
		[]string{"TERM=xterm-kitty", "TMUX=/tmp/s,1,0"}, config.ImagePreviewAuto, true))

	assert.Contains(t, out, "inside tmux")
	assert.NotContains(t, out, "allow-passthrough",
		"Atrium does not wrap the payload, so passthrough changes nothing")
}
