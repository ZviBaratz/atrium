# Design: reserve the notice row in clean mode (#438)

**Date:** 2026-07-21
**Status:** Approved
**Issue:** #438 — "Notices shift the layout when the hint bar is hidden"

## Goal

When the hint bar is hidden (`hint_bar: false`, "chrome-free" mode), a transient
notice ("branch copied", "queued for …", a validation error) must **not** change
the frame's layout. Today it does: the notice appears on a fallback row that the
layout grows to fit, bumping the pane content up by one row, then drops back down
~5 s later when the notice clears. The one state where the user asked for a still,
quiet frame is the one that jumps.

The fix extends the height-stability guarantee the hint bar *already* provides
(a notice replaces the hints on the bar's reserved row, so the frame never
resizes) to the bar-off case. The bottom row stays **always reserved** during
plain navigation; hiding the bar changes only what that row *renders* (hints →
blank), never whether it *exists*. This is the "clean instead of hidden" framing
from the issue.

**Accepted cost:** in clean mode the bottom row is always reserved, so pane
content is one row shorter than today even when idle. The row is blank
(invisible); this is the deliberate price of an ironclad no-reflow guarantee.

## Context

The bottom of the frame is two stacked single-row components:

1. **Hint bar** (`ui.Menu`) — the always-on keybinding strip, which doubles as
   the home for transient notices. A notice *replaces* the hint text on the bar's
   already-reserved row (`ui/menu.go:357-369`), so with the bar up the frame
   height never changes. `app/notice_test.go` and `ui/menu_notice_test.go` pin
   this.
2. **Error box** (`ui.ErrBox`) — a *fallback* row that carries a notice only when
   the hint bar isn't occupying a row (`ui/err.go:11-14`).

`menuVisible()` (`app/app_layout.go:134-147`) is the single "is the bottom row
reserved?" predicate. It drives three sites **in lockstep**:

- the height budget (`app_layout.go:44` and, for the divider Y-bound,
  `paneContentHeight` at `:168`),
- the `View()` append of the menu row (`app/app.go:561`),
- menu sizing (`app_layout.go:124`).

Its `stateDefault` branch currently returns
`m.generatingName || m.actionInFlight || m.appConfig.GetHintBar()` (`:145`). With
the bar off and no progress in flight it returns **false**, so the menu row is
removed and the panes reclaim it. A notice then falls to the errBox path
(`app_feedback.go:104` → `:130-131`): `errBox.SetNotice(...)` +
`recomputeLayout()` flips `errBox.HasContent()` true, the budget subtracts a row
(`app_layout.go:47-55`), `View()` appends the errBox row (`app.go:564`), and the
panes shrink by one. `hideErrMsg` clears it and re-grows them ~5 s later
(`app_update.go:58-64`). **That grow/shrink is the reflow.**

The reflow is structural: it happens because the *row count* changes. The fix
holds the row count constant in `stateDefault` by keeping the menu row reserved
unconditionally, and moves the hints-vs-blank decision into the menu itself.

### Why this scoping is safe

The change flips `menuVisible()` to always-true **only in `stateDefault`**. Every
other state is unaffected:

- Inline states (`stateFilter`, `stateVisual`, `stateDiffComment`) and
  progress (`generatingName`, `actionInFlight`) already force the row visible
  regardless of `hint_bar`; they carry unique info and must never blank.
- Overlay states (`statePrompt`, `stateRename`, `stateConfirm`, `stateHelp`,
  `stateInfo`, `stateSettings`, `stateWelcome`, `stateAccounts`, `stateHistory`,
  `stateQueue`, `stateCmdLog`) keep `menuVisible() == false`. A notice there
  still falls back to the errBox row, but the modal is composited on top, so that
  reflow is hidden — no visible jump. **`ErrBox` therefore stays as-is**; it is
  the fallback for overlay-state notices, not dead code.

