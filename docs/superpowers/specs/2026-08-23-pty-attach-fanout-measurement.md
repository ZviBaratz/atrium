# What Atrium's per-session pty attach fanout actually costs, measured

Status: measurement, 2026-08-23. Not a design. Issue #800 (UX v2 epic #793,
workstream X), absorbing #548.

Atrium holds one `tmux attach-session` pty client per started session, for that
session's whole life — `Session.Restore` (`session/tmux/tmux.go`), called from
`Start` and again on every detach. #548 filed that fanout as "bounded today,
unbounded in N", was explicit that it must be measured before anyone optimises it,
and named #546's profiling seam as the prerequisite. #546 closed and the seam
shipped (`internal/profile`, SIGUSR1). This is the measurement it was waiting for,
and the verdict the issue asked to be written from it.

The short version is at the end. Read section 5 before quoting section 4 at anyone.

## 1. What the client is for, and why removing it is not free

The client is not decoration. Attaching restores the window size, which is what
makes `capture-pane` return sensibly-shaped content for the preview pane, and it
anchors the `statusMonitor` that `Restore` installs alongside it. #548 said so and
did not propose deleting it. The proposal on the table — its step 2 — is to hold a
client only for sessions that need one and re-attach on demand, with the named risk
that a re-attached session's first capture may come back differently shaped.

So the question is never "is the client free?" It is "does the fanout cost enough
to be worth paying that risk?"

## 2. Method

Four arms. Each answers something the others cannot, and the verdict rests on
where they agree.

### Arm A — the live fleet (real agents, N=31)

The development host was running a real Atrium: pid 197691, 31 sessions, 30
background attach clients, every pane a live `claude`, ~3 h uptime. Sampled
read-only through `/proc`: cumulative CPU (`stat` fields 14–17, all four —
utime/stime and the reaped-children cutime/cstime that #546's first investigation
missed a third of the cost in), `smaps_rollup` Pss, and fd class counts.

### Arm B — a synthetic fleet, with and without the clients (the control)

`session/tmux/fanout_measure_linux_test.go`, driven by `scripts/measure-fanout.sh`.
It builds N real sessions on the package's sandboxed tmux socket, prices them,
**drops every attach client**, and prices the same fleet again. The
with-minus-without difference is the number; an absolute reading of a fleet that
always has clients cannot separate what the clients cost from what the sessions do.

The harness asserts that the control actually dropped the clients. A control that
quietly failed would report the fanout as free — the conclusion this exercise is
most at risk of reaching by accident.

Two pane modes: **idle** (`sleep 3600`) and **active**, a shell loop writing a line
as fast as the pty will take it. Active is deliberately an upper bound, far above
what an agent produces. If the clients are free under a pane that never stops
writing, they are free under `claude`.

### Arm C — the in-process profile (#546's seam, #548's own test)

`kill -USR1` on the live Atrium wrote a 30 s CPU profile plus heap and goroutine
snapshots. #548 step 1 set its own threshold: *if the pty path is under 5 % of
`atr`'s profile, close this as wontfix.*

### Arm D — does dropping a client break the capture?

Cost is only half the decision; the other half is what the proposed fix would risk.
So one client was dropped from the live fleet — this session's own, so any damage
landed on the measurer — and the pane's geometry and capture were compared across
it.

## 3. Numbers

### 3.1 Arm A — the live 31-session fleet

Host: Linux 7.0.0-28-generic, 8 CPUs, 30 GiB. `atr` pid 197691, up ~3 h, panes
running `claude`.

