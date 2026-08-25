package overlay

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestSizeSpecFit drives each rule of the resolution order — scale, extra,
// cap, floor, terminal clamp, zero-case — through a value that only that rule
// produces, so deleting or reordering any one of them fails its own row.
func TestSizeSpecFit(t *testing.T) {
	cases := []struct {
		name         string
		spec         SizeSpec
		termW, termH int
		w, h         int
	}{
		{"plain scale truncates", SizeSpec{WFrac: 0.6, HFrac: 0.85}, 81, 24, 48, 20},
		{"extra lands before the cap", SizeSpec{HFrac: 0.85, HExtra: 3, HMax: 43}, 0, 48, 0, 43},
		{"extra clears the cap without it", SizeSpec{HFrac: 0.85, HExtra: 3, HMax: 43}, 0, 40, 0, 37},
		{"cap binds", SizeSpec{WFrac: 0.85, WMax: 100}, 200, 0, 100, 0},
		{"floor applies after the cap", SizeSpec{WFrac: 1, WExtra: -2, WMax: 52, WMin: 22}, 10, 0, 22, 0},
		{"negative extra shrinks", SizeSpec{WFrac: 1, WExtra: -2, WMax: 52, WMin: 22}, 44, 0, 42, 0},
		{"zero terminal returns the preferred size", SizeSpec{WFrac: 1, WExtra: -2, WMax: 52, WMin: 22, HFrac: 1, HMax: 30}, 0, 0, 52, 30},
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

// TestEverySpecVarFitsItsTable pins every exported spec's resolution at the
// two golden sizes, a wide size that reaches the caps the golden sizes never
// bind, and the zero (unsized) case — so a mistyped fraction, extra, cap or
// floor in any declaration fails on its own row rather than surfacing as a
// moved box at some terminal size no test renders.
func TestEverySpecVarFitsItsTable(t *testing.T) {
	sizes := [4][2]int{{80, 24}, {120, 40}, {200, 50}, {0, 0}}
	cases := []struct {
		name string
		spec SizeSpec
		want [4][2]int
	}{
		{"TextInput", TextInputSize, [4][2]int{{48, 24}, {72, 40}, {120, 50}, {0, 0}}},
		{"Fullscreen", Fullscreen, [4][2]int{{80, 24}, {120, 40}, {200, 50}, {0, 0}}},
		{"Confirm", ConfirmSize, [4][2]int{{52, 0}, {52, 0}, {52, 0}, {52, 0}}},
		{"Welcome", WelcomeSize, [4][2]int{{56, 0}, {56, 0}, {56, 0}, {56, 0}}},
		{"HistoryPicker", HistoryPickerSize, [4][2]int{{50, 0}, {74, 0}, {82, 0}, {82, 0}}},
		{"CmdLog", CmdLogSize, [4][2]int{{68, 20}, {102, 34}, {120, 42}, {120, 44}}},
		{"CommandPalette", CommandPaletteSize, [4][2]int{{68, 23}, {100, 37}, {100, 43}, {100, 43}}},
		{"CustomCommands", CustomCommandsSize, [4][2]int{{56, 16}, {80, 28}, {80, 30}, {80, 30}}},
		{"Checkpoint", CheckpointSize, [4][2]int{{56, 20}, {84, 34}, {96, 40}, {96, 40}}},
		{"Image", ImageSize, [4][2]int{{68, 20}, {102, 34}, {126, 42}, {126, 48}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for i, sz := range sizes {
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
