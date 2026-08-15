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
// pane", offered as the mechanism. Overflow cannot be the mechanism, because nothing
// overflows — measured over these four captures (total rows, trailing blanks already
// stripped): 80→33, 40→38, 24→35, 20→38, every one inside the 40-row pane, the widest rung
// most comfortably of all. So the divergence has some other cause, and a rule of the form
// "resize is unsafe when the dialog is taller than the pane" would have licensed exactly the
// capture that lied here. The safe rule is the unconditional one: a resized rung is not known
// to equal a native one for this dialog, so drive it with `fresh`.
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

// The shipped gate fires at every rung it claims. pane_width_test.go runs the same predicate
// over the same ladder, and this is not redundant with it: that file's loops are driven by
// paneCoverage, so this is where the failure names gemini rather than a map key.
func TestGeminiTrustGateDetectedAtEveryDrivenWidth(t *testing.T) {
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

	// Every budget from the default up to more lines than any of these panes has.
	for _, window := range []int{WindowPrompt, 20, 24, 30, 40, 60, 200} {
		for _, c := range []paneCapture{
			{name: "geminiTrustGatePane40", width: 40, pane: geminiTrustGatePane40},
			{name: "geminiTrustGatePane24", width: 24, pane: geminiTrustGatePane24},
			geminiTrustGateMissedRung,
		} {
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

	// Not a budget problem, so nothing is recoverable by widening.
	for _, window := range []int{WindowPrompt, 40, 200} {
		probe := *gemini
		probe.GateWindow = window
		_, up := probe.GateUp(geminiTrustGatePane20)
		require.False(t, up,
			"GateWindow %d cannot recover a literal the pane no longer renders", window)
	}
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
	all := append(append([]paneCapture(nil), geminiTrustGateLadder...), geminiTrustGateMissedRung)

	for _, c := range all {
		t.Run(c.label(), func(t *testing.T) {
			require.False(t, gemini.InputBoxVisible(c.pane),
				"the trust dialog must not read as a composer, or a queued prompt would be "+
					"typed into it (#512)")
			_, ok := gemini.DetectPrompt(c.pane)
			require.False(t, ok, "the trust dialog is a gate, not a prompt")
		})
	}
}

// The literal 0.27 shipped, against the panes 0.55.1 actually draws. It matches nothing at
// any width — which is the whole of #713, and is why the drift pin moving from 0.27 to 0.55.1
// is a record of re-verification rather than a formality.
func TestGeminiPre055TrustGateLiteralMatchesNothing(t *testing.T) {
	probe := *gemini
	probe.Gates = []Gate{{Contains: []string{"Do you trust this folder"}}}

	for _, c := range append(append([]paneCapture(nil), geminiTrustGateLadder...), geminiTrustGateMissedRung) {
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

// THE REGRESSION GUARD for the fix #713 shipped, and the reason the gate is a Match rather
// than a two-entry Contains.
//
// A gate's Contains is an alternation, so keying on both option rows would fire on EITHER —
// and "Don't trust" is ordinary English, not a vendor string. This test runs the counterfactual
// first so the danger is demonstrated rather than asserted: on a pane that is visibly WORKING,
// the alternation is true. poll.go checks GateUp before HasBusyMarker on purpose ("a false
// gate beats positive proof of work"), so that combination is a row reporting "waiting on
// setup screen" while its agent streams, with its queued prompt withheld — #342's direction.
//
// The bar it has to clear is the literal it replaced: "Do you trust this folder" could not
// collide with prose, so an alternation would have been a REGRESSION, not merely a weak fix.
// That comparison is asserted below too.
func TestGeminiTrustGateIgnoresItsOwnRowsInProse(t *testing.T) {
	require.True(t, gemini.HasBusyMarker(geminiProsePane),
		"the pane must be visibly working, or the ordering in poll.go is not what is at stake")

	alternation := *gemini
	alternation.Gates = []Gate{{Contains: []string{"Trust folder", "Don't trust"}}}
	_, altUp := alternation.GateUp(geminiProsePane)
	require.True(t, altUp,
		"the counterfactual must actually fire — if it stops, this test proves nothing and the "+
			"conjunction in geminiTrustGateVisible needs a new justification")

	rotted := *gemini
	rotted.Gates = []Gate{{Contains: []string{"Do you trust this folder"}}}
	_, rottedUp := rotted.GateUp(geminiProsePane)
	require.False(t, rottedUp,
		"the 0.27 literal did not collide with prose, which is why an alternation would have "+
			"traded a missed gate for a false one")

	_, up := gemini.GateUp(geminiProsePane)
	require.False(t, up,
		"the shipped gate requires BOTH option rows, so prose carrying one of them must not "+
			"latch a working session to PaneGate")
}

// The second thing the conjunction buys. gemini's /permissions dialog reuses "Don't trust"
// byte-identically, so an alternation reports a mid-session settings dialog as a startup gate.
// Requiring both rows excludes it, because its first label is "Trust THIS folder".
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

// Both rows are load-bearing, asserted one at a time. GateUp short-circuits, so a test that
// only ever feeds it complete dialogs cannot tell a two-literal matcher from a one-literal
// one: either row could quietly stop appearing in every fixture and the suite would stay
// green. This is the gate equivalent of TestUnprovenMatcherAlternativesArePinned, which
// covers only prompt matchers (pane_width_test.go skips gates).
//
// It is also the cost of the conjunction, made explicit: the vendor rewording EITHER row kills
// the gate, where an alternation would have survived on the other. That is the fail-safe
// direction — a missed gate reads as idle and doctor's drift check is what surfaces it — but
// it is a real trade and it should fail here loudly if anyone changes their mind about it.
func TestGeminiTrustGateNeedsBothOptionRows(t *testing.T) {
	full := geminiTrustGatePane80
	require.Contains(t, full, "Trust folder")
	require.Contains(t, full, "Don't trust")

	for _, tc := range []struct{ name, drop string }{
		{"accept row reworded", "Trust folder"},
		{"decline row reworded", "Don't trust"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mangled := strings.ReplaceAll(full, tc.drop, "Xyzzy")
			_, up := gemini.GateUp(mangled)
			require.False(t, up,
				"%q is load-bearing: with it reworded the gate must go DOWN, not ride on the "+
					"other row", tc.drop)
		})
	}
}

// The plain negatives agy's gate already carries (registry_test.go pins agy false on its idle,
// confirm and accepted-gate panes) and gemini's did not. Cheap, and the direction this PR made
// riskier: it replaced one distinctive sentence with two short phrases.
func TestGeminiTrustGateIsDownOnEveryNonGatePane(t *testing.T) {
	for _, tc := range []struct{ name, pane string }{
		{"idle composer", geminiIdlePane},
		{"working with prose", geminiProsePane},
		{"permissions dialog", geminiPermissionsTrustPane},
		{"empty", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, up := gemini.GateUp(tc.pane)
			require.False(t, up, "no gate is up on %s", tc.name)
		})
	}
}
