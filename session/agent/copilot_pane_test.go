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

const copilotApprovalW20Pane = `  Current  →

   /etc/hostname   ┃
   now.            ┃
                   ┃
 $ Shell Show… 41s ┃
   cat /etc/hostn… ┃
                   ┃
╭──────────────────╮
│ Allow directory  │
│ access           │
│ ──────────────── │
│ This action may  │
│ read or write    │
│ the following    │
│ path outside     │
│ your allowed     │
│ directory list.  │
│                  │
│ ╭──────────────╮ │
│ │ /etc/        │ │
│ │ hostname     │ │
│ ╰──────────────╯ │
│                  │
│ Do you want to   │
│ allow this?      │
│                  │
│   1. Yes         │
│ ❯2. Yes, and add │
│   these          │
│  directories to  │
│  the allowed     │
│  list            │
│   3. No (Esc)    │
│                  │
│ ↑/↓ to navigate  │
│ · enter to       │
│ select · esc to  │
│ cancel           │
╰──────────────────╯`

const copilotApprovalW24Pane = `  Current   Sessions  →

   exact shell         ┃
   command: cat        ┃
   /etc/hostname       ┃
                       ┃
 ● Executing  cat      ┃
   /etc/hostname  now. ┃
                       ┃
 $ Shell Show sys… 40s ┃
   cat /etc/hostname   ┃
                       ┃
╭──────────────────────╮
│ Allow directory      │
│ access               │
│ ──────────────────── │
│ This action may read │
│  or write the        │
│ following path       │
│ outside your allowed │
│  directory list.     │
│                      │
│ ╭──────────────────╮ │
│ │ /etc/hostname    │ │
│ ╰──────────────────╯ │
│                      │
│ Do you want to allow │
│  this?               │
│                      │
│   1. Yes             │
│ ❯2. Yes, and add     │
│  these directories   │
│  to the allowed list │
│                      │
│   3. No (Esc)        │
│                      │
│ ↑/↓ to navigate ·    │
│ enter to select ·    │
│ esc to cancel        │
╰──────────────────────╯`

const copilotApprovalW26Pane = `  Current   Sessions  →

                         ┃
 ❯ Run this exact  15:39 ┃
   shell command:        ┃
   cat                   ┃
   /etc/hostname         ┃
                         ┃
 ● Executing  cat        ┃
   /etc/hostname  now.   ┃
                         ┃
 $ Shell Show syste… 38s ┃
   cat /etc/hostname     ┃
                         ┃
╭────────────────────────╮
│ Allow directory access │
│ ────────────────────── │
│ This action may read   │
│ or write the following │
│  path outside your     │
│ allowed directory      │
│ list.                  │
│                        │
│ ╭────────────────────╮ │
│ │ /etc/hostname      │ │
│ ╰────────────────────╯ │
│                        │
│ Do you want to allow   │
│ this?                  │
│                        │
│   1. Yes               │
│ ❯2. Yes, and add these │
│   directories to the   │
│  allowed list          │
│   3. No (Esc)          │
│                        │
│ ↑/↓ to navigate ·      │
│ enter to select · esc  │
│ to cancel              │
╰────────────────────────╯`

const copilotApprovalW28Pane = `  Current   Sessions  →

   20 Aug 26 15:32         ┃
   README.md               ┃
                           ┃
 ❯ Run this exact    15:39 ┃
   shell command:          ┃
   cat /etc/hostname       ┃
                           ┃
 ● Executing  cat          ┃
   /etc/hostname  now.     ┃
                           ┃
 $ Shell Show system … 37s ┃
   cat /etc/hostname       ┃
                           ┃
╭──────────────────────────╮
│ Allow directory access   │
│ ──────────────────────── │
│ This action may read or  │
│ write the following path │
│  outside your allowed    │
│ directory list.          │
│                          │
│ ╭──────────────────────╮ │
│ │ /etc/hostname        │ │
│ ╰──────────────────────╯ │
│                          │
│ Do you want to allow     │
│ this?                    │
│                          │
│   1. Yes                 │
│ ❯ 2. Yes, and add these  │
│   directories to the     │
│   allowed list           │
│   3. No (Esc)            │
│                          │
│ ↑/↓ to navigate · enter  │
│ to select · esc to       │
│ cancel                   │
╰──────────────────────────╯`

