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
// stripped): 80→33, 40→38, 24→37, 20→38, every one inside the 40-row pane, the widest rung
// most comfortably of all. TestGeminiCapturesAllFitTheDrivenPaneHeight recomputes those
// counts rather than trusting this sentence, which had 24 wrong (35) until #715 round 3
// counted them. So the divergence has some other cause, and a rule of the form
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

// The counts the header cites, recomputed. The header's argument is that overflow cannot be why
// a resized rung diverges from a native one, because no capture fills its pane — and an
// argument resting on four numbers in a comment is exactly what #648/#665 says to put somewhere
// a test can read. It is not idle: the 24 rung was cited as 35 rows until this was written.
func TestGeminiCapturesAllFitTheDrivenPaneHeight(t *testing.T) {
	for _, c := range geminiAllCaptures() {
		t.Run(c.label(), func(t *testing.T) {
			rows := len(strings.Split(strings.TrimPrefix(c.pane, "\n"), "\n"))
			require.LessOrEqual(t, rows, geminiDrivenPaneHeight,
				"%s is %d rows in a %d-row pane; if a capture ever overflows, the header's "+
					"\"nothing overflows\" reasoning about the resize divergence is void",
				c.name, rows, geminiDrivenPaneHeight)
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

	// Every budget from the default up to more lines than any of these panes has, over every
	// rung DERIVED from the ladder rather than rebuilt here. An earlier draft hand-listed the
	// 40 and 24 rungs without their notes, so the same capture printed one subtest name here
	// and a different one in the ladder test — and a rung driven later would not have been
	// swept at all, because a copy cannot grow.
	for _, window := range []int{WindowPrompt, 20, 24, 30, 40, 60, 200} {
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
// > verified (internal/doctor/compare.go). One literal plus a structural anchor covers both
// dialog shapes and survives a reword of the row it does not read.
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
