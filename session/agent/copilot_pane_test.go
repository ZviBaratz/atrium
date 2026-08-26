package agent

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Driven panes from GitHub Copilot CLI 1.0.80 (npm @github/copilot), Linux, captured
// 2026-08-26 by scripts/drive-agent.sh in an isolated COPILOT_HOME with the work token
// injected via ATR_CAP_ENV. Widths 120/60/40/34/28/26/24/20; the pane height is 40 at every
// rung, which matters for the gate (see the height note on copilotTrustgateLadder).
//
// Both of this adapter's dialogs are CLOSED round boxes whose bottom border is the last
// non-empty line at every rung, with "│"-walled interior rows — gemini's shape, not codex's
// borderless overlay. That is what lets both matchers anchor on bottomBoxBlock and read their
// literals through flattenBottomBox instead of a stripped whole-pane scan.
//
// WHAT A gemini-SHAPED MATCHER WOULD GET WRONG HERE, because the resemblance is close enough
// to invite copying: geminiTrustGateVisible vetoes a block containing an isInputBoxLine, on
// the reasoning that a composer is not a dialog. Copilot's selector IS the composer glyph
// "❯", and it sits on the dialog's highlighted option row, so that veto would return false on
// every rung of both ladders below. TestCopilotDialogsAreAlsoComposersToTheBoxPredicate holds
// the collision as a fact rather than leaving it as a trap.

const copilotTrustgateW20Pane = `  Current  →

│ trust            │
│ ──────────────── │
│ ╭──────────────╮ │
│ │ /tmp/atr-    │ │
│ │ cap/copilot/ │ │
│ │ repo         │ │
│ ╰──────────────╯ │
│                  │
│ Copilot can read │
│  files in this   │
│ folder and, with │
│  your            │
│ permission, edit │
│  them or run     │
│ code and shell   │
│ commands. It     │
│ will remember    │
│ your permissions │
│  for the rest of │
│  this session.   │
│                  │
│ Do you trust the │
│  files in this   │
│ folder?          │
│                  │
│ ❯ 1. Yes         │
│  2. Yes, and     │
│  remember this   │
│  folder for      │
│  future sessions │
│                  │
│   3. No (Esc)    │
│                  │
│ ↑/↓ to navigate  │
│ · enter to       │
│ select · esc to  │
│ cancel           │
╰──────────────────╯`

const copilotTrustgateW24Pane = `  Current   Sessions  →

   Only built-in       ┃
   servers are         ┃
   available.          ┃
                       ┃
╭──────────────────────╮
│ Confirm folder trust │
│ ──────────────────── │
│ ╭──────────────────╮ │
│ │ /tmp/atr-cap/    │ │
│ │ copilot/repo     │ │
│ ╰──────────────────╯ │
│                      │
│ Copilot can read     │
│ files in this folder │
│  and, with your      │
│ permission, edit     │
│ them or run code and │
│  shell commands. It  │
│ will remember your   │
│ permissions for the  │
│ rest of this         │
│ session.             │
│                      │
│ Do you trust the     │
│ files in this        │
│ folder?              │
│                      │
│ ❯ 1. Yes             │
│  2. Yes, and         │
│  remember this       │
│  folder for future   │
│  sessions            │
│   3. No (Esc)        │
│                      │
│ ↑/↓ to navigate ·    │
│ enter to select ·    │
│ esc to cancel        │
╰──────────────────────╯`

const copilotTrustgateW26Pane = `  Current   Sessions  →

 ! Third-party MCP       ┃
   servers are disabled  ┃
   by your               ┃
   organization's        ┃
   Copilot policy. Only  ┃
   built-in servers are  ┃
   available.            ┃
                         ┃
╭────────────────────────╮
│ Confirm folder trust   │
│ ────────────────────── │
│ ╭────────────────────╮ │
│ │ /tmp/atr-cap/      │ │
│ │ copilot/repo       │ │
│ ╰────────────────────╯ │
│                        │
│ Copilot can read files │
│  in this folder and,   │
│ with your permission,  │
│ edit them or run code  │
│ and shell commands. It │
│  will remember your    │
│ permissions for the    │
│ rest of this session.  │
│                        │
│ Do you trust the files │
│  in this folder?       │
│                        │
│ ❯ 1. Yes               │
│  2. Yes, and remember  │
│  this folder for       │
│  future sessions       │
│   3. No (Esc)          │
│                        │
│ ↑/↓ to navigate ·      │
│ enter to select · esc  │
│ to cancel              │
╰────────────────────────╯`

