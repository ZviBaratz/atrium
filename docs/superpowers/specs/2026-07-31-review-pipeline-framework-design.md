# A staged pipeline for the implement → review → triage loop

Status: design approved 2026-07-31. Six increments, PR 1–6, none stacked.

Provisional vocabulary: **pipeline** (the recipe), **run** (one execution),
**stage** (one step). Naming is the one open decision — see the last section. It
must be settled before it reaches a config key, a CLI subcommand and eight drift
sites.

## Problem

The repeating manual workflow is: open a session on an issue, let it implement
and open a PR; open a second session to review that PR and address findings; tell
the first session another agent reviewed its work and ask whether the changes are
correct; merge. It works. It is also entirely hand-driven, and the hand-driving
costs three separate things:

- **Waiting** — noticing that a session finished and it is time to launch the
  next one, across a fleet where several runs are in flight.
- **Keystrokes** — retyping the same prompts, copying PR numbers between
  sessions, pasting findings.
- **Context correctness** — every PR-review session starts on a fresh branch off
  `main` rather than the PR's code, and findings arrive as prose that is pasted
  by hand.

The goal is to encode the loop so the mechanical parts run unattended while the
judgement parts still stop and ask.

## What the record actually says

Read before designing. Each of these changed a decision, and the counts come from
this repo's own merged PRs rather than from priors about agents.

