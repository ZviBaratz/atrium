package theme

import (
	"math"
	"strconv"
	"strings"
)

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

// Scheme is the detected polarity of the terminal's background.
//
// int32 rather than int so it is exactly the width of the atomic that stores it
// (curScheme, current.go). A narrowing conversion on every Set would be an overflow
// gosec flags, and silencing that would be silencing it about the one thing the
// pairing should make impossible.
type Scheme int32

const (
	// SchemeUnknown means detection has produced no answer — either it has not run
	// yet, or nothing answered. It is the zero value on purpose: absence of
	// evidence resolves to the shipped dark default, which is what makes
	// introducing detection a no-op for anyone it cannot reach.
	SchemeUnknown Scheme = iota
	// SchemeDark means the terminal reported a dark background.
	SchemeDark
	// SchemeLight means the terminal reported a light background.
	SchemeLight
)

// ResolveScheme runs the detection ladder over its inputs and returns the
// polarity, or SchemeUnknown when nothing answered.
//
// bgIsDark is an OSC 11 answer (nil when the terminal did not reply);
// colorfgbg is the raw COLORFGBG environment value ("" when unset).
//
// Two properties are load-bearing:
//
//   - It LATCHES at the caller. "No answer" is SchemeUnknown, never a flip to a
//     default — a terminal that stays quiet must leave the current scheme alone
//     rather than be treated as having reported dark. This function reports the
//     absence; the caller is responsible for not acting on it.
//
//   - COLORFGBG can never correct an OSC 11 answer. The variable is inherited by
//     child processes and is not updated when the terminal's theme changes, so it
//     is routinely stale. It is a hint for terminals that do not answer OSC 11,
//     and nothing more.
func ResolveScheme(bgIsDark *bool, colorfgbg string) Scheme {
	if bgIsDark != nil {
		if *bgIsDark {
			return SchemeDark
		}
		return SchemeLight
	}
	return schemeFromColorFGBG(colorfgbg)
}

// schemeFromColorFGBG reads the background half of COLORFGBG, which rxvt and
// friends set as "fg;bg" (sometimes "fg;bold;bg"). The last field is the
// background, as an ANSI palette index 0-15; "default" means the terminal
// declined to say, which is no answer rather than a dark one.
//
// Indices 0-6 and 8 are the dark half of the 16-colour palette, 7 and 9-15 the
// light half. That is the same split every other consumer of this variable uses;
// it is crude, which is exactly why this rung sits below OSC 11.
//
// Written against the convention rather than against a measurement: nothing in the
// pinned stack reads COLORFGBG (bubbletea, ultraviolet, colorprofile and lipgloss
// were all searched and none mentions it), and neither terminal available while
// this was written sets it. A rung nobody could observe answering is a second
// reason to keep it below one that replies for itself.
func schemeFromColorFGBG(v string) Scheme {
	if v == "" {
		return SchemeUnknown
	}
	fields := strings.Split(v, ";")
	bg := strings.TrimSpace(fields[len(fields)-1])
	n, err := strconv.Atoi(bg)
	if err != nil || n < 0 || n > 15 {
		return SchemeUnknown
	}
	if n == 7 || n >= 9 {
		return SchemeLight
	}
	return SchemeDark
}
