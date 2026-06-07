package ui

import (
	"testing"

	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/session/git"
	"github.com/stretchr/testify/require"
)

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
	require.NotContains(t, out, "pick project", "the n/N distinction is noise with zero sessions")
}

// A paused session can't be opened or sent to — the bar must advertise what
// actually works (resume, kill) instead of actions that silently no-op.
func TestMenu_PausedHintLine(t *testing.T) {
	inst, err := session.NewInstance(session.InstanceOptions{Title: "t", Path: t.TempDir(), Program: "echo"})
	require.NoError(t, err)
	inst.SetStatus(session.Paused)

	m := NewMenu()
	m.SetSize(200, 3)
	m.SetInstance(inst)

	out := m.String()
	require.Contains(t, out, "resume")
	require.Contains(t, out, "kill")
	require.NotContains(t, out, "open", "a paused session cannot be attached")
	require.NotContains(t, out, "send", "a paused session cannot receive messages")
}

// A session with work on its branch surfaces the pause/push pair; a clean one
// keeps the bar short.
func TestMenu_DirtyHintLineAddsPausePush(t *testing.T) {
	inst, err := session.NewInstance(session.InstanceOptions{Title: "t", Path: t.TempDir(), Program: "echo"})
	require.NoError(t, err)
	inst.SetStatus(session.Running)
	inst.SetDiffStats(&git.DiffStats{Added: 3, Removed: 1, Content: "x"})

	m := NewMenu()
	m.SetSize(200, 3)
	m.SetInstance(inst)

	out := m.String()
	require.Contains(t, out, "pause")
	require.Contains(t, out, "push")

	inst.SetDiffStats(&git.DiffStats{}) // clean
	m.SetInstance(inst)
	out = m.String()
	require.NotContains(t, out, "pause", "a clean session has nothing to pause")
	require.NotContains(t, out, "push", "a clean session has nothing to push")
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
