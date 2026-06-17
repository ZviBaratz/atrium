package ui

import (
	"testing"

	"github.com/ZviBaratz/atrium/session"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/stretchr/testify/require"
)

// ActiveInstancesInView returns every pausable row, in list order — the batch
// "pause all" scope. It skips Paused rows (nothing to park) and Loading rows
// (still starting; pausing would race Start(), which is why single-pause refuses
// them too). A Loading session left unparked is the recovery loop's job, not a gap.
func TestActiveInstancesInView_OnlyActive(t *testing.T) {
	l, insts := newFilterList(t, "alpha", "bravo", "charlie", "delta")
	insts[0].SetStatus(session.Running)
	insts[1].SetStatus(session.Paused)
	insts[2].SetStatus(session.Loading)
	insts[3].SetStatus(session.Ready)

	require.Equal(t, []string{"alpha", "delta"}, titlesOf(l.ActiveInstancesInView()))
}

// An active filter narrows the scope: only active rows that match the filter are
// returned (an active-but-unmatched row is excluded).
func TestActiveInstancesInView_RespectsFilter(t *testing.T) {
	l, insts := newFilterList(t, "alpha", "bravo", "charlie")
	for _, inst := range insts {
		inst.SetStatus(session.Running)
	}

	l.SetFilter("alph")

	require.Equal(t, []string{"alpha"}, titlesOf(l.ActiveInstancesInView()))
}

// Collapse is a display fold, not a scope boundary: an active member of a
// collapsed group is still returned so "pause all" parks it.
func TestActiveInstancesInView_IncludesCollapsed(t *testing.T) {
	l := newMultiRepoList(t) // alpha, apex (repoA), bravo (repoB)
	for _, inst := range l.GetInstances() {
		inst.SetStatus(session.Running)
	}

	l.SetSelectedInstance(0) // a repoA item
	require.True(t, l.Collapse(), "precondition: repoA collapses")

	got := titlesOf(l.ActiveInstancesInView())
	require.Contains(t, got, "apex", "a folded-away active row must still be in scope")
	require.Len(t, got, 3, "every active row is included regardless of collapse")
}

// A direct (non-git) session has no worktree to free, so it cannot be parked and
// is excluded from the "pause all" scope even when active.
func TestActiveInstancesInView_ExcludesDirect(t *testing.T) {
	s := spinner.New()
	l := NewList(&s)

	branch, err := session.NewInstance(session.InstanceOptions{Title: "git", Path: "/tmp/repoA", Program: "echo"})
	require.NoError(t, err)
	branch.SetStatus(session.Running)
	l.AddInstance(branch)

	direct, err := session.NewInstance(session.InstanceOptions{Title: "direct", Path: ".", Program: "echo", Direct: true})
	require.NoError(t, err)
	direct.SetStatus(session.Running)
	l.AddInstance(direct)
	l.SetSize(80, 40)

	require.Equal(t, []string{"git"}, titlesOf(l.ActiveInstancesInView()))
}

// A Loading session is still starting (Start() is mid-flight and the
// Loading→Running transition is owned by the main loop), so pausing it would
// race that setup. It is excluded from "pause all" scope, mirroring the
// single-pause guard; the recovery loop parks it after a restart if needed.
func TestActiveInstancesInView_ExcludesLoading(t *testing.T) {
	l, insts := newFilterList(t, "alpha", "bravo")
	insts[0].SetStatus(session.Running)
	insts[1].SetStatus(session.Loading)

	require.Equal(t, []string{"alpha"}, titlesOf(l.ActiveInstancesInView()))
}

// No active rows yields an empty slice (the caller short-circuits to a notice).
func TestActiveInstancesInView_NoneWhenAllPaused(t *testing.T) {
	l, insts := newFilterList(t, "alpha", "bravo")
	for _, inst := range insts {
		inst.SetStatus(session.Paused)
	}

	require.Empty(t, l.ActiveInstancesInView())
}
