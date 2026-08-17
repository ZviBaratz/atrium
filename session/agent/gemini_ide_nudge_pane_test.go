package agent

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// gemini_ide_nudge_pane_test.go — the IDE-integration nudge's width ladder (#717).
//
// Driven natively at gemini-cli 0.55.1 on 2026-08-17 with
// `drive-agent.sh fresh <width>` + `ladder`, one fresh session per rung. NOT a resize
// ladder: gemini's dialogs are the measured counterexample to resize-equals-native — the
// trust dialog diverged from its native rung at all four widths (#713) because the CLI
// repaints only its own region and leaves torn fragments of the wider frame above it, and
// a rung whose bytes depend on the previous rung is a fixture that lies.
//
// The nudge needs an IDE to be detected, which detectIde grants only for TERM_PROGRAM in
// {vscode, sublime, Zed}, ZED_SESSION_ID, XCODE_VERSION_ACTUAL or isJetBrains(). These were
// driven with TERM_PROGRAM=vscode and an ISOLATED HOME — the one place this file departs
// from drive-agent.sh's "the agent's config dir is deliberately not isolated" rule, and for
// a specific reason: shouldShowIdePrompt reads ide.hasSeenNudge out of the real
// ~/.gemini/settings.json, answering the dialog writes it back, and the highlighted default
// installs a VS Code extension. A capture run must not be able to do either. It costs
// nothing here because the nudge renders before authentication — the vendor checks
// shouldShowIdePrompt ahead of isFolderTrustDialogOpen and every auth screen in the same
// chain — which is also why an unauthenticated capture reaches it at all.
//
// ideName renders as "IDE" in these captures because that is what detectIde's vscode
// definition carries. Nothing here keys on it; the headline is unusable as an anchor at any
// width (see geminiIdeNudgeVisible).

// geminiIdeNudgePane80 is the nudge at width 80 — headline on one line, all three option
// rows intact. The shape #717 was reported from, and the one a wide-capture-only judgement
// would have keyed on.
const geminiIdeNudgePane80 = `
 ▝▜▄      ▗█▀▀▜▙▝█▛▀▀▌▜██▖▟██▘▜█▘▜██▖▝█▛▝█▛
   ▝▜▄    █▌     █▙▟  ▐█▝█▛▐█ ▐█ ▐█▝█▖█▌ █▌
  ▗▟▀     ▜▙ ▝█▛ █▌▝ ▖▐█   ▐█ ▐█ ▐█ ▝██▌ █▌
 ▝▀        ▀▀▀▀▘▝▀▀▀▀▘▀▀▘  ▀▀▘▀▀▘▀▀▘ ▝▀▀▝▀▀

 Gemini CLI v0.55.1



Tips for getting started:
1. Create GEMINI.md files to customize your interactions
2. /help for more information
3. Ask coding questions, edit code or run commands
4. Be specific for the best results

ℹ Skipping project agents due to untrusted folder. To enable, ensure that the
  project root is trusted.

 ╭──────────────────────────────────────────────────────────────────────────────
 │
 │ > Do you want to connect IDE to Gemini CLI?
 │ If you select Yes, we'll install an extension that allows the CLI to access
 │ your open files and display diffs directly in IDE.
 │
 │ ● 1. Yes
 │   2. No (esc)
 │   3. No, don't ask again
 │
 ╰──────────────────────────────────────────────────────────────────────────────`

// geminiIdeNudgePane40 is where the HEADLINE dies. "> Do you want to connect IDE to" /
// "Gemini CLI?" are two physical lines with the box's own "│" between them, and
// flattenChrome joins on whitespace only — #713's structural finding, transferred to this
// dialog unchanged. The option rows are still whole.
const geminiIdeNudgePane40 = ` ▝▀

 Gemini CLI v0.55.1



Tips for getting started:
1. Create GEMINI.md files to customize
your interactions
2. /help for more information
3. Ask coding questions, edit code or
run commands
4. Be specific for the best results

ℹ Skipping project agents due to
  untrusted folder. To enable, ensure
  that the project root is trusted.

 ╭──────────────────────────────────────
 │
 │ > Do you want to connect IDE to
 │ Gemini CLI?
 │ If you select Yes, we'll install an
 │ extension that allows the CLI to
 │ access your open files and display
 │ diffs directly in IDE.
 │
 │ ● 1. Yes
 │   2. No (esc)
 │   3. No, don't ask again
 │
 ╰──────────────────────────────────────`