The blank render is scoped to menu `StateDefault`/`StateEmpty` only. Verified
that when the row is forced visible for progress, the menu is in
`StateGeneratingName` (set beside `generatingName = true` at `app_keys.go:599-600`)
or `StateBusy` (set beside `actionInFlight = true` at `app_session.go:1475-1476`),
and those states survive the periodic `SetInstance` ticks
(`ui/menu.go:174-184`, which only rewrites `StateDefault`/`StateEmpty`). So
blanking `StateDefault`/`StateEmpty` can never erase a progress or inline line.

### Background notices stay badge-only (#108) — scope guard

Two notice classes reach the bottom bar, and they are treated differently:

- **User-action acks and errors** (`handleInfoNotice`, `handleError`,
  `warnMissingProgram`) go through `flashNotice`. These are the ones the #438
  reflow afflicts — with the bar off they fall back to the errBox row. The fix
  routes them onto the always-reserved menu row; that is the intent.
- **Background/unsolicited notices** — the update-available toast
  (`handleUpdateNotice`, `app_updatecheck.go:137-144`) and the drift hint
  (`handleDriftFound`, `app_msgs.go:25-51`) — call `showMenuNotice` **directly**
  and, with the bar off, deliberately show **no toast**: the persistent
  Sessions-panel badge carries them, keeping the chrome-free frame quiet (#108).
  `TestUpdateBadge_PersistsWithHintBarOff` pins this.

Flipping `menuVisible()` to always-true in `stateDefault` would make
`showMenuNotice` *succeed* for these background callers in clean mode, flashing a
toast the #108 design deliberately suppresses. **Decision (confirmed with the
maintainer): preserve #108** — background notices stay badge-only when the bar is
off. Each background caller gets a `!GetHintBar()` guard so the always-reserved
row never pulls its unsolicited toast onto the quieted frame (changes 5–6).
`showMenuNotice`'s existing nil-return still covers the orthogonal "a modal owns
the screen" case for both.

## Changes

### 1. `ui/menu.go` — a `quiet` flag

Add a field to `Menu`:

```go
// quiet, set from the hint_bar setting, blanks the always-on hint line in the
// bar's reserved row (StateDefault/StateEmpty) so chrome-free mode keeps the row
// but renders nothing on it. A notice still overrides the blank (it is checked
// first in String), and the contextual states (Busy/GeneratingName/Filter/…)
// still render — they are forced visible precisely because they carry unique
// info even with the bar off.
quiet bool
```

Add the setter:

```go
// SetQuiet toggles chrome-free rendering of the always-on hint line. The menu
// row is still reserved by the layout (menuVisible stays true in stateDefault);
// quiet only decides whether that reserved row shows the hints or a blank line.
func (m *Menu) SetQuiet(quiet bool) { m.quiet = quiet }
```

In `String()`, immediately **after** the `if m.notice != ""` branch (so a notice
always wins) and **before** the `switch m.state`:

```go
// Chrome-free: the row stays reserved (menuVisible), but the always-on hint sets
// render blank. Only StateDefault/StateEmpty blank — the contextual states below
// (Filter/Visual/DiffComment/Busy/GeneratingName) are forced visible to teach a
// gesture or report progress, so they must render even when the bar is off.
if m.quiet && (m.state == StateDefault || m.state == StateEmpty) {
    return centerInBox(m.width, m.height, "")
}
```

`centerInBox(m.width, m.height, "")` returns a width×height blank so
`lipgloss.JoinVertical` counts it as exactly one row and it overwrites any prior
frame content on that row.

### 2. `app/app_layout.go` — `menuVisible()` default branch

```go
default: // stateDefault (and the empty list)
    // The bottom row is always reserved during plain navigation, so a transient
    // notice never resizes the frame (#438). generatingName / actionInFlight are
    // subsumed here; they still drive the menu's StateGeneratingName / StateBusy
    // content. When hint_bar is off the row renders blank (Menu.quiet), giving a
    // still, chrome-free frame that a notice can ride without a layout shift.
    return true
```

Reword the function's closing doc paragraph: hiding the bar no longer removes the
row — the row stays and renders blank.

