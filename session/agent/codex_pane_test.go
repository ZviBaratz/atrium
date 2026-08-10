package agent

import (
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Codex panes driven live against codex-cli 0.147.0 on 2026-08-09, in an isolated tmux
// (private TMUX_TMPDIR, socket addressed by path) on a scratch repo, and captured with
// `tmux capture-pane -p` at widths 120/60/40/28/24/20. Only trailing all-blank rows were
// removed; every remaining line is verbatim. The composer glyph was byte-verified with
// `cat -A` as M-bM-^@M-: = 0xE2 0x80 0xBA = "›" (U+203A).
//
// These exist because #510 was, exactly, a fixture that was never driven: codex's "› " has
// been written into this package's own composed fixtures (TestCodexBusyMarker) for as long
// as the adapter has existed, and nothing ever fed it to isInputBoxLine. A composed pane
// cannot falsify a predicate it was written to satisfy.
//
// Two structural facts decided the fix, and neither is guessable from codex's source:
//
//   - Codex WRAPS its dialog text rather than truncating it (the opposite of agy, #512),
//     so its gate and approval literals reconstruct under flattenChrome at every width.
//     What it truncates is only the trailing hint row and the footer, which nothing keys on.
//   - Codex echoes the user's own submitted message into the transcript with the SAME "›",
//     so a frame whose composer an overlay has replaced still carries a line that reads as
//     a composer. No glyph-level rule can tell those apart; DetectPrompt is what excludes
//     the overlay, which is why the approval matcher's width behaviour is pinned below.

var codexTrustGatePane120 = strings.Join([]string{
	"> You are in /tmp/cx510/fresh",
	"",
	"  Do you trust the contents of this directory? Working with untrusted contents comes with higher risk of prompt",
	"  injection. Trusting the directory allows project-local config, hooks, and exec policies to load.",
	"",
	"› 1. Yes, continue",
	"  2. No, quit",
	"",
	"  Press enter to continue",
}, "\n")

var codexTrustGatePane60 = strings.Join([]string{
	"> You are in /tmp/cx510/fresh",
	"",
	"  Do you trust the contents of this directory? Working with",
	"  untrusted contents comes with higher risk of prompt",
	"  injection. Trusting the directory allows project-local",
	"  config, hooks, and exec policies to load.",
	"",
	"› 1. Yes, continue",
	"  2. No, quit",
	"",
	"  Press enter to continue",
}, "\n")

var codexTrustGatePane40 = strings.Join([]string{
	"> You are in /tmp/cx510/fresh",
	"",
	"  Do you trust the contents of this",
	"  directory? Working with untrusted",
	"  contents comes with higher risk of",
	"  prompt injection. Trusting the",
	"  directory allows project-local config,",
	"  hooks, and exec policies to load.",
	"",
	"› 1. Yes, continue",
	"  2. No, quit",
	"",
	"  Press enter to continue",
}, "\n")

var codexTrustGatePane28 = strings.Join([]string{
	"> You are in /tmp/cx510/fres",
	"",
	"  Do you trust the contents",
	"  of this directory? Working",
	"  with untrusted contents",
	"  comes with higher risk of",
	"  prompt injection. Trusting",
	"  the directory allows",
	"  project-local config,",
	"  hooks, and exec policies",
	"  to load.",
	"",
	"› 1. Yes, continue",
	"  2. No, quit",
	"",
	"  Press enter to continue",
}, "\n")

var codexTrustGatePane24 = strings.Join([]string{
	"> You are in /tmp/cx510/",
	"",
	"  Do you trust the",
	"  contents of this",
	"  directory? Working",
	"  with untrusted",
	"  contents comes with",
	"  higher risk of prompt",
	"  injection. Trusting",
	"  the directory allows",
	"  project-local config,",
	"  hooks, and exec",
	"  policies to load.",
	"",
	"› 1. Yes, continue",
	"  2. No, quit",
	"",
	"  Press enter to continu",
}, "\n")

var codexTrustGatePane20 = strings.Join([]string{
	"> You are in /tmp/cx",
	"",
	"  Do you trust the",
	"  contents of this",
	"  directory? Working",
	"  with untrusted",
	"  contents comes",
	"  with higher risk",
	"  of prompt",
	"  injection.",
	"  Trusting the",
	"  directory allows",
	"  project-local",
	"  config, hooks, and",
	"  exec policies to",
	"  load.",
	"",
	"› 1. Yes, continue",
	"  2. No, quit",
	"",
	"  Press enter to con",
}, "\n")

var codexApprovalPane120 = strings.Join([]string{
	"╭─────────────────────────────────────────────╮",
	"│ >_ OpenAI Codex (v0.147.0)                  │",
	"│                                             │",
	"│ model:     gpt-5.6-terra   /model to change │",
	"│ directory: /tmp/cx510/repo                  │",
	"╰─────────────────────────────────────────────╯",
	"",
	"  Tip: New For a limited time, Codex is included in your plan for free – let’s build together.",
	"",
	"",
	"› Run this exact shell command and nothing else: rm -rf /tmp/cx510/repo/build",
	"",
	"",
	"• Running rm -rf /tmp/cx510/repo/build",
	"",
	"",
	"  Would you like to run the following command?",
	"",
	"  Environment: local",
	"",
	"  $ rm -rf /tmp/cx510/repo/build",
	"",
	"› 1. Yes, proceed (y)",
	"  2. Yes, and don't ask again for commands that start with `rm -rf /tmp/cx510/repo/build` (p)",
	"  3. No, and tell Codex what to do differently (esc)",
	"",
	"  Press enter to confirm or esc to cancel",
}, "\n")

var codexApprovalPane60 = strings.Join([]string{
	"╭─────────────────────────────────────────────╮",
	"│ >_ OpenAI Codex (v0.147.0)                  │",
	"│                                             │",
	"│ model:     gpt-5.6-terra   /model to change │",
	"│ directory: /tmp/cx510/repo                  │",
	"╰─────────────────────────────────────────────╯",
	"",
	"  Tip: New For a limited time, Codex is included in your",
	"  plan for free – let’s build together.",
	"",
	"",
	"› Run this exact shell command and nothing else: rm -rf /",
	"  tmp/cx510/repo/build",
	"",
	"",
	"• Running rm -rf /tmp/cx510/repo/build",
	"",
	"",
	"  Would you like to run the following command?",
	"",
	"  Environment: local",
	"",
	"  $ rm -rf /tmp/cx510/repo/build",
	"",
	"› 1. Yes, proceed (y)",
	"  2. Yes, and don't ask again for commands that start with",
	"     `rm -rf /tmp/cx510/repo/build` (p)",
	"  3. No, and tell Codex what to do differently (esc)",
	"",
	"  Press enter to confirm or esc to cancel",
}, "\n")

var codexApprovalPane40 = strings.Join([]string{
	"╭──────────────────────────────────────╮",
	"│ >_ OpenAI Codex (v0.147.0)           │",
	"│                                      │",
	"│ model:     gpt-5.6-terra   /model t… │",
	"│ directory: /tmp/cx510/repo           │",
	"╰──────────────────────────────────────╯",
	"",
	"  Tip: New For a limited time, Codex is",
	"  included in your plan for free – let’s",
	"  build together.",
	"",
	"",
	"› Run this exact shell command and",
	"  nothing else: rm -rf /tmp/cx510/repo/",
	"  build",
	"",
	"",
	"• Running rm -rf /tmp/cx510/repo/build",
	"",
	"",
	"  Would you like to run the following",
	"",
	"  Environment: local",
	"",
	"  $ rm -rf /tmp/cx510/repo/build",
	"",
	"› 1. Yes, proceed (y)",
	"  2. Yes, and don't ask again for",
	"     commands that start with `rm",
	"     -rf /tmp/cx510/repo/build` (p)",
	"  3. No, and tell Codex what to do",
	"     differently (esc)",
	"",
	"  Press enter to confirm or esc to cance",
}, "\n")

var codexApprovalPane28 = strings.Join([]string{
	"│ >_ OpenAI Codex (v0.147… │",
	"│                          │",
	"│ model:     gpt-5.6-terr… │",
	"│ directory: /tmp/…/repo   │",
	"╰──────────────────────────╯",
	"",
	"  Tip: New For a limited",
	"  time, Codex is included in",
	"  your plan for free – let’s",
	"  build together.",
	"",
	"",
	"› Run this exact shell",
	"  command and nothing else:",
	"  rm -rf /tmp/cx510/repo/",
	"  build",
	"",
	"",
	"• Running rm -rf /tmp/cx510/",
	"  │ repo/build",
	"",
	"",
	"  Would you like to run th",
	"",
	"  Environment: local",
	"",
	"  $ rm -rf",
	"  /tmp/cx510/repo/build",
	"",
	"› 1. Yes, proceed (y)",
	"  2. Yes, and don't ask",
	"     again for commands",
	"     that start with `rm",
	"     -rf /tmp/cx510/repo/",
	"     build` (p)",
	"  3. No, and tell Codex",
	"     what to do",
	"     differently (esc)",
	"",
	"  Press enter to confirm or",
}, "\n")

var codexApprovalPane24 = strings.Join([]string{
	"╰──────────────────────╯",
	"",
	"  Tip: New For a limited",
	"  time, Codex is",
	"  included in your plan",
	"  for free – let’s build",
	"  together.",
	"",
	"",
	"› Run this exact shell",
	"  command and nothing",
	"  else: rm -rf /tmp/",
	"  cx510/repo/build",
	"",
	"",
	"• Running rm -rf /tmp/",
	"  │ cx510/repo/",
	"  │ build",
	"",
	"",
	"  Would you like to ru",
	"",
	"  Environment: local",
	"",
	"  $ rm -rf",
	"  /tmp/cx510/repo/buil",
	"  d",
	"",
	"› 1. Yes, proceed (y)",
	"  2. Yes, and don't",
	"     ask again for",
	"     commands that",
	"     start with `rm",
	"     -rf /tmp/cx510/",
	"     repo/build` (p)",
	"  3. No, and tell",
	"     Codex what to do",
	"     differently (esc)",
	"",
	"  Press enter to confirm",
}, "\n")

var codexApprovalPane20 = strings.Join([]string{
	"",
	"",
	"› Run this exact",
	"  shell command and",
	"  nothing else: rm",
	"  -rf /tmp/cx510/",
	"  repo/build",
	"",
	"",
	"• Running rm -rf /",
	"  │ tmp/cx510/",
	"  │ repo/build",
	"",
	"",
	"  Would you like t",
	"",
	"  Environment:",
	"  local",
	"",
	"  $ rm -rf",
	"  /tmp/cx510/repo/",
	"  build",
	"",
	"› 1. Yes, proceed",
	"     (y)",
	"  2. Yes, and",
	"     don't ask",
	"     again for",
	"     commands that",
	"     start with",
	"     `rm -rf /tmp/",
	"     cx510/repo/",
	"     build` (p)",
	"  3. No, and tell",
	"     Codex what to",
	"     do",
	"     differently",
	"     (esc)",
	"",
	"  Press enter to con",
}, "\n")

var codexTypedComposerPane120 = strings.Join([]string{
	"╭─────────────────────────────────────────────╮",
	"│ >_ OpenAI Codex (v0.147.0)                  │",
	"│                                             │",
	"│ model:     gpt-5.6-terra   /model to change │",
	"│ directory: /tmp/cx510/repo                  │",
	"╰─────────────────────────────────────────────╯",
	"",
	"  Tip: New For a limited time, Codex is included in your plan for free – let’s build together.",
	"",
	"",
	"› Run this exact shell command and nothing else: rm -rf /tmp/cx510/repo/build",
	"",
	"",
	"✔ You approved codex to run rm -rf /tmp/cx510/repo/build this time",
	"",
	"• Ran rm -rf /tmp/cx510/repo/build",
	"  └ (no output)",
	"",
	"─ Worked for 1m 39s ────────────────────────────────────────────────────────────────────────────────────────────────────",
	"",
	"",
	"› refactor the parser and add a regression test",
	"",
	"  gpt-5.6-terra default · /tmp/cx510/repo",
}, "\n")

var codexTypedComposerPane40 = strings.Join([]string{
	"╭──────────────────────────────────────╮",
	"│ >_ OpenAI Codex (v0.147.0)           │",
	"│                                      │",
	"│ model:     gpt-5.6-terra   /model t… │",
	"│ directory: /tmp/cx510/repo           │",
	"╰──────────────────────────────────────╯",
	"",
	"  Tip: New For a limited time, Codex is",
	"  included in your plan for free – let’s",
	"  build together.",
	"",
	"",
	"› Run this exact shell command and",
	"  nothing else: rm -rf /tmp/cx510/repo/",
	"  build",
	"",
	"",
	"✔ You approved codex to run rm -rf /tmp/",
	"  cx510/repo/build this time",
	"",
	"• Ran rm -rf /tmp/cx510/repo/build",
	"  └ (no output)",
	"",
	"─ Worked for 1m 39s ────────────────────",
	"",
	"",
	"› refactor the parser and add a",
	"  regression test",
	"",
	"  gpt-5.6-terra default · /tmp/cx510/re…",
}, "\n")

var codexTypedComposerPane20 = strings.Join([]string{
	"╭──────────────────╮",
	"│ >_ OpenAI Codex… │",
	"│                  │",
	"│ model:     gpt-… │",
	"│ directory: …repo │",
	"╰──────────────────╯",
	"",
	"  Tip: New For a",
	"  limited time,",
	"  Codex is included",
	"  in your plan for",
	"  free – let’s build",
	"  together.",
	"",
	"",
	"› Run this exact",
	"  shell command and",
	"  nothing else: rm",
	"  -rf /tmp/cx510/",
	"  repo/build",
	"",
	"",
	"✔ You approved codex",
	"  to run rm -rf /",
	"  tmp/cx510/repo/",
	"  build this time",
	"",
	"• Ran rm -rf /tmp/",
	"  │ cx510/repo/",
	"  │ build",
	"  └ (no output)",
	"",
	"─ Worked for 1m 39s",
	"",
	"",
	"› refactor the",
	"  parser and add a",
	"  regression test",
	"",
	"  gpt-5.6-terra def…",
}, "\n")

var codexGhostComposerPane28 = strings.Join([]string{
	"",
	"✔ You approved codex to run",
	"  rm -rf /tmp/cx510/repo/",
	"  build this time",
	"",
	"• Ran rm -rf /tmp/cx510/",
	"  │ repo/build",
	"  └ (no output)",
	"",
	"─ Worked for 1m 39s ────────",
	"",
	"",
	"› Write a haiku about",
	"  parsers, then explain it",
	"  in three paragraphs.",
	"",
	"",
	"• Tokens cross the stream",
	"  Rules map each shape to",
	"  meaning",
	"  Syntax blooms from noise",
	"",
	"  The first line evokes raw",
	"  input arriving as a",
	"  sequence of tokens.",
	"",
	"  The second describes",
	"  grammar rules assigning",
	"  structure and",
	"  interpretation.",
	"",
	"  The final line captures",
	"  parsing’s payoff: turning",
	"  unstructured text into",
	"  meaningful syntax.",
	"",
	"",
	"› Summarize recent commits",
	"",
	"  gpt-5.6-terra default · /…",
}, "\n")

// A queued prompt that is a numbered list, typed into codex's live composer. Driven against
// codex 0.147.0 on 2026-08-09 at widths 120 and 20, in an isolated tmux addressed by socket
// path. These exist to pin the shape collision that decided the design: put the multi-line
// capture beside codexTrustGatePane20 and the composer's rows
//
//	› 1. refactor the parser
//	  2. add a regression test
//
// are the trust gate's
//
//	› 1. Yes, continue
//	  2. No, quit
//
// with different words in them. Codex does not collapse a bracketed paste into a chip the way
// claude does (captured, not assumed — see codexNumberedListComposerPane120), so the rows
// reach the pane verbatim. No predicate over that line's shape can reject the menu and keep
// the prompt, which is why the box check is not a guard here and GateWindow is.
var codexNumberedComposerPane120 = strings.Join([]string{
	"╭─────────────────────────────────────────────╮",
	"│ >_ OpenAI Codex (v0.147.0)                  │",
	"│                                             │",
	"│ model:     gpt-5.6-terra   /model to change │",
	"│ directory: /tmp/cx510b/repo                 │",
	"╰─────────────────────────────────────────────╯",
	"",
	"  Tip: New For a limited time, Codex is included in your plan for free – let’s build together.",
	"",
	"",
	"› 1. refactor the parser",
	"",
	"  gpt-5.6-terra default · /tmp/cx510b/repo",
}, "\n")

var codexNumberedComposerPane20 = strings.Join([]string{
	"╭──────────────────╮",
	"│ >_ OpenAI Codex… │",
	"│                  │",
	"│ model:     gpt-… │",
	"│ directory: …repo │",
	"╰──────────────────╯",
	"",
	"  Tip: New For a",
	"  limited time,",
	"  Codex is included",
	"  in your plan for",
	"  free – let’s build",
	"  together.",
	"",
	"",
	"› 1. refactor the",
	"  parser",
	"",
	"  gpt-5.6-terra def…",
}, "\n")

var codexNumberedListComposerPane120 = strings.Join([]string{
	"╭─────────────────────────────────────────────╮",
	"│ >_ OpenAI Codex (v0.147.0)                  │",
	"│                                             │",
	"│ model:     gpt-5.6-terra   /model to change │",
	"│ directory: /tmp/cx510b/repo                 │",
	"╰─────────────────────────────────────────────╯",
	"",
	"  Tip: New For a limited time, Codex is included in your plan for free – let’s build together.",
	"",
	"",
	"› 1. refactor the parser",
	"  2. add a regression test",
	"  3. run just ci",
	"",
	"  gpt-5.6-terra default · /tmp/cx510b/repo",
}, "\n")

var codexNumberedListComposerPane20 = strings.Join([]string{
	"╭──────────────────╮",
	"│ >_ OpenAI Codex… │",
	"│                  │",
	"│ model:     gpt-… │",
	"│ directory: …repo │",
	"╰──────────────────╯",
	"",
	"  Tip: New For a",
	"  limited time,",
	"  Codex is included",
	"  in your plan for",
	"  free – let’s build",
	"  together.",
	"",
	"",
	"› 1. refactor the",
	"  parser",
	"  2. add a",
	"  regression test",
	"  3. run just ci",
	"",
	"  gpt-5.6-terra def…",
}, "\n")

// The driven ladders. Each rung carries the width it was captured at as a VALUE rather than
// only in its identifier, so the invariant in pane_width_test.go can reason about it — that
// conversion is #648, and the trust gate and approval ladders feed paneCoverage directly
// rather than being listed a second time there.
var (
	codexTrustGateLadder = []paneCapture{
		{name: "codexTrustGatePane120", width: 120, pane: codexTrustGatePane120},
		{name: "codexTrustGatePane60", width: 60, pane: codexTrustGatePane60},
		{name: "codexTrustGatePane40", width: 40, note: "headline wrapped", pane: codexTrustGatePane40},
		{name: "codexTrustGatePane28", width: 28, pane: codexTrustGatePane28},
		{name: "codexTrustGatePane24", width: 24, pane: codexTrustGatePane24},
		{name: "codexTrustGatePane20", width: 20, note: "18 non-empty lines", pane: codexTrustGatePane20},
	}
	codexApprovalLadder = []paneCapture{
		{name: "codexApprovalPane120", width: 120, pane: codexApprovalPane120},
		{name: "codexApprovalPane60", width: 60, pane: codexApprovalPane60},
		{name: "codexApprovalPane40", width: 40, note: "decline option wrapped", pane: codexApprovalPane40},
		{name: "codexApprovalPane28", width: 28, pane: codexApprovalPane28},
		{name: "codexApprovalPane24", width: 24, pane: codexApprovalPane24},
		{name: "codexApprovalPane20", width: 20, note: "decline wrapped 5 ways", pane: codexApprovalPane20},
	}
	// The composer ladders are NEGATIVE for prompt detection — these panes must read as
	// composers, not as prompts — so they carry the width datum but are deliberately not
	// entries in paneCoverage, which is positive-only.
	codexComposerLadder = []paneCapture{
		{name: "codexTypedComposerPane120", width: 120, note: "one row", pane: codexTypedComposerPane120},
		{name: "codexTypedComposerPane40", width: 40, note: "two rows", pane: codexTypedComposerPane40},
		{name: "codexTypedComposerPane20", width: 20, note: "three rows", pane: codexTypedComposerPane20},
	}
	// Composers holding a numbered-list prompt — the shape a selector-row rule would
	// have rejected. Kept as its own ladder so its rungs are guarded separately.
	codexNumberedComposerLadder = []paneCapture{
		{name: "codexNumberedComposerPane120", width: 120, note: "single line", pane: codexNumberedComposerPane120},
		{name: "codexNumberedComposerPane20", width: 20, note: "single line", pane: codexNumberedComposerPane20},
		{name: "codexNumberedListComposerPane120", width: 120, note: "three items", pane: codexNumberedListComposerPane120},
		{name: "codexNumberedListComposerPane20", width: 20, note: "three items", pane: codexNumberedListComposerPane20},
	}
)

// ladderRungs is the rung list as data, narrowest first, for the guard below.
//
// It yields each rung's full label — width, identifier and note — not just its width. A width
// alone is not a rung's identity: codexNumberedComposerLadder holds two rungs at 20 and two at
// 120, separated only by which prompt was typed, so a width list reports "20 20 120 120" no
// matter which four panes are present. The map this replaced could not be defeated that way
// (duplicate keys in a map literal are a compile error), and dropping to widths quietly gave
// that up — a ladder able to lose a rung in silence is the exact thing the guard forbids.
func ladderRungs(ladder []paneCapture) []string {
	sorted := append([]paneCapture(nil), ladder...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].width != sorted[j].width {
			return sorted[i].width < sorted[j].width
		}
		return sorted[i].name < sorted[j].name
	})
	rungs := make([]string, 0, len(sorted))
	for _, c := range sorted {
		rungs = append(rungs, c.label())
	}
	return rungs
}

