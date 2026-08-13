package theme

import (
	"reflect"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	xansi "github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"
	"github.com/muesli/ansi"
	"github.com/stretchr/testify/require"
)

func TestGetFallback(t *testing.T) {
	cases := map[string]string{
		"":                 DefaultThemeName,
		"nonexistent":      DefaultThemeName,
		"  Tokyo-Night  ":  "tokyo-night",
		"CATPPUCCIN-MOCHA": "catppuccin-mocha",
		"unicode":          "unicode",
	}
	for in, want := range cases {
		if got := Get(in).Name; got != want {
			t.Errorf("Get(%q).Name = %q, want %q", in, got, want)
		}
	}
	if Get("anything") == nil {
		t.Fatal("Get returned nil")
	}
}

func TestSetAndRestore(t *testing.T) {
	if Current().Name != DefaultThemeName {
		t.Fatalf("default Current() = %q, want %q", Current().Name, DefaultThemeName)
	}
	restore := Set("unicode")
	if Current().Name != "unicode" {
		t.Errorf("after Set, Current() = %q, want unicode", Current().Name)
	}
	restore()
	if Current().Name != DefaultThemeName {
		t.Errorf("after restore, Current() = %q, want %q", Current().Name, DefaultThemeName)
	}
}

// TestSetNerdFont verifies the glyph set is an axis orthogonal to the palette:
// flipping it swaps to the Nerd-Font vendor glyphs while preserving the palette,
// and restore brings the plain glyphs back. Default is plain (never tofu).
func TestSetNerdFont(t *testing.T) {
	if got := Current().Glyphs.Branch; got != "⎇" {
		t.Fatalf("default Branch glyph = %q, want plain ⎇", got)
	}
	wantPalette := Current().Palette
	restore := SetNerdFont(true)
	if got, want := Current().Glyphs.Branch, string(rune(nfBranch)); got != want {
		t.Errorf("nerd-on Branch glyph = %q, want PUA %q", got, want)
	}
	if Current().Palette != wantPalette {
		t.Errorf("SetNerdFont must preserve the palette")
	}
	if Current().Name != DefaultThemeName {
		t.Errorf("SetNerdFont must preserve the palette theme name, got %q", Current().Name)
	}
	restore()
	if got := Current().Glyphs.Branch; got != "⎇" {
		t.Errorf("after restore, Branch glyph = %q, want plain ⎇", got)
	}
}

// TestGlyphWidths guards the alignment invariant: every cell glyph must measure
// width 1, so column math and the view-bounds test stay correct across every
// palette × glyph-set combination (all three fidelity rungs, including the ascii
// set and both per-rung spinners).
func TestGlyphWidths(t *testing.T) {
	for _, name := range Names() {
		for _, set := range []string{GlyphSetNerd, GlyphSetPlain, GlyphSetASCII} {
			t.Cleanup(Set(name))
			t.Cleanup(SetGlyphSet(set))
			assertGlyphWidths(t, Current().Name, Current().Glyphs)
		}
	}
}

