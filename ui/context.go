package ui

import (
	"fmt"
	"strconv"

	"github.com/ZviBaratz/atrium/session/agent"
	"github.com/ZviBaratz/atrium/session/transcript"
	"github.com/ZviBaratz/atrium/ui/theme"
)

// The per-session context-window chip (#596): how full the agent's context is,
// as a percentage where the model's ceiling is known and a raw token count where
// it isn't.
//
// That split is the design's safety property rather than a convenience. Because
// an unknown model falls back to a count, a window table that has gone stale
// degrades VISIBLY — "28%" becomes "283k" — instead of quietly reporting a
// confident wrong fraction. Every non-off mode honours the fallback, `bar`
// included: a meter has no way to express an unknown ceiling.

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

// Attention thresholds for the chip's colour, as percentages of the window.
// Reachable whenever the ceiling is KNOWN, in every mode — a `count`-mode chip
// on a known model tints too, and should: "900k" is as urgent as "90%", and the
// mode chooses how the number reads, not whether the urgency is real. What
// stays dim unconditionally is a count from an UNKNOWN model, which has no
// ceiling to be near (see contextColor).
const (
	contextWarnPct   = 75
	contextDangerPct = 90
)

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
func contextColor(th *theme.Theme, u transcript.Usage, mode string) theme.Color {
	if mode == contextModeCost {
		return th.Palette.FgDim
	}
	window, known := agent.ClaudeContextWindow(u.Model)
	if !known {
		return th.Palette.FgDim
	}
	switch pct := contextPercent(u.ContextTokens, window); {
	case pct >= contextDangerPct:
		return th.Palette.Danger
	case pct >= contextWarnPct:
		return th.Palette.Attention
	default:
		return th.Palette.FgDim
	}
}

// Cost-chip bounds. Both exist to keep the chip inside the same five cells the
// occupancy modes fit in, because it shares their column.
const (
	// costFloorUSD is the smallest estimate worth a chip. Below half a cent there
	// is no two-decimal figure to print that is not "$.00", and a chip that reads
	// as zero is worse than no chip — absent already means "nothing to see here",
	// and it means it without looking broken. Unreachable in practice: a single
	// opening turn on Opus 5 costs several cents.
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
func costChip(c transcript.Cost) (string, bool) {
	if c.USD < costFloorUSD {
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