// A ladder that can lose a rung silently is not a ladder: a deleted entry changes nothing a
// test can see — the loop simply runs fewer subtests and still passes. This is what makes
// "verified at every driven width" a claim the suite can falsify rather than a sentence in a
// comment. Adding a rung means driving codex again; removing one means deleting evidence, and
// should be as loud as it is here.
//
// Asserted as the exact WIDTH LIST rather than the count it used to be, now that the width is
// a value: a count says six rungs were expected and five remain, while this says which one
// went missing. Not subsumed by pane_width_test.go's floor pin, which sees only the NARROWEST
// rung of the two ladders it consumes — deleting the 60 rung, or any rung of either composer
// ladder, is invisible there and caught here.
func TestCodexLaddersKeepEveryDrivenRung(t *testing.T) {
	require.Equal(t, []string{
		"width 20 codexTrustGatePane20 (18 non-empty lines)",
		"width 24 codexTrustGatePane24",
		"width 28 codexTrustGatePane28",
		"width 40 codexTrustGatePane40 (headline wrapped)",
		"width 60 codexTrustGatePane60",
		"width 120 codexTrustGatePane120",
	}, ladderRungs(codexTrustGateLadder), "the trust gate was driven at 120/60/40/28/24/20")

	require.Equal(t, []string{
		"width 20 codexApprovalPane20 (decline wrapped 5 ways)",
		"width 24 codexApprovalPane24",
		"width 28 codexApprovalPane28",
		"width 40 codexApprovalPane40 (decline option wrapped)",
		"width 60 codexApprovalPane60",
		"width 120 codexApprovalPane120",
	}, ladderRungs(codexApprovalLadder), "the approval overlay was driven at 120/60/40/28/24/20")

	require.Equal(t, []string{
		"width 20 codexTypedComposerPane20 (three rows)",
		"width 40 codexTypedComposerPane40 (two rows)",
		"width 120 codexTypedComposerPane120 (one row)",
	}, ladderRungs(codexComposerLadder),
		"the composer was driven at 120/40/20 — the widths where the entry occupies one, two "+
			"and three rows, which is what the readback join has to survive")

	// The ladder that a width list could not guard: two rungs at 20 and two at 120,
	// distinguished only by which prompt was typed.
	require.Equal(t, []string{
		"width 20 codexNumberedComposerPane20 (single line)",
		"width 20 codexNumberedListComposerPane20 (three items)",
		"width 120 codexNumberedComposerPane120 (single line)",
		"width 120 codexNumberedListComposerPane120 (three items)",
	}, ladderRungs(codexNumberedComposerLadder),
		"a numbered-list prompt was driven single-line and three-item, at 120 and 20")
}

