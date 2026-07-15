package ui

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/session"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

// acctList builds a list whose instances carry a Path (→ repo group key = base)
// and a Claude account. Each spec is "repoBase|account"; account "" means no
// account. Titles are unique so selection can be asserted by identity.
func acctList(t *testing.T, specs ...string) *List {
	t.Helper()
	s := spinner.New()
	l := NewList(&s)
	for i, spec := range specs {
		base, acct := splitSpec(spec) // spec form "repoBase|account"
		inst, err := session.NewInstance(session.InstanceOptions{
			Title: string(rune('a' + i)), Path: "/tmp/" + base, Program: "echo",
		})
		require.NoError(t, err)
		if acct != "" {
			inst.SetClaudeAccount(acct, "", false)
		}
		l.AddInstance(inst)
	}
	l.SetSize(80, 40)
	return l
}

func splitSpec(s string) (repo, acct string) {
	for i := 0; i < len(s); i++ {
		if s[i] == '|' {
			return s[:i], s[i+1:]
		}
	}
	return s, ""
}

// orderKeys returns "repoBase|account" per item in list order.
func orderKeys(l *List) []string {
	out := make([]string, 0, len(l.items))
	for _, it := range l.items {
		out = append(out, filepath.Base(it.Path)+"|"+it.ClaudeAccountName())
	}
	return out
}

// persistKeys returns "repoBase|account" per item in InstancesForPersist order —
// the canonical (manual) order that must never reflect a view transform.
func persistKeys(l *List) []string {
	out := make([]string, 0, len(l.items))
	for _, it := range l.InstancesForPersist() {
		out = append(out, filepath.Base(it.Path)+"|"+it.ClaudeAccountName())
	}
	return out
}

func TestGroupMode_ClustersByAccount(t *testing.T) {
	// Interleaved input: work, personal, work.
	l := acctList(t, "api|work", "sideproj|personal", "infra|work")
	l.SetGroupMode("account")
	// work cluster (first appearance) then personal; repos keep first-seen order.
	require.Equal(t, []string{"api|work", "infra|work", "sideproj|personal"}, orderKeys(l))
}

func TestGroupMode_NoAccountBucketTrailsLast(t *testing.T) {
	l := acctList(t, "legacy|", "api|work")
	l.SetGroupMode("account")
	require.Equal(t, []string{"api|work", "legacy|"}, orderKeys(l))
}

func TestGroupMode_SingleAccountLeavesOrderUnchanged(t *testing.T) {
	l := acctList(t, "api|work", "infra|work")
	before := orderKeys(l)
	l.SetGroupMode("account")
	require.Equal(t, before, orderKeys(l), "≤1 distinct account is a no-op reorder")
	require.Equal(t, 1, l.distinctAccountCount())
}

func TestGroupMode_DoesNotOverwritePersistedOrder(t *testing.T) {
	l := acctList(t, "api|work", "sideproj|personal", "infra|work")
	l.SetGroupMode("account")
	persist := make([]string, 0, 3)
	for _, it := range l.InstancesForPersist() {
		persist = append(persist, filepath.Base(it.Path)+"|"+it.ClaudeAccountName())
	}
	// Persisted (canonical/manual) order stays the interleaved input order.
	require.Equal(t, []string{"api|work", "sideproj|personal", "infra|work"}, persist)
}

func TestGroupMode_RoundTripRestoresRepoOrder(t *testing.T) {
	l := acctList(t, "api|work", "sideproj|personal", "infra|work")
	before := orderKeys(l)
	l.SetGroupMode("account")
	l.SetGroupMode("repo")
	require.Equal(t, before, orderKeys(l), "switching back restores canonical order")
}

func TestGroupMode_PreservesSelectionByIdentity(t *testing.T) {
	l := acctList(t, "api|work", "sideproj|personal", "infra|work")
	l.SelectInstance(l.items[1]) // the personal session
	sel := l.GetSelectedInstance()
	l.SetGroupMode("account")
	require.Same(t, sel, l.GetSelectedInstance())
}

