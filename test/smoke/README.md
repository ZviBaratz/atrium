# Atrium end-to-end TUI smoke test (vhs spike)

> Spike for [#148](https://github.com/ZviBaratz/atrium/issues/148): evaluate
> [`charmbracelet/vhs`](https://github.com/charmbracelet/vhs) as a deterministic
> harness for the **live interactive layer** that `just test` can't reach — real
> attach/detach, pane classification against a running tmux server, and rendering
> that only happens in a running session.

## What this is

A working prototype, not a finished test suite. It drives the **real** `atrium`
binary through vhs and asserts the final screen of a
`create → attach → detach → re-attach` flow against a golden snapshot.

| File | Role |
|------|------|
| `basic-flow.tape` | vhs script: launches Atrium, creates a session, attaches, detaches, re-attaches. Static/committable — paths are injected via the environment. |
| `fake-agent.sh` | Deterministic stand-in for the agent program. Prints a fixed banner (`ATRIUM_SMOKE_AGENT_READY`) and idles. No real agent (no auth, no time-varying output). |
| `run.sh` | Orchestrator: builds the binary, sets up an isolated HOME + repo + config + tmux socket **per run**, runs the tape N times, reduces vhs's full-scrollback capture to the final screen, checks determinism, and compares to the golden. |
| `testdata/basic-flow.golden.txt` | The committed final-screen snapshot. |

## Running it

```sh
just smoke              # build, run 3×, assert determinism + golden match
RUNS=1 just smoke       # faster single run
UPDATE=1 just smoke     # refresh the golden after an intentional UI change
KEEP=1 RUNS=1 just smoke # keep the scratch dir (path printed) for debugging
```

Requires non-Go binaries on `PATH`: **`vhs`, `ttyd`, `ffmpeg`** (vhs deps) plus
**`tmux`** and **`jq`**. This is why it is deliberately **excluded** from
`just test` / `just ci` and lives in a non-Go directory (so `go test ./...`
never compiles it).

## How isolation works (and why it's safe to run anywhere)

- **Data dir**: a temp `HOME`, so the data dir, `worktrees/`, and `state.json`
  are throwaway. Each determinism run gets its own `HOME` (run 1's session must
  not pollute run 2's empty-list assertion).
- **tmux socket**: Atrium always runs its inner sessions on a socket named
  `atrium`, which normally lives in the shared `/tmp/tmux-$UID/`. The runner
  exports **`TMUX_TMPDIR`** to a per-run temp dir, so the inner `tmux -L atrium`
  socket is fully isolated — running `just smoke` does **not** touch the user's
  real Atrium sessions (verified: the live socket's sessions are unchanged and no
  `smoke` session leaks). This is a cleaner isolation than the
  "share the socket, clean up by exact name" approach used in earlier ad-hoc
  smoke work.

## Findings & Decision (#148)

### 1. Prototype works ✓

The full `create → attach → detach → re-attach` flow runs end-to-end through vhs
and produces this final screen (the attached fake-agent pane, with Atrium's
context-bar header):

```
────────────────────────────────────────────────────────────────────────────────
 • ● repo · smoke
ATRIUM_SMOKE_AGENT_READY
fake agent: input is echoed as a fixed token

────────────────────────────────────────────────────────────────────────────────
```

That snapshot alone proves several things `View()` golden tests cannot: the
session was created with a real worktree, the attach actually entered the right
pane, pane targeting/classification ran against a live tmux server, and the
context bar rendered.

### 2. Determinism ✓ — but it lives in the harness, not in vhs

3/3 runs produce byte-identical final screens. The determinism comes from things
the spike had to engineer, **not** from vhs itself:

- **A deterministic agent.** A real agent is unusable here (auth + nondeterministic,
  time-varying output). `fake-agent.sh` emits a fixed banner and no clock/spinner.
- **Avoiding Atrium's own time-relative UI.** The session **list** renders
  spinners and relative timestamps; a full-screen golden of it would flake. The
  capture deliberately ends on the **attached agent pane**, which has none of
  that. (Atrium's managed tmux config already empties `status-right` and sets
  `status-interval 0`, so the attached pane carries no clock.)
- **`vhs Wait+Screen /regex/`** removes timing flakiness by gating each step on
  on-screen text instead of fixed `Sleep`s — this *is* a genuine vhs strength.
- **Reducing the capture.** vhs's `.txt` `Output` holds the terminal's **full
  scrollback** (~800 lines, every repaint), not just the visible screen. `run.sh`
  extracts the final screen (anchored on the context-bar header) so the golden is
  small and only churns when the screen that matters changes. (`.ascii` `Output`
  produced no file with vhs v0.11.0; `.txt` is what works.)

Two real gotchas surfaced and are handled, worth knowing for any future tape:
- The **first keystroke after launch is swallowed** while the Bubble Tea model
  finishes its initial `WindowSize` round-trip — and there's a first-run
  "Welcome to Atrium" modal. The tape primes input with an inert `Escape`.
- The new-session form's **profile picker overrides `-p`**: a fresh config probes
  `PATH` and synthesizes a real `claude` profile that wins. The runner seeds a
  config then `jq`-rewrites `default_program` + `profiles` to the fake agent.

### 3. CI viability — feasible, with real cost

- **Speed**: ~8 s for a single full run (build + seed + tape + extract + compare);
  ~6 s of that is the tape's deliberate `Sleep`/`Wait` budget. RUNS=3 ≈ 20 s.
  Acceptable for a dedicated job, not for the inner loop.
- **Dependencies**: a CI job needs `vhs`, `ttyd`, `ffmpeg`, `tmux`, `jq`. Today's
  CI installs **none** of these and doesn't even install tmux (it `-skip`s the one
  real-tmux test). [`charmbracelet/vhs-action`](https://github.com/charmbracelet/vhs-action)
  bundles vhs+ttyd+ffmpeg; tmux+jq are a one-line `apt-get`.
- **Headless**: vhs runs headless via ttyd (no X / browser) — confirmed locally.

### 4. Recommendation: **adopt vhs for the e2e layer, local-first**

vhs is the right tool for this gap: the `.tape` DSL + `Wait+Screen` + golden text
capture are exactly what a reproducible interactive smoke test needs, and nothing
else evaluated (teatest/catwalk) fits an externally-orchestrated tmux app. Keep
this prototype as the seed of that layer.

Scope it deliberately, because the spike showed the harness — not vhs — carries
the determinism burden:

- **Adopt** `just smoke` as an **opt-in, local** gate now (this prototype). It is
  isolated, deterministic, and safe to run anywhere.
- **Defer the CI job** until there's more than one flow worth gating. When added,
  make it a **separate, ubuntu-only, initially non-blocking** job (via
  `vhs-action` + `apt-get install tmux jq`), so e2e flakiness can never block a
  PR on day one. This matches the issue's non-goal of not wiring CI in the spike.
- **The follow-on skill** (`#148`'s "only if adopted" item) is now justified: a
  thin project skill that scaffolds a new `.tape` + golden for a given flow,
  wrapping `run.sh` rather than reinventing a `send-keys` harness.

**Non-goals confirmed**: this does not replace the hermetic `View()` golden tests,
does not adopt teatest/catwalk, and covers exactly one flow.
