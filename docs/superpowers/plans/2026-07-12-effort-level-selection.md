# Effort-Level Selection Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add an **Effort** chip picker to the new-session create form that folds `claude --effort <level>` into the launched command, per-session.

**Architecture:** Effort rides the persisted `Program` string exactly like the existing `--model` / `--permission-mode` overrides — a new `agent.WithEffortFlag` composed in `composeProgramFlags`, and a new `EffortField` chip widget (a sibling of `ModeField`) wired into the create form. No new instance/state/serialization: the flag inside `Program` already survives save/load, pause/resume, and the daemon.

**Tech Stack:** Go, Bubble Tea (`github.com/charmbracelet/bubbletea`), lipgloss, testify (`stretchr/testify`). Module `github.com/ZviBaratz/atrium`.

**Spec:** `docs/superpowers/specs/2026-07-12-effort-level-selection-design.md`.

## Global Constraints

- **Build/test via `just`:** `just build` → `./bin/atrium`; `just test` is the source of truth. If `go`/`just` fail with a mise shim error, use `GO=/path/to/go just test` (see CLAUDE.md). Targeted runs use `go test ./<pkg>/ -run <Name> -v`.
- **Confirm every change with `just build` AND `just test` before considering it done.**
- **Run `just fmt` before each commit** (CI enforces `just fmt-check`; inserting struct fields / predicates shifts gofmt's column alignment).
- **Tests stay hermetic:** never read/write the user's real data dir; `app`/`ui` tests inherit the package `TestMain` (temp `HOME`). The one test that shells out to `claude` self-skips when it is absent and runs under a temp `HOME`.
- **Shell-safety:** the `Program` string is handed to `sh -c` by tmux, so any interpolated value must be shell-metacharacter-free. Effort values come only from a closed chip set, but `ValidEffort` is the backstop.
- **Effort levels (verbatim):** `low, medium, high, xhigh, max`. The picker adds a leading no-op `"default"` chip (no flag). **`ultracode` is excluded** — it is not a valid `--effort` value.
- **No per-model gating:** pass the level through; the CLI degrades unknown/unsupported values to default effort with a warning. Do not add compatibility validation.
- **Commits:** Conventional Commits, lowercase. End every commit message with the trailer:
  `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`

## File Structure

**Create:**
- `session/agent/effort.go` — effort vocabulary (`ClaudeEffortLevels`, `ClaudeEffortLabels`, `ValidEffort`, `WithEffortFlag`, `EffortFlag`). Mirrors `permissionmode.go`.
- `session/agent/effort_test.go` — unit tests for the vocabulary.
- `session/agent/effort_drift_test.go` — tripwire that pins `ClaudeEffortLevels` to the installed CLI.
- `ui/overlay/effortField.go` — the `EffortField` chip widget. Mirrors `modeField.go`.
- `ui/overlay/effortField_test.go` — widget unit tests.
- `ui/overlay/textInput_effort_test.go` — form-integration tests (presence/absence/selection/disable).

**Modify:**
- `ui/overlay/textInput.go` — add the `effortField` struct field.
- `ui/overlay/textInput_focus.go` — `stopEffort`, `isEffortField`, `stopEnabled`, `updateFocusState`.
- `ui/overlay/textInput_create.go` — construct + gate the field; `GetEffort` accessor.
- `ui/overlay/textInput_keys.go` — route keys to the effort field.
- `ui/overlay/textInput_render.go` — render the effort section.
- `ui/overlay/textInput_size.go` — `effortSectionLines` + `fitRows` chrome.
- `app/app_session.go` — extend `composeProgramFlags` with `effort`; update the call site.
- `app/newsession_test.go` — update `TestComposeProgramFlags` for the new signature + effort cases.

**Task order & dependencies:** 1 → 2 (needs 1) → 3 (needs 1) → 4 (needs 3) → 5 (needs 1 + 4). Each task builds and tests green on its own.

---

### Task 1: Effort vocabulary in `session/agent`

**Files:**
- Create: `session/agent/effort.go`
- Test: `session/agent/effort_test.go`

**Interfaces:**
- Consumes: `withFlag(program, name, value string) string` (existing, `session/agent/model.go:76`).
- Produces:
  - `ClaudeEffortLevels []string` = `{"low","medium","high","xhigh","max"}`
  - `ClaudeEffortLabels []string` (same values, parallel slice)
  - `ValidEffort(s string) bool`
  - `WithEffortFlag(program, level string) string`
  - `EffortFlag(program string) string`

- [ ] **Step 1: Write the failing tests**

Create `session/agent/effort_test.go`:

```go
package agent

import "testing"

func TestValidEffort(t *testing.T) {
	for _, level := range []string{"low", "medium", "high", "xhigh", "max"} {
		if !ValidEffort(level) {
			t.Errorf("ValidEffort(%q) = false, want true", level)
		}
	}
	for _, bad := range []string{"", "ultracode", "LOW", "extreme", "hi"} {
		if ValidEffort(bad) {
			t.Errorf("ValidEffort(%q) = true, want false", bad)
		}
	}
}

func TestClaudeEffortLabels_ParityWithLevels(t *testing.T) {
	if len(ClaudeEffortLabels) != len(ClaudeEffortLevels) {
		t.Fatalf("labels len %d != levels len %d", len(ClaudeEffortLabels), len(ClaudeEffortLevels))
	}
}

func TestWithEffortFlag(t *testing.T) {
	tests := []struct{ name, program, level, want string }{
		{"append to bare", "claude", "xhigh", "claude --effort xhigh"},
		{"append after other flags", "claude --model opus", "high", "claude --model opus --effort high"},
		{"replace existing pin", "claude --effort low", "max", "claude --effort max"},
		{"replace combined form", "claude --effort=low", "max", "claude --effort max"},
		{"quoted program appends, last wins", `claude --effort low --settings '/a b'`, "high", `claude --effort low --settings '/a b' --effort high`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := WithEffortFlag(tt.program, tt.level); got != tt.want {
				t.Errorf("WithEffortFlag(%q, %q) = %q, want %q", tt.program, tt.level, got, tt.want)
			}
		})
	}
}

func TestEffortFlag(t *testing.T) {
	tests := []struct{ program, want string }{
		{"claude", ""},
		{"claude --effort xhigh", "xhigh"},
		{"claude --effort=high", "high"},
		{"claude --effort low --effort max", "max"}, // last wins
	}
	for _, tt := range tests {
		if got := EffortFlag(tt.program); got != tt.want {
			t.Errorf("EffortFlag(%q) = %q, want %q", tt.program, got, tt.want)
		}
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./session/agent/ -run 'TestValidEffort|TestWithEffortFlag|TestEffortFlag|TestClaudeEffortLabels' -v`
Expected: FAIL — compile error, `undefined: ValidEffort` (and the other symbols).

- [ ] **Step 3: Write the implementation**

Create `session/agent/effort.go`:

```go
package agent

import "strings"

// ClaudeEffortLevels are the reasoning-effort levels the create form offers as
// chips (after the field's own "default" chip). The claude CLI (2.1.207 --help)
// documents exactly these for --effort: "Effort level for the current session
// (low, medium, high, xhigh, max)". Unlike --permission-mode the CLI does not
// reject an unknown value — it prints a warning and falls back to the default
// effort — so this list is the offered set, not a hard gate.
// TestClaudeEffortLevels_MatchInstalledCLI pins it to the installed binary.
var ClaudeEffortLevels = []string{"low", "medium", "high", "xhigh", "max"}

// ClaudeEffortLabels are the display labels for ClaudeEffortLevels, in the same
// order. Effort labels are identical to their values (no kebab-casing needed);
// the parallel slice mirrors chipRow's options/labels contract used by the mode
// and profile fields.
var ClaudeEffortLabels = []string{"low", "medium", "high", "xhigh", "max"}

// claudeEffortEnum is the offered level set as a lookup — the validation backstop
// behind the closed chip set.
var claudeEffortEnum = map[string]bool{
	"low": true, "medium": true, "high": true, "xhigh": true, "max": true,
}

// ValidEffort reports whether s is an --effort level Atrium offers. A cheap
// backstop behind the chip set (the field is the only source of values):
// composeProgramFlags errors on a miss so UI/enum drift is caught before launch,
// rather than silently handed to the CLI (which would only warn-and-ignore it).
func ValidEffort(s string) bool { return claudeEffortEnum[s] }

// EffortFlag returns the value of an --effort pin in program ("" = none), the
// extraction counterpart of WithEffortFlag. Agent-neutral argv parsing; the last
// pin wins, matching the CLI's argv semantics.
func EffortFlag(program string) string {
	fields := strings.Fields(program)
	value := ""
	for n, f := range fields {
		if v, ok := strings.CutPrefix(f, "--effort="); ok {
			value = v
		}
		if f == "--effort" && n+1 < len(fields) {
			value = fields[n+1]
		}
	}
	return value
}

// WithEffortFlag returns program with `--effort level` applied: verbatim append
// when the program carries no pin, replace when it does (see withFlag).
func WithEffortFlag(program, level string) string {
	return withFlag(program, "--effort", level)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./session/agent/ -run 'TestValidEffort|TestWithEffortFlag|TestEffortFlag|TestClaudeEffortLabels' -v`
Expected: PASS (all subtests ok).

- [ ] **Step 5: Commit**

```bash
git add session/agent/effort.go session/agent/effort_test.go
git commit -m "feat(agent): add claude --effort vocabulary and flag composer

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: Drift tripwire — pin the level list to the installed CLI

**Files:**
- Test: `session/agent/effort_drift_test.go`

**Interfaces:**
- Consumes: `ClaudeEffortLevels` (Task 1).
- Produces: nothing (test-only).

- [ ] **Step 1: Write the test**

Create `session/agent/effort_drift_test.go`:

```go
package agent

import (
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// TestClaudeEffortLevels_MatchInstalledCLI sources the valid --effort levels from
// the installed claude binary and asserts ClaudeEffortLevels matches, so a CLI
// that adds or removes a level trips this test rather than silently drifting. It
// self-skips when claude is not on PATH (like the real-tmux tests) and runs under
// a temp HOME so it never reads the user's real config. `--help` short-circuits
// before any session/API call (exit 0, no network).
func TestClaudeEffortLevels_MatchInstalledCLI(t *testing.T) {
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude not on PATH; skipping effort-level drift check")
	}
	cmd := exec.Command("claude", "--effort", "__atrium_drift_probe__", "--help")
	cmd.Env = append(os.Environ(), "HOME="+t.TempDir())
	out, _ := cmd.CombinedOutput() // exit 0 expected; parse regardless

	// Prefer the warning line: "... Valid values: low, medium, high, xhigh, max."
	m := regexp.MustCompile(`Valid values:\s*([a-z, ]+?)\.`).FindSubmatch(out)
	if m == nil {
		// Fallback: the --help line "Effort level for the current session (a, b, c)".
		m = regexp.MustCompile(`Effort level for the current session \(([a-z, ]+)\)`).FindSubmatch(out)
	}
	if m == nil {
		t.Skipf("no parseable effort-level list in claude output; format may have changed:\n%s", out)
	}

	var got []string
	for _, part := range strings.Split(string(m[1]), ",") {
		if s := strings.TrimSpace(part); s != "" {
			got = append(got, s)
		}
	}
	want := append([]string(nil), ClaudeEffortLevels...)
	sort.Strings(got)
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("installed claude --effort levels = %v; ClaudeEffortLevels = %v — update session/agent/effort.go", got, want)
	}
}
```

- [ ] **Step 2: Run the test**

Run: `go test ./session/agent/ -run TestClaudeEffortLevels_MatchInstalledCLI -v`
Expected: PASS when `claude` is on PATH and reports `low, medium, high, xhigh, max`; otherwise `--- SKIP`. Either is acceptable (do NOT hard-fail if it skips).

- [ ] **Step 3: Commit**

```bash
git add session/agent/effort_drift_test.go
git commit -m "test(agent): pin effort levels to the installed claude CLI

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 3: `EffortField` chip widget

