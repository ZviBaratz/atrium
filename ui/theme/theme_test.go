package theme

import (
	"strings"
	"testing"

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
