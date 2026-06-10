# Hint-based copy/open ("fingers mode")

**Date:** 2026-06-10
**Status:** Approved

## Motivation

Agent sessions constantly print things the user wants to act on — PR URLs,
file paths with line numbers, commit SHAs, branch names. Today grabbing one
means attaching to the session and using tmux copy-mode, or mousing over the
preview. tmux-fingers/tmux-thumbs solved this for raw tmux: overlay short hint
labels on pattern matches and let one keystroke copy the match.

Atrium can do this strictly better than those plugins. They must `swap-pane`
with a fake renderer process because they live *outside* the content; Atrium
**is** the renderer — `PreviewPane.previewState.text` already holds the exact
captured string being displayed. Hint mode is a pure function from that string
to a decorated string plus a key dispatch. No pane gymnastics, no subprocess.

## Scope

**In scope (v1):**
- Hint mode over the **preview tab's live view** of the selected session.
- Actions: **copy** (lowercase hint) and **copy + open** (uppercase hint;
  opens URLs in the browser, degrades to plain copy for non-URL kinds).
- Curated built-in pattern set (below). No configuration surface.

**Out of scope (later increments, designed for but not built):**
- "Send to session" action — paste the match into the selected agent's prompt
  via the action layer (`ActionKind` is extensible).
- Hint mode inside scroll-mode snapshots and the diff tab.
- Hint mode inside **attached** sessions (see "Attached sessions" below).
- User-defined patterns in `config.json`; alphabet/layout options.
- Clickable hints via bubblezone; OSC52/tmux-buffer layered clipboard.

## UX flow

