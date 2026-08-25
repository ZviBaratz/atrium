package theme

import (
	"fmt"
	"testing"

	"charm.land/lipgloss/v2"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Note on relLuminanceOf: it started here and now lives in scheme.go, because
// IsLight needs the same arithmetic and production code cannot call a test file.
// Deliberately moved rather than copied — two implementations of "how bright is
// this" would let the contrast oracle and the splash's polarity decision disagree
// about a palette in the middle, silently.
//
// The oracle itself has now made the same journey for the same reason and lives in
// contrast.go: a user theme file has to be validated at LOAD time, not at review time
// (#813). The floors, the tiers and the reasoning behind them are all there; this file
// is one of its two callers, holding the shipped palettes to it. The other is
// ui/theme/themefile.
//
// Validate reports every miss rather than the first, which preserves the property the
// per-token assert loop here used to provide: a palette that misses is meant to report
// EVERY token it misses in one run, or tuning a new palette costs one round per token.
// Where a check below still loops it uses assert, not require, for the same reason.

// TestPaletteContrastFloors holds every registered palette to the oracle. Iterating
// Names() rather than a hand-listed table is deliberate: a theme registered later is
// covered without anyone remembering to add it here — which since #813 includes a
// theme the user wrote, the kind no human reviewed.
//
// Five names register today but only four palettes are distinct — unicode reuses
// tokyo-night's and varies only its borders — which is why breaking a tokyo-night
// token reports two failing subtests rather than one.
func TestPaletteContrastFloors(t *testing.T) {
	names := Names()
	require.NotEmpty(t, names)

	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			for _, v := range Validate(Get(name).Palette) {
				assert.Failf(t, "palette token below its floor", "%s: %s", name, v.Error())
			}
		})
	}
}

// TestEveryTokenIsFlooredOrExempt is the bidirectional guard over the oracle's own
// tables: every palette token either carries a floor or is named in the exemption map
// with a reason, and nothing in either map is a token that does not exist.
//
// Without it a nineteenth token would be added to Palette and checked by nothing —
// the failure mode the settings panel's reflection guard exists to prevent for Config
// fields, in the one other place a struct is the source of a vocabulary.
func TestEveryTokenIsFlooredOrExempt(t *testing.T) {
	known := map[string]bool{}
	for _, name := range TokenNames() {
		known[name] = true
		_, floored := tokenFloors[name]
		reason, exempt := tokenFloorExempt[name]
		assert.Truef(t, floored != exempt,
			"token %q must have exactly one of a floor and an exemption (floored=%v, exempt=%v)",
			name, floored, exempt)
		if exempt {
			assert.NotEmptyf(t, reason, "token %q is exempt with no reason", name)
		}
	}
	for name := range tokenFloors {
		assert.Truef(t, known[name], "tokenFloors names %q, which is not a palette token", name)
	}
	for name := range tokenFloorExempt {
		assert.Truef(t, known[name], "tokenFloorExempt names %q, which is not a palette token", name)
	}
}

// TestValidateReportsEveryMiss holds Validate to the property the refusal message
// depends on: a palette that misses several floors reports all of them, so a user
// tuning one does not pay a round trip per token.
//
// The fixture drops Fg, Accent and Purple onto the background — three distinct TOKEN
// floors, so the count cannot be satisfied by pair violations alone.
func TestValidateReportsEveryMiss(t *testing.T) {
	p := Get(DefaultThemeName).Palette
	p.Fg, p.Accent, p.Purple = p.Bg, p.Bg, p.Bg

	names := map[string]bool{}
	for _, v := range Validate(p) {
		names[v.Name] = true
		assert.Lessf(t, v.Got, v.Floor,
			"%s reported as a violation but measures %.2f against its %.2f floor", v.Name, v.Got, v.Floor)
	}
	for _, want := range []string{"fg", "accent", "purple"} {
		assert.Truef(t, names[want], "a palette with %s == bg must report %s; got %v", want, want, names)
	}
}

// TestValidatePassesEveryShippedPalette is the negative control for the test above:
// the oracle is not simply failing everything handed to it.
func TestValidatePassesEveryShippedPalette(t *testing.T) {
	for _, name := range BuiltinNames() {
		assert.Emptyf(t, Validate(Get(name).Palette), "%s is shipped and must clear its own floors", name)
	}
}