// The defect, at the adapter boundary. Before #510 every one of these read false — codex's
// "›" was not an accepted prompt glyph — so InputBoxVisible, AwaitingInput and prompt
// delivery were dead for every codex session at every width.
//
// The readback is asserted with Equal, not Contains, deliberately: boxHoldsPrompt
// (session/prompt.go) uses a substring check, so a stripBoxInterior that accepted "›" but
// failed to STRIP it would still satisfy that caller while returning "› refactor …". Only
// equality can see the leftover glyph. It also proves the wrapped-row join, which is why the
// ladder is here rather than a single width: the same entry occupies one row at 120, two at
// 40 and three at 20.
func TestCodexComposerVisibleAndReadBack(t *testing.T) {
	for _, c := range codexComposerLadder {
		t.Run(c.label(), func(t *testing.T) {
			text, ok := codex.InputBoxText(c.pane)
			require.True(t, ok, "a live codex composer must read as an input box (#510)")
			require.Equal(t, "refactor the parser and add a regression test", text,
				"the readback must be the typed text with the prompt glyph stripped")
		})
	}
}

// An empty composer showing codex's ghost suggestion reads the hint back as its text, the
// same way claude's `Try "…"` does — documented on inputBoxText, and the reason
// boxHoldsPrompt compares by substring instead of trusting the readback verbatim.
func TestCodexGhostSuggestionReadsBackAsText(t *testing.T) {
	text, ok := codex.InputBoxText(codexGhostComposerPane28)
	require.True(t, ok)
	require.Equal(t, "Summarize recent commits", text)
}

