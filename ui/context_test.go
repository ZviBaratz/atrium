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
		got, ok := contextChip(u, transcript.Cost{}, tc.mode, ramp)
		require.Truef(t, ok, "mode %q must render a chip", tc.mode)
		assert.Equalf(t, tc.want, got, "mode %q", tc.mode)
	}

	got, ok := contextChip(u, transcript.Cost{}, contextModeOff, ramp)
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
		got, ok := contextChip(invented, transcript.Cost{}, mode, ramp)
		require.Truef(t, ok, "mode %q must still render something for an unknown model", mode)
		assert.Equalf(t, "283k", got,
			"mode %q on an unknown model must degrade to a count, never to a percentage or a meter", mode)
	}

	// And the colour must not claim urgency it cannot know about: 283k could be
	// 28% of 1M or 141% of 200K, so an unknown ceiling stays dim at any count.
	th := theme.Current()
	assert.Equal(t, th.Palette.FgDim, contextColor(th, transcript.Usage{ContextTokens: 990_000, Model: "claude-opus-99"}, contextModePercent),
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
			got, ok := contextChip(u, transcript.Cost{}, mode, ramp)
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
	assert.Equal(t, 7, contextLevel(100, rungs))
	assert.Equal(t, 0, contextLevel(50, 0), "a missing ramp must not divide by zero")
}

