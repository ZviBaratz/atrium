# Drift badge — persistent fallback surface

**Date:** 2026-06-17
**Status:** Approved
**Follows:** [agent-heuristic drift guard](2026-06-17-agent-heuristic-drift-guard-design.md)

## Motivation

The drift guard's startup hint is delivered through `handleInfoNotice`, which
returns nil (drops the toast) when the hint bar is unavailable —
`menuVisible()` is false whenever `hint_bar` is off in config, or a modal owns
the screen. With the ack-only-when-shown fix, a hint-bar-off user is therefore
never told about drift through the TUI at all; their only surface is running
`atrium doctor` by hand — which they would only do if they already suspected a
problem.

The sibling auto-update feature degrades better: beyond its transient toast it
writes a **persistent Sessions-panel border badge** (`updateBadgeText`) that
renders regardless of `hint_bar` and overlays. Drift has no equivalent. This
spec adds one, so the users most likely to miss the toast still get a passive,
non-nagging signal pointing them at `atrium doctor`.

## Behavior

The badge is a **fallback**, shown only when the toast could not be delivered —
never for users who already saw it.

| Hint bar | Outcome |
|----------|---------|
| on (toast delivered) | toast shown, ack recorded, **no badge** |
| off / overlay (toast dropped) | no toast, **`⚠ stale` badge** in panel border, no ack |
| drift resolved (next launch) | no `driftFoundMsg` fires → badge defaults empty → **gone** |

The startup check already emits `driftFoundMsg` only for *unacked* drift. The
badge is set exclusively in the handler's existing `cmd == nil` branch (toast
dropped). Because `List` is constructed fresh each launch, the badge defaults
empty and self-clears the moment drift is resolved or the toast gets through —
no explicit clear path is needed.

## Approach

All four pieces mirror the existing update-badge pattern.

1. **Glyph.** Add `Warn string` to `ui/theme.Glyphs`; set `Warn: "⚠"` in both
   registry glyph blocks (`ui/theme/registry.go`). Add `Warn` to the
   glyph-completeness assertion in `ui/theme/theme_test.go`.

2. **Badge text.** `driftBadgeText()` in `app/app_driftcheck.go` returns
   `theme.Current().Glyphs.Warn + " stale"`. Mirrors `updateBadgeText`; the
   panel degrades it word-by-word when narrow (`⚠ stale` → `⚠`), same as
   `⇡ v0.7.1` → `⇡`.

3. **List badge slot.** The panel border exposes one badge slot
   (`PanelWithBadge(title, badge, …)`), today fed only by `updateBadge`. Add a
   sibling `driftBadge string` field and `SetDriftBadge(text string)` on `*List`.
   At render, combine the two badges space-joined with `updateBadge` first
   (either may be empty), so a rare update+drift co-occurrence shows both rather
   than one clobbering the other.

4. **Wiring.** In `app/app_update.go`'s `driftFoundMsg` handler, the existing
   `cmd == nil` branch (before `return m, nil`) calls
   `m.list.SetDriftBadge(driftBadgeText())`, guarded by `m.list != nil`. The
   "toast shown" branch is unchanged (no badge).

## Testing

- **app** — extend the existing handler tests: a dropped toast
  (`TestDriftFoundMsg_NoAckWhenHintDropped`) also sets the drift badge; a shown
  toast (`TestDriftFoundMsg_AckRecordedWhenHintShown`) leaves it empty.
- **ui** — `SetDriftBadge` text appears in `List.View()`, and update+drift
  badges combine (both visible) in the single slot.
- **theme** — `Warn` covered by the glyph-completeness test.

All hermetic, consistent with the existing `app`/`ui`/`config` test setup.

## Non-goals

- No new config flag.
- No explicit clear path (fresh-per-launch default handles it).
- No change to the toast wording or the `atrium doctor` command.
- The badge never appears for users who saw the toast (it is strictly a
  fallback, not parity-with-update's always-on badge).
