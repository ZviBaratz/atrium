# Queue-management overlay Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give the user a `Q`-opened overlay to list a selected session's pending prompts and cancel any that isn't actively being delivered, so the prompt queue is no longer write-only.

**Architecture:** A new `promptMu`-guarded `CancelQueuedPrompt(idx, expectedText)` on `session.Instance` performs a matched, in-flight-guarded removal; a new dumb-view `QueueOverlay` (modelled on `RenameOverlay`) renders a snapshot the app pushes in and reports the user's cancel intent back out; the app wires a `Q` key + `stateQueue` that opens the overlay, performs cancels against the instance the overlay was opened for, and refreshes. A count is added to the existing `↦` row glyph.

**Tech Stack:** Go 1.25, Bubble Tea (`charmbracelet/bubbletea`), lipgloss, `muesli/reflow/truncate`, testify. Design spec: `docs/superpowers/specs/2026-07-13-queue-management-overlay-design.md`.

## Global Constraints

- **Go 1.25** — `slices.Delete` is available.
- **Verify every change with `just build` AND `just test`.** `just test` is the source of truth (per `CLAUDE.md`). `go`/`just` are mise-managed; if the shims aren't on `PATH`, pass go explicitly: `GO=/path/to/go just test`. `golangci-lint` is not installed locally — don't rely on `just lint`.
- **Tests stay hermetic** — anything reaching `config`/`state`/`tmux` sets `HOME` to a temp dir. The `app` package already does this in `app/app_test.go`'s `TestMain`; the `session` and `ui/overlay` tests here touch neither, but keep new instances rooted at `t.TempDir()`.
- **Commits:** Conventional Commits, lowercase (`feat: …`), each ending with:
  `Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>`
- **The overlay holds no `session` type** — it is a pure view over primitives (`[]string` + `bool`); the app maps `Instance` state to/from it.
- **Cancels target `m.queueTarget`** (the instance the overlay was opened for), never the live selection — selection can move while the overlay is open.

---

## File Structure

**Modified:**
- `session/instance.go` — add `CancelQueuedPrompt` + `QueueView` next to `ClearPrompt` (the `promptMu` state machine).
- `keys/keys.go` — add `KeyQueue` (enum + binding + keymap entry `"Q"`).
- `app/app.go` — add `stateQueue` const, `queueOverlay`/`queueTarget` fields, and the `View()` render branch.
- `app/app_layout.go` — hide the hint bar behind `stateQueue` (`menuVisible`) and size the overlay on resize.
- `app/app_update.go` — dispatch `KeyQueue` → `openQueue`, and `stateQueue` → `handleQueueState`.
- `app/app_keys.go` — `openQueue`, `handleQueueState`, `dismissQueueOverlay`.
- `app/help.go` — one help row for `Q`.
- `ui/list_render.go` — append the pending count to the `↦` glyph.

**Created:**
- `ui/overlay/queueOverlay.go` — the `QueueOverlay` view.
- `ui/overlay/queueOverlay_test.go` — its unit tests.
- `session/queue_cancel_test.go` — `CancelQueuedPrompt` / `QueueView` tests.
- `app/queue_test.go` — open/cancel integration tests.

---

## Task 1: Domain — `CancelQueuedPrompt` + `QueueView`

**Files:**
- Modify: `session/instance.go` (add after `ClearPrompt`, ~line 1293; add `"slices"` to the import block at lines 7-20)
- Test: `session/queue_cancel_test.go` (new, `package session`)

**Interfaces:**
- Consumes: existing `QueueFollowupPrompt(string)`, `ClaimPrompt() (string, bool)`, `QueueLen() int`, `Prompt() string`, `session.NewInstance(session.InstanceOptions{...})`.
- Produces:
  - `func (i *Instance) CancelQueuedPrompt(idx int, expectedText string) bool`
  - `func (i *Instance) QueueView() (texts []string, headInFlight bool)`

- [ ] **Step 1: Write the failing tests**

Create `session/queue_cancel_test.go`:

