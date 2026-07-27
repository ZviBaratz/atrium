# Configuration panel redesign — PR A: schema, taxonomy, and copy

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the settings panel's three-section, free-string schema with a
ten-category closed vocabulary, rewrite all 37 rows' help copy into
summary + detail + a closed apply-timing enum, and add the default/reset/inert
mechanisms — all rendered by the *existing* single-column renderer, so no layout risk.

**Architecture:** `newSettingRows` moves out of `settings.go` into a new
`settings_schema.go` along with four new enums. `settingRow` gains eight fields; the
renderer changes only where it reads them (`section`→`category`,
`description`+`applyNote`→`summary`+`timing.footerNote()`). The mechanisms this PR adds
(`defaultDisplay`, `reset`, `activeWhen`, `detail`) are fully tested but not yet
*rendered* — PR B draws them. Structural guard tests make the taxonomy and copy
enforceable rather than reviewable-by-eye.

**Tech Stack:** Go 1.26, Bubble Tea, lipgloss, testify. Design record:
`docs/superpowers/specs/2026-07-25-configuration-panel-design.md` (referred to below as
"the spec").

## Global Constraints

- **Toolchain is mise-managed and not on `PATH`.** Test with
  `PATH="/home/zvi/.local/share/mise/installs/go/1.26.4/bin:$PATH" /home/zvi/.local/share/mise/installs/just/1.25.2/just test`,
  or `mise exec -- just test`. Lint with
  `PATH="/home/zvi/.local/share/mise/installs/go/1.26.4/bin:$PATH" /home/zvi/go/bin/golangci-lint run ./ui/...`
  — **scoped to `./ui/...`**, because the cache is global and a bare `run` reports issues
  from other atrium worktrees.
- **`just` has no lint recipe.** `build`/`vet`/`fmt-check`/`test` all pass while
  `golangci-lint` fails. Run lint yourself before every commit.
- **`unused` is the linter that bites this repo.** This PR adds fields (`detail`, `scope`)
  that nothing *renders* until PR B. Each is read by a test in this plan — that is
  deliberate, not incidental. Do not delete a field to silence the linter.
- **`revive` rules CI enforces that `go vet` does not:** `exported` (every exported symbol
  needs a doc comment starting with its name) and `redefines-builtin-id` (never name
  anything `max`, `min`, or `len`).
- **Every summary is ≤ 74 cells** — one unwrapped line at the 80-column floor
  (`boxWidth(78) − 4 = 74`). Enforced by a test in Task 4.
- **Copy is transcribed verbatim from spec §6.** Do not paraphrase, shorten, or "improve"
  it. Where a claim looks wrong, verify against the code and report it rather than
  silently rewording — spec §15 flags copy accuracy as this work's top risk.
- **Tests must stay hermetic.** `ui/overlay`'s `TestMain` already points `HOME` at a temp
  dir; any new test that reaches `config` must not touch the real data dir.
- **Conventional Commits, lowercase.** `feat:` / `fix:` / `refactor:` / `docs:`.
- **No new `keys` registry entries in this PR.** Panel-internal keys are handled on
  `msg.String()` and PR A adds none, so the registry/help/README key drift guards are
  untouched.

---

## File Structure

| File | Responsibility |
|---|---|
| **Create** `ui/overlay/settings_schema.go` | The four enums (`settingCategory`, `applyTiming`, `settingScope`, plus `kindReadOnly` on the existing `settingKind`), the `settingRow` struct, the `boolRow` helper, and `newSettingRows` — all 37 declarations plus the read-only config-path row. Data and vocabulary only; no rendering, no navigation. |
| **Modify** `ui/overlay/settings.go` | Loses ~520 lines to the schema file. Keeps the `SettingsOverlay` type, navigation, editing, and rendering. Changes only where it reads renamed fields. |
| **Create** `ui/overlay/settings_schema_test.go` | The structural guards: every scalar config field has exactly one row; nothing is modified on a fresh `DefaultConfig()`; summary bound and timing validity; the `activeWhen` predicates; the scope seam; the detail-retention assertions. |
| **Modify** `ui/overlay/settings_test.go` | Five adaptations enumerated in Task 8. Everything else must pass untouched. |
| **Modify** `README.md` | The configuration reference table gains a Category column. |

`settings.go` is 973 lines today and this PR roughly doubles the schema's size, so the
split is not optional — it is what keeps both halves reviewable, and PR B adds
`settings_nav.go` and `settings_render.go` alongside.

---

## Task 1: The four enums and their two projections

**Files:**
- Create: `ui/overlay/settings_schema.go`
- Test: `ui/overlay/settings_schema_test.go`

**Interfaces:**
- Consumes: nothing (first task).
- Produces: `settingCategory` (with `label() string`), `allCategories() []settingCategory`,
  `applyTiming` (with `footerNote() string` and `badge() string`), `settingScope` with
  `scopeGlobal`, and `kindReadOnly` added to `settingKind`. Every later task uses these.

- [ ] **Step 1: Write the failing tests**

Create `ui/overlay/settings_schema_test.go`:

```go
package overlay

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEveryCategoryHasALabel pins that allCategories is the complete ordered
// vocabulary and that every member resolves to a non-empty rail label. A category
// added to the enum without a label case would render as a blank section header.
func TestEveryCategoryHasALabel(t *testing.T) {
	cats := allCategories()
	require.Len(t, cats, 10, "the spec's taxonomy is ten scalar categories (spec §4)")

	seen := make(map[string]bool, len(cats))
	for _, c := range cats {
		label := c.label()
		require.NotEmptyf(t, label, "category %d has no label", int(c))
		require.Falsef(t, seen[label], "duplicate category label %q", label)
		seen[label] = true
	}
}

// TestCategoryCountFitsTheRailBudget pins the spec §4 invariant: the rail must fit
// unscrolled at the project's 80x24 degradation floor. Budget = 24 - (border 2 +
// padding 2 + title 1 + blank 1 + separator 1 + help 3 + hint 1) = 13 rows. PR B
// adds three non-scalar rail entries (All settings, Profiles, Accounts), so the
// scalar categories may not exceed 10 without displacing one of those.
func TestCategoryCountFitsTheRailBudget(t *testing.T) {
	const railBudget = 13
	const nonScalarRailEntries = 3 // All settings, Profiles, Accounts (PR B/D)
	assert.LessOrEqual(t, len(allCategories())+nonScalarRailEntries, railBudget,
		"a new category must displace another or the rail scrolls at 80x24 (spec §4)")
}

// TestApplyTimingProjections pins both projections of the closed timing enum: the
// footer note the single-column renderer appends today (empty for live, so 25 of 37
// rows stay unannotated) and the right-aligned chip PR B adds.
func TestApplyTimingProjections(t *testing.T) {
	assert.Equal(t, "", timingLive.footerNote(), "live needs no footer note")
	assert.Equal(t, "affects new sessions", timingNewSessions.footerNote())
	assert.Equal(t, "applies on restart", timingRestart.footerNote())

	assert.Equal(t, "live", timingLive.badge())
	assert.Equal(t, "new sessions", timingNewSessions.badge())
	assert.Equal(t, "restart", timingRestart.badge())
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run:
```bash
PATH="/home/zvi/.local/share/mise/installs/go/1.26.4/bin:$PATH" \
  go test ./ui/overlay/ -run 'TestEveryCategoryHasALabel|TestCategoryCountFits|TestApplyTimingProjections' -v
```
Expected: FAIL to build — `undefined: allCategories`, `undefined: timingLive`.

- [ ] **Step 3: Create the schema file with the enums**

Create `ui/overlay/settings_schema.go`:

```go
package overlay

// settingCategory identifies which section (PR A) and rail entry (PR B) a settings
// row belongs to. It is a closed vocabulary rendered from allCategories(), so adding
// a category is one deliberate edit — unlike the free-string `section` it replaces,
// where a typo silently created an eleventh section of one row.
type settingCategory int

const (
	catSessions settingCategory = iota
	catWorktrees
	catAppearance
	catSessionList
	catNotifications
	catAutomation
	catInput
	catProjects
	catUpdates
	catAdvanced
)

