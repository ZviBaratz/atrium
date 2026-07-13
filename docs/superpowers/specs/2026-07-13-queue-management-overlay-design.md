# Queue-management overlay (list / cancel pending prompts)

Resolves #286.

## Problem

#269 (merged in #284) replaced the single queued-prompt slot with a persisted
FIFO queue: quick-sends (`s`) now append and deliver in order, and a `↦` row
glyph shows while a queue is non-empty. That fixed the data-loss, but the queue
is **write-only from the user's side** — you can append, but you cannot see or
manage what is pending. The only surfacing is a single, count-less `↦` glyph.

The domain model is already complete and race-safe: a `promptMu`-guarded FIFO
`promptQueue []queuedPrompt{text, queuedAt}` with a `promptInFlight` in-flight
guard, exposed via `Prompt()` / `QueueLen()` / `HasQueuedPrompt()` /
`PromptSending()` and mutated via `QueueFollowupPrompt` (append) and
`ClearPrompt(deliveredText)` (matched-dequeue of the head). What is missing is a
method to remove a **non-head** entry, and any UI to drive it.

## Goal

From a selected session, open a lightweight overlay that:

- **lists** the pending prompts (head first), marking the one in flight;
- lets the user **cancel** any entry that is not the actively-delivering head
  (including an idle head), removing it before delivery;
- persists the change immediately, so a cancel survives restart.

Plus a small discoverability win: show the pending **count** on the row glyph
(`↦2`) when depth > 1.

## Non-goals

- **Reorder.** Explicitly deferred. Queues are depth 1–2 in practice and the
  issue itself calls reorder "optional" / "polish, not a core need." Recorded as
  a documented stretch (see Tradeoffs) so the door stays open, but no move-up /
  move-down, no swap-under-lock method, in v1.
- **Per-tick live refresh.** The overlay refreshes its snapshot on open and after
  each user action, not on every poll tick. A background delivery that pops the
  head while the overlay sits idle leaves it momentarily stale; the next action
  is a safe no-op (see the matched-removal guard) that triggers a refresh. This
  keeps the poll loop untouched. Documented as an optional future enhancement.
- **Cancel confirmation.** Cancelling a queued prompt is trivially reversible
  (re-type and press `s`) and nothing has been delivered, so `d` cancels
  directly — unlike kill/merge, which confirm because they destroy worktrees.
- **Extending the quick-send overlay.** The already multi-role `TextInputOverlay`
  (its own doc comment flags it as stretched across four roles) is left untouched;
  this is a separate, single-purpose overlay modelled on `RenameOverlay`.
- **A status hint-bar entry.** The `Q` binding lives in the `?` help screen and is
  advertised by the row-glyph count; it is not added to the space-constrained
  per-status hint bar (`ui/menu.go`).

## Behavior

### Opening (`Q` from the list)

`Q` (mnemonic for **Q**ueue; `q` is quit) on the default list opens the overlay
for the selected session. Guards, mirroring `openQuickSend`:

- No selection → no-op.
- Paused session → info notice (`"session is paused …"`), don't open. (A paused
  session's queue is not actionable and reload rebuilds it.)
- **Empty queue → info notice** (`nothing queued for "<name>"`), don't open. The
  row glyph already tells the user when there is something to manage, so opening
  onto an empty box would be a dead end.
- Otherwise: record the target instance, snapshot its queue, build the overlay,
  enter `stateQueue`.

### The overlay

A bordered box titled `Queue for "<display name>"` containing a numbered,
head-first list:

```
┌─ Queue for "auth-refactor" ──────────┐
│ ▸ 1  fix the login redirect      ⟳   │
│   2  then update the tests           │
│   3  and bump the changelog          │
│                                      │
│ j/k move · d cancel · esc close      │
└──────────────────────────────────────┘
```

- `▸` marks the cursor row; `⟳` marks the head **only while it is in flight**
  (`headInFlight`).
- Each row shows a 1-based index and the prompt text collapsed to a single
  truncated line (multi-line prompts show their first line with a `…` tail;
  reuse `truncate.StringWithTail`, as `textInput_render.go` does).
- The list is windowed around the cursor (same approach as
  `renderPickerRows`) so a long queue on a short terminal stays bounded — a
  defensive measure; at depth 1–2 it never triggers.
- Empty-state (only reachable if the queue drains while open): a dim
  `no pending prompts` line.

Keys:

- `↑`/`k`, `↓`/`j` — move the cursor (clamped to the list).
- `d` or `x` — request cancellation of the selected row. The overlay only *arms*
  the request (`RemoveRequested`) and stays open; the app performs the removal
  and decides whether to refresh or close.
- `esc` / `ctrl+c` — close.

### Cancelling