### 3. `app/app_layout.go` — `applySettingChange("hint_bar")`

```go
case "hint_bar":
    // The row is always reserved (menuVisible); hint_bar only toggles the bar
    // between its hints and a blank line, so toggling it no longer changes the
    // row count — the panes never resize on toggle either.
    m.menu.SetQuiet(!m.appConfig.GetHintBar())
    m.recomputeLayout()
```

(`recomputeLayout` is retained for a clean repaint; the row count is unchanged.)

### 4. `app/app_construct.go` — `assembleHome`

Seed the flag so a `hint_bar: false` config starts quiet, right after the struct
literal (where `appConfig` is in scope):

```go
h.menu.SetQuiet(!appConfig.GetHintBar())
```

### 5. `app/app_updatecheck.go` — `handleUpdateNotice` guard

Keep the background update notice badge-only with the bar off. Guard at the top,
before the `showMenuNotice` attempt:

```go
// With the hint bar off, the update notice stays badge-only (#108): the row is
// always reserved in clean mode now (#438), so without this guard the toast would
// ride it — an unsolicited flash on the frame the user quieted. Buffer it like any
// undeliverable toast; the panel badge (set by the caller) is the durable signal.
if !m.appConfig.GetHintBar() {
    m.pendingUpdateNotice = text
    return nil
}
```

This reproduces today's outcome (buffered notice + persistent badge) and leaves
`showMenuNotice`'s nil path to handle the modal-overlay case unchanged.

### 6. `app/app_msgs.go` — `handleDriftFound` guard

Gate the toast attempt on the bar being on, so drift stays badge-only with the bar
off (badge shown, no ack, hint re-arms):

```go
// With the hint bar off, the drift hint stays badge-only (#108/#438): don't even
// attempt the toast (the clean-mode row is always reserved and would carry it).
// Leaving cmd nil falls through to the persistent-badge path below.
var cmd tea.Cmd
if m.appConfig.GetHintBar() {
    cmd = m.showMenuNotice(fmt.Sprintf("⚠ agent heuristics may be stale — run `%s doctor`", m.hintBinName()), ui.NoticeInfo)
}
if cmd == nil {
    // ... existing badge path: SetDriftBadge, no ack, return m, nil
}
```

## Testing

### New — the regression pin for #438 (`app/notice_test.go`)

`TestHandleInfoNotice_HintBarOffNoLayoutShift`: build a `stateDefault` home, set
`hint_bar` off, `SetQuiet(true)`, size it (e.g. 80×24). Capture
`paneContentHeight()` and `lipgloss.Height(View())`. Flash a notice; assert both
are **unchanged** and `h.menu.HasNotice()` is true while `h.errBox.HasContent()`
is false. Fire `hideErrMsg{gen: h.noticeGen}`; assert both are unchanged again.
This is the property the issue is about.

### New — menu quiet behavior (`ui/menu_notice_test.go`)

`TestMenu_QuietBlanksHintsNotNotices`: `SetSize`, `SetQuiet(true)`,
`SetState(StateDefault)` → `String()` contains no hint key (e.g. not "kill") and
is a single row. Then `SetNotice(...)` → the notice text shows despite quiet.
Then `SetState(StateBusy)` + `SetBusy("pushing…")` → "pushing…" shows despite
quiet (contextual states are never blanked).

### Rewrite — the errBox-fallback tests now ride the menu row

In `stateDefault` the notice rides the reserved menu row, not the errBox. Update
these in `app/notice_test.go`:

- `TestHandleError_HintBarOffFallsBackToErrRow` →
  `TestHandleError_HintBarOffRidesReservedMenuRow`: assert `h.menu.HasNotice()`,
  `!h.errBox.HasContent()`.
- `TestHandleInfoNotice_HintBarOffFallsBackToErrRow` → rides the menu row, info
  level (`h.menu.HasNotice()`, `!h.errBox.HasError()`).
