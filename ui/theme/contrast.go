package theme

import (
	"fmt"
	"math"
)

// contrast.go is the oracle for the one property a rendering test cannot see:
// whether a palette is READABLE. Every other guard in this package asks about width,
// shape or vocabulary; none of them would notice a foreground that disappeared into
// the background, which is exactly how three dark-tuned themes shipped as the only
// options (#394).
//
// It started life in contrast_test.go and moved here, whole, for the reason
// relLuminanceOf moved to scheme.go before it: production code cannot call a test
// file, and a user theme file has to be VALIDATED at load time rather than merely
// admired at review time (#813). contrast_test.go is now one of its callers rather
// than its home; the other is ui/theme/themefile.
//
// The floors are set from the MINIMUM across the shipped dark themes with margin, so
// they land green on what exists today and constrain what is added next. This is not
// an accessibility certification — the tokens Atrium deliberately renders faint would
// fail WCAG AA and should — it is a floor under "did someone pick a colour nobody can
// see".

// ContrastRatio is WCAG 2.1's contrast ratio: 1.0 for two identical colours, 21.0 for
// black on white. Order-independent.
func ContrastRatio(a, b Color) float64 {
	la, lb := relLuminanceOf(a), relLuminanceOf(b)
	hi, lo := math.Max(la, lb), math.Min(la, lb)
	return (hi + 0.05) / (lo + 0.05)
}

// tokenFloors is the minimum contrast a palette token must hold against its own
// theme's Bg, keyed by the token's on-disk name (see tokens.go). Bg is the reference
// because Atrium never paints a full-screen background — Palette.Bg means "the colour
// of the void", i.e. what the terminal itself shows — so a token's legibility is its
// ratio against it.
//
// The tiers are roles, not tastes. Status and text tokens carry meaning and get 4.5.
// success_dim gets its own 3.0: it is a status colour, but the dimming that marks a
// Ready session as already seen costs it the 4.5 tier by a hair — 4.35 on tokyo-night
// against 4.60 on mocha — so it holds WCAG's large-text/non-text threshold instead of
// a floor three of the four distinct palettes already miss (4.35 tokyo-night, 4.04
// tokyo-night-day, 3.13 catppuccin-latte). The three tokens Atrium deliberately
// recedes (fg_dim, working, and accent_muted, whose 2.55-on-tokyo-night-day to
// 8.69-on-mocha spread is why it cannot share accent's floor) get 2.4. fg_faint and
// bar_bg are the faint slate — in every shipped palette they are literally the SAME
// colour, a deliberate choice, not a defect — so they get 1.6. bg_elevated is a
// selection fill that must merely be distinguishable, so 1.1.
//
// Three tokens are absent, each for a reason TestEveryTokenIsFlooredOrExempt holds to
// a written-down entry rather than to silence: bg is the reference itself, and
// badge_bg/badge_fg meet each other rather than the void, under pairFloors.
var tokenFloors = map[string]float64{
	"fg":           4.5,
	"accent":       4.5,
	"purple":       4.5,
	"success":      4.5,
	"pending":      4.5,
	"attention":    4.5,
	"danger":       4.5,
	"cyan":         4.5,
	"success_dim":  3.0,
	"fg_dim":       2.4,
	"working":      2.4,
	"accent_muted": 2.4,
	"fg_faint":     1.6,
	"bar_bg":       1.6,
	"bg_elevated":  1.1,
}

// tokenFloorExempt names the tokens deliberately absent from tokenFloors, with the
// reason. A token in neither map fails the bidirectional guard rather than defaulting
// into "unchecked".
var tokenFloorExempt = map[string]string{
	"bg":       "the reference every other token is measured against",
	"badge_bg": "meets badge_fg, not the void: floored as a pair below",
	"badge_fg": "meets badge_bg, not the void: floored as a pair below",
}

// glyphFloor is the tier for a single width-1 mark rather than prose. It is the floor
// TestAgentBrandColoursStayLegible already applies to the agent brand accents, reused
// here for the status glyphs the in-session bar paints onto its band: one mark has to
// be findable, not readable at length.
const glyphFloor = 3.0

