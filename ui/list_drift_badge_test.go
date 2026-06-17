package ui

import (
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
