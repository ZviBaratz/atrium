# Account Grouping Implementation Plan

> **Partially superseded 2026-07-07 (PR #285):** §6's "manual reordering disabled
> in account mode" rule was later reversed — reordering is now enabled within
> account boundaries. See the superseded note in the design spec's §6. The rest of
> this plan (filter, clustering/view machinery, divider/tint, settings wiring)
> stands as implemented.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add an `account:<name>` list-filter predicate and a togglable "group by Claude account" list view mode (cluster repo groups by account, with a divider, tinted headers, and suppressed per-row account badges).

**Architecture:** The list is a flat `[]*session.Instance` kept repo-contiguous, rendered group-by-group; a `sortMode` already derives a *view* over a `manual` snapshot without persisting it. This plan generalizes that "view over manual" machinery from `sortActive()` to `viewActive() = sortActive() || accountGrouped()`, adds account clustering as a second pure-function transform in the same pipeline, and threads a `GroupMode` config preference (settings-driven, like `SessionSort`) through construction. Rendering gains an account divider, a header tint, and account-badge suppression, all gated on `distinctAccountCount() > 1` so the mode is a no-op when accounts aren't configured.

**Tech Stack:** Go, Bubble Tea / lipgloss TUI, testify (`require`). Design spec: `docs/superpowers/specs/2026-07-01-account-grouping-design.md`.

## Global Constraints

- Module path `github.com/ZviBaratz/atrium`; binary `atrium`.
- Tests must stay **hermetic** — never touch the real data dir. The packages here (`session`, `config`, `ui`, `app`) already set `HOME` to a temp dir in `TestMain`; add no new real-FS/tmux writes.
- `ui` package must **not import `config`** — modes are compared as bare string literals in `ui` (`"account"`, `"repo"`), exactly as `sortMode` uses `"status"`/`"creation"`. The `config` constants are the source of truth; `app` passes the normalized string into the list.
- Commits: Conventional Commits, lowercase (`feat(...)`, `refactor(...)`). End each commit body with `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`.
- Verify every task with `just build` **and** `just test` before marking it done — `just test` is the source of truth. Toolchain (`go`, `just`) is mise-managed; if a bare command isn't found, invoke it through mise or its absolute path. Targeted inner-loop runs use `go test ./<pkg>/ -run <TestName> -v`.
- Never hand-edit a version string; `GroupMode` defaults keep older config files behaving exactly as before (`omitempty` + normalize-to-`repo`).

---

### Task 1: `account:` filter predicate

Self-contained. Adds one predicate to the list filter and updates the filter hint.

**Files:**
- Modify: `session/filter.go` (add a `case` in `parseTerm`, add `accountTerm`)
- Modify: `ui/menu.go:249` (filter-hint vocabulary)
- Test: `session/filter_test.go` (new `TestFilter_Account`)

**Interfaces:**
- Consumes: `(*session.Instance).ClaudeAccountName() string` (exists, `session/account.go:35`).
- Produces: filter support for `account:<prefix>` and `account:none`.

- [ ] **Step 1: Write the failing test**

Add to `session/filter_test.go` (uses the existing `newFilterInstance` helper and `SetClaudeAccount`):

```go
func TestFilter_Account(t *testing.T) {
	work := newFilterInstance(t, "deploy", "b")
	work.SetClaudeAccount("work", "", false)
	personal := newFilterInstance(t, "sideproj", "b")
	personal.SetClaudeAccount("personal", "", false)
	none := newFilterInstance(t, "legacy", "b") // no account resolved

	require.True(t, ParseFilter("account:work").Matches(work))
	require.False(t, ParseFilter("account:work").Matches(personal))
	require.True(t, ParseFilter("account:wo").Matches(work), "prefix match")
	require.True(t, ParseFilter("ACCOUNT:WORK").Matches(work), "case-insensitive")
	require.True(t, ParseFilter("account:none").Matches(none), "none matches the empty account")
	require.False(t, ParseFilter("account:none").Matches(work))
	require.True(t, ParseFilter("account:").Matches(personal), "empty value is a no-op")
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./session/ -run TestFilter_Account -v`
Expected: FAIL — `account:work` currently falls through to `substringTerm` and matches on DisplayName/Branch, so `account:work` matches nothing and `Matches(work)` is false.

- [ ] **Step 3: Add the predicate**

In `session/filter.go`, add a case to `parseTerm` (place it next to the `pr:` case, before `default:`):

```go
	case strings.HasPrefix(tok, "account:"):
		return accountTerm(strings.TrimPrefix(tok, "account:"))
```

Then add the term constructor (next to `prTerm`):

```go
// accountTerm matches the session's Claude account name by case-insensitive
// prefix, mirroring statusTerm. An empty value is a no-op (matches every session)
// so a mid-typed "account:" never blinks the list empty. The literal value "none"
// matches sessions with no resolved account (ClaudeAccountName == ""), mirroring
// pr:none.
func accountTerm(value string) term {
	return func(i *Instance) bool {
		name := strings.ToLower(i.ClaudeAccountName())
		if value == "none" {
			return name == ""
		}
		return strings.HasPrefix(name, value)
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./session/ -run TestFilter_Account -v`
Expected: PASS

- [ ] **Step 5: Update the filter hint**

In `ui/menu.go:249`, append `account:` to the predicate vocabulary:

```go
			descStyle().Render("filter: status: dirty behind pr: account:")
```

Then run: `go test ./ui/ -run 'Menu|Filter' -v`
Expected: PASS. If a menu test asserts the exact predicate string, update that expected string to include `account:` the same way.

- [ ] **Step 6: Verify and commit**

Run: `just build && just test`
Expected: build succeeds, all tests pass.

```bash
git add session/filter.go session/filter_test.go ui/menu.go
git commit -m "feat(session): add account: list-filter predicate

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: `GroupMode` config field + accessor

Self-contained config plumbing; no behavior change yet.

**Files:**
- Modify: `config/types.go` (mode constants + `Config.GroupMode` field)
- Modify: `config/accessors.go` (`GetGroupMode`)
- Test: `config/group_mode_test.go` (new)

**Interfaces:**
- Produces: `config.GroupModeRepo`, `config.GroupModeAccount` (string consts); `(*config.Config).GetGroupMode() string` (normalizes empty/unknown → `GroupModeRepo`).

- [ ] **Step 1: Write the failing test**

Create `config/group_mode_test.go`:

```go
package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGetGroupMode(t *testing.T) {
	require.Equal(t, GroupModeRepo, (*Config)(nil).GetGroupMode(), "nil config defaults to repo")
	require.Equal(t, GroupModeRepo, (&Config{}).GetGroupMode(), "empty value defaults to repo")
	require.Equal(t, GroupModeRepo, (&Config{GroupMode: "bogus"}).GetGroupMode(), "unknown normalizes to repo")
	require.Equal(t, GroupModeAccount, (&Config{GroupMode: GroupModeAccount}).GetGroupMode())
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./config/ -run TestGetGroupMode -v`
Expected: FAIL to compile — `GroupModeRepo`, `GroupModeAccount`, and `GetGroupMode` are undefined.

- [ ] **Step 3: Add the constants and field**

In `config/types.go`, add the mode constants next to the `SessionSort` constants (after the `SessionSortStatus` block):

```go
// GroupMode values (Config.GroupMode). See GetGroupMode for normalization. The
// mode is a top-level grouping axis, orthogonal to SessionSort's within-group order.
const (
	// GroupModeRepo is the default: repo groups in manual order, exactly as before
	// this key existed.
	GroupModeRepo = "repo"
	// GroupModeAccount clusters repo groups by their sessions' Claude account
	// (personal/work), with a divider and tinted headers. A no-op when fewer than
	// two distinct accounts are present (e.g. ClaudeAccounts unconfigured).
	GroupModeAccount = "account"
)
```

In the `Config` struct (place next to the `SessionSort` field), add:

```go
	// GroupMode selects the top-level list grouping: "repo" (default — repo groups
	// in manual order) or "account" (cluster repo groups by their sessions' Claude
	// account). Empty or unrecognized values normalize to "repo" (GetGroupMode).
	// Only meaningful when ClaudeAccounts are configured; with fewer than two
	// distinct accounts it renders identically to "repo".
	GroupMode string `json:"group_mode,omitempty"`
```

- [ ] **Step 4: Add the accessor**

In `config/accessors.go`, next to `GetSessionSort`, add:

```go
// GetGroupMode returns the normalized top-level grouping mode: GroupModeAccount,
// or GroupModeRepo for a nil Config, an empty value, or anything unrecognized — a
// typo must never silently regroup the list.
func (c *Config) GetGroupMode() string {
	if c == nil {
		return GroupModeRepo
	}
	switch c.GroupMode {
	case GroupModeAccount:
		return c.GroupMode
	default:
		return GroupModeRepo
	}
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./config/ -run TestGetGroupMode -v`
Expected: PASS

- [ ] **Step 6: Verify and commit**

Run: `just build && just test`
Expected: build succeeds, all tests pass.

```bash
git add config/types.go config/accessors.go config/group_mode_test.go
git commit -m "feat(config): add GroupMode preference (repo/account)

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 3: List view machinery — account clustering

Generalize the `sortMode` "view over manual" pipeline to also cluster by account. This is the core structural change; it lands with tests but no new rendering yet (headers/badges come in Task 4).

**Files:**
- Modify: `ui/list.go` (add `groupMode` field; `accountKey`, `distinctAccountCount`; generalize `AddInstance`, `KillInstance`, `MoveUp`, `MoveDown` guards; add `accountGrouped` account no-op to `MoveGroupUp`/`MoveGroupDown`)
- Modify: `ui/list_sort.go` (add `groupModeAccount` const, `accountGrouped`, `viewActive`, `enterView`, `exitViewInactive`, `SetGroupMode`, `clusterByAccount`, `sortWithinRepoGroups`; refactor `sortActive`, `ManualReorderEnabled`, `InstancesForPersist`, `SetSortMode`, `ApplySort`; replace `applySort` with `rebuildView`)
- Test: `ui/list_group_account_test.go` (new)

**Interfaces:**
- Consumes: `accountKey` uses `(*session.Instance).ClaudeAccountName()`; `repoKey`, `sameOrder`, `insertByRepo`, `removeInstance`, `regroupManualLike` (exist in `ui/list_sort.go`); `GetSelectedInstance`, `SelectInstance`, `clampSelectionToNavigable` (exist in `ui/list.go`).
- Produces (used by Tasks 4 & 5): `(*List).SetGroupMode(mode string)`; `(*List).accountGrouped() bool`; `(*List).distinctAccountCount() int`; `accountKey(*session.Instance) string`. The metadata poll keeps calling `(*List).ApplySort() bool` (now `rebuildView`).

- [ ] **Step 1: Write the failing tests**

Create `ui/list_group_account_test.go`:

```go
package ui

import (
	"path/filepath"
	"testing"

	"github.com/ZviBaratz/atrium/session"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/stretchr/testify/require"
)

// acctList builds a list whose instances carry a Path (→ repo group key = base)
// and a Claude account. Each spec is "repoBase:accountName"; account "" means no
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

func TestGroupMode_DisablesManualReorder(t *testing.T) {
	l := acctList(t, "api|work", "infra|personal")
	require.True(t, l.ManualReorderEnabled())
	l.SetGroupMode("account")
	require.False(t, l.ManualReorderEnabled())
}

func TestGroupMode_GroupMovesAreNoOpInAccountMode(t *testing.T) {
	l := acctList(t, "api|work", "sideproj|personal", "infra|work")
	l.SetGroupMode("account")
	l.SelectInstance(l.items[0])
	require.False(t, l.MoveGroupDown(), "group moves are disabled while account-grouped")
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./ui/ -run TestGroupMode -v`
Expected: FAIL to compile — `SetGroupMode`, `distinctAccountCount` undefined.

- [ ] **Step 3: Add the `groupMode` field**

In `ui/list.go`, in the `List` struct, add a field right after the `manual []*session.Instance` block:

```go
	// groupMode selects the top-level grouping: "" / "repo" keeps repo groups in
	// manual order; "account" clusters repo blocks by Claude account. Like sortMode
	// it drives a view over the manual snapshot (see viewActive/rebuildView) and is
	// compared as a bare literal so ui needs no config import.
	groupMode string
```

- [ ] **Step 4: Add `accountKey` and `distinctAccountCount`**

In `ui/list.go`, next to `repoKey` / `distinctRepoCount`, add:

```go
// accountKey returns the Claude-account grouping key for an instance — its resolved
// account name, or "" for a session with no account (feature off / legacy). Account
// is derived from the repo's remote/path, so every session in a repo shares it,
// which is what lets clusterByAccount move whole repo blocks intact.
func accountKey(i *session.Instance) string {
	return i.ClaudeAccountName()
}

// distinctAccountCount returns how many distinct Claude accounts are present across
// the current items (the empty/no-account key counts as one). It gates the account-
// grouping visuals so "account" mode with fewer than two accounts renders like repo
// mode.
func (l *List) distinctAccountCount() int {
	seen := make(map[string]struct{}, len(l.items))
	for _, item := range l.items {
		seen[accountKey(item)] = struct{}{}
	}
	return len(seen)
}
```

- [ ] **Step 5: Rewrite the view machinery in `ui/list_sort.go`**

Replace the existing `sortActive`, `ManualReorderEnabled`, `InstancesForPersist`, `SetSortMode`, `ApplySort`, and `applySort` functions with the generalized versions below, and add the new helpers. (Leave `sameOrder`, `insertByRepo`, `removeInstance`, `regroupManualLike`, and `syncManualGroupOrder` unchanged.)

```go
// groupModeAccount is the account-clustering group mode (config.GroupModeAccount),
// compared as a bare literal so ui needs no config import — mirroring how sortMode
// uses "status"/"creation".
const groupModeAccount = "account"

// sortActive reports whether a non-creation within-group sort mode is in effect.
func (l *List) sortActive() bool {
	return l.sortMode != "" && l.sortMode != "creation"
}

// accountGrouped reports whether the top-level grouping clusters by Claude account.
func (l *List) accountGrouped() bool {
	return l.groupMode == groupModeAccount
}

// viewActive reports whether any display transform (within-group sort or account
// clustering) is deriving items from the manual snapshot. It generalizes the former
// sortActive() gate: manual is populated and items is a computed view whenever this
// is true.
func (l *List) viewActive() bool {
	return l.sortActive() || l.accountGrouped()
}

// ManualReorderEnabled reports whether J/K and { } manual reordering apply. Any
// active view (sort or account grouping) computes the order, so reordering is
// disabled there and the app surfaces a hint instead.
func (l *List) ManualReorderEnabled() bool {
	return !l.viewActive()
}

// InstancesForPersist returns the instances in canonical (manual) order so a view
// transform never overwrites the user's manual order on disk. With no view active
// the canonical order is simply items.
func (l *List) InstancesForPersist() []*session.Instance {
	if l.viewActive() {
		return l.manual
	}
	return l.items
}

// enterView snapshots the current items into manual the first time any view
// transform becomes active. Idempotent: a second transform activating while one is
// already active must not re-snapshot (manual already holds the canonical order).
func (l *List) enterView() {
	if l.manual == nil {
		l.manual = make([]*session.Instance, len(l.items))
		copy(l.manual, l.items)
	}
}

// exitViewInactive restores items from the manual snapshot and drops it once no view
// transform remains active. The selection is preserved by identity.
func (l *List) exitViewInactive() {
	if l.viewActive() || l.manual == nil {
		return
	}
	sel := l.GetSelectedInstance()
	l.items = l.manual
	l.manual = nil
	if sel != nil {
		l.SelectInstance(sel)
	}
}

// SetSortMode switches the within-group ordering. "" / "creation" restores the
// manual order; any other value sorts each repo group by action-priority. The
// selected session is preserved by identity.
func (l *List) SetSortMode(mode string) {
	if mode == "" {
		mode = "creation"
	}
	if mode == l.sortMode {
		return
	}
	l.sortMode = mode
	if l.viewActive() {
		l.enterView()
		l.rebuildView()
	} else {
		l.exitViewInactive()
	}
}

// SetGroupMode switches the top-level grouping. "" / "repo" restores repo groups in
// manual order; "account" clusters repo blocks by Claude account. The selected
// session is preserved by identity.
func (l *List) SetGroupMode(mode string) {
	if mode == "" {
		mode = "repo"
	}
	if mode == l.groupMode {
		return
	}
	l.groupMode = mode
	if l.viewActive() {
		l.enterView()
		l.rebuildView()
	} else {
		l.exitViewInactive()
	}
}

// ApplySort re-derives the view from the latest statuses; the metadata poll calls it
// each tick. No-op with no view active; returns whether the order changed.
func (l *List) ApplySort() bool {
	return l.rebuildView()
}

// rebuildView rebuilds items as the canonical manual order transformed by the active
// view(s): first clustered by account (if account-grouped), then sorted within each
// repo group (if a sort mode is active). It is the single writer of items while a
// view is active; the selected instance is preserved by identity. Returns whether
// items changed.
func (l *List) rebuildView() bool {
	if !l.viewActive() || l.manual == nil {
		return false
	}
	sel := l.GetSelectedInstance()
	next := make([]*session.Instance, len(l.manual))
	copy(next, l.manual)
	if l.accountGrouped() {
		next = clusterByAccount(next)
	}
	if l.sortActive() {
		sortWithinRepoGroups(next)
	}
	if sameOrder(l.items, next) {
		return false
	}
	l.items = next
	if sel != nil {
		l.SelectInstance(sel)
	} else {
		l.clampSelectionToNavigable()
	}
	return true
}

// sortWithinRepoGroups stable-sorts each contiguous repo block of items by action-
// priority (NeedsInput first, then unread Ready, …), leaving block order and
// membership untouched. Extracted from the former applySort.
func sortWithinRepoGroups(items []*session.Instance) {
	for start := 0; start < len(items); {
		key := repoKey(items[start])
		end := start + 1
		for end < len(items) && repoKey(items[end]) == key {
			end++
		}
		grp := items[start:end]
		sort.SliceStable(grp, func(i, j int) bool {
			return session.StatusUrgency(grp[i].GetStatus(), grp[i].Unread()) <
				session.StatusUrgency(grp[j].GetStatus(), grp[j].Unread())
		})
		start = end
	}
}

// clusterByAccount reorders whole repo blocks so blocks sharing a Claude account are
// contiguous, without disturbing any block's internal order. Input must be repo-
// contiguous (the manual invariant). Accounts are emitted in first-appearance order;
// the no-account ("") bucket trails last. Repo blocks within an account keep their
// first-appearance order. Pure function — the view deriver, not a state mutator.
func clusterByAccount(items []*session.Instance) []*session.Instance {
	type block struct {
		items []*session.Instance
		acct  string
	}
	var blocks []block
	for i := 0; i < len(items); {
		key := repoKey(items[i])
		j := i + 1
		for j < len(items) && repoKey(items[j]) == key {
			j++
		}
		blocks = append(blocks, block{items: items[i:j], acct: accountKey(items[i])})
		i = j
	}
	order := make([]string, 0, len(blocks))
	seen := map[string]bool{}
	for _, b := range blocks {
		if b.acct != "" && !seen[b.acct] {
			seen[b.acct] = true
			order = append(order, b.acct)
		}
	}
	order = append(order, "") // no-account bucket trails last
	out := make([]*session.Instance, 0, len(items))
	for _, acct := range order {
		for _, b := range blocks {
			if b.acct == acct {
				out = append(out, b.items...)
			}
		}
	}
	return out
}
```

- [ ] **Step 6: Generalize the mutation guards in `ui/list.go`**

In `AddInstance`, change the sort-mirror branch from `if l.sortActive()` to `if l.viewActive()` and `l.applySort()` to `l.rebuildView()`:

```go
	if l.viewActive() {
		l.manual = insertByRepo(l.manual, instance)
		l.rebuildView()
	}
```

In `KillInstance`, change the deferred branch the same way:

```go
	if l.viewActive() {
		defer func() {
			l.manual = removeInstance(l.manual, target)
			l.rebuildView()
		}()
	}
```

In `MoveUp` and `MoveDown`, change the leading guard `if l.sortActive() {` to `if l.viewActive() {` (a view owns the order; manual swap is inert):

```go
	if l.viewActive() {
		return false
	}
```

In `MoveGroupUp` and `MoveGroupDown`, add an account-mode no-op as the very first statement (before the existing `groupBounds`/bounds checks):

```go
	// Account grouping owns block order (clustering is a pure view over manual); a
	// whole-group move would break account contiguity, so it is a no-op there.
	if l.accountGrouped() {
		return false
	}
```

(`syncManualGroupOrder` stays guarded on `sortActive()` — group moves only occur in non-account modes, so it is never reached while account-grouped.)

- [ ] **Step 7: Run tests to verify they pass**

Run: `go test ./ui/ -run 'TestGroupMode|TestList|Sort' -v`
Expected: PASS — the new `TestGroupMode_*` tests pass and the existing sort/list tests (`list_sort_test.go`, `list_group_test.go`) still pass under the generalized machinery.

- [ ] **Step 8: Verify and commit**

Run: `just build && just test`
Expected: build succeeds, all tests pass.

```bash
git add ui/list.go ui/list_sort.go ui/list_group_account_test.go
git commit -m "feat(ui): cluster the session list by account under GroupMode

Generalize the sort-mode view-over-manual machinery to viewActive
(sort or account grouping) and add clusterByAccount. Rendering,
settings, and wiring follow in later commits.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 4: Render the divider, header tint, and badge suppression

Now make account mode visible: an account divider at each cluster boundary, a tinted repo header, and suppression of the redundant per-row account badge. All gated on `distinctAccountCount() > 1`.

**Files:**
- Modify: `ui/list_render.go` (add `hideAccountBadge` to `InstanceRenderer`; suppress the badge in `Render`; set the flag, emit dividers, and pass the tint in `String`)
- Modify: `ui/list.go` (extend `renderRepoHeader` with a tint; add `renderAccountDivider`)
- Test: `ui/list_group_account_test.go` (extend with render assertions)

**Interfaces:**
- Consumes (from Task 3): `accountGrouped()`, `distinctAccountCount()`, `accountKey()`; `(*session.Instance).ClaudeAccountName()`, `ClaudeAccountIsDefault()`.
- Produces: visible account grouping. No new exported API.

- [ ] **Step 1: Write the failing render tests**

First extend the file's import block (from Task 3) with `"strings"` and `"github.com/charmbracelet/x/ansi"`. Styled output carries ANSI escape codes between segments, so assert against `ansi.Strip(...)` (the plain text) — the pattern `ui/list_sanitize_test.go` already uses. Append:

```go
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
```

These discriminate badge from divider by count rather than by a spaced substring: the per-row badge (`" "+acct+" "`, `list_render.go:141`) contributes one `work` per row, the divider contributes exactly one `work` per cluster, and no repo/account name in these fixtures contains `work` as a substring.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./ui/ -run 'TestGroupMode_Renders|TestGroupMode_Suppresses|TestGroupMode_NoDivider' -v`
Expected: FAIL — no dividers are emitted and the badge is still rendered while grouped.

- [ ] **Step 3: Add badge suppression to the renderer**

In `ui/list_render.go`, add a field to the `InstanceRenderer` struct (after `permissionIndicator`):

```go
	// hideAccountBadge suppresses the per-row Claude-account badge. Set by List.String
	// when account grouping is visually active (mode == account and >1 account), so the
	// cluster divider + tinted header carry the identity instead of every row repeating it.
	hideAccountBadge bool
```

In `Render`, guard the account-badge block (change the `if acct := ...` condition):

```go
	if acct := i.ClaudeAccountName(); acct != "" && !r.hideAccountBadge {
		acctColor := th.Palette.Accent
		if i.ClaudeAccountIsDefault() {
			acctColor = th.Palette.FgDim
		}
		right1 = append(right1, p.seg(" "+acct+" ", acctColor))
	}
```

- [ ] **Step 4: Add the account divider and header tint helpers**

In `ui/list.go`, extend `renderRepoHeader` to accept a tint. Change its signature and the header-style construction:

```go
func (l *List) renderRepoHeader(key string, collapsed bool, count, needsInput, unread int, selected, foldable bool, accent lipgloss.TerminalColor) string {
```

and where it builds `header := repoHeaderStyle().Render(name)`, replace with:

```go
	headerStyle := repoHeaderStyle()
	if accent != nil {
		headerStyle = headerStyle.Foreground(accent)
	}
	header := headerStyle.Render(name)
```

Then add the divider renderer (next to `renderRepoHeader`):

```go
// renderAccountDivider renders a labelled dim rule marking the start of an account
// cluster in account-grouping mode. It is a non-selectable line, like the repo-header
// rule. The label is the account name, or "no account" for the trailing empty bucket.
func (l *List) renderAccountDivider(acct string) string {
	label := acct
	if label == "" {
		label = "no account"
	}
	th := theme.Current()
	// "── " + label + " " is the fixed prefix; the rule fills the remaining width.
	ruleLen := l.renderer.width - runewidth.StringWidth("── "+label+" ")
	if ruleLen < 0 {
		ruleLen = 0
	}
	return th.FaintStyle().Render("── ") +
		th.DimStyle().Render(label) +
		th.FaintStyle().Render(" "+strings.Repeat("─", ruleLen))
}
```

- [ ] **Step 5: Wire the divider, tint, and flag into `String`**

In `ui/list_render.go`'s `String`, just after `foldable := distinct > 1` and before `first := true`, add:

```go
	accountGroupingVisible := l.accountGrouped() && l.distinctAccountCount() > 1
	l.renderer.hideAccountBadge = accountGroupingVisible
	haveAcct := false
	prevAcct := ""
```

Inside the loop, after `first = false` (i.e. after the inter-group blank line) and before the `if showRepos {` block, emit the divider at cluster boundaries:

```go
		if accountGroupingVisible {
			acct := accountKey(l.items[start])
			if !haveAcct || acct != prevAcct {
				appendBlock(l.renderAccountDivider(acct))
			}
			haveAcct = true
			prevAcct = acct
		}
```

Then update the header call to pass the tint. Replace the existing `renderRepoHeader(...)` call (`list_render.go:345`) with:

```go
			var accent lipgloss.TerminalColor
			if accountGroupingVisible {
				anchor := l.items[start]
				if anchor.ClaudeAccountName() != "" && !anchor.ClaudeAccountIsDefault() {
					accent = th.Palette.Accent
				}
			}
			at := appendBlock(zone.Mark(listHeaderZoneID(key), l.renderRepoHeader(key, collapsed, end-start, ni, ur, headerSelected, foldable, accent)))
```

Note: `String` does not currently bind `th`; add `th := theme.Current()` at the top of the function if it is not already in scope (it is used only in the empty-list branch today, inside its own block — add a function-level `th := theme.Current()` near the top and drop the inner shadow, or reference `theme.Current().Palette.Accent` inline). Simplest: use `theme.Current().Palette.Accent` inline in the accent block to avoid touching the empty-list branch.

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test ./ui/ -run 'TestGroupMode' -v`
Expected: PASS. Then run the full ui suite for regressions: `go test ./ui/ -v` — the existing `renderRepoHeader`-dependent tests (headers, collapse, drift badge) still pass (the single production caller was updated; there are no test callers of `renderRepoHeader`).

- [ ] **Step 7: Verify and commit**

Run: `just build && just test`
Expected: build succeeds, all tests pass.

```bash
git add ui/list.go ui/list_render.go ui/list_group_account_test.go
git commit -m "feat(ui): render account dividers, tinted headers, suppress row badge

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 5: Settings row + construction/layout wiring

Expose `GroupMode` in the settings overlay and apply it to the list at startup and on change.

**Files:**
- Modify: `ui/overlay/settings.go` (add a `group_mode` enum row)
- Modify: `app/app_construct.go:81` (apply at construction)
- Modify: `app/app_layout.go` (apply on settings change)
- Test: `ui/overlay/settings_test.go` (extend) and `app/settings_test.go` (extend)

**Interfaces:**
- Consumes: `config.GroupModeRepo`, `config.GroupModeAccount`, `(*config.Config).GetGroupMode()` (Task 2); `(*List).SetGroupMode` (Task 3).

- [ ] **Step 1: Write the failing settings test**

In `ui/overlay/settings_test.go`, add a test asserting the row exists and cycles. Mirror the existing `session_sort` row test (find it with `grep -n session_sort ui/overlay/settings_test.go` and copy its shape). Concretely:

```go
func TestSettings_GroupModeRowCyclesRepoAccount(t *testing.T) {
	cfg := &config.Config{}
	rows := settingsRows() // use whatever constructor the existing tests use
	var row settingRow
	for _, r := range rows {
		if r.key == "group_mode" {
			row = r
		}
	}
	require.Equal(t, "group_mode", row.key)
	require.Equal(t, config.GroupModeRepo, row.get(cfg), "defaults to repo")
	require.NoError(t, row.set(cfg, config.GroupModeAccount))
	require.Equal(t, config.GroupModeAccount, cfg.GroupMode)
	require.Equal(t, []string{config.GroupModeRepo, config.GroupModeAccount}, row.options(cfg))
}
```

If `settingsRows()`/`settingRow` are named differently, match the names used by the existing `session_sort` assertions in that file (adapt the accessor calls accordingly — the row is a struct literal with `key`, `get`, `set`, `options` fields as seen in `settings.go:290-301`).

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./ui/overlay/ -run TestSettings_GroupMode -v`
Expected: FAIL — no `group_mode` row exists.

- [ ] **Step 3: Add the settings row**

In `ui/overlay/settings.go`, add a row immediately after the `session_sort` row (after line ~301), matching that row's structure:

```go
		{
			key: "group_mode", section: "Behavior", label: "Group by", kind: kindEnum,
			description: "Top-level list grouping: repo keeps repo groups in manual order; account clusters repo groups by their Claude account (personal/work) with a divider and tinted headers. A no-op unless two or more accounts are present.",
			get:         func(c *config.Config) string { return c.GetGroupMode() },
			set: func(c *config.Config, v string) error {
				c.GroupMode = v
				return nil
			},
			options: func(c *config.Config) []string {
				return []string{config.GroupModeRepo, config.GroupModeAccount}
			},
		},
```

- [ ] **Step 4: Run the settings test to verify it passes**

Run: `go test ./ui/overlay/ -run TestSettings_GroupMode -v`
Expected: PASS

- [ ] **Step 5: Apply at construction**

In `app/app_construct.go`, add the group-mode application right after the `SetSortMode` call (line 81):

```go
	h.list.SetSortMode(appConfig.GetSessionSort())
	// Apply the persisted top-level grouping after the full list is in place, so its
	// canonical-order snapshot is the real creation order.
	h.list.SetGroupMode(appConfig.GetGroupMode())
```

- [ ] **Step 6: Apply on settings change**

In `app/app_layout.go`, find the `case "session_sort":` block that calls `m.list.SetSortMode(...)` (around line 141) and add a sibling case:

```go
	case "group_mode":
		// Re-group the list under the new mode immediately; the list takes the
		// normalized mode string so ui needs no config import. Selection is
		// preserved by identity.
		if m.list != nil {
			m.list.SetGroupMode(m.appConfig.GetGroupMode())
		}
```

- [ ] **Step 7: Write and run a wiring test**

In `app/settings_test.go` (or the file holding the `session_sort` layout-change test — find via `grep -rn "session_sort" app/*_test.go`), add a test that changing `group_mode` reorders the list. Model it on the existing `session_sort`-change test. Minimal shape:

```go
func TestGroupModeChange_ClustersList(t *testing.T) {
	// Build a home with two repos on two accounts (reuse the package's home/test
	// helpers — see the session_sort change test for the exact constructor).
	// Set config.GroupMode = "account", dispatch the same settings-changed path the
	// session_sort test uses with key "group_mode", then assert the list order is
	// account-clustered via m.list.GetInstances().
}
```

Fill this in using the same helpers the neighbouring `session_sort` test uses (instance construction, the settings-changed message/handler, and `m.list.GetInstances()` for the assertion). Assert the two same-account repos are adjacent.

Run: `go test ./app/ -run TestGroupModeChange -v`
Expected: PASS

- [ ] **Step 8: Verify and commit**

Run: `just build && just test`
Expected: build succeeds, all tests pass.

```bash
git add ui/overlay/settings.go ui/overlay/settings_test.go app/app_construct.go app/app_layout.go app/settings_test.go
git commit -m "feat(app): surface GroupMode in settings and wire it into the list

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Self-Review

**Spec coverage:**
- Piece A (`account:` filter) → Task 1. ✅ (`account:<prefix>`, `account:none`, no-op empty, hint update.)
- Piece B `GroupMode` config → Task 2. ✅
- Piece B clustering/view machinery (§3) → Task 3. ✅ (cluster order: first-appearance, `""` last; view-over-manual; persist unchanged; selection preserved.)
- §4 divider + tint + badge suppression, gated on `distinctAccountCount() > 1` → Task 4. ✅
- §5 settings row + construction/layout wiring (no dedicated key) → Task 5. ✅
- §6 manual-reorder disabled in account mode (`ManualReorderEnabled = !viewActive`; group moves no-op) → Task 3, Steps 5–6. ✅
- Deferred: account-level collapse, GH-account axis — not implemented, matching Non-goals. ✅
- Repo-level collapse still works in account mode: `effectiveCollapsed`/`isHidden` are untouched and key on `repoKey`, so folding a repo inside a cluster is unaffected. (Consider adding a `TestGroupMode_RepoCollapseStillWorks` in Task 3/4 if you want an explicit guard.)

**Placeholder scan:** No `TODO`/`TBD` remain. The Task 5 Steps 1 & 7 test bodies defer to "the existing `session_sort` test's helpers" because those constructors live in the app/overlay test packages and must be matched exactly — grep-and-mirror is the concrete instruction, not a placeholder. The Task 4 Step 5 note about binding `th`/`theme.Current()` is a real scoping caveat, not a deferral.

**Type consistency:** `SetGroupMode`, `accountGrouped`, `viewActive`, `rebuildView`, `clusterByAccount`, `sortWithinRepoGroups`, `accountKey`, `distinctAccountCount`, `renderAccountDivider`, and the extended `renderRepoHeader(..., accent lipgloss.TerminalColor)` are named identically everywhere they appear. `ApplySort` keeps its name/signature (poll caller unchanged) and now delegates to `rebuildView`. The renderer field `hideAccountBadge` matches between struct and `Render`. Config identifiers `GroupModeRepo`/`GroupModeAccount`/`GetGroupMode` match across Tasks 2 and 5; `ui` uses the bare literal `"account"` (const `groupModeAccount`) to avoid a config import, consistent with the existing `sortMode` handling.
