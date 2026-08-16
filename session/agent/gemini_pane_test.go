package agent

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Gemini folder-trust panes, driven live against gemini-cli 0.55.1 on 2026-08-15 by
// scripts/drive-agent.sh (#647) — isolated tmux (private TMUX_TMPDIR, socket addressed by
// absolute path) on a scratch git repo under /tmp. Captured with `tmux capture-pane -p`;
// every line is verbatim, only trailing all-blank rows removed. The bytes were read back
// with `cat -vet`: the apostrophe in "Don't trust" is ASCII 0x27 (not U+2019), the selector
// is "●" (U+25CF) and the truncation mark at widths 24 and 20 is a single "…" (U+2026).
//
// These exist because #713: gemini's shipped gate literal, "Do you trust this folder",
// returns ZERO hits anywhere in the 0.55.1 bundle. GateUp went false on a real trust
// dialog, so Poll never returned PaneGate; gemini declares no HookSupport, so the ladder
// fell through to PaneIdle, ApplyPaneState set Ready, and the non-Ready→Ready edge raised
// the unread bit — a completion ding for a session blocked on a dialog it never started.
//
// EVERY RUNG IS A NATIVELY-NARROW SESSION (`fresh <width>`), not a resize, and that is a
// departure from how the codex and agy ladders were captured. #647 established that a trust
// gate resized to 28 is byte-identical to one started at 28, and drive-agent.sh's `ladder`
// verb rests on it. It did not hold for this dialog: all four widths were driven both ways in
// the same run, and the resized rung differed from the native one at all four — the Ink app
// repaints its own region and the rows above keep torn fragments of the previous, wider frame.
//
// Those fragments are not inert. The width-40 resize rung carried a leftover
// "Do you trust the files in this folder" on ONE line — contiguous, where the live dialog's
// own headline is wrapped — so a GateWindow sweep run against it reported the headline
// REACHABLE at 40, the opposite of the truth, and would have made the guard below assert a
// falsehood. A rung whose bytes depend on the previous rung is a fixture that lies. The
// workspace names in the option rows (fresh80, fresh40) are what record that these are not
// resizes; at 24 and 20 the row is truncated before the name, which is why they stop there.
//
// WHY the resize diverges is NOT established, and an earlier draft of this comment claimed it
// was: "the dialog occupies 27 non-empty lines at width 80 and 37 at 20 against a 40-row
// pane", offered as the mechanism. The rejection of that mechanism was then itself wrong:
// "nothing overflows — every capture is inside the 40-row pane" measured the wrong thing
// twice. A capture cannot exceed its pane by construction, so that half was unfalsifiable;
// and the session's OUTPUT plainly did overflow — at width 40 the capture opens on a fragment
// of the logo's last row, at 24 the logo is gone entirely, and at 20 it opens mid-sentence
// inside the untrusted-folder notice. Rows scrolled off at three of the four rungs.
//
// What is true, and is what the argument needs, is about the DIALOG rather than the session:
// the box fits the pane at every rung with room to spare — 15/20/28/33 rows against 40, the
// figures in geminiDialogRows below, which TestGeminiDialogBoxIsFullyOnScreen computes from
// the captures. So a rule of the form "resize is unsafe once the dialog is taller than the
// pane" would have found all four rungs safe and licensed exactly the capture that lied here.
// The safe rule is the unconditional one: a resized rung is not known to equal a native one
// for this dialog, so drive it with `fresh`.
//
// The per-rung CAPTURE heights are geminiCaptureRows, a separate table for a separate
// measurement. Neither is restated in prose, because when they were, the 24 rung read 35 in
// this sentence and 37 in the table and every check stayed green.
//
// Two structural facts decided the fix, and neither is visible in the bundle:
//
//   - The headline is UNREPAIRABLE once it wraps, because the dialog has a box border. The
//     wrap puts the box's own "│" between the halves, flattenChrome joins on whitespace, and
//     a "│" is not whitespace — so no GateWindow reaches it. Codex wraps without a box, which
//     is why widening its window worked and why the same move is useless here.
//   - The option rows TRUNCATE rather than wrap, and they truncate from the right, so
//     "Trust folder" survives as a left-anchored prefix of `Trust folder (${dirName})`
//     independently of how long the working directory's name is — until the row itself is
//     cut, which happens between 24 and 20.

const geminiTrustGatePane80 = `
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

 ╭────────────────────────────────────────────────────────────────────────────╮
 │                                                                            │
 │ Do you trust the files in this folder?                                     │
 │                                                                            │
 │ Trusting a folder allows Gemini CLI to load its local configurations,      │
 │ including custom commands, hooks, MCP servers, agent skills, and settings. │
 │ These configurations could execute code on your behalf or change the       │
 │ behavior of the CLI.                                                       │
 │                                                                            │
 │                                                                            │
 │ ● 1. Trust folder (fresh80)                                                │
 │   2. Trust parent folder (gemini)                                          │
 │   3. Don't trust                                                           │
 │                                                                            │
 ╰────────────────────────────────────────────────────────────────────────────╯`

const geminiTrustGatePane40 = ` ▝▀

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

 ╭────────────────────────────────────╮
 │                                    │
 │ Do you trust the files in this     │
 │ folder?                            │
 │                                    │
 │ Trusting a folder allows Gemini    │
 │ CLI to load its local              │
 │ configurations, including custom   │
 │ commands, hooks, MCP servers,      │
 │ agent skills, and settings. These  │
 │ configurations could execute code  │
 │ on your behalf or change the       │
 │ behavior of the CLI.               │
 │                                    │
 │                                    │
 │ ● 1. Trust folder (fresh40)        │
 │   2. Trust parent folder (gemini)  │
 │   3. Don't trust                   │
 │                                    │
 ╰────────────────────────────────────╯`

const geminiTrustGatePane24 = `


ℹ Skipping project
  agents due to
  untrusted folder. To
  enable, ensure that
  the project root is
  trusted.

 ╭────────────────────╮
 │                    │
 │ Do you trust the   │
 │ files in this      │
 │ folder?            │
 │                    │
 │ Trusting a folder  │
 │ allows Gemini CLI  │
 │ to load its local  │
 │ configurations,    │
 │ including custom   │
 │ commands, hooks,   │
 │ MCP servers, agent │
 │ skills, and        │
 │ settings. These    │
 │ configurations     │
 │ could execute code │
 │ on your behalf or  │
 │ change the         │
 │ behavior of the    │
 │ CLI.               │
 │                    │
 │                    │
 │ ● 1. Trust folder… │
 │   2. Trust parent… │
 │   3. Don't trust   │
 │                    │
 ╰────────────────────╯`

