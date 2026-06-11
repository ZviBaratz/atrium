package app

import (
	"testing"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/ui"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/stretchr/testify/require"
)

func newRenameTestHome(t *testing.T) (*home, []*session.Instance) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	spin := spinner.New(spinner.WithSpinner(spinner.MiniDot))
	list := ui.NewList(&spin)

	a, err := session.NewInstance(session.InstanceOptions{Title: "alpha", Path: ".", Program: "echo"})
	require.NoError(t, err)
	b, err := session.NewInstance(session.InstanceOptions{Title: "beta", Path: ".", Program: "echo"})
	require.NoError(t, err)
	list.AddInstance(a)()
	list.AddInstance(b)()

	return &home{list: list, appConfig: config.DefaultConfig()}, []*session.Instance{a, b}
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

// Duplicate rejection is scoped to the repo group: a session in another group may
// share the title (that is the point of repo-qualified tmux names).
func TestDeepRename_AllowsSameTitleAcrossGroups(t *testing.T) {
	h, insts := newRenameTestHome(t)
	other, err := session.NewInstance(session.InstanceOptions{
		Title: "elsewhere", Path: t.TempDir(), Program: "echo", Direct: true,
	})
	require.NoError(t, err)
	h.list.AddInstance(other)()

	// alpha is unstarted so the rename still fails — but at the started check,
	// AFTER the duplicate guard. An "already exists" error would mean the guard
	// wrongly counted a session from another group.
	err = h.deepRename(insts[0], "elsewhere")
	require.Error(t, err)
	require.NotContains(t, err.Error(), "already exists")
}

// Same-group rejection compares derived names, not raw titles: a variant that
// sanitizes to the same tmux segment or branch slug would still collide.
func TestDeepRename_RejectsDerivedVariantInGroup(t *testing.T) {
	h, insts := newRenameTestHome(t)
	// insts[1] is "beta" in the same group; "Beta" differs only by case.
	err := h.deepRename(insts[0], "Beta")
	require.Error(t, err)
	require.Contains(t, err.Error(), "already exists")
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