// TestGlyphsForFidelityRungs pins the three-rung ladder: nerd overlays the vendor
// PUA icons, ascii swaps in the 7-bit set (including its own spinner frames), and
// an unrecognized rung falls back to the safe plain set. It also pins the
// spinner-per-rung split (#378): the plain rung uses the block bars (universal
// coverage), while the nerd rung keeps the finer Braille motion.
func TestGlyphsForFidelityRungs(t *testing.T) {
	restore := SetGlyphSet(GlyphSetPlain)
	defer restore()

	require.Equal(t, blockSpinnerFrames[0], Current().Glyphs.SpinnerFrames[0], "plain rung uses the block spinner")

	SetGlyphSet(GlyphSetNerd)
	require.Equal(t, string(rune(nfBranch)), Current().Glyphs.Branch, "nerd rung overlays the PUA branch icon")
	require.Equal(t, miniDotFrames[0], Current().Glyphs.SpinnerFrames[0], "nerd rung keeps the Braille spinner")

	SetGlyphSet(GlyphSetASCII)
	require.Equal(t, asciiGlyphs().Branch, Current().Glyphs.Branch, "ascii rung uses the 7-bit set")
	require.Equal(t, "|", Current().Glyphs.SpinnerFrames[0], "ascii rung swaps in the |/-\\ spinner frames")

	// The two settings-panel chrome glyphs: the modified marker and the handoff arrow.
	// Both are plain Unicode with a 7-bit floor, so the ascii rung must override them
	// rather than inherit an arrow that tofus on a sparse font.
	require.Equal(t, "*", Current().Glyphs.Modified, "ascii rung uses a 7-bit modified marker")
	require.Equal(t, ">", Current().Glyphs.Handoff, "ascii rung uses a 7-bit handoff arrow")

	SetGlyphSet(GlyphSetPlain)
	require.Equal(t, "•", Current().Glyphs.Modified, "plain rung uses a bullet")
	require.Equal(t, "→", Current().Glyphs.Handoff, "plain rung uses an arrow")

	SetGlyphSet("bogus-rung")
	require.Equal(t, plainGlyphs().Branch, Current().Glyphs.Branch, "an unknown rung falls back to plain")
}

// TestContextRampRungs pins the context meter's ladder across all three rungs:
// the plain/nerd sets use block elements, and the ascii set must swap in a 7-bit
// ladder rather than inheriting them — a block element is exactly the kind of
// glyph the ascii floor exists for. Width is guarded by assertGlyphWidths across
// every palette; this test pins the *content*, which that sweep cannot see.
func TestContextRampRungs(t *testing.T) {
	t.Cleanup(SetGlyphSet(GlyphSetPlain))

	SetGlyphSet(GlyphSetPlain)
	require.Equal(t, []string{"▁", "▂", "▃", "▄", "▅", "▆", "▇", "█"}, Current().Glyphs.ContextRamp,
		"plain rung uses the block ramp")
	require.Len(t, Current().Glyphs.ContextRamp, contextRampRungs,
		"the literal above must stay the length every other assertion assumes")

	SetGlyphSet(GlyphSetNerd)
	require.Equal(t, plainGlyphs().ContextRamp, Current().Glyphs.ContextRamp,
		"nerd rung inherits the block ramp — no vendor icon needed")

	SetGlyphSet(GlyphSetASCII)
	ascii := Current().Glyphs.ContextRamp
	require.Len(t, ascii, contextRampRungs)
	require.NotEqual(t, plainGlyphs().ContextRamp, ascii,
		"ascii rung must override the block ramp, not inherit it")
	for i, r := range ascii {
		for _, c := range r {
			require.Lessf(t, c, rune(0x80), "ascii rung rung %d = %q is not 7-bit", i, r)
		}
	}
}

// TestASCIIContextRampDoesNotCollide is the invariant asciiGlyphs' own comment
// claims and nothing used to check: the four deliberate collisions in that set
// are safe only because no screen paints both meanings, so a new glyph may not
// reuse a mark that shares a frame with it.
//
// The context meter is the worst case for that rule. It renders on line 1 of a
// session row, next to Ready/Paused/Dirty/Note/Queued and above the diff stats,
// and it appears in the ? legend on the same line as the badges — which is where
// the original `. : - = + * % #` ramp printed `#` for both "note" and "context".
// A meter rung is also the one glyph a user cannot look up by shape, since its
// whole job is to be read as a position on a ladder.
//
// Asserting against the whole ascii table rather than the row subset is
// deliberate: it is the check that stays right when a glyph moves to a screen
// the meter also reaches.
func TestASCIIContextRampDoesNotCollide(t *testing.T) {
	g := asciiGlyphs()
	taken := map[string]string{}
	rv := reflect.ValueOf(g)
	rt := rv.Type()
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		if f.Name == "ContextRamp" || f.Type.Kind() != reflect.String {
			continue
		}
		if s := rv.Field(i).String(); s != "" {
			taken[s] = f.Name
		}
	}
	for _, frame := range g.SpinnerFrames {
		taken[frame] = "SpinnerFrames"
	}
	for i, rung := range g.ContextRamp {
		if owner, clash := taken[rung]; clash {
			t.Errorf("ascii context ramp rung %d = %q collides with %s, which paints on the same row",
				i, rung, owner)
		}
	}
}