// geminiTrustGatePane20 is NEGATIVE evidence — the one rung the gate does not reach. It
// carries the width datum like the others but is deliberately absent from
// geminiTrustGateLadder, because paneCoverage is positive-only and every entry there must
// fire. What it shows is a content loss, not a windowing one: the selector rows themselves
// are cut ("● 1. Trust fo…", "3. Don't tr…"), so there is nothing left on screen for any
// budget to reach. Held by TestGeminiTrustGateOptionRowsAreTruncatedAtWidth20.
const geminiTrustGatePane20 = `  untrusted folder.
  To enable, ensure
  that the project
  root is trusted.

 ╭────────────────╮
 │                │
 │ Do you trust   │
 │ the files in   │
 │ this folder?   │
 │                │
 │ Trusting a     │
 │ folder allows  │
 │ Gemini CLI to  │
 │ load its local │
 │ configurations │
 │ , including    │
 │ custom         │
 │ commands,      │
 │ hooks, MCP     │
 │ servers, agent │
 │ skills, and    │
 │ settings.      │
 │ These          │
 │ configurations │
 │ could execute  │
 │ code on your   │
 │ behalf or      │
 │ change the     │
 │ behavior of    │
 │ the CLI.       │
 │                │
 │                │
 │ ● 1. Trust fo… │
 │   2. Trust pa… │
 │   3. Don't tr… │
 │                │
 ╰────────────────╯`

// The driven ladder, feeding paneCoverage["gemini/gate"] directly rather than being listed
// there a second time — the same arrangement codex's two ladders use.
//
// 80 is the width #713 measured the bug at, 40 is where the headline dies, and 24 is the
// floor: pane_width_test.go's header derives ~24 columns as what a 70-column terminal leaves
// the agent pane, so it is the narrowest width Atrium is known to produce in practice rather
// than merely the narrowest one drivable. The rung below it is geminiTrustGatePane20, and it
// is a miss.
var geminiTrustGateLadder = []paneCapture{
	{name: "geminiTrustGatePane80", width: 80, note: "headline on one line", pane: geminiTrustGatePane80},
	{name: "geminiTrustGatePane40", width: 40, note: "headline wrapped across the box border", pane: geminiTrustGatePane40},
	{name: "geminiTrustGatePane24", width: 24, note: "option-row directory name elided", pane: geminiTrustGatePane24},
}

// The rung below the floor, as one shared value. It is not in the ladder — the gate misses
// there — but three tests below feed it through the same helpers, and building it inline in
// each let the note drift, so the same rung printed two different subtest names.
var geminiTrustGateMissedRung = paneCapture{
	name: "geminiTrustGatePane20", width: 20, note: "gate missed: option rows truncated",
	pane: geminiTrustGatePane20,
}

// geminiAllCaptures is every rung including the miss — the set the file-level invariants below
// range over, so a rung added to either list is covered without editing them.
func geminiAllCaptures() []paneCapture {
	return append(append([]paneCapture(nil), geminiTrustGateLadder...), geminiTrustGateMissedRung)
}

// geminiDrivenPaneHeight is the pane height every rung was driven at: drive-agent.sh's default
// geometry is 120x40 and the ladder overrode only the width.
const geminiDrivenPaneHeight = 40

// geminiHeadlineFitsAtWidth is the narrowest DRIVEN width at which the dialog's headline still
// renders on one physical line. Below it the headline wraps across the box border and stops
// being reachable at any GateWindow, which is the whole of #713's "the obvious fix is wrong".
// The true boundary is somewhere in (40, 80] and has not been driven — this is the measured
// value, not the mechanism's threshold.
const geminiHeadlineFitsAtWidth = 80

// geminiCaptureRows is how tall each CAPTURE is, as DATA — the one place in the tree that owns
// these four numbers. drive-agent.sh's warning not to predicate the resize rule on height
// cites this table instead of restating it, because restating it is how the 24 rung came to
// read 35 rows in one file and 37 in the other while every check stayed green.
var geminiCaptureRows = map[string]int{
	"geminiTrustGatePane80": 33,
	"geminiTrustGatePane40": 38,
	"geminiTrustGatePane24": 37,
	"geminiTrustGatePane20": 38,
}

// geminiDialogRows is how tall the DIALOG BOX is — top border through bottom border — which is
// a different measurement from the capture it sits in and the one every height argument about
// this gate actually needs. Both chrome.go's bottomBoxBlock and geminiTrustGateVisible reasoned
// about "a pane shorter than the dialog" and quoted 37 for the width-24 rung, which is that
// rung's capture height; the box is 28, so the threshold at which the top border scrolls off
// was overstated by nine rows in the direction that makes the anchor look safer than it is.
// Recomputed by TestGeminiDialogBoxIsFullyOnScreen.
var geminiDialogRows = map[string]int{
	"geminiTrustGatePane80": 15,
	"geminiTrustGatePane40": 20,
	"geminiTrustGatePane24": 28,
	"geminiTrustGatePane20": 33,
}

// The counts above, recomputed from the captures — EXACTLY, not as an upper bound. An earlier
// draft asserted only rows <= geminiDrivenPaneHeight and told the reader in its own docstring
// that it recomputed the four numbers; it did not, so any of them could be edited to any value
// under 40 and stay green. That is the #648/#665 failure in the guard written to prevent it,
// and it is exactly how 35 survived: nothing ever compared it to a capture.
func TestGeminiCapturesAllFitTheDrivenPaneHeight(t *testing.T) {
	require.Len(t, geminiCaptureRows, len(geminiAllCaptures()),
		"every capture needs a row count and vice versa, or the table stops covering the set")

	for _, c := range geminiAllCaptures() {
		t.Run(c.label(), func(t *testing.T) {
			rows := len(strings.Split(strings.TrimPrefix(c.pane, "\n"), "\n"))
			want, ok := geminiCaptureRows[c.name]
			require.True(t, ok, "%s has no entry in geminiCaptureRows", c.name)
			require.Equal(t, want, rows,
				"%s is %d rows, not the %d geminiCaptureRows publishes — correct the table and "+
					"anything citing it", c.name, rows, want)
		})
	}
}

// The dialog box's own height, recomputed, and the property the header's resize argument rests
// on: the whole box is on screen in every capture. A draft asserted rows <= the pane height
// instead and told the reader it had shown "nothing overflows" — a capture cannot exceed its
// pane, so that could not fail, while the session's output HAD scrolled at three of four rungs
// (the logo and the notice are cut). The box fitting is the claim that is both true and load-
// bearing: it is what makes "resize is unsafe once the dialog is taller than the pane" a rule
// that would have licensed the width-40 rung that lied.
func TestGeminiDialogBoxIsFullyOnScreen(t *testing.T) {
	require.Len(t, geminiDialogRows, len(geminiAllCaptures()),
		"every capture needs a box height and vice versa, or the table stops covering the set")

	for _, c := range geminiAllCaptures() {
		t.Run(c.label(), func(t *testing.T) {
			lines := strings.Split(strings.TrimPrefix(c.pane, "\n"), "\n")
			top, bottom := -1, -1
			for i, line := range lines {
				if strings.Contains(line, "╭") {
					top = i
				}
				if strings.Contains(line, "╰") {
					bottom = i
				}
			}
			// This is the load-bearing assertion, not the equality below it: a box whose top
			// border had scrolled away would have no measurable height at all, and that — not
			// any arithmetic on the capture's own row count — is what "the box fits" means.
			require.GreaterOrEqualf(t, top, 0,
				"%s renders no top border, so the box did not fit the %d-row pane it was driven "+
					"at and the header's rejection of a height-based resize rule is void",
				c.name, geminiDrivenPaneHeight)
			require.Greater(t, bottom, top, "%s: the bottom border must follow the top one", c.name)

			rows := bottom - top + 1
			require.Equal(t, geminiDialogRows[c.name], rows,
				"%s's dialog box is %d rows, not the %d geminiDialogRows publishes — correct the "+
					"table and the height arguments in chrome.go and registry.go that cite it",
				c.name, rows, geminiDialogRows[c.name])
		})
	}
}

