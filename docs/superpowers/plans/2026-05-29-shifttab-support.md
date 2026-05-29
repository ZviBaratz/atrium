# shift+tab Reverse Pane Cycling Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a `shift+tab` keybinding that cycles the right-pane tabs backward (Terminal → Diff → Preview → wrap), complementing the existing `tab` forward cycle.

**Architecture:** Purely additive change across the existing keybinding layers: a new `KeyShiftTab` enum + string-map + binding entry in `keys/keys.go`, a `ToggleReverse()` method on `TabbedWindow`, a dispatch case in `app/app.go`, and a help-row text update. The only new logic worth a test is the reverse modulo arithmetic.

**Tech Stack:** Go, Bubble Tea (`tea.KeyMsg`), Bubbles `key` package, lipgloss, testify.

---

### Task 1: Reverse-cycle method on TabbedWindow (TDD)

**Files:**
- Create: `ui/tabbed_window_test.go`
- Modify: `ui/tabbed_window.go` (add method after `Toggle()` at line 113-115)

- [ ] **Step 1: Write the failing test**

Create `ui/tabbed_window_test.go`. The window has three tabs (`PreviewTab=0`, `DiffTab=1`, `TerminalTab=2`). `ToggleReverse()` must decrement with wrap-around. Panes are not touched by `ToggleReverse`/`GetActiveTab`, so `nil` panes are fine.

