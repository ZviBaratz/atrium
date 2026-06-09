package tmux

import (
	"math"
	"testing"
)

func TestClampUint16(t *testing.T) {
	cases := []struct {
		name string
		in   int
		want uint16
	}{
		{"zero", 0, 0},
		{"one", 1, 1},
		{"typical terminal width", 220, 220},
		{"max uint16", math.MaxUint16, math.MaxUint16},
		{"above max clamps", math.MaxUint16 + 1, math.MaxUint16},
		{"far above max clamps", 1_000_000, math.MaxUint16},
		{"negative clamps to zero", -1, 0},
		{"large negative clamps to zero", -100_000, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := clampUint16(tc.in); got != tc.want {
				t.Errorf("clampUint16(%d) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}