// pairFloors are the token pairs that meet each other directly rather than over Bg,
// each at the site that renders them. Every one of these is a real render, named here
// in the order the table lists them: badge_fg over badge_bg is the per-session AUTO
// chip (ui/list_render.go, and its legend in app/help.go), Foreground(Bg) over
// Background(Accent) is the picker's selected row (ui/overlay/styles.go), over
// Background(Attention) is the notice banner (app/banner.go), fg over bg_elevated is
// the selected list row (ui/row.go), and fg over bar_bg is the diff anchor
// (ui/diff_anchor.go) as well as the in-session bar's neutral status glyphs.
//
// The last three are the rest of that bar's status glyphs (ui/contextbar.go's
// barState), added with #555's fix. Their tier is glyphFloor, not 4.5: on the shipped
// light palettes these dip as low as 3.16, so a 4.5 floor would refuse two themes that
// have been legible for months — while 4.5 was never the issue, a fg_dim-valued token
// at 1.44 was. barState's remaining arms all render fg, which the diff-anchor pair
// above already floors — so every colour that reaches the band is covered.
//
// The names carry no site gloss, and that is deliberate rather than terse. They are the
// text of a refusal a user reads in a modal that wraps at 72 cells, and a palette
// missing three floors used to render as a clipped "…; fg on…". The sites are named in
// this comment, where there is room and where someone changing the table is already
// looking.
var pairFloors = []struct {
	name  string
	floor float64
	fg    func(Palette) Color
	bg    func(Palette) Color
}{
	{"badge_fg on badge_bg", 4.5, func(p Palette) Color { return p.BadgeFg }, func(p Palette) Color { return p.BadgeBg }},
	{"bg on accent", 4.5, func(p Palette) Color { return p.Bg }, func(p Palette) Color { return p.Accent }},
	{"bg on attention", 4.5, func(p Palette) Color { return p.Bg }, func(p Palette) Color { return p.Attention }},
	{"fg on bg_elevated", 4.5, func(p Palette) Color { return p.Fg }, func(p Palette) Color { return p.BgElevated }},
	{"fg on bar_bg", 4.5, func(p Palette) Color { return p.Fg }, func(p Palette) Color { return p.BarBg }},
	{"success on bar_bg", glyphFloor, func(p Palette) Color { return p.Success }, func(p Palette) Color { return p.BarBg }},
	{"attention on bar_bg", glyphFloor, func(p Palette) Color { return p.Attention }, func(p Palette) Color { return p.BarBg }},
	{"pending on bar_bg", glyphFloor, func(p Palette) Color { return p.Pending }, func(p Palette) Color { return p.BarBg }},
}

// Violation is one measured miss: what was checked, what it measured, and what it had
// to clear.
type Violation struct {
	// Name is the token's on-disk name, or the pair's prose description.
	Name  string
	Got   float64
	Floor float64
}

// Error names the miss in the vocabulary of the file that caused it, so a refused user
// theme reports the key its author has to change.
//
// Compact because several are joined into one line of a modal that wraps at 72 cells:
// the token, what it measured, and what it had to clear is all three facts, and the
// long form ("contrast is 1.10, below the 4.50 floor for its role") spent 30 cells per
// violation restating the same sentence.
func (v Violation) Error() string {
	return fmt.Sprintf("%s: contrast %.2f, floor %.2f", v.Name, v.Got, v.Floor)
}

// Validate reports every floor a palette misses — never just the first.
//
// Reporting all of them is the whole ergonomics of the thing: tuning a palette one
// refusal per round is what an early-return version would cost, and it is the reason
// contrast_test.go's per-token checks used assert rather than require before this
// function existed.
//
// The order is deterministic — tokens in declaration order, then pairs in table order
// — because a map iteration would make an error message a user is meant to work
// through reshuffle on every run.
func Validate(p Palette) []Violation {
	var out []Violation
	for _, t := range paletteTokens {
		floor, ok := tokenFloors[t.name]
		if !ok {
			continue
		}
		if got := ContrastRatio(*t.at(&p), p.Bg); got < floor {
			out = append(out, Violation{Name: t.name, Got: got, Floor: floor})
		}
	}
	for _, pair := range pairFloors {
		if got := ContrastRatio(pair.fg(p), pair.bg(p)); got < pair.floor {
			out = append(out, Violation{Name: pair.name, Got: got, Floor: pair.floor})
		}
	}
	return out
}