const copilotTrustgateW28Pane = `  Current   Sessions  →

     atures/ai/github-app  ┃
                           ┃
 ! Third-party MCP servers ┃
   are disabled by your    ┃
   organization's Copilot  ┃
   policy. Only built-in   ┃
   servers are available.  ┃
                           ┃
╭──────────────────────────╮
│ Confirm folder trust     │
│ ──────────────────────── │
│ ╭──────────────────────╮ │
│ │ /tmp/atr-cap/        │ │
│ │ copilot/repo         │ │
│ ╰──────────────────────╯ │
│                          │
│ Copilot can read files   │
│ in this folder and, with │
│  your permission, edit   │
│ them or run code and     │
│ shell commands. It will  │
│ remember your            │
│ permissions for the rest │
│  of this session.        │
│                          │
│ Do you trust the files   │
│ in this folder?          │
│                          │
│ ❯ 1. Yes                 │
│  2. Yes, and remember    │
│  this folder for future  │
│  sessions                │
│   3. No (Esc)            │
│                          │
│ ↑/↓ to navigate · enter  │
│ to select · esc to       │
│ cancel                   │
╰──────────────────────────╯`

const copilotTrustgateW34Pane = `  Current   Sessions   Issues  →

   └ Prefer a visual workspace?  ┃
     Try out the GitHub Copilot  ┃
     desktop app                 ┃
                                 ┃
    https://github.com/features  ┃
     /ai/github-app              ┃
                                 ┃
 ! Third-party MCP servers are   ┃
   disabled by your              ┃
   organization's Copilot        ┃
   policy. Only built-in servers ┃
   are available.                ┃
                                 ┃
╭────────────────────────────────╮
│ Confirm folder trust           │
│ ────────────────────────────── │
│ ╭────────────────────────────╮ │
│ │ /tmp/atr-cap/copilot/repo  │ │
│ ╰────────────────────────────╯ │
│                                │
│ Copilot can read files in this │
│  folder and, with your         │
│ permission, edit them or run   │
│ code and shell commands. It    │
│ will remember your permissions │
│  for the rest of this session. │
│                                │
│ Do you trust the files in this │
│  folder?                       │
│                                │
│ ❯ 1. Yes                       │
│   2. Yes, and remember this    │
│   folder for future sessions   │
│   3. No (Esc)                  │
│                                │
│ ↑/↓ to navigate · enter to     │
│ select · esc to cancel         │
╰────────────────────────────────╯`

const copilotTrustgateW40Pane = `  Current   Sessions   Issues  →

 ● Tip: /app                           ┃
   └ Prefer a visual workspace? Try    ┃
   out                                 ┃
     the GitHub Copilot desktop app    ┃
                                       ┃
    https://github.com/features/ai/gi  ┃
     thub-app                          ┃
                                       ┃
 ! Third-party MCP servers are         ┃
   disabled by your organization's     ┃
   Copilot policy. Only built-in       ┃
   servers are available.              ┃
                                       ┃
╭──────────────────────────────────────╮
│ Confirm folder trust                 │
│ ──────────────────────────────────── │
│ ╭──────────────────────────────────╮ │
│ │ /tmp/atr-cap/copilot/repo        │ │
│ ╰──────────────────────────────────╯ │
│                                      │
│ Copilot can read files in this       │
│ folder and, with your permission,    │
│ edit them or run code and shell      │
│ commands. It will remember your      │
│ permissions for the rest of this     │
│ session.                             │
│                                      │
│ Do you trust the files in this       │
│ folder?                              │
│                                      │
│ ❯ 1. Yes                             │
│  2. Yes, and remember this folder    │
│  for future sessions                 │
│   3. No (Esc)                        │
│                                      │
│ ↑/↓ to navigate · enter to select ·  │
│ esc to cancel                        │
╰──────────────────────────────────────╯`

const copilotTrustgateW60Pane = `  Current   Sessions   Issues   Pull requests   Gists

  ╭─╮╭─╮
  ╰─╯╰─╯  Copilot v1.0.80 uses AI.
  █ ▘▝ █  Check for mistakes.
   ▔▔▔▔

 ● No copilot-instructions.md found. Run /init to
   generate.

 ● Tip: /app
   └ Prefer a visual workspace? Try out the GitHub Copilot
     desktop app
      https://github.com/features/ai/github-app 

 ! Third-party MCP servers are disabled by your
   organization's Copilot policy. Only built-in servers
   are available.


╭──────────────────────────────────────────────────────────╮
│ Confirm folder trust                                     │
│ ──────────────────────────────────────────────────────── │
│ ╭──────────────────────────────────────────────────────╮ │
│ │ /tmp/atr-cap/copilot/repo                            │ │
│ ╰──────────────────────────────────────────────────────╯ │
│                                                          │
│ Copilot can read files in this folder and, with your     │
│ permission, edit them or run code and shell commands. It │
│  will remember your permissions for the rest of this     │
│ session.                                                 │
│                                                          │
│ Do you trust the files in this folder?                   │
│                                                          │
│ ❯ 1. Yes                                                 │
│   2. Yes, and remember this folder for future sessions   │
│   3. No (Esc)                                            │
│                                                          │
│ ↑/↓ to navigate · enter to select · esc to cancel        │
╰──────────────────────────────────────────────────────────╯`