// exemptFromWidth1 names the Glyphs fields that assertGlyphWidths must not hold
// to the width-1 rule, each with the reason it is exempt. Everything absent from
// this map is measured, so a newly added glyph is guarded the day it lands rather
// than the day someone remembers to list it — the drift this file used to invite
// by enumerating the fields by hand.
var exemptFromWidth1 = map[string]string{
	"SpinnerFrames": "a []string, measured frame-by-frame below",
	"SpinnerFPS":    "a duration, not a glyph",
	"AutoBadge":     "an optional leading icon: empty (0) or a single cell, checked below",
	"ContextRamp":   "a []string, measured rung-by-rung below (and pinned to 8 rungs)",
}

// assertGlyphWidths checks the width-1 invariant for one resolved glyph set.
func assertGlyphWidths(t *testing.T, name string, g Glyphs) {
	t.Helper()
	rv := reflect.ValueOf(g)
	rt := rv.Type()
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		if _, skip := exemptFromWidth1[f.Name]; skip {
			continue
		}
		if f.Type.Kind() != reflect.String {
			t.Fatalf("%s: non-string Glyphs field %s is neither measured nor documented as exempt in exemptFromWidth1", name, f.Name)
		}
		if w := runewidth.StringWidth(rv.Field(i).String()); w != 1 {
			t.Errorf("%s: glyph %s = %q has width %d, want 1", name, f.Name, rv.Field(i).String(), w)
		}
	}
	// AutoBadge is an optional leading icon: empty (0) or a single cell.
	if w := runewidth.StringWidth(g.AutoBadge); w > 1 {
		t.Errorf("%s: AutoBadge %q has width %d, want <=1", name, g.AutoBadge, w)
	}
	for i, f := range g.SpinnerFrames {
		if w := runewidth.StringWidth(f); w != 1 {
			t.Errorf("%s: spinner frame %d = %q has width %d, want 1", name, i, f, w)
		}
	}
	// The context meter draws exactly one rung, so every rung carries the same
	// width-1 obligation as any other cell glyph. The length is asserted here too
	// rather than only in TestGlyphsForFidelityRungs: contextChip indexes this
	// slice by a level derived from a percentage, so a short ramp is an
	// out-of-range panic in the render path, not a cosmetic defect.
	if n := len(g.ContextRamp); n != contextRampRungs {
		t.Errorf("%s: ContextRamp has %d rungs, want %d", name, n, contextRampRungs)
	}
	for i, r := range g.ContextRamp {
		if w := runewidth.StringWidth(r); w != 1 {
			t.Errorf("%s: context ramp rung %d = %q has width %d, want 1", name, i, r, w)
		}
	}
}

// contextRampRungs is the fixed length of Glyphs.ContextRamp. Spelled once here
// rather than as a literal in each assertion, so the two places that care —
// the width sweep above and the fidelity-rung test — cannot disagree about it.
const contextRampRungs = 8

// themeAtRung is compose()'s agent-table half without the process-global: the default
// palette theme, carrying one named fidelity rung. A bare &Theme{agentGlyphs: …}
// literal is not a substitute — AgentGlyph consults the palette's polarity, and a zero
// Palette has no colours to read.
func themeAtRung(set string) *Theme {
	t := *Get(DefaultThemeName)
	t.agentGlyphs = agentGlyphsFor(set)
	return &t
}

