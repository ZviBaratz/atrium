package ui

import (
	"fmt"
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
// registry is indistinguishable from an agent Atrium does not recognise, or from
// another adapter. Those are the two halves of being tellable apart in a list, and
// each is silent about the other: a table where two agents share one glyph covers
// every registry key, and a table where every glyph is unique can still be missing
// a key entirely.
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
//
// Every RUNG is not enough, though, and that is the one axis this used to skip. Since
// #674 the table has an ascii form (plain letters, chosen against the row's 7-bit
// vocabulary), so distinctness is a per-rung property: two agents can be distinct on
// the plain rung and identical on the floor a user drops to when their font cannot
// draw it. Coverage is not per-rung — the rungs share one key set, which ui/theme's
// TestAgentRungsShareOneKeySet holds — but it is swept anyway, because sweeping is
// what makes that statement true here rather than assumed here.
func TestEveryAgentAdapterHasAnIdentityGlyph(t *testing.T) {
	// theme.Current() is a process global. Restore the KNOWN default rather than
	// whatever was set on entry: CI runs -shuffle=on, so entry state is not a fixed
	// value, and the sweep below leaves the ascii rung behind on its last iteration.
	t.Cleanup(func() { theme.SetGlyphSet(theme.GlyphSetPlain) })

	for _, set := range []string{theme.GlyphSetPlain, theme.GlyphSetNerd, theme.GlyphSetASCII} {
		t.Run(set, func(t *testing.T) {
			restore := theme.SetGlyphSet(set)
			defer restore()
			assertAdaptersAreTellableApart(t, theme.Current())
		})
	}
}

// assertAdaptersAreTellableApart runs the two halves of the invariant against one
// resolved theme — one palette × one fidelity rung.
func assertAdaptersAreTellableApart(t *testing.T, th *theme.Theme) {
	t.Helper()

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
			"(%q in the dim generic accent): give each one an entry in every rung of the agent "+
			"table, ui/theme/agent.go, or every place that paints agent identity — the session "+
			"row's agent column, the in-session bar, the profile pickers — shows that session as "+
			"one Atrium does not know",
		uncovered, generic.glyph)

	// Covered is half of tellable apart; the other half is that no two adapters
	// resolve to the SAME glyph. Distinctness is required of the glyph alone rather
	// than of the whole mark, because the pickers paint identity from the glyph and
	// discard the colour (ui/overlay/profilePicker.go, ui/overlay/variantPicker.go
	// both bind it to _), so two agents sharing a glyph are indistinguishable there
	// whatever accent the row would have given them. Nothing forces an accent either:
	// an adapter with no agentColors entry rides Palette.Fg, which is why more than
	// one of them does today.
	//
	// The generic fallback joins the adapters here, and is the reason this is a check
	// on the glyph rather than on the mark twice over: Adapters() excludes it, so the
	// uncovered loop above can only see it from the other direction (an adapter that
	// resolves LIKE generic, colour included). A generic glyph equal to a real agent's
	// fails neither of those — the colours differ — while painting an unrecognised
	// program as that agent in both pickers, which draw from the glyph alone.
	byGlyph := map[string][]agent.Key{}
	for _, a := range adapters {
		g, _ := th.AgentGlyph(string(a.Key))
		byGlyph[g] = append(byGlyph[g], a.Key)
	}
	genericGlyph, _ := th.AgentGlyph(string(agent.KeyGeneric))
	byGlyph[genericGlyph] = append(byGlyph[genericGlyph], agent.KeyGeneric)
	var shared []string
	for g, keys := range byGlyph {
		if len(keys) > 1 {
			slices.Sort(keys)
			shared = append(shared, fmt.Sprintf("%q: %v", g, keys))
		}
	}
	slices.Sort(shared)
	require.Emptyf(t, shared, "these adapters resolve to one glyph — %s — so they paint as "+
		"the same agent wherever identity is drawn from the glyph alone; give each its own "+
		"entry in the agent table, ui/theme/agent.go", shared)
}
