# Atrium — project guide

Atrium is a terminal command center for orchestrating multiple AI coding agents
(Claude Code, Codex, Gemini, Aider, …). Each session runs in its own tmux session
on a dedicated socket, inside an isolated git worktree, so many agents work in
parallel without conflicts. It is a Go TUI built on Bubble Tea, with a Cobra CLI
entrypoint in `main.go`.

Module path: `github.com/ZviBaratz/atrium`. Binary: `atrium` (alias `atr`).

## Architecture

The control flow is **Cobra → Bubble Tea → Instance → (tmux + git worktree)**:

- **`main.go`** — Cobra root command and subcommands (`reset`, `debug`, `version`).
  The bare `atrium` invocation loads config, initializes tmux, then calls
  `app.Run`. A hidden `--daemon` flag reuses the same binary as a *separate
  process* (see daemon below).
- **`app/`** — the Bubble Tea program. `home` (in `app.go`) is the root model: it
  owns the instance list, the discrete UI `state` (default / new / prompt / help /
  confirm / rename), and a per-tick poll loop that refreshes each session's status
  and diff. This is the orchestrator everything else hangs off.
- **`session/`** — `Instance` is the core domain object: one agent = one
  `Instance`, which lazily composes a `tmux.Session` + `git.Worktree` on
  `Start()`. Its `Status` (Running / Ready / Loading / Paused / NeedsInput) drives
  list rendering and daemon behavior. `naming.go` derives branch/session names from
  the immutable `Title`; `displayName` is a cosmetic, freely-mutable label.
  `storage.go` persists instances via `config.State`.
- **`session/tmux/`** — wraps a real tmux server on a *dedicated socket*. Each
  session runs the agent program in a pty; `Poll()` captures pane content and
  classifies it (busy markers, prompt detection) into a `PaneState`. tmux/git calls
  go through a `cmd.Executor` interface (`cmd/`) so tests can fake them.
- **`session/git/`** — `Worktree` manages the isolated worktree + branch:
  `Setup`/`Cleanup`/`Remove`, `CommitChanges`, `PushChanges` (uses `gh`). "Pause"
  removes the worktree but keeps the branch; "resume" recreates it.
- **`daemon/`** — autoyes runs as a background process, **not** a goroutine. When
  autoyes is on, the TUI launches `atrium --daemon`, which polls all stored
  instances and taps Enter on prompts; the TUI kills it on startup/exit. It runs
  only while no TUI is alive and snapshots the instance list once for its lifetime
  (the TUI is the sole session creator), so new sessions are picked up at the next
  relaunch rather than mid-run. A per-data-dir flock (`tui.lock`, held by the
  interactive `atrium` in `main.go`) enforces one TUI per data dir, so that
  snapshot can't be raced by a concurrent TUI (#230).
- **`config/`** — two persisted artifacts in the data dir: `config.json`
  (`Config`: program, profiles, auto-attach) and `state.json` (`State`: serialized
  instances plus UI state like collapsed repos and recent paths). See the
  identity/live-state section before touching path resolution.
- **`ui/`** — presentational Bubble Tea components (list, preview, diff,
  tabbed window, menu, overlays); they hold view state but defer domain actions to
  `home`.
- **`web/`** — **a standalone Next.js marketing site, not part of the Go binary.**
  It has its own npm toolchain (`cd web && npm run dev`); `just`, `go test`, and
  `fmt-check` deliberately exclude it. Don't apply Go conventions here.

## Commands (use `just`)

All development tasks go through the `justfile` — discover them with `just --list`.

| Task | Command |
|------|---------|
| **Verify (the local gate — mirrors CI)** | **`just ci`** = build + vet + fmt-check + lint + test + cover |
| Build (stamps version from git) | `just build` → `./bin/atrium` |
| Run | `just run -- <args>` |
| Test (hermetic — safe anywhere) | `just test` |
| Test with race detector | `just test-race` |
| Coverage | `just cover` |
| Lint | `just lint` (golangci-lint; must be on `PATH`) |
| Format | `just fmt` / check with `just fmt-check` |
| Vet | `just vet` |
| Vulnerability scan | `just vuln` (govulncheck; needs network) |
| Local release snapshot | `just snapshot` (GoReleaser) |
| Tag a release | `just release <X.Y.Z>` |

