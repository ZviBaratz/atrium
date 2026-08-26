package agent

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Driven panes from GitHub Copilot CLI 1.0.80 (npm @github/copilot), Linux, captured
// 2026-08-26 in an isolated COPILOT_HOME (COPILOT_HOME and XDG_CONFIG_HOME pointed at a
// scratch directory) with the organization's token injected through the environment, against a
// git repo created for the sweep. Widths 120/60/40/34/28/26/24/20.
//
// THE HARNESS DID NOT DRIVE THESE. scripts/drive-agent.sh has no copilot support — it names no
// copilot binary, no copilot trust write and no copilot resume row — so these were driven by
// hand and this header used to credit the script anyway, which made the captures look
// reproducible from the tree when they are not. Adding copilot to that harness is what would
// make the credit true, and until then the isolation each rung depended on is the reader's to
// reproduce: a driver that skips it writes a nonce trust record and an allowed-directory entry
// into the real ~/.copilot, which is the same hazard the script's own trust-write disclosure
// exists to state for the four agents it does drive.
//
// THE PANE HEIGHT IS 40 AT EVERY RUNG, which is a limit of this ladder and not a property of
// copilot: a width ladder cannot see a height axis. What covers that axis instead is
// TestCopilotNeverDeliversAPromptIntoADialog, which truncates each of these panes from the top
// and holds the composed delivery predicate at every height it produces.
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
// every rung of both ladders below. TestCopilotDialogSelectorIsTheComposerGlyph holds the
// collision as a fact rather than leaving it as a trap.
//
// The veto copilot DOES carry runs the other way round — ModalVeto, on the composer predicate
// rather than inside the matchers — and the two must not be confused. One asks "is a composer
// on screen, so this is not a dialog"; the other asks "is a dialog on screen, so this is not a
// composer". The first is false here at every rung. The second is what stops a queued prompt
// being typed into a dialog whose literals have scrolled off the top of a short pane.

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
// taller than the 40-row pane, so its TOP border scrolls off and the title goes with it — but
// only PARTLY. The title wraps to two rows there and the pane keeps the second: the box's first
// visible row is "trust". So it is head-truncated, not absent, and the lesson is sharper than
// "do not key on a title" — a matcher keyed on a title SUFFIX would fire here, on a fragment
// that is not the title. TestCopilotTrustGateTitleIsGoneAtWidth20 pins both halves.
//
// What that rung CANNOT show is what happens as the pane gets shorter still, because every
// capture here is 40 rows. TestCopilotNeverDeliversAPromptIntoADialog covers that axis.
var copilotTrustgateLadder = []paneCapture{
	{name: "copilotTrustgateW20Pane", width: 20, note: "box outgrows the pane; title scrolled off", pane: copilotTrustgateW20Pane},
	{name: "copilotTrustgateW24Pane", width: 24, note: "headline in three lines", pane: copilotTrustgateW24Pane},
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
		"at 20 columns the box outgrows the pane and the title's first row scrolls off the top")
	require.Contains(t, flat20, "trust",
		"HEAD-truncated, not absent: the title wrapped and the pane kept its second row, so a "+
			"matcher keyed on a title suffix would fire here on a fragment that is not the title")

	flat24, ok := flattenBottomBox(copilotTrustgateW24Pane)
	require.True(t, ok)
	require.Contains(t, flat24, title,
		"one rung up the box still fits, so the cliff is between these two and not below both")
}

// TestCopilotDialogSelectorIsTheComposerGlyph holds the collision the adapter's InputBoxPrompts
// comment describes, at the level where it holds — the box PRIMITIVE. inputBoxText finds a
// composer on every rung of both dialogs, because the selected option row opens with the
// composer's own "❯". That is the fact that rules out a gemini-style veto inside the matchers.
//
// What the ADAPTER answers is the opposite, and that is ModalVeto doing its job: the same pane
// that presents a composer to the primitive is refused by InputBoxVisible. Both are asserted
// here so the two cannot drift apart — a future change that made the primitive stop seeing a
// composer would make the veto look unnecessary, and this says why it is not.
func TestCopilotDialogSelectorIsTheComposerGlyph(t *testing.T) {
	for _, ladder := range [][]paneCapture{copilotTrustgateLadder, copilotApprovalLadder} {
		for _, c := range ladder {
			t.Run(c.label(), func(t *testing.T) {
				_, seen := inputBoxText(c.pane, promptSet(copilot.InputBoxPrompts))
				require.True(t, seen,
					"the selected option row opens with the composer glyph, so the box "+
						"primitive cannot tell this dialog from a composer")
				require.False(t, copilot.InputBoxVisible(c.pane),
					"and ModalVeto is what makes the adapter disagree with the primitive")
			})
		}
	}
}

