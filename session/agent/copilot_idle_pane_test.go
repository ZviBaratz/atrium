package agent

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------------------------
// The IDLE composer, driven.
//
// Until this ladder existed there was no idle copilot pane anywhere in the tree: composerPanes
// entered the working W120 capture for KeyCopilot, and copilotModalUp's soundness argument —
// that copilot's composer is never a bottom-anchored box, so "a box ends the pane" can stand in
// for a dialog — rested on a sentence about a pane shape nobody had bytes for. The first sweep's
// idle captures were driven and then moved off-tree as invalid, so the evidence was gone rather
// than absent.
//
// Driven here on copilot 1.0.80 against a throwaway git repo, one capture per width, 40 rows
// each. `scripts/drive-agent.sh` still has no copilot support, so these came from a tmux session
// on a private socket rather than from the harness.
//
// What they establish, and what each is used for:
//
//   - the composer is BORDERLESS at every width: a bare "❯" between two full-width horizontal
//     rules, with the multi-column footer below. That is what makes copilotModalUp sound
//     (TestCopilotIdleComposerIsNotABox) and it is the one claim the busy ladder could not make.
//   - there is no busy marker on an idle pane (TestCopilotIdlePanesAreNotWorking) — the negative
//     direction, which every previous copilot HasBusyMarker assertion left unpinned.
//   - delivery is live on it (TestCopilotIdlePanesStayDeliverable).
//
// The footer wraps into more rows as the pane narrows (one line at 60 and above, four at 20), and
// the header row above the top rule wraps at 40 and below. Both are why this is a ladder and not
// one capture.

const copilotIdleW20Pane = `  Current  →


╭─╮╭─╮
        Copilot v1
╰─╯╰─╯  uses AI.
   ▝ █  Check for
        mistakes.

▔▔▔▔

 ● No copilot-inst
   ructions.md
   found. Run
   /init to
   generate.

 ● Tip: /allow-all
   └ Enable all
     permissions
     (tools,
   paths,
     and URLs)




 ~/.cache/.../wt
 [⎇ main]
 Session: 0 AIC
 used
────────────────────
❯
────────────────────
 ← open  ·/
 sidebar  commands
          · ? help
          · tab
          next tab
 Auto`

const copilotIdleW24Pane = `  Current   Sessions  →


╭─╮╭─╮
         Copilot v1.0.
╰─╯╰─╯   AI.
   ▘▝ █  Check for mis

   ▔▔▔▔

 ● No copilot-instruct
   ions.md found. Run
   /init to generate.

 ● Tip: /allow-all
   └ Enable all
     permissions
     (tools, paths,
   and
     URLs)









 ~/.cache/cpd.q86O/wt
 [⎇ main]
 Session: 0 AIC used
────────────────────────
❯
────────────────────────
 ← open  · / commands ·
 sidebar    ? help ·
           tab next tab

 Auto`

const copilotIdleW28Pane = `  Current   Sessions  →

  ╭─╮╭─╮
  ╰─╯╰─╯  Copilot v1.0.80
  █ ▘▝ █  Check for mistak
   ▔▔▔▔

 ● No
   copilot-instructions.md
   found. Run /init to
   generate.

 ● Tip: /allow-all
   └ Enable all
   permissions
     (tools, paths, and
     URLs)













 ~/.cache/cpd.q86O/wt
 [⎇ main]
 Session: 0 AIC used
────────────────────────────
❯
────────────────────────────
 ← open   ·/ commands · ?
 sidebar   help · tab next
           tab
 Auto`

const copilotIdleW40Pane = `  Current   Sessions   Issues  →

  ╭─╮╭─╮
  ╰─╯╰─╯  Copilot v1.0.80 uses AI.
  █ ▘▝ █  Check for mistakes.
   ▔▔▔▔

 ● No copilot-instructions.md found.
   Run /init to generate.

 ● Tip: /allow-all
   └ Enable all permissions (tools,
     paths, and URLs)



















 ~/.cache/cpd.q86O/wt
 [⎇ main]           Session: 0 AIC used
────────────────────────────────────────
❯
────────────────────────────────────────
 ← open     · / commands · ? help · tab
 sidebar       next tab
 Auto`

const copilotIdleW60Pane = `  Current   Sessions   Issues   Pull requests   Gists

  ╭─╮╭─╮
  ╰─╯╰─╯  Copilot v1.0.80 uses AI.
  █ ▘▝ █  Check for mistakes.
   ▔▔▔▔

 ● No copilot-instructions.md found. Run /init to
   generate.

 ● Tip: /allow-all
   └ Enable all permissions (tools, paths, and URLs)























 ~/.cache/cpd.q86O/wt [⎇ main]          Session: 0 AIC used
────────────────────────────────────────────────────────────
❯
────────────────────────────────────────────────────────────
 ← open sidebar · / commands · ? help · tab next tab   Auto`

const copilotIdleW80Pane = `  Current   Sessions   Issues   Pull requests   Gists

  ╭─╮╭─╮
  ╰─╯╰─╯  Copilot v1.0.80 uses AI.
  █ ▘▝ █  Check for mistakes.
   ▔▔▔▔

 ● No copilot-instructions.md found. Run /init to generate.

 ● Tip: /allow-all
   └ Enable all permissions (tools, paths, and URLs)
























 ~/.cache/cpd.q86O/wt [⎇ main]                              Session: 0 AIC used
────────────────────────────────────────────────────────────────────────────────
❯
────────────────────────────────────────────────────────────────────────────────
 ← open sidebar · / commands · ? help · tab next tab                       Auto`

