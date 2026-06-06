# Sleek pass #1 — polish iteration design

**Date:** 2026-06-06
**Status:** Approved

## Motivation

Atrium is now a daily-driver IDE. Two deep audits (visual/rendering and
interaction coherence) found no functional gaps but a consistent theme: the app
**tells small lies and shows small seams**. The hint bar advertises actions that
silently no-op, invalid keys give zero feedback, transient errors reflow the
whole layout, overlays each invent their own chrome, and the diff pane wraps
where every other pane truncates.

**Design principle:** affordances track state, and feedback never moves the
layout.

## Decisions

- `q` stays instant quit — sessions survive in tmux, restart is cheap.
- `auto_attach` stays default-on.
- Keybinding remap: `p` = pause (commit + pause, frees worktree; was `c`
  "checkout"), `P` = push (was `p`). `c` is freed. The "checkout" label was
  misleading (it neither checks out a branch nor purely commits).
- Ship-loop / PR-status / notification features deprioritized: the in-session
  agent handles PRs itself, and awareness is not a felt pain.

## Batch 1 — Trust & feedback (`feat/sleek-feedback`)

1. **Status messages live in the hint-bar row.** Transient error/info text
   renders in the always-on hint-bar row, temporarily replacing the hints
   (vim-command-line pattern) and restoring them when the hide timer fires.
   Errors use the danger token; a new neutral info variant covers
   acknowledgments. Multi-line errors keep routing to the info modal. With
   `hint_bar=false` the previous row-claiming behavior remains. This removes
   the one-row layout reflow that error display causes today — a prerequisite
   for adding more feedback, not a follow-up to it.
2. **No-op key feedback.** Every silent state-guard (`s` on paused, `r` on
   running, `↵` on paused, …) flashes a one-line reason. `y` (copy branch)
   acknowledges success and surfaces clipboard failure.
3. **State-aware hint bar.** The hint set becomes a function of the selected
   session's status: paused sessions advertise `r resume`, not `↵ open · s
   send` (both no-ops today). The empty-state hints drop `N` — with zero
   sessions the n/N distinction is noise.
4. **`ctrl+q` becomes a real keybinding** (`KeyAttachToggle`) instead of a
   magic-string special case, restoring keys.go's rebinding contract and
   making it hintable.
5. **Remap `p` = pause, `P` = push; retire `c`.** All "checkout" wording
   becomes pause wording that states what it does. A help-coverage test
   asserts every bound key appears in the help screen, so hand-maintained help
   can't drift after remaps.
6. **`enter` creates from the title field.** The quick path becomes
   `n` → name → `enter`. `tab` from title reaches the prompt for the
   create-with-prompt journey (the prompt field is multiline, so enter stays
   newline there); `⌃S` remains the universal submit. Footer hint states the
   contract.

## Batch 2 — Visual coherence (`feat/sleek-chrome`)

1. **Theme-aware overlay fade.** The modal backdrop's hardcoded ANSI 236/240 +
   `#333333` shadow (the one survivor of the PR #58 token sweep) derives from
   the active theme palette. The escape-rewrite is the riskiest change in the
   pass and gets regression tests across color formats and themes.
2. **Unified overlay chrome.** `OverlayTitleStyle()` / `OverlayHintStyle()`
   theme helpers; all five overlays route their titles and footer hints
   through them (today: three title styles, three footer styles, one raw
   `Faint(true)`).
3. **Confirmation overlay responsive sizing** — currently fixed at width 50
   and excluded from window-resize handling; overflows narrow terminals.
4. **Cursor unification** — `▌` (list filter) vs `█` (pickers) vs bubbles'
   cursor become one treatment.
5. **Seam sweep:** styled diff error text, unified "setting up" copy, hint-bar
   truncation on narrow widths, spinner FPS aligned to the repaint tick,
   preview fallback centered via `lipgloss.Place` instead of magic offsets.

## Batch 3 — Diff & list refinement (`feat/sleek-diff-list`)

1. **Diff line truncation** — colorized lines truncate to pane width
   (`ansi.Truncate`), matching the preview pane's discipline; no more
   soft-wrap row jumping.
2. **Diff file boundaries** — faint rule + bold path per file; tabs expanded
   before width math.
3. **List truncation edge cases** — no dangling branch glyph at narrow
   widths; group spacing normalized at viewport edges.

## Explicitly deferred

Mouse targets inside overlays; scrollbar gutter; welcome-screen hero
treatment; awareness/notification features; full help generation from
bindings (the coverage test captures the drift-prevention value).

## Verification

Each batch: `just build && just test && just lint`, plus a visual smoke in a
throwaway tmux session — pane captures before/after at normal and ~50-column
widths, both themes, exercising the changed journeys (quick create, pause/
resume/push, no-op feedback, toast-without-reflow, every overlay).