```go
package session

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func newQueueInstance(t *testing.T) *Instance {
	t.Helper()
	i, err := NewInstance(InstanceOptions{Title: "q", Path: t.TempDir(), Program: "echo"})
	require.NoError(t, err)
	return i
}

func TestCancelQueuedPrompt(t *testing.T) {
	t.Run("removes a tail entry, preserving order", func(t *testing.T) {
		i := newQueueInstance(t)
		i.QueueFollowupPrompt("a")
		i.QueueFollowupPrompt("b")
		i.QueueFollowupPrompt("c")
		require.True(t, i.CancelQueuedPrompt(1, "b"))
		texts, _ := i.QueueView()
		require.Equal(t, []string{"a", "c"}, texts)
	})

	t.Run("removes the head when not in flight", func(t *testing.T) {
		i := newQueueInstance(t)
		i.QueueFollowupPrompt("a")
		i.QueueFollowupPrompt("b")
		require.True(t, i.CancelQueuedPrompt(0, "a"))
		texts, _ := i.QueueView()
		require.Equal(t, []string{"b"}, texts)
	})

	t.Run("refuses the in-flight head", func(t *testing.T) {
		i := newQueueInstance(t)
		i.QueueFollowupPrompt("a")
		_, ok := i.ClaimPrompt() // raises promptInFlight on the head
		require.True(t, ok)
		require.False(t, i.CancelQueuedPrompt(0, "a"))
		require.Equal(t, 1, i.QueueLen(), "the in-flight head stays")
	})

	t.Run("text mismatch is a no-op", func(t *testing.T) {
		i := newQueueInstance(t)
		i.QueueFollowupPrompt("a")
		require.False(t, i.CancelQueuedPrompt(0, "stale"))
		require.Equal(t, 1, i.QueueLen())
	})

	t.Run("out-of-range index is a no-op", func(t *testing.T) {
		i := newQueueInstance(t)
		i.QueueFollowupPrompt("a")
		require.False(t, i.CancelQueuedPrompt(-1, "a"))
		require.False(t, i.CancelQueuedPrompt(5, "a"))
		require.Equal(t, 1, i.QueueLen())
	})
}

func TestQueueView(t *testing.T) {
	i := newQueueInstance(t)
	texts, inFlight := i.QueueView()
	require.Empty(t, texts)
	require.False(t, inFlight)

	i.QueueFollowupPrompt("a")
	i.QueueFollowupPrompt("b")
	texts, inFlight = i.QueueView()
	require.Equal(t, []string{"a", "b"}, texts)
	require.False(t, inFlight)

	_, _ = i.ClaimPrompt()
	_, inFlight = i.QueueView()
	require.True(t, inFlight, "QueueView reports the head in flight after ClaimPrompt")
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `just test 2>&1 | grep -A3 queue_cancel` (or `GO=/path/to/go just test`)
Expected: compile failure — `i.CancelQueuedPrompt undefined` / `i.QueueView undefined`.

- [ ] **Step 3: Add the `slices` import**

In `session/instance.go`, add `"slices"` to the standard-library group of the import block (lines ~15-20, alphabetical among `context`/`errors`/`fmt`/`strings`/`sync`/`time`):

```go
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"
```

- [ ] **Step 4: Implement the two methods**

In `session/instance.go`, immediately after `ClearPrompt` (which ends ~line 1293):

```go
// CancelQueuedPrompt removes the queued prompt at idx, but only when it still
// matches expectedText and is not the in-flight head. The text match is the same
// double-settle guard ClearPrompt uses: if a delivery popped the head since the
// UI snapshotted the queue, the stale idx no longer matches and the call is a
// safe no-op instead of cancelling the wrong prompt. idx 0 is cancellable only
// while no send is in flight (an actively-delivering head is locked). Returns
// whether an entry was removed.
func (i *Instance) CancelQueuedPrompt(idx int, expectedText string) bool {
	i.promptMu.Lock()
	defer i.promptMu.Unlock()
	if idx < 0 || idx >= len(i.promptQueue) {
		return false
	}
	if idx == 0 && i.promptInFlight {
		return false
	}
	if i.promptQueue[idx].text != expectedText {
		return false
	}
	i.promptQueue = slices.Delete(i.promptQueue, idx, idx+1)
	return true
}

// QueueView returns a read-only snapshot for the management overlay: head-first
// prompt texts plus whether the head is currently being delivered. Taken under
// one lock so headInFlight can't tear away from the texts it describes.
func (i *Instance) QueueView() (texts []string, headInFlight bool) {
	i.promptMu.Lock()
	defer i.promptMu.Unlock()
	if len(i.promptQueue) == 0 {
		return nil, false
	}
	texts = make([]string, len(i.promptQueue))
	for idx, qp := range i.promptQueue {
		texts[idx] = qp.text
	}
	return texts, i.promptInFlight
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `just test 2>&1 | tail -20`
Expected: PASS (whole suite green; `TestCancelQueuedPrompt` and `TestQueueView` included).

- [ ] **Step 6: Commit**

```bash
git add session/instance.go session/queue_cancel_test.go
git commit -m "feat(session): add CancelQueuedPrompt and QueueView for queue management (#286)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Task 2: The `QueueOverlay` view

**Files:**
- Create: `ui/overlay/queueOverlay.go`
- Test: `ui/overlay/queueOverlay_test.go` (new, `package overlay`)

**Interfaces:**
- Consumes: `theme.Current()` (`Borders.Style`, `Palette.Accent`, `OverlayTitleStyle()`, `OverlayHintStyle()`, `AttentionStyle()`), package-local `overlaySelectedStyle()`, `overlayDimStyle()`.
- Produces:
  - `func NewQueueOverlay(name string) *QueueOverlay`
  - `func (q *QueueOverlay) SetQueue(texts []string, headInFlight bool)`
  - `func (q *QueueOverlay) SetMessage(text string)`
  - `func (q *QueueOverlay) HandleKeyPress(msg tea.KeyMsg) (shouldClose bool)`
  - `func (q *QueueOverlay) RemoveRequested() bool` (read-once; clears)
  - `func (q *QueueOverlay) SelectedIndex() int`
  - `func (q *QueueOverlay) SelectedText() string`
  - `func (q *QueueOverlay) IsCanceled() bool`
  - `func (q *QueueOverlay) SetWidth(width int)`
  - `func (q *QueueOverlay) Render() string`

- [ ] **Step 1: Write the failing tests**

Create `ui/overlay/queueOverlay_test.go`:

```go
package overlay

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/require"
)

