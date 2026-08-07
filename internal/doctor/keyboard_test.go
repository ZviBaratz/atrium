package doctor

import (
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/keys"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCheckKeyboardReadsTheEnvironment pins the parse, including the two shapes a
// reader gets wrong: an entry with no "=" must not panic or be half-read, and a
// later duplicate must win, matching os.Environ semantics.
func TestCheckKeyboardReadsTheEnvironment(t *testing.T) {
	got := CheckKeyboard([]string{
		"TERM=xterm-256color",
		"MALFORMED",
		"TERM_PROGRAM=ghostty",
		"TERM=xterm-kitty",
		"TMUX=/tmp/tmux-1000/atrium,42,0",
	})

	assert.Equal(t, "xterm-kitty", got.Term, "a later duplicate wins")
	assert.Equal(t, "ghostty", got.TermProgram)
	assert.True(t, got.InTmux)
	assert.False(t, got.Probed, "doctor is one-shot; it cannot send the query")
}

// An empty TMUX is not something tmux sets, and treating it as "inside tmux" would
// print the tmux advice — and its two config lines — to a user who is not in tmux
// at all.
func TestCheckKeyboardIgnoresAnEmptyTmuxVariable(t *testing.T) {
	assert.False(t, CheckKeyboard([]string{"TMUX="}).InTmux)
	assert.False(t, CheckKeyboard(nil).InTmux)
}

// TestRenderKeyboardSaysNotProbed is the section's whole reason for existing.
// Atrium cannot answer "does my terminal support this?" from outside the TUI, and
// silence would be read as "no" — the answer that sends someone to change
// terminals. So the report has to say which rung it could not reach, and where the
// answer actually shows up.
func TestRenderKeyboardSaysNotProbed(t *testing.T) {
	out := RenderKeyboard(CheckKeyboard([]string{"TERM=xterm-256color"}))

	require.Contains(t, out, "Keyboard protocol:")
	require.Contains(t, out, "not probed here",
		"an unreachable rung must be named as unreachable, not omitted")
	require.Contains(t, out, "⇧↵",
		"and the report must point at the surface that does have the answer")
	// The surface it points at has to be one that actually shows the clause at the
	// width people run. The create form's footer is a width ladder that drops ⇧↵ on
	// an 80-column terminal even where the protocol works, so naming it would send a
	// supported user to exactly the wrong conclusion; the quick-send box fits at the
	// floor, which ui/overlay's TestComposerFooters_AtTheEightyColumnFloor pins.
	require.Contains(t, out, "quick-send box",
		"doctor must name the composer whose footer fits an 80-column terminal")
	require.Contains(t, out, "narrow terminal",
		"and must say that the other one's silence is about width, not support")
	// The key is read from the keymap, so a rebind reaches this line (#376).
	require.Contains(t, out, keys.LabelOf(keys.KeyQuickSend))
	assert.Contains(t, out, "⌃J",
		"the universally-working key is the actionable half for anyone this fails for")
	assert.Contains(t, out, "xterm-256color", "the raw TERM is what a bug report needs")
}

// An unset variable renders as the word, never as an empty column — a blank value
// is indistinguishable from a variable that is genuinely set to "".
func TestRenderKeyboardNamesUnsetVariables(t *testing.T) {
	out := RenderKeyboard(CheckKeyboard(nil))
	assert.Equal(t, 2, strings.Count(out, "unset"),
		"both TERM and TERM_PROGRAM report unset when absent: %q", out)
}

// TestRenderKeyboardWarnsInsideTmux covers the case that actually confuses people:
// tmux does not forward the kitty protocol, so it never answers the query and the
// footer falls back to ⌃J — which looks like Atrium ignoring a capable terminal.
// Both halves matter: naming the cause, and giving the tmux settings that pass
// shift+enter through anyway.
func TestRenderKeyboardWarnsInsideTmux(t *testing.T) {
	in := RenderKeyboard(CheckKeyboard([]string{"TMUX=/tmp/tmux-1000/atrium,42,0"}))
	assert.Contains(t, in, "tmux does not forward")
	assert.Contains(t, in, "extended-keys always")
	assert.Contains(t, in, "extkeys")

	// The negative control: outside tmux none of that appears, or every user reads
	// advice about a program they are not running.
	out := RenderKeyboard(CheckKeyboard([]string{"TERM=xterm-256color"}))
	assert.NotContains(t, out, "extended-keys")
	assert.NotContains(t, out, "tmux")
}