// TestAgentGlyphWidths extends the same invariant to the agent identity glyphs:
// each must be a single cell so the list's column math holds, and every entry
// must resolve to a non-empty glyph (including the unknown-key fallback).
//
// Swept over every rung, like TestGlyphWidths above. It used to measure the one
// resolved table Get() returns, which was the whole table there was; now a rung it
// never visits is a rung nothing measures. It resolves through AgentGlyph rather
// than reading the map, so the accessor's fallback branch is measured too.
//
// The theme is built here rather than through SetGlyphSet because this file is
// in-package: agentGlyphsFor is reachable directly, and a test that leaves the
// process-global alone cannot leak a rung into whatever runs next under -shuffle.
func TestAgentGlyphWidths(t *testing.T) {
	for _, set := range []string{GlyphSetNerd, GlyphSetPlain, GlyphSetASCII} {
		th := themeAtRung(set)
		for _, key := range th.AgentKeys() {
			g, _ := th.AgentGlyph(key)
			if w := runewidth.StringWidth(g); w != 1 {
				t.Errorf("%s rung: agent glyph %s = %q has width %d, want 1", set, key, g, w)
			}
		}
		g, _ := th.AgentGlyph("unknown-agent")
		if w := runewidth.StringWidth(g); w != 1 {
			t.Errorf("%s rung: unknown-key fallback glyph %q has width %d, want 1", set, g, w)
		}
	}
}

// TestAgentRungsShareOneKeySet pins what makes Theme.AgentKeys well defined: every
// rung names the same agents, so "which agents does Atrium know" has one answer
// whatever fidelity the user is on. A key present on one rung and missing on another
// would render as an unrecognised session on exactly one of them — and the ? legend,
// which projects AgentKeys, would list a different set of agents per rung.
func TestAgentRungsShareOneKeySet(t *testing.T) {
	want := themeAtRung(GlyphSetPlain).AgentKeys()
	require.NotEmpty(t, want, "no agent keys at all: every assertion below would be vacuous")
	for _, set := range []string{GlyphSetNerd, GlyphSetPlain, GlyphSetASCII, "bogus-rung"} {
		got := themeAtRung(set).AgentKeys()
		require.Equalf(t, want, got, "the %s rung names a different set of agents than the plain one", set)
	}
}

// TestAgentGlyphsFollowTheRung pins the wiring, which is the half of #674 that every
// other guard here is structurally blind to: the rung the user selected has to reach
// the agent table on the COMPOSED theme.
//
// Nothing else can say it. The width and collision sweeps in this file build their own
// theme (themeAtRung), and package ui's coverage guard reads Current() and compares
// AgentGlyph against AgentGlyph — self-consistent whatever table is underneath. Delete
// compose()'s line and all of them stay green while an ascii user gets Unicode again,
// which is exactly the defect this PR fixed.
func TestAgentGlyphsFollowTheRung(t *testing.T) {
	// The known default, not the entry state: CI runs -shuffle=on.
	t.Cleanup(func() { SetGlyphSet(GlyphSetPlain) })

	const probe = "claude" // any real key: what is asserted is which table answered
	plain, ascii := plainAgentGlyphs()[probe], asciiAgentGlyphs()[probe]
	require.NotEqual(t, plain, ascii, "the two rungs spell this agent the same, so nothing below discriminates")

	for _, tc := range []struct{ set, want string }{
		{GlyphSetPlain, plain},
		{GlyphSetNerd, plain}, // two rungs, not three: nerd shares the plain table
		{GlyphSetASCII, ascii},
		{"bogus-rung", plain}, // an unknown rung falls back to plain, like glyphsFor
	} {
		SetGlyphSet(tc.set)
		g, _ := Current().AgentGlyph(probe)
		require.Equalf(t, tc.want, g, "on the %s rung the composed theme paints %q for %s", tc.set, g, probe)
	}
}

