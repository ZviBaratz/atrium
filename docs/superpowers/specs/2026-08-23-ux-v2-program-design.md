# UX v2 — the orchestration-IDE overhaul (program design record)

Filed 2026-08-23 as **epic #793**, issues **#794–#833** (40), at HEAD `b5e3ec3`.
Extended 2026-08-25 with **#870–#873** (4: C10–C12, A7), multi-account integration
parity — filed against HEAD `9483313` and not part of the original triage.
Research: three codebase/ecosystem exploration passes + a 34-issue backlog
triage + a code-verified foundations design, all run 2026-08-23; digests in the
appendix are the evidence the issue bodies cite. File:line citations were
verified at that HEAD — re-locate symbols before implementing.

Program ID → issue number:

| F | R | C | P | A | X |
|---|---|---|---|---|---|
| F1 #801 | R0 #796 | C1 #813 | P1 #820 | A1 #830 | X1 #831 |
| F2 #802 | R1 #806 | C2 #814 | P2 #798 | A2 #826 | X2 #832 |
| F3 #803 | R2 #807 | C3 #815 | P3 #821 | A3 #829 | X3 #800 |
| F4a #804 | R3 #808 | C4 #797 | P4 #822 | A4 #828 | X4 #794 |
| F4b #805 | R4 #809 | C5 #816 | P5 #824 | A5 #827 | X5 #795 |
| | R5 #810 | C6 #817 | P6 #823 | A6 #825 | X6 #833 |
| | R6 #811 | C7 #818 | | A7 #873 | |
| | R7 #812 | C8 #799 | | | |
| | | C9 #819 | | | |
| | | C10 #870 | | | |
| | | C11 #871 | | | |
| | | C12 #872 | | | |

## 1. Context

Atrium's first UX program (epic #370, 29 issues, closed 2026-08-08) delivered
the feature substrate: command palette, keymap registry (63 rebindable keys),
settings panel, queue overlay, diff comments → queued prompts,
fork-from-checkpoint, cost chip, adaptive theming + NO_COLOR, OSC
notifications, N-variant bake-offs, programmable CLI. What accreted is broad
but uneven: features compose awkwardly, layout is one procedural function,
information exists that is never displayed, and ~34 open UX issues each
patched one corner.

Goal of this iteration: a **complete, production-grade, highly intuitive,
highly functional, informational-but-not-overbearing, configurable end-to-end
orchestration IDE**.

Ecosystem timing (research, 2026-08): the category consolidated — Crystal
deprecated, Terragon/Bloop dead, and Anthropic now ships first-party Agent
View (Claude-only, single-machine, no worktree/diff/cost story). Atrium's
defensible ground: **cross-agent, multi-repo, tmux/SSH-native, with real
review tooling.** The industry-wide bottleneck moved from *running* agents to
*reviewing and approving* their output — exactly wave 1's focus.

## 2. Decisions locked (maintainer-confirmed 2026-08-23)

1. **Orchestration IDE** — review/git/task/telemetry depth; no in-TUI file
   tree/editor; editing stays with agents + $EDITOR.
2. **Absorb & supersede** the open UX backlog — every triaged issue is folded
   into a program issue, scheduled as-is, or explicitly declined.
3. **Redesign freely** — better defaults win; breaking changes fine with
   migration notes.
4. **Wave-1 axes:** review-loop depth, configurability end-to-end, coherence &
   polish. Attention & fleet = wave 2.

Inherited from #370 (not re-litigated): layered UX (newcomer + power user);
no-limits ambition with graceful degradation. Standing refactoring vetoes
constrain design unless an issue presents new evidence: S1 (no blanket
homogenization of state sites), T2/T3/T5/T6, G8, L1 (theme stays a lipgloss
package), L4. #376 veto: no keybinding editing inside the settings panel.

## 3. North star & design principles

**The pitch:** one terminal surface where a fleet of coding agents is
launched, observed, steered, reviewed, and merged — where the human's scarce
attention is routed, not scattered.

- **Sort, don't show.** Humans attend 3-4 items; ranking and jump keys beat
  dashboards. New data earns a place in a ranking before a pixel.
- **Informational ≠ overbearing → depth lives in the inspector.** The session
  row is at capacity (name flex 28→19 cells). Fleet surfaces stay scannable;
  per-session depth (timeline, queue, cost breakdown, PR checks, setup
  output) moves to an inspector surface. Data first, chrome second — most of
  it is already measured and unsurfaced.