// allCategories returns every category in rail/section display order. It is the
// single ordered source: the renderer walks it rather than deriving order from the
// row declarations, so a row's position in newSettingRows cannot reorder sections.
func allCategories() []settingCategory {
	return []settingCategory{
		catSessions,
		catWorktrees,
		catAppearance,
		catSessionList,
		catNotifications,
		catAutomation,
		catInput,
		catProjects,
		catUpdates,
		catAdvanced,
	}
}

// label returns the category's section/rail label.
func (c settingCategory) label() string {
	switch c {
	case catSessions:
		return "Sessions"
	case catWorktrees:
		return "Worktrees & git"
	case catAppearance:
		return "Appearance"
	case catSessionList:
		return "Session list"
	case catNotifications:
		return "Notifications"
	case catAutomation:
		return "Automation"
	case catInput:
		return "Input"
	case catProjects:
		return "Projects"
	case catUpdates:
		return "Updates"
	case catAdvanced:
		return "Advanced"
	}
	return ""
}

// applyTiming says when a change to a setting takes effect. It is a closed enum with
// two projections, so the two renderers cannot drift: footerNote() is the prose the
// single-column footer appends after "·" (empty for live, which is most rows — saying
// "live" 25 times would be noise), and badge() is the right-aligned per-row chip the
// two-pane renderer adds in PR B.
//
// It deliberately has no "modifies your local branch" member. That was one of the old
// free-text applyNote's four values, and it is a caution rather than a timing; it now
// lives in fast_forward_local_base's detail (spec §5).
type applyTiming int

const (
	timingLive applyTiming = iota
	timingNewSessions
	timingRestart
)

// footerNote returns the prose the single-column footer appends, or "" for a change
// that applies immediately.
func (t applyTiming) footerNote() string {
	switch t {
	case timingNewSessions:
		return "affects new sessions"
	case timingRestart:
		return "applies on restart"
	}
	return ""
}

// badge returns the short right-aligned chip text for the two-pane renderer.
func (t applyTiming) badge() string {
	switch t {
	case timingNewSessions:
		return "new sessions"
	case timingRestart:
		return "restart"
	}
	return "live"
}

// settingScope is the seam for a later per-repo override layer (#477) and per-session
// settings (#454). Every row is scopeGlobal today, and the renderer and navigation
// must stay scope-agnostic so that layer adds a column and a switcher without
// reshaping this schema. Do not special-case scopeGlobal anywhere (spec §5).
type settingScope int

const (
	scopeGlobal settingScope = iota
)
```

- [ ] **Step 4: Add `kindReadOnly` to the existing kind enum**

In `ui/overlay/settings.go`, extend the `settingKind` block and its doc comment:

```go
// settingKind selects how a settings row is displayed and edited: bools toggle
// in place, enums cycle with ←/→, ints and texts open an inline line editor, and
// read-only rows display a resolved fact with no editor at all.
type settingKind int

const (
	kindBool settingKind = iota
	kindEnum
	kindInt
	kindText
	// kindReadOnly is a display-only row: it has no set, no reset, and no options,
	// and every edit key is a no-op on it. Used for the resolved config.json path
	// (spec §4, Advanced).
	kindReadOnly
)
```

- [ ] **Step 5: Run the tests to verify they pass**

Run:
```bash
PATH="/home/zvi/.local/share/mise/installs/go/1.26.4/bin:$PATH" \
  go test ./ui/overlay/ -run 'TestEveryCategoryHasALabel|TestCategoryCountFits|TestApplyTimingProjections' -v
```
Expected: PASS (3 tests).

- [ ] **Step 6: Verify the label guard actually guards**

Temporarily add `catBogus` to the const block **and** to `allCategories()` without a
`label()` case. Re-run the tests. Expected: `TestEveryCategoryHasALabel` FAILS with
"category 10 has no label" — and `require.Len(..., 10)` fails too. Revert both edits and
re-run to confirm green. A guard you have not watched fail is not a guard.

- [ ] **Step 7: Lint and commit**

```bash
PATH="/home/zvi/.local/share/mise/installs/go/1.26.4/bin:$PATH" /home/zvi/go/bin/golangci-lint run ./ui/...
git add ui/overlay/settings_schema.go ui/overlay/settings_schema_test.go ui/overlay/settings.go
git commit -m "refactor(settings): closed enums for category, apply timing, and scope"
```

---

## Task 2: Migrate the schema struct and all 37 rows

**Files:**
- Modify: `ui/overlay/settings_schema.go` (receives `settingRow`, `boolRow`, `newSettingRows`)
- Modify: `ui/overlay/settings.go:18-609` (removes them)

**Interfaces:**
- Consumes: Task 1's `settingCategory`, `applyTiming`, `settingScope`, `kindReadOnly`.
- Produces: the migrated `settingRow` struct (fields: `key`, `category`, `label`, `kind`,
  `scope`, `summary`, `detail`, `timing`, `get`, `editGet`, `set`, `options`, `gloss`,
  `defaultDisplay`, `reset`, `activeWhen`), the new
  `boolRow(key string, category settingCategory, label, summary, detail string, timing applyTiming, get func(*config.Config) bool, set func(*config.Config, bool)) settingRow`
  signature, and `newSettingRows(cfg *config.Config) []settingRow` returning 38 rows
  (37 config rows + the read-only config-path row).

**This task does not compile until every row is migrated.** Go will not build a partial
migration, so Steps 3–5 are edit-only and the first test run is Step 6. That is expected;
resist the urge to add temporary compatibility fields.

`defaultDisplay`, `reset`, and `activeWhen` are declared as struct fields here but left
`nil` on every row — Tasks 5 and 6 populate them. This keeps the mechanical rename
separate from the semantic work, so a reviewer can check the copy without also checking
predicates.

- [ ] **Step 1: Move the schema out of `settings.go`**

Cut `settingRow`, `boolRow`, and `newSettingRows` (currently `settings.go:52-609`) and
paste them into `settings_schema.go` below the enums. Leave `settingKind`,
`minPollIntervalMs`, `settingsVChrome`, and `settingsMinBody` in `settings.go` — they are
renderer constants, not schema.

- [ ] **Step 2: Rewrite the struct and the bool helper**

Replace the pasted `settingRow` and `boolRow` with:

```go
// settingRow declares one editable config field. The panel is driven entirely by
// this schema, so exposing a new Config field is a matter of appending a row — the
// navigation, editing, and rendering are generic.
//
// Rows are presentational + value plumbing only: set mutates the Config (with
// validation), but persisting to disk and live-applying side effects (theme repaint,
// tmux conf re-render) are the home model's job, keyed off the row's key (see
// app.applySettingChange).
type settingRow struct {
	key      string          // stable identifier home switches on for live-apply
	category settingCategory // section (PR A) / rail entry (PR B)
	label    string
	kind     settingKind
	scope    settingScope // scopeGlobal for every row today; the #477/#454 seam

	// summary is the one-line help shown whenever the row is selected. It is capped
	// at 74 cells so it never wraps at the 80-column floor (TestSummaryFitsOneLine).
	summary string
	// detail is the optional long-form help: the value grammar, cautions, and
	// cross-references that used to be crammed into one description. PR B renders it
	// behind `?`; PR A only stores it, so the content is in place before the surface
	// that shows it exists.
	detail string
	timing applyTiming // when a change takes effect

	get func(c *config.Config) string // display value
	// editGet returns the raw value to prefill the inline editor with; nil
	// means use get. Needed where display and raw differ (e.g. "unlimited").
	editGet func(c *config.Config) string
	set     func(c *config.Config, v string) error
	options func(c *config.Config) []string // enum rows only
	// gloss explains each enum option in one line, keyed by option value. It is what
	// dissolves the 300-443-char run-on descriptions: the option semantics move out
	// of the prose and onto the options themselves. Enum rows only.
	gloss map[string]string

	// defaultDisplay returns the display string of the built-in default, for the
	// "changed from default" marker. It MUST stay pure — no exec, no filesystem —
	// because it is reached from the render path; in particular it must never call
	// config.DefaultConfig(), which runs four agent-detection binary probes.
	//
	// A nil defaultDisplay means the row has no fixed default to diverge from and is
	// never marked modified. Exactly two rows are nil by design: default_program
	// (defaults to the first *detected* agent profile) and branch_prefix (defaults to
	// the OS username). A marker on either would be a lie — do not "fix" this.
	defaultDisplay func() string
	// reset restores the built-in default. nil for kindReadOnly and for the two rows
	// with no fixed default.
	reset func(c *config.Config)
	// activeWhen reports whether changing the row currently has any effect. nil means
	// always active. An inert row is dimmed and carries a reason chip but stays fully
	// editable — a user may configure ahead of enabling the parent (spec §5).
	activeWhen func(c *config.Config) bool
}