// TestASCIIAgentGlyphsDoNotCollide holds the CONSTRAINTS asciiAgentGlyphs' first-free-
// letter rule exists to satisfy — the same job TestASCIIContextRampDoesNotCollide does
// for the meter, and for a sharper version of the same reason.
//
// Not the derivation itself, which no test here holds: spell gemini "Z" and the suite
// stays green, and generic's "." is outside the rule by construction. That is the right
// split — the rule is guidance for picking the next value, the constraints below are
// what makes a value wrong.
//
// The agent glyph is pinned to the far right of a session row (ui/row.go's agentSeg),
// so it shares ONE FRAME with every mark in the ascii Glyphs table: the status gutter,
// the git chips, the fold markers on the repo header above it, the context meter. The
// four deliberate collisions asciiGlyphs allows itself are argued from "no screen shows
// both meanings"; that argument is unavailable here, so this table gets no collisions
// at all.
//
// Case-insensitively, which is the part a naive check would miss and the part that
// decided the values: the mnemonic X (codeX) and V (antigraVity) are the case-twins of
// Muted/MarkChecked and Behind/FoldOpen, and case height is the weakest distinction a
// font can make — on the fonts this rung exists for. Distinctness within the table and
// the 7-bit floor itself are asserted here too, the latter because asciiAgentGlyphs is
// built from the plain table: an agent added there and not here inherits its Unicode
// glyph into the rung that exists to have none, which is #674 exactly.
func TestASCIIAgentGlyphsDoNotCollide(t *testing.T) {
	g := asciiGlyphs()
	taken := map[string]string{}
	rv := reflect.ValueOf(g)
	rt := rv.Type()
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		if f.Type.Kind() != reflect.String {
			continue
		}
		if s := rv.Field(i).String(); s != "" {
			taken[strings.ToLower(s)] = f.Name
		}
	}
	for _, frame := range g.SpinnerFrames {
		taken[strings.ToLower(frame)] = "SpinnerFrames"
	}
	for _, rung := range g.ContextRamp {
		taken[strings.ToLower(rung)] = "ContextRamp"
	}
	require.NotEmpty(t, taken, "nothing to collide with: this test would pass by having nothing to say")

	byGlyph := map[string]string{}
	th := themeAtRung(GlyphSetASCII)
	for _, key := range th.AgentKeys() {
		mark, _ := th.AgentGlyph(key)
		for _, r := range mark {
			require.Lessf(t, r, rune(0x80),
				"ascii agent glyph %s = %q is not 7-bit — it inherited the plain rung's Unicode mark, "+
					"which is the tofu this rung exists to avoid", key, mark)
		}
		if owner, clash := taken[strings.ToLower(mark)]; clash {
			t.Errorf("ascii agent glyph %s = %q collides with Glyphs.%s (case-insensitively), "+
				"which paints on the same row", key, mark, owner)
		}
		if other, dup := byGlyph[strings.ToLower(mark)]; dup {
			t.Errorf("ascii agent glyphs %s and %s are both %q (case-insensitively): "+
				"two agents that paint as one", other, key, mark)
		}
		byGlyph[strings.ToLower(mark)] = key
	}
}

// TestPanelExactDimensions mirrors the view-bounds invariant at the unit level:
// Panel must emit exactly height lines, each exactly width columns wide.
func TestPanelExactDimensions(t *testing.T) {
	content := "first line\nsecond line\nthird"
	for _, th := range []*Theme{Get("tokyo-night"), Get("unicode")} {
		for _, dim := range [][2]int{{20, 5}, {40, 10}, {12, 4}, {60, 20}, {8, 3}} {
			w, h := dim[0], dim[1]
			for _, active := range []bool{true, false} {
				out := th.Panel("Sessions", content, w, h, active)
				lines := strings.Split(out, "\n")
				if len(lines) != h {
					t.Errorf("%s %dx%d active=%v: %d lines, want %d", th.Name, w, h, active, len(lines), h)
					continue
				}
				for i, l := range lines {
					if pw := ansi.PrintableRuneWidth(l); pw != w {
						t.Errorf("%s %dx%d active=%v: line %d width %d, want %d", th.Name, w, h, active, i, pw, w)
						break
					}
				}
			}
		}
	}
}