const copilotApprovalW34Pane = `  Current   Sessions   Issues  →

   26 15:32 ..                   ┃
   drwxrwxr-x 7 zvi zvi 240 Aug  ┃
   26 15:32 .git                 ┃
   -rw-rw-r-- 1 zvi zvi  20 Aug  ┃
   26 15:32 README.md            ┃
                                 ┃
 ❯ Run this exact shell    15:39 ┃
   command: cat                  ┃
   /etc/hostname                 ┃
                                 ┃
 ● Executing  cat /etc/hostname  ┃
    now.                         ┃
                                 ┃
 $ Shell Show system hostna… 35s ┃
   cat /etc/hostname             ┃
                                 ┃
╭────────────────────────────────╮
│ Allow directory access         │
│ ────────────────────────────── │
│ This action may read or write  │
│ the following path outside     │
│ your allowed directory list.   │
│                                │
│ ╭────────────────────────────╮ │
│ │ /etc/hostname              │ │
│ ╰────────────────────────────╯ │
│                                │
│ Do you want to allow this?     │
│                                │
│   1. Yes                       │
│ ❯ 2. Yes, and add these        │
│   directories to the allowed   │
│   list                         │
│   3. No (Esc)                  │
│                                │
│ ↑/↓ to navigate · enter to     │
│ select · esc to cancel         │
╰────────────────────────────────╯`

const copilotApprovalW40Pane = `  Current   Sessions   Issues  →

   drwxrwxr-x 3 zvi zvi  80 Aug 26     ┃
   15:32 .                             ┃
   drwxrwxr-x 5 zvi zvi 180 Aug 26     ┃
   15:32 ..                            ┃
   drwxrwxr-x 7 zvi zvi 240 Aug 26     ┃
   15:32 .git                          ┃
   -rw-rw-r-- 1 zvi zvi  20 Aug 26     ┃
   15:32 README.md                     ┃
                                       ┃
 ❯ Run this exact shell command: 15:39 ┃
   cat /etc/hostname                   ┃
                                       ┃
 ● Executing  cat /etc/hostname  now.  ┃
                                       ┃
 $ Shell Show system hostname      34s ┃
   cat /etc/hostname                   ┃
                                       ┃
╭──────────────────────────────────────╮
│ Allow directory access               │
│ ──────────────────────────────────── │
│ This action may read or write the    │
│ following path outside your allowed  │
│ directory list.                      │
│                                      │
│ ╭──────────────────────────────────╮ │
│ │ /etc/hostname                    │ │
│ ╰──────────────────────────────────╯ │
│                                      │
│ Do you want to allow this?           │
│                                      │
│   1. Yes                             │
│ ❯2. Yes, and add these directories   │
│  to the allowed list                 │
│   3. No (Esc)                        │
│                                      │
│ ↑/↓ to navigate · enter to select ·  │
│ esc to cancel                        │
╰──────────────────────────────────────╯`

const copilotApprovalW60Pane = `  Current   Sessions   Issues   Pull requests   Gists

 │ Preparing to run shell command                          ┃
                                                           ┃
 ● Running  ls -la  in the current directory now.          ┃
                                                           ┃
 $ Shell List all files with details 6 lines…              ┃
   ls -la                                                  ┃
                                                           ┃
 ● total 4                                                 ┃
   drwxrwxr-x 3 zvi zvi  80 Aug 26 15:32 .                 ┃
   drwxrwxr-x 5 zvi zvi 180 Aug 26 15:32 ..                ┃
   drwxrwxr-x 7 zvi zvi 240 Aug 26 15:32 .git              ┃
   -rw-rw-r-- 1 zvi zvi  20 Aug 26 15:32 README.md         ┃
                                                           ┃
 ❯ Run this exact shell command: cat /etc/hostname   15:39 ┃
                                                           ┃
 ● Executing  cat /etc/hostname  now.                      ┃
                                                           ┃
 $ Shell Show system hostname                          32s ┃
   cat /etc/hostname                                       ┃
                                                           ┃
╭──────────────────────────────────────────────────────────╮
│ Allow directory access                                   │
│ ──────────────────────────────────────────────────────── │
│ This action may read or write the following path outside │
│  your allowed directory list.                            │
│                                                          │
│ ╭──────────────────────────────────────────────────────╮ │
│ │ /etc/hostname                                        │ │
│ ╰──────────────────────────────────────────────────────╯ │
│                                                          │
│ Do you want to allow this?                               │
│                                                          │
│   1. Yes                                                 │
│ ❯ 2. Yes, and add these directories to the allowed list  │
│   3. No (Esc)                                            │
│                                                          │
│ ↑/↓ to navigate · enter to select · esc to cancel        │
╰──────────────────────────────────────────────────────────╯`

