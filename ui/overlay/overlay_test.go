package overlay

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	xansi "github.com/charmbracelet/x/ansi"
)

// familyEmoji is a ZWJ grapheme cluster (man+woman+girl+boy joined by U+200D). The
// joiners are written as \u200d escapes so staticcheck (ST1018) does not flag invisible
// Unicode in the literal; the decoded value is a normal family emoji.
const familyEmoji = "\U0001F468\u200d\U0001F469\u200d\U0001F467\u200d\U0001F466"

// padTo pads s with spaces so its grapheme-aware display width equals w — i.e. it
// produces exactly what a lipgloss-wrapped pane line of width w looks like.
func padTo(s string, w int) string {
	if cur := xansi.StringWidth(s); cur < w {
		return s + strings.Repeat(" ", w-cur)
	}
	return s
}

// leftBorderColumn returns the display column of the first box-border rune on the
// line, or -1 if there is none.
func leftBorderColumn(line string) int {
	for _, bc := range []string{"│", "╭", "╰", "┌", "└"} {
		if i := strings.Index(line, bc); i >= 0 {
			return xansi.StringWidth(line[:i])
		}
	}
	return -1
}

// A centered overlay must stay centered even when a background line contains a ZWJ
// grapheme cluster (e.g. the family emoji). lipgloss wraps panes using grapheme-aware
// width (charmbracelet/x/ansi); if the compositor measures width differently it
// over-counts that line, inflating the canvas width — shoving the popup right and
// tearing the mis-measured row to a different column from the rest.
func TestPlaceOverlayCentersWithGraphemeClusterBackground(t *testing.T) {
	const canvasW, canvasH = 100, 24

	bg := make([]string, canvasH)
	for i := range bg {
		bg[i] = strings.Repeat("x", canvasW)
	}
	// One realistic line carrying a ZWJ family emoji, padded (grapheme-aware) to the
	// canvas width — exactly what a lipgloss-wrapped pane emits.
	bg[canvasH/2] = padTo("output: "+familyEmoji+" done", canvasW)

	fg := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		Padding(1, 2).
		Render("Title\nshort\na slightly longer help line here")
	fgW := xansi.StringWidth(strings.Split(fg, "\n")[0])

	out := PlaceOverlay(0, 0, fg, strings.Join(bg, "\n"), false, true)

	// 1) The composed frame must never exceed the canvas width.
	for i, l := range strings.Split(out, "\n") {
		if w := xansi.StringWidth(l); w > canvasW {
			t.Fatalf("line %d width %d exceeds canvas width %d", i, w, canvasW)
		}
	}

	// 2) The popup must start at the same column on every one of its rows (no torn
	//    row) and be horizontally centered.
	cols := map[int]int{}
	for _, l := range strings.Split(out, "\n") {
		if c := leftBorderColumn(l); c >= 0 {
			cols[c]++
		}
	}
	if len(cols) != 1 {
		t.Fatalf("popup left border not uniform across rows: column histogram %v (want a single column)", cols)
	}
	var startCol int
	for c := range cols {
		startCol = c
	}
	if want := (canvasW - fgW) / 2; startCol < want-1 || startCol > want+1 {
		t.Fatalf("popup not centered: left border at col %d, want ~%d", startCol, want)
	}
}