- `TestWarnMissingProgram_HintBarOffFallsBackToErrRow` → rides the menu row.
- `TestFlashNotice_MenuNoticeClearsStaleErrBox`: the errBox→menu handoff is no
  longer reachable from `stateDefault` (the menu row is always available there).
  Re-pin the handoff by driving it through an **overlay state**
  (`menuVisible() == false`): flash a notice → errBox row; return to
  `stateDefault` and flash again → menu row carries it and the stale errBox row is
  cleared with a `recomputeLayout` so the frame stays full height.

### Update — visibility tests (`app/menu_visibility_test.go`)

- `TestMenuVisible_ByState`: the `hint_bar` off + `stateDefault` +
  no gen/inflight case now expects `menuVisible() == true` (line ~51); reword the
  comment ("the row is always reserved in stateDefault; the bar renders blank").
  The gen/inflight/filter cases (still true) are unchanged.
- `TestView_HintBarContextual`: the bar-off branch must now call
  `h.menu.SetQuiet(true)` when it turns `hint_bar` off (production seeds this via
  `applySettingChange`/`assembleHome`; this struct-literal home bypasses both).
  With quiet set, the menu renders blank so `NotContains("kill")` still holds.
  Strengthen it: capture `paneContentHeight()` while bar-on and assert it is
  **equal** bar-off — both reserve the row, so the panes no longer grow when the
  bar is hidden (the reserved-row invariant; frame height alone is trivially
  `windowHeight` either way and proves nothing).

### Background-notice guards (#108)

- **Update guard — already pinned.** `TestUpdateBadge_PersistsWithHintBarOff`
  runs in `stateDefault` (`newUpdateHome` → `newCreateFormHome`). After the
  `menuVisible()` flip, *without* the guard this test would fail (the toast would
  ride the reserved row); *with* the guard it passes (buffered + badge,
  `menu.HasNotice()` false). So the existing test now pins the update guard — no
  new update test needed. It stays green.
- **New drift-guard test (`app/app_driftcheck_test.go`)**
  `TestDriftFoundMsg_HintBarOffStaysBadgeOnly`: mirror
  `TestDriftFoundMsg_AckRecordedWhenHintShown` (state `stateDefault`, menu
  present) but with `hint_bar` off + `SetQuiet(true)`. Assert `menu.HasNotice()`
  is false, `GetAckedDrift()` is empty (hint re-arms), and the list render
  contains "stale" (the badge carries it). The existing drift tests run with
  `hint_bar: true`, so this is the only test covering the bar-off drift path.

### Unchanged and still green

`TestHandleError_MenuCarriesToastWithoutLayoutShift` (bar-on guarantee),
`ui/menu_notice_test.go`'s existing notice tests (default `quiet == false`), the
two existing drift tests (`hint_bar: true`; the nil-menu and ack-recorded paths
are unchanged), the divider tests, and the welcome test (all run bar-on).

## Edge cases

- **Empty list, bar off.** `menuVisible()` is true, menu is `StateEmpty` → blank
  row. The "No sessions yet" CTA lives in the list pane, not the menu
  (`TestView_EmptyStateShowsCTA`), so it is unaffected.
- **Notice wider than the row.** Unchanged: the menu truncates a notice to width
  (`ui/menu.go:365-367`) rather than wrapping, so a long notice can't grow the
  reserved row.
- **Mouse divider bound.** `paneContentHeight` subtracts the now-always-reserved
  menu row in clean mode, so the divider Y-bound shifts up one row to match the
  panes — automatic, since it reads the same `menuVisible()`.
- **Multi-line / over-wide errors in `stateDefault`.** Still routed to the
  persistent info modal by `handleError`'s `!m.errBox.Fits(err)` guard
  (`app_feedback.go:41`) before `flashNotice` is reached; unchanged.

## Verification

`just ci` (build + vet + fmt-check + lint + test + cover) must pass, plus a
manual check in a live terminal: with `hint_bar` off, copy a branch name and
confirm the pane content does not move when the notice appears or clears.
