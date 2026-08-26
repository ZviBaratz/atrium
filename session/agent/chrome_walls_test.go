package agent

import (
	"os"
	"strings"
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

// TestStripBoxWallsKeepsANestedBoxWall is the case the nested-box assertion above cannot make.
// That one passes a row carrying the nested box's CORNERS, which no wall-stripping
// implementation would touch — verified by mutation: replacing this helper's body with a
// ReplaceAll that deletes every "│" in the line kept the whole package green, that assertion
// included.
//
// A nested box's SIDE WALLS are the runes at stake, because they are the same rune as the outer
// walls and every real copilot dialog draws the path under review between a pair of them. If
// they came off too, flattenBottomBox would join the path's fragments across the nested box's
// rows — which is precisely the false-positive surface flattenBottomBox's doc claims stays
// closed.
func TestStripBoxWallsKeepsANestedBoxWall(t *testing.T) {
	require.Equal(t, "│ /etc/hostname │", stripBoxWalls("│ │ /etc/hostname │ │"),
		"one wall off each end and no more: the nested box's own walls are separators")
	require.Equal(t, "│ /etc/        │", stripBoxWalls("│ │ /etc/        │ │"),
		"the wrapped half of the same path, which is the row a splice would join it to")

	// The consequence, at the level a caller sees: a path wrapped across a nested box does not
	// reconstruct, so no literal can be manufactured across one.
	pane := "  transcript above\n" +
		"╭──────────────────╮\n" +
		"│ ╭──────────────╮ │\n" +
		"│ │ /etc/        │ │\n" +
		"│ │ hostname     │ │\n" +
		"│ ╰──────────────╯ │\n" +
		"╰──────────────────╯"
	flat, ok := flattenBottomBox(pane)
	require.True(t, ok)
	require.NotContains(t, flat, "/etc/ hostname",
		"the nested walls between the fragments are what keep them apart")
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
	// t.Run per case, and require rather than assert inside it: ranging a map with a bare
	// require.Falsef stops the loop on the first failure, so the other cases would go
	// unreported on exactly the change most likely to break more than one of them.
	for name, pane := range map[string]string{
		"bare transcript": "some prose\nand more prose",
		"composer only":   "transcript\n──────────\n❯\n──────────\n hints here",
		"box then composer": "╭────────╮\n│ 1. Yes │\n╰────────╯\n" +
			"──────────\n❯\n──────────\n hints",
	} {
		t.Run(name, func(t *testing.T) {
			_, ok := flattenBottomBox(pane)
			require.False(t, ok, "must not read as an anchored dialog")
		})
	}
}

// TestFlattenBottomBoxIsTrueOnABoxDrawnComposer is the qualifier the doc above needs, and it is
// asserted here because the sentence it replaced made a general claim out of a copilot-specific
// one: "ok=false means … a composer frame". Copilot's composer is borderless between two
// horizontal rules, so for THAT adapter the claim holds. gemini draws its composer as a round
// box, and its own bottom border sits within trailingBelowBoxCap of the last line — so this
// predicate reports a box on a pane whose only box is the composer.
//
// It matters to the next adapter author rather than to copilot: an agent whose composer is
// boxed cannot read liveness off this predicate at all, and cannot use copilotModalUp's shape
// as a modal veto either — it would veto its own composer and kill prompt delivery outright.
func TestFlattenBottomBoxIsTrueOnABoxDrawnComposer(t *testing.T) {
	flat, ok := flattenBottomBox(geminiIdlePane)
	require.True(t, ok,
		"gemini's composer is itself a round box, so the anchor finds one with no dialog up")
	require.Equal(t, ">", flat, "and its interior is just the composer glyph")
}

// TestFlattenBottomBoxSynthesisSurface holds the two ways cross-row synthesis reaches text the
// dialog did not draw. Both are false-POSITIVE directions — they manufacture a dialog that is
// not there, so a queued prompt is held rather than mis-delivered — and both were claimed
// closed by a doc sentence reading "the only text that can combine is text the dialog itself
// drew".
//
// The first is now closed for real, by boxRowGap. The second is not closable here (see
// bottomBoxBlock's HEIGHT case for why a top-border requirement is worse), so it is measured:
// what this test pins is that the exposure is what it is believed to be, and it reddens if it
// ever grows.
func TestFlattenBottomBoxSynthesisSurface(t *testing.T) {
	t.Run("a blank interior row separates, it does not join", func(t *testing.T) {
		pane := "  transcript above\n" +
			"╭──────────────────╮\n" +
			"│ Do you trust the │\n" +
			"│                  │\n" +
			"│ files in this    │\n" +
			"│ folder?          │\n" +
			"╰──────────────────╯"
		flat, ok := flattenBottomBox(pane)
		require.True(t, ok)
		require.NotContains(t, flat, copilotTrustHeadline,
			"no dialog rendered this sentence across a paragraph break, so nothing may "+
				"reconstruct it from one")
		require.Contains(t, flat, boxRowGap, "the blank row is present as a separator")
	})

	t.Run("walled rows above a top-truncated box join its interior", func(t *testing.T) {
		// The state copilotTrustgateW20Pane is already in: the box outgrew the pane, so its
		// top border scrolled off and the wall run has nothing to stop it.
		//
		// The two upper rows are walled TRANSCRIPT — a session displaying this very file — and
		// neither carries the headline; it exists only once they are joined to each other. The
		// dialog's own visible row carries the option label. So the pair of literals the
		// matcher needs is assembled across the boundary between the dialog and what is above
		// it, which is the join the doc's old wording said could not happen.
		quotedA := "copilotTrustHeadline = \"Do you trust the"
		quotedB := "files in this folder?\""
		require.NotContains(t, quotedA, copilotTrustHeadline)
		require.NotContains(t, quotedB, copilotTrustHeadline)

		pane := "│ " + quotedA + " │\n" +
			"│ " + quotedB + " │\n" +
			"│ ❯ 2. " + copilotTrustOption + " │\n" +
			"╰──────────────────────────────────╯"
		block, ok := bottomBoxBlock(pane)
		require.True(t, ok)
		require.Len(t, block, 3,
			"the mechanism: with no top border to stop it, the wall run took the rows above "+
				"the dialog as interior too")
		require.True(t, copilotTrustGateVisible(pane),
			"measured, not endorsed. This direction fails CLOSED — it manufactures a gate, so "+
				"a queued prompt is held rather than mis-delivered — which is why it is "+
				"disclosed rather than closed at the cost bottomBoxBlock's HEIGHT case names")
	})
}

// TestNoPaneFixtureCarriesTheRowGap is boxRowGap's premise: it can only be a separator no
// literal spans if no captured pane contains one. Read off the fixtures rather than assumed,
// because the alternative — a printable rune — is the choice this rules out.
//
// It reads this package's SOURCE FILES rather than a capture table, and the difference matters:
// a table covers the captures somebody remembered to list, and the claim here is about every
// fixture in the package. An earlier draft walked paneCoverage and its comment said "every
// committed fixture", which is two different sets.
func TestNoPaneFixtureCarriesTheRowGap(t *testing.T) {
	entries, err := os.ReadDir(".")
	require.NoError(t, err)
	read := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		body, err := os.ReadFile(e.Name())
		require.NoError(t, err)
		read++
		require.NotContainsf(t, string(body), boxRowGap,
			"%s carries a NUL, so boxRowGap would be indistinguishable from pane text", e.Name())
	}
	require.Positive(t, read, "no source files read, so this asserted nothing")
}

