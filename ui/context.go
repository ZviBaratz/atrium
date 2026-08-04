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
)

// Attention thresholds for the chip's colour, as percentages of the window.
// Only reachable when the ceiling is known — a count is always dim.
const (
	contextWarnPct   = 75
	contextDangerPct = 90
)

// contextChip returns the chip text for one reading under mode, and whether
// there is anything to draw at all. It never returns a zero: absent is the
// answer for a session with no reading, so a non-Claude profile, an unparsable
// transcript, or a session that has not taken a turn simply carries no chip
// rather than a misleading "0%".
//
// ramp supplies the one-cell meter for `bar` mode (theme.Glyphs.ContextRamp).
func contextChip(u transcript.Usage, mode string, ramp []string) (string, bool) {
	if u.ContextTokens <= 0 || mode == contextModeOff {
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
func contextColor(th *theme.Theme, u transcript.Usage) theme.Color {
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

// humanizeTokens renders a context token count in at most 4 cells: exact below
// 1000, whole thousands to just under a million ("283k"), then one decimal of
// millions ("1.0M").
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
