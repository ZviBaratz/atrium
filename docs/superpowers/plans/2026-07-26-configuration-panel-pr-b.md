# Configuration panel redesign — PR B: two-pane renderer, navigation, degradation, visibility

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the settings panel's single-column renderer with the two-pane category
browser of spec §3 — a 13-entry rail beside the highlighted category's rows, a
**fixed-height** 3-line help pane, a single-pane fallback below a derived width
threshold, `?` expanded help, and the four visibility signals PR A left undrawn.

**Architecture:** `settings.go` keeps the type, the exported API and the editing
machinery, and loses the renderer to a new `settings_render.go`; the focus model, rail
vocabulary and key grammar go to a new `settings_nav.go`. `settings_schema.go` is
**untouched** — every mechanism this PR draws (`isModified`, `reset`, `activeWhen`,
`gloss`, `detail`, `applyTiming.badge()`) already exists there, tested and unrendered.
The one new fact the panel needs from outside is session-derived: whether `ui.List`
currently renders account clusters, injected by `home`.

**Tech Stack:** Go 1.25 (`go.mod:3`), Bubble Tea, lipgloss, `charmbracelet/x/ansi`,
testify. Design record: `docs/superpowers/specs/2026-07-25-configuration-panel-design.md`
(below: "the spec"). Predecessor:
`docs/superpowers/plans/2026-07-25-configuration-panel-pr-a.md` (below: "PR A's plan"),
merged as #482.

## Review corrections (read this first)

This plan was reviewed adversarially before implementation and revised. Two reviewers
converged on the same top defects; the fixes are folded into the tasks below, but they are
listed here because several **change the design**, not just a test:

1. **The overflow marker used to overwrite the cursor's own line**, hiding the selected
   row whenever anything was below it. `rowsPaneLines` now keeps the cursor one line inside
   the window (Task 5 Step 6), and `TestSelectedRowIsAlwaysVisible` sweeps for it.
2. **The inert reason chip was dropped at 80×24** — the degradation floor — because
   `valueCell` spent the whole slack on inline enum alternatives with nothing reserved for
   the badge. The priority is now **alternatives first to go, then the badge, then the
   value** (Task 7 Step 4), which refines spec §10: §10 never contemplated the
   alternatives competing with the badge.
3. **The rail truncated silently below 24 rows**, so the current entry could be off-screen
   with no indication. It now windows around its cursor (Task 5 Step 5).
4. **`home.settingsRail`'s zero value is 0 — All settings** — which spec §4 explicitly
   excludes as the landing, so every fresh run would have opened on the flat view. It is
   now a `*int` (Task 9 Step 6).
5. **One new sweep replaces three blind spots.** Every visibility guard ran at 100×32,
   guard 4 ran at the one height where 13 entries fit by exactly 0, and guard 6's narrowest
   sample was 3 cells above the overflow. Tasks 5 and 7 each add a width × height sweep.
6. Smaller, all real: the editing row was misaligned by one cell; the position counter was
   the first thing the help pane dropped despite its doc saying "always"; `detail` never
   reached the help pane, contradicting spec §3's mockup; single-pane drill-in showed no
   category name anywhere; `group_mode`'s chip escaped the completeness guard written for
   it; `composeRowLine`'s narrow branch returned a line *wider* than the pane; and D3's
   PgUp/PgDn were handled in the `?` view but not in the rows pane.

Six prescribed tests could not have passed as written and are corrected in place. The
recurring cause is worth naming, because it will recur: **a test whose arithmetic was
reasoned about rather than run.** Where a step below says "verify by printing it once
while iterating", do that rather than trusting the number.

## Global Constraints

- **Toolchain is mise-managed and not on `PATH`.** Test with
  `PATH="/home/zvi/.local/share/mise/installs/go/1.26.4/bin:$PATH" go test ./ui/overlay/`
  or `mise exec -- just test`. Lint with
  `PATH="/home/zvi/.local/share/mise/installs/go/1.26.4/bin:$PATH" /home/zvi/go/bin/golangci-lint run ./ui/...`
  — **scoped**, because the cache is global and a bare `run` reports issues from other
  atrium worktrees against files that may not exist here.
- **`just ci` does include `lint`** (`justfile:52`), contrary to PR A's plan's claim that
  it does not — but the recipe is a bare `golangci-lint run`, which is not on `PATH` under
  mise (dying with exit 127) and which scans every worktree's cache entries. The scoped
  invocation above is the authoritative one. **Read the paths in any lint failure before
  believing it.**
- **`unused` is the linter that bites this repo.** Every helper this plan adds is read by
  a test in the same task. If the linter flags something, a test is missing — do not
  delete the helper.
- **`revive` rules CI enforces that `go vet` does not:** `exported` (every exported symbol
  needs a doc comment starting with its name) and `redefines-builtin-id` (never name
  anything `max`, `min`, or `len`). Go 1.26's builtin `max`/`min` are fine to *call*.
- **Measure unstyled width.** A bordered lipgloss block pads every line to the same
  width, so `lipgloss.Width(renderedLine) <= boxWidth` is a tautology that can never
  fail. Overflow assertions go on the plain-text composition functions
  (`rowLineParts.plain()`, `railLines()`, `helpLines()`), not on `Render()`'s output.
- **Never resolve a filesystem-derived display value at package init.** `sync.OnceValue`,
  as `configFilePath` does (`settings_schema.go:289`). A package-level var initializer
  runs *before* `TestMain`, so it captures the developer's real `HOME` no matter what the
  suite sandboxes, and no `TestMain` can repair it (CLAUDE.md: tests must never read the
  user's real data dir).
- **Do not add a `settingRow` field.** If one seems missing, PR A almost certainly
  declared it. `settings_schema.go` is not in this PR's file list.
- **No new `keys` registry entries.** Panel-internal keys are handled on `msg.String()`,
  so the registry/help/README key-drift guards stay untouched. `,` (`KeySettings`) is
  unchanged.
- **Out of scope, do not build:** `/` search, `r` reset, `OpenAt`'s real call sites (PR
  C); the Profiles editor (PR D). The hint line must not advertise a key that does
  nothing.
- **Conventional Commits, lowercase.** `feat:` / `fix:` / `refactor:` / `test:` / `docs:`.

---

## Derived geometry

Every number below was measured from the tree, not taken from the spec's estimates.
Where the spec estimated, the derived value is what ships and the difference is recorded.

| Quantity | Value | Derivation |
|---|---|---|
| Longest rail label | 15 | `"Worktrees & git"`. The three non-category entries are `All settings` (12), `Profiles` (8), `Accounts` (8) |
| Longest row label | 26 | `"Smart dispatch auto-create"` / `"Release notes after update"` — matches spec §10's assumption |
| `boxWidth()` | `min(96, width−2)`, floor 20 | spec §10, up from today's fixed 64 |
| `innerWidth()` | `boxWidth() − 4` | `Padding(1,2)` |
| `railWidth()` | **19** | `railMarkerCells(2) + 15 + railTrailCells(2)` |
| `paneDividerCells` | **3** | `" │ "`, the middle cell taken from `Borders.Style.Left` |
| row prefix | **3** | `[selection 1][modified 1][space 1]` — two *separate* single-cell columns (spec §10) |
| `minRowsPaneWidth()` | **45** | `3 + 26 + rowLabelGap(2) + rowMinValueCells(14)` |
| `twoPaneMinInner()` | **67** | `19 + 3 + 45`, computed from the parts |
| ⇒ two-pane threshold | terminal width **≥ 73** | inner 67 ⇒ box 71 ⇒ width 73. The spec's "≈72" was an estimate |
| Rows pane at width 80 | **52** | inner 74 − 19 − 3 |
| Rows pane at width 100 | **70** | inner 92 − 19 − 3 |
| `settingsVChrome` | **7** | border 2 + padding 2 + title 1 + blank-after-title 1 + hint 1 |
| `helpPaneLines` | **3** | spec §10 |
| `maxPaneLines()` | **57** | the All settings view: 38 rows + 10 headers + 9 spacers |

**Height arithmetic.** The separator is counted with the help pane, because it is drawn
only when there is a help pane to separate:

```
helpHeight()      = clamp(height − settingsVChrome − settingsMinBody − 1, 0, helpPaneLines)
helpBlockHeight() = helpHeight() + 1, or 0 when helpHeight() == 0
paneHeight()      = clamp(height − settingsVChrome − helpBlockHeight(), settingsMinBody, maxPaneLines())
box height        = settingsVChrome + paneHeight() + helpBlockHeight()
```

| height | helpHeight | helpBlock | paneHeight | box | note |
|---|---|---|---|---|---|
| 24 | 3 | 4 | **13** | 24 | pane 13 **== the 13 rail entries** — exactly the budget `TestCategoryCountFitsTheRailBudget` reserves |
| 40 | 3 | 4 | 29 | 40 | |
| 14 | 3 | 4 | 3 | **14** | zero slack: `paneHeight` is at its `settingsMinBody` floor and the box exactly fills the terminal |
| 12 | 1 | 2 | 3 | 12 | help sheds lines rather than eating the list |
| 11 | 0 | 0 | 4 | 11 | |
| 10 | 0 | 0 | 3 | 10 | |
| 9 | 0 | 0 | 3 | 10 | **overflows by 1** — today's renderer overflows below 12, so this is strictly better |
| 70 | 3 | 4 | 57 | 68 | capped: nothing left to show |

`helpHeight()` is a function of the **terminal height alone**. It never reads the cursor.
That independence is the fix for D5 and is what guard 5 pins.

**Box height depends only on the terminal size**, never on the rail cursor or on whether
`?` is open — so `PlaceOverlay` never re-centers the panel mid-navigation (the jump
`ui/overlay/textInput_size.go:3-8` warns about).

---

## File Structure

| File | Responsibility |
|---|---|
| **Modify** `ui/overlay/settings.go` | The type, constructor, exported API (`SetSize`, `HandleKeyPress`, `SelectRow`, `RailIndex`/`SetRailIndex`, `SetAccountClusteringVisible`), `isModified`, editing machinery, width/height budgets. Loses `renderBody`/`renderFooter`/`renderValue`/`labelColWidth` |
| **Create** `ui/overlay/settings_nav.go` | `settingsFocus`, `railKind`/`railEntry`/`railEntries()`, `rowRange`, the spec §7 key grammar |
| **Create** `ui/overlay/settings_render.go` | The two-pane renderer: `railLines`, `rowsPaneLines`, `rowLineParts`/`composeRowLine`, `enumValueCandidates`, `helpLines`, `contextLine`, `inertReason`, separator/divider, single-pane fallback, `expandedHelpContent`/`expandedHelpLines`, `hintLine` |
| **Create** `ui/overlay/settings_nav_test.go` | Focus transitions, layered `Esc`, `←`/`→`-is-always-the-value, `rowRange`, guard 11 |
| **Create** `ui/overlay/settings_render_test.go` | Guards 4, 5, 6, 7, 10; unstyled overflow; the derived threshold; the visibility signals; expanded help |
| **Modify** `ui/overlay/settings_test.go` | The enumerated adaptations (Task 10 Steps 1–11) |
| **Modify** `ui/theme/theme.go`, `ui/theme/registry.go`, `ui/theme/theme_test.go` | The two new glyphs |
| **Modify** `app/help_legend_test.go` | The **fifth** glyph site: the reflection loop's `excluded` map |
| **Modify** `ui/list.go`, `ui/list_render.go` | Export `AccountClusteringVisible()` as the single definition of the clustering gate |
| **Modify** `app/app_update.go`, `app/app_keys.go`, `app/app_layout.go`, `app/app.go` | Inject the gate, remember the rail across opens |
| **Modify** `docs/superpowers/specs/2026-07-25-configuration-panel-design.md` | Fix §5's `group_mode` row |

`settings.go` is 426 lines and `settings_schema.go` 1045; the split is spec §11's and is
what keeps both halves reviewable.

---

## Task 1: Two new glyphs, across five sites

**Files:**
- Modify: `ui/theme/theme.go:38-63` (the `Glyphs` struct)
- Modify: `ui/theme/registry.go:53-80` (`plainGlyphs`), `:99-132` (`asciiGlyphs`)
- Modify: `ui/theme/theme_test.go:110-132` (`assertGlyphWidths`), `:89-105` (`TestGlyphsForFidelityRungs`)
- Modify: `app/help_legend_test.go:30-37` (the `excluded` map)

**Interfaces:**
- Consumes: nothing.
- Produces: `theme.Glyphs.Modified` and `theme.Glyphs.Handoff`, both width 1 under every
  palette × rung. Every later task's renderer reads them.

> **A new glyph costs FIVE sites, not four.** Beyond the struct, the rungs,
> `assertGlyphWidths` and `TestGlyphsForFidelityRungs`, `app/help_legend_test.go:39-52`
> reflects over **every** `Glyphs` field and requires each one to appear in the `?` legend or
> in the documented `excluded` map. Both new glyphs are panel chrome, not row vocabulary, so
> both go in `excluded` — following the `SelectionMark` / `FoldOpen` precedent already there.
>
> One caveat, established by running it rather than reading it: the loop asserts
> `Contains(legendContent, glyphValue)`, so it only *fails* when the glyph's character is
> absent from the legend prose. `•` is absent, so `Modified` genuinely needs its entry; `→`
> already occurs in the legend, so `Handoff` would pass without one — accidentally, not by
> design. Both are categorized regardless, and Step 7 mutates `Modified` because it is the
> only one of the two that can demonstrate the site.
>
> Note also that `registry.go` has only **one** complete literal, `plainGlyphs()`.
> `nerdGlyphs()` and `asciiGlyphs()` *derive* from it, so a plain-Unicode glyph needs an
> ascii override and usually no nerd override at all.

The rail's current-entry marker **reuses `SelectionMark`** and the pane overflow markers
are text (`↑ 3 more`), so no rail-caret and no scrollbar glyph are needed. That keeps
this task to two fields.

- [ ] **Step 1: Write the failing test**

Append to `ui/theme/theme_test.go`, inside `TestGlyphsForFidelityRungs` (after the ascii
rung block, before the `"bogus-rung"` block):

```go
	// The two settings-panel chrome glyphs (#482's successor, spec §10): the modified
	// marker and the handoff arrow. Both are plain Unicode with an ascii floor, so the
	// ascii rung must override them rather than inherit an arrow that tofus.
	require.Equal(t, "*", Current().Glyphs.Modified, "ascii rung uses a 7-bit modified marker")
	require.Equal(t, ">", Current().Glyphs.Handoff, "ascii rung uses a 7-bit handoff arrow")

	SetGlyphSet(GlyphSetPlain)
	require.Equal(t, "•", Current().Glyphs.Modified, "plain rung uses a bullet")
	require.Equal(t, "→", Current().Glyphs.Handoff, "plain rung uses an arrow")
```

And add the two cells to `assertGlyphWidths`'s `cells` map (`theme_test.go:110`), which
is what enforces width 1 across every palette × rung via `TestGlyphWidths`:

```go
		"Modified":      g.Modified,
		"Handoff":       g.Handoff,
```

- [ ] **Step 2: Run the tests to verify they fail**

```bash
PATH="/home/zvi/.local/share/mise/installs/go/1.26.4/bin:$PATH" \
  go test ./ui/theme/ -run 'TestGlyphWidths|TestGlyphsForFidelityRungs' -v
```
Expected: FAIL to build — `g.Modified undefined`, `g.Handoff undefined`.

- [ ] **Step 3: Add the struct fields**

In `ui/theme/theme.go`, append to the `Glyphs` struct (after `MarkChecked`, keeping the
affordance glyphs together):

```go
	Modified      string // settings row changed from its built-in default
	Handoff       string // settings rail entry whose config lives in another overlay
```

- [ ] **Step 4: Add the rung values**

In `ui/theme/registry.go`, in `plainGlyphs()` (after `MarkChecked`):

```go
		Modified:      "•", // "changed from default" dot; its own column, never SelectionMark's
		Handoff:       "→", // this rail entry hands off to another surface
```

and in `asciiGlyphs()`:

```go
	g.Modified = "*"
	g.Handoff = ">"
```

`nerdGlyphs()` needs no override: a Nerd Font renders both plain runes, and inventing a
PUA icon for a bullet would only risk a width-2 glyph.

Two ascii collisions are deliberate and worth noting in review before someone raises them:
`Modified = "*"` also spells `Ready`, and `Handoff = ">"` also spells `FoldClosed`. Neither
glyph ever shares a frame with its twin — `Modified` and `Handoff` appear only inside the
settings panel, `Ready` and `FoldClosed` only on a session row — so no single screen shows
both meanings. Inventing a distinct 7-bit glyph would mean a less legible one, which is the
opposite of what the ascii rung is a floor *for*.

- [ ] **Step 5: Categorize them in the legend guard**

In `app/help_legend_test.go`, add to the `excluded` map:

```go
		"Modified":      "settings-panel modified marker (panel chrome, not a row status)",
		"Handoff":       "settings-rail handoff arrow (panel chrome, not a row status)",
```

- [ ] **Step 6: Run the tests to verify they pass**

```bash
PATH="/home/zvi/.local/share/mise/installs/go/1.26.4/bin:$PATH" \
  go test ./ui/theme/ ./app/ -run 'TestGlyph|TestLegendCoversRowVocabulary' -v 2>&1 | tail -20
```
Expected: PASS.

- [ ] **Step 7: Verify the width guard and the legend guard both actually guard**

Mandatory. A guard nobody has watched fail proves nothing.

1. Set `Modified: "✅"` in `plainGlyphs()` (a width-2 emoji). Run `TestGlyphWidths`.
   Expected: FAIL — `glyph Modified = "✅" has width 2, want 1`. Restore `"•"`.
2. Delete the `"Modified"` line from `help_legend_test.go`'s `excluded` map. Run
   `TestLegendCoversRowVocabulary`. Expected: FAIL — `row-vocabulary glyph Modified ("•")
   must appear in the legend`. Restore it **by editing the line back**. This is the fifth
   site proving itself.

   **Mutate `Modified`, not `Handoff`.** The loop's assertion is
   `Contains(legendContent, glyphValue)`, and `→` already occurs elsewhere in the legend
   prose — so deleting `Handoff`'s entry leaves the test **passing**, by coincidence rather
   than by design. `•` does not occur there, which is why it is the target that demonstrates
   the site. `Handoff` is still categorized in `excluded`: a copy edit that drops that arrow
   from the legend would otherwise turn this into a surprise failure in an unrelated PR.
3. Re-run both and confirm green.

> **Do not `git checkout <file>` to undo a mutation.** It reverts the file to HEAD, taking
> this task's real edits with it — which is how the first run of this step destroyed both
> new `excluded` entries and had to redo them. Edit the mutated line back instead.

- [ ] **Step 8: Lint and commit**

```bash
PATH="/home/zvi/.local/share/mise/installs/go/1.26.4/bin:$PATH" /home/zvi/go/bin/golangci-lint run ./ui/... ./app/...
git add ui/theme/theme.go ui/theme/registry.go ui/theme/theme_test.go app/help_legend_test.go
git commit -m "feat(theme): modified-marker and handoff glyphs for the settings rail"
```

---

## Task 2: The rail vocabulary

**Files:**
- Create: `ui/overlay/settings_nav.go`
- Create: `ui/overlay/settings_nav_test.go`

**Interfaces:**
- Consumes: `allCategories()`, `settingCategory.label()` from `settings_schema.go`.
- Produces: `railKind` (`railAll`, `railCategory`, `railHandoff`), `railEntry{label,
  note, category, kind}`, `railEntries() []railEntry` (13 entries),
  `railDefaultIndex() int`, `railIndexForCategory(settingCategory) int`. Tasks 3–9 all
  use them.

The rail is its own vocabulary rather than `allCategories()` alone, because three of its
thirteen entries own no rows: `All settings` is a *view* of every row (spec §4 — "All
settings is a pseudo-category, not an assignment"), and `Profiles`/`Accounts` are
handoffs to surfaces PR C and PR D own.

**Both handoff entries render in PR B, deliberately.** They render dimmed with the
handoff glyph and a one-line note naming where that config lives today; `Enter`/`→` on
them is a no-op. Rendering them now is what makes guard 4 test the *real* 13-entry rail —
a PR B that shipped 11 entries would leave PR C and PR D free to overflow the 80×24
budget silently.

- [ ] **Step 1: Write the failing tests**

Create `ui/overlay/settings_nav_test.go`:

```go
package overlay

import (
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRailEntriesAreTheThirteen pins the rail's exact contents and order (spec §3/§4):
// All settings, the ten scalar categories in allCategories() order, then the two
// handoffs. The count is the one TestCategoryCountFitsTheRailBudget already reserves
// budget for, so this is the test that spends it.
func TestRailEntriesAreTheThirteen(t *testing.T) {
	entries := railEntries()
	require.Len(t, entries, 13, "spec §4: ten categories plus All settings, Profiles, Accounts")

	assert.Equal(t, "All settings", entries[0].label)
	assert.Equal(t, railAll, entries[0].kind)

	for i, c := range allCategories() {
		e := entries[i+1]
		assert.Equalf(t, railCategory, e.kind, "entry %d must project a category", i+1)
		assert.Equalf(t, c, e.category, "entry %d must be %q", i+1, c.label())
		assert.Equalf(t, c.label(), e.label, "a rail label must be the category's own label")
	}

	for _, e := range entries[11:] {
		assert.Equalf(t, railHandoff, e.kind, "%q must be a handoff", e.label)
	}
	assert.Equal(t, "Profiles", entries[11].label)
	assert.Equal(t, "Accounts", entries[12].label)
}

// TestEveryHandoffEntryNamesItsSurface pins that a rail entry owning no rows still says
// where its config lives. An entry that renders an empty pane teaches the user nothing
// and reads as a bug; PR C wires Accounts to the @ overlay and PR D builds the Profiles
// editor, so until then the note is the whole content of that pane.
func TestEveryHandoffEntryNamesItsSurface(t *testing.T) {
	handoffs := 0
	for _, e := range railEntries() {
		if e.kind != railHandoff {
			assert.Emptyf(t, e.note, "only a handoff entry carries a note: %q", e.label)
			continue
		}
		handoffs++
		assert.NotEmptyf(t, e.note, "handoff entry %q must name the surface that owns it", e.label)
	}
	// Without this the loop could stop running and the test would still pass.
	require.Equal(t, 2, handoffs, "Profiles and Accounts are the two handoffs")
}

// TestRailDefaultIndexIsTheFirstCategory pins the landing entry. Spec §4 is explicit
// that All settings is NOT the default landing — it is the flat audit view, preserved
// for muscle memory, not the browsing default. Derived rather than a literal so
// reordering the rail cannot silently land the panel on a handoff.
func TestRailDefaultIndexIsTheFirstCategory(t *testing.T) {
	entries := railEntries()
	i := railDefaultIndex()
	require.Less(t, i, len(entries))
	assert.Equal(t, railCategory, entries[i].kind, "the panel must not land on a view or a handoff")
	assert.Equal(t, "Sessions", entries[i].label)
}

// TestRailIndexForCategoryFindsEveryCategory pins that every category is reachable from
// its enum value, which is what SelectRow (and PR C's OpenAt) rely on to sync the rail
// to a deep-linked row.
func TestRailIndexForCategoryFindsEveryCategory(t *testing.T) {
	for _, c := range allCategories() {
		i := railIndexForCategory(c)
		e := railEntries()[i]
		assert.Equalf(t, railCategory, e.kind, "category %q resolved to a non-category entry", c.label())
		assert.Equalf(t, c, e.category, "category %q resolved to entry %q", c.label(), e.label)
	}
}

// TestRailWidthTracksItsLongestLabel pins that railWidth() is MEASURED rather than written
// down, which is what makes the degradation threshold move when a category is renamed
// (Task 6). Asserting that each label fits railWidth() would be a tautology — railWidth()
// is defined as the max of those very labels — so what is pinned here is the derivation,
// and Task 5's TestRailRendersEveryLabelWhole pins that nothing truncates in practice.
func TestRailWidthTracksItsLongestLabel(t *testing.T) {
	widest, label := -1, ""
	for _, e := range railEntries() {
		if n := ansi.StringWidth(e.label); n > widest {
			widest, label = n, e.label
		}
	}
	assert.Equal(t, railMarkerCells+widest+railTrailCells, railWidth())
	assert.Equal(t, "Worktrees & git", label,
		"the widest rail label today; if this changed, the threshold moved with it")
}
```

- [ ] **Step 2: Run the tests to verify they fail**

```bash
PATH="/home/zvi/.local/share/mise/installs/go/1.26.4/bin:$PATH" \
  go test ./ui/overlay/ -run 'TestRail|TestEveryHandoff' -v 2>&1 | head -20
```
Expected: FAIL to build — `undefined: railEntries`, `undefined: railWidth`.

- [ ] **Step 3: Create `settings_nav.go` with the rail vocabulary**

Task 3 adds key handlers to this file, so it will need
`tea "github.com/charmbracelet/bubbletea"` then — but **not yet**: an unused import does not
compile. Add it in Task 3, with the handlers.

```go
package overlay

// railKind distinguishes the three things a rail entry can be. Ten of the thirteen
// entries project a settingCategory; the other three own no rows of their own, which is
// why the rail is its own vocabulary rather than allCategories() alone.
type railKind int

const (
	// railAll shows every row grouped under category headers — the shape of the
	// pre-redesign list, preserved for auditing and muscle memory (spec §4). It is the
	// one entry whose pane scrolls far, and the only one that is a *view* rather than an
	// assignment: each row still belongs to exactly one real category.
	railAll railKind = iota
	// railCategory shows one category's rows.
	railCategory
	// railHandoff owns no rows: that config lives on another surface. PR B renders these
	// dimmed with the handoff glyph and their note; PR C wires Accounts to the @ overlay
	// and PR D builds the Profiles editor.
	railHandoff
)

// railEntry is one line of the left rail.
type railEntry struct {
	label string
	kind  railKind
	// category is the rows this entry shows; meaningful only when kind == railCategory.
	category settingCategory
	// note is the single line a handoff entry's pane shows, naming the surface that owns
	// its config. Empty for every other kind (TestEveryHandoffEntryNamesItsSurface).
	note string
}

// railEntries returns the rail in display order: the flat view, the ten scalar
// categories, then the two handoffs. Thirteen entries fit the 80x24 pane budget exactly
// (spec §4's invariant, pinned by TestRailFitsUnscrolledAtTheFloor) — a fourteenth has
// to displace another rather than start the rail scrolling.
func railEntries() []railEntry {
	entries := make([]railEntry, 0, len(allCategories())+3)
	entries = append(entries, railEntry{label: "All settings", kind: railAll})
	for _, c := range allCategories() {
		entries = append(entries, railEntry{label: c.label(), kind: railCategory, category: c})
	}
	return append(entries,
		railEntry{
			label: "Profiles", kind: railHandoff,
			// Stated as a plain fact about where the data lives, not as a roadmap promise:
			// PR D replaces this entry with an editor, and a note saying "not yet" would be
			// the first thing to go stale.
			note: "Agent profiles are edited in config.json, under the profiles key.",
		},
		railEntry{
			label: "Accounts", kind: railHandoff,
			note: "Managed in the accounts overlay — press @ from the session list.",
		},
	)
}

// railDefaultIndex is the entry the panel opens on: the first real category. Spec §4 is
// explicit that All settings is not the landing — it is the audit view, not the default
// way to browse. Derived rather than hardcoded so reordering the rail cannot land the
// panel on a handoff.
func railDefaultIndex() int {
	for i, e := range railEntries() {
		if e.kind == railCategory {
			return i
		}
	}
	return 0
}

// railIndexForCategory returns the rail index showing the given category, falling back
// to the All settings view when no entry claims it — which cannot happen while
// TestRailIndexForCategoryFindsEveryCategory holds.
func railIndexForCategory(c settingCategory) int {
	for i, e := range railEntries() {
		if e.kind == railCategory && e.category == c {
			return i
		}
	}
	return 0
}
```

- [ ] **Step 4: Add the rail geometry to `settings_render.go`**

Create `ui/overlay/settings_render.go` with only the rail's width for now (later tasks
fill it in):

