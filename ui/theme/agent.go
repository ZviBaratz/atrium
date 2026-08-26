package theme

import (
	"sort"

	"charm.land/lipgloss/v2"
)

// Agent identity: one glyph + accent per agent key (the canonical keys from
// session/agent, passed as plain strings so theme stays a leaf package). The
// glyphs are deliberately plain, single-cell, non-PUA Unicode — not Nerd-Font
// vendor logos — because there is no reliable way to probe for a patched font,
// and a glyph whose measured width differs from its rendered width desyncs
// bubbletea's incremental renderer (the list-ghosting defect). Every entry must
// measure width 1; TestAgentGlyphWidths guards the invariant, on every rung.
//
// That test iterates these tables, so it can only speak for the entries that exist.
// Whether they COVER session/agent's registry is a question this package cannot ask
// — a leaf cannot see the registry — so it is guarded from package ui, by
// TestEveryAgentAdapterHasAnIdentityGlyph. Editing a table here and running only
// `go test ./ui/theme` will therefore not tell you whether an agent is missing, nor
// whether two of them now share a glyph — that test covers both, per rung.
//
// A new glyph is checked against two surfaces, and the second is the one that is easy
// to skip: every rung of the Glyphs table (nerd/plain/ascii — Waiting is why codex is
// not "◆"), AND the rune literals the row paints inline, which are not Glyphs fields
// at all. agy's "✜" is a plus-shaped cross; the nearest thing the row draws is
// prCheckGlyph's failing-CI "✗" (ui/row.go), a saltire rather than a plus. A star was
// the other candidate for agy and would have read as a second gemini.
//
// copilot's "⎈" (U+2388 HELM SYMBOL) is a pilot's wheel. The neighbour it was checked
// against is Branch's "⎇" (U+2387) — one codepoint below it, so a font rendering one
// renders the other, and they share a FRAME (the agent glyph is pinned to the far right
// of the row's first line, the branch chip sits on its second). They do not share a
// shape: ⎇ is an alternate-key fork, ⎈ is a spoked wheel. The circle family was ruled
// out first — Ready "●", ReadySeen "○" and AcctAvailable "●" have it — and the diamond
// family second, for the reason codex's entry already records about "◆".
func plainAgentGlyphs() map[string]string {
	return map[string]string{
		"claude":  "✻", // claude code's own spinner glyph
		"codex":   "❖", // ◆ would collide with Glyphs.Waiting
		"gemini":  "✦",
		"aider":   "≡",
		"agy":     "✜", // a plus-cross; see the note above on what that was checked against
		"copilot": "⎈", // U+2388 HELM SYMBOL — a pilot's wheel; see above on the neighbour it was checked against
		"generic": "•",
	}
}