func runeKey(s string) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)} }

func TestQueueOverlay_RendersHeadFirstWithInFlightMark(t *testing.T) {
	o := NewQueueOverlay("auth")
	o.SetQueue([]string{"fix login", "update tests"}, true)
	out := o.Render()
	require.Contains(t, out, `Queue for "auth"`)
	require.Contains(t, out, "fix login")
	require.Contains(t, out, "update tests")
	require.Contains(t, out, "1.")
	require.Contains(t, out, "2.")
	require.Contains(t, out, queueInFlightMark, "an in-flight head is marked")
}

func TestQueueOverlay_EmptyState(t *testing.T) {
	o := NewQueueOverlay("x")
	o.SetQueue(nil, false)
	require.Contains(t, o.Render(), "no pending prompts")
	require.Equal(t, "", o.SelectedText())
}

func TestQueueOverlay_CursorMovesAndClamps(t *testing.T) {
	o := NewQueueOverlay("x")
	o.SetQueue([]string{"a", "b", "c"}, false)
	require.Equal(t, 0, o.SelectedIndex())
	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyUp}) // clamps at 0
	require.Equal(t, 0, o.SelectedIndex())
	o.HandleKeyPress(runeKey("j"))
	o.HandleKeyPress(runeKey("j"))
	o.HandleKeyPress(runeKey("j")) // clamps at last
	require.Equal(t, 2, o.SelectedIndex())
	require.Equal(t, "c", o.SelectedText())
	o.HandleKeyPress(runeKey("k"))
	require.Equal(t, 1, o.SelectedIndex())
}

func TestQueueOverlay_SetQueueClampsCursor(t *testing.T) {
	o := NewQueueOverlay("x")
	o.SetQueue([]string{"a", "b", "c"}, false)
	o.HandleKeyPress(runeKey("j"))
	o.HandleKeyPress(runeKey("j")) // cursor at 2
	o.SetQueue([]string{"a"}, false)
	require.Equal(t, 0, o.SelectedIndex(), "a shorter queue clamps the cursor")
}

func TestQueueOverlay_RemoveArmsOnceWithSelection(t *testing.T) {
	o := NewQueueOverlay("x")
	o.SetQueue([]string{"a", "b"}, false)
	o.HandleKeyPress(runeKey("j"))
	shouldClose := o.HandleKeyPress(runeKey("d"))
	require.False(t, shouldClose, "d does not close the overlay")
	require.Equal(t, 1, o.SelectedIndex())
	require.Equal(t, "b", o.SelectedText())
	require.True(t, o.RemoveRequested(), "d arms a remove")
	require.False(t, o.RemoveRequested(), "RemoveRequested is read-once")
}

func TestQueueOverlay_EscCancels(t *testing.T) {
	o := NewQueueOverlay("x")
	o.SetQueue([]string{"a"}, false)
	require.True(t, o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEsc}))
	require.True(t, o.IsCanceled())
}