// Every rung ends at the dialog's own box border, which is the fact the gate's liveness anchor
// rests on (geminiTrustGateVisible: the border must be the last non-empty line). It holds at the
// width-20 miss too — that rung misses on the truncated literal, not on the anchor — so a future
// gemini that renders a footer beneath the dialog fails here, naming the cause, rather than
// silently taking every rung's gate down.
func TestGeminiCapturesEndAtTheDialogBorder(t *testing.T) {
	for _, c := range geminiAllCaptures() {
		t.Run(c.label(), func(t *testing.T) {
			_, ok := bottomBoxBlock(c.pane)
			require.True(t, ok,
				"%s must end with the dialog's bottom border; the gate's liveness anchor is "+
					"exactly that and nothing else re-measures it", c.name)
		})
	}
}

// #713 on the height axis, which is the failure the first draft of this fix shipped. The
// dialog is taller than the pane it renders in far more often than not: the agent's tmux
// session is sized to the PREVIEW pane (session/instance.go SetPreviewSize ←
// ui/tabbed_window.go SetSize), which is a few rows shorter than the terminal and about half
// as wide — and this dialog's box is 28 rows at width 24 (geminiDialogRows; the 37 this
// sentence used to give is that rung's CAPTURE height). When the top border has scrolled off, an
// anchor that scans upward for a matching border finds none and takes the gate down: missed
// gate → PaneIdle → Ready → the false completion ding #713 is about. Measured on the earlier
// form, the width-24 rung went false at pane height 25 (an ordinary 30-row terminal) and the
// width-40 rung at 15.
//
// So the block is bounded by the run of side-walled rows above the border instead, and this
// sweeps every rung down to a pane holding only the option rows. The floor is the ding's
// cause, so it is asserted at heights nobody would call exotic.
func TestGeminiTrustGateSurvivesADialogTallerThanThePane(t *testing.T) {
	tail := func(pane string, rows int) string {
		lines := strings.Split(strings.TrimPrefix(pane, "\n"), "\n")
		if len(lines) <= rows {
			return pane
		}
		return strings.Join(lines[len(lines)-rows:], "\n")
	}

	for _, c := range geminiTrustGateLadder {
		t.Run(c.label(), func(t *testing.T) {
			require.Greater(t, geminiCaptureRows[c.name], 8,
				"the sweep below only means something while the capture is taller than the "+
					"shortest pane it is truncated to")

			for _, height := range []int{30, 25, 20, 15, 10, 8} {
				_, up := gemini.GateUp(tail(c.pane, height))
				require.Truef(t, up,
					"%s must still gate in a %d-row pane: the dialog BOX is %d rows, so its top "+
						"border has scrolled off and only the walls above the bottom border "+
						"are left to bound the block", c.label(), height, geminiDialogRows[c.name])
			}
		})
	}
}

// The other direction the block has to be bounded in. A pane whose last non-empty line is a
// rule the agent printed itself — a markdown horizontal rule, a table edge — with any earlier
// rule above it used to hand back everything in between as "box interior", uncapped. Measured
// on the earlier form: 60 lines of transcript bracketed by two plain rules, with the option
// row quoted at the top of the span, raised the gate. poll.go ranks the gate above the busy
// marker, so that is a streaming agent reported as waiting on a setup screen with its queued
// prompt withheld — #342's direction.
//
// The wall run needs no gateRegionCap/aboveBoxBlockCap equivalent to stop this: it ends at the
// first line that is not box interior, and transcript never is. The distance is swept so a
// future change that reintroduces an upward SCAN cannot pass by being narrowly capped.
func TestGeminiTrustGateIgnoresTranscriptBetweenTwoRules(t *testing.T) {
	// The span CLOSES with a real bottom border, not the plain rule the original measurement
	// used: isBoxBottomBorder now rejects a bare "────", so a guard built out of two of them
	// would pass on the anchor check and never reach the wall run it exists to test.
	const rule = "────────────────────────────────"
	const border = "╰──────────────────────────────╯"

	for _, gap := range []int{1, 5, 39, 60} {
		// gap is the number of lines BETWEEN the two rules, with the quoted row at the top —
		// so the quoted row sits exactly gap lines above the closing border, which is what the
		// failure message and chrome.go's "60 transcript lines" both say. Seeding the filler
		// loop from len(lines) instead of 1 made gap=60 build a 59-line span.
		lines := []string{rule, "  reviewing the gate: \"● 1. Trust folder (atrium)\" is the accept row."}
		for i := 1; i < gap; i++ {
			lines = append(lines, "  ✦ …and the decline row below it.")
		}
		lines = append(lines, border)

		require.Len(t, lines, gap+2,
			"the span between the two rules must be exactly the %d lines the message claims, or "+
				"the sweep's largest case is smaller than chrome.go says it measured", gap)

		_, up := gemini.GateUp(strings.Join(lines, "\n"))
		require.Falsef(t, up,
			"a quoted option row %d lines above a border is transcript, not a live dialog; "+
				"nothing between two rules is box interior unless it is walled", gap)
	}
}

// The anchor's residual exposure, pinned as behaviour rather than left in prose. bottomBoxBlock's
// doc used to promise that a quoted phrase "cannot reach the returned block"; quoted BOX ART can,
// because it carries walls and a corner border like any other box. When such a quote is the last
// thing on the pane the gate fires on a working session — the #342 direction — and this repo's
// own fixtures, PR bodies and review comments are exactly the text that reproduces it.
//
// It is asserted TRUE deliberately: this is the known cost of a text-only liveness anchor, and a
// guard that recorded the wish instead of the behaviour would be the same class of defect as the
// sentence it replaced. It is narrower than what it replaced (the quote must END the pane, so it
// is transient where a bottom-N window was persistent) but it is not zero. If a later change
// closes it — a check that the block's walls line up in a single column, a width-consistency
// test — this test fails, and the fix is to flip it and correct chrome.go's paragraph, not to
// delete it.
func TestGeminiTrustGateFiresOnQuotedBoxArtEndingThePane(t *testing.T) {
	quoted := strings.Join([]string{
		"✦ The fixture in gemini_pane_test.go renders like this at width 40:",
		"",
		"   ╭────────────────────────────╮",
		"   │ Do you trust the files in  │",
		"   │ ● 1. Trust folder (fresh40)│",
		"   │   3. Don't trust           │",
		"   ╰────────────────────────────╯",
	}, "\n")

	_, up := gemini.GateUp(quoted)
	require.True(t, up,
		"quoted box art that ends the pane DOES gate — the anchor narrows this exposure, it "+
			"does not remove it; chrome.go's bottomBoxBlock says so and this is the measurement")

	// …and the narrowing is real: one more line of transcript below the quote takes it down.
	_, up = gemini.GateUp(quoted + "\n\n  …which is why the option row is the anchor.")
	require.False(t, up,
		"the quote only gates while it is the last thing on screen; anything rendered below "+
			"the quoted border drops it")
}