const copilotApprovalW120Pane = `  Current   Sessions   Issues   Pull requests   Gists

 ⌄ Thought for 1s                                                                                                      ┃
 │ Preparing to run shell command                                                                                      ┃
                                                                                                                       ┃
 ● Running  ls -la  in the current directory now.                                                                      ┃
                                                                                                                       ┃
 $ Shell List all files with details 6 lines…                                                                          ┃
   ls -la                                                                                                              ┃
                                                                                                                       ┃
 ● total 4                                                                                                             ┃
   drwxrwxr-x 3 zvi zvi  80 Aug 26 15:32 .                                                                             ┃
   drwxrwxr-x 5 zvi zvi 180 Aug 26 15:32 ..                                                                            ┃
   drwxrwxr-x 7 zvi zvi 240 Aug 26 15:32 .git                                                                          ┃
   -rw-rw-r-- 1 zvi zvi  20 Aug 26 15:32 README.md                                                                     ┃
                                                                                                                       ┃
 ❯ Run this exact shell command: cat /etc/hostname                                                               15:39 ┃
                                                                                                                       ┃
 ● Executing  cat /etc/hostname  now.                                                                                  ┃
                                                                                                                       ┃
 $ Shell Show system hostname                                                                                      30s ┃
   cat /etc/hostname                                                                                                   ┃
                                                                                                                       ┃
╭──────────────────────────────────────────────────────────────────────────────────────────────────────────────────────╮
│ Allow directory access                                                                                               │
│ ──────────────────────────────────────────────────────────────────────────────────────────────────────────────────── │
│ This action may read or write the following path outside your allowed directory list.                                │
│                                                                                                                      │
│ ╭──────────────────────────────────────────────────────────────────────────────────────────────────────────────────╮ │
│ │ /etc/hostname                                                                                                    │ │
│ ╰──────────────────────────────────────────────────────────────────────────────────────────────────────────────────╯ │
│                                                                                                                      │
│ Do you want to allow this?                                                                                           │
│                                                                                                                      │
│   1. Yes                                                                                                             │
│ ❯ 2. Yes, and add these directories to the allowed list                                                              │
│   3. No (Esc)                                                                                                        │
│                                                                                                                      │
│ ↑/↓ to navigate · enter to select · esc to cancel                                                                    │
╰──────────────────────────────────────────────────────────────────────────────────────────────────────────────────────╯`

// copilotApprovalLadder is the out-of-worktree path approval at every driven width. Unlike
// the trust gate's box, this one FITS the 40-row pane at every rung — its top border is on
// screen at all eight — so its title survives too. That is box height rather than a property
// of titles, and the matcher keys on the headline and the option label regardless.
var copilotApprovalLadder = []paneCapture{
	{name: "copilotApprovalW20Pane", width: 20, note: "option label in four lines", pane: copilotApprovalW20Pane},
	{name: "copilotApprovalW24Pane", width: 24, note: "", pane: copilotApprovalW24Pane},
	{name: "copilotApprovalW26Pane", width: 26, note: "", pane: copilotApprovalW26Pane},
	{name: "copilotApprovalW28Pane", width: 28, note: "the widest rung raw flatten fails at", pane: copilotApprovalW28Pane},
	{name: "copilotApprovalW34Pane", width: 34, note: "", pane: copilotApprovalW34Pane},
	{name: "copilotApprovalW40Pane", width: 40, note: "selector renders \"❯2.\" with no space", pane: copilotApprovalW40Pane},
	{name: "copilotApprovalW60Pane", width: 60, note: "", pane: copilotApprovalW60Pane},
	{name: "copilotApprovalW120Pane", width: 120, note: "everything on one line", pane: copilotApprovalW120Pane},
}