// The trust gate is excluded by GateUp at EVERY driven width, including the floor. That is
// the whole guarantee: GateUp and DetectPrompt are the guards on codex, and the box check is
// not one of them (see below), so a rung where GateUp misses is a rung where a queued prompt
// lands on the trust screen — and codex's menus take number accelerators, so the first
// character of a prompt beginning "1." would answer the dialog.
func TestCodexTrustGateDetectedAtEveryDrivenWidth(t *testing.T) {
	for _, c := range codexTrustGateLadder {
		t.Run(c.label(), func(t *testing.T) {
			_, up := codex.GateUp(c.pane)
			require.True(t, up, "the trust gate must be detected, or a queued prompt is typed into it")
		})
	}
}

// The measurement that makes GateWindow load-bearing rather than decorative, pinned so it
// cannot rot into an assumption. Codex WRAPS the gate body instead of truncating it, so the
// dialog spends more lines as the pane narrows while its text stays intact — 18 non-empty
// lines at width 20, past the default 15-line budget, which drops the headline the gate is
// keyed on. The literal is fully on screen; the window is what misses it.
//
// Asserted against a bare copy of the adapter rather than a mutated global: this must fail
// when GateWindow is removed, and a test that patched codex.GateWindow in place would race
// every other test in the package and restore the very field it is measuring.
func TestCodexTrustGateHeadlineFallsOutsideTheDefaultWindowAtWidth20(t *testing.T) {
	require.Equal(t, 24, codex.gateWindow(), "the widened budget is what the rest of this test measures against")

	def := *codex
	def.GateWindow = 0
	require.Equal(t, WindowPrompt, def.gateWindow())

	_, up := def.GateUp(codexTrustGatePane20)
	require.False(t, up,
		"with the default budget the width-20 gate must MISS — if this starts passing, codex "+
			"stopped wrapping or the budget changed, and GateWindow's comment needs re-measuring")

	// Keyed on the width rather than on a rung's prose label, now that the width is a
	// value: the exclusion is a statement about 20 columns, and matching a string meant a
	// reworded label would silently start asserting the opposite of this test's point.
	for _, c := range codexTrustGateLadder {
		if c.width == 20 {
			continue
		}
		_, up := def.GateUp(c.pane)
		require.True(t, up, "%s: only the floor rung is out of reach of the default budget", c.label())
	}
}

