package theme

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// contrast_test.go is the oracle for the one property a rendering test cannot
// see: whether the palette is READABLE. Every other theme guard here asks about
// width, shape or vocabulary; none of them would notice a foreground that
// disappeared into the background, which is exactly how three dark-tuned themes
// shipped as the only options (#394).
//
// The floors are set from the MINIMUM across the shipped dark themes with margin,
// so this lands green on what exists today and constrains what is added next. It
// is not an accessibility certification — the tokens Atrium deliberately renders
// faint would fail WCAG AA and should — it is a floor under "did someone pick a
// colour nobody can see".
//
// The per-token checks below use assert, not require, on purpose: a palette that
// misses is meant to report EVERY token it misses in one run. Tightening these to
// require would abort at the first one and turn tuning a new palette into one
// round per token.

// relLuminance is WCAG 2.1's relative luminance. color.Color reports 16-bit
// alpha-premultiplied components, which for the opaque palette colours Atrium
// ships is the 8-bit value repeated (0xNN * 0x101), so >>8 recovers the byte.
func relLuminance(c Color) float64 {
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

// contrastRatio is WCAG 2.1's contrast ratio: 1.0 for two identical colours,
// 21.0 for black on white. Order-independent.
func contrastRatio(a, b Color) float64 {
	la, lb := relLuminance(a), relLuminance(b)
	hi, lo := math.Max(la, lb), math.Min(la, lb)
	return (hi + 0.05) / (lo + 0.05)
}

// tokenFloor is the minimum contrast a palette token must hold against its own
// theme's Bg. Bg is the reference because Atrium never paints a full-screen
// background — Palette.Bg means "the colour of the void", i.e. what the terminal
// itself shows — so a token's legibility is its ratio against it.
//
// The tiers are roles, not tastes. Status and text tokens carry meaning and get
// 4.5. SuccessDim gets its own 3.0: it is a status colour, but the dimming that
// marks a Ready session as already seen costs it the 4.5 tier by a hair — 4.35 on
// tokyo-night against 4.60 on mocha — so it holds WCAG's large-text/non-text
// threshold instead of a floor one shipped theme already misses. The three tokens
// Atrium deliberately recedes (FgDim, Working, and AccentMuted, whose
// 2.56-on-tokyo-night to 8.69-on-mocha spread is why it cannot share Accent's
// floor) get 2.4. FgFaint and BarBg are the faint slate — in both shipped themes
// they are literally the SAME colour, a deliberate choice, not a defect — so they
// get 1.6. BgElevated is a selection fill that must merely be distinguishable, so
// 1.1.
var tokenFloors = map[string]struct {
	floor float64
	get   func(Palette) Color
}{
	"Fg":          {4.5, func(p Palette) Color { return p.Fg }},
	"Accent":      {4.5, func(p Palette) Color { return p.Accent }},
	"Purple":      {4.5, func(p Palette) Color { return p.Purple }},
	"Success":     {4.5, func(p Palette) Color { return p.Success }},
	"Pending":     {4.5, func(p Palette) Color { return p.Pending }},
	"Attention":   {4.5, func(p Palette) Color { return p.Attention }},
	"Danger":      {4.5, func(p Palette) Color { return p.Danger }},
	"Cyan":        {4.5, func(p Palette) Color { return p.Cyan }},
	"SuccessDim":  {3.0, func(p Palette) Color { return p.SuccessDim }},
	"FgDim":       {2.4, func(p Palette) Color { return p.FgDim }},
	"Working":     {2.4, func(p Palette) Color { return p.Working }},
	"AccentMuted": {2.4, func(p Palette) Color { return p.AccentMuted }},
	"FgFaint":     {1.6, func(p Palette) Color { return p.FgFaint }},
	"BarBg":       {1.6, func(p Palette) Color { return p.BarBg }},
	"BgElevated":  {1.1, func(p Palette) Color { return p.BgElevated }},
}

// pairFloors are the token pairs that meet each other directly rather than over
// Bg, each at the site that renders them. Every one of these is a real render,
// named here in the order the table lists them: BadgeFg over BadgeBg is the
// per-session AUTO chip (ui/list_render.go, and its legend in app/help.go),
// Foreground(Bg) over Background(Accent) is the picker's selected row
// (ui/overlay/styles.go), over Background(Attention) is the notice banner
// (app/banner.go), Fg over BgElevated is the selected list row (ui/row.go), and
// Fg over BarBg is the diff anchor (ui/diff_anchor.go).
var pairFloors = []struct {
	name  string
	floor float64
	fg    func(Palette) Color
	bg    func(Palette) Color
}{
	{"BadgeFg on BadgeBg", 4.5, func(p Palette) Color { return p.BadgeFg }, func(p Palette) Color { return p.BadgeBg }},
	{"Bg on Accent (selected row)", 4.5, func(p Palette) Color { return p.Bg }, func(p Palette) Color { return p.Accent }},
	{"Bg on Attention (banner)", 4.5, func(p Palette) Color { return p.Bg }, func(p Palette) Color { return p.Attention }},
	{"Fg on BgElevated (selected list row)", 4.5, func(p Palette) Color { return p.Fg }, func(p Palette) Color { return p.BgElevated }},
	{"Fg on BarBg (diff anchor)", 4.5, func(p Palette) Color { return p.Fg }, func(p Palette) Color { return p.BarBg }},
}

// KNOWN, DELIBERATELY UNASSERTED: FgDim on BarBg. ui/contextbar.go's barState
// renders Paused and the default state in FgDim on the bar's band, which is
// 1.44:1 on tokyo-night and 1.87:1 on catppuccin-mocha — while contextbar.go:59's
// own comment says "dim greys wash out" there. It is a real legibility defect on
// the DARK themes, found by this oracle while it was being written, and it is
// filed rather than fixed here: fixing it means choosing a new colour for a
// state, which is a design decision and not #394's subject. Do not add it to
// pairFloors without fixing barState in the same change.

// TestPaletteContrastFloors holds every registered palette to the floors above.
// Iterating Names() rather than a hand-listed table is deliberate: a theme
// registered later is covered without anyone remembering to add it here.
//
// Three names register today but only two palettes are distinct — unicode reuses
// tokyo-night's and varies only its borders — which is why the header counts three
// themes while the tier note says "both", and why breaking a tokyo-night token
// reports two failing subtests rather than one.
func TestPaletteContrastFloors(t *testing.T) {
	names := Names()
	require.NotEmpty(t, names)

	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			p := Get(name).Palette
			for token, spec := range tokenFloors {
				got := contrastRatio(spec.get(p), p.Bg)
				assert.GreaterOrEqualf(t, got, spec.floor,
					"%s: %s contrast against Bg is %.2f, below the %.2f floor for its role",
					name, token, got, spec.floor)
			}
			for _, pair := range pairFloors {
				got := contrastRatio(pair.fg(p), pair.bg(p))
				assert.GreaterOrEqualf(t, got, pair.floor,
					"%s: %s contrast is %.2f, below the %.2f floor",
					name, pair.name, got, pair.floor)
			}
		})
	}
}

// TestAgentBrandColoursStayLegible covers the only two colours in the whole app
// that are NOT palette tokens: ui/theme/agent.go's Claude and Gemini brand
// accents, documented there as theme-independent. Theme-independent means every
// palette has to carry them, so a palette they vanish against is the palette's
// problem — and without this they would be the one pair of colours no oracle
// looks at.
func TestAgentBrandColoursStayLegible(t *testing.T) {
	const brandFloor = 3.0 // glyphs, not prose: a single mark at width 1
	require.NotEmpty(t, agentColors)
	for _, name := range Names() {
		p := Get(name).Palette
		for key, c := range agentColors {
			got := contrastRatio(c, p.Bg)
			assert.GreaterOrEqualf(t, got, brandFloor,
				"%s: the %s brand glyph is %.2f against Bg, below the %.2f floor",
				name, key, got, brandFloor)
		}
	}
}