// TestCopilotApprovalFiresAtEveryDrivenWidth is the positive half, and it also asserts the
// matcher's NoAutoTap, because that flag is the load-bearing part of this entry: the dialog's
// pre-selected option WIDENS the allowed-path list rather than approving one action.
func TestCopilotApprovalFiresAtEveryDrivenWidth(t *testing.T) {
	for _, c := range copilotApprovalLadder {
		t.Run(c.label(), func(t *testing.T) {
			m, ok := copilot.DetectPrompt(c.pane)
			require.True(t, ok, "the approval dialog must be detected")
			require.Equal(t, "approval", m.Name)
			require.True(t, m.NoAutoTap,
				"Enter here selects \"Yes, and add these directories to the allowed list\", "+
					"which extends the agent's filesystem reach past its worktree for the session")
		})
	}
}

// TestCopilotApprovalNeedsTheWallStrippingScan is the approval half of the same measurement
// the gate carries: raw flattening reaches this headline down to 34 and no further.
func TestCopilotApprovalNeedsTheWallStrippingScan(t *testing.T) {
	for _, c := range copilotApprovalLadder {
		flat := flattenChrome(c.pane, WindowPrompt)
		if c.width >= 34 {
			require.Containsf(t, flat, copilotApprovalHeadline,
				"%s: the headline is on one line here", c.label())
			continue
		}
		require.NotContainsf(t, flat, copilotApprovalHeadline,
			"%s: the headline wraps inside the borders here, so the flat window must NOT "+
				"reconstruct it", c.label())
	}
}

// TestCopilotApprovalOptionExcludesTheSelector is why copilotApprovalOption starts at "Yes,"
// rather than at "❯ 2.". The gap between selector and number is not stable and NOT MONOTONIC
// in width: "❯ 2." at 120, 60, 34 and 28, "❯2." at 40, 26, 24 and 20. A matcher including the
// prefix would have passed a 120-column check, failed at 40, and passed again at 34 — the
// shape of drift a single wide capture cannot see.
func TestCopilotApprovalOptionExcludesTheSelector(t *testing.T) {
	spaced := map[int]bool{120: true, 60: true, 34: true, 28: true}
	for _, c := range copilotApprovalLadder {
		t.Run(c.label(), func(t *testing.T) {
			flat, ok := flattenBottomBox(c.pane)
			require.True(t, ok)
			require.Contains(t, flat, copilotApprovalOption,
				"the label without the selector reaches every rung")

			want := "❯2. " + copilotApprovalOption
			if spaced[c.width] {
				want = "❯ 2. " + copilotApprovalOption
			}
			require.Containsf(t, flat, want,
				"this rung renders the selector %q; the OTHER spelling is what a "+
					"prefix-bearing literal would have missed", want[:len(want)-len(copilotApprovalOption)])
		})
	}
}

// TestCopilotApprovalAndTrustGateDoNotCrossMatch is the discriminator, and it is needed
// because the two dialogs share their decline row ("3. No (Esc)") and their whole navigation
// footer ("↑/↓ to navigate · enter to select · esc to cancel"). Neither shared string can tell
// them apart, so each matcher's literals must be the ones only its own dialog renders. A
// crossing failure would surface as a trust gate reported on a live approval, which holds the
// queued prompt forever, or as an approval reported on a startup gate, which is worse.
func TestCopilotApprovalAndTrustGateDoNotCrossMatch(t *testing.T) {
	for _, c := range copilotApprovalLadder {
		t.Run("approval pane is not a gate: "+c.label(), func(t *testing.T) {
			_, up := copilot.GateUp(c.pane)
			require.False(t, up)
		})
	}
	for _, c := range copilotTrustgateLadder {
		t.Run("gate pane is not an approval: "+c.label(), func(t *testing.T) {
			_, ok := copilot.DetectPrompt(c.pane)
			require.False(t, ok)
		})
	}
}