// asciiAgentGlyphs is the 7-bit floor for agent identity: the rung a user selects to
// say this font cannot render plain Unicode. Until #674 there was no such rung here,
// so the status column degraded to `* ? ~ Y` and the agent column beside it kept
// painting tofu.
//
// Built FROM plainAgentGlyphs so a key added there is never silently absent — but note
// that it is then silently UNICODE here until it is given a form below, which is the
// #674 defect re-armed. TestASCIIAgentGlyphsDoNotCollide fails on that.
//
// The values were derived by a rule, so they are arguable rather than taste: take the
// first letter of the agent's own name, uppercased, that is not already claimed by an
// earlier entry and whose lowercase form is not in the ascii row vocabulary. (generic
// is the one entry outside it — not a name to take a letter from; see below.) The rule
// is a convention for picking the NEXT one; what a test can hold is the constraint it
// exists to satisfy, and TestASCIIAgentGlyphsDoNotCollide holds exactly that: 7-bit, no
// case-insensitive collision with the ascii Glyphs vocabulary, distinct within the
// table. A different letter meeting those is a review conversation, not a build break.
//
// That vocabulary, read off asciiGlyphs rather than remembered, is `! # % & * + - / =
// > ? Y \ ^ _ o v x | ~` and the digits 1-8 — every distinct mark in the table, its
// spinner frames and its context ramp. The agent glyph does not share a LINE with all
// of it: agentSeg is pinned to the far right of the row's first line (ui/list_render.go),
// while the git chips those marks build are on its second and the fold markers are on
// the repo header above. It shares a FRAME with every one of them, which is the level
// the argument turns on — the whole list is on screen at once, so the "no screen shows
// both meanings" reasoning asciiGlyphs makes for its four deliberate collisions is not
// available here.
//
//	claude  C  free
//	codex   D  c is claude's and o is ReadySeen, so co(d)ex
//	gemini  G  free
//	aider   A  free — aider has been in this table since #67, so it keeps the letter
//	agy     N  a and g are claimed and Y is Branch, which exhausts the key: fall to the
//	           product name, a(n)tigravity
//	copilot P  c is claude's and o is ReadySeen, so co(p)ilot
//	generic .  not a name. The quietest 7-bit mark there is, standing in for "•" — the
//	           unknown-agent marker should recede, not announce itself
//
// Case-twins are excluded, not just exact matches, which is what rules out the more
// mnemonic X (codeX) and V (antigraVity): "x" is Muted AND MarkChecked, "v" is Behind
// AND FoldOpen, and case height is the weakest distinction a font can make — on exactly
// the fonts this rung exists for. Uppercase also separates the mark from the row's
// lowercase text chips (`dev`, `2h`, `18.9k`); the nearest neighbour checked is the
// AUTO badge's "A", which is a word on its own background rather than a lone mark.
func asciiAgentGlyphs() map[string]string {
	g := plainAgentGlyphs()
	g["claude"] = "C"
	g["codex"] = "D"
	g["gemini"] = "G"
	g["aider"] = "A"
	g["agy"] = "N"
	g["copilot"] = "P"
	g["generic"] = "."
	return g
}

// agentGlyphsFor returns the agent identity table for a fidelity rung. Two rungs, not
// the Glyphs table's three: the nerd rung exists to overlay vendor PUA icons, and this
// table deliberately has none (see the header comment — there is no reliable
// patched-font probe, and the plain marks render on a patched font anyway). So nerd and
// plain share one table, and anything unrecognized falls back to plain, the set that
// never renders tofu.
func agentGlyphsFor(set string) map[string]string {
	if set == GlyphSetASCII {
		return asciiAgentGlyphs()
	}
	return plainAgentGlyphs()
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

// agentTable is the identity table this theme resolves against — its fidelity rung,
// stamped by compose(). The fallback is for a Theme built without one (a literal in a
// test, say): naming every agent in the plain rung is a better answer than painting
// every session as one Atrium does not recognise, which is what an empty map would do.
func (t *Theme) agentTable() map[string]string {
	if t.agentGlyphs == nil {
		return plainAgentGlyphs()
	}
	return t.agentGlyphs
}

// AgentKeys returns this theme's agent identity keys, sorted.
//
// It exists so a caller outside this package can enumerate the table without being
// able to mutate it — the ? legend projects it (app/help.go's legendGroups) and
// TestLegendCoversRowVocabulary walks it to require that every entry reaches the
// legend or is excluded with a reason. A method rather than a package function
// because the table is per-rung: the keys happen to be the same on every rung today
// (TestAgentRungsShareOneKeySet), and reading them off the theme is what keeps that a
// fact about the tables rather than an assumption in every caller.
func (t *Theme) AgentKeys() []string {
	table := t.agentTable()
	keys := make([]string, 0, len(table))
	for k := range table {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// AgentGlyph returns the identity glyph and color for an agent key (unknown
// keys get the neutral generic marker). Key is string(agent.Resolve(p).Key).
func (t *Theme) AgentGlyph(key string) (string, Color) {
	table := t.agentTable()
	g, ok := table[key]
	if !ok {
		key, g = "generic", table["generic"]
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