// TestContextChip_BarFloorAndEmptyRamp covers the two ends contextLevel's unit
// test cannot reach, both through contextChip — which is the only caller, and
// the only place the index is actually used.
//
// The floor: "a nonzero reading below one rung still shows the lowest rung" is a
// claim about a READING, and asserting it as contextLevel(0, 8) == 0 restates
// the index math instead. Driven with 4,000 tokens against a 1M window, it goes
// through contextPercent's integer division flooring a 0.4% reading to 0 — the
// step that makes the claim true — and out to the glyph a user would see.
//
// The empty ramp: contextLevel returns 0 for a zero-length ramp, so a caller
// that trusted that guard would index [0] on an empty slice and panic. Nothing
// reaches it today, which is exactly why it needs a test rather than a comment.
func TestContextChip_BarFloorAndEmptyRamp(t *testing.T) {
	ramp := []string{"a", "b", "c", "d", "e", "f", "g", "h"}
	u := transcript.Usage{ContextTokens: 4_000, Model: "claude-opus-5"} // 0.4% of 1M

	chip, ok := contextChip(u, transcript.Cost{}, contextModeBar, ramp)
	require.True(t, ok)
	assert.Equal(t, "a", chip,
		"a session with context in it must show the lowest rung, never nothing")

	require.NotPanics(t, func() {
		chip, ok = contextChip(u, transcript.Cost{}, contextModeBar, nil)
	})
	assert.True(t, ok)
	assert.Equal(t, "4k", chip, "no meter to draw falls back to a count, like an unknown ceiling")
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
		assert.Equalf(t, tc.want, contextColor(th, opusUsage(tc.tokens), contextModePercent), tc.label)
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
		// The four-cell ceiling the doc comment claims, and the first value past
		// it. Pinned because the placement arithmetic is stated in cells: the
		// budget asserted below is five, and this is where the fifth is spent.
		{9_950_000, "9.9M"},
		{9_950_001, "10.0M"},
	} {
		assert.Equalf(t, tc.want, humanizeTokens(tc.in), "humanizeTokens(%d)", tc.in)
	}

	for _, n := range []int{1, 999, 1_000, 283_000, 999_499, 999_500, 9_950_000} {
		assert.LessOrEqualf(t, runewidth.StringWidth(humanizeTokens(n)), 4,
			"humanizeTokens(%d) = %q must fit four cells — every reading a 1M window can produce does",
			n, humanizeTokens(n))
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
					chip, ok := contextChip(u, transcript.Cost{}, mode, ramp)
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

// TestCostChipWidthCeiling is the cost mode's half of the budget above, and it
// is a genuine proof rather than a sample: the ladder saturates, so there IS a
// widest output and it can be asserted over every rung, every boundary, and the
// unreachable ends.
//
// The five cells are not this chip's own budget to spend. It shares the
// occupancy chip's column, and the claim that #596's name budgets (28 typical,
// 21 fully loaded) survive #392 unchanged is exactly the claim that no cost
// figure is wider than the occupancy figure it replaces. This test is that claim.
func TestCostChipWidthCeiling(t *testing.T) {
	const maxChipCells = 5

	// Both sides of every rung boundary, plus the ends. A rendering that grew a
	// cell on its way between two rungs would show up here and nowhere else.
	amounts := []float64{
		costFloorUSD, 0.01, 0.42, 0.99, 0.994, 0.995, 1.0, 9.94, 9.95, 10.0,
		99.9, 100.0, 999.4, 999.5, 1000.0, 4123.0, 99_499, costCeilingUSD,
		1e6, 1e12,
	}
	for _, partial := range []bool{false, true} {
		for _, usd := range amounts {
			c := transcript.Cost{USD: usd}
			if partial {
				c.Unpriced = 1
			}
			chip, ok := costChip(c)
			if !assert.Truef(t, ok, "costChip($%v) must render", usd) {
				continue
			}
			assert.LessOrEqualf(t, runewidth.StringWidth(chip), maxChipCells,
				"chip %q ($%v, partial=%v) exceeds the %d-cell budget", chip, usd, partial, maxChipCells)
		}
	}
}

// TestCostChipLadder pins what each rung actually renders, because the width
// ceiling alone is satisfied by a chip that says nothing useful.
//
// The boundary pairs are the point. Each rung hands over at the value where the
// next rung's rendering becomes the shorter one — 9.95 rounds to "10", not to
// "10.0" — so the ladder never widens mid-range. Getting one of those wrong
// costs a cell on a row that has none.
func TestCostChipLadder(t *testing.T) {
	for _, tc := range []struct {
		usd  float64
		want string
	}{
		{costFloorUSD, "~$.01"}, // the smallest chip there is
		{0.42, "~$.42"},
		{0.994, "~$.99"},
		{0.995, "~$1.0"}, // hands over rather than rendering "1.00"
		{4.1, "~$4.1"},
		{9.94, "~$9.9"},
		{9.95, "~$10"}, // hands over rather than rendering "10.0"
		{263.335, "~$263"},
		{999.4, "~$999"},
		{999.5, "~$1k"}, // hands over rather than rendering "1000"
		{4123, "~$4k"},
		{99_499, "~$99k"},
	} {
		chip, ok := costChip(transcript.Cost{USD: tc.usd})
		if !assert.Truef(t, ok, "costChip($%v)", tc.usd) {
			continue
		}
		assert.Equalf(t, tc.want, chip, "costChip($%v)", tc.usd)
	}
}

// TestCostChipMarksALowerBoundRatherThanGuessing covers the two ways an estimate
// stops being one, and the single marker that covers both.
//
// This is the visible half of the exact-match price table's safety property. A
// model the table does not carry contributes nothing to the total, so the figure
// is too low — and the whole point of refusing to guess a rate is that the user
// can SEE that, which is what ">" says and "~" would not. Saturation gets the
// same marker because it makes the same statement: the truth is at least this.
func TestCostChipMarksALowerBoundRatherThanGuessing(t *testing.T) {
	complete, ok := costChip(transcript.Cost{USD: 4.1, Requests: 3})
	assert.True(t, ok)
	assert.Equal(t, "~$4.1", complete, "a fully priced total is an estimate")

	partial, ok := costChip(transcript.Cost{USD: 4.1, Requests: 3, Unpriced: 1})
	assert.True(t, ok)
	assert.Equal(t, ">$4.1", partial,
		"one unpriceable request makes the whole figure a floor, and it must say so")

	saturated, ok := costChip(transcript.Cost{USD: 250_000, Requests: 3})
	assert.True(t, ok)
	assert.Equal(t, ">$99k", saturated,
		"a figure too wide to print must become a true bound, not a rounded lie")

	// Both at once compose into the same marker rather than needing a third.
	both, ok := costChip(transcript.Cost{USD: 250_000, Unpriced: 5})
	assert.True(t, ok)
	assert.Equal(t, ">$99k", both)
}

// TestCostChipIsAbsentRatherThanZero pins the floor. A session that has spent
// less than half a cent has no two-decimal figure to show that is not "$.00",
// and a chip reading zero looks like a bug in a way an absent chip does not.
func TestCostChipIsAbsentRatherThanZero(t *testing.T) {
	for _, usd := range []float64{0, 0.0001, 0.004} {
		chip, ok := costChip(transcript.Cost{USD: usd})
		assert.Falsef(t, ok, "costChip($%v) rendered %q, want no chip", usd, chip)
	}
	// And the first amount that does clear it renders the smallest real figure.
	chip, ok := costChip(transcript.Cost{USD: costFloorUSD})
	assert.True(t, ok)
	assert.Equal(t, "~$.01", chip)
}

// TestContextChipModesReadTheirOwnValue is the guard on the shared column: each
// mode must read the reading it is about and ignore the other, so a session
// holding one and not the other renders correctly rather than falling back to
// whichever field happens to be populated.
//
// It matters because the poll layer only ever fills ONE of them — the mode picks
// which read is taken — so the other is always zero. A renderer that consulted
// the wrong one would show no chip at all, and a test that passed both non-zero
// values would never notice.
func TestContextChipModesReadTheirOwnValue(t *testing.T) {
	ramp := theme.Current().Glyphs.ContextRamp
	occupancy := transcript.Usage{ContextTokens: 280_000, Model: "claude-opus-5"}
	spend := transcript.Cost{USD: 4.1, Requests: 2}

	t.Run("cost mode ignores an occupancy reading", func(t *testing.T) {
		_, ok := contextChip(occupancy, transcript.Cost{}, contextModeCost, ramp)
		assert.False(t, ok, "no spend recorded means no chip, whatever the context reading says")

		chip, ok := contextChip(transcript.Usage{}, spend, contextModeCost, ramp)
		assert.True(t, ok)
		assert.Equal(t, "~$4.1", chip)
	})

	t.Run("occupancy modes ignore a cost reading", func(t *testing.T) {
		for _, mode := range []string{contextModePercent, contextModeCount, contextModeBar, ""} {
			_, ok := contextChip(transcript.Usage{}, spend, mode, ramp)
			assert.Falsef(t, ok, "mode %q must not render a chip from a cost reading", mode)
		}
		chip, ok := contextChip(occupancy, transcript.Cost{}, contextModePercent, ramp)
		assert.True(t, ok)
		assert.Equal(t, "28%", chip)
	})

	t.Run("off renders nothing whatever is held", func(t *testing.T) {
		_, ok := contextChip(occupancy, spend, contextModeOff, ramp)
		assert.False(t, ok)
	})
}

// TestContextColorCostIsAlwaysDim pins the deliberate absence of an attention
// ladder on the cost chip.
//
// Occupancy has a ceiling, so 90% is a fact about the session. Spend has none —
// $5 is alarming on one plan and rounding on another — so any threshold Atrium
// picked would be a guess rendered as a warning. The fixture drives amounts that
// WOULD be alarming to make the absence deliberate rather than untested.
func TestContextColorCostIsAlwaysDim(t *testing.T) {
	th := theme.Current()
	for _, usd := range []float64{0.01, 4.1, 50, 500, 5000, 250_000} {
		// The occupancy reading is deliberately one that would paint Danger, so
		// this fails if the cost branch ever falls through to the percentage ladder.
		u := transcript.Usage{ContextTokens: 999_000, Model: "claude-opus-5"}
		assert.Equalf(t, th.Palette.FgDim, contextColor(th, u, contextModeCost),
			"a $%v cost chip must stay dim", usd)
	}
}