// isBoxBottomBorder, from the file of its only consumer. Three shapes isHorizontalRule accepts
// and a box's bottom edge is not: a bare rule the agent printed, a box's interior divider, and
// a heavy-drawn frame. The first two used to anchor the gate.
func TestBottomBoxBlockNeedsABottomCornerNotAnyRule(t *testing.T) {
	const row = "│ ● 1. Trust folder  │"

	for _, tc := range []struct {
		name, ending string
		want         bool
	}{
		{"bottom border", "╰────────────────────╯", true},
		{"square bottom border", "└────────────────────┘", true},
		{"bare rule the agent printed", "────────────────────", false},
		{"interior divider — the box's lower half is missing", "├────────────────────┤", false},
		{"top border, so the box opens rather than ends here", "╭────────────────────╮", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, ok := bottomBoxBlock(strings.Join([]string{row, tc.ending}, "\n"))
			require.Equal(t, tc.want, ok,
				"a block may only be anchored by a box's BOTTOM edge; %q", tc.ending)
		})
	}
}

// The heavy box, which the wall predicate used to claim and the anchor could never deliver:
// isHorizontalRule accepts no heavy glyph, so '┗━━┛' is not a rule and the '┃' branch of
// isBoxWallLine was unreachable. Pinned as the limit it is, so an adapter that draws heavy
// boxes finds this instead of a silent miss.
func TestBottomBoxBlockDoesNotAnchorAHeavyBox(t *testing.T) {
	_, ok := bottomBoxBlock(strings.Join([]string{
		"┏━━━━━━━━━━━━━━━━━━━━┓",
		"┃ ● 1. Trust folder  ┃",
		"┗━━━━━━━━━━━━━━━━━━━━┛",
	}, "\n"))
	require.False(t, ok,
		"heavy box drawing is unsupported at BOTH ends — teaching isBoxWallLine '┃' without "+
			"teaching isHorizontalRule '━' only advertises support")

	require.False(t, isHorizontalRule("┗━━━━━━┛"), "the heavy bottom edge is not a rule")
	require.False(t, isBoxWallLine("┃ x ┃"), "so the heavy wall must not be accepted either")
}

// The unwalled padding row, whose failure is TOTAL rather than partial. isBoxWallLine's doc
// used to say such a row "would truncate the block at that row"; when it sits directly above
// the border there is no block at all and the gate goes down.
func TestBottomBoxBlockLosesTheBlockToAnUnwalledPaddingRow(t *testing.T) {
	block, ok := bottomBoxBlock(strings.Join([]string{
		"╭────────────────────╮",
		"│ ● 1. Trust folder  │",
		"",
		"╰────────────────────╯",
	}, "\n"))
	require.False(t, ok, "an unwalled row directly above the border loses the block entirely")
	require.Empty(t, block)

	// One row further up, the loss really is a truncation: the walled row survives.
	block, ok = bottomBoxBlock(strings.Join([]string{
		"│ ● 1. Trust folder  │",
		"",
		"│   3. Don't trust   │",
		"╰────────────────────╯",
	}, "\n"))
	require.True(t, ok)
	require.Equal(t, []string{"│   3. Don't trust   │"}, block,
		"the run ends at the unwalled row, so the accept row above it is not in the block")
}

// geminiPre055TrustGatePane is the shape the repo recorded for gemini 0.27, restored as a named
// artifact. It is what TestGeminiGateAndResume asserted on `main` before #713 replaced it with a
// 0.55.1-shaped boxed pane — and two paragraphs of geminiTrustGateVisible's doc reason about
// "the tree's only 0.27-shaped artifact" while that replacement had left the tree with none.
//
// What it is NOT: evidence of what 0.27 rendered. It is hand-composed, it was hand-composed
// then too, and nobody has driven 0.27. It is evidence of one thing only — what this repo's own
// fixture claimed the dialog looked like — and that is exactly the artifact the doc cites.
var geminiPre055TrustGatePane = "Do you trust this folder?\n● 1. Trust folder\n  2. Trust parent folder"

// The older-install case, as data. Everything geminiTrustGateVisible's doc says about it is
// checkable here: the 0.27 shape has the accept row, has no decline row, is UNBOXED, and this
// matcher returns false on it.
//
// The last of those is why the reviewer's mitigation — keeping the old headline literal as a
// second Gate — does not do what it looks like it does. If 0.27 drew a box, "Trust folder" is
// already inside it and the shipped matcher covers that install with no second literal. If it
// did not, no ANCHORED literal can reach it, headline included. So a second Gate helps only
// unanchored, which reopens the PROSE class for every 0.55+ user — on a literal whose most
// likely source of quoted text is this repo's own issue tracker. The trade is not "cover the
// old installs for free"; it is "re-introduce #342 for the current ones", and it is declined.
func TestGeminiPre055ShapeIsUncoveredAndTheSecondLiteralWouldNotCoverIt(t *testing.T) {
	require.Contains(t, geminiPre055TrustGatePane, "Trust folder",
		"the 0.27 shape carries the accept row the shipped literal keys on")
	require.NotContains(t, geminiPre055TrustGatePane, "Don't trust",
		"…and no decline row, which is why round 1's conjunction would have cost coverage")
	require.NotContains(t, geminiPre055TrustGatePane, "│",
		"…and no box, which is why the anchor does not cover it either")

	_, up := gemini.GateUp(geminiPre055TrustGatePane)
	require.False(t, up,
		"the shipped gate returns FALSE on the 0.27 shape: an install older than the pin is "+
			"uncovered, doctor is silent about it (installed < verified is not drift), and that "+
			"is disclosed in registry.go rather than mitigated")

	// The mitigation, measured: anchored, the headline adds nothing the shipped literal does
	// not already have — both miss for the same reason, the absent box.
	anchored := *gemini
	anchored.Gates = []Gate{{Match: func(c string) bool {
		block, ok := bottomBoxBlock(c)
		if !ok {
			return false
		}
		return strings.Contains(strings.Join(block, "\n"), "Do you trust this folder")
	}}}
	_, up = anchored.GateUp(geminiPre055TrustGatePane)
	require.False(t, up,
		"a box-anchored second literal misses the 0.27 shape too, so it buys no coverage; only "+
			"an UNANCHORED one reaches it, and that is the trade this gate declines")
}

