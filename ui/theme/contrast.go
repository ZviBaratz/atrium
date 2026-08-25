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
// admired at review time (#813). ui/theme/themefile is the production caller, the one
// that decides whether a file loads; the tests that call it are this package's own sweep
// over the built-ins, themefile's, and app/frameparity_test.go, which asserts its user
// theme fixture is a palette the loader would accept before pinning a golden from it.
//
// The floors were DERIVED from the minimum across the shipped dark themes with margin,
// which is their provenance rather than a live description: the light palettes landed
// later and are now what binds several of them (bar_bg's 1.6 is the tightest — latte
// measures 1.6079 against it, half a percent of headroom). This is not an accessibility
// certification — the tokens Atrium deliberately renders faint would fail WCAG AA and
// should — it is a floor under "did someone pick a colour nobody can see".
//
// Since #813 the oracle is also applied to palettes nobody reviewed, which changes what
// a tight floor costs: a user extending catppuccin-latte and darkening bg by one unit
// per channel is refused for bar_bg, a token their file never mentions. That is the
// floor doing its job on an inherited value, and Violation.Error says which token —
// but see its note: the key named is not always a key the file wrote.

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
// a written-down entry rather than to silence: bg is the reference itself, and the
// badge pair is floored against each other and against Bg under pairFloors instead —
// badge_bg is a FILL (Theme.BadgeStyle sets it as a Background), so a floor measuring
// its foreground contrast with Bg would be asking the wrong question of it.
var tokenFloors = map[string]float64{
	"fg":           textFloor,
	"accent":       textFloor,
	"purple":       textFloor,
	"success":      textFloor,
	"pending":      textFloor,
	"attention":    textFloor,
	"danger":       textFloor,
	"cyan":         textFloor,
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
	"badge_bg": "a fill, not a foreground: floored against bg and badge_fg as pairs below",
	"badge_fg": "meets badge_bg, not the void: floored as a pair below",
}

// textFloor is the tier for anything meant to be READ — the status and text tokens, and
// every pair where one sits directly on the other. Named rather than repeated as a
// literal so a test can assert which tier a given entry is at: the four bar-band pairs
// deliberately span both tiers, and a comment saying so is not something a reader can
// check (TestBarBandColoursAreFloored does).
const textFloor = 4.5

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
// Three of them are that bar's status glyphs (ui/contextbar.go's barState), added with
// #555's fix. Their tier is glyphFloor, not 4.5: on the shipped light palettes these dip
// as low as 3.16, so a 4.5 floor would refuse two themes that have been legible for
// months — while 4.5 was never the issue, a fg_dim-valued token at 1.44 was. barState's
// remaining arms all render fg, which the diff-anchor pair above already floors.
//
// That covers every PALETTE TOKEN the band can carry, which is not the same as every
// colour on it. ComposeSessionContext paints one more mark there — the agent brand
// accent from Theme.AgentGlyph — and those two colours are not palette tokens at all,
// so no floor here reaches them. They are unfloored against bar_bg on purpose and not
// by oversight: measured on the band they run 2.86/2.51 (tokyo-night), 2.92/2.56
// (mocha), 1.95/1.97 (tokyo-night-day) and 2.35/2.37 (latte), and the `generic`
// fallback is Palette.FgDim, i.e. the 1.44 #555 removed. Adopting glyphFloor for them
// would refuse all five shipped themes, so the fix is to change those colours rather
// than to assert about them, and that is #855 rather than something smuggled in here.
// TestAgentBrandColoursStayLegible floors them against Bg, which is the surface the
// session LIST paints them on and the only one they clear today.
//
// badge_bg over bg is the AUTO chip's fill against the void. It is here rather than in
// tokenFloors because badge_bg is a background: it needs to be FINDABLE, not readable,
// so it takes glyphFloor, and the shipped palettes hold 4.73-8.07. Without it a user
// palette could set badge_bg to its own bg and lose the chip entirely while badge_fg
// on badge_bg still cleared 4.5.
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
	{"badge_fg on badge_bg", textFloor, func(p Palette) Color { return p.BadgeFg }, func(p Palette) Color { return p.BadgeBg }},
	{"badge_bg on bg", glyphFloor, func(p Palette) Color { return p.BadgeBg }, func(p Palette) Color { return p.Bg }},
	{"bg on accent", textFloor, func(p Palette) Color { return p.Bg }, func(p Palette) Color { return p.Accent }},
	{"bg on attention", textFloor, func(p Palette) Color { return p.Bg }, func(p Palette) Color { return p.Attention }},
	{"fg on bg_elevated", textFloor, func(p Palette) Color { return p.Fg }, func(p Palette) Color { return p.BgElevated }},
	{"fg on bar_bg", textFloor, func(p Palette) Color { return p.Fg }, func(p Palette) Color { return p.BarBg }},
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

// Error names the miss in the on-disk vocabulary, so a refused user theme reports a key
// its author can look up rather than a Go field name.
//
// Not necessarily a key their file WROTE. Every floor is measured on the RESOLVED
// palette, so a file that overrides bg alone can miss bar_bg's floor — the base
// theme's bar_bg against the new bg. The name is still the actionable one (that is the
// token to override next), but the message cannot promise the author has seen it before.
//
// Compact because it is rendered into a modal line that clips: the token, what it
// measured, and what it had to clear is all three facts, and the long form ("contrast is
// 1.10, below the 4.50 floor for its role") spent 30 cells restating the same sentence.
// `atrium doctor` prints one of these per miss, indented, which is the surface with room.
//
// Two decimals, and Validate refuses on the SAME two — see clears. Comparing the raw
// float while reporting a rounded one is how a refusal comes to read "contrast 4.50,
// floor 4.50", telling the author their colour meets the floor it was just refused for
// and offering no remedy they cannot already see is satisfied.
func (v Violation) Error() string {
	return fmt.Sprintf("%s: contrast %.2f, floor %.2f", v.Name, v.Got, v.Floor)
}

// clears reports whether a measured ratio meets a floor, at the precision Violation.Error
// prints. Rounding here rather than truncating in the message keeps the two agreeing in
// both directions: nothing is refused that the report will show as passing, and nothing
// passes that it would show as short.
//
// It loosens every floor by up to 0.005, which is below the precision any of them were
// chosen at — they are round numbers from WCAG tiers, not measurements.
func clears(got, floor float64) bool {
	return math.Round(got*100)/100 >= floor
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
		if got := ContrastRatio(*t.at(&p), p.Bg); !clears(got, floor) {
			out = append(out, Violation{Name: t.name, Got: got, Floor: floor})
		}
	}
	for _, pair := range pairFloors {
		if got := ContrastRatio(pair.fg(p), pair.bg(p)); !clears(got, pair.floor) {
			out = append(out, Violation{Name: pair.name, Got: got, Floor: pair.floor})
		}
	}
	return out
}