| Reading | Value |
|---|---|
| Attach clients | 30 (one per started session; `tmux list-clients` agrees) |
| Client CPU, cumulative over each client's whole ~3 h life | **0 ticks** — every one of the 30, on all four of utime/stime/cutime/cstime |
| Client CPU over a 35 s window, all 30 combined, fleet actively working | **0 ticks** |
| `atr` over that same window | 25.7 % of a core own, 40.3 % in reaped children |
| tmux server over that same window | 12.9 % of a core |
| Client Pss | 69–1133 KB each; two samples ~40 min apart totalled **17.1 MB** across 30 and **6.4 MB** across 28 |
| `atr` fd classes | 30 `/dev/ptmx`, 34 `[pidfd]`, 77 `[eventpoll]` (later: 28 / 28 / 83) |

A tick is 10 ms. Thirty processes accumulating zero of them across three hours is
not "small"; it is below the resolution of the counter, in both directions, for
every one of them.

### 3.2 Arm C — the in-process profile

30 s CPU profile of the live `atr`, 8.22 s of samples:

- `ansi.stringWidth` 30.5 % cumulative, `lipgloss.Style.Render` 35.0 %, the uax29
  grapheme iterator under both — the frame builder, exactly where #546 left it.
- The pty/attach path: **zero samples.** No `Restore`, no `Pty.Start`, no `ptmx`
  write, no `Setsize`, anywhere in 286 nodes.

#548 step 1 set the threshold itself: *under 5 % of `atr`'s profile means close
this as wontfix.* It is 0.00 %.

The goroutine snapshot is where the fanout does show up, and it is the cleanest
statement of the in-process cost: **29 goroutines in `session/tmux.Pty.Start.func1`**
— one per client, each parked in `os/exec.(*Cmd).Wait` → `pidfdWait` → `Waitid` —
out of 80 goroutines in the process. Parked in a wait, so they cost scheduler
bookkeeping and a stack, and no CPU.

### 3.3 Arm D — the live drop

`tmux detach-client -s <session>` against a live `claude` session, tmux 3.6:

| | before | after |
|---|---|---|
| attached clients | 1 (`/dev/pts/99`, 118x39) | 0 |
| window / pane | 118x38 / 118x38 | **118x38 / 118x38** |
| `capture-pane` | 38 lines, longest 354 bytes | **38 lines, longest 354 bytes** |

Byte-for-byte the same shape with no client attached. On this tmux a session keeps
its window size when its last client leaves; the size is the session's state, not
the client's.

### 3.4 Arm B — the synthetic fleet, with and without clients

`scripts/measure-fanout.sh 1,5,15 15` on the branch of PR #837, tmux 3.6, go 1.26.6.
The PR is the citation rather than a SHA: the harness does not exist on any commit
before it, and a squash merge means the branch SHAs never land on `main` either.
Each arm watched for 15 s. `srv_cpu` is the tmux server's own CPU over the window;
`clnt_cpu` is every client's combined.

