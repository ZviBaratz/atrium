---
name: spawn
description: Use when handing work off to a new Atrium session - creating a follow-up agent, spawning a session for a task, or choosing the model, effort and permission mode a new session should run with. Covers continuing from the current branch, writing the handoff prompt, and reporting what was actually created.
---

# Spawning an Atrium session

You are inside an Atrium session. `atrium new` creates another one — its own git
worktree, its own branch, its own agent — and it takes the flags that decide what
that agent runs with. This skill is how to choose them and how to hand off well.

Work through the four steps in order. Do not skip to the command.

## 1. Settle what the follow-up starts from

Two questions, both cheap, and both change the command:

**Does it need this session's work?** A new session's branch starts from the
origin repository's HEAD, not from yours. A follow-up that builds on what you
just did needs `--branch` naming your branch; one that starts clean needs
nothing.

**Is that work committed?** `--branch` carries commits, not your working tree. Run
`git status --porcelain`: anything uncommitted is invisible to the new session
however you spawn it. If the follow-up depends on uncommitted work, say so in
step 3 and offer to commit first — never spawn a continuation over a dirty tree
and let the next agent discover the hole.

Run `atrium new` from wherever you are. Atrium notices it is a worktree and
creates in the origin repository instead, saying so on stderr — you do not need
`--path`.

## 2. Choose the flags

Three axes, chosen independently. Bias every one of them toward quality: when two
values both fit, take the more capable one. A session that thought too hard costs
tokens; one that thought too little costs a re-run and your review.

**Permission mode — by how much review the work needs.**

| Value | When |
|---|---|
| `plan` | Design work, review work, unclear scope, anything where the approach is not already settled. Start here, and pass it explicitly. |
| `acceptEdits` | A written plan or spec already exists *and* tests guard the change, so the work is execution rather than judgment. |

Pass the flag. Omitting it does not mean `plan` — it means claude's own `default`
mode, which can edit — so the session you meant to gate behind a plan runs
unfettered.

**Effort — by how hard the problem is.**

| Value | When |
|---|---|
| `max` | The session is the last line of defence: a release, a security-sensitive change, or a retry after a previous attempt failed. |
| `xhigh` | Novel design, a hard bug, a cross-cutting refactor, or any change that is expensive to discover you got wrong. |
| `high` | The floor for anything that changes behaviour. |
| `medium`, `low` | Mechanical, fully specified, test-guarded work only. |

**Model — by how much judgment the task needs.**

| Value | When |
|---|---|
| `opus` | Everything, unless the row below is unambiguously true. If you cannot tell which of the two it is, it is not mechanical. |
| `sonnet` | The task is both genuinely mechanical *and* fully specified. |

Only `claude` takes these flags, and what happens to a non-claude session depends
on the company it keeps. The pins are refused only when *nothing* being created
runs claude — one codex session, or a fan-out of nothing but codex and gemini. A
*mixed* fan-out is accepted: the pins land on its claude members and the others
are left untouched, with no warning. So a bake-off spanning agents compares a
pinned claude against a default codex, which measures the pins and the agents at
once. Fan out across claude alone, or pin nothing.

## 3. Confirm in one question

Ask once, with `AskUserQuestion`. Put your pick first, the two nearest
alternatives beside it, and show the exact command each would run so the choice
is reviewable rather than a label. If step 1 found uncommitted work the follow-up
needs, make committing-first one of the options.

Then honour the answer. This table is a default, not a policy — the person at the
keyboard is the one who knows whether this task is the exception.

## 4. Spawn, then report what actually happened

A single-line prompt goes as the second argument:

```sh
atrium new "fix the flaky width test" "start from the failing case in the width test" \
  --model opus --effort high --permission-mode plan --wait 60s
```

A real handoff prompt is longer than that, and `-` reads it from stdin, so pass
it as a heredoc rather than writing a file into the user's tree:

```sh
atrium new "fix the flaky width test" - --branch "$(git branch --show-current)" \
  --model opus --effort xhigh --permission-mode plan --wait 60s <<'PROMPT'
What is done: the capture fixtures are in place and the table is keyed by width.
Where to start: the failing case, which reproduces on a narrow pane.
Acceptance: the test suite and the linter pass.
Do not touch: the fixture widths themselves - they are captured from a live pane.
PROMPT
```

Write that prompt properly. What was done, where to start, what "finished" means
for this repository, and what not to touch. It is the whole context the next
agent gets, and re-briefing a session that started from one vague line costs more
than writing it did.

**`atrium new` queues; it does not create.** It spools a request the running
Atrium picks up, so pass `--wait` and report the branch and worktree it comes
back with. Read its stderr: it says when nothing is draining the queue, and when
Atrium is running but attached to a session, in which case the request waits for
a detach rather than failing. Report that as what it is. Never call a queued
request a created session.

**A `--wait` that runs out exits non-zero, and the request survives it.** The
deadline is yours, not the queue's: the spooled request is still there and still
lands when something drains it. So a non-zero exit here is not a failed handoff,
and spawning again on the strength of it is how one handoff becomes two sessions.
Say the request is queued and unconfirmed, and let the person decide.

## Other flags worth knowing

- `--account <name>` pins which configured Claude account the session runs on, by
  the name it is configured under. Use it when routing would pick a different one
  than the work needs. A pool name is not an account name and is refused. Never
  write `CLAUDE_CONFIG_DIR` into the program to do this: it runs the session on
  one account while Atrium records another.
- `--variants claude:2` fans one prompt out across several sessions for a
  genuine bake-off. Pass `--branch` too, or the members start from different
  commits and the comparison measures the wrong thing.
- A title is a branch. The branch and tmux names derive from it, a title whose
  names are taken is refused rather than suffixed, and an over-long one is
  refused with the limit named. Keep it short and imperative. `--variants` is the
  exception: there the title is a stem, and each member is named from it with a
  numeric suffix, so report the names the members actually got.

`atrium new --help` is the authority on all of it, including exactly when a
queued create lands.

## Not yours to run

`atrium reset`, `atrium reap --kill` and `atrium update` belong to the person at
the keyboard. Ask before running any of them.
