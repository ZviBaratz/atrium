# Account grouping — cluster the session list by Claude account

**Date:** 2026-07-01
**Status:** Approved

## Motivation

Sessions that span more than one identity (e.g. personal vs. work) currently
interleave freely in the list: the only identity signal is the per-row Claude
account badge (`ClaudeAccountName`), and repo groups sort by manual/creation
order with no regard to which account a repo belongs to. A user juggling both
identities has no way to focus one, and no top-level visual separation between
them.

This adds two small, composable surfaces:

1. An `account:<name>` **filter predicate** — the focus tool, available in every
   mode.
2. An optional, togglable **group-by-account view mode** — clusters repo groups
   by account with a divider and tinted headers.

A deliberate non-goal is a second collapse dimension (folding a whole account).
That is the expensive, risky part of a true two-level tree — a second
`collapsed` map, a persisted field, and `isHidden`/anchor changes — and it buys
little for a feature only power users (those who have configured
`ClaudeAccounts`) can see at all. It is deferred; the divider grows a fold
control later if wanted, foreclosing nothing.

### Terminology

"Account" here is the Claude Code account — `config.ClaudeAccount`
(`config/types.go:48`), auto-routed per session by git-remote/path matching and
surfaced today as the row badge. It is **not** `config.Profile`
(`config/types.go:34`), which is a named agent-CLI *program* (claude/codex/…).
Because account is derived from the repo's remote/path, every session in a repo
resolves to the same account, so grouping repos by account is a clean partition.

## Behavior

