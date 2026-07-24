# Accounts Overlay: Reorder + Grouped Pool Display — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let the user move an account up or down in the `@` accounts table — which *is* routing precedence — and render a pool's adjacent members as one bracketed group.

**Architecture:** Entirely local to the accounts overlay. `J`/`K` (aliases `shift+↑`/`shift+↓`) swap the cursored account with its neighbour in `cfg.{Claude,GH,Agy}Accounts` and report `dirty`, which the app already persists. Grouping is a *read-out* of that real order — two pure functions map the Claude slice to per-row gutter cells and to the names of pools whose members are not adjacent — so display order always equals config order and the row index stays the config index.

**Tech Stack:** Go 1.26 (module `github.com/ZviBaratz/atrium`), Bubble Tea TUI, `testify`, `just`.

**Source spec:** `docs/superpowers/specs/2026-07-24-accounts-reorder-grouping-design.md` (Approved). Follow-up A to PR #458; item B (test-routing pool awareness) is explicitly out of scope.

## Global Constraints

- **No schema change.** No new field in `config/types.go` or `config/state.go`. The feature permutes a slice that already exists. Any task proposing a new persisted field is wrong.
- **Display order == config order, always.** Never re-sort `rows()`. The cursor is a direct config index at five existing sites (`accounts.go:172` `l`, `:199/:203/:207` `openForm`, `:293-297` delete); keeping the identity is what lets this change leave all five alone.
- **Dormancy is a contract.** With <2 accounts on a tab, `J`/`K` is inert and unadvertised. With no pools configured, `poolGutter` returns `nil`, no gutter column renders, no split note can appear, and the table is byte-identical to today's.
- **Overlay keys are local.** `handleListKey` dispatches on `msg.String()`; do **not** add anything to `keys/registry.go`.
- **Never name a dead key.** `J/K reorder` is advertised only when the active tab has ≥2 accounts; `l limited` stays Claude-only (`accounts.go:549-551` states the convention).
- **The legend must not wrap at 80 columns** (74 inner columns). `TestAccountsOverlay_ListWindowsRowsOnShortTerminal` asserts the whole overlay fits in 24 lines; a wrapped hint line breaks it.
- **Tests stay hermetic.** `t.Setenv("HOME", t.TempDir())` in any test that reaches state or config writes (see `accounts_test.go:504`). Pure render/key tests need no `HOME`.
- **The gate is `PATH="$HOME/go/bin:$PATH" just ci`** (build + vet + fmt-check + lint + test + cover) — `golangci-lint` is installed but not on `PATH`, so a bare `just ci` dies at `lint` with exit 127. Inner loop: `go test ./ui/overlay/... ./config/...`. Lint is the part nothing else substitutes for: every exported symbol needs a doc comment (`revive`'s `exported`), nothing may be named `max`/`min`/`len`, and an unused declaration fails (`unused`).
- **Commits:** Conventional Commits, lowercase. End every commit message with `Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>`.

## File Structure

New files:
- `ui/overlay/accounts_pool.go` — `poolGutter`, `splitPools`, `splitPoolNote` (pure; no receiver, no I/O).
- `ui/overlay/accounts_pool_test.go` — unit tests for those three.

Modified files:
- `ui/overlay/accounts.go` — `moveAccount` + the key cases in `handleListKey`; gutter + split note + legend in `renderList`; `rowWindow` chrome 12 → 13.
- `ui/overlay/accounts_test.go` — overlay-level key, render, routing and persistence tests.
- `README.md` — pools prose (`README:574-628`).

Not touched: `keys/registry.go`, `config/types.go`, `config/state.go`, `app/*` (the `dirty` → `SaveConfig` contract at `app/app_accounts.go:14` already covers this), `ui/list.go`.

---

## Task 1: reorder core (`J`/`K` on all three tabs)

**Files:** modify `ui/overlay/accounts.go`; test `ui/overlay/accounts_test.go` (append).

**Interfaces:**
- Consumes: `o.cursor`, `o.tab`, `o.cfg.{Claude,GH,Agy}Accounts`.
- Produces: `(*AccountsOverlay).moveAccount(delta int) (dirty bool)` — read by Tasks 2, 3, 5.

- [ ] **Step 1: Write the failing tests**

Append to `ui/overlay/accounts_test.go`. No `HOME` needed — nothing here touches disk.

```go
// TestAccountsOverlay_ReorderSwapsAndFollowsCursor: J moves the cursored account
// down one slot in config order (which IS routing precedence) and the cursor
// tracks the account, not the position, so a second J keeps moving the same one.
func TestAccountsOverlay_ReorderSwapsAndFollowsCursor(t *testing.T) {
	cfg := twoTabCfg() // Claude: work, personal
	o := NewAccountsOverlay(cfg, config.DefaultState())
	o.SetSize(80, 24)

	closed, dirty := o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'J'}})
	assert.False(t, closed)
	assert.True(t, dirty, "reorder mutates config, so the app must persist it")
	assert.Equal(t, []string{"personal", "work"}, claudeNames(cfg))
	assert.Equal(t, 1, o.cursorIndex(), "cursor follows the moved account")

	// K moves it back.
	_, dirty = o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'K'}})
	assert.True(t, dirty)
	assert.Equal(t, []string{"work", "personal"}, claudeNames(cfg))
	assert.Equal(t, 0, o.cursorIndex())
}

// Boundary presses must not report dirty — a no-op must not trigger a config write.
func TestAccountsOverlay_ReorderAtBoundsIsNoOp(t *testing.T) { /* K at row 0, J at last row */ }

// Order is first-match precedence in every section, so reorder works on all tabs.
func TestAccountsOverlay_ReorderWorksOnGHAndAgyTabs(t *testing.T) { /* selectTab, J, assert order + dirty */ }

func TestAccountsOverlay_ReorderShiftArrowAliases(t *testing.T) { /* tea.KeyShiftDown / tea.KeyShiftUp */ }

// Dormancy: a single-account tab cannot reorder and must report no change.
func TestAccountsOverlay_ReorderSingleAccountIsInert(t *testing.T) { /* 1 Claude account, J and K → dirty false */ }
```

Add one test helper next to `twoTabCfg`:

```go
func claudeNames(cfg *config.Config) []string { /* names in config order */ }
```

(and `ghNames`/`agyNames` as needed — or one generic helper; keep it to what the tests use, `unused` fails lint).

- [ ] **Step 2: Verify red** — `go test ./ui/overlay/ -run Reorder` fails to compile (`moveAccount` undefined) or fails the assertions.

- [ ] **Step 3: Implement**

In `handleListKey`, after the `down`/`j` case:

```go
case "K", "shift+up":
	return false, o.moveAccount(-1)
case "J", "shift+down":
	return false, o.moveAccount(+1)
```

And a new method near `clampCursor`:

```go
// moveAccount swaps the cursored account with its neighbour delta slots away and
// moves the cursor with it, reporting whether the config changed. Account order is
// routing precedence — first-match wins and the first rule-less account is the
// catch-all (config.matchRouteIndex) — so this is a routing edit, not a cosmetic
// one, and the caller persists it. A move off either end is a no-op that reports
// no change, so a boundary press never triggers a config write.
func (o *AccountsOverlay) moveAccount(delta int) bool {
	i, j := o.cursor, o.cursor+delta
	if i < 0 || j < 0 || i >= o.activeLen() || j >= o.activeLen() {
		return false
	}
	switch o.tab {
	case tabClaude:
		a := o.cfg.ClaudeAccounts
		a[i], a[j] = a[j], a[i]
	case tabAgy:
		a := o.cfg.AgyAccounts
		a[i], a[j] = a[j], a[i]
	default: // tabGH
		a := o.cfg.GHAccounts
		a[i], a[j] = a[j], a[i]
	}
	o.cursor = j
	return true
}
```

- [ ] **Step 4: Verify green** — `go test ./ui/overlay/...`
- [ ] **Step 5: Commit** — `feat(accounts): reorder accounts with J/K in the accounts overlay`

---

## Task 2: reorder changes routing precedence (assertions)

The behavioural claim of this feature. Nothing to implement — Task 1's code must already satisfy it; if it does not, Task 1 is wrong.

**Files:** test-only, `ui/overlay/accounts_test.go`.

- [ ] **Step 1: Write the failing tests**

```go
// The point of reordering: order is first-match precedence and the FIRST rule-less
// account is the catch-all, so moving a rule-less account to the top changes which
// account an unmatched repo resolves to — and the rendered badges follow.
func TestAccountsOverlay_ReorderChangesCatchAll(t *testing.T) {
	cfg := &config.Config{ClaudeAccounts: []config.ClaudeAccount{
		{Name: "alpha"}, // first rule-less → the catch-all
		{Name: "bravo"}, // rule-less but later → unreachable
	}}
	o := NewAccountsOverlay(cfg, config.DefaultState())
	o.SetSize(80, 24)

	name, _, isDefault := cfg.ResolveClaudeAccount("", "/tmp/anything")
	require.Equal(t, "alpha", name)
	require.True(t, isDefault)
	assert.Contains(t, rowLine(t, o.Render(), "alpha"), "default")
	assert.Contains(t, rowLine(t, o.Render(), "bravo"), "unreachable")

	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'J'}}) // alpha down

	name, _, _ = cfg.ResolveClaudeAccount("", "/tmp/anything")
	assert.Equal(t, "bravo", name, "the new first rule-less account is the catch-all")
	assert.Contains(t, rowLine(t, o.Render(), "bravo"), "default")
	assert.Contains(t, rowLine(t, o.Render(), "alpha"), "unreachable")
}

// Same for the rule-matching case: two accounts whose rules both match a remote
// resolve to whichever is first, so reorder flips the winner. GH tab, to pin that
// the GH section's order is load-bearing too.
func TestAccountsOverlay_ReorderChangesGHMatchPriority(t *testing.T) {
	// both accounts match "github.com/acme"; assert ResolveGHConfigDir flips after J
}
```

Add the line-level helper (substring asserts over the whole view are order-blind, which is exactly what these tests must not be):

```go
// rowLine returns the single rendered line containing name, so a test can assert
// which row a badge landed on rather than merely that the badge exists somewhere.
func rowLine(t *testing.T, view, name string) string { /* t.Helper(); scan strings.Split; t.Fatalf if absent */ }
```

- [ ] **Step 2: Verify red** (helper undefined) → **Step 3:** add the helper only; no production change.
- [ ] **Step 4: Verify green** — `go test ./ui/overlay/...`
- [ ] **Step 5: Commit** — `test(accounts): pin that reordering changes routing precedence`

---

## Task 3: legend reflow

**Files:** modify `ui/overlay/accounts.go` (`renderList`, ~551); test `ui/overlay/accounts_test.go`.

- [ ] **Step 1: Write the failing tests**

```go
// "move" must name only the new reorder key, so the cursor keys read "select";
// and reorder is advertised only where it can do something (>=2 rows).
func TestAccountsOverlay_LegendAdvertisesReorder(t *testing.T) {
	// twoTabCfg → Contains "↑/↓ select", NotContains "↑/↓ move", Contains "J/K reorder"
	// 1-account cfg → NotContains "J/K reorder"
	// GH tab (1 row) → NotContains "J/K reorder"; still Contains "t test routing"
}

// l limited moved to the second hint line to keep the first from wrapping; it must
// still be Claude-scoped, and no hint line may wrap at an 80-col terminal.
func TestAccountsOverlay_LegendFitsAndKeepsLimitedClaudeOnly(t *testing.T) {
	// Claude tab: Contains "l limited"; GH tab: NotContains "l limited"
	// every rendered line's lipgloss.Width <= o.boxWidth(); total lines <= 24
}
```

- [ ] **Step 2: Verify red** → **Step 3: Implement**

```go
hint := "↑/↓ select · tab switch · n new · e edit · d delete"
if o.activeLen() > 1 {
	hint = "↑/↓ select · J/K reorder · tab switch · n new · e edit · d delete"
}
extras := "t test routing · esc close"
if o.tab == tabClaude {
	extras = "l limited · " + extras
}
```

Line 1 is 65 columns at most and line 2 is 38 — both inside the 74 inner columns of an 80-column terminal. Do not add anything else to line 1 (adding `= priority`, for instance, wraps it and breaks the height test).

- [ ] **Step 4: Verify green** — `go test ./ui/overlay/...`
- [ ] **Step 5: Commit** — `feat(accounts): advertise J/K reorder in the accounts legend`

---

## Task 4: pool gutter + split-pool note + chrome

**Files:** new `ui/overlay/accounts_pool.go` + `ui/overlay/accounts_pool_test.go`; modify `ui/overlay/accounts.go` (`renderList`, `rowWindow`); test `ui/overlay/accounts_test.go`.

**Interfaces:**
- Produces: `poolGutter([]config.ClaudeAccount) []string`, `splitPools([]config.ClaudeAccount) []string`, `splitPoolNote(names []string, width int) string`. All unexported (no doc-comment lint requirement, but comment them anyway — they encode the design).

- [ ] **Step 1: Write the failing tests**

`ui/overlay/accounts_pool_test.go` — pure unit tests, no overlay:

```go
func TestPoolGutter(t *testing.T) {
	// 2 adjacent members     → ["┌ ", "└ "]
	// 3 adjacent members     → ["┌ ", "│ ", "└ "]
	// pooled + unpooled mix  → cells only on the run, "  " elsewhere
	// lone member of a pool  → nil (no run of 2 → no gutter column at all)
	// no pools               → nil
	// two adjacent Pool==""  → nil (an empty pool is not a pool)
	// two runs of one pool   → both runs bracketed
	// empty slice            → nil
}

func TestSplitPools(t *testing.T) {
	// members at 0 and 2     → ["work"]
	// members adjacent       → nil
	// two split pools        → both, in first-appearance order
	// single-member pool     → nil
}

func TestSplitPoolNote(t *testing.T) {
	// 1 name  → "pool 'work' is split — J/K to group its members"
	// 2 names → "pools 'work', 'home' are split — J/K to group their members"
	// >2 or too wide for the given width → the bounded count form,
	//   "3 pools are split — J/K to group their members"
}
```

`ui/overlay/accounts_test.go` — render level:

```go
func TestAccountsOverlay_PooledMembersRenderBracketed(t *testing.T) {
	// work-1/work-2 adjacent in one pool → rowLine(work-1) contains "┌", work-2 contains "└"
}

func TestAccountsOverlay_SplitPoolShowsNoteAndNoBracket(t *testing.T) {
	// work-1, personal, work-2 → NotContains "┌", Contains "pool 'work' is split"
}

func TestAccountsOverlay_NoPoolsRendersFlat(t *testing.T) {
	// twoTabCfg → NotContains "┌", "│", "└", and NotContains "is split"
}

func TestAccountsOverlay_GutterIsClaudeTabOnly(t *testing.T) {
	// GH tab, GH accounts named like pool members → no glyphs, no split note
}

func TestAccountsOverlay_GutterSurvivesWindowScroll(t *testing.T) {
	// 30 accounts where the last two share a pool; scroll so the run head is the
	// first visible row → its "┌" still renders (gutter is computed over the whole
	// slice, mirroring the seenCatchAll pre-scan)
}
```

- [ ] **Step 2: Verify red** → **Step 3: Implement**

`ui/overlay/accounts_pool.go`:

```go
// poolGutter maps each Claude account to its gutter cell, bracketing every run of
// two or more ADJACENT accounts sharing a pool: "┌ " at the run head, "│ " inside,
// "└ " at the tail, "  " for anything not in a run. It returns nil when no such run
// exists, which is how a pool-free config keeps rendering with no gutter column at
// all. Pool == "" never forms a run — two adjacent unpooled accounts are not a pool.
// Grouping is deliberately a read-out of the real config order (which is routing
// precedence) and never a re-sort, so the row index stays the config index.
func poolGutter(accts []config.ClaudeAccount) []string

// splitPools names the pools whose members are not all adjacent, in first-appearance
// order — the case poolGutter cannot bracket. renderList turns this into the nudge to
// reorder them together.
func splitPools(accts []config.ClaudeAccount) []string

// splitPoolNote phrases splitPools' result for the list footer, naming at most two
// pools and falling back to a bounded count form when the names would not fit width.
func splitPoolNote(names []string, width int) string
```

In `renderList`, before the row loop (alongside the `avail`/`now` hoists):

```go
var gut []string
if o.tab == tabClaude {
	gut = poolGutter(o.cfg.ClaudeAccounts) // whole slice: a tail row keeps its "└" when the head scrolls off
}
```

Inside the loop, between marker and name:

```go
gutter := ""
if gut != nil {
	gutter = t.DimStyle().Render(gut[i])
}
b.WriteString(marker + gutter + padRight(name, 12) + " " + ...)
```

After the existing `!o.hasCatchAll()` note:

```go
if o.tab == tabClaude {
	if names := splitPools(o.cfg.ClaudeAccounts); len(names) > 0 {
		b.WriteString(t.DimStyle().Render(splitPoolNote(names, o.inner())) + "\n")
	}
}
```

And `rowWindow`: `const chrome = 12` → `13`, extending its comment to say the second (split-pool) note line is budgeted. Keep it static — a dynamic chrome would widen the window for configs that render no note and shift existing scroll behaviour.

- [ ] **Step 4: Verify green** — `go test ./ui/overlay/...`, and confirm the four pre-existing order/window tests still pass by name: `TestAccountsOverlay_ListWindowsRowsOnShortTerminal`, `TestAccountsOverlay_CatchAllBadgeSurvivesWindowScroll`, `TestAccountsOverlay_BadgesMarkCatchAllAndUnreachable`, `TestAccountsOverlay_RendersPoolAndAvailability`.
- [ ] **Step 5: Commit** — `feat(accounts): render adjacent pool members as one bracketed group`

---

## Task 5: end-to-end and persistence

**Files:** test-only, `ui/overlay/accounts_test.go`.

- [ ] **Step 1: Write the failing tests**

```go
// Item A end to end: a split pool is grouped by pressing J, the bracket appears and
// the nudge clears — the note names the exact fix, and the fix works.
func TestAccountsOverlay_ReorderGroupsSplitPool(t *testing.T) {
	// cfg: work-1(pool work), personal, work-2(pool work); cursor to personal (down);
	// press J → order work-1, work-2, personal → Contains "┌", NotContains "is split"
}

// The reorder must survive a restart: dirty is the app's cue to SaveConfig
// (app/app_accounts.go:14), and the permuted order must round-trip through disk.
func TestAccountsOverlay_ReorderPersists(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	// build cfg, press J, require dirty, config.SaveConfig(cfg), config.LoadConfig(),
	// assert the loaded ClaudeAccounts order matches the new order
}
```

Check `config.LoadConfig`'s signature/behaviour before writing (it mutates the data dir — memory note `LoadState is not read-only`); a temp `HOME` is mandatory here.

- [ ] **Step 2: Verify red** → **Step 3:** no production change expected; if either fails, the defect is in Task 1 or 4 — fix it there.
- [ ] **Step 4: Verify green** — `go test ./ui/overlay/...`
- [ ] **Step 5: Commit** — `test(accounts): reorder groups a split pool and survives a restart`

---

## Task 6: README

**Files:** `README.md` (pools/accounts prose, ~574-628).

- [ ] **Step 1:** Add to the accounts/pools prose: order in `claude_accounts` is match priority and the first rule-less account is the catch-all, so `J`/`K` (or `shift+↑`/`shift+↓`) in the `@` overlay reorders accounts and *changes routing* — that is how you pick the catch-all; it works on all three tabs. Adjacent members of one pool render bracketed, and a pool whose members are scattered says so and points at the same keys. Mention the rotation-cursor consequence in one clause (reordering within a pool can repeat or skip a member once).
- [ ] **Step 2:** Confirm the `@` row in the key table (`README:268`) needs no change — overlay-internal keys are not listed there — and that the session-list `J`/`K` row (`README:257`) is untouched and unambiguous.
- [ ] **Step 3: Commit** — `docs: document accounts reorder and grouped pool display`

---

## Verification (after the last task)

1. `PATH="$HOME/go/bin:$PATH" just ci` — must be green. A lint path outside this worktree is stale global-cache noise → `golangci-lint cache clean` and re-run.
2. `just test-race ./ui/... ./config/...`.
3. Whole-branch review subagent (fresh context, given the spec + the full diff).
4. **Manual smoke** under an isolated `HOME` (its own data dir → own tmux socket and `tui.lock`, so live sessions are untouched): seed `config.json` with three Claude accounts, two sharing a pool and non-adjacent; run `atrium`; `@` → confirm flat rows + the split-pool note; `J` → confirm the bracket appears, the note clears, and the `default` badge moved; `q`, `cat config.json` to confirm the order persisted; relaunch and confirm it survived.
5. Dormancy in the same smoke: a one-account config shows no `J/K reorder` and `J` does nothing; a pool-free config shows no gutter column.
6. PR against `main` as `ZviBaratz` (`gh auth switch --user ZviBaratz`; `gh api --method PATCH` if a field needs editing), Claude Code footer in the body; report the number and `gh pr checks`.