const copilotTrustgateW120Pane = `  Current   Sessions   Issues   Pull requests   Gists

  ╭─╮╭─╮
  ╰─╯╰─╯  Copilot v1.0.80 uses AI.
  █ ▘▝ █  Check for mistakes.
   ▔▔▔▔

 ● No copilot-instructions.md found. Run /init to generate.

 ● Tip: /app
   └ Prefer a visual workspace? Try out the GitHub Copilot desktop app
      https://github.com/features/ai/github-app 

 ! Third-party MCP servers are disabled by your organization's Copilot policy. Only built-in servers are available.








╭──────────────────────────────────────────────────────────────────────────────────────────────────────────────────────╮
│ Confirm folder trust                                                                                                 │
│ ──────────────────────────────────────────────────────────────────────────────────────────────────────────────────── │
│ ╭──────────────────────────────────────────────────────────────────────────────────────────────────────────────────╮ │
│ │ /tmp/atr-cap/copilot/repo                                                                                        │ │
│ ╰──────────────────────────────────────────────────────────────────────────────────────────────────────────────────╯ │
│                                                                                                                      │
│ Copilot can read files in this folder and, with your permission, edit them or run code and shell commands. It will   │
│ remember your permissions for the rest of this session.                                                              │
│                                                                                                                      │
│ Do you trust the files in this folder?                                                                               │
│                                                                                                                      │
│ ❯ 1. Yes                                                                                                             │
│   2. Yes, and remember this folder for future sessions                                                               │
│   3. No (Esc)                                                                                                        │
│                                                                                                                      │
│ ↑/↓ to navigate · enter to select · esc to cancel                                                                    │
╰──────────────────────────────────────────────────────────────────────────────────────────────────────────────────────╯`

// copilotWorkingW120Pane is a live turn at the widest driven rung, and it lands in this task
// rather than with the rest of its ladder because composerPanes needs one driven composer per
// adapter before the adapter exists — an adapter with no entry there is one whose
// InputBoxVisible is unasserted, which is #510. A working pane serves: the composer is the
// empty "❯" between two horizontal rules, unchanged from the idle screen, and the status row
// below it is what the busy ladder reads.
const copilotWorkingW120Pane = `  Current   Sessions   Issues   Pull requests   Gists

   103                                                                                                                 ┃
   104                                                                                                                 ┃
   105                                                                                                                 ┃
   106                                                                                                                 ┃
   107                                                                                                                 ┃
   108                                                                                                                 ┃
   109                                                                                                                 ┃
   110                                                                                                                 ┃
   111                                                                                                                 ┃
   112                                                                                                                 ┃
   113                                                                                                                 ┃
   114                                                                                                                 ┃
   115                                                                                                                 ┃
   116                                                                                                                 ┃
   117                                                                                                                 ┃
   118                                                                                                                 ┃
   119                                                                                                                 ┃
   120                                                                                                                 ┃
   121                                                                                                                 ┃
   122                                                                                                                 ┃
   123                                                                                                                 ┃
   124                                                                                                                 ┃
   125                                                                                                                 ┃
   126                                                                                                                 ┃
   127                                                                                                                 ┃
   128                                                                                                                 ┃
   129                                                                                                                 ┃
   130                                                                                                                 ┃
   131                                                                                                                 ┃
   132                                                                                                                 ┃
   133                                                                                                                 ┃
   134                                                                                                                 ┃
                                                                                                                       ┃
 /tmp/atrium-capture/copilot-busy/repo [⎇ main]                                                     Session: 0 AIC used
────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────
❯
────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────
 ● Working · 544 B esc interrupt                                                                          GPT-5.3-Codex`