// A ladder that can lose a rung silently is not a ladder — deleting an entry just runs fewer
// subtests. Asserted as the exact rung list, as codex's TestCodexLaddersKeepEveryDrivenRung
// is, so a vanished rung names itself.
func TestGeminiTrustGateLadderKeepsEveryDrivenRung(t *testing.T) {
	require.Equal(t, []string{
		"width 24 geminiTrustGatePane24 (option-row directory name elided)",
		"width 40 geminiTrustGatePane40 (headline wrapped across the box border)",
		"width 80 geminiTrustGatePane80 (headline on one line)",
	}, ladderRungs(geminiTrustGateLadder), "the trust gate was driven natively at 80/40/24")

	requireDistinctCaptures(t, "geminiTrustGateLadder", geminiTrustGateLadder)
	require.NotContains(t, []string{geminiTrustGatePane80, geminiTrustGatePane40, geminiTrustGatePane24},
		geminiTrustGatePane20,
		"the width-20 miss must stay a distinct capture; a copy of a passing rung would make "+
			"the negative guard below assert nothing")
}

// The shipped gate fires at every rung it claims — and the rungs are exactly the ones
// paneCoverage["gemini/gate"] publishes, so the two can never disagree about what was driven.
//
// This is deliberately NOT justified as "a better failure message than the generic loop". An
// earlier draft said pane_width_test.go's TestEveryCoveredMatcherFiresAtEveryCapturedWidth
// would name "a map key" rather than gemini; it does not — it runs t.Run(key+"/"+c.label())
// and interpolates key into the message, so it already reports "gemini/gate does not fire on
// geminiTrustGatePane40". What this adds is the coupling asserted on the first line: the
// generic loop proves the predicate fires on whatever paneCoverage holds, and cannot notice
// if that entry stops being this ladder.
func TestGeminiTrustGateDetectedAtEveryDrivenWidth(t *testing.T) {
	require.Equal(t, ladderRungs(geminiTrustGateLadder), ladderRungs(paneCoverage["gemini/gate"]),
		"paneCoverage must publish this exact ladder, or the width table is describing a "+
			"different set of panes than this file drove")

	for _, c := range geminiTrustGateLadder {
		t.Run(c.label(), func(t *testing.T) {
			_, up := gemini.GateUp(c.pane)
			require.True(t, up, "the folder-trust gate must be detected at %s", c.label())
		})
	}
}

// THE #713 guard: the obvious fix is wrong, and it is wrong in a way a width-80 fixture
// cannot show. Substituting the CURRENT headline — the literal a bundle grep hands you, and
// the one #713 had to rule out — passes at 80 and fails at every rung where it wraps.
//
// The name says "once it wraps" rather than a width because that is what the mechanism is
// keyed to, and because the boundary between 80 and 40 has not been driven: the rungs below
// prove the wrapped case, not every width under 80.
//
// The sweep over GateWindow is the load-bearing half, and it is what makes this different
// from codex's TestCodexTrustGateHeadlineFallsOutsideTheDefaultWindowAtWidth20. Codex's
// headline is merely out of BUDGET at 20, so widening GateWindow recovers it — the remedy
// codex took. Gemini's is out of REACH: the box border lands between the wrapped halves and
// flattenChrome's whitespace join cannot repair it, so no budget helps and GateWindow is not
// an alternative fix here at all. If this ever starts passing at 40, gemini stopped drawing
// the box or stopped wrapping, and the Gates comment needs re-measuring.
func TestGeminiTrustGateHeadlineIsUnreachableOnceItWraps(t *testing.T) {
	const headline = "Do you trust the files in this folder"

	require.Equal(t, geminiHeadlineFitsAtWidth, geminiTrustGateLadder[0].width,
		"the widest driven rung is the one the headline fits on; if a wider one is ever driven, "+
			"decide whether it fits before this sweep silently skips it")

	fires := func(pane string, window int) bool {
		probe := *gemini
		probe.GateWindow = window
		probe.Gates = []Gate{{Contains: []string{headline}}}
		_, up := probe.GateUp(pane)
		return up
	}

	require.Equal(t, WindowPrompt, gemini.gateWindow(),
		"gemini keeps the default budget; the sweep below is what says widening it would not help")

	require.True(t, fires(geminiTrustGatePane80, WindowPrompt),
		"at 80 the headline fits one line, which is exactly why a single wide fixture would "+
			"have shipped this literal")

	// Two budgets, not a sample of seven, and the claim "at EVERY GateWindow" is derived rather
	// than asserted rung by rung. liveChromeLines(content, n) returns the last n non-empty
	// lines, so its result at n is a suffix of its result at n+1 and strings.Contains is
	// monotone in n: a match at some window implies a match at every larger one, and a miss at
	// the largest implies a miss at every smaller one. So the saturating window — one that
	// exceeds every capture's non-empty line count, making it the whole pane — settles the
	// range, and WindowPrompt is kept because it is the budget gemini actually ships.
	//
	// An earlier draft swept {WindowPrompt, 20, 24, 30, 40, 60, 200} and read as exhaustive
	// while proving six redundant points and no more of the range than 200 alone does.
	const saturating = 200
	for _, c := range geminiAllCaptures() {
		nonEmpty := 0
		for _, line := range strings.Split(c.pane, "\n") {
			if strings.TrimSpace(line) != "" {
				nonEmpty++
			}
		}
		require.Greaterf(t, saturating, nonEmpty,
			"%s has %d non-empty lines, so GateWindow %d is no longer the whole pane and the "+
				"monotonicity argument stops covering every larger window", c.label(), nonEmpty, saturating)
	}

	// Every rung DERIVED from the ladder rather than rebuilt here. An earlier draft hand-listed
	// the 40 and 24 rungs without their notes, so the same capture printed one subtest name here
	// and a different one in the ladder test — and a rung driven later would not have been
	// swept at all, because a copy cannot grow.
	for _, window := range []int{WindowPrompt, saturating} {
		for _, c := range geminiAllCaptures() {
			if c.width >= geminiHeadlineFitsAtWidth {
				continue // where it fits on one line the headline IS reachable — asserted above
			}
			require.False(t, fires(c.pane, window),
				"%s: the headline must stay unreachable at GateWindow %d — the box border sits "+
					"between its wrapped halves, so no budget reassembles it", c.label(), window)
		}
	}
}