**Files:**
- Create: `ui/overlay/effortField.go`
- Test: `ui/overlay/effortField_test.go`

**Interfaces:**
- Consumes: `agent.ClaudeEffortLevels`, `agent.ClaudeEffortLabels` (Task 1); `chipRow` and its methods `moveCursor`, `selected`, `render`, `Focus`, `Blur`, `SetDisabled`, `Disabled` (existing, `ui/overlay/chiprow.go`); `claudeFieldNA` (`chiprow.go:12`); `mfLabelStyle()`, `mfDimStyle()` (`ui/overlay/modelField.go:184-185`).
- Produces:
  - `type EffortField struct{ chipRow }`
  - `NewEffortField() *EffortField`
  - `(*EffortField) HandleKeyPress(tea.KeyMsg)`
  - `(*EffortField) Value() string`
  - `(*EffortField) Render() string`

- [ ] **Step 1: Write the failing tests**

Create `ui/overlay/effortField_test.go`:

```go
package overlay

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestEffortField_DefaultChipContributesNoFlag(t *testing.T) {
	f := NewEffortField()
	if got := f.Value(); got != "" {
		t.Errorf("new EffortField Value() = %q, want \"\" (default chip)", got)
	}
}

func TestEffortField_CycleSelectsLevel(t *testing.T) {
	f := NewEffortField()
	f.Focus()
	f.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRight}) // default -> low
	if got := f.Value(); got != "low" {
		t.Errorf("after one Right, Value() = %q, want \"low\"", got)
	}
	f.HandleKeyPress(tea.KeyMsg{Type: tea.KeyLeft}) // low -> default
	if got := f.Value(); got != "" {
		t.Errorf("back on default, Value() = %q, want \"\"", got)
	}
}

func TestEffortField_DisabledContributesNoFlag(t *testing.T) {
	f := NewEffortField()
	f.Focus()
	f.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRight}) // default -> low
	f.SetDisabled(true)
	if got := f.Value(); got != "" {
		t.Errorf("disabled Value() = %q, want \"\"", got)
	}
	f.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRight}) // disabled: no-op
	if got := f.Value(); got != "" {
		t.Errorf("disabled after key, Value() = %q, want \"\"", got)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./ui/overlay/ -run TestEffortField -v`