// geminiIdeNudgePane24 is where the DISMISS ROW dies: "3. No, don't ask …". #717 proposed
// keying on "No, don't ask again"; this rung is why the shipped matcher does not. 24 is also
// the width pane_width_test.go's header derives as what a 70-column terminal leaves the
// agent pane, so it is the narrowest width Atrium is known to produce rather than merely the
// narrowest one drivable.
const geminiIdeNudgePane24 = `4. Be specific for the
best results

ℹ Skipping project
  agents due to
  untrusted folder. To
  enable, ensure that
  the project root is
  trusted.

 ╭──────────────────────
 │
 │ > Do you want to
 │ connect IDE to
 │ Gemini CLI?
 │ If you select Yes,
 │ we'll install an
 │ extension that
 │ allows the CLI to
 │ access your open
 │ files and display
 │ diffs directly in
 │ IDE.
 │
 │ ● 1. Yes
 │   2. No (esc)
 │   3. No, don't ask …
 │
 ╰──────────────────────`

// geminiIdeNudgePane20 is below that floor and the gate STILL FIRES — the contrast with
// geminiTrustGatePane20, which is a miss. The trust dialog's rows carry an interpolated
// directory name that pushes them past the cut; the nudge's rows carry nothing, so
// "No (esc)" and "No, don't" both survive. The dismiss row is cut further here
// ("3. No, don't …"), which is what makes this rung the negative evidence for the proposed
// literal rather than a repeat of the one above.
const geminiIdeNudgePane20 = `  untrusted folder.
  To enable, ensure
  that the project
  root is trusted.

 ╭──────────────────
 │
 │ > Do you want to
 │ connect IDE to
 │ Gemini CLI?
 │ If you select
 │ Yes, we'll
 │ install an
 │ extension that
 │ allows the CLI
 │ to access your
 │ open files and
 │ display diffs
 │ directly in IDE.
 │
 │ ● 1. Yes
 │   2. No (esc)
 │   3. No, don't …
 │
 ╰──────────────────`

// geminiIdeNudgeLadder is the ladder as DATA, so the widths this gate is proven at are a
// value a test reads rather than a sentence in a comment (#648/#665). Every rung FIRES —
// there is no missed rung to keep beside it, which is the one structural difference from
// geminiTrustGateLadder and the reason this file has no sibling `…MissedRung` var.
var geminiIdeNudgeLadder = []paneCapture{
	{name: "geminiIdeNudgePane80", width: 80, note: "headline on one line", pane: geminiIdeNudgePane80},
	{name: "geminiIdeNudgePane40", width: 40, note: "headline wrapped across the box border", pane: geminiIdeNudgePane40},
	{name: "geminiIdeNudgePane24", width: 24, note: "dismiss row truncated", pane: geminiIdeNudgePane24},
	{name: "geminiIdeNudgePane20", width: 20, note: "dismiss row truncated further; gate still fires", pane: geminiIdeNudgePane20},
}

// TestGeminiIdeNudgeLadderKeepsEveryDrivenRung is the drift guard on the table above: a rung
// dropped from it silently narrows what paneCoverage proves, and nothing else would notice.
func TestGeminiIdeNudgeLadderKeepsEveryDrivenRung(t *testing.T) {
	require.Equal(t, []string{
		"width 20 geminiIdeNudgePane20 (dismiss row truncated further; gate still fires)",
		"width 24 geminiIdeNudgePane24 (dismiss row truncated)",
		"width 40 geminiIdeNudgePane40 (headline wrapped across the box border)",
		"width 80 geminiIdeNudgePane80 (headline on one line)",
	}, ladderRungs(geminiIdeNudgeLadder), "the IDE nudge was driven natively at 80/40/24/20")
	requireDistinctCaptures(t, "geminiIdeNudgeLadder", geminiIdeNudgeLadder)
}

// geminiIdeNudgeInputBox is whether each rung reads as a COMPOSER, as data rather than as a
// sentence, because it is not the constant #717 assumed and the difference is the bug's blast
// radius.
//
// The issue states InputBoxVisible = TRUE on the nudge, measured at one width. It is true at 80
// and 40 and FALSE at 24 and 20 — not because the glyph stops rendering, but because the dialog
// grows as its prose wraps and InputBoxVisible reads only the last WindowPrompt (15) non-empty
// lines. At 24 the box is 17 content rows, so the headline carrying "> " has fallen out of that
// window while still being on screen.
//
// So the #512 exposure — AwaitingInput() true, queued prompt typed into the menu — existed only
// at the WIDE end. At the narrow end the same dialog was #713's milder class instead: no gate,
// no prompt, no busy marker, PaneIdle, a false Ready and a completion ding. One dialog, two
// failure modes, split by width. The gate closes both; this table is why that sentence needs
// two halves.
var geminiIdeNudgeInputBox = map[string]bool{
	"geminiIdeNudgePane80": true,
	"geminiIdeNudgePane40": true,
	"geminiIdeNudgePane24": false,
	"geminiIdeNudgePane20": false,
}

