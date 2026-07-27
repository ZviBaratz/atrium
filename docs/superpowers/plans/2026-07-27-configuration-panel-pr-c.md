# Configuration panel redesign — PR C: search, reset, deep links, the Accounts handoff

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

## Context

PR A (#482) landed the taxonomy and the copy; PR B (#491) landed the two-pane renderer and
the visibility layer. Three capabilities the spec designed are still unbuilt, and PR B's
hint line deliberately advertises none of them because a hint for a dead key is worse than
no hint:

- **`/` search.** Spec D4: the panel has no search although `internal/fuzzy` + the shared
  `Picker` (`ui/overlay/picker.go`) exist for exactly this (#373). With 38 rows behind a
  13-entry rail, finding a setting you can name but cannot place is still a hunt.
- **`r` reset.** Spec D6: `row.reset` and `isModified` shipped in PR A, fully tested and
  **unreachable** — the marker says a row is changed and nothing puts it back.
- **`OpenAt` deep links.** Spec D10: `SelectRow` is exported and called only by tests. No
  dialog or notice jumps the user to the setting it is talking about — and
  `overCapMessage` still ends with `(set max_sessions in config.json to change this)`,
  sending the user to a text editor for a setting this panel owns.
- **The Accounts handoff.** The rail entry renders a note telling the user to press `@`
  somewhere else. Enter should just open it.

**Outcome:** every mechanism the schema declares is reachable from the keyboard, and two
real call sites prove the deep link rather than leaving it speculative.

**Goal:** Ship spec §8 (search), §7's `r`, §12 (`OpenAt` + its call sites) and §4's Accounts
handoff, satisfying guards 8, 9 and 11. (Guard 10, the single-pane fallback, is PR B's and
already holds; this PR must not break it — see the single-pane search contract in Task 6.)

**Architecture:** The search filter is a `Picker` value field on `SettingsOverlay` (a named
field, not embedded — embedding would leak `Focus`/`SetWidth`/`SetVisibleRows` into the
panel's public API), ranked by `internal/fuzzy.Match` over label + key + summary + category.
`s.cursor` stays the global index into `s.rows`; the picker's cursor indexes the result
list and the two are kept in sync by one function. The rail cursor **follows the
highlighted result**, which is what makes Esc's landing predictable and the rail's live
marker honest. `SelectRow` is renamed to `OpenAt` with every caller migrated in the same
commit. The Accounts handoff is a request the overlay records and `home` fulfils, because
an overlay cannot open a sibling.

**Tech Stack:** Go 1.26 toolchain over `go.mod`'s 1.25, Bubble Tea, lipgloss,
`charmbracelet/x/ansi`, testify. Design record:
`docs/superpowers/specs/2026-07-25-configuration-panel-design.md` (below: "the spec").
Predecessors: `docs/superpowers/plans/2026-07-25-configuration-panel-pr-a.md` (#482) and
`docs/superpowers/plans/2026-07-26-configuration-panel-pr-b.md` (#491, below: "PR B's
plan").

---

## Global Constraints

- **Branch off fresh main.** `origin/main` is `71f2573`; this worktree is already there and
  clean. Work on `zvi/config-c`.
- **Toolchain is mise-managed and not on `PATH`.** Test with
  `PATH="/home/zvi/.local/share/mise/installs/go/1.26.4/bin:$PATH" go test ./ui/overlay/`
  or `mise exec -- just test`. Lint **scoped**:
  `PATH="/home/zvi/.local/share/mise/installs/go/1.26.4/bin:$PATH" /home/zvi/go/bin/golangci-lint run ./ui/... ./app/...`
  — a bare `run` reports findings from other atrium worktrees against files that may not
  exist here (**issue #486** tracks that cache leak; **PR #493 is open, not merged**, so
  `justfile:52`'s `lint` recipe here is still a bare `golangci-lint run`). If #493 lands
  first, rebase and use `mise exec -- just lint ./ui/... ./app/...` instead. **Read the
  paths in any lint failure before believing it.**
- **`unused` is the linter that bites this repo.** Every helper this plan adds is read by a
  test in the same task. If the linter flags one, a test is missing — do not delete the
  helper.
- **`revive` rules CI enforces that `go vet` does not:** `exported` (every exported symbol
  needs a doc comment starting with its own name — this PR adds `OpenAt`, `Handoff`,
  `SettingsHandoff`, `HandoffNone`, `HandoffAccounts`) and `redefines-builtin-id` (never
  name anything `max`, `min` or `len`).
- **Measure unstyled width.** A bordered lipgloss block pads every line to the same width,
  so `lipgloss.Width(rendered) <= boxWidth` is a tautology that can never fail. Overflow
  assertions go on the plain-text composition functions (`rowLineParts.plain()`,
  `railLines()`, `titleLine()`), never on `Render()`'s output.
- **A renderer guard that names one terminal size is testing that size, not the renderer.**
  PR B's adversarial review found three real rendering bugs in geometry no single-size
  guard visited. Every new visibility guard here sweeps widths.
- **No new `keys` registry entries.** Panel-internal keys are handled on `msg.String()`, so
  the registry/help/README key-drift guards stay untouched. `,` (`KeySettings`) is
  unchanged.
- **Do not add a `settingRow` field, and do not touch `settings_schema.go`'s copy.** Every
  mechanism this PR reaches for (`reset`, `defaultDisplay`, `summary`, `category`) already
  exists there.
- **Out of scope, do not build:** the Profiles editor (PR D). Its rail entry keeps its
  handoff note and stays a no-op. Category-level reset is spec §2's explicit non-goal.
- **Tests stay hermetic** (`HOME` to a temp dir; `app` and `config` do it in `TestMain`).
- **Conventional Commits, lowercase.** `feat:` / `fix:` / `refactor:` / `test:` / `docs:`.
- **Never `git checkout <file>` to undo a mutation** — it reverts to HEAD and takes the
  task's real edits with it. This has destroyed work in three prior stages. Edit the
  mutated line back.

---

## Decisions taken before this plan (do not relitigate)

| Decision | Why |
|---|---|
| **The category rides the badge column on a search row**, replacing the timing badge. It degrades by truncation rather than dropping; an inert row's reason chip still wins the column. | Spec §10's ladder reused verbatim. The alternative — a category prefix on the label — is protected by "never truncate the label", pushing `labelW` to 44 and leaving ~3 cells for value+badge at 80 columns. |
| **`OpenAt(key string) bool`**, not the spec's `OpenAt(category, key)`. | `settingCategory` is unexported, so `app` cannot name it; and guard 1 pins that every key lives in exactly one category, so a passed category is a second source of truth that can only ever disagree. Recorded as a "Resolved in PR C" callout in spec §12, matching how PR B amended §5. |
| **`SelectRow` is deleted**, every caller migrated in the same commit. | 19 direct call sites, all in `_test.go`; 62 more calls reach rows through the `settingsAt` helper and are fixed by changing that one line. A permanent wrapper would leave two names for one behavior. |
| **Every `,`-advertising notice deep-links**, not just the one the spec names. | Reviewed against the tree, there are **five**: the two reorder refusals (`session_sort`, `group_mode`) and three in `app_welcome.go` that name their setting in their own copy (`default_program`). One mechanism, identical phrasing; landing in five different places would read as a bug, so a test makes the rule structural. |

---

## Derived numbers

Measured from the tree, not estimated. Where a number is a guard, the test that pins it is
named.

| Quantity | Value | Note |
|---|---|---|
| Rows in the schema | 38 | 37 config keys + the read-only config-path row |
| Largest category | **6** (`Worktrees & git`) | so a per-category match count is always **one cell** and fits the rail's existing trail — pinned by `TestEveryCategoryMatchCountFitsTheRail` |
| `railWidth()` | 19 | unchanged; search adds no cells |
| Rows pane at width 80 / 100 | 52 / 70 | unchanged |
| Longest row label | 26 | `Smart dispatch auto-create` |
| Longest category label | 15 | `Worktrees & git` |
| `searchBadgeMinCells` | **4** | three cells plus the ellipsis: the floor a category degrades to before the column is dropped |
| Confirm dialog wrap | 46 cells | `confirmWidth` 50 − `Padding(1,2)` |
| `overCapMessage` rendered lines | 5 → **4** | measured with the wrap below; the new tail is one line, the old was two |

**Hint ladders, measured** (all runes are width 1; inner width is 74 at the 80-column floor
and 92 at 100 columns):

| Focus | Rung | Cells |
|---|---|---|
| rows | `↑/↓ move · ←/→ change · ↵ edit · r reset · / search · ? more · ⇥ pane · esc back` | 80 |
| rows | `↑/↓ · ←/→ · ↵ edit · r reset · / search · ? more · esc back` | 59 |
| rows | `↵ edit · r reset · / search · esc back` | 38 |
| rows | `/ search · esc back` / `esc back` | 19 / 8 |
| rail (rows entry) | `↑/↓ category · → rows · / search · ⇥ pane · esc close` | 53 |
| rail (Accounts) | `↑/↓ category · ↵ accounts · / search · ⇥ pane · esc close` | 57 |
| rail (Profiles) | `↑/↓ category · / search · ⇥ pane · esc close` | 44 |
| search | `type to filter · ↑/↓ move · ↵ edit · ? more · esc clear` | 55 |

So at 80 columns the rows pane shows rung 2 and every other focus shows rung 1; at 100 all
show rung 1. **Esc is advertised at all three levels** — `esc clear` while filtering,
`esc back` in the rows pane, `esc close` on the rail — which is the extension spec §15
requires instead of a static string.

---

## File Structure

| File | Responsibility in this PR |
|---|---|
| **Modify** `ui/overlay/settings.go` | `search Picker` and `handoff` fields; `OpenAt` (replacing `SelectRow`); `Handoff()`; the `HandleKeyPress` router gains the search arm; `titleLine()` |
| **Modify** `ui/overlay/settings_nav.go` | `SettingsHandoff`; `railEntry.opens`; `searching`/`startSearch`/`clearSearch`/`searchResults`/`syncCursorToSearch`/`handleSearchKey`; `resetRow`; `visibleRowIndices`; the `/`, `r` and Accounts key arms |
| **Modify** `ui/overlay/settings_render.go` | search-aware `railLines` (counts + dimming), `searchPaneContent`, `searchBadge`/`badgeAvail`, search-aware `contextLine` / `helpLines` / `visibleLabelWidth`, the new hint ladders |
| **Modify** `ui/overlay/settings_nav_test.go` | reset, `OpenAt`, the handoff, the search key grammar |
| **Modify** `ui/overlay/settings_render_test.go` | the search renderer, the width sweeps, the rail counts |
| **Modify** `ui/overlay/settings_test.go` | `settingsAt` → `OpenAt`; the direct `SelectRow` call sites; the hint-line assertions |
| **Modify** `app/app.go` | `noticeSettingKey`, `pendingConfirmSettingKey` fields |
| **Modify** `app/app_update.go` | `openSettings`/`openSettingsAt`/`openAccounts` helpers; the two reorder notices |
| **Modify** `app/app_keys.go` | the settings handoff on close; `,` in the cap dialog |
| **Modify** `app/app_feedback.go` | `settingNotice`; arm/clear discipline |
| **Modify** `app/session_cap.go` | `overCapMessage`'s tail; `confirmOverCap` arms the jump |
| **Modify** `app/settings_test.go`, `app/dialog_voice_test.go`, `app/host_cap_test.go`, `app/reorder_filter_keys_test.go` | the adapted and new app-level tests |
| **Modify** `docs/superpowers/specs/2026-07-25-configuration-panel-design.md` | the §12 `OpenAt` signature callout |
| **Create** `docs/superpowers/plans/2026-07-27-configuration-panel-pr-c.md` | this plan, shipped in the PR as A and B both did |

---

## Task 1: Ship the plan, and have it torn apart first

**Files:** Create `docs/superpowers/plans/2026-07-27-configuration-panel-pr-c.md`.

- [ ] **Step 1: Copy this plan into the repo**

Write this document verbatim to
`docs/superpowers/plans/2026-07-27-configuration-panel-pr-c.md`.

- [ ] **Step 2: Adversarial review, before a line of code**

Dispatch **three independent reviewers**, each reading the plan **against the tree** rather
than against itself. Both prior stages did this and both times it changed the design, not
just a test — PR B's reviewers found three real rendering bugs the plan would otherwise
have shipped.

Give each one this plan, the spec, and one lens:

1. **Does the prescribed code compile and do what its comment says?** Every function body
   here was written against the tree but not run. Check `Picker.handleKey`'s return
   contract, the `handleSearchKey` fallthrough order, `valueCell`'s reservation parameter,
   and every field and method named in a snippet.
2. **Would each prescribed test pass, and does it test what its name claims?** Run the
   arithmetic. Check each `require` precondition is actually reachable with the query
   given, and look for assertions that hold whether or not the feature works.
3. **What did the plan not think about?** Interactions between the four deliverables: search
   plus the single-pane fallback below 73 columns; search plus the `?` view; a deep link
   into a panel that is already open; `r` while the editor is open; the handoff plus the
   remembered rail; `hideErrMsg`'s generation check versus the notice arm.

Fold every real finding into the plan **before** Task 2, and record in the plan's
Self-Review what changed and why, as PR B's plan does.

- [ ] **Step 3: Commit**

```bash
git add docs/superpowers/plans/2026-07-27-configuration-panel-pr-c.md
git commit -m "docs: pr c plan for the configuration panel redesign"
```

---

## Task 2: `r` resets a row

**Files:**
- Modify: `ui/overlay/settings_nav.go` (`resetRow`, the `"r"` arm in `handleRowsKey`)
- Modify: `ui/overlay/settings_nav_test.go`
- Modify: `app/settings_test.go`

**Interfaces:**
- Consumes: `settingRow.reset`, `settingRow.defaultDisplay`, `SettingsOverlay.isModified`
  (all PR A, all tested and unreachable until now).
- Produces: `(*SettingsOverlay).resetRow(row *settingRow) string` — the changed key, or
  `""` when nothing changed. Task 5's search grammar deliberately does **not** call it.

Guard 8: "`r` restores the default **and** reports the changed key, so `applySettingChange`
persists and live-applies it exactly like an edit."

`r` reports the key only when the displayed value actually changed. That is what makes the
returned key mean "this just changed" rather than "someone pressed r": without it, holding
`r` rewrites `config.json` and re-runs a live-apply hook (a theme repaint, a full
`tea.ClearScreen`) on every press for no reason.

- [ ] **Step 1: Write the failing tests**

Append to `ui/overlay/settings_nav_test.go`:

```go
// rowValues snapshots every row's displayed value, so a "nothing changed" assertion is
// about what the panel shows rather than about struct equality — a Config holds slices and
// pointers, and a deep compare of it answers a different question.
func rowValues(o *SettingsOverlay) []string {
	out := make([]string, len(o.rows))
	for i, r := range o.rows {
		out[i] = r.get(o.cfg)
	}
	return out
}

// TestResetRestoresTheDefaultAndReportsTheKey is spec §13's guard 8. r must behave exactly
// like an edit: restore the built-in default AND report the changed key, so home persists
// the config and runs that field's live-apply hook. A reset that changed the config without
// reporting would leave disk and screen disagreeing until the next unrelated edit.
func TestResetRestoresTheDefaultAndReportsTheKey(t *testing.T) {
	cfg := config.DefaultConfig()
	o := NewSettingsOverlay(cfg)
	settingsAt(t, o, "theme")
	require.False(t, o.isModified(o.cursor), "precondition: a fresh config starts unmodified")

	_, changed := o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRight}) // cycle off the default
	require.Equal(t, "theme", changed)
	require.True(t, o.isModified(o.cursor), "precondition: the row is modified before reset")

	_, changed = o.HandleKeyPress(keyRunes("r"))
	assert.Equal(t, "theme", changed, "r reports the key so home persists and live-applies")
	assert.False(t, o.isModified(o.cursor), "r restored the default")
}

// TestResetIsSilentOnAnUnmodifiedRow pins that the reported key means "this value just
// changed". Reporting unconditionally would rewrite config.json and re-run the live-apply
// hook — for theme, a full ClearScreen repaint — on every press of a key that did nothing.
func TestResetIsSilentOnAnUnmodifiedRow(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	settingsAt(t, o, "theme")
	require.False(t, o.isModified(o.cursor))

	_, changed := o.HandleKeyPress(keyRunes("r"))
	assert.Empty(t, changed, "a reset that changed nothing reports nothing")
}

// TestEveryResettableRowResetsToItsDefaultDisplay sweeps the schema: calling reset must
// leave get() equal to defaultDisplay(), for every row that declares one.
//
// It runs against a default config, which is NOT vacuous: the assertion fails whenever
// reset writes a value the row does not consider default — a reset setting c.Theme = "dark"
// or c.GlyphSet = "nerd" is caught here even though nothing was modified first. It is the
// direction a per-row hand-written test would miss on row 31 of 38.
func TestEveryResettableRowResetsToItsDefaultDisplay(t *testing.T) {
	cfg := config.DefaultConfig()
	o := NewSettingsOverlay(cfg)
	checked := 0
	for i := range o.rows {
		row := &o.rows[i]
		if row.reset == nil {
			continue
		}
		require.NotNilf(t, row.defaultDisplay, "row %q resets to a default it does not declare", row.key)
		row.reset(cfg)
		assert.Equalf(t, row.defaultDisplay(), row.get(cfg),
			"row %q's reset does not restore its declared default", row.key)
		checked++
	}
	require.Greater(t, checked, 20, "the sweep must actually visit the schema")
}

// TestResetAndDefaultDisplayComeInPairs pins the schema invariant r rests on: a row can be
// put back exactly when it declares somewhere to go. Without it, r on default_program would
// silently do nothing while the panel claimed a change, or a row with a marker would have no
// way to clear it. kindReadOnly has neither, by construction.
func TestResetAndDefaultDisplayComeInPairs(t *testing.T) {
	for _, r := range newSettingRows(config.DefaultConfig()) {
		if r.kind == kindReadOnly {
			assert.Nilf(t, r.reset, "%q is read-only and must not offer a reset", r.key)
			assert.Nilf(t, r.defaultDisplay, "%q is read-only and has no default to show", r.key)
			continue
		}
		assert.Equalf(t, r.defaultDisplay == nil, r.reset == nil,
			"%q declares one of defaultDisplay/reset without the other", r.key)
	}
}

// TestResetOnARowWithNoFixedDefaultIsASilentNoOp covers the two rows spec §5 makes nil by
// design — default_program (the first *detected* agent) and branch_prefix (the OS username).
// They have nowhere to go back to, so r must not pretend otherwise.
func TestResetOnARowWithNoFixedDefaultIsASilentNoOp(t *testing.T) {
	for _, key := range []string{"default_program", "branch_prefix"} {
		cfg := config.DefaultConfig()
		o := NewSettingsOverlay(cfg)
		settingsAt(t, o, key)
		require.Nil(t, o.rows[o.cursor].reset, "precondition: %q declares no reset", key)

		before := rowValues(o)
		_, changed := o.HandleKeyPress(keyRunes("r"))
		assert.Emptyf(t, changed, "r on %q must report nothing", key)
		assert.Equalf(t, before, rowValues(o), "r on %q must change nothing", key)
	}
}

// TestResetOnTheRailIsASilentNoOp is spec §2's non-goal, made structural: there is no
// category reset. Pressing r with the rail focused must not clear a category's worth of
// settings, and must not say it did.
func TestResetOnTheRailIsASilentNoOp(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Theme = "gruvbox" // something modified for a category reset to have destroyed
	o := NewSettingsOverlay(cfg)
	o.SetRailIndex(railIndexForCategory(catAppearance))
	require.Equal(t, focusRail, o.focus)

	before := rowValues(o)
	closed, changed := o.HandleKeyPress(keyRunes("r"))
	assert.False(t, closed)
	assert.Empty(t, changed)
	assert.Equal(t, before, rowValues(o), "r on the rail must not touch a single row")
}

// TestResetOnTheReadOnlyRowIsASilentNoOp: the resolved config.json path has no setter and
// no default, and every edit key is a no-op on it (spec §5's kindReadOnly).
func TestResetOnTheReadOnlyRowIsASilentNoOp(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	settingsAt(t, o, "config_file")
	require.Equal(t, kindReadOnly, o.rows[o.cursor].kind)

	_, changed := o.HandleKeyPress(keyRunes("r"))
	assert.Empty(t, changed)
}
```

> `config_file` is the read-only row's key — confirm it against `settings_schema.go`
> (`grep -n 'key: *"config' ui/overlay/settings_schema.go`) before running, and use the real
> key if it differs.

- [ ] **Step 2: Run the tests to verify they fail**

```bash
PATH="/home/zvi/.local/share/mise/installs/go/1.26.4/bin:$PATH" \
  go test ./ui/overlay/ -run 'TestReset|TestEveryResettable' 2>&1 | head -20
```
Expected: FAIL — `TestResetRestoresTheDefaultAndReportsTheKey` reports `""` because nothing
handles `r`.

- [ ] **Step 3: Implement `resetRow`**

In `ui/overlay/settings_nav.go`, next to `toggleBool` and `cycleEnum`'s siblings (they live
in `settings.go`; put `resetRow` beside the key handlers that call it, in
`settings_nav.go`):

```go
// resetRow restores a row to its built-in default and reports its key, or "" when nothing
// changed — the same contract toggleBool and cycleEnum have, so home persists and
// live-applies a reset exactly like an edit (spec §13's guard 8).
//
// The before/after comparison is what makes the reported key mean "this value just
// changed". Reporting unconditionally would rewrite config.json and re-run the field's
// live-apply hook — for theme, a full ClearScreen repaint — every time r is pressed on a
// row already at its default.
//
// A nil reset is a silent no-op, not an error: kindReadOnly has nothing to set, and
// default_program and branch_prefix have no fixed default to return to (spec §5).
func (s *SettingsOverlay) resetRow(row *settingRow) string {
	s.lastErr = ""
	if row.reset == nil {
		return ""
	}
	before := row.get(s.cfg)
	row.reset(s.cfg)
	if row.get(s.cfg) == before {
		return ""
	}
	return row.key
}
```

- [ ] **Step 4: Bind `r` in the rows pane only**

In `handleRowsKey`'s switch, after the `" "` (space) arm:

```go
	case "r":
		return false, s.resetRow(row)
```

`handleRailKey` gains **nothing**: spec §2 makes category reset a non-goal, and the absence
of an arm is the implementation. `TestResetOnTheRailIsASilentNoOp` is what stops a later
"consistency" edit from adding one.

- [ ] **Step 5: Run the tests to verify they pass**

```bash
PATH="/home/zvi/.local/share/mise/installs/go/1.26.4/bin:$PATH" \
  go test ./ui/overlay/ -run 'TestReset|TestEveryResettable' -v 2>&1 | tail -20
```
Expected: PASS (7 tests).

- [ ] **Step 6: Add the app-level round trip**

Append to `app/settings_test.go`:

```go
// r routes through applySettingChange exactly as an edit does: the config is persisted and
// the live-apply hook runs. Pinned on theme because its hook is observable — theme.Set
// swaps the active palette, so a reset that persisted without live-applying would leave the
// running UI painted in a theme config.json no longer names.
func TestSettingsPanel_ResetPersistsAndLiveApplies(t *testing.T) {
	resetSettingsTestState(t)
	h := newSettingsTestHome()
	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(",")})
	require.Equal(t, stateSettings, h.state)

	require.True(t, h.settingsOverlay.OpenAt("theme"))
	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRight}) // off the default
	changed := h.appConfig.Theme
	require.NotEmpty(t, changed, "precondition: the theme is now explicitly set")
	require.Equal(t, changed, config.LoadConfig().Theme, "precondition: the edit reached disk")

	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})

	assert.Empty(t, h.appConfig.Theme, "r cleared the explicit theme")
	assert.Empty(t, config.LoadConfig().Theme, "r persisted, like an edit")
	assert.Equal(t, theme.DefaultThemeName, theme.Current().Name,
		"r live-applied: the running UI repainted in the default palette")
}
```

> `theme.Current().Name` — confirm the field name against `ui/theme/theme.go` while
> iterating; if the theme struct exposes it differently, assert on whatever
> `applySettingChange`'s `theme.Set` observably changes. **Do not weaken this to "the config
> was saved"** — the persist half is already covered and the live-apply half is the point.

This test uses `OpenAt`, which Task 3 introduces. Write it now and run it at the end of
Task 3; or write it with `SelectRow` and rename it there. Do not leave both names live.

- [ ] **Step 7: Verify the guards fail when they should**

Mandatory. Three mutations, each restored by editing the line back — **never** with
`git checkout`:

1. Make `resetRow` return `row.key` unconditionally (delete the before/after comparison).
   Expected: `TestResetIsSilentOnAnUnmodifiedRow` FAILS. Restore.
2. Make `resetRow` return `""` always. Expected:
   `TestResetRestoresTheDefaultAndReportsTheKey` FAILS on the reported key **and**
   `TestSettingsPanel_ResetPersistsAndLiveApplies` FAILS on the disk read. Restore.
3. Add `case "r": return s.resetRow(&s.rows[s.cursor]) != ""` to `handleRailKey`'s switch
   (the shape a "consistency" edit would take). Expected: `TestResetOnTheRailIsASilentNoOp`
   FAILS. Restore by deleting the arm.
4. Change one row's `reset` in `settings_schema.go` to write a non-default value
   (`c.GlyphSet = "nerd"`). Expected: `TestEveryResettableRowResetsToItsDefaultDisplay`
   FAILS naming `glyph_set`. Restore.
5. Re-run and confirm green.

- [ ] **Step 8: Lint and commit**

```bash
PATH="/home/zvi/.local/share/mise/installs/go/1.26.4/bin:$PATH" /home/zvi/go/bin/golangci-lint run ./ui/... ./app/...
git add ui/overlay/settings_nav.go ui/overlay/settings_nav_test.go app/settings_test.go
git commit -m "feat(settings): r resets a row to its built-in default"
```

---

## Task 3: `OpenAt` replaces `SelectRow`

**Files:**
- Modify: `ui/overlay/settings.go:102-123`
- Modify: `ui/overlay/settings_test.go:30-33` (the `settingsAt` helper) and its four direct
  call sites; `ui/overlay/settings_nav_test.go`; `ui/overlay/settings_render_test.go`;
  `app/settings_test.go`
- Modify: `docs/superpowers/specs/2026-07-25-configuration-panel-design.md` (§12)

**Interfaces:**
- Consumes: `railIndexForCategory`, `focusRows`.
- Produces: `(*SettingsOverlay).OpenAt(key string) bool`. Task 6's two call sites use it
  through `home`.

Guard 11: "`OpenAt(category, key)` lands the cursor on that row with the rows pane focused."

`OpenAt` does one thing `SelectRow` did not: it **clears the panel's transient state**
first. A deep link into a panel that is mid-edit, showing `?`, or filtering would otherwise
land the cursor somewhere the user cannot see it. Today's two call sites open a fresh
panel, so nothing is pending — which is exactly why this must be in the function rather
than at the call sites, where the omission would be invisible until the third one.

- [ ] **Step 1: Write the failing test**

Replace `TestSelectRowFocusesTheRowsPaneAndSyncsTheRail` in
`ui/overlay/settings_nav_test.go` with:

```go
// TestOpenAtLandsOnEveryRowWithTheRowsPaneFocused is spec §13's guard 11, swept over the
// whole schema: the deep-link primitive must land the cursor on the row, focus the rows
// pane, and sync the rail to that row's category. Selecting a row the pane is not showing
// would leave the cursor invisible — the composite behavior IS the contract.
func TestOpenAtLandsOnEveryRowWithTheRowsPaneFocused(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	for _, r := range newSettingRows(config.DefaultConfig()) {
		require.Truef(t, o.OpenAt(r.key), "no row %q", r.key)
		assert.Equalf(t, r.key, o.selectedRow().key, "OpenAt(%q) must land the cursor on that row", r.key)
		assert.Equalf(t, focusRows, o.focus, "OpenAt(%q) must focus the rows pane", r.key)
		assert.Equalf(t, r.category, o.selectedEntry().category,
			"OpenAt(%q) must sync the rail to its category", r.key)
		start, end := o.rowRange(o.selectedEntry())
		assert.GreaterOrEqualf(t, o.cursor, start, "OpenAt(%q) left the cursor outside the pane", r.key)
		assert.Lessf(t, o.cursor, end, "OpenAt(%q) left the cursor outside the pane", r.key)
	}
	assert.False(t, o.OpenAt("not_a_row"), "an unknown key reports not-found")
}

// TestOpenAtClearsTransientState pins the half a deep link only needs when the panel is
// already open: landing while an editor, the ? view or a filter is up would put the cursor
// somewhere the user cannot see. Today's two call sites open a fresh panel — which is
// exactly why this belongs in OpenAt rather than at the call sites, where omitting it would
// stay invisible until the third one.
func TestOpenAtClearsTransientState(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	settingsAt(t, o, "branch_prefix")
	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEnter}) // opens the inline editor
	require.True(t, o.editing, "precondition: an editor is open")

	require.True(t, o.OpenAt("max_sessions"))
	assert.False(t, o.editing, "a deep link must not land inside another row's editor")

	o.HandleKeyPress(keyRunes("?"))
	require.True(t, o.helpOpen, "precondition: the ? view is open")
	require.True(t, o.OpenAt("theme"))
	assert.False(t, o.helpOpen, "a deep link must not land behind the ? view")
}
```

The filter half of that test is added in Task 5, once `startSearch` exists.

- [ ] **Step 2: Run it to verify it fails**

```bash
PATH="/home/zvi/.local/share/mise/installs/go/1.26.4/bin:$PATH" \
  go test ./ui/overlay/ -run 'TestOpenAt' 2>&1 | head
```
Expected: FAIL to build — `o.OpenAt undefined`.

- [ ] **Step 3: Rename the method and expand its contract**

Replace `SelectRow` in `ui/overlay/settings.go` with:

```go
// OpenAt moves the panel to the row with the given key, reporting whether it exists. It
// syncs the rail to that row's category, focuses the rows pane, and drops any transient
// state (an open editor, the ? view, an active filter) so the cursor lands somewhere the
// user can actually see.
//
// That composite behavior is the deep-link contract of spec §12 — it is what makes a jump
// from a dialog or a notice land somewhere usable. It is also the path most of this
// package's tests take to reach a row, through the settingsAt helper.
//
// It takes the key alone, where spec §12 wrote OpenAt(category, key). settingCategory is
// unexported, so app cannot name one; and guard 1 pins that every key belongs to exactly
// one category, so a passed category would be a second source of truth that can only ever
// disagree with the row's own. The spec is amended to match.
func (s *SettingsOverlay) OpenAt(key string) bool {
	for i, r := range s.rows {
		if r.key != key {
			continue
		}
		s.cursor = i
		s.railCursor = railIndexForCategory(r.category)
		s.focus = focusRows
		s.editing = false
		s.helpOpen = false
		s.helpScroll = 0
		s.lastErr = ""
		return true
	}
	return false
}
```

Task 5 adds `s.clearSearch()` to this body.

- [ ] **Step 4: Migrate every caller in the same commit**

```bash
sed -i 's/\.SelectRow(/.OpenAt(/g' \
  ui/overlay/settings_test.go ui/overlay/settings_nav_test.go \
  ui/overlay/settings_render_test.go app/settings_test.go
grep -rn "SelectRow" --include="*.go" .
```
The `grep` must come back with **only prose hits in comments, plus the body of
`TestSelectRowFocusesTheRowsPaneAndSyncsTheRail`** (`settings_nav_test.go:291-310`), which
Step 1 replaces wholesale — delete that test if it is still there. Rewrite the prose:
`ui/overlay/settings_test.go:574`, `ui/overlay/settings_render_test.go:280`,
`ui/overlay/settings_nav_test.go:69`, `app/settings_test.go:65` and `:386` each name the
method, and a comment naming a method that no longer exists is exactly the defect class PR
B's review found twice.

- [ ] **Step 5: Run the whole package**

```bash
PATH="/home/zvi/.local/share/mise/installs/go/1.26.4/bin:$PATH" \
  go test ./ui/overlay/ ./app/ 2>&1 | tail -10
```
Expected: PASS. If dozens of tests fail, `settingsAt` was missed — 62 calls go through it.

- [ ] **Step 6: Amend the spec**

In `docs/superpowers/specs/2026-07-25-configuration-panel-design.md` §12, insert after the
first paragraph:

```markdown
> **Resolved in PR C: the signature is `OpenAt(key string) bool`.** `settingCategory` is
> unexported, so `app` cannot name one. More importantly, guard 1 pins that every key
> belongs to exactly one category, so a category parameter is a second source of truth whose
> only possible contribution is to disagree with the row's own — and the only sane
> resolution of a disagreement is to trust the row. `OpenAt` derives the category and syncs
> the rail to it, which is what `TestOpenAtLandsOnEveryRowWithTheRowsPaneFocused` asserts
> over all 38 rows.
```

- [ ] **Step 7: Verify the guard fails when it should**

1. Delete `s.focus = focusRows` from `OpenAt`. Expected:
   `TestOpenAtLandsOnEveryRowWithTheRowsPaneFocused` FAILS on the focus assertion, and a
   handful of `app/settings_test.go` tests fail too (they send keys expecting them to reach
   the row). Restore.
2. Delete `s.railCursor = railIndexForCategory(r.category)`. Expected: the rail-sync and
   in-range assertions FAIL. Restore.
3. Delete `s.editing = false`. Expected: `TestOpenAtClearsTransientState` FAILS. Restore.
4. Re-run and confirm green.

- [ ] **Step 8: Lint and commit**

```bash
PATH="/home/zvi/.local/share/mise/installs/go/1.26.4/bin:$PATH" /home/zvi/go/bin/golangci-lint run ./ui/... ./app/...
git add -u ui/overlay app docs/superpowers/specs
git status --short   # nothing unexpected, no stray 0-byte _test.go
git commit -m "refactor(settings): promote SelectRow to the OpenAt deep-link entry point"
```

> `git status --short` before every commit in this plan. A prior stage staged a **0-byte
> `_test.go`** left behind by a helper agent; `git commit -a` would have shipped it.

---

## Task 4: Enter on Accounts opens the accounts overlay

**Files:**
- Modify: `ui/overlay/settings_nav.go` (`SettingsHandoff`, `railEntry.opens`,
  `handleRailKey`, the Accounts note)
- Modify: `ui/overlay/settings.go` (the `handoff` field, `Handoff()`)
- Modify: `ui/overlay/settings_render.go` (`hintLine`'s rail ladders)
- Modify: `ui/overlay/settings_nav_test.go` (`TestRightFocusesTheRowsPaneFromTheRail`,
  `TestEveryHandoffEntryNamesItsSurface`)
- Modify: `app/app_keys.go` (`handleSettingsState`), `app/app_update.go` (`openAccounts`)
- Modify: `app/settings_test.go`

**Interfaces:**
- Consumes: `railEntry`, `railHandoff`.
- Produces: `overlay.SettingsHandoff` (`HandoffNone`, `HandoffAccounts`);
  `(*SettingsOverlay).Handoff() SettingsHandoff`; `(*home).openAccounts() tea.Cmd`.

The overlay cannot open a sibling, so it **records a request** and returns `closed = true`;
`home` reads the request as it tears the panel down. That keeps `HandleKeyPress`'s two
return values intact — a third would touch every caller — and it keeps the decision about
what "the accounts surface" is where the surfaces live.

**Profiles stays a no-op** and keeps its note. PR D replaces that entry with an editor;
wiring it to nothing now would mean writing the handoff twice.

- [ ] **Step 1: Write the failing tests**

In `ui/overlay/settings_nav_test.go`, replace `TestRightFocusesTheRowsPaneFromTheRail`'s
handoff half and add:

```go
// TestRightFocusesTheRowsPaneFromTheRail pins the rail's three forward keys on an entry
// that owns rows.
func TestRightFocusesTheRowsPaneFromTheRail(t *testing.T) {
	for _, key := range []tea.KeyMsg{
		{Type: tea.KeyRight}, {Type: tea.KeyTab}, {Type: tea.KeyEnter},
	} {
		o := NewSettingsOverlay(config.DefaultConfig())
		o.HandleKeyPress(key)
		assert.Equalf(t, focusRows, o.focus, "%v must focus the rows pane", key)
	}
}

// TestAccountsEntryHandsOffToTheAccountsOverlay is spec §4's handoff and §7's rail row: all
// three forward keys ask home to open the @ overlay, and the panel closes to make way. The
// overlay cannot open a sibling, so a request plus closed=true is the whole protocol.
func TestAccountsEntryHandsOffToTheAccountsOverlay(t *testing.T) {
	for _, key := range []tea.KeyMsg{
		{Type: tea.KeyRight}, {Type: tea.KeyTab}, {Type: tea.KeyEnter},
	} {
		o := NewSettingsOverlay(config.DefaultConfig())
		o.SetRailIndex(len(railEntries()) - 1)
		require.Equal(t, "Accounts", o.selectedEntry().label, "precondition: the last entry is Accounts")
		require.Equal(t, HandoffNone, o.Handoff(), "precondition: nothing requested yet")

		closed, changed := o.HandleKeyPress(key)
		assert.Truef(t, closed, "%v on Accounts closes the panel to make way", key)
		assert.Empty(t, changed, "a handoff changes no setting")
		assert.Equal(t, HandoffAccounts, o.Handoff())
		assert.Equal(t, focusRail, o.focus, "focus never moves into an entry with no rows")
	}
}

// TestProfilesEntryStaysANoOp pins the deliberate asymmetry: PR D builds the profiles
// editor, so PR C must not wire that entry to anything. A handoff to a surface that does
// not exist is worse than the note.
func TestProfilesEntryStaysANoOp(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	o.SetRailIndex(len(railEntries()) - 2)
	require.Equal(t, "Profiles", o.selectedEntry().label)

	closed, changed := o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEnter})
	assert.False(t, closed)
	assert.Empty(t, changed)
	assert.Equal(t, HandoffNone, o.Handoff())
	assert.Equal(t, focusRail, o.focus)
}

// TestRailHintNamesWhatTheForwardKeyDoes: the hint differs per entry because the forward key
// does three different things — focus the rows, open another overlay, or nothing at all.
// Advertising "→ rows" on an entry with no rows is the same class of lie as a static esc
// hint (spec §15).
func TestRailHintNamesWhatTheForwardKeyDoes(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	o.SetSize(100, 32)

	require.Equal(t, railCategory, o.selectedEntry().kind)
	assert.Contains(t, stripANSI(o.hintLine()), "→ rows")

	o.SetRailIndex(len(railEntries()) - 1)
	accounts := stripANSI(o.hintLine())
	assert.Contains(t, accounts, "↵ accounts")
	assert.NotContains(t, accounts, "→ rows", "Accounts has no rows to focus")

	o.SetRailIndex(len(railEntries()) - 2)
	profiles := stripANSI(o.hintLine())
	assert.NotContains(t, profiles, "→ rows")
	assert.NotContains(t, profiles, "↵ accounts", "Profiles opens nothing in PR C")
	assert.Contains(t, profiles, "esc close")
}
```

And update `TestEveryHandoffEntryNamesItsSurface`'s expectation by changing the Accounts
note (Step 3) — the test itself only requires a non-empty note, so it keeps passing.

Append to `app/settings_test.go`:

```go
// The rail's Accounts entry actually opens the accounts overlay: the panel closes, the @
// overlay opens in its place, and the remembered rail brings the user back to Accounts on
// the next ','. Home-level wiring, because an overlay cannot open a sibling.
func TestSettingsPanel_AccountsEntryOpensTheAccountsOverlay(t *testing.T) {
	resetSettingsTestState(t)
	h := newSettingsTestHome()
	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(",")})
	require.Equal(t, stateSettings, h.state)

	h.settingsOverlay.SetRailIndex(h.settingsOverlay.RailEntryCount() - 1)
	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyEnter})

	assert.Equal(t, stateAccounts, h.state, "Enter on Accounts opens the @ overlay")
	assert.NotNil(t, h.accountsOverlay)
	assert.Nil(t, h.settingsOverlay, "the settings panel closed to make way")

	// Closing accounts and reopening settings lands back on the entry we left.
	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyEsc})
	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(",")})
	require.NotNil(t, h.settingsOverlay)
	assert.Equal(t, h.settingsOverlay.RailEntryCount()-1, h.settingsOverlay.RailIndex())
}
```

- [ ] **Step 2: Run to verify they fail**

```bash
PATH="/home/zvi/.local/share/mise/installs/go/1.26.4/bin:$PATH" \
  go test ./ui/overlay/ ./app/ -run 'Handoff|Accounts|Profiles|RailHint' 2>&1 | head -20
```
Expected: FAIL to build — `undefined: HandoffNone`, `o.Handoff undefined`,
`RailEntryCount undefined`.

- [ ] **Step 3: Add the handoff vocabulary**

In `ui/overlay/settings_nav.go`, above `railKind`:

```go
// SettingsHandoff names a surface the settings panel has asked the home model to open in
// its place. The panel cannot open a sibling overlay itself, so a rail entry that owns no
// rows records a request and closes; home reads it as it tears the panel down.
type SettingsHandoff int

const (
	// HandoffNone means the panel closed on its own terms.
	HandoffNone SettingsHandoff = iota
	// HandoffAccounts asks home to open the Claude/GitHub/Antigravity account manager — the
	// same overlay the '@' key opens from the session list.
	HandoffAccounts
)
```

Add the field to `railEntry`:

```go
	// opens is the surface this entry hands off to, for a railHandoff entry that has one.
	// Profiles is HandoffNone until PR D replaces it with a real editor: a handoff to a
	// surface that does not exist would be worse than the note.
	opens SettingsHandoff
```

and set it in `railEntries()`, rewriting the Accounts note now that Enter does the work:

```go
		railEntry{
			label: "Accounts", kind: railHandoff, opens: HandoffAccounts,
			note: "Claude, GitHub and Antigravity accounts — press ↵ to open the accounts overlay.",
		},
```

- [ ] **Step 4: Record and expose the request**

In `ui/overlay/settings.go`, add to the struct:

```go
	// handoff is the sibling surface a rail entry asked home to open in this panel's place.
	// Read once, as the panel closes — see Handoff.
	handoff SettingsHandoff
```

and, beside `RailIndex`:

```go
// Handoff reports which sibling surface the panel asked to open in its place, or
// HandoffNone. home reads it when HandleKeyPress reports the panel closed.
func (s *SettingsOverlay) Handoff() SettingsHandoff { return s.handoff }

// RailEntryCount is how many entries the rail has, so home (and its tests) can address the
// last one without importing the rail's vocabulary.
func (s *SettingsOverlay) RailEntryCount() int { return len(railEntries()) }
```

- [ ] **Step 5: Wire the rail key**

In `handleRailKey`, replace the `"right", "tab", "enter"` arm:

```go
	case "right", "tab", "enter":
		if start, end := s.rowRange(s.selectedEntry()); end > start {
			s.focus = focusRows
			return false
		}
		// An entry with no rows either hands off to another surface or does nothing. The
		// panel closes on a handoff so the surface it names takes the screen; focus never
		// moves into an empty pane either way.
		if opens := s.selectedEntry().opens; opens != HandoffNone {
			s.handoff = opens
			return true
		}
	}
```

- [ ] **Step 6: Split the rail's hint ladder**

In `settings_render.go`'s `hintLine`, replace the `default:` (rail) branch:

```go
	default:
		ladder = railHintLadder(s.selectedEntry())
	}
```

and add, below `hintLine`:

```go
// railHintLadder is the rail's key hints for one entry, widest wording first.
//
// The forward key does three different things on the rail — focus the rows, open another
// overlay, or nothing at all — so the hint names the one that applies. A static "→ rows" on
// an entry with no rows is the same class of lie a static esc hint would be (spec §15).
func railHintLadder(e railEntry) []string {
	forward := "→ rows"
	if e.kind == railHandoff {
		forward = "" // Profiles: PR D gives it an editor; until then the key does nothing
		if e.opens == HandoffAccounts {
			forward = "↵ accounts"
		}
	}
	if forward == "" {
		return []string{
			"↑/↓ category · / search · ⇥ pane · esc close",
			"↑/↓ · / search · esc close",
			"esc close",
		}
	}
	return []string{
		"↑/↓ category · " + forward + " · / search · ⇥ pane · esc close",
		"↑/↓ · " + forward + " · / search · esc close",
		"/ search · esc close",
		"esc close",
	}
}
```

`/ search` appears here before Task 5 wires it. That is the one ordering compromise in this
plan and it is deliberate: splitting the ladder twice would mean writing
`TestRailHintNamesWhatTheForwardKeyDoes` against a string Task 5 immediately rewrites.
**Task 5 must land in the same PR** — a shipped hint for a dead key is worse than no hint.

- [ ] **Step 7: Wire home**

In `app/app_update.go`, extract the two overlay openers from the `KeySettings` /
`KeyAccounts` arms so the handoff can reuse one:

```go
	case keys.KeySettings:
		return m, m.openSettings()
	case keys.KeyAccounts:
		return m, m.openAccounts()
```

and add, near them:

```go
// openSettings opens the configuration panel on the rail entry the last open left it on.
// Returns the command that re-sizes it; every caller is a key handler that returns it
// straight through.
func (m *home) openSettings() tea.Cmd {
	m.state = stateSettings
	m.settingsOverlay = overlay.NewSettingsOverlay(m.appConfig)
	if m.settingsRail != nil {
		m.settingsOverlay.SetRailIndex(*m.settingsRail)
	}
	m.refreshSettingsClusteringGate()
	m.recomputeLayout() // the hint bar hides behind the modal; panes reclaim its row
	return tea.WindowSize()
}

// openAccounts opens the Claude/GitHub/Antigravity account manager — the '@' key's overlay,
// and the surface the settings rail's Accounts entry hands off to.
func (m *home) openAccounts() tea.Cmd {
	m.state = stateAccounts
	m.accountsOverlay = overlay.NewAccountsOverlay(m.appConfig, m.appState)
	m.recomputeLayout()
	return tea.WindowSize()
}
```

In `app/app_keys.go`'s `handleSettingsState`, read the request **before** dropping the
overlay:

```go
	if closed {
		handoff := m.settingsOverlay.Handoff()
		rail := m.settingsOverlay.RailIndex()
		m.settingsRail = &rail
		m.settingsOverlay = nil
		m.state = stateDefault
		m.recomputeLayout() // menuVisible flipped; the hint bar may reclaim its row
		if handoff == overlay.HandoffAccounts {
			// The rail entry asked for the sibling overlay; openAccounts sets the state and
			// returns its own WindowSize, so the default one below is not also needed.
			return m, tea.Batch(append(cmds, m.openAccounts())...)
		}
		cmds = append(cmds, tea.WindowSize())
	}
```

- [ ] **Step 8: Run everything**

```bash
PATH="/home/zvi/.local/share/mise/installs/go/1.26.4/bin:$PATH" \
  go test ./ui/overlay/ ./app/ 2>&1 | tail -10
```
Expected: PASS.

- [ ] **Step 9: Verify the guards fail when they should**

1. Set `opens: HandoffAccounts` on the **Profiles** entry. Expected:
   `TestProfilesEntryStaysANoOp` FAILS. Restore.
2. In `handleSettingsState`, move `handoff := m.settingsOverlay.Handoff()` below
   `m.settingsOverlay = nil`. Expected: a nil-pointer panic in
   `TestSettingsPanel_AccountsEntryOpensTheAccountsOverlay` — which is the ordering hazard
   the step exists to pin. Restore.
3. Return `false` instead of `true` from the handoff arm in `handleRailKey`. Expected:
   `TestAccountsEntryHandsOffToTheAccountsOverlay` FAILS on `closed`, **and**
   `TestSettingsPanel_AccountsEntryOpensTheAccountsOverlay` FAILS on the state. Restore.
4. Re-run and confirm green.

- [ ] **Step 10: Lint and commit**

```bash
PATH="/home/zvi/.local/share/mise/installs/go/1.26.4/bin:$PATH" /home/zvi/go/bin/golangci-lint run ./ui/... ./app/...
git status --short
git add -u ui/overlay app
git commit -m "feat(settings): the accounts rail entry opens the accounts overlay"
```

---

## Task 5: `/` search — the model

**Files:**
- Modify: `ui/overlay/settings.go` (the `search` field, the `HandleKeyPress` router,
  `OpenAt`)
- Modify: `ui/overlay/settings_nav.go` (the search state machine)
- Modify: `ui/overlay/settings_nav_test.go`

**Interfaces:**
- Consumes: `Picker` (`newPicker(false)`, `handleKey`, `clampCursor`, `Focus`, `Blur`,
  `IsFocused`), `fuzzy.Match`.
- Produces: `(*SettingsOverlay).searching() bool`, `.startSearch()`, `.clearSearch()`,
  `.searchResults() []int`, `.syncCursorToSearch()`, `.visibleRowIndices() []int`,
  `.searchHaystack(settingRow) string`. Task 6 renders against all of them.

**The Picker is a named field, not embedded.** Embedding would promote `Focus`, `Blur`,
`IsFocused`, `SetWidth`, `SetVisibleRows` and `SetPreviewHook` onto `SettingsOverlay`'s
public API, where five of the six are meaningless for a panel that sizes itself.

**`s.cursor` stays the global row index.** `isModified(i)`, `inertReason(i)`,
`renderRowLine(i, …)`, `expandedHelpContent(s.cursor)` and `handleEditKey` all read it, so
the search keeps it authoritative and derives it from the picker's cursor after every move.

**The rail cursor follows the highlighted result.** That is not decoration: it makes the
rail's marker honest while the rail is inert, it shows which category a hit lives in, and
it means `Esc` needs no special landing logic — the rail is already synced, so clearing the
filter leaves the user on the row they found, in its category.

- [ ] **Step 1: Write the failing tests**

Append to `ui/overlay/settings_nav_test.go`:

```go
// typeFilter sends each rune of s to the panel as its own key press, which is how a real
// filter is typed — sending them as one KeyRunes would hide a per-keystroke bug.
func typeFilter(o *SettingsOverlay, s string) {
	for _, r := range s {
		o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
}

// resultKeys is the search result list as row keys, which is what the assertions are about.
func resultKeys(o *SettingsOverlay) []string {
	out := make([]string, 0, len(o.searchResults()))
	for _, i := range o.searchResults() {
		out = append(out, o.rows[i].key)
	}
	return out
}

// TestSearchFindsARowByKeyByLabelAndBySummaryWord is spec §13's guard 9. Three query shapes,
// because the whole point of matching four fields is that a user who remembers any one of
// them finds the row.
func TestSearchFindsARowByKeyByLabelAndBySummaryWord(t *testing.T) {
	cases := []struct{ name, query, want string }{
		{"by key", "notify_command", "notify_command"},
		{"by label", "Glyph set", "glyph_set"},
		{"by a word from the summary", "taskbar", "os_chrome"},   // "…window title and taskbar progress."
		{"by category name", "Worktrees", "branch_prefix"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := NewSettingsOverlay(config.DefaultConfig())
			o.HandleKeyPress(keyRunes("/"))
			typeFilter(o, tc.query)
			assert.Containsf(t, resultKeys(o), tc.want, "%q must find %q", tc.query, tc.want)
		})
	}
}

// TestSearchRanksTheLabelAndKeyHitFirst pins the ranking bonus, on the one query shape that
// can tell the difference: without it, "agent" is a three-way tie at 60 that stable-sorts to
// default_program — which matches only through its summary ("Agent command new sessions
// launch") — ahead of the row actually called Agent OOM margin. With the label and key
// bonuses that row scores 180 and leads.
//
// "theme" is NOT the query for this: measured, the theme row wins 60-to-40 on the haystack
// alone, so the bonus changes nothing and the mutation in Step 9 would pass.
func TestSearchRanksTheLabelAndKeyHitFirst(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	o.HandleKeyPress(keyRunes("/"))
	typeFilter(o, "agent")
	require.NotEmpty(t, resultKeys(o))
	assert.Equal(t, "agent_oom_margin", resultKeys(o)[0],
		"a label-and-key hit leads a search over a row that only matches in its summary")
}

// TestSearchFlattensAcrossCategories is spec §8's shape: results ignore the rail entry
// entirely. A filter that only searched the current category would be a category filter,
// not a search.
func TestSearchFlattensAcrossCategories(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	require.Equal(t, catSessions, o.selectedEntry().category, "precondition: the landing category")

	o.HandleKeyPress(keyRunes("/"))
	typeFilter(o, "in")
	seen := map[settingCategory]bool{}
	for _, i := range o.searchResults() {
		seen[o.rows[i].category] = true
	}
	// Named categories, not len(seen) > 1: the earlier draft's assertion was the require
	// above restated, and so was true by construction. Measured, "in" returns 36 rows
	// spanning all ten categories; these three are the landing, one below it, and the last.
	assert.True(t, seen[catSessions], "the landing category")
	assert.True(t, seen[catAppearance], "a category the rail is not on")
	assert.True(t, seen[catAdvanced], "and the far end of the rail")
}

// TestSlashFocusesTheRowsPaneFromEitherPane is spec §8's first focus rule, stated because it
// is the detail that gets guessed wrong: / works from the rail as well as the rows.
func TestSlashFocusesTheRowsPaneFromEitherPane(t *testing.T) {
	for _, from := range []settingsFocus{focusRail, focusRows} {
		o := NewSettingsOverlay(config.DefaultConfig())
		if from == focusRows {
			o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRight})
		}
		require.Equal(t, from, o.focus)

		o.HandleKeyPress(keyRunes("/"))
		assert.True(t, o.searching(), "/ opens the filter from either pane")
		assert.Equal(t, focusRows, o.focus, "/ moves focus to the results")
	}
}

// TestSlashDoesNotMoveTheCursorBeforeAnythingIsTyped. An empty query matches all 38 rows at
// score 0, so a naive sync snaps the cursor to row 0 and the rail to Sessions the moment `/`
// is pressed — and the Esc that "lands you on the row you found" then lands you on the top
// of the schema instead. Opened from the landing category the bug is invisible (row 0 is
// already the cursor), which is exactly why this opens from Advanced.
func TestSlashDoesNotMoveTheCursorBeforeAnythingIsTyped(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	settingsAt(t, o, "agent_oom_margin")
	require.Equal(t, catAdvanced, o.selectedEntry().category, "precondition: away from the landing")

	o.HandleKeyPress(keyRunes("/"))
	assert.Equal(t, "agent_oom_margin", o.selectedRow().key, "/ must not move the cursor")
	assert.Equal(t, catAdvanced, o.selectedEntry().category, "nor the rail")
	assert.Contains(t, stripANSI(o.contextLine(o.innerWidth())),
		fmt.Sprintf("/%d", len(o.rows)), "and the readout must count the row it is actually on")

	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEsc})
	assert.Equal(t, "agent_oom_margin", o.selectedRow().key, "esc on an untyped filter is a no-op")
	assert.Equal(t, catAdvanced, o.selectedEntry().category)
}

// TestRunesTypeWhileTheFilterHasFocus is spec §8's second rule and the one most likely to be
// implemented backwards: j and k are letters in a search box, not navigation. r is here too
// — the reset key must not fire mid-query — and space extends the filter rather than
// toggling the highlighted bool.
func TestRunesTypeWhileTheFilterHasFocus(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Theme = "gruvbox" // any non-default value; r would clear it
	o := NewSettingsOverlay(cfg)
	o.HandleKeyPress(keyRunes("/"))
	before := rowValues(o)

	typeFilter(o, "jkr")
	assert.Equal(t, "jkr", o.search.filter, "j, k and r type; they do not navigate or reset")
	// Every row, not just theme: the cursor is wherever the filter left it, so asserting on
	// the theme row alone would hold even if r had reset whatever row IS highlighted.
	assert.Equal(t, before, rowValues(o), "r must not have reset anything")

	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeySpace})
	assert.Equal(t, "jkr ", o.search.filter, "space extends the filter")

	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyBackspace})
	assert.Equal(t, "jkr", o.search.filter)
}

// TestArrowsMoveTheResultCursor is spec §8's third rule: ↑/↓ still navigate while the filter
// types. It also pins the coupling the rest of the panel depends on — s.cursor is the global
// row index and must track the picker's cursor, or the help pane describes one row while the
// list highlights another.
func TestArrowsMoveTheResultCursor(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	o.HandleKeyPress(keyRunes("/"))
	typeFilter(o, "in")
	results := o.searchResults()
	require.Greater(t, len(results), 2, "the query must return enough rows to move within")

	require.Equal(t, results[0], o.cursor, "the cursor starts on the best match")
	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyDown})
	assert.Equal(t, results[1], o.cursor)
	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyUp})
	assert.Equal(t, results[0], o.cursor)
	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyUp})
	assert.Equal(t, results[0], o.cursor, "up at the first result clamps")
}

// TestTheRailFollowsTheHighlightedResult: the rail cannot take keys while filtering, so its
// marker must mean something else — which category the current hit lives in. It is also what
// makes Esc's landing predictable, since clearing the filter leaves the rail already synced.
func TestTheRailFollowsTheHighlightedResult(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	require.Equal(t, catSessions, o.selectedEntry().category)

	o.HandleKeyPress(keyRunes("/"))
	typeFilter(o, "glyph")
	require.Equal(t, "glyph_set", o.selectedRow().key)
	assert.Equal(t, catAppearance, o.selectedEntry().category,
		"the rail marks the highlighted result's category")
}

// TestEditingAMatchedRowWorksAndKeepsItInTheResults is spec §8's fourth rule. The result set
// is derived from label/key/summary/category — never from the value — so an edit cannot make
// the row you are editing disappear from under you.
func TestEditingAMatchedRowWorksAndKeepsItInTheResults(t *testing.T) {
	cfg := config.DefaultConfig()
	o := NewSettingsOverlay(cfg)
	o.HandleKeyPress(keyRunes("/"))
	typeFilter(o, "notifications")
	require.Equal(t, "notifications", o.selectedRow().key)
	before := resultKeys(o)

	_, changed := o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRight})
	assert.Equal(t, "notifications", changed, "→ cycles the value, exactly as unfiltered")
	assert.Equal(t, before, resultKeys(o), "the row stays in the result list after an edit")
	assert.Equal(t, "notifications", o.selectedRow().key, "and stays highlighted")

	_, changed = o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEnter})
	assert.Equal(t, "notifications", changed, "↵ cycles an enum, exactly as unfiltered")
}

// TestEnterOpensTheLineEditorFromASearchResult: an int/text row edits the same way from a
// filtered list, and the editor — not the filter — takes the keystrokes while it is open.
func TestEnterOpensTheLineEditorFromASearchResult(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	o.HandleKeyPress(keyRunes("/"))
	typeFilter(o, "branch_prefix")
	require.Equal(t, "branch_prefix", o.selectedRow().key)

	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEnter})
	require.True(t, o.editing, "↵ opens the inline editor")
	typeFilter(o, "zz")
	assert.Equal(t, "branch_prefix", o.search.filter,
		"an open editor swallows runes; the filter must not grow behind it")

	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEsc})
	assert.False(t, o.editing)
	assert.True(t, o.searching(), "cancelling the edit returns to the filtered list")
}

// TestEscIsThreeLayeredWithAFilter is spec §8's dismissal rule and §15's warning made
// concrete: clear, back, close. Each level is advertised by hintLine (Task 6).
func TestEscIsThreeLayeredWithAFilter(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	o.HandleKeyPress(keyRunes("/"))
	typeFilter(o, "theme")
	require.True(t, o.searching())

	closed, _ := o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEsc})
	assert.False(t, closed, "the first esc clears the filter")
	assert.False(t, o.searching())
	assert.Equal(t, focusRows, o.focus, "and keeps the rows pane focused")
	assert.Equal(t, "theme", o.selectedRow().key, "landing on the row the search found")
	assert.Equal(t, catAppearance, o.selectedEntry().category)

	closed, _ = o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEsc})
	assert.False(t, closed, "the second esc backs out to the rail")
	assert.Equal(t, focusRail, o.focus)

	closed, _ = o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEsc})
	assert.True(t, closed, "the third esc closes the panel")
}

// TestQuestionMarkOpensHelpForTheHighlightedResult: ? is the one rune the filter does not
// get. Spec §8 assigns it to the expanded help while also saying runes type, and no row's
// label, key, summary or category contains a question mark — so reserving it costs the
// search nothing.
func TestQuestionMarkOpensHelpForTheHighlightedResult(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	o.HandleKeyPress(keyRunes("/"))
	typeFilter(o, "clustering")
	require.Equal(t, "group_mode", o.selectedRow().key)

	o.HandleKeyPress(keyRunes("?"))
	assert.True(t, o.helpOpen, "? opens the expanded help")
	assert.Equal(t, "clustering", o.search.filter, "? did not land in the filter")
	assert.Contains(t, o.expandedHelpContent(o.cursor), "Account clustering")

	o.HandleKeyPress(keyRunes("?"))
	assert.False(t, o.helpOpen)
	assert.True(t, o.searching(), "? returns to the filtered list it was opened from")
}

// TestNoRowContainsAQuestionMark is the premise the reservation above rests on, asserted
// rather than assumed — a future summary using one would silently make it unsearchable.
func TestNoRowContainsAQuestionMark(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	for _, r := range o.rows {
		assert.NotContainsf(t, o.searchHaystack(r), "?",
			"row %q would be unreachable: ? is reserved for the expanded help", r.key)
	}
}

// TestZeroMatchesIsStableAndRecoverable: a query matching nothing must not panic, must not
// move the cursor onto a row it cannot justify, and must be typed out of.
func TestZeroMatchesIsStableAndRecoverable(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	o.HandleKeyPress(keyRunes("/"))
	typeFilter(o, "theme")
	require.Equal(t, "theme", o.selectedRow().key)

	typeFilter(o, "zzzz")
	require.Empty(t, o.searchResults(), "precondition: nothing matches")
	assert.Equal(t, "theme", o.selectedRow().key, "the cursor holds its last valid row")

	for range 4 {
		o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyBackspace})
	}
	assert.Equal(t, "theme", o.selectedRow().key, "backspacing back to a match recovers")
}

// TestTabLeavesTheSearchForTheRail: the rail is inert while filtering, so Tab cannot focus
// it with the filter still applied. It clears and moves — the two escs in one key — rather
// than being a dead key on a keyboard that has an obvious meaning for it.
func TestTabLeavesTheSearchForTheRail(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	o.HandleKeyPress(keyRunes("/"))
	typeFilter(o, "theme")

	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyTab})
	assert.False(t, o.searching())
	assert.Equal(t, focusRail, o.focus)
	assert.Equal(t, catAppearance, o.selectedEntry().category, "on the category the search found")
}

// TestOpenAtClearsAnActiveFilter completes TestOpenAtClearsTransientState: a deep link into
// an open, filtered panel must show the row it names, not a filtered list that may exclude
// it.
func TestOpenAtClearsAnActiveFilter(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	o.HandleKeyPress(keyRunes("/"))
	typeFilter(o, "theme")
	require.True(t, o.searching())

	require.True(t, o.OpenAt("max_sessions"))
	assert.False(t, o.searching(), "a deep link clears the filter that would hide its row")
	assert.Equal(t, "max_sessions", o.selectedRow().key)
	assert.Equal(t, catSessions, o.selectedEntry().category)
}
```

- [ ] **Step 2: Run to verify they fail**

```bash
PATH="/home/zvi/.local/share/mise/installs/go/1.26.4/bin:$PATH" \
  go test ./ui/overlay/ -run 'TestSearch|TestSlash|TestRunes|TestArrowsMove|TestTheRail|TestEditingAMatched|TestEnterOpens|TestEscIsThree|TestQuestionMarkOpensHelpFor|TestNoRowContains|TestZeroMatches|TestTabLeaves|TestOpenAtClearsAn' 2>&1 | head
```
Expected: FAIL to build — `o.search undefined`, `undefined: o.searching`.

- [ ] **Step 3: Add the picker field**

In `ui/overlay/settings.go`'s struct, after `clusteringVisible`:

```go
	// search is the `/` filter (spec §8). It is a NAMED field rather than an embedded
	// Picker: embedding would promote Focus/Blur/SetWidth/SetVisibleRows/SetPreviewHook onto
	// this type's public API, where five of the six are meaningless for a panel that sizes
	// itself. The picker owns the filter text, the result cursor and the shared key grammar;
	// its IsFocused() is the "a filter is active" flag, so there is no second bool to keep in
	// step with it.
	search Picker
```

and in the constructor, **inside the `&SettingsOverlay{…}` composite literal**, beside
`height: 24,` — not after it, where `s.syncCursorToRail()` is a statement and a `field:`
line is a syntax error:

```go
		search: newPicker(false), // sync source: a filter edit re-ranks and resets the cursor
```

Add `s.clearSearch()` to `OpenAt`'s body, beside `s.editing = false`.

- [ ] **Step 4: Add the search arm to the router**

In `HandleKeyPress`, between the `helpOpen` and `focusRail` cases:

```go
	case s.searching():
		return s.handleSearchKey(msg)
```

and extend the doc comment's last paragraph:

```go
// The order of these guards is the grammar: an open editor swallows everything (so j/k type
// rather than navigate), then the expanded-help view, then an active filter (which swallows
// runes for the same reason), then the focused pane.
```

- [ ] **Step 5: Implement the search state machine**

Append to `ui/overlay/settings_nav.go`:

```go
// searching reports whether the `/` filter is active. The picker's own focus flag is the
// single source of truth, so there is no second bool to fall out of step with it.
func (s *SettingsOverlay) searching() bool { return s.search.IsFocused() }

// startSearch opens the filter and moves focus to the results (spec §8: `/` works from
// either pane). It always starts from an empty query — a `/` that resumed the last search
// would surprise a user who pressed it to look for something else.
//
// The picker's cursor is seeded FROM the current row, not the other way round. An empty
// query matches every row at score 0 (fuzzy.Match returns true for ""), so syncing in the
// usual direction would snap the cursor to row 0 and the rail to Sessions the instant `/`
// was pressed — before a character was typed — and Esc would then "return" you to the top
// of the schema rather than where you were. Seeding keeps both cursors in step, so
// contextLine's position readout is honest on the first frame too.
func (s *SettingsOverlay) startSearch() {
	s.search = newPicker(false)
	s.search.Focus()
	s.focus = focusRows
	s.lastErr = ""
	for i, row := range s.searchResults() {
		if row == s.cursor {
			s.search.cursor = i
			break
		}
	}
	s.syncCursorToSearch()
}

// clearSearch drops the filter, leaving the cursor on whatever row was highlighted and the
// rail already synced to its category — because syncCursorToSearch kept it there
// throughout. That is what makes Esc land you on the row you found rather than back where
// the search started.
func (s *SettingsOverlay) clearSearch() {
	s.search = newPicker(false) // a fresh sync picker is already blurred and unfiltered
	s.lastErr = ""
}

// searchHaystack is the text one row is matched against: its label, its key, its summary and
// its category name, so a user who remembers any one of them finds the row (spec §8).
func (s *SettingsOverlay) searchHaystack(r settingRow) string {
	return r.label + " " + r.key + " " + r.summary + " " + r.category.label()
}

// searchResults returns the indices of the rows matching the current filter, best first.
//
// The matcher is internal/fuzzy — the one subsequence matcher in the tree (#373) — and this
// is its ranking helper for settings rows, exactly as rankCandidates is the pickers' for
// paths. The bonus is the same idea as that function's basename bonus: a hit on the label or
// the key outranks an equal-scoring hit buried in a summary, because those are what a user
// types. An empty filter matches everything at score 0, so the list stays in schema order.
func (s *SettingsOverlay) searchResults() []int {
	q := s.search.filter
	type scored struct{ idx, score int }
	matches := make([]scored, 0, len(s.rows))
	for i, r := range s.rows {
		ok, score := fuzzy.Match(q, s.searchHaystack(r))
		if !ok {
			continue
		}
		if ok, bonus := fuzzy.Match(q, r.label); ok {
			score += bonus
		}
		if ok, bonus := fuzzy.Match(q, r.key); ok {
			score += bonus
		}
		matches = append(matches, scored{i, score})
	}
	sort.SliceStable(matches, func(a, b int) bool { return matches[a].score > matches[b].score })
	out := make([]int, len(matches))
	for i, m := range matches {
		out[i] = m.idx
	}
	return out
}

// syncCursorToSearch pulls the global row cursor — and with it the rail — onto the
// highlighted result.
//
// s.cursor stays the index into s.rows because isModified, inertReason, renderRowLine and
// expandedHelpContent all read it; the picker's cursor indexes the result list. Keeping the
// rail on the result's category is not decoration: the rail takes no keys while filtering,
// so its marker has to mean something else, and it is what lets clearSearch leave the user
// on the row they found.
//
// With no results the cursor is left where it was. It is still a valid row index — nothing
// renders it as selected, because the pane draws the no-match line instead.
func (s *SettingsOverlay) syncCursorToSearch() {
	results := s.searchResults()
	if len(results) == 0 {
		return
	}
	s.search.clampCursor(len(results))
	s.cursor = results[s.search.cursor]
	s.railCursor = railIndexForCategory(s.rows[s.cursor].category)
	s.lastErr = ""
}

// visibleRowIndices is the set of rows the rows pane is showing: the search results while a
// filter is active, else the current rail entry's own contiguous range. Every width and
// content decision that used to read rowRange directly goes through this, so the search
// inherits them instead of duplicating them.
func (s *SettingsOverlay) visibleRowIndices() []int {
	if s.searching() {
		return s.searchResults()
	}
	start, end := s.rowRange(s.selectedEntry())
	out := make([]int, 0, max(0, end-start))
	for i := start; i < end; i++ {
		out = append(out, i)
	}
	return out
}

// handleSearchKey routes a key while the `/` filter is active (spec §8).
//
// The division is: the filter owns every rune, space and backspace — j and k are letters in
// a search box, and r must not reset a row mid-query — while the value keys (↵, ←, →) still
// edit the highlighted row exactly as they do unfiltered, and ↑/↓ still move the result
// cursor. Space is the one casualty of that split: it extends the filter rather than
// toggling a bool, and ↵ is the toggle while filtering.
//
// `?` is the single rune the filter does not get, because spec §8 also assigns it to the
// expanded help. TestNoRowContainsAQuestionMark pins the premise that makes the reservation
// free.
//
// Keys the shared Picker does not consume — it takes only KeyUp, KeyDown, KeyBackspace,
// KeyRunes and KeySpace — fall through to nothing and are deliberately inert. One consequence
// worth knowing: bubbletea makes ctrl+h a distinct key type from backspace, so ctrl+h does
// not delete here even though the session-list filter treats it as backspace. That is the
// shared Picker's behavior across every picker in the tree, not this panel's, so it is left
// alone rather than special-cased in one place.
func (s *SettingsOverlay) handleSearchKey(msg tea.KeyMsg) (closed bool, changedKey string) {
	results := s.searchResults()
	switch msg.String() {
	case "esc", "ctrl+c":
		// Layer one of three: clear, back to the rail, close (spec §8).
		s.clearSearch()
		return false, ""
	case "tab", "shift+tab":
		// The rail is inert while a filter is active, so Tab cannot focus it with the filter
		// still applied — it is the two escs in one key.
		s.clearSearch()
		s.focus = focusRail
		return false, ""
	case "?":
		if len(results) > 0 {
			s.helpOpen, s.helpScroll = true, 0
		}
		return false, ""
	case "pgup", "pgdown", "home", "end":
		if len(results) > 0 {
			s.search.cursor = clamp(s.pagedSearchCursor(msg.String(), len(results)), 0, len(results)-1)
			s.syncCursorToSearch()
		}
		return false, ""
	}
	if len(results) > 0 {
		row := &s.rows[s.cursor]
		switch msg.String() {
		case "left":
			return false, s.cycleEnum(row, -1)
		case "right":
			return false, s.cycleEnum(row, +1)
		case "enter":
			switch row.kind {
			case kindBool:
				return false, s.toggleBool(row)
			case kindEnum:
				return false, s.cycleEnum(row, +1)
			case kindInt, kindText:
				s.startEdit(row)
			}
			return false, ""
		}
	}
	if consumed, filterChanged, cursorMoved := s.search.handleKey(msg, len(results)); consumed &&
		(filterChanged || cursorMoved) {
		s.syncCursorToSearch()
	}
	return false, ""
}

// pagedSearchCursor resolves a paging key to a result index, mirroring pagedCursor's rule
// for the unfiltered list so PgDn does not mean two different distances in one panel.
func (s *SettingsOverlay) pagedSearchCursor(key string, count int) int {
	page := max(1, s.paneHeight()-1) // overlap one row so context is never lost
	switch key {
	case "pgup":
		return s.search.cursor - page
	case "pgdown":
		return s.search.cursor + page
	case "home":
		return 0
	default: // "end"
		return count - 1
	}
}
```

Add `"sort"` and `"github.com/ZviBaratz/atrium/internal/fuzzy"` to `settings_nav.go`'s
imports.

- [ ] **Step 6: Bind `/` in both panes**

In `handleRailKey`'s switch, before `case "right", "tab", "enter":`:

```go
	case "/":
		s.startSearch()
```

and the same arm in `handleRowsKey`, next to `case "r":`.

- [ ] **Step 7: Point `visibleLabelWidth` at the visible set**

In `settings_render.go`:

```go
func (s *SettingsOverlay) visibleLabelWidth() int {
	w := 0
	for _, i := range s.visibleRowIndices() {
		if n := ansi.StringWidth(s.rows[i].label); n > w {
			w = n
		}
	}
	return w
}
```

This is what keeps `editorWidth()` and the label column honest while filtering, without
either of them learning about search.

- [ ] **Step 8: Run the tests**

```bash
PATH="/home/zvi/.local/share/mise/installs/go/1.26.4/bin:$PATH" \
  go test ./ui/overlay/ 2>&1 | tail -20
```
Expected: **the whole package green.** An earlier draft of this step said "some render tests
may fail here — that is Task 6", which authorizes committing past whatever the implementer
happens to see. Nothing should fail: no existing test presses `/`, and `visibleLabelWidth`'s
new `visibleRowIndices()` body is identical to the old one when `searching()` is false. If
something does fail, it is a regression in this task, not deferred work.

> `TestSearchFlattensAcrossCategories` and `TestArrowsMoveTheResultCursor` both need their
> query to return a specific shape of result. **Print the result set once while iterating**
> (`t.Log(resultKeys(o))`) and pick a query that satisfies the `require` preconditions,
> rather than trusting that "in" matches three rows across two categories. Six prescribed
> tests in PR B could not have passed as written for exactly this reason.

- [ ] **Step 9: Verify the guards fail when they should**

1. In `handleSearchKey`, move the `s.search.handleKey(...)` delegation **above** the
   `switch` (so the picker sees every key first). Expected:
   `TestQuestionMarkOpensHelpForTheHighlightedResult` FAILS — `?` lands in the filter.
   Restore.
2. In `searchResults`, delete both bonus blocks. Expected:
   `TestSearchRanksTheLabelAndKeyHitFirst` FAILS. **If it still passes, the query is not
   discriminating** — find one that is (print the ranked list) rather than deleting the
   test. Restore.
3. In `syncCursorToSearch`, delete the `s.railCursor = …` line. Expected:
   `TestTheRailFollowsTheHighlightedResult` and `TestEscIsThreeLayeredWithAFilter`'s
   category assertion both FAIL. Restore.
4. In `searchHaystack`, drop `r.summary`. Expected: the "by a word from the summary"
   subtest FAILS. Then drop `r.category.label()` instead: the "by category name" subtest
   FAILS. Restore both.
5. In `handleSearchKey`, remove the `"tab", "shift+tab"` arm. Expected:
   `TestTabLeavesTheSearchForTheRail` FAILS. Restore.
6. Re-run and confirm green.

- [ ] **Step 10: Lint and commit**

```bash
PATH="/home/zvi/.local/share/mise/installs/go/1.26.4/bin:$PATH" /home/zvi/go/bin/golangci-lint run ./ui/...
git status --short
git add -u ui/overlay
git commit -m "feat(settings): / filters every category through the shared picker"
```

---

## Task 6: `/` search — the renderer

**Files:**
- Modify: `ui/overlay/settings_render.go` (`railLines`, `rowsPaneContent`,
  `searchPaneContent`, `renderRowLine`, `badgeAvail`/`searchBadge`, `contextLine`,
  `helpLines`, `hintLine`)
- Modify: `ui/overlay/settings.go` (`Render`, `titleLine`)
- Modify: `ui/overlay/settings_render_test.go`, `ui/overlay/settings_test.go`

**Interfaces:**
- Consumes: Task 5's `searching`, `searchResults`, `visibleRowIndices`.
- Produces: `searchBadge(category string, avail int) string`,
  `badgeAvail(width, labelW int, value string) int`,
  `(*SettingsOverlay).railMatchCounts() []int`, `.searchPaneContent(width) []paneLine`,
  `.titleLine() string`.

**The category rides the badge column, and it degrades rather than dropping.** Spec §10's
priority is reused unchanged — the label never truncates, the badge column yields first —
but a search result's category is what tells two similarly-named rows apart, so like the
inert chip it shortens (`Worktrees &…`) instead of vanishing. It drops only below
`searchBadgeMinCells`.

**The inert chip still wins the column.** A row that does nothing right now has more urgent
news than which category it lives in — and `contextLine` names the category of the
highlighted row regardless, so the information is never lost, only deprioritised.

**The filter rides the title row.** PR B's invariant is that the box's height depends on the
terminal alone, so a centered `PlaceOverlay` never re-centers mid-navigation. A filter line
of its own would break that the moment `/` was pressed.

**Below 73 columns there is no rail, and therefore no match counts.** `bodyLines` shows the
rail **or** the rows in single-pane mode, never both, and `/` focuses the rows — so
`railLines` (with its counts and its dimming) is simply not drawn. That is the honest
contract, not an oversight: spec §8's "the rail dims and shows a per-category match count" is
a two-pane affordance, and the single-pane substitute is `contextLine`'s category prefix,
which names the highlighted result's category on every frame. `TestSearchCarriesTheCategoryBelowTheThreshold`
pins it, so the substitute is asserted rather than assumed. The rail is one `Tab` (or two
`Esc`s) away.

- [ ] **Step 1: Write the failing tests**

Append to `ui/overlay/settings_render_test.go`:

```go
// searchedPane renders the rows pane under a filter and returns its unstyled lines, which is
// the surface every assertion below is about.
func searchedPane(t *testing.T, o *SettingsOverlay, query string) []string {
	t.Helper()
	o.HandleKeyPress(keyRunes("/"))
	typeFilter(o, query)
	out := make([]string, 0, len(o.rowsPaneLines()))
	for _, l := range o.rowsPaneLines() {
		out = append(out, stripANSI(l))
	}
	return out
}

// TestSearchResultsShowTheirCategory is spec §8's "each hit's category shown on the row".
// A flat list drawn from ten categories is unreadable without it.
func TestSearchResultsShowTheirCategory(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	o.SetSize(100, 32)
	lines := searchedPane(t, o, "glyph")

	require.NotEmpty(t, lines)
	assert.Contains(t, strings.Join(lines, "\n"), "Appearance",
		"the Glyph set hit names the category it lives in")
}

// TestSearchResultCategoryDegradesRatherThanVanishing sweeps every two-pane width. The
// category is what tells two similarly-named results apart, so — like the inert chip, and
// unlike the timing badge — it shortens rather than dropping. PR B's review found three
// rendering bugs in geometry that single-width guards never visited; this is the search
// analogue of the sweep that replaced them.
//
// Three things this gets right that an obvious version does not:
//
//   - It asserts on the BADGE rowValueAndBadge returns, not on a substring of the rendered
//     line. `strings.Contains(line, "…")` also matches a truncated VALUE, so a line-level
//     check passes while the category is gone — measured, `"theme"` at width 73 drops the
//     badge and still shows an ellipsis from the value.
//   - It checks each row against ITS OWN category, not a literal. "base" is a subsequence
//     query and matches 13 rows across six categories (In-session status bar matches through
//     "…st-a-tus b-ar…"); an earlier draft asserted every line contained "Worktrees & git".
//   - It sweeps three queries. "base"'s non-inert hits are all bool rows with 6-cell values,
//     so the badge column is never squeezed and the sweep proves nothing on its own;
//     "config" and "session" each bring an enum and a long text row, which is where the
//     eviction fitValue exists to prevent actually happens.
//
// update_base_on_create is switched off so fast_forward_local_base is genuinely inert: under
// DefaultConfig it is active, the skip never fires, and the branch this test claims to cover
// is dead. require.NotZero is what keeps that honest.
func TestSearchResultCategoryDegradesRatherThanVanishing(t *testing.T) {
	skipped := 0
	for _, q := range []string{"base", "config", "session"} {
		for w := 73; w <= 120; w++ {
			cfg := config.DefaultConfig()
			off := false
			cfg.UpdateBaseOnCreate = &off
			o := NewSettingsOverlay(cfg)
			o.SetSize(w, 32)
			o.HandleKeyPress(keyRunes("/"))
			typeFilter(o, q)
			results := o.searchResults()
			require.NotEmptyf(t, results, "query %q, width %d: must match", q, w)

			labelW, paneW := o.visibleLabelWidth(), o.rowsPaneWidth()
			for _, i := range results {
				if o.inertReason(i) != "" {
					skipped++ // the inert chip legitimately takes the column instead
					continue
				}
				_, badge := o.rowValueAndBadge(i, paneW, labelW, "")
				cat := o.rows[i].category.label()
				require.NotEmptyf(t, badge, "query %q, width %d, row %q lost its category %q",
					q, w, o.rows[i].key, cat)
				assert.Truef(t, strings.HasPrefix(cat, strings.TrimSuffix(badge, "…")),
					"query %q, width %d, row %q: badge %q is not a shortening of %q",
					q, w, o.rows[i].key, badge, cat)
			}
		}
	}
	require.NotZero(t, skipped, "the inert branch must actually be exercised")
}

// TestInertChipBeatsTheCategoryOnASearchRow: a row that does nothing right now has more
// urgent news than which category it lives in. The category is not lost — contextLine names
// it for the highlighted row (TestContextLineNamesTheCategoryWhileSearching).
func TestInertChipBeatsTheCategoryOnASearchRow(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Notifications = "off"
	o := NewSettingsOverlay(cfg)
	o.SetSize(100, 32)
	o.HandleKeyPress(keyRunes("/"))
	typeFilter(o, "finished")

	i := o.searchResults()[0]
	require.Equal(t, "notifications_finished", o.rows[i].key)
	require.NotEmpty(t, o.inertReason(i), "precondition: the row is inert with notifications off")

	line := stripANSI(o.renderRowLine(i, o.rowsPaneWidth(), o.visibleLabelWidth()))
	assert.Contains(t, line, "needs Notifications", "the inert chip keeps the badge column")
}

// TestSearchRowLinesFillThePaneExactlyAndKeepTheirChip sweeps widths AND queries. A search
// row carries the widest content the panel ever composes — the label column is the widest
// MATCHING label, and the badge is a category name rather than a five-cell timing word.
//
// It asserts EQUALITY, not "≤". composeRowLine bounds its own output by construction (both
// branches set gap = avail − …, and an over-wide badge is dropped rather than overflowing),
// so a "≤ paneW" assertion is a tautology no bug in searchBadge or valueCell can trip. What
// can actually break is the gap arithmetic — a short line leaves the badge un-right-aligned
// — and the chip being evicted, which is the presence half below.
func TestSearchRowLinesFillThePaneExactlyAndKeepTheirChip(t *testing.T) {
	for _, q := range []string{"", "e", "s", "session", "base", "notif", "zzz"} {
		for w := 40; w <= 120; w += 7 {
			o := NewSettingsOverlay(config.DefaultConfig())
			o.SetSize(w, 32)
			o.HandleKeyPress(keyRunes("/"))
			typeFilter(o, q)
			labelW, paneW := o.visibleLabelWidth(), o.rowsPaneWidth()
			for _, i := range o.searchResults() {
				line := stripANSI(o.renderRowLine(i, paneW, labelW))
				if rowMarkerCells+labelW+rowLabelGap+1 > paneW {
					// Below the floor the label rule yields and the head is truncated; parity
					// with the pre-PR-B renderer, and composeRowLine's documented branch.
					assert.LessOrEqualf(t, ansi.StringWidth(line), paneW,
						"query %q, width %d, row %q overflows even truncated", q, w, o.rows[i].key)
					continue
				}
				assert.Equalf(t, paneW, ansi.StringWidth(line),
					"query %q, width %d, row %q does not fill its pane: %q", q, w, o.rows[i].key, line)
				if w >= 73 && o.inertReason(i) == "" {
					_, badge := o.rowValueAndBadge(i, paneW, labelW, "")
					assert.NotEmptyf(t, badge,
						"query %q, width %d, row %q lost its category chip", q, w, o.rows[i].key)
				}
			}
		}
	}
}

// TestRailShowsPerCategoryMatchCounts is spec §8's rail behavior. The counts are the only
// orientation left once the pane goes flat: they say where the hits are without the user
// clearing the filter to find out.
func TestRailShowsPerCategoryMatchCounts(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	o.SetSize(100, 32)
	o.HandleKeyPress(keyRunes("/"))
	typeFilter(o, "notif")

	counts := o.railMatchCounts()
	require.Len(t, counts, len(railEntries()))
	for i, e := range railEntries() {
		if e.kind == railCategory && e.category == catNotifications {
			assert.Greaterf(t, counts[i], 0, "the Notifications category must report its hits")
		}
		if e.kind != railCategory {
			assert.Equalf(t, 0, counts[i], "%q is not a category and reports no count", e.label)
		}
	}

	// The rendered line must CARRY the count. Asserting the rail merely contains
	// "Notifications" would hold with or without the feature — the rail renders all thirteen
	// labels unconditionally, filtered or not.
	var line string
	for _, l := range o.railLines() {
		if strings.Contains(stripANSI(l), "Notifications") {
			line = strings.TrimRight(stripANSI(l), " ")
		}
	}
	require.NotEmpty(t, line, "the Notifications entry must be on screen")
	want := strconv.Itoa(counts[railIndexForCategory(catNotifications)])
	require.NotEqual(t, "0", want, "precondition: the query must hit this category")
	assert.True(t, strings.HasSuffix(line, want),
		"the rail entry must end in its match count %s, got %q", want, line)
}

// TestRailCountsSumToTheResultCount pins that the rail is a read-out of the result list
// rather than a second query — the failure mode where the pane and the rail disagree about
// what matched.
func TestRailCountsSumToTheResultCount(t *testing.T) {
	for _, q := range []string{"", "e", "session", "base", "zzz"} {
		o := NewSettingsOverlay(config.DefaultConfig())
		o.HandleKeyPress(keyRunes("/"))
		typeFilter(o, q)
		total := 0
		for _, n := range o.railMatchCounts() {
			total += n
		}
		assert.Equalf(t, len(o.searchResults()), total,
			"query %q: the rail counts must account for every result", q)
	}
}

// TestEveryCategoryMatchCountFitsTheRailsOneCell is the premise the single-cell count rests
// on: the largest category has six rows (measured), so a count is always one digit and fits
// the trail cell the handoff arrow otherwise occupies — which is why railWidth() does not
// move when / is pressed. An eleventh category of ten rows would break the rail silently;
// this fails first and forces the decision.
//
// It is a premise guard, not a render assertion: TestRailShowsPerCategoryMatchCounts is what
// checks a rendered line carries its count.
func TestEveryCategoryMatchCountFitsTheRailsOneCell(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	require.Equal(t, 1, railTrailCells-1, "the trail is one glyph after its leading space")
	for _, c := range allCategories() {
		start, end := o.rowRange(railEntries()[railIndexForCategory(c)])
		assert.LessOrEqualf(t, end-start, 9,
			"category %q has %d rows; a match count no longer fits the rail's one cell",
			c.label(), end-start)
	}
}

// TestRailIsInertWhileFiltering: the rail takes no keys under a filter, so nothing on it may
// look like the focused pane. Its only bright marker is the highlighted result's category.
func TestRailIsInertWhileFiltering(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	o.SetSize(100, 32)
	before := o.railLines()

	o.HandleKeyPress(keyRunes("/"))
	typeFilter(o, "glyph")
	after := o.railLines()

	require.Len(t, after, len(before))
	assert.NotEqual(t, before, after, "the rail must visibly change when a filter is active")
	for i, l := range after {
		assert.LessOrEqualf(t, ansi.StringWidth(stripANSI(l)), railWidth(),
			"rail line %d overflows while filtering", i)
	}
}

// TestNoMatchesSaysSoAndSaysHowToRecover: an empty pane reads as a broken panel. The prose
// names the two keys out of it, which is the same obligation the handoff note carries.
func TestNoMatchesSaysSoAndSaysHowToRecover(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	o.SetSize(100, 32)
	lines := searchedPane(t, o, "zzzz")
	require.Empty(t, o.searchResults())

	pane := strings.Join(lines, "\n")
	assert.Contains(t, pane, "No setting matches")

	help := stripANSI(strings.Join(o.helpLines(), "\n"))
	assert.Contains(t, help, "backspace")
	assert.Contains(t, help, "esc")
	assert.NotContains(t, help, o.selectedRow().summary,
		"with nothing matching, the help must not describe a row the list is not showing")
}

// TestSearchCarriesTheCategoryBelowTheThreshold sweeps the widths the other search guards
// cannot reach. Below 73 columns the panel is single-pane: `/` focuses the rows, so the rail
// — and with it every match count — is not drawn at all, and the badge column is squeezed
// hard enough that the category chip genuinely does get dropped (measured: everywhere below
// about 50 columns). contextLine's category prefix is the whole orientation down there, so it
// is what this asserts.
func TestSearchCarriesTheCategoryBelowTheThreshold(t *testing.T) {
	for w := 40; w < 73; w++ {
		o := NewSettingsOverlay(config.DefaultConfig())
		o.SetSize(w, 32)
		o.HandleKeyPress(keyRunes("/"))
		typeFilter(o, "config")
		require.NotEmptyf(t, o.searchResults(), "width %d: the query must match", w)
		require.Falsef(t, o.twoPane(), "width %d must be below the two-pane threshold", w)

		ctx := stripANSI(o.contextLine(o.innerWidth()))
		cat := o.selectedRow().category.label()
		assert.Truef(t, strings.Contains(ctx, cat) || strings.Contains(ctx, "…"),
			"width %d: the context line must still name %q: %q", w, cat, ctx)
		assert.LessOrEqualf(t, ansi.StringWidth(ctx), o.innerWidth(), "width %d: %q", w, ctx)
	}
}

// TestContextLineNamesTheCategoryWhileSearching closes the gap the badge column leaves: the
// highlighted result always says where it lives, even when the badge went to an inert chip
// or degraded to an ellipsis.
func TestContextLineNamesTheCategoryWhileSearching(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Notifications = "off"
	o := NewSettingsOverlay(cfg)
	o.SetSize(100, 32)
	o.HandleKeyPress(keyRunes("/"))
	typeFilter(o, "finished")
	require.Equal(t, "notifications_finished", o.selectedRow().key)

	ctx := stripANSI(o.contextLine(o.innerWidth()))
	assert.Contains(t, ctx, "Notifications", "the category of the highlighted result")
	assert.Contains(t, ctx, "1/", "the position readout counts results, not category rows")
}

// TestPositionReadoutCountsResultsWhileSearching: the counter is the pane's orientation, and
// under a filter "3/5" must mean the third of five hits, not the third of a category.
func TestPositionReadoutCountsResultsWhileSearching(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	o.SetSize(100, 32)
	o.HandleKeyPress(keyRunes("/"))
	typeFilter(o, "session")
	n := len(o.searchResults())
	require.Greater(t, n, 1)

	assert.Contains(t, stripANSI(o.contextLine(o.innerWidth())), fmt.Sprintf("1/%d", n))
	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyDown})
	assert.Contains(t, stripANSI(o.contextLine(o.innerWidth())), fmt.Sprintf("2/%d", n))
}

// TestFilterRidesTheTitleRow: the box's height depends on the terminal alone (PR B), so the
// filter must not claim a line of its own — pressing / would otherwise re-centre the whole
// panel under the user.
func TestFilterRidesTheTitleRow(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	o.SetSize(100, 32)
	before := lipgloss.Height(o.Render())

	o.HandleKeyPress(keyRunes("/"))
	typeFilter(o, "theme")
	assert.Equal(t, before, lipgloss.Height(o.Render()), "opening the filter must not resize the box")

	title := stripANSI(o.titleLine())
	assert.Contains(t, title, "Settings")
	assert.Contains(t, title, "/theme")
	assert.LessOrEqual(t, ansi.StringWidth(title), o.innerWidth())
}

// TestTitleRowSurvivesAnOverlongFilter: the filter is user input with no length bound, and a
// title line wider than the box soft-wraps, grows the box a row and clips the pinned hint.
func TestTitleRowSurvivesAnOverlongFilter(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	o.SetSize(80, 24)
	before := lipgloss.Height(o.Render())
	o.HandleKeyPress(keyRunes("/"))
	typeFilter(o, strings.Repeat("x", 200))

	assert.LessOrEqual(t, ansi.StringWidth(stripANSI(o.titleLine())), o.innerWidth())
	assert.Equal(t, before, lipgloss.Height(o.Render()))
}

// TestHintAdvertisesSearchAndReset: PR B deliberately shipped neither, because a hint for a
// dead key is worse than no hint. Both are live now, so both must be advertised — and the
// filtered hint must name the esc level that applies (spec §15).
func TestHintAdvertisesSearchAndReset(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	o.SetSize(100, 32)
	assert.Contains(t, stripANSI(o.hintLine()), "/ search", "the rail advertises search")

	settingsAt(t, o, "theme")
	rows := stripANSI(o.hintLine())
	assert.Contains(t, rows, "r reset")
	assert.Contains(t, rows, "/ search")
	assert.Contains(t, rows, "esc back")

	o.HandleKeyPress(keyRunes("/"))
	filtered := stripANSI(o.hintLine())
	assert.Contains(t, filtered, "esc clear", "the filter's own esc level")
	assert.NotContains(t, filtered, "esc back")
}

// TestHintFitsEveryWidth sweeps the ladder rather than sampling it. It asserts the absence
// of an ellipsis, not a width bound: hintLine ends in ansi.Truncate(…, inner, "…"), so a
// width assertion can never fail — what actually breaks is a shortest rung that does not fit
// and gets clipped to something that says nothing. The esc level is the hint a user stuck in
// the panel needs most, so it is asserted separately.
func TestHintFitsEveryWidth(t *testing.T) {
	for w := 40; w <= 120; w++ {
		o := NewSettingsOverlay(config.DefaultConfig())
		o.SetSize(w, 32)
		// One overlay walked through three states in order: rail → rows → search.
		for _, state := range []struct {
			name string
			key  tea.KeyMsg
		}{
			{"rail", tea.KeyMsg{}},
			{"rows", tea.KeyMsg{Type: tea.KeyRight}},
			{"search", keyRunes("/")},
		} {
			if state.name != "rail" {
				o.HandleKeyPress(state.key)
			}
			hint := stripANSI(o.hintLine())
			assert.NotContainsf(t, hint, "…", "width %d, %s: the ladder has no rung that fits: %q",
				w, state.name, hint)
			assert.Containsf(t, hint, "esc", "width %d, %s: the esc level must survive: %q",
				w, state.name, hint)
		}
	}
}
```

Extend PR B's existing sweeps rather than duplicating them: in
`TestNoPaneLineOverflowsItsWidth` and `TestNoBodyLineOverflowsTheInnerWidth`, add a
filtered variant of each sampled size (press `/`, type `"e"`, re-assert). Note in a comment
that the filtered pass is what covers the widest lines the panel can compose.

`settings_render_test.go` already imports `fmt`, `strings`, `lipgloss` and `ansi`; only
`strconv` is new, and `goimports` via `mise exec -- just fmt` adds it.

- [ ] **Step 2: Run to verify they fail**

```bash
PATH="/home/zvi/.local/share/mise/installs/go/1.26.4/bin:$PATH" \
  go test ./ui/overlay/ -run 'TestSearchResults|TestSearchRow|TestInertChipBeats|TestRail|TestNoMatches|TestContextLineNames|TestPositionReadoutCounts|TestFilterRides|TestTitleRow|TestHint|TestEveryCategoryMatch' 2>&1 | head -20
```
Expected: FAIL to build — `undefined: railMatchCounts`, `o.titleLine undefined`.

- [ ] **Step 3: The badge helpers**

In `settings_render.go`, next to `fitBadge`, extract the shared arithmetic and add the
degrading category chip:

```go
// badgeAvail is the room left for the right-aligned badge column once the head and the value
// have taken theirs, including the one space that separates them. One definition, so
// fitBadge and searchBadge cannot disagree about how much room there is.
func badgeAvail(width, labelW int, value string) int {
	return width - rowMarkerCells - labelW - rowLabelGap - ansi.StringWidth(value) - 1
}

// searchBadgeMinCells is the floor a category chip degrades to before it is dropped: three
// cells of name plus the ellipsis. Below that the chip carries no information a user could
// act on, and the column is better spent on the value.
const searchBadgeMinCells = 4

// searchBadge is the category chip on a search result row.
//
// Unlike a timing badge — reference information, dropped outright by spec §10 — the category
// is what tells two similarly-named results apart in a list flattened across ten categories,
// so it degrades the way an inert chip does: "Worktr…" still does the job a blank column
// cannot. fitValue is what makes that promise keepable; see its comment.
func searchBadge(category string, avail int) string {
	if avail < searchBadgeMinCells {
		return ""
	}
	if ansi.StringWidth(category) <= avail {
		return category
	}
	return ansi.Truncate(category, avail, "…")
}

// valueAvail is the room the value column has before any badge is considered.
func valueAvail(width, labelW int) int { return width - rowMarkerCells - labelW - rowLabelGap }

// fitValue shortens a value so a badge of `reserve` cells survives beside it — unless doing
// so would leave the value less than rowMinValueCells, below which the value is the more
// useful of the two and the badge is dropped instead.
//
// It exists because valueCell's own reservation bites ONLY on kindEnum: kindBool, kindInt,
// kindText and kindReadOnly return their value bare and ignore the badge argument entirely,
// so a long path or a long command evicts the chip beside it. Measured on the real schema,
// that drops "Advanced" from the Config file row at EVERY terminal width, and the category
// from Carry files up to 90 columns.
//
// That eviction is correct for a timing badge — spec §10 drops it first — and wrong for the
// two badges that must not vanish: an inert reason chip (a dimmed row with no marker reads as
// broken) and a search result's category. So only those two callers reserve. Nothing is lost
// either way: contextLine renders a truncated value in full (spec §10).
func fitValue(v string, avail, reserve int) string {
	budget := avail - reserve - 1
	if budget < rowMinValueCells || ansi.StringWidth(v) <= budget {
		return v
	}
	return ansi.Truncate(v, budget, "…")
}
```

and rewrite `fitBadge`'s first line as `avail := badgeAvail(width, labelW, value)`.

> **`fitValue`'s inert caller is a PR B bug fix, not new behavior.** PR B's "chips degrade
> rather than vanish" rule has the same hole today: `notify_command` is a `kindText` row that
> goes inert whenever Notifications is not `desktop`, and a long enough command evicts its
> `needs desktop mode` chip. Fixing it here is what makes the search category's promise
> keepable by the same mechanism rather than a second one.

- [ ] **Step 4: Give `renderRowLine` its three badge modes**

Replace the badge block inside `renderRowLine` (the `inert`/`candidates`/`value`/`badge`
lines) with a call to one helper, so the three-way choice reads as a choice:

```go
	inert := s.inertReason(i)
	value, badge := s.rowValueAndBadge(i, width, labelW, inert)
	p := composeRowLine(width, labelW, sel, modified, row.label, value, badge)
```

and add:

```go
// rowValueAndBadge sizes a row's value cell and picks its right-aligned badge. There are
// three claims on that column, in priority order:
//
//   - an inert reason chip, which degrades to one word but never drops: a dimmed row with no
//     marker reads as broken, and the help pane only describes the SELECTED row;
//   - a search result's category, which degrades by truncation for the same reason — a flat
//     list drawn from ten categories needs it — and is why the timing badge yields while a
//     filter is active;
//   - the apply timing, which is reference information and is dropped outright (spec §10).
//
// Either way the value is sized against the SHORTEST form the badge can take, never the
// widest: reserving against the widest lets a rich enum value find no room beside a long
// chip, fall back to the whole slack, and evict the very chip the ladder was supposed to
// guarantee (PR B, Task 7).
func (s *SettingsOverlay) rowValueAndBadge(i, width, labelW int, inert string) (value, badge string) {
	avail := valueAvail(width, labelW)
	switch {
	case inert != "":
		candidates := inertBadgeCandidates(inert)
		shortest := candidates[len(candidates)-1]
		value = fitValue(s.valueCell(i, width, labelW, shortest), avail, ansi.StringWidth(shortest))
		return value, fitBadge(candidates, width, labelW, value)
	case s.searching():
		value = fitValue(
			s.valueCell(i, width, labelW, strings.Repeat("x", searchBadgeMinCells)),
			avail, searchBadgeMinCells)
		return value, searchBadge(s.rows[i].category.label(), badgeAvail(width, labelW, value))
	default:
		// No fitValue here, deliberately: spec §10 drops a timing badge before touching the
		// value, and a timing badge is reference information that loses nothing by going.
		candidates := []string{s.rows[i].timing.badge()}
		value = s.valueCell(i, width, labelW, candidates[0])
		return value, fitBadge(candidates, width, labelW, value)
	}
}
```

> The `strings.Repeat("x", searchBadgeMinCells)` stand-in is deliberate: `valueCell` only
> reads the reservation's *width*, and the floor is a width, not a string. Keep the comment
> that says so.

- [ ] **Step 5: The rail's counts and dimming**

Add to `settings_render.go`:

```go
// railMatchCounts is the per-entry match count the rail shows while a filter is active
// (spec §8), or nil when there is none.
//
// It is a read-out of searchResults rather than a second query — a rail that counted matches
// its own way could disagree with the pane about what matched, which is the bug
// TestRailCountsSumToTheResultCount exists to prevent. Only real categories are counted: All
// settings is a view whose count is the total (which the pane already shows), and a handoff
// owns no rows.
func (s *SettingsOverlay) railMatchCounts() []int {
	if !s.searching() {
		return nil
	}
	byCategory := make(map[settingCategory]int, len(allCategories()))
	for _, i := range s.searchResults() {
		byCategory[s.rows[i].category]++
	}
	counts := make([]int, len(railEntries()))
	for i, e := range railEntries() {
		if e.kind == railCategory {
			counts[i] = byCategory[e.category]
		}
	}
	return counts
}
```

and in `railLines`, replace the `trail` and `style` selection:

```go
	counts := s.railMatchCounts()
	...
		trail := " "
		switch {
		case counts != nil && counts[i] > 0:
			// One digit always: the largest category has six rows
			// (TestEveryCategoryMatchCountFitsTheRail), so the count fits the cell the handoff
			// arrow otherwise occupies and the rail's width does not move when / is pressed.
			trail = strconv.Itoa(counts[i])
		case e.kind == railHandoff:
			trail = t.Glyphs.Handoff
		}
		line := mark + " " + padRight(e.label, labelW) + " " + trail

		style := t.DimStyle()
		switch {
		case i == s.railCursor && s.focus == focusRail:
			// searching() cannot hold here: startSearch always sets focusRows, and every exit
			// from a filter clears it first.
			style = t.AccentStyle()
		case i == s.railCursor:
			// Current but not taking keys — including throughout a search, where the marker
			// tracks the highlighted result's category rather than a cursor the user can move.
			style = t.FgStyle()
		case s.searching(), e.kind == railHandoff:
			// The rail is inert under a filter (spec §8: "the rail dims"), and a handoff entry
			// is dimmer than an ordinary one at all times.
			style = t.FaintStyle()
		}
```

Add `"strconv"` to the imports.

- [ ] **Step 6: The results pane**

In `rowsPaneContent`, before the handoff branch:

```go
	if s.searching() {
		return s.searchPaneContent(width)
	}
```

and add:

```go
// searchPaneContent is the rows pane under a filter: a flat list of every match, in rank
// order, each carrying its category (spec §8). It ignores the rail entry entirely — a filter
// that only searched the current category would be a category filter, not a search.
func (s *SettingsOverlay) searchPaneContent(width int) []paneLine {
	results := s.searchResults()
	if len(results) == 0 {
		// An empty pane reads as a broken panel. Naming the query and the two keys out of it
		// is the same obligation a handoff entry's note carries.
		style := theme.Current().FaintStyle()
		text := "No setting matches " + strconv.Quote(s.search.filter) + "."
		var lines []paneLine
		for _, l := range strings.Split(ansi.Wrap(text, width, ""), "\n") {
			lines = append(lines, paneLine{text: style.Render(l), rowIdx: -1})
		}
		return lines
	}
	labelW := s.visibleLabelWidth()
	lines := make([]paneLine, 0, len(results))
	for _, i := range results {
		lines = append(lines, paneLine{text: s.renderRowLine(i, width, labelW), rowIdx: i})
	}
	return lines
}
```

- [ ] **Step 7: The help pane and the context line**

In `helpLines`, after the handoff clause:

```go
	if s.searching() && len(s.searchResults()) == 0 {
		// With nothing matching, describing s.cursor's row would describe a row the list is
		// not showing. Say what happened and name the way out instead.
		prose = "Nothing matches — backspace to widen the search, esc to clear it."
	}
```

and make the `ctx` guard:

```go
	if s.lastErr == "" && s.selectedEntry().kind != railHandoff &&
		(!s.searching() || len(s.searchResults()) > 0) {
```

**Write it in that form, not as `!(A && B)`.** `staticcheck`'s QF1001 ("could apply De
Morgan's law") fires on the negated conjunction, and `.golangci.yml` excludes only QF1003.
`go build`, `go vet`, `gofmt` and the whole suite stay green while `lint` fails — exactly
the class CLAUDE.md warns about, and it was verified by building the negated form.

In `contextLine`, replace the position derivation and prefix the body:

```go
	var pos string
	if s.searching() {
		results := s.searchResults()
		if len(results) == 0 {
			return ""
		}
		// Under a filter the counter counts RESULTS: "3/5" must mean the third of five hits,
		// not the third row of whatever category the rail happens to mark.
		pos = fmt.Sprintf("%d/%d", s.search.cursor+1, len(results))
	} else {
		start, end := s.rowRange(s.selectedEntry())
		if end <= start {
			return ""
		}
		pos = fmt.Sprintf("%d/%d", s.cursor-start+1, end-start)
	}
```

and after `body` is chosen:

```go
	if s.searching() {
		// The badge column carries the category only when the row is live and the pane is wide
		// enough for it. Naming it here means the highlighted result ALWAYS says where it
		// lives, which is what keeps a flat cross-category list navigable.
		category := s.selectedRow().category.label()
		if body == "" {
			body = category
		} else {
			body = category + " · " + body
		}
	}
```

- [ ] **Step 8: The hint ladders**

In `hintLine`, add the search case before `case s.focus == focusRows:` and extend the rows
ladder:

```go
	case s.searching():
		// The filter's own esc level, the third of three (spec §8: clear, back, close). "type
		// to filter" is the affordance, since nothing else on screen says the runes are going
		// somewhere.
		ladder = []string{
			"type to filter · ↑/↓ move · ↵ edit · ? more · esc clear",
			"↑/↓ move · ↵ edit · ? more · esc clear",
			"↑/↓ · ↵ edit · esc clear",
			"esc clear",
		}
	case s.focus == focusRows:
		ladder = []string{
			"↑/↓ move · ←/→ change · ↵ edit · r reset · / search · ? more · ⇥ pane · esc back",
			"↑/↓ · ←/→ · ↵ edit · r reset · / search · ? more · esc back",
			"↵ edit · r reset · / search · esc back",
			"/ search · esc back",
			"esc back",
		}
```

and delete the doc comment's last paragraph ("It deliberately does not advertise `/` or
`r`…"), replacing it with:

```go
// Both `/` and `r` are advertised now that PR C makes them live; PR B's comment saying it
// deliberately does not is the thing this replaces.
```

- [ ] **Step 9: The title row**

In `settings.go`, add:

```go
// titleLine is the panel's first row: its name, plus the active filter.
//
// The filter rides this row rather than claiming one of its own because the box's height
// depends only on the terminal size — a taller box on `/` would re-centre the whole panel
// under the user mid-keystroke (the jump ui/overlay/textInput_size.go:3-8 warns about). The
// query is truncated to the inner width for the same reason: an over-wide line soft-wraps,
// grows the box a row, and clips the pinned hint off the bottom.
func (s *SettingsOverlay) titleLine() string {
	t := theme.Current()
	if !s.searching() {
		return t.OverlayTitleStyle().Render("Settings")
	}
	const gap = "   "
	head := "Settings" + gap
	filter := "/" + s.search.filter + t.Glyphs.TextCursor
	if budget := s.innerWidth() - ansi.StringWidth(head); ansi.StringWidth(filter) > budget {
		filter = ansi.Truncate(filter, max(0, budget), "…")
	}
	return t.OverlayTitleStyle().Render("Settings") + gap + overlayFilterStyle().Render(filter)
}
```

and in `Render`, replace the `title := …` line with `title := s.titleLine()`. Add
`"github.com/charmbracelet/x/ansi"` to `settings.go`'s imports.

- [ ] **Step 10: Run everything**

```bash
PATH="/home/zvi/.local/share/mise/installs/go/1.26.4/bin:$PATH" \
  go test ./ui/overlay/ 2>&1 | tail -20
```
Expected: PASS, with **no existing test needing a change**. This was verified by applying
both new ladders to a copy of the tree and running the package: `TestSettingsOverlay_RenderSmoke`
asserts only the `"esc close"` / `"esc back"` substrings, which both ladders preserve, and no
test in the tree pins a full ladder string. The one existing overlay test this PR does break
is `TestRightFocusesTheRowsPaneFromTheRail`, which Task 4 Step 1 already replaces. **If
something else fails here, read it — it is a real regression, not churn.**

- [ ] **Step 11: Verify the guards fail when they should**

1. In `searchBadge`, `return ""` instead of truncating. Expected:
   `TestSearchResultCategoryDegradesRatherThanVanishing` FAILS at widths 73–78, where
   `Worktrees & git` is the category being shortened.
2. Delete the `fitValue` call from `rowValueAndBadge`'s **search** arm. Expected: the same
   sweep FAILS on `config_file` (which loses its category at *every* width without it) and on
   `carry_files` up to 90 columns. This is the mutation that matters — the version of this
   plan before review claimed `valueCell`'s own reservation covered these rows, and it does
   not: `valueCell` reads its `badge` argument only in the `kindEnum` branch. Restore.
3. Delete the `fitValue` call from the **inert** arm. Expected: a failure on a long
   `notify_command` — if nothing fails, construct one (`cfg.NotifyCommand` to a 60-character
   command, notifications not `desktop`) and confirm the chip is evicted without it. **Report
   the result either way**; this arm is a PR B bug fix and the PR body claims it.
4. In `railMatchCounts`, count over `s.rows` instead of `s.searchResults()`. Expected:
   `TestRailCountsSumToTheResultCount` FAILS **and** `TestRailShowsPerCategoryMatchCounts`
   FAILS on the rendered line's suffix. Restore.
5. In `contextLine`, keep the unfiltered position derivation under a filter. Expected:
   `TestPositionReadoutCountsResultsWhileSearching` FAILS. Restore.
6. In `titleLine`, drop the truncation. Expected: `TestTitleRowSurvivesAnOverlongFilter`
   FAILS on the box height. Restore.
7. Remove `"r reset"` from the rows ladder's first two rungs. Expected:
   `TestHintAdvertisesSearchAndReset` FAILS. Restore.
8. Shorten the rows ladder's last rung to something wider than the narrowest inner width
   (e.g. `"esc go back to the rail"`, 23 cells, against inner 34 at width 40 — pick one that
   does not fit). Expected: `TestHintFitsEveryWidth` FAILS on the ellipsis assertion, which
   is the half that replaced a width bound `ansi.Truncate` made unfalsifiable. Restore.
9. Re-run and confirm green.

- [ ] **Step 12: Lint and commit**

```bash
PATH="/home/zvi/.local/share/mise/installs/go/1.26.4/bin:$PATH" /home/zvi/go/bin/golangci-lint run ./ui/...
git status --short
git add -u ui/overlay
git commit -m "feat(settings): render the search results, rail counts and filter row"
```

---

## Task 7: The deep links — two real call sites

**Files:**
- Modify: `app/app.go` (two fields), `app/app_feedback.go` (`settingNotice`),
  `app/app_update.go` (the notices, `openSettingsAt`), `app/app_keys.go`
  (`handleConfirmState`), `app/session_cap.go` (`overCapMessage`, `confirmOverCap`)
- Modify: `app/dialog_voice_test.go`, `app/host_cap_test.go`,
  `app/reorder_filter_keys_test.go`, `app/settings_test.go`

**Interfaces:**
- Consumes: `overlay.SettingsOverlay.OpenAt`, `(*home).openSettings` (Task 4).
- Produces: `(*home).openSettingsAt(key string) tea.Cmd`,
  `(*home).settingNotice(text, key string) tea.Cmd`, `home.noticeSettingKey`,
  `home.pendingConfirmSettingKey`.

Both call sites reuse the key the app already teaches — `,` — rather than inventing one.
The dialog and the notices already say "press `,`"; the only change is **where `,` lands**.

`overCapMessage`'s tail is the obligation spec §12 attaches to this: both branches end with
`(set max_sessions in config.json to change this)`, which is 5 rendered lines at the
dialog's 46-cell wrap and sends the user to a text editor for a setting this panel owns.
The replacement is one line, so the dialog gets *shorter*.

- [ ] **Step 1: Write the failing tests**

Replace `TestOverCapMessage` in `app/dialog_voice_test.go`:

```go
// The over-cap create confirmation states the capacity, the consequence, and the escape
// hatch, in that order — the one dialog that leads with facts rather than the verb (#399
// left it as it was). Its tail used to send the user to a text editor; since PR C the panel
// owns max_sessions and ',' opens the dialog straight onto that row.
func TestOverCapMessage(t *testing.T) {
	require.Equal(t,
		"Host capacity is 2, with 1 already running.\n"+
			"Another will queue, not parallelize, and drive up load.\n"+
			"Create it anyway? (, to change the limit)",
		overCapMessage(2, 1, 1))
	require.Equal(t,
		"Host capacity is 2, with 1 already running.\n"+
			"Spawning 3 more will queue, not parallelize, and drive up load.\n"+
			"Create them anyway? (, to change the limit)",
		overCapMessage(2, 1, 3))
}

// The dialog wraps at 46 cells, so a message is priced in RENDERED lines, not characters.
// Teaching the key costs less than pointing at config.json did: the old tail wrapped onto a
// second line and the new one does not.
func TestOverCapMessageIsShorterThanThePathItReplaced(t *testing.T) {
	// confirmWidth's preferred 50 less Padding(1,2)'s four cells. Stated as a literal because
	// the dialog is only this wide on a terminal that can afford it; a narrower terminal wraps
	// harder, and this is the case the wording was chosen against.
	const wrap = 46
	for _, m := range []string{overCapMessage(2, 1, 1), overCapMessage(2, 1, 3)} {
		lines := 0
		for _, para := range strings.Split(m, "\n") {
			lines += len(strings.Split(xansi.Wrap(para, wrap, ""), "\n"))
		}
		assert.LessOrEqualf(t, lines, 4, "the confirmation must fit four rendered lines: %q", m)
	}
}
```

Append to `app/host_cap_test.go`:

```go
// The over-cap dialog's ',' is a real deep link: it cancels the create, opens the settings
// panel, and lands on the Session limit row — the setting the message just named. Without
// the landing it would be a shortcut to a 13-entry rail, which is what the old
// "set max_sessions in config.json" tail already amounted to.
func TestOverCapDialogCommaOpensTheSessionLimit(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	h := newFanOutHome(t, gitInitRepo(t))
	h.hostCap = 2
	h.appConfig.MaxSessions = nil
	addStubInstances(t, h, 2)
	before := h.list.NumInstances()

	typeString(h, "race")
	ctrlS(h)
	require.Equal(t, stateConfirm, h.state)

	h.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(",")})

	assert.Equal(t, stateSettings, h.state, "',' leaves the dialog for the settings panel")
	require.NotNil(t, h.settingsOverlay)
	assert.Equal(t, "max_sessions", h.settingsOverlay.SelectedRowKey(),
		"and lands on the row the message named")
	assert.Nil(t, h.confirmationOverlay, "the dialog is dismissed")
	assert.Equal(t, before, h.list.NumInstances(), "',' spawns nothing — it is a cancel")
}

// ',' is armed by the cap dialog and nothing else: an unrelated confirmation must not have a
// key that silently cancels it and opens a panel.
func TestCommaIsInertInAnUnarmedConfirmation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	h := newFanOutHome(t, gitInitRepo(t))
	h.confirmAction("Do the thing?", func() tea.Msg { return nil })
	require.Equal(t, stateConfirm, h.state)

	h.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(",")})
	assert.Equal(t, stateConfirm, h.state, "an unarmed dialog ignores ','")
	assert.NotNil(t, h.confirmationOverlay)
}
```

Append to `app/reorder_filter_keys_test.go`:

```go
// The refusal notices that advertise ',' land on the setting they are about. Advertising a
// key that opens a 13-entry rail is barely better than not advertising it — the notice knows
// exactly which row it means.
func TestReorderNoticesDeepLinkToTheirSetting(t *testing.T) {
	cases := []struct {
		name, want string
		setup      func(h *home)
		key        rune
	}{
		{
			name: "status sort refusal", want: "session_sort", key: 'J',
			setup: func(h *home) { h.list.SetSortMode("status") },
		},
		{
			name: "cluster reorder refusal", want: "group_mode", key: '[',
			setup: func(h *home) { h.list.SetGroupMode("repo") },
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := filterReorderHome(t,
				[3]string{"api-one", "repoA", ""},
				[3]string{"api-two", "repoA", ""},
				[3]string{"infra-one", "repoB", ""})
			tc.setup(h)
			h.list.SetSelectedInstance(0)

			pressKey(h, tc.key)
			require.True(t, h.menu.HasNotice(), "precondition: the key must be refused with a notice")
			require.Contains(t, h.menu.String(), ", to switch")

			pressKey(h, ',')
			require.Equal(t, stateSettings, h.state)
			require.NotNil(t, h.settingsOverlay)
			assert.Equal(t, tc.want, h.settingsOverlay.SelectedRowKey())
		})
	}
}

// The arm lives exactly as long as the advice does. A ',' pressed after the notice has gone
// opens the panel where the user left it, not on a row a refusal mentioned a minute ago.
func TestSettingsJumpExpiresWithItsNotice(t *testing.T) {
	h := filterReorderHome(t,
		[3]string{"api-one", "repoA", ""},
		[3]string{"api-two", "repoA", ""},
		[3]string{"infra-one", "repoB", ""})
	h.list.SetSortMode("status")
	h.list.SetSelectedInstance(0)

	pressKey(h, 'J')
	require.True(t, h.menu.HasNotice())
	h.Update(hideErrMsg{gen: h.noticeGen}) // the toast's own timer fires

	pressKey(h, ',')
	require.NotNil(t, h.settingsOverlay)
	assert.NotEqual(t, "session_sort", h.settingsOverlay.SelectedRowKey(),
		"an expired notice must not still be steering ','")
}

// A BACKGROUND notice disarms the jump too. This is the path that made scheduleNoticeHide
// the clear site rather than flashNotice: the drift, agent and update notices reach the row
// through showMenuNotice directly, and each bumps noticeGen — so the original toast's timer
// mismatches, hideErrMsg skips its clear, and ',' stays pointed at a setting whose advice
// left the screen five seconds ago.
func TestABackgroundNoticeDisarmsTheSettingsJump(t *testing.T) {
	h := filterReorderHome(t,
		[3]string{"api-one", "repoA", ""},
		[3]string{"api-two", "repoA", ""},
		[3]string{"infra-one", "repoB", ""})
	h.list.SetSortMode("status")
	h.list.SetSelectedInstance(0)

	pressKey(h, 'J')
	require.True(t, h.menu.HasNotice())
	gen := h.noticeGen

	// A background notice that never passes through flashNotice.
	_ = h.showMenuNotice("⚠ agent heuristics may be stale", ui.NoticeInfo)
	require.NotEqual(t, gen, h.noticeGen, "precondition: the background notice bumped the generation")
	h.Update(hideErrMsg{gen: gen}) // the ORIGINAL timer fires and is ignored as stale

	pressKey(h, ',')
	require.NotNil(t, h.settingsOverlay)
	assert.NotEqual(t, "session_sort", h.settingsOverlay.SelectedRowKey(),
		"a notice the user can no longer see must not still be steering ','")
}

// A second notice replaces the first one's arm rather than stacking on it.
func TestANewNoticeReplacesTheSettingsJump(t *testing.T) {
	h := filterReorderHome(t,
		[3]string{"api-one", "repoA", ""},
		[3]string{"api-two", "repoA", ""},
		[3]string{"infra-one", "repoB", ""})
	h.list.SetSortMode("status")
	h.list.SetSelectedInstance(0)

	pressKey(h, 'J')                            // arms session_sort
	_ = h.handleInfoNotice("something else")    // an unarmed notice takes the row
	pressKey(h, ',')
	require.NotNil(t, h.settingsOverlay)
	assert.NotEqual(t, "session_sort", h.settingsOverlay.SelectedRowKey())
}
```

> Both setups were verified against the tree: `SetSortMode("status")` makes
> `SessionReorderEnabled()` false so `app_update.go:951` fires, and `SetGroupMode("repo")`
> makes `accountGrouped()` false so the **first** `[` branch (`app_update.go:991`) fires —
> not the `AccountReorderEnabled` one below it. `[` is `KeyMoveAccountUp`
> (`keys/registry.go:197`). The `require.Contains` is still there so a future change to
> either branch fails loudly rather than silently testing the other one.
>
> This test also needs `dialog_voice_test.go`-style imports of its own; see Step 1's import
> note below.

`dialog_voice_test.go` needs `strings` and `xansi "github.com/charmbracelet/x/ansi"` (the
alias `app/settings_test.go` already uses); `goimports` will not pick the alias, so add it by
hand.

- [ ] **Step 2: Add `SelectedRowKey` to the overlay**

In `ui/overlay/settings.go`, beside `RailIndex`:

```go
// SelectedRowKey is the key of the row the rows pane has highlighted, so home's tests can
// assert where a deep link landed without reaching into the panel's cursor.
func (s *SettingsOverlay) SelectedRowKey() string { return s.selectedRow().key }
```

- [ ] **Step 3: Run to verify they fail**

```bash
PATH="/home/zvi/.local/share/mise/installs/go/1.26.4/bin:$PATH" \
  go test ./app/ -run 'OverCap|Comma|ReorderNoticesDeepLink|SettingsJump|ANewNotice' 2>&1 | head -20
```
Expected: FAIL — the message literals differ and `,` does nothing.

- [ ] **Step 4: Rewrite the cap message**

In `app/session_cap.go`:

```go
// overCapMessage is the host-capacity confirmation text: it names the derived cap and the
// live count so the tradeoff — more sessions queue rather than parallelize — is explicit at
// the moment the user crosses it, and it names the key that changes the limit.
//
// That tail used to read "(set max_sessions in config.json to change this)", which sent the
// user to a text editor for a setting the configuration panel owns. ',' now opens the panel
// straight onto the Session limit row (confirmOverCap arms it). The dialog wraps at 46
// cells, so the replacement is priced in rendered lines: the old tail took two and the new
// one takes one, making the dialog shorter as well as more useful.
func overCapMessage(limit, active, adding int) string {
	if adding > 1 {
		return fmt.Sprintf(
			"%s.\nSpawning %d more will queue, not parallelize, and drive up load.\n"+
				"Create them anyway? (, to change the limit)",
			hostCapacityLine(limit, active), adding)
	}
	return fmt.Sprintf(
		"%s.\nAnother will queue, not parallelize, and drive up load.\n"+
			"Create it anyway? (, to change the limit)",
		hostCapacityLine(limit, active))
}
```

and in `confirmOverCap`, after `m.pendingOverCap = &plan`:

```go
	// The message teaches ','; handleConfirmState turns that into a deep link onto the row
	// it names. Armed here rather than in confirmAction, because it is this dialog's copy
	// that promises it.
	m.pendingConfirmSettingKey = "max_sessions"
```

- [ ] **Step 5: Add the two home fields**

In `app/app.go`, beside `settingsRail`:

```go
	// noticeSettingKey is the settings row the transient notice currently on screen points
	// at, so the ',' that notice advertises lands on that row instead of the remembered
	// category. Armed with the notice and cleared when it goes, so an unrelated ',' minutes
	// later is unaffected. See settingNotice.
	noticeSettingKey string

	// pendingConfirmSettingKey is the settings row the open confirmation dialog's copy
	// promises ',' will open — the host-capacity dialog and nothing else today. Empty means
	// ',' is inert in stateConfirm, which is what keeps the key from silently cancelling an
	// unrelated confirmation.
	pendingConfirmSettingKey string
```

- [ ] **Step 6: Arm and clear the notice jump — at the real chokepoint**

`flashNotice` is **not** the chokepoint. Three background notices reach the row through
`showMenuNotice` directly — `app/app_msgs.go:36` (drift), `app/app_agentcheck.go:44`,
`app/app_updatecheck.go:146` — and each one calls `scheduleNoticeHide()`, which **bumps
`m.noticeGen`**. Clearing only in `flashNotice` and inside `hideErrMsg`'s
`msg.gen == m.noticeGen` guard leaves a real hole: arm on `J`, let a drift toast replace the
text within five seconds, and the original timer mismatches, skips the clear, and leaves `,`
pointed at `session_sort` with an unrelated toast on screen.

`scheduleNoticeHide` (`app/app_feedback.go:248`) is what every notice path calls and it owns
the generation the arm's lifetime is defined against. Clear it there:

```go
func (m *home) scheduleNoticeHide() tea.Cmd {
	// Any new notice supersedes the previous one's settings jump, including the background
	// notices that reach showMenuNotice without passing through flashNotice (drift, agent,
	// update). settingNotice re-arms after this returns, so it is unaffected.
	m.noticeSettingKey = ""
	m.noticeGen++
```

and add below `handleInfoNotice`:

```go
// settingNotice flashes a notice that names ',' and points that ',' at the setting it is
// about. The notice already told the user which key to press; this is what makes the key
// land somewhere useful instead of on the rail entry they last visited.
//
// It takes the level because the call sites disagree: a reorder refusal is informational,
// while a missing default program is an error. The arm lives exactly as long as the advice —
// scheduleNoticeHide clears it for any newer notice, and the hideErrMsg handler clears it
// when the toast expires.
func (m *home) settingNotice(text string, level ui.NoticeLevel, key string) tea.Cmd {
	cmd := m.flashNotice(text, level)
	m.noticeSettingKey = key
	return cmd
}
```

In `app/app_update.go`'s `hideErrMsg` case, inside the `msg.gen == m.noticeGen` block:

```go
			m.noticeSettingKey = "" // the advice is off screen; ',' goes back to the rail
```

- [ ] **Step 7: Point every `,`-advertising notice at its row**

There are **five**, not two. The spec named one and this plan's decision table named two; a
grep for `press ,` / `(, to` finds three more in `app/app_welcome.go`, and those three
*name the setting in their own copy*, which makes them the stronger case:

In `app/app_update.go`:

```go
			if !m.list.SessionReorderEnabled() {
				return m, m.settingNotice(
					"session reorder is off while sorting by status (, to switch)",
					ui.NoticeInfo, "session_sort")
			}
```

```go
			if !m.list.AccountGrouped() {
				return m, m.settingNotice(
					"cluster reorder needs account grouping (, to switch)",
					ui.NoticeInfo, "group_mode")
			}
```

In `app/app_welcome.go`, the skip path (`:74`) and both `warnMissingProgram` branches
(`:117`, `:119`) — all three land on `default_program`:

```go
			m.settingNotice("Setup skipped — press , to pick a default agent, or n to start a session",
				ui.NoticeInfo, "default_program"),
```

```go
	return m.settingNotice(text, ui.NoticeError, "default_program")
```

No wording changes anywhere: every one of the five already teaches `,`, and this PR only
changes where `,` lands.

- [ ] **Step 7a: Make the rule structural**

Without a guard, the tree now has two indistinguishable classes of `,`-notice and the next
author has a coin-flip chance of reaching for `flashNotice`. Add to
`app/reorder_filter_keys_test.go`:

```go
// Every notice that tells the user to press ',' must go through settingNotice, so the ',' it
// advertises lands on the setting it is about. flashNotice and handleInfoNotice are the
// generic paths and actively DISARM a jump, so a notice using one of them is the bug this
// catches — five call sites exist today and they read identically at a glance.
func TestEveryCommaNoticeGoesThroughSettingNotice(t *testing.T) {
	root := ".."
	var offenders []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") ||
			strings.HasSuffix(path, "_test.go") {
			return err
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(src), "\n") {
			if !strings.Contains(line, "press ,") && !strings.Contains(line, "(, to") {
				continue
			}
			if strings.Contains(line, "//") || strings.Contains(line, "settingNotice") {
				continue
			}
			offenders = append(offenders, fmt.Sprintf("%s:%d: %s", path, i+1, strings.TrimSpace(line)))
		}
		return nil
	})
	require.NoError(t, err)
	assert.Empty(t, offenders,
		"a notice advertising ',' must use settingNotice so the key lands on its setting")
}
```

> This walks the tree, so it will also see `ui/overlay/welcome.go:121` — prose inside the
> welcome overlay, not a notice, and unreachable from `settingNotice`. Confirm what the walk
> actually reports on the first run and either scope `root` to `app` or add that one line to
> a documented exemption with the reason. **Do not weaken the match pattern to make it
> pass** — the pattern is the rule.

- [ ] **Step 8: Consume the arms**

In `app/app_update.go`, replace the `KeySettings` arm from Task 4:

```go
	case keys.KeySettings:
		if key := m.noticeSettingKey; key != "" {
			m.noticeSettingKey = ""
			return m, m.openSettingsAt(key)
		}
		return m, m.openSettings()
```

and add beside `openSettings`:

```go
// openSettingsAt opens the configuration panel focused on one row — the deep link of spec
// §12. It falls back to the remembered rail when the key is unknown, so a stale caller
// degrades to today's behavior rather than opening a panel with an invisible cursor.
func (m *home) openSettingsAt(key string) tea.Cmd {
	cmd := m.openSettings()
	m.settingsOverlay.OpenAt(key)
	return cmd
}
```

In `app/app_keys.go`, at the top of `handleConfirmState`:

```go
	// A dialog whose copy promises ',' opens the setting it named. It is a cancel: nothing
	// staged is run, and the stashed create form stays restorable exactly as declining leaves
	// it. Armed per-dialog (confirmOverCap), so ',' stays inert in every other confirmation.
	if key := m.pendingConfirmSettingKey; key != "" && msg.String() == "," {
		m.pendingConfirmSettingKey = ""
		m.pendingConfirmAction = nil
		m.pendingConfirmBusyLabel = ""
		m.confirmationOverlay = nil
		return m, m.openSettingsAt(key)
	}
```

and clear the arm on the normal close path, beside `m.pendingConfirmAction = nil`:

```go
		m.pendingConfirmSettingKey = ""
```

- [ ] **Step 9: Run everything**

```bash
PATH="/home/zvi/.local/share/mise/installs/go/1.26.4/bin:$PATH" \
  go test ./app/ ./ui/... 2>&1 | tail -20
```
Expected: PASS.

- [ ] **Step 10: Verify the guards fail when they should**

1. Delete `m.settingsOverlay.OpenAt(key)` from `openSettingsAt`. Expected:
   `TestOverCapDialogCommaOpensTheSessionLimit` and both
   `TestReorderNoticesDeepLinkToTheirSetting` subtests FAIL. Restore.
2. Delete `m.noticeSettingKey = ""` from `scheduleNoticeHide`. Expected:
   `TestANewNoticeReplacesTheSettingsJump` **and**
   `TestABackgroundNoticeDisarmsTheSettingsJump` both FAIL. Restore.
2a. Move that line from `scheduleNoticeHide` into `flashNotice` (the shape this plan had
   before review). Expected: `TestANewNoticeReplacesTheSettingsJump` passes but
   `TestABackgroundNoticeDisarmsTheSettingsJump` FAILS — which is the whole reason the clear
   moved. Restore.
3. Delete `m.noticeSettingKey = ""` from the `hideErrMsg` handler. Expected:
   `TestSettingsJumpExpiresWithItsNotice` FAILS. Restore.
3a. Change one `settingNotice` call in `app/app_welcome.go` back to `flashNotice`. Expected:
   `TestEveryCommaNoticeGoesThroughSettingNotice` FAILS naming that line. Restore.
4. Drop the `key != ""` condition from `handleConfirmState`'s guard (so `,` always jumps).
   Expected: `TestCommaIsInertInAnUnarmedConfirmation` FAILS. Restore.
5. Restore `overCapMessage`'s old tail. Expected: both `TestOverCapMessage` and
   `TestOverCapMessageIsShorterThanThePathItReplaced` FAIL — the second one is the
   rendered-line price and is the reason the wording was chosen. Restore.
6. Re-run and confirm green.

- [ ] **Step 11: Lint and commit**

```bash
PATH="/home/zvi/.local/share/mise/installs/go/1.26.4/bin:$PATH" /home/zvi/go/bin/golangci-lint run ./ui/... ./app/...
git status --short
git add -u app ui/overlay
git commit -m "feat(app): deep-link the cap dialog and the reorder notices into the settings panel"
```

---

## Task 8: Full verification, the manual eyeball, and the PR

**Files:** none — verification only.

- [ ] **Step 1: The local gate**

```bash
mise exec -- just ci 2>&1 | tail -30
```
`lint` may die with exit 127 (`golangci-lint` is not on `PATH` under mise) — the known
toolchain gap, not a regression. If it reports issues in files under a **different** atrium
worktree, that is #486's global-cache leak: `golangci-lint cache clean` and re-run. Step 3
is the authoritative lint.

- [ ] **Step 2: The race detector**

```bash
mise exec -- just test-race 2>&1 | tail -20
```
Expected: PASS.

- [ ] **Step 3: The authoritative, scoped lint**

```bash
PATH="/home/zvi/.local/share/mise/installs/go/1.26.4/bin:$PATH" \
  /home/zvi/go/bin/golangci-lint run ./ui/... ./app/... 2>&1 | tail -20
```
Expected: no issues. Watch for `revive`'s `exported` on `OpenAt`, `Handoff`,
`SelectedRowKey`, `RailEntryCount`, `SettingsHandoff`, `HandoffNone`, `HandoffAccounts` —
each needs a doc comment starting with its own name — and for `unused` on `searchHaystack`
or `badgeAvail` if a test was rewritten away from them.

- [ ] **Step 4: Eyeball the real panel at 100×32 and 80×24**

**This is the step tests cannot substitute for.** A search that passes every assertion can
still feel wrong: results in a baffling order, a rail whose counts you do not notice, a
deep link that lands somewhere technically correct and practically useless.

```bash
S=/tmp/atrprc; rm -rf $S; mkdir -p $S/home/.atrium $S/tmux $S/repo
echo '{"default_program":"bash"}' > $S/home/.atrium/config.json
cd $S/repo && git init -q . && git config user.email t@t && git config user.name t \
  && echo hi > a.txt && git add . && git commit -qm init
cd -
export TMUX_TMPDIR=$S/tmux
mise exec -- just build
tmux -L eyeball new-session -d -x 100 -y 32 -c $S/repo \
  "HOME=$S/home TMUX_TMPDIR=$S/tmux $PWD/bin/atrium"
sleep 3
tmux -L eyeball send-keys Escape; sleep 1   # dismiss the first-run welcome
tmux -L eyeball send-keys ','; sleep 1
tmux -L eyeball capture-pane -p -t 0
```

**`TMUX_TMPDIR` must be exported in every shell.** An isolated `HOME` alone does *not*
isolate Atrium's tmux socket; a new shell otherwise reports "no server running" — or worse,
lands the throwaway session on the developer's live server.

Confirm by eye, at 100×32:

1. **Walk the rail down to `Advanced` first, then press `/`.** The title becomes
   `Settings   /▌`, the rail dims, the rows pane goes flat with a category on the right of
   every line, the hint says `esc clear` — and the highlight has **not moved off the Advanced
   row you were on**. Pressing `/` from the landing category cannot show you this: row 0 is
   already the cursor there, which is how the cursor-teleport bug survived the first draft of
   this script.
1a. `Esc` immediately, without typing: you are back on the same Advanced row.
1b. Check `Config file` in that same category — a long path — carries `Advanced` on the
   right. That is the row `fitValue` exists for; without it the chip is gone at every width.
2. Type `theme`: the *Theme* row is first, the rail's Appearance entry shows `1`, and the
   rail marker has moved to Appearance.
3. Type `j`, `k`, `r` into the filter and confirm they are **letters** — nothing navigates,
   nothing resets, the theme does not change.
4. `↓`/`↑` move the highlight and the rail marker follows across categories.
5. `↵` on an enum result cycles it in place and the row stays in the list; `?` opens that
   row's full help and a second `?` returns to the filtered list.
6. Backspace to a query matching nothing: the pane says `No setting matches "…"` and the
   help pane names backspace and esc.
7. `Esc` clears and leaves you on the row you found, in its category; `Esc` returns to the
   rail; `Esc` closes. The hint says `esc clear` → `esc back` → `esc close`.
8. On a modified row (cycle the theme first), `r` clears the `•` and the palette repaints in
   the same frame.
9. On the rail, `Accounts` says `↵ accounts` in the hint and Enter opens the `@` overlay;
   Esc from there and `,` reopens settings on Accounts.
10. `Profiles` still shows its note and Enter does nothing.

Then the degradation floor:

```bash
export TMUX_TMPDIR=/tmp/atrprc/tmux
tmux -L eyeball resize-window -t 0 -x 80 -y 24; sleep 1
tmux -L eyeball capture-pane -p -t 0
```

At 80×24, with `/config` typed: every hit still shows a category (possibly `Worktrees &…`),
no line wraps, the rail still fits unscrolled with its counts, and the hint is the second
rung.

Then below the two-pane threshold, where the rail is gone entirely:

```bash
export TMUX_TMPDIR=/tmp/atrprc/tmux
tmux -L eyeball resize-window -t 0 -x 64 -y 24; sleep 1
tmux -L eyeball capture-pane -p -t 0
```

At 64 columns `/config` shows a flat result list with **no rail and no counts** — that is the
documented contract, not a bug — and the help pane's last line still names the highlighted
result's category. If it does not, the single-pane substitute is missing and the search is
unusable there.

Then the deep links, from a real dialog:

```bash
export TMUX_TMPDIR=/tmp/atrprc/tmux
tmux -L eyeball resize-window -t 0 -x 100 -y 32; sleep 1
tmux -L eyeball send-keys Escape; sleep 1
tmux -L eyeball send-keys J; sleep 1          # refused: sorting by status? if not, set it first
tmux -L eyeball capture-pane -p -t 0 | tail -3
tmux -L eyeball send-keys ','; sleep 1
tmux -L eyeball capture-pane -p -t 0 | head -12
```

Expected: the panel opens with the cursor **on Sort within group**, in Session list — not on
the rail. If the notice did not fire, switch `session_sort` to `status` in the panel first.

- [ ] **Step 5: Tear down and confirm the live server is untouched**

```bash
export TMUX_TMPDIR=/tmp/atrprc/tmux
tmux -L eyeball kill-server 2>/dev/null
rm -rf /tmp/atrprc
unset TMUX_TMPDIR
tmux -L atrium list-sessions | head -3   # the developer's live server must be intact
```

- [ ] **Step 6: Open the PR**

```bash
gh auth switch --user ZviBaratz
git push -u origin HEAD
```

Write the body to a file first and pass `--body-file`; the classifier blocks a heredoc body
containing shell-looking content, and a `--body-file` is the path that survives it.

```bash
cat > /tmp/prc-body.md <<'EOF'
PR C of the configuration panel redesign
(`docs/superpowers/specs/2026-07-25-configuration-panel-design.md`), following #482 and #491.

PR A landed the taxonomy and copy; PR B drew the two-pane browser and the visibility layer.
This makes the last three mechanisms reachable from the keyboard.

**`/` search** flattens across all ten categories through the shared `Picker` (#373's
primitive) and `internal/fuzzy` — the one matcher in the tree — over label + key + summary +
category, with a bonus so a label or key hit outranks a word buried in a summary. `/` works
from either pane; while the filter has focus runes type (j and k are letters in a search
box, and so is `r`) while ↑/↓ still move the result cursor. `?` is the single reserved rune,
and `TestNoRowContainsAQuestionMark` pins the premise that makes reserving it free.

**The rail stays useful while filtering.** It dims, shows a per-category match count, and its
marker follows the highlighted result — which is also what makes `Esc` land you on the row
you found, in its category, rather than back where the search started.

**Search rows are the widest lines the panel composes**, so the category rides the badge
column under PR B's own ladder: the label never truncates, the badge yields first, and the
category degrades to `Worktrees &…` rather than vanishing — the same rule that keeps an inert
chip on screen. An inert chip still wins the column, and `contextLine` names the highlighted
row's category regardless, which is also the whole orientation below 73 columns where the
rail is not drawn at all.

**One PR B bug fell out of making that promise keepable.** `valueCell`'s badge reservation
bites only on `kindEnum`; every other kind returns its value bare, so a long path or command
evicts the chip beside it. That is correct for a timing badge (spec §10 drops it first) and
wrong for the two badges that must not vanish — measured, it drops the category from the
Config file row at *every* terminal width, and it drops PR B's own `needs desktop mode` chip
from a long enough `notify_command`. `fitValue` reserves for those two callers only, and
never below `rowMinValueCells`; a shortened value is still shown in full in the help pane.

**`r` reset** restores a row and reports the changed key, so it persists and live-applies
through `applySettingChange` exactly like an edit — and reports nothing when the value was
already default, so holding `r` does not rewrite `config.json` and re-run a theme repaint on
every press. `r` on the rail is a silent no-op (spec §2: no category reset), and a test
exists to stop a later "consistency" edit from adding one.

**`SelectRow` is now `OpenAt`**, with every caller migrated in the same commit. It takes the
key alone rather than the spec's `(category, key)`: `settingCategory` is unexported, and
guard 1 already pins that a key belongs to exactly one category, so a passed category could
only ever disagree with the row's own. The spec is amended in this PR.

**Five call sites prove the deep link,** not the two the spec names. The host-capacity
dialog's tail used to read `(set max_sessions in config.json to change this)` — a text
editor, for a setting this panel owns. It now reads `(, to change the limit)`, and `,`
cancels the create and opens the panel on *Session limit*. The dialog got shorter too:
priced at the 46-cell wrap, the old tail took two rendered lines and the new one takes one.
A grep for `press ,` then found three more notices in `app_welcome.go` that *name the
setting in their own copy* and land on `default_program`, alongside the two reorder refusals
— so a test now asserts every `,`-advertising notice goes through `settingNotice`, because
otherwise the tree carries two indistinguishable classes of them.

The arm lives exactly as long as the advice, and the clear sits in `scheduleNoticeHide`
rather than `flashNotice`: three background notices (drift, agent, update) reach the hint row
without passing through `flashNotice` and each bumps the notice generation, which would
otherwise strand an arm behind an unrelated toast.

**Accounts is a real handoff.** The rail entry rendered a note telling you to press `@`
somewhere else; Enter now opens the overlay. The panel cannot open a sibling, so it records a
request and `home` fulfils it as the panel closes. Profiles keeps its note — that is PR D.

**Worth reviewing:** the three-way badge priority in `rowValueAndBadge` and `fitValue`'s
rule about which badges may evict a value and which may not; the width sweeps, which assert
on the badge the renderer returns rather than on a substring of the line (an ellipsis in a
rendered line can come from a truncated *value*, which is how the first version of that guard
passed while the category was gone); and the arm/clear discipline for `noticeSettingKey`,
where the interesting tests are the three that assert the jump *expires* — including the
background-notice path that moved the clear out of `flashNotice`.

The plan this shipped with records what an adversarial review changed before any code was
written, including two defects that would have shipped: the cursor teleport and the false
degradation promise.

The Profiles editor is PR D.

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
gh pr create --title "feat(settings): search, reset, and the deep links" --body-file /tmp/prc-body.md
```

`gh pr edit` is broken on this repo — use `gh api --method PATCH` if the body needs
changing.

- [ ] **Step 7: Read CI before calling it merge-ready**

```bash
gh pr checks --required
```
`gh pr checks` has **no** `--json` flag; a poll loop using one fails every iteration and
times out silently. Expected: all required checks pass, including the macOS job — which
exercises `agent_oom_margin`'s non-Linux `activeWhen` branch and therefore the interaction
between a filter and an inert row on a platform this machine cannot reproduce.

---

## What the adversarial review changed

Three reviewers read the first draft against the tree — one applying its production code to a
throwaway copy and running `go build`, the suite and `golangci-lint` against it; one computing
real fuzzy scores and rendered widths for every prescribed test; one hunting interactions. The
findings are folded into the tasks above; these are the ones that **changed the design**, not
just a test:

1. **`/` teleported the cursor before a character was typed.** An empty query matches all 38
   rows at score 0, so the sync snapped the cursor to row 0 and the rail to Sessions the
   instant `/` was pressed — and `Esc`, whose whole selling point is "land on the row you
   found", landed you at the top of the schema. `startSearch` now seeds the picker's cursor
   *from* the current row. The eyeball script was structurally blind to this: it pressed `/`
   from the landing category, the one place row 0 is already the cursor.
2. **The category chip's "degrades rather than vanishes" promise was false**, and measurably
   so: `valueCell`'s badge reservation bites only on `kindEnum`, so `config_file` lost its
   category at *every* terminal width and `carry_files` up to 90 columns. `fitValue` fixes it
   for the two badges that must not vanish — which also repairs the same hole in **PR B's**
   inert chip on a long `kindText` value.
3. **The guard written for that promise could not have caught it, twice over.** Its query's
   non-inert hits were all bool rows with 6-cell values, so nothing was ever squeezed; and
   `Contains(line, "…")` also matches a truncated *value*, so the assertion passed on a line
   whose badge was gone. It now asserts on the badge `rowValueAndBadge` returns, over three
   queries chosen because they include an enum and a long text row.
4. **The notice arm was cleared at the wrong chokepoint.** Three background notices reach the
   hint row through `showMenuNotice` without passing `flashNotice`, and each bumps
   `noticeGen` — so a drift toast within the five-second window stranded `,` pointed at a
   setting whose advice had left the screen. The clear moved to `scheduleNoticeHide`.
5. **There were five `,`-advertising notices, not two.** The three in `app_welcome.go` name
   their setting in their own copy, which makes them a stronger case than the two the spec
   listed. A structural guard now asserts every one goes through `settingNotice`.
6. **The plan claimed guard 10.** It is PR B's. That false claim is exactly what let the
   search × single-pane-fallback interaction go unexamined — below 73 columns there is no
   rail and therefore no match counts, which is now a stated contract with its own sweep.

Two smaller ones worth naming because they are invisible to everything but the gate: the
prescribed `helpLines` guard was `!(A && B)`, which trips `staticcheck`'s **QF1001** while
`build`, `vet`, `fmt` and the whole suite stay green; and `dialog_voice_test.go` needed an
`assert` import the plan's note omitted.

**Four of the plan's own claims were counts that were simply wrong** — 19 direct `SelectRow`
callers rather than "~25", 62 `settingsAt` calls rather than "~40", an import note listing
four packages the file already imported, and an instruction to fix existing hint tests that
do not break. Each is corrected in place. They are recorded rather than quietly fixed because
the "if ~40 tests fail" line was a *triage heuristic* an implementer would have reasoned
from.

## What changed during implementation

The plan above is the reviewed version. Five things moved while executing it, each because
running the code said so:

1. **`fitValue`'s floor is `squeezedValueCells` (8), not `rowMinValueCells` (14).** With 14
   the promise the plan had just fixed still failed: measured, the tightest two-pane geometry
   leaves a 14-cell value column, so a 14-cell floor never squeezes and the chip drops
   anyway. 8 was chosen against that measurement — reserving 4 for the chip leaves 9 — and
   the sweep is what fails if the geometry moves.
2. **`valueWasTruncated` now asks `rowValueAndBadge`** instead of re-deriving the value. A
   value can be shortened twice — by `fitValue` and again by `composeRowLine` — and the old
   helper saw only the second, so a squeezed value was reported as whole and the help pane
   dropped its obligation to show it in full.
3. **The comma-notice guard is scoped to the call plus the variable feeding it.** Two other
   scopes were tried against the tree and rejected: call-only misses `warnMissingProgram`
   (which builds its text into a variable across two branches — confirmed by reverting that
   site and watching the check stay green), and whole-function flags `handleKeyPress`, a
   400-line switch that legitimately holds both converted notices and a dozen unrelated ones.
   `parser.ParseDir` also had to go — `staticcheck` deprecates it (SA1019), which only `lint`
   sees.
4. **The plan's one ordering compromise was avoidable.** Task 4 was told to put `/ search` in
   the rail ladder before Task 5 wired the key; instead the ladder shipped without it and
   Task 6 added it alongside the working key, so no commit ever advertised a dead key.
5. **Two prescribed tests were duplicates of PR A guards** (`TestResetRestoresTheDefault`,
   `TestResetIsPresentWhereverADefaultIs`) and were dropped rather than added, with a comment
   pointing at the originals. The plan's own Global Constraints warned about exactly this for
   schema *fields*; it holds for schema *guards* too.

One PR B bug surfaced and is fixed with its own guard: a long `notify_command` evicted its
inert chip at every width. The mutation for it came back negative at first — no existing test
failed — so the case was constructed, the eviction confirmed by measurement at 73/80/100/120
columns, and `TestALongValueCannotEvictAnInertChip` written before the fix was trusted.

## Self-Review

**Spec coverage.** §8 search → Tasks 5 and 6, clause by clause: `/` from either pane
(`TestSlashFocusesTheRowsPaneFromEitherPane`), runes type / arrows navigate
(`TestRunesTypeWhileTheFilterHasFocus`, `TestArrowsMoveTheResultCursor`), editing keeps the
row in the results (`TestEditingAMatchedRowWorksAndKeepsItInTheResults`), three-layered Esc
(`TestEscIsThreeLayeredWithAFilter`), `?` on a result and its dismissal
(`TestQuestionMarkOpensHelpForTheHighlightedResult`), the rail's dim + per-category counts
(`TestRailIsInertWhileFiltering`, `TestRailShowsPerCategoryMatchCounts`). §7's `r` → Task 2.
§12 deep links + `overCapMessage` → Tasks 3 and 7. §4's Accounts handoff → Task 4. §10's
truncation priority reused unchanged, extended only where the spec never contemplated a
category chip. Guards **8, 9 and 11** land here; **guard 10 (the single-pane fallback) is PR
B's and already holds** — an earlier draft claimed it for PR C, and that false claim is
precisely what let the search × single-pane interaction go unexamined until review. Guard 12
is PR D's.

**Deferred by design:** the Profiles editor and its guard 12.

**Four decisions diverge from the spec's letter, each recorded rather than silently
adopted.** (1) `OpenAt(key)` instead of `OpenAt(category, key)` — amended into §12 in Task 3.
(2) Space extends the filter rather than toggling a bool, which §8's "editing works exactly
as unfiltered" and its "runes go to the filter" cannot both be true of; `↵` is the toggle
while filtering, and `handleSearchKey`'s doc comment says so. (3) `?` is reserved from the
filter, which is the same collision resolved the other way, and
`TestNoRowContainsAQuestionMark` pins that it costs the search nothing. (4) A third notice
gains a deep link, because identical phrasing landing in two different places would read as
a bug.

**Two ordering compromises, both deliberate.** Task 4's rail hint ladder mentions `/ search`
before Task 5 wires it — splitting the ladder twice would mean writing a test against a
string the next task rewrites, and the two tasks ship in one PR. And Task 2's app-level
reset test uses `OpenAt`, which Task 3 introduces; the step says to run it at the end of
Task 3 rather than leaving both names live.

**Every query and width in this plan has now been measured**, not reasoned: "agent" ranks
`agent_oom_margin` at 180 against a three-way tie at 60 without the bonus; "in" returns 36
rows across all ten categories; "base" returns 13 across six; "theme"/"zzz"/"themezzzz"
behave as the tests assume; the largest category is 6; `overCapMessage` is 5 rendered lines
before and 4 after; all nine hint rungs match the table. Where a step still says "print it
once while iterating", that is a genuine unknown, not boilerplate.

**One mutation step is allowed to come back negative**, and says what to do then: Task 6 Step
11's mutation 3 (the inert arm of `fitValue`). If no existing row is long enough to evict its
chip, construct one and report the result either way — the PR body claims that arm is a bug
fix, and a claim nobody watched fail is not evidence.

**The trap the brief named, answered explicitly.** "A filtered list plus a folded/inert row
is where this repo has been bitten repeatedly." Three assertions cover it rather than an
eyeball: `TestInertChipBeatsTheCategoryOnASearchRow` (the chip keeps the column),
`TestSearchResultCategoryDegradesRatherThanVanishing` (which skips inert rows explicitly —
and, after review, *counts the skips* and requires a non-zero total, because under
`DefaultConfig` none of its rows were inert and the branch was dead), and
`TestRailCountsSumToTheResultCount` (the rail is a read-out of the results, never a second
query). The `group_mode` row is the sharpest case — it is inert only when `home` injects a
gate the panel cannot compute — and it appears in the eyeball script.
