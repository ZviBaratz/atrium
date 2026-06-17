# Agent-Heuristic Drift Guard Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Detect when an installed agent CLI has moved past the version Atrium's pane-classification heuristics were verified against, and surface it as an `atrium doctor` table plus an acknowledgeable startup hint.

**Architecture:** A new pure field (`VerifiedVersion` + `DriftGranularity`) on each `agent.Adapter` records the verified ceiling. A new `internal/doctor` package probes installed CLI versions (`<bin> --version`) through an injectable runner and compares them with `Masterminds/semver/v3` at per-adapter granularity. Two surfaces consume the result: a synchronous `atrium doctor` subcommand and a non-blocking startup `tea.Cmd` that emits a hint, acknowledged per-version in `config.State`.

**Tech Stack:** Go, Cobra (CLI), Bubble Tea (TUI), `github.com/Masterminds/semver/v3` (already a dependency).

## Global Constraints

- `go` is not on the Bash-tool PATH. Use `GO=/home/zvi/.local/share/mise/installs/go/latest/bin/go`. Run package tests as `$GO test ./path/ -run Name -v`; run the full suite as `GO=$GO just test`. Build with `GO=$GO just build`.
- The `agent` package is **pure data + string matching — no IO, no subprocesses, no tmux** (see its package doc). All shell-out lives in `internal/doctor`.
- Tests must be **hermetic**: never touch a real CLI or the user's data dir. The `doctor` probe is exercised through a fake runner; the `app` package already sets `HOME` to a temp dir in `TestMain`.
- Commits: Conventional Commits, lowercase. End each commit message body with:
  `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`
- Verified seed values (from registry provenance): `claude → "2.1.170"` (patch granularity), `gemini → "0.27"` (minor granularity). `codex`/`aider` stay unversioned (`""`).

---

### Task 1: Adapter version fields + registry seed + accessor

**Files:**
- Modify: `session/agent/agent.go` (add `Granularity` type + two `Adapter` fields + `Adapters()` accessor)
- Modify: `session/agent/registry.go` (seed `VerifiedVersion`/`DriftGranularity` on claude + gemini)
- Test: `session/agent/drift_fields_test.go` (create)

**Interfaces:**
- Produces:
  - `type Granularity int` with `GranularityPatch Granularity = iota`, `GranularityMinor`, `GranularityMajor` (patch is the zero value → the conservative default).
  - `Adapter.VerifiedVersion string`, `Adapter.DriftGranularity Granularity`.
  - `func Adapters() []*Adapter` — returns the recognized adapters (the unexported `registry`; excludes `Generic`).

- [ ] **Step 1: Write the failing test**

Create `session/agent/drift_fields_test.go`:

```go
package agent

import "testing"

func TestAdaptersExposesSeededVersions(t *testing.T) {
	want := map[Key]struct {
		verified string
		gran     Granularity
	}{
		KeyClaude: {"2.1.170", GranularityPatch},
		KeyGemini: {"0.27", GranularityMinor},
		KeyCodex:  {"", GranularityPatch},
		KeyAider:  {"", GranularityPatch},
	}
	got := Adapters()
	if len(got) != len(want) {
		t.Fatalf("Adapters() returned %d adapters, want %d", len(got), len(want))
	}
	for _, a := range got {
		w, ok := want[a.Key]
		if !ok {
			t.Fatalf("unexpected adapter %q", a.Key)
		}
		if a.VerifiedVersion != w.verified {
			t.Errorf("%s VerifiedVersion = %q, want %q", a.Key, a.VerifiedVersion, w.verified)
		}
		if a.DriftGranularity != w.gran {
			t.Errorf("%s DriftGranularity = %d, want %d", a.Key, a.DriftGranularity, w.gran)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `GO=/home/zvi/.local/share/mise/installs/go/latest/bin/go; $GO test ./session/agent/ -run TestAdaptersExposesSeededVersions -v`
Expected: FAIL — `undefined: Adapters`, `undefined: Granularity`.

- [ ] **Step 3: Add the type, fields, and accessor**

In `session/agent/agent.go`, after the `Key` constants block (after line 30), add:

```go
// Granularity is the smallest semver component whose increase past an adapter's
// VerifiedVersion counts as drift. Patch is the zero value and the conservative
// default: any installed version above the verified ceiling drifts.
type Granularity int

const (
	GranularityPatch Granularity = iota
	GranularityMinor
	GranularityMajor
)
```

In the `Adapter` struct (after the `Key`/`DisplayName` fields, around line 112), add:

```go
	// VerifiedVersion is the highest CLI version whose heuristic strings have
	// been confirmed against a live pane — a ceiling, not a frozen pin. An
	// installed version above it is unverified territory and triggers a drift
	// warning. Bump it (after re-checking) whenever a matcher string is edited,
	// and on a plain re-verification of a newer release. Empty = unversioned
	// (codex/aider): shown in `atrium doctor`, never triggers a hint.
	VerifiedVersion string
	// DriftGranularity is the smallest semver component whose increase past
	// VerifiedVersion counts as drift. Zero value (GranularityPatch) is the
	// conservative default.
	DriftGranularity Granularity
