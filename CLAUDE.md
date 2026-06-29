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
  relaunch rather than mid-run.
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
| Build (stamps version from git) | `just build` → `./bin/atrium` |
| Run | `just run -- <args>` |
| Test (hermetic — safe anywhere) | `just test` |
| Test with race detector | `just test-race` |
| Coverage | `just cover` |
| Lint | `just lint` (golangci-lint) |
| Format | `just fmt` / check with `just fmt-check` |
| Vet | `just vet` |
| Local release snapshot | `just snapshot` (GoReleaser) |
| Tag a release | `just release <X.Y.Z>` |

**Toolchain note:** if `go` is not on `PATH`, pass it explicitly:
`GO=/path/to/go just test`. CI uses `go-version-file: go.mod`.

## Verifying your work

Always confirm a change with `just build` **and** `just test` before claiming it
works. `just test` is the source of truth for correctness; `just build` proves the
binary still compiles and version-stamps.

Some `session/tmux` tests (e.g. `TestSessionDeathStopsProbing`) drive a **real**
tmux server, so they self-skip when `tmux` is not on `PATH`. They run all tmux
commands on Atrium's dedicated socket (`tmux -L <socket>`) — if you add a test that
shells out to tmux directly, route it through the package's `tmuxCommand()` helper
so it targets that same socket, not tmux's default one.

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

Tests must never read or write the user's real data dir. Packages that resolve the
config dir, save state, or touch tmux set `HOME` to a temp dir (see
`config/config_test.go` and `app/app_test.go` `TestMain`). Any new test that can
reach `config`/`state`/`tmux` writes must do the same.