const copilotWorkingW20Pane = `  Current  →

   939             ┃
   940             ┃
   941             ┃
   942             ┃
   943             ┃
   944             ┃
   945             ┃
   946             ┃
   947             ┃
   948             ┃
   949             ┃
   950             ┃
   951             ┃
   952             ┃
   953             ┃
   954             ┃
   955             ┃
   956             ┃
   957             ┃
   958             ┃
   959             ┃
   960             ┃
   961             ┃
   962             ┃
   963             ┃
   9               ┃
                   ┃
 /tmp/.../repo
 [⎇ main]
 Session: 0 AIC
 used
────────────────────
❯
────────────────────
 ◎    · 3.8 esc
 WorkiKiB   interr
 ng         upt
 GPT-5.3-Codex`

const copilotWorkingW24Pane = `  Current   Sessions  →

   850                 ┃
   851                 ┃
   852                 ┃
   853                 ┃
   854                 ┃
   855                 ┃
   856                 ┃
   857                 ┃
   858                 ┃
   859                 ┃
   860                 ┃
   861                 ┃
   862                 ┃
   863                 ┃
   864                 ┃
   865                 ┃
   866                 ┃
   867                 ┃
   868                 ┃
   869                 ┃
   870                 ┃
   871                 ┃
   872                 ┃
   873                 ┃
   874                 ┃
   875                 ┃
   876                 ┃
                       ┃
 /tmp/.../repo
 [⎇ main]
 Session: 0 AIC used
────────────────────────
❯
────────────────────────
 ◉     · 3.4  esc
 WorkinKiB    interrup
 g            t
 GPT-5.3-Codex`

const copilotWorkingW26Pane = `  Current   Sessions  →

   720                   ┃
   721                   ┃
   722                   ┃
   723                   ┃
   724                   ┃
   725                   ┃
   726                   ┃
   727                   ┃
   728                   ┃
   729                   ┃
   730                   ┃
   731                   ┃
   732                   ┃
   733                   ┃
   734                   ┃
   735                   ┃
   736                   ┃
   737                   ┃
   738                   ┃
   739                   ┃
   740                   ┃
   741                   ┃
   742                   ┃
   743                   ┃
   744                   ┃
   745                   ┃
   746                   ┃
                         ┃
 /tmp/.../repo
 [⎇ main]
 Session: 0 AIC used
──────────────────────────
❯
──────────────────────────
 ◎      · 2.9   esc
 WorkingKiB     interrupt

 GPT-5.3-Codex`

const copilotWorkingW28Pane = `  Current   Sessions  →

   601                     ┃
   602                     ┃
   603                     ┃
   604                     ┃
   605                     ┃
   606                     ┃
   607                     ┃
   608                     ┃
   609                     ┃
   610                     ┃
   611                     ┃
   612                     ┃
   613                     ┃
   614                     ┃
   615                     ┃
   616                     ┃
   617                     ┃
   618                     ┃
   619                     ┃
   620                     ┃
   621                     ┃
   622                     ┃
   623                     ┃
   624                     ┃
   625                     ┃
   626                     ┃
   6                       ┃
                           ┃
 /tmp/.../repo
 [⎇ main]
 Session: 0 AIC used
────────────────────────────
❯
────────────────────────────
 ◎      · 2.4    esc
 WorkingKiB      interrupt

 GPT-5.3-Codex`

const copilotWorkingW34Pane = `  Current   Sessions   Issues  →

   476                           ┃
   477                           ┃
   478                           ┃
   479                           ┃
   480                           ┃
   481                           ┃
   482                           ┃
   483                           ┃
   484                           ┃
   485                           ┃
   486                           ┃
   487                           ┃
   488                           ┃
   489                           ┃
   490                           ┃
   491                           ┃
   492                           ┃
   493                           ┃
   494                           ┃
   495                           ┃
   496                           ┃
   497                           ┃
   498                           ┃
   499                           ┃
   500                           ┃
   501                           ┃
   502                           ┃
   503                           ┃
   504                           ┃
                                 ┃
 /tmp/atrium-capture/.../repo
 [⎇ main]     Session: 0 AIC used
──────────────────────────────────
❯
──────────────────────────────────
 ◉ Working· 1.9 KiB esc
                    interrupt
 GPT-5.3-Codex`

