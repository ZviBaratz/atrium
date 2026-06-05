# Unlimited sessions by default

**Date:** 2026-06-05
**Status:** Approved

## Problem

Atrium caps session creation at 10 by default. The number is inherited from
upstream claude-squad's hardcoded `GlobalInstanceLimit = 10`, which shipped with
no recorded rationale. Nothing breaks beyond 10: the real per-session costs
(two subprocesses per 500ms poll tick for *active* sessions, a tmux session, a
git worktree) scale linearly and are costs the user knowingly takes on by
deliberately creating sessions through the form. Paused sessions cost almost
nothing yet still count toward the cap. For a tool whose purpose is running
many agents in parallel, the default cap is an arbitrary wall.

## Decision

Remove the default cap. `max_sessions` remains as an **opt-in** guardrail.

### Config semantics (`config/config.go`)

- `MaxSessions *int` keeps its JSON shape (`max_sessions,omitempty`), but the
  meaning of unset / 0 / negative flips from "fall back to 10" to "unlimited".
- `GetMaxSessions()` returns `0` to mean "no cap"; any positive value is the cap.
- `DefaultMaxSessions` is deleted. `DefaultConfig()` stops writing the key, so
  fresh `config.json` files omit it entirely.

### Enforcement (`app/app.go`, single check site in `openCreateForm`)

- Guard becomes `limit > 0 && m.list.NumInstances() >= limit`.
- Error message unchanged — it already points at `max_sessions in config.json`,
  which is exactly right now that the cap only exists when configured.

### Migration

None. Explicit values in existing configs are honored, including the literal
`"max_sessions": 10` that `DefaultConfig` wrote into config files created since
PR #61 — an explicit value is indistinguishable from a user-chosen one, and
overriding user config silently is worse than asking affected users to delete
one line. Note this in the PR description.

### Edge cases

- `"max_sessions": -5` yields unlimited rather than an error, consistent with
  the config package's lenient coercion style (it previously coerced
  non-positive values to the default rather than failing).

## Testing

- Rewrite `config/maxsessions_test.go`: nil → 0, zero → 0, negative → 0,
  explicit 25 → 25.
- Add an `app`-level test that creation is blocked at a configured cap and not
  blocked when the key is absent (the guard currently has no test coverage).

## Rationale for the sentinel

`GetMaxSessions()` returns `0` (not `math.MaxInt`) for "unlimited" so callers
must consciously write `limit > 0 &&`, documenting at the call site that the
cap is optional. Keeping the `*int` + `omitempty` shape means absence of the
key *is* the default, so nothing needs migrating if the default ever changes
again.