// boolRow builds a kindBool row over a getter and a setter; get displays
// "on"/"off" and set accepts the same strings (the toggle handler flips them).
// Its defaultDisplay is derived from the accessor's own default, which callers pass
// as defaultOn, so a bool row cannot disagree with its accessor about the default.
func boolRow(key string, category settingCategory, label, summary, detail string, timing applyTiming, defaultOn bool, get func(c *config.Config) bool, set func(c *config.Config, v bool)) settingRow {
	display := func(on bool) string {
		if on {
			return "on"
		}
		return "off"
	}
	return settingRow{
		key: key, category: category, label: label, kind: kindBool,
		summary: summary, detail: detail, timing: timing, scope: scopeGlobal,
		get: func(c *config.Config) string { return display(get(c)) },
		set: func(c *config.Config, v string) error {
			set(c, v == "on")
			return nil
		},
		defaultDisplay: func() string { return display(defaultOn) },
		reset:          func(c *config.Config) { set(c, defaultOn) },
	}
}
```

Note `boolRow` now takes `defaultOn` and wires `defaultDisplay`/`reset` itself — so the
13 bool rows get Task 5's mechanism for free, and only the 24 non-bool rows need it by
hand.

- [ ] **Step 3: Migrate the rows, category by category**

For each of the ten categories, set `category:`, `scope: scopeGlobal`, replace
`description:` with `summary:` + `detail:`, replace `applyNote:` with `timing:`, and add
`gloss:` to enum rows. **Transcribe every summary, detail, and gloss string verbatim from
spec §6** — it is the reviewed source of truth and Task 4's guards will not catch a
paraphrase, only a missing or over-long one.

Assignments are spec §4; timings are spec §5 (`timingNewSessions` for
`default_program`, `branch_prefix`, `carry_files`, `link_paths`,
`session_context_bar`, `tmux_config_override`, `update_base_on_create`,
`fast_forward_local_base`, `agent_oom_margin`; `timingRestart` for `auto_update`,
`trust_worktrees_root`, `daemon_poll_interval`; `timingLive` for the other 25).

Three worked examples, one per shape. An **enum with a gloss**:

```go
		{
			key: "notifications", category: catNotifications, label: "Notifications", kind: kindEnum,
			scope:   scopeGlobal,
			timing:  timingLive,
			summary: "How Atrium signals a background session that finishes or blocks.",
			detail: "The selected, attached and muted sessions always stay silent, and so " +
				"does a focused terminal unless Notify when focused is on.",
			gloss: map[string]string{
				config.NotificationsOff:     "no signal",
				config.NotificationsBell:    "rings the terminal",
				config.NotificationsDesktop: "runs a notifier",
				config.NotificationsOSC:     "an OSC 9 escape that reaches you over SSH with no local binary",
			},
			get: func(c *config.Config) string { return c.GetNotifications() },
			set: func(c *config.Config, v string) error {
				c.Notifications = v
				return nil
			},
			options: func(c *config.Config) []string {
				return []string{config.NotificationsOff, config.NotificationsBell, config.NotificationsDesktop, config.NotificationsOSC}
			},
		},
```

An **int with a tri-state `editGet`** (body unchanged from today — only the help fields move):

```go
		{
			key: "max_sessions", category: catSessions, label: "Session limit", kind: kindInt,
			scope:   scopeGlobal,
			timing:  timingLive,
			summary: "How many sessions Atrium will hold. Empty auto-derives from this host.",
			detail: "Empty is a soft cap of half your CPU threads (minimum 2), counting only " +
				"live sessions — a create or resume past it asks for confirmation rather than " +
				"refusing. A number is a hard cap on every session, paused ones included, and a " +
				"create past it is refused. 0 means unlimited, with no confirmation. " +
				"`atrium doctor` reports the same host capacity.",
			get: func(c *config.Config) string {
				switch {
				case c.MaxSessions == nil:
					return fmt.Sprintf("auto (%d)", config.DefaultSessionCap())
				case *c.MaxSessions < 1:
					return "unlimited"
				default:
					return strconv.Itoa(*c.MaxSessions)
				}
			},
			editGet: func(c *config.Config) string {
				switch {
				case c.MaxSessions == nil:
					return "" // empty selects the host-derived auto default
				case *c.MaxSessions < 1:
					return "0" // explicit unlimited edits as 0
				default:
					return strconv.Itoa(*c.MaxSessions)
				}
			},
			set: func(c *config.Config, v string) error {
				v = strings.TrimSpace(v)
				if v == "" {
					c.MaxSessions = nil // auto (host-derived)
					return nil
				}
				n, err := strconv.Atoi(v)
				if err != nil || n < 0 {
					return fmt.Errorf("max sessions must be a non-negative number (0 = unlimited, empty = auto)")
				}
				c.MaxSessions = &n // 0 = explicit unlimited; positive = hard cap
				return nil
			},
		},
```

A **bool** through the new helper:

```go
		boolRow("mouse", catInput, "Mouse",
			"Clickable rows, tabs and hint bar, wheel scroll, draggable divider.",
			"Off hands the mouse back to the terminal so native select-to-copy works. "+
				"While on, Shift+drag is the per-gesture escape.",
			timingLive, true,
			(*config.Config).GetMouse,
			func(c *config.Config, v bool) { c.Mouse = &v }),
```

Six labels change (spec §5): `Max sessions`→`Session limit`,
`Session context bar`→`In-session status bar`, `Poll interval (ms)`→`Auto-yes poll
interval`, `Session sort`→`Sort within group`, `Smart dispatch auto-create`→`Smart
dispatch`, `Release notes after update`→`Release notes`. Every other label is unchanged.

Keep the `rawDefaultProgram` capture and its comment exactly as it is — a hand-edited raw
command in `default_program` must stay a cycle option.

- [ ] **Step 4: Add the read-only config-path row**

Append to `catAdvanced`. The path is resolved **once**, at `newSettingRows` time:
`config.GetConfigDir()` stats the filesystem twice, and the render path must not.

```go
		// The resolved config.json path, so the file the panel writes is discoverable
		// from inside the panel. Resolved once here rather than per render: GetConfigDir
		// stats the filesystem, and #380 is explicit that the render path does not.
		{
			key: "config_file", category: catAdvanced, label: "Config file", kind: kindReadOnly,
			scope:   scopeGlobal,
			timing:  timingLive,
			summary: "Where Atrium keeps the settings on this page.",
			detail: "Atrium reads this file at launch and rewrites it whenever you change a " +
				"setting here, so an edit made by hand while the TUI is running will be " +
				"overwritten.",
			get: func(c *config.Config) string { return configFilePath },
		},