// Account grouping only reorders whole repo blocks, so J/K within-group reordering
// stays available under it (unlike a status sort, which owns within-group order).
func TestGroupMode_KeepsManualReorderButSortDisablesIt(t *testing.T) {
	l := acctList(t, "api|work", "infra|personal")
	require.True(t, l.ManualReorderEnabled())
	l.SetGroupMode("account")
	require.True(t, l.ManualReorderEnabled(), "account grouping leaves J/K available")
	l.SetSortMode("status")
	require.False(t, l.ManualReorderEnabled(), "a status sort disables J/K")
}

// A whole-group move within an account cluster is allowed and reflected in both the
// display and the canonical (persisted) order.
func TestGroupMode_GroupMoveWithinClusterSucceeds(t *testing.T) {
	// Clustered display: api(work), infra(work), sideproj(personal).
	l := acctList(t, "api|work", "sideproj|personal", "infra|work")
	l.SetGroupMode("account")
	l.SelectInstance(l.items[0]) // api, the work cluster's first block
	sel := l.GetSelectedInstance()

	require.True(t, l.MoveGroupDown(), "moving api past infra stays within the work cluster")
	require.Equal(t, []string{"infra|work", "api|work", "sideproj|personal"}, orderKeys(l),
		"api and infra swap within the work cluster; personal untouched")
	require.Same(t, sel, l.GetSelectedInstance(), "selection follows the moved session")
	// Canonical order reflects the transpose but is NOT clusterized: personal stays
	// interleaved where it was, only the two work blocks swapped.
	require.Equal(t, []string{"infra|work", "sideproj|personal", "api|work"}, persistKeys(l))
	// items stays consistent with a fresh cluster of the canonical order.
	require.Equal(t, l.items, clusterByAccount(l.InstancesForPersist(), l.AccountOrder()))
}

func TestGroupMode_RendersAccountDividers(t *testing.T) {
	l := acctList(t, "api|work", "sideproj|personal")
	l.SetGroupMode("account")
	out := ansi.Strip(l.String())
	require.Contains(t, out, "── work", "work divider present")
	require.Contains(t, out, "── personal", "personal divider present")
}

func TestGroupMode_SuppressesRowAccountBadgeWhenGrouped(t *testing.T) {
	// Two work sessions in one repo + a personal repo → grouping active (2 accounts).
	// No name here contains the substring "work" except the account name itself, so
	// counting "work" cleanly separates row badges (repo mode) from the divider.
	l := acctList(t, "api|work", "api|work", "sideproj|personal")
	require.Equal(t, 2, strings.Count(ansi.Strip(l.String()), "work"), "two row badges in repo mode")
	l.SetGroupMode("account")
	// Badges suppressed; "work" now survives only in the single work divider.
	require.Equal(t, 1, strings.Count(ansi.Strip(l.String()), "work"), "badges gone, one divider remains")
}

func TestGroupMode_NoDividerWithSingleAccount(t *testing.T) {
	l := acctList(t, "api|work", "infra|work")
	l.SetGroupMode("account")
	out := ansi.Strip(l.String())
	require.NotContains(t, out, "── work", "no divider when only one account")
	require.Equal(t, 2, strings.Count(out, "work"), "row badges kept when grouping is a no-op")
}

// The divider is emitted once per contiguous account *cluster*, not once per repo:
// two distinct work repos (api, infra) must share a single "work" divider once
// clustering makes them adjacent, proving the divider tracks account-block
// boundaries rather than repo-group boundaries.
func TestGroupMode_DividerOncePerAccountClusterSpanningRepos(t *testing.T) {
	l := acctList(t, "api|work", "sideproj|personal", "infra|work")
	l.SetGroupMode("account")
	out := ansi.Strip(l.String())

	require.Equal(t, 1, strings.Count(out, "── work"), "one work divider spans both work repos")
	require.Equal(t, 1, strings.Count(out, "── personal"), "one personal divider")
	require.Contains(t, out, "API", "api repo header present")
	require.Contains(t, out, "INFRA", "infra repo header present")
	require.Equal(t, []string{"api|work", "infra|work", "sideproj|personal"}, orderKeys(l),
		"work repos (api, infra) are adjacent, then personal")
}

