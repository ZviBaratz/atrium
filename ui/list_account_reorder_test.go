package ui

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Account-cluster reordering ([ / ]): the chosen order lives in accountOrder, not in
// the canonical manual snapshot, so a cluster move never disturbs the persisted
// session order. Helpers (acctList, orderKeys, persistKeys) live in
// list_group_account_test.go.

// accountsOf returns the rendered cluster sequence — the account of each repo block
// anchor, deduped in display order.
func accountsOf(l *List) []string {
	return l.accountSequence()
}

// An empty order must reproduce the pre-reordering behavior exactly: accounts in
// first-appearance order. This locks the back-compat path an older state file takes.
func TestAccountOrder_EmptyFallsBackToFirstAppearance(t *testing.T) {
	l := acctList(t, "api|work", "sideproj|personal", "infra|work")
	l.SetGroupMode("account")
	require.Equal(t, []string{"work", "personal"}, accountsOf(l))
	require.Equal(t, []string{"api|work", "infra|work", "sideproj|personal"}, orderKeys(l))
}

// A listed account leads, regardless of which session was created first.
func TestAccountOrder_ListedAccountLeads(t *testing.T) {
	l := acctList(t, "api|work", "sideproj|personal", "infra|work")
	l.SetAccountOrder([]string{"personal"})
	l.SetGroupMode("account")
	require.Equal(t, []string{"personal", "work"}, accountsOf(l))
	require.Equal(t, []string{"sideproj|personal", "api|work", "infra|work"}, orderKeys(l))
}

// Every other case here sets the order before the grouping — the startup path, where the
// first cluster build already honors it. The reverse must land too: an order arriving
// while clustering is live rebuilds the view on the spot. assembleHome's call ordering is
// documented as a preference rather than a requirement on the strength of that, so pin it.
func TestAccountOrder_ArrivingAfterGroupingRebuildsTheView(t *testing.T) {
	l := acctList(t, "api|work", "sideproj|personal")
	l.SetGroupMode("account")
	require.Equal(t, []string{"work", "personal"}, accountsOf(l), "first-appearance to begin with")

	l.SetAccountOrder([]string{"personal"})

	require.Equal(t, []string{"personal", "work"}, accountsOf(l))
	require.Equal(t, []string{"sideproj|personal", "api|work"}, orderKeys(l))
}

// Accounts absent from the order keep first-appearance order behind the listed ones,
// so a partial order never scrambles the rest.
func TestAccountOrder_UnlistedFollowInFirstAppearanceOrder(t *testing.T) {
	l := acctList(t, "api|work", "sideproj|personal", "oss|community")
	l.SetAccountOrder([]string{"community"})
	l.SetGroupMode("account")
	require.Equal(t, []string{"community", "work", "personal"}, accountsOf(l))
}

// A name in the order with no live sessions contributes nothing to the view — it is
// kept only so the account regains its slot when a session for it returns.
func TestAccountOrder_AbsentNameDoesNotAffectView(t *testing.T) {
	l := acctList(t, "api|work", "sideproj|personal")
	l.SetAccountOrder([]string{"ghost", "personal", "work"})
	l.SetGroupMode("account")
	require.Equal(t, []string{"personal", "work"}, accountsOf(l))
}

// The no-account bucket still trails last while unlisted (today's rule)...
func TestAccountOrder_NoAccountTrailsWhenUnlisted(t *testing.T) {
	l := acctList(t, "legacy|", "api|work")
	l.SetAccountOrder([]string{"work"})
	l.SetGroupMode("account")
	require.Equal(t, []string{"work", ""}, accountsOf(l))
}

// ...but is an ordinary entry once listed, so it can be moved like any other cluster.
func TestAccountOrder_NoAccountLeadsWhenListedFirst(t *testing.T) {
	l := acctList(t, "api|work", "legacy|")
	l.SetAccountOrder([]string{"", "work"})
	l.SetGroupMode("account")
	require.Equal(t, []string{"", "work"}, accountsOf(l))
	require.Equal(t, []string{"legacy|", "api|work"}, orderKeys(l))
}

func TestMoveAccount_DownSwapsClusters(t *testing.T) {
	l := acctList(t, "api|work", "sideproj|personal")
	l.SetGroupMode("account")
	l.SetSelectedInstance(0) // a work session
	sel := l.items[0]

	require.True(t, l.MoveAccountDown())

	require.Equal(t, []string{"personal", "work"}, accountsOf(l))
	require.Same(t, sel, l.GetSelectedInstance(), "the selection follows its session by identity")
}

func TestMoveAccount_UpSwapsClusters(t *testing.T) {
	l := acctList(t, "api|work", "sideproj|personal")
	l.SetGroupMode("account")
	l.SetSelectedInstance(1) // the personal session
	sel := l.items[1]

	require.True(t, l.MoveAccountUp())

	require.Equal(t, []string{"personal", "work"}, accountsOf(l))
	require.Same(t, sel, l.GetSelectedInstance())
}

// The whole point of holding the order outside manual: a cluster move must leave the
// canonical (persisted) session order completely untouched.
func TestMoveAccount_LeavesCanonicalOrderUntouched(t *testing.T) {
	l := acctList(t, "api|work", "sideproj|personal", "infra|work")
	l.SetGroupMode("account")
	before := persistKeys(l)

	l.SetSelectedInstance(0)
	require.True(t, l.MoveAccountDown())

	require.Equal(t, before, persistKeys(l), "an account move must not touch the manual order")
	require.Equal(t, []string{"api|work", "sideproj|personal", "infra|work"}, persistKeys(l))
}

