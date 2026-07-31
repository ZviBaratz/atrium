package theme

import "charm.land/lipgloss/v2"

// light.go carries the light-background palettes.
//
// They are ordinary registered themes, not a mode: selectable by name today, and
// what `theme: auto` resolves to on a light terminal once detection exists
// (#394 Stage E). Registering them normally is what gets them the existing guards
// for free — canonical-hex validation, glyph widths, the settings picker, and the
// contrast oracle.
//
// The token that needs the most care is Bg. Atrium never paints a full-screen
// background, so Palette.Bg is not a fill: it is "the colour of the void", used as
// a FOREGROUND on filled chips (ui/overlay/styles.go's selected row,
// app/banner.go's notice) and as the fade's substitute background
// (ui/overlay/overlay.go). On a light palette it therefore has to be near-white AND
// the Accent and Attention it sits on have to be dark enough to carry it — which is
// what pairFloors in contrast_test.go asserts.
//
// HOW THESE VALUES WERE CHOSEN, because "from upstream" is only half true and the
// other half is the interesting half.
//
// Each token starts at its counterpart in the canonical upstream light palette —
// folke/tokyonight.nvim's `day` and catppuccin's `latte`. Upstream is the source of
// hue identity, not of the shipped hex: neither light palette clears the contrast
// oracle as published, because both are tuned for prose in an editor rather than
// for a UI whose floors were set from two dark themes people have read for months.
// Measured against contrast_test.go, `day` misses eight token floors and all five
// pair floors; `latte` misses six and three.
//
// So: verbatim upstream wherever a verbatim token passes, and where it does not,
// the same colour with its HSL lightness lowered — hue and saturation preserved —
// to the minimum that clears the binding constraint plus a small margin. Every
// derived token names the upstream colour it came from. That keeps the palettes
// recognisably tokyonight-day and latte while making them pass a bar their
// upstreams do not aim at.
//
// The binding constraint is usually not the absolute floor. It is
// TestLightPaletteMatchesItsDarkTwin's lower band — a light token must hold at
// least 55% of its dark twin's ratio — and for several tokens that demands more
// contrast than the 4.5 floor does. latte's Attention is the extreme case: mocha's
// is 12.91:1, so the band asks for 7.10 where the floor asks for 4.5.
//
// Sources: `day` is COMPUTED upstream (lua/tokyonight/colors/day.lua inverts the
// night palette through Util.invert), so there is no authored table to copy; the
// resolved values below were read from the generated extras
// (extras/lua/tokyonight_day.lua, extras/kitty/tokyonight_day.conf). `latte` is
// from catppuccin/palette's palette.json.

// tokyoNightDay is the light twin of tokyoNight: folke/tokyonight.nvim's "day"
// palette, mapped onto Atrium's semantic tokens and darkened where the oracle
// requires it (see the file comment).
var tokyoNightDay = &Theme{
	Name: "tokyo-night-day",
	Palette: Palette{
		Bg:         lipgloss.Color("#e1e2e7"), // bg
		BgElevated: lipgloss.Color("#c4c8da"), // bg_highlight
		BarBg:      lipgloss.Color("#a8aecb"), // fg_gutter
		// fg #3760bf darkened. The forcing constraint is the pair floor Fg-on-BarBg,
		// not Fg's own: upstream's fg is 4.52 against bg (it clears 4.5 by a hair) but
		// only 2.67 against fg_gutter, where the diff anchor renders it. With BarBg at
		// its own floor, Fg has to reach ~7.6 against bg for that pair to hold.
		Fg:      lipgloss.Color("#243f7e"),
		FgDim:   lipgloss.Color("#6172b0"), // fg_dark
		FgFaint: lipgloss.Color("#a8aecb"), // fg_gutter — == BarBg, as in both dark themes
		Accent:  lipgloss.Color("#155fc4"), // blue #2e7de9 darkened (3.11 -> 4.68)
		// blue0 #7890dd darkened, and only barely: upstream is 2.38 against a 2.40
		// floor. The nudge is two hundredths of a ratio, kept rather than rounded away
		// because a token sitting under its floor is under it.
		AccentMuted: lipgloss.Color("#718adb"),
		// day's `purple`, verbatim. The role-faithful choice would be `magenta`
		// #9854f1 — tokyo-night's Purple is night's magenta — but day's magenta is 3.33
		// and would have to be derived, while day's purple passes as published at 4.73.
		// A real upstream colour beats a derived one.
		Purple: lipgloss.Color("#7847bd"),
		// green #587539 darkened (4.04 -> 5.35). SuccessDim keeps the undarkened
		// upstream green, which lands at 4.04 against its own 3.0 floor — so Success
		// stays the stronger of the two, which is the whole point of the pair.
		Success:    lipgloss.Color("#49612f"),
		SuccessDim: lipgloss.Color("#587539"), // green, verbatim
		Working:    lipgloss.Color("#6172b0"), // fg_dark — matches FgDim: working rows recede
		Pending:    lipgloss.Color("#005c7c"), // cyan #007197 darkened: calm cyan, distinct from Working/Success/Attention
		// orange #b15c00 darkened, not yellow. Deriving from day's `yellow` #8c6c3e
		// gives #785d35, which clears the floor by 0.25 and reads as khaki; the orange
		// is warmer, holds the attention character, and lands at 5.40. Both are
		// upstream colours, so this is a choice between them rather than an invention.
		Attention: lipgloss.Color("#8a4800"),
		Danger:    lipgloss.Color("#b13636"), // red1/error #c64343 darkened (3.79 -> 4.71)
		Cyan:      lipgloss.Color("#005c7c"), // cyan darkened — == Pending
		BadgeBg:   lipgloss.Color("#7847bd"), // == Purple
		BadgeFg:   lipgloss.Color("#e1e2e7"), // == Bg
	},
	Glyphs:  plainGlyphs(),
	Borders: Borders{Style: lipgloss.RoundedBorder()},
}