// Combining account clustering with status sorting must produce the same
// clustered-then-status-ordered result whichever order the two view transforms
// are toggled in (sort→group or group→sort), and InstancesForPersist must stay
// the canonical creation order in both cases — the snapshot never captures a
// transformed order regardless of toggle sequence. This is because rebuildView
// always derives items from the canonical l.manual snapshot rather than from
// whatever l.items happened to be, so order of activation cannot matter.
func TestGroupMode_SortAndAccountCombineRegardlessOfToggleOrder(t *testing.T) {
	// Repo-contiguous insertion (via AddInstance) turns the interleaved specs into
	// canonical creation order api,api,sideproj,sideproj,infra — titled a,d,b,e,c
	// respectively (title reflects spec position, not final placement).
	build := func(t *testing.T) (l *List, a, b, c, d, e *session.Instance) {
		t.Helper()
		l = acctList(t, "api|work", "sideproj|personal", "infra|work", "api|work", "sideproj|personal")
		byTitle := func(title string) *session.Instance {
			for _, it := range l.items {
				if it.Title == title {
					return it
				}
			}
			t.Fatalf("no instance titled %q", title)
			return nil
		}
		a, b, c, d, e = byTitle("a"), byTitle("b"), byTitle("c"), byTitle("d"), byTitle("e")
		a.SetStatus(session.Running)
		d.SetStatus(session.NeedsInput)
		b.SetStatus(session.Running)
		e.SetStatus(session.NeedsInput)
		c.SetStatus(session.Running)
		return
	}

	// Account-clustered (work: api then infra; personal: sideproj), and within each
	// repo group NeedsInput sorts ahead of Running.
	want := func(a, b, c, d, e *session.Instance) []*session.Instance {
		return []*session.Instance{d, a, c, e, b}
	}
	// Canonical creation order (repo-contiguous), untouched by either view transform.
	canonical := func(a, b, c, d, e *session.Instance) []*session.Instance {
		return []*session.Instance{a, d, b, e, c}
	}

	t.Run("sort then group", func(t *testing.T) {
		l, a, b, c, d, e := build(t)
		l.SetSortMode("status")
		l.SetGroupMode("account")
		require.Equal(t, want(a, b, c, d, e), l.items, "sort-then-group order")
		require.Equal(t, canonical(a, b, c, d, e), l.InstancesForPersist(), "persisted order stays canonical")
	})

	t.Run("group then sort", func(t *testing.T) {
		l, a, b, c, d, e := build(t)
		l.SetGroupMode("account")
		l.SetSortMode("status")
		require.Equal(t, want(a, b, c, d, e), l.items, "group-then-sort order")
		require.Equal(t, canonical(a, b, c, d, e), l.InstancesForPersist(), "persisted order stays canonical")
	})
}

// AddInstance while account-grouped must place the new session contiguous with
// its account's cluster in the display order, while InstancesForPersist keeps the
// canonical repo-contiguous order (the new session lands next to its repo's
// existing entry, not wherever the clustered view happened to put it).
func TestGroupMode_AddInstanceLandsInAccountCluster(t *testing.T) {
	l := acctList(t, "api|work", "sideproj|personal", "infra|work")
	l.SetGroupMode("account")
	require.Equal(t, []string{"api|work", "infra|work", "sideproj|personal"}, orderKeys(l))

	newInst, err := session.NewInstance(session.InstanceOptions{
		Title: "z", Path: "/tmp/api", Program: "echo",
	})
	require.NoError(t, err)
	newInst.SetClaudeAccount("work", "", false)
	l.AddInstance(newInst)

	require.Equal(t, []string{"api|work", "api|work", "infra|work", "sideproj|personal"}, orderKeys(l),
		"new work session lands inside the contiguous work cluster")
	require.Equal(t, []string{"api|work", "api|work", "sideproj|personal", "infra|work"}, persistKeys(l),
		"persisted order stays canonical (repo-contiguous), new session next to its repo's existing entry")
}

// KillInstance while account-grouped must keep the display clustered and drop the
// killed session from InstancesForPersist while keeping that order canonical
// (repo-contiguous). The killed target here is an unstarted NewInstance, so
// target.Kill() is a harmless no-op (no live tmux session or worktree to tear
// down), matching the pattern already used by TestSessionSort_KillReflectedInCanonicalOrder.
func TestGroupMode_KillInstanceKeepsClusteringAndCanonicalPersist(t *testing.T) {
	l := acctList(t, "api|work", "sideproj|personal", "infra|work")
	l.SetGroupMode("account")
	require.Equal(t, []string{"api|work", "infra|work", "sideproj|personal"}, orderKeys(l))

	target := l.items[0] // api|work, the first work-cluster entry
	_ = l.KillInstance(target)

	// Removing api leaves canonical order sideproj,infra: personal now leads the
	// first-appearance account order, so the cluster order flips relative to
	// before the kill — a further check that clustering is recomputed from the
	// post-removal canonical order, not patched in place.
	require.Equal(t, []string{"sideproj|personal", "infra|work"}, orderKeys(l),
		"clustering stays consistent with the killed session gone")
	require.Equal(t, []string{"sideproj|personal", "infra|work"}, persistKeys(l),
		"persisted order drops the killed session and stays canonical")
}