Expected: FAIL — compile error, `undefined: NewEffortField`.

- [ ] **Step 3: Write the implementation**

Create `ui/overlay/effortField.go`:

```go
package overlay

import (
	"strings"

	"github.com/ZviBaratz/atrium/session/agent"
	tea "github.com/charmbracelet/bubbletea"
)

// effortInherit is the chip that contributes no --effort flag. Labeled "default"
// (matching the ModeField idiom): no flag means claude uses its resolved default
// effort — the user's settings.json effortLevel, or the built-in default — which
// is exactly what the chip should preserve.
const effortInherit = "default"

// EffortField is the create form's optional Claude reasoning-effort override: a
// pure chip row over agent.ClaudeEffortLevels, a sibling of ModeField. The chosen
// level rides the persisted Program string as --effort, so it is re-applied on
// pause/resume and by the daemon (matching --model / --permission-mode). Unlike
// --permission-mode the CLI does not reject an unknown level (it warns and uses
// the default), so an unsupported-for-model pick degrades gracefully rather than
// killing the launch. The field is disabled (dim, skipped in Tab order,
// Value() == "") while the effective program does not resolve to claude,
// mirroring ModelField / ModeField.
type EffortField struct {
	chipRow
}

// NewEffortField builds the effort field, starting on the default chip.
func NewEffortField() *EffortField {
	return &EffortField{chipRow{
		options: append([]string{effortInherit}, agent.ClaudeEffortLevels...),
		labels:  append([]string{effortInherit}, agent.ClaudeEffortLabels...),
	}}
}

// HandleKeyPress cycles the chips with the arrow keys; every other key is a
// no-op (see chipRow.moveCursor).
func (f *EffortField) HandleKeyPress(msg tea.KeyMsg) {
	if f.disabled {
		return
	}
	f.moveCursor(msg)
}

// Value returns the effort override, or "" when the field should contribute no
// flag: disabled, or sitting on the default chip.
func (f *EffortField) Value() string { return f.selected() }

// Render renders the field: label + a constant-height hint row, then the chip
// row, so the form never jumps as focus changes. Disabled renders a dim
// placeholder instead, mirroring the model and mode fields' inert state.
func (f *EffortField) Render() string {
	var s strings.Builder
	s.WriteString(mfLabelStyle().Render("Effort"))
	if f.disabled {
		s.WriteString("\n\n")
		s.WriteString(mfDimStyle().Render(claudeFieldNA))
		return s.String()
	}
	if f.focused {
		s.WriteString(mfDimStyle().Render("  ↑↓ change"))
	}
	s.WriteString("\n\n")
	s.WriteString(f.render())
	return s.String()
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./ui/overlay/ -run TestEffortField -v`
Expected: PASS (all three tests ok).