// TestFlatteningNormalizesNoBreakSpace and TestHorizontalRuleAcceptsNoBreakSpacePadding are the
// two halves of one finding: Go's \s does not include U+00A0, and copilot emits them. Untreated,
// the first costs a literal match inside a dialog and the second costs the box ANCHOR — which is
// the fail-dangerous direction, because a border row padded with one would make every matcher
// report "no dialog on screen".
//
// The premise is asserted rather than counted in prose. A count is exactly the kind of claim
// that rots, and the first draft of this comment carried one that was wrong by a factor of two
// — 56, which is the number of BYTES, taken from a report instead of re-derived.
func TestFlatteningNormalizesNoBreakSpace(t *testing.T) {
	// The DIALOG ladders, which is where they are: copilot pads the command it echoes into the
	// transcript with them ("● Executing \u00a0cat /etc/hostname\u00a0 now."). The busy ladder
	// carries none, and this assertion found that out by failing when it was pointed there —
	// which is the difference between a premise and a guess.
	nbsp := 0
	for _, ladder := range [][]paneCapture{copilotTrustgateLadder, copilotApprovalLadder} {
		for _, c := range ladder {
			nbsp += strings.Count(c.pane, "\u00a0")
		}
	}
	require.Positive(t, nbsp,
		"the premise: copilot's driven dialog panes really do carry NO-BREAK SPACEs. If a "+
			"future re-drive stops emitting them, this treatment is no longer measured — "+
			"decide whether to keep it, do not just delete this line")

	pane := "  transcript above\n" +
		"╭──────────────────╮\n" +
		"│ Do\u00a0you trust the │\n" +
		"│ files\u00a0in this   │\n" +
		"│ folder?          │\n" +
		"╰──────────────────╯"
	flat, ok := flattenBottomBox(pane)
	require.True(t, ok)
	require.Contains(t, flat, copilotTrustHeadline,
		"a NO-BREAK SPACE inside the sentence must collapse like any other run of whitespace")
}

func TestHorizontalRuleAcceptsNoBreakSpacePadding(t *testing.T) {
	require.True(t, isHorizontalRule("╰────────\u00a0───────╯"),
		"a NO-BREAK SPACE is padding; refusing it costs the box anchor and reports no dialog")
	require.False(t, isHorizontalRule("╰──── x ────╯"),
		"and the predicate must still reject a rule carrying real text")
}