// Stated as a fact rather than left as a surprise: the gate's selector DOES read as a
// composer, and deliberately so. Codex draws "› 1. Yes, continue" with the composer glyph,
// and the sibling test below shows a real queued prompt drawing the identical shape — so a
// predicate that rejected one would reject the other. AwaitingInput is where the exclusion
// lives, which session/tmux's TestAwaitingInputCodex pins end to end.
func TestCodexTrustGateReadsAsABoxSoGateUpIsTheGuard(t *testing.T) {
	for _, c := range codexTrustGateLadder {
		t.Run(c.label(), func(t *testing.T) {
			require.True(t, codex.InputBoxVisible(c.pane),
				"the gate's selector reads as a box; GateUp, not the box check, is what excludes it")
		})
	}
}

// The regression this design exists to avoid. A queued prompt that is a numbered list must
// read as a composer and read BACK verbatim — otherwise InputBoxVisible goes false the moment
// the prompt is typed (so it is never submitted and, because promptDeliveryReady requires
// awaitingInput, never retried or expired), or the readback misses and boxHoldsPrompt returns
// false on every tick, re-typing the prompt into the live composer forever. Both were
// reachable with a numbered-selector rule in place; neither is with GateUp doing the work.
func TestCodexNumberedPromptReadsAsAComposerAndReadsBackWhole(t *testing.T) {
	// Keyed on the rung's note rather than its full label: the expected readback depends on
	// which prompt was typed, not on the width it was captured at — and that independence is
	// what the ladder exists to demonstrate.
	want := map[string]string{
		"single line": "1. refactor the parser",
		"three items": "1. refactor the parser 2. add a regression test 3. run just ci",
	}
	for _, c := range codexNumberedComposerLadder {
		t.Run(c.label(), func(t *testing.T) {
			require.Contains(t, want, c.note, "every rung must name which prompt was typed")
			text, ok := codex.InputBoxText(c.pane)
			require.True(t, ok, "a numbered-list prompt is a prompt, not a menu")
			require.Equal(t, want[c.note], text,
				"the whole prompt must read back, or boxHoldsPrompt never confirms and it is re-typed every tick")
		})
	}
}