func TestQueueOverlay_MessageShownAndClearedOnSetQueue(t *testing.T) {
	o := NewQueueOverlay("x")
	o.SetQueue([]string{"a"}, true)
	o.SetMessage("can't cancel — prompt is being delivered")
	require.Contains(t, o.Render(), "being delivered")
	o.SetQueue([]string{"a"}, true) // a refresh clears the transient message
	require.False(t, strings.Contains(o.Render(), "being delivered"))
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `just test 2>&1 | grep -A3 queueOverlay`
Expected: compile failure — `NewQueueOverlay undefined`, `queueInFlightMark undefined`.

- [ ] **Step 3: Implement the overlay**

Create `ui/overlay/queueOverlay.go`:

```go
package overlay

import (
	"fmt"
	"strings"

	"github.com/ZviBaratz/atrium/ui/theme"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/reflow/truncate"
)

// QueueOverlay lists a session's pending prompts (head first) and lets the user
// cancel one. It is a dumb view over primitives: the app pushes the queue
// snapshot in via SetQueue and reads the user's intent back out
// (RemoveRequested/SelectedIndex/SelectedText), performing the actual mutation on
// the session.Instance itself. It holds no session type.
type QueueOverlay struct {
	title        string   // the session's display name
	items        []string // head-first prompt texts
	cursor       int
	headInFlight bool
	message      string // transient in-overlay note (e.g. a cancel refusal); cleared by SetQueue
	width        int
	removeReq    bool
	canceled     bool
}

// queueInFlightMark rides the head row while its prompt is being delivered; a
// locked head cannot be cancelled (see Instance.CancelQueuedPrompt), so the mark
// doubles as the "why did d do nothing here" cue.
const queueInFlightMark = "⟳"

// NewQueueOverlay builds the overlay for a session with the given display name.
// Width defaults to a sensible box; the app widens it responsively via SetWidth.
func NewQueueOverlay(name string) *QueueOverlay {
	return &QueueOverlay{title: name, width: 60}
}

// SetQueue replaces the displayed queue, clamps the cursor into range, and clears
// the pending remove request and any transient message, so a refresh after an
// action starts clean.
func (q *QueueOverlay) SetQueue(texts []string, headInFlight bool) {
	q.items = texts
	q.headInFlight = headInFlight
	q.removeReq = false
	q.message = ""
	if q.cursor >= len(q.items) {
		q.cursor = len(q.items) - 1
	}
	if q.cursor < 0 {
		q.cursor = 0
	}
}

// SetMessage sets a transient note rendered above the footer hint (cleared by the
// next SetQueue).
func (q *QueueOverlay) SetMessage(text string) { q.message = text }

// HandleKeyPress moves the cursor, arms a cancel, or closes. It returns true only
// when the overlay should close (esc/ctrl+c); a cancel (d/x) arms removeReq and
// keeps the overlay open so the app can act and refresh.
func (q *QueueOverlay) HandleKeyPress(msg tea.KeyMsg) (shouldClose bool) {
	switch msg.String() {
	case "esc", "ctrl+c":
		q.canceled = true
		return true
	case "up", "k":
		if q.cursor > 0 {
			q.cursor--
		}
		return false
	case "down", "j":
		if q.cursor < len(q.items)-1 {
			q.cursor++
		}
		return false
	case "d", "x":
		if len(q.items) > 0 {
			q.removeReq = true
		}
		return false
	default:
		return false
	}
}

// RemoveRequested reports whether a cancel was armed since the last call and
// clears the flag (read-once), so the app acts on each press exactly once.
func (q *QueueOverlay) RemoveRequested() bool {
	r := q.removeReq
	q.removeReq = false
	return r
}

// SelectedIndex is the 0-based cursor position (head = 0).
func (q *QueueOverlay) SelectedIndex() int { return q.cursor }

// SelectedText is the prompt under the cursor, or "" when the queue is empty.
func (q *QueueOverlay) SelectedText() string {
	if q.cursor < 0 || q.cursor >= len(q.items) {
		return ""
	}
	return q.items[q.cursor]
}

// IsCanceled reports whether the user dismissed the overlay.
func (q *QueueOverlay) IsCanceled() bool { return q.canceled }

// SetWidth sets the box width, flooring it so the box never collapses.
func (q *QueueOverlay) SetWidth(width int) {
	if width < 20 {
		width = 20
	}
	q.width = width
}

// queueFirstLine collapses a possibly multi-line prompt to its first line,
// truncated to fit width with an ellipsis tail.
func queueFirstLine(s string, width int) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = strings.TrimRight(s[:i], " ") + " …"
	}
	return truncate.StringWithTail(s, uint(width), "…")
}

// Render draws the bordered list.
func (q *QueueOverlay) Render() string {
	th := theme.Current()
	box := lipgloss.NewStyle().
		Border(th.Borders.Style).
		BorderForeground(th.Palette.Accent).
		Padding(1, 2).
		Width(q.width)

	inner := q.width - 6 // borders (2) + horizontal padding (2*2)
	if inner < 10 {
		inner = 10
	}

	var b strings.Builder
	b.WriteString(th.OverlayTitleStyle().Render(`Queue for "`+q.title+`"`) + "\n\n")

	if len(q.items) == 0 {
		b.WriteString(overlayDimStyle().Render("no pending prompts") + "\n\n")
	} else {
		for idx, text := range q.items {
			num := fmt.Sprintf("%d. ", idx+1)
			bw := inner - len(num) - 4 // room for the "▸ " cursor and a trailing mark
			if bw < 1 {
				bw = 1
			}
			row := num + queueFirstLine(text, bw)
			if idx == 0 && q.headInFlight {
				row += " " + queueInFlightMark
			}
			if idx == q.cursor {
				b.WriteString(overlaySelectedStyle().Render("▸ " + row))
			} else {
				b.WriteString("  " + row)
			}
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	if q.message != "" {
		b.WriteString(th.AttentionStyle().Render(q.message) + "\n\n")
	}
	b.WriteString(th.OverlayHintStyle().Render("j/k move · d cancel · esc close"))
	return box.Render(b.String())
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `just test 2>&1 | tail -20`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add ui/overlay/queueOverlay.go ui/overlay/queueOverlay_test.go
git commit -m "feat(ui): add QueueOverlay view for pending-prompt management (#286)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Task 3: Wire the `Q` key, `stateQueue`, open & close

Deliverable: `Q` on a session with a queue opens a read-only, cursor-navigable overlay; `esc` closes it; an empty queue is refused with a notice; paused/loading sessions are allowed. (Cancel comes in Task 4.)

**Files:**
- Modify: `keys/keys.go` (enum ~line 67 area; binding block ~line 227; keymap ~line 148)
- Modify: `app/app.go` (state iota ~line 135; `home` fields ~line 303; `View()` branch ~line 486)
- Modify: `app/app_layout.go` (`menuVisible` ~line 102; resize block ~line 80)
- Modify: `app/app_update.go` (state dispatch ~line 622; action case ~line 730)
- Modify: `app/app_keys.go` (`openQueue`, `handleQueueState`, `dismissQueueOverlay` near `openRenameSelected`/`handleRenameState`)
- Modify: `app/help.go` (Handoff section ~line 84)
- Test: `app/queue_test.go` (new, `package app`)

**Interfaces:**
- Consumes: Task 1 `QueueView`; Task 2 `NewQueueOverlay`, `SetQueue`, `SetWidth`, `HandleKeyPress`, `IsCanceled`; existing `m.handleInfoNotice`, `m.instanceChanged`, `m.list.GetSelectedInstance`, `ui.StateDefault`, `overlay.PlaceOverlay`.
- Produces:
  - `keys.KeyQueue`
  - `home.stateQueue`, `home.queueOverlay`, `home.queueTarget`
  - `func (m *home) openQueue() (tea.Model, tea.Cmd)`
  - `func (m *home) handleQueueState(msg tea.KeyMsg) (tea.Model, tea.Cmd)`
  - `func (m *home) dismissQueueOverlay()`

- [ ] **Step 1: Write the failing tests**

Create `app/queue_test.go`:

```go
package app

import (
	"testing"

	"github.com/ZviBaratz/atrium/session"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/require"
)

func queueInstance(t *testing.T, title string) *session.Instance {
	t.Helper()
	inst, err := session.NewInstance(session.InstanceOptions{
		Title: title, Path: t.TempDir(), Program: "echo",
	})
	require.NoError(t, err)
	return inst
}

func TestOpenQueue_EmptyQueueRefused(t *testing.T) {
	h := newCreateFormHome(t)
	inst := queueInstance(t, "q")
	h.list.AddInstance(inst)
	h.list.SelectInstance(inst)

	_, _ = h.openQueue()

	require.Equal(t, stateDefault, h.state, "an empty queue is a dead end — don't open")
	require.Nil(t, h.queueOverlay)
	require.True(t, h.menu.HasNotice(), "the refusal is surfaced as a notice")
}

func TestOpenQueue_OpensWithPendingPrompts(t *testing.T) {
	h := newCreateFormHome(t)
	inst := queueInstance(t, "q")
	inst.QueueFollowupPrompt("a")
	inst.QueueFollowupPrompt("b")
	h.list.AddInstance(inst)
	h.list.SelectInstance(inst)

	_, _ = h.openQueue()

	require.Equal(t, stateQueue, h.state)
	require.NotNil(t, h.queueOverlay)
	require.Same(t, inst, h.queueTarget)
}

func TestOpenQueue_AllowsPausedSession(t *testing.T) {
	h := newCreateFormHome(t)
	inst := queueInstance(t, "q")
	inst.QueueFollowupPrompt("a")
	inst.SetStatus(session.Paused)
	h.list.AddInstance(inst)
	h.list.SelectInstance(inst)

	_, _ = h.openQueue()

	require.Equal(t, stateQueue, h.state, "queue management needs no live pane")
}

func TestQueueOverlay_EscCloses(t *testing.T) {
	h := newCreateFormHome(t)
	inst := queueInstance(t, "q")
	inst.QueueFollowupPrompt("a")
	h.list.AddInstance(inst)
	h.list.SelectInstance(inst)
	_, _ = h.openQueue()

	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyEsc})

	require.Equal(t, stateDefault, h.state)
	require.Nil(t, h.queueOverlay)
	require.Nil(t, h.queueTarget)
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `just test 2>&1 | grep -A3 queue_test`
Expected: compile failure — `h.openQueue undefined`, `h.queueOverlay undefined`, `stateQueue undefined`.

