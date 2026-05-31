package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"
	"github.com/stretchr/testify/require"
)

// A user-set display name can carry an emoji cluster (here a ZWJ family sequence)
// that width libraries measure as one 2-cell glyph but a terminal lacking the
// combined glyph renders as its separate, far-wider components. If the list row is
// laid out against the measured (narrow) width, it overflows on such a terminal
// and wraps onto an extra physical row — desyncing bubbletea's incremental
// renderer exactly as over-wide pane content does. The row must therefore be
// sanitized (the cluster decomposed, so measured == rendered) before the width
// math, so it can never wrap. This guards that the render site applies
// theme.SanitizeWidth, mirroring TestSanitizeWidth in ui/theme.
func TestRender_DisplayNameSanitizedBeforeMeasurement(t *testing.T) {
	// Joiners written as escapes (ST1018: no invisible format chars in string literals).
	const family = "\U0001F468\u200d\U0001F469\u200d\U0001F467" // 0x1F468 ZWJ 0x1F469 ZWJ 0x1F467
	l, insts := newFilterList(t, "session")
	insts[0].SetDisplayName(family)

	row := l.renderer.Render(insts[0], 1, false)

	// The joiner that lets the rendered width diverge from the measured width must
	// be gone from the laid-out row: its presence is precisely what causes the
	// wrap/desync, so a sanitized row cannot contain it.
	if strings.ContainsRune(row, 0x200D) {
		t.Errorf("rendered row left a ZERO WIDTH JOINER in %q", row)
	}

	// Every physical line must fit the renderer width when measured the way a
	// glyph-less terminal renders the (now decomposed) name — i.e. no line is wide
	// enough to wrap. Strip ANSI styling first so only printable cells are counted.
	for _, line := range strings.Split(row, "\n") {
		if w := runewidth.StringWidth(ansi.Strip(line)); w > l.renderer.width {
			t.Errorf("rendered line width %d exceeds renderer width %d (row would wrap): %q",
				w, l.renderer.width, ansi.Strip(line))
		}
	}

	// The stored display name must be untouched — sanitization is render-only.
	require.Equal(t, family, insts[0].DisplayName(),
		"sanitization must not mutate the stored display name")
}