// TestCopilotNeverDeliversAPromptIntoADialog is the height axis no width ladder can reach, and
// the reason ModalVeto is structural rather than another literal.
//
// It recomputes what session/tmux's AwaitingInput computes — !GateUp && !DetectPrompt &&
// InputBoxVisible — because that composition is what decides whether a queued prompt is typed
// into the pane, and no test in this package was asking the composed question. It then asks it
// at every pane height each driven dialog can be reduced to, by dropping rows off the TOP: that
// is what a pane shorter than the dialog renders, and copilotTrustgateW20Pane is already in
// that state at 40 rows.
//
// The failure it exists to prevent is specific. Both matchers key on literals, so as the pane
// shrinks the headline goes first and the matcher goes false while the selector, the option
// rows and the whole navigation footer are still on screen. Without the veto there is a band of
// heights at every rung where the dialog is plainly up, GateUp and DetectPrompt are both
// false, and InputBoxVisible is true — so SendPrompt types the queued prompt and presses Enter,
// which on the approval dialog selects the pre-highlighted "Yes, and add these directories to
// the allowed list". NoAutoTap does not cover it: that flag is read only after DetectPrompt has
// fired.
//
// The second half is the anti-vacuity check, and it is not optional. If the matchers happened to
// hold at every height, the first half would pass with the veto deleted and this test would be
// asserting nothing. So it also counts the heights that WOULD have been deliverable without the
// veto — matchers blind, and the box primitive still reporting a composer — and requires that
// band to be non-empty at every rung. That count is the finding as a number, kept in the test
// rather than in a sentence: the first attempt at that sentence gave a figure from one
// arithmetic, and the count belongs to the panes.
func TestCopilotNeverDeliversAPromptIntoADialog(t *testing.T) {
	awaitingInput := func(pane string) bool {
		if _, up := copilot.GateUp(pane); up {
			return false
		}
		if _, prompted := copilot.DetectPrompt(pane); prompted {
			return false
		}
		return copilot.InputBoxVisible(pane)
	}

	for _, ladder := range [][]paneCapture{copilotTrustgateLadder, copilotApprovalLadder} {
		for _, c := range ladder {
			t.Run(c.label(), func(t *testing.T) {
				lines := strings.Split(c.pane, "\n")
				vetoIsWhatSavedIt := 0
				for drop := 0; drop < len(lines); drop++ {
					short := strings.Join(lines[drop:], "\n")
					if !copilotModalUp(short) {
						// The box's own bottom border has gone: this is no longer a pane
						// showing a dialog, so there is nothing left to protect.
						break
					}
					require.Falsef(t, awaitingInput(short),
						"%s truncated to %d rows: the dialog is still on screen and a queued "+
							"prompt would be typed into it",
						c.name, len(lines)-drop)

					_, gated := copilot.GateUp(short)
					_, prompted := copilot.DetectPrompt(short)
					_, composer := inputBoxText(short, promptSet(copilot.InputBoxPrompts))
					if !gated && !prompted && composer {
						vetoIsWhatSavedIt++
					}
				}
				require.Positivef(t, vetoIsWhatSavedIt,
					"%s: no truncation of this pane was one where the matchers went blind AND "+
						"the box primitive still saw a composer, so the assertion above would "+
						"also pass with ModalVeto deleted — it must not be read as evidence "+
						"that the veto works", c.name)
			})
		}
	}
}