// A whole-group move that would cross an account boundary is refused: the personal
// cluster's only block cannot move up into the work cluster, since re-clustering
// would immediately undo it. GroupMoveCrossesAccount reports the boundary so the app
// can explain it.
func TestGroupMode_GroupMoveAcrossAccountBoundaryRefused(t *testing.T) {
	// Clustered display: api(work), infra(work), sideproj(personal).
	l := acctList(t, "api|work", "sideproj|personal", "infra|work")
	l.SetGroupMode("account")

	l.SelectInstance(l.items[2]) // sideproj, first (only) block of the personal cluster
	require.True(t, l.GroupMoveCrossesAccount(true), "moving personal up crosses into work")
	require.False(t, l.MoveGroupUp(), "the cross-account move is a no-op")
	require.Equal(t, []string{"api|work", "infra|work", "sideproj|personal"}, orderKeys(l), "order unchanged")

	l.SelectInstance(l.items[1]) // infra, last block of the work cluster
	require.True(t, l.GroupMoveCrossesAccount(false), "moving infra down crosses into personal")
	require.False(t, l.MoveGroupDown(), "the cross-account move is a no-op")
	require.Equal(t, []string{"api|work", "infra|work", "sideproj|personal"}, orderKeys(l), "order unchanged")
}

// Under a status sort with NO account grouping, a whole-group move across
// different-account repos still works — the account-boundary gate applies only while
// account-grouped, so status-sort group moves are unrestricted.
func TestGroupMode_StatusSortGroupMoveCrossesAccountsFreely(t *testing.T) {
	l := acctList(t, "api|work", "infra|personal")
	l.SetSortMode("status") // status sort only; repo grouping (no clustering)
	require.False(t, l.GroupMoveCrossesAccount(false), "no boundary gate without account grouping")
	l.SelectInstance(l.items[0]) // api|work
	require.True(t, l.MoveGroupDown(), "group move works under a pure status sort")
	require.Equal(t, []string{"infra|personal", "api|work"}, orderKeys(l))
	require.Equal(t, []string{"infra|personal", "api|work"}, persistKeys(l))
}

// J/K reorders sessions within a repo group while account-grouped. The swap is
// mirrored into the canonical order, and items stays == clusterByAccount(manual).
func TestGroupMode_WithinGroupReorderUnderAccountGrouping(t *testing.T) {
	// Two work sessions share repo api; a personal repo makes grouping active.
	l := acctList(t, "api|work", "api|work", "sideproj|personal")
	l.SetGroupMode("account")
	first, second := l.items[0], l.items[1] // both in api
	require.Equal(t, "api", filepath.Base(first.Path))

	l.SelectInstance(second)
	require.True(t, l.MoveUp(), "J/K works within a repo under account grouping")
	require.Equal(t, []*session.Instance{second, first}, l.items[:2], "the two api sessions swapped")
	require.Same(t, second, l.GetSelectedInstance(), "selection follows the moved session")
	require.Equal(t, l.items, clusterByAccount(l.InstancesForPersist(), l.AccountOrder()), "items stays consistent with canonical")
	// A second rebuild is stable (idempotent).
	before := append([]*session.Instance(nil), l.items...)
	l.rebuildView()
	require.Equal(t, before, l.items)
}