On an armed remove, the app calls `CancelQueuedPrompt(idx, selectedText)` on the
**target** instance — the one the overlay was opened for (`m.queueTarget`), not
the currently-selected row. Selection can move while the overlay is open (a
background poll can reorder rows, auto-naming can shift the cursor), and
`handleRenameState` guards against exactly this by acting on its stored target;
the queue overlay follows that precedent.

- **Success** → persist (`persistInstances()`), re-push the fresh snapshot into
  the overlay, clamp the cursor. If the queue is now empty, close with an info
  notice (`queue empty`). Otherwise flash `cancelled` and stay open.
- **Refused** (the head is in flight, or the queue shifted so `idx`/text no longer
  match) → flash `can't cancel — prompt is being delivered`, re-push the fresh
  snapshot (which self-corrects any staleness), stay open.

Because `CancelQueuedPrompt` re-validates index **and** text under `promptMu`, a
snapshot that went stale between render and keypress can never cancel the wrong
prompt — the mismatch makes it a no-op and the subsequent re-push shows the user
the corrected list.

### Row glyph count

`ui/list_render.go`: when `HasQueuedPrompt()`, render `g.Queued`; when
`QueueLen() > 1`, append the decimal count (`↦2`, `↦3`). Depth 1 is unchanged
(`↦`).

## Implementation

### `session/instance.go`

Add, next to `ClearPrompt`, documented as part of the `promptMu` state machine:

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

`slices` is already importable (Go 1.25). No struct/persistence change: the
existing `promptQueueSnapshot()` → `PromptQueue` serialization covers a shortened
queue for free.

### `ui/overlay/queueOverlay.go` (new)

A pure view over primitives, modelled on `renameOverlay.go`. No `session`
import — the app pushes the snapshot in.

```go
type QueueOverlay struct {
	title        string
	items        []string
	cursor       int
	headInFlight bool
	width        int
	removeReq    bool
	canceled     bool
}

func NewQueueOverlay(title string) *QueueOverlay
func (q *QueueOverlay) SetQueue(texts []string, headInFlight bool) // replaces items, clamps cursor, clears removeReq
func (q *QueueOverlay) HandleKeyPress(msg tea.KeyMsg) (shouldClose bool)
func (q *QueueOverlay) Render() string
func (q *QueueOverlay) SelectedIndex() int
func (q *QueueOverlay) SelectedText() string   // "" when empty
func (q *QueueOverlay) RemoveRequested() bool  // reads and clears the flag
func (q *QueueOverlay) IsCanceled() bool
func (q *QueueOverlay) SetWidth(width int)
```

`HandleKeyPress`: `up`/`k` and `down`/`j` move (clamped); `d`/`x` set `removeReq`
and return `false`; `esc`/`ctrl+c` set `canceled` and return `true`; everything
else is ignored. `Render` uses the theme styles already used by `renameOverlay`
(`theme.Current()` border/accent, `OverlayTitleStyle`, `OverlayHintStyle`,
`DimStyle`) and the `g.Queued` (`↦`) / a `⟳` in-flight glyph.

### `keys/keys.go`

- Add `KeyQueue` to the `KeyName` enum.
- Add the binding:
  ```go
  KeyQueue: key.NewBinding(
      key.WithKeys("Q"),
      key.WithHelp("Q", "manage queued prompts"),
  ),
  ```
- Add `"Q": KeyQueue` to the keymap.

### `app/app.go`

- Add the state const near `stateRename`:
  ```go
  // stateQueue is the state when the pending-prompt management overlay is up.
  stateQueue
  ```
- Add the fields: `queueOverlay *overlay.QueueOverlay` and
  `queueTarget *session.Instance` (the instance the overlay was opened for, so a
  cancel targets it even if the selection moves — mirrors `renameTarget`).
- Add a render branch in `View()` (mirroring the `stateRename` block, ~line 482):
  ```go
  } else if m.state == stateQueue {
      if m.queueOverlay == nil {
          return mainView
      }
      return overlay.PlaceOverlay(0, 0, m.queueOverlay.Render(), mainView, true)
  }
  ```

### `app/app_layout.go`

Add `stateQueue` to the overlay-state list at line 102 (so layout sizing / the
overlay-open predicate treat it like the other modal overlays).

### `app/app_update.go`

- Action dispatch, after the `KeyQuickSend` case (~line 730):
  ```go
  case keys.KeyQueue:
      return m.openQueue()
  ```
- State dispatch, after the `stateRename` block (~line 621):
  ```go
  if m.state == stateQueue {
      return m.handleQueueState(msg)
  }
  ```

### `app/app_keys.go`

- `openQueue()` near `openQuickSend` (~line 372): the guards described in
  **Behavior › Opening**; on success set `m.queueTarget = selected`,
  `m.state = stateQueue`, build the overlay, push the snapshot, set width,
  `return m, tea.WindowSize()`.