```

Above `newSettingRows`, add the resolver:

```go
// configFilePath is the resolved config.json path shown by the read-only Config file
// row. It is resolved at init rather than per render (GetConfigDir stats the
// filesystem) and degrades to a legible placeholder rather than an empty cell when
// the home directory cannot be determined.
var configFilePath = func() string {
	dir, err := config.GetConfigDir()
	if err != nil {
		return "(unresolved)"
	}
	return filepath.Join(dir, "config.json")
}()
```

Add `"path/filepath"` to the imports.

- [ ] **Step 5: Update the renderer's field reads**

In `settings.go`, three call sites read renamed fields:

1. `renderBody`'s section-break loop compares `r.section` — change to a
   `settingCategory` and render `r.category.label()`:

```go
	var lines []bodyLine
	first := true
	lastCategory := allCategories()[0]
	for i, r := range s.rows {
		if first || r.category != lastCategory {
			if !first {
				lines = append(lines, bodyLine{text: "", rowIdx: -1})
			}
			lines = append(lines, bodyLine{text: headerStyle.Render(r.category.label()), rowIdx: -1})
			lastCategory = r.category
			first = false
		}
```

   The old loop used `lastSection != ""` as its "first iteration" test, which a
   zero-valued `settingCategory` cannot express — `catSessions` is 0, so an
   uninitialized `lastCategory` would equal the first row's category and swallow its
   header. The explicit `first` flag is why.

2. `renderFooter` builds `desc` from `description` + `applyNote`:

```go
	desc := row.summary
	style := t.DimStyle()
	if s.lastErr != "" {
		desc = s.lastErr
		style = t.DangerStyle()
	} else if note := row.timing.footerNote(); note != "" {
		desc += " · " + note
	}
```

3. `renderValue` must not draw an editor affordance for a read-only row:

```go
	case kindReadOnly:
		return v
```

   and `HandleKeyPress`'s `enter` switch already falls through for an unlisted kind, so
   `kindReadOnly` is inert there without further change. Confirm that by reading the
   switch rather than assuming it.

- [ ] **Step 6: Build and run the full package**

Run:
```bash
PATH="/home/zvi/.local/share/mise/installs/go/1.26.4/bin:$PATH" go build ./... && \
PATH="/home/zvi/.local/share/mise/installs/go/1.26.4/bin:$PATH" go test ./ui/overlay/ 2>&1 | tail -40
```
Expected: it **builds**, and exactly the five tests named in Task 8 fail (they pin the old
section names, the old "Max sessions" label, and description prose that is now `detail`).
Any *other* failure is a migration mistake — fix it before moving on. Do not touch the
failing five yet; Task 8 owns them.

- [ ] **Step 7: Commit**

```bash
PATH="/home/zvi/.local/share/mise/installs/go/1.26.4/bin:$PATH" /home/zvi/go/bin/golangci-lint run ./ui/...
git add ui/overlay/settings_schema.go ui/overlay/settings.go
git commit -m "refactor(settings): ten-category taxonomy and summary/detail help copy"
```

---

## Task 3: Guard — every scalar config field has exactly one row

**Files:**
- Modify: `ui/overlay/settings_schema_test.go`

**Interfaces:**
- Consumes: `newSettingRows`, `kindReadOnly`.
- Produces: nothing consumed later. This is a drift guard.

- [ ] **Step 1: Write the failing test**

Append to `settings_schema_test.go`:

```go
// TestEveryScalarConfigFieldHasARow is the panel twin of
// config.TestReadmeDocumentsEveryConfigField: a new scalar config key must not ship
// reachable only by hand-editing config.json, because that makes it invisible to
// every user who configures Atrium through the panel.
//
// Exempt are the four list-of-record keys, which a one-value-per-row panel cannot
// express (accounts are managed from the Accounts overlay; profiles get their own
// editor in PR D, which is not a settingRow either), and the deprecated nerd_font,
// superseded by glyph_set.
func TestEveryScalarConfigFieldHasARow(t *testing.T) {
	exempt := map[string]string{
		"profiles":        "list of records — Profiles editor (PR D), not a settingRow",
		"claude_accounts": "list of records — Accounts overlay",
		"gh_accounts":     "list of records — Accounts overlay",
		"agy_accounts":    "list of records — Accounts overlay",
		"nerd_font":       "deprecated, superseded by glyph_set",
	}

	count := map[string]int{}
	for _, r := range newSettingRows(config.DefaultConfig()) {
		count[r.key]++
	}

	tp := reflect.TypeOf(config.Config{})
	for i := 0; i < tp.NumField(); i++ {
		name := strings.Split(tp.Field(i).Tag.Get("json"), ",")[0]
		if name == "" || name == "-" {
			continue
		}
		if reason, ok := exempt[name]; ok {
			assert.Zerof(t, count[name], "%s is exempt (%s) but has a settings row", name, reason)
			continue
		}
		assert.Equalf(t, 1, count[name],
			"config field %s (json:%q) must have exactly one settings row", tp.Field(i).Name, name)
	}
}

// TestEveryRowKeyIsAConfigFieldOrReadOnly is the reverse direction: a row whose key
// matches no config field would persist nothing, and applySettingChange would switch
// on a key that can never arrive. kindReadOnly rows are exempt — they display a
// resolved fact (the config.json path) rather than a config value.
func TestEveryRowKeyIsAConfigFieldOrReadOnly(t *testing.T) {
	fields := map[string]bool{}
	tp := reflect.TypeOf(config.Config{})
	for i := 0; i < tp.NumField(); i++ {
		if name := strings.Split(tp.Field(i).Tag.Get("json"), ",")[0]; name != "" && name != "-" {
			fields[name] = true
		}
	}

	for _, r := range newSettingRows(config.DefaultConfig()) {
		if r.kind == kindReadOnly {
			assert.Nil(t, r.set, "a read-only row must have no setter: %s", r.key)
			continue
		}
		assert.Truef(t, fields[r.key], "row %q matches no config.Config json field", r.key)
	}
}
```

Add `"reflect"`, `"strings"`, and `"github.com/ZviBaratz/atrium/config"` to the test
imports.

- [ ] **Step 2: Run and expect PASS**

Run:
```bash
PATH="/home/zvi/.local/share/mise/installs/go/1.26.4/bin:$PATH" \
  go test ./ui/overlay/ -run 'TestEveryScalarConfigFieldHasARow|TestEveryRowKeyIsAConfigField' -v
```
Expected: PASS. A drift guard passes the moment it is written if the tree is already
correct — which is why Step 3 exists.

- [ ] **Step 3: Verify both guards fail when they should**

This is mandatory. A guard nobody has seen fail proves nothing.

1. Delete the `kill_double_tap_confirm` row from `newSettingRows`. Run the tests.
   Expected: `TestEveryScalarConfigFieldHasARow` FAILS with "must have exactly one
   settings row". Restore it.
2. Change one row's `key` to `"not_a_field"`. Run the tests. Expected:
   `TestEveryRowKeyIsAConfigFieldOrReadOnly` FAILS with "matches no config.Config json
   field". Restore it.
3. Re-run and confirm green.

- [ ] **Step 4: Commit**

```bash
git add ui/overlay/settings_schema_test.go
git commit -m "test(settings): guard that every scalar config field has exactly one row"
```

---

## Task 4: Guard — summary bound, timing validity, and the scope seam

**Files:**
- Modify: `ui/overlay/settings_schema_test.go`

**Interfaces:**
- Consumes: `newSettingRows`, `allCategories`, `applyTiming`, `scopeGlobal`.
- Produces: nothing consumed later.

- [ ] **Step 1: Write the failing tests**

```go
// summaryBudget is the widest a summary may be: the panel's inner width at the
// project's 80-column floor once PR B widens the box to min(96, width-2) — a 78-cell
// box, inner 74. Today's boxWidth is capped at 64 (inner 60), so a summary near this
// bound wraps onto a second footer line under the current renderer; that is harmless
// while the footer is still variable-height, and PR B's wider box plus fixed-height
// help pane makes it one line. The bound is enforced now so the copy does not have to
// be revisited when the box grows.
const summaryBudget = 74

// TestSummaryFitsOneLine pins the summary bound from spec §6. A summary that wraps
// is not a defect on its own, but the bound is what keeps the fixed-height help pane
// PR B introduces from needing to scroll for an ordinary row.
func TestSummaryFitsOneLine(t *testing.T) {
	for _, r := range newSettingRows(config.DefaultConfig()) {
		require.NotEmptyf(t, r.summary, "row %q has no summary", r.key)
		assert.LessOrEqualf(t, runewidth.StringWidth(r.summary), summaryBudget,
			"row %q summary is %d cells, over the %d-cell budget: %q",
			r.key, runewidth.StringWidth(r.summary), summaryBudget, r.summary)
	}
}

// TestEveryRowHasAKnownCategoryAndScope pins that no row carries a category outside
// allCategories() (which would render under no section header at all) and that the
// scope seam is uniform. The scope assertion is also what keeps the `unused` linter
// from flagging a field PR A stores but does not yet render.
func TestEveryRowHasAKnownCategoryAndScope(t *testing.T) {
	known := map[settingCategory]bool{}
	for _, c := range allCategories() {
		known[c] = true
	}
	for _, r := range newSettingRows(config.DefaultConfig()) {
		assert.Truef(t, known[r.category], "row %q has a category outside allCategories()", r.key)
		assert.Equalf(t, scopeGlobal, r.scope, "row %q must be scopeGlobal until #477 adds a layer", r.key)
	}
}

// TestEnumRowsGlossEveryOption pins that each enum option carries a one-line gloss.
// This is what replaced the 300-443-char run-on descriptions: if an option has no
// gloss, its meaning went missing rather than moving.
func TestEnumRowsGlossEveryOption(t *testing.T) {
	cfg := config.DefaultConfig()
	for _, r := range newSettingRows(cfg) {
		if r.kind != kindEnum {
			continue
		}
		for _, opt := range r.options(cfg) {
			assert.NotEmptyf(t, r.gloss[opt], "enum row %q has no gloss for option %q", r.key, opt)
		}
	}
}

// TestCategoryRowCounts pins the spec §4 taxonomy row-by-row, so a row cannot drift
// to a neighbouring category unnoticed during a later refactor. The Advanced count
// includes the read-only config-file row.
func TestCategoryRowCounts(t *testing.T) {
	want := map[settingCategory]int{
		catSessions:      4,
		catWorktrees:     6,
		catAppearance:    5,
		catSessionList:   5,
		catNotifications: 4,
		catAutomation:    4,
		catInput:         3,
		catProjects:      2,
		catUpdates:       2,
		catAdvanced:      3, // 2 settings + the read-only config-file row
	}
	got := map[settingCategory]int{}
	total := 0
	for _, r := range newSettingRows(config.DefaultConfig()) {
		got[r.category]++
		total++
	}
	assert.Equal(t, 38, total, "37 config rows plus the read-only config-file row")
	for _, c := range allCategories() {
		assert.Equalf(t, want[c], got[c], "category %q row count", c.label())
	}
}
```

Add `"github.com/mattn/go-runewidth"` to the test imports (already a module dependency —
`ui/theme`'s glyph-width test uses it).

**On `summaryBudget` = 74, already resolved:** `boxWidth()` is `min(64, width−2)`, so at an
80-column terminal the inner width is **60**, not 74 — a summary near the bound wraps to a
second footer line under this PR's renderer. That is deliberate and harmless: PR A keeps
today's variable-height footer, and PR B widens the box to `min(96, width−2)` (inner 74 at
width 80), at which point the bound is exactly one line. Enforcing 74 now means the copy is
written once. Do **not** lower the constant to 60 — that would force a second rewrite of
every summary when the box grows.

- [ ] **Step 2: Run the tests**

Run:
```bash
PATH="/home/zvi/.local/share/mise/installs/go/1.26.4/bin:$PATH" \
  go test ./ui/overlay/ -run 'TestSummaryFitsOneLine|TestEveryRowHasAKnownCategory|TestEnumRowsGlossEveryOption|TestCategoryRowCounts' -v
```
Expected: PASS. If `TestEnumRowsGlossEveryOption` fails, an enum's gloss map is missing an
option — likely `theme` or `splash`, whose options are dynamic (`theme.Names()`,
`config.SplashVariants()`).

**If a dynamic-option enum cannot be glossed exhaustively** (theme and splash both list
values that grow when a new theme or splash variant is added), do not weaken the test to
"some options glossed". Exempt those two rows by key with a comment naming why — a theme
name glosses itself — and keep the guard strict for the enums whose options are a fixed
vocabulary.

- [ ] **Step 3: Verify the summary guard fails when it should**

Append 40 characters to any row's summary. Run `TestSummaryFitsOneLine`. Expected: FAIL
naming that row and its cell count. Revert.

- [ ] **Step 4: Commit**

```bash
PATH="/home/zvi/.local/share/mise/installs/go/1.26.4/bin:$PATH" /home/zvi/go/bin/golangci-lint run ./ui/...
git add ui/overlay/settings_schema_test.go
git commit -m "test(settings): guard summary budget, category assignment, and enum glosses"
```

---

## Task 5: `defaultDisplay`, `reset`, and the modified predicate

**Files:**
- Modify: `ui/overlay/settings_schema.go` (populate the two fields on the 25 non-bool rows)
- Modify: `ui/overlay/settings.go` (add the `isModified` method)
- Modify: `ui/overlay/settings_schema_test.go`

**Interfaces:**
- Consumes: the migrated schema.
- Produces: `func (s *SettingsOverlay) isModified(i int) bool` — PR B's renderer calls it
  to draw the marker; `row.reset` — PR C's `r` key calls it.

- [ ] **Step 1: Write the failing tests**

```go
// TestNoRowIsModifiedOnAFreshConfig is the cheapest guard on defaultDisplay: on a
// default config, nothing may claim to be changed. It catches every default that was
// transcribed wrong, in one assertion per row, without enumerating the expected
// values a second time (which would just move the transcription risk).
func TestNoRowIsModifiedOnAFreshConfig(t *testing.T) {
	cfg := config.DefaultConfig()
	o := NewSettingsOverlay(cfg)
	for i, r := range o.rows {
		if r.defaultDisplay == nil {
			continue // default_program and branch_prefix — machine-derived, spec §5
		}
		assert.Falsef(t, o.isModified(i),
			"row %q is marked modified on a fresh config: value %q vs default %q",
			r.key, r.get(cfg), r.defaultDisplay())
	}
}

// TestOnlyMachineDerivedRowsOptOutOfDefaults pins *which* rows are allowed to have no
// default, so a future row cannot quietly skip the marker by leaving the field nil.
func TestOnlyMachineDerivedRowsOptOutOfDefaults(t *testing.T) {
	var optedOut []string
	for _, r := range newSettingRows(config.DefaultConfig()) {
		if r.defaultDisplay == nil && r.kind != kindReadOnly {
			optedOut = append(optedOut, r.key)
		}
	}
	assert.ElementsMatch(t, []string{"default_program", "branch_prefix"}, optedOut,
		"only the two machine-derived rows may have no default (spec §5)")
}

// TestModifiedTracksAnEdit pins the positive direction: after a change, the row does
// report modified. Without this the suite would pass with isModified hardwired false.
func TestModifiedTracksAnEdit(t *testing.T) {
	cfg := config.DefaultConfig()
	o := NewSettingsOverlay(cfg)
	settingsAt(t, o, "mouse")
	i := o.cursor

	require.False(t, o.isModified(i), "mouse starts at its default")
	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeySpace}) // toggle off
	assert.True(t, o.isModified(i), "a toggled row reports modified")
}