**Toolchain note:** if `go` is not on `PATH`, pass it explicitly:
`GO=/path/to/go just test`. CI uses `go-version-file: go.mod`.

## Verifying your work

Confirm a change with **`just ci`** before claiming it works or pushing. It is the
local gate that mirrors CI: `build vet fmt-check lint test cover`. `just test` alone
is the inner loop while you iterate — it is the source of truth for *correctness*,
but it is not the gate, because it cannot see the checks CI fails on.

**`just ci` needs `golangci-lint` on `PATH`.** `go install
github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest` puts it in
`$(go env GOPATH)/bin`, which is often not on `PATH` even when `go` itself is — so
the tool can be installed and still not resolve. When it doesn't, the sweep runs
build, vet and fmt-check green and *then* dies at `lint` with `not found` (exit
127), which reads like a broken recipe rather than a missing tool.

Lint is the part of the gate the rest cannot substitute for. `build`, `vet` and
`fmt-check` all pass happily while `golangci-lint` fails, so a lint break is
invisible to every other command here. `unused` is the usual culprit — a const or
field declared for an assertion you meant to write compiles fine and only CI
notices. `revive`'s `exported` (doc comments on exported symbols) and
`redefines-builtin-id` (don't name things `max`/`min`/`len`) bite the same way.

**Lint through `just lint`, never a bare `golangci-lint run`** — to scope it, pass
packages to the recipe (`just lint ./ui/...`). The recipe keys `GOLANGCI_LINT_CACHE`
to this worktree; the global cache a bare run uses reports stale findings from a
sibling worktree (#486).

`just ci` does not cover everything CI runs: the race detector (`just test-race`),
a macOS job, and `govulncheck` (`just vuln`, needs network) are CI-only. And a
local gate is not a green CI — on a PR, read `gh pr checks <n>` before calling it
merge-ready.

Some `session/tmux` tests (e.g. `TestSessionDeathStopsProbing`) drive a **real**
tmux server, so they self-skip when `tmux` is not on `PATH`. They run all tmux
commands on Atrium's dedicated socket (`tmux -L <socket>`) — if you add a test that
shells out to tmux directly, route it through the package's `tmuxCommand()` helper
so it targets that same socket, not tmux's default one. That helper is about *which
socket*, not about isolation: what keeps a raw `exec.Command("tmux", "-L", …)` off
the live fleet is the inherited `TMUX_TMPDIR` (see "Tests must stay hermetic"), which
is why `config_parse_test.go` can safely bypass `tmuxCommand()` for its own probe
socket. A test that sets its own `TMUX_TMPDIR` opts out of the teardown that reaps
what it leaves behind.

## Reviewing a change here

Measured over 51 PRs (#489–#562), the 59 follow-up *commits* that fix a defect
split 29 behaviour / 21 claim / 9 test — one commit routinely absorbs a whole
round, so this is not a finding census
(`docs/superpowers/specs/2026-08-01-review-loop-measurement.md`). `go vet`, lint
and the suite catch none of the middle class, and a review that only hunts for
bugs reports about half of what this repo actually ships wrong. Three rules, aimed
at what is missed rather than at what is already caught:

- **A claim about behaviour needs a citation, not an inference from naming — or from
  declaration order.** When a comment, docstring, README line, plan step or hint
  states what the code does — a count, a cell width, a path, a flag, a command,
  "both cases", "the only caller" — open the code and check it. A statement that was
  true when written and is false now is a defect, and it is the one nothing else here
  can see. So is one that was never true: #665 had to correct "`permission-network` is
  permanently shadowed by `permission`", read off `permission` coming first in claude's
  `Prompts` when it is the *stricter* predicate and misses the sandbox dialog
  `permission-network` catches. `DetectPrompt` does return the first match, so order
  settles it where both fire — on the fetch pane both do, and `permission` wins only by
  being first. Order plus one predicate is the inference that fails; read both.
- **Check a claim against the artifact it names, across file boundaries — and a
  comment naming N artifacts is N lookups.** The README naming `/tmp` while the code
  calls `os.TempDir()`, a test docstring promising a guard the assertion does not
  make, a comment counting consumers of a symbol that has since grown one. These live
  in a different file from the code they describe, which is why they survive. Checking
  the first name and believing the rest is how a header in #665 came to cite three
  guards for the direction its table could not cover when only two existed — and the
  missing one was a real hole.
- **Follow a found mechanism to its worst consequence before writing it up.** A
  collision reported as "a paste beginning with a space is mishandled" and a
  collision reported as "pasting `q` quits the app without confirmation" are the
  same finding; only the second gets prioritised correctly.

Prefer `file:line` evidence over reasoning about what a name implies. A finding
you cannot cite is a hypothesis. Shipped prose is the opposite case — a finding is
read while it is still true, a comment is not — so cite the symbol, never the
position. `TestNoProseCitesAPosition` reads the git index and enforces that in
exactly three places: Go comments, `#` comments in `.sh`, and unfenced markdown.
Prose in any other file type is unguarded, as is a plan under `docs/superpowers`,
which is exempt on purpose. And it reads one spelling — a path, a colon, a number.
It cannot tell whether you cited a symbol, and it does not see a position written
any other way, so `<file> line 251` and a GitHub-style `#L251` go through.

## Conventions

- **Commits:** Conventional Commits, lowercase (`feat: …`, `fix: …`).
- **Versioning:** the git tag is the single source of truth. `main.go`'s `version`
  defaults to `dev` and is injected via `-ldflags` at build/release time — never
  hand-edit a version string.
- **License:** AGPL-3.0 (mandatory — Atrium is a derivative of
  [claude-squad](https://github.com/smtg-ai/claude-squad); see `NOTICE`).

## Identity & live-state safety (important)

There are three identity layers. The first two are pure renames; the third is
state-bearing and must never be migrated in place:

- **Module / imports:** `github.com/ZviBaratz/atrium/...`.
- **Brand:** binary name, URLs, docs.
- **Runtime identifiers (live state):** the data dir and the tmux socket. Resolved
  by one function, `config.RuntimeName()`, which returns `atrium` for fresh
  installs and the legacy `claudesquad` when only `~/.claude-squad` exists. From it
  derive the data dir (`~/.atrium` vs `~/.claude-squad`), the tmux socket, the
  session-name prefix (`Prefix()`), and the managed conf filename.

`config.GetConfigDir()` implements **prefer-new, fall back to legacy, never move**:
it picks `~/.atrium` if present, else an existing `~/.claude-squad` (untouched),
else defaults to `~/.atrium`. This matters because the data dir contains the
`worktrees/` tree and a `state.json` of **absolute** paths, and git records each
worktree's absolute path in the project repo's `.git/worktrees/<name>/gitdir` —
moving the dir would orphan live sessions. When adding anything that names the
data dir or the tmux socket, derive it from `config.RuntimeName()`; do not
hardcode either name.

## Tests must stay hermetic

Tests must never read or write the user's real data dir, or its live tmux fleet.
Those are two separate isolations, and `testutil.SandboxHomeMain` (called from a
package's `TestMain`) installs both: `HOME` at a temp dir for the data dir, and
`TMUX_TMPDIR` at a private socket root for tmux. Any new test that can reach
`config`/`state`/`tmux` writes must go through it.

**`HOME` alone does not isolate tmux.** The socket name is `config.RuntimeName()`,
which returns `atrium` for a sandbox `HOME` *and* for every non-legacy install — so
a `HOME`-only sandbox binds the exact socket a running Atrium is on. Only the
socket *directory* separates them, which is why `RequireTmux` hard-fails (never
skips) when `TMUX_TMPDIR` is not the sandbox root: an unisolated real-tmux test
injects panes into the developer's live sessions (#581). And a teardown that kills
a tmux server must never name one the live fleet could answer to: `-L` resolves
against `TMUX_TMPDIR`, which tmux reads as `/tmp` when it is empty *or* names a
missing directory, so `tmux -L atrium kill-server` in a teardown whose root had gone
destroys every running agent. `internal/testutil` reaps by absolute socket path
(`tmux -S`), which cannot resolve anywhere but the path given;
`config_parse_test.go` keeps `-L` because its socket name is a per-run
`<brand>-cfgparse-<rand>` that no live server ever binds. Address it by path unless
the name is unmistakably yours.

Note which half of that name does which job. The brand prefix is there so
`ownsSocketName` claims the socket and a *live* server left on it shows up in
`atrium doctor` and `atrium reap` (#602) — it buys visibility, not safety, and only
via `ScanServers`, which finds a server through `/proc` wherever its socket sits.
The leftover socket *file* is out of reach of the stale-socket list whatever it is
called, because `ScanStaleSockets` reads only `SocketDir`. What keeps `-L` safe is
the **random suffix**, and only that: with the brand prefix in place the name now
looks like one the live fleet could answer to, so shortening it to a fixed
`atrium-cfgparse` would put a `kill-server` one missing `TMUX_TMPDIR` away from a
name a real server binds.

## Facts with more than one home

A keybinding, a `Config` field, a glyph and a UI state each live in several places
on purpose, because help and docs are *projections* rather than second copies. Most
of those sites are guarded by a drift test; a few are not, and guessing which is how
a key ships registered, documented, and dead.

`.claude/skills/tui-drift-sites/SKILL.md` is the enumerated map — every site, and
the test that fails when you miss one. Read it before adding or rebinding any of
those things. The headline gap: **nothing asserts that a registered key has a
`case` in `handleKeyPress`**, so a green suite does not prove a new key does
anything. Press it.

**Prose says why; data says what.** When a comment is about to state a width, a count,
a path or a list, put the value somewhere a test can read it instead — a struct field,
a table, a `grep` recipe with a guard beside it. `session/agent/pane_width_test.go` is
the worked example: `paneCapture.width` makes a fixture's width a value, and the coverage
invariant is computed from it (#648, #665). The const names (`codexApprovalPane28`) and the
doc comments still carry it too — the datum was added *beside* the prose, not swapped for
it, so the table is the authority and the rest is description. The drift-sites skill does
the same for its counts, which `keys/skill_counts_test.go` holds to the tree. Where a value
must stay in prose, one file owns it and the rest cite that file. Reasons are not exempt
from checking, only from this remedy — verify a reason against the mechanism, per the
first review rule above. And correct a claim by grepping the *claim*, not the sentence in
front of you: in #646 a fix landed in `registry.go` and left `registry_test.go` repeating
the same two false things, in the file whose own fixtures disproved them, under a commit
message saying "Both corrected". Cite the PR, not the *branch* commit: a squash-merged
PR's branch SHAs never land on `main`, so a bare one strands anyone who has only `main`.
Recovering it (`git fetch origin refs/pull/N/head`) needs the PR number anyway.

**On the third attempt at one sentence, delete the claim instead of restating it.**
#720 cited a line pair, corrected it to a *second* wrong pair — its own edits had
moved them again — and it only stopped rotting when the third pass removed the
numbers rather than replacing them. #675 hit the same wall with symbol names: "any
name in that slot would have been the third wrong one". Two failed corrections say
the mechanism does not want stating, not that the words were wrong.

For the general discipline of proving a TUI change is right — why a passing Go suite
cannot see width, reflow, colour, or a click that hit the neighbouring row — use the
`verify-tui` skill from the `charm-tui` plugin. It is enabled in
`.claude/settings.json` but lives in an external marketplace, which that file cannot
install for you: run `/plugin marketplace add ZviBaratz/claude-plugins` once per
machine, or the skill will not resolve.
