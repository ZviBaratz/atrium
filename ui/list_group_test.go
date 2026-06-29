package ui

import (
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/ui/theme"
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
	l := NewList(&s)
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

// A single-repo list still shows the project as a section header so the user
// always sees which repo the session(s) belong to. The lone group is not
// foldable, so its header is a plain label with no fold marker — distinguishing
// it from the foldable multi-repo headers guarded above.
func TestList_RendersHeaderForSingleRepo(t *testing.T) {
	l := newGroupList(t, "/tmp/repoA", "/tmp/repoA")

	out := l.String()
	require.Contains(t, out, "REPOA")
	require.NotContains(t, out, theme.Current().Glyphs.FoldOpen)
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

// A collapsed group hides its member rows, so a session blocked on user input becomes
// invisible. The collapsed header is badged with the waiting glyph followed by the count of
// needs-input sessions in the group, so an attention-needing group is spotted without expanding
// it.
func TestList_CollapsedHeaderBadgesNeedsInputCount(t *testing.T) {
	t.Cleanup(theme.Set("unicode"))
	l := newGroupList(t, "/tmp/repoA", "/tmp/repoA", "/tmp/repoB")
	l.items[0].SetStatus(session.NeedsInput)
	l.items[1].SetStatus(session.NeedsInput)
	l.SetCollapsedRepos([]string{"repoA"})

	badge := theme.Current().Glyphs.Waiting + "2"
	require.Contains(t, l.String(), badge, "collapsed group with 2 needs-input sessions shows %q", badge)
}

// With no session blocked on input, the collapsed header carries no badge — the waiting glyph
// must not appear at all (the only other place it shows is a member row, which is hidden while
// collapsed).
func TestList_CollapsedHeaderNoBadgeWhenNoNeedsInput(t *testing.T) {
	t.Cleanup(theme.Set("unicode"))
	l := newGroupList(t, "/tmp/repoA", "/tmp/repoA", "/tmp/repoB")
	l.SetCollapsedRepos([]string{"repoA"})

	require.NotContains(t, l.String(), theme.Current().Glyphs.Waiting, "no needs-input session means no badge")
}

// An expanded group never badges its header: its member rows already carry the per-row waiting
// glyph, so a header badge would be redundant. Collapsing repoB keeps the (multi-repo) header
// path active while leaving repoA — the group under test — expanded.
func TestList_ExpandedHeaderHasNoNeedsInputBadge(t *testing.T) {
	t.Cleanup(theme.Set("unicode"))
	l := newGroupList(t, "/tmp/repoA", "/tmp/repoA", "/tmp/repoB")
	l.items[0].SetStatus(session.NeedsInput)
	l.SetCollapsedRepos([]string{"repoB"})

	out := l.String()
	// The expanded repoA member row shows one waiting glyph; the header must not add a "◆1"
	// count badge on top of it.
	require.NotContains(t, out, theme.Current().Glyphs.Waiting+"1", "expanded header must not carry a count badge")
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
