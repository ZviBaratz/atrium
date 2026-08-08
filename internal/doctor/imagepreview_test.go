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
	}, config.ImagePreviewAuto)

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
	got := CheckImagePreview([]string{"TMUX=", "TERM=xterm-kitty"}, config.ImagePreviewAuto)
	assert.False(t, got.InTmux)
	assert.True(t, got.Eligible)
}

// The four preferences resolve differently against the same environment, and the
// pairs that matter are auto-vs-kitty inside tmux (the documented override) and
// auto on a terminal nobody recognises.
func TestCheckImagePreviewResolvesEveryPreference(t *testing.T) {
	kitty := []string{"TERM=xterm-kitty"}
	plain := []string{"TERM=xterm-256color"}
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
			assert.Equal(t, tc.want, CheckImagePreview(tc.environ, tc.pref).Eligible)
		})
	}
}

// Ghostty is recognised by its own variables as well as by TERM_PROGRAM, because
// a shell that lost TERM_PROGRAM still has these.
func TestCheckImagePreviewRecognisesGhosttyVariables(t *testing.T) {
	assert.True(t, CheckImagePreview(
		[]string{"GHOSTTY_BIN_DIR=/usr/bin"}, config.ImagePreviewAuto).Recognized)
	assert.False(t, CheckImagePreview(
		[]string{"GHOSTTY_BIN_DIR="}, config.ImagePreviewAuto).Recognized,
		"an empty value is not a terminal")
}

// The section must say eligible and never say it works. An eligible rung that
// reads as confirmed sends a user whose terminal is on the list — and who still
// sees glyphs — looking for the wrong fault.
func TestRenderImagePreviewNeverClaimsConfirmation(t *testing.T) {
	out := RenderImagePreview(CheckImagePreview([]string{"TERM=xterm-kitty"}, config.ImagePreviewAuto))

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
		{"unrecognised", []string{"TERM=xterm-256color"}, config.ImagePreviewAuto, "no terminal Atrium"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := RenderImagePreview(CheckImagePreview(tc.environ, tc.pref))
			assert.Contains(t, out, tc.want)
			assert.NotContains(t, out, "eligible —", "this environment is not eligible")
		})
	}
}

func TestRenderImagePreviewNamesUnsetVariables(t *testing.T) {
	out := RenderImagePreview(CheckImagePreview(nil, config.ImagePreviewAuto))
	assert.Equal(t, 2, strings.Count(out, "unset"), "TERM and TERM_PROGRAM, and nothing else")
}

// The section has to sit beside the others without looking foreign: same header
// shape, same two-space rows, newline-terminated with no trailing blank (main.go
// owns the separators).
func TestRenderImagePreviewMatchesTheSectionConvention(t *testing.T) {
	out := RenderImagePreview(CheckImagePreview(nil, config.ImagePreviewAuto))

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