// TestBarBandColoursAreFloored pins #555's fix to what it was about: every colour
// ui/contextbar.go's barState can paint onto the bar's BarBg band has a pair floor.
//
// It names the tokens rather than calling barState, which it cannot reach without an
// import cycle — so this is a claim about another file, and the rule for those applies:
// when barState changes, open it and check this list. Today it paints fg for the
// neutral states (running, loading, paused, default), success for ready, attention for
// needs-input and pending for pending. Before #813 the neutral arms painted fg_dim,
// which is 1.44:1 on tokyo-night, and no pair floor covered the band at all.
func TestBarBandColoursAreFloored(t *testing.T) {
	floored := map[string]float64{}
	for _, pair := range pairFloors {
		floored[pair.name] = pair.floor
	}
	// The floor VALUE, not merely the name. ui/contextbar_contrast_test.go asserts the
	// same band from the other side of the import boundary and cannot see this constant;
	// it reads the number back out through Validate, so a tier changed here without
	// changing it there is a divergence, not a silent pass. Naming the expected tier per
	// pair is also what says out loud that these four are NOT one number: the neutral
	// arms ride fg, which is the band's own foreground and floored as text.
	for name, want := range map[string]float64{
		"fg on bar_bg":        textFloor,
		"success on bar_bg":   glyphFloor,
		"attention on bar_bg": glyphFloor,
		"pending on bar_bg":   glyphFloor,
	} {
		got, ok := floored[name]
		if assert.Truef(t, ok, "barState paints this on the band, unfloored: %s", name) {
			assert.Equalf(t, want, got, "%s is floored at %.2f, not the %.2f tier it is documented at", name, got, want)
		}
	}
	// The token that used to be there must not come back. Its own floor is 2.4 against
	// Bg, which says nothing about the band — so a pair under this name would mean
	// someone had floored the receding value rather than stopped painting it.
	_, fgDimFloored := floored["fg_dim on bar_bg"]
	assert.Falsef(t, fgDimFloored,
		"fg_dim is floored against the band, so barState is painting a receding token there again")
}

// TestTheAutoChipSurvivesItsOwnBackground covers what the badge pair alone does not: the
// chip is a FILL, and badge_fg-on-badge_bg stays perfect while badge_bg walks into Bg and
// the chip stops existing. Palette.BadgeBg is set as a Background by Theme.BadgeStyle,
// which is why its exemption from tokenFloors is not an exemption from being checked.
//
// The fixture is the failure the exemption's old reason ("meets badge_fg, not the void")
// let through verbatim: badge_bg set to the palette's own bg, with a badge_fg that still
// clears 4.5 against it.
func TestTheAutoChipSurvivesItsOwnBackground(t *testing.T) {
	p := Get(DefaultThemeName).Palette
	p.BadgeBg, p.BadgeFg = p.Bg, lipgloss.Color("#ffffff")

	require.GreaterOrEqual(t, ContrastRatio(p.BadgeFg, p.BadgeBg), 4.5,
		"the fixture must clear the badge PAIR, or it proves nothing about the fill")

	names := map[string]bool{}
	for _, v := range Validate(p) {
		names[v.Name] = true
	}
	assert.Truef(t, names["badge_bg on bg"],
		"a chip fill equal to the background must be refused; got %v", names)
}

// TestAgentBrandColoursStayLegible covers the only two colours in the whole app
// that are NOT palette tokens: ui/theme/agent.go's Claude and Gemini brand
// accents. Every palette has to carry them, so a palette they vanish against is the
// palette's problem — and without this they would be the one pair of colours no
// oracle looks at.
//
// It resolves through AgentGlyph rather than reading agentColors directly, which is
// the difference between testing a table and testing what renders. The tables are
// now two — the brand hex and its light-background form — and only AgentGlyph knows
// which one a given palette gets, so a check that read either map alone would pass
// while the wrong one shipped. Reading the resolved colour also means the light table
// is covered here with no second assertion.
//
// It iterates Names(), which since #813 CAN carry a user theme — but does not here: no
// theme file is loaded in a test process, so what this sweep covers is the shipped set.
// A user palette is checked at LOAD time by theme.Validate, which does not look at these
// two colours at all, so a user theme whose Bg swallows the Claude accent is not caught
// anywhere. That is a real hole and it is a pre-existing one: the same colours are
// unfloored against bar_bg for the shipped palettes too (see pairFloors). Both want the
// accents changed rather than an assertion added, and both are #855.
//
// The floor is against Bg, which is the session LIST's surface. The in-session tmux
// band paints them on bar_bg, where none of them clears it.
func TestAgentBrandColoursStayLegible(t *testing.T) {
	require.NotEmpty(t, agentColors)
	require.NotEmpty(t, agentColorsLight)
	for _, name := range Names() {
		th := Get(name)
		for key := range agentColors {
			_, c := th.AgentGlyph(key)
			got := ContrastRatio(c, th.Palette.Bg)
			assert.GreaterOrEqualf(t, got, glyphFloor,
				"%s: the %s brand glyph is %.2f against Bg, below the %.2f floor",
				name, key, got, glyphFloor)
		}
	}
}