// TestGeminiIdeNudgeIsAGateOverAVisibleInputBox is acceptance criteria 1 and 4 of #717, and the
// two halves must be read together.
//
// Where InputBoxVisible is TRUE that is not a bug to be fixed here and must not be silently
// "corrected": the nudge really does render a "> " glyph inside a bordered box, which is
// byte-for-byte the shape inputBoxText looks for, and no reading of the box can tell it from a
// composer. What excludes the pane from prompt delivery is the GATE — AwaitingInput() is
// `!GateUp && !DetectPrompt && InputBoxVisible` (session/instance.go), so GateUp true is the
// whole of the fix, at every width and for both failure modes.
//
// Before this gate existed the wide rungs returned AwaitingInput() true, and Atrium typed the
// queued initial prompt into a RadioButtonSelect whose highlighted row installs an IDE
// extension. `atrium new --prompt` makes that unattended.
func TestGeminiIdeNudgeIsAGateOverAVisibleInputBox(t *testing.T) {
	require.Len(t, geminiIdeNudgeInputBox, len(geminiIdeNudgeLadder),
		"every rung needs a recorded composer reading, or the table stops covering the ladder")
	for _, c := range geminiIdeNudgeLadder {
		t.Run(c.label(), func(t *testing.T) {
			want, ok := geminiIdeNudgeInputBox[c.name]
			require.True(t, ok, "%s has no entry in geminiIdeNudgeInputBox", c.name)
			require.Equal(t, want, gemini.InputBoxVisible(c.pane),
				"the composer reading is honest and is recorded, not asserted uniformly")

			require.True(t, gateUpOn(gemini, c.pane),
				"and the GATE is what excludes the pane from prompt delivery, at every width")
			require.False(t, promptOn(gemini, c.pane),
				"it is not a prompt: nothing here may be auto-tapped")
		})
	}
}

// TestGeminiIdeNudgeDismissRowTruncatesBelowWidth40 is the measurement that overrode #717's own
// proposal, kept as data so the proposal cannot be re-adopted by reading the issue.
//
// The issue suggested keying on "No, don't ask again" plus one other row. gemini truncates the
// dismiss row from the right, so that literal is present at 80 and 40 and GONE at 24 and 20 —
// the widths Atrium's preview pane actually produces. A gate keyed on it would have missed
// exactly there, which is #713's mistake made from a wide capture, again.
func TestGeminiIdeNudgeDismissRowTruncatesBelowWidth40(t *testing.T) {
	const proposed = "No, don't ask again"
	for _, c := range geminiIdeNudgeLadder {
		t.Run(c.label(), func(t *testing.T) {
			if c.width >= 40 {
				require.Contains(t, c.pane, proposed, "wide enough to carry the full row")
			} else {
				require.NotContains(t, c.pane, proposed,
					"truncated away — this is why the matcher keys on the surviving prefix")
			}
			// What the matcher does key on, at every rung including the two above.
			require.Contains(t, c.pane, "No (esc)")
			require.Contains(t, c.pane, "No, don't")
		})
	}
}

// TestGeminiIdeNudgeHeadlineWrapsInsideTheBox is #713's structural finding, re-measured on this
// dialog rather than assumed to transfer. It is why the headline is not an anchor.
//
// A wrapped headline has the box's own "│" between its halves and flattenChrome joins physical
// lines on whitespace, so no window reassembles it. Asserted through the production flattener at
// a window far deeper than any of these panes, so "unreachable" means unreachable rather than
// "not in the default window".
func TestGeminiIdeNudgeHeadlineWrapsInsideTheBox(t *testing.T) {
	const headline = "Do you want to connect IDE to Gemini CLI?"
	require.Contains(t, geminiIdeNudgePane80, headline, "whole at 80")
	for _, c := range geminiIdeNudgeLadder {
		if c.width >= 80 {
			continue
		}
		t.Run(c.label(), func(t *testing.T) {
			require.NotContains(t, c.pane, headline, "wrapped in the raw pane")
			require.NotContains(t, flattenChrome(c.pane, 200), headline,
				"and not repairable by flattening, because the wrap has a box border in it")
		})
	}
}

