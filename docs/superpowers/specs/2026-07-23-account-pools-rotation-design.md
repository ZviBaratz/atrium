# Account pools with availability-aware rotation

**Date:** 2026-07-23
**Status:** Approved (v1 scope)
**Branch:** `zvi/multiple-accounts`

## Summary of decisions (all confirmed with the user)

- Introduce an **account pool**: a named set of Claude accounts that a new
  session rotates across (the user's "group the two work accounts under one
  *work* selection"). Named **pool** — not "group" — because the codebase already
  uses "group" for repo groups and for the list-clustering feature
  (`group_mode=account`, see `2026-07-01-account-grouping-design.md`). Renaming
  avoids overloading the word in code (`accountKey`, `group_mode`).
- Selection becomes **`route → pool → next *available* member (round-robin)`**,
  with a manual pin still available in the create-form picker.
- **Availability-aware** is mandatory, not optional: a member the user has flagged
  rate-limited is skipped. This is what prevents "every other new session lands on
  the maxed-out account and fails". Availability is a manual toggle in v1.
- **List clustering** (`group_mode=account`): cluster by **pool**; per-row badge
  keeps showing the concrete **member** (`work-1`/`work-2`).
- **All members exhausted → confirm dialog**, then pin the soonest-to-reset
  member. Never silently spawn a doomed session; never hard-block.
- **Scope: Claude accounts only.** `gh_accounts` keep routing by repo remote
  (both work Claude logins already share the one `work` gh account — correct,
  unchanged). Antigravity untouched.
- **v1 only.** Auto-detection (v2) and the utilization-% proxy (v3) are documented
  below as future phases, not built now.

## Motivation

The user has a second work Claude account (`zvi.baratz2@quantivly.com`). Both work
accounts serve the same repos (same `quantivly` remotes), so today's routing —
`route → first-matching account`, first-match-wins in config order — cannot
distinguish them: `work` always wins and `work2` is a **dead route**. Two goals:

1. Treat the two work accounts as one selection ("work") in the create flow.
2. **Rotate** work sessions across the two accounts so no single account's
   5-hour rate limit is hit as quickly — effectively doubling the work budget.

The critical constraint the user raised: when one account is already maxed out
(their `zvi.baratz` is, right now), blind round-robin would send half of all new
sessions to the dead account and fail them immediately. So rotation **must** skip
known-exhausted accounts, and marking an account exhausted must be trivial.

## Research finding: can we read the rate-limit percentage?

The user asked whether Atrium could "fish out the rate-limit percentage reported
by Claude Code" and drive rotation quantitatively. Investigated against the real
Claude Code binary (v2.1.218) and the real config dirs:

- The binary **does** read `anthropic-ratelimit-unified-status` / `-reset` /
  `-overage-status` / `-overage-reset` response headers and derives internal
  `utilization` (the %), `resets_at`, `five_hour`, `rolling`, `rate_limit_tier`.
  This is what powers `/usage` and the footer.
- **None of it is persisted to disk.** Every file in a `CLAUDE_CONFIG_DIR` was
  checked: `policy-limits.json` is org policy (web-setup / MCP-isolation
  restrictions), `passesLastSeenRemaining` is guest-pass referrals,
  `cachedExtraUsageDisabledReason` is overage-consent, `metricsStatusCache` is a
  telemetry-enabled flag. The utilization % lives only in the running process's
  memory, sourced from API response headers per request.

**Verdict — the % is real but not reliably extractable locally today:**

| Vector | Gets the %? | Assessment |
|---|---|---|
| Read a local file | ❌ not persisted | Dead end (confirmed) |
| Scrape the session pane (`/usage`/footer/banner) | ⚠️ only when rendered; layout drifts across versions | Fragile; but the hard-limit **banner** reliably carries the **reset time** |
| Local reverse-proxy via `ANTHROPIC_BASE_URL` → read `unified-status` header | ✅ the real number, every request | Clean data, but puts Atrium on the API request path — a real project with reliability/security weight |

This is why v1 uses a **manual** availability flag. The quantitative % is the
**v3** research spike (below), not a v1 dependency.

## Terminology

- **Account** — an existing `config.ClaudeAccount`: a name + `config_dir`
  (`CLAUDE_CONFIG_DIR`) + route rules (`remote_matches`/`path_matches`).
- **Pool** — a named set of accounts sharing a new `pool` field. A pool with one
  member (or an account with no `pool`) behaves exactly as today.
- **Member** — an account that belongs to a pool.
- **Availability** — per-account runtime state: `available` or `limited` (with an
  optional reset time). Lives in `state.json`, not `config.json`.
- **Rotation cursor** — per-pool index into its member list, in `state.json`,
  advanced at each session creation.

Relationship to prior work: this is orthogonal to
`2026-07-01-account-grouping-design.md`, which added the *visual* clustering
(`group_mode=account`) and the `account:<name>` filter. This spec adds a
*selection* mechanism (pools + rotation) and extends those two surfaces so a pool
clusters as one block and the filter matches a pool name.

## Non-goals (explicitly out of scope for v1)

- **Mid-session account migration.** An account is injected as `CLAUDE_CONFIG_DIR`
  into tmux at session birth and cannot change live; migrating would mean killing
  and relaunching the agent, losing its context. Rejected. "Exhausted" therefore
  steers only *new* sessions.
- **Auto-detection of rate-limiting** (v2, below).
- **Quantitative utilization %** (v3, below).
- **gh / Antigravity rotation.** gh routes by repo remote, independently; both work
  Claude logins share the one work gh account. Unchanged.
- **A usage/cost meter.** Tracked separately (issues #298, #392).

## Design

### 1. Config schema — a `pool` field on `ClaudeAccount`

Add one optional field. Route rules on a single member are enough; a bare `pool`
pulls the sibling into rotation.

```go
// config/types.go
type ClaudeAccount struct {
    Name          string   `json:"name"`
    ConfigDir     string   `json:"config_dir"`
    RemoteMatches []string `json:"remote_matches,omitempty"`
    PathMatches   []string `json:"path_matches,omitempty"`
    Pool          string   `json:"pool,omitempty"` // NEW: rotation-pool membership
}
```

Target config for the user (also fixes two latent bugs — see Migration):

```json
"claude_accounts": [
  {"name":"work-1","config_dir":"~/.claude-work",  "pool":"work",
   "remote_matches":["quantivly"], "path_matches":["/quantivly/"]},
  {"name":"work-2","config_dir":"~/.claude-work2", "pool":"work"},
  {"name":"personal","config_dir":"~/.claude-personal"}
]
```

Backward compatible: no `pool` on any account ⇒ every account is its own
singleton pool ⇒ behavior identical to today.

### 2. Runtime state schema — `state.json`

Availability and the rotation cursor are runtime state (auto-expiring, must survive
a restart because a real 5-hour limit does), so they live in `State`, keyed by
account **name**, mirroring the existing `AccountOrder` accessor pattern (setters
call `SaveState(s)` and self-persist — confirmed in `config/state.go`).

```go
// config/state.go
type AccountAvailability struct {
    Limited bool   `json:"limited"`
    Until   string `json:"until,omitempty"` // RFC3339; "" while Limited = indefinite
}

type State struct {
    // ...existing fields...
    AccountAvailability map[string]AccountAvailability `json:"account_availability,omitempty"`
    AccountRotation     map[string]int                 `json:"account_rotation,omitempty"` // pool → next index
}
```

New `StateManager` interface methods + `*State` impls (each Set persists):

```go
GetAccountAvailability() map[string]AccountAvailability
SetAccountLimited(name, untilRFC3339 string) error // "" until = indefinite
ClearAccountLimited(name string) error
GetAccountRotation(pool string) int
SetAccountRotation(pool string, idx int) error
```

Effective-availability helper (normal app code; `time.Now()` is fine here):

```go
func accountAvailable(av AccountAvailability, now time.Time) bool {
    if !av.Limited { return true }
    if av.Until == "" { return false }           // indefinite
    t, err := time.Parse(time.RFC3339, av.Until)
    if err != nil { return false }               // malformed → treat as limited
    return now.After(t)                          // window elapsed → available again
}
```

Expired entries are treated as available on read; optionally clear them lazily.

### 3. Pure routing in `config`

Keep routing pure and stateless; rotation (which needs `State`) lives in `app`.

```go
// config/accounts.go
// ResolveClaudePool finds the first account whose rules match (unchanged
// first-match logic via matchRouteIndex), then returns that account's whole pool:
// the pool name and its ordered members. isDefault mirrors ResolveClaudeAccount
// (the matched account was the rule-less catch-all). An ungrouped account is a
// singleton pool whose name is the account name.
func (c *Config) ResolveClaudePool(remoteURL, targetPath string) (pool string, members []ClaudeAccount, isDefault bool)
```

`ResolveClaudeAccount` stays for callers that only need a single account (e.g. the
`@` view's "test routing" preview); it can be reimplemented on top of
`ResolveClaudePool` returning `members[0]`, or left as-is. Do not remove it.

### 4. Availability-aware round-robin (the one new decision point)

In `app/app_session.go`, `startNewSession` currently calls
`ResolveClaudeAccount` and applies `accountOverride`. Replace that block with pool
resolution + rotation:

```
members, poolName, isDefault := route (or the picker's chosen pool)
avail := m.appState.GetAccountAvailability(); now := time.Now()
start := mod(m.appState.GetAccountRotation(poolName), len(members))
chosen := -1
for k := 0; k < len(members); k++ {
    idx := (start + k) % len(members)
    if accountAvailable(avail[members[idx].Name], now) { chosen = idx; break }
}
if chosen == -1 {              // all members exhausted
    return m.confirmAllExhausted(plan, members)   // see §5
}
m.appState.SetAccountRotation(poolName, chosen+1) // advance past the chosen one
pin members[chosen] via instance.SetClaudeAccount(name, dir, isDefault) + SetClaudeAccountPool(poolName)
```

A single-member pool degenerates to today's behavior (one member, cursor no-ops).

### 5. All-members-exhausted path

Reuse the existing confirm-dialog pattern (`confirmOverCap` in `app_session.go` is
the model): `confirmAllExhausted(plan, members)` renders

```
⚠ all work accounts are rate-limited
  work-1 resets 16:32
  work-2 resets 17:05
create anyway on work-1? [y/N]
```

On confirm, pin the **soonest-to-reset** member (earliest parseable `Until`;
indefinite sorts last; if all indefinite, fall back to `members[start]`) and
proceed. On decline, abort creation cleanly (return to the form, no session, no
worktree — the same rollback contract as the cap-block path).

### 6. Instance pinning — carry the pool

The badge must show the member while clustering/filter key off the pool, so pin
both on the instance.

- `session/instance.go`: add field `claudeAccountPool string`; include in
  `InstanceData` round-trip (`session/storage.go`, `ClaudeAccountPool` JSON,
  `omitempty`).
- `session/account.go`: add `SetClaudeAccountPool(pool string)` and
  `ClaudeAccountPool() string`. (Alternatively widen `SetClaudeAccount` to take a
  pool arg; a separate setter is less churn on existing call sites.)
- Read without the lock, like the other `claude*` fields (fixed at creation).

### 7. Create-form picker — pools + members

`ui/overlay/accountPicker.go` and its wiring in `textInput_create.go`:

- Build entries from `ClaudeAccounts` grouped by `pool`. Each multi-member pool
  contributes **one rotating entry** (label `work ⇄`) plus **one entry per member**
  (`work-1`, `work-2`) for pinning. Singleton/ungrouped accounts contribute one
  entry as today. Order: pool entry immediately followed by its members.
- Preselect the **routed pool** (extend `PreselectAccount`/`SelectByName` to accept
  a pool name; untouched preselection still yields to a deliberate touch).
- Selection result is a small selector, so the submit path can express "rotate this
  pool" vs "pin this member":

```go
type AccountSelection struct {
    Pool   string          // rotate within this pool
    Member *ClaudeAccount  // non-nil ⇒ pin this exact member (overrides Pool)
}
```

`startNewSession`'s override argument becomes `*AccountSelection`:
- `Member != nil` → pin it (skip rotation/availability; a deliberate pin is the
  escape hatch even for a limited account).
- `Member == nil, Pool != ""` → rotate that pool (§4).
- `nil` (untouched) → route → pool → rotate (§3–§4).

### 8. List clustering, badge, filter

- **Cluster key** — change `accountKey()` (`ui/list.go:83`) to return the pinned
  **pool** when set, else `ClaudeAccountName()`. This is the single change that
  makes `clusterByAccount` (`ui/list_sort.go`) cluster by pool with no other edits;
  `State.AccountOrder` and `[`/`]` now order pool names (back-compat: ungrouped
  accounts still key by their own name, so pre-existing `AccountOrder` entries keep
  working; stale entries like `work2` are ignored by the present-intersection).
- **Badge** — `ui/list_render.go:201` keeps rendering `ClaudeAccountName()` (the
  member). *Optional polish:* append a `⛔` mark when that session's account is
  currently flagged limited (requires passing availability into the renderer).
- **Cluster header accent** — `ui/list_render.go:446` uses the anchor's
  `ClaudeAccountName()`/`IsDefault()`; a routed pool member is non-default, so the
  header stays accented. No change needed.
- **Filter** — `session/filter.go` `accountTerm`: match if `value` prefixes the
  member name **or** the pinned pool name, so `account:work` matches both members
  and `account:work-1` matches one. `account:none` unchanged.

### 9. The `@` accounts view — pool column + availability toggle

`ui/overlay/accounts.go` (`AccountsOverlay`) is the home for toggling availability.
Two changes:

- **Show pool + availability** on each Claude row: e.g.
  `work-1   ~/.claude-work    pool:work   ⛔ limited · 16:32` /
  `work-2   ~/.claude-work2   pool:work   ● available`.
- **Toggle key** (e.g. `space` = toggle limited, `t` is already "test routing" so
  pick a free key such as `l`/`space`): flips the cursored Claude account's
  availability; when marking limited, an optional inline prompt accepts a reset
  time (blank = indefinite). Clearing removes the flag.

Architecture note: the overlay currently holds only `*config.Config`. Availability
lives in `State`, so pass a `StateManager` (or a minimal availability-controller
interface) into `NewAccountsOverlay`. The `State` setters self-persist, so no new
"dirty" channel is needed for availability — call the setter directly. Config edits
keep using the existing `(closed, dirty)` → `SaveConfig` path. Also surface the new
`pool` field in the account edit form (`accountForm`) as an optional text input.

### 10. N-variant fan-out interaction (#387)

Because rotation happens per-session inside `startNewSession`, an **unpinned**
fan-out batch naturally spreads across the pool's members (variant 1 → work-1,
variant 2 → work-2, …) — a free win for the 5-hour-budget goal. A **pinned** batch
(user chose a specific member) uses that member for all variants. The
all-exhausted confirm is evaluated **once** for the batch: if the pool has ≥1
available member, spawn and let the cursor advance per variant; if zero available,
one confirm covers the batch. `spawnPlan.account` changes from
`*config.ClaudeAccount` to `*AccountSelection` (§7).

### 11. Doctor / misconfiguration warning

Two Claude accounts in the same pool pointing at the **same `config_dir`** are the
same login — rotation is a silent no-op (exactly the user's current `work2` bug).
Add a check (in `atrium doctor` and/or a soft warning in the `@` view): flag pool
members that share a `config_dir`. *Optional:* warn if a pool member's `config_dir`
lacks a `.credentials.json` (never logged in).

## Touch-point map

| File | Change |
|---|---|
| `config/types.go` | `ClaudeAccount.Pool` field |
| `config/accounts.go` | `ResolveClaudePool`; keep `ResolveClaudeAccount` |
| `config/state.go` | `AccountAvailability` type; `State.AccountAvailability`, `State.AccountRotation`; `StateManager` methods + self-persisting impls; `accountAvailable` helper |
| `config/state_test.go` | availability + rotation accessor tests (hermetic: temp `HOME`) |
| `session/instance.go` | `claudeAccountPool` field |
| `session/account.go` | `SetClaudeAccountPool` / `ClaudeAccountPool()` |
| `session/storage.go` | `InstanceData.ClaudeAccountPool` round-trip |
| `app/app_session.go` | pool resolution + availability-aware round-robin in `startNewSession`; `confirmAllExhausted`; `spawnPlan.account` → `*AccountSelection`; batch semantics |
| `ui/list.go` | `accountKey()` → pool-else-name |
| `ui/list_render.go` | (optional) `⛔` limited mark on the member badge |
| `session/filter.go` | `accountTerm` matches pool or member |
| `ui/overlay/accountPicker.go` | pool + member entries; `AccountSelection`; pool preselect |
| `ui/overlay/textInput_create.go` | wire selector through `GetSelectedAccount` |
| `ui/overlay/accounts.go` | pool column; availability toggle; `StateManager`; `pool` in edit form |
| `main.go` (doctor) | same-`config_dir`-in-pool warning |
| `README.md` | Claude-accounts section: pools, rotation, availability, the `@` toggle |

## Backward compatibility & migration

- **No `pool` anywhere ⇒ zero behavior change.** Every account is a singleton pool;
  `accountKey` returns the account name; the picker shows accounts as today.
- **The user's current config has two latent bugs this feature forces fixing:**
  1. `work2` points at the **same** `~/.claude-work` as `work` — the same login.
     It must get its own dir (`~/.claude-work2`) and a real login as
     `zvi.baratz2`.
  2. Even with a distinct dir, `work2`'s route is dead (`work` matches `quantivly`
     first). Pools remove the need for a distinct route: both members carry
     `"pool":"work"`; `work-1` alone needs the route.
- Rename `work`→`work-1` and `work2`→`work-2` (cosmetic; any distinct names work).
  A stale `AccountOrder` entry (`work2`) is harmless (ignored).
- Pre-existing sessions have no pinned pool → `accountKey` falls back to their
  account name → they cluster exactly as before until recreated.

## Setup / onboarding (manual, one-time)

```
CLAUDE_CONFIG_DIR=~/.claude-work2 claude   # then /login as zvi.baratz2@quantivly.com
```

Confirm `~/.claude-work2/.credentials.json` exists and is a *different* account
from `~/.claude-work`. Then apply the target config (§1). No Atrium code performs
the login; document it in the README.

## Testing

All new tests must stay hermetic (temp `HOME`, per CLAUDE.md and
`config/config_test.go`/`app/app_test.go` `TestMain`).

- **`config`** — `ResolveClaudePool`: routes to the right pool; returns members in
  config order; singleton/ungrouped account → singleton pool; catch-all pool;
  empty `claude_accounts` dormant. `accountAvailable`: not-limited, indefinite,
  future `until`, past `until` (expired), malformed `until`.
- **`config` state** — availability + rotation accessors persist and reload.
- **rotation (app-level, table-driven)** — cursor advances; skips a limited member;
  wraps; all-limited returns the exhausted signal; a pinned member bypasses
  rotation and availability; soonest-reset selection on confirm.
- **`ui`** — `accountKey` returns pool-else-name; `clusterByAccount` clusters two
  members into one pool block; `accountTerm` matches pool and member.
- **picker** — pool + member entries built from `Pool`; `AccountSelection`
  pin-vs-rotate; pool preselect yields to touch.
- **`@` view** — availability toggle mutates State; pool column renders; same-dir
  warning fires.
- Run the full gate: `just ci` (build vet fmt-check lint test cover). Lint (esp.
  `unused`/`revive exported`) is the part the other checks can't substitute for.

## Edge cases

- **Rotation is by session count, not tokens** — a busy and an idle session weigh
  the same. Best heuristic without usage telemetry; the availability flag is the
  manual correction. State this in the README.
- **Availability keyed by name** — renaming an account in the `@` editor orphans its
  entry (auto-expires / ignored when the name isn't present). Acceptable.
- **Cursor vs member add/remove** — `GetAccountRotation % len(members)` keeps a
  stored index valid after the member list changes.
- **Catch-all account with a `pool`** — resolve the pool and rotate; `isDefault`
  stays true for its members (dim badge). Rare but well-defined.
- **Pin overrides availability** — a deliberate member pin runs even on a limited
  account (the user knows best); rotation/availability apply only to unpinned
  creations.

## Phasing

- **v1 (this spec)** — `pool` field; availability-aware round-robin; manual
  availability toggle in `@`; pool clustering / badge / filter; picker; batch
  spreading; doctor warning; README.
- **v2 (future)** — pane-detect Claude's rate-limit banner in a running session →
  auto-flip the same availability flag and parse `resets_at`. Layers on the v1
  state with no schema change; fragile (Claude wording drifts across versions), so
  it must be proven on top of a working manual flag.
- **v3 (documented, not built)** — a local reverse-proxy via `ANTHROPIC_BASE_URL`
  reading `anthropic-ratelimit-unified-status` for live `utilization` %, turning
  availability quantitative (skip at >N%). The only clean path to the number; a
  real project (Atrium on the API request path, reliability/security weight).
  Evaluate as its own spec.

## Open questions (non-blocking; sensible defaults chosen)

- Availability toggle key in `@` (`space` vs `l`) — implementer's call; `t` is taken.
- Whether to render the `⛔` badge mark in v1 (needs availability plumbed into the
  list renderer) or defer as polish — defer if it adds meaningful plumbing.