// The approval overlay is excluded by DetectPrompt, not by the box check — codex echoes the
// user's submitted message into the transcript with the same "›", so on the wider rungs a
// line that reads as a composer is still on screen (pinned below). Codex wraps the decline
// option rather than truncating it, so flattenChrome reconstructs it at every driven width.
//
// This is the load-bearing assertion for #347's residual: if any rung ever stops matching,
// AwaitingInput goes true on a live approval overlay, and a queued prompt is typed into it.
// That is not merely a misplaced keystroke — driving codex 0.147.0, typing the string
// "hey there" at this overlay approved the command outright, because "y" is the accelerator
// for "1. Yes, proceed (y)" and it confirms immediately, with no Enter.
func TestCodexApprovalDetectedAtEveryDrivenWidth(t *testing.T) {
	for _, c := range codexApprovalLadder {
		t.Run(c.label(), func(t *testing.T) {
			m, ok := codex.DetectPrompt(c.pane)
			require.True(t, ok, "the approval overlay must be detected, or a queued prompt is typed into it")
			require.Equal(t, "approval", m.Name)
			require.True(t, m.NoAutoTap, "an unanchored approval must never be Enter-approved (#347)")
		})
	}
}

// The approval overlay reads as a box, stated as a fact rather than left as a surprise, and
// it does so twice over: its own "› 1. Yes, proceed (y)" selector is composer-shaped, and
// codex additionally echoes the submitted message into the transcript with the same glyph, so
// even a frame whose selector were filtered would still carry one. That redundancy is why
// Adapter.InputBoxVisible's doc says the box check cannot be made to tell a composer from a
// keystroke-consuming screen — DetectPrompt is the guard here, and it is the only one.
//
// The readback on such a frame is therefore option text, not the user's input. Harmless
// because it is unreachable: SendPrompt reads the box only after AwaitingInput, which
// DetectPrompt has already turned false. inputBoxText's doc says the bottom-most anchor is a
// heuristic and not a proof of liveness for exactly this reason.
func TestCodexApprovalReadsAsABoxAndOnlyDetectPromptExcludesIt(t *testing.T) {
	require.True(t, codex.InputBoxVisible(codexApprovalPane120),
		"the overlay frame carries composer-shaped lines — DetectPrompt is what excludes it, not this")

	text, ok := codex.InputBoxText(codexApprovalPane120)
	require.True(t, ok)
	require.Contains(t, text, "Yes, proceed",
		"the readback on an overlay frame is the option text; unreachable, since AwaitingInput "+
			"is already false here — but it must not be mistaken for the user's composer input")
}

// Replacing the default set rather than extending it is load-bearing, and this is the
// evidence. Codex's own startup banner ("│ >_ OpenAI Codex (v0.147.0)") and its header
// ("> You are in <dir>") both open with ">". Under the default set the banner won: on the
// 120-column composer pane, InputBoxText returned the BANNER's text, not the composer's —
// so the pre-#510 behaviour was not merely "no box found", it was a confident wrong
// readback that boxHoldsPrompt would have compared a queued prompt against.
func TestCodexBannerAndHeaderAreNotComposers(t *testing.T) {
	for _, line := range []string{
		"│ >_ OpenAI Codex (v0.147.0)                  │",
		"> You are in /tmp/cx510/fresh",
	} {
		require.False(t, isInputBoxLine(line, codex.inputBoxPrompts()),
			"%q must not read as a codex composer", line)
		require.True(t, isInputBoxLine(line, defaultPrompts),
			"...but it does under the default set, which is why codex REPLACES it")
	}
}
