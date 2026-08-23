package ui

import (
	"fmt"
	"strconv"

	"github.com/ZviBaratz/atrium/session/agent"
	"github.com/ZviBaratz/atrium/session/transcript"
	"github.com/ZviBaratz/atrium/ui/theme"
)

// The per-session transcript chip: how full the agent's context is (#596), or
// what the session has spent (#392), in one shared column.
//
// The OCCUPANCY modes render a percentage where the model's ceiling is known and
// a raw token count where it isn't. That split is the design's safety property
// rather than a convenience: because an unknown model falls back to a count, a
// window table that has gone stale degrades VISIBLY — "28%" becomes "283k" —
// instead of quietly reporting a confident wrong fraction. All three honour the
// fallback, `bar` included: a meter has no way to express an unknown ceiling.
//
// The COST mode is a different reading, so it does not share that fallback —
// there is no count to fall back TO, since an unpriceable request contributes no
// dollars rather than a wrong number of them. It keeps the same property by a
// different route: the total becomes a lower bound and the chip's "~" becomes a
// ">", so a stale price table is just as visible as a stale window table. See
// costChip.
//
// One column for both is a width decision, and the reason is in list_render.go:
// a ninth chip on line 1 would come straight out of the flex name segment, which
// has 21 cells on a fully loaded row. Sharing still costs the name two of them
// in cost mode — 5 cells for "~$4.1" where "28%" needs 3 — which is within the
// 5-cell ceiling the layout was sized against but is NOT free, so both budgets
// are measured and asserted rather than assumed equal.

// Context-chip modes. These mirror config.ContextIndicator* verbatim so the app
// can pass GetContextIndicator's normalized value straight through and ui needs
// no config import — the same arrangement as the model/effort/permission chips.
const (
	contextModeOff     = "off"
	contextModeCount   = "count"
	contextModePercent = "percent"
	contextModeBar     = "bar"
	contextModeCost    = "cost"
)

// Default attention thresholds for the chip's colour, as percentages of the
// window. Reachable whenever the ceiling is KNOWN, in every mode — a
// `count`-mode chip on a known model tints too, and should: "900k" is as urgent
// as "90%", and the mode chooses how the number reads, not whether the urgency
// is real. What stays dim unconditionally is a count from an UNKNOWN model,
// which has no ceiling to be near (see contextColor).
//
// Since #799 both are user-configurable (config.GetContextWarnPercent /
// GetContextDangerPercent, plumbed through InstanceRenderer). These stay as the
// fallback a zero band resolves to, so the numbers a caller gets when it
// configures nothing are stated in one place and can be asserted.
const (
	contextWarnPct   = 75
	contextDangerPct = 90
)

// contextBands resolves the configured warn/danger pair, substituting the
// package defaults for a zero (unset) band and holding warn at or below danger.
//
// The ordering rule is a second copy of the one in config.GetContextWarnPercent,
// on purpose rather than trusted: ui takes plain ints from whoever calls
// SetContextThresholds and has no way to know they came through that accessor, and
// an inverted pair paints Attention above the point that should read Danger — the
// one failure the renderer must not have. The [1,100] range clamp is NOT repeated
// here; a band outside it still paints a coherent ladder, so it stays the
// accessor's business alone.
func contextBands(warn, danger int) (int, int) {
	if danger <= 0 {
		danger = contextDangerPct
	}
	if warn <= 0 {
		warn = contextWarnPct
	}
	return min(warn, danger), danger
}

