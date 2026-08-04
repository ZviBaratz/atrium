# Orphaned tmux servers — the reaper (#547 PR 2) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make an unreachable orphaned tmux server *findable* and *killable on request*, without ever deleting a directory. A server whose socket file was removed with its `TMUX_TMPDIR` root is invisible to `tmux ls`, to `atrium reset`, and to every cleanup path Atrium ships; the only thing that can name it is a process scan.

**Architecture:** Three layers, each independently landable. `session/tmux` gains a pure scanner (`ScanServers`) that identifies Atrium-owned servers by socket path read from `/proc/net/unix`, never by argv — the argv carries injected `GH_TOKEN`s. `internal/doctor` gains a read-only report over it. A new headless `atrium reap` command is the only thing that kills, always behind a confirmation naming what dies. **It kills processes and deletes nothing** — no `os.Remove` of socket files, no `RemoveAll` of roots, not in `reap` and not in `doctor`.

**Tech stack:** Go 1.26; Linux-only (`/proc`), with a build-tagged no-op elsewhere; Cobra; testify; tmux ≥ 3.2 (`tmux.MinVersion`).

Design doc: `docs/superpowers/specs/2026-08-04-orphaned-tmux-servers-design.md`. Issue: #547. PR 1 (prevention) merged as `2e8b4da` (#589).

## Global Constraints