Two independent axes: a **filter** (always available) and a **group mode**
(togglable, persisted, default `repo` = today's behavior).

| Group mode | List shape |
|------------|------------|
| `repo` (default) | Exactly today: repo groups in manual order, per-row account badge shown. |
| `account`, ≤1 distinct account | No-op: renders identically to `repo` mode (badge still shown). Covers the unconfigured-accounts default. |
| `account`, >1 distinct account | Repo groups clustered so same-account repos are contiguous; a `── work ──` divider at each account boundary; repo headers tinted by account; **per-row account badge suppressed** (the cluster + tint already carry it). |

The `account:<name>` filter narrows to sessions whose Claude account name
prefix-matches `<name>` (`account:none` = sessions with no account). It composes
with either group mode: filtering hides non-matches, grouping clusters whatever
remains.

Repo-level collapse (`←`/`→`, `CollapsedRepos`) keeps working in both modes;
only the account level has no fold in v1.

## Approach

### 1. `account:` filter predicate (`session/filter.go`)

Add a `case strings.HasPrefix(tok, "account:")` in `parseTerm`, returning an
`accountTerm(value)` that prefix-matches `strings.ToLower(i.ClaudeAccountName())`
— identical prefix/no-op semantics to `statusTerm`/`prTerm` (empty value matches
all; a value prefixing nothing matches none). `account:none` (or the empty
account) matches `ClaudeAccountName() == ""`, mirroring `pr:none`.

Extend the user-facing filter-syntax hint that enumerates predicates to include
`account:`.

### 2. `GroupMode` config field (`config/types.go`, accessors)

Add `GroupMode string json:"group_mode,omitempty"` to `Config`, modeled on the
existing `SessionSort` (`config/types.go:249`): constants `GroupModeRepo =
"repo"` (default) and `GroupModeAccount = "account"`, plus a normalizing
`GetGroupMode()` accessor (empty/unknown → `repo`). Persisted like `SessionSort`;
no new state.json field.

### 3. Group-mode plumbing on `*List`

Mirror the sort-mode machinery in `ui/list_sort.go`, which already computes a
display order as a *view over a snapshot of the manual order* without
overwriting the persisted order:

- `List.groupMode string` + `SetGroupMode(mode string)`, set at construction
  from `GetGroupMode()` (alongside the existing sort-mode wiring in
  `app/app_construct.go`).
- When `account` mode is active and `distinctAccountCount() > 1`, `l.items` is
  arranged so repo blocks are ordered by account cluster. Reuse the sort-mode
  approach: snapshot the manual order, derive the clustered view from it,
  restore on switching back to `repo`. The existing per-instance repo-contiguity
  invariant (`groupBounds`, `ui/list.go:719`) is preserved — clustering only
  reorders whole repo blocks; it never splits one.
- `accountKey(inst)` = `inst.ClaudeAccountName()`. A repo group's account is its
  anchor (first) session's account; sessions in a repo agree, so the rare
  mixed-account repo (routing rules changed mid-life) falls back to the anchor's
  account and is otherwise untouched.
- **Cluster order:** configured accounts appear by first appearance in the manual
  order (stable, predictable); the **no-account (`""`) bucket sorts last**. A
  catch-all/default account (`ClaudeAccountIsDefault`) is a normal named cluster,
  rendered dim (§4) rather than reordered.
- `distinctAccountCount()` mirrors `distinctRepoCount()` (`ui/list.go:63`) and
  gates the divider, the header tint, and the badge suppression — so `account`
  mode with 0–1 accounts is a true no-op.

### 4. Render: divider, tint, badge suppression (`ui/list_render.go`, `ui/list.go`)

- **Divider.** In the `String()` render loop (`ui/list_render.go:323`), when
  account grouping is visually active, emit a lightweight account divider (a
  labelled dim rule, reusing `repoRuleStyle`) before the first repo header of
  each account cluster. Selection/`selectedIdx` is unaffected: the divider is a
  non-selectable rule, exactly like the existing repo-header rule.
- **Tint.** `renderRepoHeader` (`ui/list.go:85`) colors the header by account
  (a stable color derived from the account name; dim for the catch-all, matching
  how the badge already dims via `ClaudeAccountIsDefault`,
  `session/account.go:49`), gated on account grouping being active.
- **Badge suppression.** Suppress the per-row account badge in
  `InstanceRenderer.Render` (`ui/list_render.go:116`) when account grouping is
  visually active, driven off the same display-flag path the renderer already
  uses for the model/permission indicators. Tied to the **mode**, not the
  filter: narrowing with `account:` in `repo` mode keeps the badge (it still
  confirms the matched prefix; no tinted header is doing that job).

### 5. Toggle + settings surface

`GroupMode` is a persisted preference, changed the same way `SessionSort` is —
there is **no dedicated sort-mode keybinding today**; the mode lives in the
settings overlay and is applied to the list at construction/layout.

- A `group_mode` enum row in the settings overlay (`ui/overlay/settings.go`),
  alongside the existing `default_program` / `session_sort` rows (`settings.go:291`).
- Applied via `list.SetGroupMode(GetGroupMode())` next to the existing
  `SetSortMode` call in `app/app_construct.go:81` and `app/app_layout.go:142`.

A dedicated quick-toggle key is a natural later addition but is **out of scope for
v1** — it would be new keyspace + help/discoverability surface for a preference
that behaves like the settings-only `session_sort`.

### 6. Interaction with manual reordering

> **Superseded 2026-07-07 (PR #285).** The rule below was revised: manual
> reordering is now *enabled* within account boundaries. J/K reorders within a
> repo group (gated only on the status sort, since account clustering never
> touches within-block order), and `{ }` reorders whole groups within an account
> cluster — a move that would cross an account boundary is refused with a hint.
> `ManualReorderEnabled()` is `!sortActive()` (not `!viewActive()`), and the
> reorder is reflected into `manual` (a within-block swap, or an in-place block
> transpose) then the view rebuilt. The original v1 decision is kept below as the
> historical record.

While `account` mode is active, **manual reordering is disabled** — both
within-group J/K and whole-group `{ }` moves — exactly as status-sort already
disables J/K today. This is the single clean rule that keeps clustering a pure
view over `manual`: with no user reordering, `manual` stays canonical and the
clustered order never leaks into the persisted order. Concretely
`ManualReorderEnabled()` becomes `!viewActive()` (was `!sortActive()`), and
`MoveGroupUp`/`MoveGroupDown` short-circuit to a no-op (→ hint) when
`accountGrouped()`. The `session_sort` mode itself is unaffected: `{ }` group
moves still work in a pure status-sort (non-account) view as they do today.

## Testing

All hermetic, consistent with the existing `session`/`ui`/`config` setups.

- **session/filter_test.go** — `account:<prefix>` matches by case-insensitive
  prefix; `account:none` matches the empty account; an empty value is a no-op; a
  non-matching value yields an empty list.
- **config** — `GetGroupMode()` normalizes empty/unknown → `repo`; round-trips
  `account`.
- **ui** — in `account` mode with >1 account: repo blocks cluster by account,
  one divider per account boundary, headers tint per account, and the per-row
  account badge is absent; with ≤1 account the view is byte-identical to `repo`
  mode (badge present). Clustering does not overwrite the persisted manual order
  (assert `InstancesForPersist` unchanged); repo collapse still folds a single
  repo inside a cluster; `{ }` cannot move a group across an account boundary;
  selection stays on a visible row across a mode toggle.

## Non-goals / deferred

- **Account-level collapse/fold** (`▼ WORK (3)` with a persisted
  `CollapsedAccounts`) — deferred phase 2. The v1 divider is a plain rule; it
  grows a fold control later. No second `collapsed` map or `isHidden`/anchor
  change in v1.
- **GH account as a grouping axis.** v1 keys strictly on the Claude account (the
  badge already shown). `GHAccount` (`config/types.go:72`) can diverge from the
  Claude account for a repo; a GH axis is out of scope.
- No change to account *routing* or to how badges render in `repo` mode.
- No behavior for users without `ClaudeAccounts` configured: they have ≤1
  distinct account, so `account` mode is a harmless no-op.