**The finding class is claims, not bugs.** #371's review produced findings that
were all *claims*; #393 PR 6's five-agent review found two defects, both comment
defects, both invisible to lint; #360's were all doc/guard drift. Interleaved
with those are genuine correctness catches — the aliasing hole (#519), the
all-exhausted gate ordering (#504), the unflushed CPU profile (#556), the
`filepath.Join("", …)` false-flip (#337).

This is why the finding rate does not fall when the implementer's effort level
rises. A comment that states a cell count the string does not have was true when
it was written; the author cannot see it, because the comment and the string came
out of one mental model. What the reviewer supplies is a reader who was not
there. A high finding rate is therefore the expected steady state, not a symptom
— and the repo is unusually exposed to it, because `go vet`, lint and the suite
are all structurally blind to prose drift.

**"Always has findings" is not quite the observed rate.** #313's five-agent
review found no issue at or above the reporting bar; on #399 item 5 the five
review agents came back clean and the one real finding was the human's; #452
produced exactly one finding, scored 70. What is near-certain is that the
reviewer *emits* something; whether it clears the ≥80 bar varies, and
sub-threshold findings are routinely fixed anyway (#452, #360), including a case
where findings scored 20/20/62 were reported as "no issues" and were real.

**The reviewer's own fix commits are the least-reviewed code in the PR.** They
land after the implementer's rigour and before merge, and nothing reads them.
Both known defects of this shape live exactly there: on #544 the review
introduced a wrong number in the comment it was correcting, and on #545/#552 an
accepted finding was fixed at two of five sites.

**Premises go stale.** #546's report had three of its four proposals aimed at the
wrong thing; #308's fix was right but its rationale was falsified by #332; #448's
review found the cited precedent pointed the other way. A pipeline whose only
successful outcome is a PR would implement these anyway.

**The review session starts on the wrong code.** On #451 the review session's
branch sat at `main`'s tip while the PR head was elsewhere; two of five agents
rediscovered the mismatch at real token cost and three never noticed.

## What the tree actually says

**`Ready` cannot distinguish "finished" from "asked you a question."** The Stop
hook "fires once at true end-of-turn" (`session/tmux/hooks.go`), the pane
classifier sees an idle pane, and `ApplyPaneState` maps `PaneIdle → Ready`
(`session/instance.go`). A prose question — which is what an architectural or UX
junction always is — ends the turn and reads as success. Any engine that advanced
on `Ready` would step over every design question an agent bothered to ask. This
single fact is the design's central constraint.

**The dialog case is already classified, and only the dialog case.**
`session/instance_test.go:357-389` pins a three-way mapping: an approval prompt
(auto-tapped under AutoYes), a manual prompt (`NeedsInput`, never tapped) and a
startup gate (`NeedsInput`, never auto-accepted, "auto-accepting trust is
unsafe"). Prose questions are outside all three.

**Status transitions are already recorded.** `Instance` keeps a bounded ring of
`StatusTransition{From,To,At}` (`session/instance.go:78`, cap 32 at `:93`). A
Running→Ready edge is available without new plumbing.

**Idle-only prompt delivery already exists.** `QueueFollowupPrompt`
(`session/instance.go:1428`) enqueues with a zero clock so delivery happens
"strictly when the agent next idles rather than force-injecting it mid-turn."
That is exactly stage-handoff semantics; no new delivery path is needed.

**The daemon cannot host this.** It runs only while no TUI is alive and snapshots
the instance list once for its lifetime, and the TUI is the sole session creator
(CLAUDE.md). Creating the review session is precisely what a run must do.

**There is no session-create CLI.** `main.go:538-568` registers `ls`, `peek`,
`send`, `debug`, `version`, `reset`, `profiles`, `doctor`, `hook`, `update`. An
external driver therefore cannot spawn a session; sequencing lives in the TUI or
reaches it through `internal/outbox`, whose package doc explains why `state.json`
has exactly one writer.

**Hook artifacts already live outside the worktree** — `<configDir>/hooks/<name>/`
— "so they survive pause (worktree removal) and never pollute the agent's git
status / diff" (`session/tmux/hooks.go`). Run artifacts must follow the same rule
or they land in the diff under review.

**Tabs are index constants.** `PreviewTab`/`DiffTab`/`TerminalTab` are `iota`
constants (`ui/tabbed_window.go:76-79`) and `Toggle` cycles `len(w.tabs)`
(`:162`). A conditional fourth tab must be appended, never inserted, and
`activeTab` must reset when it disappears while active.

**Subprocesses must stay off the Update thread.** #380 spent a PR clearing them
off it; #526 and #527 are open races there now. A probe that shells out to `gh`
runs as an async `Cmd`.

## Decisions taken

1. **Autonomy level (ii): advance forward automatically, never merge.** Level (i)
   (every transition keyed) does not solve the waiting cost; level (iii)
   (auto-merge for a qualifying class) is an **explicit non-goal** until the
   journal can define its bar — a green-checks predicate would have merged #544.
   Stage 0's human ack is part of the recipe, not an exception to the level: the
   run pauses there by design, at the start, and runs unattended afterwards.
2. **Consultation is up front, with a just-in-time escape hatch.** Junctions are
   enumerated before code is written; one discovered later escalates.
3. **A stage advances on an artifact, never on an assertion.** Every predicate
   reads disk or the tracker. An agreeable agent cannot produce one.
4. **The product owns the gates; prompts own the work.** What must be reliable,
   visible and mutation-testable is in Go; what will change weekly is in
   checked-in skills.
5. **One hardcoded recipe, with the seam for config.** `Recipe`/`Stage` are types
   from day one and predicates are a closed set named by string, so a later
   `pipelines.json` can name one without `state.json` becoming a scripting host.

## The recipe

Five automated stages between two human bookends.

| # | Stage | Runs in | Prompt's job | Artifact that completes it |
|---|-------|---------|--------------|----------------------------|
| 0 | **consult** | implementer session (new) | Judge the premise; then enumerate the decisions this work forces — approach, depth, UX direction, non-goals — each with options and a recommendation. Stop. | `decisions.json` exists **and** carries the human's answers (`answered_at`) |
| 1 | **implement** | same session | Implement to the answers. `just ci`. Commit, push, open the PR. | an open PR whose head is this branch |
| 2 | **review** | new session, **on the PR head** | Run `/code-review` against the PR. | `findings.json`, one structured record per finding |
| 3 | **triage** | implementer session | Contradiction check, then verify the review's diff. Apply what survives, push. | every finding carries a verdict **with a reason** |
| 4 | **recheck** | review session (warm) | Re-read only the fix commits: was each accepted finding fixed, at every site? | verdict on the fix diff |
| — | **merge** | human | — | a keypress |

**Stage 0 always stops for the human.** It fires at the start of a run, when the
human is present; the rest runs unattended. A zero-junction consult still stops —
it is a one-key ack — and is flagged in the journal, because "did the agent stop
asking?" is the drift the analytics exists to catch. Stage 0 may investigate
(read code, run probes) before answering, since #546 shows premise falsification
can require measurement; its deadline is the most generous of any stage.

**Stage 3's prompt is a reframe, not a rewording of today's message.** "Do you
agree with the review?" is a question whose answer is predictable and therefore
carries almost no information. Its two real jobs:

- **Contradiction check** — for each finding, does it collide with a decision
  recorded in `decisions.json`, a rejected alternative, or a constraint from the
  issue? Cite the record. On #541 a review agent argued for a spelling the human
  had already seen and declined; only the implementer's session held that fact.
- **Verify the review's diff with the rigour the original got** — recompute every
  stated number, mutation-test every new assertion (#544).

The engine enforces what a prompt cannot: **every finding gets a verdict,
including sub-threshold ones.** No silent drops.

**Stage 4 exists on evidence** (#544, #545) and is bounded: one small diff, one
question, a warm session. It is the first stage to cut if the journal says it
never fires.

## Two exits per stage

A stage that can only succeed by producing a PR will produce a PR even when the
issue is stale. To make refusal compete with compliance, refusal must be cheap
and legible:

Alongside its completion artifact, any stage may write `verdict.json` —
`{stage, outcome: refused | asked | blocked, evidence, recommendation}`. Both
exits are artifacts; neither is silence.

Stage 0's record leads with a **premise verdict**: `holds | already_implemented |
falsified | partly`, with evidence.

A run's terminal states are therefore `merged`, `closed-no-change` (premise
falsified or already implemented), `superseded` (scope changed, needs a new
issue) and `abandoned`.

**Invariant 4 generalises from "never merge" to "never take an outward-facing
irreversible action."** The agent drafts the falsification to
`<dataDir>/pipelines/<id>/premise.md`; the human posts the comment and closes the
issue. An agent that can close issues on evidence it gathered itself is a far
larger trust grant than one that opens a PR the human will read anyway.

## The engine

**State.** `config.State` gains `Pipelines []PipelineRun`: id, stage index,
session names by role, tracker ref, PR number, blocked cause, timestamps.
Persisted, so a run survives a TUI restart.

**Package.** A new `pipeline/` package holds the recipe, the predicates and a
pure state machine with no Bubble Tea in it. `app/` holds only wiring — edge
subscription, `Cmd` dispatch, rendering. This keeps the invariants unit-testable
without a TUI and keeps the new surface out of the `app` package the refactoring
programme is trying to shrink.

**Trigger: edge, not poll.** A run's predicate is evaluated on a Running→Ready
edge of its active session, plus once at startup, plus a manual "check now". That
is roughly one probe per turn boundary rather than two per second, and it is the
only moment a completion predicate is meaningful. The probe runs as an async
`Cmd`.

**Handoff.** `QueueFollowupPrompt`, unchanged.

**Invariants** — the reason the gate is Go and not a prompt:

1. Never advance without the artifact predicate.
2. Never advance a session sitting in `NeedsInput`.
3. **Idle without an artifact is a junction, not a failure**: block, escalate,
   wait. This is what makes silence safe.
4. Never take an outward-facing irreversible action (merge, close, comment).
5. Never run two stages of one run concurrently; spawning the review session goes
   through the **existing** session-cap path (#360/#463) and blocks at the hard
   cap rather than bypassing a gate.

**Blocked is the only failure state.** The engine never cancels a run, kills a
session or rolls anything back. Four actionable causes:

| Cause | Meaning | Signal quality |
|---|---|---|
| `refused` | the stage judged the work should not happen | a decision, not a failure |
| `asked` | junction raised (the JIT hatch) | expected; rate tunes stage 0 |
| `incomplete` | no artifact **and** no verdict | the only genuinely bad one — a prompt bug with an address |
| `dead` / `deadline` | session died, or wall-clock elapsed | mechanical; a deadline escalates, never acts |

**Stage completion is monotonic** — a PR closed later never rewinds a run.

**Resume is free**, because every predicate reads disk or the tracker: on restart
the engine re-evaluates the persisted stage. There is no in-memory state to
reconstruct and no way for a run to disagree with reality.

**The config seam.** `Recipe{Stages []Stage}`,
`Stage{Name, Role, PromptTemplate, Artifact ArtifactKind, Deadline}`.
`ArtifactKind` is a closed set named by string, one per stage plus the refusal
exit — `decisions_answered` (0), `pr_exists` (1), `findings_written` (2),
`findings_all_verdicted` **and** `commits_pushed_since` (3), `recheck_verdicted`
(4), `issue_drafted` (the `refused` exit) — never arbitrary executable logic. A
stage names one or more; all must hold.

## The tracker

The issue source is an interface from day one — `tracker.Ref{provider, id, url}`
with `Fetch` and `Draft` — even though v1 implements GitHub only. The alternative
is `gh` invocations baked into stage prompts, which later cannot be lifted out.

**Linear changes the autonomy level, not just the API.** Linear is used on work
repos, and work repos are ask-before-commit-and-PR while personal repos are
commit-and-PR-once-verified. On a Linear-tracked repo the default drops to level
(i): every transition needs a keypress. That is a derived rule, and better
encoded than remembered.

## UX surface

**One new keybinding; everything else through the palette.** A run has at least
six verbs (start, pause, abort, check now, resolve, merge) and each new key costs
eight drift sites (#528) — `keys/keys.go:178` records what shipping an
unregistered key cost last time. `paletteGates` already dims what the selection
cannot do (#516).

- **Starting a run**: the one new key opens the existing create form in pipeline
  mode, with the tracker ref as a required field. Same form, same smart-dispatch
  routing, one extra field.
- **Seeing a run**: a stage chip on the row — `▸ review 3/5` → `▸3/5` → `▸`. It
  is a hint, so it ladders, and a call-site ladder cannot join `hintLadders`, so
  this one must be built to. Given #464/#541/#545/#557 and #478/#479 the
  80-column guard ships in the same PR as the chip.
- **The two sessions stay two rows.** No third grouping axis in the list; the
  derived title (`pr-556 review`) plus the shared chip is enough. Explicit YAGNI
  cut.
- **Reading a blocked run** needs almost no new surface: the agent asked in
  prose, and the preview pane is already showing it. What no surface holds is the
  run as a whole, so a **read-only fourth tab**, present only when the selected
  session is in a run: stages with timestamps and outcomes, the decision record,
  findings × verdicts × reasons, blocked cause. Read-only is what avoids #523's
  stated footgun, where `enter` means "expand a detail" in one mode and "perform
  a git mutation" in another.
- **Answering a junction reuses quick-send.** Reply with `s`; the next
  Running→Ready edge re-fires the probe. The existing quick-send *is* the unblock
  action.
- **Escalation**: new attention-ladder events (#450, #377) —
  `pipeline.junction`, `pipeline.refused`, `pipeline.incomplete`,
  `pipeline.ready-to-merge`. The last is the "come back, it is your turn" signal
  for an unattended run and sits at the loudest permitted rung. Muted sessions
  stay muted; the chip still shows.
- **Merging extends what exists**: the terminal state renders `✓ ready to merge`
  and the existing push/merge affordance performs it, naming the PR, the finding
  count and the accepted/rejected split (folding in #469).

## The decision journal

**What it must answer** — a store that cannot name its questions is what #523
argues against:

1. Is the reviewer earning its keep? Findings per run by class, acceptance rate,
   and how many accepted findings changed *shipped code* rather than a comment or
   a test.
2. Which PR shapes never yield an accepted finding? That is the skip predicate.
3. Does the reviewer introduce defects? Recheck verdicts rejecting the review's
   own fix (#544 class) and partial fixes (#545 class). Stage 4 must justify its
   own continuation.
4. Is the consultation rate right? Junctions per run, how often the human's
   answer matched the recommendation, and how often a junction arrived **JIT
   rather than up front** — the direct quality signal on stage 0's prompt.
5. How often is the premise stale? Given #546, plausibly the most valuable number
   here, and it is about issue-filing rather than about agents.
6. Where do runs get stuck? Blocked-cause distribution and the `incomplete` rate
   per stage.

**Where it lives: nowhere new.** `<dataDir>/pipelines/<runID>/` already holds
`decisions.json`, `findings.json`, `premise.md` and the verdicts, because the
gates read them. `run.json` beside them is the index — the same shape as
`internal/undo`'s one-record-per-entry store. Nothing is logged as a chore: the
artifact-gating chosen for safety produces the labelled dataset for free.

**No TUI surface.** The run tab shows one run; cross-run analysis is
`atrium pipeline report`, a CLI subcommand beside `ls`/`peek`/`send`.

**Schema** — `run.json`: tracker ref, repo, branch, PR; per stage
`{name, started, ended, outcome, session, artifact}`; `junctions[]`
`{question, options, recommendation, answer, raised: upfront|jit}`;
`premise{verdict, evidence}`; `findings[]`
`{class, score, file, claim, stage, verdict, reason, impact}`; `recheck[]`
`{finding, refixed, defect_introduced}`; `terminal`.

Two fields are load-bearing and non-obvious:

- **Recipe fingerprint.** A hash of the stage prompts and predicates in every
  run, with a report that refuses to pool across fingerprints without saying so.
  Tuning a prompt makes earlier runs incomparable — the same trap as #393 PR 6's
  shuffle seed across a changed test set.
- **`impact`, derived from the fix diff**, not asked for: `code | test | comment
  | docs`, computed from which lines the fixing commit touched. The agent's own
  `accepted` verdict is a biased label — it is the same agreeableness being
  measured — while "did shipped behaviour change" is computable and unfakeable.
  On this repo's record the honest prediction is that it lands heavily in
  `comment`/`test`, which would be a finding about what the reviewer is *for*,
  not a disappointment.

**Two honest limits.** There is no token/cost column: local usage data is not
obtainable (atrium#298), and a made-up number is worse than none — wall-clock per
stage is what exists. And the merge is the only human-applied label, so
overriding a verdict at merge time needs one key to record it, or the dataset
inherits the agent's bias.

## Staging

**Nothing is stacked.** Workflows trigger only on `base=main`, so a stacked PR
gets no CI and is unverified rather than lagging. Every increment is main-based
and independently green.

| PR | Contents | Value on its own |
|---|---|---|
| 1 | "review PR #N" — a palette action and create-form mode producing a session checked out at `pull/N/head`, titled `pr-N review`, seeded with the review prompt. No engine. | Kills the #451 trap; builds the create-from-a-ref plumbing every later stage needs |
| 2 | The prompts as checked-in skills: consult / review / triage / recheck, with the decision-record format and the two-jobs triage framing. **No product change.** | The whole workflow becomes runnable by hand, and artifacts start accumulating before the engine exists |
| 3 | `pipeline/`: recipe, stages, closed-set predicates, the five invariants. Fake clock, fake tracker, no network, no Bubble Tea, no behaviour change. | The invariants get reviewed as pure logic, isolated from UI |
| 4 | Wiring behind a config flag defaulting off: edge subscription, async probe, `State` persistence, the row chip and its 80-col guard. | First increment that actually advances a run |
| 5 | Escalation and the run tab: ladder events, appended fourth tab with its index-reset test, palette entries and gates. | Unattended runs become legible |
| 6 | `atrium pipeline report` and the derived `impact` field. | The loop becomes measurable |

**PR 1 and 2 run by hand for roughly ten PRs before PR 3 is written.** The
artifacts collected in that window validate that the predicates are checkable,
produce the first numbers on junction rate and finding impact, and are allowed to
change the design — including cutting stage 4 or gating stage 2 by diff shape.
This is the "stage the rewrite under the old renderer first" lesson from the
configuration-panel series, applied to a workflow rather than a UI.

## Verification

- **One mutation test per invariant**, mutating what the docstring names:
  advance without the artifact; advance from `NeedsInput`; advance past a raised
  junction; two stages at once; merge. Each mutation must turn exactly one guard
  red — survivors that mask each other are a design signal, not a
  test-writing gap.
- **The load-bearing negative control**: an agent goes idle with no artifact and
  no verdict, and the run must **not** move. One test per blocked cause plus a
  mutation for each. A false advance is the silent, expensive failure this design
  exists to prevent.
- Hermetic throughout: anything reaching `config`/`state`/`tmux` sets `HOME` to a
  temp dir.
- The chip gets an 80-column render guard; the tab gets the append-and-reset
  test; the new key gets all eight drift sites **and someone pressing it**, since
  nothing asserts that a registered key has a `case` in `handleKeyPress`.
- Before PR 4 is called done: an end-to-end run against a real issue, eyeballed
  in an isolated tmux (`HOME` *and* `TMUX_TMPDIR`).

## Non-goals

- **Level (iii) autonomy** — auto-merge for a qualifying class. Deferred until
  the journal can define the bar from data; a green-checks predicate would have
  merged #544.
- **Linear support in v1.** The interface exists; the implementation is GitHub.
- **A lifecycle log.** `L` already shows every subprocess and `U` covers kills.
  The journal records decisions and verdicts, which nothing else holds.
- **Restructuring the session list** to express parent/child runs.
- **Token or cost accounting** (atrium#298).

## Acceptance criteria

1. A review session created by PR 1 has the PR's head commit checked out, proven
   by a test asserting the worktree HEAD equals `pull/N/head`, not `main`.
2. A stage never advances without its artifact predicate returning true, proven
   by one mutation per invariant.
3. An agent that idles with neither artifact nor verdict leaves the run blocked
   with cause `incomplete`, and the run does not move.
4. A run survives a TUI restart and resumes at the persisted stage with no
   in-memory reconstruction.
5. The engine performs no merge, issue close or issue comment on any path.
6. Spawning a review session at the hard session cap blocks and escalates rather
   than bypassing the cap.
7. Every finding in `findings.json` carries a verdict and a reason before stage 3
   completes.
8. `run.json` carries a recipe fingerprint, and the report does not pool across
   fingerprints silently.
9. The row chip renders inside 80 columns at every ladder rung.
10. The fourth tab is appended, never inserted, and `activeTab` resets when it
    disappears while active.

## Open decision: naming

"Pipeline / run / stage" is accurate and generic. This repo's vocabulary is more
particular — sessions, profiles, presets, pools, variants, batch, dispatch. The
name reaches a config key, a CLI subcommand (`atrium <name> report`), a `State`
field, a palette entry, a keybinding, the chip, the tab label and the help
screen, so it should be settled before PR 3. Candidates worth weighing against
"pipeline": **relay** (names the handoff), **loop** (names the shape), **run**
promoted to the top-level noun, or **circuit**.