// TestResetRestoresTheDefault pins that every resettable row's reset returns it to the
// value defaultDisplay advertises — the two must agree, or `r` would leave a row still
// marked modified.
func TestResetRestoresTheDefault(t *testing.T) {
	cfg := config.DefaultConfig()
	o := NewSettingsOverlay(cfg)
	for i, r := range o.rows {
		if r.reset == nil || r.defaultDisplay == nil {
			continue
		}
		r.reset(cfg)
		assert.Equalf(t, r.defaultDisplay(), r.get(cfg),
			"row %q: reset must produce the advertised default", r.key)
		assert.Falsef(t, o.isModified(i), "row %q is still modified after reset", r.key)
	}
}
```

- [ ] **Step 2: Run to verify they fail**

Run:
```bash
PATH="/home/zvi/.local/share/mise/installs/go/1.26.4/bin:$PATH" \
  go test ./ui/overlay/ -run 'TestNoRowIsModified|TestOnlyMachineDerived|TestModifiedTracks|TestResetRestores' -v
```
Expected: FAIL to build — `o.isModified undefined`.

- [ ] **Step 3: Add the predicate**

In `settings.go`:

```go
// isModified reports whether row i's value differs from its built-in default, for
// the "changed from default" marker. A row with no fixed default (see
// settingRow.defaultDisplay) is never modified.
func (s *SettingsOverlay) isModified(i int) bool {
	row := s.rows[i]
	if row.defaultDisplay == nil {
		return false
	}
	return row.get(s.cfg) != row.defaultDisplay()
}
```

- [ ] **Step 4: Populate `defaultDisplay` and `reset` on the non-bool rows**

The 13 `boolRow` calls are already done via Task 2's `defaultOn` parameter. For the rest,
the default must come from the same accessor default the getter uses — never a literal
copied by hand, or the two drift. Examples covering each shape:

```go
			// tri-state int: nil is the default, and its display is the getter's own
			// "auto (N)" form, so the two cannot disagree about N.
			defaultDisplay: func() string { return fmt.Sprintf("auto (%d)", config.DefaultSessionCap()) },
			reset:          func(c *config.Config) { c.MaxSessions = nil },