// contextChip returns the chip text for one session under mode, and whether
// there is anything to draw at all. It never returns a zero: absent is the
// answer for a session with no reading, so a non-Claude profile, an unparsable
// transcript, or a session that has not taken a turn simply carries no chip
// rather than a misleading "0%" or "$0.00".
//
// It takes both readings because the modes share one column. Only one of them is
// ever populated on a given tick — the poll layer reads whichever the mode calls
// for and clears the other — so the unused argument is the zero value, not a
// stale value being ignored.
//
// ramp supplies the one-cell meter for `bar` mode (theme.Glyphs.ContextRamp).
func contextChip(u transcript.Usage, c transcript.Cost, mode string, ramp []string) (string, bool) {
	if mode == contextModeOff {
		return "", false
	}
	if mode == contextModeCost {
		return costChip(c)
	}
	if u.ContextTokens <= 0 {
		return "", false
	}
	window, known := agent.ClaudeContextWindow(u.Model)
	// An unknown ceiling collapses every mode to a count — see the file comment.
	// The empty mode is treated as the default here for the same reason the
	// config accessor does it: the renderer's zero value must behave like the
	// documented default, not like a fifth silent mode.
	if !known || mode == contextModeCount {
		return humanizeTokens(u.ContextTokens), true
	}
	pct := contextPercent(u.ContextTokens, window)
	if mode == contextModeBar {
		// An empty ramp collapses to a count for the same reason an unknown
		// ceiling does: there is no meter to draw. Not reachable today — every
		// theme takes its Glyphs from plainGlyphs() and assertGlyphWidths pins the
		// length at 8 across every palette — but contextLevel already returns 0
		// for a zero-length ramp, so without this the "safe" guard hands an index
		// straight to a panic. A guard that implies safety it does not provide is
		// worse than no guard.
		if len(ramp) == 0 {
			return humanizeTokens(u.ContextTokens), true
		}
		return ramp[contextLevel(pct, len(ramp))], true
	}
	return fmt.Sprintf("%d%%", pct), true
}

// contextPercent converts a reading to a whole percentage of the window,
// clamped to [0,100]. The clamp is not defensive padding: the corpus's peak
// reading was 99.93% of the declared window, so a model that overshoots its
// published ceiling by a hair is a live possibility, and "103%" would read as a
// bug in Atrium rather than as a full context.
func contextPercent(tokens, window int) int {
	if window <= 0 {
		return 0
	}
	pct := tokens * 100 / window
	if pct < 0 {
		return 0
	}
	if pct > 100 {
		return 100
	}
	return pct
}

// contextLevel maps a percentage to a ramp index in [0,rungs). A nonzero
// reading below one rung's worth still lands on rung 0 rather than on nothing —
// the chip's presence already says "there is context here", and the lowest rung
// is what says "barely any".
func contextLevel(pct, rungs int) int {
	if rungs <= 0 {
		return 0
	}
	idx := pct * rungs / 100
	if idx >= rungs {
		idx = rungs - 1
	}
	if idx < 0 {
		idx = 0
	}
	return idx
}

// contextColor returns the chip's colour. Dim until the window is three
// quarters gone, Attention past that, Danger near the ceiling — so "which
// session is about to compact?" is answerable by scanning rather than reading
// every row. The number carries the same signal, so colour is reinforcement and
// never the only channel.
//
// A count (unknown model) always stays dim: without a ceiling there is nothing
// to be near, and a raw number must not imply urgency it cannot know about.
//
// So does every cost chip, for the same reason written larger. Occupancy has a
// ceiling, so "three quarters gone" is a fact about the session. Spend has none:
// $5 is alarming on one plan and rounding on another, and any threshold Atrium
// picked would be a guess dressed as a warning. The number is the whole signal.
// warn and danger are the configured bands; either at zero falls back to the
// package default (see contextBands).
func contextColor(th *theme.Theme, u transcript.Usage, mode string, warn, danger int) theme.Color {
	if mode == contextModeCost {
		return th.Palette.FgDim
	}
	window, known := agent.ClaudeContextWindow(u.Model)
	if !known {
		return th.Palette.FgDim
	}
	warn, danger = contextBands(warn, danger)
	switch pct := contextPercent(u.ContextTokens, window); {
	case pct >= danger:
		return th.Palette.Danger
	case pct >= warn:
		return th.Palette.Attention
	default:
		return th.Palette.FgDim
	}
}

// Cost-chip bounds. They are not a pair despite sitting together: the ceiling is
// a width bound, keeping the chip inside the same five cells the occupancy modes
// fit in, and the floor is a legibility one.
const (
	// costFloorUSD is the smallest COMPLETE estimate worth a chip. Below half a
	// cent there is no two-decimal figure to print that is not "$.00", and a chip
	// that reads as zero is worse than no chip — absent already means "nothing to
	// see here", and it means it without looking broken. Rarely reached on its own
	// terms: a single opening turn on Opus 5 costs several cents.
	//
	// It does NOT apply to a partial reading, and costChip says why at length —
	// an all-unpriced session totals zero, so an unconditional floor would hide
	// the chip precisely when the price table has failed.
	costFloorUSD = 0.005
	// costCeilingUSD is where the ladder saturates. "99k" plus a prefix is the
	// widest thing that fits, so above this the chip stops being an estimate and
	// becomes a bound — which is why saturation forces the ">" prefix rather than
	// printing a rounded number that would be false. Reaching it would take on the
	// order of 200 billion cache-read tokens in one session.
	costCeilingUSD = 99_500
)