```
RUN A                        clients  ptmx  epoll  pidfd    fds gorout   self_cpu  child_cpu    srv_cpu   clnt_cpu     clnt_mem
baseline (no sessions)             0     0      1      0      6      2       30ms         0s        n/a         0s          0 B
N=1  idle   with-clients           1     1      1      1      8      3       40ms         0s         0s         0s      1.1 MiB
N=1  idle   no-clients             0     0      1      0      6      2       20ms         0s       10ms         0s          0 B
N=1  active with-clients           1     1      1      1      8      3       10ms         0s      4.09s         0s      1.1 MiB
N=1  active no-clients             0     0      1      0      6      2       30ms         0s      7.93s         0s          0 B
N=5  idle   with-clients           5     5      1      5     16      7       30ms         0s         0s         0s      5.4 MiB
N=5  idle   no-clients             0     0      1      0      6      2       20ms         0s         0s         0s          0 B
N=5  active with-clients           5     5      1      5     16      7       20ms         0s      8.93s         0s      5.4 MiB
N=5  active no-clients             0     0      1      0      6      2       30ms         0s      8.43s         0s          0 B
N=15 idle   with-clients          15    15      1     15     36     17       20ms         0s       10ms         0s     15.6 MiB
N=15 idle   no-clients             0     0      1      0      6      2       10ms         0s         0s         0s          0 B
N=15 active with-clients          15    15      1     15     36     17       30ms         0s     12.17s         0s     15.4 MiB
N=15 active no-clients             0     0      1      0      6      2       20ms         0s      7.42s         0s          0 B

RUN B (same parameters, same commit, ~1 h later)
baseline (no sessions)             0     0      1      0      6      2       30ms         0s        n/a         0s          0 B
N=1  idle   with-clients           1     1      1      1      8      3         0s         0s         0s         0s      1.1 MiB
N=1  idle   no-clients             0     0      1      0      6      2       30ms         0s         0s         0s          0 B
N=1  active with-clients           1     1      1      1      8      3       10ms         0s     10.96s         0s      1.1 MiB
N=1  active no-clients             0     0      1      0      6      2       10ms         0s     12.31s         0s          0 B
N=5  idle   with-clients           5     5      1      5     16      7       20ms         0s       10ms         0s      5.5 MiB
N=5  idle   no-clients             0     0      1      0      6      2       10ms         0s         0s         0s          0 B
N=5  active with-clients           5     5      1      5     16      7       10ms         0s      7.88s         0s      5.5 MiB
N=5  active no-clients             0     0      1      0      6      2       30ms         0s      4.84s         0s          0 B
N=15 idle   with-clients          15    15      1     15     36     17       30ms         0s       10ms         0s     15.6 MiB
N=15 idle   no-clients             0     0      1      0      6      2       20ms         0s         0s         0s          0 B
N=15 active with-clients          15    15      1     15     36     17       30ms         0s      6.33s         0s     15.5 MiB
N=15 active no-clients             0     0      1      0      6      2       30ms         0s      5.49s         0s          0 B
```

Two runs are shown because they disagree, and where they disagree matters more than
either of them alone. Every object count and every zero above reproduced exactly.
The `srv_cpu` column of the *active* arms did not — see section 4.

Per client, from the with-minus-without difference. Every row above the rule is
identical at N = 1, 5 and 15; the last row is the one that is not, and section 4
takes it apart:

| Axis | Per attach client |
|---|---|
| pty masters held by Atrium | **+1** |
| pidfds | **+1** |
| total fds | **+2** |
| goroutines | **+1** |
| **epoll instances** | **+0** |
| client CPU, idle or active | **0**, at a resolution of 10 ms over 15 s |
| client Pss | **~1.0–1.1 MiB** at these sizes |
| tmux-server CPU, idle pane | **0** |
| — | — |
| tmux-server CPU, active pane | **not measurable here** — the two runs disagree by 5.6× and do not share an ordering |

## 4. Reading

**The fanout is exactly linear, and it is three objects wide.** One pty master, one
pidfd, one parked goroutine, per session, forever, at every size measured. Nothing
else on Atrium's side moved — not its CPU, not its epoll count, not its goroutine
count beyond the one. The cost that is *not* on Atrium's side is the tmux server's,
two blocks down.

**#548's epoll question is answered, and the answer is no.** It asked what allocates
the 125 `[eventpoll]` descriptors and whether the pty plumbing is responsible. The
harness holds the epoll count at **1** across N = 0, 1, 5 and 15, with and without
clients: the epolls are not the attach path's. The live fleet agrees from the other
direction — the count went 77 → 83 while sessions went 30 → 28. Whatever allocates
them, it is not per session, and #548's item 3 can be dropped from this line of
work. What *is* per-session is the pidfd, one per client, which is the Go runtime's
own child-wait mechanism and was not in #548's inventory at all.

**The client's own CPU cost is not small; it is absent.** Not "under 5 %", which
was the threshold #548 wrote for itself — zero samples in a 30 s profile of Atrium,
zero ticks for the clients over a 15 s controlled window in either pane mode, and
zero ticks over the entire three-hour life of thirty real clients. The mechanism is visible in the code: nothing reads a background
client's pty. `Attach` starts a pump goroutine; a session that has never been
interactively attached has no reader at all. So the client writes until the pty
buffer fills and then blocks in `write` for the rest of its life.

