package theme

import "math"

// scheme.go owns the dark/light axis: what makes a palette light, and (from #394
// Stage E) how a detected terminal background selects one.

// relLuminanceOf is WCAG 2.1's relative luminance. color.Color reports 16-bit
// alpha-premultiplied components, which for the opaque palette colours Atrium
// ships is the 8-bit value repeated (0xNN * 0x101), so >>8 recovers the byte.
//
// It lives here rather than in contrast_test.go, where it started, because IsLight
// needs the same arithmetic and production code cannot call a test file. One copy
// is the point: two would drift, and the contrast oracle and the splash's polarity
// decision disagreeing about a palette is exactly the failure that would not
// announce itself.
func relLuminanceOf(c Color) float64 {
	r16, g16, b16, _ := c.RGBA()
	lin := func(v uint32) float64 {
		s := float64(v>>8&0xff) / 255
		if s <= 0.03928 {
			return s / 12.92
		}
		return math.Pow((s+0.055)/1.055, 2.4)
	}
	return 0.2126*lin(r16) + 0.7152*lin(g16) + 0.0722*lin(b16)
}

// IsLight reports whether a palette is built for a light-background terminal.
//
// The test is the relative luminance of Bg. Bg is the right token to ask because
// Atrium never paints a full-screen background — Bg is its statement about what the
// terminal itself is showing.
//
// The threshold is 0.35, not 0.5, and it is deliberately not load-bearing: the
// shipped palettes sit at 0.011 and 0.013 (tokyo-night, catppuccin-mocha) against
// 0.77 and 0.86 (tokyo-night-day, catppuccin-latte), so nothing is near the cut.
// 0.35 leaves room for a genuinely mid-tone palette to be classed light, which is
// the safer error: a light-tuned splash on a mid background reads, a dark-tuned one
// on a mid background inverts.
//
// One predicate, three consumers: the agent brand accents (agent.go), the splash's
// brightness channel (ui/splash.go), and the scheme axis to come. Two independent
// thresholds would eventually disagree about a palette in the middle, and the
// disagreement would show up as a UI tuned for the wrong polarity.
func IsLight(p Palette) bool {
	return relLuminanceOf(p.Bg) > 0.35
}
