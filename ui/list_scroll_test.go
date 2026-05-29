package ui

import (
	"fmt"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/ui/theme"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/stretchr/testify/require"
)

// scrollList builds a single-repo list of n sessions sized to the given height.
func scrollList(t *testing.T, n, width, height int) *List {
	t.Helper()
	t.Cleanup(theme.Set("unicode"))
	s := spinner.New()
	l := NewList(&s, false)
	for i := 0; i < n; i++ {
		inst, err := session.NewInstance(session.InstanceOptions{
			Title: fmt.Sprintf("sess-%02d", i), Path: ".", Program: "echo",
		})
		require.NoError(t, err)
		l.AddInstance(inst)()
	}
	l.SetSize(width, height)
	return l
}

func TestList_ScrollClipsToHeight(t *testing.T) {
	l := scrollList(t, 20, 40, 12) // 20 rows * 2 lines = 40 lines >> 12
	lines := strings.Split(l.String(), "\n")
	require.Len(t, lines, 12, "output must be exactly the list height")
}

func TestList_ScrollKeepsSelectionVisible(t *testing.T) {
	l := scrollList(t, 20, 40, 12)

	// Select the last item; it must be visible and an up-indicator shown.
	for i := 0; i < 19; i++ {
		l.Down()
	}
	out := l.String()
	require.Contains(t, out, "sess-19", "selected last item must stay visible")
	require.Contains(t, out, "↑", "scrolled down → show an up indicator")

	// Back to the top; first item visible and a down-indicator shown.
	l.Down() // wraps to 0
	out = l.String()
	require.Contains(t, out, "sess-00", "selected first item must stay visible")
	require.Contains(t, out, "↓", "more below → show a down indicator")
	require.NotContains(t, out, "↑ ", "at the top there is nothing above")
}

func TestList_NoScrollWhenItFits(t *testing.T) {
	l := scrollList(t, 3, 40, 30) // 3 rows * 2 = 6 lines < 30
	out := l.String()
	require.NotContains(t, out, "more", "no scroll indicators when content fits")
}