**The server-side question is one this harness did not answer, and an earlier draft
of this page claimed it did.** Attached clients are what the tmux server renders for,
so under the synthetic active pane there ought to be a per-client server cost. Two
runs at identical parameters on the same commit:

| Active arm | run A: with / without → per client | run B: with / without → per client |
|---|---|---|
| N=1 | 4.09 s / 7.93 s → **−3.84 s** | 10.96 s / 12.31 s → **−1.35 s** |
| N=5 | 8.93 s / 8.43 s → +0.10 s | 7.88 s / 4.84 s → **+0.61 s** |
| N=15 | 12.17 s / 7.42 s → **+0.32 s** | 6.33 s / 5.49 s → **+0.06 s** |

They disagree by 5.6× at N=15 and do not share an ordering: run A rises with N, run B
peaks at N=5. The `without` column alone — the same arm, the same work, no clients
at all — swings from 7.42 s to 12.31 s. That is not a per-client cost being measured
badly; it is host load being measured. These runs shared an 8-core laptop with the
developer's own 28-session Atrium and its live agents, and the tmux server is
single-threaded, so it competes for whatever is left.

**So no per-client server figure is quoted here.** What survives two runs is a bound
and a sign that will not settle: every per-client difference falls within ±0.7 s per
15 s (±4.6 % of a core), and it is negative at N=1 in both. The negative is the one
suggestive result — a client nobody drains would apply backpressure, stopping the
server pushing, stopping it reading the pane, throttling the writer — but two runs
cannot establish a mechanism, and this page does not claim one.

Resolving it needs a quiet host, which this was not. `scripts/measure-fanout.sh` is
how; section 6's re-measure list says when it would be worth doing.

**Memory is the only axis with a number worth quoting.** ~1 MiB of Pss per client
at small N, and 6.4–17.1 MB total across ~30 real ones — the range is wide because
Pss falls as more processes share the same pages, so the marginal client is cheaper
than the first. Call it **under 20 MB for a 30-session fleet**, which is 0.06 % of
this host's RAM.

## 5. What this does not establish

- **The active mode is a proxy.** It is a shell loop writing as fast as the pty
  takes it, not an agent. It is an upper bound on output volume, but it is not a
  full-screen TUI redraw, and a client rendering alternate-screen updates could in
  principle differ. Arm A covers that gap with 30 real `claude` panes and finds the
  same zero, which is why the proxy is acceptable rather than why it is faithful.
- **One host, one kernel, one tmux.** Linux 7.0, 8 cores, tmux 3.6. The client cost
  being zero is a statement about this tmux's behaviour when its output blocks.
- **The host was not quiet.** Every synthetic run shared an 8-core laptop with a
  live 28-session Atrium and its agents. The object counts, the zeros and the memory
  figures were unaffected — they reproduced exactly across two runs — but the active
  arms' server CPU was not, and section 4 declines to quote it for that reason. A
  measurement that needs the server-side term wants a dedicated host.
- **The live-fleet arm is uncontrolled.** Arm A's `atr` and tmux-server figures were
  taken while real agents worked; they are context, not a controlled comparison.
  Everything load-bearing comes from Arm B, which is controlled.
- **The self-CPU column is at its noise floor.** The test process does nothing
  during a window, and the per-client differences (±20 ms over 15 s, and a
  666 µs/client figure that is one 10 ms tick divided by 15) are quantisation, not
  signal. They are printed rather than suppressed so the floor is visible.
- **Nothing here prices the *transient* client.** `Start` spawns a second, brief
  pty client for `new-session -d` before `Restore` spawns the lasting one. That one
  is out of scope: it does not scale with fleet size, it scales with session
  creation.
