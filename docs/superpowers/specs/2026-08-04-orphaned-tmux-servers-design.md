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
| `syscall.Kill(pid, 0)` / `ESRCH` is a liveness test | **Insufficient.** It succeeds for a *zombie* — a process that has exited and is waiting only for its parent to collect it. Normally that window is microseconds, because init reaps orphans at once; on the tmux 3.2 CI job, where the orphaned server was re-parented to a container init that never `wait()`s, it lasted the reaper's entire SIGTERM-then-SIGKILL budget and `reap` reported that the server had *survived SIGKILL*. Nothing survives SIGKILL. Liveness is now signal 0 **plus** `/proc/<pid>/stat`'s state not being `Z`. |
| `#{socket_path}` needs a reconstructed-path fallback | **Obsolete.** #587 set `tmux.MinVersion = "3.2"` and enforces it before any session starts; `#{socket_path}` is 2.2+, so it is present wherever this code can run. |
| `os.TempDir()` names the directory tmux puts its sockets in | **False, and platform-specific.** `os.TempDir()` honours `$TMPDIR`; tmux does not — for an empty or missing `TMUX_TMPDIR` it hardcodes `/tmp` and consults no other variable. On any host with `TMPDIR` set — every macOS one, where it is a per-user `/var/folders/…` path — `socketDir` named a directory tmux never binds in, so the stale-socket section would confidently report "none in \<wrong dir\>": a clean bill of health for a directory that was never the right one. Now a `tmuxDefaultTmpdir = "/tmp"` const. `internal/testutil` already documented this fallback and it was still got wrong, which is the argument for the const over a second inline `os.TempDir()`. |
| The scan inherits a bounded context from Cobra | **False.** Cobra's command context has no deadline, so `atrium reap` ran the probe loop — one `tmux -S <path> display-message` per candidate — with no budget at all, and a single wedged server could hang the command indefinitely. The doctor section had a budget only because `RunChecks` applied one. `reap` now derives its own `context.WithTimeout(ctx, doctor.ProbeTimeout)`, the same 10 s the report gets and for the same reason. |
| The PID-reuse guard is a precondition, so checking it once before the first signal is enough | **False — it is an invariant of the whole ladder.** Up to seven seconds elapse between SIGTERM and SIGKILL (`reapTermGrace` + `reapKillGrace`), and a pid freed inside that window can be recycled onto an unrelated process, which then answers the liveness poll and takes the SIGKILL. Identity is now re-checked on every poll (`scannedProcessGone`), so escalation can only ever land on the process the scan recorded. `terminate` took a `scanned time.Time` to do it. |
| `ambientServerPID`'s `found == false` "is the empty-fleet case rather than an error: there is then simply nothing to exclude" | **False — it was both cases at once**, and the doc comment asserting otherwise was the bug's cover. `tmux ran and reported no server` and `tmux could not be asked` both returned false, and they have opposite consequences: an empty fleet has no live server to protect, while an unanswered probe means the live server is still in the candidate list. It then answers its own socket, so it arrives **`Reachable`** — excluded from the default target set, but `--all` targets reachable servers by design, and the report prints a verified `tmux -S <path> kill-server` for one. So the same conflation `ReachableKnown` was introduced to prevent had survived, one function away, aimed at the live fleet. Fixed by giving the ambient probe the same three-state answer `probeSocketOwner` already had — extracted into one shared `classifyPIDProbe`, since two copies of a rule about what counts as evidence will drift — plus `ScanGaps.LiveServerUnknown`, which withholds the remedy line and refuses an `--all` whose selected targets include a reachable row. Deliberately **not** counted by `Any()`, and deliberately keyed on the targets rather than on the flag: the previous regression here came from refusing too eagerly, and an `--all` with nothing reachable to select picks exactly what the default picks. |
| The ambient probe's determined answer — `pid 0, known true` — means "this host has no live Atrium server", which #599 called safe "because a fleet with no live server has nothing that needs excluding" | **False, and it is the wrong-place error rather than a failed read (#603).** The probe addresses `-L socketName()` (`tmuxCommand`), a socket resolved from the *reap process's own* `HOME` against its own `TMUX_TMPDIR`, with its own `-f <conf>` — so a non-zero exit establishes only "nothing answered where I looked". Three routes make that somewhere else: a `TMUX_TMPDIR` mismatch (or a deleted root, which tmux resolves against `/tmp`), the other brand (the *inventory* keys on both `orphanBrands` precisely because an orphan comes from another `HOME`; the exclusion probe keyed on one), and a managed config that fails to parse. In all three the exclusion `c.PID == live` runs with `live == 0` and excludes nothing, the live server answers its own socket by absolute path and arrives `Reachable`, and `LiveServerUnknown` stays **false** — so `--kill --all --yes` took the fleet with no guard fired and no gap reported, and the report printed a `kill-server` naming it. Fixed by *verifying* the claim instead of asserting it: `ScanServers` sets `EmptyFleetUnproven` when a determined-empty answer coexists with a reachable server on a socket name some Atrium run addresses as its own — a *bare* brand, in either brand: `OnAnAmbientSocket`, narrower than `ownsSocketName`, because no Atrium addresses its own server by a suffixed name, so a leaked `-precheck-` row cannot be the server the probe missed and must not withhold `--all` from the rows that need reaping. Both causes now share one `LiveServerUnidentified()` predicate — the "may I act?" question, kept apart from `Any()`'s "is the inventory complete?". Counted by neither, for the reason the row above this one gives. |
| Fixing `ambientServerPID` fixed the conflation | **False — it fixed one of two call sites.** `internal/doctor/oom.go`'s `discoverTmuxOOM` ran its own copy of the same `display-message -p '#{pid}'` probe and returned a single `ok`, so the fix above never reached it. Reproduced on the development host: with tmux off `PATH`, `atrium doctor` printed "no live atrium tmux server — start a session to see the live ranking" *two lines above* an orphan section naming the very server it denied — pid 1952486, holding 18 processes. Worse than a wrong label, because that sentence is an instruction handed to a user whose fleet is running; and `exec.Command` fails without an `ExitError` precisely under memory pressure (`ENOMEM`/`EAGAIN` on fork), so the section whose purpose is "would an OOM kill shed one session or every session" went blind exactly when the host is in the state it exists to diagnose, dropping its verdict in favour of a claim of an empty fleet. Fixed by exporting `tmux.AmbientServerPID` and deleting the second copy — `classifyPIDProbe`'s doc comment already argued the rule must not be able to drift between two call sites, and a second *caller* is how it drifted — plus `OOMResult.LiveServerUnknown`, named for the same fact as `ScanGaps.LiveServerUnknown`. |
| `OOMResult`: "ServerFound is false when no server runs on the socket" | **False.** It was also false when tmux could not be run at all, which is the defect above; the doc comment was again the cover for it. Now stated as a pair: only `LiveServerUnknown == false` makes `!ServerFound` evidence of an empty fleet. |
| `ScanGaps.Any()`'s exclusions are adequately held by its comment and its test | **Fragile rather than false.** The exclusions were correct, but a predicate named for *any* while reporting on a subset of its fields put its own scope in prose, so classifying the next field added to the struct required reading and agreeing with a nine-line argument. #607 then added that next field — `EmptyFleetUnproven` — and the paragraph grew to cover two exclusions out of four fields, which is the drift the rename removes. Renamed `IncompleteInventory()`: scoped to the inventory, the exclusions follow from the question. #607 also supplied the second predicate this PR had argued was unnecessary — `LiveServerUnidentified()` — so the claim "there is deliberately no second predicate for 'may a kill proceed'" was **false by the time it rebased**, and is now replaced by a statement of how the two divide the work. |
| The gate label "unknown (no resolved value on disk)" describes what was read | **False on two of the five paths to `GateUnknown`.** A relative or empty config dir is refused before any file is opened, and a malformed `.claude.json` fails at the parse with the value quite possibly sitting on disk. The label reported a failed *read* as a determined *absence* — the conflation `RenderGates`' own doc comment forbids, in the label of the section that states the rule — and it contradicted `GateUnknown`'s own doc comment one file over, which already said "no comparable value could be read". The label now says that. |
| No test made any of these sources fail | **True, and the reason every instance shipped.** Build, vet, lint and the suite were green every time; each was found by running the real binary or by review, and each fix then invented a bespoke seam after the fact. `internal/doctor/evidence_test.go` is the standing version: a fault-injection row per source, plus a reflection check over the audited types that fails on a completeness flag no row exercises. It earned that immediately, demanding a row for `OOMResult.LiveServerUnknown` rather than letting the new flag ship unasserted. |
| The reflection check "is what lets this file see a source which does not exist yet" | **Overstated, and in the file whose subject is overstated claims.** `reflect` enumerates a type's *fields*; it cannot enumerate a package's *types*. So a flag added to one of the eight listed types was caught, and a flag on a brand-new type — which is how every source in this table actually arrived — was invisible to both halves of the file. `CapacityResult.RAMKnown` was already sitting outside the audited set, unremarked, when the claim was written. Closed by `TestNoEvidenceFlagEscapesTheAuditedSet`, which parses both packages' sources and requires every flag-word field to be on an audited type or carry a reason in `evidenceOutOfScope`. Proved by control: a new type with a completeness flag, added to `internal/doctor` on a throwaway copy, now fails the suite. Parsing file-by-file rather than by package is deliberate — no build constraint is consulted, so a `_linux.go` or `_darwin.go` declaration is seen on every platform. |
| The harness "sees a source which does not exist yet" — verified by control, so settled | **False within four days, and the next source proved it.** #607 added `ScanGaps.EmptyFleetUnproven`, a completeness flag by any reading of its own doc comment, and every check in `evidence_test.go` stayed green through the rebase: the reflection walk, the source scan and the flag count all begin by asking whether a field *name* matches `evidenceFlagWords`, and "Unproven" was not on that list. The one limitation the file disclosed in prose — "a new flag that follows none of these conventions escapes" — was not a residual risk but the very next thing that happened, and the mutation battery could not have found it, because every mutant was named on-convention by the person who already knew the convention. A naming rule cannot be enforced by a check that starts from the name. Fixed twice over: "Unproven" added, and `TestAuditedTypesHaveNotGrownAField` pins a field count per audited type, so a new field fails under any name and the author must classify it. Proved by reproducing the escape — word removed, row's claim dropped — and confirming the count catches it anyway. |
| A `covers:` claim means the flag it names is asserted | **False for one row, and it certified the hole.** The row covering `PressureResult.RAMKnown` set *both* RAM seams unread, so `availRAMValue` returned on its first branch and the `!RAMKnown` branch below it was never reached. Deleting that branch outright left the whole `internal/doctor` package green. Worse than an ordinary gap, because the exhaustiveness check counted the flag as covered — so the file marked as audited a consumer nothing exercised, which is the defect the file's own comment calls "the same class one level up". Same shape in the zram row: its assertion rested on `ZramBytes == 0` rather than on `ZramKnown`, so dropping the flag from the guard changed nothing. Both rows now hand the seam a **non-zero value with `ok == false`** — a reading the source could not vouch for. Production's readers all zero on failure, which is exactly why a harness built from zeroes cannot tell a consumer that branches on the flag from one that leans on the producer's zeroing. |
| The `/proc/net/unix` rows duplicating a server's path belong to its **clients**, so without the `SO_ACCEPTCON` filter "a client's pid resolves to the server's socket and is reported as owning it" (`listeningFlags`' own comment) | **False about which side, though the filter it justifies is right to keep.** Measured on the development host: `/tmp/tmux-1000/atrium` had 13 rows, one listening and twelve not, and every one of the 13 inodes resolved through `/proc/<pid>/fd` to the **server** pid — none of the twelve `tmux: client` processes held a path-carrying row at all. A stream client's socket is never bound (`connect(2)` names the peer), while the socket `accept()` returns inherits the listener's address. So an unfiltered table would have credited the server with its own path a second time, harmlessly, and never a client with anything. Consistent with the argv row above, which found a client has *no bound path* — this row is the other half of that sentence, and the earlier comment stated it backwards. The filter stays, with a larger job: it is now what separates the listener from the accepted connections, so both `SocketPath` and `ConnectedClients` come out of one table. Pinned by `TestPathBoundSocketsSeparatesAListenerFromWhatItAccepted`, whose N+1-not-2N+1 assertion fails if the claim is ever restored. |
| Nothing in the inventory can tell a live fleet whose socket file was deleted from a class-(c) orphan, so `reap --kill`'s per-server confirmation is the only protection there can be (#614's premise) | **False for an attached client, true for everything else — and that is why the fix is narrow.** Both are alive, `/proc` says so identically, both hold agent children, both answer nothing when probed by absolute path, and both may sit at the path this run's tmux would bind, so neither the classification, nor `OnAnAmbientSocket`, nor the socket path discriminates. A *pane* child does not either: its `TMUX=` environ proves "has live panes", which an orphan has too (and environ carries injected `GH_TOKEN` values, so the scan reads it no more than it reads argv). An **attached client** does, and it is countable without either: the row above gives the mechanism, and 12 accepted sockets against `tmux list-clients`' 12 attached clients is the calibration. The asymmetry that makes it safe to act on: once the socket file is unlinked `connect(2)` has no path, so nothing can *become* a client of an unreachable server — a count above zero is therefore a connection that predates the unlink, while a real orphan, whose clients died with the run that made them, counts zero (asserted against a real tmux server, and against a real pty-attached one for the converse). Acted on only where there is nobody to tell: `--yes` leaves such a row alone and exits non-zero; the default target set is untouched, because an exclusion there would refuse the case the command exists for. **Still unfalsified, and left standing:** a fleet with its TUI closed has no client on it and is genuinely indistinguishable from an orphan — the child list is all there is. |
| "verified N child process(es); none left" is true whenever no child survived signalling | **False when a child failed the reuse guard.** That child is deliberately left running and unsignalled, so the clean-tree message named it as accounted for while it was still alive — the reverse of the error this whole design exists to avoid. A `leftAlone` counter now splits the two, and the guard's outcome is reported rather than absorbed. |

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
