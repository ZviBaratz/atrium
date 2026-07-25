# Pool-aware test-routing preview — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the `@` overlay's `t` preview answer the same question the session
launcher answers — which pool, which member, and why — by moving the member-choice
logic into `config` and calling it from both.

**Architecture:** Two pure functions in a new `config/rotation.go` become the single
source of truth for "which member would a new session use". `app/app_session.go`'s
rotate branch, its all-exhausted gate, and its confirm all route through them; so does
`renderPreview`'s Claude branch, which grows a pool block (members, availability chips,
the chosen member, a decision sentence) beneath an otherwise unchanged headline.

**Tech Stack:** Go 1.26 (module `github.com/ZviBaratz/atrium`), Bubble Tea TUI,
`testify`, `just`.

**Source spec:** `docs/superpowers/specs/2026-07-25-test-routing-pool-aware-design.md`
(Approved). Item B of the post-merge feedback on PR #458; item A shipped in PR #475.

## Global Constraints

- **No schema change.** Nothing new in `config/types.go` or `config/state.go`. Any task
  proposing a persisted field is wrong.
- **Refactor-only in `app/`.** `app/rotation_test.go`'s tests are the safety net and
  must stay green **unmodified** — the sole exception is `TestSoonestResetMember`, which
  moves to `config/` with the function it tests.
- **`SelectPoolMember` returns `start` on all-limited**, not 0 and not the soonest-reset
  index. That is `app_session.go:1409`'s existing fallback; changing it would move app
  behaviour under cover of a refactor.
- **Read-only preview.** `renderPreview` must never call `SetAccountRotation`. Bubble
  Tea re-renders per keystroke; a writing preview would rotate the pool once per typed
  character.
- **Hermetic time.** Every pure helper takes `now time.Time`. Only `renderPreview` calls
  `time.Now()`.
- **Dormancy is a contract.** `len(members) < 2` keeps today's exact
  `ResolveClaudeAccount` block. The eight existing preview tests
  (`accounts_test.go:325, 340, 357, 388, 405, 418, 433, 449`) must pass **unmodified**.
- **Claude branch only.** Do not touch the GH or Antigravity branches of
  `renderPreview`, `handlePreviewKey`, the `t` handler in `handleListKey`, or
  `keys/registry.go`.
- **Tests hermetic.** `t.Setenv("HOME", t.TempDir())` in anything reaching state or
  config writes. Pure render/selector tests need no HOME (`config_test.go`'s `TestMain`
  already sandboxes the package).