// catppuccinLatte is the light twin of catppuccinMocha: catppuccin's "latte"
// flavour, mapped onto Atrium's semantic tokens and darkened where the oracle
// requires it (see the file comment).
var catppuccinLatte = &Theme{
	Name: "catppuccin-latte",
	Palette: Palette{
		Bg:         lipgloss.Color("#eff1f5"), // base
		BgElevated: lipgloss.Color("#ccd0da"), // surface0, matching mocha's role
		BarBg:      lipgloss.Color("#bcc0cc"), // surface1, matching mocha's role
		Fg:         lipgloss.Color("#494c65"), // text #4c4f69 darkened, for Fg-on-BarBg (4.39 -> 4.61)
		// subtext0, NOT overlay0. mocha's FgDim is overlay0, but latte's overlay0
		// #9ca0b0 is 2.30 against a 2.40 floor — the role survives the crossing, the
		// palette-slot name does not.
		FgDim:   lipgloss.Color("#6c6f85"),
		FgFaint: lipgloss.Color("#bcc0cc"), // surface1 — == BarBg, as in both dark themes
		Accent:  lipgloss.Color("#125ef4"), // blue #1e66f5 darkened (4.34 -> 4.72)
		// sapphire #209fb5 darkened. This ends up HIGHER contrast than Accent (5.00 vs
		// 4.72), which reads backwards for a token called "muted" — it is inherited
		// from mocha, whose AccentMuted is sapphire at 8.69 while tokyo-night's is
		// 2.56. contrast_test.go's tier note already calls that spread out. The twin
		// band is measured per pair, so latte has to answer mocha's number, not
		// tokyo-night's.
		AccentMuted: lipgloss.Color("#177181"),
		Purple:      lipgloss.Color("#8839ef"), // mauve, verbatim
		Success:     lipgloss.Color("#28641b"), // green #40a02b darkened (2.96 -> 6.34)
		SuccessDim:  lipgloss.Color("#3e9b2a"), // green darkened just to its own 3.0 floor, so Success stays the stronger
		Working:     lipgloss.Color("#6c6f85"), // subtext0 — matches FgDim: working rows recede
		Pending:     lipgloss.Color("#026187"), // sky #04a5e5 darkened: calm cyan, distinct from Working/Success/Attention
		// yellow #df8e1d darkened, and the largest departure in either palette:
		// upstream is 2.31 while mocha's Attention is 12.91, so the twin band's lower
		// bound asks for 7.10. A light-background amber that carries near-white text on
		// top of it has nowhere else to go. That is the band working, not a mis-tune.
		Attention: lipgloss.Color("#6d450e"),
		Danger:    lipgloss.Color("#d20f39"), // red, verbatim
		Cyan:      lipgloss.Color("#026187"), // sky darkened — == Pending
		BadgeBg:   lipgloss.Color("#8839ef"), // == Purple
		BadgeFg:   lipgloss.Color("#eff1f5"), // == Bg
	},
	Glyphs:  plainGlyphs(),
	Borders: Borders{Style: lipgloss.RoundedBorder()},
}

// lightTwin maps a dark theme to its light counterpart. It is what `theme: auto`
// walks once detection exists (#394 Stage E); until then it is the statement of
// which pairs are pairs, kept here beside the palettes so a light theme added later
// cannot be registered without deciding what it is the twin of.
//
// It is also what TestLightPaletteMatchesItsDarkTwin reads, which is what makes the
// pairing an assertion rather than a comment: a light palette is held to its twin's
// measured contrast, so the dark themes are the specification and there is no taste
// constant in the interesting direction.
//
// Only the DEFAULT family's pair is reachable from `auto`, by design: a
// catppuccin-mocha user wanting adaptivity selects `auto` and gets tokyo-night.
// Per-family adaptivity would need a second config field and would create an
// invalid-combination space (a family with no twin, asked to go light).
var lightTwin = map[string]string{
	"tokyo-night":      "tokyo-night-day",
	"catppuccin-mocha": "catppuccin-latte",
}
