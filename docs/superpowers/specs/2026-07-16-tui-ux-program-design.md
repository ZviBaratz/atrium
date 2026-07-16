# The UX Program — design record (2026-07-16)

**Goal.** Make Atrium's TUI optimal, inviting, and intuitive at an industry level —
for newcomers *and* the daily driver — and push the limits of the terminal with an
honest degradation story. Decisions are encoded as GitHub issues for hand-off; this
document is the durable record of how they were reached.

## Scope decisions (user-confirmed)

- **Audience: layered.** The lazygit/k9s model — self-explaining defaults for
  newcomers, density and speed underneath. Not newcomer-first, not power-only.
- **Ambition: no limits.** Advanced terminal protocols and radical interactions are
  in scope; feasibility is filtered at issue triage, and every capability must
  detect support and degrade gracefully.
- **Deliverable: reviewed portfolio → filed issues.** Full set presented for
  review before filing; issues labeled P1/P2/P3 + area:*, cross-linked to an epic.

## Method

1. **Hermetic live drive** of atrium 0.8.0-116: sandboxed HOME + dedicated
   `TMUX_TMPDIR`, `program=bash` sessions, truecolor captures (tmux `capture-pane -e`
   → PNG) at 140×42 and 80×24, walking first-run → empty state → help → creation →
   list/diff/kill/settings/filter/visual → attach.
2. **Code-surface map**: all 14 `state` values, every overlay, the full keymap and
   its three hand-maintained projections, config's 40 fields, CLI subcommands,
   README gaps — with file:line references (12 verified debt flags).
3. **Four research sweeps** (reports preserved in the session record): TUI
   exemplars (~75 sourced patterns across lazygit, k9s, zellij, helix, yazi, btop,
   gh-dash, crush, posting…); terminal frontier as of July 2026 (Bubble Tea v2 is
   out; OSC 8/9/52/9;4, kitty keyboard/graphics, mode 2026/2031 support matrices);
   the orchestrator landscape (Terragon and Vibe Kanban shut down, Crystal
   deprecated, Anthropic ships parallel-session UX natively — moats are tmux
   composability, keyboard speed, vendor neutrality, status detection); and a
   15-question UX/a11y audit rubric (Nielsen, clig.dev, WCAG-for-terminals).

## The verdict in one paragraph

Atrium's bones are excellent — context-sensitive hints, consequence-stating
dialogs, a rich creation form, a filter language, graceful 80×24 degradation. The
program attacks six clusters: **(T1)** hints/help/keys are three hand-maintained
copies of one truth and already drift (the #357 class); **(T2)** the core signal —
"an agent needs you" — never leaves the terminal, and the status vocabulary fails
the desaturation/tofu tests; **(T3)** first-run has concrete warts (welcome wrap,
"agent(s)", non-persisted skip, blank empty-state panel) and README documents half
the keymap; **(T4)** review is the industry's bottleneck and the diff tab is
read-only; **(T5)** power workflows (fan-out, scripting, prompt reuse, setup
scripts, undo, cost) are latent; **(T6)** Bubble Tea v2 (July 2026) unlocks the
frontier — synchronized output, kitty keyboard, theme events — first-class.

## The portfolio

29 issues in 6 themes (8×P1, 14×P2, 7×P3), filed 2026-07-16 as epic **#370**
with children **#371–#399** (program number NN maps to issue #(370+NN)).
Canonical one-paragraph rationales and
provenance live in each issue body; the visual audit (screenshots + annotations)
was delivered as a session artifact.

| # | Theme | Title (short) | Pri |
|---|-------|---------------|-----|
| 01 | T1 Foundation | Keymap registry: one table, three projections | P1 |
| 02 | T1 | Command log: show the real tmux/git/gh commands | P1 |
| 03 | T1 | One fuzzy-picker primitive | P2 |
| 04 | T1 | Command palette | P2 |
| 05 | T1 | Custom commands over session context | P3 |
| 06 | T1 | Remappable keys | P3 |
| 07 | T2 Attention | Focus-gated desktop notifications (OSC 9/777/99 + 1004) | P1 |
| 08 | T2 | Status vocabulary hardening (shape+color, legend, glyph ladder) | P1 |
| 09 | T2 | Window title + OSC 9;4 progress | P2 |
| 10 | T2 | Anti-jank & named-loading audit | P2 |
| 11 | T3 First-run | Welcome/empty-state polish | P1 |
| 12 | T3 | Docs refresh (promotes reserve R8) | P1 |
| 13 | T4 Review | Diff-comment → queued prompt | P1 |
| 14 | T4 | Post-merge lifecycle: archive + checks on the row | P2 |
| 15 | T4 | Checkpoint/rewind surface (exploratory) | P3 |
| 16 | T5 Power | Programmable surface: ls --json / send / peek | P2 |
| 17 | T5 | N-variant spawn & compare | P2 |
| 18 | T5 | Prompt history & reuse | P2 |
| 19 | T5 | Worktree setup scripts + managed ports | P2 |
| 20 | T5 | Layout presets on one key | P2 |
| 21 | T5 | Undo & recovery pass | P3 |
| 22 | T5 | Per-session cost/tokens chip | P3 |
| 23 | T6 Platform | Bubble Tea v2 + lipgloss v2 migration (enabler) | P1 |
| 24 | T6 | Adaptive theming: light/dark + NO_COLOR | P2 |
| 25 | T6 | OSC 8 hyperlinks + OSC 52 clipboard | P2 |
| 26 | T6 | Kitty keyboard wins (shift+enter) | P2 |
| 27 | T6 | Mouse polish (clickable hints, copy bypass, opt-out) | P3 |
| 28 | T6 | Inline image preview of agent artifacts | P3 |
| 29 | T6 | Copy & feedback consistency pass | P2 |

## Considered and not filed

- **Attention sort + header counters, one-question first-run wizard** — already
  shipped (status sort floats NeedsInput/unread; group headers carry counts; the
  welcome agent picker is the wizard pattern).
- **Kitty text-sizing wordmark, declarative fleet files, web client** — too thin /
  YAGNI / out of scope; revisit on demand.
- **Full screen-reader support** — the honest bar for a Bubble Tea canvas app is a
  linear fallback and diff-only repaints; folded into 08/16 rather than promised.
- **Fuzz targets; gh-token-in-argv** — standing vetoes from the 2026-07-03 review.

## Relations to existing state

- #316 (reduced-motion splash) stays open; T2/T6 reference it.
- #298 (per-account budget) stays deferred; issue 22 is per-session and
  transcript-parsed — a different thing.
- #357 (help-text nit) is retired by issue 01's registry.
- The S1 veto (no homogenizing of the ~24 state sites) bounds issue 01 to hint
  *derivation* only.
- Cut-list revivals with new evidence: `atrium ls --json` (16), OSC 52 (25),
  light theme (24).

## Priorities encode leverage × readiness, not size

The P1 set is the spine: registry (01) and command log (02) are foundations other
issues stand on; notifications (07) and status hardening (08) are the
orchestrator's core job; first-run (11) and docs (12) are the inviting part;
diff-comments (13) attacks the industry bottleneck; the v2 migration (23) unblocks
the platform tier (24, 26, and sync-output rendering for free).
