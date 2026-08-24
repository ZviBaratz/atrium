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
	// The floor for a single width-1 mark, matching ui/theme's glyph tier (the agent
	// brand accents and the bar pair floors are both set there). Spelled as a number
	// because ui/theme keeps its constant unexported; the two are held together by
	// ui/theme's TestBarBandColoursAreFloored, which names the same pairs.
	const glyphFloor = 3.0

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

// TestBarStateKeepsTheStatesDistinguishable is the constraint the fix had to respect
// while moving four arms onto one token: the states that carried distinct colours
// before still do. Ready, NeedsInput and Pending are the three the header exists to
// tell apart at a glance — the glyph colour is the only state signal up there.
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