// TestCopilotTrustGateNeedsBothLiterals and its approval sibling are the only shapes that can
// tell this matcher's AND from an OR. Every driven pane agrees with both, because the two
// dialogs differ in BOTH literals — so a ladder of real captures cannot falsify the conjunction,
// and changing "&&" to "||" in copilotDialogVisible left the whole package green.
//
// The panes are therefore built rather than driven, and they are built minimally: a box, one
// literal, no other. That is a real screen shape — a session printing this file's own consts,
// or an agent quoting one dialog's headline while discussing it — and under an OR each one
// gates. A gated session holds its queued first prompt forever.
func TestCopilotTrustGateNeedsBothLiterals(t *testing.T) {
	_, up := copilot.GateUp(boxedPane(copilotTrustHeadline))
	require.False(t, up, "the headline alone is ordinary English and must not gate")
	_, up = copilot.GateUp(boxedPane(copilotTrustOption))
	require.False(t, up, "the option label alone must not gate either")
	_, up = copilot.GateUp(boxedPane(copilotTrustHeadline, copilotTrustOption))
	require.True(t, up, "and the pair must, or this test is measuring the wrong thing")
}

func TestCopilotApprovalNeedsBothLiterals(t *testing.T) {
	_, ok := copilot.DetectPrompt(boxedPane(copilotApprovalHeadline))
	require.False(t, ok, "the headline alone must not read as the approval dialog")
	_, ok = copilot.DetectPrompt(boxedPane(copilotApprovalOption))
	require.False(t, ok, "the option label alone must not either")
	_, ok = copilot.DetectPrompt(boxedPane(copilotApprovalHeadline, copilotApprovalOption))
	require.True(t, ok, "and the pair must")
}

// boxedPane renders lines inside a round box that ends the pane — the minimum shape
// bottomBoxBlock anchors on. Each line gets its own row, so a caller passing two literals is
// testing a conjunction across rows, which is what flattenBottomBox exists to rejoin.
func boxedPane(lines ...string) string {
	width := 0
	for _, l := range lines {
		if len(l) > width {
			width = len(l)
		}
	}
	out := []string{" transcript row above the box", "╭" + strings.Repeat("─", width+2) + "╮"}
	for _, l := range lines {
		out = append(out, "│ "+l+strings.Repeat(" ", width-len(l))+" │")
	}
	return strings.Join(append(out, "╰"+strings.Repeat("─", width+2)+"╯"), "\n")
}

