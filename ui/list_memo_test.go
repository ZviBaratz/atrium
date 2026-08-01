package ui

import (
	"testing"

	"github.com/ZviBaratz/atrium/internal/memo"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/ui/theme"

	"charm.land/bubbles/v2/spinner"
	"github.com/stretchr/testify/require"
)

// The list's panel chrome is memoized on panelKey (#565): the rows are re-rendered
// every frame, the border, title and badges around them are not. Same discipline as
// the tabbed window's tests — count the composes, never just compare the frames.

func newMemoList(t *testing.T, n int) *List {
	t.Helper()
	t.Cleanup(theme.Set("unicode"))
	s := spinner.New()
	l := NewList(&s)
	for i := range n {
		inst, err := session.NewInstance(session.InstanceOptions{
			Title: string(rune('a' + i)), Path: t.TempDir(), Program: "echo",
		})
		require.NoError(t, err)
		l.AddInstance(inst)()
	}
	l.SetSize(40, 20)
	return l
}

func TestListPanelMemo_UnchangedListDrawsThePanelOnce(t *testing.T) {
	l := newMemoList(t, 3)

	first := l.String()
	for range 9 {
		require.Equal(t, first, l.String(), "an unchanged list must render an identical panel")
	}

	require.Equal(t, 1, l.PanelComposeRuns(), "10 renders of an unchanged list must draw the panel once")
}

// The negative control: a body change must reach the panel. Without it the test
// above passes against a memo that never invalidates, which would pin the panel to
// whatever the list contained on its first frame.
func TestListPanelMemo_ChangedBodyRedrawsThePanel(t *testing.T) {
	l := newMemoList(t, 3)

	first := l.String()
	require.Equal(t, 1, l.PanelComposeRuns())

	l.SetSelectedInstance(1) // a different row carries the accent bar
	second := l.String()

	require.Equal(t, 2, l.PanelComposeRuns(), "a changed body must redraw the panel")
	require.NotEqual(t, first, second)
}

// One case per field of panelKey beyond content.
func TestListPanelMemo_EveryKeyedFieldInvalidates(t *testing.T) {
	cases := []struct {
		name   string
		change func(t *testing.T, l *List)
	}{
		{"width", func(_ *testing.T, l *List) { l.SetSize(50, 20) }},
		{"height", func(_ *testing.T, l *List) { l.SetSize(40, 24) }},
		{"updateBadge", func(_ *testing.T, l *List) { l.SetUpdateBadge("v9.9.9") }},
		{"driftBadge", func(_ *testing.T, l *List) { l.SetDriftBadge("stale") }},
		{"theme palette", func(t *testing.T, _ *List) { t.Cleanup(theme.Set("tokyo-night")) }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			l := newMemoList(t, 3)
			first := l.String()
			require.Equal(t, 1, l.PanelComposeRuns())

			tc.change(t, l)
			second := l.String()

			require.Equal(t, 2, l.PanelComposeRuns(), "changing %s must redraw the panel", tc.name)
			require.NotEqual(t, first, second, "changing %s must change the panel", tc.name)
		})
	}
}

// The saving this memo claims is a quiet-fleet saving, and its own doc comment says
// so: a spinning row rewrites the body ten times a second and misses every time.
// Asserted rather than left as prose, because a PR that quoted the memo's hit rate
// without this would be quoting a number nothing holds it to.
func TestListPanelMemo_ASpinningRowMissesEveryFrame(t *testing.T) {
	l := newMemoList(t, 3)
	l.GetInstances()[0].SetStatus(session.Running)

	_ = l.String()
	require.Equal(t, 1, l.PanelComposeRuns())

	// Advance the spinner the way the app's 10Hz tick does. If this failed to move
	// the frame the panel would HIT and the assertion below would fail, so the test
	// cannot pass by not advancing it.
	for i := range 3 {
		*l.renderer.spinner, _ = l.renderer.spinner.Update(spinner.TickMsg{ID: l.renderer.spinner.ID()})
		_ = l.String()
		require.Equal(t, i+2, l.PanelComposeRuns(), "a moving spinner must miss the panel memo")
	}
}

func TestListPanelMemo_DisabledDrawsEveryTime(t *testing.T) {
	defer memo.SetEnabled(false)()

	l := newMemoList(t, 3)
	first := l.String()
	require.Equal(t, first, l.String())

	require.Equal(t, 2, l.PanelComposeRuns())
}

func TestListPanelMemo_ResetForcesARedraw(t *testing.T) {
	l := newMemoList(t, 3)

	_ = l.String()
	_ = l.String()
	require.Equal(t, 1, l.PanelComposeRuns())

	l.ResetMemo()
	_ = l.String()

	require.Equal(t, 1, l.PanelComposeRuns(), "Reset zeroes the count, so the redraw reads as the first")
}