```

```go
			// enum with a named default constant
			defaultDisplay: func() string { return config.NotificationsOff },
			reset:          func(c *config.Config) { c.Notifications = "" },
```

```go
			// list-valued text row: the default is the accessor's default list, joined
			// exactly as get joins it.
			defaultDisplay: func() string { return strings.Join(config.DefaultCarryFiles(), ", ") },
			reset:          func(c *config.Config) { c.CarryFiles = nil },
```

Two things to check while doing this:

1. **`carry_files`' default.** `GetCarryFiles` returns `[".claude/settings.local.json"]`
   for a nil field, and `get` renders an empty list as `(none)`. Find how the default
   list is exposed — if there is no exported `DefaultCarryFiles`, either add one in
   `config/accessors.go` beside the accessor or derive the default display by calling
   `(&config.Config{}).GetCarryFiles()`. Prefer the latter: it needs no new API and
   cannot drift from the accessor. The same trick works for every tri-state row —
   `(&config.Config{}).GetX()` *is* the default by definition.
2. **`splash` and `theme`.** Their defaults are `config.SplashRandom` and
   `theme.DefaultThemeName`; `get` already substitutes those for an empty field, so
   `defaultDisplay` must return the same substituted string, not `""`.

The `(&config.Config{}).GetX()` pattern is the recommended default for all 25 — it is
pure (no exec, no filesystem), it cannot disagree with the getter, and
`TestNoRowIsModifiedOnAFreshConfig` proves it row by row. Use a literal only where no
accessor exists.

- [ ] **Step 5: Run the tests**

Run:
```bash
PATH="/home/zvi/.local/share/mise/installs/go/1.26.4/bin:$PATH" \
  go test ./ui/overlay/ -run 'TestNoRowIsModified|TestOnlyMachineDerived|TestModifiedTracks|TestResetRestores' -v
```
Expected: PASS (4 tests). A failure here names the row and both values — fix the default,
not the test.

- [ ] **Step 6: Commit**

```bash
PATH="/home/zvi/.local/share/mise/installs/go/1.26.4/bin:$PATH" /home/zvi/go/bin/golangci-lint run ./ui/...
git add ui/overlay/settings_schema.go ui/overlay/settings.go ui/overlay/settings_schema_test.go
git commit -m "feat(settings): per-row defaults and reset, with a modified predicate"
```

---

## Task 6: The `activeWhen` inert predicates

**Files:**
- Modify: `ui/overlay/settings_schema.go`
- Modify: `ui/overlay/settings_schema_test.go`

**Interfaces:**
- Consumes: the migrated schema.
- Produces: `row.activeWhen` populated on seven rows; PR B dims on it and renders the
  reason. **The reason string is not part of this task** — PR B owns it, because it is
  rendered text and belongs with the renderer that positions it.

- [ ] **Step 1: Verify the `group_mode` predicate against the list, before writing it**

Spec §5 flags this as the one predicate derived from prose rather than code. Find
`ui.List`'s actual account-clustering gate:

```bash
grep -rn "GroupModeAccount\|clusters\|accountCluster" ui/list*.go | grep -v _test | head -20
```

The row's prose says clustering is "a visual no-op unless two or more accounts are
present". Determine what the list actually gates on — configured `ClaudeAccounts`,
distinct accounts across live sessions, or cluster count (which
`atrium-account-cluster-reorder` records as *not* equal to account count). **Write the
predicate the list uses, not the one this plan guesses.** If they disagree, say so in the
commit message and use the list's.

- [ ] **Step 2: Write the failing tests**

```go
// TestInertPredicates pins each activeWhen from spec §5: a row is inert exactly when
// changing it cannot currently do anything. Each case toggles the parent and asserts
// both directions, so a predicate stuck at true or false fails.
func TestInertPredicates(t *testing.T) {
	tests := []struct {
		name         string
		row          string
		makeInert    func(*config.Config)
		makeActive   func(*config.Config)
	}{
		{
			name:       "finished turns follows notifications",
			row:        "notifications_finished",
			makeInert:  func(c *config.Config) { c.Notifications = config.NotificationsOff },
			makeActive: func(c *config.Config) { c.Notifications = config.NotificationsBell },
		},
		{
			name:       "notify when focused follows notifications",
			row:        "notify_when_focused",
			makeInert:  func(c *config.Config) { c.Notifications = config.NotificationsOff },
			makeActive: func(c *config.Config) { c.Notifications = config.NotificationsBell },
		},
		{
			name:       "notify command needs desktop mode specifically",
			row:        "notify_command",
			makeInert:  func(c *config.Config) { c.Notifications = config.NotificationsBell },
			makeActive: func(c *config.Config) { c.Notifications = config.NotificationsDesktop },
		},
		{
			name:       "fast-forward follows update base on create",
			row:        "fast_forward_local_base",
			makeInert:  func(c *config.Config) { f := false; c.UpdateBaseOnCreate = &f },
			makeActive: func(c *config.Config) { tr := true; c.UpdateBaseOnCreate = &tr },
		},
		{
			name:       "poll interval follows auto-yes",
			row:        "daemon_poll_interval",
			makeInert:  func(c *config.Config) { c.AutoYes = false },
			makeActive: func(c *config.Config) { c.AutoYes = true },
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.DefaultConfig()
			row := rowByKey(t, cfg, tc.row)
			require.NotNil(t, row.activeWhen, "row %q must declare activeWhen", tc.row)

			tc.makeInert(cfg)
			assert.False(t, row.activeWhen(cfg), "expected inert")
			tc.makeActive(cfg)
			assert.True(t, row.activeWhen(cfg), "expected active")
		})
	}
}

// TestInertRowsStayEditable pins the rule from spec §5: inert means "changing this has
// no effect right now", never "you may not touch this" — a user may configure ahead of
// enabling the parent. An inert row keeps its setter and its reset.
func TestInertRowsStayEditable(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Notifications = config.NotificationsOff
	row := rowByKey(t, cfg, "notifications_finished")

	require.False(t, row.activeWhen(cfg), "the row is inert with notifications off")
	require.NoError(t, row.set(cfg, config.NotificationsBell), "an inert row is still settable")
	assert.Equal(t, config.NotificationsBell, cfg.NotificationsFinished)
}

// TestOOMMarginIsLinuxOnly pins the one platform predicate. It asserts against the
// build's own GOOS rather than a hardcoded expectation, so it is meaningful on the
// macOS CI job too.
func TestOOMMarginIsLinuxOnly(t *testing.T) {
	cfg := config.DefaultConfig()
	row := rowByKey(t, cfg, "agent_oom_margin")
	require.NotNil(t, row.activeWhen)
	assert.Equal(t, runtime.GOOS == "linux", row.activeWhen(cfg))
}

// rowByKey returns the row with the given key, failing the test when absent.
func rowByKey(t *testing.T, cfg *config.Config, key string) settingRow {
	t.Helper()
	for _, r := range newSettingRows(cfg) {
		if r.key == key {
			return r
		}
	}
	t.Fatalf("no settings row with key %q", key)
	return settingRow{}
}
```

Add `"runtime"` to the test imports.

- [ ] **Step 3: Run to verify they fail**

Run:
```bash
PATH="/home/zvi/.local/share/mise/installs/go/1.26.4/bin:$PATH" \
  go test ./ui/overlay/ -run 'TestInertPredicates|TestInertRowsStayEditable|TestOOMMarginIsLinuxOnly' -v
```
Expected: FAIL — "row %q must declare activeWhen" (nil after Task 2).

- [ ] **Step 4: Add the predicates**

On the six settled rows:

```go
			// notifications_finished, notify_when_focused
			activeWhen: func(c *config.Config) bool {
				return c.GetNotifications() != config.NotificationsOff
			},