- **Nothing here says what the 77–83 epolls are.** It says they are not per-session.
  The positive question is open, and `atrium doctor` depth (#833) is where a fleet
  inventory would surface it if anyone wants it answered.

## 6. Verdict

**Measured-acceptable. #548's concern is closed by data, and no fix issue is
filed.**

At 30 sessions the fanout costs 30 pty masters, 30 pidfds, 30 parked goroutines and
under 20 MB. Its CPU cost is zero in Atrium, zero in the clients themselves, and
zero in the tmux server while panes are idle. #548 asked to be closed as wontfix if
the pty path came in under 5 % of Atrium's profile. It is 0.00 %, and the mechanism
(nobody drains a background client's pty, so it blocks forever after the first
bufferful) explains why that is structural rather than lucky.

The one axis this did not settle is the tmux server under panes writing at pty
speed: two runs at identical parameters disagreed by 5.6× and would not agree on a
sign, so section 4 quotes no per-client figure for it. It does not carry the
verdict either way, because the live fleet prices that whole server at **12.9 % of
a core** while serving 30 real clients for working agents. It is written into the
re-measure list below rather than dismissed.

#548 named a risk for the lazy-attach work: that a re-attached session's first
capture comes back differently shaped, because the attach is what restores the
window size. Arm D shows **half of that risk does not exist** — dropping the last
client changed neither the window size nor the capture, because on tmux 3.6 the
size belongs to the session and outlives its clients. The half that remains is the
re-attach: a fresh `attach-session` runs in a pty of whatever size Atrium gives it
until `updateWindowSize` is called, and *that* is where a differently-shaped
capture could come from. This measurement did not exercise the re-attach path, so
that half is neither confirmed nor cleared here.

Which leaves the decision resting on cost, and at the rates agents actually produce
output that cost is two descriptors, one parked goroutine and about a megabyte per
session. That is not enough to buy a lifecycle change to the path that feeds every
preview in the app.

**Two axes to re-measure, if anyone does, and neither is Atrium's own CPU:**

1. **File descriptors — but not soon, and an earlier draft of this page had this
   badly wrong.** It claimed 1024 as the ceiling and a few hundred sessions as where
   a fleet meets it. Measured on this host: both `RLIMIT_NOFILE` limits are
   **524288**, and Go raises a process's soft limit to its hard limit at startup, so
   the hard limit is what binds — about 250 000 sessions at two descriptors each.
   This is the only term in the fanout with a ceiling rather than a slope, which is
   why it stays on the list, but on a systemd-era Linux desktop it is not a
   near-term one. Where it *would* bite is a host that sets a low hard limit;
   containers commonly ship 1024, and there two descriptors per session is a real
   constraint.
2. **The tmux server under fast-writing panes — still unmeasured, and it is the
   *only* axis where dropping clients could save anything.** It is single-threaded,
   and two runs of the synthetic active arm disagreed by 5.6× because the host was
   busy running the developer's own fleet. What bounds it in practice is the live
   fleet: 30 real clients and working agents put the whole server at 12.9 % of a
   core, so at agent output rates there is little there to save. If a future agent
   streams hard (long tool output, a verbose build, a TUI redrawing at video rates)
   on many sessions at once, this is the number that moves. Re-take it with
   `scripts/measure-fanout.sh` **on an otherwise idle host** — that, not the harness,
   is what these two runs lacked.

## 7. Reproducing

```
scripts/measure-fanout.sh              # sizes 1,5,15, 10s window
scripts/measure-fanout.sh 1,10,30 20   # explicit sizes and window seconds
```

The harness is opt-in (`ATRIUM_MEASURE_FANOUT=1`) and `just ci` skips it; what CI
does run is `session/tmux/proccost_linux_test.go`, which holds the instrument — the
procfs reader — to reading the right fields. The measurement being expensive is not
a reason for the thing doing the measuring to be unguarded.
