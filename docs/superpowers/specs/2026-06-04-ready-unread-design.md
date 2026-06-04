# Read/unread state for Ready sessions

## Problem

With many parallel agent sessions, "Ready + green" carries no signal after the
first glance: the user cannot distinguish "finished, needs my triage" from
"finished, I already looked at it, waiting on me". Attention routing is the core
job of an orchestrator list, and the Ready status currently has no memory of
whether the user has seen the result.

## Behavior

A session that reaches `Ready` renders **bright green filled `●`** ("unread")
until visited, then **dim green hollow `○`** ("seen") until the agent does
another turn of work and finishes again.

- **Unread trigger** — any status transition *into* `Ready`, edge-detected
  inside `Instance.SetStatus()`. No content hashing or timestamps: the state
  machine transition "agent worked, then went idle" is the crisp event.
- **Visit triggers** (clear unread):
  - **Attach** — entering the session's tmux pane.
  - **Selection dwell** — the row stays selected ≥ 1.5 s in the default UI
    state (the preview pane shows live content, so a dwelled selection *is* a
    read). Cursor travel through intermediate rows does not mark them.
- **Visual** — unread keeps today's Ready look (`Palette.Success`, `●`); seen
  uses a new `Palette.SuccessDim` token and a new `Glyphs.ReadySeen` hollow
  `○`. Color *and* shape change, so the signal survives colorblindness and
  low-color terminals. Grey remains exclusively Paused's color; the session
  name stays at full `Fg` either way — only the status word and glyph dim.
- **Collapsed repo groups** — the header gains a bright `●N` unread-count
  badge next to the existing `◆N` needs-input badge, so unread sessions are
  not invisible while their group is folded.
- **Persistence** — the unread bit round-trips through `state.json`
  (`InstanceData.Unread`), with the same best-effort semantics as `Status`
  itself: saved on quit and instance operations, not on every poll tick.
- **Scope limits (deliberate)** — Ready only (NeedsInput stays
  attention-colored regardless; Running/Loading are inherently live); no
  unread-first sorting; no anti-AFK input gating (dwell only ever fires for
  the *selected* row, whose preview shows the content on screen, so a
  mark-seen while away is harmless — the content greets the user on return).

## The synthetic-transition problem

Four code paths write `SetStatus(Running)` for lifecycle reasons, not because
the agent is working. The poll that follows settles to `Ready`, and that
synthetic `Running → Ready` edge must NOT flag unread:

1. **Restore-reattach** — `FromInstanceData` forces `Running` after a
   successful tmux reattach; without suppression every TUI restart would
   re-flag all idle sessions, defeating persistence.
2. **recoverInPlace** — reboot recovery restarts the agent; its post-boot idle
   is not new output. Without suppression every reboot is a wall of green.
3. **Resume** — a resumed paused session boots back into its old
   conversation; nothing new happened.
4. **Post-detach fresh poll** — the agent finished *while the user was
   attached watching it*; the detach-triggered poll observes the leftover
   stale `Running` settle to `Ready`. Includes the sibling-cycle
   (Ctrl+PgUp/PgDn) detach branch.

**Mechanism** — a one-shot `suppressNextUnread` flag on `Instance`
(mutex-guarded, in-memory only). The next into-Ready transition consumes it
without flagging; any non-Ready `SetStatus` clears it, so an *observed*
working phase re-arms normal flagging and a genuine completion still flags.
Arming sites must arm *after* their own `SetStatus(Running)` write, or the
write would clear the flag they just set.

Cases that genuinely flag (correct by design): a persisted-`Running` session
whose agent finished while the TUI was closed; a brand-new session reaching
its first Ready (it is unvisited); quick-send replies; sessions the autoyes
daemon advanced while the TUI was closed.

## Components

| Piece | Where | Change |
|---|---|---|
| Domain state | `session/instance.go` | `unread`, `suppressNextUnread`, `unreadAt` fields; edge detection in `SetStatus`; `Unread()/UnreadAt()/MarkSeen()/ArmReadySuppression()`; suppression arming in `FromInstanceData` (iff persisted Status==Ready), `recoverInPlace`, `Resume` |
| Serialization | `session/storage.go` | `Unread bool` on `InstanceData` (omitempty; old files default to seen) |
| Theme | `ui/theme/theme.go`, `registry.go` | `Palette.SuccessDim` (tokyo-night `#6a8a4a`, catppuccin-mocha `#6c9168`; unicode inherits tokyo-night's palette), `Glyphs.ReadySeen` (`○`) |
| Rendering | `ui/list.go` | `stateParts` branches on `Unread()` for Ready; `groupUnreadCount` + dual badge in `renderRepoHeader` |
| Triggers | `app/app.go` | `MarkSeen` in `attachExec`; `ArmReadySuppression` on both detach branches; `selectedSince` + `markSeenAfterDwell` (1.5 s dwell on both selection age and unread age, gated on `stateDefault`) driven by the existing 100 ms preview tick |

The `unreadAt` floor guarantees every unread state stays visibly bright for at
least the dwell duration even when its row is already selected (quick-send
replies, finish-while-watching), so the signal is never consumed before it
could be perceived.

## Error handling

No new failure modes: the feature is a pure presentation/state bit. A hard
crash (no `handleQuit`) loses unread recency the same way it loses status
recency — accepted. The autoyes daemon never calls `SetStatus`, so it neither
flags nor clears unread; its exit save round-trips the loaded bit.

## Testing

- `session/instance_test.go` — edge detection, idempotence (`Ready→Ready`),
  re-flagging after `MarkSeen`, suppression consume/clear/ordering, restore
  semantics (persisted Ready vs Running), Resume suppression.
- `session/storage_test.go` — `Unread` serialization round-trip.
- `app/app_test.go` — `markSeenAfterDwell`: dual dwell floors, `stateDefault`
  gate, nil selection.
- `ui/theme` glyph-width test covers `ReadySeen` (width 1).
- Manual scenarios: cursor-travel immunity, quick-send brightness floor,
  watch-then-detach suppression, sibling-cycle, overlay gating, collapsed
  badge, pause/resume, restart persistence, reboot recovery.