const copilotWorkingW40Pane = `  Current   Sessions   Issues  →

   351                                 ┃
   352                                 ┃
   353                                 ┃
   354                                 ┃
   355                                 ┃
   356                                 ┃
   357                                 ┃
   358                                 ┃
   359                                 ┃
   360                                 ┃
   361                                 ┃
   362                                 ┃
   363                                 ┃
   364                                 ┃
   365                                 ┃
   366                                 ┃
   367                                 ┃
   368                                 ┃
   369                                 ┃
   370                                 ┃
   371                                 ┃
   372                                 ┃
   373                                 ┃
   374                                 ┃
   375                                 ┃
   376                                 ┃
   377                                 ┃
   378                                 ┃
   379                                 ┃
   380                                 ┃
                                       ┃
 /tmp/atrium-capture/copilot-busy/repo
 [⎇ main]           Session: 0 AIC used
────────────────────────────────────────
❯
────────────────────────────────────────
 ◎ Working · 1.5 KiB esc interrupt
 GPT-5.3-Codex`

const copilotWorkingW60Pane = `  Current   Sessions   Issues   Pull requests   Gists

   229                                                     ┃
   230                                                     ┃
   231                                                     ┃
   232                                                     ┃
   233                                                     ┃
   234                                                     ┃
   235                                                     ┃
   236                                                     ┃
   237                                                     ┃
   238                                                     ┃
   239                                                     ┃
   240                                                     ┃
   241                                                     ┃
   242                                                     ┃
   243                                                     ┃
   244                                                     ┃
   245                                                     ┃
   246                                                     ┃
   247                                                     ┃
   248                                                     ┃
   249                                                     ┃
   250                                                     ┃
   251                                                     ┃
   252                                                     ┃
   253                                                     ┃
   254                                                     ┃
   255                                                     ┃
   256                                                     ┃
   257                                                     ┃
   258                                                     ┃
   259                                                     ┃
   2                                                       ┃
                                                           ┃
 /tmp/atrium-capture/.../repo [⎇ main]  Session: 0 AIC used
────────────────────────────────────────────────────────────
❯
────────────────────────────────────────────────────────────
 ◉ Working · 1.0 KiB esc interrupt            GPT-5.3-Codex`

// copilotBusyLadder is a live turn at every driven width the marker survives. The marker sits
// in the status row that REPLACES the hint row below the composer, so MarkerWindow stays 0 and
// footerRegion's below-the-box anchor finds it — claude's arrangement, not codex's or gemini's,
// both of which render their status row ABOVE the composer and need a window instead.
//
// WHY THIS LADDER WAS DRIVEN TWICE. The first sweep ended its turn after the width-60 rung, so
// six of its eight rungs captured an IDLE pane while looking like a measurement — an identical
// credit figure across all six and the hint row where the status row belongs. A ladder is only
// valid for a transient state if the state outlives the sweep. The invalid captures are kept
// beside the run directory under captures-invalid-working-2026-08-26 rather than deleted,
// because the design spec cites them as the evidence that they were invalid.
//
// WHAT MAKES THIS SWEEP VALID is not "the marker is present at every rung" — it is not, and
// two rungs below live next door. It is the BYTE COUNTER, which grows at every one of the
// eight: 544 B, then 1.0, 1.5, 1.9, 2.4, 2.9, 3.4 and 3.8 KiB. A paused turn can leave a
// painted status row behind; it cannot advance a counter. That distinction is what separates a
// missed rung from an invalid one, and it is the check the first sweep lacked.
var copilotBusyLadder = []paneCapture{
	{name: "copilotWorkingW26Pane", width: 26, note: "the floor; footer multi-column, marker on its own line", pane: copilotWorkingW26Pane},
	{name: "copilotWorkingW28Pane", width: 28, note: "footer becomes multi-column", pane: copilotWorkingW28Pane},
	{name: "copilotWorkingW34Pane", width: 34, note: "renders \"Working·\" with no space before the separator", pane: copilotWorkingW34Pane},
	{name: "copilotWorkingW40Pane", width: 40, note: "narrowest rung the whole hint is still contiguous", pane: copilotWorkingW40Pane},
	{name: "copilotWorkingW60Pane", width: 60, note: "", pane: copilotWorkingW60Pane},
	{name: "copilotWorkingW120Pane", width: 120, note: "status row on one line", pane: copilotWorkingW120Pane},
}