// The disclosed floor, held as a guard rather than left in prose. The gate is NOT detected at
// width 20, and pinning that is the point: an undisclosed miss reads as coverage, and #648's
// whole argument is that a table which cannot report what it fails to prove is worse than no
// table.
//
// agy's gate stops at 24 too, but that is NOT the same fact and an earlier draft here called
// it one. paneCoverage's "agy/gate" holds 120/28/24 and nothing narrower, while "agy/busy" and
// "agy/prompt/confirmation" both reach 20 — so 24 is simply the narrowest width agy's gate has
// been driven at, an evidence gap. Gemini's 24 is a measured cliff: 20 WAS driven, and this
// test is the record that it misses.
//
// It fails SAFE — a missed gate reads as idle, and gemini's dialog is not a composer either
// (the sibling test below), so the #512 accident of typing a queued prompt into the menu
// cannot happen at this width. What the user loses is the "waiting on setup screen" hint and
// gains a false completion ding, which is the #713 bug surviving at one width.
//
// Asserted against BOTH option-row literals, not just the gate's verdict, so a future edit
// that "fixes" this by shortening one of them has to come here and say so.
func TestGeminiTrustGateOptionRowsAreTruncatedAtWidth20(t *testing.T) {
	_, up := gemini.GateUp(geminiTrustGatePane20)
	require.False(t, up,
		"width 20 is a known miss; if this starts passing, new evidence was driven and "+
			"geminiTrustGatePane20 belongs in the ladder with wantRungs updated")

	require.NotContains(t, geminiTrustGatePane20, "Trust folder",
		"the miss must be TRUNCATION of the row, not a windowing accident — the pane renders "+
			"\"Trust fo…\"")
	require.NotContains(t, geminiTrustGatePane20, "Don't trust",
		"…and \"Don't tr…\" for the decline row")

	// Those two assertions are the whole proof that no window recovers this: they range over the
	// ENTIRE pane, so the literals are absent from every possible window of it. An earlier draft
	// tried to say the same thing by sweeping probe.GateWindow over the shipped adapter, which
	// measured nothing at all — GateUp short-circuits on Match before it ever calls gateWindow(),
	// so all three iterations ran byte-identical code while the message claimed a measurement.
	// The gate this rung misses is also anchored on the box, not a window; the miss is lexical.
	require.True(t, geminiTrustGateMissedRung.width < geminiTrustGateLadder[len(geminiTrustGateLadder)-1].width,
		"the miss must sit below the ladder's floor, or 'the narrowest rung misses' is not what "+
			"this test is pinning")
}

// The blast radius, pinned. #713's first question was whether this is the #512 class — a
// gate whose selector row also reads as a composer, so AwaitingInput goes true and the queued
// first prompt is typed into the menu. It is not: gemini draws its selector with "●" and "1."
// rather than the composer glyph, so no rung reads as an input box, and no prompt matcher
// claims the pane either. The consequence of the missed gate therefore stops at a wrong
// status and a false ding.
//
// The width-20 miss is included deliberately. That is the rung where the gate does NOT fire,
// which makes it the only one where a composer reading would actually be dangerous.
func TestGeminiTrustGateIsNeitherComposerNorPrompt(t *testing.T) {
	for _, c := range geminiAllCaptures() {
		t.Run(c.label(), func(t *testing.T) {
			require.False(t, gemini.InputBoxVisible(c.pane),
				"the trust dialog must not read as a composer, or a queued prompt would be "+
					"typed into it (#512)")
			_, ok := gemini.DetectPrompt(c.pane)
			require.False(t, ok, "the trust dialog is a gate, not a prompt")
		})
	}
}

// The composer veto's premise, over the WHOLE block rather than a window of it. The gate
// rejects any bottom box holding a composer glyph, so it is only correct if the real dialog
// never renders one — and the sibling test above cannot say that: InputBoxVisible reads the
// last WindowPrompt (15) non-empty lines, while the block it would have to speak for is deeper
// than that on the narrow rungs. How much deeper is measured below rather than stated here. A
// glyph above that window leaves it green and takes the gate down. The registry comment cited
// it for this property until #715 round 4; this is the guard that actually holds it.
func TestGeminiCapturesRenderNoComposerGlyphInsideTheDialog(t *testing.T) {
	deepest, deepestName := 0, ""
	for _, c := range geminiAllCaptures() {
		if block, ok := bottomBoxBlock(c.pane); ok && len(block) > deepest {
			deepest, deepestName = len(block), c.name
		}
	}
	// Asserted over the SET, not per capture: at width 80 the dialog's interior is 13 lines and
	// InputBoxVisible would in fact reach all of it. What makes this guard non-redundant is that
	// the narrow rungs are deeper than the window, and those are the rungs the veto has to be
	// right about.
	require.Greater(t, deepest, WindowPrompt,
		"the deepest block is %s at %d lines; once no capture is deeper than the %d-line window "+
			"InputBoxVisible reads, this test says nothing the sibling test does not",
		deepestName, deepest, WindowPrompt)

	for _, c := range geminiAllCaptures() {
		t.Run(c.label(), func(t *testing.T) {
			block, ok := bottomBoxBlock(c.pane)
			require.True(t, ok, "%s must present a bottom box at all", c.name)

			for i, line := range block {
				require.Falsef(t, isInputBoxLine(line, defaultPrompts),
					"%s line %d (%q) reads as a composer, so geminiTrustGateVisible's veto "+
						"would reject the real dialog", c.name, i, line)
			}
		})
	}
}

// The literal 0.27 shipped, against the panes 0.55.1 actually draws. It matches nothing at
// any width — which is the whole of #713, and is why the drift pin moving from 0.27 to 0.55.1
// is a record of re-verification rather than a formality.
func TestGeminiPre055TrustGateLiteralMatchesNothing(t *testing.T) {
	probe := *gemini
	probe.Gates = []Gate{{Contains: []string{"Do you trust this folder"}}}

	for _, c := range geminiAllCaptures() {
		_, up := probe.GateUp(c.pane)
		require.False(t, up,
			"%s: the 0.27 literal is absent from every 0.55.1 pane and from the bundle", c.label())
	}
}

// geminiProsePane is a WORKING gemini pane whose transcript tail happens to contain the
// decline row's wording as ordinary English. Composed, not captured — it is a statement about
// the matcher, not about what gemini renders, and every line above the spinner is text an
// agent could plausibly emit while reviewing code.
var geminiProsePane = strings.Join([]string{
	"✦ Here are the review notes for the input handler:",
	"",
	"  1. Don't trust user input; validate every field before use.",
	"  2. Prefer a whitelist over a blacklist for the file extensions.",
	"  3. The parser should reject anything over 4 KiB.",
	"",
	"⠏ Reticulating splines... (esc to cancel, 12s)",
	"",
	"╭──────────────────────────────────────────╮",
	"│ >                                        │",
	"╰──────────────────────────────────────────╯",
	"~/project   no sandbox   gemini-2.5-pro",
}, "\n")

// geminiPermissionsTrustPane is gemini's /permissions modify-trust dialog, composed from the
// 0.55.1 bundle's TRUST_LEVEL_ITEMS (bundle/interactiveCli-2Z3Q3FYM.js): the labels are
// `Trust this folder (${dirName})`, `Trust parent folder (${parentFolder})` and "Don't trust".
// Note the FIRST one — "Trust THIS folder" — which is what separates it from the startup
// dialog, whose label is `Trust folder (${dirName})`.
var geminiPermissionsTrustPane = strings.Join([]string{
	"✦ Opening permission settings.",
	"",
	"╭──────────────────────────────────────────────╮",
	"│ Modify trust level                           │",
	"│                                              │",
	"│ ● 1. Trust this folder (project)             │",
	"│   2. Trust parent folder (worktrees)         │",
	"│   3. Don't trust                             │",
	"╰──────────────────────────────────────────────╯",
	"~/project   no sandbox   gemini-2.5-pro",
}, "\n")