- **The review loop is the product.** Diff → comment → re-prompt exists; the
  overhaul completes the cycle: navigate, curate commits, judge PR readiness,
  merge — without leaving the TUI.
- **Configurable end-to-end, curated.** Every *surface* becomes configurable
  (themes as files, per-repo config, per-agent schemas, saved filters); every
  *constant* does not — a recorded decision per knob, not config sprawl.
- **Degrade gracefully everywhere.** Width ladders, glyph rungs, NO_COLOR,
  tmux-hostile features (images) declined. SSH-ability is a moat, never
  broken.
- **Muscle memory is real even when redesigning freely.** Defaults may
  change; every change ships with migration notes and the palette/help
  teaching it.

## 4. Program structure

Six workstreams; one epic (#793) with themed checklists (the #370 pattern).
Issue bodies carry scope, size, recommended model @ effort, deps, and what
they absorb — this section records the design rationale per workstream.

### F — foundations (#801, #802, #803, #804, #805)

Design doctrine (architect design, code-verified 2026-08-23): **selection
plumbing merges; behavior never does** — the S1 veto is HONORED by expressing
per-state variance as data. The five hand-written per-state enumerations
(viewContent overlay if-ladder, handleKeyPress 17-guard prelude, menuVisible
switch, per-overlay SetSize blocks, paste-routing switch) collapse into one
`surfaceSpec` table with a completeness guard. Acceptance bar for F1–F3:
**zero golden re-baselines** — the proof the refactor changed selection, not
behavior. F4a (data-driven tabs via the declared-but-unused `ui.Tab`) is the
one deliberate re-baseline.

Declined with reasons: ultraviolet's Cassowary layout solver (pseudo-versioned
API; 3-region arithmetic suffices — SizeSpec/bodyRegions are the seams it
would slot into later), a focus *stack* (one `focusTarget` enum; overlays stay
state-modeled), tab-set swapping per mode, a widget framework / Elm-child
routing, a big-bang `home` split (the F6 standing rule instead: each F/R PR
moves the fields it touches into a sub-struct). Note: bubbletea v2.0.8 has no
Layer API (a doc comment references a nonexistent symbol); bubblezone v2
stays.

### R — review loop (#796, #806–#812)