- **The gate is `PATH="$HOME/go/bin:$PATH" just ci`** — a bare `just ci` dies at `lint`
  with exit 127 because `golangci-lint` is installed but off `PATH`. Inner loop:
  `go test ./config/... ./app/... ./ui/overlay/...`. Lint is what nothing else
  substitutes for: exported symbols need doc comments (`revive`'s `exported`), nothing
  may be named `max`/`min`/`len`, an unused declaration fails (`unused`).
- **Commits:** Conventional Commits, lowercase, each ending with
  `Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>`.

## File Structure

New:
- `config/rotation.go` — `SelectPoolMember`, `SoonestResetMember`.
- `config/rotation_test.go` — table tests, fixed `now`.
- `ui/overlay/accounts_preview.go` — `previewMemberBudget`, `previewMemberWindow`, `previewDecisionLine` (pure; no receiver, no I/O, no clock).
- `ui/overlay/accounts_preview_test.go` — unit tests for those three.

Modified:
- `app/app_session.go` — four call sites; delete the private `soonestResetMember`.
- `app/rotation_test.go` — remove `TestSoonestResetMember` only.
- `ui/overlay/accounts.go` — `renderPreview`'s Claude branch.
- `ui/overlay/accounts_test.go` — new preview tests (append).
- `README.md` — Rotation pools prose (~640-702).

---

## Task 1: extract the selector into `config`

**Files:** new `config/rotation.go`, `config/rotation_test.go`.

**Interfaces produced:** `config.SelectPoolMember`, `config.SoonestResetMember` — read
by Tasks 2 and 4.

- [ ] **Step 1: Write the failing tests** in `config/rotation_test.go`. Pure — no
  `t.Setenv` needed.

```go
func TestSelectPoolMember(t *testing.T) {
	now := time.Date(2026, 7, 23, 16, 0, 0, 0, time.UTC)
	two := []ClaudeAccount{{Name: "work-1"}, {Name: "work-2"}}
	cases := []struct {
		name       string
		members    []ClaudeAccount
		avail      map[string]AccountAvailability
		cursor     int
		wantIdx    int
		wantAllLim bool
	}{
		{"cursor 0, all available", two, nil, 0, 0, false},
		{"cursor 1, all available", two, nil, 1, 1, false},
		{"cursor wraps past the end", two, nil, 2, 0, false},
		{"negative cursor normalizes", two, nil, -1, 1, false},
		{"oversized cursor normalizes", two, nil, 7, 1, false},
		{"cursor's member limited -> next", two,
			map[string]AccountAvailability{"work-1": {Limited: true}}, 0, 1, false},
		{"skip wraps to an earlier member", two,
			map[string]AccountAvailability{"work-2": {Limited: true}}, 1, 0, false},
		// The fallback startNewSession relies on: the CURSOR's member, not 0.
		{"all limited returns the cursor's own member", two,
			map[string]AccountAvailability{"work-1": {Limited: true}, "work-2": {Limited: true}},
			1, 1, true},
		{"elapsed Until counts available", two,
			map[string]AccountAvailability{"work-1": {Limited: true, Until: "2026-07-23T15:00:00Z"}}, 0, 0, false},
		{"future Until counts limited", two,
			map[string]AccountAvailability{"work-1": {Limited: true, Until: "2026-07-23T17:00:00Z"}}, 0, 1, false},
		{"malformed Until counts limited", two,
			map[string]AccountAvailability{"work-1": {Limited: true, Until: "nope"}}, 0, 1, false},
		{"empty members", nil, nil, 0, -1, false},
	}
	// subtests asserting both return values
}

// Moved from app/rotation_test.go:95 with the function it tests.
func TestSoonestResetMember(t *testing.T) { /* verbatim body, unqualified names */ }
```

- [ ] **Step 2: Verify red** — `go test ./config/ -run 'SelectPoolMember|SoonestReset'`
  fails to compile (undefined).

- [ ] **Step 3: Implement** `config/rotation.go`, lifting `app_session.go:1408-1416`
  and `:1775-1791`. Doc comments are mandatory (`revive`). Euclidean normalization
  `((cursor % n) + n) % n` — a stale or negative persisted cursor must never panic.

- [ ] **Step 4: Verify green** — `go test ./config/...`
- [ ] **Step 5: Commit** — `refactor(config): extract pool member selection into a pure selector`

---

## Task 2: rewire the creation path

**Files:** modify `app/app_session.go`; delete `TestSoonestResetMember` from
`app/rotation_test.go`.

No new behaviour. If any test in `app/rotation_test.go` needs editing to pass, the
rewire is wrong.

- [ ] **Step 1: Verify red-by-deletion** — remove the private `soonestResetMember` and
  confirm `go test ./app/` fails to compile. (There is no new test to write here; the
  existing suite *is* the spec.)

- [ ] **Step 2: Implement**

`app_session.go:1406-1416` becomes:

```go
avail := m.appState.GetAccountAvailability()
now := time.Now()
// Shared with the accounts overlay's routing preview, so what the preview shows
// and what creation does cannot drift.
chosen, _ := config.SelectPoolMember(members, avail, m.appState.GetAccountRotation(poolName), now)
```

Keep `:1417-1432` — the `len(members) > 1` persist guard, `SetAccountRotation(poolName,
chosen+1)`, the field extraction and the pool-stamp suppression — exactly as they are.

`gateAllExhausted:1819-1825` becomes:

```go
avail := m.appState.GetAccountAvailability()
// The len(members) < 2 guard above is deliberate and NOT redundant with allLimited:
// SelectPoolMember reports allLimited for a singleton pool whose one account is
// limited, but a pool of one has nothing to rotate, so it must not raise the confirm.
if _, allLimited := config.SelectPoolMember(members, avail, m.appState.GetAccountRotation(poolName), time.Now()); !allLimited {
	return nil, false
}
```

`confirmAllExhausted:1835` → `soonest := config.SoonestResetMember(members, avail)`.

- [ ] **Step 3: Verify green** — `go test ./app/... ./config/...`. Name the survivors:
  `TestStartNewSession_RotatesAndAdvances`, `_SkipsLimited`, `_PinnedBypassesAvailability`,
  `_NoPoolStaysDormant`, `TestCreateForm_AllExhaustedAsksConfirm`,
  `TestCreateForm_HardCapBeatsExhaustedGate`, `TestCreateForm_ExhaustedAcceptSpawnsSoonest`.
- [ ] **Step 4: Commit** — `refactor(app): route session creation through the shared pool selector`

---

## Task 3: pure preview helpers

**Files:** new `ui/overlay/accounts_preview.go`, `ui/overlay/accounts_preview_test.go`.

- [ ] **Step 1: Write the failing tests**

```go
func TestPreviewMemberBudget(t *testing.T) {
	// height 24, no decision line -> 5;  with decision line -> 4
	// height 40 -> 21 / 20;  height 10 (tiny) -> floored at 2
}

func TestPreviewMemberWindow(t *testing.T) {
	// n <= budget                  -> (0, n, 0)          no overflow line
	// n=12, budget=4, chosen=0     -> (0, 3, 9)          first three, 9 hidden
	// n=12, budget=4, chosen=11    -> (9, 12, 9)         THE decisive member is visible
	// n=12, budget=4, chosen=6     -> window contains 6
	// clamped: end never exceeds n, start never below 0
}

func TestPreviewDecisionLine(t *testing.T) {
	// no skip                      -> ""
	// one skipped                  -> "work-1 limited → rotating to work-2"
	// two skipped                  -> "2 members limited → rotating to work-3"
	// all limited, all indefinite  -> "creating from the form asks to confirm, then uses work-1 (first member)"
	// all limited, one has Until   -> "... uses work-2 (resets soonest)"
}
```

The `chosen=11` case is the one that matters: a cap that can hide the `← next` row
defeats the feature it protects.

- [ ] **Step 2: Verify red** → **Step 3: Implement**

```go
// previewChrome is the number of lines renderPreview costs outside the pool block:
// border 2 + Padding(1,2) 2 + title/blank 2 + 12 body lines ("Test routing", blank,
// two labelled inputs, blank, three result lines, blank, hint).
const previewChrome = 18

func previewMemberBudget(height int, decision bool) int
func previewMemberWindow(n, chosen, budget int) (start, end, hidden int)
func previewDecisionLine(members []config.ClaudeAccount, avail map[string]config.AccountAvailability,
	start, chosen int, allLimited bool, now time.Time) string
```

`previewMemberWindow`: `n <= budget` → `(0, n, 0)`. Otherwise `shown := budget - 1`
(one line pays for the overflow notice), `start := 0`, and if `chosen >= shown`,
`start = chosen - shown + 1`; clamp `start` to `[0, n-shown]`; `end = start + shown`;
`hidden = n - shown`.

- [ ] **Step 4: Verify green** — `go test ./ui/overlay/...`
- [ ] **Step 5: Commit** — `feat(accounts): add pure helpers for the pool-aware routing preview`

---

## Task 4: wire the preview

**Files:** modify `ui/overlay/accounts.go` (`renderPreview` Claude branch only); test
`ui/overlay/accounts_test.go` (append).

- [ ] **Step 1: Write the failing tests** — pure render, no HOME except where a state
  setter writes.

```go
// The pool, both members, and the limited marker all render — the "no pool
// information" half of the report.
func TestAccountsOverlay_PreviewShowsPoolAndMembers(t *testing.T)

// The report's other half: a limited member must not be presented as the pick.
func TestAccountsOverlay_PreviewSkipsLimitedAndSaysWhy(t *testing.T) {
	// work-1 limited -> headline names work-2, work-2 row has "← next",
	// and the decision line reads "work-1 limited → rotating to work-2"
}

// Mirrors what creation ACTUALLY does when everything is limited: the confirm,
// pinned to the soonest-to-reset member.
func TestAccountsOverlay_PreviewAllLimitedShowsConfirmDecision(t *testing.T)

// Dormancy: a config with no pool must render exactly as it did before this feature.
func TestAccountsOverlay_PreviewNoPoolUnchanged(t *testing.T) {
	// NotContains "pool '", "⇄", "← next"; still Contains the account and its dir
}

// The cap keeps the overlay usable AND keeps the decision visible.
func TestAccountsOverlay_PreviewCapsLongPool(t *testing.T) {
	// 12 members at 80x24: Contains "esc back", Contains "more members not shown",
	// and the chosen member's row is present
}

// The read-only contract. Bubble Tea re-renders per keystroke; a writing preview
// would rotate the pool once per typed character.
func TestAccountsOverlay_PreviewNeverAdvancesRotation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	// SetAccountRotation("work", 1); render 3x; assert GetAccountRotation("work") == 1
}
```

Assert on `o.renderPreview()` rather than `o.Render()` where `┌`/`└` would collide with
the box border — the existing suite already does this (`accounts_test.go:938-941`).

- [ ] **Step 2: Verify red** → **Step 3: Implement**

In `renderPreview`, replace only the Claude resolution:

```go
_, members, _ := o.cfg.ResolveClaudePool(remote, path)
claude := "inherit ambient env"
poolBlock := ""
if len(members) < 2 {
	// ... today's ResolveClaudeAccount switch, verbatim ...
} else {
	claude, poolBlock = o.renderPoolDecision(members, time.Now())
}
```

and write `poolBlock` after the Claude line. `renderPoolDecision` reads
`o.state.GetAccountAvailability()` and `o.state.GetAccountRotation(members[0].Pool)`
once, calls `config.SelectPoolMember`, and on `allLimited` also
`config.SoonestResetMember` for the `← on confirm` marker and the
`⚠ all '<pool>' accounts limited` headline.

Member rows use `poolGutter(members)` — **nil-guarded**: `PoolMembers` can return a
slice with no adjacent same-pool run when a pool name also matches an ungrouped
account by name, and `poolGutter` returns nil for that. Fall back to two spaces.
Chips are the list's exact literals, `● available` (DimStyle) / `⛔ limited`
(DangerStyle). Indent the block with a `previewIndent` const of 9 spaces — the printed
width of `"Claude → "`.

- [ ] **Step 4: Verify green** — `go test ./ui/overlay/...`, and confirm the eight
  pre-existing preview tests pass **unmodified**: `PreviewShowsAgy`, `PreviewResolves`,
  `PreviewEmptyAndRuleOnlyInheritAmbient`, `PreviewCatchAllNamedShowsName`,
  `PreviewRuleMatchedNamedDefaultShowsName`, `PreviewPathFieldRoutes`,
  `PreviewGHMatchShowsDirAndToken`, `PreviewGHTokenWithoutDirSurfacesToken`.
- [ ] **Step 5: Commit** — `feat(accounts): make test routing pool- and availability-aware`

---

## Task 5: README

**Files:** `README.md`, Rotation pools prose (~640-702).

- [ ] **Step 1:** Document what `t` now reports: the resolved pool, every member with
  its limited state, which member a new session would take and why it skipped the
  others, and — when every member is flagged — the confirm and the member it would pin.
  State that it is a read-only "what would happen now": it never advances the rotation
  cursor.
- [ ] **Step 2:** Verify the literals against the shipped code before writing them.
- [ ] **Step 3: Commit** — `docs: document the pool-aware test-routing preview`

---

## Verification (after the last task)

1. `PATH="$HOME/go/bin:$PATH" just ci` — must be green. Exit 127 at `lint` means the
   tool is off `PATH`, not a broken recipe. A lint path outside this worktree is stale
   global-cache noise → `golangci-lint cache clean`, re-run. If `go` is absent:
   `GO=/home/zvi/.local/share/mise/installs/go/1.26.4/bin/go`.
2. `just test-race` over `./config/... ./app/... ./ui/...`.
3. Whole-branch review subagent, fresh context, given the spec and the full diff.
4. **Manual smoke** under an isolated `HOME` (own data dir → own tmux socket and
   `tui.lock`; press `Escape` first, onboarding eats `@`): two pooled work accounts →
   `@` → `t` shows the pool and both members with the next pick; `l` one member limited
   → `t` marks it and names the *other* as next with the rotating-to line; limit both →
   `t` shows the ⚠ headline and the on-confirm member; a no-pool account reads exactly
   as before; `cat state.json` confirms `account_rotation` never moved.
5. File the auto-dispatch-skips-`gateAllExhausted` issue.
6. PR against `main` as `ZviBaratz` (`gh auth switch --user ZviBaratz`; `gh pr edit` is
   broken here → `gh api --method PATCH`), Claude Code footer in the body. Report the
   number and `gh pr checks`.