// geminiBothRowsQuotedPane is a WORKING pane whose transcript quotes BOTH option rows inside
// the gate's window. It exists because the round-2 fix — a Match requiring both rows — was
// still a lexical test, and this is the pane that defeats it. Composed, and reachable in this
// very repo: registry.go and this file both carry the two phrases within a few lines.
var geminiBothRowsQuotedPane = strings.Join([]string{
	"✦ I read session/agent/registry.go. The startup gate keys on:",
	"",
	"    - \"Trust folder\" (the accept row)",
	"    - \"Don't trust\" (the decline row)",
	"",
	"  Requiring both is what keeps ordinary prose from firing it.",
	"",
	"⠏ Reticulating splines... (esc to cancel, 12s)",
	"",
	"╭──────────────────────────────────────────╮",
	"│ >                                        │",
	"╰──────────────────────────────────────────╯",
	"~/project   no sandbox   gemini-2.5-pro",
}, "\n")

// geminiWrapSynthesisPane renders NEITHER option row: no line contains "Trust folder". The two
// words land either side of a hard wrap, which flattenChrome's whitespace join splices into the
// literal. Composed to be the minimal statement of that mechanism.
var geminiWrapSynthesisPane = strings.Join([]string{
	"✦ On handling third-party archives:",
	"",
	"  Never Trust",
	"  folder contents from an untrusted source; validate every entry.",
	"",
	"⠏ Thinking... (esc to cancel, 4s)",
	"",
	"╭──────────────────────────────────────────╮",
	"│ >                                        │",
	"╰──────────────────────────────────────────╯",
	"~/project   no sandbox   gemini-2.5-pro",
}, "\n")

// THE REGRESSION GUARD for the fix #713 shipped, in both of the shapes that fooled an earlier
// round of it. The gate must be DOWN on a working pane that merely talks about the dialog.
//
// Two counterfactuals run first, so the danger is demonstrated rather than asserted, and they
// fail differently on purpose:
//
//   - The ALTERNATION (round 1) fires on either row alone, and "Don't trust" is ordinary
//     English. It is true on both panes here.
//   - The CONJUNCTION (round 2) needs both, which handles one-row prose — and is still true on
//     geminiBothRowsQuotedPane, because requiring two literals in a window is a narrower
//     lexical test, not a liveness test.
//
// poll.go checks GateUp before HasBusyMarker on purpose ("a false gate beats positive proof of
// work"), so either failure is a row reporting "waiting on setup screen" while its agent
// streams, with its queued prompt withheld — #342's direction. The bar to clear is the literal
// they replaced: "Do you trust this folder" could not collide with prose at all, so a fix that
// collides is a REGRESSION, not merely a weak fix. That comparison is asserted too.
func TestGeminiTrustGateIgnoresItsOwnRowsInProse(t *testing.T) {
	alternation := *gemini
	alternation.Gates = []Gate{{Contains: []string{"Trust folder", "Don't trust"}}}
	conjunction := *gemini
	conjunction.Gates = []Gate{{Match: func(c string) bool {
		flat := flattenChrome(c, WindowPrompt)
		return strings.Contains(flat, "Trust folder") && strings.Contains(flat, "Don't trust")
	}}}
	rotted := *gemini
	rotted.Gates = []Gate{{Contains: []string{"Do you trust this folder"}}}

	for _, tc := range []struct {
		name       string
		pane       string
		wantAltUp  bool
		wantConjUp bool
	}{
		{"decline row as prose", geminiProsePane, true, false},
		{"both rows quoted", geminiBothRowsQuotedPane, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.True(t, gemini.HasBusyMarker(tc.pane),
				"the pane must be visibly working, or the ordering in poll.go is not what is at stake")

			_, altUp := alternation.GateUp(tc.pane)
			require.Equal(t, tc.wantAltUp, altUp,
				"round 1's alternation — if this stops firing the counterfactual proves nothing")
			_, conjUp := conjunction.GateUp(tc.pane)
			require.Equal(t, tc.wantConjUp, conjUp,
				"round 2's conjunction: it closed one-row prose and not this")

			_, rottedUp := rotted.GateUp(tc.pane)
			require.False(t, rottedUp,
				"the 0.27 literal did not collide with prose, which is the bar any fix has to clear")

			_, up := gemini.GateUp(tc.pane)
			require.False(t, up,
				"the shipped gate needs the row inside a box that ENDS the pane, so a transcript "+
					"quoting it cannot latch a working session to PaneGate")
		})
	}
}

// The wrap that manufactures a literal nothing renders. flattenChrome joins physical lines with
// a space to repair wrapped chrome, and that repair cuts both ways: two adjacent transcript
// lines can splice into a phrase neither contains. bottomBoxBlock returns its interior
// unflattened precisely so this cannot happen, and this is the guard that says so.
func TestGeminiTrustGateIsNotSynthesizedAcrossAWrap(t *testing.T) {
	for _, line := range strings.Split(geminiWrapSynthesisPane, "\n") {
		require.NotContains(t, line, "Trust folder",
			"the premise: no single line of this pane renders the literal")
	}
	require.Contains(t, flattenChrome(geminiWrapSynthesisPane, WindowPrompt), "Trust folder",
		"…and flattening splices it into existence, which is what the gate must not read")

	_, up := gemini.GateUp(geminiWrapSynthesisPane)
	require.False(t, up, "a spliced literal must not raise the gate")
}

// The liveness half of the anchor, stated as a property of the MATCHER: anything rendered below
// the dialog's box takes the gate down. That is what separates an open modal from one left in
// scrollback, and it is the difference between a gate and a latch — while a gate is up
// AwaitingInput (session/tmux/tmux.go) is false, so a stale one withholds the queued FIRST
// prompt indefinitely, the moment a queued prompt is most likely to exist. agy pins the same
// property with agyAcceptedGatePane and earns it differently: agy REPLACES the trust screen.
//
// What gemini actually leaves on screen after the dialog is answered has NOT been driven — that
// costs an Enter at a dialog whose acceptance writes ~/.gemini/trustedFolders.json, and the
// capture harness deliberately does not isolate the agent's config dir. So this asserts only
// what it can: the matcher stops matching the moment something is drawn beneath the box. If the
// post-acceptance pane is ever captured, it belongs here as a verbatim fixture.
func TestGeminiTrustGateDropsOnceSomethingRendersBelowIt(t *testing.T) {
	for _, tc := range []struct{ name, below string }{
		{"composer drawn below", "╭────────────────────╮\n│ >                  │\n╰────────────────────╯"},
		{"bare footer line", "~/project   no sandbox   gemini-2.5-pro"},
		{"a single character", "x"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, up := gemini.GateUp(geminiTrustGatePane80 + "\n" + tc.below)
			require.False(t, up,
				"with %s the dialog is no longer the bottom-most live element, so the gate must "+
					"drop rather than hold the queued first prompt", tc.name)
		})
	}

	// The control: the same pane with nothing below it IS the gate. Without this the test above
	// would pass just as well against a matcher that never fires.
	_, up := gemini.GateUp(geminiTrustGatePane80)
	require.True(t, up, "the unmodified capture must still raise the gate")

	// Trailing blank rows are what capture-pane pads a short dialog with, so they must not read
	// as "something rendered below".
	_, up = gemini.GateUp(geminiTrustGatePane80 + "\n\n   \n\n")
	require.True(t, up, "blank padding below the box is not a render; the gate must survive it")
}