// A move records the full effective sequence, so the order is explicit from then on
// and no longer depends on first-appearance.
func TestMoveAccount_RecordsOrder(t *testing.T) {
	l := acctList(t, "api|work", "sideproj|personal")
	l.SetGroupMode("account")
	l.SetSelectedInstance(0)
	require.True(t, l.MoveAccountDown())

	require.Equal(t, []string{"personal", "work"}, l.AccountOrder())
}

// A stored-but-absent account interleaved between the two moved clusters keeps its
// slot: the swap is positional within accountOrder, not a remove-and-reinsert.
func TestMoveAccount_AbsentNameKeepsItsSlot(t *testing.T) {
	l := acctList(t, "api|work", "sideproj|personal")
	l.SetAccountOrder([]string{"work", "ghost", "personal"})
	l.SetGroupMode("account")
	l.SetSelectedInstance(0)

	require.True(t, l.MoveAccountDown())

	require.Equal(t, []string{"personal", "ghost", "work"}, l.AccountOrder(),
		"the absent name stays between the two swapped clusters")
	require.Equal(t, []string{"personal", "work"}, accountsOf(l))
}

// The order the app hands in is config.State's own slice, and a move rewrites the order
// in place — so the list must copy on the way in, or a move would silently mutate
// persisted state before (or despite) the save that is supposed to commit it.
func TestAccountOrder_DoesNotAliasTheCallersSlice(t *testing.T) {
	l := acctList(t, "api|work", "sideproj|personal")
	caller := []string{"work", "personal"}
	l.SetAccountOrder(caller)
	l.SetGroupMode("account")
	l.SetSelectedInstance(0)

	require.True(t, l.MoveAccountDown())

	require.Equal(t, []string{"work", "personal"}, caller, "the caller's slice must be untouched")
	require.Equal(t, []string{"personal", "work"}, l.AccountOrder())
}

// ...and the reader must not hand out the live slice either.
func TestAccountOrder_ReaderReturnsACopy(t *testing.T) {
	l := acctList(t, "api|work", "sideproj|personal")
	l.SetAccountOrder([]string{"work", "personal"})
	l.SetGroupMode("account")

	got := l.AccountOrder()
	got[0] = "tampered"

	require.Equal(t, []string{"work", "personal"}, l.AccountOrder(),
		"mutating the returned slice must not reach the list")
}

func TestMoveAccount_NoOpAtTheEnds(t *testing.T) {
	l := acctList(t, "api|work", "sideproj|personal")
	l.SetGroupMode("account")

	l.SetSelectedInstance(0) // leading cluster
	require.False(t, l.MoveAccountUp())
	l.SetSelectedInstance(1) // trailing cluster
	require.False(t, l.MoveAccountDown())
	require.Equal(t, []string{"work", "personal"}, accountsOf(l))
}

func TestMoveAccount_NoOpWithASingleAccount(t *testing.T) {
	l := acctList(t, "api|work", "infra|work")
	l.SetGroupMode("account")
	l.SetSelectedInstance(0)
	require.False(t, l.MoveAccountDown(), "one cluster has nothing to swap with")
	require.False(t, l.AccountReorderEnabled())
}

// A repo whose sessions span accounts renders as ONE cluster (its anchor's), even though
// two distinct accounts are present. The guard must measure clusters, not accounts:
// counting accounts here would claim the move is available, the move itself would refuse,
// and [ / ] would be a dead key with nothing explaining why.
func TestAccountReorderEnabled_CountsClustersNotAccounts(t *testing.T) {
	l := acctList(t, "api|work", "api|personal") // one repo block, two accounts
	l.SetGroupMode("account")

	require.Equal(t, 2, l.distinctAccountCount(), "two accounts are present...")
	require.Equal(t, []string{"work"}, l.accountSequence(), "...but they render as one cluster")

	require.False(t, l.AccountReorderEnabled(), "one cluster is not reorderable")
	require.False(t, l.MoveAccountDown(), "and the guard must agree with the move")
}

func TestMoveAccount_NoOpWhenNotAccountGrouped(t *testing.T) {
	l := acctList(t, "api|work", "sideproj|personal")
	// repo mode: clusters aren't a thing, so the move must refuse rather than
	// silently rewrite an order nothing renders.
	l.SetSelectedInstance(0)
	require.False(t, l.AccountReorderEnabled())
	require.False(t, l.MoveAccountDown())
	require.Empty(t, l.AccountOrder())
}

// Cluster order and within-block sort are orthogonal, so unlike J/K a cluster move
// stays available under a status sort.
func TestMoveAccount_WorksUnderStatusSort(t *testing.T) {
	l := acctList(t, "api|work", "sideproj|personal")
	l.SetGroupMode("account")
	l.SetSortMode("status")
	l.SetSelectedInstance(0)

	require.True(t, l.MoveAccountDown())
	require.Equal(t, []string{"personal", "work"}, accountsOf(l))
}

// A repo whose sessions span accounts renders under its anchor's divider, so the
// move must key off the anchor — not whichever row happens to be selected.
func TestMoveAccount_MixedRepoMovesByItsAnchorAccount(t *testing.T) {
	// Both sessions live in repo "api"; the anchor is work, the second is personal.
	l := acctList(t, "api|work", "api|personal", "oss|community")
	l.SetGroupMode("account")
	require.Equal(t, []string{"work", "community"}, accountsOf(l),
		"the api block clusters under its anchor's account")

	l.SetSelectedInstance(1) // the personal row inside the work-anchored block
	require.True(t, l.MoveAccountDown())

	require.Equal(t, []string{"community", "work"}, accountsOf(l),
		"the move applies to the anchor's cluster, not the selected row's account")
}