- [ ] **Step 5: Commit**

```bash
git add ui/overlay/effortField.go ui/overlay/effortField_test.go
git commit -m "feat(overlay): add EffortField chip widget

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 4: Wire the effort field into the new-session form

**Files:**
- Modify: `ui/overlay/textInput.go` (struct field)
- Modify: `ui/overlay/textInput_focus.go` (enum, `isEffortField`, `stopEnabled`, `updateFocusState`)
- Modify: `ui/overlay/textInput_create.go` (construct + gate + `GetEffort`)
- Modify: `ui/overlay/textInput_keys.go` (key routing)
- Modify: `ui/overlay/textInput_render.go` (render section)
- Modify: `ui/overlay/textInput_size.go` (`effortSectionLines`, `fitRows`)
- Test: `ui/overlay/textInput_effort_test.go`

**Interfaces:**
- Consumes: `NewEffortField`, `EffortField` (Task 3); existing form seams (`focusStop`, `stops`, `syncClaudeFieldsEnabled`, `section`, `fitRows`).
- Produces:
  - `stopEffort` (focusStop)
  - `(*TextInputOverlay) isEffortField() bool`
  - `(*TextInputOverlay) GetEffort() string`
  - `effortField *EffortField` field on `TextInputOverlay`

- [ ] **Step 1: Write the failing integration tests**

Create `ui/overlay/textInput_effort_test.go`:

```go
package overlay