// A J/K swap in a mixed-account repo (sessions with differing accounts) must go
// through rebuildView so the block re-clusters by its new anchor account and items
// stays == clusterByAccount(manual). This guards the "no rebuild" shortcut bug.
func TestGroupMode_WithinGroupReorderMixedAccountRepoReclusters(t *testing.T) {
	// repoB holds a work session (anchor) then a personal one; p is a personal repo,
	// w a work repo, so both accounts appear and clustering is active.
	l := acctList(t, "solo|personal", "solo2|work", "shared|work", "shared|personal")
	l.SetGroupMode("account")
	// Select the personal session inside the work-anchored shared repo and move it up
	// to the anchor slot, flipping the block's anchor account to personal.
	var bAnchor, bDiverge *session.Instance
	for _, it := range l.items {
		if filepath.Base(it.Path) == "shared" {
			if it.ClaudeAccountName() == "work" {
				bAnchor = it
			} else {
				bDiverge = it
			}
		}
	}
	require.NotNil(t, bAnchor)
	require.NotNil(t, bDiverge)
	l.SelectInstance(bDiverge)
	require.True(t, l.MoveUp(), "swap within the shared repo")
	require.Equal(t, l.items, clusterByAccount(l.InstancesForPersist(), l.AccountOrder()),
		"items stays consistent even though the swap changed the block's anchor account")
}

// J/K stays disabled under a status sort even while account-grouped (the sort owns
// within-group order).
func TestGroupMode_WithinGroupReorderDisabledUnderStatusSort(t *testing.T) {
	l := acctList(t, "api|work", "api|work", "sideproj|personal")
	l.SetGroupMode("account")
	l.SetSortMode("status")
	l.SelectInstance(l.items[0])
	require.False(t, l.MoveUp())
	require.False(t, l.MoveDown())
}

// A group move within a single account cluster spanning three repos works both ways
// and keeps the selection.
func TestGroupMode_GroupMoveSymmetryWithinCluster(t *testing.T) {
	l := acctList(t, "a|work", "b|work", "c|work")
	l.SetGroupMode("account") // single account: whole list is one cluster
	l.SelectInstance(l.items[0])
	sel := l.GetSelectedInstance()
	require.True(t, l.MoveGroupDown())
	require.Equal(t, []string{"b|work", "a|work", "c|work"}, orderKeys(l))
	require.Same(t, sel, l.GetSelectedInstance())
	require.True(t, l.MoveGroupUp())
	require.Equal(t, []string{"a|work", "b|work", "c|work"}, orderKeys(l))
	require.Same(t, sel, l.GetSelectedInstance())
}

// Round-trip: a group move made in account mode survives switching back to repo
// mode — the canonical order carries the transpose.
func TestGroupMode_GroupMoveRoundTripToRepoMode(t *testing.T) {
	l := acctList(t, "api|work", "sideproj|personal", "infra|work")
	l.SetGroupMode("account")
	l.SelectInstance(l.items[0]) // api
	require.True(t, l.MoveGroupDown())
	l.SetGroupMode("repo")
	require.Equal(t, []string{"infra|work", "sideproj|personal", "api|work"}, orderKeys(l),
		"repo mode shows the canonical order with the transpose applied")
}

// A session whose account diverges from its repo anchor (a mixed-account repo)
// keeps its per-row account badge, so the block's divider and tinted header — which
// speak for the anchor's account — never silently mislabel it. Badge suppression is
// per-row (redundant only when the row matches its cluster), not a global toggle.
func TestGroupMode_KeepsBadgeForAccountDivergingFromRepoAnchor(t *testing.T) {
	// api holds a work session (the block anchor) then a personal one; infra is
	// personal. No title/repo contains "personal", so every "personal" in the output
	// is an account signal: the personal divider label + the diverging row's badge.
	l := acctList(t, "api|work", "api|personal", "infra|personal")
	l.SetGroupMode("account")
	out := ansi.Strip(l.String())
	require.Equal(t, 2, strings.Count(out, "personal"),
		"the personal session under the work-clustered api block keeps its badge")
}

// In account-only mode (no status sort) the per-tick ApplySort must never reorder:
// clustering keys on each session's account and repo, not its status, so a status
// change leaves the order untouched and ApplySort reports no work done.
func TestGroupMode_ApplySortInertWhenAccountOnly(t *testing.T) {
	l := acctList(t, "api|work", "infra|personal")
	l.SetGroupMode("account")
	before := append([]*session.Instance(nil), l.items...)
	l.items[1].SetStatus(session.NeedsInput) // would float to the top under a status sort
	require.False(t, l.ApplySort(), "account-only mode has no status sort to re-apply")
	require.Equal(t, before, l.items, "a status change does not reorder an account-only view")
}
