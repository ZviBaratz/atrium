package app

import (
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/ui"
	"testing"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/stretchr/testify/require"
)

func newRenameTestHome(t *testing.T) (*home, []*session.Instance) {
	t.Helper()
	spin := spinner.New(spinner.WithSpinner(spinner.MiniDot))
	list := ui.NewList(&spin)

	a, err := session.NewInstance(session.InstanceOptions{Title: "alpha", Path: ".", Program: "echo"})
	require.NoError(t, err)
	b, err := session.NewInstance(session.InstanceOptions{Title: "beta", Path: ".", Program: "echo"})
	require.NoError(t, err)
	list.AddInstance(a)()
	list.AddInstance(b)()

	return &home{list: list}, []*session.Instance{a, b}
}

// Title is the storage key, so a deep rename onto a title another session already owns must
// be rejected (before any side effects) rather than silently colliding two sessions.
func TestDeepRename_RejectsDuplicateTitle(t *testing.T) {
	h, insts := newRenameTestHome(t)
	require.Error(t, h.deepRename(insts[0], "beta"))
	// The instance is untouched.
	require.Equal(t, "alpha", insts[0].Title)
}

func TestDeepRename_RejectsEmptyTitle(t *testing.T) {
	h, insts := newRenameTestHome(t)
	require.Error(t, h.deepRename(insts[0], ""))
}

// Renaming to the same title another check would reject is fine when it's the instance's own
// title — keeping its current title must not trip the duplicate guard.
func TestDeepRename_AllowsKeepingOwnTitle(t *testing.T) {
	h, insts := newRenameTestHome(t)
	// alpha is unstarted, so Rename() will fail at the started check — but only AFTER the
	// duplicate guard passes. A duplicate-title error here would mean the guard wrongly
	// flagged the instance's own title.
	err := h.deepRename(insts[0], "alpha")
	require.Error(t, err)
	require.NotContains(t, err.Error(), "already exists")
}
