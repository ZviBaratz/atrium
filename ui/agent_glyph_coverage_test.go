package ui

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ZviBaratz/atrium/session/agent"
	"github.com/ZviBaratz/atrium/ui/theme"
)

// The registry -> glyph completeness guard (#670).
//
// ui/theme keys its agent identity table by plain strings so it stays a leaf
// package — ui/theme/agent.go's header comment says so — which means nothing inside
// ui/theme can ask session/agent what the keys actually are. TestAgentGlyphWidths,
// over there, iterates that table: it measures every entry that exists and is
// structurally silent about one that does not, so an adapter added to the registry
// without a glyph ships green and paints as the unknown-agent marker.
//
// This file is the half of the invariant the leaf package cannot state. It lives in
// package ui because ui already holds both halves — ui/row.go imports session/agent
// and ui/theme to paint the agent column — so the guard adds no import edge that
// production code does not already have, and none at all to ui/theme.
//
// Width is deliberately not re-asserted here. TestAgentGlyphWidths ranges over the
// table, so an entry added to satisfy this test is measured the day it lands; and a
// check from out here could only measure AgentGlyph's resolved output, never the
// table, which is the weaker of the two claims.

// agentMark is one resolved agent identity: what AgentGlyph paints for a key.
// AgentGlyph is all this package can see of ui/theme's table.
type agentMark struct {
	glyph string
	color theme.Color
}

func resolveAgentMark(th *theme.Theme, key string) agentMark {
	g, c := th.AgentGlyph(key)
	return agentMark{glyph: g, color: c}
}

// equals reports whether two marks paint the same thing. Colours are compared
// through RGBA() rather than ==: theme.Color aliases image/color.Color, an
// interface, and == on an interface panics on a non-comparable dynamic type — which
// is a property of whatever concrete colour a palette happens to hold, not of
// anything this test controls.
func (m agentMark) equals(o agentMark) bool {
	if m.glyph != o.glyph {
		return false
	}
	mr, mg, mb, ma := m.color.RGBA()
	or, og, ob, oa := o.color.RGBA()
	return mr == or && mg == og && mb == ob && ma == oa
}

// glyphExemptFromIdentity names the adapters allowed to render as an unrecognised
// agent, each with the reason it is allowed to. It is empty, and the exact-set
// assertion below is what keeps it that way: every agent Atrium ships an adapter for
// is one a user deliberately picked, so every one of them has to be tellable apart
// in a list of sessions.
var glyphExemptFromIdentity = map[agent.Key]string{}

// TestEveryAgentAdapterHasAnIdentityGlyph fails when an adapter in session/agent's
// registry is indistinguishable from an agent Atrium does not recognise.
//
// The discriminator is the fallback itself. AgentGlyph normalises an unrecognised
// key onto the generic entry BEFORE it consults any palette, so "absent from the
// table" and "resolves exactly like a key that was never in it" are the same
// statement: a sentinel key no adapter can claim yields that generic mark, and any
// adapter whose mark equals it is uncovered.
//
// The comparison has no false negative by construction — a missing key takes
// literally the same branch as the sentinel — and no false positive either, because
// the generic mark is the only one carrying Palette.FgDim and no registry key can
// reach that branch: Adapters() excludes the Generic fallback, as its doc comment
// states. Glyph and colour are both compared because either alone is weaker. Glyph
// alone would flag a future adapter that legitimately picked the bullet; colour
// alone would depend on FgDim differing from Fg in whatever palette runs here.
//
// One theme is enough for the same reason the palette is irrelevant above:
// membership is decided before the palette is, so a key missing from the table is
// missing on every theme. Width and contrast for the entries that DO exist belong
// to TestAgentGlyphWidths and TestAgentBrandColoursStayLegible in ui/theme, which
// range over those tables.
func TestEveryAgentAdapterHasAnIdentityGlyph(t *testing.T) {
	th := theme.Get(theme.DefaultThemeName)

	// Both premises of the comparison, asserted so the guard cannot pass vacuously:
	// a registry to walk, and a fallback to recognise.
	adapters := agent.Adapters()
	require.NotEmpty(t, adapters, "no adapters to check: this guard would pass by having nothing to say")
	generic := resolveAgentMark(th, "atrium-no-such-agent-key")
	require.NotEmpty(t, generic.glyph,
		"an unknown key resolves to no glyph at all, so it cannot serve as the missing-entry discriminator below")
	require.True(t, generic.equals(resolveAgentMark(th, string(agent.KeyGeneric))),
		"an unknown key no longer resolves to the generic mark; this test detects a missing "+
			"table entry by that equivalence and must be rewritten if it stops holding")

	exempt := make([]agent.Key, 0, len(glyphExemptFromIdentity))
	for k := range glyphExemptFromIdentity {
		exempt = append(exempt, k)
	}
	slices.Sort(exempt)
	require.Equal(t, []agent.Key{}, exempt,
		"the set of agents allowed to render as an unrecognised session changed; every agent "+
			"with an adapter is one a user picked on purpose and has to be tellable apart in "+
			"the list, so an addition here needs an argued reason in review, not just a map entry")

	var uncovered []agent.Key
	for _, a := range adapters {
		if _, ok := glyphExemptFromIdentity[a.Key]; ok {
			continue
		}
		if resolveAgentMark(th, string(a.Key)).equals(generic) {
			uncovered = append(uncovered, a.Key)
		}
	}
	require.Emptyf(t, uncovered,
		"adapter %v resolves through AgentGlyph to the same mark an unrecognised agent gets "+
			"(%q in the dim generic accent): give each one an entry in agentGlyphs, "+
			"ui/theme/agent.go, or every place that paints agent identity — the session row's "+
			"agent column, the in-session bar, the profile pickers — shows that session as one "+
			"Atrium does not know",
		uncovered, generic.glyph)
}
