package ui

import (
	"testing"

	"github.com/ZviBaratz/atrium/session"
	"github.com/stretchr/testify/require"
)

func TestMenu_NewInstanceHintShownOnlyWhileNaming(t *testing.T) {
	m := NewMenu()
	m.SetSize(200, 3)
	m.SetNewInstanceHint("myrepo")

	m.SetState(StateNewInstance)
	require.Contains(t, m.String(), "myrepo", "hint should show while naming a new session")

	// The hint must not leak into other states.
	m.SetState(StateDefault)
	require.NotContains(t, m.String(), "myrepo")
}

// The default bar is a short, fixed line of high-value keys — a reminder that
// keys exist (with ? as the door to the full list), not a reference card.
func TestMenu_DefaultHintLine(t *testing.T) {
	inst, err := session.NewInstance(session.InstanceOptions{Title: "t", Path: t.TempDir(), Program: "echo"})
	require.NoError(t, err)
	m := NewMenu()
	m.SetSize(200, 3)
	m.SetInstance(inst)

	out := m.String()
	for _, want := range []string{"open", "new", "send", "kill", "help"} {
		require.Contains(t, out, want)
	}
	require.NotContains(t, out, "scroll", "the scroll hint is reserved for the scrolling tabs")

	// The diff/terminal tabs add the scroll hint.
	m.SetActiveTab(DiffTab)
	require.Contains(t, m.String(), "scroll")
	m.SetActiveTab(PreviewTab)
	require.NotContains(t, m.String(), "scroll")
}

// With no sessions, the bar surfaces the create/help/quit keys instead.
func TestMenu_EmptyHintLine(t *testing.T) {
	m := NewMenu()
	m.SetSize(200, 3)
	m.SetInstance(nil)

	out := m.String()
	for _, want := range []string{"new", "help", "quit"} {
		require.Contains(t, out, want)
	}
	require.NotContains(t, out, "kill", "no session to kill in the empty state")
}

// The filter state shows its own accept/clear cue.
func TestMenu_FilterHintLine(t *testing.T) {
	m := NewMenu()
	m.SetSize(200, 3)
	m.SetState(StateFilter)

	out := m.String()
	require.Contains(t, out, "accept")
	require.Contains(t, out, "clear")
}