- **Gate:** `PATH=$PATH:$HOME/go/bin just ci` must be green before any commit is called done. `golangci-lint` lives in `~/go/bin`, which is *not* on mise's PATH — without the prefix the sweep dies at `lint` with exit 127, which reads like a broken recipe. Also run `just test-race`: the package-level seams here are shared state.
- **Lint through `just lint`, never a bare `golangci-lint run`.** The recipe keys `GOLANGCI_LINT_CACHE` to this worktree; a bare run uses the global cache and reports stale findings from a sibling worktree (#486).
- **No sweep, and no recursive delete over a glob, anywhere.** Not at startup, not in `reap`, not in `doctor`. #586 removed exactly such a sweep after it wiped the developer's live fleet and most of `/tmp`. Historical sockets get *reported*, with the command printed for the user to run.
- **Absence of an ownership marker never means "safe to act."** A candidate qualifies because something Atrium wrote says so, not because it failed to look like someone else's.
- **Never mutate a destructive guard in place to prove it is load-bearing.** Extract the predicate and assert on it as a pure function; where the guard is a whole script, run the *previous committed version* as the control. This applies to every verification step in this plan.
- **Address a tmux server by absolute socket path (`tmux -S`), never by name (`tmux -L`), unless the name is unmistakably yours.** tmux reads an empty *or missing* `TMUX_TMPDIR` as `/tmp`, so `-L atrium` in the wrong context is the live fleet.
- **Never retain or echo a tmux server's argv.** It carries `-e GH_TOKEN=gho_…` and `-e GITHUB_PERSONAL_ACCESS_TOKEN=…`.
- **Tests must stay hermetic.** `testutil.SandboxHomeMain` installs both `HOME` and `TMUX_TMPDIR`; do **not** mint a `TMUX_TMPDIR` of your own in a new Go test — `RequireTmux` hard-fails when the sandbox root is absent, and a second root tests a path production never takes.
- **`internal/testutil/sandbox_contract_test.go` AST-guards `os.Exit` outside `TestMain`** (#586). Don't trip it.
- **Commits:** Conventional Commits, lowercase. **Every exported symbol needs a doc comment** (`revive:exported`). Never name anything `max`, `min` or `len` (`revive:redefines-builtin-id`). **Declare nothing you do not read** — `unused` fails CI on a const added for an assertion you meant to write and didn't, while `build`, `vet` and `fmt-check` all pass.

## File Structure

**Created**

| Path | Responsibility |
|---|---|
| `session/tmux/orphan.go` | `OrphanServer`, `ChildProc`, `ScanServers` — the portable surface and the ownership predicate. |
| `session/tmux/orphan_linux.go` | `/proc` inventory: candidate pids, fd→inode→`/proc/net/unix` socket path, start time, children. |
| `session/tmux/orphan_other.go` | `//go:build !linux` — returns `supported == false`. |
| `session/tmux/orphan_test.go` | Tier 1 (hermetic, no tmux) + tier 2 (real tmux, the end-to-end regression). |
| `internal/doctor/orphans.go` | `CheckOrphans` / `RenderOrphans`. |
| `internal/doctor/orphans_test.go` | `Render*` against hand-built structs, so it is covered on every platform. |
| `cli_reap.go` | `reapCmd` + `runReap(ctx, w io.Writer, …)`. |
| `cli_reap_test.go` | `runReap` against `io.Discard` / a buffer. |

**Modified**

| Path | Change |
|---|---|
| `main.go` | Doctor section after the OOM one (its own `doctor.ProbeTimeout` context, following the `oomCtx` block); `rootCmd.AddCommand(reapCmd)` beside the other headless commands. |
| `README.md` | A `` | `reap` | … | `` row in the `### Usage` table. |
| `readme_commands_test.go` | Add `reap` to `TestHeadlessCommandsTakeNoTUILock` **and fix its docstring**. |

---

## Task 1: The scanner

**Files:** create `session/tmux/orphan.go`, `orphan_linux.go`, `orphan_other.go`, `orphan_test.go`.

**Interfaces:**

```go
type ChildProc struct {
    PID  int
    Comm string // e.g. "claude"
}

type OrphanServer struct {
    PID        int
    Socket     string      // socket name: base of SocketPath, else the argv -L value
    SocketPath string      // true bound path from /proc/net/unix; "" if undetermined
    Reachable  bool        // `tmux -S SocketPath display-message -p '#{pid}'` == PID
    Started    time.Time   // /proc/<pid>/stat field 22 + btime; also the PID-reuse guard
    CWDDeleted bool        // readlink /proc/<pid>/cwd ends in " (deleted)"
    Children   []ChildProc
}

// ScanServers returns Atrium-owned tmux servers other than the live one on the
// ambient socket. supported is false off Linux.
func ScanServers(ctx context.Context) (servers []OrphanServer, supported bool)
```

> **As shipped, this sketch is out of date in three ways** — `session/tmux/orphan.go` is
> the current surface. `OrphanServer` carries `ReachableKnown` beside `Reachable`,
> because "the probe said nothing is there" and "the probe could not run" have opposite
> safety consequences. `Socket` and `SocketPath` never fall back to argv, which carries
> injected `GH_TOKEN`s and, worse, claimed live `tmux: client` attach proxies as reap
> candidates. And `ScanServers` returns a third result, `gaps ScanGaps`, so that an
> inventory which could not read `/proc/net/unix` or could not finish its `/proc` walk
> stops being indistinguishable from a clean host.

Seams mirroring `internal/doctor/oom.go`'s `var (…)` block: package-level `var`s for the process scan and the ambient lookup, so assembly is testable without a live server.

It lives in `session/tmux` because socket naming is tmux-domain knowledge (`socketName()` → `config.RuntimeName()`); `internal/doctor` and `main` both import it with no cycle.

**Identification, cheapest filter first:**

1. `/proc/<pid>/comm` contains `tmux` — a **prefilter that fails open**, not a gate. `tmux: server` is an artifact of the 16-byte `prctl` name and a 4-char progname; treat a near-miss as a candidate and let step 3 decide.
2. `/proc/<pid>` owned by `os.Getuid()`. Other users' servers are unkillable anyway, and listing them is the privacy concern #445 raised.
3. **Socket name from the socket path**, `filepath.Base(socketPath)`. Accept `atrium`, `claudesquad`, or either plus a `-` suffix — **both brands unconditionally**, the way `clean.sh` already does. `RuntimeName()` resolves from the *reaper's own* HOME, so keying on it alone would make a legacy install blind to `atrium` orphans and vice versa. Fall back to argv (`-L <name>` / `-S <path>`) **only** when the path could not be read, which keeps the common path from touching argv at all.
4. Exclude the pid that `tmux -L <RuntimeName()> display-message -p '#{pid}'` reports under the ambient environment. When it fails there is nothing to exclude — the no-server case, not an error.

- [x] **Step 1: Tier-1 test — a bound socket path survives unlink**

The mechanism the whole design rests on. Hermetic, no tmux, runs on every Linux CI job.

```go
// A tmux server whose TMUX_TMPDIR root was removed still has a bound socket, and the
// kernel still knows its path — that is the only reason an unreachable orphan can be
// identified at all. Asserted with a plain unix listener so it holds with no tmux.
func TestSocketPathSurvivesUnlink(t *testing.T) {
    dir := t.TempDir()
    path := filepath.Join(dir, "probe.sock")
    ln, err := net.Listen("unix", path)
    // … t.Cleanup(ln.Close); os.Remove(path)
    // assert socketPathOf(os.Getpid()) == path
}
```

Verified by hand 2026-08-04: the `/proc/net/unix` row keeps the exact original path after `os.Remove`, with Flags `00010000` (`SO_ACCEPTCON`).

- [x] **Step 2: Tier-1 test — the start-time parser survives a comm with a space**

```go
// /proc/<pid>/stat's comm field is "(tmux: server)". Its embedded space shifts every
// later field, so the naive strings.Fields(raw)[21] returns 0 — which formats to the
// machine's BOOT time, a plausible-looking wrong answer rather than an obvious
// failure. Parse after the LAST ") ".
func TestStartTimeParsesACommContainingASpace(t *testing.T) { … }
```

Assert the parser agrees with a known value for `os.Getpid()`, **and** feed it a synthetic stat line whose comm contains a space and a `)`. Demonstrated on a live tmux server 2026-08-04: naive → boot time, correct → matches `ps -o lstart` to the second.

- [x] **Step 3: Implement `orphan_linux.go`**

Socket path: collect `socket:[N]` fd inodes from `/proc/<pid>/fd`, read `/proc/net/unix` **once**, take the row whose inode matches and whose Flags are `00010000`.

Children: one pass over `/proc/*/status` reading `PPid:` — which parses cleanly, unlike `/proc/<pid>/stat` — collecting `comm` and pid per child.

- [x] **Step 4: Reachability is an identity test, not a file test**

`os.Stat(SocketPath)` answers "is there a file here now", which is the wrong question: a restarted Atrium re-binds `/tmp/tmux-1000/atrium`, so a stale orphan carrying that path would stat true, be classified reachable, and doctor would print `tmux -L atrium kill-server` as the remedy — **aimed at the new, live server.** Run `tmux -S <SocketPath> display-message -p '#{pid}'` and require the answer to equal the candidate pid. Verified 2026-08-04: returns the pid for the live path, `no server running on …` for a dead one.

- [x] **Step 5: `orphan_other.go` and `just ci`**

---

## Task 2: The doctor report

**Files:** create `internal/doctor/orphans.go`, `orphans_test.go`; modify `main.go`.

**Interfaces:** `CheckOrphans(ctx) OrphanResult` / `RenderOrphans(OrphanResult) string`, wired into `doctorCmd.RunE` after the OOM section with its own `doctor.ProbeTimeout` context.

- [x] **Step 1: Render against hand-built structs**

`internal/doctor` already tests platform primitives against the test process's own `/proc` and `Render*` against hand-built structs (`oom_test.go`) — do both, so `RenderOrphans` is covered on every platform.

- [x] **Step 2: "checked and clean" must not look like "nothing to say"**

Print `none` rather than rendering empty, per the principle stated in `RenderGates`'s doc comment (`internal/doctor/render.go`): *"the gate was checked and matches" and "the check silently found nothing to say" must not look identical.* Off Linux, render an unavailable/Linux-only note, as the OOM section already does.

- [x] **Step 3: Heading**

**"Orphaned tmux servers:"**, never bare "orphans" — doctor already uses that word for a Claude login the account list no longer names (`ui/overlay/accounts.go:309`).

- [x] **Step 4: Also report class (a)**

Socket files in the ambient socket dir matching either brand's prefix that no live server owns — free from data already gathered. This is the home for the `atrium-barstyle-test*` sockets left by ad-hoc #394 verification runs (not `session/tmux/barstyle_test.go`, which starts no server); unlike the old `atrium-precheck` their names were unique, so they accumulate without bound. **Report them; never delete them.** Print the `find … -delete` line for the user to run.

- [x] **Step 5: Confirm the report never prints argv**

Run with a `GH_TOKEN`-injected session live and grep the output for `gho_`. This is a test *and* a manual check.

---

## Task 3: `atrium reap`

**Files:** create `cli_reap.go`, `cli_reap_test.go`; modify `main.go`, `README.md`, `readme_commands_test.go`.

Follows the headless-CLI convention (`cli_ls.go`, `cli_peek.go`, `cli_send.go`): a `reapCmd` with `Use`/`Short`/`Long` and a thin `RunE` delegating to `runReap(ctx, w io.Writer, …)`, unit-testable against `io.Discard`.

```
atrium reap                 # list; same data as the doctor section; exit 0
atrium reap --kill          # unreachable orphans only, confirming each
atrium reap --kill --all    # include reachable ones (class b)
atrium reap --kill --yes    # no prompt, for scripts
```

- [x] **Step 1: No TUI lock, with a comment saying why**

An orphan is unrelated to the live server, so requiring a closed TUI would make the command useless in the situation it exists for.

- [x] **Step 2: Unreachable-only by default**

This is the answer to "how do you avoid killing a legitimate second Atrium": a smoke run in flight, or a second Atrium under its own `TMUX_TMPDIR`, answers its own socket and is class (b) — reported with the exact command, never killed unless `--all`. (Another *user's* server never gets this far; the uid filter drops it.) With reachability an identity test rather than `os.Stat`, this default is sound.

- [x] **Step 3: SIGTERM, then SIGKILL after a bounded wait, then verify**

Poll `syscall.Kill(pid, 0)` / `ESRCH` — the technique `session/tmux/pty_reap_unix_test.go` uses. SIGTERM first so tmux exits cleanly; do **not** claim it is what reaps the children (it is not — the pty hangup is). **Then verify:** re-check every captured child pid and signal survivors directly, applying the start-time guard to each child, whose pid is staler than the server's. Verifying rather than trusting the mechanism is the difference between reaping the orphan and quietly halving it.

- [x] **Step 4: PID-reuse guard**

Re-read `/proc/<pid>/stat` start time immediately before signalling and compare to the value captured at scan time; skip with a message on mismatch. **A confirmation must carry the target it armed with** — the #502 lesson.

- [x] **Step 5: The prompt names what dies**

#267's precedent: `pid 2989219  16h32m  holds a live 'claude'  — kill? [y/N]`. Never automatic, never from the TUI, never from `atr reset`.

- [x] **Step 6: It deletes nothing**

No `os.Remove` of socket files, no `RemoveAll` of roots. A killed server's socket file is inert; leaving it costs a zero-byte inode, and the doctor's class-(a) list already names it. Revision 2 had `reap --kill` unlink verified-stale sockets; that is dropped entirely.

- [x] **Step 7: The three drift guards a new subcommand trips**

All in `readme_commands_test.go`:

- `TestReadmeDocumentsEveryCommand` (`:46`) needs a literal `` | `reap` | … | `` row in README's `### Usage` table — prose does not satisfy `hasCommandRow` (`:120`).
- `TestEveryCommandHasAShortDescription` (`:86`) needs a non-empty `Short`.
- `TestHeadlessCommandsTakeNoTUILock` (`:96`) must gain `reap` — **including its docstring**, which says "Only the bare atrium and reset may take `tui.lock`" and becomes false the moment a third lock-free command exists.

---

## Task 4: The end-to-end regression test

**Files:** modify `session/tmux/orphan_test.go`. Linux + `testutil.RequireTmux`.

This is the test the issue asks for: reproduce the incident, then prove the reaper reaches it.

- [x] **Step 1: Build the orphan**

1. `os.MkdirTemp("/tmp", "atr")` — a short root, per the `sun_path` budget reasoning in `config_parse_test.go` and `internal/testutil`'s `installSandboxTmuxTmpdir`.
2. Socket `atrium-reaptest-<rand>` — matches the *production* predicate, so the test exercises the real classifier rather than an injected one.
3. Start `tmux -L <sock> new-session -d "sleep 60"` with `TMUX_TMPDIR` on `cmd.Env` (not `os.Setenv` — that would disturb the sandbox the rest of the binary runs in). **Arm teardown before starting**; once the pid is known, register a `t.Cleanup` that SIGKILLs it. The test must never be the thing that leaks.

- [x] **Step 2: The negative control**

`os.RemoveAll(tmuxTmp)`, then assert `tmux -L <sock> list-sessions` **fails** — proving existing tooling cannot reach it. This must run with an explicitly isolated, *existing* `TMUX_TMPDIR` (or `-S`), or the fallback-to-`/tmp` behaviour makes the negative control lie: with an empty or missing one, `-L` would answer from the developer's live fleet and the assertion would pass for the wrong reason.

- [x] **Step 3: The assertion**

`ScanServers` returns a row with that pid, `Reachable == false`, `Socket == <sock>`, `SocketPath` equal to the now-deleted path, and a `Started` matching `ps`.

- [x] **Step 4: Reap and confirm**

Assert `syscall.Kill(pid, 0)` → `ESRCH`, and that the `sleep` child is gone.

The scan will also see the developer's live server; the test asserts about, and kills, **only its own pid**.

---

## Verification

- `just ci` on each task, and `just test-race` — the package-level seams are shared state.
- End-to-end, reproducing the incident by hand: a server under a temp `TMUX_TMPDIR`, `rm -rf` the root, confirm `tmux -L … kill-server` cannot reach it (from a shell with an *existing* isolated `TMUX_TMPDIR`), then `atrium doctor` lists it unreachable and `atrium reap --kill` removes it and its child.
- Confirm the report never prints argv: run it with a `GH_TOKEN`-injected session live and grep for `gho_`.
- Confirm `atrium doctor` on a clean machine prints `none`.
- Record the live-server count before and after any manual run: `ps -eo comm --no-headers | grep -cx 'tmux: server'`, and the session count via `tmux -S /tmp/tmux-$(id -u)/atrium list-sessions`. A verification step that changes either is a bug in the verification.
- `gh pr checks <n>` before calling anything merge-ready. Note `gh pr checks` has no `--json`, so a poll loop that assumes one times out silently.