import (
	"testing"

	"github.com/ZviBaratz/atrium/config"
	tea "github.com/charmbracelet/bubbletea"
)

func claudeProfiles() []config.Profile {
	return []config.Profile{{Name: "claude", Program: "claude"}}
}

func TestCreateForm_EffortField_PresentForClaude(t *testing.T) {
	ov := NewSessionCreateOverlay(claudeProfiles(), nil, []string{t.TempDir()}, "claude")
	if ov.effortField == nil {
		t.Fatal("effort field should exist for a claude program")
	}
	if ov.GetEffort() != "" {
		t.Errorf("fresh form GetEffort() = %q, want \"\"", ov.GetEffort())
	}
	if ov.indexOfStop(stopEffort) < 0 {
		t.Error("stopEffort should be in the focus ring for a claude profile")
	}
}

func TestCreateForm_EffortField_SelectionReadOut(t *testing.T) {
	ov := NewSessionCreateOverlay(claudeProfiles(), nil, []string{t.TempDir()}, "claude")
	ov.focusStop(stopEffort)
	ov.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRight}) // default -> low
	if got := ov.GetEffort(); got != "low" {
		t.Errorf("GetEffort() after Right = %q, want \"low\"", got)
	}
}

func TestCreateForm_EffortField_AbsentForNonClaude(t *testing.T) {
	profiles := []config.Profile{{Name: "aider", Program: "aider"}}
	ov := NewSessionCreateOverlay(profiles, nil, []string{t.TempDir()}, "aider")
	if ov.effortField != nil {
		t.Error("effort field should be nil when no candidate program is claude")
	}
	if ov.GetEffort() != "" {
		t.Errorf("GetEffort() with no field = %q, want \"\"", ov.GetEffort())
	}
	if ov.indexOfStop(stopEffort) >= 0 {
		t.Error("stopEffort should not be in the focus ring for a non-claude form")
	}
}

