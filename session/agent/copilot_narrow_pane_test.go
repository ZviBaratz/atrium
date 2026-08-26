package agent

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------------------------
// BELOW 20: the band the busy floor used to be asserted about.
//
// paneCoverage claimed "the floor is 20 — the narrowest pane Atrium's own layout can produce is
// covered, so this adapter's busy marker has no unmeasured band". That contradicted this file's
// own header and registry.go's agy entry, which both record that the preview pane's width is NOT
// clamped to any minimum: the list may take maxListRatio = 0.60 of the terminal and nothing
// floors the remainder. 20 was where driving stopped, not a floor, and the direction below it is
// the fail-dangerous one — a marker that stops matching makes hasMarker true with markerWorking
// false, which makes the content-change fallback unreachable, so the session reads Ready through
// a live turn.
//
// So it was driven instead of argued. Four more rungs on copilot 1.0.80, and "Worki" survives on
// a single row at every one of them. w12 is the interesting rung: the footer renders " Worki inte"
// with "ng" on the row below, so the marker's five characters are EXACTLY what fits — the prefix
// was not merely short enough, it is the longest one that survives here.
//
// TestCopilotBusyMarkerIsTheLongestSurvivingPrefix reads these along with the wider ladder.

const copilotWorkingW18Pane = `  Current  →

   readme file   ┃
   containing a  ┃
   single line   ┃
   of text. It   ┃
   currently     ┃
   just says     ┃
   "hello" and   ┃
   serves as the ┃
   primary       ┃
   documentation ┃
   file for      ┃
   this          ┃
   repository.   ┃
                 ┃
 ❯ list    01:31 ┃
   every         ┃
   file in       ┃
   this          ┃
   repo          ┃
   and des       ┃
   cribe         ┃
   each          ┃
   one in        ┃
   two sen       ┃
   tences        ┃
                 ┃
 ~/.cache/.../wt
 [⎇ main]
 Session: 3.05
 AIC used
──────────────────
❯
──────────────────
 ◎      esc
 Workin interrup
 g      t
 Auto → claude-
 haiku-4.5`

const copilotWorkingW16Pane = `  Current  →

 ❯ Thought for ┃
   1s          ┃
               ┃
 $ Shell  2 li ┃
   wc -l READ… ┃
               ┃
 ❯ Thinking…   ┃
               ┃
 ● README.md   ┃
   has 1 line. ┃
               ┃
 ❯ list  01:31 ┃
   every       ┃
   file        ┃
   in          ┃
   this        ┃
   repo        ┃
   and         ┃
   descr       ┃
   ibe         ┃
   each        ┃
   one         ┃
   in          ┃
   two         ┃
   sente       ┃
   nces        ┃
               ┃
 ~/.../wt
 [⎇ main]
 Session: 2.17
 AIC used
────────────────
❯
────────────────
 ○     esc
 Workininterru
 Auto → claude-
 haiku-4.5`

const copilotWorkingW14Pane = `  Current  →

             ┃
   Use       ┃
   rebase    ┃
   for local ┃
   cleanup,  ┃
   merge     ┃
   for       ┃
   shared    ┃
   branches. ┃
             ┃
 ❯ exp 01:32 ┃
   lai       ┃
   n w       ┃
   hat       ┃
             ┃
   git       ┃
             ┃
   reb       ┃
   ase       ┃
   do        ┃
   es        ┃
   in        ┃
   det       ┃
   ail       ┃
             ┃
 ~/.../wt
 [⎇ main]
 Session:
 3.98 AIC
 used
──────────────
❯
──────────────
 ◎      esc
 Workin inter
 g      rupt
 Auto →
 claude-`

const copilotWorkingW12Pane = `  Current  →

           ┃
   r       ┃
   e       ┃
   b       ┃
   a       ┃
   s       ┃
   e       ┃
           ┃
   d       ┃
   o       ┃
   e       ┃
   s       ┃
           ┃
   i       ┃
   n       ┃
           ┃
   d       ┃
   e       ┃
   t       ┃
   a       ┃
   i       ┃
   l       ┃
           ┃
 ~/.../wt
 [⎇ main]
 Session:
 3.55 AIC
 used
────────────
❯
────────────
 ○     esc
 Worki inte
 ng    rrup
       t
 Auto →
 claude-
 haiku-4.5`

// copilotBusyNarrowLadder is the sub-20 band, widest first.
var copilotBusyNarrowLadder = []paneCapture{
	{name: "copilotWorkingW18Pane", width: 18, note: "\"Working\" splits after \"Workin\"; \"g\" on the next row", pane: copilotWorkingW18Pane},
	{name: "copilotWorkingW16Pane", width: 16, note: "the two footer columns merge into \"Workininterru\"", pane: copilotWorkingW16Pane},
	{name: "copilotWorkingW14Pane", width: 14, note: "\"Workin\" then \"g\"", pane: copilotWorkingW14Pane},
	{name: "copilotWorkingW12Pane", width: 12, note: "\"Worki\" exactly, then \"ng\" — the marker's own length", pane: copilotWorkingW12Pane},
}

// TestCopilotBusyMarkerSurvivesBelowTwenty is the guard for the band above.
//
// Both directions, because each is a different defect. A marker that stops MATCHING makes
// hasMarker true and markerWorking false, which makes the content-change fallback unreachable
// (poll.go), so the row reads Ready through a live turn, the completion notification fires
// early, and promptDeliveryReady hands over a queued prompt mid-turn. A marker that matched the
// pane's own transcript instead of its footer would latch the other way and never settle.
func TestCopilotBusyMarkerSurvivesBelowTwenty(t *testing.T) {
	require.NotEmpty(t, copilotBusyNarrowLadder, "the ladder must not be empty, or this asserts nothing")
	for _, c := range copilotBusyNarrowLadder {
		t.Run(c.label(), func(t *testing.T) {
			require.Truef(t, copilot.HasBusyMarker(c.pane),
				"%s: the busy marker must still fire, or a live turn reads Ready", c.name)
			require.Falsef(t, copilot.InputBoxVisible(c.pane) && copilotModalUp(c.pane),
				"%s: a busy pane is not a modal", c.name)
		})
	}
}

// TestCopilotBusyMarkerIsExactlyWhatFitsAtTwelve pins why the marker is five characters and not
// six. At w12 copilot's footer splits "Working" as "Worki" / "ng", so "Workin" — the prefix the
// wider rungs would happily have accepted — is the first length that fails here.
//
// Asserted on the shipped constant rather than on a repeated literal, so lengthening
// BusyMarkers reddens this rather than leaving a comment behind that says it cannot be.
func TestCopilotBusyMarkerIsExactlyWhatFitsAtTwelve(t *testing.T) {
	pane := copilotWorkingW12Pane
	markers := Resolve("copilot").BusyMarkers
	require.Len(t, markers, 1, "copilot declares one busy marker")

	require.True(t, copilot.HasBusyMarker(pane),
		"the shipped marker must fire at the narrowest driven rung")
	require.Contains(t, pane, markers[0]+" ",
		"and it is the footer's own fragment, not a longer word it happens to prefix")

	longer := markers[0] + "n"
	require.NotContains(t, pane, longer,
		"one more character than the shipped marker does not survive w12, which is what makes "+
			"five the answer rather than a guess")
}
