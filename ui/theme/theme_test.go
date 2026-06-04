package theme

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
	"github.com/muesli/ansi"
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

// TestGlyphWidths guards the alignment invariant: every cell glyph must measure
// width 1, so column math and the view-bounds test stay correct across themes.
func TestGlyphWidths(t *testing.T) {
	for _, name := range Names() {
		g := Get(name).Glyphs
		cells := map[string]string{
			"Ready":         g.Ready,
			"ReadySeen":     g.ReadySeen,
			"Waiting":       g.Waiting,
			"Paused":        g.Paused,
			"Branch":        g.Branch,
			"Ahead":         g.Ahead,
			"Behind":        g.Behind,
			"Dirty":         g.Dirty,
			"FoldOpen":      g.FoldOpen,
			"FoldClosed":    g.FoldClosed,
			"SelectionMark": g.SelectionMark,
			"DiffAdd":       g.DiffAdd,
			"DiffDel":       g.DiffDel,
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
