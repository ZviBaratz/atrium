package overlay

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestSizeSpecFit drives each rule of the resolution order — scale, extra,
// cap, floor, terminal clamp, zero-case — through a value only that rule
// produces: deleting any one fails its own row. The two observable orderings
// have discriminating rows of their own: extra-before-cap (at a 56-row
// terminal the capped sum is 43; capping first would give 46) and
// cap-before-floor (a floor above a cap resolves to the floor, 40, not the
// cap, 30 — on the unsized branch too).
func TestSizeSpecFit(t *testing.T) {
	cases := []struct {
		name         string
		spec         SizeSpec
		termW, termH int
		w, h         int
	}{
		{"plain scale truncates", SizeSpec{WFrac: 0.6, HFrac: 0.85}, 81, 24, 48, 20},
		{"extra lands before the cap", SizeSpec{HFrac: 0.85, HExtra: 3, HMax: 43}, 0, 56, 0, 43},
		{"extra clears the cap without it", SizeSpec{HFrac: 0.85, HExtra: 3, HMax: 43}, 0, 40, 0, 37},
		{"cap binds", SizeSpec{WFrac: 0.85, WMax: 100}, 200, 0, 100, 0},
		{"floor applies after the cap", SizeSpec{WFrac: 1, WExtra: -2, WMax: 52, WMin: 22}, 10, 0, 22, 0},
		{"a floor above a cap wins", SizeSpec{WFrac: 1, WMax: 30, WMin: 40}, 100, 0, 40, 0},
		{"negative extra shrinks", SizeSpec{WFrac: 1, WExtra: -2, WMax: 52, WMin: 22}, 44, 0, 42, 0},
		{"zero terminal returns the preferred size", SizeSpec{WFrac: 1, WExtra: -2, WMax: 52, WMin: 22, HFrac: 1, HMax: 30}, 0, 0, 52, 30},
		{"zero terminal prefers a floor above the cap", SizeSpec{WMax: 30, WMin: 40}, 0, 0, 40, 0},
		{"height clamps to the terminal", SizeSpec{HFrac: 1, HExtra: 3}, 0, 24, 0, 24},
		{"width does not clamp to the terminal", SizeSpec{WFrac: 1, WExtra: 3}, 24, 0, 27, 0},
		{"full-terminal pass-through", SizeSpec{WFrac: 1, HFrac: 1}, 120, 40, 120, 40},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w, h := tc.spec.Fit(tc.termW, tc.termH)
			assert.Equal(t, tc.w, w, "width")
			assert.Equal(t, tc.h, h, "height")
		})
	}
}

// specFitSizes are the terminals specFitTable pins every spec at: the two
// golden sizes, a wide size that reaches the caps the golden sizes never
// bind, an absurdly narrow size that reaches the floors and the palette's
// terminal clamp, and the zero (unsized) case.
var specFitSizes = [5][2]int{{80, 24}, {120, 40}, {200, 50}, {10, 6}, {0, 0}}

// specFitTable pins every exported spec's resolution at specFitSizes, so a
// mistyped fraction, extra, cap or floor in any declaration fails on its own
// row rather than surfacing as a moved box at some terminal size no test
// renders. Row names are the spec var names verbatim:
// TestSpecVarTablesAreTotal enumerates the exported SizeSpec declarations
// from the source and holds this table (and the width contract) to them, so
// a new spec cannot land rowless.
var specFitTable = []struct {
	name string
	spec SizeSpec
	want [5][2]int
}{
	{"TextInputSize", TextInputSize, [5][2]int{{48, 24}, {72, 40}, {120, 50}, {6, 6}, {0, 0}}},
	{"Fullscreen", Fullscreen, [5][2]int{{80, 24}, {120, 40}, {200, 50}, {10, 6}, {0, 0}}},
	{"ConfirmSize", ConfirmSize, [5][2]int{{52, 0}, {52, 0}, {52, 0}, {22, 0}, {52, 0}}},
	{"WelcomeSize", WelcomeSize, [5][2]int{{56, 0}, {56, 0}, {56, 0}, {22, 0}, {56, 0}}},
	{"HistoryPickerSize", HistoryPickerSize, [5][2]int{{50, 0}, {74, 0}, {82, 0}, {22, 0}, {82, 0}}},
	{"CmdLogSize", CmdLogSize, [5][2]int{{68, 20}, {102, 34}, {120, 42}, {8, 5}, {120, 44}}},
	{"CommandPaletteSize", CommandPaletteSize, [5][2]int{{68, 23}, {100, 37}, {100, 43}, {8, 6}, {100, 43}}},
	{"CustomCommandsSize", CustomCommandsSize, [5][2]int{{56, 16}, {80, 28}, {80, 30}, {7, 4}, {80, 30}}},
	{"CheckpointSize", CheckpointSize, [5][2]int{{56, 20}, {84, 34}, {96, 40}, {7, 5}, {96, 40}}},
	{"ImageSize", ImageSize, [5][2]int{{68, 20}, {102, 34}, {126, 42}, {8, 5}, {126, 48}}},
}

// TestEverySpecVarFitsItsTable runs specFitTable; see its doc for what the
// rows pin and what holds the table total.
func TestEverySpecVarFitsItsTable(t *testing.T) {
	for _, tc := range specFitTable {
		t.Run(tc.name, func(t *testing.T) {
			for i, sz := range specFitSizes {
				w, h := tc.spec.Fit(sz[0], sz[1])
				assert.Equal(t, tc.want[i], [2]int{w, h}, "Fit(%d, %d)", sz[0], sz[1])
			}
		})
	}
}

// TestSizeSpecFitIsFloat32 pins the scaling to float32 arithmetic at the one
// terminal width where it matters: 0.7 of 90 columns is 63 under float32 and
// 62 under float64, so a reimplementation of fitAxis in float64 — which
// agrees at both golden sizes and looks like a harmless cleanup — dies here
// instead of moving box widths at terminal sizes no golden renders.
func TestSizeSpecFitIsFloat32(t *testing.T) {
	w, _ := SizeSpec{WFrac: 0.7}.Fit(90, 0)
	assert.Equal(t, 63, w, "0.7 of 90 must truncate the float32 product, not the float64 one")
}