// copilotBusyTruncatedRungs are the rungs where the multi-column footer splits "Working"
// mid-word, so no substring survives and no window value could reach one. They are negative
// evidence, not a windowing failure: the row is ON SCREEN and the phrase is not there.
// This is the rung LiveSpinner exists for — the animating spinner is the only signal left —
// and it is deliberately not a standalone latch, so a session here reports idle until the
// spinner support lands.
var copilotBusyTruncatedRungs = []paneCapture{
	{name: "copilotWorkingW24Pane", width: 24, note: "marker split \"Workin\" / \"g\"", pane: copilotWorkingW24Pane},
	{name: "copilotWorkingW20Pane", width: 20, note: "marker split \"Worki\" / \"ng\"", pane: copilotWorkingW20Pane},
}

// TestCopilotBusyMarkerFiresAtEveryDrivenWidth is the positive half.
func TestCopilotBusyMarkerFiresAtEveryDrivenWidth(t *testing.T) {
	for _, c := range copilotBusyLadder {
		t.Run(c.label(), func(t *testing.T) {
			require.True(t, copilot.HasBusyMarker(c.pane))
		})
	}
}

// TestCopilotBusyMarkerIsTruncatedAtTheNarrowestRungs records the two misses as measurements
// rather than dropping them, the way geminiBusyTruncatedRungs does. The premise is asserted
// alongside the verdict: without it this would say only "the marker misses here", which is
// also what an idle pane says — and an idle pane is exactly what the first sweep of this
// ladder produced. The growing byte counter is what tells the two apart, so it is what the
// premise reads.
func TestCopilotBusyMarkerIsTruncatedAtTheNarrowestRungs(t *testing.T) {
	for _, c := range copilotBusyTruncatedRungs {
		t.Run(c.label(), func(t *testing.T) {
			require.Contains(t, c.pane, "KiB",
				"the premise: the byte counter is on screen, so this is a LIVE turn and not "+
					"the idle pane the first sweep of this ladder mistook for one")
			require.NotContains(t, footerRegion(c.pane), "Working",
				"the marker is split mid-word here, so no window reaches it")
			require.False(t, copilot.HasBusyMarker(c.pane),
				"recording the miss is the point; this rung is what LiveSpinner would be for")
		})
	}
}

// TestCopilotBusyMarkerCannotKeyOnTheInterruptHint is why BusyMarkers holds "Working" alone.
// The status row reads "<spinner> Working · <N> B esc interrupt", so the byte counter sits
// BETWEEN the two words and "Working esc interrupt" is never contiguous at any width — a fact
// a wide capture alone would suggest is fine. And "esc interrupt" stops being contiguous below
// 40, one rung ABOVE the width at which the footer goes multi-column, because the
// single-column row wraps there first. Both halves are asserted against the driven panes
// rather than described, over every rung including the two the marker misses — the hint's
// reach is a fact about the row, not about whether this adapter can read it.
func TestCopilotBusyMarkerCannotKeyOnTheInterruptHint(t *testing.T) {
	all := append(append([]paneCapture{}, copilotBusyLadder...), copilotBusyTruncatedRungs...)
	for _, c := range all {
		t.Run(c.label(), func(t *testing.T) {
			region := footerRegion(c.pane)
			require.NotContains(t, region, "Working esc interrupt",
				"the byte counter sits between the words at every width")
			if c.width >= 40 {
				require.Contains(t, region, "esc interrupt",
					"the hint is contiguous while the single-column row still fits it")
				return
			}
			require.NotContains(t, region, "esc interrupt",
				"the row wraps its cells independently from here down, so a matcher keyed "+
					"on this hint would miss every narrow pane")
		})
	}
}

