package agent

import (
	"regexp"
	"strings"
)

// Live-spinner detection for claude (2.1.210).
//
// Claude's busy footer marker (the interrupt hint, matched on "to interrupt" since #354
// broadened it off the user-rebindable chord) is not guaranteed on a working pane.
// #308 attributed that to a responsive, prioritized hint area crowding the marker out
// when a session accrues contextual chips (a "PR #NNN" link, "ctrl+t to hide tasks",
// "N shell", "1 monitor", "↓ to manage"). The #332 sweep falsified that: the footer's
// hint list is built by plain concatenation with no width term and no priority — the
// interrupt hint and the chips render side by side — and a live 2.1.210 busy pane keeps
// "esc to interrupt" intact down to width 56. Chips never displace it.
//
// What actually drops the marker is that the footer lights the hint off the CLI's
// narrowest notion of busy: the bundle tracks isLoading / isExternalLoading /
// betweenCalls as separate states and only isLoading shows the hint. A turn can be
// underway with the marker absent — which is what #308's bug pane captured. The poller,
// which never trusts the bare "working" hook latch (the #46 stuck-file guard), then
// settles it to idle mid-work.
//
// (A narrow enough pane also truncates the whole footer line — a busy width-30 pane reads
// "⏸ manual mode on · esc to …" — but that is one composed line overflowing, not hint
// selection, and the spinner's signature sits at the head of its own line where truncation
// reaches last.)
//
// The complementary positive proof of work is the spinner STATUS LINE claude renders
// above the input box: "<glyph> <Gerund>… (<elapsed> · …)". The two signals cover
// different phases of a turn rather than the same one twice, which is why both are kept:
// the spinner is up while claude thinks and runs tools, and is *replaced* by the streamed
// reply once text starts arriving — a streaming pane carries the footer marker and no
// spinner at all (live 2.1.210). The glyph (✻ ✽ ✢ ✶ * ·) and gerund vary, so only the
// structure is matchable — and its discriminator, confirmed live against 2.1.210
// (2026-07-15, #332; first pinned at 2.1.207, 2026-07-13), is the elapsed timer sitting
// immediately after "(" and followed by a " · " middot:
//
//	✽ Opening PR and running CI… (14m 24s · ↓ 34.6k tokens)
//	✢ Moseying… (2s · ↓ 281 tokens · thinking with low effort)   ← 2.1.210 capture
//
// A settled turn instead shows a PAST-TENSE summary with no parens/middot ("✻ Worked for
// 20m 57s"); a bare tool duration has no middot ("done (2m 38s)"); a sub-agent completion
// has a middot but the duration is last, not right after "(" ("Done (15 tool uses · … ·
// 2m 20s)"). The gerund ellipsis is U+2026, which normal program output/prose does not
// emit before a timer (it uses ASCII "..."), so requiring it is a second FP guard on top
// of the middot. Detection is confined to aboveBoxBlock — the live status band directly
// above the box, blank-delimited from the transcript — because a pane can carry the same
// signature quoted in scrollback; a whole-pane match would pin such a session working
// forever (the #46 failure). Like every heuristic here it fails safe: a rewording upstream
// makes the pane read idle (the pre-fix behavior), never a wrong action, and the
// VerifiedVersion drift guard flags the next minor bump for re-verification.

// claudeSpinnerRegex matches the live spinner status line's signature: the gerund ellipsis
// (U+2026), then an in-paren elapsed timer whose seconds sit immediately after "(" and are
// followed by a " · " (U+00B7) middot. Explicit code points keep the pattern independent of
// the source file's byte encoding.
var claudeSpinnerRegex = regexp.MustCompile(`\x{2026}\s*\((?:\d+h )?(?:\d+m )?\d+s \x{00b7} `)

// claudeSpinnerWorking backs the claude adapter's LiveSpinner: it reports whether the live
// status block above the input box shows an animating spinner. Matching is per-line (never
// flattened) so a transcript line ending in "…" cannot combine with a different line's
// "(Ns · )" into a false signature.
func claudeSpinnerWorking(content string) bool {
	block, ok := aboveBoxBlock(content)
	if !ok {
		return false
	}
	for _, line := range strings.Split(block, "\n") {
		if claudeSpinnerRegex.MatchString(line) {
			return true
		}
	}
	return false
}
