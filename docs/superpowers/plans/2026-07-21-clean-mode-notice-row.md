# Clean-Mode Notice Row (#438) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** When the hint bar is hidden, a transient notice must not shift the pane content — the bottom row stays always-reserved in `stateDefault` (blank when the bar is off) so notices ride it with zero reflow.

**Architecture:** `menuVisible()` is the single "is the bottom row reserved?" predicate (drives the height budget, the `View()` append, and menu sizing in lockstep). Make it always-true in `stateDefault`; move the hints-vs-blank decision into the menu via a `quiet` flag seeded from `hint_bar`. User-action acks (`flashNotice`) ride the reserved row; the two background callers (`handleUpdateNotice`, `handleDriftFound`) get a `!GetHintBar()` guard so they stay badge-only in clean mode (preserve #108).

**Tech Stack:** Go, Bubble Tea (`tea`), Lipgloss, testify. Spec: `docs/superpowers/specs/2026-07-21-clean-mode-notice-row-design.md`.

## Global Constraints

- Module path `github.com/ZviBaratz/atrium`; license AGPL-3.0.
- **Tests must stay hermetic** — never read/write the real data dir. The test homes here (`newCreateFormHome`, struct literals) don't touch `config`/`state`/`tmux`, so no `HOME` override is needed; keep it that way.
- The gate is **`just ci`** (build + vet + fmt-check + lint + test + cover) and it needs `golangci-lint` on `PATH`. `unused` and `revive` (`exported` doc comments) are the usual local-only failures.
- Toolchain resolves in this worktree: `just`, `go` (1.26.4). Inner loop: `go test ./ui/ ./app/ -count=1`.
- Commits: Conventional Commits, lowercase; include the repo's standard `Co-Authored-By` / `Claude-Session` trailers.
- Scope guard (confirmed with maintainer): background update/drift notices stay **badge-only** with the bar off. Do not let them ride the reserved row.

---

### Task 1: Menu `quiet` flag (blank reserved row)

Self-contained `ui` unit. `quiet` defaults false, so this task changes no production behavior on its own; it only adds the capability + its unit test.

**Files:**
- Modify: `ui/menu.go` (struct field, `SetQuiet`, `String()` blank branch)
- Test: `ui/menu_notice_test.go`

**Interfaces:**
- Produces: `func (m *Menu) SetQuiet(quiet bool)`; `Menu.String()` returns a width×height blank line when `quiet && (state == StateDefault || state == StateEmpty)` and no notice is set.

- [ ] **Step 1: Write the failing test**

In `ui/menu_notice_test.go`, add `"strings"` to the import block:

```go
import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/stretchr/testify/require"
)
```

Append this test:

```go
// quiet blanks the always-on hint sets so chrome-free mode (hint_bar off) keeps
// the reserved row but shows nothing on it. A notice still overrides the blank,
// and contextual states (Busy/…) still render — they carry unique info even with
// the bar off.
func TestMenu_QuietBlanksHintsNotNotices(t *testing.T) {
	m := NewMenu()
	m.SetSize(200, 1)
	m.SetQuiet(true)

	m.SetState(StateDefault)
	require.Empty(t, strings.TrimSpace(m.String()), "quiet blanks the default hint line")
	require.Equal(t, 1, lipgloss.Height(m.String()), "the reserved row is still exactly one line")

	m.SetState(StateEmpty)
	require.Empty(t, strings.TrimSpace(m.String()), "quiet blanks the empty hint line too")

	// A notice overrides the blank.
	m.SetNotice("branch copied", NoticeInfo)
	require.Contains(t, m.String(), "branch copied", "a notice still shows despite quiet")
	m.ClearNotice()

	// Contextual states still render — they are forced visible even with the bar off.
	m.SetBusy("pushing…")
	require.Contains(t, m.String(), "pushing…", "busy progress renders despite quiet")
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./ui/ -run 'TestMenu_QuietBlanksHintsNotNotices' -count=1`
Expected: FAIL — build error `m.SetQuiet undefined (type *Menu has no field or method SetQuiet)`.

- [ ] **Step 3: Add the `quiet` field**

In `ui/menu.go`, in the `Menu` struct, insert `quiet` after `noticeLevel`:

```go
	notice        string
	noticeLevel   NoticeLevel
	// quiet blanks the always-on hint line (StateDefault/StateEmpty) so chrome-free
	// mode (hint_bar off) keeps the reserved row but renders nothing on it. A notice
	// still overrides the blank — it is checked first in String — and the contextual
	// states (Filter/Visual/DiffComment/Busy/GeneratingName) still render, since they
	// are forced visible to teach a gesture or report progress even with the bar off.
	quiet         bool
	contextHints  []keys.KeyName
```

- [ ] **Step 4: Add the `SetQuiet` setter**

In `ui/menu.go`, immediately after `SetNotice`:

```go
// SetQuiet toggles chrome-free rendering of the always-on hint line. The menu row
// is still reserved by the layout (menuVisible stays true in stateDefault); quiet
// only decides whether that reserved row shows the hints or a blank line.
func (m *Menu) SetQuiet(quiet bool) { m.quiet = quiet }
```

- [ ] **Step 5: Add the blank branch in `String()`**

In `ui/menu.go`, in `String()`, insert the quiet branch between the notice branch and `var line string`:

```go
		return centerInBox(m.width, m.height, style.Render(text))
	}

	// Chrome-free: the reserved row (menuVisible stays true in stateDefault) renders
	// blank instead of the hints. Only the always-on hint sets blank — the contextual
	// states in the switch below (Filter/Visual/DiffComment/Busy/GeneratingName) are
	// forced visible to teach a gesture or report progress, so they render even with
	// the bar off.
	if m.quiet && (m.state == StateDefault || m.state == StateEmpty) {
		return centerInBox(m.width, m.height, "")
	}

	var line string
```

- [ ] **Step 6: Run the test to verify it passes**

Run: `go test ./ui/ -run 'TestMenu_QuietBlanksHintsNotNotices' -count=1 -v`
Expected: PASS.

- [ ] **Step 7: Run the whole `ui` package**

Run: `go test ./ui/ -count=1`
Expected: PASS (existing menu tests default `quiet == false`, so they're unaffected).

- [ ] **Step 8: Commit**

```bash
git add ui/menu.go ui/menu_notice_test.go
git commit -m "feat(menu): add quiet blank-row rendering for chrome-free mode"
```

---

### Task 2: Reserve the row in clean mode + preserve badge-only background notices (#438)

Wires the always-reserved row, seeds/toggles `quiet`, adds the two background-caller guards, and updates every test whose expectation the change flips. This is one atomic, green deliverable — the production change and the flipped tests must land together.

**Files:**
- Modify: `app/app_layout.go` (`menuVisible()` default branch + doc; `applySettingChange("hint_bar")`)
- Modify: `app/app_construct.go` (`assembleHome` seed)
- Modify: `app/app_updatecheck.go` (`handleUpdateNotice` guard)
- Modify: `app/app_msgs.go` (`handleDriftFound` guard)
- Test: `app/notice_test.go` (new regression + 4 rewrites)
- Test: `app/menu_visibility_test.go` (2 updates)
- Test: `app/app_driftcheck_test.go` (new drift-guard test)

**Interfaces:**
- Consumes: `Menu.SetQuiet` (Task 1).
- Produces: `menuVisible()` returns `true` in `stateDefault`; `paneContentHeight()` is constant across a notice's lifecycle in clean mode.

- [ ] **Step 1: Write the #438 regression test (RED)**

In `app/notice_test.go`, append:

```go
// #438: with the hint bar hidden, a transient user-action ack must not shift the
// pane content. The bottom row is always reserved in stateDefault (blank when the
// bar is off), so the notice rides it instead of growing an errBox row —
// paneContentHeight is identical before, during, and after the notice.
func TestHandleInfoNotice_HintBarOffNoLayoutShift(t *testing.T) {
	h := newCreateFormHome(t)
	off := false
	h.appConfig.HintBar = &off
	h.menu.SetQuiet(true) // assembleHome seeds this from hint_bar in production
	h.updateHandleWindowSizeEvent(tea.WindowSizeMsg{Width: 80, Height: 24})

	before := h.paneContentHeight()

	cmd := h.handleInfoNotice("branch copied")
	require.NotNil(t, cmd)
	assert.True(t, h.menu.HasNotice(), "the reserved menu row carries the notice")
	assert.False(t, h.errBox.HasContent(), "no errBox row is claimed, so nothing reflows")
	assert.Equal(t, before, h.paneContentHeight(), "the panes must not shrink when the notice appears")

	h.Update(hideErrMsg{gen: h.noticeGen})
	assert.False(t, h.menu.HasNotice(), "the notice clears")
	assert.Equal(t, before, h.paneContentHeight(), "the panes must not grow when the notice clears")
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./app/ -run 'TestHandleInfoNotice_HintBarOffNoLayoutShift' -count=1`
Expected: FAIL — `paneContentHeight` differs (24 before, 23 during): the notice falls to the errBox row and shrinks the panes.

- [ ] **Step 3: Reserve the row — `menuVisible()` default branch**

In `app/app_layout.go`, replace the `default` branch:

```go
	default: // stateDefault (and the empty list)
		// generatingName and actionInFlight each force the bar visible so their
		// progress row shows even when the always-on hint bar is turned off.
		return m.generatingName || m.actionInFlight || m.appConfig.GetHintBar()
	}
```

with:

```go
	default: // stateDefault (and the empty list)
		// The bottom row is always reserved during plain navigation, so a transient
		// notice never resizes the frame (#438). generatingName / actionInFlight are
		// subsumed here — they still drive the menu's StateGeneratingName / StateBusy
		// content. With hint_bar off the row renders blank (Menu.quiet, seeded from the
		// setting), giving a still chrome-free frame a notice can ride without a shift.
		return true
	}
```

Then reword the last sentence of the `menuVisible` doc comment. Replace:

```go
// hint line unless the user turned it off (hint_bar in config.json), which
// restores the chrome-free interface.
```

with:

```go
// hint line; with it turned off (hint_bar in config.json) the row stays reserved
// but renders blank (Menu.quiet) instead of disappearing, so a transient notice
// can ride it without shifting the layout (#438).
```

- [ ] **Step 4: Seed `quiet` at construction**

In `app/app_construct.go` (`assembleHome`), insert after the `SetPermissionIndicator` seeder:

```go
	// Seed the permission-mode chip (on/off; see config.GetPermissionIndicator).
	h.list.SetPermissionIndicator(appConfig.GetPermissionIndicator())
	// Seed the hint bar's chrome-free flag: with hint_bar off the menu row stays
	// reserved but renders blank, so notices ride it without a shift (#438).
	h.menu.SetQuiet(!appConfig.GetHintBar())
```

- [ ] **Step 5: Live-toggle `quiet` — `applySettingChange`**

In `app/app_layout.go`, replace the `hint_bar` case:

```go
	case "hint_bar":
		m.recomputeLayout() // the bar claims or releases its row
```

with:

```go
	case "hint_bar":
		// The row is always reserved (menuVisible stays true in stateDefault); hint_bar
		// only toggles the bar between its hints and a blank line, so the row count no
		// longer changes on toggle — the panes don't resize either. Update the flag and
		// repaint.
		m.menu.SetQuiet(!m.appConfig.GetHintBar())
		m.recomputeLayout()
```

- [ ] **Step 6: Guard the background update notice (badge-only, #108)**

In `app/app_updatecheck.go`, replace `handleUpdateNotice`'s body-opening with the guard:

```go
func (m *home) handleUpdateNotice(text string) tea.Cmd {
	// With the hint bar off, the background update notice stays badge-only (#108):
	// the row is always reserved in clean mode now (#438), so without this guard the
	// unsolicited toast would ride it. Buffer it like any undeliverable toast; the
	// panel badge (set by the caller) is the durable signal.
	if !m.appConfig.GetHintBar() {
		m.pendingUpdateNotice = text
		return nil
	}
	if cmd := m.showMenuNotice(text, ui.NoticeInfo); cmd != nil {
		m.pendingUpdateNotice = ""
		return cmd
	}
	m.pendingUpdateNotice = text
	return nil
}
```

- [ ] **Step 7: Guard the background drift hint (badge-only, #108)**

In `app/app_msgs.go` (`handleDriftFound`), replace:

```go
	cmd := m.showMenuNotice(fmt.Sprintf("⚠ agent heuristics may be stale — run `%s doctor`", m.hintBinName()), ui.NoticeInfo)
	if cmd == nil {
```

with:

```go
	// With the hint bar off, the drift hint stays badge-only (#108/#438): don't even
	// attempt the toast (the clean-mode row is always reserved and would carry it).
	// Leaving cmd nil falls through to the persistent-badge path below.
	var cmd tea.Cmd
	if m.appConfig.GetHintBar() {
		cmd = m.showMenuNotice(fmt.Sprintf("⚠ agent heuristics may be stale — run `%s doctor`", m.hintBinName()), ui.NoticeInfo)
	}
	if cmd == nil {
```

- [ ] **Step 8: Run the regression test to verify it passes**

Run: `go test ./app/ -run 'TestHandleInfoNotice_HintBarOffNoLayoutShift' -count=1 -v`
Expected: PASS.

- [ ] **Step 9: Run the full app package to see which existing tests flipped**

Run: `go test ./app/ -count=1`
Expected: FAIL in exactly these (their old expectations encode the pre-fix behavior; Steps 10–11 update them):
- `TestHandleError_HintBarOffFallsBackToErrRow`
- `TestHandleInfoNotice_HintBarOffFallsBackToErrRow`
- `TestWarnMissingProgram_HintBarOffFallsBackToErrRow`
- `TestFlashNotice_MenuNoticeClearsStaleErrBox`
- `TestMenuVisible_ByState`
- `TestView_HintBarContextual`

`TestUpdateBadge_PersistsWithHintBarOff` must **still pass** (the Step 6 guard keeps it badge-only). If it fails, the guard is wrong — fix Step 6 before continuing.

- [ ] **Step 10: Rewrite the four flipped notice tests**

In `app/notice_test.go`, replace `TestHandleError_HintBarOffFallsBackToErrRow` with:

```go
// With the hint bar off, a short error rides the always-reserved menu row in
// stateDefault (blank until the notice lands) rather than growing a separate
// errBox row — so feedback never reflows the panes (#438).
func TestHandleError_HintBarOffRidesReservedMenuRow(t *testing.T) {
	h := newCreateFormHome(t)
	off := false
	h.appConfig.HintBar = &off
	h.menu.SetQuiet(true)

	h.handleError(fmt.Errorf("boom"))

	assert.True(t, h.menu.HasNotice(), "the reserved menu row carries the error")
	assert.False(t, h.errBox.HasContent(), "no separate errBox row is claimed")
}
```

Replace `TestHandleInfoNotice_HintBarOffFallsBackToErrRow` with:

```go
// Info acknowledgments with the hint bar off ride the reserved menu row too —
// shown, not dropped (#287), and without a reflow (#438).
func TestHandleInfoNotice_HintBarOffRidesReservedMenuRow(t *testing.T) {
	h := newCreateFormHome(t)
	off := false
	h.appConfig.HintBar = &off
	h.menu.SetQuiet(true)

	cmd := h.handleInfoNotice("branch copied")

	require.NotNil(t, cmd, "a fallen-back info notice still schedules its own hide")
	assert.True(t, h.menu.HasNotice(), "the reserved menu row carries the ack")
	assert.False(t, h.errBox.HasContent(), "no separate errBox row is claimed")
}
```

Replace `TestWarnMissingProgram_HintBarOffFallsBackToErrRow` with:

```go
// The missing-program warning with the hint bar off rides the reserved menu row
// (error-level) rather than a separate errBox row (#287/#438).
func TestWarnMissingProgram_HintBarOffRidesReservedMenuRow(t *testing.T) {
	h := newCreateFormHome(t)
	off := false
	h.appConfig.HintBar = &off
	h.menu.SetQuiet(true)

	cmd := h.warnMissingProgram("definitely-not-a-real-program")

	require.NotNil(t, cmd, "the warning schedules its own hide")
	assert.True(t, h.menu.HasNotice(), "the reserved menu row carries the warning")
	assert.False(t, h.errBox.HasContent(), "no separate errBox row is claimed")
}
```

Replace `TestFlashNotice_MenuNoticeClearsStaleErrBox` with (the errBox→menu handoff, now reached through an overlay state since `stateDefault` always has the menu row):

```go
// flashNotice holds only one transient surface at a time. A notice raised while an
// overlay owns the screen (menuVisible false) falls back to the errBox row; when a
// later notice can ride the menu row it must clear that stale errBox row AND
// recompute, so the frame never renders a line short (#287).
func TestFlashNotice_MenuNoticeClearsStaleErrBox(t *testing.T) {
	h := newCreateFormHome(t)
	h.updateHandleWindowSizeEvent(tea.WindowSizeMsg{Width: 80, Height: 24})

	// An overlay-state notice takes the errBox fallback (the menu row is hidden
	// behind the overlay there).
	h.state = stateHelp
	h.flashNotice("earlier notice", ui.NoticeInfo)
	require.True(t, h.errBox.HasContent(), "an overlay-state notice falls back to the errBox row")

	// Back in stateDefault the next notice rides the menu row; the stale errBox row
	// must be dropped and the panes must grow back so the frame stays 24 rows.
	h.state = stateDefault
	h.flashNotice("pushed changes", ui.NoticeInfo)
	assert.True(t, h.menu.HasNotice(), "the new notice rides the menu row")
	assert.False(t, h.errBox.HasContent(), "the stale errBox row must be cleared")
	assert.Equal(t, 24, lipgloss.Height(h.View()),
		"reclaiming the errBox row must recompute the layout, not leave the frame a line short")
}
```

- [ ] **Step 11: Update the two visibility tests**

In `app/menu_visibility_test.go`, in `TestMenuVisible_ByState`, replace:

```go
	require.False(t, h.menuVisible(), "hint_bar=false restores clean navigation")
```

with:

```go
	require.True(t, h.menuVisible(),
		"the bottom row is always reserved in stateDefault; hint_bar off renders it blank, not absent (#438)")
```

In the same file, in `TestView_HintBarContextual`, replace the block from the first `require.Contains(... "kill" ...)` through the trailing `h.appConfig.HintBar = &on`:

```go
	h.updateHandleWindowSizeEvent(tea.WindowSizeMsg{Width: 120, Height: 30})
	require.Contains(t, h.View(), "kill", "default navigation renders the hint bar")

	// hint_bar off: plain navigation goes chrome-free.
	off := false
	h.appConfig.HintBar = &off
	h.updateHandleWindowSizeEvent(tea.WindowSizeMsg{Width: 120, Height: 30})
	require.NotContains(t, h.View(), "kill", "hint_bar=false must not render the bar")
	on := true
	h.appConfig.HintBar = &on
```

with:

```go
	h.updateHandleWindowSizeEvent(tea.WindowSizeMsg{Width: 120, Height: 30})
	require.Contains(t, h.View(), "kill", "default navigation renders the hint bar")
	withBar := h.paneContentHeight()

	// hint_bar off: plain navigation goes chrome-free. The bottom row stays reserved
	// (so the panes don't grow), but it renders blank instead of the hints —
	// assembleHome/applySettingChange seed menu.quiet from the setting.
	off := false
	h.appConfig.HintBar = &off
	h.menu.SetQuiet(true)
	h.updateHandleWindowSizeEvent(tea.WindowSizeMsg{Width: 120, Height: 30})
	require.NotContains(t, h.View(), "kill", "hint_bar=false must not render the bar's hints")
	require.Equal(t, withBar, h.paneContentHeight(),
		"the reserved row keeps the panes the same height with the bar off (#438)")
	on := true
	h.appConfig.HintBar = &on
	h.menu.SetQuiet(false)
```

- [ ] **Step 12: Add the drift-guard test**

In `app/app_driftcheck_test.go`, append (mirrors `TestDriftFoundMsg_AckRecordedWhenHintShown`'s construction and house style — standard `testing` asserts):

```go
// With the hint bar off, a background drift hint stays badge-only (#108/#438): the
// clean-mode row is always reserved, so without the guard the unsolicited toast
// would ride it. No toast, the persistent badge carries it, and the hint stays
// re-armed (no ack) — exactly as when a modal owns the screen.
func TestDriftFoundMsg_HintBarOffStaysBadgeOnly(t *testing.T) {
	st := config.DefaultState()
	s := spinner.New()
	off := false
	m := &home{
		ctx:       context.Background(),
		state:     stateDefault,
		list:      ui.NewList(&s),
		menu:      ui.NewMenu(),
		appConfig: config.DefaultConfig(),
		appState:  st,
	}
	m.appConfig.HintBar = &off
	m.menu.SetQuiet(true)

	agents := []doctor.Result{
		{Key: agent.KeyClaude, Name: "Claude Code", Installed: "2.1.179", Status: doctor.StatusDrifted},
	}
	m.Update(driftFoundMsg{agents: agents})

	if m.menu.HasNotice() {
		t.Errorf("a drift toast rendered with the hint bar off; it must stay badge-only (#108)")
	}
	if got := m.appState.GetAckedDrift(); len(got) != 0 {
		t.Fatalf("ack recorded despite the badge-only path: %v", got)
	}
	m.list.SetSize(80, 24)
	if out := m.list.String(); !strings.Contains(out, "stale") {
		t.Errorf("drift badge not shown in clean mode; panel:\n%s", out)
	}
}
```

- [ ] **Step 13: Run both packages to verify all green**

Run: `go test ./app/ ./ui/ -count=1`
Expected: PASS (all packages, including the previously-flipped tests and both guard-pinning tests).

- [ ] **Step 14: Run the full gate**

Run: `just ci`
Expected: PASS (build + vet + fmt-check + lint + test + cover). If `lint` reports paths outside this worktree, it's stale global cache: `golangci-lint cache clean` and re-run.

- [ ] **Step 15: Commit**

```bash
git add app/app_layout.go app/app_construct.go app/app_updatecheck.go app/app_msgs.go \
        app/notice_test.go app/menu_visibility_test.go app/app_driftcheck_test.go
git commit -m "fix(app): keep notices height-neutral when the hint bar is hidden (#438)"
```

---

## Manual verification (after Task 2)

Build and run with the hint bar off, then flash a notice and confirm no reflow:

```bash
just build
# In the TUI: press , to open Settings, turn Hint bar off, Esc out.
# Copy a branch name (y) or queue a prompt; watch the pane content.
```

Expected: the "branch copied" notice appears on the bottom row and clears after ~5s with **no** vertical shift of the list/preview content. A background "update available" or drift condition shows **only** the Sessions-panel badge (no bottom-row toast) while the bar is off.
