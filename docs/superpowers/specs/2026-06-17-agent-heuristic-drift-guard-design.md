# Agent-heuristic drift guard

**Date:** 2026-06-17
**Status:** Approved

## Motivation

Atrium's entire status model rests on version-pinned strings in
`session/agent/registry.go`: busy markers (`"esc to interrupt"`), prompt
matchers (`"No, and tell Claude what to do differently"`), plan/auth/model
matchers, and startup gates. Every one is a heuristic captured against a
*specific* CLI version — the comments say so repeatedly ("version-sensitive by
nature", "pinned against a live 2.1.170 pane", "verified against the installed
0.27 package source").

When an agent CLI rewords one of these strings, the matcher silently breaks.
The failure modes are the dangerous, invisible kind:

- A needs-input session reads as **idle** — it sits unattended.
- Autoyes never surfaces a prompt it should have (or, for a busy-marker change,
  a working session reads as idle and autoyes acts on stale content).

Nothing detects this today. The fixtures in `registry_test.go` pin strings
against *captured* panes — they prove the matcher works against the snapshot,
not against the version actually installed. A passing test suite says nothing
about whether the installed `claude` still emits those strings.

The drift is already live on a typical machine:

| Agent  | Heuristics pinned to | Installed (2026-06-17) | Drift            |
|--------|----------------------|------------------------|------------------|
| Claude | 2.1.170              | 2.1.179                | 9 patch versions |
| Gemini | 0.27                 | 0.45.1                 | 18 minor versions|

This is a **detect-and-tell-the-human** guard. It turns silent heuristic drift
into a visible, actionable signal. It does not, and deliberately must not,
auto-adapt matching behavior by version (see Non-goals).

## Scope

**In scope (v1):**

- A `VerifiedVersion` field per `Adapter`, treated as a **ceiling** the
  maintainer advances — the highest CLI version whose heuristic strings have
  been confirmed.
- Runtime version probing of installed agent CLIs (`<bin> --version`) through an
  injectable runner.
- Drift comparison: `installed > VerifiedVersion`, at a per-adapter
  granularity, using the existing `Masterminds/semver/v3` dependency.
- `atrium doctor` subcommand: full drift table for every recognized agent.
- An acknowledgeable startup hint when any installed agent on `PATH` has drifted,
  reusing the existing hint/badge plumbing.
- The documented **additive-with-deprecation** remediation policy for when a
  string genuinely changes.

**Out of scope (deliberately) — see Non-goals for rationale:**

- Live string-presence probing (capturing a running pane to confirm matchers).
- Version-*selected* matchers (auto-choosing a string-set by detected version).
- Structured per-string deprecation metadata + an auto "safe-to-remove" report.
- Semver ranges / downgrade warnings (older-than-verified is silent).
- CI gating on drift (installed versions are environment-specific).
- A test that *enforces* bumping `VerifiedVersion`.

## Approach

### Data model

Add one field to `Adapter` in `session/agent/registry.go`:

```go
// VerifiedVersion is the highest CLI version whose heuristic strings have been
// confirmed against a live pane. Treated as a ceiling: an installed version
// above it is "unverified territory" and triggers a drift warning. Bump it
// (after re-checking) whenever a matcher string is edited, and also on a plain
// re-verification of a newer release. Empty = unversioned (codex/aider): shown
// in `atrium doctor`, never triggers a hint.
VerifiedVersion string

// DriftGranularity is the smallest semver component whose increase past
// VerifiedVersion counts as drift. Defaults to patch (most conservative).
DriftGranularity Granularity // Patch | Minor | Major
```

Seed from existing provenance: `claude → "2.1.170"`, granularity **patch**
(Claude rewords gating strings inside patch releases, so patch is mandatory —
a coarser setting would silently miss real drift). `gemini → "0.27"`,
granularity **minor** (0.45 vs 0.27 is the meaningful axis; pure patch bumps
within a minor don't warrant a warning). `codex → ""`, `aider → ""`
(unversioned). Default granularity is **patch**.

Expose the registry for probing via a new accessor — the `agent` package stays
pure (no I/O):

```go
// Adapters returns the recognized adapters (excludes Generic).
func Adapters() []*Adapter
```

### Version probe & comparison — `internal/doctor`

A new package owns all shell-out and comparison logic; it imports `agent` and
shells out, keeping `agent` pure.

- **Probe.** Run `<bin> --version` with a ~2s timeout through an injectable
  runner interface (so tests never touch a real CLI), extract the first semver
  token via regex `(\d+\.\d+(?:\.\d+)?)`. Verified against real output:
  `claude "2.1.179 (Claude Code)"` → `2.1.179`; `gemini "0.45.1"` → `0.45.1`.
- **Binary set.** Recognized adapters whose alias binary resolves on `PATH`.
  Not-installed agents are skipped.
- **Compare.** Parse both sides with `Masterminds/semver/v3`. Drift is
  `installed > VerifiedVersion`, evaluated only down to `DriftGranularity`
  (components below it are zeroed before comparison). An installed version that
  is *older than or equal to* the ceiling is **not** drift (no warning — its
  strings are a subset of what we verified). An unparseable installed version or
  an empty `VerifiedVersion` yields status **unknown** (shown in `doctor`,
  never a hint).

The package exposes a single entry point returning a structured result per
agent (`Key`, installed version, verified ceiling, status:
`ok`/`drifted`/`unknown`/`not-installed`), consumed by both surfaces.

### Surface 1 — `atrium doctor`

A new Cobra subcommand in `main.go` (alongside `debug`, `version`, `update`).
Probes synchronously and prints the full table for every recognized agent —
never nags, the deliberate diagnostic a user runs when a session is
misbehaving.

```
$ atrium doctor
Agent heuristics:
  claude   2.1.179   verified 2.1.170   ⚠ drifted (installed newer)
  gemini   0.45.1    verified 0.27      ⚠ drifted (installed newer)
  codex    not installed
  aider    not installed
```

### Surface 2 — acknowledgeable startup hint

At startup the same check runs (non-blocking, mirroring the auto-update check in
`app/app_updatecheck.go`). If any installed agent on `PATH` is `drifted` and not
acknowledged at its current installed version, emit a one-line hint through the
existing hint-bar / Sessions-panel-badge plumbing, pointing at `atrium doctor`.
Per the hint-scope decision, the hint fires for **any** drifted agent on `PATH`,
not only agents in active use — the per-version acknowledgement caps the noise.

**Acknowledgement** is stored in `config.State` as a new field:

```go
// AckedDrift maps an agent key to the installed version the user dismissed the
// drift hint for. The hint stays quiet while installed == acked; a later
// version bump re-arms it. Mirrors the update-hint dismissal pattern.
AckedDrift map[string]string `json:"acked_drift,omitempty"`
```

Dismissing the hint records the currently-installed version for that agent.

### Remediation policy — additive, with a deprecation window

When the guard fires and the maintainer confirms a gating string actually
changed, the fix is **additive, never replace-in-place**:

- Add the new variant *alongside* the old in the same matcher (`Any:`/`All:`
  list) and bump `VerifiedVersion`. The matchers are already string lists, so
  this is zero new machinery — the registry already carries alternate labels
  this way (e.g. the claude plan matcher's `Any:` set).
- Keep both variants through a deprecation window; remove the old one once that
  CLI version is rare.

This is strictly safer than version-selected matching: a pane only ever shows
one variant, so a union match cannot guess wrong, and **no version detection
enters the matching path at all** — version detection serves only the
human-facing warning.

Deprecation is tracked by **comment convention**, matching the registry's
existing provenance-in-comments style:

```go
// claude ≥2.1.180; "No, keep planning" kept for <2.1.180, remove after.
```

A plain re-verification (strings unchanged at a newer release) is just a
`VerifiedVersion` bump — no string edit. The smoke harness from #152 is the
intended way to perform that re-check: capture a live pane, confirm the strings,
advance the ceiling.

## Testing

All hermetic, fitting the existing `HOME`-temp convention:

- **Version-string parse** — table tests over real `--version` outputs
  (claude/gemini/codex/aider variants, and malformed input).
- **Drift compare** — table tests over (installed, verified, granularity) →
  (status, direction). Pure, no I/O.
- **Probe** — through the fake runner; asserts not-installed / timeout / parse
  failure paths without invoking a real CLI.
- **`doctor` render** — golden test on the table output.
- **Ack/suppress** — unit test: drift + no ack → hint; drift + matching ack →
  suppressed; version bump past ack → re-armed.

## Non-goals (and why)

- **Live string-presence probing.** Accurate, but needs a running agent in a
  known state; the version proxy is cheap and always available. The smoke
  harness (#152) covers live verification when a session exists.
- **Version-selected matchers** (pick a string-set by detected version). A wrong
  guess for an unseen version fails *silent* — the exact failure the guard
  exists to surface, now caused by the guard. Additive union matching is the
  safe alternative.
- **Structured per-string deprecation metadata + auto "safe-to-remove" report.**
  A lingering extra alternate in an `Any:` list is cheap and low-risk (matchers
  are windowed/structural against false positives). Revisit only if old strings
  actually pile up. The single matcher-selection point (`Resolve` →
  `adapter.Prompts`) is where any future version-aware selection would hook in;
  v1 adds no scaffolding for it.
- **CI gating on drift.** Installed CLI versions vary per environment; a CI gate
  would be flaky and meaningless.
- **A test enforcing the `VerifiedVersion` bump.** Too clever for v1; the
  comment convention carries it.