const copilotIdleW120Pane = `  Current   Sessions   Issues   Pull requests   Gists

  ╭─╮╭─╮
  ╰─╯╰─╯  Copilot v1.0.80 uses AI.
  █ ▘▝ █  Check for mistakes.
   ▔▔▔▔

 ● No copilot-instructions.md found. Run /init to generate.

 ● Tip: /allow-all
   └ Enable all permissions (tools, paths, and URLs)
























 ~/.cache/cpd.q86O/wt [⎇ main]                                                                      Session: 0 AIC used
────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────
❯
────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────
 ← open sidebar · / commands · ? help · tab next tab                                                               Auto`

// copilotIdleLadder is the driven idle ladder, widest first (the order copilotBusyLadder uses).
var copilotIdleLadder = []paneCapture{
	{name: "copilotIdleW120Pane", width: 120, note: "as driven", pane: copilotIdleW120Pane},
	{name: "copilotIdleW80Pane", width: 80, note: "as driven", pane: copilotIdleW80Pane},
	{name: "copilotIdleW60Pane", width: 60, note: "the widest rung whose footer still wraps to one line", pane: copilotIdleW60Pane},
	{name: "copilotIdleW40Pane", width: 40, note: "header row wraps above the top rule", pane: copilotIdleW40Pane},
	{name: "copilotIdleW28Pane", width: 28, note: "footer wraps to three rows", pane: copilotIdleW28Pane},
	{name: "copilotIdleW24Pane", width: 24, note: "footer wraps to three rows", pane: copilotIdleW24Pane},
	{name: "copilotIdleW20Pane", width: 20, note: "footer wraps to four rows; header row gone", pane: copilotIdleW20Pane},
}

// TestCopilotIdleComposerIsNotABox is the premise copilotModalUp rests on.
//
// The veto is "a bottom-anchored box all but ends the pane" — deliberately structural, so it can
// hold at a height where no dialog literal is left to read. That only discriminates if copilot's
// own composer is never such a box, and until this ladder was driven the argument for that cited
// a busy-only ladder. If it were false the veto would fire on an idle pane, InputBoxVisible and
// AwaitingInput would go permanently false, and prompt delivery for the whole adapter would be
// dead with the suite green — #510 exactly.
func TestCopilotIdleComposerIsNotABox(t *testing.T) {
	for _, c := range copilotIdleLadder {
		t.Run(c.label(), func(t *testing.T) {
			require.Falsef(t, copilotModalUp(c.pane),
				"%s: the veto fires on an IDLE composer, so it would hold prompt delivery "+
					"forever instead of only on a dialog", c.name)
			_, boxed := flattenBottomBox(c.pane)
			require.Falsef(t, boxed,
				"%s: copilot's composer must stay borderless — two horizontal rules around a "+
					"bare glyph, never a bottom-anchored box", c.name)
		})
	}
}

// TestCopilotIdlePanesAreNotWorking pins the direction every previous copilot busy-marker
// assertion left open: they are all require.True on a busy pane, so a marker that latched on an
// idle footer had no guard. claude, codex and gemini each pin an idle False; this is copilot's.
//
// "Worki" is a 5-character prefix and the footer it is confined to is multi-column, so the
// failure to rule out is a fragment of some other footer word matching it. A latched marker
// never self-heals: hasMarker makes the content-change fallback unreachable, so the session
// would read Working through every idle moment and the row would never settle.
func TestCopilotIdlePanesAreNotWorking(t *testing.T) {
	for _, c := range copilotIdleLadder {
		t.Run(c.label(), func(t *testing.T) {
			require.Falsef(t, copilot.HasBusyMarker(c.pane),
				"%s: an idle pane must carry no busy marker", c.name)
		})
	}
}

// TestCopilotIdlePanesStayDeliverable is the other direction of the veto: a queued prompt must
// actually be delivered on the pane the user is looking at most of the time.
func TestCopilotIdlePanesStayDeliverable(t *testing.T) {
	for _, c := range copilotIdleLadder {
		t.Run(c.label(), func(t *testing.T) {
			_, gated := copilot.GateUp(c.pane)
			require.Falsef(t, gated, "%s: no gate on an idle composer", c.name)
			_, prompted := copilot.DetectPrompt(c.pane)
			require.Falsef(t, prompted, "%s: no blocking prompt on an idle composer", c.name)
			require.Truef(t, copilot.InputBoxVisible(c.pane),
				"%s: the composer must be visible, or AwaitingInput is false and the queued "+
					"prompt is held", c.name)
			text, ok := copilot.InputBoxText(c.pane)
			require.Truef(t, ok, "%s: and it must hand back a readback for boxHoldsPrompt", c.name)
			require.Emptyf(t, strings.TrimSpace(text),
				"%s: an idle composer reads back empty, which is what makes a first-line "+
					"signature match mean the prompt landed", c.name)
		})
	}
}