// TestPanelWithBadgeExactDimensions: the badge variant must hold the same
// bounds invariant as Panel at every size, including widths too narrow for
// the badge to render at all.
func TestPanelWithBadgeExactDimensions(t *testing.T) {
	content := "first line\nsecond line\nthird"
	for _, th := range []*Theme{Get("tokyo-night"), Get("unicode")} {
		for _, dim := range [][2]int{{20, 5}, {40, 10}, {12, 4}, {60, 20}, {8, 3}, {24, 5}} {
			w, h := dim[0], dim[1]
			for _, active := range []bool{true, false} {
				out := th.PanelWithBadge("Sessions", "⇡ v9.9.9", content, w, h, active)
				lines := strings.Split(out, "\n")
				if len(lines) != h {
					t.Errorf("%s %dx%d active=%v: %d lines, want %d", th.Name, w, h, active, len(lines), h)
					continue
				}
				for i, l := range lines {
					if pw := ansi.PrintableRuneWidth(l); pw != w {
						t.Errorf("%s %dx%d active=%v: line %d width %d, want %d", th.Name, w, h, active, i, pw, w)
						break
					}
				}
			}
		}
	}
}

// TestPanelBadgeDegradation pins the badge's narrow-width fallback order:
// full badge → text after the glyph → glyph alone → nothing. The title is
// never sacrificed, and a partial version string is never shown.
func TestPanelBadgeDegradation(t *testing.T) {
	th := Get("tokyo-night")
	cases := []struct {
		width          int
		wants, rejects []string
	}{
		{24, []string{"⇡ v9.9.9"}, nil},               // 4 + titleSeg(10) + badgeSeg(10): exact fit
		{23, []string{"v9.9.9"}, []string{"⇡"}},       // glyph dropped, version survives
		{22, []string{"v9.9.9"}, []string{"⇡"}},       // tier-2 lower bound
		{21, []string{"⇡"}, []string{"v9.9.9", "v9"}}, // glyph only
		{17, []string{"⇡"}, []string{"v9.9.9", "v9"}}, // tier-3 lower bound
		{16, nil, []string{"⇡", "v9.9.9", "v9"}},      // no room: today's plain border
	}
	for _, c := range cases {
		out := th.PanelWithBadge("Sessions", "⇡ v9.9.9", "x", c.width, 4, true)
		top := xansi.Strip(strings.Split(out, "\n")[0])
		if !strings.Contains(top, "Sessions") {
			t.Errorf("width %d: title missing from %q", c.width, top)
		}
		for _, want := range c.wants {
			if !strings.Contains(top, want) {
				t.Errorf("width %d: top row %q missing %q", c.width, top, want)
			}
		}
		for _, reject := range c.rejects {
			if strings.Contains(top, reject) {
				t.Errorf("width %d: top row %q must not contain %q", c.width, top, reject)
			}
		}
		for i, l := range strings.Split(out, "\n") {
			if pw := ansi.PrintableRuneWidth(l); pw != c.width {
				t.Errorf("width %d: line %d width %d", c.width, i, pw)
			}
		}
	}
}

// TestPanelMultiBadgeDegradation pins the multi-badge fallback ladder: both
// badges full → every glyph alone → nothing. A narrow panel must keep both
// signals as glyphs rather than orphaning one badge's glyph or dropping a whole
// badge (the bug where "⚠ stale" vanished before "⇡ v9.9.9" under width pressure).
func TestPanelMultiBadgeDegradation(t *testing.T) {
	th := Get("tokyo-night")
	badges := []string{"⇡ v9.9.9", "⚠ stale"}
	cases := []struct {
		width          int
		wants, rejects []string
	}{
		{40, []string{"⇡ v9.9.9", "⚠ stale"}, nil},            // both full
		{28, []string{"⇡", "⚠"}, []string{"v9.9.9", "stale"}}, // collapsed to glyphs
		{16, nil, []string{"⇡", "⚠", "v9.9.9", "stale"}},      // no room: plain border
	}
	for _, c := range cases {
		out := th.PanelWithBadges("Sessions", badges, "x", c.width, 4, true)
		top := xansi.Strip(strings.Split(out, "\n")[0])
		if !strings.Contains(top, "Sessions") {
			t.Errorf("width %d: title missing from %q", c.width, top)
		}
		for _, want := range c.wants {
			if !strings.Contains(top, want) {
				t.Errorf("width %d: top row %q missing %q", c.width, top, want)
			}
		}
		for _, reject := range c.rejects {
			if strings.Contains(top, reject) {
				t.Errorf("width %d: top row %q must not contain %q", c.width, top, reject)
			}
		}
		for i, l := range strings.Split(out, "\n") {
			if pw := ansi.PrintableRuneWidth(l); pw != c.width {
				t.Errorf("width %d: line %d width %d", c.width, i, pw)
			}
		}
	}
}

