package ui

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDriftBadgeRendersInPanel(t *testing.T) {
	l, _ := newFilterList(t, "alpha")
	l.SetSize(80, 24)
	l.SetDriftBadge("⚠ stale")
	require.Contains(t, l.String(), "stale")
}

func TestUpdateAndDriftBadgesCombine(t *testing.T) {
	l, _ := newFilterList(t, "alpha")
	l.SetSize(80, 24)
	l.SetUpdateBadge("⇡ v0.7.1")
	l.SetDriftBadge("⚠ stale")
	out := l.String()
	require.Contains(t, out, "v0.7.1")
	require.Contains(t, out, "stale")
}

// TestBadgesDegradeTogetherWhenNarrow guards the fallback at widths too narrow
// for both full badges: they must collapse to their glyphs together, so the
// drift "⚠" is never dropped while the update badge keeps a slot. A width that
// fits "⇡ ⚠" but not "⇡ v0.7.1 ⚠ stale".
func TestBadgesDegradeTogetherWhenNarrow(t *testing.T) {
	l, _ := newFilterList(t, "alpha")
	l.SetSize(26, 24)
	l.SetUpdateBadge("⇡ v0.7.1")
	l.SetDriftBadge("⚠ stale")
	top := strings.Split(l.String(), "\n")[0]
	require.Contains(t, top, "⚠", "drift glyph must survive the narrow fallback")
	require.Contains(t, top, "⇡", "update glyph must survive the narrow fallback")
	require.NotContains(t, top, "v0.7.1", "full text must not render at this width")
}
