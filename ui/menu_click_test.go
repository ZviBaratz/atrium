package ui

import (
	"testing"

	"github.com/ZviBaratz/atrium/session"
	"github.com/stretchr/testify/require"
)

// Each hint-bar variant marks exactly the entries that map to a single
// dispatchable key as click zones, in render order: the default/empty bars mark
// every KeyName by its primary key, the mode bars mark only single-key entries
// (enter/esc/space), and range/compound teaching cues ("a–z", "p/r/x") mark
// nothing so a click on them does nothing surprising.
func TestMenu_HintClickTargetsPerState(t *testing.T) {
	dirty, err := session.NewInstance(session.InstanceOptions{Title: "t", Path: t.TempDir(), Program: "echo"})
	require.NoError(t, err)
	dirty.SetStatus(session.Ready)

	cases := []struct {
		name  string
		setup func(m *Menu)
		want  []string
	}{
		{
			name:  "default",
			setup: func(m *Menu) { m.SetInstance(dirty) },
			// defaultHintKeys: ↵/o open, n new, s send, ctrl-x kill, ? help.
			want: []string{"enter", "n", "s", "ctrl+x", "?"},
		},
		{
			name:  "empty",
			setup: func(m *Menu) { m.SetInstance(nil) },
			want:  []string{"n", "?", "q"},
		},
		{
			name:  "filter",
			setup: func(m *Menu) { m.SetState(StateFilter) },
			// enter accept, esc clear; the predicate-syntax tail is not a key.
			want: []string{"enter", "esc"},
		},
		{
			name:  "hints",
			setup: func(m *Menu) { m.SetState(StateHints) },
			// a–z and A–Z carry no key; only esc is clickable.
			want: []string{"esc"},
		},
		{
			name:  "visual",
			setup: func(m *Menu) { m.SetState(StateVisual) },
			// space marks, esc exits; p/r/x is a three-key compound → inert.
			want: []string{" ", "esc"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := NewMenu()
			m.SetSize(200, 3)
			tc.setup(m)
			_ = m.String() // populates clickTargets
			require.Equal(t, tc.want, m.clickTargets)
		})
	}
}

// A click inside a hint entry's rendered zone resolves back to the key it
// fires; a click outside every entry resolves nothing. Coordinates come from the
// zone's own reported bounds, so the test does not hard-code the bar's layout.
func TestMenu_KeyAtZoneResolvesClick(t *testing.T) {
	m := NewMenu()
	m.SetSize(200, 3)
	m.SetInstance(nil) // empty bar: n / ? / q, deterministic

	for _, key := range []string{"n", "?", "q"} {
		z := waitZone(t, m.String, hintZoneID(key))
		got, ok := m.KeyAtZone(clickAt(z.StartX, z.StartY))
		require.True(t, ok, "click inside %q's zone should hit it", key)
		require.Equal(t, key, got)
	}

	// A click far outside the bar hits no entry.
	_, ok := m.KeyAtZone(clickAt(9999, 9999))
	require.False(t, ok)
}

// The notice / busy / generating bars show no keys, so they register no click
// zones — a stale target from a prior hint bar must not survive the switch.
func TestMenu_NoClickZonesWithoutKeys(t *testing.T) {
	m := NewMenu()
	m.SetSize(200, 3)
	m.SetInstance(nil)
	_ = m.String()
	require.NotEmpty(t, m.clickTargets, "the empty bar has clickable keys")

	m.SetBusy("pushing…")
	_ = m.String()
	require.Empty(t, m.clickTargets, "a busy progress bar carries no keys")

	m.SetState(StateEmpty)
	m.SetNotice("branch copied", NoticeInfo)
	_ = m.String()
	require.Empty(t, m.clickTargets, "a notice replaces the keys, so nothing is clickable")
}