- `handleQueueState(msg)` near `handleRenameState` (~line 197): delegate to
  `m.queueOverlay.HandleKeyPress`; on close/cancel clear `queueOverlay` +
  `queueTarget` + state (mirror the rename close path, including
  `m.menu.SetState(ui.StateDefault)`); on `RemoveRequested()` run the cancel flow
  from **Behavior › Cancelling** against `m.queueTarget`, re-pushing
  `m.queueTarget.QueueView()` after the attempt.

### `app/help.go`

Add to the **Handoff** section of `helpTypeGeneral.toContent()`, next to the `s`
row:
```go
helpRow("Q", "manage queued prompts (list / cancel)"),
```

### `ui/list_render.go`

At the pending-prompt marker (~line 184):
```go
if i.HasQueuedPrompt() {
	label := g.Queued
	if n := i.QueueLen(); n > 1 {
		label += strconv.Itoa(n)
	}
	right1 = append(right1, p.seg(label, th.Palette.Accent))
}
```

## Tradeoffs

**Dumb view + snapshot vs. overlay-holds-Instance.** The overlay could hold the
live `*session.Instance` and read the queue in `Render` for automatic liveness
(`ui/overlay` already imports `session`). We instead keep it a pure view and have
the app push snapshots. This matches the codebase's stated convention ("the
overlay is a dumb view: the app layer computes … and pushes it in"), makes the
overlay trivially unit-testable without constructing an `Instance`, and — paired
with the matched-removal guard — costs only momentary staleness that the next
keypress self-corrects. Accepted deliberately (this is the deferred per-tick
live-refresh; see Non-goals).

**Deferring reorder.** The issue lists reorder as optional. At depth 1–2 the
payoff is marginal and it would add a swap-under-lock method plus index-shift edge
cases. Recorded here as the natural follow-up if real queues ever get deep; the
`CancelQueuedPrompt(idx, expectedText)` matched-mutation pattern generalises to a
`MoveQueuedPrompt` later.

**`Q` as the binding.** `Q` is free and mnemonic. The alternative — reaching the
queue from inside the quick-send overlay — was rejected to avoid re-coupling the
two surfaces the placement decision separated.

## Testing

All `app`-level tests stay hermetic (temp `HOME` via the existing `TestMain` in
`app/app_test.go`).

### `session` unit tests (`session/instance_prompt_test.go` or a new file)
`TestCancelQueuedPrompt`:
- removes a tail entry, preserving order of the rest;
- removes the head when **not** in flight;
- **refuses** the head while `promptInFlight` is set (queue unchanged, returns
  false);
- text mismatch at `idx` → no-op, returns false;
- out-of-range `idx` (negative and ≥ len) → no-op, returns false;
- a middle-entry removal keeps head and tail intact;
- after a cancel, `promptQueueSnapshot()` / `PromptQueue` reflects the shortened
  queue (persistence round-trips).

### `ui/overlay` unit tests (`ui/overlay/queueOverlay_test.go`)
- `Render` lists items head-first with 1-based indices; the head shows `⟳` only
  when `headInFlight`; the cursor row shows `▸`.
- `SetQueue` clamps an out-of-range cursor and clears a stale `removeReq`.
- `j`/`k` (and arrows) move and clamp the cursor.
- `d` arms `RemoveRequested()` with the correct `SelectedIndex` / `SelectedText`
  and does **not** close; a second read of `RemoveRequested()` returns false.
- `esc` sets `IsCanceled()` and closes.
- empty `items` renders the empty-state and `SelectedText()` is `""`.

### `app` integration tests (`app/queue_test.go`, mirroring `quicksend_test.go`)
- `openQueue` with no selection / paused / empty queue → info notice, `state`
  stays `stateDefault`, no overlay built.
- `openQueue` on a session with a queue → `stateQueue`, overlay populated from
  `QueueView`.
- `handleQueueState` `d` on a tail entry → `CancelQueuedPrompt` called, state
  persisted, overlay refreshed, stays open.
- cancelling the last entry → overlay closes with the `queue empty` notice.
- `d` on an in-flight head → refusal notice, queue unchanged, overlay stays open.
- a cancel targets the instance the overlay was opened for even after the
  selection moves to a different row (mutate `m.list` selection between open and
  `d`, assert the *target's* queue shrank, not the newly-selected one's).

### `ui/row_test.go`
Extend `TestRender_QueuedPromptChip` with a depth-2 queue asserting the row
contains `↦2`, and keep the depth-1 assertion for the bare `↦`.

## Verification

`just build` **and** `just test` must pass before the work is considered
complete (per `CLAUDE.md`; `go`/`just` are mise-managed — use absolute paths if
the shims aren't on `PATH`). Manual smoke: create a session, queue 2–3 prompts
with `s`, press `Q`, cancel the middle one, and confirm the row glyph count drops
(`↦3` → `↦2`) and `state.json`'s `prompt_queue` reflects the removal.
