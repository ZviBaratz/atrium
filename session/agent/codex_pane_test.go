package agent

import (
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

// codexDrivenWidths maps a rung's name to the pane captured at that width. Naming the rung
// rather than numbering it keeps a failure legible: the subtest name says which width broke.
var (
	codexTrustGateLadder = map[string]string{
		"width 120":                      codexTrustGatePane120,
		"width 60":                       codexTrustGatePane60,
		"width 40, headline wrapped":     codexTrustGatePane40,
		"width 28":                       codexTrustGatePane28,
		"width 24":                       codexTrustGatePane24,
		"width 20, GateUp itself misses": codexTrustGatePane20,
	}
	codexApprovalLadder = map[string]string{
		"width 120":                        codexApprovalPane120,
		"width 60":                         codexApprovalPane60,
		"width 40, decline option wrapped": codexApprovalPane40,
		"width 28":                         codexApprovalPane28,
		"width 24":                         codexApprovalPane24,
		"width 20, decline wrapped 5 ways": codexApprovalPane20,
	}
	codexComposerLadder = map[string]string{
		"width 120, one row":   codexTypedComposerPane120,
		"width 40, two rows":   codexTypedComposerPane40,
		"width 20, three rows": codexTypedComposerPane20,
	}
)

// A ladder that can lose a rung silently is not a ladder. Each map above is keyed by a
// human-readable width, so a deleted entry changes nothing a test can see — the loop simply
// runs fewer subtests and still passes. These counts are what make "verified at every driven
// width" a claim the suite can falsify rather than a sentence in a comment. Raising a count
// means driving codex again and adding the capture; lowering one means deleting evidence,
// and should be as loud as it is here.
func TestCodexLaddersKeepEveryDrivenRung(t *testing.T) {
	require.Len(t, codexTrustGateLadder, 6, "the trust gate was driven at 120/60/40/28/24/20")
	require.Len(t, codexApprovalLadder, 6, "the approval overlay was driven at 120/60/40/28/24/20")
	require.Len(t, codexComposerLadder, 3,
		"the composer was driven at 120/40/20 — the widths where the entry occupies one, two "+
			"and three rows, which is what the readback join has to survive")
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
	for name, pane := range codexComposerLadder {
		t.Run(name, func(t *testing.T) {
			text, ok := codex.InputBoxText(pane)
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

// The trust gate must never read as a composer. This is the guarantee
// SelectorSharesPromptChar buys, and it is not redundant with GateUp: at width 20 GateUp
// MISSES (see below), so on that rung the box check is the only thing between a queued
// prompt and a screen whose highlighted row is "› 1. Yes, continue".
func TestCodexTrustGateIsNotAComposer(t *testing.T) {
	for name, pane := range codexTrustGateLadder {
		t.Run(name, func(t *testing.T) {
			require.False(t, codex.InputBoxVisible(pane),
				"the gate's \"› 1. Yes, continue\" selector must not read as a live composer")
		})
	}
}

// The measurement behind the claim above, pinned so it cannot rot into an assumption.
// Codex wraps the gate's body instead of truncating it, so at width 20 the body alone spans
// enough rows to push the headline out of flattenChrome's WindowPrompt budget — GateUp's
// literal is intact on screen but outside the window it is matched in. Every wider rung
// still matches, which is what makes 20 the interesting one rather than a blanket failure.
func TestCodexTrustGateBoxCheckHoldsWhereGateUpMisses(t *testing.T) {
	for name, pane := range codexTrustGateLadder {
		if name == "width 20, GateUp itself misses" {
			continue
		}
		_, up := codex.GateUp(pane)
		require.True(t, up, "%s: the gate literal must still match", name)
	}

	_, up := codex.GateUp(codexTrustGatePane20)
	require.False(t, up,
		"at width 20 the wrapped body pushes the gate headline out of the WindowPrompt "+
			"flatten budget; if this ever starts passing, the budget or the wrap changed and "+
			"the comment on Adapter.SelectorSharesPromptChar needs re-measuring")
	require.False(t, codex.InputBoxVisible(codexTrustGatePane20),
		"...so on this rung the box check is the ONLY guard, and it must hold")
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
	for name, pane := range codexApprovalLadder {
		t.Run(name, func(t *testing.T) {
			m, ok := codex.DetectPrompt(pane)
			require.True(t, ok, "the approval overlay must be detected, or a queued prompt is typed into it")
			require.Equal(t, "approval", m.Name)
			require.True(t, m.NoAutoTap, "an unanchored approval must never be Enter-approved (#347)")
		})
	}
}

// The transcript echo, stated as a fact rather than left as a surprise. It is why
// Adapter.InputBoxVisible's doc says the box check cannot be made to tell a composer from a
// keystroke-consuming screen, and why SelectorSharesPromptChar is described as covering the
// gate rather than the overlay.
func TestCodexApprovalStillReadsAsABoxViaTheTranscriptEcho(t *testing.T) {
	require.True(t, codex.InputBoxVisible(codexApprovalPane120),
		"codex echoes the submitted message with \"›\", so the overlay frame still carries a "+
			"line that reads as a composer — DetectPrompt is what excludes it, not this")
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
