package agent

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestStripBoxWallsTakesBothWallsAndNoGlyph pins the split from stripBoxInterior: the walls
// come off, the composer glyph does NOT. That asymmetry is the whole reason the helper
// exists — a dialog's prose opens with "❯ 1. Yes" on its selected row, and eating the glyph
// there would be harmless, but eating it in stripBoxInterior's caller is what reads back a
// user's typed text, so only one of the two callers may want it.
func TestStripBoxWallsTakesBothWallsAndNoGlyph(t *testing.T) {
	require.Equal(t, "Confirm folder trust", stripBoxWalls("│ Confirm folder trust      │"))
	require.Equal(t, "❯ 1. Yes", stripBoxWalls("│ ❯ 1. Yes                  │"),
		"the glyph must survive: flattenBottomBox reads dialog prose, not a composer")
	require.Equal(t, "plain prose", stripBoxWalls("  plain prose  "),
		"a line with no walls is just trimmed")
	require.Equal(t, "╭──────╮", stripBoxWalls("│ ╭──────╮ │"),
		"a NESTED box's own border survives as a separator; only the outer walls come off")
}

// TestStripBoxInteriorStillStripsTheGlyph is the regression half of the extraction: the
// existing caller's behaviour must be byte-identical, so the split is a refactor rather than
// a change. Reads the composer glyph off defaultPrompts rather than a literal so a change to
// that set cannot leave this asserting against a glyph nothing draws.
func TestStripBoxInteriorStillStripsTheGlyph(t *testing.T) {
	require.Equal(t, "refactor the parser",
		stripBoxInterior("│ ❯ refactor the parser     │", defaultPrompts))
	require.Equal(t, "1. Yes", stripBoxInterior("│ ❯ 1. Yes │", defaultPrompts))
}

// TestFlattenBottomBoxRejoinsAWrappedSentence is the property the two adapters below need
// and that flattenChrome structurally cannot deliver: a sentence hard-wrapped inside a box
// has the border runes and their padding BETWEEN its fragments, so collapsing newlines to
// spaces leaves "…files in this │ │ folder?…". Stripping the walls first is what rejoins it.
func TestFlattenBottomBoxRejoinsAWrappedSentence(t *testing.T) {
	pane := "  transcript above\n" +
		"╭──────────────────╮\n" +
		"│ Do you trust the │\n" +
		"│  files in this   │\n" +
		"│ folder?          │\n" +
		"╰──────────────────╯"
	flat, ok := flattenBottomBox(pane)
	require.True(t, ok, "the bottom border ends the pane and walled rows sit above it")
	require.Contains(t, flat, "Do you trust the files in this folder?")

	// The contrast, asserted rather than described: the same pane through the flat window
	// the prompt matchers use does NOT reconstruct the sentence.
	require.NotContains(t, flattenChrome(pane, WindowPrompt),
		"Do you trust the files in this folder?",
		"if this ever passes, flattenBottomBox has stopped being the thing that earns its keep")
}

// TestFlattenBottomBoxRefusesAPaneWithNoAnchoredBox is the half that makes the whole-pane
// alternative unnecessary. A composer pane, a bare transcript and box art with a composer
// drawn below it all report false, so "no dialog" is an anchored answer rather than a scan
// of scrollback — which is what confines the false-positive surface to bottomBoxBlock's own
// disclosed one (quoted box art that ends the pane).
func TestFlattenBottomBoxRefusesAPaneWithNoAnchoredBox(t *testing.T) {
	for name, pane := range map[string]string{
		"bare transcript": "some prose\nand more prose",
		"composer only":   "transcript\n──────────\n❯\n──────────\n hints here",
		"box then composer": "╭────────╮\n│ 1. Yes │\n╰────────╯\n" +
			"──────────\n❯\n──────────\n hints",
	} {
		_, ok := flattenBottomBox(pane)
		require.Falsef(t, ok, "%s must not read as an anchored dialog", name)
	}
}
