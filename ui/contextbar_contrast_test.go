package ui

import (
	"testing"

	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/ui/theme"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// contextbar_contrast_test.go is #555's guard, and it is the half ui/theme's oracle
// cannot write: contrast.go floors the token PAIRS the bar can paint, but only this
// package can say which tokens barState actually reaches for.
//
// The defect it pins: every state that recedes in the session list — Running, Loading,
// Paused, and the default arm — painted Palette.FgDim on the bar's BarBg band, which
// measures 1.44:1 on tokyo-night and 1.87:1 on catppuccin-mocha. Working carries the
// same hex as FgDim in every shipped palette, on purpose, so "Paused is dim" and
// "Running is dim" were one defect wearing two token names.

// barStates enumerates every session.Status plus a value past the end, so the switch's
// default arm — which is a real render, not a formality — is measured too.
func barStates() []session.Status {
	return []session.Status{
		session.Running, session.Ready, session.Loading,
		session.Paused, session.NeedsInput, session.Pending,
		session.Status(99),
	}
}

// TestBarStateIsLegibleOnTheBand measures what barState returns against the band it is
// drawn on, for every status and every registered palette.
//
// It measures the RESOLVED colour rather than asserting which token was chosen, which
// is the difference between this and a test that would have to be rewritten every time
// a state's colour is retuned. A future arm that picks a fresh dim grey fails here
// without anyone having to remember that #555 happened.
func TestBarStateIsLegibleOnTheBand(t *testing.T) {
	glyphFloor := barPairFloor(t)

	names := theme.Names()
	require.NotEmpty(t, names)
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			t.Cleanup(theme.Set(name))
			th := theme.Current()
			for _, st := range barStates() {
				_, colour := barState(st, th)
				got := theme.ContrastRatio(theme.ParseHex(colour), th.Palette.BarBg)
				assert.GreaterOrEqualf(t, got, glyphFloor,
					"%s: the %v glyph is %.2f against the bar band, below the %.2f floor",
					name, st, got, glyphFloor)
			}
		})
	}
}

// TestBarStateNeverPaintsARecedingToken is the same claim stated at the token level, so
// a regression names the cause rather than a ratio. FgDim and Working are the two
// tokens whose whole job is to sink into a list of neighbours; the band has no
// neighbours.
//
// Both are checked even though every shipped palette gives them the same hex: a user
// theme (#813) may set them apart, and then a check on one would pass while the other
// shipped.
func TestBarStateNeverPaintsARecedingToken(t *testing.T) {
	for _, name := range theme.Names() {
		t.Run(name, func(t *testing.T) {
			t.Cleanup(theme.Set(name))
			th := theme.Current()
			for _, st := range barStates() {
				_, colour := barState(st, th)
				assert.NotEqualf(t, theme.Hex(th.Palette.FgDim), colour,
					"%s: the %v glyph paints FgDim on the band (#555)", name, st)
				assert.NotEqualf(t, theme.Hex(th.Palette.Working), colour,
					"%s: the %v glyph paints Working on the band, which is FgDim by another name", name, st)
			}
		})
	}
}

// barPairFloor reads the bar-band GLYPH-tier floor out of ui/theme, rather than restating
// it here as a literal.
//
// The literal it replaces claimed to be held to ui/theme's constant by
// TestBarBandColoursAreFloored, which was false twice over: that test builds a set of pair
// NAMES and never reads pair.floor, and the four bar pairs do not even share one number —
// `fg on bar_bg` is floored at the 4.5 text tier (it is also the diff anchor), the other
// three at the 3.0 tier for a single width-1 mark. A lone 3.0 here was therefore looser
// than ui/theme's own table for four of the seven arms, which is safe but was not what the
// comment said.
//
// So this probes `success on bar_bg`, the glyph tier, and the bound this file enforces is
// deliberately the weaker of the two: "no arm is below the tier for a single mark".
// ui/theme's table is what holds fg to 4.5, and TestBarBandColoursAreFloored now reads the
// floors rather than only the names.
func barPairFloor(t *testing.T) float64 {
	t.Helper()
	p := theme.Get(theme.DefaultThemeName).Palette
	p.Success = p.BarBg // ratio 1.0 — below any floor this tier could hold
	for _, v := range theme.Validate(p) {
		if v.Name == "success on bar_bg" {
			return v.Floor
		}
	}
	t.Fatal("ui/theme no longer floors `success on bar_bg`; #555's fix has lost its oracle")
	return 0
}

// TestBarStateKeepsTheStatesDistinguishable is the constraint the fix had to respect
// while moving four arms onto one token: the states that carried distinct colours
// before still do. Ready, NeedsInput and Pending are the three the header exists to
// tell apart at a glance, and they are the only three whose colour still says anything.
func TestBarStateKeepsTheStatesDistinguishable(t *testing.T) {
	t.Cleanup(theme.Set(theme.DefaultThemeName))
	th := theme.Current()

	seen := map[string]session.Status{}
	for _, st := range []session.Status{session.Ready, session.NeedsInput, session.Pending, session.Running} {
		_, colour := barState(st, th)
		prev, dup := seen[colour]
		assert.Falsef(t, dup, "%v and %v are the same colour on the band", prev, st)
		seen[colour] = st
	}
}

// TestBarStateColourSaysSomethingOrTheGlyphDoes is the invariant #555's fix created and
// nothing covered: barState now paints four arms in Palette.Fg, which is exactly what
// barStyleColours hands tmux as the BAND's own foreground. So for those four the colour
// is not a signal at all — it is the same ink as the repo name beside them — and the
// glyph is the whole message.
//
// Both halves are asserted because either one alone passes the bug. Legibility
// (TestBarStateIsLegibleOnTheBand) passes if every state is Fg; distinguishability
// (TestBarStateKeepsTheStatesDistinguishable) passes if the three signal states merely
// differ from each other, including the case where one has quietly become Fg too.
func TestBarStateColourSaysSomethingOrTheGlyphDoes(t *testing.T) {
	for _, name := range theme.Names() {
		t.Run(name, func(t *testing.T) {
			t.Cleanup(theme.Set(name))
			th := theme.Current()
			bandFg := theme.Hex(th.Palette.Fg)

			// The three the header exists to catch the eye with must not be the band's own
			// foreground: a state that becomes Fg joins the neutral group silently, and no
			// other test in this file can tell.
			for _, st := range []session.Status{session.Ready, session.NeedsInput, session.Pending} {
				_, colour := barState(st, th)
				assert.NotEqualf(t, bandFg, colour,
					"%v rides the band's own foreground, so its colour signals nothing", st)
			}

			// And the neutral arms, whose colour signals nothing by design, must be told
			// apart by shape where they are meant to be told apart at all. Running and
			// Loading deliberately share both (one "working" marker); Paused and the empty
			// default must not join them.
			running, _ := barState(session.Running, th)
			paused, _ := barState(session.Paused, th)
			blank, _ := barState(session.Status(99), th)
			assert.NotEqualf(t, running, paused,
				"paused and running share the band's foreground, so an identical glyph leaves nothing")
			assert.NotEqual(t, running, blank, "the default arm must not read as a working session")
		})
	}
}
