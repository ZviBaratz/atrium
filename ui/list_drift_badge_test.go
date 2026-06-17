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

func TestJoinBadges(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want string
	}{
		{"both empty", []string{"", ""}, ""},
		{"update only", []string{"⇡ v0.7.1", ""}, "⇡ v0.7.1"},
		{"drift only", []string{"", "⚠ stale"}, "⚠ stale"},
		{"both present", []string{"⇡ v0.7.1", "⚠ stale"}, "⇡ v0.7.1 ⚠ stale"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			require.Equal(t, c.want, joinBadges(c.in...))
		})
	}
}
