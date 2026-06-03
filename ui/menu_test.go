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

// TestMenu_DirectSessionHidesPushAndPause verifies a direct (non-git) session's menu
// omits both push (no branch to push) and checkout/pause (no worktree to free), while a
// git session still offers both.
func TestMenu_DirectSessionHidesPushAndPause(t *testing.T) {
	mk := func(direct bool) string {
		inst, err := session.NewInstance(session.InstanceOptions{
			Title: "t", Path: t.TempDir(), Program: "echo", Direct: direct,
		})
		require.NoError(t, err)
		m := NewMenu()
		m.SetSize(200, 3)
		m.SetInstance(inst)
		return m.String()
	}

	git := mk(false)
	require.Contains(t, git, "push", "a git session offers push")
	require.Contains(t, git, "checkout", "a git session offers checkout/pause")

	direct := mk(true)
	require.NotContains(t, direct, "push", "a direct session must not offer push")
	require.NotContains(t, direct, "checkout", "a direct session must not offer checkout/pause")
}
