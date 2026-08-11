package theme

import "charm.land/lipgloss/v2"

// Agent identity: one glyph + accent per agent key (the canonical keys from
// session/agent, passed as plain strings so theme stays a leaf package). The
// glyphs are deliberately plain, single-cell, non-PUA Unicode — not Nerd-Font
// vendor logos — because there is no reliable way to probe for a patched font,
// and a glyph whose measured width differs from its rendered width desyncs
// bubbletea's incremental renderer (the list-ghosting defect). Every entry must
// measure width 1; TestAgentGlyphWidths guards the invariant.
//
// That test iterates this table, so it can only speak for the entries that exist.
// Whether the table COVERS session/agent's registry is a question this package
// cannot ask — a leaf cannot see the registry — so it is guarded from package ui,
// by TestEveryAgentAdapterHasAnIdentityGlyph. Editing this table and running only
// `go test ./ui/theme` will therefore not tell you whether an agent is missing.
var agentGlyphs = map[string]string{
	"claude":  "✻", // claude code's own spinner glyph
	"codex":   "❖", // ◆ would collide with Glyphs.Waiting
	"gemini":  "✦",
	"aider":   "≡",
	"agy":     "✜", // a cross: no Glyphs rung draws one, and a star would read as a second gemini
	"generic": "•",
}

// agentColors carries the brand accents that identify an agent at a glance.
// They are brand colors, not palette colors, so they do not vary by palette
// FAMILY; agents without a strong brand accent ride the theme foreground instead.
// They do vary by palette POLARITY — see agentColorsLight.
var agentColors = map[string]Color{
	"claude": lipgloss.Color("#d97757"),
	"gemini": lipgloss.Color("#4285f4"),
}

// agentColorsLight is the same two brands on a light background: the identical
// hues, darkened only as far as legibility requires.
//
// This exists because the brands as shipped cannot be read on paper, and no
// palette tuning fixes it. Claude's #d97757 peaks at 3.12:1 against pure white — it
// would need a background lighter than #fcfcfc to clear the 3.0 floor
// TestAgentBrandColoursStayLegible sets, and lands at 2.41 on tokyo-night-day and
// 2.76 on catppuccin-latte. The colour is simply too light to be a mark on a light
// field, which is a property of the brand rather than of any palette Atrium could
// pick.
//
// Darkening rather than substituting is the point: hue and saturation are
// preserved, so #cc552e is still recognisably the same clay and #2774f2 still
// recognisably the same blue. These are not different brands, they are the brand
// with enough ink behind it to be a glyph. Every theme shipping before #394 renders
// byte-identically — this table is only ever reached by a palette that did not
// exist then.
var agentColorsLight = map[string]Color{
	"claude": lipgloss.Color("#cc552e"), // #d97757 darkened: 2.41 -> 3.31 on tokyo-night-day
	"gemini": lipgloss.Color("#2774f2"), // #4285f4 darkened: 2.75 -> 3.34 on tokyo-night-day
}

// AgentGlyph returns the identity glyph and color for an agent key (unknown
// keys get the neutral generic marker). Key is string(agent.Resolve(p).Key).
func (t *Theme) AgentGlyph(key string) (string, Color) {
	g, ok := agentGlyphs[key]
	if !ok {
		key, g = "generic", agentGlyphs["generic"]
	}
	// Polarity first: a brand accent that has a light form must use it on a light
	// palette, or the mark it exists to make is one nobody can see.
	if IsLight(t.Palette) {
		if c, ok := agentColorsLight[key]; ok {
			return g, c
		}
	}
	if c, ok := agentColors[key]; ok {
		return g, c
	}
	if key == "generic" {
		return g, t.Palette.FgDim
	}
	return g, t.Palette.Fg
}