- [ ] **Step 3: Add the key**

In `keys/keys.go`: add `KeyQueue` to the `KeyName` enum (near `KeyQuickSend`, ~line 67):

```go
	KeyQueue // Open the pending-prompt management overlay for the selected session
```

Add the binding to the bindings map, right after the `KeyQuickSend` block (~line 230):

```go
	KeyQueue: key.NewBinding(
		key.WithKeys("Q"),
		key.WithHelp("Q", "manage queued prompts"),
	),
```

Add to the keymap (near `"s": KeyQuickSend`, ~line 148):

```go
	"Q":          KeyQueue,
```

- [ ] **Step 4: Add the state, fields, and render branch in `app/app.go`**

Add the const to the `state` iota block (after `stateRename`, ~line 135):

```go
	// stateQueue is the state when the pending-prompt management overlay is up.
	stateQueue
```

Add the fields to the `home` struct (near `textInputOverlay`/`stashedDraft`, ~line 303):

```go
	// queueOverlay manages a session's pending prompt queue (list / cancel).
	queueOverlay *overlay.QueueOverlay
	// queueTarget is the instance the queue overlay was opened for; a cancel acts
	// on it even if the selection moves (mirrors renameTarget).
	queueTarget *session.Instance
```

Add the render branch in `View()`, right after the `stateRename` branch (~line 486):