```go
package overlay

import (
	"github.com/charmbracelet/x/ansi"
)

// Rail line geometry: [selection 1][space 1][label ...][space 1][handoff 1].
const (
	railMarkerCells = 2 // the selection mark and the space after it
	railTrailCells  = 2 // the space before the handoff cell, and the cell itself
)

// railWidth is the rail's fixed width: the widest rail label plus its marker and
// handoff cells. Derived from railEntries() rather than a literal, so renaming a
// category moves the rail — and, through twoPaneMinInner, the degradation threshold with
// it (spec §10: the threshold "must be computed from the parts, not hardcoded").
func railWidth() int {
	w := 0
	for _, e := range railEntries() {
		if n := ansi.StringWidth(e.label); n > w {
			w = n
		}
	}
	return railMarkerCells + w + railTrailCells
}
```

- [ ] **Step 5: Run the tests to verify they pass**

```bash
PATH="/home/zvi/.local/share/mise/installs/go/1.26.4/bin:$PATH" \
  go test ./ui/overlay/ -run 'TestRail|TestEveryHandoff' -v
```
Expected: PASS (5 tests). `railWidth()` should be 19; confirm by adding a temporary
`t.Log(railWidth())` if you want to see it, then remove it.

- [ ] **Step 6: Verify the guards fail when they should**

1. Add a fourteenth entry to `railEntries()`. Run the tests. Expected:
   `TestRailEntriesAreTheThirteen` FAILS on the length. Remove it.
2. Clear `Accounts`'s `note`. Expected: `TestEveryHandoffEntryNamesItsSurface` FAILS.
   Restore it.
3. Move `All settings` to the end of `railEntries()`. Expected:
   `TestRailEntriesAreTheThirteen` FAILS *and* `TestRailDefaultIndexIsTheFirstCategory`
   still passes (it is derived) — which is the point of deriving it. Restore the order.
4. Re-run and confirm green.

- [ ] **Step 7: Lint and commit**

```bash
PATH="/home/zvi/.local/share/mise/installs/go/1.26.4/bin:$PATH" /home/zvi/go/bin/golangci-lint run ./ui/...
git add ui/overlay/settings_nav.go ui/overlay/settings_render.go ui/overlay/settings_nav_test.go
git commit -m "feat(settings): the thirteen-entry rail vocabulary"
```

---

## Task 3: Focus, cursors, and the navigation grammar

**Files:**
- Modify: `ui/overlay/settings.go:50-71` (struct + constructor), `:73-83` (`SelectRow`),
  `:104-145` (`HandleKeyPress`)
- Modify: `ui/overlay/settings_nav.go` (the key handlers)
- Modify: `ui/overlay/settings_nav_test.go`
- Modify: `ui/overlay/settings_test.go:514-531` (`TestSettingsOverlay_NavigationClampsAtEnds`)

**Interfaces:**
- Consumes: Task 2's rail vocabulary.
- Produces: `settingsFocus` (`focusRail`, `focusRows`); `SettingsOverlay.focus`,
  `.railCursor`; `(*SettingsOverlay).rowRange(railEntry) (start, end int)`;
  `.selectedEntry() railEntry`; `.selectedRow() settingRow`; `.syncCursorToRail()`;
  `SelectRow` with its new composite behavior. Tasks 5–9 render against these.

**`?` is not wired here.** The expanded-help state (`helpOpen`, `helpScroll`), its key
handler and its `?` entry point all land together in Task 8. Splitting them would leave
this task with an enterable state nothing renders, or unread struct fields the `unused`
linter flags — either way the package would not be green at a task boundary, which is the
one thing every task must guarantee.

**`s.cursor` stays a global index into `s.rows`.** The rows pane derives its visible
slice from the rail entry; navigation moves `cursor±1` bounded by that entry's
`[start,end)`. This is safe because `TestRowsAreGroupedByCategory`
(`settings_schema_test.go:233`) already pins that every category's rows form one
unbroken block in `allCategories()` order. Keeping the global index is what preserves
`SelectRow`, `isModified(i)`, and every existing test's mental model.

- [ ] **Step 1: Write the failing tests**

Append to `ui/overlay/settings_nav_test.go`:

```go
// TestRowRangeCoversEveryRowExactlyOnce pins that the category entries partition the row
// slice and the All settings view spans all of it. A gap would make a row unreachable
// from the rail; an overlap would show it twice.
func TestRowRangeCoversEveryRowExactlyOnce(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())

	start, end := o.rowRange(railEntries()[0])
	assert.Equal(t, 0, start, "All settings starts at the first row")
	assert.Equal(t, len(o.rows), end, "All settings spans every row")

	seen := make([]int, len(o.rows))
	for _, e := range railEntries() {
		if e.kind != railCategory {
			continue
		}
		s, en := o.rowRange(e)
		require.Lessf(t, s, en, "category %q has no rows", e.label)
		for i := s; i < en; i++ {
			seen[i]++
		}
	}
	for i, n := range seen {
		assert.Equalf(t, 1, n, "row %q is claimed by %d category entries", o.rows[i].key, n)
	}
}

// TestHandoffEntryHasNoRows pins that a handoff's range is empty, which is what makes
// →/Tab/Enter a no-op on it rather than focusing an empty pane.
func TestHandoffEntryHasNoRows(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	for _, e := range railEntries() {
		if e.kind != railHandoff {
			continue
		}
		start, end := o.rowRange(e)
		assert.Equalf(t, end, start, "handoff entry %q must own no rows", e.label)
	}
}

// TestPanelOpensOnTheRail pins the initial focus and cursor: the rail, on the first real
// category, with the row cursor pulled into it. Opening focused on the rows pane would
// make ↑/↓ move rows before the user has chosen a category.
func TestPanelOpensOnTheRail(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	assert.Equal(t, focusRail, o.focus)
	assert.Equal(t, railDefaultIndex(), o.railCursor)

	start, end := o.rowRange(o.selectedEntry())
	assert.GreaterOrEqual(t, o.cursor, start)
	assert.Less(t, o.cursor, end, "the row cursor must start inside the landing category")
}

// TestRailNavigationMovesTheRailNotTheRows pins that ↑/↓ on the rail change the category
// and pull the row cursor with them — the live-preview behavior of spec §3, where moving
// the rail immediately re-renders the right pane so there is no hidden state.
func TestRailNavigationMovesTheRailNotTheRows(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	before := o.railCursor

	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyDown})
	assert.Equal(t, before+1, o.railCursor)
	start, end := o.rowRange(o.selectedEntry())
	assert.GreaterOrEqualf(t, o.cursor, start, "the row cursor must follow the rail")
	assert.Less(t, o.cursor, end)

	// j/k navigate too.
	o.HandleKeyPress(keyRunes("k"))
	assert.Equal(t, before, o.railCursor)
}

// TestRailNavigationClampsAtEnds pins that the rail does not wrap: at the top, up is a
// no-op; at the bottom, down is.
func TestRailNavigationClampsAtEnds(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	for range railEntries() {
		o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyUp})
	}
	assert.Equal(t, 0, o.railCursor, "up at the top clamps")

	for range railEntries() {
		o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyDown})
	}
	assert.Equal(t, len(railEntries())-1, o.railCursor, "down at the bottom clamps")
}

// TestRowNavigationStaysWithinTheCategory pins that ↑/↓ in the rows pane cannot walk out
// of the visible category — the cursor would leave the pane and the help text would
// describe a row nobody can see.
func TestRowNavigationStaysWithinTheCategory(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	settingsAt(t, o, "theme") // Appearance: 5 rows
	start, end := o.rowRange(o.selectedEntry())
	require.Equal(t, start, o.cursor, "theme is Appearance's first row")

	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyUp})
	assert.Equal(t, start, o.cursor, "up at the category's first row clamps")

	for range o.rows {
		o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyDown})
	}
	assert.Equal(t, end-1, o.cursor, "down stops at the category's last row")
}

// TestArrowsAreAlwaysTheValueNeverAPaneSwitch pins spec §7's one real collision. ←/→
// cycle enum values — that is today's grammar and what the hint line advertises — so
// they cannot double as "switch pane". Tab does that instead.
func TestArrowsAreAlwaysTheValueNeverAPaneSwitch(t *testing.T) {
	cfg := config.DefaultConfig()
	o := NewSettingsOverlay(cfg)
	settingsAt(t, o, "notifications")
	require.Equal(t, focusRows, o.focus)

	_, changed := o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRight})
	assert.Equal(t, "notifications", changed, "→ must cycle the value")
	assert.Equal(t, focusRows, o.focus, "→ must not move focus")

	_, changed = o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyLeft})
	assert.Equal(t, "notifications", changed, "← must cycle the value")
	assert.Equal(t, focusRows, o.focus, "← must not move focus")
}

// TestTabSwitchesPanes pins the key that does move focus, in both directions.
func TestTabSwitchesPanes(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	require.Equal(t, focusRail, o.focus)

	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyTab})
	assert.Equal(t, focusRows, o.focus)
	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyTab})
	assert.Equal(t, focusRail, o.focus)
	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyTab})
	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyShiftTab})
	assert.Equal(t, focusRail, o.focus, "shift+tab switches panes too")
}

// TestRightFocusesTheRowsPaneFromTheRail pins the rail's forward keys. On a handoff entry
// they are no-ops: there are no rows to focus, and PR C is what wires Enter to the
// accounts overlay.
func TestRightFocusesTheRowsPaneFromTheRail(t *testing.T) {
	for _, key := range []tea.KeyMsg{
		{Type: tea.KeyRight}, {Type: tea.KeyTab}, {Type: tea.KeyEnter},
	} {
		o := NewSettingsOverlay(config.DefaultConfig())
		o.HandleKeyPress(key)
		assert.Equalf(t, focusRows, o.focus, "%v must focus the rows pane", key)
	}

	o := NewSettingsOverlay(config.DefaultConfig())
	o.railCursor = len(railEntries()) - 1 // Accounts, a handoff
	require.Equal(t, railHandoff, o.selectedEntry().kind)
	closed, changed := o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRight})
	assert.Equal(t, focusRail, o.focus, "a handoff entry has no rows to focus")
	assert.False(t, closed)
	assert.Empty(t, changed)
}

// TestEscIsLayered pins spec §7's layered Esc: from the rows pane it backs out to the
// rail, and only a second Esc closes. The hint line says "esc back" in the rows pane and
// "esc close" on the rail, so the extra level is advertised rather than surprising
// (spec §15).
func TestEscIsLayered(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	settingsAt(t, o, "theme")
	require.Equal(t, focusRows, o.focus)

	closed, _ := o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEsc})
	assert.False(t, closed, "the first esc backs out of the rows pane")
	assert.Equal(t, focusRail, o.focus)

	closed, _ = o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEsc})
	assert.True(t, closed, "the second esc closes the panel")
}

// TestSelectRowFocusesTheRowsPaneAndSyncsTheRail is spec §13's guard 11 in the form PR B
// can test it: the deep-link primitive lands the cursor on the row with the rows pane
// focused and the rail showing that row's category. Selecting a row the pane is not
// showing would leave the cursor invisible.
//
// PR C promotes this exact behavior to OpenAt(category, key) and adds the two real call
// sites (the session-cap dialog and the manual-reorder notice); the behavior is proven
// here so that promotion is a rename rather than new semantics.
func TestSelectRowFocusesTheRowsPaneAndSyncsTheRail(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	for _, r := range newSettingRows(config.DefaultConfig()) {
		require.Truef(t, o.SelectRow(r.key), "no row %q", r.key)
		assert.Equalf(t, focusRows, o.focus, "SelectRow(%q) must focus the rows pane", r.key)
		assert.Equalf(t, r.category, o.selectedEntry().category,
			"SelectRow(%q) must sync the rail to its category", r.key)
		start, end := o.rowRange(o.selectedEntry())
		assert.GreaterOrEqualf(t, o.cursor, start, "SelectRow(%q) left the cursor outside the pane", r.key)
		assert.Lessf(t, o.cursor, end, "SelectRow(%q) left the cursor outside the pane", r.key)
	}
	assert.False(t, o.SelectRow("not_a_row"), "an unknown key reports not-found")
}
```

Add `"github.com/ZviBaratz/atrium/config"` and `tea "github.com/charmbracelet/bubbletea"`
to the test imports.

- [ ] **Step 2: Run the tests to verify they fail**

```bash
PATH="/home/zvi/.local/share/mise/installs/go/1.26.4/bin:$PATH" \
  go test ./ui/overlay/ -run 'TestRowRange|TestHandoff|TestPanelOpens|TestRailNavigation|TestRowNavigation|TestArrowsAre|TestTabSwitches|TestRightFocuses|TestEscIsLayered|TestSelectRowFocuses' -v 2>&1 | head -20
```
Expected: FAIL to build — `o.focus undefined`, `undefined: focusRail`.

- [ ] **Step 3: Add the focus state to the struct and constructor**

In `ui/overlay/settings.go`, replace the struct and constructor:

```go
// SettingsOverlay is the in-TUI configuration panel: a rail of categories beside the
// highlighted category's rows, edited in place. It mutates the *live* Config it was
// constructed with; the home model persists and live-applies after each change (see
// HandleKeyPress's changedKey return).
type SettingsOverlay struct {
	rows   []settingRow
	cfg    *config.Config
	cursor int // index into rows; global, not per category

	// focus selects which pane consumes navigation keys. railCursor indexes
	// railEntries(); the rows pane shows whatever that entry owns.
	focus      settingsFocus
	railCursor int

	width, height int

	editing bool
	input   textinput.Model
	lastErr string

	// clusteringVisible is home's answer to "does ui.List currently render account
	// clusters?" — nil until home injects it. It is a *bool rather than a bool because
	// nil must mean "unknown, show no chip": group_mode's honest gate is session-derived
	// and a panel that cannot see the session list must not guess. See
	// SetAccountClusteringVisible and TestGroupModeHasNoConfigOnlyInertPredicate.
	clusteringVisible *bool
}

// NewSettingsOverlay builds the settings panel over the given live config, focused on
// the rail at its default category.
func NewSettingsOverlay(cfg *config.Config) *SettingsOverlay {
	s := &SettingsOverlay{
		rows:       newSettingRows(cfg),
		cfg:        cfg,
		focus:      focusRail,
		railCursor: railDefaultIndex(),
		// Sensible floor so Render works before the first SetSize.
		width:  80,
		height: 24,
	}
	s.syncCursorToRail()
	return s
}
```

- [ ] **Step 4: Add the focus type, selectors and range helper to `settings_nav.go`**

```go
// settingsFocus selects which pane consumes navigation keys. It is a closed pair rather
// than a bool so the switch statements read as what they are.
type settingsFocus int

const (
	focusRail settingsFocus = iota
	focusRows
)

// selectedEntry is the rail entry the cursor is on.
func (s *SettingsOverlay) selectedEntry() railEntry { return railEntries()[s.railCursor] }

// selectedRow is the row the rows pane has highlighted.
func (s *SettingsOverlay) selectedRow() settingRow { return s.rows[s.cursor] }

// rowRange returns the [start,end) slice of s.rows the given rail entry shows.
//
// Contiguity is safe to rely on: TestRowsAreGroupedByCategory pins that every category's
// rows form one unbroken block in allCategories() order, which is also what lets the
// rows pane bound ↑/↓ with two integers instead of a filtered slice.
func (s *SettingsOverlay) rowRange(e railEntry) (start, end int) {
	switch e.kind {
	case railAll:
		return 0, len(s.rows)
	case railCategory:
		start, end = -1, -1
		for i, r := range s.rows {
			if r.category != e.category {
				continue
			}
			if start < 0 {
				start = i
			}
			end = i + 1
		}
		if start < 0 {
			return 0, 0
		}
		return start, end
	}
	return 0, 0 // railHandoff owns no rows
}

// syncCursorToRail pulls the row cursor into the current entry's range, so moving the
// rail leaves the rows pane with a valid selection and the help pane describing a row
// that is actually visible. A handoff entry owns no rows, so the cursor is left where it
// was rather than clamped to a meaningless index.
func (s *SettingsOverlay) syncCursorToRail() {
	s.lastErr = ""
	start, end := s.rowRange(s.selectedEntry())
	if end <= start {
		return
	}
	if s.cursor < start || s.cursor >= end {
		s.cursor = start
	}
}
```

Note what `syncCursorToRail` does *not* do: entering **All settings** preserves the
cursor, because its range spans every row. Moving from `Appearance` to `All settings` and
back therefore keeps your place — which is why the flat view is useful for auditing
rather than a separate mode you get lost in.

- [ ] **Step 5: Split `HandleKeyPress` by focus**

Replace `settings.go:104-145` with a router, and put the two handlers in
`settings_nav.go`:

```go
// HandleKeyPress processes one key press. It reports whether the panel should close,
// and — when a value changed — the changed row's key so the home model can persist the
// config and run that field's live-apply hook.
//
// The order of these guards is the grammar: an open editor swallows everything (so j/k
// type rather than navigate), then the focused pane. Task 8 inserts the expanded-help
// view between them.
func (s *SettingsOverlay) HandleKeyPress(msg tea.KeyMsg) (closed bool, changedKey string) {
	switch {
	case s.editing:
		return false, s.handleEditKey(msg)
	case s.focus == focusRail:
		return s.handleRailKey(msg), ""
	default:
		return s.handleRowsKey(msg)
	}
}
```

In `settings_nav.go`:

```go
// handleRailKey routes a key while the rail has focus. Moving the cursor re-renders the
// right pane immediately — the rail live-previews, so there is no hidden state and no
// drill-in feeling on a wide terminal (spec §3).
func (s *SettingsOverlay) handleRailKey(msg tea.KeyMsg) (closed bool) {
	switch msg.String() {
	case "esc", "ctrl+c":
		return true
	case "up", "k":
		if s.railCursor > 0 {
			s.railCursor--
			s.syncCursorToRail()
		}
	case "down", "j":
		if s.railCursor < len(railEntries())-1 {
			s.railCursor++
			s.syncCursorToRail()
		}
	case "right", "tab", "enter":
		// A handoff entry owns no rows, so there is nothing to focus. PR C wires Enter
		// on Accounts to the @ overlay; until then this is deliberately a no-op rather
		// than focus on an empty pane.
		if start, end := s.rowRange(s.selectedEntry()); end > start {
			s.focus = focusRows
		}
	}
	return false
}

// handleRowsKey routes a key while the rows pane has focus. ←/→ are ALWAYS the value —
// spec §7's one real collision: they cycle enums today and the hint line says so, so
// they cannot double as a pane switch. Tab does that.
func (s *SettingsOverlay) handleRowsKey(msg tea.KeyMsg) (closed bool, changedKey string) {
	start, end := s.rowRange(s.selectedEntry())
	if end <= start {
		// Defensive: focus can only reach focusRows on an entry that owns rows.
		s.focus = focusRail
		return false, ""
	}
	row := &s.rows[s.cursor]
	switch msg.String() {
	case "esc", "ctrl+c":
		// Layered: back to the rail first, close from there. Advertised as "esc back".
		s.focus = focusRail
		s.lastErr = ""
	case "tab", "shift+tab":
		s.focus = focusRail
	case "up", "k":
		if s.cursor > start {
			s.cursor--
			s.lastErr = ""
		}
	case "down", "j":
		if s.cursor < end-1 {
			s.cursor++
			s.lastErr = ""
		}
	case "pgup", "pgdown", "home", "end":
		// The rest of D3: reaching the last row of the old flat list took 36 keypresses,
		// and the rail only fixes the "jump to a section" half. Spec §7's table omits these,
		// but D3 names them explicitly, and handleHelpKey already scrolls with the same keys
		// — a panel where PgDn works in the help view and not in the list is just
		// inconsistent.
		s.cursor = clamp(s.pagedCursor(msg.String(), start, end), start, end-1)
		s.lastErr = ""
	case "left":
		return false, s.cycleEnum(row, -1)
	case "right":
		return false, s.cycleEnum(row, +1)
	case " ":
		if row.kind == kindBool {
			return false, s.toggleBool(row)
		}
	case "enter":
		switch row.kind {
		case kindBool:
			return false, s.toggleBool(row)
		case kindEnum:
			return false, s.cycleEnum(row, +1)
		case kindInt, kindText:
			s.startEdit(row)
		}
	}
	return false, ""
}

// pagedCursor resolves a paging key to a target row index within [start,end). It is a
// separate function only so the four keys read as one rule instead of four cases.
func (s *SettingsOverlay) pagedCursor(key string, start, end int) int {
	page := max(1, s.paneHeight()-1) // overlap one row so context is never lost
	switch key {
	case "pgup":
		return s.cursor - page
	case "pgdown":
		return s.cursor + page
	case "home":
		return start
	default: // "end"
		return end - 1
	}
}
```

`paneHeight` arrives in Task 5. To keep this task's package green on its own, write
`pagedCursor` with a literal page size of `10` now and switch it to `s.paneHeight()-1` in
Task 5 Step 4 — or implement Task 5 before running the full suite. Do not leave a stub
behind.

Task 8 adds the `case "?":` arm here alongside the view it opens.

- [ ] **Step 6: Give `SelectRow` its composite behavior**

Replace `settings.go:73-83`:

```go
// SelectRow moves the cursor onto the row with the given key, reporting whether it
// exists. It also syncs the rail to that row's category and focuses the rows pane:
// selecting a row the pane is not showing would leave the cursor invisible.
//
// That composite behavior is the deep-link contract — it is what makes a jump from a
// dialog or a notice land somewhere usable — and PR C promotes it to
// OpenAt(category, key) with two real call sites. It is also what keeps the ~40 tests
// that reach a row through settingsAt working: they select a row, then send keys
// expecting them to reach it.
func (s *SettingsOverlay) SelectRow(key string) bool {
	for i, r := range s.rows {
		if r.key != key {
			continue
		}
		s.cursor = i
		s.railCursor = railIndexForCategory(r.category)
		s.focus = focusRows
		s.lastErr = ""
		return true
	}
	return false
}
```

- [ ] **Step 7: Rewrite the one existing nav test**

`TestSettingsOverlay_NavigationClampsAtEnds` (`settings_test.go:514`) walks `o.cursor`
across every row from a fresh overlay — which now moves the *rail*. Replace it with a
pointer to the two focused replacements, so the coverage is not silently dropped:

```go
// Navigation clamping now lives in settings_nav_test.go, split by pane:
// TestRailNavigationClampsAtEnds and TestRowNavigationStaysWithinTheCategory. A single
// test cannot cover both any more — ↑/↓ mean "category" on the rail and "row" in the
// rows pane, and a fresh overlay opens on the rail (TestPanelOpensOnTheRail).
```

Delete the old test body. This is the one test in the suite whose *subject* no longer
exists; every other adaptation in Task 10 keeps its subject and changes its expectations.

- [ ] **Step 8: Run the tests to verify they pass**

```bash
PATH="/home/zvi/.local/share/mise/installs/go/1.26.4/bin:$PATH" \
  go test ./ui/overlay/ -run 'TestRowRange|TestHandoff|TestPanelOpens|TestRailNavigation|TestRowNavigation|TestArrowsAre|TestTabSwitches|TestRightFocuses|TestEscIsLayered|TestSelectRowFocuses' -v 2>&1 | tail -30
```
Expected: PASS (11 tests). The rest of the package will not be green until Task 5
replaces the renderer — that is expected and Task 10 owns the cleanup.

- [ ] **Step 9: Verify the collision guard actually guards — this is the important one**

Spec §7 names `←`/`→` as the grammar's one real hazard. Prove the guard catches it:

1. In `handleRowsKey`, change `case "left":` to `case "left": s.focus = focusRail; return false, ""`.
2. Run:
   ```bash
   PATH="/home/zvi/.local/share/mise/installs/go/1.26.4/bin:$PATH" \
     go test ./ui/overlay/ -run 'TestArrowsAreAlways|TestSettingsOverlay_Cycle' -v 2>&1 | tail -20
   ```
   Expected: `TestArrowsAreAlwaysTheValueNeverAPaneSwitch` FAILS on the `←` assertion,
   **and** `TestSettingsOverlay_CycleThemeWraps` fails on its `left` step. Two
   independent nets, which is why the collision cannot ship.
3. Revert the mutation and re-run. **Revert by editing the line back — never
   `git checkout <file>`**, which would discard the task's real work alongside the
   mutation.

4. Second mutation: delete `s.focus = focusRows` from `SelectRow`. Expected:
   `TestSelectRowFocusesTheRowsPaneAndSyncsTheRail` FAILS for the first row. Revert.

- [ ] **Step 10: Lint and commit**

```bash
PATH="/home/zvi/.local/share/mise/installs/go/1.26.4/bin:$PATH" /home/zvi/go/bin/golangci-lint run ./ui/...
git add ui/overlay/settings.go ui/overlay/settings_nav.go ui/overlay/settings_nav_test.go ui/overlay/settings_test.go
git commit -m "feat(settings): two-pane focus model and navigation grammar"
```

---

## Task 4: The row line composer and its truncation ladder

**Files:**
- Modify: `ui/overlay/settings_render.go`
- Create: `ui/overlay/settings_render_test.go`

**Interfaces:**
- Consumes: `padRight` (already in `ui/overlay/accounts.go:733` — **reuse it, do not write
  a second one**).
- Produces: `rowLineParts{head, value, gap, badge}` with `.plain() string`;
  `composeRowLine(width, labelW int, sel, modified, label, value, badge string)
  rowLineParts`; the constants `rowMarkerCells`, `rowLabelGap`, `rowMinValueCells`,
  `paneDividerCells`. Tasks 5 and 7 render through it.

This task exists as its own unit because the truncation ladder is the one piece of layout
arithmetic that can be tested exactly, in isolation, on plain text — and because guard 6
is otherwise untestable. **A post-render width assertion is a tautology:** the bordered
lipgloss block pads every line to the same width, so `lipgloss.Width(line) <= boxWidth`
passes whether or not the content overflows (it silently soft-wraps instead, growing the
box). The honest guard measures this function's output.

- [ ] **Step 1: Write the failing tests**

Create `ui/overlay/settings_render_test.go`:

```go
package overlay

import (
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/config"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestComposeRowLineIsExactlyThePaneWidth pins the invariant every later width guard
// rests on: whatever the inputs, the composed line fills the pane exactly. Short of it
// and the badge stops being right-aligned; over it and the lipgloss box soft-wraps the
// line, grows a row, and pushes the pinned hint off the bottom.
func TestComposeRowLineIsExactlyThePaneWidth(t *testing.T) {
	cases := []struct {
		name                string
		width, labelW       int
		label, value, badge string
	}{
		{"roomy", 52, 26, "Session limit", "auto (8)", "live"},
		{"exact fit", 40, 20, "Notify command", "(built-in)", "live"},
		{"badge must go", 34, 26, "Smart dispatch auto-create", "[ ] off", "new sessions"},
		{"value must shrink", 36, 26, "Release notes after update", "a-very-long-value-indeed", "restart"},
		{"no badge at all", 44, 12, "Theme", "‹ atrium ›", ""},
		{"wide value, no badge", 30, 11, "Carry files", ".claude/settings.local.json", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := composeRowLine(tc.width, tc.labelW, "▎", "•", tc.label, tc.value, tc.badge)
			assert.Equal(t, tc.width, ansi.StringWidth(p.plain()),
				"composed line must be exactly the pane width: %q", p.plain())
		})
	}
}

// TestComposeRowLineDropsTheBadgeBeforeTruncatingTheValue pins spec §10's priority order.
// Both signals compete for the same slack; dropping the badge costs the user a timing
// note they can still read in the help pane, while truncating first would hide part of
// the value for no reason.
func TestComposeRowLineDropsTheBadgeBeforeTruncatingTheValue(t *testing.T) {
	const (
		width  = 50
		labelW = 20
		value  = "‹off› bell desktop osc"
		badge  = "new sessions"
	)
	// TWO preconditions, because the case only exercises the priority order when both
	// hold: the badge must not fit, AND the value must fit once it is gone. With only the
	// first, the value is truncated too and the second assertion below is impossible —
	// which is exactly how the first draft of this test was unpassable.
	avail := width - (rowMarkerCells + labelW + rowLabelGap)
	require.Greater(t, ansi.StringWidth(value)+ansi.StringWidth(badge)+1, avail,
		"the badge must not fit in the %d-cell slack, or nothing is dropped", avail)
	require.LessOrEqual(t, ansi.StringWidth(value), avail,
		"the value must fit once the badge is gone, or this tests truncation instead")

	p := composeRowLine(width, labelW, "▎", " ", "Notifications", value, badge)
	assert.Empty(t, p.badge, "the badge is dropped first")
	assert.Equal(t, value, p.value, "the value survives intact once the badge is gone")
}

// TestComposeRowLineTruncatesTheValueWithATailEllipsis pins the second rung: when even a
// badgeless line cannot hold the value, it loses its tail rather than its head — the
// start of a value is what identifies it.
func TestComposeRowLineTruncatesTheValueWithATailEllipsis(t *testing.T) {
	p := composeRowLine(30, 11, "▎", " ", "Carry files", ".claude/settings.local.json", "live")
	assert.Empty(t, p.badge)
	assert.True(t, strings.HasSuffix(p.value, "…"), "value must be tail-ellipsized: %q", p.value)
	assert.True(t, strings.HasPrefix(p.value, ".claude"), "the head of the value must survive: %q", p.value)
	assert.Equal(t, 30, ansi.StringWidth(p.plain()))
}

// TestComposeRowLineNeverTruncatesTheLabelThatFits pins the rule spec §10 states most
// emphatically: a half-written label makes the row unidentifiable, so the label is the last
// column sacrificed. The value goes first, then it goes short — but as long as the label
// column fits the pane, it is rendered whole.
//
// The labelW values are deliberately SMALLER than the label, which is where padRight cannot
// save the test: the first draft passed labelW = len(label) and therefore proved only that
// padRight does not truncate, which it never does.
func TestComposeRowLineNeverTruncatesTheLabelThatFits(t *testing.T) {
	const label = "Smart dispatch auto-create" // 26 cells
	for _, tc := range []struct{ width, labelW int }{
		{52, 26}, {40, 26}, {34, 26}, {31, 26}, // labelW == the label
		{52, 12}, {40, 10}, {34, 8}, // labelW UNDER it: padRight must not clip either
	} {
		p := composeRowLine(tc.width, tc.labelW, "▎", "•", label, "[ ] off", "live")
		assert.Containsf(t, p.head, label,
			"label truncated at width %d / labelW %d: %q", tc.width, tc.labelW, p.head)
		assert.NotContainsf(t, p.head, "…",
			"no ellipsis may appear in the label column (width %d)", tc.width)
	}
}

// TestComposeRowLineNeverExceedsThePaneEvenWhenTheLabelCannotFit pins the floor below which
// the label rule yields. Below rowMarkerCells + label + rowLabelGap there is no line that
// both shows the label whole and fits the pane, and an over-wide line is the worse failure:
// lipgloss soft-wraps it, the box grows a row, and the pinned hint gets clipped off the
// bottom.
//
// Hard-clipping there is parity with the pre-PR-B renderer, which truncated every body line
// to the inner width (settings.go:318) — not a new regression.
func TestComposeRowLineNeverExceedsThePaneEvenWhenTheLabelCannotFit(t *testing.T) {
	for width := 4; width <= 40; width++ {
		p := composeRowLine(width, 26, "▎", "•", "Smart dispatch auto-create", "[ ] off", "live")
		assert.LessOrEqualf(t, ansi.StringWidth(p.plain()), width,
			"composed line must never exceed the pane, even at width %d: %q", width, p.plain())
	}
}

// TestComposeRowLineRightAlignsTheBadge pins that the badge ends flush with the pane's
// right edge — the whole point of a badge column is that it lines up across rows so a
// user can scan "which of these apply on restart?" without reading each line.
func TestComposeRowLineRightAlignsTheBadge(t *testing.T) {
	for _, label := range []string{"Theme", "Glyph set", "Hint bar"} {
		p := composeRowLine(52, 12, " ", " ", label, "‹ on ›", "live")
		require.NotEmptyf(t, p.badge, "the badge must fit at this width (label %q)", label)
		assert.Truef(t, strings.HasSuffix(p.plain(), "live"),
			"the badge must be flush right for label %q: %q", label, p.plain())
	}
	// (Alignment ACROSS rows follows from every line being exactly the pane width, which
	// TestComposeRowLineIsExactlyThePaneWidth pins. Re-asserting it here by comparing three
	// widths that are all `width` by construction would be a tautology.)
}

// TestComposeRowLineKeepsTheMarkerColumnsSeparate pins spec §10's explicit requirement:
// the selection mark and the modified mark are two single-cell columns, not one. A row
// that is both selected and modified must show both, so the modified marker cannot reuse
// the SelectionMark cell.
func TestComposeRowLineKeepsTheMarkerColumnsSeparate(t *testing.T) {
	p := composeRowLine(52, 12, "▎", "•", "Theme", "‹ atrium ›", "live")
	assert.True(t, strings.HasPrefix(p.head, "▎• "),
		"selection and modified marks occupy separate adjacent cells: %q", p.head)

	// And the columns hold their positions when only one mark is present, so labels stay
	// aligned down the pane.
	//
	// The offset is measured in CELLS, not bytes: strings.Index would return 5 for the
	// glyph cases, since "▎" and "•" are three bytes each, and comparing that to
	// rowMarkerCells (3) would fail two of the three cases for the wrong reason.
	for _, marks := range [][2]string{{"▎", " "}, {" ", "•"}, {" ", " "}, {"▎", "•"}} {
		p := composeRowLine(52, 12, marks[0], marks[1], "Theme", "‹ atrium ›", "live")
		at := strings.Index(p.head, "Theme")
		require.GreaterOrEqualf(t, at, 0, "the label must be present: %q", p.head)
		assert.Equalf(t, rowMarkerCells, ansi.StringWidth(p.head[:at]),
			"the label must start at cell %d whatever the marks %v: %q", rowMarkerCells, marks, p.head)
	}
}

// TestEnumValueCandidatesLeadWithTheAlternatives pins the fix for D8: `‹ desktop ›` alone
// never revealed that three other modes existed, so discovering them meant cycling — and
// every ←/→ press persists to disk and live-applies, so discovering four options wrote
// four of them. The rich rendering shows all options with the current one bracketed; the
// compact one is the fallback for a pane that cannot hold them.
func TestEnumValueCandidatesLeadWithTheAlternatives(t *testing.T) {
	got := enumValueCandidates("off", []string{"off", "bell", "desktop", "osc"})
	require.Len(t, got, 2, "a rich rendering and a compact fallback")
	assert.Equal(t, "‹off› bell desktop osc", got[0])
	assert.Equal(t, "‹ off ›", got[1])
	assert.Greater(t, ansi.StringWidth(got[0]), ansi.StringWidth(got[1]),
		"the candidates must be ordered widest first, or the caller picks wrong")
}

// TestEnumValueCandidatesForASingleOptionHasNoAlternatives pins that a one-option enum —
// default_program with a single profile — does not render a bracketed value beside an
// empty alternatives list.
func TestEnumValueCandidatesForASingleOptionHasNoAlternatives(t *testing.T) {
	got := enumValueCandidates("bash", []string{"bash"})
	assert.Equal(t, []string{"‹ bash ›"}, got)
}

// TestEveryEnumRowsRichValueIsOfferedFirst pins the ladder against the real schema rather
// than a synthetic case: for every enum row, the rich candidate must contain every option,
// so no option can go missing from the inline alternatives.
func TestEveryEnumRowsRichValueIsOfferedFirst(t *testing.T) {
	cfg := config.DefaultConfig()
	enums := 0
	for _, r := range newSettingRows(cfg) {
		if r.kind != kindEnum {
			continue
		}
		opts := r.options(cfg)
		if len(opts) < 2 {
			continue
		}
		enums++
		rich := enumValueCandidates(r.get(cfg), opts)[0]
		for _, o := range opts {
			assert.Containsf(t, rich, o, "row %q's inline alternatives omit option %q", r.key, o)
		}
	}
	require.Positive(t, enums, "the schema must declare at least one multi-option enum")
}
```

- [ ] **Step 2: Run the tests to verify they fail**

```bash
PATH="/home/zvi/.local/share/mise/installs/go/1.26.4/bin:$PATH" \
  go test ./ui/overlay/ -run 'TestComposeRowLine|TestEnumValue|TestEveryEnumRows' -v 2>&1 | head -20
```
Expected: FAIL to build — `undefined: composeRowLine`, `undefined: rowLineParts`,
`undefined: enumValueCandidates`, `undefined: rowMarkerCells`.

- [ ] **Step 3: Add the constants**

Append to `ui/overlay/settings_render.go`'s const block:

```go
// Rows-pane line geometry:
//
//	[selection 1][modified 1][space 1][label ...][gap 2][value ...][slack][badge]
//
// The selection mark and the modified marker are SEPARATE single-cell columns. A row that
// is both selected and modified shows both, so the modified marker must not reuse the
// SelectionMark cell (spec §10, pinned by TestComposeRowLineKeepsTheMarkerColumnsSeparate).
const (
	rowMarkerCells = 3 // selection + modified + the space after them
	rowLabelGap    = 2
	// rowMinValueCells is the narrowest value column worth offering: enough for
	// "‹ creation ›" (12), "[ ] off" (7) and "auto (8)" (8). It is what makes
	// minRowsPaneWidth — and so the single-pane threshold — a derived number.
	rowMinValueCells = 14
)

// paneDividerCells is what the vertical rail/rows divider costs: a space, the theme's own
// left-border rune, and a space.
const paneDividerCells = 3
```

- [ ] **Step 4: Add the composer**

```go
// rowLineParts is one rows-pane line decomposed into the plain-text segments the renderer
// styles independently — the head dim, the value bright, the badge faint, exactly as the
// single-column renderer coloured its two halves.
type rowLineParts struct {
	head  string // selection + modified + space + padded label + gap
	value string
	gap   string // the slack that right-aligns the badge
	badge string // "" when dropped for width
}

// plain returns the whole line as unstyled text.
//
// Tests measure THIS, not Render()'s output. The bordered lipgloss box pads every line to
// the same width, so asserting on a rendered line's width is a tautology that can never
// fail — an over-wide line soft-wraps and grows the box instead of exceeding it
// (atrium-accounts-reorder-grouping).
func (p rowLineParts) plain() string { return p.head + p.value + p.gap + p.badge }

// composeRowLine lays out one rows-pane line to exactly width cells.
//
// Truncation priority is spec §10's, and the order is the whole point: drop the badge
// first, then tail-ellipsize the value, and never touch the label. A half-written label
// makes the row unidentifiable, while a truncated value is recoverable — the help pane
// renders it in full (see contextLine).
//
// sel and modified are single-cell strings (a glyph or a space); passing an empty string
// would collapse the columns and misalign every label below.
func composeRowLine(width, labelW int, sel, modified, label, value, badge string) rowLineParts {
	// Clamp the label column to what the pane can hold. The never-truncate-the-label rule
	// holds wherever the label CAN fit; below that there is no line that both shows it whole
	// and fits, and an over-wide line is the worse failure — lipgloss soft-wraps it, the box
	// grows a row, and the pinned hint gets clipped. The pre-PR-B renderer hard-clipped every
	// body line to the inner width (settings.go:318), so this is parity, not a regression.
	if maxLabel := width - rowMarkerCells - rowLabelGap; labelW > maxLabel {
		labelW = max(0, maxLabel)
	}
	p := rowLineParts{
		head: sel + modified + " " + padRight(label, labelW) + strings.Repeat(" ", rowLabelGap),
	}
	if ansi.StringWidth(p.head) > width {
		p.head = ansi.Truncate(p.head, width, "")
		return p
	}
	avail := width - ansi.StringWidth(p.head)
	if avail < 1 {
		return p
	}
	// Keep the badge if the value, the badge and at least one separating space all fit.
	if badge != "" && ansi.StringWidth(value)+ansi.StringWidth(badge)+1 <= avail {
		p.value, p.badge = value, badge
		p.gap = strings.Repeat(" ", avail-ansi.StringWidth(value)-ansi.StringWidth(badge))
		return p
	}
	if ansi.StringWidth(value) > avail {
		value = ansi.Truncate(value, avail, "…")
	}
	p.value = value
	p.gap = strings.Repeat(" ", avail-ansi.StringWidth(value))
	return p
}

// enumValueCandidates returns an enum's value renderings from widest to plainest, so the
// caller can take the widest that fits — the degradation ladder theme.badgeCandidates
// uses for panel badges.
//
// The rich form is the fix for D8. `‹ desktop ›` alone never revealed that three other
// modes existed, so the only way to discover them was to cycle — and every ←/→ press
// persists to disk and live-applies, so discovering four options wrote four of them.
func enumValueCandidates(cur string, opts []string) []string {
	compact := "‹ " + cur + " ›"
	if len(opts) < 2 {
		return []string{compact}
	}
	parts := make([]string, 0, len(opts))
	for _, o := range opts {
		if o == cur {
			parts = append(parts, "‹"+o+"›")
			continue
		}
		parts = append(parts, o)
	}
	return []string{strings.Join(parts, " "), compact}
}
```

Add `"strings"` to `settings_render.go`'s imports. **`padRight` already exists** at
`ui/overlay/accounts.go:733` in this same package — call it rather than adding a duplicate.

- [ ] **Step 5: Run the tests to verify they pass**

```bash
PATH="/home/zvi/.local/share/mise/installs/go/1.26.4/bin:$PATH" \
  go test ./ui/overlay/ -run 'TestComposeRowLine|TestEnumValue|TestEveryEnumRows' -v 2>&1 | tail -30
```
Expected: PASS (9 tests).

- [ ] **Step 6: Verify the ladder guards actually guard**

Each mutation targets a different rung, because a single mutation would only prove one.

1. **Swap the priority.** Move the `ansi.Truncate` block *above* the badge block, so the
   value is truncated before the badge is dropped. Expected:
   `TestComposeRowLineDropsTheBadgeBeforeTruncatingTheValue` FAILS (`p.badge` non-empty).
   Revert by editing the block back.
2. **Truncate the label.** Change `padRight(label, labelW)` to
   `ansi.Truncate(padRight(label, labelW), labelW, "…")`. Expected:
   `TestComposeRowLineNeverTruncatesTheLabelThatFits` FAILS on one of the
   `labelW` < label cases — which is precisely why those cases were added; with
   `labelW == len(label)` throughout, `padRight` never truncates and the mutation would
   have gone undetected. Revert.
3. **Remove the label clamp.** Delete the `if maxLabel := ...` block. Expected:
   `TestComposeRowLineNeverExceedsThePaneEvenWhenTheLabelCannotFit` FAILS at the narrow
   widths, while every other composer test still passes — the clamp only matters below the
   floor. Revert.
4. **Break the width invariant.** Drop the final `p.gap = strings.Repeat(...)`. Expected:
   `TestComposeRowLineIsExactlyThePaneWidth` FAILS on the `no badge at all` case, and
   `TestComposeRowLineRightAlignsTheBadge` still passes — which is why the exact-width
   test is separate from the alignment test. Revert.
5. **Reverse the candidate order.** Return `[]string{compact, rich}` from
   `enumValueCandidates`. Expected:
   `TestEnumValueCandidatesLeadWithTheAlternatives` FAILS on the widest-first assertion.
   Revert.
6. Re-run and confirm green.

- [ ] **Step 7: Lint and commit**

```bash
PATH="/home/zvi/.local/share/mise/installs/go/1.26.4/bin:$PATH" /home/zvi/go/bin/golangci-lint run ./ui/...
git add ui/overlay/settings_render.go ui/overlay/settings_render_test.go
git commit -m "feat(settings): row line composer with spec 10's truncation priority"
```

---

## Task 5: The fixed-height help pane and the two-pane renderer

