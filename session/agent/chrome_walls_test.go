package agent

import (
	"strings"
	"testing"
	"unicode"

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

	// And the rest of the Zs class, for the reason isHorizontalRule admits it: whiteSpaceRegex
	// used to be `[\s\u00a0]+`, one rune wider than plain `\s` and still narrower than the
	// unicode.IsSpace that strings.TrimSpace applies two lines away. U+00A0 is the one copilot
	// 1.0.80 happens to emit; a matcher that reconstructs a literal across whitespace should not
	// depend on which space a vendor picked, and every one of these renders as a gap.
	for _, sp := range []struct {
		name string
		r    rune
	}{
		{"U+2007 FIGURE SPACE", '\u2007'},
		{"U+2009 THIN SPACE", '\u2009'},
		{"U+202F NARROW NO-BREAK SPACE", '\u202f'},
		{"U+205F MEDIUM MATHEMATICAL SPACE", '\u205f'},
		{"U+3000 IDEOGRAPHIC SPACE", '\u3000'},
	} {
		t.Run(sp.name, func(t *testing.T) {
			padded := "  transcript above\n" +
				"╭──────────────────╮\n" +
				"│ Do" + string(sp.r) + "you trust the │\n" +
				"│ files" + string(sp.r) + "in this   │\n" +
				"│ folder?          │\n" +
				"╰──────────────────╯"
			flat, ok := flattenBottomBox(padded)
			require.True(t, ok)
			require.Containsf(t, flat, copilotTrustHeadline,
				"%s must collapse like any other run of whitespace, or the gate misses its own "+
					"dialog over a space nobody can see", sp.name)
		})
	}
}

func TestHorizontalRuleAcceptsNoBreakSpacePadding(t *testing.T) {
	// Every Unicode space, not just U+00A0. strings.TrimSpace — which this predicate calls on
	// its own first line — and the flattening passes around it all go through unicode.IsSpace,
	// so admitting one rune of that class and rejecting the rest left this scan narrower than
	// its neighbours in the fail-dangerous direction: a border row carrying any of them takes
	// bottomBoxBlock down and reports "no dialog on screen".
	for _, pad := range []struct {
		name string
		r    rune
	}{
		{"U+0020 SPACE", ' '},
		{"U+00A0 NO-BREAK SPACE", '\u00a0'}, // copilot 1.0.80 emits this one
		{"U+2007 FIGURE SPACE", '\u2007'},
		{"U+2009 THIN SPACE", '\u2009'},
		{"U+202F NARROW NO-BREAK SPACE", '\u202f'},
		{"U+205F MEDIUM MATHEMATICAL SPACE", '\u205f'},
		{"U+3000 IDEOGRAPHIC SPACE", '\u3000'},
	} {
		t.Run(pad.name, func(t *testing.T) {
			require.Truef(t, isHorizontalRule("╰────────"+string(pad.r)+"───────╯"),
				"%s is padding; refusing it costs the box anchor and reports no dialog", pad.name)
			require.Truef(t, unicode.IsSpace(pad.r),
				"%s must be a space to Go as well, or this row is asserting the wrong class",
				pad.name)
		})
	}

	require.False(t, isHorizontalRule("╰──── x ────╯"),
		"and the predicate must still reject a rule carrying real text")
	require.False(t, isHorizontalRule("╰────\u200b────╯"),
		"U+200B ZERO WIDTH SPACE is not a space to Go and has no width, so it is not padding")
}

// A NUL arriving in the pane must not act as flattenBottomBox's blank-row separator.
//
// boxRowGap is NUL precisely because no captured pane carries one, and that premise is now made
// true by construction — flattenBottomBox strips it from its input — rather than asserted. The
// assertion it replaced could not fail: a raw NUL is `illegal character NUL` to the Go compiler,
// so the package cannot build in a state where a scan of its own source for one would fire.
//
// Written with the Go escape, which is what a fixture that genuinely captured a NUL would use
// and exactly what a byte scan of the source does not see. Without the strip the sentinel splits
// the headline and copilotTrustGateVisible stops recognising its own dialog — the fail-dangerous
// direction, since that is the predicate holding the folder-trust gate.
func TestFlattenBottomBoxStripsAnIncomingRowGap(t *testing.T) {
	const headline = "Allow directory access"
	split := boxedPane(headline[:6]+"\x00"+headline[6:], "  1. Yes")

	flat, ok := flattenBottomBox(split)
	require.True(t, ok, "the box is still a box")
	require.NotContains(t, flat, "\x00", "the incoming NUL must not survive into the readback")
	require.Contains(t, flat, headline,
		"a NUL inside a row must not break the row's literal in half")

	// The separator's real job is unaffected: a BLANK row still keeps two fragments apart.
	blank := boxedPane(headline[:6], "", headline[6:])
	flatBlank, ok := flattenBottomBox(blank)
	require.True(t, ok)
	require.NotContains(t, flatBlank, headline,
		"a blank row is still a paragraph break, so the literal must not reconstruct across it")
}
