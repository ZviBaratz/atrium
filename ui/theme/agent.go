package theme

import "github.com/charmbracelet/lipgloss"

// Agent identity: one glyph + accent per agent key (the canonical keys from
// session/agent, passed as plain strings so theme stays a leaf package). The
// glyphs are deliberately plain, single-cell, non-PUA Unicode — not Nerd-Font
// vendor logos — because there is no reliable way to probe for a patched font,
// and a glyph whose measured width differs from its rendered width desyncs
// bubbletea's incremental renderer (the list-ghosting defect). Every entry must
// measure width 1; TestAgentGlyphWidths guards the invariant.

var agentGlyphs = map[string]string{
	"claude":  "✻", // claude code's own spinner glyph
	"codex":   "❖", // ◆ would collide with Glyphs.Waiting
	"gemini":  "✦",
	"aider":   "≡",
	"generic": "•",
}

// agentColors carries the brand accents that identify an agent at a glance.
// They are brand colors, not palette colors, so they are theme-independent;
// agents without a strong brand accent ride the theme foreground instead.
var agentColors = map[string]lipgloss.Color{
	"claude": lipgloss.Color("#d97757"),
	"gemini": lipgloss.Color("#4285f4"),
}

// AgentGlyph returns the identity glyph and color for an agent key (unknown
// keys get the neutral generic marker). Key is string(agent.Resolve(p).Key).
func (t *Theme) AgentGlyph(key string) (string, lipgloss.Color) {
	g, ok := agentGlyphs[key]
	if !ok {
		key, g = "generic", agentGlyphs["generic"]
	}
	if c, ok := agentColors[key]; ok {
		return g, c
	}
	if key == "generic" {
		return g, t.Palette.FgDim
	}
	return g, t.Palette.Fg
}