```

After the `NamerKeys` function (after line 183), add:

```go
// Adapters returns the recognized agent adapters (excludes the Generic
// fallback). The doctor package probes these for version drift.
func Adapters() []*Adapter {
	return registry
}
```

- [ ] **Step 4: Seed the registry**

In `session/agent/registry.go`, in the `claude` adapter literal, add after `aliases: []string{"claude"},` (line 19):

```go
	// Heuristic strings verified against claude 2.1.170 (see Prompts provenance).
	// Patch granularity: claude rewords gating strings inside patch releases, so
	// any version above this ceiling is unverified — a coarser granularity would
	// silently miss real drift.
	VerifiedVersion:  "2.1.170",
	DriftGranularity: GranularityPatch,
```

In the `gemini` adapter literal, add after `aliases: []string{"gemini"},` (line 172):

```go
	// Heuristic strings verified against gemini 0.27. Minor granularity: the
	// confirmation wording tracks minor releases; pure patch bumps within a
	// minor don't warrant a warning.
	VerifiedVersion:  "0.27",
	DriftGranularity: GranularityMinor,
```

Leave `codex` and `aider` unversioned (no fields added).

- [ ] **Step 5: Run test to verify it passes**

Run: `$GO test ./session/agent/ -run TestAdaptersExposesSeededVersions -v`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add session/agent/agent.go session/agent/registry.go session/agent/drift_fields_test.go
git commit -m "feat(agent): record verified CLI version and drift granularity per adapter" -m "Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: Version-string parsing (`internal/doctor`)

**Files:**
- Create: `internal/doctor/version.go`
- Test: `internal/doctor/version_test.go`

**Interfaces:**
- Produces: `func parseVersion(out string) (string, bool)` — returns the first `MAJOR.MINOR[.PATCH]` token in `out`, and `false` if none.

- [ ] **Step 1: Write the failing test**

Create `internal/doctor/version_test.go`:

```go
package doctor

import "testing"