```
```go
			// notify_command — desktop is the only mode that runs a command
			activeWhen: func(c *config.Config) bool {
				return c.GetNotifications() == config.NotificationsDesktop
			},
```
```go
			// fast_forward_local_base — nothing to fast-forward if the base is not refreshed
			activeWhen: (*config.Config).GetUpdateBaseOnCreate,
```
```go
			// daemon_poll_interval — the daemon only runs when auto-yes is on
			activeWhen: func(c *config.Config) bool { return c.AutoYes },
```
```go
			// agent_oom_margin — the kernel knob is Linux-only
			activeWhen: func(c *config.Config) bool { return runtime.GOOS == "linux" },
```

Add `"runtime"` to `settings_schema.go`'s imports. Then add `group_mode`'s predicate as
determined in Step 1, and add a `t.Run` case for it to `TestInertPredicates` matching what
you found.

- [ ] **Step 5: Run the tests**

Run:
```bash
PATH="/home/zvi/.local/share/mise/installs/go/1.26.4/bin:$PATH" \
  go test ./ui/overlay/ -run 'TestInert|TestOOMMargin' -v
```
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
PATH="/home/zvi/.local/share/mise/installs/go/1.26.4/bin:$PATH" /home/zvi/go/bin/golangci-lint run ./ui/...
git add ui/overlay/settings_schema.go ui/overlay/settings_schema_test.go
git commit -m "feat(settings): declare which rows are currently inert"
```

---

## Task 7: Preserve the prose the copy rewrite moves

**Files:**
- Modify: `ui/overlay/settings_schema_test.go`

**Interfaces:**
- Consumes: the migrated schema.
- Produces: nothing. This task exists to stop PR A from silently deleting user-facing
  documentation.

The rewrite moves long-form content out of `description` into `detail`, which PR A does
**not render** (PR B shows it behind `?`). Without an assertion, the reordering grammar,
the trailing-slash warning, and the OOM mechanism could be dropped in transcription and
every test would still pass — the old tests that covered them assert on *rendered* output,
and Task 8 is about to retarget those.

- [ ] **Step 1: Write the failing test**

```go
// TestDetailRetainsTheMovedProse pins the specific facts that moved from the old
// one-paragraph descriptions into detail. Each was the only place Atrium documented
// something a user cannot discover from the UI, and PR A does not render detail yet —
// so without this test, losing one in transcription is invisible until PR B ships.
func TestDetailRetainsTheMovedProse(t *testing.T) {
	want := map[string][]string{
		"group_mode": {
			"an account boundary is refused", // the {/} reorder rule
			"[",                              // the cluster-reorder keys
		},
		"link_paths": {
			"no trailing slash", // the ignore-pattern trap from #471
		},
		"agent_oom_margin": {
			"Linux only",
			"oom_score_adj", // names the kernel knob so the setting is searchable
		},
		"max_sessions": {
			"paused ones included", // the hard-cap contract
		},
		"notify_command": {
			"ATRIUM_SESSION", // the env contract
		},
		"mouse": {
			"Shift+drag", // the per-gesture escape hatch
		},
		"notifications": {
			"muted", // which sessions stay silent
		},
	}

	rows := newSettingRows(config.DefaultConfig())
	byKey := make(map[string]settingRow, len(rows))
	for _, r := range rows {
		byKey[r.key] = r
	}

	for key, phrases := range want {
		row, ok := byKey[key]
		require.Truef(t, ok, "no row %q", key)
		for _, p := range phrases {
			assert.Containsf(t, row.detail, p,
				"row %q lost %q from its help — it documents something the UI cannot show", key, p)
		}
	}
}
```

- [ ] **Step 2: Run it**

Run:
```bash
PATH="/home/zvi/.local/share/mise/installs/go/1.26.4/bin:$PATH" \
  go test ./ui/overlay/ -run TestDetailRetainsTheMovedProse -v
```
Expected: PASS if Task 2's transcription was faithful. **If it fails, the transcription
dropped content — restore it from spec §6 rather than relaxing the assertion.**

- [ ] **Step 3: Commit**

```bash
git add ui/overlay/settings_schema_test.go
git commit -m "test(settings): pin the long-form help that moved into detail"
```

---

## Task 8: Adapt the five copy-dependent existing tests

**Files:**
- Modify: `ui/overlay/settings_test.go` (five sites)

**Interfaces:**
- Consumes: everything above.
- Produces: a green `./ui/...` and `./app/...`.

These five tests fail as of Task 2 Step 6 because they pin copy this PR rewrites. Each
adaptation is named below; **do not delete a test to make the suite green.**

- [ ] **Step 1: Fix `maxSessionsRow`'s label (line ~373)**

The helper greps the rendered line for `"Max sessions"`, now `"Session limit"`:

```go
// maxSessionsRow returns the rendered "Session limit" row line (not the description
// line), for the tri-state display assertions below.
func maxSessionsRow(t *testing.T, o *SettingsOverlay) string {
	t.Helper()
	o.SetSize(80, 40)
	for _, line := range strings.Split(stripANSI(o.Render()), "\n") {
		if strings.Contains(line, "Session limit") {
			return line
		}
	}
	t.Fatal("no \"Session limit\" row in the render")
	return ""
}
```

- [ ] **Step 2: Fix `TestSettingsOverlay_RenderSmoke`'s section names (line ~472)**

It asserts the old `"General"`, `"Appearance"`, `"Behavior"`. Assert the new taxonomy
instead — and derive it from `allCategories()` so the test cannot drift from the
vocabulary:

```go
func TestSettingsOverlay_RenderSmoke(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	o.SetSize(80, 40)
	out := stripANSI(o.Render())

	assert.Contains(t, out, "Settings")
	assert.Contains(t, out, "Theme")
	assert.Contains(t, out, "esc close")
	// Every category must reach the render as a section header. The panel windows its
	// body, so use a terminal tall enough for all of them.
	o.SetSize(80, 80)
	tall := stripANSI(o.Render())
	for _, c := range allCategories() {
		assert.Containsf(t, tall, c.label(), "category %q has no section header", c.label())
	}
}
```

- [ ] **Step 3: Retarget `TestSettingsOverlay_LongDescriptionShownInFull` (line ~500)**

It asserts the rendered footer contains `"an account boundary is refused"` — prose that is
now `group_mode`'s **detail**, which PR A does not render. The *mechanism* it guards
(a help string longer than the inner width wraps and is shown in full rather than clipped
to one line) is still live, because a 74-cell summary exceeds the inner width on a narrow
terminal.

Retarget it at that: pick the longest summary, render at a narrow width, and assert its
tail survives. Task 7 now covers the moved phrase, so nothing is lost.

```go
// TestSettingsOverlay_LongSummaryShownInFull pins that a summary too wide for the box
// wraps and is shown in full rather than clipped to one line with an ellipsis. The
// phrase asserted on is the summary's tail, which wraps onto its own line — its
// presence proves the text reached the end.
//
// (Before the summary/detail split this test used group_mode's 443-char description;
// that prose now lives in detail, which PR B renders behind `?`. The phrase itself is
// pinned by TestDetailRetainsTheMovedProse.)
func TestSettingsOverlay_LongSummaryShownInFull(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	o.SetSize(50, 40) // narrow box, tall terminal: the summary must wrap, not be capped
	settingsAt(t, o, "max_sessions")
	out := stripANSI(o.Render())

	assert.Contains(t, out, "host", "the summary's tail must survive wrapping")
	assert.Contains(t, out, "esc close", "the key hint stays visible")
}
```

Verify the asserted tail word actually lands on a wrapped line at width 50 by printing
the render once while iterating; adjust the word to whatever the summary's final line
holds. An assertion on a word that happens to appear on the *first* line proves nothing.

- [ ] **Step 4: Check the two short-terminal footer tests (lines ~539, ~555)**

`TestSettingsOverlay_LongDescriptionCapsWithEllipsis` and
`TestSettingsOverlay_FooterCutLineStaysWithinInner` pin `renderFooter`'s
`maxDescLines` cap. With summaries at most 74 cells the cap may no longer bite at the
sizes those tests use, which would make them **vacuously green** — they would pass
whether or not the cap works.