func TestCreateForm_EffortField_DisabledForNonClaudeProfile(t *testing.T) {
	profiles := []config.Profile{
		{Name: "claude", Program: "claude"},
		{Name: "aider", Program: "aider"},
	}
	ov := NewSessionCreateOverlay(profiles, nil, []string{t.TempDir()}, "claude")
	ov.focusStop(stopProfile)
	ov.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRight}) // claude -> aider
	if !ov.effortField.Disabled() {
		t.Error("effort field should be disabled when a non-claude profile is selected")
	}
	if ov.GetEffort() != "" {
		t.Errorf("disabled GetEffort() = %q, want \"\"", ov.GetEffort())
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./ui/overlay/ -run TestCreateForm_EffortField -v`
Expected: FAIL — compile error, `undefined: stopEffort` / `ov.effortField undefined` / `ov.GetEffort undefined`.

- [ ] **Step 3: Add the struct field** (`ui/overlay/textInput.go`)

Find (around line 28-31):

```go
	modelField      *ModelField
	modeField       *ModeField
	accountPicker   *AccountPicker
```

Replace with:

```go
	modelField      *ModelField
	modeField       *ModeField
	effortField     *EffortField
	accountPicker   *AccountPicker
```

- [ ] **Step 4: Add the focus stop, predicate, enabled-gate, and focus fan-out** (`ui/overlay/textInput_focus.go`)

4a. In the `focusStop` const block, find:

```go
	stopModel
	stopMode
	stopAccount
```

Replace with:

```go
	stopModel
	stopMode
	stopEffort
	stopAccount
```

4b. After the `isModeField` predicate, find:

```go
func (t *TextInputOverlay) isModeField() bool       { return t.currentStop() == stopMode }
```

Add on the next line:

```go
func (t *TextInputOverlay) isEffortField() bool     { return t.currentStop() == stopEffort }
```

4c. In `stopEnabled`, find:

```go
	if kind == stopModel && t.modelField != nil && t.modelField.Disabled() {
		return false
	}
	return true
```

Replace with:

```go
	if kind == stopModel && t.modelField != nil && t.modelField.Disabled() {
		return false
	}
	if kind == stopEffort && t.effortField != nil && t.effortField.Disabled() {
		return false
	}
	return true
```

4d. In `updateFocusState`, find the `modeField` block:

```go
	if t.modeField != nil {
		if t.isModeField() {
			t.modeField.Focus()
		} else {
			t.modeField.Blur()
		}
	}
```

Add immediately after it:

```go
	if t.effortField != nil {
		if t.isEffortField() {
			t.effortField.Focus()
		} else {
			t.effortField.Blur()
		}
	}
```

- [ ] **Step 5: Construct, gate, and expose the field** (`ui/overlay/textInput_create.go`)

5a. In `NewSessionCreateOverlay`, find the claude-field construction:

```go
	var mf *ModelField
	var pmf *ModeField
	for _, c := range candidates {
		if agent.Resolve(c).Key == agent.KeyClaude {
			mf = NewModelField()
			pmf = NewModeField()
			break
		}
	}
```

Replace with:

```go
	var mf *ModelField
	var pmf *ModeField
	var ef *EffortField
	for _, c := range candidates {
		if agent.Resolve(c).Key == agent.KeyClaude {
			mf = NewModelField()
			pmf = NewModeField()
			ef = NewEffortField()
			break
		}
	}
```

5b. In the focus-ring assembly, find:

```go
	if mf != nil {
		stops = append(stops, stopModel)
	}
	if pmf != nil {
		stops = append(stops, stopMode)
	}
```

Replace with (effort goes between model and mode — Model → Effort → Permissions):

```go
	if mf != nil {
		stops = append(stops, stopModel)
	}
	if ef != nil {
		stops = append(stops, stopEffort)
	}
	if pmf != nil {
		stops = append(stops, stopMode)
	}
```

5c. In the `&TextInputOverlay{...}` literal, find:

```go
		modelField:      mf,
		modeField:       pmf,
```

Replace with:

```go
		modelField:      mf,
		modeField:       pmf,
		effortField:     ef,
```

5d. In `syncClaudeFieldsEnabled`, find:

```go
	t.modelField.SetDisabled(disabled)
	t.modeField.SetDisabled(disabled)
```

Replace with:

```go
	t.modelField.SetDisabled(disabled)
	t.modeField.SetDisabled(disabled)
	t.effortField.SetDisabled(disabled)
```

(The existing `if t.modelField == nil { return }` guard at the top of the function covers `effortField` too — the three fields are created together.)

5e. After the `GetPermissionMode` accessor, add:

```go
// GetEffort returns the selected Claude effort-level override, or "" when no
// flag should be composed: no effort field, the field is inert (non-claude
// profile selected), or it sits on the default chip.
func (t *TextInputOverlay) GetEffort() string {
	if t.effortField == nil {
		return ""
	}
	return t.effortField.Value()
}
```

- [ ] **Step 6: Route keys to the field** (`ui/overlay/textInput_keys.go`)

In the `default:` block, find the `isModeField` branch:

```go
			if t.isModeField() {
				// No pre-filter: the field itself acts only on arrow keys (unlike the
				// profile picker's filter above, which is load-bearing for the sync call).
				t.modeField.HandleKeyPress(msg)
				return false, false
			}
```

Add immediately after it:

```go
			if t.isEffortField() {
				t.effortField.HandleKeyPress(msg)
				return false, false
			}
```

- [ ] **Step 7: Render the section** (`ui/overlay/textInput_render.go`)

In `renderCreateForm`, find:

```go
	if t.modelField != nil {
		section(t.modelField.Render())
	}
	if t.modeField != nil {
		section(t.modeField.Render())
	}
```

Replace with (effort between model and mode, matching focus order):

```go
	if t.modelField != nil {
		section(t.modelField.Render())
	}
	if t.effortField != nil {
		section(t.effortField.Render())
	}
	if t.modeField != nil {
		section(t.modeField.Render())
	}
```

- [ ] **Step 8: Size the section** (`ui/overlay/textInput_size.go`)

8a. In the `const` block, find:

```go
	// modeSectionLines is the height the permission-mode section adds when present,
	// mirroring modelSectionLines (label + blank + chips row + a divider).
	modeSectionLines = 4
```

Add immediately after it (before the closing `)`):

```go
	// effortSectionLines is the height the effort section adds when present,
	// mirroring modeSectionLines (label + blank + chips row + a divider).
	effortSectionLines = 4