// TestGeminiIdeNudgeIgnoresAnIdleComposer is acceptance criterion 3's first half. The nudge's
// glyph is a composer glyph, so the matcher cannot reject on the glyph the way the trust gate
// does — which makes "an ordinary composer does not raise this gate" a thing to prove rather
// than a thing to assume.
func TestGeminiIdeNudgeIgnoresAnIdleComposer(t *testing.T) {
	require.False(t, geminiIdeNudgeVisible(geminiIdlePane),
		"an idle composer carries neither option row")
	require.False(t, gateUpOn(gemini, geminiIdlePane),
		"and neither gemini gate fires on it")
}

// TestGeminiIdeNudgeIgnoresItsOwnRowsInProse is acceptance criterion 3's second half, and the
// #715-round-1 regression as a test: a working pane that merely QUOTES the dialog — an agent
// reading this tracker, or this file — must not raise the gate.
//
// What keeps it down is the box anchor. bottomBoxBlock returns the bottom-most box, which on a
// working pane is the composer; prose scrolling by above it is not in the block at all.
func TestGeminiIdeNudgeIgnoresItsOwnRowsInProse(t *testing.T) {
	pane := "  The nudge offers three rows: Yes, No (esc), and No, don't ask again.\n" +
		"  Keying on the third alone would miss below width 40.\n" +
		"\n" +
		" ╭────────────────────────────────────────╮\n" +
		" │ >                                      │\n" +
		" ╰────────────────────────────────────────╯\n" +
		"  ~/repo (main*)                  esc to cancel\n"
	require.False(t, geminiIdeNudgeVisible(pane),
		"the rows are above the composer box, so they are not in the bottom block")
	require.False(t, gateUpOn(gemini, pane), "and no gemini gate fires on it")
}

// TestGeminiIdeNudgeFiresOnAComposerQuotingBothRows is the residue this gate does NOT close,
// pinned so it stays disclosed rather than rotting into a surprise.
//
// The composer is a box too, and this matcher cannot use the isInputBoxLine rejection that
// closes the equivalent hole for the trust gate — the glyph is the nudge's own. So a user who
// pastes both literals INTO their composer raises the gate.
//
// It is left open deliberately, because the two errors are not the same size. Firing wrongly
// makes AwaitingInput false, so the queued prompt is WITHHELD and the row reads "waiting on
// setup screen" until the paste clears — recoverable, and the #342 direction. Not firing types
// that prompt into a menu whose highlighted default installs an extension. Given a choice of
// which way to be wrong, this gate is wrong in the recoverable direction.
func TestGeminiIdeNudgeFiresOnAComposerQuotingBothRows(t *testing.T) {
	pane := " ╭────────────────────────────────────────╮\n" +
		" │ > why does the nudge show No (esc) and  │\n" +
		" │   No, don't ask again as separate rows? │\n" +
		" ╰────────────────────────────────────────╯\n"
	assert.True(t, geminiIdeNudgeVisible(pane),
		"DISCLOSED: a composer quoting both literals raises the gate")
	assert.True(t, gemini.InputBoxVisible(pane),
		"the user really is at their composer; the cost is a withheld prompt, not a typed one")
}

// TestGeminiIdeNudgeNeedsBothLiterals is the conjunction's negative control, and the specific
// mistake #715 round 1 shipped: Gate.Contains is an ALTERNATION, so had these two literals been
// given to it instead of to Match, either alone would raise the gate. "No (esc)" alone is a
// plausible line for an agent to write; so is "No, don't".
func TestGeminiIdeNudgeNeedsBothLiterals(t *testing.T) {
	box := func(inner string) string {
		return " ╭──────────────────────────────────────╮\n" +
			" │ " + inner + "\n" +
			" ╰──────────────────────────────────────╯\n"
	}
	assert.False(t, geminiIdeNudgeVisible(box("2. No (esc)")), "one literal is not enough")
	assert.False(t, geminiIdeNudgeVisible(box("3. No, don't ask again")), "nor the other")
	assert.True(t, geminiIdeNudgeVisible(
		" ╭──────────────────────────────────────╮\n"+
			" │   2. No (esc)                        │\n"+
			" │   3. No, don't ask again             │\n"+
			" ╰──────────────────────────────────────╯\n"),
		"both together, inside a live box, is the dialog")
}

// TestGeminiIdeNudgeNeedsALiveBox pins the other half of the matcher: the literals must be
// inside a box whose bottom border ends the pane (or sits within trailingBelowBoxCap of it).
// An answered dialog with the session's output printed below it must not keep the gate up.
func TestGeminiIdeNudgeNeedsALiveBox(t *testing.T) {
	answered := " ╭──────────────────────────────────────╮\n" +
		" │   2. No (esc)                        │\n" +
		" │   3. No, don't ask again             │\n" +
		" ╰──────────────────────────────────────╯\n" +
		"\n" +
		"  Loaded cached credentials.\n" +
		"  Reading files from the workspace...\n" +
		"  Ready when you are.\n"
	assert.False(t, geminiIdeNudgeVisible(answered),
		"more than trailingBelowBoxCap rows below the border means the dialog is gone")
}