// Cost-chip prefixes, one cell each and both plain ASCII, so the chip needs no
// Glyphs entry and no nerd/plain/ascii ladder.
const (
	// costEstimate marks a figure priced from a complete transcript: everything
	// was recognized, and the arithmetic is Claude Code's own /usage arithmetic.
	// It is never "the" cost — list rates are not a subscription's bill and may
	// not be an API account's either — hence a tilde on every single one.
	costEstimate = "~"
	// costAtLeast marks a figure that is a LOWER BOUND, for either of two
	// reasons: something in the transcript could not be priced (an unrecognized
	// model, fast mode on a model with no published fast rate), or the true figure
	// is past costCeilingUSD and would not fit. Both make ">" true, which is why
	// one marker covers both and why they compose without a third case.
	costAtLeast = ">"
)

// costChip renders a spend estimate, and reports false when there is nothing
// worth showing. See contextChip for why absent beats a zero.
//
// The floor does NOT apply to a partial reading, and that exception is the whole
// point rather than an edge case. When every request in a transcript is
// unpriceable — which is exactly what a model shipping ahead of the price table
// looks like — the total is 0 and the floor would suppress the chip entirely,
// leaving a session indistinguishable from one running codex or one that has not
// taken a turn. That is the failure the ">" exists to prevent, so it must
// survive the case that causes it: ">$.00" says "there is spend here and Atrium
// could not price any of it", which is both true and visibly different from
// nothing at all.
func costChip(c transcript.Cost) (string, bool) {
	if c.USD < costFloorUSD && !c.Partial() {
		return "", false
	}
	prefix := costEstimate
	if c.Partial() || c.USD >= costCeilingUSD {
		prefix = costAtLeast
	}
	return prefix + "$" + costFigure(c.USD), true
}

// costFigure renders a dollar amount in AT MOST three cells, which is the whole
// budget once the prefix and the "$" have taken one each.
//
// The rungs are chosen so each one keeps the digits that can change a decision at
// its magnitude and drops the ones that cannot: cents matter under a dollar,
// tenths under ten, and nothing below the dollar matters above that. Every
// boundary is stated as the value at which the NEXT rung's rendering becomes the
// shorter one (9.95 rounds to "10", not "10.0"), so the ladder is continuous —
// no amount renders wider than three cells on its way between two rungs.
func costFigure(usd float64) string {
	switch {
	case usd >= costCeilingUSD:
		return "99k"
	case usd < 0.995:
		return fmt.Sprintf(".%02d", int(usd*100+0.5))
	case usd < 9.95:
		return strconv.FormatFloat(usd, 'f', 1, 64)
	case usd < 999.5:
		return strconv.Itoa(int(usd + 0.5))
	default:
		return strconv.Itoa(int(usd/1000+0.5)) + "k"
	}
}

// humanizeTokens renders a context token count compactly: exact below 1000,
// whole thousands to just under a million ("283k"), then one decimal of
// millions ("1.0M").
//
// Width: four cells for every reading this chip can actually carry, and five is
// the budget the layout is sized against. Four holds through 9,950,000 ("9.9M");
// one token past that is "10.0M", five cells — unreachable in practice, since
// the widest window in the table (agent.ClaudeContextWindow) is 1M and a reading
// ten times that would mean the transcript, not the formatter, had gone wrong.
// Both sides of that boundary are pinned in TestHumanizeTokens; the chip tests
// assert the five-cell ceiling rather than four so the bound holds for any
// input, not just the plausible ones.
//
// Deliberately not humanizeCount (row.go), which shares the "k" idea but keeps
// a decimal place throughout: it renders 999,323 as "999.3k", six cells. Six is
// affordable on the version-control line it was written for and is not
// affordable here, where the chip is a fixed segment on line 1's right cluster
// and every cell comes out of the name column's budget. Whole thousands cost
// nothing in meaning at this magnitude — nobody reads a context chip for the
// last 300 tokens.
func humanizeTokens(n int) string {
	switch {
	case n < 1000:
		return strconv.Itoa(n)
	case n < 999_500:
		return strconv.Itoa((n+500)/1000) + "k"
	default:
		return strconv.FormatFloat(float64(n)/1_000_000.0, 'f', 1, 64) + "M"
	}
}