```go
	} else if m.state == stateQueue {
		if m.queueOverlay == nil {
			log.ErrorLog.Printf("queue overlay is nil")
		}
		return overlay.PlaceOverlay(0, 0, m.queueOverlay.Render(), mainView, true)
```

(`session` and `overlay` are already imported by `app/app.go`.)

- [ ] **Step 5: Hide the hint bar and size the overlay in `app/app_layout.go`**

In `menuVisible`, add `stateQueue` to the modal-overlay case (~line 102):

```go
	case statePrompt, stateRename, stateQueue, stateConfirm, stateHelp, stateInfo, stateSettings, stateWelcome, stateAccounts:
		return false
```

In `updateHandleWindowSizeEvent`, add a sizing block after the `welcomeOverlay` block (~line 80):

```go
	if m.queueOverlay != nil {
		w := int(float32(msg.Width) * 0.6)
		if w > 80 {
			w = 80
		}
		m.queueOverlay.SetWidth(w)
	}
```

- [ ] **Step 6: Dispatch the key and state in `app/app_update.go`**

Add the action case right after `case keys.KeyQuickSend: return m.openQuickSend()` (~line 730):

```go
	case keys.KeyQueue:
		return m.openQueue()
```

Add the state dispatch in `handleKeyPress`, right after the `stateRename` block (~line 622):

```go
	if m.state == stateQueue {
		return m.handleQueueState(msg)
	}
```

- [ ] **Step 7: Implement `openQueue`, `handleQueueState`, `dismissQueueOverlay` in `app/app_keys.go`**

Add near `openRenameSelected` / `handleRenameState`. `handleQueueState` here handles only navigation + close; Task 4 adds the cancel branch:

```go
// openQueue opens the pending-prompt management overlay for the selected session,
// listing its queued prompts so the user can cancel one before delivery. Unlike
// openQuickSend it needs no live pane (management is a pure in-memory read +
// cancel + persist), so paused and loading sessions are fair game; only an empty
// queue is a dead end worth refusing. The overlay acts on this instance even if
// the selection later moves (queueTarget), mirroring the rename flow.
func (m *home) openQueue() (tea.Model, tea.Cmd) {
	selected := m.list.GetSelectedInstance()
	if selected == nil {
		return m, nil
	}
	if !selected.HasQueuedPrompt() {
		return m, m.handleInfoNotice(fmt.Sprintf("nothing queued for %q", selected.DisplayName()))
	}
	m.queueTarget = selected
	m.queueOverlay = overlay.NewQueueOverlay(selected.DisplayName())
	texts, headInFlight := selected.QueueView()
	m.queueOverlay.SetQueue(texts, headInFlight)
	m.state = stateQueue
	// tea.WindowSize re-runs layout so the overlay gets its responsive width.
	return m, tea.WindowSize()
}

// handleQueueState routes a key to the queue overlay: cursor moves and esc are
// handled inside the overlay. (Task 4 adds the cancel branch.)
func (m *home) handleQueueState(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.queueOverlay.HandleKeyPress(msg) {
		m.dismissQueueOverlay()
		return m, m.instanceChanged()
	}
	return m, nil
}

// dismissQueueOverlay tears down the queue overlay and returns to the list.
func (m *home) dismissQueueOverlay() {
	m.queueOverlay = nil
	m.queueTarget = nil
	m.state = stateDefault
	m.menu.SetState(ui.StateDefault)
}
```

(`fmt`, `ui`, `overlay`, `tea` are already imported by `app/app_keys.go`.)

- [ ] **Step 8: Add the help row in `app/help.go`**

In `helpTypeGeneral.toContent()`, in the **Handoff** section, right after the `s` row (~line 84):

```go
			helpRow("Q", "manage queued prompts (list / cancel)"),
```

- [ ] **Step 9: Run build + tests to verify they pass**

Run: `just build && just test 2>&1 | tail -20`
Expected: build OK; PASS (the four `app/queue_test.go` tests included).

- [ ] **Step 10: Commit**