// TestCopilotComposerRejectsThePlainAngleBracket is why InputBoxPrompts is narrowed to "❯"
// rather than left nil. defaultPrompts also holds ">", which this CLI never opens a composer
// with, and inputBoxText anchors on the BOTTOM-MOST prompt-glyph line in its window — so under
// the default set a ">"-opening transcript row below the real composer becomes the composer.
// The pane here is that shape: copilot's own composer, with one quoted line under it.
func TestCopilotComposerRejectsThePlainAngleBracket(t *testing.T) {
	pane := strings.Join([]string{
		"────────────────────────",
		"❯ the real composer",
		"────────────────────────",
		"> quoted output below it",
	}, "\n")

	_, seen := inputBoxText(pane, defaultPrompts)
	require.True(t, seen, "the premise: the default set reads the quoted row as a composer")
	text, _ := inputBoxText(pane, defaultPrompts)
	require.Equal(t, "quoted output below it", text,
		"and it reads the wrong line, which is the fail-open this narrowing closes")

	text, seen = copilot.InputBoxText(pane)
	require.True(t, seen)
	require.Equal(t, "the real composer", text,
		"narrowed to copilot's own glyph, the anchor lands on the composer")
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
	{name: "copilotApprovalW20Pane", width: 20, note: "option label in five lines", pane: copilotApprovalW20Pane},
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
//
// IT DOES NOT TEST THE CONJUNCTION, and it once read as though it did. The two dialogs differ
// in BOTH of each matcher's literals, so this reaches the same verdict whether the predicate
// requires one literal or two — changing "&&" to "||" leaves it green. What tells an AND from
// an OR is a pane rendering exactly one literal, which no driven capture does:
// TestCopilotTrustGateNeedsBothLiterals and TestCopilotApprovalNeedsBothLiterals build them.
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

// copilotBusyLadder is a live turn at every driven width. The marker sits in the status row
// that REPLACES the hint row below the composer, so MarkerWindow stays 0 and footerRegion's
// below-the-box anchor finds it — claude's arrangement, not codex's or gemini's, both of which
// render their status row ABOVE the composer and need a window instead.
//
// EIGHT RUNGS, ALL POSITIVE. An earlier draft split the two narrowest off into a separate
// "truncated" list on the belief that no substring survived them. It does: the footer splits
// the WORD, not the row, and "Worki" is on screen at every one of the eight.
// TestCopilotBusyMarkerIsTheLongestSurvivingPrefix reads the prefix ladder off these panes.
//
// WHY THIS LADDER WAS DRIVEN TWICE. The first sweep ended its turn after the width-60 rung, so
// six of its eight rungs captured an IDLE pane while looking like a measurement — an identical
// credit figure across all six and the hint row where the status row belongs. A ladder is only
// valid for a transient state if the state outlives the sweep. The invalid captures are kept
// beside the run directory under captures-invalid-working-2026-08-26 rather than deleted,
// because the design spec cites them as the evidence that they were invalid.
//
// WHAT MAKES THIS SWEEP VALID is the BYTE COUNTER: a paused turn can leave a painted status row
// behind, but it cannot advance a counter. Marker presence cannot do that job — the first sweep
// would have partly satisfied it too. So the counter is not described here, it is a table:
// copilotBusyCounters carries the eight readings and TestCopilotBusyLadderCounterGrows holds
// them to the panes and to each other. That test is the validity check the first sweep lacked,
// and a re-drive that again caught an idle pane at some rung reddens it.
var copilotBusyLadder = []paneCapture{
	{name: "copilotWorkingW20Pane", width: 20, note: "\"Working\" splits into \"Worki\" / \"ng\"; the marker's floor is read off this rung", pane: copilotWorkingW20Pane},
	{name: "copilotWorkingW24Pane", width: 24, note: "\"Working\" splits into \"Workin\" / \"g\"", pane: copilotWorkingW24Pane},
	{name: "copilotWorkingW26Pane", width: 26, note: "the Working cell wraps whole to the second row, jammed against \"KiB\"", pane: copilotWorkingW26Pane},
	{name: "copilotWorkingW28Pane", width: 28, note: "same shape as 26, one column wider", pane: copilotWorkingW28Pane},
	{name: "copilotWorkingW34Pane", width: 34, note: "first rung the row wraps at all: \"interrupt\" breaks into its column, and \"Working·\" loses the space before the separator", pane: copilotWorkingW34Pane},
	{name: "copilotWorkingW40Pane", width: 40, note: "narrowest rung the whole row is still one line", pane: copilotWorkingW40Pane},
	{name: "copilotWorkingW60Pane", width: 60, note: "", pane: copilotWorkingW60Pane},
	{name: "copilotWorkingW120Pane", width: 120, note: "", pane: copilotWorkingW120Pane},
}

// copilotBusySplitWordRungs are the two rungs where the footer's column grid splits "Working"
// mid-word. They are not misses — the marker is found at both, which is the point of keying on
// the surviving prefix — they are where the prefix's floor comes from.
var copilotBusySplitWordRungs = []paneCapture{
	{name: "copilotWorkingW24Pane", width: 24, note: "\"Workin\" / \"g\"", pane: copilotWorkingW24Pane},
	{name: "copilotWorkingW20Pane", width: 20, note: "\"Worki\" / \"ng\"", pane: copilotWorkingW20Pane},
}

// copilotBusyCounters is the byte counter this ladder's validity rests on, as a table so it can
// be checked instead of read. Order is the order the sweep drove — widest pane first — which is
// the order the counter must grow in; digits are what the pane renders beside the separator,
// and bytes is that reading normalized so the growth is computable rather than eyeballed.
//
// It exists because the invalid first sweep was caught by a human noticing an identical credit
// figure across six rungs. That is not a check, it is a coincidence of attention, and the
// remedy for it is a value a test can read.
var copilotBusyCounters = []struct {
	name   string
	digits string
	bytes  float64
}{
	{"copilotWorkingW120Pane", "544", 544},
	{"copilotWorkingW60Pane", "1.0", 1.0 * 1024},
	{"copilotWorkingW40Pane", "1.5", 1.5 * 1024},
	{"copilotWorkingW34Pane", "1.9", 1.9 * 1024},
	{"copilotWorkingW28Pane", "2.4", 2.4 * 1024},
	{"copilotWorkingW26Pane", "2.9", 2.9 * 1024},
	{"copilotWorkingW24Pane", "3.4", 3.4 * 1024},
	{"copilotWorkingW20Pane", "3.8", 3.8 * 1024},
}

// TestCopilotBusyLadderCounterGrows is the ladder's validity check: every rung shows the
// counter reading recorded for it, and the readings grow strictly across the sweep. A rung
// re-captured from a paused turn repeats its neighbour's figure and reddens the second half; a
// rung whose recorded figure was mistyped reddens the first.
//
// It also holds the table to the ladder in both directions, because a counter table that has
// silently stopped covering a rung is the same defect one step removed.
func TestCopilotBusyLadderCounterGrows(t *testing.T) {
	byName := map[string]paneCapture{}
	for _, c := range copilotBusyLadder {
		byName[c.name] = c
	}
	require.Len(t, copilotBusyCounters, len(copilotBusyLadder),
		"every rung's counter is what makes that rung evidence, so the table covers the ladder")

	for i, cnt := range copilotBusyCounters {
		c, ok := byName[cnt.name]
		require.Truef(t, ok, "%s is not a rung of copilotBusyLadder", cnt.name)
		require.Containsf(t, footerRegion(c.pane), "· "+cnt.digits,
			"%s: the counter reading recorded for this rung is not the one the pane renders", c.label())
		if i > 0 {
			require.Greaterf(t, cnt.bytes, copilotBusyCounters[i-1].bytes,
				"the sweep drove widest-first, so the turn's byte counter must have advanced "+
					"between %s and %s — equal or falling figures are what an idle pane produces",
				copilotBusyCounters[i-1].name, cnt.name)
		}
	}
}

// TestCopilotBusyMarkerFiresAtEveryDrivenWidth is the positive half. paneCoverage asserts the
// same thing generically; this exists so a failure names the rung in this file's own terms.
func TestCopilotBusyMarkerFiresAtEveryDrivenWidth(t *testing.T) {
	for _, c := range copilotBusyLadder {
		t.Run(c.label(), func(t *testing.T) {
			require.True(t, copilot.HasBusyMarker(c.pane))
		})
	}
}

// TestCopilotBusyMarkerIsTheLongestSurvivingPrefix is why BusyMarkers holds "Worki" and not the
// whole word, and it is the test that replaced a claim its own fixtures disproved: the earlier
// entry keyed on "Working", called the two narrowest rungs unreachable, and said no substring
// survived them. "Worki" survives all eight.
//
// Both halves are needed and they pull in opposite directions. The first says the marker Atrium
// ships is on screen at every rung — that is the floor holding. The second says one character
// MORE is not, at the narrowest rung, which is what makes this prefix the longest one available
// rather than an arbitrarily short string that happens to work: shortening a marker widens what
// it can false-match, so the shortest sufficient form is not automatically the right one, and
// the case for stopping here is that stopping one character later misses.
//
// The stakes are why the miss mattered. A non-empty BusyMarkers is what disables the
// content-change fallback (session/tmux/poll.go), and copilot has no hook record, so a marker
// that misses is not a stale Working that decays — the session never reports working at all.
func TestCopilotBusyMarkerIsTheLongestSurvivingPrefix(t *testing.T) {
	require.Equal(t, []string{"Worki"}, copilot.BusyMarkers,
		"this test measures the marker copilot actually ships; if that changed, so must this")

	for _, c := range copilotBusyLadder {
		t.Run(c.label(), func(t *testing.T) {
			require.Contains(t, footerRegion(c.pane), "Worki",
				"the shipped marker must be in the marker region at every driven rung")
			require.True(t, copilot.HasBusyMarker(c.pane))
		})
	}

	// Distinctness and membership first. This list is the negative evidence, and it is the
	// shape that goes vacuous quietly: pointing its width-20 entry at the width-24 pane would
	// leave both assertions below green while the floor rested on one rung twice.
	requireDistinctCaptures(t, "copilotBusySplitWordRungs", copilotBusySplitWordRungs)
	ladderPane := map[string]string{}
	for _, c := range copilotBusyLadder {
		ladderPane[c.name] = c.pane
	}
	for _, c := range copilotBusySplitWordRungs {
		require.Equalf(t, ladderPane[c.name], c.pane,
			"%s must carry the same pane the ladder does, not a second copy of another rung", c.name)
	}

	for _, c := range copilotBusySplitWordRungs {
		t.Run(c.label(), func(t *testing.T) {
			require.NotContains(t, footerRegion(c.pane), "Working",
				"the premise: the column grid splits the word here, which is why the whole "+
					"word cannot be the marker")
		})
	}

	require.NotContains(t, footerRegion(copilotWorkingW20Pane), "Workin",
		"one character past the shipped marker misses at the narrowest rung, which is what "+
			"makes \"Worki\" the longest prefix this ladder supports rather than merely a short one")
}

// TestCopilotBusyMarkerCannotKeyOnTheInterruptHint is why the marker is a word from the status
// row's HEAD rather than its tail. The row reads "<spinner> Working · <N> B esc interrupt", so
// the byte counter sits BETWEEN the two words and "Working esc interrupt" is never contiguous at
// any width — a fact a wide capture alone would suggest is fine. And "esc interrupt" stops being
// contiguous below 40, which is where the row first wraps at all; what each rung's footer
// actually looks like is recorded per rung in copilotBusyLadder's notes rather than narrated as
// one onset here, because the row loses its cells to the wrap in stages and a single sentence
// about "where it goes multi-column" has been wrong twice.
func TestCopilotBusyMarkerCannotKeyOnTheInterruptHint(t *testing.T) {
	for _, c := range copilotBusyLadder {
		t.Run(c.label(), func(t *testing.T) {
			region := footerRegion(c.pane)
			require.NotContains(t, region, "Working esc interrupt",
				"the byte counter sits between the words at every width")
			if c.width >= 40 {
				require.Contains(t, region, "esc interrupt",
					"the hint is contiguous while the row is still one line")
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
// footer, where both dialog matchers must stay silent.
//
// WHAT MAKES THEM SILENT IS THE ANCHOR, NOT THE LITERALS, and getting that backwards leads to
// a mutation that cannot fail. flattenBottomBox is false on all eight of these panes — the
// composer is delimited by horizontal rules, so nothing here presents a bottom border above
// walled rows — and both matchers return early on that. Setting copilotTrustHeadline to
// "Worki" therefore leaves this green: the gate never reaches its literals at all. The first
// assertion pins the anchor for that reason, so the mechanism is what is guarded rather than a
// coincidence of which strings the footer happens not to contain.
func TestCopilotBusyPanesAreNeitherGateNorPrompt(t *testing.T) {
	for _, c := range copilotBusyLadder {
		t.Run(c.label(), func(t *testing.T) {
			_, boxed := flattenBottomBox(c.pane)
			require.False(t, boxed,
				"the mechanism: a live turn presents no anchored box, so both dialog matchers "+
					"return before they ever look at a literal")

			_, up := copilot.GateUp(c.pane)
			require.False(t, up, "a live turn is not a startup gate")
			_, ok := copilot.DetectPrompt(c.pane)
			require.False(t, ok, "a live turn is not a blocking prompt")
		})
	}
}

// TestCopilotBusyPanesStayDeliverable is ModalVeto's other direction, and the one that would
// break prompt delivery outright if the veto were wrong. copilotModalUp reads no literal, so
// nothing about WHICH screen is up constrains it — the only thing keeping it off a live
// composer is that copilot's composer is borderless. If a future build boxed the composer, this
// reddens, and it reddens before a user finds out by having a queued prompt silently held.
func TestCopilotBusyPanesStayDeliverable(t *testing.T) {
	for _, c := range copilotBusyLadder {
		t.Run(c.label(), func(t *testing.T) {
			require.False(t, copilotModalUp(c.pane),
				"a live turn is not a modal, so the veto must not fire on it")
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
// distinguishes 0 from 8. The last assertion records that as a fact rather than leaving it as an
// unstated hole.
//
// What IS guardable is the PREMISE MarkerWindow 0 rests on — that copilot paints its status row
// BELOW the composer, claude's arrangement, where codex and gemini paint theirs ABOVE it. Both
// halves are asserted, and the negative half is deliberately BOUNDED to the rows immediately
// above the composer's top rule, which is where the arrangement being ruled out puts the row.
// An earlier draft asked aboveBoxBlock instead: that walk is delimited by a blank line, copilot
// draws a scrollbar rune in the last column of every transcript row, and so it returned 31 to 34
// lines of scrolled-back transcript on these panes. Against that region the assertion said only
// "this fixture's transcript does not happen to contain the marker" — it would redden on a
// session that narrated "Working on the parser" and stay green on the build it was written to
// catch.
func TestCopilotBusyMarkerSitsBelowTheComposer(t *testing.T) {
	const marker = "Worki" // copilot's only BusyMarkers entry

	for _, c := range copilotBusyLadder {
		t.Run(c.label(), func(t *testing.T) {
			below, ok := footerBelowBox(c.pane)
			require.True(t, ok, "the composer's bottom rule is the anchor footerRegion needs")
			require.Contains(t, below, marker,
				"the status row REPLACES the hint row below the composer, which is what makes "+
					"MarkerWindow 0 the right value")

			require.NotContains(t, composerHeadroom(t, c.pane), marker,
				"and it is not codex's or gemini's arrangement — their status row sits here, "+
					"in the rows just above the composer, which is why they need a window")

			require.Contains(t, liveChromeLines(c.pane, 8), marker,
				"disclosed, not asserted as a virtue: codex's window would ALSO find the "+
					"marker here, so this ladder cannot falsify MarkerWindow 8. What rules it "+
					"out is the arrangement above, not a failing rung")
		})
	}
}

// composerHeadroom returns the few pane rows immediately ABOVE the composer's top rule — the
// band codex and gemini paint their status row into. Bounded on purpose: the question is where
// THIS agent puts its status row, and any wider region answers a question about the transcript
// instead. It fails the test rather than returning "" when the composer's rules are not where
// this adapter's shape says they are, so a restructured pane cannot make the negative vacuous.
func composerHeadroom(t *testing.T, pane string) string {
	t.Helper()
	const headroom = 3
	lines := strings.Split(pane, "\n")
	first := -1
	for i, l := range lines {
		if isHorizontalRule(l) {
			first = i
			break
		}
	}
	require.GreaterOrEqual(t, first, headroom,
		"copilot's composer sits between two horizontal rules with a transcript above it; "+
			"no rule found with room above it means this pane is not that shape")
	return strings.Join(lines[first-headroom:first], "\n")
}

// TestCopilotPasteCollapsed pins the predicate against copilot's two placeholder shapes, both
// read off the vendor bundle at 1.0.80 rather than off a screenshot: the composer replaces a
// paste over the line threshold with "[Paste #N - L lines]", and one over the byte threshold is
// written to the workspace and replaced with "[Saved pasted content to workspace (<file>)
// id=N]". The vendor's own detector makes the " - L lines" clause optional, which is why the
// bare-index form is here as a case rather than as an oversight.
//
// The negative cases are the ones that matter for the delivery path. A chip means "the paste
// landed", so a predicate that fires on ordinary typed text would confirm a prompt that never
// arrived and submit an empty composer.
func TestCopilotPasteCollapsed(t *testing.T) {
	for _, box := range []string{
		"[Paste #1 - 29 lines]",
		"[Paste #12 - 1 line]",
		"[Paste #3]",
		"before [Paste #2 - 40 lines] after",
		"[Saved pasted content to workspace (paste-2.txt) id=2]",
	} {
		t.Run("collapsed: "+box, func(t *testing.T) {
			require.True(t, copilotPasteCollapsed(box))
		})
	}

	for _, box := range []string{
		"",
		"refactor the parser and add a regression test",
		"explain how bracketed paste works",
		"[Pasted text #1 +29 lines]", // claude's chip, not copilot's
		"[Paste #]",
		"[Saved pasted content to workspace () id=]",
	} {
		t.Run("plain: "+box, func(t *testing.T) {
			require.False(t, copilotPasteCollapsed(box))
		})
	}

	require.NotNil(t, Resolve("copilot").PasteCollapsed,
		"and the adapter must actually wire it, or every queued prompt over ten lines is "+
			"pasted and never submitted")
}