The diff pane grows file navigation (#806, using #691's exact
suppress/promote list) and width-gated side-by-side (#807); the tab set grows
permanent Commits (#808 — scoped curation verbs with undo-journal discipline,
no hunk staging) and PR (#809 — check-level detail from data already polled)
tabs; PR creation gains agent-drafted descriptions (#810, headless-hygiene
inherited); one key cycles a ranked review queue (#811); variant bake-offs get
the compare surface they never had (#812).

### C — configurability (#797, #799, #813–#819, #870–#872)

User theme files over the 18 semantic tokens, validated by the contrast
oracle (#813, honoring L1); a per-repo trust ledger (#814, answering #629's
five questions) gating repo-local `.atrium.json` (#815, the scopeGlobal seam's
first customer); adapter-declared session config schemas (#816) deleting the
Claude-shaped create form (#797 ships the immediate slice); CRUD editors for
custom_commands/repo_scripts (#817 — config forms, not the vetoed file
editing); the #376-safe keymap surface (#818: viewer, conflict checker,
commented dump); four curated cadence knobs (#799 — the rest stay hardcoded by
recorded decision); one liveness model for run_auto + Dev tab + re-run setup
(#819).

Added 2026-08-25, after a pooled rotation was found routing sessions to accounts
that silently lacked the integrations the pool was assumed to share: a read-only
per-config-dir capability probe plus a doctor parity section (#870, completing the
identity / aliasing / interchangeability three-way that CheckIdentity and
CheckPools leave two-thirds finished); an integrations view in the accounts panel
(#871, a fourth overlay MODE rather than a new app state, so it costs none of the
state or keybinding drift sites); and a declaration of which per-dir settings are
pool-shared versus deliberately individual, with an allowlisted writer (#872).
Recorded decisions for that group: no outbound API probe of connector grant state
(it would be Atrium's first network egress and its first token handling, and
Claude Code keeps credentials in the macOS Keychain rather than a readable file);
the writer touches settings.json only, never .claude.json's token material and
never .credentials.json; and routing still does not consult capability, preserving
expect_account's boundary that verification decides whether a chosen account may
launch, never which one is chosen.

### P — coherence & polish (#798, #820–#824, #823)

A table-driven notice priority ladder + NoticeWarn level (#820); confirm
double-tap generalized + push-dialog honesty (#798); ONE fact-priority table
driving row yield, gain, chip precedence, and chip-value filters (#821 —
staged under the old renderer first, config-panel style); help/palette
inversion per #694 (#822); onboarding + doctor + host pressure in-TUI (#824);
designed empty/loading states everywhere (#823).

### A — attention & fleet, wave 2 (#825–#830, #873)

The capstone is the unified approval inbox (#830): every blocked
session/gate/asked-question in one ranked queue, inline-answerable where the
adapter safety matrix allows, attach as fallback, autoyes stays binary.
Feeding it: blocked-duration + asked-question row indicators (#826), the
StatusHistory timeline (#829 — the ring's first consumer), handoff visibility
(#828), saved filter presets (#827), and `--readonly` per #522's adopted spec
(#825).

Added 2026-08-25: #873 gives AccountAvailability its missing sensor. The flag, the
rotation skip and SoonestResetMember all exist; Limited is only ever set by hand,
and Until has never been written by anything. The signal is read from the
transcript, discriminated by the isApiErrorMessage field, and deliberately NOT
through a session/agent PromptMatcher — a flat pane window over a limit literal is
#343 with a worse consequence, because the literal would live in this repo and a
session merely reading the file would match itself and flag a live account out of
rotation.

### X — production hardening (#794, #795, #800, #831–#833)

Wave 0 leads with the two UI-adjacent races (#794 overlay Cmd-goroutine, #795
identity-field family) so nothing is rebuilt on top of them; then resize-fanout
debounce (#831), the #570 frame-build remainder after a lipgloss bump (#832 —
go.mod still at lipgloss v2.0.5, so the "free" part has not landed), the pty
attach-fanout measurement (#800), and doctor report depth (#833).

## 5. Waves & sequencing

- **Wave 0** (no foundation deps): #794, #795 first, then #796, #797, #798,
  #799, #800 in parallel. 7 issues.
- **Wave 1**: #801 first, then #802 ∥ #803 ∥ #804, then #805. R/C/P tracks
  proceed as deps clear (per-issue deps lines). 24 issues.
- **Wave 2**: #825 anytime; #826/#827 after #821; #828 after #820; #829 after
  #805; #830 last. 6 issues.
- **Ongoing**: #831, #832, #833 opportunistically.
- **Added 2026-08-25** (multi-account integration parity, no foundation deps):
  #870 first, then #871, then #872 — wave 1, configurability. #873 is wave 2 by
  workstream but blocked on nothing. 4 issues.

Totals at filing: 40 issues (≈12 S / 21 M / 7 L). Recommended models: Fable 5 ×15
(#801, #814, #830 at max effort), Opus 5 ×17, Sonnet 5 ×8. The 2026-08-25 extension
adds 4 (3 M, 1 M-L; Fable 5 ×1, Opus 5 ×3) and is counted separately, because the
figures above are the output of the 2026-08-23 triage and nothing re-ran it.

## 6. Absorb map

All 34 triaged backlog issues plus 4 adjacent (#677, #527, #719, #529) were
dispositioned at filing time; each closed issue carries a pointer comment.

| Disposition | Issues |
|---|---|
| → F2 #802 (geometry acceptance cases) | #689, #693, #695 |
| → R | #696→#796, #691→#806 |
| → C | #574+#555→#813, #629→#814, #477→#815, #690→#797, #454→#816, #627/#628/#630→#819 |
| → P | #524/#525→#820, #469/#520→#798, #634/#677/#692→#821, #694→#822, #595/#688→#824 |
| → A | #763→#828, #522→#825 (spec adopted verbatim) |
| → X | #526→#831, #570→#832 (as-is), #548→#800, #527→#794, #719/#529→#795, #445/#604→#833 |
| Outside the program, open | #684 (adapter test debt) |
| Kept deferred, open | #298 |
| Closed declined | #639, #640 (images are a dead end inside tmux) |

## 7. Explicitly declined

Kanban-in-terminal, RTS canvases, agent-mailbox UIs, hardware decks (fads) ·
repomon-style 4-level zoom/grid (reopen on sustained N>15) · gh-dash
lanes-as-sections (#827 presets capture the value) · Sculptor-style
pre-review triage (revisit only if review stays the bottleneck after the
R-workstream) · graded risk-scored auto-approve (speculative until #830 inbox
usage exists) · Conductor per-turn git-ref checkpoints (fork/checkpoints
cover it) · in-panel keymap editing (#376 veto; #818 is the safe alternative)
· theme fs-watcher hot-reload (apply-on-save suffices) · in-TUI file
tree/editor (out of scope by decision 1).

## 8. Verification doctrine (every downstream session)

- `just ci` is the local gate; `just lint`, never bare golangci-lint.
- TUI changes prove themselves via the charm-tui plugin's verify-tui skill —
  a green Go suite cannot see width, reflow, colour, or clicks.
- The tui-drift-sites skill before touching keys, Config fields, glyphs, or
  UI states; press every new key (nothing asserts dispatch).
- New guards mutation-verified (mutate what the docstring names; check exit
  codes, not `--- FAIL` greps; shrink non-compiling mutations).
- Sandbox drives isolate `HOME`, `TMUX_TMPDIR`, and `CLAUDE_CONFIG_DIR`;
  never `-L` a socket name a live server could answer to.
- The contrast oracle covers any new colour pair; new glyphs are width-1 with
  an ascii-rung value; goldens and colours fingerprints are appended, never
  rewritten wholesale.

## Appendix — research digests (2026-08-23)

### A. Current UI surface

Scale: app/ ≈21.5k non-test lines (54 files), ui/ ≈23.5k (78), keys/ ≈1.9k,
session/ ≈25.6k.

What exists: 21 discrete UI states; 63 registered keys, all user-rebindable
via `config.keybindings` (three value forms + "disabled"; the palette is the
safety net); help/hint-bar/README are projections of the registry. Settings
panel: 10-category rail, 39/47 Config fields have rows, reflection-guarded in
both directions, `scopeGlobal` seam pre-built. Four layout presets + column
stepping + mouse divider drag. Tabs: Preview/Diff/Terminal. Theme system: 18
semantic palette tokens, 26+ glyph tokens, 3 fidelity rungs, light/dark twins
+ auto scheme, NO_COLOR mono, contrast oracle, width-1 glyph invariant, zero
hardcoded colors outside ui/theme. `dispatchAction` is an action bus (52
cases) shared by keypress/palette/hint-clicks with per-action Effect
classification. Mouse: rows, headers, tabs, hint entries, divider drag,
wheel; no overlay is mouse-navigable. Feedback: menu notice → errBox → info
modal; 11 pending-notice buffers. Ticks: preview 100ms, metadata 500ms (full
sweep each 4th), spinner 10Hz self-killing. Create form: 11 focus stops,
variant fan-out steppers, draft stashing, cap verdicts. Queue overlay,
checkpoints + TUI-only fork, prompt history, undo-kill, quick-send→queue,
batch ops. CLI: ls --json / peek / send / new / guide / doctor / reap /
profiles detect / update / reset.

Overhaul-hard: (1) no layout engine — a 180-line procedural function +
duplicated height budget + per-overlay fraction/cap literals; (2) components
don't own width (documented ±2 defect class); (3) the state enumeration
written 5× + 3 fixture files — adding a state = 7 sites; (4) `home` ≈130-field
god object with 20 overlay pointers; app_session.go 2694 lines; (5) no focus
model; modal-only overlays; (6) sizing constants scattered across ~20 sites;
(7) glyph gaps (agent table 2 rungs vs 3; inline rune literals outside both
tables; 4 deliberate ascii collisions); (8) extreme comment-to-code ratio —
rewrites must preserve or consciously retire rationale.

### B. Domain data & capability inventory

Status model: 6 statuses + 9 PaneStates (incl. PaneGate/PaneBackground/
PanePromptManual); a StatusUrgency ranking exists. StatusHistory: a 32-entry
transition ring with timestamps and zero non-test consumers.

Measured but never displayed: the StatusHistory ring; StatusChangedAt elapsed
for non-Pending states (a NeedsInput row blocked 4h shows nothing);
EndedAsking (no row indicator); PR ChecksPass/Fail/Pending + FilesChanged
(diff tab only); Cost.Requests/Unpriced; per-session cmdlog CPU; the setup
output tail; PortProblem (flash-once); PaneLive; queue head-in-flight;
account rate-limit Until; baseRef ("behind what"); LayoutCustom; the
context-ambiguity verdict.

Displayed but hardcoded: every cadence (metadata 500ms, frames 100ms, PR TTL
25/8s, read-dwell 1.5s, idle hysteresis 2/6 ticks, heartbeat 30s, watchdog
30min, notify throttle 3s); 4 presets; 5 themes with no user mechanism;
glyph rungs only; fixed 2-3-line rows; chip order; context thresholds 75/90;
the tmux bar format.

Structural constraint: the row is at capacity (name flex 28→21→19 cells);
context_indicator is a *mode* because there is no room for a ninth chip. New
per-session data needs a new surface.

Extension points: the dispatchAction bus; cmd.Executor + cmdlog; claude hooks
(working/ready/subagent in-flight set/heartbeat/permission-mode/effort +
SessionStart brief ≤750 runes); transcript readers (model/usage/cost/asked/
checkpoints/render + a markdown renderer); outbox spools (send/new) drained
at 100ms; custom commands with $ATRIUM_* env; tmux user options + the managed
conf template; per-account config-dir/token injection. Codex/gemini have no
hook support (#773). Fork is claude-only and TUI-only. The prompt queue is a
persisted, cancelable FIFO with zero-clock followups and composer read-back
delivery verification.

### C. Ecosystem research

Landscape: the category consolidated in H1 2026 — Crystal deprecated,
Terragon and Bloop dead. Anthropic's Agent View (May 2026) has state-grouped
rows, needs-input floating to the top, inline answer-from-the-list, and peek
vs attach tiers — Claude-only, single-machine. Codex ships cloud tasks as CLI
nouns. Atrium's moat: cross-agent, multi-repo, tmux/SSH-native, real review
tooling.

Category-wide gaps (recurring user complaints across peers): notifications, a
unified permission inbox (only octomux has one), integrated cost, real diff
review, flat lists that die at N=10, silent-failure onboarding.

Durable ideas adopted (→ program issue): Agent View state-grouping + inline
answering and the octomux permission inbox (→#830); Conductor's agent-drafted
PR descriptions (→#810); lazygit's rebase-as-keypresses, scoped down
(→#808); cmux's row-metadata richness (→#821/#826); gh-dash's config lanes,
scaled to saved presets (→#827); k9s/herdr theme files + a commented default
dump (→#813/#818); the ranked-inbox attention discipline (→#811/#830).
Declined: the fads and oversized patterns in §7.

Platform: bubbletea v2.0.8 / lipgloss v2.0.5 / bubbles v2.1.1 / bubblezone
v2 in go.mod; mode-2026 synchronized output is free; OSC 8 hyperlinks work in
tmux ≥3.4 (use more); OSC 52 needs an allow-passthrough note; desktop
notification OSC variants are fragmented and tmux strips them by default —
the existing enum + OS-native fallback stands; kitty keyboard only via
explicit enable (flag 1, non-kitty fallbacks); terminal images are a dead end
inside tmux.

### D. Backlog triage

Clusters found (one program issue each): the Claude-shaped create form
(#454+#690+#524); row density with no priority model (#692+#677+#634+#688+
#693); notice arbitration (#525+#524+#595+#763); confirm dialogs (#469+#520);
per-repo config + trust (#477+#629); the dev lifecycle (#627+#628+#630);
host-cost visibility (#445+#548+#595+#604); frame/update perf (#526+#570+
#548); the diff pane (#691+#696); kitty graphics (#639+#640); overlay
geometry (#695+#689).

Issue bodies with concrete proposals adopted rather than reinvented: #520,
#522 (near-complete spec), #691 (exact suppress/promote list), #634, #694,
#690, #595 (drawn mock), #298 (design record, kept deferred), #629 (five
ledger questions), #696 (elimination table), #684.

Staleness found at triage: #522's blocker #521 closed; #548's #546 closed;
#595's #594 closed; #520 claims to close the already-closed #465 (verify at
implementation); #574 self-heals on any theme touch (re-verify); #570's
lipgloss-bump portion had not landed (v2.0.5 at HEAD).