```

8b. In `fitRows`, find:

```go
	if t.modeField != nil {
		chrome += modeSectionLines
	}
```

Add immediately after it:

```go
	if t.effortField != nil {
		chrome += effortSectionLines
	}
```

(Do **not** add a `SetWidth` call in `SetSize`: `EffortField` is a pure `chipRow`, which has no width to set — only the free-text `ModelField` is width-managed there.)

- [ ] **Step 9: Run the integration tests to verify they pass**

Run: `go test ./ui/overlay/ -run TestCreateForm_EffortField -v`
Expected: PASS (all four tests ok).

- [ ] **Step 10: Run the full overlay package to check for regressions**

Run: `go test ./ui/overlay/ -v`
Expected: PASS (no existing overlay test broken by the new focus stop / render section).

- [ ] **Step 11: Commit**

```bash
git add ui/overlay/textInput.go ui/overlay/textInput_focus.go ui/overlay/textInput_create.go ui/overlay/textInput_keys.go ui/overlay/textInput_render.go ui/overlay/textInput_size.go ui/overlay/textInput_effort_test.go
git commit -m "feat(overlay): wire the effort picker into the new-session form

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 5: Fold the selected effort into the launch command

**Files:**
- Modify: `app/app_session.go` (`composeProgramFlags` signature + body + call site + doc comment)
- Modify: `app/newsession_test.go` (`TestComposeProgramFlags`)

**Interfaces:**
- Consumes: `agent.ValidEffort`, `agent.WithEffortFlag` (Task 1); `(*TextInputOverlay) GetEffort()` (Task 4).
- Produces: `composeProgramFlags(program, model, mode, effort string) (string, error)` (new 4-arg signature).

- [ ] **Step 1: Update the existing tests for the new signature + add effort cases** (`app/newsession_test.go`)

Replace the whole `TestComposeProgramFlags` function (currently at `app/newsession_test.go:568-594`) with:

```go
func TestComposeProgramFlags(t *testing.T) {
	t.Run("invalid model name is rejected", func(t *testing.T) {
		_, err := composeProgramFlags("claude", "bad model!", "", "")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid model name")
	})
	t.Run("invalid permission mode is rejected", func(t *testing.T) {
		_, err := composeProgramFlags("claude", "", "bogusmode", "")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid permission mode")
	})
	t.Run("invalid effort level is rejected", func(t *testing.T) {
		_, err := composeProgramFlags("claude", "", "", "ultracode")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid effort level")
	})
	t.Run("valid model, mode, and effort compose onto a claude program", func(t *testing.T) {
		got, err := composeProgramFlags("claude", "opus", "plan", "xhigh")
		require.NoError(t, err)
		assert.Equal(t, "claude --model opus --permission-mode plan --effort xhigh", got)
	})
	t.Run("effort alone composes", func(t *testing.T) {
		got, err := composeProgramFlags("claude", "", "", "high")
		require.NoError(t, err)
		assert.Equal(t, "claude --effort high", got)
	})
	t.Run("a non-claude program is left untouched", func(t *testing.T) {
		got, err := composeProgramFlags("echo", "opus", "plan", "xhigh")
		require.NoError(t, err)
		assert.Equal(t, "echo", got)
	})
	t.Run("empty overrides leave the program untouched", func(t *testing.T) {
		got, err := composeProgramFlags("claude", "", "", "")
		require.NoError(t, err)
		assert.Equal(t, "claude", got)
	})
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./app/ -run TestComposeProgramFlags -v`
Expected: FAIL — `too many arguments in call to composeProgramFlags` (still 3-arg).

- [ ] **Step 3: Extend `composeProgramFlags`** (`app/app_session.go`)

Replace the function (currently `app/app_session.go:956-970`) with:

```go
func composeProgramFlags(program, model, mode, effort string) (string, error) {
	if model != "" && agent.Resolve(program).Key == agent.KeyClaude {
		if !agent.ValidModelName(model) {
			return "", fmt.Errorf("invalid model name %q (letters, digits, . _ : / - only)", model)
		}
		program = agent.WithModelFlag(program, model)
	}
	if mode != "" && agent.Resolve(program).Key == agent.KeyClaude {
		if !agent.ValidPermissionMode(mode) {
			return "", fmt.Errorf("invalid permission mode %q", mode)
		}
		program = agent.WithPermissionModeFlag(program, mode)
	}
	if effort != "" && agent.Resolve(program).Key == agent.KeyClaude {
		if !agent.ValidEffort(effort) {
			return "", fmt.Errorf("invalid effort level %q", effort)
		}
		program = agent.WithEffortFlag(program, effort)
	}
	return program, nil
}
```

Also update the doc comment just above the function so it names effort. Find the last sentence of the comment block ending:

```go
// order; since --model leaves the base command claude, Resolve is unaffected.
```

Replace it with:

```go
// order; since --model leaves the base command claude, Resolve is unaffected.
// Effort is composed the same way, but note the CLI soft-validates --effort
// (an unknown value is warned-and-ignored, not rejected like --permission-mode),
// so ValidEffort here is a UI/enum-drift backstop rather than a launch guard.
```

- [ ] **Step 4: Update the call site** (`app/app_session.go`)

Find (currently `app/app_session.go:1031`):

```go
	program, err := composeProgramFlags(program, ov.GetModel(), ov.GetPermissionMode())
```

Replace with:

```go
	program, err := composeProgramFlags(program, ov.GetModel(), ov.GetPermissionMode(), ov.GetEffort())
```

- [ ] **Step 5: Run the test to verify it passes**

Run: `go test ./app/ -run TestComposeProgramFlags -v`
Expected: PASS (all subtests ok).

- [ ] **Step 6: Full build + test**

Run: `just build && just test`
Expected: build succeeds (version-stamped binary at `./bin/atrium`); all tests pass. (Use `GO=/path/to/go just build && GO=/path/to/go just test` if the toolchain is not on PATH.)

- [ ] **Step 7: Commit**

```bash
git add app/app_session.go app/newsession_test.go
git commit -m "feat(app): fold the selected --effort level into the launch command

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Manual Verification (after Task 5)

- [ ] `just build` then `./bin/atrium` (or run against a scratch data dir). Press `N` with a Claude profile: the **Effort** row renders between Model and Permissions, cycles with ←/→, and the overlay height does not jump as focus moves onto/off it.
- [ ] Create a session with Effort = `xhigh`; confirm the launched command carries `--effort xhigh` (inspect the tmux pane command or the instance's persisted `Program` in `state.json`).
- [ ] Select a non-Claude profile (e.g. aider, if installed): the Effort row shows the dim `n/a — the selected profile is not Claude Code` placeholder and is skipped by Tab.
- [ ] Leave Effort on `default`: the launched command has no `--effort` flag.

## Self-Review (completed during planning)

**Spec coverage:** Injection via `--effort` in `Program` → Tasks 1 & 5. Levels `low/medium/high/xhigh/max` + `default` chip, no ultracode → Tasks 1 & 3. No per-model gating (pass-through) → Task 5 (no compatibility check added). Drift tripwire sourcing the list from claude → Task 2. `EffortField` mirroring `ModeField` + all wiring seams → Tasks 3 & 4. Claude-only gating → Task 4 (construction gated on `KeyClaude`, `syncClaudeFieldsEnabled`). `composeProgramFlags` backstop + call site → Task 5. Testing (agent unit, drift, widget, form-integration, compose) → Tasks 1-5. Non-goals (smart-auto-dispatch bypass, no config default, no draft persistence, Gemini Phase 2) require no code and are untouched.

**Placeholder scan:** none — every step carries real code, exact commands, and expected output.

**Type consistency:** `composeProgramFlags` is 4-arg (`program, model, mode, effort`) in Task 5's definition, call site, and every test call. `GetEffort()`/`effortField`/`stopEffort`/`isEffortField`/`EffortField`/`NewEffortField`/`ValidEffort`/`WithEffortFlag`/`EffortFlag` are named identically across the tasks that define and consume them. Render/focus order (Model → Effort → Mode) is consistent between the `stops` slice (Task 5 step 5b), `updateFocusState` (step 4d), and `renderCreateForm` (step 7).