```bash
git add keys/keys.go app/app.go app/app_layout.go app/app_update.go app/app_keys.go app/help.go app/queue_test.go
git commit -m "feat(app): open a queue-management overlay with Q (#286)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Task 4: Cancel flow — remove, persist, refresh, close-when-empty, refusal

Deliverable: `d`/`x` cancels the selected prompt on the target instance, persists, refreshes the overlay; cancelling the last entry closes with a notice; an in-flight head is refused with an in-overlay message.

**Files:**
- Modify: `app/app_keys.go` (replace `handleQueueState` from Task 3)
- Test: `app/queue_test.go` (append)

**Interfaces:**
- Consumes: Task 1 `CancelQueuedPrompt`, `QueueView`; Task 2 `RemoveRequested`, `SelectedIndex`, `SelectedText`, `SetQueue`, `SetMessage`; existing `m.persistInstances`, `m.handleInfoNotice`, `m.instanceChanged`, `m.dismissQueueOverlay` (Task 3), `log.ErrorLog`.
- Produces: the full `handleQueueState` behavior.

- [ ] **Step 1: Write the failing tests (append to `app/queue_test.go`)**

```go
func TestQueueCancel_RemovesEntryAndPersists(t *testing.T) {
	h := newCreateFormHome(t)
	h.storage = mustStorage(t)
	inst := queueInstance(t, "q")
	inst.QueueFollowupPrompt("a")
	inst.QueueFollowupPrompt("b")
	h.list.AddInstance(inst)
	h.list.SelectInstance(inst)
	_, _ = h.openQueue() // cursor on head "a"

	_, _ = h.handleKeyPress(runeKeyApp("d"))

	require.Equal(t, 1, inst.QueueLen(), "the head was cancelled")
	require.Equal(t, "b", inst.Prompt())
	require.Equal(t, stateQueue, h.state, "still open with one entry left")
}

func TestQueueCancel_LastEntryClosesOverlay(t *testing.T) {
	h := newCreateFormHome(t)
	h.storage = mustStorage(t)
	inst := queueInstance(t, "q")
	inst.QueueFollowupPrompt("only")
	h.list.AddInstance(inst)
	h.list.SelectInstance(inst)
	_, _ = h.openQueue()

	_, _ = h.handleKeyPress(runeKeyApp("d"))

	require.Equal(t, 0, inst.QueueLen())
	require.Equal(t, stateDefault, h.state)
	require.Nil(t, h.queueOverlay)
	require.True(t, h.menu.HasNotice())
}

func TestQueueCancel_InFlightHeadRefused(t *testing.T) {
	h := newCreateFormHome(t)
	h.storage = mustStorage(t)
	inst := queueInstance(t, "q")
	inst.QueueFollowupPrompt("boot")
	_, ok := inst.ClaimPrompt() // raises the in-flight guard on the head
	require.True(t, ok)
	h.list.AddInstance(inst)
	h.list.SelectInstance(inst)
	_, _ = h.openQueue()

	_, _ = h.handleKeyPress(runeKeyApp("d"))

	require.Equal(t, 1, inst.QueueLen(), "the in-flight head is not cancelled")
	require.Equal(t, stateQueue, h.state, "the overlay stays open")
	require.Contains(t, h.queueOverlay.Render(), "being delivered", "the refusal is explained in-overlay")
}

func TestQueueCancel_TargetsOpenedInstanceNotSelection(t *testing.T) {
	h := newCreateFormHome(t)
	h.storage = mustStorage(t)
	a := queueInstance(t, "a")
	a.QueueFollowupPrompt("xa")
	b := queueInstance(t, "b")
	b.QueueFollowupPrompt("xb")
	h.list.AddInstance(a)
	h.list.AddInstance(b)
	h.list.SelectInstance(a)
	_, _ = h.openQueue() // target = a

	h.list.SelectInstance(b) // selection moves away while the overlay is open
	_, _ = h.handleKeyPress(runeKeyApp("d"))

	require.Equal(t, 0, a.QueueLen(), "the opened instance's queue shrank")
	require.Equal(t, 1, b.QueueLen(), "the newly-selected instance is untouched")
}
```

Add these two helpers to `app/queue_test.go` (below `queueInstance`), and the imports they need (`config`, `session`):

```go
func runeKeyApp(s string) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)} }