// The composer is a box too — the one hole "bottom-most box" does not close by itself. A pane
// ending at a composer border whose typed text happens to contain the gate's literal raised it,
// and InputBoxVisible is true there, so the result is a false gate with the user's own
// keystrokes as the trigger: AwaitingInput goes false and the queued prompt is withheld while
// they are looking at an active composer.
//
// gemini's real render puts a footer line below the composer, which is why this is narrow — the
// first case below is what actually ships and it was already safe. The second is the one that
// was not, and a footer is not a thing to rely on.
func TestGeminiTrustGateIgnoresTypingInTheComposer(t *testing.T) {
	const typed = "│ > Trust folder is the anchor │"
	box := func(tail string) string {
		return "✦ Done.\n\n╭──────────────────────────────╮\n" + typed +
			"\n╰──────────────────────────────╯" + tail
	}

	for _, tc := range []struct{ name, pane string }{
		{"composer with gemini's footer below it", box("\n~/project   no sandbox   gemini-2.5-pro")},
		{"composer ending the pane", box("")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.True(t, gemini.InputBoxVisible(tc.pane),
				"the premise: this pane reads as a live composer")
			_, up := gemini.GateUp(tc.pane)
			require.False(t, up,
				"typing the gate's literal into the composer must not raise it — that would "+
					"withhold the queued prompt while the user is typing")
		})
	}
}

// geminiTrustGateVisible substitutes defaultPrompts for the adapter's own glyph set, because a
// package-level func cannot reference `gemini` without an initialization cycle. That is only
// sound while gemini declares no custom set; this is the guard that notices if it gains one.
func TestGeminiUsesTheDefaultComposerGlyphs(t *testing.T) {
	require.Empty(t, gemini.InputBoxPrompts,
		"gemini declares no composer glyphs, so geminiTrustGateVisible's defaultPrompts is the "+
			"same set InputBoxVisible uses; give it a custom set and the gate must be updated too")
	require.Equal(t, defaultPrompts, gemini.inputBoxPrompts())
}

// The /permissions dialog, which shares the decline row byte-identically and is separated by one
// word in the accept row: its label is `Trust this folder (${dirName})`, not `Trust folder
// (${dirName})`. Round 1's alternation reported it as the startup gate.
//
// It also leaves that dialog matched by nothing at all, which is a real gap rather than a win,
// and this test is where that is written down: reaching it needs an authenticated session, so
// it sits at the same evidence tier as gemini's busy and confirmation strings. If it is ever
// driven, it wants its own Gate — not a loosening of this one.
func TestGeminiTrustGateIgnoresThePermissionsTrustDialog(t *testing.T) {
	require.Contains(t, geminiPermissionsTrustPane, "Don't trust",
		"the collision is the premise: this dialog shares the decline row verbatim")
	require.NotContains(t, geminiPermissionsTrustPane, "Trust folder",
		"…and is separated only by \"Trust THIS folder\" in the first row")

	alternation := *gemini
	alternation.Gates = []Gate{{Contains: []string{"Trust folder", "Don't trust"}}}
	_, altUp := alternation.GateUp(geminiPermissionsTrustPane)
	require.True(t, altUp, "an alternation would misreport this as the startup trust gate")

	_, up := gemini.GateUp(geminiPermissionsTrustPane)
	require.False(t, up,
		"the folder-trust gate must not claim the /permissions dialog; that dialog is uncovered "+
			"and disclosed, not silently folded into this gate")
}

// Which literals are load-bearing, asserted one at a time — and the answer is deliberately
// asymmetric, so it is written down rather than left to be inferred from the source.
//
// The ACCEPT row is the whole lexical anchor: reword it and the gate must go down. The DECLINE
// row is NOT required, and that is a choice with a reason. Round 2 required both; the dialog
// gemini shipped at 0.27 had "Trust folder" but the tree's only fixture of it carried no
// "Don't trust" row, so a conjunction would have quietly taken the gate away from every install
// older than the pin — while doctor stayed silent, because driftExceeds only reports installed
// > verified (internal/doctor/compare.go). So one literal removes one of two ways to miss on
// an older install, and survives a reword of the row it does not read. It does not COVER the
// older shape: the anchor is a second requirement, 0.27 was never driven, and the tree's only
// artifact of that shape is unboxed and does not gate. Three files said "covers both dialog
// shapes" until #715 round 4 grepped the claim rather than the sentence in front of it.
//
// GateUp short-circuits, so a test that only ever feeds it complete dialogs cannot tell which
// literals a matcher actually reads: any of them could quietly stop appearing and the suite
// would stay green. This is the gate equivalent of TestUnprovenMatcherAlternativesArePinned,
// which covers only prompt matchers (pane_width_test.go skips gates).
func TestGeminiTrustGateReadsTheAcceptRowOnly(t *testing.T) {
	full := geminiTrustGatePane80
	require.Contains(t, full, "Trust folder")
	require.Contains(t, full, "Don't trust")

	for _, tc := range []struct {
		name   string
		drop   string
		wantUp bool
	}{
		{"accept row reworded", "Trust folder", false},
		{"decline row reworded", "Don't trust", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mangled := strings.ReplaceAll(full, tc.drop, "Xyzzy")
			_, up := gemini.GateUp(mangled)
			require.Equal(t, tc.wantUp, up,
				"with %q reworded the gate must be up=%v — see this test's doc for why the two "+
					"rows are treated differently", tc.drop, tc.wantUp)
		})
	}
}

// The plain negatives agy's gate already carries (registry_test.go pins agy false on its idle,
// confirm and accepted-gate panes) and gemini's did not.
func TestGeminiTrustGateIsDownOnEveryNonGatePane(t *testing.T) {
	for _, tc := range []struct{ name, pane string }{
		{"idle composer", geminiIdlePane},
		{"working with prose", geminiProsePane},
		{"working quoting both rows", geminiBothRowsQuotedPane},
		{"wrap synthesis", geminiWrapSynthesisPane},
		{"permissions dialog", geminiPermissionsTrustPane},
		{"empty", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, up := gemini.GateUp(tc.pane)
			require.False(t, up, "no gate is up on %s", tc.name)
		})
	}
}