// TestPanelEmptyBadgeEqualsPanel locks Panel as the empty-badge identity so
// the badge path can't drift the plain border rendering.
func TestPanelEmptyBadgeEqualsPanel(t *testing.T) {
	for _, th := range []*Theme{Get("tokyo-night"), Get("unicode")} {
		plain := th.Panel("Sessions", "x", 24, 4, true)
		badged := th.PanelWithBadge("Sessions", "", "x", 24, 4, true)
		if plain != badged {
			t.Errorf("%s: PanelWithBadge with empty badge differs from Panel:\n%q\nvs\n%q", th.Name, badged, plain)
		}
	}
}

// TestPanelLongTitleTruncates ensures an over-long title can't blow the width.
func TestPanelLongTitleTruncates(t *testing.T) {
	th := Get("tokyo-night")
	out := th.Panel(strings.Repeat("verylongtitle", 5), "x", 20, 4, true)
	for i, l := range strings.Split(out, "\n") {
		if pw := ansi.PrintableRuneWidth(l); pw != 20 {
			t.Errorf("line %d width %d, want 20", i, pw)
		}
	}
}

func TestNoteGlyphIsSingleCellEverywhere(t *testing.T) {
	for _, name := range Names() {
		t.Cleanup(Set(name))
		g := Current().Glyphs.Note
		require.NotEmpty(t, g, "%s: note glyph must be set", name)
		require.Equal(t, 1, runewidth.StringWidth(g), "%s: note glyph must be single-cell (no emoji)", name)
	}
}

// SanitizeWidth must decompose font-dependent emoji clusters so the width a layout
// library measures equals what a terminal lacking the combined glyph renders. The
// family ZWJ sequence is the regression case: it measures 2 (one cluster) but renders
// as three separate 2-cell people (6). After sanitizing, the measured width must equal
// that rendered 6 — otherwise the composed line overflows, wraps, and desyncs the
// alt-screen renderer (the duplicated-rows-on-navigation bug).
func TestSanitizeWidth(t *testing.T) {
	// Joiners written as escapes (ST1018: no invisible format chars in string literals).
	const family = "\U0001F468\u200d\U0001F469\u200d\U0001F467" // 👨 ZWJ 👩 ZWJ 👧

	// Pre-condition that creates the bug: the cluster measures as a single 2-cell glyph.
	if w := lipgloss.Width(family); w != 2 {
		t.Fatalf("precondition: lipgloss.Width(family ZWJ) = %d, want 2", w)
	}

	got := SanitizeWidth(family)
	if strings.ContainsRune(got, 0x200D) {
		t.Errorf("SanitizeWidth left a ZERO WIDTH JOINER in %q", got)
	}
	// Decomposed: three standalone emoji, each 2 cells = 6, matching the terminal's render.
	if w := lipgloss.Width(got); w != 6 {
		t.Errorf("lipgloss.Width(sanitized) = %d, want 6 (three 2-cell emoji)", w)
	}

	// Variation selector and skin-tone modifier are also stripped.
	if got := SanitizeWidth("\u2764\ufe0f"); got != "\u2764" { // ❤️ -> ❤
		t.Errorf("variation selector not stripped: %q", got)
	}
	if got := SanitizeWidth("\U0001F44D\U0001F3FD"); got != "\U0001F44D" { // 👍🏽 -> 👍
		t.Errorf("skin-tone modifier not stripped: %q", got)
	}

	// Content with no risky codepoints is returned unchanged (and not reallocated needlessly).
	plain := "│ zvi/bad-rendering ⇡11  +2646 -652 ● ready"
	if SanitizeWidth(plain) != plain {
		t.Errorf("plain content was modified: %q", SanitizeWidth(plain))
	}
}
