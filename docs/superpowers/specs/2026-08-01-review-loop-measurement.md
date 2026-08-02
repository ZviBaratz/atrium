# What the review loop actually produces, measured

Status: measurement, 2026-08-01. Not a design.

This started as a design for automating the implement → review → triage loop. The
measurement dissolved the design — twice — so what survives is the measurement and
what it licenses. The discarded proposals are recorded at the end, because "we
considered building an engine and here is the number that stopped us" is the part
that stays useful.

## 1. What review actually finds here

Population: 51 PRs (#489–#562), merged 2026-07-27..2026-08-01, 168 commits. The
number range is the filter, not the dates — 56 PRs merged in that window. Of
98 follow-up commits, 30 are staged implementation steps and 68 are reactive.
Excluding 9 planning/bookkeeping docs, the 59 that fix a defect split:

| Class | Count | Share |
|---|---:|---:|
| Behaviour fix — the code did the wrong thing | 29 | 49% |
| Claim correction — a comment, doc or hint stated something untrue | 21 | 36% |
| Test hardening | 9 | 15% |

32 of 51 PRs (63%) had at least one reactive follow-up; 19 came out of the
implementer clean.

**The reviewer is not a documentation janitor.** Roughly half of what it produces
is code that behaves differently afterwards. An earlier read of this record — that
prose drift dominates — was drawn from a 20-PR headline skim and was wrong.

## 2. How the built-in reviewer compares

Four merged PRs were checked out at their **pre-fix** commit in a throwaway clone
and reviewed with `claude -p '/code-review HEAD^...HEAD'`, then scored against what
the human-driven review session had actually found on each.

Two arms, identical code and identical reviewer. The only difference in arm B is
the "Reviewing a change here" section added to `CLAUDE.md` in this commit.

| PR | Ground truth | Arm A (no guidance) | Arm B (guidance) |
|---|---:|---|---|
| #537 paste routing | 1 | partial — found the `Code = r[0]` / `tea.KeySpace` collision, never surfaced that pasting `q` quits | **hit** — named the quit path at `app_update.go:947` and read `bubbletea@v1.3.10 key.go:72-86` to establish v1's `[...]` guard |
| #551 bar restyle | 3 | 2 — the data race, the `status-style` clobber under `tmux_config_override` | 2 — same two |
| #556 idle profiling | 3 | 1 — the unstopped CPU profile | **3** — plus the README's `/tmp` vs `os.TempDir()`, and the sub-millisecond verb rendering as `0s` |
| #560 light palettes | 2 | 2 | 2, with the cause traced (`IsLight`'s doc names a consumer that does not exist yet) |
| **Recall** | **9** | **5 + 1 partial — 56%, or 61% crediting the partial half** | **8 ≈ 89%** |

Arm A missed one finding for a reason guidance could not fix: `refresh-client -S`
batched behind a `;`, which needs tmux-specific semantics. Arm B missed the same
one and nothing else.

**Both arms also produced findings the human-driven review never made.** Arm A
produced ten, two of which were verified live on `main` and are fixed in this
branch:

- `cmdlog/totals.go` `verb()` re-split a space-joined argv, so
  `verb("git -C /home/u/my repo status")` returned `"git repo"` — one bucket per
  repository, the exact scattering `preludeFlagsWithValues` claims to prevent.
- `ui/overlay/confirmationOverlay.go` captured `borderColor` at construction, so a
  dialog open across a theme change kept the old accent.

Arm B's novel findings shifted hard toward the targeted class: **two vacuous
tests** (`profile.go`'s unreachable `minWindow` clamp, whose test "below minimum is
clamped up" passes with the branch deleted; and `paste_test.go`'s guard, which
passes with the line it exists to check removed), and **four falsified numeric
claims** in #560, found by re-deriving every quoted contrast ratio with an
independent WCAG implementation.

### Three honest limits on this measurement

- **It scores finding, not fixing.** In the manual flow the review session finds
  *and* implements the fixes, which is why its commits carry mutation kill counts
  and verification against the real binary. Nothing here says `/code-review --fix`
  fixes to that standard.
- **Arm B has a confound.** The guidance was copied into each worktree as an
  uncommitted file, so it entered the review's scope; two of the four runs mention
  it. Both called it docs-only, but the arms are not perfectly clean.
- **Checking a claim is not the same as checking it correctly.** Arm B listed
  "`verb()`'s `i++`-then-`continue` correctly skips prelude flag values" under
  *checked and found correct*. That is false, and it is the very bug arm A had
  found and this branch proves with a probe. More claim-checking bought a large
  recall gain and one confidently wrong clean bill.

## 3. What this licenses

**Run `/code-review` before opening the PR.** Measured: ~89% of what a separate
review session finds, plus findings it does not, in minutes, at ordinary plan
usage.

**Keep the guidance in `CLAUDE.md`, not in a skill or `REVIEW.md`.** Local
`/code-review` follows `CLAUDE.md`, does not read `REVIEW.md` (only the managed
GitHub Code Review app does), and does not read a skill it was not told to load.
Guidance placed anywhere else is guidance the reviewer never sees.

**Keep the fixer and the adjudicator in different contexts.** The review's fix
commits are the least-reviewed code in a PR: they land after the implementer's
rigour and before merge. Both known defects of that shape live exactly there — on
#544 the review introduced a wrong number into the comment it was correcting, and
on #545/#552 an accepted finding was fixed at two of five sites. Whoever applies a
fix cannot audit it.

**Escalation stays a human decision.** `/code-review ultra` costs $5–25 in usage
credits after three free runs. At ~8 PRs/day that is $40–200/day unconditionally,
which is a veto, not a tuning knob.

## 4. What was proposed and dropped

A five-stage pipeline with a `pipeline/` state machine, `State.Pipelines`, resume,
a tracker interface, a run tab, an attention-ladder vocabulary and an
`atrium pipeline report` CLI. Dropped in two steps:

1. **The review session was already a product.** `/code-review` runs a background
   subagent with its own context window; `claude ultrareview <PR#>` reviews a PR
   from a cloud sandbox that clones it server-side. A staged increment existed
   purely to create a session on `pull/N/head` and dodge #451's review-on-main
   trap, which PR-mode review cannot have.
2. **With the finder in-session, at most one handoff survives**, and one handoff
   does not need a state machine.

## 5. The one product defect this surfaced

`promptDeliveryReady` (`app/app_poll.go`) requires only that the agent's input box
is on screen and the pane is not working. A turn that ends with a *prose question*
satisfies both, because `ApplyPaneState` maps `PaneIdle → Ready` and prose
questions are outside the three-way dialog classification pinned at
`session/instance_test.go:357-389`.

So a queued follow-up is delivered as the answer to a question the user never saw
— and `maybeNotify` (`app/app_notify.go:106`) suppresses the notification
*because* a follow-up is queued, on the reasoning that it is "about to be
auto-continued". The comment there already concedes such a turn "can't be told
apart from a real finish that awaits them", and the surrounding code exempts the
`NeedsInput` edge specifically so a blocked pane "stays genuinely actionable" — the
dialog case was reasoned about; the prose case was not.

The same gap makes the attention ladder backwards for the most actionable event a
session can produce: `notifyEventFor` classifies a question as `EventFinished`,
which `notifyRungFor` routes through `notifications_finished` — configurable down
to `off` — while a permission dialog always uses the base mode.

Filed separately. It is independent of any workflow: it is wrong for every session
in the fleet.
