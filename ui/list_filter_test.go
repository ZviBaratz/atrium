package ui

import (
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/session"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/stretchr/testify/require"
)

// newFilterList builds a single-repo list whose instances carry the given titles (which become
// their DisplayNames). They share one repo path so no group headers render, keeping render
// assertions focused on the rows themselves.
func newFilterList(t *testing.T, titles ...string) (*List, []*session.Instance) {
	t.Helper()
	s := spinner.New()
	l := NewList(&s)
	insts := make([]*session.Instance, 0, len(titles))
	for _, title := range titles {
		inst, err := session.NewInstance(session.InstanceOptions{Title: title, Path: "/tmp/repoA", Program: "echo"})
		require.NoError(t, err)
		l.AddInstance(inst)
		insts = append(insts, inst)
	}
	l.SetSize(80, 40)
	return l, insts
}

// A non-empty filter must remove non-matching rows from the rendered list, not merely from
// navigation. This is the core regression guard: the filter exists to narrow what is shown.
//
// Note: the filter bar echoes the raw query, so a positive "row is visible" assertion uses a
// query *fragment* ("alph") shorter than the name ("alpha") — that way Contains("alpha") can
// only be satisfied by the rendered row, never by the echoed query.
func TestFilter_RenderHidesNonMatchingRows(t *testing.T) {
	l, _ := newFilterList(t, "alpha", "bravo", "charlie")

	l.SetFilter("alph")
	out := l.String()

	require.Contains(t, out, "alpha")
	require.NotContains(t, out, "bravo")
	require.NotContains(t, out, "charlie")
}

// An empty query disables filtering: every row is shown again.
func TestFilter_EmptyQueryRendersAll(t *testing.T) {
	l, _ := newFilterList(t, "alpha", "bravo")

	l.SetFilter("alpha")
	l.SetFilter("")
	out := l.String()

	require.Contains(t, out, "alpha")
	require.Contains(t, out, "bravo")
}

// The match is case-insensitive and also tests the Branch field, not just DisplayName.
func TestFilter_MatchesBranchCaseInsensitively(t *testing.T) {
	l, insts := newFilterList(t, "alpha", "bravo")
	insts[1].Branch = "feature/login"

	l.SetFilter("LOGIN")
	out := l.String()

	require.Contains(t, out, "bravo", "row matched via its Branch should be visible")
	require.NotContains(t, out, "alpha")
}

// When the filter hides the selected item, the selection must land on a still-visible
// (matching) item — never on a hidden one.
func TestFilter_ClampMovesSelectionToVisibleItem(t *testing.T) {
	l, _ := newFilterList(t, "alpha", "bravo", "charlie")

	l.SetSelectedInstance(1) // bravo
	l.SetFilter("charlie")

	require.False(t, l.isHidden(l.selectedIdx), "selection must rest on a visible item")
	require.Equal(t, "charlie", l.GetSelectedInstance().DisplayName())
}

// Regression for the group-anchor clamp bug: clamping snapped to the group's first item, but
// under a filter that first item may itself fail to match. The selection must skip it.
func TestFilter_ClampSkipsHiddenGroupAnchor(t *testing.T) {
	l, _ := newFilterList(t, "alpha", "bravo", "charlie")

	l.SetSelectedInstance(2) // charlie
	l.SetFilter("bravo")     // anchor "alpha" (idx 0) does not match

	require.False(t, l.isHidden(l.selectedIdx))
	require.Equal(t, "bravo", l.GetSelectedInstance().DisplayName())
}

// Navigation only ever lands on matching items, wrapping past the non-matching ones.
func TestFilter_NavigationSkipsNonMatching(t *testing.T) {
	l, _ := newFilterList(t, "alpha", "alphabet", "bravo")

	l.SetFilter("alpha") // matches alpha + alphabet, not bravo
	l.SetSelectedInstance(0)

	l.Down()
	require.Equal(t, "alphabet", l.GetSelectedInstance().DisplayName())
	l.Down()
	require.Equal(t, "alpha", l.GetSelectedInstance().DisplayName(), "should wrap, skipping bravo")
}

// newMultiRepoList builds a list spanning two repos so group headers render.
func newMultiRepoList(t *testing.T) *List {
	t.Helper()
	s := spinner.New()
	l := NewList(&s)
	for _, spec := range []struct{ title, path string }{
		{"alpha", "/tmp/repoA"},
		{"apex", "/tmp/repoA"},
		{"bravo", "/tmp/repoB"},
	} {
		inst, err := session.NewInstance(session.InstanceOptions{Title: spec.title, Path: spec.path, Program: "echo"})
		require.NoError(t, err)
		l.AddInstance(inst)
	}
	l.SetSize(80, 40)
	return l
}

// A group with no matching members renders neither its header nor a separating blank line.
func TestFilter_SuppressesHeaderForFullyFilteredGroup(t *testing.T) {
	l := newMultiRepoList(t)

	l.SetFilter("alpha")
	out := l.String()

	require.Contains(t, out, "REPOA")
	require.NotContains(t, out, "REPOB", "a group with no matches must not render its header")
	require.NotContains(t, out, "bravo")
}

// A query that matches nothing shows an explicit hint and no rows, so the empty list does not
// read as "no sessions exist".
func TestFilter_NoMatchesShowsHint(t *testing.T) {
	l, _ := newFilterList(t, "alpha", "bravo")

	l.SetFilter("zzz")
	out := l.String()

	require.Contains(t, strings.ToLower(out), "no match")
	require.NotContains(t, out, "alpha")
	require.NotContains(t, out, "bravo")
}

// An active filter takes precedence over collapse so matches inside a folded group still surface.
func TestFilter_OverridesCollapse(t *testing.T) {
	l := newMultiRepoList(t)

	l.SetSelectedInstance(0) // a repoA item
	require.True(t, l.ToggleCollapse(), "precondition: repoA collapses")

	// "pex" is a fragment of "apex" so Contains("apex") can only match the rendered row, not
	// the query echoed in the filter bar. apex lives inside the collapsed repoA group.
	l.SetFilter("pex")
	out := l.String()

	require.Contains(t, out, "apex", "a match inside a collapsed group must surface while filtering")
}
