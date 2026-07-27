# Configuration panel redesign — PR D: the Profiles editor

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

## Context

PR A (#482) landed the taxonomy and copy, PR B (#491) the two-pane renderer and the
visibility layer, PR C (#494) search, `r` reset, the `OpenAt` deep links and the Accounts
handoff. One rail entry is still a dead end:

```
  Profiles              │  Agent profiles are edited in config.json, under the profiles key.
```

That note is spec **D11** stated as a fact — `profiles` is the one configuration surface with
**no TUI at all**. A user who installs a new agent, or wants a second launch command with
different flags, must leave the app and hand-edit JSON. `atrium profiles detect` exists, but it
only *appends*; it cannot rename, retarget or remove anything.

**Goal:** ship spec §9 — a record editor over `config.Profiles` in that rail slot — satisfying
guard 12: *"Profiles: new/edit/delete/detect round-trip through `config.SaveConfig`; deleting
the profile named by `default_program` is guarded; a raw `default_program` command survives a
profiles edit."* After this the four-stage series is complete.

**Architecture.** Profiles becomes a **fourth `railKind`** (`railProfiles`), not an eleventh
`settingCategory` and not a handoff. It owns a focusable pane like a category, but that pane is
over `cfg.Profiles` rather than over `s.rows` — so `rowRange` still reports zero rows for it and
`newSettingRows` is untouched, which is what keeps
`TestEveryScalarConfigFieldHasARow`'s `profiles` exemption true. The pane reuses the panel's own
line geometry (`composeRowLine`, `windowPane`, the help pane, the hint ladder) rather than
importing the accounts overlay's; the *form* follows `accountForm.go`'s pattern (one struct of
`textinput.Model`s, tab-cycled focus, validate-then-commit on the overlay, `lastErr` rendered by
the existing help pane). Mutations report the changed key `"profiles"` through the existing
`(closed, changedKey)` return, so `applySettingChange` stays the panel's only writer.

**Tech Stack:** Go 1.26 toolchain over `go.mod`'s 1.25, Bubble Tea, `bubbles/textinput`,
lipgloss, `charmbracelet/x/ansi`, testify. Design record:
`docs/superpowers/specs/2026-07-25-configuration-panel-design.md` (below: "the spec").
Predecessors: `docs/superpowers/plans/2026-07-25-configuration-panel-pr-a.md` (#482),
`.../2026-07-26-configuration-panel-pr-b.md` (#491), `.../2026-07-27-configuration-panel-pr-c.md`
(#494, below: "PR C's plan").

---

## Global Constraints

- **Branch off fresh main.** `origin/main` is `124b227`; this worktree is already there and
  clean, on `zvi/config-d`. Do not create another branch — Atrium owns this worktree.
- **Toolchain is mise-managed and not on `PATH`.** Inner loop:
  `mise exec -- just test`, or
  `PATH="/home/zvi/.local/share/mise/installs/go/1.26.4/bin:$PATH" go test ./ui/overlay/`.
  Gate: `mise exec -- just ci`. Lint (post-#493 the recipe keys its cache per worktree, but
  `golangci-lint` still is not on mise's `PATH`):
  `PATH="/home/zvi/go/bin:$PATH" mise exec -- just lint ./ui/... ./app/...`.
- **`unused` is the linter that bites this repo.** Every helper this plan adds is read by a test
  in the same task. If lint flags one, a test is missing — do not delete the helper. Put
  test-only helpers (e.g. `profilesRailIndex`) in `_test.go`.
- **`revive` rules CI enforces that `go vet` does not:** `exported` (every exported symbol needs
  a doc comment starting with its own name — this PR adds `TakeProfileDetect` and
  `NoteProfilesDetected`) and `redefines-builtin-id` (never name anything `max`, `min`, `len`).
- **Measure unstyled width.** A bordered lipgloss block pads every line to the same width, so
  `lipgloss.Width(rendered) <= boxWidth` is a tautology that can never fail. Width assertions go
  on `rowLineParts.plain()` and on `stripANSI(paneLine.text)`, never on `Render()`'s output.
- **A renderer guard that names one terminal size is testing that size, not the renderer.** Every
  new visibility guard here sweeps widths, as `TestEveryHandoffNoteFitsItsPane` does.
- **`composeRowLine` bounds its own output and `hintLine` ends in `ansi.Truncate`** — a
  `width <= paneW` assertion on either is a tautology no bug can trip. Assert `== paneW` plus a
  presence half, and assert `NotContains "…"` on hints.
- **No new `keys` registry entries.** Panel-internal keys are handled on `msg.String()`
  (spec §7), so the registry/help/README key-drift guards stay untouched. `n`, `e`, `d`, `D` are
  panel-internal exactly as `r`, `/` and `?` are.
- **No new glyphs.** The `default` badge is a word in the badge column, not a theme glyph, so
  none of `Glyphs`/`registry.go`/`assertGlyphWidths`/`TestGlyphsForFidelityRungs` moves.
- **Do not add a `settingRow`, do not touch `settings_schema.go`'s copy, and do not edit
  `TestEveryScalarConfigFieldHasARow`'s exempt map.** The editor is not a settingRow; that
  exemption stays valid. If you find yourself editing that map, the design has gone wrong.
- **Tests stay hermetic** (`HOME` to a temp dir; `app` and `config` do it in `TestMain`).
- **Conventional Commits, lowercase.** `feat:` / `fix:` / `refactor:` / `test:` / `docs:`.
- **Never `git checkout <file>` to undo a mutation** — it reverts to HEAD and takes the task's
  real edits with it. This has destroyed work in three prior stages. Edit the mutated line back.
- **This worktree auto-`git add -N`s new files**, so untracked files show as `A`/`D` and
  `git add -u` can stage a scratch file. Run `git status --short` before every commit and stage
  paths explicitly.

---

## Decisions taken before this plan (do not relitigate)

| Decision | Why |
|---|---|
| **A fourth `railKind`, `railProfiles`** — not an 11th `settingCategory`, not a `railHandoff`. | A category means `settingRow`s, which a list of records cannot be (`TestEveryRowKeyIsAConfigFieldOrReadOnly` would reject a synthetic `profiles.0.program` key, and `TestCategoryCountFitsTheRailBudget` has **zero** headroom at 13). A handoff means no focusable pane. The editor is neither. Entry **count stays 13** and `nonScalarRailEntries` stays **3**. |
| **Delete is refused when the profile is the one `default_program` names** — not repointed. | Spec §9 says pick one. `default_program` lives in another category, so a silent repoint changes what every new session launches from a pane that cannot show the change; the panel's grammar elsewhere is to refuse rather than echo back a value the accessor ignores (`project_search_depth`'s clamp refusal). The message leads with the setting's own label so truncation cannot eat it. |
| **A rename of that profile *does* carry `default_program` with it.** | The asymmetry has a crisp reason: a rename keeps the record the default names, so following it preserves exactly what launches. A delete removes the record and has no successor that preserves anything — falling through to `GetProgram`'s raw-command path would silently launch a different command. |
| **`d` asks for confirmation (`y` / `n`), diverging from spec §9's bare key list.** | Deleting a record is the **first irreversible action in this panel** — `r` restores a default, an enum cycle is reversible, this is not — and the sibling record editor over the same config file (`accounts.go`'s `modeConfirmDelete`) already confirms. Two record editors in one app where `d` means "gone" in one and "are you sure" in the other is the drift a user feels. Recorded as a "Resolved in PR D" callout in the spec. |
| **Profiles are not searchable by `/`.** | `searchResults` walks `s.rows`; the haystack is label + key + summary + category, i.e. the *setting schema*. A profile is data, not a setting. `/` from the Profiles pane opens the ordinary settings search and moves the rail off Profiles, exactly as `/` from a handoff entry does today. Stated in the spec callout so the omission is a decision rather than an oversight. |
| **`D` runs detection through `home` as a `tea.Cmd`, not inline.** | `config.DetectAgentProfiles` probes claude through `config.GetClaudeCommand`, which **spawns a login shell sourcing the user's rc file under a 10-second timeout** (`config/detect.go:23-46`). A synchronous call freezes the update loop — and with it every session's poll — for as long as that takes. It reuses `app`'s existing `detectAgents` seam (`app/app_welcome.go:15`), which is already the one the startup agent check runs, so the TUI and `atrium profiles detect` cannot probe differently. The merge half stays `(*config.Config).MergeDetectedProfiles`. |
| **`m.program` is re-resolved on `default_program` and `profiles` changes.** | Pre-existing gap this PR closes because the editor makes it easiest to reach: `m.program` is set once at launch from `cfg.GetProgram()` and is the create form's fallback launch command whenever there is no variant picker (0 or 1 profiles). Editing the default profile's command must not leave the form launching the previous one. |
| **No `OpenAt("profiles")` deep link, and no change to the agent-check notice.** | They are one change, not two: the `agentCheckDoneMsg` notice ("Run `atrium profiles detect` to add it") is the natural first customer, but converting it to a `,`-advertising `settingNotice` drags in `pendingAgentNotice`'s held-over flush semantics and `TestEveryCommaNoticeGoesThroughSettingNotice`. Spec §12 already lists further call sites as follow-up. Proposed together after merge. |

---

## Derived numbers

Measured from the tree at `124b227`, not estimated. Where a number is a guard, the test that
pins it is named.

| Quantity | Value | Note |
|---|---|---|
| Rail entries | **13**, unchanged | `railProfiles` replaces a `railHandoff` in slot 11; `TestCategoryCountFitsTheRailBudget`'s `nonScalarRailEntries = 3` is unchanged and needs no edit |
| `railWidth()` | **19** = 2 + 15 + 2 | driven by the longest *label* (`Worktrees & git`); "Profiles" is 8, so the rail and with it `twoPaneMinInner()` do not move |
| Handoff entries after this PR | **1** (Accounts) | three `require.Equal(t, 2, …)` liveness counters become 1 |
| Entries owning `settingRow`s | **11** (All settings + 10 categories) | unchanged |
| Entries owning a focusable pane | **12** (everything but Accounts) | the new counter `TestRailHintNeverPromisesAPaneSwapWithoutRows` needs |
| `paneHeight()` floor | **3** (`settingsMinBody`) | so the record form's fixed **3** lines (heading + 2 fields) fit every geometry the panel supports — no shedding ladder is needed |
| Rows pane at width 80 / 100 | **52 / 70** | `innerWidth − railWidth − paneDividerCells` |
| `profileDefaultBadge` | `"default"`, 7 cells | dropped first by `composeRowLine` when narrow, per spec §10; `profilesContextLine` is its fallback sentence |
| Form input width | `paneW − 3 − 7 − 2 − 1` = **39** at 80 columns | `rowMarkerCells` + `len("Program")` + `rowLabelGap`, **plus one for the cursor cell**: `textinput.Model.View()` renders `Width + 1` on both the value and the placeholder path (measured against `bubbles@v0.20.0`), so a naive subtraction overflows the pane at every geometry |
| Two-pane threshold | `twoPaneMinInner` = **67**, i.e. two panes at **w ≥ 73** | `railWidth 19 + paneDividerCells 3 + minRowsPaneWidth 45`; below it the rail is off screen entirely |

**Hint ladders, to be re-measured in Task 2 Step 6** (all runes are width 1; inner width is 74 at
the 80-column floor and 92 at 100 columns):

| Focus | Rung | Cells |
|---|---|---|
| rail (Profiles) | `↑/↓ category · → profiles · / search · ⇥ pane · esc close` | 57 |
| profiles pane | `↑/↓ move · n new · ↵ edit · d delete · D detect · / search · ⇥ pane · esc back` | 78 |
| profiles pane | `↑/↓ move · n new · ↵ edit · d delete · D detect · / search · esc back` | 69 |
| profiles pane | `↑/↓ · n new · ↵ edit · d delete · D detect · esc back` | 53 |
| profiles pane | `n new · ↵ edit · d delete · esc back` | 36 |
| profiles pane | `esc back` | 8 |
| form | `⇥ field · ↵ save · esc cancel` | 29 |
| confirm | `y delete · n cancel · esc cancel` | 32 |

So at 100 columns the pane shows rung 0 and at 80 it shows rung 1 — `/ search` survives the
80-column floor because `⇥ pane` yields before it. **Esc is now advertised at four levels**:
`esc cancel` in the form and the confirm, `esc back` in the pane, `esc close` on the rail — the
extension spec §15 requires instead of a static string.

---

## File Structure

| File | Responsibility in this PR |
|---|---|
| **Create** `ui/overlay/settings_profiles.go` | the whole editor: `profileForm`, its key handling, `validateProfile`/`commitProfile`/`armProfileDelete`/`clampProfileCursor`, detection request/apply, `profilesPaneContent`, `profileFormLines`, `profilesHelp`, `profilesContextLine`, `profilesHintLadder` |
| **Create** `ui/overlay/settings_profiles_test.go` | every behavior and render guard for the editor |
| **Create** `app/app_profiles.go` | `profilesDetectedMsg`, `detectProfilesCmd` |
| **Modify** `ui/overlay/settings_nav.go` | `railProfiles` kind; the Profiles `railEntry`; `handleRailKey`'s forward arm; `resetProfileTransients` from `syncCursorToRail`; `rowRange`'s comment |
| **Modify** `ui/overlay/settings.go` | six editor fields; `HandleKeyPress`'s new arm; `OpenAt` drops the editor's transient state |
| **Modify** `ui/overlay/settings_render.go` | `rowsPaneContent`'s dispatch; `paneCursor`; `wrappedPaneLines` + `rightAligned` extractions; `helpLines`/`contextLine`/`hintLine` arms; `railHintLadder`'s `railProfiles` case |
| **Modify** `ui/overlay/settings_nav_test.go` | the six drift guards restated |
| **Modify** `ui/overlay/settings_render_test.go` | the handoff-note counters |
| **Modify** `ui/overlay/settings_test.go` | `TestSettingsOverlay_RawDefaultProgramSurvivesCycle`'s new sibling |
| **Modify** `app/app_keys.go` | `TakeProfileDetect` in `handleSettingsState` |
| **Modify** `app/app_update.go` | the `profilesDetectedMsg` case |
| **Modify** `app/app_layout.go` | `applySettingChange`'s `"default_program", "profiles"` case |
| **Modify** `app/settings_test.go` | the two app-level round trips |
| **Modify** `README.md` | the configuration-reference exception prose + `profiles`' Category cell + the Profiles section |
| **Modify** `docs/superpowers/specs/2026-07-25-configuration-panel-design.md` | the §9 "Resolved in PR D" callout, and §14's mis-credited guard 10 |
| **Create** `docs/superpowers/plans/2026-07-28-configuration-panel-pr-d.md` | this plan, shipped in the PR as A, B and C all did |

---

## Task 1: Ship the plan, and have it torn apart first

**Files:** Create `docs/superpowers/plans/2026-07-28-configuration-panel-pr-d.md`.

- [ ] **Step 1: Copy this plan into the repo**

Write this document verbatim to `docs/superpowers/plans/2026-07-28-configuration-panel-pr-d.md`.

- [ ] **Step 2: Adversarial review, before a line of code**

Dispatch **three independent reviewers**, each reading the plan **against the tree** rather than
against itself. All three prior stages did this and all three times it changed the design, not
just a test — PR B's reviewers found three real rendering bugs and PR C's found two defects that
would otherwise have shipped.

Give each one this plan, the spec, and one lens:

1. **Does the prescribed code compile and do what its comment says?** Apply the production
   snippets to a throwaway copy of the tree and run `go build`, the `ui/overlay` and `app`
   suites, and `golangci-lint`. Check every field, method and constant named in a snippet:
   `padRight`'s behavior on a *styled* string, `wrapIndex`'s signature, `newFieldInput`'s
   visibility from `settings_profiles.go`, `theme.Current().Glyphs.SelectionMark`,
   `clamp`/`max` availability, and whether `textinput.Model.View()` honours `Width` the way
   `profileFormLines` assumes.
2. **Would each prescribed test pass, and does it test what its name claims?** Run the
   arithmetic on every hint-ladder cell count and every width sweep. Check each `require`
   precondition is reachable with the fixture given — in particular that
   `config.DefaultConfig()` has `Profiles == nil`, so any test that does not seed profiles is
   exercising the **empty** pane. Look for assertions that hold whether or not the feature works.
3. **What did the plan not think about?** Interactions: the editor plus the single-pane fallback
   below 73 columns; the editor plus `?`; the editor plus `/` (including a zero-result filter);
   a `D` result landing after the rail moved, after the panel closed, or after the same profile
   was deleted by hand; `OpenAt` into a panel with the form open; `SetSize` while the form is
   open; `windowPane`'s cursor matching with two different index spaces; and whether
   `TestOpenAtLandsOnEveryRowWithTheRowsPaneFocused` still holds.

Fold every real finding into the plan **before** Task 2, and record in the plan's "What the
adversarial review changed" section what changed and why.

- [ ] **Step 3: Commit**

```bash
git status --short
git add docs/superpowers/plans/2026-07-28-configuration-panel-pr-d.md
git commit -m "docs: pr d plan for the configuration panel redesign"
```

---

## Task 2: `railProfiles` — the fourth kind, and the read-only pane

**Files:**
- Modify: `ui/overlay/settings_nav.go` (`railKind`, `railEntries`, `rowRange`'s comment,
  `handleRailKey`, `syncCursorToRail`)
- Modify: `ui/overlay/settings.go` (the editor's fields, `HandleKeyPress`, `OpenAt`)
- Modify: `ui/overlay/settings_render.go` (`rowsPaneContent`, `paneCursor`, `wrappedPaneLines`,
  `helpLines`, `contextLine`, `hintLine`, `railHintLadder`)
- Create: `ui/overlay/settings_profiles.go`
- Create: `ui/overlay/settings_profiles_test.go`
- Modify: `ui/overlay/settings_nav_test.go`, `ui/overlay/settings_render_test.go`

**Interfaces:**
- Produces, consumed by Tasks 3–6:
  - `railProfiles railKind`
  - `(*SettingsOverlay).profilesPaneActive() bool`
  - `(*SettingsOverlay).profileCursor int`, `profileForm *profileForm`, `profileConfirm bool`,
    `profileNote string`, `profileDetecting bool`, `profileDetectPending bool`
  - `(*SettingsOverlay).handleProfilesKey(tea.KeyMsg) (closed bool, changedKey string)`
  - `(*SettingsOverlay).profilesHelp() (prose string, danger bool)`
  - `(*SettingsOverlay).clampProfileCursor()`
  - `wrappedPaneLines(text string, width int, style lipgloss.Style) []paneLine`
  - `rightAligned(body, pos string, width int) string`
  - `const profilesChangedKey = "profiles"`

This task lands the kind, the pane that lists profiles, the help pane, the geometry, and the six
drift guards. It binds **navigation only** — `↑/↓`, `esc`, `tab`, `/`. `n`/`e`/`d`/`D` and their
hint rungs arrive with the code that makes them work, because PR C's ordering lesson was that no
commit should ever advertise a dead key.

- [ ] **Step 1: Write the failing tests**

Create `ui/overlay/settings_profiles_test.go`:

```go
package overlay

import (
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/config"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// profilesRailIndex is the rail index of the Profiles editor, derived rather than counted from
// the end of railEntries() so a future entry cannot silently move every test that lands there.
func profilesRailIndex() int {
	for i, e := range railEntries() {
		if e.kind == railProfiles {
			return i
		}
	}
	return -1
}

// profilesCfg builds a config whose profile list and default_program are exactly as given.
// config.DefaultConfig() leaves Profiles nil (TestDefaultConfigDoesNotProbe pins that it never
// probes), so a test that does not call this is exercising the EMPTY pane.
func profilesCfg(defaultProgram string, profiles ...config.Profile) *config.Config {
	cfg := config.DefaultConfig()
	cfg.DefaultProgram = defaultProgram
	cfg.Profiles = profiles
	return cfg
}

// threeProfiles is the standard fixture: three records, two of them holding a hand-written
// command with flags, and default_program naming the first.
//
// The default record's NAME and PROGRAM deliberately differ. cfg.DefaultProgram and
// cfg.GetProgram() are interchangeable whenever they coincide, and the delete guard must
// compare against the pointer rather than the resolved command — a fixture where both are
// "claude" cannot tell those apart, so the guard's mutation would come back negative.
func threeProfiles() *config.Config {
	return profilesCfg("claude",
		config.Profile{Name: "claude", Program: "claude --model opus"},
		config.Profile{Name: "aider", Program: "aider --model ollama_chat/gemma3:1b"},
		config.Profile{Name: "codex", Program: "codex"},
	)
}

// profilesAt opens the panel on the Profiles editor with its pane focused, which is the state
// every editor test starts from.
//
// The explicit focusRail is load-bearing, not tidiness. settingsAt goes through OpenAt, which
// sets focusRows — so a test that parks the cursor on a row first and THEN calls this would
// send its Enter to the editor's own key handler rather than to handleRailKey, opening the edit
// form on profile 0. The require below would still pass, because focus is already focusRows.
// That silent divergence broke three tests before it was found by running them.
func profilesAt(t *testing.T, o *SettingsOverlay) {
	t.Helper()
	require.GreaterOrEqual(t, profilesRailIndex(), 0, "the rail must have a Profiles editor")
	o.focus = focusRail
	o.SetRailIndex(profilesRailIndex())
	require.Equal(t, railProfiles, o.selectedEntry().kind)
	_, _ = o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEnter})
	require.Equal(t, focusRows, o.focus, "the forward key must focus the editor's pane")
	require.Nil(t, o.profileForm, "landing on the editor must not open a form")
}

// paneText renders the rows pane and returns its plain lines, which is what width and content
// assertions must measure: Render() pads every line to the box width, so asserting on it is a
// tautology that cannot fail.
func paneText(o *SettingsOverlay) []string {
	out := []string{}
	for _, l := range o.rowsPaneContent(o.rowsPaneWidth()) {
		out = append(out, stripANSI(l.text))
	}
	return out
}

// TestProfilesEntryFocusesItsEditor replaces TestProfilesEntryStaysANoOp, which pinned the
// deliberate PR C asymmetry that Enter on Profiles did nothing. All three forward keys now
// focus the editor's pane, and none of them closes the panel or asks home for a handoff — the
// editor lives inside the panel, unlike Accounts.
func TestProfilesEntryFocusesItsEditor(t *testing.T) {
	for _, key := range []tea.KeyMsg{
		{Type: tea.KeyEnter}, {Type: tea.KeyRight}, {Type: tea.KeyTab},
	} {
		o := NewSettingsOverlay(threeProfiles())
		o.SetRailIndex(profilesRailIndex())
		require.Equal(t, focusRail, o.focus)

		closed, changed := o.HandleKeyPress(key)
		assert.Falsef(t, closed, "%v must not close the panel", key)
		assert.Emptyf(t, changed, "%v changes no setting", key)
		assert.Equal(t, HandoffNone, o.Handoff(), "the editor is not a handoff")
		assert.Equalf(t, focusRows, o.focus, "%v must focus the editor", key)
	}
}

// TestProfilesPaneListsEveryProfile is the pane's contract: one line per record, name in the
// label column and program in the value column, with the default marked.
func TestProfilesPaneListsEveryProfile(t *testing.T) {
	o := NewSettingsOverlay(threeProfiles())
	o.SetSize(100, 32)
	profilesAt(t, o)

	lines := paneText(o)
	require.Len(t, lines, 3, "one line per profile, no headers and no spacers")
	for i, want := range []string{"claude", "aider", "codex"} {
		assert.Containsf(t, lines[i], want, "line %d must name its profile", i)
	}
	assert.Contains(t, lines[1], "aider --model ollama_chat/gemma3:1b",
		"the program is the value column")
	assert.Contains(t, lines[0], profileDefaultBadge,
		"the profile default_program names carries the default badge")
	assert.NotContains(t, lines[1], profileDefaultBadge)
	assert.NotContains(t, lines[2], profileDefaultBadge)
}

// TestEmptyProfilesPaneNamesTheWayOut: config.DefaultConfig() has no profiles at all, which is
// the state a fresh install with no detected agent lands in. An empty pane reads as a broken
// panel — the same obligation a handoff note carries — so it must name both keys that fill it
// and the help pane must explain what runs meanwhile.
func TestEmptyProfilesPaneNamesTheWayOut(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	o.SetSize(100, 32)
	require.Empty(t, o.cfg.Profiles, "precondition: DefaultConfig declares no profiles")
	profilesAt(t, o)

	pane := strings.Join(paneText(o), " ")
	assert.Contains(t, pane, "press n to add one",
		"the empty pane must name the key that adds a profile")
	assert.Contains(t, pane, "D to detect", "and the key that detects installed agents")

	prose, danger := o.profilesHelp()
	assert.False(t, danger)
	assert.Contains(t, prose, "Default program",
		"with no profiles, default_program IS the launch command — say so")
}

// TestProfilesPaneNeverDescribesAnUnrelatedRow is the trap this pane inherits: s.cursor still
// points at whatever settingRow it last sat on, and selectedRow() is unguarded. Describing that
// row in the help pane would put an unrelated setting's summary under a list of profiles — the
// same lie railHandoff's blank prose avoids.
func TestProfilesPaneNeverDescribesAnUnrelatedRow(t *testing.T) {
	o := NewSettingsOverlay(threeProfiles())
	o.SetSize(100, 32)
	settingsAt(t, o, "theme") // park s.cursor on a real row first
	summary := o.selectedRow().summary
	require.NotEmpty(t, summary)
	profilesAt(t, o)

	help := stripANSI(strings.Join(o.helpLines(), " "))
	assert.NotContains(t, help, summary, "the help pane must not describe s.cursor's row here")
	assert.Contains(t, help, "claude", "it describes the highlighted profile instead")
}

// TestProfilesHelpShowsTheProgramInFull: the row line truncates a long program with a tail
// ellipsis, and spec §10 requires the full value to reappear in the help pane rather than being
// lost. Asserted at the tightest two-pane geometry, where the truncation actually bites.
func TestProfilesHelpShowsTheProgramInFull(t *testing.T) {
	long := "aider --model ollama_chat/gemma3:1b --no-auto-commits --dark-mode"
	o := NewSettingsOverlay(profilesCfg("claude", config.Profile{Name: "aider", Program: long}))
	o.SetSize(80, 24)
	profilesAt(t, o)

	require.Contains(t, paneText(o)[0], "…", "precondition: this geometry truncates the program")
	prose, _ := o.profilesHelp()
	assert.Equal(t, long, prose, "the truncated value must be recoverable from the help pane")
}

// TestProfilesContextLineCountsProfiles: the position readout must count the profile list, not
// whatever category the rail last marked — "2/3" has to mean the second of three profiles.
func TestProfilesContextLineCountsProfiles(t *testing.T) {
	o := NewSettingsOverlay(threeProfiles())
	o.SetSize(100, 32)
	profilesAt(t, o)
	_, _ = o.HandleKeyPress(keyRunes("j"))

	ctx := stripANSI(o.contextLine(o.innerWidth()))
	assert.Contains(t, ctx, "2/3")
	assert.NotContains(t, ctx, "Default program launches",
		"aider is not the default, so the badge's fallback sentence stays off")

	_, _ = o.HandleKeyPress(keyRunes("k"))
	assert.Contains(t, stripANSI(o.contextLine(o.innerWidth())), "Default program launches this profile.",
		"the sentence behind the badge, for the width where the badge was dropped")
}

// TestProfileCursorIsBoundedByTheList pins that j/k cannot walk off either end — the cursor
// indexes cfg.Profiles, and an out-of-range value panics the very next render.
func TestProfileCursorIsBoundedByTheList(t *testing.T) {
	o := NewSettingsOverlay(threeProfiles())
	o.SetSize(100, 32)
	profilesAt(t, o)

	for i := 0; i < 5; i++ {
		_, _ = o.HandleKeyPress(keyRunes("k"))
	}
	assert.Equal(t, 0, o.profileCursor, "up stops at the first profile")
	for i := 0; i < 9; i++ {
		_, _ = o.HandleKeyPress(keyRunes("j"))
	}
	assert.Equal(t, 2, o.profileCursor, "down stops at the last profile")
}

// TestSelectedProfileIsAlwaysVisible is the windowing guard's twin for the second index space
// this pane introduces. paneLine.rowIdx used to mean "index into s.rows" and rowsPaneLines
// matched it against s.cursor; a profile line carries a profile index, and s.cursor is a small
// int too, so matching against the wrong cursor silently windows around an unrelated line.
func TestSelectedProfileIsAlwaysVisible(t *testing.T) {
	var profiles []config.Profile
	for i := 0; i < 20; i++ {
		profiles = append(profiles, config.Profile{
			Name: "p" + strings.Repeat("x", i%4) + string(rune('a'+i)), Program: "cmd",
		})
	}
	o := NewSettingsOverlay(profilesCfg("none", profiles...))
	o.SetSize(100, 20)
	profilesAt(t, o)
	require.Greater(t, len(profiles), o.paneHeight(), "precondition: the list must overflow")

	for i := range profiles {
		o.profileCursor = i
		found := false
		for _, l := range o.rowsPaneLines() {
			if strings.Contains(stripANSI(l), profiles[i].Name) {
				found = true
			}
		}
		assert.Truef(t, found, "profile %d is off-screen with the cursor on it", i)
	}
}

// TestProfilesPaneFitsEveryGeometry is the width sweep. A profile name and a program are user
// data of unbounded length, unlike the fixed schema labels spec §10 says never to truncate — so
// the name column is capped and tail-ellipsized, and no line may exceed the pane at any size
// the panel supports. Swept rather than pinned at one size, because the rows pane is widest in
// single-pane mode and narrowest just above the two-pane threshold.
func TestProfilesPaneFitsEveryGeometry(t *testing.T) {
	cfg := profilesCfg("claude",
		config.Profile{Name: "claude", Program: "claude"},
		config.Profile{
			Name:    "a-deliberately-very-long-profile-name-nobody-would-type",
			Program: "aider --model ollama_chat/gemma3:1b --no-auto-commits --dark-mode",
		},
	)
	checked := 0
	for _, h := range []int{settingsVChrome + settingsMinBody, 16, 24, 40} {
		for w := 40; w <= 200; w++ {
			o := NewSettingsOverlay(cfg)
			o.SetSize(w, h)
			o.SetRailIndex(profilesRailIndex())
			o.focus = focusRows
			paneW := o.rowsPaneWidth()
			lines := o.rowsPaneContent(paneW)

			// Below the two-pane threshold the rail is off screen and the pane names itself, so
			// the expected count is width-dependent. Hardcoding 2 here would forbid that header.
			want := 2
			if !o.twoPane() {
				want = 3
			}
			require.Lenf(t, lines, want, "%dx%d: one line per profile, plus the single-pane header", w, h)

			for _, l := range lines {
				plain := stripANSI(l.text)
				if l.rowIdx < 0 {
					assert.LessOrEqualf(t, ansi.StringWidth(plain), paneW,
						"%dx%d: the header overflows the pane: %q", w, h, plain)
					continue
				}
				// EXACTLY paneW, not <=. composeRowLine bounds its own output on every path, so
				// a <= assertion is a tautology no bug can trip — the plan's own Global
				// Constraints forbid it, and it was measured letting the name-truncation
				// mutation pass. == catches the gap arithmetic instead.
				assert.Equalf(t, paneW, ansi.StringWidth(plain),
					"%dx%d: a profile line is not exactly the pane width: %q", w, h, plain)
			}

			// The presence half: a width assertion alone is satisfied by a blank line.
			joined := stripANSI(lines[len(lines)-1].text)
			assert.Containsf(t, joined, "aider", "%dx%d: the program column vanished: %q", w, h, joined)
			checked++
		}
	}
	require.Greater(t, checked, 600, "the sweep must actually visit the geometries")
}

// TestEmptyProfilesPaneFitsEveryGeometry is the width half of the empty-state line, and it
// exists because that line replaced one the tree already swept.
//
// TestEveryHandoffNoteFitsItsPane sweeps every handoff note over w=40..200 × four heights,
// because a static string that over-wraps makes windowPane draw a "↓ n more" marker on a pane
// with nothing to scroll to. This PR takes Profiles out of that sweep and puts a LONGER static
// string (68 cells, against the note's 65) in the same position. In this repo a copy change is a
// width change, so the sweep has to come with it.
func TestEmptyProfilesPaneFitsEveryGeometry(t *testing.T) {
	checked := 0
	for _, h := range []int{settingsVChrome + settingsMinBody, 16, 24, 40} {
		for w := 40; w <= 200; w++ {
			o := NewSettingsOverlay(config.DefaultConfig())
			o.SetSize(w, h)
			o.SetRailIndex(profilesRailIndex())
			o.focus = focusRows
			paneW, paneH := o.rowsPaneWidth(), o.paneHeight()
			lines := o.rowsPaneContent(paneW)
			assert.LessOrEqualf(t, len(lines), paneH,
				"%dx%d: the empty-state line wraps to %d lines in a %d-line pane, so windowPane "+
					"shows a scroll marker on a pane with nothing to scroll to", w, h, len(lines), paneH)
			for _, l := range lines {
				assert.LessOrEqualf(t, ansi.StringWidth(stripANSI(l.text)), paneW,
					"%dx%d: the empty-state line overflows the pane: %q", w, h, stripANSI(l.text))
			}
			checked++
		}
	}
	require.Greater(t, checked, 600, "the sweep must actually visit the geometries")
}

// TestALongProfileNameCannotEvictTheProgram: without a cap on the name column one long name
// eats the whole pane and composeRowLine truncates the head instead, hiding every program on
// screen. The name yields; the program keeps a legible column.
func TestALongProfileNameCannotEvictTheProgram(t *testing.T) {
	o := NewSettingsOverlay(profilesCfg("none", config.Profile{
		Name: strings.Repeat("n", 120), Program: "claude --model opus",
	}))
	for _, w := range []int{73, 80, 100, 120} {
		o.SetSize(w, 32)
		o.SetRailIndex(profilesRailIndex())
		line := paneText(o)[0]
		assert.Containsf(t, line, "…", "%d: the over-long name must be truncated", w)
		assert.Containsf(t, line, "claude", "%d: the program must survive the long name", w)
	}
}

// TestQuestionMarkIsInertOnTheProfilesPane. `?` opens expandedHelpContent(s.cursor), which
// describes a settingRow. On this pane s.cursor points at an unrelated row, so `?` must do
// nothing at all rather than open help about a setting the user is not looking at.
func TestQuestionMarkIsInertOnTheProfilesPane(t *testing.T) {
	o := NewSettingsOverlay(threeProfiles())
	o.SetSize(100, 32)
	profilesAt(t, o)

	_, _ = o.HandleKeyPress(keyRunes("?"))
	assert.False(t, o.helpOpen, "? must not open help for a row this pane is not showing")
}

// TestSlashFromTheProfilesPaneSearchesSettings. `/` is the settings search; profiles are data,
// not settings, and searchResults walks s.rows. So `/` here behaves exactly as it does from a
// handoff entry: it opens the ordinary filter and takes the rail with it.
func TestSlashFromTheProfilesPaneSearchesSettings(t *testing.T) {
	o := NewSettingsOverlay(threeProfiles())
	o.SetSize(100, 32)
	profilesAt(t, o)

	_, _ = o.HandleKeyPress(keyRunes("/"))
	require.True(t, o.searching())
	assert.False(t, o.profilesPaneActive(), "a filter takes the pane over regardless of the rail")
	assert.NotEqual(t, railProfiles, o.selectedEntry().kind,
		"the rail follows the highlighted result out of the editor")
}

// TestEscIsLayeredOutOfTheProfilesPane: the pane adds a level to spec §15's ladder. From the
// editor, esc backs to the rail; a second esc closes.
func TestEscIsLayeredOutOfTheProfilesPane(t *testing.T) {
	o := NewSettingsOverlay(threeProfiles())
	o.SetSize(100, 32)
	profilesAt(t, o)

	closed, _ := o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEsc})
	assert.False(t, closed, "the first esc backs out of the pane")
	assert.Equal(t, focusRail, o.focus)

	closed, _ = o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEsc})
	assert.True(t, closed, "the second esc closes the panel")
}

// TestLeavingTheProfilesPaneDropsItsTransientState. Moving the rail away must not leave a
// half-typed record or an armed delete behind a pane the user can no longer see — the next
// visit would resume a state they have no way to know about.
func TestLeavingTheProfilesPaneDropsItsTransientState(t *testing.T) {
	o := NewSettingsOverlay(threeProfiles())
	o.SetSize(100, 32)
	profilesAt(t, o)
	o.profileForm = newProfileForm(-1, "half", "typed")
	o.profileConfirm = true
	o.profileNote = "stale"

	o.SetRailIndex(railDefaultIndex())

	assert.Nil(t, o.profileForm)
	assert.False(t, o.profileConfirm)
	assert.Empty(t, o.profileNote)
}
```

Append to `ui/overlay/settings_nav_test.go` — and **replace** the four guards named below rather
than adding beside them:

```go
// TestRailProfilesIsTheEditorSlot pins the rail's new shape: Profiles is its own kind, with no
// note (it owns a pane, not a sentence) and no handoff (the editor lives inside the panel).
func TestRailProfilesIsTheEditorSlot(t *testing.T) {
	entries := railEntries()
	require.Len(t, entries, 13, "spec §4: the editor replaces a handoff, it does not add a row")

	e := entries[11]
	assert.Equal(t, "Profiles", e.label)
	assert.Equal(t, railProfiles, e.kind)
	assert.Empty(t, e.note, "an entry with a pane of its own has no note to render")
	assert.Equal(t, HandoffNone, e.opens, "the editor is not a handoff")

	assert.Equal(t, "Accounts", entries[12].label)
	assert.Equal(t, railHandoff, entries[12].kind)
}
```

In `TestRailEntriesAreTheThirteen`, replace the trailing block

```go
	for _, e := range entries[11:] {
		assert.Equalf(t, railHandoff, e.kind, "%q must be a handoff", e.label)
	}
	assert.Equal(t, "Profiles", entries[11].label)
	assert.Equal(t, "Accounts", entries[12].label)
```

with

```go
	// The last two entries own no settingRows, but for different reasons: Profiles has a pane
	// of its own (PR D's editor) and Accounts hands off to the @ overlay.
	assert.Equal(t, "Profiles", entries[11].label)
	assert.Equal(t, railProfiles, entries[11].kind)
	assert.Equal(t, "Accounts", entries[12].label)
	assert.Equal(t, railHandoff, entries[12].kind)
```

In `TestEveryHandoffEntryNamesItsSurface`, change the liveness counter and its comment:

```go
	// Without this the loop could stop running and the test would still pass. Accounts is the
	// only handoff left: PR D replaced the Profiles note with an editor, and the assert.Emptyf
	// above is what stops that note being left behind on the new kind.
	require.Equal(t, 1, handoffs, "Accounts is the only handoff")
```

Replace `TestRailHintNeverPromisesAPaneSwapWithoutRows` wholesale:

```go
// TestRailHintNeverPromisesAPaneSwapWithoutRows holds "⇥ pane" and "→ rows" to spec §15's
// standard, restated for PR D's fourth rail kind.
//
// Before the editor the two promises had ONE discriminator: railHandoff was exactly the no-rows
// case, so an entry with no rows also had no pane to swap into. railProfiles splits them. It
// owns a focusable pane — tab genuinely swaps into it, so "⇥ pane" is honest — while owning no
// settingRows, so "→ rows" is not. The invariant is therefore two facts, not one:
//
//   - "→ rows" requires settingRows: exactly railAll and railCategory have them.
//   - "⇥ pane" requires a pane the forward key can focus: everything except railHandoff.
func TestRailHintNeverPromisesAPaneSwapWithoutRows(t *testing.T) {
	o := NewSettingsOverlay(config.DefaultConfig())
	withRows, panes, handoffs := 0, 0, 0
	for _, e := range railEntries() {
		start, end := o.rowRange(e)
		owns := end > start
		require.Equalf(t, e.kind == railAll || e.kind == railCategory, owns,
			"entry %q: settingRows belong to railAll and railCategory alone", e.label)
		focusable := e.kind != railHandoff
		if owns {
			withRows++
		}
		if focusable {
			panes++
		} else {
			handoffs++
		}
		for i, rung := range railHintLadder(e) {
			if !owns {
				assert.NotContainsf(t, rung, "→ rows",
					"entry %q rung %d promises rows it does not own: %q", e.label, i, rung)
			}
			if !focusable {
				assert.NotContainsf(t, rung, "⇥ pane",
					"entry %q rung %d promises a pane swap it cannot do: %q", e.label, i, rung)
			}
		}
	}
	// Without these the loop could stop covering a side and the test would still pass.
	require.Equal(t, 11, withRows, "All settings plus the ten categories own rows")
	require.Equal(t, 12, panes, "every entry but Accounts owns a focusable pane")
	require.Equal(t, 1, handoffs, "Accounts is the only handoff")
	// The positive half, on both kinds that can swap panes.
	assert.Contains(t, railHintLadder(railEntries()[railDefaultIndex()])[0], "⇥ pane")
	assert.Contains(t, railHintLadder(railEntries()[profilesRailIndex()])[0], "⇥ pane",
		"the editor's pane is focusable, so its widest rung says so")
}
```

Replace the Profiles block of `TestRailHintNamesWhatTheForwardKeyDoes`:

```go
	o.SetRailIndex(profilesRailIndex())
	profiles := stripANSI(o.hintLine())
	assert.Contains(t, profiles, "→ profiles", "the forward key opens the editor, and says so")
	assert.NotContains(t, profiles, "→ rows", "Profiles owns no settingRows")
	assert.NotContains(t, profiles, "↵ accounts")
	assert.Contains(t, profiles, "esc close")
```

Delete `TestProfilesEntryStaysANoOp` — `TestProfilesEntryFocusesItsEditor` is its inverse and
lives in `settings_profiles_test.go`. Update `TestEveryWiredHandoffNamesItsForwardKey`'s
liveness message from `"Accounts is the only wired handoff in PR C"` to
`"Accounts is the only handoff left"` (the count stays 1).

In `ui/overlay/settings_render_test.go`, change `TestEveryHandoffNoteFitsItsPane`'s counter to
`require.Equal(t, 1, notes, "Accounts is the only handoff note")`.

- [ ] **Step 2: Run the tests to verify they fail**

```bash
PATH="/home/zvi/.local/share/mise/installs/go/1.26.4/bin:$PATH" \
  go test ./ui/overlay/ 2>&1 | head -30
```
Expected: the package does not compile — `railProfiles`, `profileDefaultBadge`,
`profilesHelp`, `newProfileForm`, `profilesPaneActive` and `o.profileCursor` are undefined.

- [ ] **Step 3: Add the kind and the entry**

In `ui/overlay/settings_nav.go`, extend the `railKind` block:

```go
	// railProfiles is the Profiles record editor (spec §9). It owns a focusable pane exactly as
	// a category does, but that pane is over cfg.Profiles rather than over settingRows — so
	// rowRange reports zero rows for it, newSettingRows never sees it, and
	// TestEveryScalarConfigFieldHasARow's `profiles` exemption stays true. PR D.
	railProfiles
	// railHandoff owns no rows AND no pane: that config lives on another surface, and the
	// forward key opens it. Accounts is the only one.
	railHandoff
```

Replace the Profiles entry in `railEntries()`:

```go
		railEntry{label: "Profiles", kind: railProfiles},
```

Update `rowRange`'s trailing comment to `return 0, 0 // railProfiles and railHandoff own no settingRows`.

In `handleRailKey`'s forward arm, between the rows branch and the handoff branch:

```go
		if s.selectedEntry().kind == railProfiles {
			// The editor owns a pane the same way a category does; it just is not driven by
			// settingRows, so the rowRange gate above cannot see it. Focus moves in, the panel
			// stays open — unlike a handoff, which gives the screen to another overlay.
			s.focus = focusRows
			s.clampProfileCursor()
			return false
		}
```

Add the transient reset and call it from `syncCursorToRail` (right after `s.lastErr = ""`) and
from `OpenAt` (beside `s.clearSearch()`):

```go
// resetProfileTransients drops the editor's per-visit state: an open form, an armed delete and
// the one-keypress note. Moving the rail off Profiles — or deep-linking past it — must not
// leave a half-typed record or an armed "y deletes" behind a pane the user can no longer see.
//
// profileDetecting is deliberately NOT cleared: a shell probe already in flight still lands,
// and its merge is what the user asked for.
func (s *SettingsOverlay) resetProfileTransients() {
	s.profileForm = nil
	s.profileConfirm = false
	s.profileNote = ""
}
```

- [ ] **Step 4: Add the overlay's fields and the key router arm**

In `ui/overlay/settings.go`'s `SettingsOverlay`, after the `search Picker` field:

```go
	// The Profiles editor's state (spec §9). profileCursor indexes cfg.Profiles — a SECOND
	// index space beside s.cursor, which indexes s.rows; see paneCursor for the one place that
	// distinction is load-bearing. The rest are transient and cleared by
	// resetProfileTransients.
	profileCursor  int
	profileForm    *profileForm
	profileConfirm bool
	// profileNote is a one-keypress informational line for the editor's help pane — a detection
	// result. It is not lastErr: "no new agents detected" is an outcome, not a failure, and the
	// help pane paints lastErr in DangerStyle.
	profileNote string
	// profileDetectPending is D's request for home to run detection off the update loop;
	// profileDetecting stays set until the result lands, so a second D cannot queue a second
	// shell probe. See requestProfileDetect.
	profileDetectPending bool
	profileDetecting     bool
```

Extend `HandleKeyPress` and its doc comment:

```go
// The order of these guards is the grammar: an open editor swallows everything (so j/k type
// rather than navigate) — the inline line editor or the Profiles record form — then the
// expanded-help view, then an active filter (which swallows runes for the same reason), then
// the focused pane, where the Profiles editor takes its own keys.
func (s *SettingsOverlay) HandleKeyPress(msg tea.KeyMsg) (closed bool, changedKey string) {
	switch {
	case s.editing:
		return false, s.handleEditKey(msg)
	case s.profileForm != nil:
		return false, s.handleProfileFormKey(msg)
	case s.helpOpen:
		s.handleHelpKey(msg)
		return false, ""
	case s.searching():
		return s.handleSearchKey(msg)
	case s.focus == focusRail:
		return s.handleRailKey(msg), ""
	case s.profilesPaneActive():
		return s.handleProfilesKey(msg)
	default:
		return s.handleRowsKey(msg)
	}
}
```

- [ ] **Step 5: Create `ui/overlay/settings_profiles.go` — state, navigation, and the pane**

```go
package overlay

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/ui/theme"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// profilesChangedKey is what the editor reports to home when cfg.Profiles changed. It is the
// config.json key itself, so it routes through applySettingChange exactly as a row edit does —
// keeping the panel's existing persist path the only writer — and reaches the one live-apply
// case a profile has: re-resolving the launch command GetProgram derives from it.
const profilesChangedKey = "profiles"

// profileDefaultBadge marks the profile default_program names. It rides the badge column, so
// composeRowLine drops it first on a narrow pane (spec §10) and profilesContextLine carries the
// same fact as a sentence — the badge is scannable, the sentence is the fallback.
const profileDefaultBadge = "default"

// profilesPaneActive reports whether the right pane is currently the Profiles editor. A filter
// takes the pane over regardless of which rail entry is marked, so the search arm wins.
func (s *SettingsOverlay) profilesPaneActive() bool {
	return !s.searching() && s.selectedEntry().kind == railProfiles
}

// clampProfileCursor pulls the cursor back inside cfg.Profiles after the list shrinks under it.
//
// It is the accounts overlay's clampCursor (ui/overlay/accounts.go), and it exists for exactly
// one case: deleting the LAST record leaves the cursor one past the end, and the very next
// render — or the next d — indexes out of range and panics. Deleting a middle record needs no
// clamp; the cursor correctly lands on what was the next profile.
func (s *SettingsOverlay) clampProfileCursor() {
	s.profileCursor = clamp(s.profileCursor, 0, max(0, len(s.cfg.Profiles)-1))
}

// handleProfilesKey routes a key while the Profiles pane has focus (spec §9).
//
// Every arm starts from a clean slate: lastErr and the one-keypress note are cleared first, so a
// refusal or a detection result lives exactly until the next key rather than lingering over a
// pane it no longer describes. The confirmation is checked BEFORE that clear, because its
// prompt has to survive its own render.
//
// `?` and `r` are deliberately unbound. Both read s.rows[s.cursor], which on this pane points at
// whatever settingRow the cursor last sat on — help about a setting the user is not looking at,
// or a reset of one they cannot see.
func (s *SettingsOverlay) handleProfilesKey(msg tea.KeyMsg) (closed bool, changedKey string) {
	if s.profileConfirm {
		return false, s.handleProfileConfirmKey(msg)
	}
	s.lastErr, s.profileNote = "", ""
	switch msg.String() {
	case "esc", "ctrl+c", "tab", "shift+tab":
		// Layered: back to the rail first, close from there. Advertised as "esc back".
		s.focus = focusRail
	case "up", "k":
		if s.profileCursor > 0 {
			s.profileCursor--
		}
	case "down", "j":
		if s.profileCursor < len(s.cfg.Profiles)-1 {
			s.profileCursor++
		}
	case "/":
		s.startSearch()
	}
	return false, ""
}

// profilesPaneContent is the rows pane for the Profiles entry: the record form while one is
// open, else one line per profile.
//
// The lines are composed by composeRowLine, the same function the settings rows use, so the
// editor inherits spec §10's truncation ladder rather than reimplementing it: the badge yields
// first, then the program, and the name column is capped (see profileLabelWidth). The styling
// splits the same way renderRowLine's default arm does — head dim, value bright, badge faint —
// so a profile's command is not dimmer than a setting's value one pane over.
func (s *SettingsOverlay) profilesPaneContent(width int) []paneLine {
	if s.profileForm != nil {
		return s.profileFormLines(width)
	}
	t := theme.Current()
	var lines []paneLine
	if !s.twoPane() {
		// Single-pane drill-in hides the rail, so the pane has to name itself — otherwise the
		// user is looking at an unlabelled list of names and commands, which is D2 (no
		// orientation) reintroduced at narrow widths. rowsPaneContent does exactly this for a
		// category, and dispatching to this function jumps over that branch.
		lines = append(lines, paneLine{
			text:   t.DimStyle().Bold(true).Render("Profiles"),
			rowIdx: -1,
		})
	}
	if len(s.cfg.Profiles) == 0 {
		// An empty pane reads as a broken panel — the obligation a handoff note carries. Name
		// both keys that fill it.
		return append(lines, wrappedPaneLines(
			"No profiles yet — press n to add one, or D to detect installed agents.",
			width, t.FaintStyle())...)
	}
	labelW := s.profileLabelWidth(width)
	for i, p := range s.cfg.Profiles {
		// Both panes always show their cursor; only the STYLE differs, so exactly one
		// accent-bright marker is on screen at a time (renderRowLine's rule).
		sel := " "
		selected := i == s.profileCursor
		rowStyle := t.FgStyle()
		if selected {
			sel = t.Glyphs.SelectionMark
			if s.focus == focusRows {
				rowStyle = t.AccentStyle()
			}
		}
		badge := ""
		if p.Name == s.cfg.DefaultProgram {
			badge = profileDefaultBadge
		}
		parts := composeRowLine(width, labelW, sel, " ",
			ansi.Truncate(p.Name, labelW, "…"), p.Program, badge)
		text := t.DimStyle().Render(parts.head) +
			t.FgStyle().Render(parts.value+parts.gap) +
			t.FaintStyle().Render(parts.badge)
		if selected {
			// Accent wins over the split: the row under the cursor must read as one unit.
			text = rowStyle.Render(parts.plain())
		}
		lines = append(lines, paneLine{text: text, rowIdx: i})
	}
	return lines
}

// profileLabelWidth is the name column: the longest profile name, capped so the program column
// keeps rowMinValueCells.
//
// A profile name is user data of unbounded length, unlike the fixed schema labels spec §10 says
// never to truncate — so this column is capped and the name tail-ellipsized, with the full name
// and program in the help pane. Without the cap one long name eats the pane and composeRowLine
// truncates the head instead, hiding every program on screen.
func (s *SettingsOverlay) profileLabelWidth(width int) int {
	w := 0
	for _, p := range s.cfg.Profiles {
		if n := ansi.StringWidth(p.Name); n > w {
			w = n
		}
	}
	return clamp(w, 1, max(1, width-rowMarkerCells-rowLabelGap-rowMinValueCells))
}

// profilesHelp is the help pane's prose for the editor, and whether it is a warning.
//
// It replaces settingRow.footerText() for this pane because s.cursor still points at whatever
// settingRow it last sat on, and selectedRow() is unguarded — describing that row here would put
// an unrelated setting's summary under a list of profiles, the same lie railHandoff's blank
// prose avoids.
func (s *SettingsOverlay) profilesHelp() (prose string, danger bool) {
	switch {
	case s.profileConfirm:
		// Armed by armProfileDelete (Task 4). It outranks everything below because the other
		// states are impossible while it is up, and stating the order makes that structural.
		return "Delete profile " + strconv.Quote(s.cfg.Profiles[s.profileCursor].Name) +
			"? This cannot be undone.", true
	case s.profileDetecting:
		// Derived from the in-flight flag, NOT from profileNote — which handleProfilesKey clears
		// on every key. The probe outlives the key that started it, so a j pressed while it runs
		// must not erase the only thing on screen explaining why nothing has happened yet. It is
		// also what makes a second D a visible no-op rather than a key that removes feedback.
		return "Detecting installed agents…", false
	case s.profileNote != "":
		return s.profileNote, false
	case s.profileForm != nil:
		// The form fills the pane and the hint row names its keys; a third voice would crowd
		// out the validation message that lands here on a rejected save.
		return "", false
	case len(s.cfg.Profiles) == 0:
		return "Without a profile, Default program is run as the launch command itself.", false
	default:
		// Spec §10: a truncated value must be shown in full here, or the truncation loses
		// information rather than deferring it.
		return s.cfg.Profiles[s.profileCursor].Program, false
	}
}

// profilesContextLine is the editor's position readout, with the default-program fact as its
// body — the sentence behind the badge composeRowLine drops first on a narrow pane.
func (s *SettingsOverlay) profilesContextLine(width int) string {
	n := len(s.cfg.Profiles)
	if n == 0 || s.profileForm != nil {
		return ""
	}
	body := ""
	if s.cfg.Profiles[s.profileCursor].Name == s.cfg.DefaultProgram {
		body = "Default program launches this profile."
	}
	return rightAligned(body, fmt.Sprintf("%d/%d", s.profileCursor+1, n), width)
}

// profilesHintLadder is the editor's key hints, widest wording first. `/ search` outranks
// "⇥ pane" in the ladder so the filter stays advertised at the 80-column floor, where the
// widest rung does not fit.
func (s *SettingsOverlay) profilesHintLadder() []string {
	if s.profileConfirm {
		return []string{"y delete · n cancel · esc cancel", "y delete · n cancel", "y / n"}
	}
	return []string{
		"↑/↓ move · n new · ↵ edit · d delete · D detect · / search · ⇥ pane · esc back",
		"↑/↓ move · n new · ↵ edit · d delete · D detect · / search · esc back",
		"↑/↓ · n new · ↵ edit · d delete · D detect · esc back",
		"n new · ↵ edit · d delete · esc back",
		"esc back",
	}
}
```

> `n`, `e`, `d` and `D` are in the ladder above but bound in Tasks 3–5. **Do not ship this
> ladder in Task 2's commit** — replace the list rungs with
> `{"↑/↓ move · / search · ⇥ pane · esc back", "↑/↓ · / search · esc back", "esc back"}` here
> and restore the full ladder in Task 5, where the last of those keys goes live. PR C's
> ordering lesson: no commit should advertise a dead key.
>
> **Imports at this commit boundary.** The final-state list above is exactly right, but Task 2
> alone uses only `strings`, `config`, `theme`, `ansi` and `tea` — `fmt` arrives with
> `profilesContextLine`, and `strconv`/`textinput` with the form. Since Step 7 lands the form and
> the confirm prose in this same commit, all eight are used from the start; if you split the
> commit differently, an unused import fails the build before any test runs.

- [ ] **Step 6: Wire the renderer**

In `ui/overlay/settings_render.go`, extract the two helpers the new pane shares with the old
ones, and rewrite `handoffPaneContent` plus `searchPaneContent`'s empty branch to use the first:

```go
// wrappedPaneLines wraps prose to the pane width as styled, rowIdx -1 pane lines — the shape
// every non-row pane content uses: a handoff note, a no-match line, an empty editor.
func wrappedPaneLines(text string, width int, style lipgloss.Style) []paneLine {
	var lines []paneLine
	for _, l := range strings.Split(ansi.Wrap(text, width, ""), "\n") {
		lines = append(lines, paneLine{text: style.Render(l), rowIdx: -1})
	}
	return lines
}

// rightAligned lays a body string and a right-aligned position readout into exactly width
// cells, truncating the body to make room. The counter is five cells and the body is
// recoverable from `?`, so content yields to it and never the other way round.
func rightAligned(body, pos string, width int) string {
	budget := width - ansi.StringWidth(pos) - 1
	if budget < 1 {
		return theme.Current().FaintStyle().Render(pos)
	}
	if ansi.StringWidth(body) > budget {
		body = ansi.Truncate(body, budget, "…")
	}
	gap := width - ansi.StringWidth(body) - ansi.StringWidth(pos)
	return theme.Current().FaintStyle().Render(body + strings.Repeat(" ", gap) + pos)
}
```

`contextLine`'s tail becomes `return rightAligned(body, pos, width)`.

Add the pane dispatch in `rowsPaneContent`, right after the `railHandoff` branch:

```go
	if e.kind == railProfiles {
		return s.profilesPaneContent(width)
	}
```

Add `paneCursor` and use it in `rowsPaneLines`:

```go
// paneCursor is the index rowsPaneLines matches paneLine.rowIdx against: the row cursor for a
// settingRow pane, the profile cursor for the Profiles editor.
//
// The two index DIFFERENT lists. Before the editor there was one index space and rowsPaneLines
// could read s.cursor directly; a profile line carries a profile index, and s.cursor is a small
// int too, so matching against the wrong one silently windows around an unrelated line and the
// selected profile scrolls off screen.
func (s *SettingsOverlay) paneCursor() int {
	if s.profilesPaneActive() {
		return s.profileCursor
	}
	return s.cursor
}
```

and in `rowsPaneLines`, `if l.rowIdx == s.paneCursor() {`. Update `paneLine`'s doc comment:
`rowIdx is the index of the record this line shows — into s.rows, or into cfg.Profiles on the
Profiles pane — and -1 for a header, a spacer, an overflow marker or wrapped prose.`

In `helpLines`, replace the prose seed:

```go
	style := t.DimStyle()
	var prose string
	switch {
	case s.selectedEntry().kind == railHandoff:
		// A handoff entry's note is already the whole content of the rows pane, so echoing it
		// here would print the same sentence twice in one frame. The pane stays blank.
	case s.profilesPaneActive():
		var danger bool
		prose, danger = s.profilesHelp()
		if danger {
			style = t.DangerStyle()
		}
	default:
		prose = s.selectedRow().footerText()
	}
```

In `contextLine`, first statement:

```go
	if s.profilesPaneActive() {
		return s.profilesContextLine(width)
	}
```

In `hintLine`'s switch, between `case s.searching():` and `case s.focus == focusRows:`:

```go
	case s.profilesPaneActive() && s.focus == focusRows:
		ladder = s.profilesHintLadder()
```

and in `railHintLadder`, before the `railHandoff` branch:

```go
	if e.kind == railProfiles {
		// The editor owns a pane but no rows, so the forward key names the editor rather than
		// "rows" — and "⇥ pane" is honest here, because tab really does focus it.
		return []string{
			"↑/↓ category · → profiles · / search · ⇥ pane · esc close",
			"↑/↓ · → profiles · / search · esc close",
			"/ search · esc close",
			"esc close",
		}
	}
```

**And delete the `forward == ""` branch inside the existing `railHandoff` arm**, together with
its comment (`"Profiles: PR D gives it an editor; until then the forward key does nothing…"`).
That branch existed for exactly one entry, which this PR removes: Accounts is now the only
handoff and it is wired, so `handoffHint` can no longer return `""` and the branch is dead code
carrying a comment that has become false. `unused` does not catch a dead *branch*, so nothing
would have told us. The arm becomes:

```go
	if e.kind == railHandoff {
		// Every handoff is wired — TestEveryHandoffEntryNamesItsSurface is what keeps that true,
		// and an unwired one would render a ladder naming no forward key at all, the lie this
		// function exists to prevent.
		forward := handoffHint(e.opens)
		return []string{
			"↑/↓ category · " + forward + " · / search · esc close",
			"↑/↓ · " + forward + " · / search · esc close",
			"/ search · esc close",
			"esc close",
		}
	}
```

That deletion is only safe if "every handoff is wired" is *asserted* rather than merely true, so
add the missing half to `TestEveryHandoffEntryNamesItsSurface`'s handoff branch:

```go
		assert.NotEqualf(t, HandoffNone, e.opens,
			"handoff entry %q opens nothing — an entry with no rows, no pane and no surface has "+
				"no reason to exist, and railHintLadder now assumes it cannot", e.label)
```

- [ ] **Step 7: Add the form stub so the package compiles**

Tasks 3–5 fill these in; Task 2 needs `newProfileForm`, `handleProfileFormKey`,
`profileFormLines` and `handleProfileConfirmKey` to exist because `HandleKeyPress`,
`profilesPaneContent` and `handleProfilesKey` name them. Write them **complete** now — they are
Task 3's and Task 5's deliverables and are quoted in full there — rather than as stubs, and
leave only the key bindings (`n`, `e`, `d`, `D`) for those tasks. A stub would be dead code the
`unused` linter flags and a reviewer cannot judge.

- [ ] **Step 8: Run the tests to verify they pass**

```bash
PATH="/home/zvi/.local/share/mise/installs/go/1.26.4/bin:$PATH" \
  go test ./ui/overlay/ 2>&1 | tail -20
```
Expected: PASS, including the six restated drift guards. Any failure naming a count (13, 11, 12,
1) is a real disagreement with this plan's Derived numbers — re-measure before editing the test.

- [ ] **Step 9: Verify the guards fail when they should**

Mandatory. Restore each by editing the line back — **never** with `git checkout`:

1. Give the Profiles `railEntry` back its old `note:` string. Expected:
   `TestEveryHandoffEntryNamesItsSurface` FAILS on the non-handoff empty-note assertion.
2. Change `railHintLadder`'s `railProfiles` rungs to say `→ rows`. Expected:
   `TestRailHintNeverPromisesAPaneSwapWithoutRows` FAILS naming Profiles, and
   `TestRailHintNamesWhatTheForwardKeyDoes` FAILS.
3. Make `paneCursor` return `s.cursor` unconditionally. Expected:
   `TestSelectedProfileIsAlwaysVisible` FAILS. **If it does not, the fixture is too short —
   raise the profile count until the list overflows `paneHeight()` and report the number.**
4. Delete the `ansi.Truncate` on the name in `profilesPaneContent`. Expected:
   `TestALongProfileNameCannotEvictTheProgram` FAILS on the missing program.
5. Make `profilesHelp`'s default arm return `s.selectedRow().footerText()`. Expected:
   `TestProfilesPaneNeverDescribesAnUnrelatedRow` FAILS.
6. Delete the `!s.twoPane()` header from `profilesPaneContent`. Expected:
   `TestProfilesPaneFitsEveryGeometry` FAILS at every width below 73, naming the line count.
7. Change the profile-line composition to render `parts.plain()` in one style for unselected
   rows. Expected: **nothing fails** — the split is a visual-consistency fix with no width or
   content consequence, so confirm it by eye in Task 8 Step 4 instead and say so here. A
   mutation with no guard is a finding, not a pass: if you would rather guard it, assert that an
   unselected line's badge segment carries a different SGR sequence than its value segment.
8. Re-run and confirm green.

- [ ] **Step 10: Lint and commit**

```bash
PATH="/home/zvi/go/bin:$PATH" mise exec -- just lint ./ui/... ./app/...
git status --short
git add ui/overlay/settings.go ui/overlay/settings_nav.go ui/overlay/settings_render.go \
  ui/overlay/settings_profiles.go ui/overlay/settings_profiles_test.go \
  ui/overlay/settings_nav_test.go ui/overlay/settings_render_test.go
git commit -m "feat(settings): the profiles rail entry becomes a pane of its own"
```

---

## Task 3: the record form — `n` new, `e`/`↵` edit

**Files:**
- Modify: `ui/overlay/settings_profiles.go` (`profileForm`, `newProfileForm`,
  `handleProfileFormKey`, `validateProfile`, `commitProfile`, `profileFormLines`, the `n`/`e`
  arms)
- Modify: `ui/overlay/settings_profiles_test.go`

**Interfaces:**
- Consumes: `newFieldInput(placeholder string) textinput.Model` (`ui/overlay/accountForm.go:50`),
  `wrapIndex(cur, delta, n int) int` (`ui/overlay/chiprow.go:122`), `padRight`, `clamp`, `max`.
- Produces:
  - `newProfileForm(editIndex int, name, program string) *profileForm`
  - `(*SettingsOverlay).handleProfileFormKey(tea.KeyMsg) (changedKey string)`
  - `(*SettingsOverlay).validateProfile() string` — `""` when valid
  - `(*SettingsOverlay).commitProfile() string` — the changed key

Guard 12, first two clauses: new and edit round-trip through `config.SaveConfig`.

`openForm(-1)` is new and `openForm(cursor)` is edit — the accounts overlay's single sentinel,
which drives both `validate`'s self-exclusion and `commit`'s append-vs-replace. This form keeps
the sentinel on the form itself (`editIndex`) because the panel has no `modeEdit` to hang it on.

- [ ] **Step 1: Write the failing tests**

Append to `ui/overlay/settings_profiles_test.go`:

```go
// typeProfile sends each rune of s to the overlay as individual key messages, so the form's
// inputs see the same stream a user produces. It deliberately does NOT send Enter — the commit
// keypress's return value is what the tests assert on.
func typeProfile(o *SettingsOverlay, s string) {
	for _, r := range s {
		_, _ = o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
}

// TestNewProfileRoundTripsIntoTheConfig is guard 12's first clause. n opens an empty form, tab
// moves to the program field, and Enter appends the record and reports the changed key so home
// persists it through applySettingChange — the panel's one writer.
func TestNewProfileRoundTripsIntoTheConfig(t *testing.T) {
	cfg := threeProfiles()
	o := NewSettingsOverlay(cfg)
	o.SetSize(100, 32)
	profilesAt(t, o)

	_, _ = o.HandleKeyPress(keyRunes("n"))
	require.NotNil(t, o.profileForm, "n opens the form")
	require.Equal(t, -1, o.profileForm.editIndex, "-1 is the new-record sentinel")

	typeProfile(o, "gemini")
	_, _ = o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyTab})
	typeProfile(o, "gemini --yolo")
	_, changed := o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEnter})

	assert.Equal(t, profilesChangedKey, changed, "the editor reports the config key it changed")
	assert.Nil(t, o.profileForm, "a committed form closes")
	require.Len(t, cfg.Profiles, 4)
	assert.Equal(t, config.Profile{Name: "gemini", Program: "gemini --yolo"}, cfg.Profiles[3])
	assert.Equal(t, 3, o.profileCursor, "the cursor lands on the record you just made")
}

// TestEditProfileReplacesInPlace: e seeds the form from the highlighted record and Enter writes
// it back at the same index rather than appending a near-duplicate.
func TestEditProfileReplacesInPlace(t *testing.T) {
	cfg := threeProfiles()
	o := NewSettingsOverlay(cfg)
	o.SetSize(100, 32)
	profilesAt(t, o)
	_, _ = o.HandleKeyPress(keyRunes("j")) // onto aider

	_, _ = o.HandleKeyPress(keyRunes("e"))
	require.NotNil(t, o.profileForm)
	assert.Equal(t, 1, o.profileForm.editIndex)
	assert.Equal(t, "aider", o.profileForm.name(), "the form is seeded from the record")
	assert.Equal(t, "aider --model ollama_chat/gemma3:1b", o.profileForm.program())

	// applyFocus leaves the cursor at end, so typing appends rather than overtyping.
	_, _ = o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyTab})
	typeProfile(o, " --dark-mode")
	_, changed := o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEnter})

	assert.Equal(t, profilesChangedKey, changed)
	require.Len(t, cfg.Profiles, 3, "an edit replaces, it does not append")
	assert.Equal(t, "aider --model ollama_chat/gemma3:1b --dark-mode", cfg.Profiles[1].Program)
	assert.Equal(t, "aider", cfg.Profiles[1].Name)
}

// TestEnterIsAnAliasForEdit — spec §9 lists "e/Enter edit", and the accounts overlay binds them
// together too.
func TestEnterIsAnAliasForEdit(t *testing.T) {
	o := NewSettingsOverlay(threeProfiles())
	o.SetSize(100, 32)
	profilesAt(t, o)

	_, _ = o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEnter})
	require.NotNil(t, o.profileForm)
	assert.Equal(t, 0, o.profileForm.editIndex)
}

// TestEditAndDeleteAreInertWithNoProfiles: n needs no selection, but e/↵ and d index the list.
// On an empty pane they must do nothing rather than panic — the guard accounts.go writes as
// `if o.activeLen() > 0`.
func TestEditAndDeleteAreInertWithNoProfiles(t *testing.T) {
	cfg := config.DefaultConfig()
	o := NewSettingsOverlay(cfg)
	o.SetSize(100, 32)
	require.Empty(t, cfg.Profiles)
	profilesAt(t, o)

	for _, key := range []string{"e", "d"} {
		_, changed := o.HandleKeyPress(keyRunes(key))
		assert.Emptyf(t, changed, "%q changes nothing on an empty list", key)
		assert.Nilf(t, o.profileForm, "%q must not open a form over nothing", key)
		assert.Falsef(t, o.profileConfirm, "%q must not arm a delete over nothing", key)
	}
	_, _ = o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEnter})
	assert.Nil(t, o.profileForm, "↵ is the edit alias and is inert here too")

	_, _ = o.HandleKeyPress(keyRunes("n"))
	assert.NotNil(t, o.profileForm, "n needs no selection")
}

// TestFormValidationRejectsAndStaysOpen. A rejected save must be fixable in place rather than
// thrown away, so the form stays open with the message in the help pane — and the SECOND Enter
// in the same form instance must still reach validate, which is what the accounts overlay's
// `o.form.submitted = false` reset exists for.
func TestFormValidationRejectsAndStaysOpen(t *testing.T) {
	cfg := threeProfiles()
	o := NewSettingsOverlay(cfg)
	o.SetSize(100, 32)
	profilesAt(t, o)
	_, _ = o.HandleKeyPress(keyRunes("n"))

	_, changed := o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEnter}) // empty name
	assert.Empty(t, changed)
	require.NotNil(t, o.profileForm, "a rejected save stays in the form")
	assert.Contains(t, o.lastErr, "name")
	assert.Len(t, cfg.Profiles, 3, "nothing was written")

	typeProfile(o, "claude") // now a duplicate
	_, changed = o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEnter})
	assert.Empty(t, changed)
	require.NotNil(t, o.profileForm)
	assert.Contains(t, o.lastErr, "already exists")
	assert.Len(t, cfg.Profiles, 3)

	typeProfile(o, "-fast") // unique now, but no program yet
	_, changed = o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEnter})
	assert.Empty(t, changed)
	require.NotNil(t, o.profileForm)
	assert.Contains(t, o.lastErr, "program",
		"an empty program is not 'inherit the default', it is a session that launches nothing")
	assert.Len(t, cfg.Profiles, 3)
}

// TestEditingWithoutRenamingIsNotADuplicateOfItself is the self-exclusion half: validate skips
// the record being edited, so re-saving an unrenamed edit works, while renaming ONTO another
// record still fails.
func TestEditingWithoutRenamingIsNotADuplicateOfItself(t *testing.T) {
	cfg := threeProfiles()
	o := NewSettingsOverlay(cfg)
	o.SetSize(100, 32)
	profilesAt(t, o)

	_, _ = o.HandleKeyPress(keyRunes("e")) // claude, unrenamed
	_, changed := o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEnter})
	assert.Equal(t, profilesChangedKey, changed, "an unrenamed edit is not a duplicate of itself")
	assert.Empty(t, o.lastErr)

	_, _ = o.HandleKeyPress(keyRunes("e"))
	_, _ = o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyCtrlU}) // clear the seeded name
	typeProfile(o, "codex")                                 // rename onto another record
	_, changed = o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEnter})
	assert.Empty(t, changed)
	assert.Contains(t, o.lastErr, "already exists")
	assert.Equal(t, "claude", cfg.Profiles[0].Name, "the rename was refused, not applied")
}

// TestEscInTheFormDiscardsTheEdit — the form works on its own string copies, so cancelling
// touches nothing. This is the editor's own Esc level, above the panel's three.
func TestEscInTheFormDiscardsTheEdit(t *testing.T) {
	cfg := threeProfiles()
	before := cfg.Profiles[0]
	o := NewSettingsOverlay(cfg)
	o.SetSize(100, 32)
	profilesAt(t, o)

	_, _ = o.HandleKeyPress(keyRunes("e"))
	typeProfile(o, "-mangled")
	closed, changed := o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEsc})

	assert.False(t, closed, "esc in the form must not close the panel")
	assert.Empty(t, changed)
	assert.Nil(t, o.profileForm)
	assert.Equal(t, before, cfg.Profiles[0], "esc discards the edit")
	assert.Equal(t, focusRows, o.focus, "and leaves you in the editor, not on the rail")
}

// TestFormSwallowsNavigationKeys. While a form is open, j/k/d/n/D are letters — the same rule
// the settings line editor and the `/` filter follow. Getting this wrong deletes a record while
// the user is typing a name.
func TestFormSwallowsNavigationKeys(t *testing.T) {
	cfg := threeProfiles()
	o := NewSettingsOverlay(cfg)
	o.SetSize(100, 32)
	profilesAt(t, o)
	_, _ = o.HandleKeyPress(keyRunes("n"))

	typeProfile(o, "jkdnD/?")
	assert.Equal(t, "jkdnD/?", o.profileForm.name(), "every rune is text in a form")
	assert.Len(t, cfg.Profiles, 3, "nothing was deleted")
	assert.False(t, o.searching(), "/ does not open the filter from inside the form")
	assert.False(t, o.helpOpen)
	assert.Equal(t, 0, o.profileCursor, "j did not navigate")
}

// TestFormTabCyclesTheTwoFields, both directions, wrapping.
func TestFormTabCyclesTheTwoFields(t *testing.T) {
	o := NewSettingsOverlay(threeProfiles())
	o.SetSize(100, 32)
	profilesAt(t, o)
	_, _ = o.HandleKeyPress(keyRunes("n"))

	assert.Equal(t, fldProfileName, o.profileForm.focus)
	_, _ = o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyTab})
	assert.Equal(t, fldProfileProgram, o.profileForm.focus)
	_, _ = o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyTab})
	assert.Equal(t, fldProfileName, o.profileForm.focus, "tab wraps")
	_, _ = o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyShiftTab})
	assert.Equal(t, fldProfileProgram, o.profileForm.focus, "shift+tab wraps the other way")
}

// TestRenamingTheDefaultProfileCarriesDefaultProgramWithIt. default_program is a NAME, so a
// rename that left it behind would silently change what new sessions launch: the pointer would
// stop matching any profile and GetProgram would fall through to running the old name as a raw
// shell command. Following the rename preserves exactly what launches.
func TestRenamingTheDefaultProfileCarriesDefaultProgramWithIt(t *testing.T) {
	cfg := threeProfiles()
	require.Equal(t, "claude", cfg.DefaultProgram)
	require.Equal(t, "claude --model opus", cfg.GetProgram())
	o := NewSettingsOverlay(cfg)
	o.SetSize(100, 32)
	profilesAt(t, o)

	_, _ = o.HandleKeyPress(keyRunes("e"))
	_, _ = o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyCtrlU})
	typeProfile(o, "claude-fast")
	_, changed := o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEnter})

	assert.Equal(t, profilesChangedKey, changed)
	assert.Equal(t, "claude-fast", cfg.DefaultProgram, "the pointer follows the record")
	assert.Equal(t, "claude --model opus", cfg.GetProgram(),
		"and still resolves to the profile's command rather than a raw fallthrough")
	// Without the carry, DefaultProgram would still read "claude", match no record, and
	// GetProgram would fall through to running the bare name as a shell command — a different
	// program, chosen by nobody.
}

// TestRenamingANonDefaultProfileLeavesDefaultProgramAlone is the negative control that makes
// the test above mean something: the carry is conditional on the record being the default, not
// unconditional.
func TestRenamingANonDefaultProfileLeavesDefaultProgramAlone(t *testing.T) {
	cfg := threeProfiles()
	o := NewSettingsOverlay(cfg)
	o.SetSize(100, 32)
	profilesAt(t, o)
	_, _ = o.HandleKeyPress(keyRunes("j")) // aider, not the default

	_, _ = o.HandleKeyPress(keyRunes("e"))
	_, _ = o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyCtrlU})
	typeProfile(o, "aider2")
	_, _ = o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEnter})

	assert.Equal(t, "claude", cfg.DefaultProgram, "an unrelated rename must not move the pointer")
}

// TestProfileFormFitsEveryGeometry. The form is three lines — a heading and the two fields —
// which is exactly paneHeight()'s floor (settingsMinBody), so it survives every terminal the
// panel supports without a shedding ladder. Both field labels must be present at every size:
// the Program field is the one that decides what launches, and it is the line a stacked
// label-above-input layout would lose first.
// It sweeps BOTH a new form and a seeded edit form. Nothing bounds a form line the way
// composeRowLine bounds a row line, so the width assertion here is a real one rather than a
// tautology — and the seeded case is the one that catches it: textinput only recomputes its
// visible window from Update or setCursor, so an edit form built by SetValue while Width is
// still 0 emits its ENTIRE value until the user types. Measured before the fix: a 66-cell line
// in a 54-cell pane at 60 columns. A sweep over the empty form alone cannot see that.
func TestProfileFormFitsEveryGeometry(t *testing.T) {
	long := "aider --model ollama_chat/gemma3:1b --no-auto-commits --dark-mode"
	forms := map[string]func() *profileForm{
		"new":  func() *profileForm { return newProfileForm(-1, "", "") },
		"edit": func() *profileForm { return newProfileForm(1, "aider", long) },
	}
	checked := 0
	for kind, build := range forms {
		for _, h := range []int{settingsVChrome + settingsMinBody, 16, 24, 40} {
			for w := 40; w <= 200; w += 7 {
				o := NewSettingsOverlay(threeProfiles())
				o.SetSize(w, h)
				o.SetRailIndex(profilesRailIndex())
				o.focus = focusRows
				o.profileForm = build()

				paneW := o.rowsPaneWidth()
				lines := o.rowsPaneContent(paneW)
				require.LessOrEqualf(t, len(lines), o.paneHeight(),
					"%s %dx%d: the form must fit the pane rather than scroll", kind, w, h)
				joined := ""
				for _, l := range lines {
					plain := stripANSI(l.text)
					assert.LessOrEqualf(t, ansi.StringWidth(plain), paneW,
						"%s %dx%d: a form line overflows the pane: %q", kind, w, h, plain)
					joined += plain + "\n"
				}
				assert.Containsf(t, joined, profileNameLabel, "%s %dx%d: the Name field must be visible", kind, w, h)
				assert.Containsf(t, joined, profileProgramLabel, "%s %dx%d: the Program field must be visible", kind, w, h)
				checked++
			}
		}
	}
	require.Greater(t, checked, 180, "the sweep must actually visit both forms at every geometry")
}

// TestFormHeadingNamesWhichOperationItIs — "New profile" vs "Edit profile" is the only thing on
// screen distinguishing an append from a replace, and getting it wrong is how a user overwrites
// a record they meant to add beside.
func TestFormHeadingNamesWhichOperationItIs(t *testing.T) {
	o := NewSettingsOverlay(threeProfiles())
	o.SetSize(100, 32)
	profilesAt(t, o)

	_, _ = o.HandleKeyPress(keyRunes("n"))
	assert.Contains(t, strings.Join(paneText(o), " "), "New profile")

	_, _ = o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEsc})
	_, _ = o.HandleKeyPress(keyRunes("e"))
	assert.Contains(t, strings.Join(paneText(o), " "), "Edit profile")
}

// TestFormHintNamesItsOwnKeys — the form is a fourth Esc level, and the hint row is the only
// place saying so (spec §15: differing hints per focus, not one static string).
func TestFormHintNamesItsOwnKeys(t *testing.T) {
	o := NewSettingsOverlay(threeProfiles())
	o.SetSize(100, 32)
	profilesAt(t, o)
	_, _ = o.HandleKeyPress(keyRunes("n"))

	hint := stripANSI(o.hintLine())
	assert.Contains(t, hint, "esc cancel", "the form's esc cancels rather than backing out")
	assert.Contains(t, hint, "↵ save")
	assert.Contains(t, hint, "⇥ field", "tab switches fields here, not panes")
	assert.NotContains(t, hint, "…", "the ladder must fit rather than be truncated")
}
```

- [ ] **Step 2: Run the tests to verify they fail**

```bash
PATH="/home/zvi/.local/share/mise/installs/go/1.26.4/bin:$PATH" \
  go test ./ui/overlay/ -run 'Profile|Form' 2>&1 | head -20
```
Expected: FAIL — `n` and `e` are unbound, so `o.profileForm` stays nil.

- [ ] **Step 3: Implement the form type**

In `ui/overlay/settings_profiles.go`:

```go
// The form's fields, as slice indices. profileFieldCount closes the block so nav wraps on the
// real length rather than a literal, exactly as accountForm keys off len(inputs).
const (
	fldProfileName = iota
	fldProfileProgram
	profileFieldCount
)

// The field labels, shared by the renderer and its width guard. They are the label column of
// the form's two lines, so profileProgramLabel — the longer — sets that column's width.
const (
	profileNameLabel    = "Name"
	profileProgramLabel = "Program"
)

// profileForm is the add/edit sub-form for one config.Profile. It works purely in strings; the
// panel validates and builds the record on submit, exactly as AccountsOverlay does for
// accountForm.
//
// editIndex is -1 for a new profile and the cfg.Profiles index for an edit — the single
// sentinel that drives both validateProfile's self-exclusion and commitProfile's
// append-vs-replace.
type profileForm struct {
	inputs    [profileFieldCount]textinput.Model
	focus     int
	editIndex int
}

// newProfileForm builds the form, seeded for an edit or empty for a new record.
func newProfileForm(editIndex int, name, program string) *profileForm {
	f := &profileForm{editIndex: editIndex}
	f.inputs[fldProfileName] = newFieldInput("e.g. codex")
	f.inputs[fldProfileProgram] = newFieldInput("e.g. claude --model opus")
	f.inputs[fldProfileName].SetValue(name)
	f.inputs[fldProfileProgram].SetValue(program)
	f.applyFocus()
	return f
}

// applyFocus focuses exactly one input and blurs the rest, leaving the cursor at the end so a
// seeded field can be appended to rather than overtyped (accountForm.applyFocus's contract, and
// what lets a test tab into a field and type).
func (f *profileForm) applyFocus() {
	for i := range f.inputs {
		if i == f.focus {
			f.inputs[i].Focus()
			f.inputs[i].CursorEnd()
			continue
		}
		f.inputs[i].Blur()
	}
}

// setWidth sizes both inputs and re-windows any seeded value against the new width.
//
// The re-window is not optional. textinput only recomputes its visible window from Update or
// setCursor, and newProfileForm calls SetValue + CursorEnd while Width is still 0 — so an EDIT
// form emits its whole value regardless of Width until the user types. Measured: a 60-column
// terminal rendered a 66-cell line into a 54-cell pane, which lipgloss soft-wraps, growing the
// box and clipping the pinned hint. settings.go's startEdit avoids this by setting Width before
// CursorEnd; this form cannot, because it does not know the pane width until render.
//
// SetCursor(Position()) is the no-op that forces that recompute.
func (f *profileForm) setWidth(w int) {
	for i := range f.inputs {
		if f.inputs[i].Width == w {
			continue
		}
		f.inputs[i].Width = w
		f.inputs[i].SetCursor(f.inputs[i].Position())
	}
}

func (f *profileForm) name() string    { return strings.TrimSpace(f.inputs[fldProfileName].Value()) }
func (f *profileForm) program() string { return strings.TrimSpace(f.inputs[fldProfileProgram].Value()) }
```

- [ ] **Step 4: Implement the key handling, validation and commit**

```go
// handleProfileFormKey routes a key while the record form is open — the editor's own Esc level,
// above the panel's three (spec §15's ladder, extended by spec §9's editor).
//
// Enter validates before committing and, on failure, leaves the form open with the message in
// the help pane: a rejected save must be fixable in place rather than thrown away, and every
// subsequent Enter must still reach validation (the accounts overlay writes that as resetting
// its own `submitted` flag; here the flag does not exist, so the property is structural).
//
// Everything the switch does not name goes to the focused input, which is why j/k/d/n/D are
// letters in a form — the rule the settings line editor and the `/` filter also follow.
func (s *SettingsOverlay) handleProfileFormKey(msg tea.KeyMsg) (changedKey string) {
	f := s.profileForm
	switch msg.String() {
	case "esc", "ctrl+c":
		s.profileForm = nil
		s.lastErr = ""
		return ""
	case "tab":
		f.focus = wrapIndex(f.focus, +1, len(f.inputs))
		f.applyFocus()
		return ""
	case "shift+tab":
		f.focus = wrapIndex(f.focus, -1, len(f.inputs))
		f.applyFocus()
		return ""
	case "enter":
		if err := s.validateProfile(); err != "" {
			s.lastErr = err
			return ""
		}
		changed := s.commitProfile()
		s.profileForm = nil
		s.lastErr = ""
		return changed
	default:
		f.inputs[f.focus], _ = f.inputs[f.focus].Update(msg)
		return ""
	}
}

// validateProfile rejects an empty name, an empty program, and a name another record already
// uses. It returns "" when valid, matching AccountsOverlay.validate — a string rather than an
// error because it is rendered prose, not a wrapped failure.
//
// A program is required where the accounts form lets a config dir be blank: an empty program is
// not "inherit the ambient default", it is a session that launches nothing.
//
// `i != f.editIndex` is the self-exclusion: an unrenamed edit is not a duplicate of itself,
// while renaming ONTO another record still fails. With editIndex -1 the exclusion never fires,
// so a new record is checked against every existing one.
//
// ORDER MATTERS: name-empty, then duplicate-name, then program-empty. A user fills the form top
// to bottom, so at the moment they type a colliding name the program field is still empty —
// checking the program first answers a question they have not reached yet and hides the one
// thing wrong with what they HAVE typed. The more specific error wins.
func (s *SettingsOverlay) validateProfile() string {
	f := s.profileForm
	name := f.name()
	if name == "" {
		return "A profile needs a name."
	}
	for i, p := range s.cfg.Profiles {
		if i != f.editIndex && p.Name == name {
			return "A profile named " + strconv.Quote(name) + " already exists."
		}
	}
	if f.program() == "" {
		return "A profile needs a program — the shell command that launches the agent."
	}
	return ""
}

// commitProfile writes the form back into cfg.Profiles and reports the changed key.
//
// A rename carries default_program with it. That pointer is a NAME, so leaving it behind would
// silently change what new sessions launch: it would stop matching any profile and
// config.GetProgram would fall through to running the old name as a raw shell command. Deleting
// that record has no successor that preserves anything, which is why delete refuses instead —
// see armProfileDelete.
//
// The whole struct is replaced, so any config.Profile field this form does not show would be
// destroyed. Profile is {Name, Program} today and the form shows both; the moment it grows a
// third field, carry it across here — the lesson AccountsOverlay.commit records for
// ExpectAccount.
//
// Unlike resetRow there is no before/after comparison: this is one deliberate Enter rather than
// a repeatable key, so an unchanged save costs one write instead of a rewrite per keypress.
func (s *SettingsOverlay) commitProfile() string {
	f := s.profileForm
	p := config.Profile{Name: f.name(), Program: f.program()}
	if f.editIndex < 0 {
		s.cfg.Profiles = append(s.cfg.Profiles, p)
		s.profileCursor = len(s.cfg.Profiles) - 1
		return profilesChangedKey
	}
	if s.cfg.Profiles[f.editIndex].Name == s.cfg.DefaultProgram {
		s.cfg.DefaultProgram = p.Name
	}
	s.cfg.Profiles[f.editIndex] = p
	return profilesChangedKey
}
```

- [ ] **Step 5: Implement the form's renderer**

```go
// profileFormLines renders the record form inside the rows pane: one line per field, the label
// in the pane's own label column and the input in its value column.
//
// Label BESIDE input rather than accountForm's label-above-input, because this form lives in a
// pane whose height floor is settingsMinBody (3): stacked it needs five lines and would be
// clipped on a short terminal, where Program — the field that decides what launches — is the
// line that disappears. Beside, the whole form is exactly three lines at every geometry the
// panel supports.
func (s *SettingsOverlay) profileFormLines(width int) []paneLine {
	t := theme.Current()
	f := s.profileForm
	labelW := ansi.StringWidth(profileProgramLabel)
	// The trailing -1 is the cursor cell: textinput.Model.View() renders Width + 1, on both the
	// value and the placeholder path. Without it every form line is one cell over the pane at
	// every geometry, which lipgloss soft-wraps.
	f.setWidth(max(10, width-rowMarkerCells-labelW-rowLabelGap-1))

	heading := "New profile"
	if f.editIndex >= 0 {
		heading = "Edit profile"
	}
	lines := []paneLine{{text: t.DimStyle().Bold(true).Render(heading), rowIdx: -1}}
	for i, label := range []string{profileNameLabel, profileProgramLabel} {
		style := t.DimStyle()
		if i == f.focus {
			style = t.AccentStyle()
		}
		// Pad the PLAIN label before styling, so the padding carries the style — the order
		// renderRowLine:515 uses. (padRight measures with lipgloss.Width, which ignores escape
		// bytes, so padding a styled string would also align correctly; this is about the
		// rendered result, not about miscounting.)
		lines = append(lines, paneLine{
			text: strings.Repeat(" ", rowMarkerCells) +
				style.Render(padRight(label, labelW)) +
				strings.Repeat(" ", rowLabelGap) +
				f.inputs[i].View(),
			rowIdx: -1,
		})
	}
	return lines
}
```

> `textinput.Model.View()` pads to `Width`; confirm during Step 6 that the rendered line is
> `rowMarkerCells + labelW + rowLabelGap + Width` cells and adjust `setWidth`'s subtraction if
> the widget reserves a cell for its cursor. `TestProfileFormFitsEveryGeometry` is what fails if
> it does.

- [ ] **Step 6: Bind `n` and `e`/`↵`, and add the form's hint rungs**

In `handleProfilesKey`'s switch:

```go
	case "n":
		s.profileForm = newProfileForm(-1, "", "")
	case "e", "enter":
		if len(s.cfg.Profiles) > 0 {
			p := s.cfg.Profiles[s.profileCursor]
			s.profileForm = newProfileForm(s.profileCursor, p.Name, p.Program)
		}
```

In `hintLine`, after `case s.editing:`:

```go
	case s.profileForm != nil:
		ladder = []string{"⇥ field · ↵ save · esc cancel", "↵ save · esc cancel", "esc cancel"}
```

and extend the Task 2 pane ladder's rungs with `n new · ↵ edit`:

```go
	return []string{
		"↑/↓ move · n new · ↵ edit · / search · ⇥ pane · esc back",
		"↑/↓ move · n new · ↵ edit · / search · esc back",
		"↑/↓ · n new · ↵ edit · esc back",
		"esc back",
	}
```

(`d delete` and `D detect` join in Tasks 4 and 5, with the keys that make them work.)

- [ ] **Step 7: Run the tests to verify they pass**

```bash
PATH="/home/zvi/.local/share/mise/installs/go/1.26.4/bin:$PATH" \
  go test ./ui/overlay/ -run 'Profile|Form' -v 2>&1 | tail -30
```
Expected: PASS. If `TestFormHintNamesItsOwnKeys` reports a truncated hint, re-measure the rungs
against `innerWidth()` at 100×32 (92 cells) and record the real numbers in Derived numbers.

- [ ] **Step 8: Verify the guards fail when they should**

1. Drop the `i != f.editIndex` self-exclusion from `validateProfile`. Expected:
   `TestEditingWithoutRenamingIsNotADuplicateOfItself` FAILS on the first Enter.
2. Make `commitProfile` always append (delete the `editIndex < 0` branch's guard). Expected:
   `TestEditProfileReplacesInPlace` FAILS on the length.
3. Delete the `DefaultProgram` carry in `commitProfile`. Expected:
   `TestRenamingTheDefaultProfileCarriesDefaultProgramWithIt` FAILS on `GetProgram()` — and
   confirm the failure is on the *resolution*, not only on the stored name, because the
   resolution is the behavior the carry protects.
4. Make the carry unconditional (`s.cfg.DefaultProgram = p.Name` with no `if`). Expected:
   `TestRenamingANonDefaultProfileLeavesDefaultProgramAlone` FAILS.
5. Drop `handleProfileFormKey`'s `default:` arm so unhandled keys fall through to the pane.
   Expected: `TestFormSwallowsNavigationKeys` FAILS.
6. Remove the `-1` from `setWidth`'s argument in `profileFormLines`. Expected:
   `TestProfileFormFitsEveryGeometry` FAILS at **every** geometry by exactly one cell.
7. Remove the `SetCursor(f.inputs[i].Position())` re-window from `setWidth`. Expected:
   `TestProfileFormFitsEveryGeometry` FAILS on the **`edit`** half only, and by a large margin
   rather than by one cell. If the `new` half also fails, the two defects have been conflated —
   they are independent, and the sweep distinguishes them by which subtest name appears.
8. Move `validateProfile`'s program-empty check back above the duplicate-name loop. Expected:
   `TestFormValidationRejectsAndStaysOpen` FAILS on the "already exists" assertion.
9. Re-run and confirm green.

- [ ] **Step 9: Lint and commit**

```bash
PATH="/home/zvi/go/bin:$PATH" mise exec -- just lint ./ui/... ./app/...
git status --short
git add ui/overlay/settings_profiles.go ui/overlay/settings_profiles_test.go \
  ui/overlay/settings_render.go
git commit -m "feat(settings): add and edit agent profiles from the panel"
```

---

## Task 4: `d` delete — the confirmation, the `default_program` guard, the cursor clamp

**Files:**
- Modify: `ui/overlay/settings_profiles.go` (`armProfileDelete`, `handleProfileConfirmKey`, the
  `d` arm, the confirm's help prose and hint rungs)
- Modify: `ui/overlay/settings_render.go` (`profilesHelp`'s confirm arm is already routed;
  nothing else)
- Modify: `ui/overlay/settings_profiles_test.go`

**Interfaces:**
- Consumes: `clampProfileCursor` (Task 2), `profilesHelp` (Task 2).
- Produces: `(*SettingsOverlay).armProfileDelete()`,
  `(*SettingsOverlay).handleProfileConfirmKey(tea.KeyMsg) (changedKey string)`.

Guard 12's middle clause — **the one behavior a passing test can most easily misrepresent**, so
it gets both a positive and a negative control and an end-to-end drive in Task 7.

- [ ] **Step 1: Write the failing tests**

Append to `ui/overlay/settings_profiles_test.go`:

```go
// TestDeleteAsksBeforeRemoving. Deleting a record is the first irreversible action in this
// panel — r restores a default and an enum cycle is reversible, this is not — and the sibling
// record editor over the same config file confirms too. d alone must change nothing.
func TestDeleteAsksBeforeRemoving(t *testing.T) {
	cfg := threeProfiles()
	o := NewSettingsOverlay(cfg)
	o.SetSize(100, 32)
	profilesAt(t, o)
	_, _ = o.HandleKeyPress(keyRunes("j")) // aider, not the default

	_, changed := o.HandleKeyPress(keyRunes("d"))
	assert.Empty(t, changed, "arming the confirmation changes no config")
	require.True(t, o.profileConfirm)
	assert.Len(t, cfg.Profiles, 3, "d alone deletes nothing")

	prose, danger := o.profilesHelp()
	assert.True(t, danger, "the prompt is a warning, and the help pane must paint it as one")
	assert.Contains(t, prose, "aider", "the prompt names the record it is about to destroy")

	hint := stripANSI(o.hintLine())
	assert.Contains(t, hint, "y delete")
	assert.Contains(t, hint, "n cancel")
	assert.NotContains(t, hint, "…")
}

// TestConfirmDeletesAndReportsTheKey — y (and ↵) removes the record and reports "profiles", so
// home persists through the panel's one writer.
func TestConfirmDeletesAndReportsTheKey(t *testing.T) {
	for _, key := range []tea.KeyMsg{keyRunes("y"), {Type: tea.KeyEnter}} {
		cfg := threeProfiles()
		o := NewSettingsOverlay(cfg)
		o.SetSize(100, 32)
		profilesAt(t, o)
		_, _ = o.HandleKeyPress(keyRunes("j"))
		_, _ = o.HandleKeyPress(keyRunes("d"))

		_, changed := o.HandleKeyPress(key)
		assert.Equal(t, profilesChangedKey, changed)
		assert.False(t, o.profileConfirm, "the prompt closes")
		require.Len(t, cfg.Profiles, 2)
		assert.Equal(t, []string{"claude", "codex"}, profileNames(cfg))
	}
}

// TestCancelKeepsTheProfile: n, esc and ctrl+c all back out, and every OTHER key is ignored
// rather than treated as a cancel — a stray press must not confirm, and must not silently
// disarm either (the accounts overlay's rule).
func TestCancelKeepsTheProfile(t *testing.T) {
	for _, key := range []tea.KeyMsg{keyRunes("n"), {Type: tea.KeyEsc}, {Type: tea.KeyCtrlC}} {
		cfg := threeProfiles()
		o := NewSettingsOverlay(cfg)
		o.SetSize(100, 32)
		profilesAt(t, o)
		// Off the default record first: d on THAT one is refused and never arms, so a loop
		// starting at index 0 would be testing the guard rather than the cancel.
		_, _ = o.HandleKeyPress(keyRunes("j"))
		_, _ = o.HandleKeyPress(keyRunes("d"))
		require.True(t, o.profileConfirm)

		closed, changed := o.HandleKeyPress(key)
		assert.False(t, closed, "cancelling a delete must not close the panel")
		assert.Empty(t, changed)
		assert.False(t, o.profileConfirm)
		assert.Len(t, cfg.Profiles, 3)
	}

	cfg := threeProfiles()
	o := NewSettingsOverlay(cfg)
	o.SetSize(100, 32)
	profilesAt(t, o)
	_, _ = o.HandleKeyPress(keyRunes("j"))
	_, _ = o.HandleKeyPress(keyRunes("d"))
	_, changed := o.HandleKeyPress(keyRunes("z"))
	assert.Empty(t, changed)
	assert.True(t, o.profileConfirm, "an unrecognized key leaves the prompt up")
	assert.Len(t, cfg.Profiles, 3)
}

// TestDeletingTheLastProfileClampsTheCursor is the off-by-one clampProfileCursor exists for.
// The splice shortens the list while the cursor stays put, so deleting the LAST record leaves
// it one past the end — and the very next render, or the next d, indexes out of range and
// panics. Deleting a middle record needs no clamp, which is why this test deletes the last one.
func TestDeletingTheLastProfileClampsTheCursor(t *testing.T) {
	cfg := threeProfiles()
	o := NewSettingsOverlay(cfg)
	o.SetSize(100, 32)
	profilesAt(t, o)
	_, _ = o.HandleKeyPress(keyRunes("j"))
	_, _ = o.HandleKeyPress(keyRunes("j"))
	require.Equal(t, 2, o.profileCursor, "precondition: on the last record")

	_, _ = o.HandleKeyPress(keyRunes("d"))
	_, _ = o.HandleKeyPress(keyRunes("y"))

	assert.Equal(t, 1, o.profileCursor, "the cursor clamps onto the new last record")
	assert.NotPanics(t, func() { _ = o.Render() }, "and the very next render is safe")
	// The next d must also be safe: the confirm prompt indexes the list to name the record.
	assert.NotPanics(t, func() {
		_, _ = o.HandleKeyPress(keyRunes("d"))
		_, _ = o.HandleKeyPress(keyRunes("y"))
	})
	assert.Len(t, cfg.Profiles, 1)
}

// TestDeletingAMiddleProfileLandsOnTheNextOne is the negative control that keeps the clamp from
// being written as "always move up": deleting from the middle should leave the cursor where it
// is, which now points at what was the following record.
func TestDeletingAMiddleProfileLandsOnTheNextOne(t *testing.T) {
	cfg := threeProfiles()
	o := NewSettingsOverlay(cfg)
	o.SetSize(100, 32)
	profilesAt(t, o)
	_, _ = o.HandleKeyPress(keyRunes("j")) // aider

	_, _ = o.HandleKeyPress(keyRunes("d"))
	_, _ = o.HandleKeyPress(keyRunes("y"))

	assert.Equal(t, 1, o.profileCursor)
	assert.Equal(t, "codex", cfg.Profiles[o.profileCursor].Name,
		"the cursor stays put and now points at what followed")
}

// TestDeletingTheOnlyProfileIsSafe — the n == 0 case, where a naive clamp yields -1.
func TestDeletingTheOnlyProfileIsSafe(t *testing.T) {
	cfg := profilesCfg("something-else", config.Profile{Name: "solo", Program: "solo"})
	o := NewSettingsOverlay(cfg)
	o.SetSize(100, 32)
	profilesAt(t, o)

	_, _ = o.HandleKeyPress(keyRunes("d"))
	_, changed := o.HandleKeyPress(keyRunes("y"))

	assert.Equal(t, profilesChangedKey, changed)
	assert.Empty(t, cfg.Profiles)
	assert.Equal(t, 0, o.profileCursor)
	assert.NotPanics(t, func() { _ = o.Render() })
	assert.Contains(t, strings.Join(paneText(o), " "), "No profiles yet",
		"an emptied list falls back to the empty-state line")
}

// TestDeletingTheDefaultProfileIsRefused is spec §13's guard 12, the clause a passing test can
// most easily misrepresent. The refusal must be all three of: nothing removed, nothing reported
// as changed (so no SaveConfig runs), and a message naming the SETTING — because the user has
// to go somewhere else to clear it, and "Default program" is the label they will be looking for.
//
// The message leads with that label so the help pane's truncation ladder cannot eat it.
func TestDeletingTheDefaultProfileIsRefused(t *testing.T) {
	cfg := threeProfiles()
	o := NewSettingsOverlay(cfg)
	o.SetSize(100, 32)
	profilesAt(t, o)
	require.Equal(t, cfg.Profiles[o.profileCursor].Name, cfg.DefaultProgram,
		"precondition: the cursor is on the profile default_program names")

	closed, changed := o.HandleKeyPress(keyRunes("d"))

	assert.False(t, closed)
	assert.Empty(t, changed, "a refused delete must not reach SaveConfig")
	assert.False(t, o.profileConfirm, "and must not even arm the confirmation")
	assert.Len(t, cfg.Profiles, 3, "nothing was removed")
	assert.True(t, strings.HasPrefix(o.lastErr, "Default program"),
		"the refusal leads with the setting's own label: %q", o.lastErr)
	assert.Contains(t, o.lastErr, "under Sessions",
		"with alternatives available, the refusal names where to go")
}

// TestRefusingTheOnlyProfileNamesAnActionThatWorks. With exactly one profile, "change it under
// Sessions first" is advice the panel makes impossible to follow: default_program's options are
// the profile names plus the captured raw command, so a single profile means a single option,
// and cycleEnum returns early on len(opts) < 2 with no error, no inert chip and no reset — a
// silent dead key.
//
// This is not an exotic state. seededDefaultConfig sets DefaultProgram = Profiles[0].Name, so a
// machine with only claude installed lands here on first run. The refusal has to name an action
// that exists.
func TestRefusingTheOnlyProfileNamesAnActionThatWorks(t *testing.T) {
	cfg := profilesCfg("solo", config.Profile{Name: "solo", Program: "solo --flag"})
	o := NewSettingsOverlay(cfg)
	o.SetSize(100, 32)
	profilesAt(t, o)

	// The premise, asserted rather than assumed: the row this refusal would point at cannot move.
	settingsAt(t, o, "default_program")
	require.Len(t, o.rows[o.cursor].options(cfg), 1, "precondition: one profile, one option")
	_, changed := o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRight})
	require.Empty(t, changed, "precondition: cycling a single-option enum is a silent no-op")

	profilesAt(t, o)
	_, changed = o.HandleKeyPress(keyRunes("d"))

	assert.Empty(t, changed)
	assert.False(t, o.profileConfirm)
	assert.Len(t, cfg.Profiles, 1)
	assert.True(t, strings.HasPrefix(o.lastErr, "Default program"), "%q", o.lastErr)
	assert.NotContains(t, o.lastErr, "under Sessions",
		"that row cannot be changed here, so the refusal must not send the user to it")
	assert.Contains(t, o.lastErr, "with n", "it names the key that creates the alternative instead")
}

// TestTheRefusalFitsTheHelpPaneAtEveryWidth. A guard message the user cannot read is not a
// guard, and this one is prose in a FIXED-height pane: helpLines wraps lastErr to innerWidth,
// keeps the first proseBudget lines and marks the cut with an ellipsis.
//
// Asserting that "Default program" survives would prove nothing — it leads the sentence, so it
// is always in line 0 whatever the cap does. Measured, a 211-cell message left that assertion
// green. The falsifiable half is the TAIL plus the absence of an ellipsis, and the margin is
// real: at 40 columns (inner 34) the 71-cell refusal wraps to exactly 3 lines against a
// proseBudget of 3. One more clause and it is cut. That is the same zero-margin-at-the-floor
// shape PR C measured for the handoff notes.
func TestTheRefusalFitsTheHelpPaneAtEveryWidth(t *testing.T) {
	for w := 40; w <= 200; w++ {
		cfg := threeProfiles()
		o := NewSettingsOverlay(cfg)
		o.SetSize(w, 24)
		o.SetRailIndex(profilesRailIndex())
		o.focus = focusRows
		_, _ = o.HandleKeyPress(keyRunes("d"))
		require.NotEmptyf(t, o.lastErr, "%d: the delete must be refused", w)

		help := stripANSI(strings.Join(o.helpLines(), " "))
		assert.Containsf(t, help, "Default program", "%d: the refusal lost the setting's name: %q", w, help)
		assert.NotContainsf(t, help, "…", "%d: the refusal was cut by the help pane's cap: %q", w, help)
		assert.Containsf(t, help, "first.", "%d: the refusal lost its tail: %q", w, help)
	}
}

// TestARefusalDoesNotOutliveTheNextKey. lastErr is cleared at the top of handleProfilesKey, so
// the message describes the key that produced it and nothing later — a stale refusal sitting
// under a different record would be read as being about that record.
func TestARefusalDoesNotOutliveTheNextKey(t *testing.T) {
	o := NewSettingsOverlay(threeProfiles())
	o.SetSize(100, 32)
	profilesAt(t, o)
	_, _ = o.HandleKeyPress(keyRunes("d"))
	require.NotEmpty(t, o.lastErr)

	_, _ = o.HandleKeyPress(keyRunes("j"))
	assert.Empty(t, o.lastErr, "moving off the record clears its refusal")
}

// TestDeletingAProfileWhileDefaultProgramIsARawCommand is the guard's OTHER direction: the
// refusal is about the pointer, not about being first in the list. With default_program holding
// a raw command that matches no record, every record is deletable.
func TestDeletingAProfileWhileDefaultProgramIsARawCommand(t *testing.T) {
	cfg := profilesCfg("/home/user/launch-claude.sh",
		config.Profile{Name: "claude", Program: "claude"},
		config.Profile{Name: "codex", Program: "codex"},
	)
	o := NewSettingsOverlay(cfg)
	o.SetSize(100, 32)
	profilesAt(t, o)

	_, _ = o.HandleKeyPress(keyRunes("d"))
	require.True(t, o.profileConfirm, "no record is the default, so none is protected")
	_, changed := o.HandleKeyPress(keyRunes("y"))

	assert.Equal(t, profilesChangedKey, changed)
	assert.Equal(t, []string{"codex"}, profileNames(cfg))
	assert.Equal(t, "/home/user/launch-claude.sh", cfg.DefaultProgram, "and the raw command is untouched")
}
```

Add the shared helper beside `profilesCfg`:

```go
// profileNames collapses the list to its names so an ordering assertion is one require.Equal.
func profileNames(cfg *config.Config) []string {
	out := make([]string, len(cfg.Profiles))
	for i, p := range cfg.Profiles {
		out[i] = p.Name
	}
	return out
}
```

- [ ] **Step 2: Run the tests to verify they fail**

```bash
PATH="/home/zvi/.local/share/mise/installs/go/1.26.4/bin:$PATH" \
  go test ./ui/overlay/ -run 'Delet|Refus|Cancel|Confirm' 2>&1 | head -20
```
Expected: FAIL — `d` is unbound, so `o.profileConfirm` stays false and nothing is removed. (Note
`TestDeletingTheDefaultProfileIsRefused` would *pass vacuously* at this point: nothing is
deleted because nothing is bound. Its `assert.True(strings.HasPrefix(o.lastErr, …))` is the half
that fails, which is why the message assertion is not optional.)

- [ ] **Step 3: Implement the guard and the confirmation**

In `ui/overlay/settings_profiles.go`:

```go
// armProfileDelete refuses when the highlighted record is the one default_program names, and
// otherwise arms the confirmation.
//
// Refusing is spec §9's guard 12, taken over the repoint alternative. default_program lives in
// another category, so a silent repoint would change what every new session launches from a
// pane that cannot show the change; and there is no successor record that preserves the
// launch command, unlike a rename (see commitProfile). It is also the panel's existing voice
// for a value it will not silently rewrite — project_search_depth refuses a value past the
// accessor's clamp rather than echoing back a number the accessor ignores.
//
// The message leads with the setting's own label, because the help pane caps prose at
// helpHeight() lines with a tail ellipsis and that label is the one word the user needs to find
// the row.
// The one-profile wording is not politeness. default_program's options are the profile names
// plus the captured raw command, and cycleEnum returns early on a single-option enum with no
// error, no inert chip and no reset — a silent dead key. seededDefaultConfig points
// default_program at Profiles[0], so a machine with one agent installed lands in exactly that
// state on first run, and "change it under Sessions first" would send that user to a row the
// panel makes impossible to change. Name the action that actually works instead.
func (s *SettingsOverlay) armProfileDelete() {
	if s.cfg.Profiles[s.profileCursor].Name != s.cfg.DefaultProgram {
		s.profileConfirm = true
		return
	}
	if len(s.cfg.Profiles) == 1 {
		s.lastErr = "Default program points at your only profile — add another with n first."
		return
	}
	s.lastErr = "Default program points at this profile — change it under Sessions first."
}

// handleProfileConfirmKey routes the delete confirmation. y or ↵ deletes; n, esc or ctrl+c backs
// out; every other key is ignored, so a stray press can neither confirm nor silently disarm
// (the accounts overlay's rule).
func (s *SettingsOverlay) handleProfileConfirmKey(msg tea.KeyMsg) (changedKey string) {
	switch msg.String() {
	case "y", "enter":
		s.profileConfirm = false
		i := s.profileCursor
		s.cfg.Profiles = append(s.cfg.Profiles[:i], s.cfg.Profiles[i+1:]...)
		s.clampProfileCursor()
		return profilesChangedKey
	case "n", "esc", "ctrl+c":
		s.profileConfirm = false
	}
	return ""
}
```

Add the `d` arm to `handleProfilesKey`'s switch, beside `e`:

```go
	case "d":
		if len(s.cfg.Profiles) > 0 {
			s.armProfileDelete()
		}
```

Add the confirm's prose to `profilesHelp`, as its **first** case — it outranks the note and the
form, both of which are impossible while it is armed, and stating the order makes that
structural:

```go
	case s.profileConfirm:
		return "Delete profile " + strconv.Quote(s.cfg.Profiles[s.profileCursor].Name) +
			"? This cannot be undone.", true
```

The confirm's hint rungs are already in `profilesHintLadder` from Task 2. Extend the list rungs
with `d delete`:

```go
	return []string{
		"↑/↓ move · n new · ↵ edit · d delete · / search · ⇥ pane · esc back",
		"↑/↓ move · n new · ↵ edit · d delete · / search · esc back",
		"↑/↓ · n new · ↵ edit · d delete · esc back",
		"n new · ↵ edit · d delete · esc back",
		"esc back",
	}
```

- [ ] **Step 4: Run the tests to verify they pass**

```bash
PATH="/home/zvi/.local/share/mise/installs/go/1.26.4/bin:$PATH" \
  go test ./ui/overlay/ -run 'Delet|Refus|Cancel|Confirm' -v 2>&1 | tail -30
```
Expected: PASS.

- [ ] **Step 5: Verify the guards fail when they should**

1. Delete the `Name == DefaultProgram` branch from `armProfileDelete`. Expected:
   `TestDeletingTheDefaultProfileIsRefused` FAILS on **all four** assertions — read them, because
   the one that matters most is `changed` being non-empty (a delete that reached SaveConfig).
2. Change the refusal to `"Change the default program setting first."` (the label no longer
   leads). Expected: `TestDeletingTheDefaultProfileIsRefused` FAILS on the `HasPrefix`. Restore,
   then append ` Sessions is the category.` to the multi-profile refusal. Expected:
   `TestTheRefusalFitsTheHelpPaneAtEveryWidth` FAILS at the narrow end on the ellipsis or the
   tail — **report the width at which it breaks**, because that number is the wording's real
   budget and the margin at 40 columns is zero lines.
2a. Drop the `len(s.cfg.Profiles) == 1` branch from `armProfileDelete`. Expected:
   `TestRefusingTheOnlyProfileNamesAnActionThatWorks` FAILS on the `NotContains "under Sessions"`
   — i.e. the panel is back to advising an action it makes impossible.
3. Delete the `s.clampProfileCursor()` call. Expected:
   `TestDeletingTheLastProfileClampsTheCursor` FAILS or panics. Either is a pass for the
   mutation; note which, because a panic means the clamp is load-bearing rather than cosmetic.
4. Make `clampProfileCursor` use `len(s.cfg.Profiles)-1` without the `max(0, …)`. Expected:
   `TestDeletingTheOnlyProfileIsSafe` FAILS (cursor -1).
5. Add `default: s.profileConfirm = false` to `handleProfileConfirmKey`. Expected:
   `TestCancelKeepsTheProfile`'s stray-key half FAILS.
6. Make `armProfileDelete` compare against `s.cfg.GetProgram()` instead of `s.cfg.DefaultProgram`
   — the plausible wrong reading, since both are "the default program". Expected:
   `TestDeletingTheDefaultProfileIsRefused` FAILS, because the fixture's default record has a
   name (`claude`) and a command (`claude --model opus`) that differ, so the resolved command
   matches no name and the guard never fires. **If it passes, the fixture has drifted back to a
   coincidence — fix the fixture, not the mutation.**
7. Re-run and confirm green.

- [ ] **Step 6: Lint and commit**

```bash
PATH="/home/zvi/go/bin:$PATH" mise exec -- just lint ./ui/... ./app/...
git status --short
git add ui/overlay/settings_profiles.go ui/overlay/settings_profiles_test.go
git commit -m "feat(settings): delete a profile, unless default_program names it"
```

---

## Task 5: `D` detect — off the update loop, through the seam the CLI already uses

**Files:**
- Modify: `ui/overlay/settings_profiles.go` (`requestProfileDetect`, `TakeProfileDetect`,
  `NoteProfilesDetected`, the `D` arm, the full hint ladder)
- Create: `app/app_profiles.go`
- Modify: `app/app_keys.go` (`handleSettingsState`), `app/app_update.go` (the msg case)
- Modify: `ui/overlay/settings_profiles_test.go`, `app/settings_test.go`

**Interfaces:**
- Consumes: `detectAgents` (`app/app_welcome.go:15`, `var detectAgents = config.DetectAgentProfiles`),
  `(*config.Config).MergeDetectedProfiles(detected []config.Profile) (added []string)`
  (`config/agents.go:51`).
- Produces:
  - `(*SettingsOverlay).TakeProfileDetect() bool` — exported, consumed once per request
  - `(*SettingsOverlay).NoteProfilesDetected(added []string, text string) (shown bool)`
  - `profilesDetectedMsg{detected []config.Profile}`, `(*home).detectProfilesCmd() tea.Cmd`,
    `profilesDetectedText(added []string) string`

**The merge lives in `home`, not in the overlay.** `home` owns the config and the persist path,
and the merge must happen whether or not the panel is still on the editor — the overlay's only
job is to report the outcome *where the user is looking*, and to say when it cannot.

Guard 12's fourth clause. **`D` cannot run inline.** `config.DetectAgentProfiles` probes claude
through `config.GetClaudeCommand`, which spawns `$SHELL -c "source ~/.zshrc …; which claude"`
under a **10-second** timeout (`config/detect.go:13-46`). A synchronous call in `HandleKeyPress`
blocks the Bubble Tea update loop — and with it every session's status poll — for as long as
that takes. So the overlay records a *request* and `home` fulfils it, the same shape the
Accounts handoff uses: the panel cannot run a command itself.

It goes through `app`'s existing `detectAgents` seam, which is already what the startup agent
check runs, so `D` and `atrium profiles detect` can never probe differently; the merge half is
`MergeDetectedProfiles`, whose documented contract is that existing profiles and
`DefaultProgram` are never modified.

- [ ] **Step 1: Write the failing tests**

Append to `ui/overlay/settings_profiles_test.go`:

```go
// TestDetectRequestsRatherThanProbing. D must not call into config detection itself: the claude
// probe spawns a login shell under a ten-second timeout, and running it inside HandleKeyPress
// freezes the update loop and every session's poll with it. The key records a request, says so,
// and returns immediately.
func TestDetectRequestsRatherThanProbing(t *testing.T) {
	o := NewSettingsOverlay(threeProfiles())
	o.SetSize(100, 32)
	profilesAt(t, o)

	closed, changed := o.HandleKeyPress(keyRunes("D"))
	assert.False(t, closed)
	assert.Empty(t, changed, "nothing has been detected yet, so nothing has changed")
	prose, danger := o.profilesHelp()
	assert.Contains(t, prose, "Detecting", "the pane says a probe is running")
	assert.False(t, danger)

	assert.True(t, o.TakeProfileDetect(), "home is told to run it")
	assert.False(t, o.TakeProfileDetect(), "and told exactly once")
}

// TestASecondDetectDoesNotQueueASecondProbe: holding D must not spawn a shell per keypress.
func TestASecondDetectDoesNotQueueASecondProbe(t *testing.T) {
	o := NewSettingsOverlay(threeProfiles())
	o.SetSize(100, 32)
	profilesAt(t, o)

	_, _ = o.HandleKeyPress(keyRunes("D"))
	require.True(t, o.TakeProfileDetect())
	_, _ = o.HandleKeyPress(keyRunes("D"))
	assert.False(t, o.TakeProfileDetect(), "a run is already in flight")

	// And the message survives the wait: it is derived from the in-flight flag, not from the
	// one-keypress note, so pressing j while the probe runs must not erase it — nor may the
	// second D, which would otherwise be a key that REMOVES feedback.
	_, _ = o.HandleKeyPress(keyRunes("j"))
	prose, _ := o.profilesHelp()
	assert.Contains(t, prose, "Detecting", "navigating while a probe runs must not erase it")
	_, _ = o.HandleKeyPress(keyRunes("D"))
	prose, _ = o.profilesHelp()
	assert.Contains(t, prose, "Detecting", "a second D repeats the message rather than clearing it")

	// The result releases the latch, so a later D works.
	o.NoteProfilesDetected(nil, "")
	_, _ = o.HandleKeyPress(keyRunes("D"))
	assert.True(t, o.TakeProfileDetect())
}

// TestDetectionOutcomeLandsOnTheRecordItAdded. The pane's report is a one-keypress note, and the
// cursor follows the first added record so D and n agree about where you end up.
func TestDetectionOutcomeLandsOnTheRecordItAdded(t *testing.T) {
	cfg := threeProfiles()
	o := NewSettingsOverlay(cfg)
	o.SetSize(100, 32)
	profilesAt(t, o)
	cfg.Profiles = append(cfg.Profiles, config.Profile{Name: "gemini", Program: "gemini"})

	shown := o.NoteProfilesDetected([]string{"gemini"}, "added profiles: gemini")

	assert.True(t, shown, "the editor's pane is on screen, so it reports the outcome itself")
	assert.Equal(t, 3, o.profileCursor, "the cursor lands on what was added")
	prose, danger := o.profilesHelp()
	assert.Contains(t, prose, "gemini")
	assert.False(t, danger, "an outcome is not a failure")
}

// TestDetectionOutcomeIsHandedBackWhenThePaneCannotShowIt is the guard against the silent write.
//
// The probe outlives the keypress that started it, and syncCursorToRail clears the note on the
// way past — so a user who presses D and then moves the rail would otherwise have config.json
// rewritten underneath them with nothing at all on screen. Returning false is how home learns it
// has to say so itself.
func TestDetectionOutcomeIsHandedBackWhenThePaneCannotShowIt(t *testing.T) {
	cfg := profilesCfg("claude", config.Profile{Name: "claude", Program: "claude"})
	o := NewSettingsOverlay(cfg)
	o.SetSize(100, 32)
	profilesAt(t, o)
	_, _ = o.HandleKeyPress(keyRunes("D"))
	require.True(t, o.TakeProfileDetect())

	o.SetRailIndex(railDefaultIndex()) // wander off while the probe runs
	require.True(t, o.profileDetecting,
		"resetProfileTransients must NOT clear the in-flight latch — the probe is still running")

	cfg.Profiles = append(cfg.Profiles, config.Profile{Name: "codex", Program: "codex"})
	shown := o.NoteProfilesDetected([]string{"codex"}, "added profiles: codex")

	assert.False(t, shown, "the pane is not on screen, so home must report the outcome")
	assert.False(t, o.profileDetecting, "and the latch is released either way")
}

// TestDetectionOutcomeIsHandedBackWhileAFilterIsUp: a filter takes the pane over regardless of
// the rail, so the editor cannot report there either.
func TestDetectionOutcomeIsHandedBackWhileAFilterIsUp(t *testing.T) {
	o := NewSettingsOverlay(threeProfiles())
	o.SetSize(100, 32)
	profilesAt(t, o)
	_, _ = o.HandleKeyPress(keyRunes("D"))
	_, _ = o.HandleKeyPress(keyRunes("/"))
	require.True(t, o.searching())

	assert.False(t, o.NoteProfilesDetected([]string{"codex"}, "added profiles: codex"))
}

// TestProfilesHintNamesEveryLiveKey — every key the pane binds appears in its widest rung, and
// every key that rung names actually does something.
//
// The second half is the gap `.claude/skills/tui-drift-sites/SKILL.md` calls the headline one:
// **nothing in this repo asserts that an advertised key has a handler**, so a key can ship
// hinted, documented and completely dead. A `Contains` over the ladder cannot see it — only
// pressing the key can. This closes the gap for this pane.
func TestProfilesHintNamesEveryLiveKey(t *testing.T) {
	o := NewSettingsOverlay(threeProfiles())
	o.SetSize(100, 32)
	profilesAt(t, o)

	widest := o.profilesHintLadder()[0]
	for _, k := range []string{"n new", "↵ edit", "d delete", "D detect", "/ search", "esc back"} {
		assert.Containsf(t, widest, k, "the widest rung must name %q", k)
	}
	assert.NotContains(t, widest, "r reset", "r is not bound here")
	assert.NotContains(t, widest, "? more", "? is not bound here")
	assert.NotContains(t, widest, "←/→", "a profile has no cyclable value")

	// The other direction: press each advertised key on a fresh panel and assert an observable
	// effect. j first, so d lands on a record the default_program guard does not protect.
	press := func(k tea.KeyMsg) *SettingsOverlay {
		fresh := NewSettingsOverlay(threeProfiles())
		fresh.SetSize(100, 32)
		profilesAt(t, fresh)
		_, _ = fresh.HandleKeyPress(keyRunes("j"))
		_, _ = fresh.HandleKeyPress(k)
		return fresh
	}
	assert.NotNil(t, press(keyRunes("n")).profileForm, "n new: opens a form")
	assert.NotNil(t, press(tea.KeyMsg{Type: tea.KeyEnter}).profileForm, "↵ edit: opens a form")
	assert.True(t, press(keyRunes("d")).profileConfirm, "d delete: arms the confirmation")
	assert.True(t, press(keyRunes("D")).profileDetecting, "D detect: starts a detection")
	assert.True(t, press(keyRunes("/")).searching(), "/ search: opens the filter")
	assert.Equal(t, focusRail, press(tea.KeyMsg{Type: tea.KeyEsc}).focus, "esc back: returns to the rail")

	hint := stripANSI(o.hintLine())
	assert.NotContains(t, hint, "…", "the ladder must fit at 100 columns rather than truncate")
	o.SetSize(80, 24)
	assert.NotContains(t, stripANSI(o.hintLine()), "…", "and at the 80-column floor")
	assert.Contains(t, stripANSI(o.hintLine()), "/ search",
		"the filter stays advertised at the floor — ⇥ pane yields before it")
}
```

Append to `app/settings_test.go`:

```go
// TestSettingsPanel_ProfileDetectRunsOffTheUpdateLoop pins the whole D path through home: the
// panel records a request, home turns it into a command rather than probing inline, and the
// result merges and persists.
//
// It stubs detectAgents — the same package var the startup agent check uses — which is what
// makes the TUI and `atrium profiles detect` share one detector.
func TestSettingsPanel_ProfileDetectRunsOffTheUpdateLoop(t *testing.T) {
	resetSettingsTestState(t)
	stubDetect(t, []config.Profile{{Name: "codex", Program: "codex"}})
	h := newSettingsTestHome()
	h.appConfig.Profiles = []config.Profile{{Name: "claude", Program: "claude --model opus"}}
	h.appConfig.DefaultProgram = "claude"

	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(",")})
	require.Equal(t, stateSettings, h.state)
	h.settingsOverlay.SetRailIndex(h.settingsOverlay.RailEntryCount() - 2)
	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyEnter}) // focus the editor

	_, cmd := h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("D")})
	require.NotNil(t, cmd, "D must produce a command rather than probing inline")
	msg := cmd()
	detected, ok := msg.(profilesDetectedMsg)
	require.Truef(t, ok, "expected a profilesDetectedMsg, got %T", msg)

	_, _ = h.Update(detected)

	assert.Equal(t, []string{"claude", "codex"}, profileNamesOf(h.appConfig))
	assert.Equal(t, "claude --model opus", h.appConfig.Profiles[0].Program,
		"detection never modifies an existing profile")
	saved := config.LoadConfig()
	assert.Equal(t, []string{"claude", "codex"}, profileNamesOf(saved),
		"the merge reached disk through applySettingChange")
}

// TestSettingsPanel_ProfileDetectAfterCloseStillMergesAndSaysSo. The probe takes long enough
// that the user can close the panel before it returns; the merge is what they asked for, so it
// happens, and home is the one that reports it.
//
// The alternative — dropping the result — made one set of keystrokes produce three different
// outcomes depending on how fast the user moved, including a silent config.json write.
func TestSettingsPanel_ProfileDetectAfterCloseStillMergesAndSaysSo(t *testing.T) {
	resetSettingsTestState(t)
	h := newSettingsTestHome()
	h.appConfig.Profiles = []config.Profile{{Name: "claude", Program: "claude --model opus"}}
	require.Nil(t, h.settingsOverlay, "precondition: no panel")

	_, cmd := h.Update(profilesDetectedMsg{detected: []config.Profile{
		{Name: "claude", Program: "/usr/local/bin/claude"},
		{Name: "codex", Program: "codex"},
	}})

	assert.Equal(t, []string{"claude", "codex"}, profileNamesOf(h.appConfig))
	assert.Equal(t, "claude --model opus", h.appConfig.Profiles[0].Program,
		"detection never modifies an existing profile, panel or no panel")
	assert.Equal(t, []string{"claude", "codex"}, profileNamesOf(config.LoadConfig()),
		"and it reached disk")
	// handleAgentNotice either shows the toast now (a cmd) or holds it over in
	// pendingAgentNotice when the hint row is busy. Both are "announced"; neither is silence.
	assert.True(t, cmd != nil || h.pendingAgentNotice != "",
		"the outcome must be announced, not swallowed")
	if h.pendingAgentNotice != "" {
		assert.Contains(t, h.pendingAgentNotice, "codex", "the held-over notice names what was added")
	}
}

// TestProfilesDetectedTextMirrorsTheCLI pins the wording against `atrium profiles detect`'s own
// output (main.go's profilesDetectCmd). Two surfaces for one operation should not describe it in
// two voices, and this is the assertion that keeps them together — the notice path itself is
// covered above, where the exact rendering depends on whether the hint row is free.
func TestProfilesDetectedTextMirrorsTheCLI(t *testing.T) {
	assert.Equal(t, "no new agents detected; profiles unchanged", profilesDetectedText(nil))
	assert.Equal(t, "added profiles: codex", profilesDetectedText([]string{"codex"}))
	assert.Equal(t, "added profiles: codex, gemini", profilesDetectedText([]string{"codex", "gemini"}))
}

// TestSettingsPanel_ProfileDetectWhileTheRailMovedAwayIsAnnounced is the silent-write guard at
// the app level: the pane is not showing the editor, so nothing in the panel can report the
// merge, and without the handback the user sees a rewritten config.json and no explanation.
func TestSettingsPanel_ProfileDetectWhileTheRailMovedAwayIsAnnounced(t *testing.T) {
	resetSettingsTestState(t)
	h := newSettingsTestHome()
	h.appConfig.Profiles = []config.Profile{{Name: "claude", Program: "claude"}}

	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(",")})
	h.settingsOverlay.SetRailIndex(h.settingsOverlay.RailEntryCount() - 2)
	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyEnter})
	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("D")})
	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyEsc}) // back to the rail, note cleared
	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyUp})  // and off the Profiles entry

	_, cmd := h.Update(profilesDetectedMsg{detected: []config.Profile{{Name: "codex", Program: "codex"}}})

	assert.Equal(t, []string{"claude", "codex"}, profileNamesOf(h.appConfig))
	assert.True(t, cmd != nil || h.pendingAgentNotice != "",
		"a merge the panel cannot report must still be announced")
}

// profileNamesOf collapses a config's profile list to its names.
func profileNamesOf(cfg *config.Config) []string {
	out := make([]string, len(cfg.Profiles))
	for i, p := range cfg.Profiles {
		out[i] = p.Name
	}
	return out
}
```

> `stubDetect(t, profiles)` is `app/app_welcome_test.go:19`'s helper; `newSettingsTestHome` and
> `resetSettingsTestState` are `app/settings_test.go`'s. Confirm all three signatures before
> running, and confirm `h.Update` is the exported Bubble Tea method rather than an internal one.

- [ ] **Step 2: Run the tests to verify they fail**

```bash
PATH="/home/zvi/.local/share/mise/installs/go/1.26.4/bin:$PATH" \
  go test ./ui/overlay/ ./app/ -run 'Detect' 2>&1 | head -20
```
Expected: compile failure — `TakeProfileDetect`, `NoteProfilesDetected` and
`profilesDetectedMsg` are undefined.

- [ ] **Step 3: Implement the overlay half**

In `ui/overlay/settings_profiles.go`:

```go
// requestProfileDetect asks home to run agent detection off the update loop.
//
// It cannot run inline: config.DetectAgentProfiles probes claude through config.GetClaudeCommand,
// which spawns a login shell sourcing the user's rc file under a ten-second timeout
// (config/detect.go). A synchronous call would freeze the update loop — and with it every
// session's poll — for as long as that takes. home runs it as a tea.Cmd through the same
// detectAgents seam the startup agent check already uses, so the panel and
// `atrium profiles detect` cannot probe differently, and hands the result to
// NoteProfilesDetected.
//
// The latch is what stops a held key spawning a shell per repeat; NoteProfilesDetected releases
// it. It sets no note: profilesHelp derives "Detecting installed agents…" from the flag
// instead, because handleProfilesKey clears the note on every key — so a note would vanish the
// moment the user pressed j while waiting, and a second D would visibly REMOVE the only
// explanation on screen rather than repeat it.
func (s *SettingsOverlay) requestProfileDetect() {
	if s.profileDetecting {
		return
	}
	s.profileDetecting, s.profileDetectPending = true, true
}

// TakeProfileDetect reports whether the Profiles editor has asked for a detection run,
// consuming the request. home calls it after every key press, exactly as it reads Handoff on
// close: an overlay cannot run a command itself.
func (s *SettingsOverlay) TakeProfileDetect() bool {
	if !s.profileDetectPending {
		return false
	}
	s.profileDetectPending = false
	return true
}

// NoteProfilesDetected records a completed detection's outcome for the editor's help pane and
// reports whether the user will actually see it there.
//
// home does the merging and owns the wording; this half exists only so the result is REPORTED
// where the user is looking. When the editor's pane is not on screen — the rail moved, a filter
// is up, the panel closed — it returns false and home surfaces the outcome as a notice instead.
// Without that split the merge could rewrite config.json with nothing whatever on screen: the
// probe outlives the keypress, and syncCursorToRail clears the note on the way past.
//
// The cursor follows the first added record, so D and n agree about where you end up.
func (s *SettingsOverlay) NoteProfilesDetected(added []string, text string) (shown bool) {
	s.profileDetecting = false
	if len(added) > 0 {
		for i, p := range s.cfg.Profiles {
			if p.Name == added[0] {
				s.profileCursor = i
				break
			}
		}
	}
	s.clampProfileCursor()
	if !s.profilesPaneActive() {
		return false
	}
	s.profileNote = text
	return true
}
```

Bind `D` in `handleProfilesKey`, and restore the full ladder in `profilesHintLadder`:

```go
	case "D":
		s.requestProfileDetect()
```

```go
	return []string{
		"↑/↓ move · n new · ↵ edit · d delete · D detect · / search · ⇥ pane · esc back",
		"↑/↓ move · n new · ↵ edit · d delete · D detect · / search · esc back",
		"↑/↓ · n new · ↵ edit · d delete · D detect · esc back",
		"n new · ↵ edit · d delete · esc back",
		"esc back",
	}
```

- [ ] **Step 4: Implement the app half**

Create `app/app_profiles.go`:

```go
package app

import (
	"strings"

	"github.com/ZviBaratz/atrium/config"
	tea "github.com/charmbracelet/bubbletea"
)

// profilesDetectedMsg carries a completed agent detection back to the settings panel's Profiles
// editor.
type profilesDetectedMsg struct {
	detected []config.Profile
}

// profilesDetectedText is the outcome wording for a completed detection, deliberately mirroring
// `atrium profiles detect`'s own output (main.go) so the two surfaces read the same.
func profilesDetectedText(added []string) string {
	if len(added) == 0 {
		return "no new agents detected; profiles unchanged"
	}
	return "added profiles: " + strings.Join(added, ", ")
}

// detectProfilesCmd probes for installed agent CLIs off the update loop.
//
// It goes through the same detectAgents seam the startup agent check uses, so the panel's D and
// `atrium profiles detect` can never probe differently; the merge half is
// config.MergeDetectedProfiles, which the overlay calls on the result. Running it inline would
// block every session's poll for the claude probe's ten-second shell timeout.
func (m *home) detectProfilesCmd() tea.Cmd {
	return func() tea.Msg {
		return profilesDetectedMsg{detected: detectAgents()}
	}
}
```

In `app/app_keys.go`'s `handleSettingsState`, after the `changedKey` block and **before** the
`if closed` block (which drops the overlay):

```go
	if m.settingsOverlay.TakeProfileDetect() {
		cmds = append(cmds, m.detectProfilesCmd())
	}
```

In `app/app_update.go`'s message switch, beside `case agentCheckDoneMsg:`:

```go
	case profilesDetectedMsg:
		// The merge is unconditional. The user asked for it, and the probe takes long enough
		// that gating it on the panel still being open made one set of keystrokes produce three
		// different outcomes — dropped if they closed the panel, merged if they reopened it
		// (a DIFFERENT overlay instance), merged-but-silent if they only moved the rail. What
		// varies is where the outcome is reported, never whether it happens.
		added := m.appConfig.MergeDetectedProfiles(msg.detected)
		text := profilesDetectedText(added)
		shown := m.settingsOverlay != nil && m.settingsOverlay.NoteProfilesDetected(added, text)
		var cmds []tea.Cmd
		if len(added) > 0 {
			// Nothing added means nothing to persist, mirroring the CLI's early return.
			cmds = append(cmds, m.applySettingChange("profiles"))
		}
		if !shown {
			// handleAgentNotice is the held-over path the startup agent check already uses: it
			// shows now if the hint bar is free, and otherwise waits — which is exactly right
			// while a panel is covering the row.
			cmds = append(cmds, m.handleAgentNotice(text))
		}
		return m, tea.Batch(cmds...)
```

- [ ] **Step 5: Run the tests to verify they pass**

```bash
PATH="/home/zvi/.local/share/mise/installs/go/1.26.4/bin:$PATH" \
  go test ./ui/overlay/ ./app/ -run 'Detect|Profile' -v 2>&1 | tail -30
```
Expected: PASS.

- [ ] **Step 6: Verify the guards fail when they should**

1. Make the msg case call `applySettingChange` unconditionally (drop `if len(added) > 0`).
   Expected: no test fails on correctness — this one is about a needless write, so instead
   confirm by hand that `config.json`'s mtime changes on a second `D` and **add the assertion
   that catches it** to `TestSettingsPanel_ProfileDetectRunsOffTheUpdateLoop` (capture
   `os.Stat(...).ModTime()` around a second, empty detection). A mutation with no guard is a
   missing guard, not a passing one.
2. Replace `MergeDetectedProfiles` in the msg case with a plain `append`. Expected:
   `TestSettingsPanel_ProfileDetectRunsOffTheUpdateLoop` FAILS on the duplicated `claude` **and**
   on its overwritten program — read both, since the second is the promise README makes.
3. Drop the `profileDetecting` latch from `requestProfileDetect`. Expected:
   `TestASecondDetectDoesNotQueueASecondProbe` FAILS.
4. Make `TakeProfileDetect` return `s.profileDetectPending` without clearing it. Expected:
   `TestDetectRequestsRatherThanProbing` FAILS on the second call.
5. Make `NoteProfilesDetected` return `true` unconditionally. Expected:
   `TestDetectionOutcomeIsHandedBackWhenThePaneCannotShowIt`,
   `TestDetectionOutcomeIsHandedBackWhileAFilterIsUp` and
   `TestSettingsPanel_ProfileDetectWhileTheRailMovedAwayIsAnnounced` all FAIL. That last one is
   the silent-write case, so watch it specifically.
6. Add `s.profileDetecting = false` to `resetProfileTransients` — the drift its comment forbids.
   Expected: `TestDetectionOutcomeIsHandedBackWhenThePaneCannotShowIt` FAILS on the
   `require.True(t, o.profileDetecting)`. (An earlier version of that test asserted only the
   merge, which is unconditional, and this mutation left it green.)
7. Drive `profilesHelp`'s "Detecting…" off `s.profileNote` again instead of `s.profileDetecting`.
   Expected: `TestASecondDetectDoesNotQueueASecondProbe` FAILS on the `j` assertion.
8. Point `detectProfilesCmd` at `config.DetectAgentProfiles` directly instead of `detectAgents`.
   Expected: `TestSettingsPanel_ProfileDetectRunsOffTheUpdateLoop` FAILS, because the stub is no
   longer in the path — **and the real probe runs, so the test also gets slow. If it passes,
   the seam is not the seam and the drift guarantee is unproven.**
9. Re-run and confirm green.

- [ ] **Step 7: Lint and commit**

```bash
PATH="/home/zvi/go/bin:$PATH" mise exec -- just lint ./ui/... ./app/...
git status --short
git add ui/overlay/settings_profiles.go ui/overlay/settings_profiles_test.go \
  ui/overlay/settings_render.go app/app_profiles.go app/app_keys.go app/app_update.go \
  app/settings_test.go
git commit -m "feat(settings): D detects installed agents without blocking the ui"
```

---

## Task 6: the `default_program` interlock

**Files:**
- Modify: `app/app_layout.go` (`applySettingChange`'s new case and its doc comment)
- Modify: `ui/overlay/settings_test.go` (beside `TestSettingsOverlay_RawDefaultProgramSurvivesCycle`)
- Modify: `app/settings_test.go`
- **No change to `ui/overlay/settings_schema.go`.**

**Interfaces:**
- Consumes: `newSettingRows`'s `rawDefaultProgram` capture (`settings_schema.go:304`), the
  `default_program` row's `options` closure (`:330-338`), `(*config.Config).GetProgram()`
  (`config/accessors.go:457`).

Guard 12's last clause, plus the live-apply gap the editor makes reachable.

**Two facts, established by reading the closures rather than by guessing:**

1. **`options(c)` walks `c.Profiles` on every call**, so a profile added, renamed or deleted by
   the editor appears in `default_program`'s cycle vocabulary on the very next frame. **There is
   nothing to refresh and `s.rows` must NOT be rebuilt.** `rawDefaultProgram` is a lexical
   capture taken once at `NewSettingsOverlay`; calling `newSettingRows(cfg)` again would
   recompute it from the *current* `DefaultProgram`, and if the user has since cycled onto a
   profile name, the original raw command would vanish from the options — irrecoverable, which
   is the exact loss the capture exists to prevent. The task here is to **pin that it survives**,
   not to build a refresh.
2. **`m.program` is stale after any of this.** It is resolved once at launch
   (`main.go:118`) or on welcome confirm (`app_welcome.go:85`) and is the create form's fallback
   launch command whenever `GetVariants()` has no picker to draw from — i.e. with zero or one
   profile. `applySettingChange` has no `default_program` case today, so this is a pre-existing
   gap; the editor makes it reachable by *editing the command of the profile the default names*,
   which is the case where the guarantee "default_program keeps pointing at something real"
   silently stops being true of what actually launches.

- [ ] **Step 1: Write the failing tests**

Append to `ui/overlay/settings_test.go`, directly after
`TestSettingsOverlay_RawDefaultProgramSurvivesCycle`:

```go
// TestSettingsOverlay_RawDefaultProgramSurvivesAProfilesEdit is spec §9's third obligation and
// guard 12's last clause. The sibling above pins that a hand-edited raw command stays a cycle
// option across a cycle; this pins that it stays one across a PROFILES edit, which is the new
// way to reach it.
//
// The mechanism is a lexical capture taken once in newSettingRows. It survives precisely because
// s.rows is built once and never rebuilt: a refresh would recompute the capture from the
// CURRENT DefaultProgram — by then a profile name — and the raw command would be gone with no
// way back. So this test is also the guard against a well-meaning "re-read the row after a
// profiles edit" change.
func TestSettingsOverlay_RawDefaultProgramSurvivesAProfilesEdit(t *testing.T) {
	const raw = "/home/user/launch-claude.sh"
	cfg := config.DefaultConfig()
	cfg.DefaultProgram = raw
	cfg.Profiles = []config.Profile{{Name: "claude", Program: "claude"}}
	o := NewSettingsOverlay(cfg)
	o.SetSize(100, 32)

	settingsAt(t, o, "default_program")
	row := o.rows[o.cursor]
	require.Contains(t, row.options(cfg), raw, "precondition: the raw command is a cycle option")

	// Cycle off it, so the live config no longer holds the raw string anywhere.
	_, _ = o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRight})
	require.NotEqual(t, raw, cfg.DefaultProgram, "precondition: the raw value is only in the capture now")

	// Now edit the profile list from the editor: add one, then delete one.
	profilesAt(t, o)
	_, _ = o.HandleKeyPress(keyRunes("n"))
	typeProfile(o, "codex")
	_, _ = o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyTab})
	typeProfile(o, "codex")
	_, _ = o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEnter})
	_, _ = o.HandleKeyPress(keyRunes("d"))
	_, _ = o.HandleKeyPress(keyRunes("y"))

	settingsAt(t, o, "default_program")
	opts := o.rows[o.cursor].options(cfg)
	assert.Contains(t, opts, raw, "a profiles edit must not destroy the captured raw command")
	assert.Contains(t, opts, "claude", "and the live profile names are still there")
}

// TestSettingsOverlay_NewProfileBecomesACycleOption is the other direction: options() walks
// cfg.Profiles live, so a record added by the editor is immediately cyclable — no row rebuild
// needed, and none wanted (see the sibling above).
func TestSettingsOverlay_NewProfileBecomesACycleOption(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.DefaultProgram = "claude"
	cfg.Profiles = []config.Profile{{Name: "claude", Program: "claude"}}
	o := NewSettingsOverlay(cfg)
	o.SetSize(100, 32)

	settingsAt(t, o, "default_program")
	require.NotContains(t, o.rows[o.cursor].options(cfg), "gemini")

	profilesAt(t, o)
	_, _ = o.HandleKeyPress(keyRunes("n"))
	typeProfile(o, "gemini")
	_, _ = o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyTab})
	typeProfile(o, "gemini")
	_, _ = o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEnter})

	settingsAt(t, o, "default_program")
	assert.Contains(t, o.rows[o.cursor].options(cfg), "gemini",
		"the enum reads cfg.Profiles live, so a new record is cyclable at once")
}
```

Append to `app/settings_test.go`:

```go
// TestSettingsPanel_EditingTheDefaultProfileReResolvesTheLaunchCommand closes a live-apply gap
// the Profiles editor makes reachable.
//
// m.program is resolved once at launch and is the create form's fallback launch command
// whenever there is no variant picker — which is exactly the single-profile case below. Editing
// that profile's command without re-resolving leaves the form launching the previous one until
// the app is relaunched, which contradicts the whole point of guarding default_program.
func TestSettingsPanel_EditingTheDefaultProfileReResolvesTheLaunchCommand(t *testing.T) {
	resetSettingsTestState(t)
	h := newSettingsTestHome()
	h.appConfig.Profiles = []config.Profile{{Name: "claude", Program: "claude"}}
	h.appConfig.DefaultProgram = "claude"
	h.program = h.appConfig.GetProgram()
	require.Equal(t, "claude", h.program)

	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(",")})
	require.Equal(t, stateSettings, h.state)
	h.settingsOverlay.SetRailIndex(h.settingsOverlay.RailEntryCount() - 2)
	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyEnter}) // focus the editor
	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("e")})
	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyTab})
	for _, r := range " --model opus" {
		_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyEnter})

	assert.Equal(t, "claude --model opus", h.appConfig.Profiles[0].Program)
	assert.Equal(t, "claude --model opus", config.LoadConfig().Profiles[0].Program,
		"the edit reached disk through the panel's one writer")
	assert.Equal(t, "claude --model opus", h.program,
		"and the resolved launch command was re-derived, not left at launch-time")
}

// TestSettingsPanel_DefaultProgramReResolvesTheLaunchCommand is the same gap on the row that has
// always existed — cycling default_program. It is here because the fix covers both keys and a
// test for only one would let a later edit drop the other.
func TestSettingsPanel_DefaultProgramReResolvesTheLaunchCommand(t *testing.T) {
	resetSettingsTestState(t)
	h := newSettingsTestHome()
	h.appConfig.Profiles = []config.Profile{
		{Name: "claude", Program: "claude"},
		{Name: "codex", Program: "codex --sandbox"},
	}
	h.appConfig.DefaultProgram = "claude"
	h.program = h.appConfig.GetProgram()

	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(",")})
	require.True(t, h.settingsOverlay.OpenAt("default_program"))
	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRight})

	require.Equal(t, "codex", h.appConfig.DefaultProgram)
	assert.Equal(t, "codex --sandbox", h.program,
		"the launch command follows the setting rather than the launch-time snapshot")
}

// TestSettingsPanel_ProfileEditDropsAStashedDraft. A create form escaped with a dirty title is
// stashed whole and restored by the next bare n — including the []config.Profile it snapshotted
// at build time, which VariantPicker replays as launch commands verbatim. So a draft stashed
// before a profiles edit would offer a renamed-away profile and launch its OLD command.
//
// handleAccountsState already drops the stash for exactly this reason (app_accounts.go:22-24);
// this is the same line for the same hazard on the other record editor.
func TestSettingsPanel_ProfileEditDropsAStashedDraft(t *testing.T) {
	resetSettingsTestState(t)
	h := newSettingsTestHome()
	// TWO profiles: cycleEnum is a silent no-op on a single-option enum, so a one-profile
	// fixture would never reach applySettingChange and the test would pass for the wrong reason.
	h.appConfig.Profiles = []config.Profile{
		{Name: "claude", Program: "claude"},
		{Name: "codex", Program: "codex"},
	}
	h.appConfig.DefaultProgram = "claude"
	h.stashedDraft = &overlay.TextInputOverlay{} // a draft is pinned to the old profile list

	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(",")})
	require.True(t, h.settingsOverlay.OpenAt("default_program"))
	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRight})
	require.Equal(t, "codex", h.appConfig.DefaultProgram, "precondition: the cycle landed")

	assert.Nil(t, h.stashedDraft, "a stale draft must not survive a change to what launches")
}
```

> Confirm `h.program`'s field name against `app/app.go` before running; if the create form reads
> it through an accessor, assert through that instead. **Do not weaken these to "the config was
> saved"** — the persist half is already covered elsewhere and the re-resolution is the point.

- [ ] **Step 2: Run the tests to verify they fail**

```bash
PATH="/home/zvi/.local/share/mise/installs/go/1.26.4/bin:$PATH" \
  go test ./ui/overlay/ ./app/ -run 'RawDefaultProgram|CycleOption|ReResolves' 2>&1 | head -20
```
Expected: the two `app` tests FAIL on `h.program` still holding the launch-time value. The two
`ui/overlay` tests should **already PASS** — they pin behavior the closures already have. That
is the point: they are regression guards against a refresh nobody has written yet. Record that
they passed on first run rather than silently treating them as TDD failures.

- [ ] **Step 3: Implement the live-apply case**

In `app/app_layout.go`'s `applySettingChange`, add a case (order within the switch does not
matter; put it first so it reads as the launch-path case):

```go
	case "default_program", "profiles":
		// m.program is the create form's fallback launch command, resolved once at startup from
		// GetProgram(). With no variant picker — zero or one profile — it IS the command a new
		// session runs, so a changed default, or an edited profile the default names, must
		// re-resolve it or the form keeps launching the previous command until relaunch.
		//
		// Running sessions are untouched by design: session.Instance.Program stores its own
		// resolved command (session/instance.go:144) and is never re-derived, so a profile edit
		// cannot reach a session that already exists.
		m.program = m.appConfig.GetProgram()
		// A stashed create-form draft snapshotted GetProfiles() and m.program when it was built
		// (app_session.go), and VariantPicker replays each profile's Program verbatim — so a
		// restored draft would offer a renamed-away profile and launch its OLD command. Drop it
		// so the next open rebuilds from live config. handleAccountsState does exactly this for
		// exactly this reason (app_accounts.go:22-24).
		m.stashedDraft = nil
```

And extend the function's doc comment, whose current text says a profiles change would be
persist-only:

```go
// applySettingChange persists the config after the settings panel changed the given row — or,
// for "profiles", after its record editor changed the profile list — then live-applies whatever
// that field controls. Fields without a case here are read live at their point of use
// (auto_attach, max_sessions, kill_double_tap_confirm) or only consumed by later operations
// (branch_prefix; daemon_poll_interval on the next daemon run), so persisting is all they need.
```

- [ ] **Step 4: Run the tests to verify they pass**

```bash
PATH="/home/zvi/.local/share/mise/installs/go/1.26.4/bin:$PATH" \
  go test ./ui/overlay/ ./app/ -run 'RawDefaultProgram|CycleOption|ReResolves' -v 2>&1 | tail -20
```
Expected: PASS (4 tests).

- [ ] **Step 5: Verify the guards fail when they should**

1. Add `s.rows = newSettingRows(s.cfg)` at the end of `commitProfile` — the "refresh the row
   after a profiles edit" change this task exists to prevent. Expected:
   `TestSettingsOverlay_RawDefaultProgramSurvivesAProfilesEdit` FAILS on the missing raw option.
   **This is the mutation that proves the guard is worth its lines**; if it passes, the capture
   is not doing what its comment claims and that is a finding to report.
2. Narrow the case to `case "default_program":`. Expected:
   `TestSettingsPanel_EditingTheDefaultProfileReResolvesTheLaunchCommand` FAILS.
3. Narrow it to `case "profiles":`. Expected:
   `TestSettingsPanel_DefaultProgramReResolvesTheLaunchCommand` FAILS.
4. Delete `m.stashedDraft = nil`. Expected:
   `TestSettingsPanel_ProfileEditDropsAStashedDraft` FAILS.
5. Re-run and confirm green.

- [ ] **Step 6: Note the phantom option, and leave it alone**

No code change. Record this in the PR body and in the spec callout, because it is the first
thing a reviewer will find and it is **not** a defect:

After renaming or deleting the record whose name `rawDefaultProgram` captured, that captured
string is no longer any profile's name, so `options` prepends it as a cycle option — a *raw
command* rather than a profile name. It looks like a stale entry and is in fact exactly what the
capture promises: `default_program` genuinely held that string when the panel opened, and
`config.GetProgram` passes an unmatched value through to the shell, so every option in the list
launches something the config once named. Narrowing the capture to "only when it matches no
profile" is a behavior change to the one mechanism spec §9 says not to simplify away, and it
would trade a cosmetic oddity for a real irrecoverable-value risk.

- [ ] **Step 7: Lint and commit**

```bash
PATH="/home/zvi/go/bin:$PATH" mise exec -- just lint ./ui/... ./app/...
git status --short
git add app/app_layout.go app/settings_test.go ui/overlay/settings_test.go
git commit -m "fix(app): re-resolve the launch command when the default program changes"
```

---

## Task 7: the docs the editor makes wrong

**Files:**
- Modify: `README.md`
- Modify: `docs/superpowers/specs/2026-07-25-configuration-panel-design.md`

`profiles` stays in `TestEveryScalarConfigFieldHasARow`'s and
`TestReadmeSettingsExceptionsMatchTheRowSchema`'s exempt maps — it still has no `settingRow`, and
both assertions are about rows. **The README's surrounding prose is not**: it claims the panel
*cannot express* profiles, which stops being true in this PR and is guarded by nothing.

- [ ] **Step 1: Fix the configuration-reference prose**

In `README.md`, under `#### Configuration reference`, replace:

> Every `config.json` key, its default, and where it is documented above. Nearly all
> are also editable live from the Settings panel (`,`). The exceptions are the four
> keys whose value is a *list of records* — `profiles`, `claude_accounts`,
> `gh_accounts`, `agy_accounts` — which the one-value-per-row panel cannot express
> (the accounts are instead managed from the Accounts overlay), and the
> deprecated `nerd_font`, which `glyph_set` supersedes.

**Replace only that sentence.** The paragraph continues past it with "A test
(`config.TestReadmeDocumentsEveryConfigField`) fails the build if a new field is added without a
row here." (`README.md:848-854`) — a literal block replace would delete that guard sentence.
Keep it, verbatim, as the paragraph's tail. The replacement is:

> Every `config.json` key, its default, and where it is documented above. Nearly all
> are also editable live from the Settings panel (`,`). The exceptions are the three
> account lists — `claude_accounts`, `gh_accounts`, `agy_accounts` — which the
> one-value-per-row panel cannot express and which are managed from the Accounts
> overlay instead, and the deprecated `nerd_font`, which `glyph_set` supersedes.
> `profiles` is a list of records too, but the panel gives it a record editor of its
> own under Profiles rather than a row.

and, two paragraphs down, replace `The five keys with no panel row carry `—` instead.` with:

> The four keys with no panel row carry `—` instead; `profiles` names its editor.

Then change the `profiles` row's Category cell from `—` to `Profiles`:

```
| `profiles` | Profiles | array | detected | named program configs ([Profiles](#profiles)) |
```

- [ ] **Step 2: Teach the Profiles section its new home**

In `README.md`'s `#### Profiles`, after the first paragraph, insert:

> Profiles are edited in the Settings panel (`,` → **Profiles**): `n` adds one, `e` or `↵`
> edits the highlighted record, `d` deletes it, and `D` probes for installed agent CLIs and
> appends any that are missing. A profile named by `default_program` cannot be deleted until
> that setting points elsewhere; renaming it carries the setting along.

and change:

> On first run, Atrium probes for installed agent CLIs (`claude`, `codex`, `gemini`, `aider`)
> and seeds a profile for each one it finds. After installing a new agent, run:

to name the real probe list — `config/agents.go`'s `knownAgentBins` is
`{"claude", "codex", "gemini", "aider", "agy"}`, so the prose has been one short:

> On first run, Atrium probes for installed agent CLIs (`claude`, `codex`, `gemini`, `aider`,
> `agy`) and seeds a profile for each one it finds. After installing a new agent, press `D` in
> the panel's Profiles category, or run:

Finally, change `To configure profiles by hand, add a `profiles` array…` to
`To configure profiles by hand instead, add a `profiles` array…`.

- [ ] **Step 3: Amend the spec**

In `docs/superpowers/specs/2026-07-25-configuration-panel-design.md` §9, append a callout in the
form PR B and PR C used:

```markdown
> **Resolved in PR D.** Five decisions the section left open:
>
> - **Profiles is a fourth `railKind` (`railProfiles`), not an 11th category.** A category means
>   `settingRow`s, which a list of records cannot be — and §4's rail budget has zero headroom at
>   13. The entry owns a focusable pane but no rows, which splits an invariant PR B could state
>   as one fact: `railHandoff` was *exactly* the no-rows case, so "→ rows" and "⇥ pane" shared a
>   discriminator. They no longer do. `→ rows` requires `settingRow`s; `⇥ pane` requires a pane
>   the forward key can focus, which the editor has.
> - **Deleting the profile `default_program` names is refused, not repointed.** The setting lives
>   in another category, so a silent repoint would change what every new session launches from a
>   pane that cannot show the change. **A rename does carry the setting along**, because the
>   record it names still exists and following it preserves exactly what launches; a delete has
>   no successor that preserves anything.
> - **`d` asks `y / n` first**, diverging from this section's bare key list. Deleting a record is
>   the first irreversible action in the panel — `r` restores a default, an enum cycle is
>   reversible — and the sibling record editor over the same config file already confirms.
> - **Profiles are not searchable.** `/` matches label + key + summary + category over the
>   setting schema; a profile is data, not a setting. `/` from the editor opens the ordinary
>   filter and takes the rail with it, exactly as it does from a handoff entry.
> - **`D` runs detection as a `tea.Cmd`, not inline.** `config.DetectAgentProfiles` probes claude
>   through `config.GetClaudeCommand`, which spawns a login shell under a ten-second timeout, so
>   a synchronous call would freeze the update loop and every session's poll with it. The overlay
>   records a request and `home` fulfils it through the same `detectAgents` seam the startup
>   agent check uses; the merge stays `(*config.Config).MergeDetectedProfiles`.
>
> One consequence worth recording so it is not later "fixed": after renaming or deleting the
> record whose name `rawDefaultProgram` captured, that captured string is prepended to
> `default_program`'s options as a *raw command*. It looks stale and is exactly what the capture
> promises — the config genuinely held it at panel open, and `GetProgram` passes an unmatched
> value to the shell. Narrowing the capture trades a cosmetic oddity for a real irrecoverable
> value.
```

In §14's staging table, the **Contents** cell for PR **C** reads "Guards 8–11"; guard 10 (the
single-pane fallback) is PR B's. Correct the two cells:

- **B**: `Guards 4–7, 10.`
- **C**: `Guards 8, 9, 11.`

- [ ] **Step 4: Run the doc guards**

```bash
PATH="/home/zvi/.local/share/mise/installs/go/1.26.4/bin:$PATH" \
  go test ./config/ ./ui/overlay/ -run 'Readme' -v 2>&1 | tail -20
```
Expected: PASS — `config.TestReadmeDocumentsEveryConfigField` and
`TestReadmeSettingsExceptionsMatchTheRowSchema`. The second is the one to watch: `profiles` must
still be in its `exceptions` map (the editor is not a row) **and** still appear in the table,
which the Category-cell edit preserves.

- [ ] **Step 5: Commit**

```bash
git status --short
git add README.md docs/superpowers/specs/2026-07-25-configuration-panel-design.md
git commit -m "docs: the panel can edit profiles now, and the spec records how"
```

---

## Task 8: Full verification, the manual eyeball, and the PR

**Files:** none — verification only.

- [ ] **Step 1: The local gate**

```bash
mise exec -- just ci 2>&1 | tail -30
```
If `lint` dies with exit 127, `golangci-lint` is not on mise's `PATH` — the known toolchain gap,
not a regression; Step 3 is the authoritative lint.

- [ ] **Step 2: The race detector**

```bash
mise exec -- just test-race 2>&1 | tail -20
```
Expected: PASS. A `ui/wheel`-zone failure with no reported data race is a known flake — re-run,
do not patch.

- [ ] **Step 3: The authoritative, scoped lint**

```bash
PATH="/home/zvi/go/bin:$PATH" mise exec -- just lint ./ui/... ./app/... 2>&1 | tail -20
```
Expected: no issues. Watch for `revive`'s `exported` on `TakeProfileDetect` and
`NoteProfilesDetected` (each needs a doc comment starting with its own name), and for `unused`
on `rightAligned`, `wrappedPaneLines` or `profileLabelWidth` if a test was rewritten away from
them. Lint through the recipe, never a bare `golangci-lint run` — #493 keys the cache to this
worktree and a bare run reports stale findings from a sibling one.

- [ ] **Step 4: Eyeball the real panel at 100×32 and 80×24**

**This is the step tests cannot substitute for**, and the brief names the reason: the
`default_program` guard is the one behavior a passing test can most easily misrepresent. Drive
the whole loop.

```bash
S=/tmp/atrprd; rm -rf $S; mkdir -p $S/home/.atrium $S/tmux $S/repo
cat > $S/home/.atrium/config.json <<'JSON'
{
  "default_program": "claude",
  "profiles": [
    { "name": "claude", "program": "claude --model opus" },
    { "name": "aider", "program": "aider --model ollama_chat/gemma3:1b" }
  ]
}
JSON
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

**`TMUX_TMPDIR` must be exported in every shell.** An isolated `HOME` alone does *not* isolate
Atrium's tmux socket; a new shell otherwise reports "no server running" — or worse, lands the
throwaway session on the developer's live server.

Confirm by eye, at 100×32. Navigate the rail down to **Profiles** first:

1. The rail entry no longer shows the handoff arrow, and the hint reads
   `↑/↓ category · → profiles · / search · ⇥ pane · esc close`.
2. `→` focuses the pane. Two rows: `claude  claude --model opus   default` and
   `aider  aider --model ollama_chat/gemma3:1b`. The help pane shows the highlighted profile's
   command in full and `1/2`, and the hint names `n`, `↵`, `d`, `D`.
3. **`n`, add a profile.** Type `codex`, `⇥`, `codex --sandbox`, `↵`. It appears, the cursor is
   on it, and `cat $S/home/.atrium/config.json` shows it — the round trip through
   `applySettingChange`.
4. **Make it the default.** `⇥` to the rail, up to *Sessions*, `→`, onto *Default program*,
   `→`/`←` until it reads `codex`. Back to Profiles: the `default` badge has moved to codex.
5. **Delete a different one.** On `aider`, `d` → the help pane asks
   `Delete profile "aider"? This cannot be undone.` and the hint says `y delete · n cancel`.
   Press `n` — nothing happens. Press `d`, then `y` — it goes, and the file loses it.
6. **Try to delete the default.** On `codex`, `d` → the help pane says, in the danger colour,
   `Default program points at this profile — change it under Sessions first.` Nothing is armed
   and nothing is removed. **This is the check the whole task exists for; if the message is
   truncated or the wrong colour, fix it before proceeding.**
6a. **Then follow that advice and check it is followable.** Go to Sessions → Default program and
   confirm `←`/`→` really does move it. Now delete profiles until only the default is left, and
   press `d` again: the message must change to `Default program points at your only profile —
   add another with n first.` **Press `←`/`→` on Default program in that state and watch nothing
   happen** — that silent dead key is exactly why the wording is conditional, and it is
   invisible to any fixture with two profiles in it.
7. **`D`.** The pane says `Detecting installed agents…` **and the UI stays responsive** — press
   `j`/`k` while it runs, confirm the cursor moves **and that the message is still there** (it
   is derived from the in-flight flag, not from a note the next key clears). Then either
   `added profiles: …` — with the cursor landed on the first one — or
   `no new agents detected; profiles unchanged`. Press `D` again: the second run says the
   latter, and `config.json`'s mtime must not change (`stat -c %Y $S/home/.atrium/config.json`
   before and after).
7a. **`D` then walk away.** Press `D`, then immediately `esc` `esc` to close the panel. When the
   probe returns, the merge still happens **and the hint bar says so** — this is the case that
   used to be silent, or dropped, depending on how fast you moved. Re-open `,` → Profiles and
   confirm the list matches what the toast claimed.
8. **Esc is four-deep.** From an open form: `esc` → the list; `esc` → the rail; `esc` → closed.
   And from the list with a delete armed, `esc` cancels the delete rather than leaving the pane.
9. `/` from the Profiles pane opens the ordinary settings filter and the rail leaves Profiles.
   `?` on the Profiles pane does nothing.
10. **Empty state:** quit, `echo '{"default_program":"bash"}' > $S/home/.atrium/config.json`,
    relaunch, and confirm the pane reads `No profiles yet — press n to add one, or D to detect
    installed agents.` with the help pane explaining that `Default program` is run directly.

Then the degradation floor and the single-pane fallback:

```bash
export TMUX_TMPDIR=/tmp/atrprd/tmux
tmux -L eyeball resize-window -t 0 -x 80 -y 24; sleep 1
tmux -L eyeball capture-pane -p -t 0
tmux -L eyeball resize-window -t 0 -x 64 -y 24; sleep 1
tmux -L eyeball capture-pane -p -t 0
```

At **80×24**: no line wraps, the rail still fits unscrolled, the hint is the rung that keeps
`/ search`, and the long `aider` command is truncated with `…` while the help pane shows it
whole. At **64 columns** the rail is gone (single-pane drill-in): `↵` on Profiles opens the
editor full width, the form still shows both field labels, and `esc` returns to the rail screen.

- [ ] **Step 5: Tear down and confirm the live server is untouched**

```bash
export TMUX_TMPDIR=/tmp/atrprd/tmux
tmux -L eyeball kill-server 2>/dev/null
rm -rf /tmp/atrprd
unset TMUX_TMPDIR
tmux -L atrium list-sessions | head -3   # the developer's live server must be intact
```

- [ ] **Step 6: Open the PR**

```bash
gh auth switch --user ZviBaratz
git status --short
git push -u origin HEAD
```

Write the body to a file and pass `--body-file`; the classifier blocks a heredoc body containing
shell-looking content.

```bash
cat > /tmp/prd-body.md <<'EOF'
PR D, the last stage of the configuration panel redesign
(`docs/superpowers/specs/2026-07-25-configuration-panel-design.md`), following #482, #491 and
#494. It closes spec D11: `profiles` was the one configuration surface with no TUI at all, and
the rail entry for it rendered a note telling you to edit `config.json` by hand.

**Profiles is now a record editor** in that slot — `n` new, `e`/`↵` edit, `d` delete, `D` detect
— over `config.Profiles`, persisted through `applySettingChange` so the panel keeps exactly one
writer. It is a **fourth `railKind`**, not an eleventh category: a category means `settingRow`s,
which a list of records cannot be, and §4's rail budget has zero headroom at 13 entries. So
`newSettingRows` is untouched and `profiles` stays exempt from the every-scalar-field guard.

**That split an invariant PR B could state as one fact.** `railHandoff` used to be *exactly* the
no-rows case, so `→ rows` and `⇥ pane` shared a discriminator. The editor owns a focusable pane
but no rows, and the guard now pins two facts instead: `→ rows` requires `settingRow`s, `⇥ pane`
requires a pane the forward key can focus.

**Deleting the profile `default_program` names is refused**, with a message that leads with the
setting's own label so the help pane's truncation ladder cannot eat it — swept across every
width, because a guard message the user cannot read is not a guard. **Renaming that profile
carries the setting along**, which is the opposite decision for a crisp reason: a rename keeps
the record the pointer names, so following it preserves exactly what launches; a delete has no
successor that preserves anything, and falling through to `GetProgram`'s raw-command path would
silently run a different program.

**`d` asks first.** That diverges from the spec's bare key list, deliberately: deleting a record
is the first irreversible action in this panel — `r` restores a default, an enum cycle is
reversible — and the sibling record editor over the same config file already confirms.

**`D` cannot run on the update loop.** `config.DetectAgentProfiles` probes claude through
`config.GetClaudeCommand`, which spawns a login shell sourcing your rc file under a ten-second
timeout. So the overlay records a request and `home` fulfils it as a command, through the same
`detectAgents` seam the startup agent check already uses — which is also what makes the panel
and `atrium profiles detect` impossible to drift apart. The merge stays
`MergeDetectedProfiles`, so an existing profile still wins whole over a detected one, and a run
that adds nothing never rewrites `config.json`.

**The probe outliving the keypress is the interesting part.** The merge is unconditional —
gating it on the panel still being open made one set of keystrokes produce three different
outcomes depending on how fast the user moved, including a `config.json` write with nothing at
all on screen (press `D`, `esc` to the rail, `j`: the note is cleared on the way past). What
varies is only *where* the outcome is reported: the editor's help pane when its pane is on
screen, and otherwise the same held-over notice path the startup agent check already uses.
`NoteProfilesDetected` returns whether it showed it, and that return value is the whole guard.

**One live-apply gap is fixed on the way past.** `m.program` — the create form's fallback launch
command, and the actual command with zero or one profile — was resolved once at launch and never
again, so changing `default_program` left the form launching the previous command until
relaunch. Editing the default profile's command hits the same stale value, which would have
contradicted the whole point of guarding the pointer. It now re-resolves on both keys, and drops
a stashed create-form draft for the same reason `handleAccountsState` already does: a draft
snapshots the profile list when it is built and replays each program verbatim, so a restored one
would offer a renamed-away profile and launch its old command.

**The refusal's wording is conditional, and that is not politeness.** With exactly one profile,
`default_program`'s enum has one option, and `cycleEnum` returns early on a single-option enum
with no error, no chip and no reset — a silent dead key. `seededDefaultConfig` points
`default_program` at `Profiles[0]`, so a machine with one agent installed lands there on first
run, and "change it under Sessions first" would be advice the panel makes impossible to follow.
It says "add another with n first" instead.

**Known inconsistency, deliberately not fixed here:** the startup notice still reads "New agent
`x` detected. Run `atrium profiles detect` to add it", pointing at a CLI for something `D` now
does in-app. Retargeting it means making it a `,`-advertising notice, which
`TestEveryCommaNoticeGoesThroughSettingNotice` requires to go through `settingNotice` — and that
needs an `OpenAt("profiles")` the deep link does not have yet, plus `pendingAgentNotice`'s
held-over flush semantics. The two belong in one follow-up, alongside spec §12's other deferred
call sites.

**Worth reviewing:** `paneCursor`, because the editor introduces a second index space and
`paneLine.rowIdx` used to mean one thing; the name-column cap, since a profile name is unbounded
user data where a schema label is not; and the `rawDefaultProgram` interaction — `s.rows` is
deliberately *not* rebuilt after a profiles edit, and there is a test whose job is to fail if
someone adds a refresh, because recomputing that capture would destroy a hand-edited raw command
with no way back.

The plan this shipped with records what an adversarial review changed before any code was
written.

After this the four-stage series is complete. Two follow-ups belong together and are not here:
`OpenAt("profiles")` plus retargeting the "Run `atrium profiles detect`" notice at the panel —
spec §12 already lists further deep-link call sites as follow-up work.
EOF
gh pr create --title "feat(settings): edit agent profiles without leaving the app" --body-file /tmp/prd-body.md
```

`gh pr edit` is broken on this repo — use `gh api --method PATCH` if the body needs changing.

- [ ] **Step 7: Read CI before calling it merge-ready**

```bash
gh pr checks --required
```
`gh pr checks` has **no** `--json` flag; a poll loop using one fails every iteration and times
out silently. Expected: all required checks pass, including the macOS job. If `mergeStateStatus`
is `BEHIND` (main is `strict: true`), `gh api --method PUT repos/ZviBaratz/atrium/pulls/N/update-branch`
fixes it without a force-push — then **re-read the checks**, because the state passes through
`BLOCKED` before returning to `CLEAN`, and a clean auto-merge is not the same as a green gate on
the merged state.

---

## Self-Review

**Spec coverage.** §9 clause by clause: the rail slot becomes an editor (Task 2); `n`/`e`/`↵`
(Task 3); `d` (Task 4); `D` reusing `MergeDetectedProfiles(DetectAgentProfiles())` with detection
never modifying an existing profile (Task 5); the `default_program` delete guard (Task 4); live
sessions needing no guard because `Instance.Program` stores its own resolved command — asserted
in a comment rather than a test, because there is nothing to assert (Task 6); `default_program`'s
options re-reading `Profiles` and the `rawDefaultProgram` capture surviving (Task 6). §13's
**guard 12** lands here in full. Guards 1–11 are A/B/C's; the six of them this PR restates are
listed in Task 2, and none is weakened — three counters change value, one biconditional becomes
two implications with an added counter, and one test is inverted rather than deleted.

**Five decisions diverge from the spec's letter**, each recorded in the §9 callout rather than
silently adopted: the fourth `railKind`; refuse-on-delete with carry-on-rename; the `y`/`n`
confirmation; profiles not being searchable; and asynchronous detection. A sixth change is
outside §9 entirely — `m.program`'s re-resolution — and is called out in the PR body as a
pre-existing gap the editor makes reachable.

**Type consistency.** `profilesChangedKey` is the one string reported to `home`, used by Tasks
3, 4 and 5 and switched on in Task 6. `profileForm.editIndex` is the single `-1` sentinel read by
`validateProfile` and `commitProfile`. `profilesPaneActive()` is the one predicate read by
`HandleKeyPress`, `helpLines`, `contextLine`, `hintLine` and `paneCursor` — written once in Task
2 so the five readers cannot drift. `profileNameLabel`/`profileProgramLabel` are shared by the
renderer and its width guard, so a copy change is caught by the sweep rather than by eye.

**Three things this plan deliberately does not build**, each because building it would be worse:
a row refresh after a profiles edit (it would destroy the raw capture — there is a mutation whose
job is to prove that); an `OpenAt("profiles")` deep link (it is one change with the agent-check
notice, and §12 already defers further call sites); and paging keys in the editor's pane (D3's
36-keypress measurement is about a 37-row schema, and every key added must be advertised in a
ladder already carrying six).

**Every number in Derived numbers has now been measured** — two reviewers computed them
independently against the tree, one of them by building the plan's code and running its tests.
The only one that was wrong is corrected in place: the rail rung is **57** cells, not 53 (the
first draft copied the `→ rows` rung and `→ profiles` is four cells longer). It changes nothing
— 57 ≤ 74 at the floor. Where a step still says "report the width at which it breaks", that is a
genuine unknown, not boilerplate.

**Task 2 Step 9's `paneCursor` mutation was worried about for nothing** — measured, at 100×20
`paneHeight()` is 9 and the 20-profile fixture overflows it, so the mutation fires cleanly. The
step keeps its instruction anyway, because the reason it was doubted (a fixture too short to
overflow) is exactly the failure mode a later edit to the fixture would reintroduce.

---

## What the adversarial review changed

Three reviewers read the first draft **against the tree** before any code was written: one
applied its production code and every prescribed test to a throwaway copy and ran build, vet,
lint and the full `ui/overlay` + `app` suites; one computed every hint-rung width, sweep boundary
and counter independently; one hunted interactions. Both of the first two independently found the
same five failures, which is the strongest signal in the report. These are the findings that
changed the **design**, not just a test:

1. **The delete refusal named an action the panel makes impossible.** With exactly one profile,
   `default_program`'s enum has one option and `cycleEnum` returns early with no error, no chip
   and no reset — and `seededDefaultConfig` puts a one-agent machine in exactly that state on
   first run. The wording is now conditional, with its own test and its own eyeball step; the
   old fixtures (three profiles, or a raw `default_program`) could not see it.
2. **The detect round trip lost its result three different ways, one of them a silent
   `config.json` write.** Press `D`, `esc` to the rail, `j`: the merge ran, `SaveConfig` ran, and
   `syncCursorToRail` had already cleared the note. Press `D`, close, reopen: it merged into a
   *different* overlay instance. Press `D` and close: it was dropped entirely. The merge moved to
   `home` and is now unconditional; the overlay only reports, and says when it cannot. The
   plan's own test asserted the merge survived and never asserted anyone was told.
3. **A stashed create-form draft pins a deleted or renamed profile** — the hazard
   `handleAccountsState` already fixes with one line and a written-down reason. Added.
4. **The editor rendered unlabelled in single-pane drill-in.** `rowsPaneContent`'s header branch
   fires only for `railCategory`, and the new dispatch jumps over it — so below 73 columns the
   user saw a bare list of names and commands while the *form* in the same pane showed a
   heading. Worse, the plan's own sweep asserted `require.Len(lines, 2)`, which would have
   forbidden the fix.
5. **`textinput.Model.View()` renders `Width + 1`**, so every form line was one cell over the
   pane at every geometry — and separately, a *seeded* edit form ignores `Width` entirely
   (textinput re-windows only from `Update`/`setCursor`, and `newProfileForm` sets the value
   while `Width` is still 0), emitting a 66-cell line into a 54-cell pane. The empty-form sweep
   was structurally blind to the second one; it now sweeps both.
6. **`validateProfile` answered a question the user had not reached.** It checked the empty
   program before the duplicate name, so typing a colliding name reported the empty program —
   which is also what the plan's own test expected not to happen.

Three test-quality findings, each **proved by mutation** rather than argued:

- **`TestTheRefusalIsVisibleAtEveryWidth` was unfalsifiable.** `helpLines` keeps the *first*
  `proseBudget` lines and the setting's label leads the sentence, so it is always in line 0 —
  a 211-cell message left the assertion green. It now asserts the tail and the absence of the
  cap's ellipsis, where the margin at 40 columns is genuinely zero.
- **The pane's width sweep was a `composeRowLine` tautology**, the exact thing this plan's own
  Global Constraints forbid: deleting the name truncation left it green. It asserts `== paneW`
  plus a presence half now.
- **`TestDetectResultSurvivesLeavingThePane` did not test its docstring** — adding the very
  drift its comment forbids (`profileDetecting` cleared by `resetProfileTransients`) left it
  green, because it only asserted an unconditional merge.

Two smaller ones worth naming: `railHintLadder`'s `forward == ""` branch becomes **dead code
carrying a false comment** the moment Profiles stops being a handoff, and `unused` cannot see a
dead branch — it is deleted, and the assumption that lets it be deleted is now asserted. And the
README replacement, taken literally, would have **deleted the sentence naming
`TestReadmeDocumentsEveryConfigField`**.

**One reviewer claim was checked and rejected:** that `?` could be open while the rail sits on
Profiles. No key path moves `railCursor` while `helpOpen` — `handleHelpKey` binds only esc, `?`
and the scroll keys, `OpenAt` clears it, and `app` only calls `SetRailIndex` on a freshly built
overlay. `?` in the pane is inert as designed.