1. Default state, preview tab active, selected session live (not paused) with
   non-empty capture: press **`f`**. The pane content freezes, matches are
   scanned, hint mode renders. No matches → error-box notice ("no matches on
   screen"), state unchanged.
2. Hint-mode render: backdrop dimmed (uniform dim replaces original ANSI
   colors — a deliberate contrast effect, same trade-off tmux-fingers makes),
   matches highlighted, each match prefixed with a 1–2 char bold accent hint
   label drawn over its first cells. The menu line swaps to:
   `type hint: copy · UPPERCASE: copy+open · esc: cancel`.
3. Typing: single-char hint acts immediately. With 2-char hints, the first
   char filters (non-matching hints disappear; consumed prefix shown), the
   second acts. Any uppercase char in the sequence selects copy+open.
4. `esc` or any non-hint key exits to the live view. Selection change, pause,
   resize, or instance death force-exit (scroll-mode discipline).
5. Hints are assigned **bottom-up** — matches nearest the prompt get the
   single-char hints. Identical match text shares one hint (fingers' dedup).

## Architecture

```
keys/keys.go         + KeyHints ("f") in KeyName enum, GlobalKeyStringsMap,
                       GlobalkeyBindings
app/app.go           + stateHints in the state enum. View() unchanged — no
                       overlay; the preview pane renders hint mode itself.
app/app_update.go    + stateHints guard in handleKeyPress → handleHintsState(msg)
                     + KeyHints case in default state: validate, scan, enter mode
app/help.go          + help row for f
ui/preview.go        + hint mode on PreviewPane: enter/exit + render path,
                       sibling of scroll mode with the same identity rules
hints/               NEW package — pure logic, no UI/tmux deps:
    patterns.go        pattern table: name, compiled regex, Kind (url/path/text)
    scan.go            Scan(text string) []Match — per-line, earliest-match-wins
    assign.go          Hints(n int) []string — thumbs greedy expansion
    render.go          Render(lines, matches, hints, typedPrefix, w, h) string
app/open.go          openTarget(kind, text) — detached browser open; package
                       var (like copyToClipboard) for test substitution
```

### Why in-place decoration, not an overlay

Alternatives considered:
- **`PlaceOverlay` floating layer** over the preview region: requires computing
  the preview's screen x/y and replicating its line-slicing/truncation exactly;
  any drift misplaces labels. More moving parts, zero UX gain.
- **Centered modal listing matches** (urlview-style): loses spatial context,
  which is the point of the fingers UX.

In-place wins: the hint renderer *is* the content renderer, so positions are
correct by construction. `PreviewPane` already has a precedent second mode
(scroll) that freezes content and swaps the render path; hint mode is a third
sibling mode.

### Why match on stripped text and dim the backdrop

Matching runs on ANSI-stripped text, and hint mode renders that stripped text
dimmed rather than splicing labels into ANSI-styled lines. This sidesteps the
hardest bug class in the feature — byte-offset vs. cell-offset drift inside
escape sequences — and doubles as the backdrop-contrast effect fingers applies
deliberately. Original colors return the instant hint mode exits.

## Matching: curated pattern set

Priority-ordered. Per line, all patterns run; the earliest match wins (ties →
higher-priority pattern), the scanner advances past it, repeat. A named
`match` group, where present, selects the copyable substring. All RE2-safe.

| # | pattern | regex (essence) | kind |
|---|---------|-----------------|------|
| 1 | markdown link | `\[[^]]*\]\((?P<match>[^)]+)\)` | url |
| 2 | url | `(?P<match>(https?\|git\|ssh\|ftp\|file)://[^\s()"']+\|git@[^\s()"']+)` | url |
| 3 | diff path | `(---\|\+\+\+) [ab]/(?P<match>.+)` | path |
| 4 | git status | `(modified\|deleted\|new file): +(?P<match>.+)` | path |
| 5 | path | `(?P<match>([.\w\-@~]+)?(/[.\w\-@]+)+(?::\d+(?::\d+)?)?)` | path |
| 6 | uuid | `[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}` | text |
| 7 | sha | `[0-9a-f]{7,64}` | text |
| 8 | ipv4 | `\d{1,3}(\.\d{1,3}){3}` | text |
| 9 | hex / color | `0x[0-9a-fA-F]+` and `#[0-9a-fA-F]{6}` | text |

Post-processing:
- Trailing `.,;:` trimmed from URL matches (sentence-final URLs in logs).
- The path pattern captures an optional `:line[:col]` suffix; it is part of
  the copied text (useful to paste into editors/agents).
- Matches starting beyond the visible pane width are skipped.
- Matches shorter than their assigned hint label are skipped (fingers' guard).

## Hint assignment

Thumbs' greedy expansion over the ergonomic alphabet
`asdfqwerzxcvjklmiuopghtybn`: start with all single chars; while more hints
are needed, pop the **last** single char and expand it to two-char combos
(`prefix+c` for each alphabet char). Prefix-free by construction; ≤26 matches
⇒ all single-key. Assignment order is bottom-of-screen-first; duplicate match
texts share one hint.

## Actions

- **Copy:** existing `copyToClipboard` package var (`atotto/clipboard`), same
  substitution-for-tests pattern as the copy-branch feature.
- **Copy + open:** copy, then for `kind == url` open via detected opener —
  `xdg-open` → `x-www-browser` → `wslview` on Linux, `open` on darwin —
  launched detached (`exec.Command(...).Start()`, stdio discarded). Never
  `tea.Exec`: the browser doesn't need the terminal. Non-URL kinds degrade to
  plain copy in v1.
- Failures surface through the existing `m.handleError` path; success is
  acknowledged with a hint-bar toast (`'…' copied`), following the
  copy-branch precedent — without a toast, success and failure are
  indistinguishable from the keyboard.

## Lifecycle & edge cases

- **Enter** reuses the already-rendered `previewState.text` (no re-capture),
  so hints always match exactly what the user was just looking at.
- **Freeze:** `UpdateContent` early-returns while hint mode is active (scroll
  pattern), so the poll tick cannot repaint over the hints.
- **Force exit** on: selection change to another instance, pause, instance
  death, window resize, tab switch. Same unconditional-exit stance the scroll
  snapshot uses (the stuck-preview lesson: a frozen mode must never be able
  to outlive its trigger conditions).
- **Line geometry:** hint rendering applies the same line-slicing as the live
  preview (`availableHeight` rows, width-clamped), so labels land on the same
  rows/columns the user sees. Capture uses `-J` (joined lines), so a logical
  line maps to one preview row.

## Attached sessions (deferred increment, designed for)

Inside an attached session Atrium's UI is not on screen — tmux owns the
terminal — so the preview render path cannot be reused.

Evaluated and **deferred** (2026-06-10): while attached, terminals make
plain URLs clickable via their own text detection (note tmux strips OSC 8
hyperlinks on passthrough unless tmux ≥ 3.4 *and* the outer terminal's
`hyperlinks` terminal-feature is enabled — not the default for
`xterm-256color`-style TERMs — so links whose display text differs from the
target degrade to the visible text), and detach (`ctrl+q`) → `f` reaches
everything else for two keystrokes — while building this means Atrium's
first second-renderer subprocess, a permanent maintenance surface. Revisit
when the detach→f hop is a felt friction, not before.

The v1 engine boundary held and remains binding: `hints/` stays pure (no
UI/tmux deps); it is the shared 80% of the feature. The prep also landed
independently (#98 scoped pane capture *and* keystrokes to the agent's pane
id; #99 extracted the shared `internal/actions` package), so the increment
is cheap to pick up. If built:

1. A hidden `atrium fingers --target <pane-id>` subcommand: capture the pane
   via the existing tmux wrapper, run the same scan/assign/render, print the
   decorated screen, read the hint from its pty, fire `internal/actions`.
2. Host it in a borderless full-window popup —
   `display-popup -B -x0 -y0 -w100% -h100% -E ...` (tmux ≥ 3.3; version-gate
   or accept a border on 3.2) — **not** the thumbs-style `swap-pane` dance
   this section originally sketched. A popup never mutates the pane tree:
   a crashed fingers process self-cleans (the popup just closes, where
   swap-pane needs crash-safe restore), and anything still addressing the
   session rather than the pane id keeps hitting the agent pane — #98 makes
   that a belt-and-braces defense instead of a correctness dependency. tmux
   routes client input to an open popup, so the subcommand reads the hint
   directly from its own pty — none of the input-socket machinery
   tmux-fingers needs.
3. Trigger from the attach stdin proxy (`classifyAttachInput`, beside Ctrl+Q
   and Ctrl+X, gated off for the Terminal tab the way `allowKill` is) rather
   than a managed-conf key binding: same interception point as the existing
   in-session keys, and hermetically testable.

Known risk to resolve before building: the popup child's parent is the tmux
*server*, whose environment may lack `DISPLAY`/`WAYLAND_DISPLAY`, so
atotto/clipboard can fail where the TUI's copy works. First-line fix: the
trigger runs client-side with the real environment, so forward
`DISPLAY`/`WAYLAND_DISPLAY` (plus `XAUTHORITY`/`XDG_RUNTIME_DIR`) via
`display-popup -e` (tmux ≥ 3.2); keep an OSC 52 or `tmux load-buffer`
fallback only for remote (SSH) attaches, which env forwarding cannot fix.

Until it ships, detach (`ctrl+q`) → `f` remains the two-keystroke workaround.

## Testing

- `hints/` is pure → table-driven unit tests:
  - patterns against realistic agent-output samples (PR URLs in prose,
    `path/file.go:412`, git status blocks, diff headers, SHAs in commit
    lines), including overlap priority (url beats path, uuid beats sha);
  - assignment: prefix-freedom, the 26/27 boundary, dedup, bottom-up order;
  - render: string assertions for label placement, dimming, width/height
    clamping, prefix filtering.
- App level, following `app/copybranch_test.go`: fake clipboard + fake opener
  package vars; drive `handleKeyPress` with `f` then hint chars; assert
  clipboard content, copy+open invokes the opener for URLs only, esc cancels,
  no-matches stays in default state, selection-change force-exits. Hermetic —
  hint mode operates on injected preview text, no tmux required.