**Files:**
- Modify: `ui/overlay/settings.go` (`boxWidth`, `innerWidth`, `SetSize`, `startEdit`,
  `Render`; delete `renderBody`, `renderFooter`, `renderValue`, `labelColWidth`,
  `settingsVChrome`'s comment)
- Modify: `ui/overlay/settings_render.go`
- Modify: `ui/overlay/settings_render_test.go`

**Interfaces:**
- Consumes: Tasks 2–4.
- Produces, all in `settings_render.go` unless noted:
  - constants: `helpPaneLines` (in `settings.go`)
  - geometry: `(*SettingsOverlay).helpHeight()`, `.helpBlockHeight()`, `.paneHeight()`,
    `.maxPaneLines()`, `.rowsPaneWidth()`, `.visibleLabelWidth()`, `.longestRowLabel()`,
    `.minRowsPaneWidth()`, `.twoPaneMinInner()`, `.twoPane()`, `.editorWidth()`
  - content: `paneLine{text, rowIdx}`, `.rowsPaneContent(width int) []paneLine`,
    `.handoffPaneContent(e railEntry, width int) []paneLine`,
    `.renderRowLine(i, width, labelW int) string`,
    `.valueCell(i, width, labelW int, badge string) string`, `.valueWasTruncated() bool`
  - layout: `windowPane(lines []string, cursor, budget int) []string`, `.railLines()`,
    `.rowsPaneLines()`, `.bodyLines()`, `.helpLines()`, `.contextLine(width int) string`,
    `.separatorLine()`, `.hintLine()`, `paneDivider()`

  Tasks 6–9 extend `bodyLines`, `rowsPaneContent`, `renderRowLine`, `valueCell`,
  `contextLine` and `hintLine`. **`valueCell` takes its `badge` argument from this task
  onward** even though it is always `""` here — Task 7 is what passes a real one, and
  changing the signature later would mean touching every call site twice.

**Guard 5 is the first test written in this task**, before any renderer code. It is the
regression test for D5 — the defect that motivated the entire redesign. At 80×24,
selecting *Account clustering* used to collapse the body to **8 visible rows while its
help took 8 lines**, because `renderFooter`'s height fed `renderBody`'s budget. The fix is
structural: `helpHeight()` reads the terminal height and nothing else.

- [ ] **Step 1: Write guard 5 and guard 4, failing**

Append to `ui/overlay/settings_render_test.go`:

```go
// TestSelectingTheLongestHelpRowKeepsTheRowCount is spec §13's guard 5 and the direct
// regression test for D5, the defect that motivated this redesign: at 80x24, selecting
// Account clustering collapsed the list to 8 visible rows while its help took 8 lines,
// because the footer's height fed the body's budget.
//
// The assertion is deliberately over EVERY row rather than the worst one: the property is
// that the visible row count does not depend on the cursor at all, and picking a single
// "longest help" row would silently stop testing that the day the copy changes.
func TestSelectingTheLongestHelpRowKeepsTheRowCount(t *testing.T) {
	for _, size := range []struct{ w, h int }{{80, 24}, {100, 32}, {80, 40}} {
		t.Run(fmt.Sprintf("%dx%d", size.w, size.h), func(t *testing.T) {
			o := NewSettingsOverlay(config.DefaultConfig())
			o.SetSize(size.w, size.h)

			// All settings, so every row is in one pane and the count is comparable
			// across rows from different categories.
			o.railCursor = 0
			want := len(o.rowsPaneLines())
			require.Greater(t, want, 3, "the pane must show real rows for this to mean anything")

			for _, r := range newSettingRows(config.DefaultConfig()) {
				require.True(t, o.SelectRow(r.key))
				o.railCursor = 0 // stay in the flat view; SelectRow moved the rail
				assert.Equalf(t, want, len(o.rowsPaneLines()),
					"selecting %q changed the visible row count from %d to %d — help height "+
						"must not depend on the cursor (D5)", r.key, want, len(o.rowsPaneLines()))
				assert.Equalf(t, o.helpHeight(), len(o.helpLines()),
					"the help pane must be exactly helpHeight() lines on row %q", r.key)
			}
		})
	}
}

// TestHelpHeightIgnoresTheCursor pins the same property one level down, on the function
// rather than on the render. If this passes and guard 5 fails, the leak is in the pane
// budget; if both fail, it is here.
func TestHelpHeightIgnoresTheCursor(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	o.SetSize(80, 24)
	want := o.helpHeight()
	require.Equal(t, helpPaneLines, want, "80x24 must afford the full help pane")

	for _, r := range newSettingRows(config.DefaultConfig()) {
		require.True(t, o.SelectRow(r.key))
		assert.Equalf(t, want, o.helpHeight(), "row %q changed the help pane's height", r.key)
	}
}

// TestRailFitsUnscrolledAtTheFloor is spec §13's guard 4 and §4's rail-height invariant:
// the whole rail must be visible at the project's 80x24 degradation floor, because the
// rail is the panel's only orientation — a rail that scrolls means the user cannot see
// what categories exist.
//
// Thirteen entries fit thirteen pane rows exactly. That is not a coincidence to be
// preserved by luck: a fourteenth entry has to displace another, and this test is what
// forces that decision to be deliberate.
func TestRailFitsUnscrolledAtTheFloor(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	o.SetSize(80, 24)

	assert.GreaterOrEqual(t, o.paneHeight(), len(railEntries()),
		"the rail must fit unscrolled at 80x24 (spec §4); a 14th entry must displace another")

	lines := o.railLines()
	require.Len(t, lines, o.paneHeight(), "the rail pane is padded to the shared pane height")
	for _, e := range railEntries() {
		assert.Containsf(t, stripANSI(strings.Join(lines, "\n")), e.label,
			"rail entry %q is not visible at 80x24", e.label)
	}
}

// TestBoxHeightDependsOnlyOnTheTerminal pins that the box neither jumps as the rail moves
// nor changes when the row cursor does. A centered overlay whose height changes gets
// re-centered under the user's cursor mid-navigation — the jump
// ui/overlay/textInput_size.go:3-8 warns about.
func TestBoxHeightDependsOnlyOnTheTerminal(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	o.SetSize(100, 32)
	want := lipgloss.Height(o.Render())

	for i := range railEntries() {
		o.railCursor = i
		o.syncCursorToRail()
		assert.Equalf(t, want, lipgloss.Height(o.Render()),
			"rail entry %q changed the box height", railEntries()[i].label)
	}
	for _, key := range []string{"max_sessions", "group_mode", "agent_oom_margin", "config_file"} {
		require.True(t, o.SelectRow(key))
		assert.Equalf(t, want, lipgloss.Height(o.Render()), "row %q changed the box height", key)
	}
}

// TestNoPaneLineOverflowsItsWidth is spec §13's guard 6, measured where it can actually
// fail: on the plain composed lines, before the bordered box pads them all to the same
// width. A post-render width assert is a tautology — an over-wide line makes lipgloss
// soft-wrap and grow the box, never exceed its width.
func TestNoPaneLineOverflowsItsWidth(t *testing.T) {
	cfg := config.DefaultConfig()
	// A pathologically long value, so the truncation paths are exercised rather than
	// merely available.
	cfg.TmuxConfigOverride = strings.Repeat("/very/long/path", 20)
	cfg.ProjectSearchRoots = []string{strings.Repeat("~/deeply/nested", 12)}

	for _, size := range []struct{ w, h int }{{80, 24}, {100, 32}, {73, 24}, {72, 24}, {60, 20}, {40, 14}} {
		t.Run(fmt.Sprintf("%dx%d", size.w, size.h), func(t *testing.T) {
			o := NewSettingsOverlay(cfg)
			o.SetSize(size.w, size.h)
			for i := range railEntries() {
				o.railCursor = i
				o.syncCursorToRail()
				for _, line := range o.rowsPaneLines() {
					assert.LessOrEqualf(t, ansi.StringWidth(stripANSI(line)), o.rowsPaneWidth(),
						"a rows-pane line overflows %d cells on entry %q: %q",
						o.rowsPaneWidth(), railEntries()[i].label, stripANSI(line))
				}
				for _, line := range o.railLines() {
					assert.LessOrEqualf(t, ansi.StringWidth(stripANSI(line)), railWidth(),
						"a rail line overflows %d cells: %q", railWidth(), stripANSI(line))
				}
				for _, line := range o.helpLines() {
					assert.LessOrEqualf(t, ansi.StringWidth(stripANSI(line)), o.innerWidth(),
						"a help line overflows the inner width %d: %q", o.innerWidth(), stripANSI(line))
				}
			}
		})
	}
}

// TestSelectedRowIsAlwaysVisible pins the invariant the panel exists to serve: whatever the
// terminal size, whatever the category, the row the cursor is on is rendered.
//
// This is the guard the plan's first draft lacked, and it would have caught a real defect:
// with the cursor pinned to the LAST visible line, the "↓ n more" overflow marker lands on
// top of it, so the selected row vanished for every cursor position except the very last —
// in All settings at 80x24 past row 13, and in every category on a terminal of 14 rows or
// fewer. Guard 5 counts lines and cannot see it; only asserting the label can.
func TestSelectedRowIsAlwaysVisible(t *testing.T) {
	sizes := []struct{ w, h int }{{100, 32}, {80, 24}, {80, 14}, {80, 12}, {72, 24}, {60, 20}, {50, 16}}
	for _, size := range sizes {
		t.Run(fmt.Sprintf("%dx%d", size.w, size.h), func(t *testing.T) {
			o := NewSettingsOverlay(config.DefaultConfig())
			o.SetSize(size.w, size.h)
			for _, r := range newSettingRows(config.DefaultConfig()) {
				for _, entry := range []int{railIndexForCategory(r.category), 0} {
					require.True(t, o.SelectRow(r.key))
					o.railCursor = entry // 0 exercises the flat view, where windowing bites
					o.syncCursorToRail()
					pane := stripANSI(strings.Join(o.rowsPaneLines(), "\n"))
					assert.Containsf(t, pane, r.label,
						"selected row %q is not rendered in entry %q at %dx%d",
						r.key, railEntries()[entry].label, size.w, size.h)
				}
			}
		})
	}
}

// TestCurrentRailEntryIsAlwaysVisible is the rail's half of the same invariant. Below 24 rows
// the rail cannot show all thirteen entries, and the first draft simply dropped the tail —
// so navigating to Accounts on an 80x20 terminal left no visible selection mark anywhere.
func TestCurrentRailEntryIsAlwaysVisible(t *testing.T) {
	for _, size := range []struct{ w, h int }{{100, 32}, {80, 24}, {80, 20}, {80, 14}, {80, 12}} {
		t.Run(fmt.Sprintf("%dx%d", size.w, size.h), func(t *testing.T) {
			o := NewSettingsOverlay(config.DefaultConfig())
			o.SetSize(size.w, size.h)
			for i, e := range railEntries() {
				o.railCursor = i
				o.syncCursorToRail()
				rail := stripANSI(strings.Join(o.railLines(), "\n"))
				assert.Containsf(t, rail, e.label,
					"current rail entry %q is not rendered at %dx%d", e.label, size.w, size.h)
			}
		})
	}
}

// TestRailRendersEveryLabelWhole pins that no rail label is ever clipped. The rail is the
// panel's only orientation, so a half-written category name is worse than a scrolled rail.
func TestRailRendersEveryLabelWhole(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	o.SetSize(80, 24) // the floor, where all thirteen fit unscrolled
	rail := stripANSI(strings.Join(o.railLines(), "\n"))
	assert.NotContains(t, rail, "…", "no rail label may be truncated")
	for _, e := range railEntries() {
		assert.Containsf(t, rail, e.label, "rail label %q is clipped", e.label)
	}
}

// TestMaxPaneLinesMatchesTheFlatView pins that the pane-height cap is the real size of the
// tallest view rather than a formula that can drift from it. maxPaneLines is used to cap
// paneHeight, so an over-estimate leaves permanent blank rows at the bottom of a tall
// terminal and an under-estimate makes the flat view scroll when it need not.
func TestMaxPaneLinesMatchesTheFlatView(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	o.SetSize(100, 200) // tall enough that nothing is windowed
	o.railCursor = 0    // All settings
	assert.Equal(t, len(o.rowsPaneContent(o.rowsPaneWidth())), o.maxPaneLines(),
		"maxPaneLines must equal the flat view's actual line count")
}
```

Add `"fmt"` and `"github.com/charmbracelet/lipgloss"` to the test imports.

- [ ] **Step 2: Run to verify they fail**

```bash
PATH="/home/zvi/.local/share/mise/installs/go/1.26.4/bin:$PATH" \
  go test ./ui/overlay/ -run 'TestSelectingTheLongest|TestHelpHeight|TestRailFits|TestBoxHeight|TestNoPaneLine|TestMaxPaneLines' -v 2>&1 | head -20
```
Expected: FAIL to build — `o.rowsPaneLines undefined`, `o.helpHeight undefined`.

- [ ] **Step 3: Replace the width and height budgets in `settings.go`**

Replace the `settingsVChrome` comment and `boxWidth`/`innerWidth`/`labelColWidth`:

```go
// settingsVChrome is the vertical chrome around the panes and the help pane: border (2)
// + Padding(1,2) verticals (2) + title (1) + blank-after-title (1) + hint (1).
//
// The pane/help separator is deliberately NOT counted here — it is counted with the help
// pane (helpBlockHeight), because it is drawn only when there is a help pane to separate.
const settingsVChrome = 7

// settingsMinBody is the minimum number of pane rows kept visible, which keeps the cursor
// row on screen. On a terminal too short for the full layout the help pane sheds lines
// down to zero before this floor is touched — the row list is what the panel is for.
const settingsMinBody = 3

// helpPaneLines is the help pane's height whenever the terminal can afford it (spec §10).
// Fixed height is the entire point: the old footer grew with the help text and stole rows
// from the list, so selecting Account clustering at 80x24 left 8 visible rows while its
// help took 8 lines (D5).
const helpPaneLines = 3
```

```go
// boxWidth is the lipgloss .Width of the panel (content + padding, excluding the border);
// innerWidth is the usable text width inside the padding.
//
// The 96 cap replaces the old fixed 64, which wasted a third of a 100-column terminal
// (D12). At the 80-column floor the box is 78 and the inner width 74 — which is exactly
// the summaryBudget PR A wrote the copy against.
func (s *SettingsOverlay) boxWidth() int {
	w := 96
	if limit := s.width - 2; w > limit { // leave room for the border
		w = limit
	}
	if w < 20 {
		w = 20
	}
	return w
}

func (s *SettingsOverlay) innerWidth() int { return s.boxWidth() - 4 }
```

Delete `labelColWidth` and replace its two callers (`SetSize`, `startEdit`) with
`s.editorWidth()`:

```go
// SetSize is given the full terminal dimensions; the panel sizes itself within them,
// falls back to a single pane on a narrow terminal, and windows its rows on a short one.
func (s *SettingsOverlay) SetSize(width, height int) {
	s.width = width
	s.height = height
	s.input.Width = s.editorWidth()
}
```

and in `startEdit`, `in.Width = max(10, s.innerWidth()-s.labelColWidth()-4)` becomes
`in.Width = s.editorWidth()`.

- [ ] **Step 4: Add the geometry helpers to `settings_render.go`**

```go
// visibleLabelWidth is the label column width for the rows currently on screen: the widest
// label among them.
//
// It is per-entry rather than global on purpose. Padding every pane to the schema's widest
// label (26 cells, "Smart dispatch auto-create") would spend that width in Input, whose
// widest is 21, for no gain. The degradation threshold still budgets for the global worst
// case (minRowsPaneWidth), so a narrow pane is never a surprise.
func (s *SettingsOverlay) visibleLabelWidth() int {
	start, end := s.rowRange(s.selectedEntry())
	w := 0
	for _, r := range s.rows[start:end] {
		if n := ansi.StringWidth(r.label); n > w {
			w = n
		}
	}
	return w
}

// longestRowLabel is the widest label in the whole schema — the worst case the rows pane
// must hold without truncating one, and so the basis of the single-pane threshold.
func (s *SettingsOverlay) longestRowLabel() int {
	w := 0
	for _, r := range s.rows {
		if n := ansi.StringWidth(r.label); n > w {
			w = n
		}
	}
	return w
}

// minRowsPaneWidth is the narrowest rows pane worth rendering: the widest label
// untruncated — spec §10 never truncates a label — plus a legible value column.
func (s *SettingsOverlay) minRowsPaneWidth() int {
	return rowMarkerCells + s.longestRowLabel() + rowLabelGap + rowMinValueCells
}

// twoPaneMinInner is the inner width below which the panel falls back to single-pane
// drill-in (Task 6).
//
// It is computed from the parts — rail, divider, minimum rows pane — because spec §10
// requires exactly that. A hardcoded threshold would silently stop tracking a renamed
// category or a longer label, offering two panes at a width where the rows pane can no
// longer show one. Pinned by TestThresholdIsDerivedFromTheParts.
func (s *SettingsOverlay) twoPaneMinInner() int {
	return railWidth() + paneDividerCells + s.minRowsPaneWidth()
}

// twoPane reports whether the terminal is wide enough for the rail and rows side by side.
func (s *SettingsOverlay) twoPane() bool { return s.innerWidth() >= s.twoPaneMinInner() }

// rowsPaneWidth is the rows pane's width: the inner width less the rail and divider in
// two-pane mode, or the whole inner width when the rail is a separate screen.
func (s *SettingsOverlay) rowsPaneWidth() int {
	if s.twoPane() {
		return s.innerWidth() - railWidth() - paneDividerCells
	}
	return s.innerWidth()
}

// editorWidth is the inline editor's width: the rows pane less the marker columns, the
// visible label column and its gap.
func (s *SettingsOverlay) editorWidth() int {
	return max(10, s.rowsPaneWidth()-rowMarkerCells-s.visibleLabelWidth()-rowLabelGap)
}

// helpHeight is the help pane's line count: helpPaneLines whenever the terminal can afford
// them, fewer only when it cannot.
//
// It reads the terminal height and NOTHING else — in particular not the cursor. That
// independence is the fix for D5 and is what TestSelectingTheLongestHelpRowKeepsTheRowCount
// and TestHelpHeightIgnoresTheCursor pin. The -1 reserves the separator, which is drawn
// only alongside a help pane.
func (s *SettingsOverlay) helpHeight() int {
	return clamp(s.height-settingsVChrome-settingsMinBody-1, 0, helpPaneLines)
}

// helpBlockHeight is the help pane plus its separator, or 0 when there is no help pane.
func (s *SettingsOverlay) helpBlockHeight() int {
	if h := s.helpHeight(); h > 0 {
		return h + 1
	}
	return 0
}

// maxPaneLines is the tallest content any rail entry could need: the All settings view,
// with a header per category and a spacer between them. Capping paneHeight at it means the
// box grows with the terminal but never past what it can fill. Pinned against the flat
// view's real line count by TestMaxPaneLinesMatchesTheFlatView.
func (s *SettingsOverlay) maxPaneLines() int {
	cats := len(allCategories())
	return max(len(railEntries()), len(s.rows)+cats+(cats-1))
}

// paneHeight is the shared height of the rail and rows panes.
//
// It is a function of the terminal size alone — not of the rail cursor, not of the row
// cursor — so the centered box never changes height as you navigate. At 80x24 it is 13,
// which is exactly the thirteen rail entries (spec §4's invariant).
func (s *SettingsOverlay) paneHeight() int {
	return clamp(s.height-settingsVChrome-s.helpBlockHeight(), settingsMinBody, s.maxPaneLines())
}
```

`clamp(v, lower, upper int) int` already exists at `ui/overlay/overlay.go:185`.

- [ ] **Step 5: Add the rail renderer**

```go
// railLines renders the left rail, padded to the shared pane height so the divider column
// runs its full length.
//
// At the 80x24 floor all thirteen entries fit unscrolled — spec §4's invariant, pinned by
// TestRailFitsUnscrolledAtTheFloor. Below 24 rows they cannot, so the rail windows around
// its cursor exactly as the rows pane does. Spec §4 anticipates this ("the rail windows like
// today's body does"); what it must never do is silently drop the entries past the budget,
// which would leave the current entry off-screen with no indication of where you are.
func (s *SettingsOverlay) railLines() []string {
	t := theme.Current()
	labelW := railWidth() - railMarkerCells - railTrailCells
	entries := railEntries()

	rendered := make([]string, 0, len(entries))
	for i, e := range entries {
		mark := " "
		if i == s.railCursor {
			mark = t.Glyphs.SelectionMark
		}
		trail := " "
		if e.kind == railHandoff {
			trail = t.Glyphs.Handoff
		}
		line := mark + " " + padRight(e.label, labelW) + " " + trail

		style := t.DimStyle()
		switch {
		case i == s.railCursor && s.focus == focusRail:
			style = t.AccentStyle()
		case i == s.railCursor:
			// Current but unfocused: still legible, but the accent belongs to whichever
			// pane is taking keys, so exactly one bright marker is on screen at a time.
			style = t.FgStyle()
		case e.kind == railHandoff:
			// Dimmer than an ordinary entry: PR B cannot open these yet.
			style = t.FaintStyle()
		}
		rendered = append(rendered, style.Render(line))
	}
	return windowPane(rendered, s.railCursor, s.paneHeight())
}

// windowPane windows lines around a cursor index, padding to exactly budget lines and
// replacing the edge lines with "n more" markers when content is hidden.
//
// The cursor is kept one line INSIDE the window whenever the budget allows, so a marker
// overwriting an edge line can never hide the line the user is pointing at. Getting this
// wrong is not a cosmetic bug: with the cursor pinned to the last visible line, the "↓ n
// more" marker lands on top of it and the selected row disappears for every cursor position
// except the very last. Pinned by TestSelectedRowIsAlwaysVisible and
// TestCurrentRailEntryIsAlwaysVisible.
func windowPane(lines []string, cursor, budget int) []string {
	if budget < 1 {
		return nil
	}
	out := make([]string, 0, budget)
	if len(lines) <= budget {
		out = append(out, lines...)
		for len(out) < budget {
			out = append(out, "")
		}
		return out
	}

	// Leave one line of margin below the cursor when there is room for it.
	margin := 0
	if budget >= 3 {
		margin = 1
	}
	start := 0
	if cursor >= budget-margin {
		start = cursor - budget + 1 + margin
	}
	if maxStart := len(lines) - budget; start > maxStart {
		start = maxStart
	}
	if start < 0 {
		start = 0
	}

	out = append(out, lines[start:start+budget]...)
	faint := theme.Current().FaintStyle()
	if start > 0 {
		out[0] = faint.Render(fmt.Sprintf("  ↑ %d more", start))
	}
	if hidden := len(lines) - start - budget; hidden > 0 {
		out[budget-1] = faint.Render(fmt.Sprintf("  ↓ %d more", hidden))
	}
	return out
}
```

The markers overwrite the edge lines rather than adding rows, so the pane height stays
exactly `budget`. That is safe *because* of the margin: the cursor is never on an edge line
while `budget >= 3`, and `settingsMinBody` is 3, so the budget never drops below it.

- [ ] **Step 6: Add the rows-pane renderer and its windowing**

```go
// paneLine is one rows-pane line together with the row it belongs to, so the windowing can
// locate the cursor without re-deriving it from the text. rowIdx is -1 for a category
// header, a spacer, an overflow marker, or a handoff note.
type paneLine struct {
	text   string
	rowIdx int
}

// rowsPaneContent builds every line the current rail entry could show, unwindowed.
//
// The All settings view adds a header per category and a blank spacer between them — the
// shape of the pre-redesign list, which is what makes it usable for auditing a config. A
// single category shows its rows bare: the rail entry already names it, so a header would
// only repeat itself.
func (s *SettingsOverlay) rowsPaneContent(width int) []paneLine {
	e := s.selectedEntry()
	if e.kind == railHandoff {
		return s.handoffPaneContent(e, width)
	}
	t := theme.Current()
	start, end := s.rowRange(e)
	labelW := s.visibleLabelWidth()

	var lines []paneLine
	// The explicit `first` flag is required because catSessions is 0: an uninitialized
	// lastCategory would equal the first row's category and swallow its header.
	first := true
	lastCategory := allCategories()[0]
	for i := start; i < end; i++ {
		if e.kind == railAll && (first || s.rows[i].category != lastCategory) {
			if !first {
				lines = append(lines, paneLine{text: "", rowIdx: -1})
			}
			lines = append(lines, paneLine{
				text:   t.DimStyle().Bold(true).Render(s.rows[i].category.label()),
				rowIdx: -1,
			})
			lastCategory = s.rows[i].category
			first = false
		}
		first = false
		lines = append(lines, paneLine{text: s.renderRowLine(i, width, labelW), rowIdx: i})
	}
	return lines
}

// handoffPaneContent is the rows pane for an entry that owns no rows: its note, wrapped.
// Naming the surface that does own the config is the honest thing to render until PR C
// wires Accounts to the @ overlay and PR D builds the Profiles editor — an empty pane
// would read as a bug.
func (s *SettingsOverlay) handoffPaneContent(e railEntry, width int) []paneLine {
	style := theme.Current().DimStyle()
	var lines []paneLine
	for _, l := range strings.Split(ansi.Wrap(e.note, width, ""), "\n") {
		lines = append(lines, paneLine{text: style.Render(l), rowIdx: -1})
	}
	return lines
}

// rowsPaneLines renders the highlighted entry's rows, windowed around the cursor and padded
// to the shared pane height. When the entry outgrows the pane — All settings always does —
// an edge line becomes an "n more" marker, so the panel says that more exists rather than
// just ending (D2: no orientation).
func (s *SettingsOverlay) rowsPaneLines() []string {
	content := s.rowsPaneContent(s.rowsPaneWidth())
	lines := make([]string, len(content))
	cursorLine := 0
	for i, l := range content {
		lines[i] = l.text
		if l.rowIdx == s.cursor {
			cursorLine = i
		}
	}
	return windowPane(lines, cursorLine, s.paneHeight())
}

// renderRowLine composes and styles one row's line. Task 7 fills in the modified marker,
// the badge and the inert dimming; here the marker columns are reserved but empty, so the
// layout is final before the signals land on it.
func (s *SettingsOverlay) renderRowLine(i, width, labelW int) string {
	t := theme.Current()
	row := s.rows[i]
	selected := i == s.cursor

	// Both panes always show their cursor — hiding the rows pane's while the rail has focus
	// would leave the user unable to see where → would land. Only the STYLE differs, so
	// exactly one accent-bright marker is on screen at a time.
	sel := " "
	rowStyle := t.FgStyle()
	if selected {
		sel = t.Glyphs.SelectionMark
		rowStyle = t.FgStyle()
		if s.focus == focusRows {
			rowStyle = t.AccentStyle()
		}
	}

	if s.editing && selected {
		// The live text input carries its own cursor styling, so it is appended rather
		// than composed — an editor is not a value cell and must not be truncated.
		//
		// The head must use the SAME three marker cells composeRowLine does (selection,
		// modified, space), or every label jumps sideways the instant Enter opens the
		// editor. Task 7 fills the middle cell in; here it is a space.
		head := sel + " " + " " + padRight(row.label, labelW) + strings.Repeat(" ", rowLabelGap)
		return t.AccentStyle().Render(head) + s.input.View()
	}

	p := composeRowLine(width, labelW, sel, " ", row.label, s.valueCell(i, width, labelW, ""), "")
	if selected {
		return rowStyle.Render(p.plain())
	}
	return t.DimStyle().Render(p.head) + t.FgStyle().Render(p.value+p.gap+p.badge)
}

// valueCell formats a row's value by kind.
//
// For an enum it takes the widest rendering that fits — and `badge` is what the caller
// intends to put in the right-aligned column, so the ladder can step down to the compact
// form rather than squeezing the badge out. That ordering matters: the badge carries the
// inert reason, and a rich value that evicted it would dim a row with no explanation. See
// Task 7, where the badge is actually passed; here it is always "".
func (s *SettingsOverlay) valueCell(i, width, labelW int, badge string) string {
	row := s.rows[i]
	v := row.get(s.cfg)
	switch row.kind {
	case kindBool:
		if v == "on" {
			return "[x] on"
		}
		return "[ ] off"
	case kindEnum:
		avail := width - rowMarkerCells - labelW - rowLabelGap
		// Try to fit beside the badge first, then to fit the pane at all. The inline
		// alternatives are an enrichment, so they are the first thing to go; the compact
		// `‹ v ›` loses nothing about the current value (it is the pre-PR-B rendering).
		budgets := []int{avail}
		if badge != "" {
			budgets = []int{avail - ansi.StringWidth(badge) - 1, avail}
		}
		for _, budget := range budgets {
			for _, c := range enumValueCandidates(v, row.options(s.cfg)) {
				if ansi.StringWidth(c) <= budget {
					return c
				}
			}
		}
		return "‹ " + v + " ›"
	default:
		// kindInt, kindText and kindReadOnly all show the value bare; a read-only row
		// gets no editor affordance.
		return v
	}
}
```

- [ ] **Step 7: Add the help pane, the separator and the hint**

```go
// helpLines renders the fixed-height help pane: the selected row's summary with its caution
// and timing note, wrapped, then one context line — capped and padded to exactly
// helpHeight() lines.
//
// The prose is settingRow.footerText() unchanged from PR A, which appends the caution and
// the timing note after the summary. The timing therefore appears twice on a wide pane —
// once as the row's badge, once here. That is deliberate: the badge is a scannable column
// for comparing rows, the prose is the sentence for the selected one, and because spec §10
// drops the badge first on a narrow pane, the prose is its fallback rather than pure
// duplication. Reusing footerText also keeps TestEveryCautionReachesTheFooter and
// TestFooterTextFitsTwoLines live with no schema change.
func (s *SettingsOverlay) helpLines() []string {
	h := s.helpHeight()
	if h == 0 {
		return nil
	}
	t := theme.Current()
	inner := s.innerWidth()

	style := t.DimStyle()
	prose := s.selectedRow().footerText()
	if s.selectedEntry().kind == railHandoff {
		// A handoff entry's note is already the whole content of the rows pane, so echoing
		// it here would print the same sentence twice in one frame. The pane stays blank.
		prose = ""
	}
	if s.lastErr != "" {
		prose, style = s.lastErr, t.DangerStyle()
	}

	// The context line carries the position readout, so it must never be the thing that gets
	// evicted when the prose is long: capping the prose at h-1 is what makes contextLine's
	// "always" true of the rendered panel rather than only of the function. At h == 1 the
	// prose yields entirely — knowing where you are beats a truncated first sentence.
	ctx := ""
	if s.lastErr == "" && s.selectedEntry().kind != railHandoff {
		ctx = s.contextLine(inner)
	}
	proseBudget := h
	if ctx != "" {
		proseBudget = h - 1
	}

	lines := strings.Split(ansi.Wrap(prose, inner, ""), "\n")
	if len(lines) > proseBudget {
		lines = lines[:max(0, proseBudget)]
		if len(lines) > 0 {
			last := lines[len(lines)-1]
			// Appending the ellipsis to an already-full line would push it past inner, and
			// the lipgloss box would soft-wrap it, add a row, and clip the pinned hint.
			if ansi.StringWidth(last) > inner-1 {
				last = ansi.Truncate(last, inner-1, "")
			}
			lines[len(lines)-1] = last + "…"
		}
	}
	for i, l := range lines {
		lines[i] = style.Render(l)
	}
	if ctx != "" {
		// Pad first, so the context line — and with it the position readout — is always the
		// pane's LAST line rather than floating up under a short summary.
		for len(lines) < h-1 {
			lines = append(lines, "")
		}
		lines = append(lines, ctx)
	}
	for len(lines) < h {
		lines = append(lines, "")
	}
	return lines
}

// contextLine is the help pane's last line: whatever the row line could not say. Task 7
// fills in the inert reason and the enum gloss; here it carries only the position readout.
//
// The position counter always rides this line, right-aligned, so the pane says where you
// are in the category even when there is nothing else to add (D2: no orientation, no
// position). Content is truncated to make room for it, never the other way round — the
// counter is five cells and the content is recoverable from `?`.
func (s *SettingsOverlay) contextLine(width int) string {
	start, end := s.rowRange(s.selectedEntry())
	if end <= start {
		return ""
	}
	pos := fmt.Sprintf("%d/%d", s.cursor-start+1, end-start)

	var body string
	if full := s.selectedRow().get(s.cfg); s.valueWasTruncated() {
		// Spec §10: a truncated value must be shown in full in the help pane.
		body = full
	}

	budget := width - ansi.StringWidth(pos) - 1
	if budget < 1 {
		return pos
	}
	if ansi.StringWidth(body) > budget {
		body = ansi.Truncate(body, budget, "…")
	}
	return body + strings.Repeat(" ", width-ansi.StringWidth(body)-ansi.StringWidth(pos)) + pos
}

// valueWasTruncated reports whether the selected row's value cell had to be shortened to
// fit its pane, which is what obliges the help pane to show it in full (spec §10).
func (s *SettingsOverlay) valueWasTruncated() bool {
	labelW := s.visibleLabelWidth()
	width := s.rowsPaneWidth()
	p := composeRowLine(width, labelW, " ", " ", s.selectedRow().label,
		s.valueCell(s.cursor, width, labelW, ""), "")
	return strings.HasSuffix(p.value, "…")
}

// paneDivider is the vertical rule between the rail and the rows pane, taken from the
// theme's own border rune so it matches the box's rounded/square style.
func paneDivider() string {
	left := theme.Current().Borders.Style.Left
	if lipgloss.Width(left) != 1 {
		// A border style with no single-cell vertical would break every column below it.
		return "|"
	}
	return left
}

// separatorLine is the horizontal rule between the panes and the help pane, with a tee
// where the vertical divider meets it.
//
// It sits inside the box padding rather than spliced into the border: lipgloss v1 has no
// API for a mid-border row (theme.PanelWithBadges hand-composes its top border for exactly
// this reason), and a rule two cells short of the border reads the same. It is omitted
// entirely when the terminal is too short for a help pane, since there is then nothing to
// separate.
func (s *SettingsOverlay) separatorLine() string {
	if s.helpHeight() == 0 {
		return ""
	}
	b := theme.Current().Borders.Style
	h := b.Bottom
	if lipgloss.Width(h) != 1 {
		h = "─"
	}
	inner := s.innerWidth()
	rule := strings.Repeat(h, inner)
	if s.twoPane() {
		tee := b.MiddleBottom
		if lipgloss.Width(tee) != 1 {
			tee = h
		}
		at := railWidth() + 1 // the divider sits one cell into the three-cell gap
		rule = strings.Repeat(h, at) + tee + strings.Repeat(h, inner-at-1)
	}
	return theme.Current().FaintStyle().Render(rule)
}

// hintLine is the key hints for the current focus, at the widest wording that fits.
//
// The hints differ per focus rather than being one static string, because Esc closes from
// the rail but only backs out of the rows pane — advertising the wrong one is how a layered
// Esc becomes surprising instead of discoverable (spec §15). The ladder guarantees that
// "esc back" / "esc close" survives at any width: it is the hint a user stuck in the panel
// needs most.
//
// It deliberately does not advertise `/` or `r`. Search and reset are PR C, and a hint for
// a key that does nothing is worse than no hint at all.
func (s *SettingsOverlay) hintLine() string {
	var ladder []string
	switch {
	case s.editing:
		ladder = []string{"↵ save · esc cancel", "esc cancel"}
	case s.focus == focusRows:
		ladder = []string{
			"↑/↓ move · ←/→ change · ↵ edit · ? more · ⇥ pane · esc back",
			"↑/↓ · ←/→ · ↵ edit · ? more · esc back",
			"↵ edit · ? more · esc back",
			"esc back",
		}
	default:
		ladder = []string{
			"↑/↓ category · → rows · ⇥ pane · esc close",
			"↑/↓ · → rows · esc close",
			"esc close",
		}
	}
	inner := s.innerWidth()
	hint := ladder[len(ladder)-1]
	for _, h := range ladder {
		if ansi.StringWidth(h) <= inner {
			hint = h
			break
		}
	}
	return ansi.Truncate(theme.Current().OverlayHintStyle().Render(hint), inner, "…")
}
```

- [ ] **Step 8: Replace `Render`**

In `settings.go`, replace `Render` and delete `renderBody`, `renderFooter` and
`renderValue`:

```go
// Render draws the panel as a centered bordered box: a title, the rail beside the
// highlighted category's rows, a separator, the fixed-height help pane, and the key hints.
//
// The box's height is a function of the terminal size alone, so it never changes as the
// rail or row cursor moves — a centered overlay that resizes gets re-centered under the
// user mid-navigation.
func (s *SettingsOverlay) Render() string {
	t := theme.Current()

	lines := s.bodyLines()
	if sep := s.separatorLine(); sep != "" {
		lines = append(lines, sep)
	}
	lines = append(lines, s.helpLines()...)
	lines = append(lines, s.hintLine())

	title := t.OverlayTitleStyle().Render("Settings")
	return lipgloss.NewStyle().
		Border(t.Borders.Style).
		BorderForeground(t.Palette.Accent).
		Padding(1, 2).
		Width(s.boxWidth()).
		Render(title + "\n\n" + strings.Join(lines, "\n"))
}
```

and in `settings_render.go`:

```go
// bodyLines is the pane region: the rail beside the rows on a wide terminal. Task 6 adds
// the single-pane fallback for narrow ones.
func (s *SettingsOverlay) bodyLines() []string {
	rows := s.rowsPaneLines()
	rail := s.railLines()

	div := paneDivider()
	out := make([]string, 0, len(rail))
	for i := range rail {
		// Pad each side explicitly rather than using JoinHorizontal: the panes are already
		// exactly railWidth() and rowsPaneWidth() cells, and JoinHorizontal would pad to
		// its own idea of the widest line.
		out = append(out, padRight(rail[i], railWidth())+" "+div+" "+rows[i])
	}
	return out
}
```

Add `"fmt"` and `"github.com/charmbracelet/lipgloss"` to `settings_render.go`'s imports;
`settings.go` keeps `lipgloss` and `strings` and loses `fmt` and `xansi` if nothing else
uses them (the compiler will say).

- [ ] **Step 9: Run the new tests**

```bash
PATH="/home/zvi/.local/share/mise/installs/go/1.26.4/bin:$PATH" \
  go test ./ui/overlay/ -run 'TestSelectingTheLongest|TestHelpHeight|TestRailFits|TestBoxHeight|TestNoPaneLine|TestMaxPaneLines' -v 2>&1 | tail -40
```
Expected: PASS (6 tests). The wider package is still red until Task 10; that is expected.

- [ ] **Step 10: Verify guard 5 actually guards — the most important mutation in this plan**

Guard 5 is the regression test for the defect that motivated the whole redesign. Prove it
would catch that defect coming back:

1. Make the help pane variable-height again, exactly as `renderFooter` was:
   ```go
   func (s *SettingsOverlay) helpHeight() int {
       n := len(strings.Split(ansi.Wrap(s.selectedRow().footerText(), s.innerWidth(), ""), "\n")) + 1
       return clamp(n, 0, s.height-settingsVChrome-settingsMinBody-1)
   }
   ```
2. Run:
   ```bash
   PATH="/home/zvi/.local/share/mise/installs/go/1.26.4/bin:$PATH" \
     go test ./ui/overlay/ -run 'TestSelectingTheLongest|TestHelpHeight|TestBoxHeight' -v 2>&1 | tail -30
   ```
   Expected: `TestSelectingTheLongestHelpRowKeepsTheRowCount` and
   `TestHelpHeightIgnoresTheCursor` **both FAIL**, naming the row whose help is wider than
   one line and reporting the row count it stole. That double failure is the shape of D5.
   `TestBoxHeightDependsOnlyOnTheTerminal` does **not** fail — `paneHeight` absorbs whatever
   `helpBlockHeight` returns, so the box is `height` either way. Do not expect it to; a
   mutation step that predicts the wrong failures teaches you to distrust the next one.
3. Revert by editing `helpHeight` back to the terminal-only form. **Do not
   `git checkout ui/overlay/settings_render.go`** — it would discard this whole task.
4. **Break the pane cap.** Change `maxPaneLines` to `return len(railEntries())`. Expected:
   `TestMaxPaneLinesMatchesTheFlatView` FAILS, and `TestBoxHeightDependsOnlyOnTheTerminal`
   still passes (the cap is terminal-independent) — which is why the cap needs its own test.
   Revert.
5. **Reintroduce the cursor-overwrite bug.** In `windowPane`, set `margin = 0`
   unconditionally. Expected: `TestSelectedRowIsAlwaysVisible` FAILS on the 80×14 and 80×12
   sizes, and guard 5 still passes — the row count is unchanged, only *which* rows are
   visible is wrong. That contrast is the reason both tests exist. Revert.
6. **Reintroduce the rail truncation.** Change `railLines`'s final line to
   `return rendered[:min(len(rendered), s.paneHeight())]`. Expected:
   `TestCurrentRailEntryIsAlwaysVisible` FAILS at 80×20 naming a tail entry, while
   `TestRailFitsUnscrolledAtTheFloor` still passes — it only ever looks at 80×24, where the
   rail fits by exactly zero rows. Revert.
7. **Break the row-line width.** In `rowsPaneContent`, pass `s.innerWidth()` instead of
   `width` to `renderRowLine`. Expected: `TestNoPaneLineOverflowsItsWidth` FAILS in
   two-pane mode — this is guard 6's mutation, which the first draft omitted. Revert.
8. Re-run and confirm green.

- [ ] **Step 11: Lint and commit**

```bash
PATH="/home/zvi/.local/share/mise/installs/go/1.26.4/bin:$PATH" /home/zvi/go/bin/golangci-lint run ./ui/...
git add ui/overlay/settings.go ui/overlay/settings_render.go ui/overlay/settings_render_test.go
git commit -m "feat(settings): two-pane renderer with a fixed-height help pane"
```

---

## Task 6: The single-pane fallback and the derived threshold

**Files:**
- Modify: `ui/overlay/settings_render.go` (`bodyLines`)
- Modify: `ui/overlay/settings_render_test.go`

**Interfaces:**
- Consumes: Task 5's `twoPane()`, `twoPaneMinInner()`, `railLines()`, `rowsPaneLines()`.
- Produces: `bodyLines()` with both layouts. Nothing later depends on the change.

Below the derived threshold the panel becomes **single-pane drill-in**: the rail alone,
`Enter`/`→` opens the category's rows, `Esc` returns. Same mental model, one pane at a
time. The focus model already implements the navigation — this task is only about which
pane gets drawn.

- [ ] **Step 1: Write the failing tests**

```go
// TestSinglePaneFallbackBelowTheThreshold is spec §13's guard 10: below the derived width
// the panel shows one pane at a time, and both are reachable. The two widths are taken
// from twoPaneMinInner() rather than written as literals, so the test follows the
// threshold instead of pinning today's value of it.
func TestSinglePaneFallbackBelowTheThreshold(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	o.SetSize(200, 24) // wide, so twoPaneMinInner() is measured against a stable rail
	minInner := o.twoPaneMinInner()

	// boxWidth = min(96, width-2) and innerWidth = boxWidth-4, so inner == width-6 below
	// the 96 cap. One cell either side of the threshold.
	wide, narrow := minInner+6, minInner+5

	o.SetSize(wide, 24)
	require.True(t, o.twoPane(), "inner %d must afford two panes", o.innerWidth())
	body := stripANSI(strings.Join(o.bodyLines(), "\n"))
	assert.Contains(t, body, "Sessions", "the rail is visible")
	assert.Contains(t, body, "Session limit", "and so are the rows, side by side")

	o.SetSize(narrow, 24)
	require.False(t, o.twoPane(), "inner %d must fall back to one pane", o.innerWidth())

	// Focused on the rail: the rail is the whole body.
	require.Equal(t, focusRail, o.focus)
	railOnly := stripANSI(strings.Join(o.bodyLines(), "\n"))
	assert.Contains(t, railOnly, "Sessions", "the rail is the whole pane")
	assert.NotContains(t, railOnly, "Session limit", "the rows are a separate screen now")

	// Enter drills in; the rows become the whole body and the rail steps aside.
	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEnter})
	require.Equal(t, focusRows, o.focus)
	rowsOnly := stripANSI(strings.Join(o.bodyLines(), "\n"))
	assert.Contains(t, rowsOnly, "Session limit", "the rows are reachable")
	assert.NotContains(t, rowsOnly, "Worktrees & git", "the rail is not drawn beside them")

	// Esc returns, so nothing is a one-way door.
	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEsc})
	assert.Equal(t, focusRail, o.focus)
	assert.Contains(t, stripANSI(strings.Join(o.bodyLines(), "\n")), "Worktrees & git")
}

// TestThresholdIsDerivedFromTheParts pins spec §10's requirement that the threshold be
// "computed from the parts, not hardcoded as a magic number". The proof is that changing a
// part moves it: a longer rail label needs a wider terminal before two panes fit.
//
// A hardcoded threshold would keep offering two panes at a width where the rail had eaten
// the rows pane, which is exactly the failure the derivation prevents.
func TestThresholdIsDerivedFromTheParts(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	o.SetSize(200, 24)

	assert.Equal(t, railWidth()+paneDividerCells+o.minRowsPaneWidth(), o.twoPaneMinInner(),
		"the threshold is the sum of its parts, not a literal")
	assert.Equal(t, rowMarkerCells+o.longestRowLabel()+rowLabelGap+rowMinValueCells,
		o.minRowsPaneWidth(),
		"the minimum rows pane holds the widest label untruncated plus a legible value")

	// At the threshold the rows pane can still show the widest label without truncating it
	// — which is what the threshold is FOR.
	o.SetSize(o.twoPaneMinInner()+6, 24)
	require.True(t, o.twoPane())
	assert.GreaterOrEqual(t, o.rowsPaneWidth(), rowMarkerCells+o.longestRowLabel(),
		"at the threshold the widest label must still fit whole")
}

// TestSinglePaneFallbackShowsTheCategoryName pins that drilling in does not lose the
// orientation the rail was providing. Below the threshold the rail is not drawn, so without
// a header the user is looking at an unlabelled list of rows — D2, the defect this redesign
// exists to fix, reintroduced at narrow widths.
func TestSinglePaneFallbackShowsTheCategoryName(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	o.SetSize(200, 24)
	o.SetSize(o.twoPaneMinInner()+5, 24) // one cell under the threshold
	require.False(t, o.twoPane())

	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEnter}) // drill in
	require.Equal(t, focusRows, o.focus)
	assert.Contains(t, stripANSI(strings.Join(o.bodyLines(), "\n")), o.selectedEntry().label,
		"the drilled-in pane must name the category the rail is no longer showing")

	// And the header must NOT appear in two-pane mode, where the rail already names it.
	o.SetSize(100, 32)
	require.True(t, o.twoPane())
	rows := stripANSI(strings.Join(o.rowsPaneLines(), "\n"))
	assert.NotContains(t, rows, o.selectedEntry().label,
		"a header beside the rail would only repeat it")
}
```

(`TestRailWidthTracksItsLongestLabel` lives in Task 2 — it is the derivation this task's
threshold rests on, and it belongs with the rail vocabulary that defines it.)

- [ ] **Step 2: Run to verify they fail**

```bash
PATH="/home/zvi/.local/share/mise/installs/go/1.26.4/bin:$PATH" \
  go test ./ui/overlay/ -run 'TestSinglePane|TestThresholdIsDerived|TestRailWidthTracks' -v 2>&1 | tail -20
```
Expected: `TestSinglePaneFallbackBelowTheThreshold` FAILS — `bodyLines` still draws both
panes, so the narrow case finds "Session limit" in the rail-only body. The other two
should pass already (they assert the derivation Task 5 built), which is fine: they are
there to stop it being *un*-derived later, and Step 4 proves they would catch that.

- [ ] **Step 3: Add the fallback to `bodyLines`, and the drilled-in header**

First, in `rowsPaneContent` (Task 5), render the entry's own label as a header when the rail
is not beside it. Insert directly after the `if e.kind == railHandoff` early return:

```go
	// Single-pane drill-in hides the rail, so the pane has to name the category itself —
	// otherwise the user is looking at an unlabelled list of rows, which is D2 (no
	// orientation) reintroduced at narrow widths. In two-pane mode the rail already names
	// it and a header would only repeat it.
	var lines []paneLine
	if !s.twoPane() && e.kind == railCategory {
		lines = append(lines, paneLine{
			text:   theme.Current().DimStyle().Bold(true).Render(e.label),
			rowIdx: -1,
		})
	}
```

and delete the later bare `var lines []paneLine` declaration so the header is not discarded.

Then the layout switch:

```go
// bodyLines is the pane region: the rail beside the rows on a wide terminal, or one of
// them on a narrow one.
//
// Below twoPaneMinInner the panel becomes single-pane drill-in (spec §10) — the rail, then
// Enter opens a category's rows and Esc returns. It is the same mental model and the same
// focus state; only the drawing changes, which is why the navigation tests do not care
// which layout is active.
func (s *SettingsOverlay) bodyLines() []string {
	if !s.twoPane() {
		if s.focus == focusRows {
			return s.rowsPaneLines()
		}
		return s.railLines()
	}

	rail := s.railLines()
	rows := s.rowsPaneLines()
	div := paneDivider()
	out := make([]string, 0, len(rail))
	for i := range rail {
		// Pad each side explicitly rather than using JoinHorizontal: the panes are already
		// exactly railWidth() and rowsPaneWidth() cells, and JoinHorizontal would pad to
		// its own idea of the widest line — which for a styled line is not the same number.
		out = append(out, padRight(rail[i], railWidth())+" "+div+" "+rows[i])
	}
	return out
}
```

- [ ] **Step 4: Run the tests and verify the guards guard**

```bash
PATH="/home/zvi/.local/share/mise/installs/go/1.26.4/bin:$PATH" \
  go test ./ui/overlay/ -run 'TestSinglePane|TestThresholdIsDerived|TestRailWidthTracks|TestNoPaneLine' -v 2>&1 | tail -30
```
Expected: PASS.

Then the mutations:

1. **Hardcode the threshold.** Change `twoPaneMinInner` to `return 67`. Run the tests.
   Expected: `TestThresholdIsDerivedFromTheParts` FAILS on the sum assertion — *even
   though 67 is the correct value today*. That is the point: the test rejects the literal,
   not the number. Revert.
2. **Rename a category.** Change `catWorktrees`'s label to
   `"Worktrees, git and pull requests"` in `settings_schema.go`. Run the tests. Expected:
   Task 2's `TestRailWidthTracksItsLongestLabel` FAILS naming the new widest label, while
   `TestSinglePaneFallbackBelowTheThreshold` and `TestThresholdIsDerivedFromTheParts` both
   still pass — they derive their widths, so the threshold moved with the rail exactly as
   intended. Confirm `railWidth()` grew by 16 with a temporary `t.Log`. Revert the label.
3. **Invert the fallback.** Change `if !s.twoPane()` to `if s.twoPane()`. Expected:
   `TestSinglePaneFallbackBelowTheThreshold` FAILS on the wide case. Revert.
4. **Drop the drilled-in header.** Remove the `!s.twoPane()` block from `rowsPaneContent`.
   Expected: `TestSinglePaneFallbackShowsTheCategoryName` FAILS. Then make it
   unconditional and confirm the test's *second* half FAILS instead — the header must be
   absent beside the rail, not merely present when alone. Revert.
5. Re-run and confirm green.

- [ ] **Step 5: Lint and commit**

```bash
PATH="/home/zvi/.local/share/mise/installs/go/1.26.4/bin:$PATH" /home/zvi/go/bin/golangci-lint run ./ui/...
git add ui/overlay/settings_render.go ui/overlay/settings_render_test.go
git commit -m "feat(settings): single-pane fallback below the derived width threshold"
```

---

## Task 7: The visibility layer

**Files:**
- Modify: `ui/overlay/settings_render.go` (`renderRowLine`, `valueCell`, `contextLine`; add
  `inertReasons`, `inertReasonsWithoutPredicate`, `inertReason`, `firstSentence`)
- Modify: `ui/overlay/settings_render_test.go`

**Interfaces:**
- Consumes: `isModified(i)` (`settings.go:88`), `row.timing.badge()`
  (`settings_schema.go:107`), `row.activeWhen`, `row.gloss` — all four landed in PR A,
  tested and unrendered. **This task renders them; it does not re-derive them.**
- Produces: `inertReasons map[string]string`; `(*SettingsOverlay).inertReason(i int)
  string`. Task 9 adds `group_mode`'s branch to `inertReason`.

The inline enum alternatives — the fourth signal — already landed with Task 5's
`valueCell`. This task adds the other three: the modified marker, the timing badge, and
inert dimming with reason chips.

**Reason strings live with the renderer, not the schema** (spec §5): they are rendered
text, positioned by the code that draws them, and PR A deliberately left
`settingRow.activeWhen` carrying only the predicate.

- [ ] **Step 1: Write the failing tests**

```go
// TestModifiedMarkerAndSelectionMarkAreSeparateColumns pins spec §10's explicit
// requirement, and the trap it exists to prevent: a row that is both selected and modified
// must show BOTH marks. Reusing the SelectionMark cell for the modified marker would make
// "changed from default" invisible on exactly the row the user is looking at.
func TestModifiedMarkerAndSelectionMarkAreSeparateColumns(t *testing.T) {
	cfg := config.DefaultConfig()
	o := NewSettingsOverlay(cfg)
	o.SetSize(100, 32)
	settingsAt(t, o, "mouse")
	i := o.cursor
	require.False(t, o.isModified(i), "mouse starts at its default")

	// Toggle it off, so the selected row is also modified.
	_, changed := o.HandleKeyPress(tea.KeyMsg{Type: tea.KeySpace})
	require.Equal(t, "mouse", changed)
	require.True(t, o.isModified(i))

	g := theme.Current().Glyphs
	line := stripANSI(o.renderRowLine(i, o.rowsPaneWidth(), o.visibleLabelWidth()))
	assert.Containsf(t, line, g.SelectionMark+g.Modified,
		"a selected+modified row shows both marks in adjacent cells: %q", line)
}

// TestUnmodifiedRowShowsNoMarker pins the negative direction. Without it the marker could
// be hardwired on and every assertion above would still pass.
func TestUnmodifiedRowShowsNoMarker(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	o.SetSize(100, 32)
	settingsAt(t, o, "theme")
	marked := 0
	for i := range o.rows {
		if strings.Contains(stripANSI(o.renderRowLine(i, o.rowsPaneWidth(), o.visibleLabelWidth())),
			theme.Current().Glyphs.Modified) {
			marked++
		}
	}
	assert.Zero(t, marked, "no row may show a modified marker on a fresh DefaultConfig (guard 2's render twin)")
}

// TestTimingBadgeRendersForEveryNonLiveRow pins that applyTiming.badge() reaches the
// screen. PR A declared badge() and nothing called it; a projection no renderer reads is
// the same bug class TestEveryCautionReachesTheFooter caught.
func TestTimingBadgeRendersForEveryNonLiveRow(t *testing.T) {
	cfg := config.DefaultConfig()
	o := NewSettingsOverlay(cfg)
	o.SetSize(100, 32) // wide, so no badge is dropped for width

	badged := 0
	for i, r := range o.rows {
		if r.timing == timingLive || o.inertReason(i) != "" {
			continue // an inert row's chip replaces its badge, tested separately below
		}
		badged++
		line := stripANSI(o.renderRowLine(i, o.rowsPaneWidth(), o.visibleLabelWidth()))
		assert.Containsf(t, line, r.timing.badge(),
			"row %q must render its %q badge: %q", r.key, r.timing.badge(), line)
	}
	require.Positive(t, badged, "the schema must declare rows that are not timingLive")
}

// TestEveryInertPredicateHasAReason pins the drift guard that matters most here: a row
// dimmed with no explanation is worse than a row not dimmed at all, because the user sees
// something is off and has nothing to act on. Adding a seventh activeWhen predicate without
// a reason chip fails here.
func TestEveryInertPredicateHasAReason(t *testing.T) {
	predicated := map[string]bool{}
	for _, r := range newSettingRows(config.DefaultConfig()) {
		if r.activeWhen == nil {
			continue
		}
		predicated[r.key] = true
		assert.NotEmptyf(t, inertReasons[r.key],
			"row %q declares activeWhen but has no reason chip — it would dim with no explanation", r.key)
	}
	require.NotEmpty(t, predicated, "the schema must declare at least one activeWhen")

	for key := range inertReasons {
		if reason, ok := inertReasonsWithoutPredicate[key]; ok {
			assert.NotEmptyf(t, reason, "exception %q must document why it has no predicate", key)
			continue
		}
		assert.Truef(t, predicated[key],
			"inertReasons names %q, which declares no activeWhen — a stale entry that can never render", key)
	}

	// The exception list itself must not rot: an entry naming a row that has since gained a
	// predicate would silently disable the guard for it.
	for key := range inertReasonsWithoutPredicate {
		assert.NotEmptyf(t, inertReasons[key], "exception %q names no reason chip", key)
		assert.Falsef(t, predicated[key],
			"row %q now declares activeWhen, so it no longer needs an exception", key)
	}
}

// TestInertRowIsDimmedAndChippedAndStillEditable is spec §13's guard 7, all three clauses.
// The transitions are driven through the real config so the predicate is exercised rather
// than the map: Notifications off makes Finished turns inert, and switching to desktop makes
// Notify command active.
func TestInertRowIsDimmedAndChippedAndStillEditable(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Notifications = config.NotificationsOff
	o := NewSettingsOverlay(cfg)
	o.SetSize(100, 32)

	// Park the cursor elsewhere: a selected row is accented, so dimming can only be
	// observed on an unselected one.
	settingsAt(t, o, "notifications")
	finished := indexOfRowKey(t, o, "notifications_finished")
	require.NotEqual(t, o.cursor, finished)

	raw := o.renderRowLine(finished, o.rowsPaneWidth(), o.visibleLabelWidth())
	assert.Equal(t, "needs Notifications", o.inertReason(finished))
	assert.Contains(t, stripANSI(raw), "needs Notifications", "the reason chip must be on the row")
	assert.Equal(t, theme.Current().FaintStyle().Render(stripANSI(raw)), raw,
		"an inert row is rendered in the faint style")

	// Still fully editable: inert means "changing this has no effect right now", never
	// "you may not touch this" (spec §5).
	settingsAt(t, o, "notifications_finished")
	_, changed := o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRight})
	assert.Equal(t, "notifications_finished", changed, "an inert row still cycles")

	// And the transition works the other way: desktop mode activates Notify command.
	cfg.Notifications = config.NotificationsDesktop
	cmd := indexOfRowKey(t, o, "notify_command")
	assert.Empty(t, o.inertReason(cmd), "desktop mode activates notify_command")
	assert.NotContains(t, stripANSI(o.renderRowLine(cmd, o.rowsPaneWidth(), o.visibleLabelWidth())),
		"needs desktop", "an active row carries no chip")
}

// TestInertChipReplacesTheTimingBadge pins that the two do not both try to occupy the
// right-aligned column. A row that currently does nothing has more urgent news than when it
// would take effect.
func TestInertChipReplacesTheTimingBadge(t *testing.T) {
	cfg := config.DefaultConfig()
	f := false
	cfg.UpdateBaseOnCreate = &f // makes fast_forward_local_base inert
	o := NewSettingsOverlay(cfg)
	o.SetSize(100, 32)

	i := indexOfRowKey(t, o, "fast_forward_local_base")
	require.Equal(t, timingNewSessions, o.rows[i].timing)
	line := stripANSI(o.renderRowLine(i, o.rowsPaneWidth(), o.visibleLabelWidth()))
	assert.Contains(t, line, "needs Update base on create")
	assert.NotContains(t, line, timingNewSessions.badge(), "the chip takes the badge's column")
}

// TestContextLineExplainsAnInertRowInProse pins the help pane's half of the signal. The chip
// is three words in a column and is easy to misread as a prohibition; the sentence says what
// it actually means.
func TestContextLineExplainsAnInertRowInProse(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.AutoYes = false
	o := NewSettingsOverlay(cfg)
	o.SetSize(100, 32)
	settingsAt(t, o, "daemon_poll_interval")

	ctx := stripANSI(o.contextLine(o.innerWidth()))
	assert.Contains(t, ctx, "No effect right now")
	assert.Contains(t, ctx, "needs Auto-yes")
}

// TestContextLineShowsAGlossForTheCurrentEnumOption pins that gloss reaches the help pane on
// an ordinary row, which is what makes cycling an enum teach rather than guess.
func TestContextLineShowsAGlossForTheCurrentEnumOption(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Notifications = config.NotificationsOSC
	o := NewSettingsOverlay(cfg)
	o.SetSize(100, 32)
	settingsAt(t, o, "notifications")

	row := o.selectedRow()
	require.NotEmpty(t, row.gloss[config.NotificationsOSC], "the schema glosses osc")
	assert.Contains(t, stripANSI(o.contextLine(o.innerWidth())), row.gloss[config.NotificationsOSC])
}

// TestContextLineShowsATruncatedValueInFull pins spec §10's obligation: the value column may
// be shortened, but the full value must appear in the help pane, or the truncation loses
// information outright.
func TestContextLineShowsATruncatedValueInFull(t *testing.T) {
	cfg := config.DefaultConfig()
	o := NewSettingsOverlay(cfg)
	o.SetSize(80, 24)
	settingsAt(t, o, "carry_files")

	full := o.selectedRow().get(cfg)
	require.True(t, o.valueWasTruncated(),
		"carry_files' default (%d cells) must not fit an 80-col rows pane (%d cells), or this "+
			"test proves nothing", ansi.StringWidth(full), o.rowsPaneWidth())
	assert.Contains(t, stripANSI(o.contextLine(o.innerWidth())), full,
		"a truncated value must be shown in full in the help pane")
}

// TestPositionReadoutSurvivesInTheRenderedPane pins the orientation readout (D2: scrolled to
// the bottom of the old list you could not tell where you were).
//
// It asserts through helpLines(), NOT through contextLine() directly. That distinction is the
// whole test: the first draft called contextLine and would have passed while the rendered
// pane threw the counter away — helpLines appended it and *then* capped the list, so any row
// whose prose filled the pane evicted it silently.
func TestPositionReadoutSurvivesInTheRenderedPane(t *testing.T) {
	for _, size := range []struct{ w, h int }{{100, 32}, {80, 24}, {56, 24}, {40, 24}} {
		t.Run(fmt.Sprintf("%dx%d", size.w, size.h), func(t *testing.T) {
			o := NewSettingsOverlay(config.DefaultConfig())
			o.SetSize(size.w, size.h)
			for _, key := range []string{
				"default_program", "carry_files", "notifications", "config_file",
				"fast_forward_local_base", // the widest footer: the eviction case
			} {
				settingsAt(t, o, key)
				start, end := o.rowRange(o.selectedEntry())
				want := fmt.Sprintf("%d/%d", o.cursor-start+1, end-start)
				pane := stripANSI(strings.Join(o.helpLines(), "\n"))
				assert.Containsf(t, pane, want,
					"row %q's help pane must carry the position %q at %dx%d:\n%s",
					key, want, size.w, size.h, pane)
			}
		})
	}
}

// TestVisibilitySignalsSurviveTheDegradationFloor is the guard the first draft was missing
// entirely: every other test in this task runs at 100x32, where nothing competes for width.
//
// At 80x24 — the project's floor, and the size the whole design is budgeted against — the
// rows pane is 52 cells, and an inline enum rendering that spends all of it leaves no room
// for the badge. The badge is what carries the inert reason, so the row would dim with no
// explanation: exactly what inertReasons' own doc comment calls worse than not dimming at
// all. Two rows in Notifications would disagree about it, one explained and one not.
func TestVisibilitySignalsSurviveTheDegradationFloor(t *testing.T) {
	for _, size := range []struct{ w, h int }{{100, 32}, {80, 24}, {76, 24}, {73, 24}} {
		t.Run(fmt.Sprintf("%dx%d", size.w, size.h), func(t *testing.T) {
			cfg := config.DefaultConfig()
			cfg.Notifications = config.NotificationsOff // inerts two Notifications rows
			f := false
			cfg.UpdateBaseOnCreate = &f // inerts fast_forward_local_base
			o := NewSettingsOverlay(cfg)
			o.SetSize(size.w, size.h)

			checked := 0
			for i, r := range o.rows {
				chip := o.inertReason(i)
				if chip == "" {
					continue
				}
				checked++
				o.railCursor = railIndexForCategory(r.category)
				o.syncCursorToRail()
				line := stripANSI(o.renderRowLine(i, o.rowsPaneWidth(), o.visibleLabelWidth()))
				assert.Containsf(t, line, chip,
					"inert row %q lost its %q chip at %dx%d — it dims with no explanation: %q",
					r.key, chip, size.w, size.h, line)
			}
			require.Positive(t, checked, "the fixture must make at least one row inert")
		})
	}
}

// indexOfRowKey returns the row index for a key without moving the cursor, so a test can
// render a row it is not sitting on.
func indexOfRowKey(t *testing.T, o *SettingsOverlay, key string) int {
	t.Helper()
	for i, r := range o.rows {
		if r.key == key {
			return i
		}
	}
	t.Fatalf("no settings row with key %q", key)
	return -1
}
```

Add `"github.com/ZviBaratz/atrium/ui/theme"` to the test imports.

- [ ] **Step 2: Run to verify they fail**

```bash
PATH="/home/zvi/.local/share/mise/installs/go/1.26.4/bin:$PATH" \
  go test ./ui/overlay/ -run 'TestModifiedMarker|TestUnmodifiedRow|TestTimingBadge|TestEveryInertPredicate|TestInertRowIs|TestInertChip|TestContextLine' -v 2>&1 | head -20
```
Expected: FAIL to build — `o.inertReason undefined`, `undefined: inertReasons`.

- [ ] **Step 3: Add the reason map and predicate**

```go
// inertReasons is the right-aligned chip for each row that declares an activeWhen
// predicate (spec §5's table). Reason strings live here rather than in the schema because
// they are rendered text: PR A deliberately gave settingRow only the predicate, leaving the
// wording to the renderer that positions it.
//
// Every predicated row must appear here — a row dimmed with no explanation is worse than a
// row not dimmed at all, because the user can see something is off and has nothing to act
// on. TestEveryInertPredicateHasAReason enforces both directions.
var inertReasons = map[string]string{
	"notifications_finished":  "needs Notifications",
	"notify_when_focused":     "needs Notifications",
	"notify_command":          "needs desktop mode",
	"fast_forward_local_base": "needs Update base on create",
	"daemon_poll_interval":    "needs Auto-yes",
	"agent_oom_margin":        "Linux only",
	// group_mode is here even though it declares no activeWhen: its gate is session-derived
	// and injected by home (Task 9), not expressible as a config predicate. Keeping the
	// string in this map rather than inline in inertReason is what keeps every reason chip
	// in one place, and it is the one key TestEveryInertPredicateHasAReason must exempt from
	// its "no stale entries" direction.
	"group_mode": "nothing to cluster",
}

// inertReasonsWithoutPredicate names the rows whose reason chip is driven by something other
// than settingRow.activeWhen, so the completeness guard can tell a deliberate exception from
// a stale entry.
var inertReasonsWithoutPredicate = map[string]string{
	"group_mode": "gate is session-derived; home injects it via SetAccountClusteringVisible",
}

// inertReason returns row i's reason chip when changing it currently has no effect, or ""
// when the row is live. Task 9 adds group_mode's branch, whose gate is session-derived and
// therefore not expressible as an activeWhen predicate at all.
func (s *SettingsOverlay) inertReason(i int) string {
	row := s.rows[i]
	if row.activeWhen == nil || row.activeWhen(s.cfg) {
		return ""
	}
	return inertReasons[row.key]
}
```

- [ ] **Step 4: Extend `renderRowLine` with the three signals**

Replace the composing branch of `renderRowLine` (the editor branch above it is unchanged):

```go
	modified := " "
	if s.isModified(i) {
		modified = t.Glyphs.Modified
	}

	// The badge column carries the apply timing — unless the row is inert, in which case
	// the reason takes it: a row that does nothing right now has more urgent news than when
	// it would take effect. Either way spec §10 drops this column first when the pane is
	// narrow, which is why the help pane repeats both in prose.
	inert := s.inertReason(i)
	badge := row.timing.badge()
	if inert != "" {
		badge = inert
	}

	// The badge is passed to valueCell so an enum's inline alternatives step down to the
	// compact form rather than squeezing the badge out. This refines spec §10: §10 ordered
	// badge-before-value but never contemplated the alternatives competing with the badge,
	// and at the 80-column floor they do — a 19-cell chip against a 28-cell slack.
	p := composeRowLine(width, labelW, sel, modified, row.label,
		s.valueCell(i, width, labelW, badge), badge)
	switch {
	case selected:
		// Accent wins over dimming: the row under the cursor must stay legible. The chip
		// still says the row is inert, and the help pane spells it out.
		return rowStyle.Render(p.plain())
	case inert != "":
		// Dimmed, not hidden. Inert means "changing this has no effect right now", never
		// "you may not touch this" — a user may configure ahead of enabling the parent
		// (spec §5).
		return t.FaintStyle().Render(p.plain())
	default:
		return t.DimStyle().Render(p.head) +
			t.FgStyle().Render(p.value+p.gap) +
			t.FaintStyle().Render(p.badge)
	}
```

- [ ] **Step 5: Extend `contextLine`**

Replace its `body` computation:

```go
	var body string
	switch chip := s.inertReason(s.cursor); {
	case s.valueWasTruncated():
		// Spec §10: a truncated value must be shown in full here, or the truncation loses
		// information rather than deferring it.
		body = s.selectedRow().get(s.cfg)
	case chip != "":
		// The chip is three words in a column and is easy to misread as a prohibition;
		// this is the sentence that says what it actually means.
		body = "No effect right now — " + chip + "."
	default:
		// The current option's gloss, which is what makes cycling an enum teach rather
		// than guess (D8) — and failing that, the first sentence of detail.
		//
		// The detail fallback is what makes spec §3's mockup literal: its second help line
		// for Notifications ("The selected, attached and muted sessions stay silent.") is
		// that row's detail, not its summary. Without it a row whose long-form help is only
		// detail shows one prose line and a blank, and the 443 characters PR A moved stay
		// invisible until `?`.
		row := s.selectedRow()
		body = row.gloss[row.get(s.cfg)]
		if body == "" {
			body = firstSentence(row.detail)
		}
	}
```

and add the helper beside `contextLine`:

```go
// firstSentence returns s up to and including its first sentence-ending period, or "" when
// there is none. It is used to surface one line of a row's detail in the help pane without
// the pane trying to render a paragraph it has no room for.
func firstSentence(s string) string {
	if i := strings.Index(s, ". "); i >= 0 {
		return s[:i+1]
	}
	if strings.HasSuffix(s, ".") {
		return s
	}
	return ""
}
```

- [ ] **Step 6: Run the tests**

```bash
PATH="/home/zvi/.local/share/mise/installs/go/1.26.4/bin:$PATH" \
  go test ./ui/overlay/ -run 'TestModifiedMarker|TestUnmodifiedRow|TestTimingBadge|TestEveryInertPredicate|TestInertRowIs|TestInertChip|TestContextLine|TestNoPaneLine|TestSelectingTheLongest' -v 2>&1 | tail -40
```
Expected: PASS. If `TestContextLineShowsATruncatedValueInFull`'s precondition fails,
`carry_files`' default now fits the 80-column pane — re-derive the width in the test rather
than weakening the assertion, and record the new number in its comment.

- [ ] **Step 7: Verify each signal's guard independently**

Four mutations, one per signal, because a single mutation only proves one net.

1. **Reuse the selection cell.** In `renderRowLine`, change `modified := " "` to
   `modified := ""` and drop the `sel +` from the head. Expected:
   `TestModifiedMarkerAndSelectionMarkAreSeparateColumns` FAILS, and
   `TestComposeRowLineKeepsTheMarkerColumnsSeparate` still passes (it tests the composer,
   which is still correct) — proving the two tests cover different layers. Revert.
2. **Hardwire the marker on.** `modified := t.Glyphs.Modified` unconditionally. Expected:
   `TestUnmodifiedRowShowsNoMarker` FAILS with a non-zero count. Revert.
3. **Drop the badge.** Pass `""` as `composeRowLine`'s badge. Expected:
   `TestTimingBadgeRendersForEveryNonLiveRow` and `TestInertChipReplacesTheTimingBadge`
   both FAIL. Revert.
4. **Delete a reason.** Remove `"agent_oom_margin"` from `inertReasons`. Expected:
   `TestEveryInertPredicateHasAReason` FAILS naming it. Then *add* a stale entry
   (`"branch_prefix": "nope"`) and confirm the reverse assertion FAILS too. Then delete
   `"group_mode"` from `inertReasonsWithoutPredicate` and confirm the reverse assertion
   catches it as stale — proving the exception is an exception and not a hole. Revert all
   three.
5. **Stop reserving the badge's width.** Pass `""` as `valueCell`'s `badge`. Expected:
   `TestVisibilitySignalsSurviveTheDegradationFloor` FAILS at 80×24 on
   `notifications_finished` — and every other test in this task still passes, because they
   all run at 100×32. That contrast is the entire reason the sweep exists. Revert.
6. **Misalign the editor.** In `renderRowLine`'s editing branch, drop one of the two spaces
   after `sel`. Expected: `TestEditingRowKeepsTheLabelColumn` (Task 7 Step 1a below) FAILS.
   Revert.
7. Re-run and confirm green.

- [ ] **Step 1a: also add the editor-alignment test** (it belongs with the marker columns)

```go
// TestEditingRowKeepsTheLabelColumn pins that opening the inline editor does not shift the
// label. The editing branch builds its own head rather than going through composeRowLine, so
// it has to spend the same three marker cells — otherwise every label jumps sideways the
// instant Enter is pressed, which reads as the panel glitching.
func TestEditingRowKeepsTheLabelColumn(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	o.SetSize(100, 32)
	settingsAt(t, o, "branch_prefix")
	i := o.cursor
	labelW, width := o.visibleLabelWidth(), o.rowsPaneWidth()

	before := stripANSI(o.renderRowLine(i, width, labelW))
	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEnter}) // open the editor
	require.True(t, o.editing)
	after := stripANSI(o.renderRowLine(i, width, labelW))

	at := func(line string) int { return ansi.StringWidth(line[:strings.Index(line, "Branch prefix")]) }
	assert.Equal(t, at(before), at(after), "the label column must not move when editing opens")
	assert.Equal(t, rowMarkerCells, at(after))
}
```

- [ ] **Step 8: Lint and commit**

```bash
PATH="/home/zvi/.local/share/mise/installs/go/1.26.4/bin:$PATH" /home/zvi/go/bin/golangci-lint run ./ui/...
git add ui/overlay/settings_render.go ui/overlay/settings_render_test.go
git commit -m "feat(settings): draw the modified marker, timing badge and inert reasons"
```

---

## Task 8: `?` expanded help

**Files:**
- Modify: `ui/overlay/settings.go` (struct: `helpOpen`, `helpScroll`; `HandleKeyPress`
  router; `Render`)
- Modify: `ui/overlay/settings_nav.go` (`handleRowsKey`'s `?` case; `handleHelpKey`)
- Modify: `ui/overlay/settings_render.go` (`expandedHelpContent`, `expandedHelpLines`,
  `maxHelpScroll`, `hintLine`)
- Modify: `ui/overlay/settings_render_test.go`

**Interfaces:**
- Consumes: `row.detail`, `row.gloss`, `row.caution`, `row.timing.footerNote()` — all PR A
  fields. Task 7's `inertReason`.
- Produces: `(*SettingsOverlay).expandedHelpContent(i int) string`, `.expandedHelpLines()
  []string`, `.maxHelpScroll() int`, `.handleHelpKey(tea.KeyMsg)`.

This is the surface `detail` was written for. PR A stored it and rendered nothing, and
`TestDetailRetainsTheMovedProse` pins the *strings* — but a string in a field no renderer
reads is invisible, which is precisely the bug class
`TestEveryCautionReachesTheFooter` was written to catch. The guard here is its twin.

- [ ] **Step 1: Write the failing tests**

```go
// TestEveryDetailAndGlossReachExpandedHelp is the render-level twin of
// TestDetailRetainsTheMovedProse, and it guards the same bug class as
// TestEveryCautionReachesTheFooter: help copy living in a field the renderer never reads is
// invisible, and a test that only pins the field's contents still passes.
//
// PR A moved as much as 443 characters per row out of `description` into `detail` and
// rendered none of it. This is the test that says it is now visible.
func TestEveryDetailAndGlossReachExpandedHelp(t *testing.T) {
	cfg := config.DefaultConfig()
	o := NewSettingsOverlay(cfg)
	o.SetSize(100, 32)

	details, glosses := 0, 0
	for i, r := range o.rows {
		content := o.expandedHelpContent(i)
		assert.Containsf(t, content, r.label, "row %q's expanded help must name the row", r.key)
		assert.Containsf(t, content, r.summary, "row %q's summary must reach expanded help", r.key)
		if r.detail != "" {
			details++
			assert.Containsf(t, content, r.detail, "row %q's detail must reach expanded help", r.key)
		}
		if r.caution != "" {
			assert.Containsf(t, content, r.caution, "row %q's caution must reach expanded help", r.key)
		}
		if r.kind != kindEnum {
			continue
		}
		for _, opt := range r.options(cfg) {
			assert.Containsf(t, content, opt, "row %q's option %q must be listed", r.key, opt)
			if g := r.gloss[opt]; g != "" {
				glosses++
				assert.Containsf(t, content, g, "row %q's gloss for %q must reach expanded help", r.key, opt)
			}
		}
	}
	// Without these the loops could stop running and the test would still pass.
	require.Positive(t, details, "the schema must declare rows with detail")
	require.Positive(t, glosses, "the schema must declare glossed enum options")
}

// TestExpandedHelpShowsTheCurrentValueInFull pins that `?` is the escape hatch for a value
// the row line and even the help pane's context line had to shorten.
func TestExpandedHelpShowsTheCurrentValueInFull(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.ProjectSearchRoots = []string{strings.Repeat("~/deeply/nested/path", 8)}
	o := NewSettingsOverlay(cfg)
	o.SetSize(80, 24)
	i := indexOfRowKey(t, o, "project_search_roots")
	assert.Contains(t, o.expandedHelpContent(i), cfg.ProjectSearchRoots[0])
}

// TestExpandedHelpNamesTheApplyTimingForEveryRow pins that the timing is stated in words
// rather than only as a badge, since `?` is where a user goes when the badge was not enough.
func TestExpandedHelpNamesTheApplyTimingForEveryRow(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	o.SetSize(100, 32)
	for i, r := range o.rows {
		content := o.expandedHelpContent(i)
		want := r.timing.footerNote()
		if want == "" {
			want = "applies immediately" // timingLive has no footer note by design
		}
		assert.Containsf(t, content, want, "row %q's expanded help must state its apply timing", r.key)
	}
}

// TestQuestionMarkOpensAndClosesExpandedHelp pins the key grammar of spec §8: `?` opens from
// the rows pane, esc or a second `?` returns to whatever was focused, and an unrecognized key
// does NOT dismiss — the panel is a working surface, and closing on a stray keystroke would
// lose the user's place in the rail.
func TestQuestionMarkOpensAndClosesExpandedHelp(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	o.SetSize(100, 32)
	settingsAt(t, o, "group_mode")

	o.HandleKeyPress(keyRunes("?"))
	require.True(t, o.helpOpen)
	// Assert on the unwrapped content, not the render: ansi.Wrap breaks group_mode's detail
	// mid-phrase at inner 92, so a Contains against Render() fails for a reason that has
	// nothing to do with whether the detail arrived. TestEveryDetailAndGlossReachExpandedHelp
	// covers the content; what this test needs from the render is only that `?` took over.
	assert.Contains(t, o.expandedHelpContent(o.cursor), "an account boundary is refused",
		"the expanded view must carry the row's detail")
	assert.Contains(t, stripANSI(o.Render()), "Account clustering",
		"the expanded view is titled with the row's label")
	assert.NotContains(t, stripANSI(o.Render()), "Worktrees & git",
		"and it takes over the box, so the rail is not drawn beside it")

	o.HandleKeyPress(keyRunes("x"))
	assert.True(t, o.helpOpen, "an unrecognized key must not dismiss the help view")

	o.HandleKeyPress(keyRunes("?"))
	assert.False(t, o.helpOpen, "a second ? closes it")
	assert.Equal(t, focusRows, o.focus, "and returns to the pane that was focused")

	o.HandleKeyPress(keyRunes("?"))
	require.True(t, o.helpOpen)
	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEsc})
	assert.False(t, o.helpOpen, "esc closes it too")
	assert.Equal(t, focusRows, o.focus, "esc must not also back out of the rows pane")
}

// TestExpandedHelpScrolls pins that long detail is reachable rather than clipped — the
// content that does not fit is exactly the content `?` exists to show.
//
// The SIZE is chosen to guarantee overflow rather than to be representative. At 80x24 the
// budget is paneHeight(13) + helpBlock(4) = 17 lines against inner 74, and no row in the
// schema wraps past 17 there — the first draft asserted otherwise and could not pass. 60x20
// gives a 13-line budget against inner 54, where max_sessions' 343-character detail alone
// needs seven lines.
func TestExpandedHelpScrolls(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	o.SetSize(60, 20)
	settingsAt(t, o, "max_sessions") // the longest detail literal in the schema
	o.HandleKeyPress(keyRunes("?"))
	require.Positive(t, o.maxHelpScroll(),
		"this row's help must overflow a %d-line budget at inner %d, or scrolling is untested",
		o.expandedHelpHeight(), o.innerWidth())

	top := stripANSI(strings.Join(o.expandedHelpLines(), "\n"))
	for range 40 {
		o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyDown})
	}
	assert.Equal(t, o.maxHelpScroll(), o.helpScroll, "↓ must clamp at the end, not run past it")
	bottom := stripANSI(strings.Join(o.expandedHelpLines(), "\n"))
	assert.NotEqual(t, top, bottom, "scrolling must change what is shown")
	assert.Contains(t, bottom, "Current value",
		"scrolling to the end must reach the last section, which no unscrolled view showed")
	assert.NotContains(t, top, "Current value",
		"or the assertion above proves nothing about scrolling")

	for range 40 {
		o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyUp})
	}
	assert.Zero(t, o.helpScroll, "↑ must clamp at the top")
}

// TestExpandedHelpDoesNotChangeTheBoxHeight pins that opening `?` cannot resize the panel.
// PlaceOverlay centers the box, so a height change would re-center it under the user's
// cursor the instant they press `?`.
func TestExpandedHelpDoesNotChangeTheBoxHeight(t *testing.T) {
	for _, size := range []struct{ w, h int }{{100, 32}, {80, 24}, {60, 20}, {80, 12}} {
		t.Run(fmt.Sprintf("%dx%d", size.w, size.h), func(t *testing.T) {
			o := NewSettingsOverlay(config.DefaultConfig())
			o.SetSize(size.w, size.h)
			settingsAt(t, o, "agent_oom_margin")
			closed := lipgloss.Height(o.Render())
			o.HandleKeyPress(keyRunes("?"))
			require.True(t, o.helpOpen)
			assert.Equal(t, closed, lipgloss.Height(o.Render()), "opening ? must not resize the box")
		})
	}
}
```

- [ ] **Step 2: Run to verify they fail**

```bash
PATH="/home/zvi/.local/share/mise/installs/go/1.26.4/bin:$PATH" \
  go test ./ui/overlay/ -run 'TestEveryDetailAndGloss|TestExpandedHelp|TestQuestionMark' -v 2>&1 | head -20
```
Expected: FAIL to build — `o.expandedHelpContent undefined`, `o.helpOpen undefined`.

- [ ] **Step 3: Add the state and the key wiring**

In `settings.go`'s struct, after `railCursor`:

```go
	// helpOpen is the `?` expanded-help view, with its own scroll offset. It takes over
	// the box rather than opening a second overlay, so the panel's focus and rail cursor
	// survive it untouched.
	helpOpen   bool
	helpScroll int
```

In `HandleKeyPress`, insert the branch between `s.editing` and the focus switch:

```go
	case s.helpOpen:
		s.handleHelpKey(msg)
		return false, ""
```

In `handleRowsKey`, add before `case "enter":`:

```go
	case "?":
		s.helpOpen = true
		s.helpScroll = 0
```

And in `settings_nav.go`:

```go
// handleHelpKey routes a key while `?` is open: ↑/↓ and PgUp/PgDn scroll, esc or a second ?
// returns to whatever was focused before (spec §8).
//
// Unlike TextOverlay — where any unrecognized key dismisses — a stray keystroke here is
// ignored. The settings panel is a working surface with a rail position and a row cursor
// worth keeping; dismissing on an accidental key would lose both.
func (s *SettingsOverlay) handleHelpKey(msg tea.KeyMsg) {
	switch msg.String() {
	case "esc", "ctrl+c", "?":
		s.helpOpen = false
		s.helpScroll = 0
	case "up", "k":
		s.helpScroll = max(0, s.helpScroll-1)
	case "down", "j":
		s.helpScroll = min(s.maxHelpScroll(), s.helpScroll+1)
	case "pgup":
		s.helpScroll = max(0, s.helpScroll-s.paneHeight())
	case "pgdown":
		s.helpScroll = min(s.maxHelpScroll(), s.helpScroll+s.paneHeight())
	}
}
```

- [ ] **Step 4: Add the content builder**

In `settings_render.go`:

```go
// expandedHelpContent assembles everything the panel knows about row i, in reading order:
// what it does, the caution, when a change takes effect, the long-form detail, one line per
// enum option from gloss, and the current value in full.
//
// This is the surface settingRow.detail and settingRow.gloss were written for. PR A stored
// both and rendered neither, so up to 443 characters per row existed only in the source
// until now — see TestEveryDetailAndGlossReachExpandedHelp.
func (s *SettingsOverlay) expandedHelpContent(i int) string {
	row := s.rows[i]
	var b strings.Builder

	b.WriteString(row.label + "\n\n" + row.summary + "\n")
	if row.caution != "" {
		b.WriteString("\nCaution: " + row.caution + ".\n")
	}
	if note := row.timing.footerNote(); note != "" {
		b.WriteString("\nA change " + note + ".\n")
	} else {
		// timingLive has no footer note by design — saying "live" on 25 of 38 rows would be
		// noise in the footer. Here there is room to say it.
		b.WriteString("\nA change applies immediately.\n")
	}
	if chip := s.inertReason(i); chip != "" {
		b.WriteString("\nNo effect right now — " + chip + ". You can still change it now and " +
			"it will apply once the parent setting is on.\n")
	}
	if row.detail != "" {
		b.WriteString("\n" + row.detail + "\n")
	}
	if row.kind == kindEnum {
		if opts := row.options(s.cfg); len(opts) > 0 {
			b.WriteString("\nOptions:\n")
			for _, o := range opts {
				if g := row.gloss[o]; g != "" {
					b.WriteString("  " + o + " — " + g + "\n")
					continue
				}
				b.WriteString("  " + o + "\n")
			}
		}
	}
	b.WriteString("\nCurrent value: " + row.get(s.cfg) + "\n")
	return b.String()
}

// expandedHelpHeight is the number of lines `?` may fill: the panes plus the help block, so
// the box's height is identical open or closed. A centered overlay that resized on `?` would
// jump under the user's cursor.
func (s *SettingsOverlay) expandedHelpHeight() int {
	return s.paneHeight() + s.helpBlockHeight()
}

// expandedHelpWrapped is the content wrapped to the inner width, unwindowed.
func (s *SettingsOverlay) expandedHelpWrapped() []string {
	return strings.Split(ansi.Wrap(s.expandedHelpContent(s.cursor), s.innerWidth(), ""), "\n")
}

// maxHelpScroll is the furthest the expanded help can scroll, so both the key handler and
// the renderer clamp to the same number.
func (s *SettingsOverlay) maxHelpScroll() int {
	return max(0, len(s.expandedHelpWrapped())-s.expandedHelpHeight())
}

// expandedHelpLines renders the `?` view: the wrapped content, scrolled and padded to
// exactly expandedHelpHeight() lines, with a position readout when it overflows.
func (s *SettingsOverlay) expandedHelpLines() []string {
	t := theme.Current()
	lines := s.expandedHelpWrapped()
	budget := s.expandedHelpHeight()

	s.helpScroll = clamp(s.helpScroll, 0, s.maxHelpScroll())
	end := min(len(lines), s.helpScroll+budget)

	out := make([]string, 0, budget)
	for _, l := range lines[s.helpScroll:end] {
		out = append(out, t.DimStyle().Render(l))
	}
	for len(out) < budget {
		out = append(out, "")
	}
	if s.maxHelpScroll() > 0 {
		// Overwrite the last line rather than adding one, so the height holds.
		out[budget-1] = t.FaintStyle().Render(fmt.Sprintf("  %d/%d", end, len(lines)))
	}
	return out
}
```

- [ ] **Step 5: Route `Render` through it, and extend the hint ladder**

In `settings.go`'s `Render`, replace the body assembly:

```go
	var lines []string
	if s.helpOpen {
		// The expanded view fills the panes and the help block together, so the box's
		// height does not change when it opens.
		lines = s.expandedHelpLines()
	} else {
		lines = s.bodyLines()
		if sep := s.separatorLine(); sep != "" {
			lines = append(lines, sep)
		}
		lines = append(lines, s.helpLines()...)
	}
	lines = append(lines, s.hintLine())
```

And add the help case to `hintLine`'s switch, **before** the `focusRows` case:

```go
	case s.helpOpen:
		ladder = []string{"↑/↓ scroll · ? or esc back", "esc back"}
```

- [ ] **Step 6: Run the tests**

```bash
PATH="/home/zvi/.local/share/mise/installs/go/1.26.4/bin:$PATH" \
  go test ./ui/overlay/ -run 'TestEveryDetailAndGloss|TestExpandedHelp|TestQuestionMark|TestBoxHeight' -v 2>&1 | tail -40
```
Expected: PASS. If `TestExpandedHelpScrolls`'s precondition fails, `max_sessions`' help now
fits 80×24 — pick a longer row (compare `len(o.expandedHelpContent(i))` across rows) rather
than dropping the assertion.

- [ ] **Step 7: Verify the visibility guards actually guard**

1. **Stop rendering detail.** Delete the `if row.detail != ""` block. Expected:
   `TestEveryDetailAndGlossReachExpandedHelp` FAILS naming the first row with detail — and
   note that `TestDetailRetainsTheMovedProse` still **passes**, which is the entire reason
   this test exists. Revert.
2. **Stop rendering gloss.** Drop the `" — " + g` from the options loop. Expected: the same
   test FAILS on a gloss. Revert.
3. **Let a stray key dismiss.** Add a `default: s.helpOpen = false` to `handleHelpKey`.
   Expected: `TestQuestionMarkOpensAndClosesExpandedHelp` FAILS on the `x` assertion.
   Revert.
4. **Break the height invariant.** Change `expandedHelpHeight` to `return s.paneHeight()`.
   Expected: `TestExpandedHelpDoesNotChangeTheBoxHeight` FAILS. Revert.
5. Re-run and confirm green.

- [ ] **Step 8: Lint and commit**

```bash
PATH="/home/zvi/.local/share/mise/installs/go/1.26.4/bin:$PATH" /home/zvi/go/bin/golangci-lint run ./ui/...
git add ui/overlay/settings.go ui/overlay/settings_nav.go ui/overlay/settings_render.go ui/overlay/settings_render_test.go
git commit -m "feat(settings): expanded help behind ? renders detail and enum glosses"
```

---

## Task 9: The `group_mode` live gate, the remembered rail, and the spec correction

**Files:**
- Modify: `ui/list.go` (add `AccountClusteringVisible`, beside `AccountGrouped` at `:1145`
  and `AccountReorderEnabled` at `:1153`)
- Modify: `ui/list_render.go:411` (call it, so the gate has one definition)
- Modify: `ui/list_group_account_test.go`
- Modify: `ui/overlay/settings.go` (`SetAccountClusteringVisible`, `RailIndex`,
  `SetRailIndex`)
- Modify: `ui/overlay/settings_render.go` (`inertReason`'s `group_mode` branch)
- Modify: `ui/overlay/settings_render_test.go`
- Modify: `app/app.go` (the `settingsRail` field), `app/app_update.go:805-811`,
  `app/app_keys.go:337-354`, `app/app_layout.go:271-277`
- Modify: `app/settings_test.go`
- Modify: `docs/superpowers/specs/2026-07-25-configuration-panel-design.md` (§5's table and
  its callout)

**Interfaces:**
- Produces: `func (l *List) AccountClusteringVisible() bool`;
  `func (s *SettingsOverlay) SetAccountClusteringVisible(visible bool)`;
  `func (s *SettingsOverlay) RailIndex() int`;
  `func (s *SettingsOverlay) SetRailIndex(i int)`; `home.settingsRail int`.

### Why this row is different from the other six

`settingRow.activeWhen` has the signature `func(*config.Config) bool`. It can see the
config and nothing else. But the list's real gate is
`ui/list_render.go:411` —

```go
accountGroupingVisible := l.accountGrouped() && l.distinctAccountCount() > 1
```

— where `distinctAccountCount` (`ui/list.go:95`) counts distinct
`Instance.AccountClusterKey()` values over the **live session list**, and that key
(`session/account.go:60`) is the session's *rotation pool* when it has one, else its
account name, else `""`.

So spec §5's proposed `len(cfg.ClaudeAccounts) >= 2` is wrong in **both** directions:

- Several configured accounts sharing one rotation pool collapse to a single cluster key,
  so clustering is a visual no-op while `len(ClaudeAccounts) >= 2` would call it active.
- Sessions with no account attribution key on `""` and still form a second cluster, so
  clustering can be visible with fewer than two accounts configured.

`TestGroupModeHasNoConfigOnlyInertPredicate` (`settings_schema_test.go:457`) pins the
deliberate absence of a predicate and hands the gate to PR B. This task takes it.

**And note a third count exists:** `AccountReorderEnabled` uses `len(l.accountSequence())`
— *clusters*, not distinct accounts — because a repo whose sessions span accounts renders
as one cluster. It is **not** a substitute for the visibility gate. Do not reuse it.

- [ ] **Step 1: Write the `ui` gate's failing test**

Append to `ui/list_group_account_test.go`:

```go
// TestAccountClusteringVisible pins the exported gate as the single definition of "does the
// list currently render account clusters?". The settings panel's group_mode reason chip
// reads it, so a second copy of the rule in another package is exactly the drift
// ui.accountKey's doc comment warns about.
//
// The pooled case is the one a config-only predicate gets wrong: two configured accounts
// pinned to one rotation pool share a cluster key, so there is nothing to cluster even
// though two accounts exist.
func TestAccountClusteringVisible(t *testing.T) {
	t.Run("off when not account-grouped", func(t *testing.T) {
		l := acctList(t, "api|work", "infra|personal")
		require.False(t, l.AccountClusteringVisible(), "repo grouping renders no clusters")
	})

	t.Run("off with a single account", func(t *testing.T) {
		l := acctList(t, "api|work", "infra|work")
		l.SetGroupMode("account")
		require.False(t, l.AccountClusteringVisible(), "one account is one cluster: a visual no-op")
	})

	t.Run("on with two accounts", func(t *testing.T) {
		l := acctList(t, "api|work", "infra|personal")
		l.SetGroupMode("account")
		require.True(t, l.AccountClusteringVisible())
	})

	t.Run("off when two accounts share one pool", func(t *testing.T) {
		s := spinner.New()
		l := NewList(&s)
		for i, acct := range []string{"work", "personal"} {
			inst, err := session.NewInstance(session.InstanceOptions{
				Title: string(rune('a' + i)), Path: "/tmp/repo" + acct, Program: "echo",
			})
			require.NoError(t, err)
			// SetClaudeAccount's second argument is the CLAUDE_CONFIG_DIR, not the pool
			// (session/account.go:14). The pool has its own setter, and it is the field
			// AccountClusterKey actually prefers (session/account.go:60) — so setting it
			// through the wrong argument would leave two distinct keys and make this
			// subtest assert the opposite of what it claims.
			inst.SetClaudeAccount(acct, "", false)
			inst.SetClaudeAccountPool("shared")
			l.AddInstance(inst)
		}
		l.SetSize(80, 40)
		l.SetGroupMode("account")
		require.False(t, l.AccountClusteringVisible(),
			"two accounts in one pool share a cluster key — len(ClaudeAccounts) >= 2 would be wrong here")
	})

	t.Run("on with an unattributed session", func(t *testing.T) {
		l := acctList(t, "api|work", "infra|")
		l.SetGroupMode("account")
		require.True(t, l.AccountClusteringVisible(),
			`a session with no account keys on "" and still forms a second cluster`)
	})
}
```

- [ ] **Step 2: Add the exported gate and route the renderer through it**

In `ui/list.go`, beside `AccountGrouped` and `AccountReorderEnabled`:

```go
// AccountClusteringVisible reports whether the list currently renders account clusters:
// account grouping must be on AND more than one distinct cluster key must be present across
// the live items. With one key, "account" mode renders exactly like repo mode.
//
// Exposed so the settings panel can dim its Account clustering row when the setting is on
// but doing nothing. The rule lives here, and list_render.go's own gate calls this method,
// so the two cannot disagree — the drift accountKey's comment warns about.
//
// Note this is NOT AccountReorderEnabled's count. That one counts *clusters* (repo-block
// anchors), because a repo whose sessions span accounts still renders as one cluster; this
// one counts distinct cluster keys, which is what the divider and tinting gate on.
func (l *List) AccountClusteringVisible() bool {
	return l.accountGrouped() && l.distinctAccountCount() > 1
}
```

And at `ui/list_render.go:411`:

```go
	accountGroupingVisible := l.AccountClusteringVisible()
```

- [ ] **Step 3: Run the `ui` tests**

```bash
PATH="/home/zvi/.local/share/mise/installs/go/1.26.4/bin:$PATH" go test ./ui/ -run 'TestAccountClustering|TestAccountGroup' -v 2>&1 | tail -20
```
Expected: PASS (5 subtests), and the existing account-grouping render tests still pass —
they exercise the same expression through the renderer.

- [ ] **Step 4: Write the panel's failing tests**

Append to `ui/overlay/settings_render_test.go`:

```go
// TestGroupModeChipIsSilentUntilTheGateIsInjected pins the tri-state. A panel that cannot
// see the session list must not guess: the honest gate is session-derived, and a default of
// "inert" would put "nothing to cluster" on every row on every open, while a default of
// "active" would be a silent wrong answer. nil means no chip.
func TestGroupModeChipIsSilentUntilTheGateIsInjected(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.GroupMode = config.GroupModeAccount
	o := NewSettingsOverlay(cfg)
	o.SetSize(100, 32)
	i := indexOfRowKey(t, o, "group_mode")
	assert.Empty(t, o.inertReason(i), "with no injected gate the panel says nothing")
}

// TestGroupModeChipTracksTheInjectedGate pins all four combinations of (setting, gate). The
// chip appears only when the setting is ON and the list says clustering is invisible —
// "off" is not inert, it is simply off, and a chip there would be noise on a row doing
// exactly what it says.
func TestGroupModeChipTracksTheInjectedGate(t *testing.T) {
	cases := []struct {
		name    string
		mode    string
		visible bool
		want    string
	}{
		{"on and clustering", config.GroupModeAccount, true, ""},
		{"on but nothing to cluster", config.GroupModeAccount, false, "nothing to cluster"},
		{"off and clustering", config.GroupModeRepo, true, ""},
		{"off and not clustering", config.GroupModeRepo, false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.DefaultConfig()
			cfg.GroupMode = tc.mode
			o := NewSettingsOverlay(cfg)
			o.SetSize(100, 32)
			o.SetAccountClusteringVisible(tc.visible)
			assert.Equal(t, tc.want, o.inertReason(indexOfRowKey(t, o, "group_mode")))
		})
	}
}

// TestRailIndexRoundTrips pins the accessor pair home uses to remember the category across
// opens within a run (spec §7). Persisting it to state.json is a deliberate non-goal — a
// fresh launch starting at the top is fine — so an in-memory int on home is the whole
// mechanism.
func TestRailIndexRoundTrips(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	for i := range railEntries() {
		o.SetRailIndex(i)
		assert.Equal(t, i, o.RailIndex())
		start, end := o.rowRange(o.selectedEntry())
		if end > start {
			assert.GreaterOrEqualf(t, o.cursor, start, "SetRailIndex(%d) left the cursor outside the pane", i)
			assert.Less(t, o.cursor, end)
		}
	}
	// Out-of-range values are clamped rather than panicking: home's remembered index could
	// outlive a rail that shrank.
	o.SetRailIndex(999)
	assert.Equal(t, len(railEntries())-1, o.RailIndex())
	o.SetRailIndex(-5)
	assert.Equal(t, 0, o.RailIndex())
}
```

- [ ] **Step 5: Add the panel's setters and the `group_mode` branch**

In `ui/overlay/settings.go`:

```go
// SetAccountClusteringVisible records whether ui.List currently renders account clusters, so
// the Account clustering row can be dimmed when the setting is on but doing nothing.
//
// It exists because that gate is session-derived and settingRow.activeWhen can only see
// *config.Config. Until home calls this, the panel shows no chip for the row at all: spec
// §5's config-only predicate was wrong in both directions (accounts sharing a rotation pool
// collapse to one cluster; unattributed sessions form one anyway), so a panel that cannot
// see the session list must not guess. See TestGroupModeHasNoConfigOnlyInertPredicate.
func (s *SettingsOverlay) SetAccountClusteringVisible(visible bool) {
	s.clusteringVisible = &visible
}

// RailIndex reports which rail entry is current, so home can restore it the next time the
// panel opens (spec §7). Persisting it to state.json is a deliberate non-goal.
func (s *SettingsOverlay) RailIndex() int { return s.railCursor }

// SetRailIndex moves the rail to the given entry, clamping out-of-range values — a
// remembered index can outlive a rail that shrank — and pulling the row cursor with it.
func (s *SettingsOverlay) SetRailIndex(i int) {
	s.railCursor = clamp(i, 0, len(railEntries())-1)
	s.syncCursorToRail()
}
```

In `settings_render.go`, add the branch at the top of `inertReason`:

```go
	if row.key == "group_mode" {
		// The one row whose gate is not a config predicate. It is inert when clustering is
		// switched ON but the list has nothing to cluster — "off" is not inert, it is off,
		// and a chip there would be noise. clusteringVisible is nil until home injects the
		// list's own answer.
		if groupModeOnOff(s.cfg) == "on" && s.clusteringVisible != nil && !*s.clusteringVisible {
			return inertReasons["group_mode"]
		}
		return ""
	}
```

The string comes from `inertReasons` rather than a literal here, so every reason chip lives in
one place and `TestEveryInertPredicateHasAReason` covers this row too — via
`inertReasonsWithoutPredicate`, which documents *why* it has no `activeWhen` instead of
letting it slip past the guard unnoticed.

- [ ] **Step 6: Wire it in `app`**

Add to `app/app.go`'s `home` struct, beside `settingsOverlay`:

```go
	// settingsRail remembers which settings category was current, so reopening the panel
	// returns to it within a run. The overlay is reconstructed on every ',', so the memory
	// has to live out here. Deliberately not persisted to state.json (spec §7).
	//
	// It is a *int because a plain int's zero value is 0 — which is the All settings entry,
	// the one rail entry spec §4 explicitly excludes as the landing. nil means "the panel has
	// not been opened yet this run", so the first ',' gets railDefaultIndex().
	settingsRail *int
```

In `app/app_update.go`, replace the `keys.KeySettings` case body:

```go
	case keys.KeySettings:
		m.state = stateSettings
		m.settingsOverlay = overlay.NewSettingsOverlay(m.appConfig)
		if m.settingsRail != nil {
			m.settingsOverlay.SetRailIndex(*m.settingsRail)
		}
		m.refreshSettingsClusteringGate()
		m.recomputeLayout() // the hint bar hides behind the modal; panes reclaim its row
		return m, tea.WindowSize()
```

In `app/app_keys.go`'s `handleSettingsState`, remember the rail before dropping the overlay:

```go
	if closed {
		rail := m.settingsOverlay.RailIndex()
		m.settingsRail = &rail
		m.settingsOverlay = nil
		m.state = stateDefault
		m.recomputeLayout() // menuVisible flipped; the hint bar may reclaim its row
		cmds = append(cmds, tea.WindowSize())
	}
```

In `app/app_layout.go`, add the helper and call it from the `group_mode` case:

```go
// refreshSettingsClusteringGate hands the settings panel the list's own answer to "are
// account clusters currently visible?", which the panel cannot derive: the gate counts
// distinct cluster keys over live sessions, and settingRow predicates only see the config.
func (m *home) refreshSettingsClusteringGate() {
	if m.settingsOverlay == nil || m.list == nil {
		return
	}
	m.settingsOverlay.SetAccountClusteringVisible(m.list.AccountClusteringVisible())
}
```

```go
	case "group_mode":
		// Re-group the list under the new mode immediately; the list takes the normalized
		// mode string so ui needs no config import. Selection is preserved by identity.
		if m.list != nil {
			m.list.SetGroupMode(m.appConfig.GetGroupMode())
		}
		// Re-ask the list: turning clustering on with a single account must flip the row's
		// chip to "nothing to cluster" in the same frame, not on the next open.
		m.refreshSettingsClusteringGate()
```

- [ ] **Step 7: Write the `app` wiring test**

Append to `app/settings_test.go`, reusing the existing `accountGroupedHome(t)` helper at
`app/settings_test.go:170` (which already builds a home with two accounts and account
grouping on):

```go
// TestSettingsPanel_GroupModeChipFollowsTheLiveList pins the wiring that carries the
// session-derived gate into the panel. It is an app-level test because neither package can
// answer it alone: ui.List owns the count and ui/overlay owns the chip.
func TestSettingsPanel_GroupModeChipFollowsTheLiveList(t *testing.T) {
	resetSettingsTestState(t)
	h := accountGroupedHome(t) // two distinct accounts, grouping on
	// accountGroupedHome sets the LIST's mode, not the config's (app/settings_test.go:190).
	// The chip needs both: the panel reads the setting from config and the gate from the list.
	h.appConfig.GroupMode = config.GroupModeAccount
	require.True(t, h.list.AccountClusteringVisible(), "the fixture must actually cluster")

	openPanel := func() {
		_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(",")})
		require.Equal(t, stateSettings, h.state)
		require.True(t, h.settingsOverlay.SelectRow("group_mode"))
	}
	// Both Escs go through handleKeyPress, not straight to the overlay: home only learns the
	// panel closed via handleSettingsState's `closed` return (app/app_keys.go:347). Calling
	// the overlay directly leaves h.state == stateSettings, so the next ',' is routed INTO
	// the still-open panel (app/app_update.go:712) and the overlay is never rebuilt.
	closePanel := func() {
		_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyEsc}) // rows pane -> rail
		_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyEsc}) // rail -> closed
		require.Nil(t, h.settingsOverlay)
	}

	openPanel()
	assert.NotContains(t, xansi.Strip(h.settingsOverlay.Render()), "nothing to cluster",
		"two clusters are visible, so the row is not inert")
	closePanel()

	// Collapse to one cluster: the chip must appear on the next open.
	for _, inst := range h.list.GetInstances() {
		inst.SetClaudeAccount("work", "", false)
	}
	require.False(t, h.list.AccountClusteringVisible())

	openPanel()
	assert.Contains(t, xansi.Strip(h.settingsOverlay.Render()), "nothing to cluster",
		"one cluster means the setting is on but doing nothing")
}

// TestSettingsPanel_GroupModeChipAppearsInTheSameFrame pins the refresh in
// applySettingChange, which the reopen-based test above cannot see. Turning clustering on
// with a single account must flip the chip immediately, not on the next open.
func TestSettingsPanel_GroupModeChipAppearsInTheSameFrame(t *testing.T) {
	resetSettingsTestState(t)
	h := accountGroupedHome(t)
	for _, inst := range h.list.GetInstances() {
		inst.SetClaudeAccount("work", "", false) // collapse to one cluster
	}
	h.appConfig.GroupMode = config.GroupModeRepo
	h.list.SetGroupMode(config.GroupModeRepo)

	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(",")})
	require.True(t, h.settingsOverlay.SelectRow("group_mode"))
	require.NotContains(t, xansi.Strip(h.settingsOverlay.Render()), "nothing to cluster",
		"off is not inert")

	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRight}) // cycle off -> on
	require.Equal(t, config.GroupModeAccount, h.appConfig.GetGroupMode())
	assert.Contains(t, xansi.Strip(h.settingsOverlay.Render()), "nothing to cluster",
		"the chip must appear without reopening the panel")
}

// TestSettingsPanel_RemembersTheCategoryAcrossOpens pins spec §7's in-memory rail memory —
// and that a FIRST open still lands on the default category rather than on All settings,
// which a zero-valued int would have produced.
func TestSettingsPanel_RemembersTheCategoryAcrossOpens(t *testing.T) {
	resetSettingsTestState(t)
	h := newSettingsTestHome()

	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(",")})
	assert.NotEqual(t, 0, h.settingsOverlay.RailIndex(),
		"a fresh run must not land on All settings (spec §4)")
	require.True(t, h.settingsOverlay.SelectRow("agent_oom_margin")) // Advanced
	want := h.settingsOverlay.RailIndex()

	// Two Escs: SelectRow focused the rows pane, and Esc is layered.
	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyEsc})
	require.Equal(t, stateSettings, h.state, "the first esc backs out of the rows pane")
	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyEsc})
	require.Nil(t, h.settingsOverlay)

	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(",")})
	assert.Equal(t, want, h.settingsOverlay.RailIndex(), "reopening returns to the last category")
}
```

**`app` has no `stripANSI` helper** — it strips inline (`app/help_legend_test.go:21`,
`app/kill_warning_test.go:19`). Use `xansi.Strip` and add
`xansi "github.com/charmbracelet/x/ansi"` to `app/settings_test.go`'s imports, which
currently has neither ansi package.

- [ ] **Step 8: Run the tests**

```bash
PATH="/home/zvi/.local/share/mise/installs/go/1.26.4/bin:$PATH" \
  go test ./ui/ ./ui/overlay/ ./app/ -run 'TestAccountClustering|TestGroupMode|TestRailIndex|TestSettingsPanel' -v 2>&1 | tail -40
```
Expected: PASS.

- [ ] **Step 9: Verify the gate's guards actually guard**

1. **Ship the spec's wrong predicate.** Replace `inertReason`'s branch with
   `if len(s.cfg.ClaudeAccounts) < 2 { return "nothing to cluster" }`. Expected:
   `TestGroupModeChipTracksTheInjectedGate` FAILS on **two** cases — `on and clustering`
   (a `DefaultConfig` has no accounts, so it would claim inert while the list clusters) and
   `off and not clustering` (a chip on an off row). Both directions, exactly as the spec
   correction predicts. Revert.
2. **Default the tri-state to false.** Change `clusteringVisible *bool` to a plain
   `clusteringVisible bool`. Expected:
   `TestGroupModeChipIsSilentUntilTheGateIsInjected` FAILS — an un-injected panel would
   claim inert. Revert.
3. **Duplicate the gate.** Revert `list_render.go:411` to the inline expression and change
   `AccountClusteringVisible` to `l.accountGrouped() && l.distinctAccountCount() > 2`.
   Expected: `TestAccountClusteringVisible`'s `on with two accounts` FAILS while every
   *render* test still passes — which is exactly the drift a single definition prevents.
   Revert both halves.
4. **Drop the live refresh.** Remove `m.refreshSettingsClusteringGate()` from the
   `group_mode` case. Expected: `TestSettingsPanel_GroupModeChipAppearsInTheSameFrame` FAILS
   while `TestSettingsPanel_GroupModeChipFollowsTheLiveList` still passes — the latter
   reopens the panel, so it cannot see a missing live refresh. That is why both exist; a
   refresh nothing tests is a refresh that will be deleted by the next reader.
5. **Default the rail to zero.** Change `settingsRail *int` to `settingsRail int` and call
   `SetRailIndex(m.settingsRail)` unconditionally. Expected:
   `TestSettingsPanel_RemembersTheCategoryAcrossOpens` FAILS on its first assertion — every
   fresh run would open on All settings, which spec §4 excludes as the landing. Note that
   `TestRailDefaultIndexIsTheFirstCategory` and `TestPanelOpensOnTheRail` both still pass:
   neither goes through `home`, which is exactly how this defect survived the first draft.
   Revert.
6. Re-run and confirm green.

- [ ] **Step 10: Correct spec §5**

In `docs/superpowers/specs/2026-07-25-configuration-panel-design.md`, replace the
`group_mode` row of §5's `activeWhen` table:

```markdown
| `group_mode` | see below — **not an `activeWhen` predicate** | `nothing to cluster` |
```

and replace the implementer callout beneath the table with the resolution:

```markdown
> **Resolved in PR B: `group_mode` has no `activeWhen` predicate.** The predicate proposed
> here, `len(cfg.ClaudeAccounts) >= 2`, was derived from the row's own prose and is wrong in
> **both** directions. `ui.List`'s real gate is
> `AccountClusteringVisible() == accountGrouped() && distinctAccountCount() > 1`
> (`ui/list.go`), where `distinctAccountCount` counts distinct
> `session.Instance.AccountClusterKey()` values over the **live session list** — and that key
> is a session's *rotation pool* when it has one, else its account, else `""`. So several
> configured accounts sharing one pool collapse to a single cluster (clustering is a visual
> no-op while the config count says otherwise), and sessions with no account attribution key
> on `""` and form a second cluster anyway (clustering is visible with fewer than two
> accounts configured).
>
> A `settingRow` predicate only sees `*config.Config`, so the honest gate is not expressible
> in the schema. `group_mode.activeWhen` stays `nil`
> (`TestGroupModeHasNoConfigOnlyInertPredicate`), `ui.List.AccountClusteringVisible()` is the
> single definition of the gate, and `home` injects its answer via
> `SettingsOverlay.SetAccountClusteringVisible`. The chip shows only when the setting is
> **on** and the list reports nothing to cluster — "off" is not inert.
>
> Note a *third* count exists and is not a substitute: `AccountReorderEnabled` counts
> *clusters* (repo-block anchors), because a repo whose sessions span accounts renders as one.
```

- [ ] **Step 11: Lint and commit**

```bash
PATH="/home/zvi/.local/share/mise/installs/go/1.26.4/bin:$PATH" /home/zvi/go/bin/golangci-lint run ./ui/... ./app/...
git add ui/list.go ui/list_render.go ui/list_group_account_test.go ui/overlay/settings.go \
  ui/overlay/settings_render.go ui/overlay/settings_render_test.go app/app.go app/app_update.go \
  app/app_keys.go app/app_layout.go app/settings_test.go \
  docs/superpowers/specs/2026-07-25-configuration-panel-design.md
git commit -m "feat(settings): dim account clustering from the list's own gate"
```

---

## Task 10: Adapt the existing suite

**Files:**
- Modify: `ui/overlay/settings_test.go` (11 sites)
- Modify: `app/settings_test.go:48` (one site — the layered Esc)

**Interfaces:**
- Consumes: everything above.
- Produces: a green `./ui/... ./app/... ./config/...`.

Every item below was derived by reading the test, not guessed. **Do not delete a test to go
green** — each one still guards something; the geometry it guards has moved.

Work through them in order and run the package after each, so a failure is attributable.

- [ ] **Step 1: `widestFooterRow`'s hardcoded 60 (`settings_test.go:49`)**

`require.Greater(t, widest, 60, ...)` names today's inner width. The box is now
`min(96, width−2)`, so the inner width at 80 columns is **74**. Derive it rather than
substituting a second literal:

```go
	o := NewSettingsOverlay(config.DefaultConfig())
	o.SetSize(80, 24)
	// A one-line footer would make the callers' capping assertions vacuous. The bound is the
	// panel's own inner width at the 80-column floor (74 since PR B widened the box from a
	// fixed 64), read from the overlay so a further width change cannot leave a stale
	// literal here.
	require.Greater(t, widest, o.innerWidth(),
		"the widest footer (%q, %d cells) must exceed the inner width %d to wrap",
		key, widest, o.innerWidth())
```

`fast_forward_local_base`'s footer is 116 cells, so it still clears 74 comfortably.

- [ ] **Step 2: `TestFooterTextFitsTwoLines` (`:62`) — guard against vacuity**

The bound loosened from inner 60 to inner 74, so the test could now pass with no row
anywhere near two lines. Add the missing precondition:

```go
	wrapped := 0
	for _, r := range newSettingRows(config.DefaultConfig()) {
		lines := strings.Split(ansi.Wrap(r.footerText(), inner, ""), "\n")
		if len(lines) == 2 {
			wrapped++
		}
		assert.LessOrEqualf(t, len(lines), 2, ...)
	}
	// PR B widened the box from inner 60 to inner 74, so without this the cap could pass
	// with every footer on one line — proving nothing about the two-line budget the help
	// pane is sized against.
	require.Positive(t, wrapped, "at least one footer must actually need two lines at inner %d", inner)
```

- [ ] **Step 3: `TestEveryCautionReachesTheFooter` (`:83`)**

It calls `o.renderFooter(o.innerWidth())`, which no longer exists. Retarget at the help pane
— the mechanism is unchanged (a caution the renderer never reads is invisible) and it is
still the guard a new caution must clear:

```go
		o := NewSettingsOverlay(config.DefaultConfig())
		o.SetSize(100, 32) // wide and tall, so the help pane is at its full three lines
		settingsAt(t, o, r.key)
		help := stripANSI(strings.Join(o.helpLines(), " "))
		assert.Containsf(t, help, r.caution,
			"row %q declares a caution the help pane never renders", r.key)
```

- [ ] **Step 4: `TestSettingsOverlay_RenderSmoke` (`:533`)**

`assert.Contains(out, "Theme")` fails: `theme` is an *Appearance* row and the panel lands on
`Sessions`. The ten category labels now come free from the rail, which is strictly better
coverage than before. Rewrite:

```go
func TestSettingsOverlay_RenderSmoke(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	o.SetSize(100, 32)
	out := stripANSI(o.Render())

	assert.Contains(t, out, "Settings")
	assert.Contains(t, out, "Session limit", "the landing category's rows are visible")
	assert.Contains(t, out, "esc close", "the rail's hint")

	// Every rail entry is visible at once — the two-pane rail is the orientation the old
	// single column lacked (D2), so this no longer needs an artificially tall terminal.
	for _, e := range railEntries() {
		assert.Containsf(t, out, e.label, "rail entry %q is not rendered", e.label)
	}

	// A row from another category becomes visible once selected.
	settingsAt(t, o, "theme")
	assert.Contains(t, stripANSI(o.Render()), "Theme")
	assert.Contains(t, stripANSI(o.Render()), "esc back", "the rows pane's hint differs (spec §15)")
}
```

- [ ] **Step 5: `TestSettingsOverlay_LongSummaryShownInFull` (`:579`)**

`renderFooter` is gone and the mechanism has changed: the help pane is a fixed three lines,
so "shown in full" now means "wraps within the pane" rather than "the footer grows". Rewrite
against the new mechanism, keeping the anti-vacuity check that the tail is on a *wrapped*
line:

```go
// TestSettingsOverlay_LongSummaryWrapsWithinTheHelpPane pins that a summary too wide for one
// line is wrapped and shown whole inside the fixed-height help pane, rather than clipped.
// The assertion is on the tail *and* on its absence from the first line, so it cannot pass
// on a summary that simply fit.
//
// (Before PR B the footer grew to fit and this test asserted on renderFooter's line count.
// The pane is now fixed at three lines — that is the D5 fix — so what is pinned here is that
// the text still arrives, not that the pane resized.)
func TestSettingsOverlay_LongSummaryWrapsWithinTheHelpPane(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	o.SetSize(56, 40) // narrow: single-pane, so the summary must wrap
	settingsAt(t, o, "max_sessions")

	help := o.helpLines()
	require.Len(t, help, o.helpHeight(), "the pane is exactly helpHeight() lines")
	assert.NotContains(t, stripANSI(help[0]), "host",
		"the tail must be on a wrapped line, or this test proves nothing")
	assert.Contains(t, stripANSI(strings.Join(help, "\n")), "host",
		"the summary's tail must survive wrapping")
	assert.Contains(t, stripANSI(o.Render()), "esc back", "the key hint stays visible")
}
```

Verify the asserted tail word lands on a wrapped line at that width by printing the pane
once while iterating; adjust the width if `host` reaches line 0.

- [ ] **Step 6: `TestSettingsOverlay_LongDescriptionCapsWithEllipsis` (`:633`)**

`maxDescLines` is gone; the cap is now `helpHeight()`. At inner 74 nothing overflows three
lines, so the test needs a narrow terminal to reach the branch at all:

```go
// TestSettingsOverlay_HelpPaneCapsWithEllipsis pins that help too long for the fixed pane is
// capped with a trailing ellipsis and the pane stays exactly its budgeted height.
//
// The width matters: at the 80-column inner width (74) the widest footer wraps to two lines
// and never reaches the cap, so this test would be vacuous there. 40 columns gives inner 34,
// where the same text needs four lines against a three-line pane.
func TestSettingsOverlay_HelpPaneCapsWithEllipsis(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	o.SetSize(40, 24)
	settingsAt(t, o, widestFooterRow(t))

	help := o.helpLines()
	require.Len(t, help, o.helpHeight())
	// Precondition: the text must actually exceed the pane, or the ellipsis below proves
	// nothing.
	require.Greater(t,
		len(strings.Split(ansi.Wrap(o.selectedRow().footerText(), o.innerWidth(), ""), "\n")),
		o.helpHeight(),
		"the footer must need more than %d lines at inner width %d", o.helpHeight(), o.innerWidth())

	assert.Contains(t, stripANSI(strings.Join(help, "\n")), "…", "the capped help ends with an ellipsis")
	assert.Contains(t, stripANSI(o.Render()), "esc back", "the hint must remain visible")
}
```

- [ ] **Step 7: `TestSettingsOverlay_FooterCutLineStaysWithinInner` (`:661`)**

This is the most tightly coupled test in the suite: it pins that appending `…` to an
already-full kept line does not push it past the inner width, and it does so at 80×12 with
`update_read_base_on_create` because that row's first wrapped line was **exactly 60 cells**
at the old inner width. The geometry moved, so the row and the size must be re-derived — but
**keep the discipline the comment records**: the precondition must measure the same `key`
variable the cursor sits on, so the test fails both ways it can rot (a copy edit that moves
the wrap point, and a swap to a row that never reaches the branch).

```go
func TestSettingsOverlay_HelpCutLineStaysWithinInner(t *testing.T) {
	// Re-derive rather than trusting a remembered key: find a row whose LAST kept help line
	// is full-width at this size, which is the only case the hard-truncate branch guards.
	// Deriving it is what keeps this test honest across a copy edit — the previous version
	// named update_base_on_create because its first line was exactly 60 cells at the old
	// inner width of 60, and PR B moved the wrap point.
	const w, h = 40, 24
	o := NewSettingsOverlay(config.DefaultConfig())
	o.SetSize(w, h)
	inner := o.innerWidth()

	key := ""
	for _, r := range newSettingRows(config.DefaultConfig()) {
		lines := strings.Split(ansi.Wrap(r.footerText(), inner, ""), "\n")
		if len(lines) > o.helpHeight() && ansi.StringWidth(lines[o.helpHeight()-1]) > inner-1 {
			key = r.key
			break
		}
	}
	require.NotEmpty(t, key,
		"no row's kept help line is full-width at inner %d, so the hard-truncate branch is "+
			"unreachable — widen the search or record that the branch is dead", inner)
	settingsAt(t, o, key)

	for i, line := range o.helpLines() {
		assert.LessOrEqualf(t, ansi.StringWidth(stripANSI(line)), inner,
			"help line %d must stay within inner width %d after capping (row %q)", i, inner, key)
	}
	assert.Contains(t, stripANSI(strings.Join(o.helpLines(), "\n")), "…",
		"the cap must have fired, so the width check above exercises the truncate path")
	// And the box must not have grown: a soft-wrapped help line is exactly how the pinned
	// hint used to get clipped.
	assert.LessOrEqual(t, lipgloss.Height(o.Render()), h)
}
```

If the search finds no row, **do not weaken the assertion** — report it. It means the
truncate branch is unreachable under the current copy, which is information the reviewer
needs.

- [ ] **Step 8: `TestSettingsOverlay_FooterNeverClipsHint` (`:607`) — keep it and add a width sweep**

This sweep is the lockstep net between the pane-budget and help-height formulas, and it
matters *more* now that there are two of them plus a separator. Keep it, adapt the hint
literal, and add the width dimension that two panes introduce:

```go
func TestSettingsOverlay_BoxNeverOutgrowsTheTerminal(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	settingsAt(t, o, widestFooterRow(t))

	// Height sweep: paneHeight and helpHeight are separate formulas that must stay in
	// numeric lockstep, so a dense sweep catches drift a few samples would miss. 10 is the
	// floor below which the layout degrades (see the geometry table).
	for h := 10; h <= 40; h++ {
		o.SetSize(80, h)
		out := o.Render()
		assert.LessOrEqualf(t, lipgloss.Height(out), h, "box height must fit terminal height %d", h)
		assert.Containsf(t, stripANSI(out), "esc back", "the hint must survive at height %d", h)
	}

	// Width sweep across the two-pane threshold, which is new in PR B: the rail, the
	// divider and the rows pane must sum to the inner width on both sides of it.
	for w := 30; w <= 120; w++ {
		o.SetSize(w, 24)
		for _, line := range strings.Split(o.Render(), "\n") {
			assert.LessOrEqualf(t, lipgloss.Width(line), max(w, 22),
				"a rendered line exceeds terminal width %d", w)
		}
		assert.Containsf(t, stripANSI(o.Render()), "esc", "a hint must survive at width %d", w)
	}
}
```

The `max(w, 22)` is the box's own floor (`boxWidth` clamps to 20 plus the border), which a
terminal narrower than that cannot help. Note this width assertion is the *weak* tautological
kind — it is here to catch soft-wrap-induced height growth, and
`TestNoPaneLineOverflowsItsWidth` is the honest one.

- [ ] **Step 9: `maxSessionsRow` and its two callers (`:442`, `:456`, `:478`)**

The helper greps the rendered lines for `"Session limit"`. In two-pane mode that line also
carries the rail's text, which is harmless — but **verify the value is not truncated** before
trusting it:

```go
// maxSessionsRow returns the rendered "Session limit" row line. In two-pane mode the line
// also carries the rail's text to its left; that is harmless, because the assertions are on
// the value and no other column contains the words "auto" or "unlimited".
func maxSessionsRow(t *testing.T, o *SettingsOverlay) string {
	t.Helper()
	o.SetSize(100, 32) // wide, so the value column is never truncated
	for _, line := range strings.Split(stripANSI(o.Render()), "\n") {
		if strings.Contains(line, "Session limit") {
			require.NotContains(t, line, "…", "the value must not be truncated at this width")
			return line
		}
	}
	t.Fatal("no \"Session limit\" row in the render")
	return ""
}
```

- [ ] **Step 10: `TestSettingsOverlay_CarryFilesGetDisplaysDefault` (`:775`)**

`.claude/settings.local.json` is 27 cells. In the Worktrees category at 80 columns the rows
pane is 52 and the label column is 23, so `3+23+2+27 = 55 > 52` — **the value truncates**.
That is correct behavior, and spec §10 says the full value must then appear in the help pane.
Assert the requirement rather than dodging it:

```go
func TestSettingsOverlay_CarryFilesGetDisplaysDefault(t *testing.T) {
	cfg := config.DefaultConfig()
	o := NewSettingsOverlay(cfg)
	o.SetSize(80, 24)
	settingsAt(t, o, "carry_files")

	// At 80 columns the default (27 cells) does not fit the Worktrees rows pane, so it is
	// truncated on the row and shown in full in the help pane — spec §10's requirement, and
	// the reason this test is a free check on it.
	require.True(t, o.valueWasTruncated())
	assert.Contains(t, stripANSI(strings.Join(o.helpLines(), "\n")), ".claude/settings.local.json")

	// Given room, it renders on the row itself.
	o.SetSize(120, 32)
	require.False(t, o.valueWasTruncated())
	assert.Contains(t, stripANSI(o.Render()), ".claude/settings.local.json")
}
```

- [ ] **Step 11: Verify the three tests expected to survive unchanged**

Run them and read the result — do not assume:

```bash
PATH="/home/zvi/.local/share/mise/installs/go/1.26.4/bin:$PATH" \
  go test ./ui/overlay/ -run 'TestSettingsOverlay_RenderFitsWidth|TestSettingsOverlay_ShortTerminalScrollsToCursor|TestSettingsOverlay_ErrShownInRender' -v 2>&1 | tail -20
```

- `_RenderFitsWidth` (60 cols) exercises the single-pane path; it should pass.
- `_ShortTerminalScrollsToCursor` (80×14, `tmux_config_override`) — Advanced has 3 rows and
  `SelectRow` syncs the rail, so the row is trivially visible now. That makes it **vacuous**:
  it no longer tests windowing at all. Retarget it at All settings, where windowing is real:

```go
func TestSettingsOverlay_ShortTerminalScrollsToCursor(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	o.SetSize(80, 14)
	settingsAt(t, o, "tmux_config_override")
	o.railCursor = 0 // All settings: 57 lines into a pane of 3, so windowing must engage
	o.syncCursorToRail()
	require.Greater(t, len(o.rowsPaneContent(o.rowsPaneWidth())), o.paneHeight(),
		"the flat view must overflow the pane, or windowing is untested")
	assert.Contains(t, stripANSI(o.Render()), "Tmux config override",
		"the selected row must be visible on a short terminal")
}
```

- `_ErrShownInRender` asserts `o.Render()` contains `o.lastErr`; the help pane renders it in
  danger style, so it should pass.

- [ ] **Step 12: The one `app` test that breaks — `app/settings_test.go:48`**

`TestSettingsPanel_OpenEditPersistClose` does `SelectRow("auto_attach")` and then **one**
`Esc`, asserting `stateDefault` and a nil overlay (`app/settings_test.go:64-67`). `SelectRow`
now focuses the rows pane and `Esc` is layered, so the first Esc only backs out to the rail
and the assertions fail.

This is the **only place in the suite where the layered Esc is observable end to end**, so
it should *assert the layering* rather than be patched around it:

```go
	// Esc is layered since the two-pane redesign: SelectRow focuses the rows pane, so the
	// first Esc backs out to the rail and only the second closes. The hint line says "esc
	// back" and then "esc close" so the extra level is advertised (spec §7/§15).
	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyEsc})
	require.Equal(t, stateSettings, h.state, "the first esc backs out of the rows pane")
	require.NotNil(t, h.settingsOverlay)

	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyEsc})
	assert.Equal(t, stateDefault, h.state)
	assert.Nil(t, h.settingsOverlay)
```

Then check the other eight for the same shape:

```bash
grep -n "KeyEsc" app/settings_test.go
```

The four `Cycle`/`Toggle` tests (`:70`, `:86`, `:103`, `:124`) never press Esc, and the three
`GroupMode` move tests never open the panel, so this is expected to be the single site — but
**read the grep output rather than trusting that**.

- [ ] **Step 13: Run the whole affected surface**

```bash
PATH="/home/zvi/.local/share/mise/installs/go/1.26.4/bin:$PATH" \
  go test ./ui/... ./app/... ./config/... 2>&1 | tail -25
```
Expected: all PASS.

- [ ] **Step 14: Commit**

```bash
PATH="/home/zvi/.local/share/mise/installs/go/1.26.4/bin:$PATH" /home/zvi/go/bin/golangci-lint run ./ui/... ./app/...
git add ui/overlay/settings_test.go app/settings_test.go
git commit -m "test(settings): adapt the layout assertions to the two-pane renderer"
```

---

## Task 11: Full verification, the manual eyeball, and the PR

**Files:** none — verification only.

- [ ] **Step 1: Run the local gate**

```bash
mise exec -- just ci 2>&1 | tail -30
```
Expected: `build vet fmt-check lint test cover`. **`lint` will likely die with exit 127**
(`golangci-lint` is not on `PATH` under mise) — that is the known toolchain gap, not a
regression. If it instead reports issues in files under a *different* atrium worktree, that
is the global cache leaking; `golangci-lint cache clean` and re-run. Either way Step 3 is
the authoritative lint.

- [ ] **Step 2: Run the race detector**

```bash
mise exec -- just test-race 2>&1 | tail -20
```
Expected: PASS. CI-only otherwise, and this PR touches structs read from the render path.

- [ ] **Step 3: Run the authoritative, scoped lint**

```bash
PATH="/home/zvi/.local/share/mise/installs/go/1.26.4/bin:$PATH" \
  /home/zvi/go/bin/golangci-lint run ./ui/... ./app/... 2>&1 | tail -20
```
Expected: no issues. Watch for `unused` on any helper whose only caller is a test that was
later rewritten, and for `revive`'s `exported` on the four new exported methods
(`AccountClusteringVisible`, `SetAccountClusteringVisible`, `RailIndex`, `SetRailIndex`) —
each needs a doc comment starting with its own name.

- [ ] **Step 4: Eyeball the real panel at 100×32 and 80×24**

**This is the step tests cannot substitute for.** A two-pane layout is the one change in this
series whose correctness is visual: column alignment, the divider meeting the separator, and
whether the dimming reads as "inert" rather than "broken".

```bash
S=/tmp/atrprb; rm -rf $S; mkdir -p $S/home/.atrium $S/tmux $S/repo
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
isolate Atrium's tmux socket, and a new shell per command otherwise reports "no server
running" — or worse, lands the throwaway session on the developer's live server.

Confirm by eye, at 100×32:

1. Thirteen rail entries, `All settings` first, `Sessions` marked current, `Profiles` and
   `Accounts` dimmer with a `→`.
2. The rail, the divider and the rows pane align on every line; the separator's `┴` sits
   directly under the divider.
3. The box does not change height as you walk the rail (`Down` ×12, then `Up` ×12).
4. `Right` focuses the rows; the hint changes from `esc close` to `esc back`.
5. Walk to *Notifications*: the enum row shows `‹off› bell desktop osc`, and *Finished
   turns* / *Notify when focused* are dimmed with `needs Notifications`.
6. Press `Right` on *Notifications* to select `bell`: the two dimmed rows go live in the same
   frame, and *Notify command* stays dimmed with `needs desktop mode`.
7. A changed row shows a `•` beside the selection bar — both cells, not one.
8. `?` on *Account clustering* shows the full detail, scrolls with `↓`, and returns on `?`.
9. `Esc`, `Esc` closes; `,` reopens on the category you left.

Then resize and repeat the essentials:

```bash
export TMUX_TMPDIR=/tmp/atrprb/tmux
tmux -L eyeball resize-window -t 0 -x 80 -y 24 2>/dev/null || \
  tmux -L eyeball kill-server; # fall back to a fresh session at 80x24 if resize is refused
tmux -L eyeball capture-pane -p -t 0
```

At 80×24 confirm the rail still fits **unscrolled** (all thirteen entries) and the help pane
is three lines. Then check the fallback boundary:

```bash
export TMUX_TMPDIR=/tmp/atrprb/tmux
for w in 73 72; do
  tmux -L eyeball resize-window -t 0 -x $w -y 24; sleep 1
  echo "=== width $w ==="; tmux -L eyeball capture-pane -p -t 0 | head -20
done
```

Expected: 73 shows two panes, 72 shows the rail alone. If the boundary is elsewhere, the
threshold arithmetic and the geometry table disagree — reconcile them and update the table
rather than the eyeball.

- [ ] **Step 5: Tear down and confirm the live server is untouched**

```bash
export TMUX_TMPDIR=/tmp/atrprb/tmux
tmux -L eyeball kill-server 2>/dev/null
rm -rf /tmp/atrprb
unset TMUX_TMPDIR
tmux -L atrium list-sessions | head -3   # the developer's live server must be intact
```

- [ ] **Step 6: Open the PR**

```bash
gh auth switch --user ZviBaratz
git push -u origin HEAD
gh pr create --title "feat(settings): two-pane configuration panel with the visibility layer" --body "$(cat <<'EOF'
PR B of the configuration panel redesign
(`docs/superpowers/specs/2026-07-25-configuration-panel-design.md`), following #482.

PR A landed the taxonomy and the copy under the old single-column renderer, which left five
mechanisms tested but **undrawn**: `isModified`, `reset`, `activeWhen`, `gloss`, `detail`
and `applyTiming.badge()`. This draws them, in the two-pane browser the spec designed.

**The defect this fixes.** At 80×24, selecting *Account clustering* used to collapse the
list to **8 visible rows while its help took 8 lines** — the footer's height fed the body's
budget (D5). The help pane is now fixed at three lines and its height reads the terminal and
nothing else, so the visible row count cannot move when the cursor does. That is guard 5,
asserted over *every* row rather than the worst one, because the property is the
independence, not one row's size.

**What is new:** a 13-entry rail (the flat view, ten categories, two handoffs) that fits
80×24 unscrolled by construction; a box widened from a fixed 64 to `min(96, width−2)`; a
single-pane drill-in fallback below a **derived** threshold (rail + divider + minimum rows
pane — renaming a category moves it, and a test proves the literal is rejected); `?`
expanded help; and the four visibility signals — the modified marker in its own column
beside the selection bar, the timing badge, inert dimming with reason chips, and inline enum
alternatives so cycling an enum no longer means writing four values to disk to discover
three of them (D8).

**One spec correction lands here.** §5 proposed `len(cfg.ClaudeAccounts) >= 2` as
`group_mode`'s inert predicate. It is wrong in both directions: accounts sharing a rotation
pool collapse to one cluster key, and unattributed sessions key on `""` and form a cluster
anyway. `ui.List.AccountClusteringVisible()` is now the single definition of that gate,
`home` injects its answer, and the panel shows no chip at all until it does — a panel that
cannot see the session list must not guess. The spec is corrected in this PR.

**Worth reviewing:** the geometry table at the top of the plan (every number is derived, and
the derivations are what the tests assert); guard 5's mutation, which reproduces D5 exactly;
and `TestEveryDetailAndGlossReachExpandedHelp`, which is the render-level twin of PR A's
`TestDetailRetainsTheMovedProse` — the field test passes whether or not anything draws the
string.

Search, `r` reset and the deep-link call sites are PR C; the Profiles editor is PR D. The
hint line deliberately advertises neither.

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```

`gh pr edit` is broken on this repo — use `gh api --method PATCH` if the body needs changing.

- [ ] **Step 7: Read CI before calling it merge-ready**

```bash
gh pr checks --required
```
`gh pr checks` has **no** `--json` flag; a poll loop using one fails every iteration and
times out silently. Expected: all required checks pass, including the macOS job (which
exercises `agent_oom_margin`'s non-Linux `activeWhen` branch, and therefore the
`Linux only` chip's absence).

---

## Self-Review

**Spec coverage.** §3 shape → Tasks 2, 5, 6. §4 taxonomy + the rail-height invariant →
Tasks 2, 5 (guard 4). §5 `activeWhen` reason chips + the `group_mode` correction → Tasks 7,
9. §6 copy → unchanged; `footerText()` is reused verbatim, so no summary or detail is
retouched. §7 nav grammar, including the `←`/`→` collision and the layered `Esc` → Task 3.
§8's `?` clauses → Task 8 (its `/` clauses are PR C's). §10 layout, row line composition,
truncation priority, the derived threshold, the four glyph sites → Tasks 1, 4, 5, 6. §11 file
split → the File Structure table. §12 `OpenAt` → Task 3 proves the behavior against
`SelectRow`; the rename and the call sites are PR C's. §13 guards 4–7, 10, 11 → Tasks 5, 4,
7, 6, 3. §15's risks → the width sweep (Task 10 Step 8), the `group_mode` predicate (Task 9),
and the test churn (Task 10's twelve enumerated sites, one of them in `app/`).

**Deferred by design:** guards 8–9 (`r`, search) are PR C's; guard 12 (profiles) is PR D's.

**Two corrections to the brief this plan makes.**

1. **A new glyph costs five sites, not four.** `app/help_legend_test.go:39-52` reflects over
   every `Glyphs` field and fails on any new one unless it is in the `?` legend or the
   documented `excluded` map. Task 1 Step 7's second mutation makes that site prove itself.
   Relatedly, `ui/theme/registry.go` has only **one** complete `Glyphs` literal —
   `plainGlyphs()`; the other two rungs derive from it — so the "all three rungs" framing
   would have had the implementer looking for literals that do not exist.
2. **`just` does have a `lint` recipe** (`justfile:52`), contrary to PR A's plan. It is a
   bare `golangci-lint run`, so it either 127s under mise or reports cross-worktree cache
   noise — which is why the scoped invocation is named as the authoritative one rather than
   as a workaround.

**Two numbers where this plan diverges from the spec, both recorded rather than silently
adopted.** The single-pane threshold is terminal width **73** (inner 67), not the spec's
"≈72": the spec's estimate assumed a ≈49-cell rows pane at the threshold, and deriving it
from the parts gives 45. And the rows pane at the threshold is therefore 45 cells, not 49.
Both are consequences of taking §10's "computed from the parts, not hardcoded" literally,
which is why `TestThresholdIsDerivedFromTheParts` asserts the *sum* rather than the value.

**One redundancy accepted deliberately.** The apply timing appears twice on a wide pane —
as the row's badge and at the end of the help prose, because `helpLines` reuses PR A's
`footerText()` unchanged. The alternative was to retire `footerText()` and adapt its two
guards. Keeping it costs one visible repeat and buys zero schema churn plus two live guards,
and the repeat is not pure duplication: §10 drops the badge column first on a narrow pane,
so the prose is its fallback. Task 5's `helpLines` comment records the reasoning where a
future reader will look for it.

**One place this plan deliberately does not pre-decide**, because the answer must come from
running the code: Task 10 Step 7's row selection for the help-truncate branch. The old test
named `update_base_on_create` because its first wrapped line was *exactly* 60 cells at the
old inner width — a fact PR B invalidates. The replacement derives the row and asserts a
precondition that fails both ways it can rot, and if no row reaches the branch the
instruction is to **report that**, not to weaken the assertion.

**Vacuity checks added where the geometry loosened.** Four existing tests get *easier* under
the wider box and would have passed while testing nothing: `TestFooterTextFitsTwoLines`
(inner 60 → 74), `widestFooterRow`'s literal 60,
`TestSettingsOverlay_ShortTerminalScrollsToCursor` (whose category now trivially fits, since
`SelectRow` syncs the rail), and `TestSettingsOverlay_LongDescriptionCapsWithEllipsis` (whose
cap is unreachable at inner 74). Each gains a `require` that the case it exercises is real.
This is the failure mode PR A's own Task 8 Step 4 warned about and the one most likely to be
waved through in review.

### What the adversarial review changed, and the pattern behind it

Two independent reviewers read the first draft against the code. They converged on the same
top findings, which is what makes them worth recording rather than just fixing.

**Three of the defects were real rendering bugs, not test bugs** — the panel would have
hidden the thing the user was pointing at, and nothing in the first draft would have noticed:

1. The overflow marker overwrote the cursor's own line, so the selected row vanished for
   every cursor position except the last.
2. The rail dropped its tail below 24 rows, so the current entry could be off-screen with no
   indication.
3. The inert reason chip was squeezed out by inline enum alternatives at 80×24 — the
   degradation floor — leaving a dimmed row with no explanation, which the code's own comment
   calls worse than not dimming at all.

**The pattern is one blind spot, not three mistakes.** Every visibility guard ran at 100×32,
where nothing competes for width; guard 4 ran at the single height where thirteen entries fit
by exactly zero rows; and guard 6's narrowest sample sat three cells above the overflow. Each
guard was individually reasonable and the *set* of them tested one corner of the space. The
fix is the two sweeps Tasks 5 and 7 now carry — `TestSelectedRowIsAlwaysVisible`,
`TestCurrentRailEntryIsAlwaysVisible`, `TestVisibilitySignalsSurviveTheDegradationFloor` —
each of which asserts a property across sizes rather than a value at one size. The lesson to
carry into PR C: **a renderer guard that names one terminal size is testing that size, not the
renderer.**

**Six prescribed tests could not have passed as written.** The recurring cause was arithmetic
reasoned about rather than run: a width of 38 that made a test's own second assertion
impossible, a byte offset compared to a cell count, a scroll precondition against a budget
that no row in the schema can overflow at that size. Where a step now says "verify by printing
it once while iterating", that is not boilerplate.

**Two mutation steps predicted the wrong failures**, which is its own hazard: a mutation step
that over-claims teaches you to stop trusting the next one. Task 5's D5 mutation fails two
tests, not three — `TestBoxHeightDependsOnlyOnTheTerminal` cannot fail, because `paneHeight`
absorbs whatever `helpBlockHeight` returns. Both predictions are corrected in place, and three
mutations were added for guards that had none (guard 6, the label clamp, the badge
reservation).

**One design decision is flagged as the most likely to be challenged in review**, and is
deliberate rather than overlooked: the box now fills the terminal height, so a two-row category
on a 40-row terminal renders 27 blank pane lines. It buys an invariant worth more than the
compactness — the box's height depends only on the terminal, so `PlaceOverlay` never
re-centers the panel under the user's cursor mid-navigation (the jump
`ui/overlay/textInput_size.go:3-8` warns about), and opening `?` cannot resize it either. The
alternative — growing only to fit the current category — jumps every time the rail crosses
into or out of All settings. If the eyeball step makes the blank space look worse than the
jump, the change is one clamp in `paneHeight`.