// TestIsLightAgreesWithTheRegistry pins which shipped palettes are light. The
// predicate exists because three consumers outside this file need the same answer —
// the agent brand accents, the splash's brightness channel, and the scheme axis —
// and independent luminance thresholds would eventually disagree about a palette in
// the middle.
func TestIsLightAgreesWithTheRegistry(t *testing.T) {
	for _, name := range []string{"tokyo-night", "catppuccin-mocha", "unicode"} {
		assert.Falsef(t, IsLight(Get(name).Palette), "%s is a dark palette", name)
	}
	for _, name := range []string{"tokyo-night-day", "catppuccin-latte"} {
		assert.Truef(t, IsLight(Get(name).Palette), "%s is a light palette", name)
	}
}

// TestLightPaletteMatchesItsDarkTwin is the relative half of the oracle: rather
// than asserting a light palette hits absolute numbers someone picked, it asserts
// each light theme is AS READABLE AS the dark theme it twins. That removes the
// taste constant from the interesting direction — the dark themes are the ones
// people have actually read for months, so their ratios are the specification.
//
// The tolerance is wide on purpose. Matching a dark palette's contrast exactly on a
// light background is not achievable (light backgrounds compress the available
// range at the bright end), and a narrow band would make the test a colour-picker
// rather than a guard. What it catches is a token that is off by a FACTOR — the
// pastel that looked fine on slate and vanishes on paper. Both upstream light
// palettes fail it as published, which is why light.go's values are derived from
// upstream rather than copied from it.
//
// Only floored tokens are compared: an unfloored one (bg itself, the badge pair) has
// no role tier to be measured against, and bg's ratio to itself is 1.00 in both
// palettes, which would compare vacuously.
//
// assert, not require, inside the loop, matching this file's other per-theme
// checks: a mis-tuned palette should report every token it misses in one run rather
// than one round per token.
func TestLightPaletteMatchesItsDarkTwin(t *testing.T) {
	require.NotEmpty(t, lightTwin, "no pairs to check")

	// A light token may hold between 55% and 210% of its dark twin's ratio.
	const lo, hi = 0.55, 2.10

	for dark, light := range lightTwin {
		t.Run(dark+"->"+light, func(t *testing.T) {
			require.Equal(t, light, Get(light).Name,
				"lightTwin names a theme that is not registered under that name")
			require.Equal(t, dark, Get(dark).Name,
				"lightTwin is keyed by a theme that is not registered under that name")

			dp, lp := Get(dark).Palette, Get(light).Palette
			for _, token := range TokenNames() {
				if _, floored := tokenFloors[token]; !floored {
					continue
				}
				at := tokenAt(token)
				require.NotNilf(t, at, "TokenNames returned %q, which tokenAt does not know", token)
				dr := ContrastRatio(*at(&dp), dp.Bg)
				lr := ContrastRatio(*at(&lp), lp.Bg)
				ratio := lr / dr
				assert.Truef(t, ratio >= lo && ratio <= hi,
					"%s: %s holds %.2f contrast where %s holds %.2f (%.0f%% of it, outside %.0f-%.0f%%)",
					light, token, lr, dark, dr, ratio*100, lo*100, hi*100)
			}
		})
	}
}

// TestARefusalNeverReportsAPassingRatio is the self-consistency of the message a user is
// meant to act on: Validate refuses at the precision Violation.Error prints.
//
// Before `clears`, Validate compared the raw float while Error rendered %.2f, so a
// palette measuring 4.4999 was refused with "fg: contrast 4.50, floor 4.50" — a message
// telling its author their colour meets the floor it was just refused for, and offering
// as remedy the one thing they can see is already satisfied. Both tiers are driven,
// because the arithmetic is the same at either and the bug was not tier-specific.
func TestARefusalNeverReportsAPassingRatio(t *testing.T) {
	for _, name := range BuiltinNames() {
		for _, v := range Validate(Get(name).Palette) {
			require.Failf(t, "precondition", "%s is shipped and must not violate: %v", name, v)
		}
	}

	// Every ratio in the neighbourhood of a floor, from just under to just over. The
	// property is one implication: if it was refused, the printed ratio is BELOW the
	// printed floor. A truncating message would satisfy this too, which is why the
	// converse is asserted as well.
	for _, floor := range []float64{glyphFloor, textFloor, 1.6, 1.1} {
		for _, delta := range []float64{-0.02, -0.006, -0.005, -0.0049, -0.0001, 0, 0.0001, 0.02} {
			got := floor + delta
			v := Violation{Name: "probe", Got: got, Floor: floor}
			shown := v.Error()
			if clears(got, floor) {
				continue
			}
			assert.NotContainsf(t, shown, fmt.Sprintf("contrast %.2f, floor %.2f", floor, floor),
				"a refusal at %.6f reports the floor as met: %q", got, shown)
		}
		// And the converse: nothing that the message would show as short is allowed to pass.
		for _, delta := range []float64{-0.02, -0.006} {
			assert.Falsef(t, clears(floor+delta, floor),
				"%.6f rounds below %.2f and must be refused, or the report shows a miss that passed", floor+delta, floor)
		}
	}
}