```go
package ui

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTabbedWindow_ToggleReverse(t *testing.T) {
	w := NewTabbedWindow(nil, nil, nil)
	require.Equal(t, PreviewTab, w.GetActiveTab(), "starts on Preview")

	w.ToggleReverse()
	require.Equal(t, TerminalTab, w.GetActiveTab(), "reverse from Preview wraps to Terminal")

	w.ToggleReverse()
	require.Equal(t, DiffTab, w.GetActiveTab(), "reverse from Terminal lands on Diff")

	w.ToggleReverse()
	require.Equal(t, PreviewTab, w.GetActiveTab(), "reverse from Diff lands on Preview")
}

func TestTabbedWindow_ToggleAndReverseAreInverse(t *testing.T) {
	w := NewTabbedWindow(nil, nil, nil)
	w.Toggle()        // Preview -> Diff
	w.ToggleReverse() // Diff -> Preview
	require.Equal(t, PreviewTab, w.GetActiveTab(), "Toggle then ToggleReverse returns to start")
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `just test 2>&1 | grep -A3 ToggleReverse` (or `GO=/path/to/go just test`)
Expected: FAIL — compile error `w.ToggleReverse undefined (type *TabbedWindow has no field or method ToggleReverse)`.

- [ ] **Step 3: Write minimal implementation**

In `ui/tabbed_window.go`, add immediately after the existing `Toggle()` method (after line 115):

```go
// ToggleReverse cycles to the previous tab, wrapping from the first tab to the
// last. It is the complement of Toggle. The + len(w.tabs) term keeps the
// operand non-negative, since Go's % can return a negative result.
func (w *TabbedWindow) ToggleReverse() {
	w.activeTab = (w.activeTab - 1 + len(w.tabs)) % len(w.tabs)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `just test 2>&1 | tail -20`
Expected: PASS — package `ui` tests green (ignore the known-flaky `TestSessionDeathStopsProbing` in `session/tmux`, unrelated here).

- [ ] **Step 5: Commit**

```bash
git add ui/tabbed_window.go ui/tabbed_window_test.go
git commit -m "feat: add ToggleReverse for reverse pane cycling"
```

---

### Task 2: Register the shift+tab keybinding

**Files:**
- Modify: `keys/keys.go` (enum block ~line 20-21; `GlobalKeyStringsMap` ~line 74; `GlobalkeyBindings` ~line 143-146)

This task is declarative configuration — no separate unit test; correctness is verified by the build plus Task 3's wiring.

- [ ] **Step 1: Add the enum constant**

In `keys/keys.go`, in the special-keybinding area, change:

```go
	KeyTab        // Tab is a special keybinding for switching between panes.
	KeySubmitName // SubmitName is a special keybinding for submitting the name of a new instance.
```

to:

```go
	KeyTab        // Tab is a special keybinding for switching between panes.
	KeyShiftTab   // ShiftTab cycles between panes in reverse order.
	KeySubmitName // SubmitName is a special keybinding for submitting the name of a new instance.
```

- [ ] **Step 2: Add the string-map entry**

In `GlobalKeyStringsMap`, after the `"tab":` line:

```go
	"tab":        KeyTab,
	"shift+tab":  KeyShiftTab,
```

- [ ] **Step 3: Add the binding**

In `GlobalkeyBindings`, after the `KeyTab` binding block (ends at line 146):

```go
	KeyTab: key.NewBinding(
		key.WithKeys("tab"),
		key.WithHelp("tab", "switch tab"),
	),
	KeyShiftTab: key.NewBinding(
		key.WithKeys("shift+tab"),
		key.WithHelp("shift+tab", "prev tab"),
	),
```

- [ ] **Step 4: Verify it builds**

Run: `just build`
Expected: builds cleanly to `./bin/atrium`, no errors.

- [ ] **Step 5: Commit**

```bash
git add keys/keys.go
git commit -m "feat: register shift+tab keybinding"
```

---

### Task 3: Dispatch shift+tab in the app

**Files:**
- Modify: `app/app.go` (after the `keys.KeyTab` case at lines 792-795)

- [ ] **Step 1: Add the dispatch case**

In `app/app.go`'s `handleKeyPress` switch, the existing case reads:

```go
	case keys.KeyTab:
		m.tabbedWindow.Toggle()
		m.menu.SetActiveTab(m.tabbedWindow.GetActiveTab())
		return m, m.instanceChanged()
```

Add immediately after it:

```go
	case keys.KeyShiftTab:
		m.tabbedWindow.ToggleReverse()
		m.menu.SetActiveTab(m.tabbedWindow.GetActiveTab())
		return m, m.instanceChanged()
```

- [ ] **Step 2: Verify it builds**

Run: `just build`
Expected: builds cleanly, no errors.

- [ ] **Step 3: Verify tests pass**

Run: `just test 2>&1 | tail -20`
Expected: PASS (ignore the known-flaky `TestSessionDeathStopsProbing`).

- [ ] **Step 4: Commit**

```bash
git add app/app.go
git commit -m "feat: handle shift+tab to cycle panes in reverse"
```

---

### Task 4: Surface shift+tab in the help screen

**Files:**
- Modify: `app/help.go:60`

- [ ] **Step 1: Update the help row**

In `app/help.go`, change line 60 from:

```go
		helpRow("tab", "preview / diff / terminal"),
```

to:

```go
		helpRow("tab / shift-tab", "next / prev pane"),
```

- [ ] **Step 2: Verify it builds and tests pass**

Run: `just build && just test 2>&1 | tail -20`
Expected: builds cleanly; tests PASS (ignore the known-flaky `TestSessionDeathStopsProbing`).

- [ ] **Step 3: Commit**

```bash
git add app/help.go
git commit -m "docs: document shift+tab in the help screen"
```

---

### Task 5: Final verification

- [ ] **Step 1: Full build + test**

Run: `just build && just test`
Expected: binary builds and version-stamps; all tests pass except possibly the documented-flaky `TestSessionDeathStopsProbing` in `session/tmux` (unrelated to this change — re-run with `just test -- -skip TestSessionDeathStopsProbing` to confirm if it appears).

- [ ] **Step 2: Manual smoke check (optional but recommended)**

Run `./bin/atrium`, select a session, press `tab` a few times (Preview → Diff → Terminal → Preview), then `shift+tab` to confirm it walks backward. Press `?` and confirm the Navigate section shows `tab / shift-tab — next / prev pane`.
