package ui

import (
	"testing"

	"github.com/ZviBaratz/atrium/session/transcript"
	"github.com/ZviBaratz/atrium/ui/theme"

	"github.com/mattn/go-runewidth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// opusUsage builds a reading on a model whose window is in the table (1M), so a
// test can state a percentage by picking a token count.
func opusUsage(tokens int) transcript.Usage {
	return transcript.Usage{ContextTokens: tokens, Model: "claude-opus-5"}
}

func plainRamp(t *testing.T) []string {
	t.Helper()
	t.Cleanup(theme.SetGlyphSet(theme.GlyphSetPlain))
	theme.SetGlyphSet(theme.GlyphSetPlain)
	return theme.Current().Glyphs.ContextRamp
}

// TestContextChip_Modes walks the four modes on a known model.
func TestContextChip_Modes(t *testing.T) {
	ramp := plainRamp(t)
	u := opusUsage(283_000) // 28% of 1M

	for _, tc := range []struct{ mode, want string }{
		{contextModePercent, "28%"},
		{contextModeCount, "283k"},
		{contextModeBar, "▃"}, // 28% → rung 2 of 8
		{"", "28%"},           // the zero value must behave like the documented default
	} {
		got, ok := contextChip(u, tc.mode, ramp)
		require.Truef(t, ok, "mode %q must render a chip", tc.mode)
		assert.Equalf(t, tc.want, got, "mode %q", tc.mode)
	}

	got, ok := contextChip(u, contextModeOff, ramp)
	assert.False(t, ok, "off must render nothing")
	assert.Empty(t, got)
}

// TestContextChip_UnknownModelDegradesToACount is #596's acceptance criterion 8,
// at the layer the user actually sees.
//
// The invented model id is the whole point: it shares a prefix with a real
// entry, so a lookup that had been "helpfully" loosened to prefix matching would
// return a percentage here and pass every other test in this file. Both
// percentage-shaped modes are checked, `bar` included — a meter cannot express
// an unknown ceiling, so it must fall back too rather than silently drawing a
// rung off a denominator it does not have.
func TestContextChip_UnknownModelDegradesToACount(t *testing.T) {
	ramp := plainRamp(t)
	invented := transcript.Usage{ContextTokens: 283_000, Model: "claude-opus-99"}

	for _, mode := range []string{contextModePercent, contextModeBar, contextModeCount, ""} {
		got, ok := contextChip(invented, mode, ramp)
		require.Truef(t, ok, "mode %q must still render something for an unknown model", mode)
		assert.Equalf(t, "283k", got,
			"mode %q on an unknown model must degrade to a count, never to a percentage or a meter", mode)
	}

	// And the colour must not claim urgency it cannot know about: 283k could be
	// 28% of 1M or 141% of 200K, so an unknown ceiling stays dim at any count.
	th := theme.Current()
	assert.Equal(t, th.Palette.FgDim, contextColor(th, transcript.Usage{ContextTokens: 990_000, Model: "claude-opus-99"}),
		"an unknown ceiling must stay dim however large the count")
}

// TestContextChip_AbsentWithoutAReading is acceptance criterion 2: no zeros, no
// errors, no layout shift for a session that has nothing to report.
func TestContextChip_AbsentWithoutAReading(t *testing.T) {
	ramp := plainRamp(t)
	for _, u := range []transcript.Usage{
		{}, // never polled / non-claude
		{ContextTokens: 0, Model: "claude-opus-5"},   // known model, no reading
		{ContextTokens: -1, Model: "claude-opus-5"},  // nonsense, defended
		{ContextTokens: 0, Model: "claude-opus-4-8"}, // ditto on another known model
		{ContextTokens: 0, Model: "<synthetic>"},     // an API-error entry that slipped through
	} {
		for _, mode := range []string{contextModePercent, contextModeCount, contextModeBar, ""} {
			got, ok := contextChip(u, mode, ramp)
			assert.Falsef(t, ok, "usage %+v mode %q must render no chip", u, mode)
			assert.Empty(t, got)
		}
	}
}

// TestContextPercent_Clamps pins the ceiling. The corpus's peak reading was
// 99.93% of the declared window, so a model overshooting its published ceiling
// by a hair is a live possibility — and "103%" would read as a bug in Atrium
// rather than as a full context.
func TestContextPercent_Clamps(t *testing.T) {
	assert.Equal(t, 100, contextPercent(1_050_000, 1_000_000), "over the ceiling clamps to 100")
	assert.Equal(t, 100, contextPercent(1_000_000, 1_000_000))
	assert.Equal(t, 99, contextPercent(999_323, 1_000_000), "the corpus peak is 99%, not 100")
	assert.Equal(t, 0, contextPercent(500, 1_000_000), "a tiny reading floors to 0%")
	assert.Equal(t, 0, contextPercent(1_000, 0), "a zero window cannot divide")
	assert.Equal(t, 0, contextPercent(-5, 1_000_000))
}

// TestContextLevel_CoversEveryRung pins the meter's index math. It is asserted
// rather than reasoned about because contextChip indexes the ramp with this
// value: an off-by-one here is a panic in the render path, not a wrong glyph.
func TestContextLevel_CoversEveryRung(t *testing.T) {
	const rungs = 8
	for pct := 0; pct <= 100; pct++ {
		idx := contextLevel(pct, rungs)
		require.GreaterOrEqualf(t, idx, 0, "pct %d", pct)
		require.Lessf(t, idx, rungs, "pct %d", pct)
	}
	assert.Equal(t, 0, contextLevel(0, rungs), "a nonzero reading below one rung still shows the lowest rung")
	assert.Equal(t, 7, contextLevel(100, rungs))
	assert.Equal(t, 0, contextLevel(50, 0), "a missing ramp must not divide by zero")
}

// TestContextColor_Thresholds pins where the chip changes colour. The number
// carries the same signal, so colour is reinforcement — but the thresholds are
// what make "which session is about to compact?" answerable by scanning.
func TestContextColor_Thresholds(t *testing.T) {
	th := theme.Current()
	for _, tc := range []struct {
		tokens int
		want   theme.Color
		label  string
	}{
		{280_000, th.Palette.FgDim, "28% is unremarkable"},
		{740_000, th.Palette.FgDim, "just under the warn threshold"},
		{750_000, th.Palette.Attention, "at the warn threshold"},
		{890_000, th.Palette.Attention, "just under the danger threshold"},
		{900_000, th.Palette.Danger, "at the danger threshold"},
		{999_323, th.Palette.Danger, "the corpus peak"},
	} {
		assert.Equalf(t, tc.want, contextColor(th, opusUsage(tc.tokens)), tc.label)
	}
}

// TestHumanizeTokens pins the count's shape and, more importantly, its width
// ceiling. The chip is a fixed segment on line 1's right cluster, so every cell
// it takes comes straight out of the name column's budget — this is why it does
// not reuse humanizeCount, which renders 999,323 as the six-cell "999.3k".
func TestHumanizeTokens(t *testing.T) {
	for _, tc := range []struct {
		in   int
		want string
	}{
		{0, "0"},
		{742, "742"},
		{999, "999"},
		{1_000, "1k"},
		{1_499, "1k"},
		{1_500, "2k"},
		{37_900, "38k"},
		{283_000, "283k"},
		{521_300, "521k"},
		{999_323, "999k"}, // humanizeCount would give the 6-cell "999.3k"
		{999_499, "999k"},
		{999_500, "1.0M"},
		{1_048_576, "1.0M"},
		{1_500_000, "1.5M"},
	} {
		assert.Equalf(t, tc.want, humanizeTokens(tc.in), "humanizeTokens(%d)", tc.in)
	}
}

// TestContextChipWidthCeiling proves the budget the placement argument rests on,
// across every mode, every glyph rung, and the whole range of readings a session
// can produce — including the two ends nothing else reaches.
//
// It is a proof rather than a measurement because the inputs are bounded: the
// percentage is clamped to [0,100] and the count is capped by humanizeTokens, so
// there is a real worst case to assert. A row-level test can only sample.
func TestContextChipWidthCeiling(t *testing.T) {
	const maxChipCells = 5 // "100%" is 4, "1.5M" is 4; 5 leaves the ceiling honest
	models := []string{"claude-opus-5", "claude-haiku-4-5", "claude-opus-99" /* unknown → count */}

	for _, set := range []string{theme.GlyphSetNerd, theme.GlyphSetPlain, theme.GlyphSetASCII} {
		restore := theme.SetGlyphSet(set)
		ramp := theme.Current().Glyphs.ContextRamp
		for _, model := range models {
			for _, tokens := range []int{1, 999, 1_000, 199_999, 283_000, 999_323, 1_000_000, 5_000_000} {
				u := transcript.Usage{ContextTokens: tokens, Model: model}
				for _, mode := range []string{contextModePercent, contextModeCount, contextModeBar, ""} {
					chip, ok := contextChip(u, mode, ramp)
					if !ok {
						continue
					}
					assert.LessOrEqualf(t, runewidth.StringWidth(chip), maxChipCells,
						"chip %q (set=%s model=%s tokens=%d mode=%s) exceeds the %d-cell budget",
						chip, set, model, tokens, mode, maxChipCells)
				}
			}
		}
		restore()
	}
}
