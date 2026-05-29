# Design: shift+tab to reverse-cycle panes

**Date:** 2026-05-29
**Status:** Approved

## Goal

Add `shift+tab` as a complement to the existing `tab` keybinding. Where `tab`
cycles the right-pane tabs forward (Preview → Diff → Terminal → wrap),
`shift+tab` cycles them backward (Terminal → Diff → Preview → wrap). This
mirrors the near-universal TUI/browser convention for `shift+tab`.

## Context

The keybinding system is cleanly layered:

- `keys.GlobalKeyStringsMap` maps Bubble Tea's `msg.String()` (e.g.
  `"shift+tab"`) to a `KeyName` enum value.
- `app.go`'s `handleKeyPress` looks up the `KeyName` and switches on it.
- The actual tab-cycling logic lives in one method,
  `TabbedWindow.Toggle()`:
  `w.activeTab = (w.activeTab + 1) % len(w.tabs)`.

Adding `shift+tab` is therefore purely additive — no existing line changes
behavior.

## Changes

All changes are additive except the single help-row text update.

### 1. `keys/keys.go`

- Add a `KeyShiftTab` enum constant near `KeyTab` (in the special-keybinding
  block).
- `GlobalKeyStringsMap`: add `"shift+tab": KeyShiftTab`.
- `GlobalkeyBindings`: add
  `KeyShiftTab: key.NewBinding(key.WithKeys("shift+tab"), key.WithHelp("shift+tab", "prev tab"))`.

### 2. `ui/tabbed_window.go`

Add a method:

```go
func (w *TabbedWindow) ToggleReverse() {
    w.activeTab = (w.activeTab - 1 + len(w.tabs)) % len(w.tabs)
}
```

The `+ len(w.tabs)` term keeps the operand non-negative, since Go's `%` can
return a negative result for a negative left operand.

### 3. `app/app.go`

Add a case immediately after the existing `keys.KeyTab` case, with parallel
structure:

```go
case keys.KeyShiftTab:
    m.tabbedWindow.ToggleReverse()
    m.menu.SetActiveTab(m.tabbedWindow.GetActiveTab())
    return m, m.instanceChanged()
```

### 4. `app/help.go`

Update the existing navigate row to surface both bindings:

```go
helpRow("tab / shift-tab", "next / prev pane"),
```

(Replaces `helpRow("tab", "preview / diff / terminal")`.)

The bottom menu bar (`ui/menu.go`) is intentionally left unchanged to avoid
crowding the limited horizontal footer space; `shift+tab` is documented in the
`?` help screen only.

## Testing

Add a unit test for `ToggleReverse()` alongside the existing tabbed-window
tests, asserting:

- Wrap-around from `PreviewTab` backward lands on `TerminalTab`.
- The full reverse sequence Preview → Terminal → Diff → Preview.

The keymap and dispatch wiring is declarative configuration and does not need a
dedicated test.

## Edge cases

- **Backtab ANSI sequence:** Some terminals send `shift+tab` as the ANSI
  "backtab" sequence (`CSI Z`). Bubble Tea normalizes this to the string
  `"shift+tab"` (the same `tea.KeyShiftTab` already handled in
  `ui/overlay/textInput.go`), so the `GlobalKeyStringsMap` string lookup is
  sufficient — no special decoding required.

## Verification

`just build` and `just test` must both pass before the change is considered
complete.
