package app

// The quiet-pane gate: the rule that decides a pane has stopped moving, so the
// capture chain can stop paying for it.
//
// An idle Atrium forked one `capture-pane` against the selected session every
// 100ms forever, whether or not that pane had changed a byte in eight hours.
// Measured on a live fleet, one capture costs ~3.0ms of client CPU plus ~0.5ms
// in the tmux server, so the chain alone was ~35ms/s — about a third of an idle
// Atrium's child CPU, and the largest single verb in the command log (#546).
//
// The rule is deliberately trivial, and the reasoning lives here rather than in
// the code: byte equality on the frame text itself. Not `stableStreak`, the
// tmux monitor's own change counter, for three reasons — it is computed from
// ANSI-stripped content while the preview renders `capture-pane -e` *with*
// colour, `PollNow` resets it on every detach, and it advances at 0.5Hz for a
// non-selected session. Not a hash either: the frames are a few KB and the
// comparison is against the string the preview is already holding.
//
// Byte equality is only a useful signal because it was measured to be one:
// five captures 100ms apart against three different idle agent panes returned a
// single distinct value each time. An idle pane does not flicker — capture-pane
// dumps cell contents, so there is no cursor blink and no SGR churn to defeat
// this. A pane that *does* flicker simply never settles, which costs nothing but
// the saving.

// frameQuietRuns is how many identical captures in a row mark a pane as no
// longer moving. Twenty at the 100ms cadence is two seconds, which is far longer
// than it needs to be on purpose: every agent that is actually working repaints
// well inside that window (a claude spinner advances every ~100ms), so the
// threshold only ever fires on a pane that has genuinely stopped. Erring long
// costs 20 captures once per settling pane; erring short would drop a working
// pane to the 2Hz fallback and make the preview visibly stutter.
//
// A var, not a const, so tests can shrink it.
var frameQuietRuns = 20

// quietRun is the run of consecutive byte-identical captures observed for one
// frame target. Main-thread only, like the rest of the capture chain's state.
type quietRun struct {
	target frameTarget
	text   string
	seen   int
}

// noteQuietFrame folds one observed frame into the run.
//
// A different target or a single differing byte restarts it at one rather than
// zeroing it: the frame just observed is itself the first of the new run, and
// counting it is what makes `seen` mean "identical captures seen", not
// "identical captures seen after the first".
func noteQuietFrame(prev quietRun, target frameTarget, text string) quietRun {
	if prev.target != target || prev.text != text {
		return quietRun{target: target, text: text, seen: 1}
	}
	prev.seen++
	return prev
}

// settled reports whether target has produced a long enough run to stop being
// captured at the frame cadence.
//
// The target check is not redundant with noteQuietFrame's. Both writers key the
// run on the frame's OWN target rather than on whatever resolveFrameTarget would
// answer now: handlePaneFrame notes the instance its capture was taken from,
// which may be one the user has already moved off, and the sweep's harvest notes
// the selected session whichever tab is showing. So a caller can ask about a
// target the run has never described, and answering from `seen` alone would gate
// a pane on another pane's stillness. noteFrameTargetChange's resets do not cover
// this: they fire on the deliberate re-points, not on a frame that lands late.
func (q quietRun) settled(target frameTarget, runs int) bool {
	return q.target == target && q.seen >= runs
}

// noteFrameSeen folds one observed frame into the quiet run.
//
// Both writers of the pane cache call it — the 100ms capture chain
// (handlePaneFrame) and the 500ms sweep's free harvest (applyHarvestedFrame) —
// because either one is evidence about whether the pane is moving, and once the
// gate closes the harvest is the ONLY evidence there is: no capture is running
// to notice that the pane started moving again. It is a helper rather than the
// assignment written twice because feeding it is exactly the half a new writer
// would forget, and a writer that stores a frame without noting it strands the
// gate closed until the next target change.
func (m *home) noteFrameSeen(target frameTarget, text string) {
	m.frameQuiet = noteQuietFrame(m.frameQuiet, target, text)
}