func TestParseVersion(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"2.1.179 (Claude Code)\n", "2.1.179", true},
		{"0.45.1\n", "0.45.1", true},
		{"aider 0.64.1\n", "0.64.1", true},
		{"codex-cli 0.12\n", "0.12", true},
		{"no version here", "", false},
		{"", "", false},
	}
	for _, c := range cases {
		got, ok := parseVersion(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("parseVersion(%q) = (%q,%v), want (%q,%v)", c.in, got, ok, c.want, c.ok)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `$GO test ./internal/doctor/ -run TestParseVersion -v`
Expected: FAIL — package/`parseVersion` not defined.

- [ ] **Step 3: Implement**

Create `internal/doctor/version.go`:

```go
// Package doctor probes installed agent CLIs and reports whether their versions
// have drifted past the heuristic ceiling Atrium's pane classification was
// verified against. It is the only place that shells out to agent binaries; the
// agent package itself stays pure.
package doctor

import "regexp"

// versionRe matches the first MAJOR.MINOR[.PATCH] token in --version output.
var versionRe = regexp.MustCompile(`\d+\.\d+(?:\.\d+)?`)

// parseVersion extracts the first semver-shaped token from a --version line.
func parseVersion(out string) (string, bool) {
	m := versionRe.FindString(out)
	if m == "" {
		return "", false
	}
	return m, true
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `$GO test ./internal/doctor/ -run TestParseVersion -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/doctor/version.go internal/doctor/version_test.go
git commit -m "feat(doctor): parse semver token from agent --version output" -m "Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 3: Drift comparison at granularity (`internal/doctor`)

**Files:**
- Create: `internal/doctor/compare.go`
- Test: `internal/doctor/compare_test.go`

**Interfaces:**
- Consumes: `agent.Granularity`, `agent.GranularityPatch/Minor/Major` (Task 1).
- Produces: `func driftExceeds(installed, verified string, g agent.Granularity) (bool, error)` — `true` when `installed > verified` after both are truncated to `g`; error if either fails to parse as semver.

- [ ] **Step 1: Write the failing test**

Create `internal/doctor/compare_test.go`:

```go
package doctor

import (
	"testing"

	"github.com/ZviBaratz/atrium/session/agent"
)

func TestDriftExceeds(t *testing.T) {
	cases := []struct {
		name      string
		installed string
		verified  string
		gran      agent.Granularity
		want      bool
	}{
		{"claude patch newer drifts", "2.1.179", "2.1.170", agent.GranularityPatch, true},
		{"claude patch equal no drift", "2.1.170", "2.1.170", agent.GranularityPatch, false},
		{"claude patch older no drift", "2.1.165", "2.1.170", agent.GranularityPatch, false},
		{"gemini minor newer drifts", "0.45.1", "0.27", agent.GranularityMinor, true},
		{"gemini patch within minor no drift", "0.27.5", "0.27.0", agent.GranularityMinor, false},
		{"major gran ignores minor bump", "1.4.0", "1.2.0", agent.GranularityMajor, false},
		{"major gran catches major bump", "2.0.0", "1.9.0", agent.GranularityMajor, true},
	}
	for _, c := range cases {
		got, err := driftExceeds(c.installed, c.verified, c.gran)
		if err != nil {
			t.Fatalf("%s: unexpected error: %v", c.name, err)
		}
		if got != c.want {
			t.Errorf("%s: driftExceeds(%q,%q) = %v, want %v", c.name, c.installed, c.verified, got, c.want)
		}
	}
}

func TestDriftExceedsBadVersionErrors(t *testing.T) {
	if _, err := driftExceeds("not-a-version", "2.1.0", agent.GranularityPatch); err == nil {
		t.Error("expected error for unparseable installed version")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `$GO test ./internal/doctor/ -run TestDriftExceeds -v`
Expected: FAIL — `undefined: driftExceeds`.

- [ ] **Step 3: Implement**

Create `internal/doctor/compare.go`:

```go
package doctor

import (
	"github.com/Masterminds/semver/v3"

	"github.com/ZviBaratz/atrium/session/agent"
)

// driftExceeds reports whether installed is newer than verified once both are
// truncated to the given granularity. Components below the granularity are
// zeroed before comparison, so e.g. a minor-granularity adapter ignores patch
// bumps. Returns an error if either string is not valid semver.
func driftExceeds(installed, verified string, g agent.Granularity) (bool, error) {
	iv, err := semver.NewVersion(installed)
	if err != nil {
		return false, err
	}
	vv, err := semver.NewVersion(verified)
	if err != nil {
		return false, err
	}
	return truncate(iv, g).Compare(truncate(vv, g)) > 0, nil
}

// truncate zeroes the version components below the given granularity.
func truncate(v *semver.Version, g agent.Granularity) *semver.Version {
	switch g {
	case agent.GranularityMajor:
		return semver.New(v.Major(), 0, 0, "", "")
	case agent.GranularityMinor:
		return semver.New(v.Major(), v.Minor(), 0, "", "")
	default:
		return semver.New(v.Major(), v.Minor(), v.Patch(), "", "")
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `$GO test ./internal/doctor/ -run TestDriftExceeds -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/doctor/compare.go internal/doctor/compare_test.go
git commit -m "feat(doctor): compare installed vs verified version at granularity" -m "Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 4: Probe + Check orchestration (`internal/doctor`)

**Files:**
- Create: `internal/doctor/check.go`
- Test: `internal/doctor/check_test.go`

**Interfaces:**
- Consumes: `agent.Adapters()`, `agent.Key`, `parseVersion` (Task 2), `driftExceeds` (Task 3).
- Produces:
  - `type Status int` with `StatusOK`, `StatusDrifted`, `StatusUnknown`, `StatusNotInstalled`.
  - `type Result struct { Key agent.Key; Name string; Installed string; Verified string; Status Status }`.
  - `type Runner interface { version(ctx context.Context, bin string) (string, error) }` (unexported method → fakes live in-package).
  - `var ErrNotInstalled = errors.New("not installed")` — a `Runner` returns this (wrapped) when the binary is absent.
  - `func Check(ctx context.Context, adapters []*agent.Adapter, r Runner) []Result`.
  - `func CheckInstalled(ctx context.Context) []Result` — `Check(ctx, agent.Adapters(), execRunner{})`.
  - `func Drifted(results []Result) []Result` — the subset with `StatusDrifted`.

- [ ] **Step 1: Write the failing test**

Create `internal/doctor/check_test.go`:

```go
package doctor

import (
	"context"
	"fmt"
	"testing"

	"github.com/ZviBaratz/atrium/session/agent"
)

// fakeRunner returns canned --version output (or error) per binary.
type fakeRunner struct {
	out map[string]string
	err map[string]error
}

func (f fakeRunner) version(_ context.Context, bin string) (string, error) {
	if e, ok := f.err[bin]; ok {
		return "", e
	}
	if o, ok := f.out[bin]; ok {
		return o, nil
	}
	return "", fmt.Errorf("%w: %s", ErrNotInstalled, bin)
}

func statusFor(results []Result, k agent.Key) Status {
	for _, r := range results {
		if r.Key == k {
			return r.Status
		}
	}
	return StatusNotInstalled
}

func TestCheckClassifies(t *testing.T) {
	r := fakeRunner{
		out: map[string]string{
			"claude": "2.1.179 (Claude Code)\n", // verified 2.1.170, patch -> drifted
			"gemini": "0.27.4\n",                // verified 0.27, minor -> ok
			"codex":  "0.12.0\n",                // unversioned adapter -> unknown
		},
		err: map[string]error{},
	}
	got := Check(context.Background(), agent.Adapters(), r)

	if s := statusFor(got, agent.KeyClaude); s != StatusDrifted {
		t.Errorf("claude status = %v, want StatusDrifted", s)
	}
	if s := statusFor(got, agent.KeyGemini); s != StatusOK {
		t.Errorf("gemini status = %v, want StatusOK", s)
	}
	if s := statusFor(got, agent.KeyCodex); s != StatusUnknown {
		t.Errorf("codex status = %v, want StatusUnknown (unversioned)", s)
	}
	if s := statusFor(got, agent.KeyAider); s != StatusNotInstalled {
		t.Errorf("aider status = %v, want StatusNotInstalled", s)
	}
}

func TestCheckUnparseableVersionIsUnknown(t *testing.T) {
	r := fakeRunner{out: map[string]string{"claude": "weird build\n"}}
	got := Check(context.Background(), agent.Adapters(), r)
	if s := statusFor(got, agent.KeyClaude); s != StatusUnknown {
		t.Errorf("claude status = %v, want StatusUnknown", s)
	}
}

func TestDriftedFilter(t *testing.T) {
	in := []Result{
		{Key: agent.KeyClaude, Status: StatusDrifted},
		{Key: agent.KeyGemini, Status: StatusOK},
	}
	out := Drifted(in)
	if len(out) != 1 || out[0].Key != agent.KeyClaude {
		t.Errorf("Drifted() = %+v, want only claude", out)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `$GO test ./internal/doctor/ -run 'TestCheck|TestDrifted' -v`
Expected: FAIL — `undefined: Check`, `ErrNotInstalled`, etc.

- [ ] **Step 3: Implement**

Create `internal/doctor/check.go`:

```go
package doctor

import (
	"context"
	"errors"
	"os/exec"

	"github.com/ZviBaratz/atrium/session/agent"
)

// Status is the drift classification of one agent CLI.
type Status int

const (
	// StatusOK: installed and within the verified ceiling.
	StatusOK Status = iota
	// StatusDrifted: installed version is past the verified ceiling.
	StatusDrifted
	// StatusUnknown: installed but version unparseable, or the adapter is
	// unversioned (no VerifiedVersion to compare against).
	StatusUnknown
	// StatusNotInstalled: the binary is not on PATH.
	StatusNotInstalled
)

// Result is the drift report for one agent.
type Result struct {
	Key       agent.Key
	Name      string
	Installed string // parsed installed version, "" when not installed/unparseable
	Verified  string // adapter's VerifiedVersion, "" when unversioned
	Status    Status
}

// ErrNotInstalled is returned (wrapped) by a Runner when the binary is absent.
var ErrNotInstalled = errors.New("not installed")

// Runner probes an agent binary's reported version. The method is unexported so
// only in-package fakes (tests) can implement it.
type Runner interface {
	version(ctx context.Context, bin string) (string, error)
}

// execRunner is the production Runner: it shells out to `<bin> --version`.
type execRunner struct{}

func (execRunner) version(ctx context.Context, bin string) (string, error) {
	path, err := exec.LookPath(bin)
	if err != nil {
		return "", ErrNotInstalled
	}
	out, err := exec.CommandContext(ctx, path, "--version").Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// Check probes each adapter's binary (by its Key) and classifies drift. It
// never errors: every adapter yields a Result whose Status carries the outcome.
func Check(ctx context.Context, adapters []*agent.Adapter, r Runner) []Result {
	results := make([]Result, 0, len(adapters))
	for _, a := range adapters {
		res := Result{Key: a.Key, Name: a.DisplayName, Verified: a.VerifiedVersion}
		out, err := r.version(ctx, string(a.Key))
		switch {
		case errors.Is(err, ErrNotInstalled):
			res.Status = StatusNotInstalled
		case err != nil:
			res.Status = StatusUnknown
		default:
			res.Status = classify(out, a)
			if v, ok := parseVersion(out); ok {
				res.Installed = v
			}
		}
		results = append(results, res)
	}
	return results
}

// classify turns a successful --version capture into a Status for one adapter.
func classify(out string, a *agent.Adapter) Status {
	v, ok := parseVersion(out)
	if !ok {
		return StatusUnknown
	}
	if a.VerifiedVersion == "" {
		return StatusUnknown
	}
	drift, err := driftExceeds(v, a.VerifiedVersion, a.DriftGranularity)
	if err != nil {
		return StatusUnknown
	}
	if drift {
		return StatusDrifted
	}
	return StatusOK
}

// CheckInstalled probes the recognized adapters against the real environment.
func CheckInstalled(ctx context.Context) []Result {
	return Check(ctx, agent.Adapters(), execRunner{})
}

// Drifted returns the subset of results classified as drifted.
func Drifted(results []Result) []Result {
	var out []Result
	for _, r := range results {
		if r.Status == StatusDrifted {
			out = append(out, r)
		}
	}
	return out
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `$GO test ./internal/doctor/ -run 'TestCheck|TestDrifted' -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/doctor/check.go internal/doctor/check_test.go
git commit -m "feat(doctor): probe installed agent versions and classify drift" -m "Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 5: Render table + `atrium doctor` subcommand

**Files:**
- Create: `internal/doctor/render.go`
- Test: `internal/doctor/render_test.go`
- Modify: `main.go` (add `doctorCmd` var + `rootCmd.AddCommand(doctorCmd)`)

**Interfaces:**
- Consumes: `Result`, `Status` constants (Task 4).
- Produces: `func Render(results []Result) string` — a human-readable, newline-terminated table.

- [ ] **Step 1: Write the failing test**

Create `internal/doctor/render_test.go`:

```go
package doctor

import (
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/session/agent"
)

func TestRender(t *testing.T) {
	out := Render([]Result{
		{Key: agent.KeyClaude, Name: "Claude Code", Installed: "2.1.179", Verified: "2.1.170", Status: StatusDrifted},
		{Key: agent.KeyGemini, Name: "Gemini CLI", Installed: "0.27.4", Verified: "0.27", Status: StatusOK},
		{Key: agent.KeyCodex, Name: "Codex", Status: StatusNotInstalled},
		{Key: agent.KeyAider, Name: "Aider", Installed: "0.64.1", Status: StatusUnknown},
	})

	for _, want := range []string{
		"Claude Code", "2.1.179", "2.1.170", "drifted",
		"Gemini CLI", "ok",
		"Codex", "not installed",
		"Aider", "unknown",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("Render() output missing %q\n--- got ---\n%s", want, out)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `$GO test ./internal/doctor/ -run TestRender -v`
Expected: FAIL — `undefined: Render`.

- [ ] **Step 3: Implement Render**

Create `internal/doctor/render.go`:

```go
package doctor

import (
	"fmt"
	"strings"
)

func (s Status) label() string {
	switch s {
	case StatusOK:
		return "ok"
	case StatusDrifted:
		return "⚠ drifted"
	case StatusNotInstalled:
		return "not installed"
	default:
		return "unknown"
	}
}

// Render formats drift results as an aligned, newline-terminated table.
func Render(results []Result) string {
	var b strings.Builder
	b.WriteString("Agent heuristics:\n")
	for _, r := range results {
		installed := r.Installed
		if installed == "" {
			installed = "-"
		}
		verified := r.Verified
		if verified == "" {
			verified = "-"
		}
		fmt.Fprintf(&b, "  %-12s installed %-10s verified %-10s %s\n",
			r.Name, installed, verified, r.Status.label())
	}
	return b.String()
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `$GO test ./internal/doctor/ -run TestRender -v`
Expected: PASS

- [ ] **Step 5: Add the `atrium doctor` subcommand**

In `main.go`, add the import (in the existing import block, grouped with other `internal/*` imports):

```go
	"github.com/ZviBaratz/atrium/internal/doctor"
```

Add a new command var after `updateCmd`'s closing `}` (before the closing `)` of the var block at line 278):

```go
	doctorCmd = &cobra.Command{
		Use:   "doctor",
		Short: "Check installed agent CLIs against Atrium's verified heuristic versions",
		Long: "Probes installed agent CLIs (claude, codex, gemini, aider) and reports whether each\n" +
			"one's version has drifted past the version Atrium's pane-classification heuristics were\n" +
			"verified against. Drift means a session's status (busy / needs-input / idle) may be\n" +
			"misread; re-verify the matcher strings in session/agent/registry.go.",
		RunE: func(cmd *cobra.Command, args []string) error {
			log.Initialize(false)
			defer log.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			fmt.Print(doctor.Render(doctor.CheckInstalled(ctx)))
			return nil
		},
	}
```

In `init()` (around line 307), register it:

```go
	rootCmd.AddCommand(doctorCmd)
```

- [ ] **Step 6: Build and verify the command exists**

Run: `GO=/home/zvi/.local/share/mise/installs/go/latest/bin/go just build && ./bin/atrium doctor`
Expected: build succeeds; prints the "Agent heuristics:" table for the local environment (e.g. claude shows `⚠ drifted` since installed > 2.1.170).

- [ ] **Step 7: Commit**

```bash
git add internal/doctor/render.go internal/doctor/render_test.go main.go
git commit -m "feat(doctor): add atrium doctor subcommand with drift table" -m "Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 6: Persist drift acknowledgement in `config.State`

**Files:**
- Modify: `config/state.go` (add `AckedDrift` field, `AppState` interface methods, `State` methods)
- Test: `config/drift_ack_test.go` (create)

**Interfaces:**
- Produces (on `config.AppState` and `*config.State`):
  - `GetAckedDrift() map[string]string` — agent-key → acknowledged installed version (never nil).
  - `SetAckedDrift(key, version string) error` — records and persists.

- [ ] **Step 1: Write the failing test**

Create `config/drift_ack_test.go`:

```go
package config

import "testing"

func TestAckedDriftRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // hermetic: never touch the real data dir

	s := DefaultState()
	if got := s.GetAckedDrift(); len(got) != 0 {
		t.Fatalf("fresh state GetAckedDrift() = %v, want empty", got)
	}
	if err := s.SetAckedDrift("claude", "2.1.179"); err != nil {
		t.Fatalf("SetAckedDrift: %v", err)
	}
	if got := s.GetAckedDrift()["claude"]; got != "2.1.179" {
		t.Errorf("GetAckedDrift()[claude] = %q, want 2.1.179", got)
	}

	reloaded := LoadState()
	if got := reloaded.GetAckedDrift()["claude"]; got != "2.1.179" {
		t.Errorf("after reload, GetAckedDrift()[claude] = %q, want 2.1.179", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `$GO test ./config/ -run TestAckedDriftRoundTrip -v`
Expected: FAIL — `s.GetAckedDrift undefined`.

- [ ] **Step 3: Add the struct field**

In `config/state.go`, in the `State` struct (after `LastNotesVersion`, line 124), add:

```go
	// AckedDrift maps an agent key to the installed version the user dismissed the
	// heuristic-drift hint for. The hint stays quiet while installed == acked; a
	// later version bump re-arms it.
	AckedDrift map[string]string `json:"acked_drift,omitempty"`
```

- [ ] **Step 4: Add the interface methods**

In `config/state.go`, in the `AppState` interface (after `SetLastNotesVersion`), add:

```go
	// GetAckedDrift returns the agent-key → acknowledged-version map (never nil)
	GetAckedDrift() map[string]string
	// SetAckedDrift records the installed version the drift hint was dismissed at
	SetAckedDrift(key, version string) error
```

- [ ] **Step 5: Add the method implementations**

In `config/state.go`, after the existing `SetLastNotesVersion` method (in the `AppState interface implementation` section), add:

```go
// GetAckedDrift returns the acknowledged-drift map, never nil.
func (s *State) GetAckedDrift() map[string]string {
	if s.AckedDrift == nil {
		return map[string]string{}
	}
	return s.AckedDrift
}

// SetAckedDrift records the installed version the drift hint was dismissed at and persists it.
func (s *State) SetAckedDrift(key, version string) error {
	if s.AckedDrift == nil {
		s.AckedDrift = map[string]string{}
	}
	s.AckedDrift[key] = version
	return SaveState(s)
}
```

- [ ] **Step 6: Run test to verify it passes**

Run: `$GO test ./config/ -run TestAckedDriftRoundTrip -v`
Expected: PASS

- [ ] **Step 7: Commit**

```bash
git add config/state.go config/drift_ack_test.go
git commit -m "feat(config): persist per-agent drift-hint acknowledgement" -m "Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 7: Startup drift hint in the TUI

**Files:**
- Create: `app/app_driftcheck.go`
- Test: `app/app_driftcheck_test.go`
- Modify: `app/app.go:368` (add `m.driftCheckCmd()` to the startup batch)
- Modify: `app/app_update.go` (add a `case driftFoundMsg:` to the message switch)

**Interfaces:**
- Consumes: `doctor.CheckInstalled`, `doctor.Result`, `doctor.Drifted` (Tasks 4-5); `m.appState.GetAckedDrift/SetAckedDrift` (Task 6); `m.handleInfoNotice`, `m.hintBinName` (existing).
- Produces (package-internal to `app`):
  - `var checkDrift = doctor.CheckInstalled` (faked in tests).
  - `type driftFoundMsg struct { agents []doctor.Result }`.
  - `func (m *home) driftCheckCmd() tea.Cmd`.

- [ ] **Step 1: Write the failing test**

Create `app/app_driftcheck_test.go`:

```go
package app

import (
	"context"
	"testing"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/internal/doctor"
	"github.com/ZviBaratz/atrium/session/agent"
)

func TestDriftCheckCmdEmitsUnackedDrift(t *testing.T) {
	orig := checkDrift
	t.Cleanup(func() { checkDrift = orig })
	checkDrift = func(context.Context) []doctor.Result {
		return []doctor.Result{
			{Key: agent.KeyClaude, Name: "Claude Code", Installed: "2.1.179", Status: doctor.StatusDrifted},
			{Key: agent.KeyGemini, Name: "Gemini CLI", Installed: "0.45.1", Status: doctor.StatusOK},
		}
	}

	m := &home{ctx: context.Background(), appState: config.DefaultState()}
	msg := m.driftCheckCmd()()
	df, ok := msg.(driftFoundMsg)
	if !ok {
		t.Fatalf("driftCheckCmd returned %T, want driftFoundMsg", msg)
	}
	if len(df.agents) != 1 || df.agents[0].Key != agent.KeyClaude {
		t.Fatalf("driftFoundMsg.agents = %+v, want only claude", df.agents)
	}
}

func TestDriftCheckCmdSuppressesAcked(t *testing.T) {
	orig := checkDrift
	t.Cleanup(func() { checkDrift = orig })
	checkDrift = func(context.Context) []doctor.Result {
		return []doctor.Result{
			{Key: agent.KeyClaude, Name: "Claude Code", Installed: "2.1.179", Status: doctor.StatusDrifted},
		}
	}

	st := config.DefaultState()
	st.AckedDrift = map[string]string{"claude": "2.1.179"} // already acked at this version
	m := &home{ctx: context.Background(), appState: st}
	if msg := m.driftCheckCmd()(); msg != nil {
		t.Fatalf("driftCheckCmd returned %T, want nil (acked)", msg)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `$GO test ./app/ -run TestDriftCheckCmd -v`
Expected: FAIL — `undefined: checkDrift`, `driftFoundMsg`.

- [ ] **Step 3: Implement the command and message**

Create `app/app_driftcheck.go`:

```go
package app

// Startup agent-heuristic drift check: probes installed agent CLIs and, when one
// has drifted past Atrium's verified version (and the user hasn't already
// acknowledged that version), shows a one-line hint pointing at `atrium doctor`.
// Like the update check, it runs in a background tea.Cmd and never blocks.

import (
	"context"
	"time"

	"github.com/ZviBaratz/atrium/internal/doctor"

	tea "github.com/charmbracelet/bubbletea"
)

// driftCheckTimeout bounds the version probes so a wedged agent binary can't pin
// the startup command for the whole session.
const driftCheckTimeout = 10 * time.Second

// checkDrift is a package var so tests can fake the probe (same pattern as
// checkForUpdate).
var checkDrift = doctor.CheckInstalled

// driftFoundMsg reports drifted agents the user has not yet acknowledged at
// their current installed version.
type driftFoundMsg struct {
	agents []doctor.Result
}

// driftCheckCmd probes installed agents and emits driftFoundMsg for any drifted,
// unacknowledged agent, or nil when there is nothing to surface. The ack map is
// read on the main thread and captured, so the goroutine never touches appState.
func (m *home) driftCheckCmd() tea.Cmd {
	acked := m.appState.GetAckedDrift()
	appCtx := m.ctx
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(appCtx, driftCheckTimeout)
		defer cancel()
		var fresh []doctor.Result
		for _, r := range doctor.Drifted(checkDrift(ctx)) {
			if acked[string(r.Key)] != r.Installed {
				fresh = append(fresh, r)
			}
		}
		if len(fresh) == 0 {
			return nil
		}
		return driftFoundMsg{agents: fresh}
	}
}
```

- [ ] **Step 4: Run the command test to verify it passes**

Run: `$GO test ./app/ -run TestDriftCheckCmd -v`
Expected: PASS

- [ ] **Step 5: Wire the message handler**

In `app/app_update.go`, add a new case to the message switch, immediately after the `case updateCheckDoneMsg:` block (after line 69):

```go
	case driftFoundMsg:
		// Record the ack at each agent's current installed version so the hint
		// shows once per version, then surface it. The doctor command stays the
		// durable surface, so a missed toast is acceptable — no buffering.
		for _, r := range msg.agents {
			if err := m.appState.SetAckedDrift(string(r.Key), r.Installed); err != nil {
				log.WarningLog.Printf("failed to record drift ack for %s: %v", r.Key, err)
			}
		}
		return m, m.handleInfoNotice(fmt.Sprintf("⚠ agent heuristics may be stale — run `%s doctor`", m.hintBinName()))
```

(Verify `fmt` and `log` are already imported in `app_update.go`; they are used by adjacent cases.)

- [ ] **Step 6: Add the check to the startup batch**

In `app/app.go`, at line 368 where `m.updateCheckCmd()` is added to the `tea.Batch`, add the drift check alongside it:

```go
		m.updateCheckCmd(),   // nil (inert) is fine: tea.Batch skips nil cmds
		m.driftCheckCmd(),    // agent-heuristic drift hint
```

- [ ] **Step 7: Run the full package test + build**

Run: `$GO test ./app/ -v 2>&1 | tail -20`
Expected: PASS (existing app tests unaffected; new drift tests pass).

Run: `GO=/home/zvi/.local/share/mise/installs/go/latest/bin/go just build`
Expected: build succeeds.

- [ ] **Step 8: Commit**

```bash
git add app/app_driftcheck.go app/app_driftcheck_test.go app/app.go app/app_update.go
git commit -m "feat(app): show acknowledgeable startup hint when agent heuristics drift" -m "Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 8: Document the remediation policy + full verification

**Files:**
- Modify: `session/agent/registry.go` (extend the top-of-file convention comment)

**Interfaces:** none (documentation + verification).

- [ ] **Step 1: Extend the convention comment**

In `session/agent/registry.go`, the comment block at lines 8-13 ends with "add a fixture to registry_test.go pinning the new string against a captured pane." Extend it so the version ceiling and additive policy are part of the documented ritual. Replace the existing paragraph (lines 11-13):

```go
// Heuristic strings are version-sensitive by nature. When editing, add a fixture
// to registry_test.go pinning the new string against a captured pane.
```

with:

```go
// Heuristic strings are version-sensitive by nature. When editing, add a fixture
// to registry_test.go pinning the new string against a captured pane, and bump
// the adapter's VerifiedVersion to the version you captured against (the drift
// guard in internal/doctor warns when an installed CLI moves past it).
//
// Remediation is ADDITIVE, never replace-in-place: when a CLI rewords a gating
// string, ADD the new variant alongside the old in the same matcher list and
// keep both through a deprecation window, e.g.
//   // claude >=2.1.180; "No, keep planning" kept for <2.1.180, remove after.
// A union match can't guess wrong (a pane shows only one variant), so matching
// never depends on the detected version. A plain re-verification (strings still
// valid at a newer release) is just a VerifiedVersion bump, no string edit.
```

- [ ] **Step 2: Run the full hermetic suite**

Run: `GO=/home/zvi/.local/share/mise/installs/go/latest/bin/go just test 2>&1 | tail -30`
Expected: all packages PASS.

- [ ] **Step 3: Lint + vet + fmt-check**

Run: `GO=/home/zvi/.local/share/mise/installs/go/latest/bin/go just fmt-check && GO=/home/zvi/.local/share/mise/installs/go/latest/bin/go just vet`
Expected: no diffs, no vet errors. (If `just lint` is available and golangci-lint is installed, run `GO=... just lint` too.)

- [ ] **Step 4: Manual smoke of the doctor command**

Run: `GO=/home/zvi/.local/share/mise/installs/go/latest/bin/go just build && ./bin/atrium doctor`
Expected: prints the table; claude shows `⚠ drifted` (installed 2.1.179 > verified 2.1.170), gemini shows `⚠ drifted` (0.45.1 > 0.27, minor).

- [ ] **Step 5: Commit**

```bash
git add session/agent/registry.go
git commit -m "docs(agent): document verified-version ceiling and additive remediation policy" -m "Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Self-Review

**Spec coverage:**
- `VerifiedVersion` field + ceiling semantics → Task 1, 8. ✓
- Per-adapter `DriftGranularity` (claude=patch, gemini=minor, default=patch) → Task 1, 3. ✓
- `agent.Adapters()` accessor, `agent` stays pure → Task 1. ✓
- Version probe via injectable runner, `internal/doctor` package, `Masterminds/semver/v3` → Tasks 2-4. ✓
- `>`-only + granularity comparison, unknown/not-installed handling → Tasks 3-4. ✓
- `atrium doctor` full table → Task 5. ✓
- Acknowledgeable startup hint for any drifted agent on PATH, ack in `config.State` → Tasks 6-7. ✓
- Additive-with-deprecation remediation policy (comment convention) → Task 8. ✓
- Hermetic tests throughout → every task. ✓
- Non-goals (live probe, version-selected matchers, structured deprecation tracking, CI gating, bump-enforcing test) → none implemented, as intended. ✓

**Placeholder scan:** No TBD/TODO/"handle edge cases"/"similar to Task N". Every code step shows complete code. ✓

**Type consistency:** `Granularity`/`GranularityPatch|Minor|Major` (Task 1) used in Tasks 3-4. `Result`/`Status`/`StatusOK|Drifted|Unknown|NotInstalled` (Task 4) used in Tasks 5, 7. `Runner.version`/`ErrNotInstalled`/`Check`/`CheckInstalled`/`Drifted` (Task 4) used in Tasks 5, 7. `driftFoundMsg`/`checkDrift` (Task 7) consistent across command and handler. `GetAckedDrift`/`SetAckedDrift` (Task 6) used in Task 7. ✓
