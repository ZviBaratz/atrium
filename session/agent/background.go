package agent

import "regexp"

// Background-work detection for claude (2.1.210).
//
// A turn can END while work it started is still running. Claude's Stop hook fires at the
// turn boundary regardless, and a background Bash(run_in_background) or Monitor is NOT a
// sub-agent, so neither enters the in-flight set that #290 keys Pending off. The poller
// therefore read ready+empty as a finished turn and the row went green while the work ran.
//
// The pane says otherwise, in the footer's mode line below the input box:
//
//	⏵⏵ auto mode on · 2 shells · ← for agents · ↓ to manage
//
// spinner.go names these chips too, but for the opposite purpose: #332 proved they never
// DISPLACE the interrupt marker. That they are not noise for that question is what makes
// them evidence for this one — the marker answers "is the turn running", these answer "did
// the turn leave something running". It is also why the marker still outranks them: the two
// render side by side, so a chip beside a live marker means a running turn, not a finished
// one.
//
// The shapes below are verbatim captures from a live pane driven with a real background
// Bash and a real Monitor (tmux capture-pane, 2026-08-12):
//
//	· 1 shell ·                  · 1 monitor ·
//	· 2 shells ·                 · 1 shell, 1 monitor ·
//	· 2 shells, 2 monitors ·
//
// The COMMA is the whole reason this is a regex and not two Contains calls. Both counts
// share a single "·"-delimited segment, so an alternation delimited only by "·" matches
// NEITHER half of "1 shell, 1 monitor": the shell run is closed by a comma, and the monitor
// run is opened by one. Treating "," as a delimiter in its own right is what covers the
// mixed case, which is also the most common one.
//
// Requiring a delimiter to CLOSE the run is what separates the chip from prose. Claude
// prints "● Running 1 shell command…" while a foreground Bash runs, and its transcript
// summaries read "Ran 3 shell commands" — same two words, and both would latch a session
// busy forever under a bare substring match. Neither closes on "·" or ",".
//
// DELIBERATELY UNMATCHED: the footer can chip other task kinds the same way — local agents,
// teams, background dynamic workflows, MCP tasks — and can fall back to a bare "N background
// task(s)". None is matched here, because shells and monitors are the scope this was built
// for and each extra literal is a false-positive surface that nothing in this file can
// currently evidence. A session running only one of those reads as it does today, which is
// the pre-existing behaviour rather than a new wrong one. Adding one is a decision that owes
// a capture, like every other literal in this package.
var claudeBackgroundChipRegex = regexp.MustCompile(`(?:^|[·,])\s*\d+ (?:shell|monitor)s?\s*(?:[·,]|$)`)

// claudeBackgroundWorkVisible backs the claude adapter's BackgroundWork: it reports whether
// the live footer carries a shell/monitor count chip.
//
// Anchored on footerBelowBox and FAILING CLOSED when there is no box border, which is the
// same region and the same gate claudePermissionMode uses (permissionmode.go) — the chip
// renders on that very line, and two readers of one physical line should not disagree about
// which region it lives in.
//
// The strictness is not stylistic. Every other signal the poller can latch on is bounded:
// the busy marker self-clears, LiveSpinner is gated on the pane animating, and PanePending
// has a wall-clock watchdog. This one has no watchdog by design (session/instance.go), so a
// false positive is a row that reads busy FOREVER. footerVisibleInSegments would cover a
// little more ground, but only via its no-rule fallback to a flat bottom-N window — the same
// "budget, not a liveness test" window that cost #342/#343 — and the forgeable text here is
// self-inflicted: these literals live in this file and its tests, so an Atrium session
// working on this feature quotes them on screen. Failing closed costs a MISS on a pane with
// no border, which is the behaviour that shipped before this existed; failing open would
// cost a stuck row. The same trade is already recorded for the mode chip, which goes
// indeterminate rather than guess.
func claudeBackgroundWorkVisible(content string) bool {
	footer, ok := footerBelowBox(content)
	if !ok {
		return false
	}
	// Flatten so a footer wrapped by a narrow pane still reads as one run of segments.
	return claudeBackgroundChipRegex.MatchString(whiteSpaceRegex.ReplaceAllString(footer, " "))
}
