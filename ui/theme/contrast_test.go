package theme

import (
	"testing"

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
	floored := map[string]bool{}
	for _, pair := range pairFloors {
		floored[pair.name] = true
		// The token that used to be there must not come back: fg_dim's own floor is 2.4
		// against Bg, which says nothing about the band.
		assert.NotContainsf(t, pair.name, "fg_dim on bar_bg",
			"fg_dim is floored against the band, which means barState is painting a receding token there again")
	}
	for _, name := range []string{
		"fg on bar_bg",
		"success on bar_bg",
		"attention on bar_bg",
		"pending on bar_bg",
	} {
		assert.Truef(t, floored[name], "barState paints this on the band, unfloored: %s", name)
	}
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
// while the wrong one shipped. Reading the resolved colour also means the light
// table is covered here with no second assertion — and, since Names() carries user
// themes, so is a palette written by someone who never heard of these two colours.
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
