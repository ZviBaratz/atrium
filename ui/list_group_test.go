package ui

import (
	"github.com/ZviBaratz/atrium/session"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/stretchr/testify/require"
)

// repoKeys returns the per-item grouping keys in list order, for asserting that
// same-repo instances stay contiguous.
func repoKeys(l *List) []string {
	keys := make([]string, 0, len(l.items))
	for _, item := range l.items {
		keys = append(keys, filepath.Base(item.Path))
	}
	return keys
}

func newGroupList(t *testing.T, paths ...string) *List {
	t.Helper()
	s := spinner.New()
	l := NewList(&s, false)
	for _, p := range paths {
		inst, err := session.NewInstance(session.InstanceOptions{Title: "x", Path: p, Program: "echo"})
		require.NoError(t, err)
		l.AddInstance(inst)
	}
	l.SetSize(80, 40)
	return l
}

func TestRepoKey_FallsBackToPathBaseWhenUnstarted(t *testing.T) {
	inst, err := session.NewInstance(session.InstanceOptions{Title: "x", Path: "/tmp/repoA", Program: "echo"})
	require.NoError(t, err)
	require.Equal(t, "repoA", repoKey(inst))
}

// Repo headers are derived from the repos actually present in the list, so a multi-repo list
// always shows them. newGroupList adds instances without running the start finalizer, which
// mirrors the churn/migration window where the old incrementally-maintained l.repos counter
// stayed empty (RepoName errors for not-yet-started instances) and headers wrongly vanished
// even though the list plainly spanned multiple repos. This is the regression guard for that.
func TestList_RendersRepoHeadersWhenMultipleRepos(t *testing.T) {
	l := newGroupList(t, "/tmp/repoA", "/tmp/repoA", "/tmp/repoB")

	out := l.String()
	// Headers are uppercased as section dividers.
	require.Contains(t, out, "REPOA")
	require.Contains(t, out, "REPOB")
}

func TestList_NoHeadersForSingleRepo(t *testing.T) {
	l := newGroupList(t, "/tmp/repoA", "/tmp/repoA")

	out := l.String()
	// With a single repo no header is emitted, so the uppercased header token must
	// not appear.
	require.NotContains(t, out, repoHeaderStyle().Render("REPOA"))
	_ = strings.TrimSpace(out)
}

// AddInstance must place a new instance under its existing repo group rather than
// at the end of the list, keeping same-repo instances contiguous.
func TestAddInstance_InsertsUnderExistingGroup(t *testing.T) {
	l := newGroupList(t, "/tmp/repoA", "/tmp/repoB", "/tmp/repoA")
	require.Equal(t, []string{"repoA", "repoA", "repoB"}, repoKeys(l))
}

// A repo split across the list (which the old append-at-end behavior produced)
// would emit its header twice; grouped insertion keeps it to one.
func TestList_NoDuplicateHeaderForInterleavedRepo(t *testing.T) {
	l := newGroupList(t, "/tmp/repoA", "/tmp/repoB", "/tmp/repoA")

	require.Equal(t, 1, strings.Count(l.String(), "REPOA"))
	require.Equal(t, 1, strings.Count(l.String(), "REPOB"))
}

// Reorder must not move an item across a group boundary, since that would split a
// group and reintroduce a duplicate header.
func TestMove_BlockedAtGroupBoundary(t *testing.T) {
	l := newGroupList(t, "/tmp/repoA", "/tmp/repoB")
	before := repoKeys(l)

	l.SetSelectedInstance(1) // first (and only) item of the repoB group
	require.False(t, l.MoveUp())
	require.Equal(t, before, repoKeys(l))

	l.SetSelectedInstance(0) // last item of the repoA group
	require.False(t, l.MoveDown())
	require.Equal(t, before, repoKeys(l))
}

// Reorder within a group is still allowed.
func TestMove_WithinGroupAllowed(t *testing.T) {
	l := newGroupList(t, "/tmp/repoA", "/tmp/repoA")
	first, second := l.items[0], l.items[1]

	l.SetSelectedInstance(0)
	require.True(t, l.MoveDown())
	require.Equal(t, 1, l.selectedIdx)
	require.Equal(t, second, l.items[0])
	require.Equal(t, first, l.items[1])
}