// gateUpOn and promptOn drop the second return value so the assertions above read as the
// predicates they are. Deliberately the PRODUCTION methods, not re-implementations: a test
// that called geminiIdeNudgeVisible directly would stay green if the Gate were never
// registered on the adapter (the drift-sites lesson — registration and behaviour are
// separate sites).
func gateUpOn(a *Adapter, content string) bool {
	_, up := a.GateUp(content)
	return up
}

func promptOn(a *Adapter, content string) bool {
	_, ok := a.DetectPrompt(content)
	return ok
}

// TestOpenBottomBoxBlockAcceptsWhatTheStrictPairRejects measures the relaxation rather than
// describing it, so the cost of the right-open variant is a value and not a claim.
//
// openBottomBoxBlock drops two requirements bottomBoxBlock makes — the bottom border's right
// corner and the wall lines' closing wall — because gemini's IdeIntegrationNudge renders its
// Box with width:"100%" and marginLeft:1 and so overflows the pane by one column at EVERY
// width. Its sibling FolderTrustDialog computes a dialogWidth and is fully walled, which is why
// bottomBoxBlock anchors that one and cannot anchor this one.
//
// What the relaxation lets through is the point: shapes that are NOT a closed box now form a
// block. Only geminiIdeNudgeVisible calls it, and it pairs the block with two literals from the
// dialog it is looking for — but the guard has to be visible, because the next caller will not
// re-derive it.
func TestOpenBottomBoxBlockAcceptsWhatTheStrictPairRejects(t *testing.T) {
	// The real dialog: open on the right at every rung. The strict pair sees nothing.
	for _, c := range geminiIdeNudgeLadder {
		t.Run(c.label(), func(t *testing.T) {
			_, strict := bottomBoxBlock(c.pane)
			assert.False(t, strict,
				"the shipped box primitive cannot see this dialog at all — that is why the "+
					"open variant exists, and it is measured here rather than asserted once")
			block, open := openBottomBoxBlock(c.pane)
			require.True(t, open)
			assert.NotEmpty(t, block)
		})
	}

	// The cost. A right-open frame that is not a dialog now forms a block.
	halfFrame := " │ some transcript line\n │ another\n ╰────────────────────\n"
	_, strict := bottomBoxBlock(halfFrame)
	assert.False(t, strict, "the strict pair refuses a frame missing its right edge")
	_, open := openBottomBoxBlock(halfFrame)
	assert.True(t, open, "DISCLOSED: the open variant accepts it; the caller's literals are the guard")
	assert.False(t, geminiIdeNudgeVisible(halfFrame), "and for this caller they hold it down")

	// Both variants still need a bottom-LEFT corner and something walled above it.
	_, open = openBottomBoxBlock(" │ walled\n ─────────────────────\n")
	assert.False(t, open, "a bare rule is not a box bottom, in either variant")
	_, open = openBottomBoxBlock(" not walled\n ╰────────────────────\n")
	assert.False(t, open, "and a border with nothing walled above it is not a block")
}

// TestOpenBoxWallLineAcceptsALoneWall is the regression guard for the mistake the first draft
// of isOpenBoxWallLine made, and it is worth its own test because the reasoning was plausible.
//
// isBoxWallLine rejects a single-rune line, correctly: in a closed box that rune would have to
// be both walls at once. Carrying that guard into the open variant looked like conservatism and
// was a bug — the nudge's Box has padding:1, so its padding rows render as a left wall and
// nothing else, and one sits directly above the bottom border on all four captures. The walk
// stopped on its first step and openBottomBoxBlock returned false on every pane it was written
// for, with a comment explaining why the guard was safe.
func TestOpenBoxWallLineAcceptsALoneWall(t *testing.T) {
	assert.True(t, isOpenBoxWallLine(" │"), "an open box's padding row is exactly this")
	assert.False(t, isBoxWallLine(" │"), "while the closed-box predicate rejects it, as it should")
	assert.False(t, isOpenBoxWallLine(""), "empty is still not a wall")
	assert.False(t, isOpenBoxWallLine("  plain text"), "nor is an unwalled line")
	assert.True(t, isOpenBoxWallLine(" │ ● 1. Yes"), "and a content row still is")
}
