package ui

import (
	"testing"

	"charm.land/bubbles/v2/spinner"
	"github.com/ZviBaratz/atrium/session"
	"github.com/stretchr/testify/require"
)

// NumActiveInstances counts every live (non-Paused) session — the host-load count
// behind the soft session cap. Unlike ActiveInstancesInView (the "pause all"
// scope), it counts Loading rows too: a Loading session is spinning up an agent
// and already imposes load. Only Paused is excluded (no worktree, no process).
func TestNumActiveInstances_CountsNonPausedIncludingLoading(t *testing.T) {
	l, insts := newFilterList(t, "alpha", "bravo", "charlie", "delta")
	insts[0].SetStatus(session.Running)
	insts[1].SetStatus(session.Paused)
	insts[2].SetStatus(session.Loading)
	insts[3].SetStatus(session.Ready)

	require.Equal(t, 3, l.NumActiveInstances(), "Running + Loading + Ready count; Paused does not")
}

// The cap is a whole-fleet limit, so the count ignores the active filter (an
// unmatched live session still imposes load), mirroring NumInstances.
func TestNumActiveInstances_IgnoresFilter(t *testing.T) {
	l, insts := newFilterList(t, "alpha", "bravo", "charlie")
	for _, inst := range insts {
		inst.SetStatus(session.Running)
	}

	l.SetFilter("alph")

	require.Equal(t, 3, l.NumActiveInstances(), "a filtered-out live session still counts")
}

// A direct (non-git) session runs an agent in place, so it imposes load and
// counts — unlike ActiveInstancesInView, which excludes direct (nothing to park).
func TestNumActiveInstances_IncludesDirect(t *testing.T) {
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

	require.Equal(t, 2, l.NumActiveInstances(), "a direct live session counts toward host load")
}

// All-paused yields zero: a parked fleet imposes no load, so the soft cap never
// fires on it.
func TestNumActiveInstances_ZeroWhenAllPaused(t *testing.T) {
	l, insts := newFilterList(t, "alpha", "bravo")
	for _, inst := range insts {
		inst.SetStatus(session.Paused)
	}

	require.Equal(t, 0, l.NumActiveInstances())
}