// copilotTrustgateLadder is the folder-trust gate at every driven width. The notes carry
// what is notable at each rung; the widths are the datum pane_width_test.go computes the
// floor from, so this is where "the floor is 20" stops being a sentence.
//
// THE 20 RUNG IS A HEIGHT FINDING, not just a width one. At 20 columns the dialog's box grows
// taller than the 40-row pane, so its TOP border and the title row scroll off — the title
// "Confirm folder trust" is present at 24 and simply absent at 20, not truncated. Every
// literal this matcher keys on therefore sits LOW in the dialog; a title-keyed matcher would
// miss a gate that is plainly on screen. TestCopilotTrustGateTitleIsGoneAtWidth20 pins it.
var copilotTrustgateLadder = []paneCapture{
	{name: "copilotTrustgateW20Pane", width: 20, note: "box outgrows the pane; title scrolled off", pane: copilotTrustgateW20Pane},
	{name: "copilotTrustgateW24Pane", width: 24, note: "headline in two lines", pane: copilotTrustgateW24Pane},
	{name: "copilotTrustgateW26Pane", width: 26, note: "", pane: copilotTrustgateW26Pane},
	{name: "copilotTrustgateW28Pane", width: 28, note: "raw flatten already fails here", pane: copilotTrustgateW28Pane},
	{name: "copilotTrustgateW34Pane", width: 34, note: "", pane: copilotTrustgateW34Pane},
	{name: "copilotTrustgateW40Pane", width: 40, note: "the widest rung raw flatten fails at", pane: copilotTrustgateW40Pane},
	{name: "copilotTrustgateW60Pane", width: 60, note: "headline on one line", pane: copilotTrustgateW60Pane},
	{name: "copilotTrustgateW120Pane", width: 120, note: "headline on one line", pane: copilotTrustgateW120Pane},
}

// TestCopilotTrustGateFiresAtEveryDrivenWidth is the positive half. paneCoverage asserts the
// same thing generically; this exists so a failure names the rung in this file's own terms,
// and because the negative halves below need the ladder anyway.
func TestCopilotTrustGateFiresAtEveryDrivenWidth(t *testing.T) {
	for _, c := range copilotTrustgateLadder {
		t.Run(c.label(), func(t *testing.T) {
			g, up := copilot.GateUp(c.pane)
			require.True(t, up, "the folder-trust gate must be detected")
			require.Equal(t, "trust", g.Name)
		})
	}
}

// TestCopilotTrustGateNeedsTheWallStrippingScan is the measurement that justifies
// flattenBottomBox existing at all, as an assertion rather than a claim in a comment: the
// SAME literal read through the flat window every declarative matcher uses is absent from 40
// down. If this ever goes green at 40, flattenChrome has started reconstructing across box
// borders and flattenBottomBox is no longer load-bearing.
func TestCopilotTrustGateNeedsTheWallStrippingScan(t *testing.T) {
	for _, c := range copilotTrustgateLadder {
		flat := flattenChrome(c.pane, WindowPrompt)
		if c.width >= 60 {
			require.Containsf(t, flat, copilotTrustHeadline,
				"%s: the headline is on one line here, so the flat window still reaches it", c.label())
			continue
		}
		require.NotContainsf(t, flat, copilotTrustHeadline,
			"%s: the headline wraps inside the borders here, so the flat window must NOT "+
				"reconstruct it — that is what flattenBottomBox is for", c.label())
	}
}

// TestCopilotTrustGateTitleIsGoneAtWidth20 is the height cliff the width ladder hides, and the
// reason this matcher keys on the headline and an option label rather than on the title. It
// asserts the title's absence at the narrowest rung and its presence one rung up, so a future
// build that stops overflowing reddens this instead of silently widening the matcher's options.
func TestCopilotTrustGateTitleIsGoneAtWidth20(t *testing.T) {
	const title = "Confirm folder trust"

	flat20, ok := flattenBottomBox(copilotTrustgateW20Pane)
	require.True(t, ok)
	require.NotContains(t, flat20, title,
		"at 20 columns the box outgrows the pane and the title row scrolls off the top")

	flat24, ok := flattenBottomBox(copilotTrustgateW24Pane)
	require.True(t, ok)
	require.Contains(t, flat24, title,
		"one rung up the box still fits, so the cliff is between these two and not below both")
}

// TestCopilotDialogsAreAlsoComposersToTheBoxPredicate holds the collision the adapter's
// InputBoxPrompts comment describes: the gate's selector is the composer's own "❯", so
// InputBoxVisible answers TRUE on a screen that consumes keystrokes. That is why GateUp and
// DetectPrompt are the guards keeping a queued first prompt off this dialog — exactly as for
// claude and agy — and it is why a gemini-style composer veto inside the matcher is wrong here.
func TestCopilotDialogsAreAlsoComposersToTheBoxPredicate(t *testing.T) {
	for _, c := range copilotTrustgateLadder {
		t.Run(c.label(), func(t *testing.T) {
			require.True(t, copilot.InputBoxVisible(c.pane),
				"the dialog reads as a composer, which is the hazard GateUp exists to cover")
			_, up := copilot.GateUp(c.pane)
			require.True(t, up, "and GateUp is what actually covers it")
		})
	}
}