// TestCopilotBusyPanesAreNeitherGateNorPrompt is the negative direction paneCoverage cannot
// express (that table is positive-only). It uses the busy panes because a working pane is the
// shape most likely to false-match: it is the one that actually renders a composer and a
// footer, where both dialog matchers must stay silent. Both lists are walked, because a rung
// the busy marker misses is still a rung the dialog matchers must not fire on.
//
// WHAT MAKES THEM SILENT IS THE ANCHOR, NOT THE LITERALS, and getting that backwards leads to
// a mutation that cannot fail. flattenBottomBox is false on all eight of these panes — the
// composer is delimited by horizontal rules, so nothing here presents a bottom border above
// walled rows — and both matchers return early on that. Setting copilotTrustHeadline to
// "Working" therefore leaves this green: the gate never reaches its literals at all. The first
// assertion pins the anchor for that reason, so the mechanism is what is guarded rather than a
// coincidence of which strings the footer happens not to contain.
//
// The last assertion is the one with teeth on the other axis. A copilot dialog reads as a
// composer to InputBoxVisible, so that predicate cannot tell the two apart; what makes the
// collision harmless is that GateUp and DetectPrompt disagree on these panes and agree on the
// dialogs.
func TestCopilotBusyPanesAreNeitherGateNorPrompt(t *testing.T) {
	all := append(append([]paneCapture{}, copilotBusyLadder...), copilotBusyTruncatedRungs...)
	for _, c := range all {
		t.Run(c.label(), func(t *testing.T) {
			_, boxed := flattenBottomBox(c.pane)
			require.False(t, boxed,
				"the mechanism: a live turn presents no anchored box, so both dialog matchers "+
					"return before they ever look at a literal")

			_, up := copilot.GateUp(c.pane)
			require.False(t, up, "a live turn is not a startup gate")
			_, ok := copilot.DetectPrompt(c.pane)
			require.False(t, ok, "a live turn is not a blocking prompt")
			require.True(t, copilot.InputBoxVisible(c.pane),
				"and the composer IS readable here, which is what makes prompt delivery work")
		})
	}
}

// TestCopilotBusyMarkerSitsBelowTheComposer is what stands in for a guard on MarkerWindow, and
// the first thing to say is that the field itself CANNOT be guarded by these panes. Setting it
// to codex's 8 reddens nothing: the marker sits one to three non-empty lines from the bottom at
// every rung, so a bottom-8 window is a strict SUPERSET of the region footerRegion picks. A
// superset can add a false positive; it cannot produce a miss, so no driven pane here
// distinguishes 0 from 8. The third assertion below records that as a fact rather than leaving
// it as an unstated hole.
//
// What IS guardable is the PREMISE MarkerWindow 0 rests on — that copilot paints its status row
// BELOW the composer, claude's arrangement. Both halves are asserted, because only the pair
// says it: the marker is inside the below-box footer, and absent from the block ABOVE the box,
// which is exactly where codex and gemini put theirs and why they need a window. A future build
// that moved the row above the composer would leave HasBusyMarker green through the fallback
// and redden this instead, which is the whole reason it is worth writing.
func TestCopilotBusyMarkerSitsBelowTheComposer(t *testing.T) {
	const marker = "Working" // copilot's only BusyMarkers entry

	for _, c := range copilotBusyLadder {
		t.Run(c.label(), func(t *testing.T) {
			below, ok := footerBelowBox(c.pane)
			require.True(t, ok, "the composer's bottom rule is the anchor footerRegion needs")
			require.Contains(t, below, marker,
				"the status row REPLACES the hint row below the composer, which is what makes "+
					"MarkerWindow 0 the right value")

			above, ok := aboveBoxBlock(c.pane)
			require.True(t, ok, "the premise: there IS a live block above the box to look in")
			require.NotContains(t, above, marker,
				"and it is not codex's or gemini's arrangement — their status row is here, "+
					"above the composer, which is why they need a window and copilot does not")

			require.Contains(t, liveChromeLines(c.pane, 8), marker,
				"disclosed, not asserted as a virtue: codex's window would ALSO find the "+
					"marker here, so this ladder cannot falsify MarkerWindow 8. What rules it "+
					"out is the arrangement above, not a failing rung")
		})
	}
}
