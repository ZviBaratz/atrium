# Orphaned tmux servers no cleanup path can reach (#547)

Status: PR 1 (prevention) merged 2026-08-04 as `2e8b4da` (#589). PR 2 (the reaper)
implemented — `session/tmux/orphan*.go`, `internal/doctor/orphans.go`, `cli_reap.go`.
Two design changes fell out of running the scan against a real host; both are in the
falsified-claims table below.

This is revision 4. Revisions 1–3 are not reproduced; what they got wrong is, because
each wrong answer here was expensive and two of them would have destroyed live work.

## Problem

A throwaway-`HOME` Atrium run left a live tmux server plus a real `claude` at 318 MB
running for 16.5 hours. Its socket lived under a `TMUX_TMPDIR` temp root that was later
deleted, so the socket inode went with the root, and `tmux -L atrium kill-server` could
never name it. Only `kill <pid>`, from a PID found by process scan, reached it.
`clean.sh:4`, `clean_hard.sh:4` and `atr reset` all resolve the socket by name under the
ambient `TMUX_TMPDIR`, so none of them can.

The sharp edge, stated plainly: **the socket isolation #331 recommended converts a
leaked server from noisy-but-fixable into silent-and-unreachable.**

## Three things leak here, not one

The issue body treats them as one problem. They have different severities and different
fixes, and conflating them is what produced two dangerous designs.

| | Symptom | Severity | Fix |
|---|---|---|---|
| **(a)** | Stale socket **file**; server was correctly killed | Cosmetic — clutters the directory holding the live socket | Unique probe socket name, then unlink after `kill-server` — **done, PR 1** |
| **(b)** | Live server orphaned, socket **reachable** | Recoverable — existing tooling works | Report it and print the exact command; never auto-kill — PR 2 |
| **(c)** | Live server orphaned, socket **unreachable** | The real bug — invisible and immortal, pins a live agent | Reap by process inventory — PR 2 |

## The two incidents that constrain the design

### The sweep that wiped the live fleet

#584 added `sweepStaleTmuxRoots`, a startup reaper for roots a crashed run stranded.
While verifying that its prefix guard was load-bearing, the guard was mutated **in place**
and the test binary run; `TestMain` executes the sweep against the real `/tmp`, the glob
became `/tmp/*`, and `os.RemoveAll` took `/tmp/tmux-<uid>` with the live `atrium` socket
in it. Thirteen running sessions dropped at once. #586 removed the sweep, fixed the leak
at its source (an `os.Exit` in a test body skipping every `TestMain` defer), and added an
AST guard against `os.Exit` outside `TestMain`.

Two constraints fall out, and they are the hard edges of PR 2:

- **A recursive delete over a glob is a standing hazard whose worst case is unbounded;
  the orphan it reaps is rare and recoverable.** That trade never justifies itself.
- **Absence of an ownership marker must never mean "safe to delete."** That default is
  what turned a one-expression change into a catastrophe.

A third, about method: **never mutate a destructive guard in place to prove it is
load-bearing.** Extract the predicate and assert on it as a pure function, or — better,
where the guard is a whole script — run the *previous committed version* as the control.
PR 1 did the latter: it ran the pre-PR `run.sh` under `SIGINT` and watched it strand a
server, which is the same evidence with none of the blast radius.

### The fallback that would have destroyed the fleet from inside the fix

**`TMUX_TMPDIR` does not isolate tmux when the directory is empty or missing — it
silently falls back to `/tmp`.** Measured read-only against the live server:

```
TMUX_TMPDIR=                 tmux -L atrium list-sessions  → 18 live sessions   (fell back to /tmp)
TMUX_TMPDIR=/nonexistent-xyz tmux -L atrium list-sessions  → 18 live sessions   (fell back to /tmp)
TMUX_TMPDIR=/var/tmp         tmux -L atrium list-sessions  → error connecting … (isolated)
```

Revision 1 prescribed `TMUX_TMPDIR=<dir> tmux -L atrium kill-server` inside a cleanup
trap and explicitly allowed the variable to be unset. Both of those states fall back to
`/tmp`, so the prescribed fix would have destroyed every session in the developer's live
fleet — the defect the plan exists to prevent, introduced by the plan.

**Rule for every change here: address a tmux server by absolute socket path (`tmux -S`),
never by name (`tmux -L`), unless the name is unmistakably yours.** `tmux -S <path>`
cannot resolve anywhere but the path given, so it is *structurally* incapable of reaching
the live fleet — strictly better than a name plus a guard, where the guard is the only
thing standing between a cleanup and 18 live sessions. This is now CLAUDE.md's stated
rule and `internal/testutil` already works this way.

## What the tree actually says

Verified on this host — Linux 7.0.0-28, tmux 3.6 — on 2026-08-03 and re-confirmed
2026-08-04. Each of these changed a decision.

- **A bound unix socket keeps its path in `/proc/net/unix` after the file is unlinked.**
  Confirmed by binding a listener, `os.Remove`ing the path, and reading the row back: the
  path matches the original exactly, with Flags `00010000` (`SO_ACCEPTCON`). This is what
  makes class (c) solvable at all — no reconstruction of tmux's
  `$TMUX_TMPDIR/tmux-<uid>/<name>` layout is needed, which is the thing #331 warned
  against.
- **`tmux -S <path> display-message -p '#{pid}'` answers "who owns this socket".**
  Returns the pid for the live path, `no server running on …` for a dead one. This is the
  identity test the classifier needs.
- **Process start time comes from `/proc/<pid>/stat` field 22 plus `/proc/stat` btime,
  parsed after the *last* `") "`.** The comm field is `(tmux: server)` and its embedded
  space shifts every subsequent field. Demonstrated on a live tmux server: the naive
  `split()[21]` returns `0`, which formats to *boot time* — a plausible-looking wrong
  answer, not an obvious failure — while parsing after the last `") "` matches
  `ps -o lstart` to the second.
- **The server argv carries injected secrets** (`-e GH_TOKEN=gho_…`,
  `-e GITHUB_PERSONAL_ACCESS_TOKEN=…`). Never retain or echo argv. Taking the socket name
  from the socket *path* keeps the common path from reading argv at all.

## Claims that were falsified

Kept because each was believed, acted on, and wrong — and because the review that caught
them is the reason this design is not the one that shipped.

| Claim | Verdict |
|---|---|
| `/proc/<pid>` dir mtime == process start time | **False.** 13 of 200 pids off by >2 s, worst 481 s; three pids with different starts share one mtime. It stamps proc-inode instantiation, not `task->start_time`. The five-sample check that "confirmed" it hit only processes whose dentries happened to be created at fork. |
| `run.sh`/`render.sh` orphan a server on SIGINT via a trap race | **False.** The outer shell blocks in `wait` on the foreground subshell, so the subshell's `EXIT` trap completes before the outer trap's `rm -rf`. `set -e` cannot reorder it. |
| `KEEP=1` leaves a 24-hour fake agent alive | **False.** The subshell trap is on `EXIT` and fires on the success path too. `KEEP` gates only the `rm -rf`. |
| The precheck / `cfgparse` probes are a "textbook class (c) orphan" | **Overstated.** Both run `new-session -d 'sleep 60'`; when the sleep exits the session dies and `exit-empty` retires the server. Any orphan is bounded at ≤60 s and holds nothing. The durable artifact is the socket *file* — class (a). |
| `comm == "tmux: server"` is a stable identity | **Fragile.** tmux's Linux `setproctitle` shim builds `"<progname>: server (<socket>)"`, truncates to 16 bytes, then trims at the last space — `"tmux: server"` falls out only because `getprogname()` is 4 chars. Keep it as a cheap prefilter that **fails open**, never as the gate. |
| SIGTERM matters because "tmux tears its panes down on it" | **Unsupported.** `server_destroy_pane()` only `close()`s the pty master; there is no `kill()` of the child. Children die from the kernel's pty hangup, which a SIGKILL of the server produces identically. Keep SIGTERM-first for a clean exit and hooks — but the verify-then-signal-survivors step is what carries the correctness. **Since measured further:** a server with a live session was still running five seconds after SIGTERM on the tmux 3.2 CI job, while the same test passed on 3.6. SIGTERM is not reliably fatal across the supported range, so the SIGKILL rung is the one that lands. |
| The argv `-L <name>` fallback names a server whose socket path could not be read | **False in practice, and dangerous.** Measured on the development host: the scan returned 15 candidates and 14 were `tmux: client` attach proxies for *live* sessions. A client passes the comm prefilter and has a socket fd, but that fd is a connected endpoint rather than a listening one — so it has no bound path, which is exactly the condition the fallback triggers on, and its argv carries `-L atrium`. Under the reachability model this document originally specified (one `Reachable bool`) all 14 would have been `Reachable == false` and therefore in the default `reap --kill` set. Dropped entirely: a process that is not listening is not a server. That also removes argv reading from the codebase, so the secret-hygiene rule is structural rather than a discipline. |
| One `Reachable bool` is enough | **Unsafe.** Reachability is computed by running tmux, so "the probe found nothing" and "the probe could not run" are different facts — and with tmux off `PATH` the *ambient* lookup that excludes the live server fails too, so every live session's server arrives looking like an unreachable orphan. Split into `Reachable` + `ReachableKnown`; only `ReachableKnown && !Reachable` is a kill candidate, with or without `--all`. |
| `ChildProc` needs only `PID` and `Comm` | **Incomplete.** The plan requires the PID-reuse guard to be applied per child, and there was nothing captured to compare against. `Started` added. |
| `#{socket_path}` needs a reconstructed-path fallback | **Obsolete.** #587 set `tmux.MinVersion = "3.2"` and enforces it before any session starts; `#{socket_path}` is 2.2+, so it is present wherever this code can run. |

## PR 1 — prevention (merged, `2e8b4da`)

Three edits to `validateConfig`, in an order where each is unsafe without its predecessor:

1. **Unique probe socket name** — `fmt.Sprintf("%s-precheck-%d-%d", socketName(),
   os.Getpid(), counter)` with a package-level `atomic.Uint64`. PID alone is not enough:
   `Init` is documented safe off the update thread and fires on every live
   `session_context_bar` / `tmux_config_override` / theme change, so two `Init`s in one
   process are reachable.
2. **Then** the teardown defer may move above `start.Run()`. Before (1), a failed start
   would `kill-server` a *concurrent* `Init`'s live probe.
3. **Then** the unlink is safe, using `#{socket_path}`. Before (1), A's unlink would
   remove the path C had just re-bound, and `filepath.Base(path) == sock` would not catch
   it — the path is *correct*, it just no longer belongs to the caller.

The fixed name was not merely untidy: concurrent `Init`s shared one probe server, the
first teardown killed it under the others, their `source-file` failed `no server
running`, and `Init` records that as a parse error — **silently launching every later
session with no custom titles, mouse, clipboard or status bar.**
`session/tmux/barstyle_test.go` had been driving eight concurrent `Init`s into that
collision all along and could not see it, because it discards the errors on purpose.

### The near-miss inside PR 1, and the rule it produced

The first version justified the unconditional unlink on "tmux's default `exit-empty`".
The probe started with **no `-f`**, so tmux sourced the user's `~/.tmux.conf` into it —
and `set -g exit-empty off` there is inherited. Measured, tmux 3.6:

| probe start | `show -gv exit-empty` |
|---|---|
| no `-f` | **`off`**, with `set -g exit-empty off` in `~/.tmux.conf` |
| `-f /dev/null` | `on` |

For such a user, a `kill-server` that failed to land would leave a live server whose
socket had just been unlinked — **exactly the orphan this document is about, introduced
by the fix for it**, and the thing `internal/testutil`'s teardown explicitly refuses to
create. The probe now starts with `-f os.DevNull`, which pins the default back and is
closer to production besides, since a real session always gets its own `-f`.

> **Rule:** a justification that rests on a *default* must name what can take the default
> away. "tmux's default" is not a property of the running server; it is a property of the
> server *you started the way you started it*.

## Deliberately not doing

- **No automatic killing, ever** — not from the TUI, not from `atr reset`, not from
  `doctor`. These servers hold real agents with unpushed work (#267).
- **No sweep, and no recursive delete over a glob, anywhere.** Historical sockets get
  *reported*, with the command printed for the user to run. Atrium does not run it.
- **No `clean.sh` / `clean_hard.sh` process scan.** `clean_hard.sh` may gain one line
  invoking the built binary when it exists, and nothing more.
- **No macOS/Windows support.** The section renders "unavailable", as OOM already does.
  All incident evidence is Linux, and a `ps` fallback could supply neither the socket path
  nor the cwd-deleted signal.
- **No process-library dependency.**
- **No change to real session creation** (`session/tmux/tmux.go:479`).
- **`atr reset` / `CleanupSessions` is out of scope.** It leaves the server alive but
  under the ambient environment on the ambient socket, so the survivor stays
  name-reachable — class (b) at worst, and usually nothing, since `exit-empty` retires a
  server whose last session dies.
- **Not #445** (global OOM ranking), though the orphan section sits beside it.

## Related

- **#331** — socket *file* leak from the managed-config probe. Same isolation mechanism,
  opposite failure: that one kills the server and leaks the file; this one keeps the
  server and loses the file.
- **#362** — `PtyFactory.Start` discarding the `*exec.Cmd` and leaking zombie tmux clients.
- **#581 / #584** — real-tmux tests ran on the developer's live socket. `SandboxHomeMain`
  now installs `TMUX_TMPDIR` as well as `HOME`, `RequireTmux` hard-fails when the sandbox
  root is absent, and teardown reaps by absolute socket path.
- **#582 / #587** — the tmux version floor. It is **3.2**, not the 3.1 revision 1 claimed:
  `new-session -e` landed in 3.2, settled from tmux's own sources
  (`cmd-new-session.c:42`'s option string gains `e:` only at 3.2) rather than from
  changelog prose, which is the stronger evidence tier and the standard to hold here.
- **#590** — `just smoke` / `just gifs` hang from a terminal, because the headless seed
  run never exits when it has a controlling tty. Found while verifying PR 1; pre-existing,
  and the reason PR 1's signal test delivers `SIGINT` by `killpg` rather than through a
  pty's line discipline.
- **#445** — global OOM ranking. The orphan report sits beside it in `doctor`.
