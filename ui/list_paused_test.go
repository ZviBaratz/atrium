package ui

import (
	"testing"

	"github.com/ZviBaratz/atrium/session"
	"github.com/stretchr/testify/require"
)

func titlesOf(insts []*session.Instance) []string {
	out := make([]string, len(insts))
	for i, inst := range insts {
		out[i] = inst.DisplayName()
	}
	return out
}

// PausedInstancesInView returns only paused rows, in list order, ignoring the
// non-paused ones — the batch "resume all" scope.
func TestPausedInstancesInView_OnlyPaused(t *testing.T) {
	l, insts := newFilterList(t, "alpha", "bravo", "charlie")
	insts[0].SetStatus(session.Paused)
	insts[1].SetStatus(session.Running)
	insts[2].SetStatus(session.Paused)

	require.Equal(t, []string{"alpha", "charlie"}, titlesOf(l.PausedInstancesInView()))
}

// An active filter narrows the scope: only paused rows that match the filter
// are returned (a paused-but-unmatched row is excluded).
func TestPausedInstancesInView_RespectsFilter(t *testing.T) {
	l, insts := newFilterList(t, "alpha", "bravo", "charlie")
	for _, inst := range insts {
		inst.SetStatus(session.Paused)
	}

	l.SetFilter("alph")

	require.Equal(t, []string{"alpha"}, titlesOf(l.PausedInstancesInView()))
}

// Collapse is a display fold, not a scope boundary: a paused member of a
// collapsed group is still returned so "resume all" restores it.
func TestPausedInstancesInView_IncludesCollapsed(t *testing.T) {
	l := newMultiRepoList(t) // alpha, apex (repoA), bravo (repoB)
	for _, inst := range l.GetInstances() {
		inst.SetStatus(session.Paused)
	}

	l.SetSelectedInstance(0) // a repoA item
	require.True(t, l.Collapse(), "precondition: repoA collapses")

	got := titlesOf(l.PausedInstancesInView())
	require.Contains(t, got, "apex", "a folded-away paused row must still be in scope")
	require.Len(t, got, 3, "every paused row is included regardless of collapse")
}

// No paused rows yields an empty slice (the caller short-circuits to a notice).
func TestPausedInstancesInView_NoneWhenAllRunning(t *testing.T) {
	l, insts := newFilterList(t, "alpha", "bravo")
	for _, inst := range insts {
		inst.SetStatus(session.Running)
	}

	require.Empty(t, l.PausedInstancesInView())
}
