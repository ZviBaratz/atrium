# The implement → review → triage loop

Status: designed 2026-07-31; **substantially revised 2026-08-01** after a
measurement falsified two of the original premises. The first version specified a
six-PR pipeline engine. The measurement cut it to two increments, one of which
ships no Go at all.

## What the measurement changed

Three findings, in the order they matter.

**1. The finding class is bugs, not claims.** The original spec asserted that the
reviewer's dominant output is prose drift — comments and docstrings stating things
the code does not do — and built its argument on that. Measured over the 51 PRs
merged between 2026-07-27 and 2026-08-01 (#489–#562, 168 commits), the reverse
holds. Of 98 follow-up commits, 30 are staged implementation steps and 68 are
reactive; excluding 9 planning/bookkeeping docs, the 59 that fix a defect split:

| Class | Count | Share |
|---|---:|---:|
| Behaviour fix — the code did the wrong thing | 29 | 49% |
| Claim correction — a comment, doc or hint stated something untrue | 21 | 36% |
| Test hardening | 9 | 15% |

32 of 51 PRs (63%) had at least one; 19 came out of the implementer clean. Claim
drift is a large minority, not the main event. The review stage is fixing
behaviour about half the time it fires.

**2. The separate review session is not the cheapest way to get that.** Claude
Code has shipped review as a product since March–April 2026 and this machine runs
v2.1.220, which has all of it: `/code-review` runs a background subagent with its
own context window, `--fix` applies findings and `--comment` posts them inline;
`/review <pr>` reviews a PR; `/code-review ultra <PR#>` runs a verified
multi-agent fleet in a cloud sandbox that clones the PR directly; `claude
ultrareview <PR#>` does the same non-interactively for scripts.

So the original stage 2 — spawn a second Atrium session on `pull/N/head` and
prompt it to review — was a hand-rolled, weaker version of a shipped feature, and
the original PR 1 existed to fix a trap (#451's review-on-main) that PR-mode
review cannot have, because the reviewer never touches the local worktree.

**3. Measured head-to-head, the built-in reviewer is competitive.** Four merged
PRs were checked out at their **pre-fix** commit in a throwaway clone and reviewed
with `claude -p '/code-review HEAD^...HEAD'`, then scored against what the
human-driven review session had actually found on each.

| PR | Ground-truth findings | Recovered | Missed |
|---|---|---|---|
| #537 paste routing | 1 | root cause found (`Code = r[0]` colliding with `tea.KeySpace`, plus the CR case), severity understated — never surfaced that pasting `q` quits or `esc` discards the draft | — |
| #551 bar restyle | 3 | the data race on `configOverridePath`/`managedConfigInvalid`; `ApplyBarStyle` clobbering a user's `status-style` under `tmux_config_override` | `refresh-client -S` batched behind `;` sinking the push |
| #556 idle profiling | 3 | the unstopped CPU profile → zero-byte pprof, with the same empirical confirmation (0 bytes vs 351) | `summaryLine` rounding after guarding the raw duration; the README's `/tmp` vs `os.TempDir()` |
| #560 light palettes | 2 | the inverted luminance ramp **and** the `lightTwin` comment asserting an invariant nothing checks | three other stale comment sites |

**Recall ≈ 55–60%** (5 hits + 1 partial of 9). It also produced **10 findings the
human-driven review never made**, two of which were verified as real and are
**still live on `main`**:

- `cmdlog/totals.go` `verb()` re-splits a space-joined argv, so
  `verb("git -C /home/u/my repo status")` returns `"git repo"`. Proven by probe
  against `origin/main`. Nothing tests it. This is the exact failure the
  function's own `preludeFlagsWithValues` comment says it prevents.
- `ui/overlay/confirmationOverlay.go:46` captures `borderColor` from
  `theme.Current()` at construction, falsifying the frame-parity helper's premise
  that styles are read lazily — so the confirm state is the one state the light
  golden does not actually verify.

A prediction recorded before the run — that the #560 splash finding would be
missed because the defect is only visible in a rendered frame — **was wrong**. The
reviewer found it statically, by reading the new golden and spotting near-black
ANSI spans that would render as foreground ink on a near-white terminal.

**What the misses have in common.** Of the three clean misses and one partial, two
are cross-artifact claim consistency (a README naming a path the code does not
use; a rounded value contradicting a test docstring's promise), one is
domain-specific semantics (tmux's `;` sinking a batch), and one is failing to
propagate a found mechanism to its worst consequence. That is a **tunable** gap,
and it is the same class the original spec over-weighted — right target, wrong
reason. Not because claim drift dominates the output, but because it is what the
default reviewer systematically misses.

**What this measures, and what it does not.** All of the above scores the built-in
reviewer as a **finder**. It says nothing about it as a **fixer**, and in the
manual flow the review session does both — it finds *and* implements the fixes,
which is why its commits carry mutation kill counts, verification against the real
binary, and measured ratios. An edit pass applied after a review is not
demonstrably the same work. Any claim that `/code-review --fix` substitutes for a
review session is therefore unevidenced and is not made here.

**Consequences for the design.** The finder moves in-session on measured evidence,
which removes the round trip that exists purely to *obtain* findings. The fixer's
location stays open. What is cut regardless is the machinery the original spec
proposed for sequencing sessions — the `pipeline/` engine, run state, resume, the
tracker interface, the run tab and the report — because at most one handoff
survives, and a single handoff does not need a state machine to drive it. Human
touchpoints drop from four to two: consult at the start, merge at the end.

## What remains a real problem

**The stop-for-a-decision signal.** The Stop hook fires once at true end-of-turn
(`session/tmux/hooks.go`), the classifier sees an idle pane, and `ApplyPaneState`
maps `PaneIdle → Ready` (`session/instance.go`). A prose question — which is what
an architectural or UX junction always is — ends the turn and reads as success.
`session/instance_test.go:357-389` pins a three-way mapping for *dialogs* only: an
approval prompt (auto-tapped under AutoYes), a manual prompt (`NeedsInput`), and a
startup gate (`NeedsInput`, never auto-accepted). Prose questions are outside all
three, so a session that stopped to ask is indistinguishable from one that
finished. This is unchanged by the measurement and is now the whole product half
of the design.

**The waiting cost.** ~8 PRs a day sustained (15 on 2026-07-27, 11 on the 28th and
29th). Even at two touchpoints per PR, the cost is not the keystrokes — it is
noticing that a session is waiting.

**The review's own fix commits are the least-reviewed code in the PR.** They land
after the implementer's rigour and before merge. On #544 the review introduced a
wrong number into the comment it was correcting; on #545/#552 an accepted finding
was fixed at two of five sites. This survives the redesign because it is about
*who checks the fix*, not about how many sessions there are.

**Premises go stale.** #546's report had three of four proposals aimed at the
wrong thing; #308's fix was right but its rationale was falsified by #332; #448's
review found the cited precedent pointed the other way. Nothing in the product —
`/code-review` reviews a diff — asks whether the issue is still worth doing.

## The recipe

Four stages between two human bookends. At most one session handoff, and no
engine.

| # | Stage | Prompt's job | Done when |
|---|-------|--------------|-----------|
| 0 | **consult** | Judge the premise, then enumerate the decisions this work forces — approach, depth, UX direction, non-goals — each with options and a recommendation. Stop. | the human has answered |
| 1 | **implement** | Implement to the answers. `just ci`. Commit, push, open the PR. | an open PR |
| 2 | **review and fix** | An independent reader finds, verdicts *every* finding including sub-threshold ones, and applies what survives. Whoever fixes must verify to the standard the original got — `just ci`, re-render, recompute stated numbers, mutation-test new assertions. Push. | findings verdicted and fixes pushed |
| 3 | **adjudicate** | **A different context from the fixer** reads only the fix diff: is each change correct, aligned with the stage-0 decisions, and beneficial? Cite the record where it collides. | every fix carries a verdict |
| — | **merge** | human | a keypress |

**Stage 0 always stops.** It fires when the human is present; everything after runs
unattended. A zero-junction consult still stops — it is a one-key ack.

**The load-bearing invariant is that the fixer and the adjudicator are different
contexts.** The review's fix commits are the least-reviewed code in the PR — they
land after the implementer's rigour and before merge — and both known defects of
that shape live exactly there: on #544 the review introduced a wrong number into
the comment it was correcting, and on #545/#552 an accepted finding was fixed at
two of five sites. A context that applied a fix cannot audit it; that is the one
thing the self-correction literature is unambiguous about. Either arrangement
satisfies the invariant — a separate review session that finds and fixes with the
implementer adjudicating (the current manual flow), or an in-session
`/code-review --fix` with a fresh context adjudicating — so long as the two roles
never collapse into one.

**Stage 3 is not "do you agree?"** That question's answer is predictable and
therefore carries almost no information. Its real job is the contradiction check:
does a change collide with a decision recorded at stage 0, a rejected alternative,
or a constraint from the issue? On #541 a review agent argued for a spelling the
human had already declined, and only the implementer's session held that fact.

**Who should fix is an open question, deliberately left open.** The measurement
below scored the built-in reviewer as a *finder* and says nothing about it as a
*fixer*. The manual flow's review sessions demonstrably fix to a high standard —
mutation kill counts, verification against the real binary, measured ratios — and
an edit pass applied after a review is not obviously the same thing. Moving the
finder in-session is evidenced; moving the fixer is not, and should not be done
until it is.

**Escalation is the human's call, not a gate.** `/code-review ultra <PR#>` costs
$5–25 in usage credits after three free runs. At ~8 PRs/day, running it
unconditionally is $40–200/day, which is a veto rather than a tuning knob. It stays
a deliberate per-PR decision for substantial changes.

## Two exits

A stage that can only succeed by producing a PR will produce a PR even when the
issue is stale. Stage 0 therefore leads with a **premise verdict** — `holds |
already_implemented | falsified | partly`, with evidence — and any stage may stop
with `refused`, `asked` or `blocked` instead of its artifact. Both exits are
explicit; neither is silence.

**The agent never takes an outward-facing irreversible action** — no merge, no
issue close, no issue comment. It drafts the falsification; the human posts it. An
agent that can close issues on evidence it gathered itself is a far larger trust
grant than one that opens a PR the human will read anyway.

## Deliverable 1 — the recipe as checked-in configuration

**No Go. No product change.** A skill beside `.claude/skills/tui-drift-sites/`
encoding the four stages, the decision-record format, the fixer-is-not-the-
adjudicator rule, and the review instructions.

The review instructions target the measured miss profile, not a guess:

- Behaviour claims in comments and docstrings need a `file:line` citation, not an
  inference from naming.
- A doc, README or plan that names a path, count, flag or command is checked
  against the code it describes.
- A found mechanism must be propagated to its worst consequence before it is
  written up — the #537 partial.

**Important detail:** local `/code-review` follows `CLAUDE.md` but **does not read
`REVIEW.md`**. Only the managed GitHub Code Review app does. So these instructions
belong in `CLAUDE.md` or the skill, and a `REVIEW.md` is only worth writing if
managed Code Review is enabled for this repo — which requires a Team or Enterprise
Claude organisation and a GitHub App install. **Checking that is the first
action**, because if it is available, stage 2's finding half fires automatically on
push, with thread auto-resolution as fixes land and no CLI step at all.

Run by hand for roughly ten PRs before any Go is written. That window produces the
first honest numbers on junction rate, on whether the finding rate moves, and — the
question this design most needs answered — **whether a fix applied by
`/code-review --fix` survives adjudication as often as one written by a review
session.** That is the one comparison the measurement did not make, and it decides
whether the remaining handoff goes away.

## Deliverable 2 — the awaiting-decision signal

The one thing no prompt, skill or platform feature can supply: distinguishing *the
agent stopped because it needs you* from *the agent finished*.

**Mechanism.** The recipe's stages end by writing a small status artifact under
`<configDir>/hooks/<sanitizedName>/` — the location hook artifacts already use "so
they survive pause (worktree removal) and never pollute the agent's git status /
diff" (`session/tmux/hooks.go`). A run artifact in the worktree would land in the
diff under review. Atrium reads it on the existing Running→Ready edge —
`Instance` already keeps a bounded ring of `StatusTransition{From,To,At}`
(`session/instance.go:78`, cap 32 at `:93`) — and derives a fourth attention state
alongside Running/Ready/NeedsInput.

**The load-bearing rule: silence never means success.** An idle session with no
artifact is treated as *awaiting a decision*, not as finished. That is what makes
the failure mode safe — a prompt that forgets to write its artifact produces a
session that asks for attention, never one that is silently assumed done.

**Surface.**

- A new attention-ladder rung (#450, #377), named by event:
  `session.awaiting-decision`. Muted sessions stay muted.
- A row chip, laddered for width. Given #464/#541/#545/#557 and #478/#479, the
  80-column guard ships in the same PR as the chip.
- Answering reuses quick-send (`s`). `QueueFollowupPrompt`
  (`session/instance.go:1428`) already enqueues with a zero clock so delivery
  happens "strictly when the agent next idles rather than force-injecting it
  mid-turn" — exactly the semantics needed, unchanged.
- Any probe that shells out runs as an async `Cmd`. #380 spent a PR clearing
  subprocesses off the Update thread and #526/#527 are open races there now.

**No new keybinding.** Quick-send is the unblock action and the palette already
dims what the selection cannot do (#516). A new key costs eight drift sites
(#528), and `keys/keys.go:178` records what shipping an unregistered key cost last
time.

## Staging

Nothing is stacked: workflows trigger only on `base=main`, so a stacked PR gets no
CI and is unverified rather than lagging.

| PR | Contents | Value on its own |
|---|---|---|
| 1 | Deliverable 1 — the recipe skill and the review instructions. Zero Go. | The whole workflow is runnable by hand today, and produces the data PR 2 needs |
| 2 | Deliverable 2 — the artifact convention, the Running→Ready read, the ladder rung and the row chip with its 80-column guard | Every session becomes legible about whether it needs you, pipeline or not |

PR 2 is worth building **independently of the recipe**. "Did this agent stop
because it needs me?" is a question about every session in the fleet.

## Verification

- **The load-bearing negative control**: a session goes idle with no artifact and
  must be reported as *awaiting a decision*, never as finished. A false "done" is
  the silent, expensive failure this design exists to prevent.
- One mutation per invariant, mutating what the docstring names. Each must turn
  exactly one guard red; survivors that mask each other are a design signal, not a
  test-writing gap.
- The chip gets an 80-column render guard at every ladder rung.
- Hermetic throughout: anything reaching `config`/`state`/`tmux` sets `HOME` to a
  temp dir.
- Before PR 2 is called done: an end-to-end run against a real issue, eyeballed in
  an isolated tmux (`HOME` *and* `TMUX_TMPDIR` — `HOME` alone does not isolate the
  socket).

## Non-goals

- **A pipeline engine** — `pipeline/` package, `State.Pipelines`, per-run state
  machine, resume, run tab, `atrium pipeline report`. Cut: with review and triage
  at most one handoff there is no multi-stage sequence to drive.
- **Building a way to spawn a review session on a PR head.** Superseded for
  *finding* by `/code-review`, `/review <pr>` and `claude ultrareview <PR#>`, none
  of which touch the local worktree. If a separate session keeps the *fixing*
  role, it is started the way any session is started today; nothing new is needed
  for one handoff.
- **Auto-merge.** A green-checks predicate would have merged #544.
- **A tracker abstraction.** GitHub via `gh`, in the prompt, until a second tracker
  actually appears.
- **Continuous analytics.** The question "is the reviewer earning its keep?" was
  answered by one afternoon's deliberate experiment; the interesting successor —
  "what does it systematically miss?" — is answered the same way. A permanent
  store is not what produced either number.
- **Token or cost accounting** (atrium#298).

## Acceptance criteria

1. A session that idles without writing its stage artifact is reported as awaiting
   a decision, and is never reported as finished.
2. The awaiting-decision state is derived on the existing Running→Ready edge, with
   no new poll loop and no subprocess on the Update thread.
3. Stage artifacts live outside the git worktree and never appear in the PR's diff.
4. The row chip renders inside 80 columns at every ladder rung.
5. A muted session stays muted at the new rung; the chip still renders.
6. The recipe skill is runnable end-to-end by hand, with no Go changes, before
   PR 2 is written.

## Follow-ups this measurement generated

Two live defects found while measuring, neither related to the design. Both should
be filed and fixed independently of this work:

- `cmdlog/totals.go` `verb()` mis-parses a prelude flag value containing a space.
  Proven against `origin/main`; no test covers it.
- `ui/overlay/confirmationOverlay.go:46` captures `borderColor` at construction,
  so the frame-parity helper's lazy-style premise is false for the confirm state.

## Open decision: naming

Mostly dissolved with the engine. What survives needs a name for one state, not a
subsystem: **awaiting-decision** is descriptive and reads correctly in a chip, a
ladder event and a help line. It reaches fewer surfaces than the original
`pipeline` vocabulary would have, so it is no longer a blocking decision.