For each: run it, then confirm it still exercises the cap by asserting the ellipsis is
present. If a test no longer reaches the capping branch, shrink its terminal (e.g.
`SetSize(40, 12)`) until it does, and leave a comment recording the size the cap needs.
If no size reaches it, mark the test with a comment saying the cap is unreachable under
the summary budget and that PR B's fixed-height help pane replaces the mechanism — but
only after checking, not by assumption.

- [ ] **Step 5: Run the whole affected surface**

Run:
```bash
PATH="/home/zvi/.local/share/mise/installs/go/1.26.4/bin:$PATH" go test ./ui/... ./app/... ./config/... 2>&1 | tail -20
```
Expected: all PASS. `app/settings_test.go` locates rows via `SelectRow(key)` and keys are
unchanged, so it should need no edits — if it fails, a key was renamed by mistake.

- [ ] **Step 6: Commit**

```bash
git add ui/overlay/settings_test.go
git commit -m "test(settings): adapt the copy-dependent assertions to the new taxonomy"
```

---

## Task 9: Document the taxonomy in the README

**Files:**
- Modify: `README.md` (the `#### Configuration reference` table and its preamble)

**Interfaces:**
- Consumes: the final taxonomy.
- Produces: docs consistent with the panel.

`config.TestReadmeDocumentsEveryConfigField` only requires the section to contain
`` `json_name` `` for every field, so adding a column is safe. Verify, do not assume.

- [ ] **Step 1: Add a Category column**

Change the table header to `| Key | Category | Type | Default | Notes |` and fill each
row's category from spec §4. Keep every existing Key, Type, Default and Notes cell
exactly as it is — this step adds a column, it does not rewrite the reference.

For the five keys with no panel row, use `—` and let the existing Notes explain:
`profiles`, `claude_accounts`, `gh_accounts`, `agy_accounts`, `nerd_font`.

- [ ] **Step 2: Update the preamble**

The paragraph above the table says the panel is "one-value-per-row" and names the
exceptions. It stays accurate, but add one sentence naming the taxonomy so a reader can
map the table to the panel:

```markdown
The panel groups these keys into ten categories — Sessions, Worktrees & git,
Appearance, Session list, Notifications, Automation, Input, Projects, Updates, and
Advanced — shown in the Category column below.
```

- [ ] **Step 3: Verify the drift tests still pass**

Run:
```bash
PATH="/home/zvi/.local/share/mise/installs/go/1.26.4/bin:$PATH" go test ./config/... ./keys/... -v -run 'Readme' 2>&1 | tail -20
```
Expected: PASS, including `TestReadmeDocumentsEveryConfigField` and the `keys` README
drift test (untouched — this PR adds no keybindings).

- [ ] **Step 4: Commit**

```bash
git add README.md
git commit -m "docs: group the configuration reference by panel category"
```

---

## Task 10: Full verification and the manual eyeball

**Files:** none — verification only.

- [ ] **Step 1: Run the local gate**

Run:
```bash
mise exec -- just ci 2>&1 | tail -30
```
Expected: `build vet fmt-check lint test cover` all green. If `lint` dies with
`not found` (exit 127), `golangci-lint` is not on `PATH` — invoke it directly as in the
Global Constraints and run the rest via `just`.

- [ ] **Step 2: Run the race detector**

Run:
```bash
mise exec -- just test-race 2>&1 | tail -20
```
Expected: PASS. This is CI-only otherwise, and this PR touches a struct read from the
render path.

- [ ] **Step 3: Eyeball the real panel**

Tests cannot show section balance or how the footer reads. Drive the real binary in full
isolation (an isolated `HOME` alone does **not** isolate Atrium's tmux socket — throwaway
sessions would land on the developer's live server):

```bash
S=/tmp/atrpra; rm -rf $S; mkdir -p $S/home/.atrium $S/tmux $S/repo
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

Confirm by eye: ten section headers in spec §4's order; the Advanced section ends with a
`Config file` row showing a real path; the footer shows a one-line summary; and
`affects new sessions` / `applies on restart` appear only on the twelve rows that carry
them. Walk to the bottom (`Down` ×40) and confirm no row lost its label.

Re-export `TMUX_TMPDIR` in every shell — a new shell per command otherwise reports "no
server running".

- [ ] **Step 4: Tear down**

```bash
export TMUX_TMPDIR=/tmp/atrpra/tmux
tmux -L eyeball kill-server 2>/dev/null
rm -rf /tmp/atrpra
tmux -L atrium list-sessions | head -3   # the developer's live server must be untouched
```

- [ ] **Step 5: Open the PR**

```bash
git push -u origin HEAD
gh pr create --title "feat(settings): ten-category taxonomy and rewritten help copy" --body "$(cat <<'EOF'
PR A of the configuration panel redesign
(`docs/superpowers/specs/2026-07-25-configuration-panel-design.md`).

The settings panel was one column of 37 rows in three sections, 24 of them sharing the
label "Behavior". This lands the taxonomy and the copy under the **existing**
single-column renderer, so there is no layout change to review: ten categories, a
one-line summary per row with the long-form help moved into `detail`, and a closed
apply-timing enum replacing the free-text `applyNote`.

It also adds the mechanisms PR B renders — `defaultDisplay`/`reset` (what you changed,
and how to change it back) and `activeWhen` (which rows currently do nothing) — fully
tested but not yet drawn.

Guards worth reviewing: every scalar config field has exactly one row; nothing is marked
modified on a fresh `DefaultConfig()`; every enum option carries a gloss; and the
long-form prose that moved into `detail` is pinned by phrase, because PR A does not
render `detail` and losing a line in transcription would otherwise be invisible.

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```

- [ ] **Step 6: Read CI before calling it done**

Run:
```bash
gh pr checks --required
```
`gh pr checks` has no `--json` flag — a poll loop using one fails every iteration and
times out silently. Expected: all required checks pass, including the macOS job (which
exercises `TestOOMMarginIsLinuxOnly`'s non-Linux branch).

---

## Self-Review

**Spec coverage.** Spec §4 taxonomy → Tasks 2, 4 (`TestCategoryRowCounts`), 9. §5 schema,
timings, `defaultDisplay` nil-rows, `activeWhen` table → Tasks 1, 2, 5, 6. §6 copy →
Task 2 (transcription), 4 (bounds/glosses), 7 (retention). §11 file split → Task 2 Step 1.
§13 guards 1–3 → Tasks 3, 4, 5. §13 guard 13 (existing tests) → Task 8.

**Deferred to later PRs, by design:** guards 4–7 and 10–11 (rail rendering, the
fixed-height help pane, no-overflow, single-pane fallback, `OpenAt`) are PR B's; guards
8–9 (`r`, search) are PR C's; guard 12 (profiles) is PR D's. The three rail entries PR B
adds (All settings, Profiles, Accounts) are reserved by
`TestCategoryCountFitsTheRailBudget`'s `nonScalarRailEntries` so A cannot spend their
budget.

**One place this plan deliberately does not pre-decide**, because the answer must come from
the code rather than from me: the `group_mode` inert predicate (Task 6 Step 1). It is
called out at the step that needs it, with the decision criteria and the instruction to
follow `ui.List` rather than this plan. Guessing it would have shipped a false reason chip
behind a green test.

**One error this review caught.** The spec claimed the 74-cell summary bound was "one
unwrapped line at the 80-column floor (`boxWidth(78) − 4 = 74`)". It is not: `boxWidth` is
`min(64, width−2)`, so the inner width at width 80 is **60**, and 74-cell summaries wrap to
two footer lines under PR A's renderer. The bound is still right — it matches PR B's
widened `min(96, width−2)` box — but the arithmetic justifying it was wrong. Both documents
are corrected, and Task 4 now states the reasoning instead of asking the implementer to
rediscover it.

**Copy transcription** is a step that reads from spec §6 rather than inlining 37 strings
twice. That is a considered trade: duplicating the copy here would create a second source
of truth that can drift from the reviewed one, and Tasks 4 and 7 make an omission or an
over-long summary fail the build. A *paraphrase* would still pass — hence the Global
Constraint forbidding it, and the instruction to report suspect claims rather than reword
them.
