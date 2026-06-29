package theme

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
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
// palette × glyph-set combination (plain and Nerd-Font).
func TestGlyphWidths(t *testing.T) {
	for _, name := range Names() {
		for _, nerd := range []bool{false, true} {
			t.Cleanup(Set(name))
			t.Cleanup(SetNerdFont(nerd))
			assertGlyphWidths(t, Current().Name, Current().Glyphs)
		}
	}
}

// assertGlyphWidths checks the width-1 invariant for one resolved glyph set.
func assertGlyphWidths(t *testing.T, name string, g Glyphs) {
	t.Helper()
	cells := map[string]string{
		"Ready":         g.Ready,
		"ReadySeen":     g.ReadySeen,
		"Waiting":       g.Waiting,
		"Paused":        g.Paused,
		"Branch":        g.Branch,
		"Ahead":         g.Ahead,
		"Warn":          g.Warn,
		"Behind":        g.Behind,
		"Dirty":         g.Dirty,
		"Note":          g.Note,
		"PR":            g.PR,
		"FoldOpen":      g.FoldOpen,
		"FoldClosed":    g.FoldClosed,
		"SelectionMark": g.SelectionMark,
		"MarkChecked":   g.MarkChecked,
		"DiffAdd":       g.DiffAdd,
		"DiffDel":       g.DiffDel,
		"TextCursor":    g.TextCursor,
	}
	for label, glyph := range cells {
		if w := runewidth.StringWidth(glyph); w != 1 {
			t.Errorf("%s: glyph %s = %q has width %d, want 1", name, label, glyph, w)
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
}

// TestAgentGlyphWidths extends the same invariant to the agent identity glyphs:
// each must be a single cell so the list's column math holds, and every entry
// must resolve to a non-empty glyph (including the unknown-key fallback).
func TestAgentGlyphWidths(t *testing.T) {
	th := Get(DefaultThemeName)
	for key := range agentGlyphs {
		g, _ := th.AgentGlyph(key)
		if w := runewidth.StringWidth(g); w != 1 {
			t.Errorf("agent glyph %s = %q has width %d, want 1", key, g, w)
		}
	}
	g, _ := th.AgentGlyph("unknown-agent")
	if w := runewidth.StringWidth(g); w != 1 {
		t.Errorf("unknown-key fallback glyph %q has width %d, want 1", g, w)
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
