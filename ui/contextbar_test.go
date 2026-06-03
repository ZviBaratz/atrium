package ui

import (
	"testing"

	"github.com/ZviBaratz/atrium/session"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/stretchr/testify/require"
)

func mkInst(t *testing.T, title, path string) *session.Instance {
	t.Helper()
	i, err := session.NewInstance(session.InstanceOptions{Title: title, Path: path, Program: "echo"})
	require.NoError(t, err)
	return i
}

// siblingInGroup walks the repo group as a ring, skipping members the predicate
// rejects, confined to the group and wrapping at its ends. A lone member (or an
// all-rejected group) yields the input unchanged so an in-session jump is a no-op.
func TestSiblingInGroup_RingWalk(t *testing.T) {
	s := spinner.New()
	l := NewList(&s, false)
	a := mkInst(t, "a", "/tmp/repoA")
	b := mkInst(t, "b", "/tmp/repoA")
	c := mkInst(t, "c", "/tmp/repoA")
	d := mkInst(t, "d", "/tmp/repoB")
	for _, in := range []*session.Instance{a, b, c, d} {
		l.AddInstance(in)
	}

	yes := func(*session.Instance) bool { return true }

	require.Equal(t, b, l.siblingInGroup(a, +1, yes))
	require.Equal(t, c, l.siblingInGroup(b, +1, yes))
	require.Equal(t, a, l.siblingInGroup(c, +1, yes), "next wraps to group start")
	require.Equal(t, c, l.siblingInGroup(a, -1, yes), "prev wraps to group end")
	require.Equal(t, a, l.siblingInGroup(b, -1, yes))

	// d is alone in repoB → no sibling to move to.
	require.Equal(t, d, l.siblingInGroup(d, +1, yes))
	// Navigation never crosses a repo boundary.
	require.NotEqual(t, d, l.siblingInGroup(c, +1, yes))

	// Rejected members are skipped.
	notB := func(i *session.Instance) bool { return i != b }
	require.Equal(t, c, l.siblingInGroup(a, +1, notB), "skips b to reach c")

	// Whole group ineligible → stay put.
	none := func(*session.Instance) bool { return false }
	require.Equal(t, a, l.siblingInGroup(a, +1, none))

	// dir 0 and unknown instance are no-ops.
	require.Equal(t, a, l.siblingInGroup(a, 0, yes))
	require.Equal(t, a, l.siblingInGroup(a, 99, none))
}

func TestComposeSessionContext(t *testing.T) {
	a := mkInst(t, "alpha", "/tmp/repoA")

	name, left := ComposeSessionContext(a, "repoA")

	require.Equal(t, "alpha", name, "name drives the terminal title")
	// The header reads "<glyph> <repo> · <name>".
	require.Contains(t, left, "alpha")
	require.Contains(t, left, "repoA")
}

// With no repo (direct-mode sessions), the header collapses to "<glyph> <name>" with
// no repo field or separator.
func TestComposeSessionContext_NoRepo(t *testing.T) {
	a := mkInst(t, "alpha", "/tmp/repoA")

	_, left := ComposeSessionContext(a, "")

	require.Contains(t, left, "alpha")
	require.NotContains(t, left, "·", "no separator without a repo")
}

// '#' in dynamic text must be escaped so tmux doesn't read it as a format directive.
func TestTmuxEsc(t *testing.T) {
	require.Equal(t, "a##b", tmuxEsc("a#b"))
	require.Equal(t, "plain", tmuxEsc("plain"))
}