func mustStorage(t *testing.T) *session.Storage {
	t.Helper()
	st, err := session.NewStorage(config.DefaultState())
	require.NoError(t, err)
	return st
}
```

Add `"github.com/ZviBaratz/atrium/config"` to the test file's imports.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `just test 2>&1 | grep -A3 QueueCancel`
Expected: FAIL — the head-cancel test finds the queue unchanged (Task 3's `handleQueueState` ignores `d`); the last-entry test stays in `stateQueue`.

- [ ] **Step 3: Replace `handleQueueState` in `app/app_keys.go`**

Replace the Task 3 stub with the full version:

```go
// handleQueueState routes a key to the queue overlay: cursor moves and esc are
// handled inside the overlay; a cancel (d/x) is performed here against the target
// instance the overlay was opened for (queueTarget), not the live selection —
// which can move while the overlay is open. A successful cancel persists and
// refreshes the list; cancelling the last entry closes the overlay and flashes on
// the now-visible hint bar; a refusal (the in-flight head, or a queue that shifted
// under the snapshot) explains itself in-overlay, since the hint bar is hidden
// behind the box.
func (m *home) handleQueueState(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	shouldClose := m.queueOverlay.HandleKeyPress(msg)

	if m.queueOverlay.RemoveRequested() && m.queueTarget != nil {
		removed := m.queueTarget.CancelQueuedPrompt(m.queueOverlay.SelectedIndex(), m.queueOverlay.SelectedText())
		if removed {
			if err := m.persistInstances(); err != nil {
				log.ErrorLog.Printf("failed to persist after cancelling queued prompt: %v", err)
			}
		}
		texts, headInFlight := m.queueTarget.QueueView()
		if len(texts) == 0 {
			// The queue drained — close and flash on the (now visible) hint bar.
			m.dismissQueueOverlay()
			return m, tea.Sequence(m.handleInfoNotice("queue cleared"), m.instanceChanged())
		}
		m.queueOverlay.SetQueue(texts, headInFlight)
		if !removed {
			m.queueOverlay.SetMessage("can't cancel — prompt is being delivered")
		}
		return m, m.instanceChanged()
	}

	if shouldClose {
		m.dismissQueueOverlay()
		return m, m.instanceChanged()
	}
	return m, nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `just test 2>&1 | tail -20`
Expected: PASS (all `TestQueueCancel_*` included).

- [ ] **Step 5: Commit**

```bash
git add app/app_keys.go app/queue_test.go
git commit -m "feat(app): cancel pending prompts from the queue overlay (#286)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Task 5: Row-glyph pending count

Deliverable: a session with more than one queued prompt shows `↦2` on its row; a single queued prompt keeps the bare `↦`.

**Files:**
- Modify: `ui/list_render.go` (the `HasQueuedPrompt` block, ~lines 184-186)
- Test: `ui/row_test.go` (extend `TestRender_QueuedPromptChip`)

**Interfaces:**
- Consumes: existing `i.HasQueuedPrompt()`, `i.QueueLen()`, `g.Queued`, `th.Palette.Accent`, `p.seg`. `fmt` is already imported by `ui/list_render.go`.

- [ ] **Step 1: Extend the failing test**

In `ui/row_test.go`, replace the body of `TestRender_QueuedPromptChip` with (adds a depth-2 assertion; keeps the depth-1 and additive-gutter checks):

```go
func TestRender_QueuedPromptChip(t *testing.T) {
	t.Cleanup(theme.Set("unicode"))
	th := theme.Current()
	s := spinner.New()
	r := &InstanceRenderer{spinner: &s}
	r.setWidth(80)

	queued := instWithStatus(t, "q", session.NeedsInput)
	queued.QueueFollowupPrompt("ship it")
	bare := instWithStatus(t, "b", session.NeedsInput)

	withGlyph := ansi.Strip(r.Render(queued, 0, false, false))
	require.Contains(t, withGlyph, th.Glyphs.Queued, "a queued prompt must show the pending-prompt glyph")
	require.Contains(t, withGlyph, th.Glyphs.Waiting,
		"the status gutter glyph is additive-only: NeedsInput still shows its waiting glyph")

	require.NotContains(t, ansi.Strip(r.Render(bare, 0, false, false)), th.Glyphs.Queued,
		"a session with no queued prompt shows no pending-prompt glyph")

	// Depth > 1 surfaces the count so the user knows there's a queue worth opening.
	queued.QueueFollowupPrompt("and again")
	deep := ansi.Strip(r.Render(queued, 0, false, false))
	require.Contains(t, deep, th.Glyphs.Queued+"2", "two queued prompts show the count")
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `just test 2>&1 | grep -A3 QueuedPromptChip`
Expected: FAIL — `deep` contains bare `↦`, not `↦2`.

- [ ] **Step 3: Implement the count**

In `ui/list_render.go`, replace the `HasQueuedPrompt` block (~lines 184-186):

```go
	if i.HasQueuedPrompt() {
		label := g.Queued
		if n := i.QueueLen(); n > 1 {
			label = fmt.Sprintf("%s%d", g.Queued, n)
		}
		right1 = append(right1, p.seg(label, th.Palette.Accent))
	}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `just test 2>&1 | tail -20`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add ui/list_render.go ui/row_test.go
git commit -m "feat(ui): show the pending-prompt count on the row glyph (#286)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Final verification

- [ ] **Full build + test:** `just build && just test` — both green.
- [ ] **Manual smoke:** run `./bin/atrium`, create a session, queue 2–3 prompts with `s`, confirm the row shows `↦3`; press `Q`, move with `j/k`, cancel the middle one with `d`, confirm the row drops to `↦2` and the overlay reflects the removal; cancel the rest and confirm the overlay closes with a "queue cleared" notice; quit and reopen to confirm `state.json`'s `prompt_queue` matches (no cancelled prompts resurrected).
- [ ] **Optional:** `just fmt-check` (formatting) before opening the PR.
