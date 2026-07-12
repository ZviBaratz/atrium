# Effort-level selection in the new-session form

## Problem

The new-session form (`n` / `N`) lets the user tune the Claude agent per session
via a **Model** override (`--model`) and a **Permissions** override
(`--permission-mode`), but not its **reasoning effort**. Claude Code exposes
effort as a first-class launch knob (`--effort <level>`), and the user routinely
wants a per-session choice (e.g. a cheap `low` scratch session vs an `xhigh`
long-horizon one) without editing a profile or their global `settings.json`.

There is no `effort`/`reasoning` symbol anywhere in the codebase today — this is
genuinely new vocabulary, but it slots into machinery that already exists.

## Goal

Add an **Effort** chip picker to the new-session create form, offered whenever the
selected profile resolves to Claude, that folds `--effort <level>` into the
launched command. Because the flag rides the persisted `Program` string (exactly
like `--model`/`--permission-mode`), the choice survives save/load, pause/resume,
and the daemon with **no new instance state, storage, or serialization**.

## Findings (research summary)

Grounded in official docs, the installed CLI binary
(`/home/zvi/.local/share/claude/versions/2.1.207`, a compiled native
executable), and empirical probes.

### Claude — a clean, graceful launch flag
- **Flag:** `claude --effort <level>` — session-only override, no file I/O.
  Registered with a custom `argParser` (soft-validates), **not** commander's
  `.choices()` (which hard-rejects). Help text is
  `Effort level for the current session (low, medium, high, xhigh, max)`.
- **Also available (not used here):** env `CLAUDE_CODE_EFFORT_LEVEL`, and
  `effortLevel` in `settings.json` (User/Project/Local scope). Precedence:
  managed > CLI flag > local > project > user.
- **Valid levels:** `low, medium, high, xhigh, max`. Confirmed as a zod
  `enum(["low","medium","high","xhigh","max"])` in the binary.
- **Unknown/unsupported values degrade gracefully — this is the load-bearing
  finding.** Empirically:
  ```
  $ claude --effort __bogus__ --help
  Warning: Unknown --effort value '__bogus__' — ignoring it and using the
  default effort. Valid values: low, medium, high, xhigh, max.
  ... (help prints, exit 0)
  ```
  An unknown value is ignored with a warning and the session boots at default
  effort (exit 0, no crash). For a *known-but-unsupported-for-model* level the
  CLI surfaces a soft in-session `"Effort not supported for {model}"` indicator
  rather than aborting.
- **Per-model support is internal and entitlement-gated.** The binary computes
  `supportedEffortLevels` per model (capability gates for `xhigh`/`max`) *and*
  applies an org-entitlement `maxEffortLevel`. None of this is exposed by any CLI
  subcommand (`agents, auth, auto-mode, doctor, gateway, install, mcp, plugin,
  project, setup-token, ultrareview, update` — no `models`/capabilities dump).
  It is also account-dependent, so Atrium cannot reliably replicate it.
- **`ultracode` is NOT an `--effort` value.** It is a separate mode ("xhigh
  effort **plus** standing dynamic-workflow orchestration") enabled via a
  settings boolean / prompt keyword. `--effort ultracode` falls into the
  "unknown value → ignored" path, so an ultracode effort chip would be a broken
  control. Excluded (see Non-goals).

### Gemini — no launch flag (Phase 2)
Gemini CLI has **no** thinking/effort launch flag or env var. The only preset
mechanism is writing `.gemini/settings.json` (`modelConfigs` block), and the knob
differs by model generation (`thinkingBudget` int for 2.5, `thinkingLevel`
named for 3.x) with per-model range clamping. That is a file-writing,
model-generation-branching sub-project, out of scope for v1. See Appendix.

## Non-goals

- **Per-model gating / compatibility validation in Atrium.** Claude is the
  authority (it knows the model *and* the account entitlements) and degrades
  gracefully. Atrium passes the level through, matching the existing `--model`
  stance ("completion sugar, never a validation allowlist",
  `session/agent/model.go`). Attempting to replicate an entitlement-dependent
  table we cannot fully see would be *less* robust, not more.
- **`ultracode` chip.** Not a valid `--effort` value (see Findings). A future
  ultracode feature would use its real settings-boolean/keyword mechanism and is
  a distinct "multi-agent mode", not an effort level.
- **A configurable default effort in `config.json`.** The picker starts on the
  inherit ("default") chip, exactly like the Model/Mode fields. No persisted
  default in v1.
- **Draft persistence.** Effort is re-derived from the target/profile like model
  and mode, not stashed in `SessionDraft` — matching
  `2026-06-24-persist-new-session-draft-design.md`.
- **Smart-auto-dispatch effort.** The confident-match auto path
  (`app/app_session.go:249`) calls `startNewSession(... m.program ...)` and
  bypasses `composeProgramFlags`, so it inherits the default effort. Only the
  form sets effort. (If wanted later, thread an effort arg through that path.)
- **Env-var / settings.json injection.** Rejected in favor of the flag (no file
  I/O, rides the `Program` string; see Decisions).
- **Gemini.** Phase 2 (Appendix), not built.

## Decisions

1. **Injection = `--effort <level>` folded into `Program`** via
   `composeProgramFlags` (`app/app_session.go:956`), using a new
   `agent.WithEffortFlag` built on the existing `withFlag`
   (`session/agent/model.go:76`). Same seam as `--model`/`--permission-mode`;
   the flag persists inside `Program` → pause/resume and daemon re-apply it for
   free.
2. **Levels = inherit + `low, medium, high, xhigh, max`.** The first chip is the
   no-op "default" (contributes no flag).
3. **Pass-through, no per-model gating** (see Non-goals).
4. **Static list + a drift tripwire test** that sources the canonical list from
   `claude` itself and asserts parity — robustness without runtime probing (see
   Testing).

## Behavior

### The Effort field
- A horizontal chip row (the Mode-field idiom): `default · low · medium · high ·
  xhigh · max`. Arrow keys cycle (wrapping); every other key is a no-op. No
  free-text (the level set is closed), matching `ModeField`.
- **First chip "default" contributes no `--effort` flag.** Claude then uses
  whatever its resolved config says (the user's `effortLevel`, or its built-in
  default).
- **Gating:** present only when some selectable program resolves to
  `agent.KeyClaude` (created together with Model/Mode); *enabled* only while the
  effective program is Claude, else rendered as the dim `claudeFieldNA`
  placeholder and skipped in Tab order. Driven by the existing
  `syncClaudeFieldsEnabled`.
- **Placement:** Profile → Model → **Effort** → Permissions → Account (effort
  groups with Model as model-behavior tuning).

### Inherit semantics under account isolation (documented caveat)
Atrium sets a per-session `CLAUDE_CONFIG_DIR` for account isolation. If that
config dir does not carry the user's global `effortLevel`, the "default" chip may
resolve to a *lower* effort than the user's `/effort` setting. Explicitly picking
a level in the form is therefore the reliable way to guarantee it — which is
extra motivation for the feature, not a bug. Noted so nobody "fixes" default to
inject the user's global level (which Atrium cannot reliably read per account).

## Implementation

All anchors are current as of this spec. Follow the `ModeField`/
`--permission-mode` precedent at each seam.

### New: `session/agent/effort.go`
Mirror `session/agent/permissionmode.go`:
```go
// ClaudeEffortLevels are the levels the create form offers as chips (after the
// field's own "default" chip). The claude CLI (2.1.207 --help) documents exactly
// these for --effort; unlike --permission-mode the CLI does not reject an unknown
// value — it warns and falls back to default effort — so this list is the offered
// set, not a hard gate. TestClaudeEffortLevels_MatchInstalledCLI pins it to the
// installed binary's own list.
var ClaudeEffortLevels = []string{"low", "medium", "high", "xhigh", "max"}

// ClaudeEffortLabels are the display labels (identical to the values; no
// kebab-casing needed), kept as a parallel slice for chipRow symmetry with mode.
var ClaudeEffortLabels = []string{"low", "medium", "high", "xhigh", "max"}

var claudeEffortEnum = map[string]bool{
    "low": true, "medium": true, "high": true, "xhigh": true, "max": true,
}

// ValidEffort reports whether s is an --effort level Atrium offers. A cheap
// backstop behind the closed chip set (the field is the only source of values);
// composition errors on a miss so UI/enum drift is caught before launch rather
// than silently passed to the CLI (which would merely warn-and-ignore it).
func ValidEffort(s string) bool { return claudeEffortEnum[s] }

// WithEffortFlag returns program with `--effort level` applied (append when no
// pin, replace when one exists — see withFlag).
func WithEffortFlag(program, level string) string { return withFlag(program, "--effort", level) }

// EffortFlag returns the value of an --effort pin in program ("" = none), the
// extraction counterpart of WithEffortFlag (agent-neutral argv parse, last wins).
func EffortFlag(program string) string { /* mirror ModelFlag: scan for --effort / --effort= */ }
```
Note: `ClaudeEffortLabels` could be derived, but effort labels == values, so a
literal parallel slice is simplest and matches `chipRow`'s `options`/`labels`
contract. `EffortFlag` is included for symmetry/testability; the UI does not need
it.

### New: `ui/overlay/effortField.go`
A ~20-line copy of `ui/overlay/modeField.go`:
```go
const effortInherit = "default" // contributes no --effort flag

type EffortField struct{ chipRow }

func NewEffortField() *EffortField {
    return &EffortField{chipRow{
        options: append([]string{effortInherit}, agent.ClaudeEffortLevels...),
        labels:  append([]string{effortInherit}, agent.ClaudeEffortLabels...),
    }}
}

func (f *EffortField) HandleKeyPress(msg tea.KeyMsg) { if f.disabled { return }; f.moveCursor(msg) }
func (f *EffortField) Value() string { return f.selected() } // "" on default chip / disabled

func (f *EffortField) Render() string { /* label "Effort"; claudeFieldNA when disabled; else hint + f.render() */ }
```
`chipRow.selected()` already returns `""` for cursor 0 (default) and when
disabled — no new chip logic.

### `ui/overlay/textInput.go`
Add the struct field beside `modeField` (`textInput.go:29`):
```go
effortField *EffortField
```

### `ui/overlay/textInput_focus.go`
- `focusStop` enum (`:8`): add a `stopEffort` identifier. Its position in the
  enum is cosmetic — the actual focus order is set by the `stops` slice built in
  `textInput_create.go` (below), so this is just a new constant.
- Add `func (t *TextInputOverlay) isEffortField() bool { return t.currentStop() == stopEffort }`.
- `stopEnabled` (`:112`): add
  `if kind == stopEffort && t.effortField != nil && t.effortField.Disabled() { return false }`.
- `updateFocusState` (`:132`): add the focus/blur block for `effortField`
  mirroring `modeField`.

### `ui/overlay/textInput_create.go`
- In `NewSessionCreateOverlay` (`:49-57`): create `EffortField` in the same
  `KeyClaude` loop that builds `mf`/`pmf` (one new `var ef *EffortField`).
- Focus-ring insertion (`:68-73`): after `if mf != nil { stops = append(..., stopModel) }`
  add `if ef != nil { stops = append(stops, stopEffort) }`, keeping the
  Model→Effort→Mode order.
- Struct literal (`:79`): set `effortField: ef`.
- `syncClaudeFieldsEnabled` (`:102-115`): add `t.effortField.SetDisabled(disabled)`
  (the model-field presence check already guards all three, since they are
  created together).
- Add accessor mirroring `GetPermissionMode` (`:288`):
```go
func (t *TextInputOverlay) GetEffort() string {
    if t.effortField == nil { return "" }
    return t.effortField.Value()
}
```

### `ui/overlay/textInput_keys.go`
In the `default:` block add, after the `isModeField` branch (`:127-132`):
```go
if t.isEffortField() { t.effortField.HandleKeyPress(msg); return false, false }
```

### `ui/overlay/textInput_render.go`
In `renderCreateForm`, after the model section (`:237-239`) and before the mode
section (to keep Model→Effort→Permissions):
```go
if t.effortField != nil { section(t.effortField.Render()) }
```

### `ui/overlay/textInput_size.go`
- Add `effortSectionLines = 4` beside `modeSectionLines` (`:36`).
- In `fitRows`, add `if t.effortField != nil { chrome += effortSectionLines }`
  (`:87-92`) so the constant-height overlay invariant holds.
- Do **not** add a `SetWidth` call in `SetSize`: `EffortField` is a pure
  `chipRow` (like `ModeField`), which has no `SetWidth`/width field — only the
  free-text `ModelField` is width-managed there.

### `app/app_session.go`
- Extend `composeProgramFlags` (`:956`) with an `effort` param:
```go
func composeProgramFlags(program, model, mode, effort string) (string, error) {
    // ... existing model + mode blocks ...
    if effort != "" && agent.Resolve(program).Key == agent.KeyClaude {
        if !agent.ValidEffort(effort) {
            return "", fmt.Errorf("invalid effort level %q", effort)
        }
        program = agent.WithEffortFlag(program, effort)
    }
    return program, nil
}
```
- Update the call site (`:1031`):
```go
program, err := composeProgramFlags(program, ov.GetModel(), ov.GetPermissionMode(), ov.GetEffort())
```
- Update the doc comment on `composeProgramFlags` to mention effort and its
  soft-validation (CLI warns-and-ignores unknowns, unlike `--permission-mode`).

## Tradeoffs

- **Pass-through vs per-model gating.** Chosen pass-through because the CLI
  degrades gracefully (unknown → warn + default effort, exit 0; unsupported →
  soft indicator) and is the only layer that knows the account's entitlements.
  Cost: a user can pick `xhigh` on a model that ignores it and get default effort
  with a pane warning — the same recoverable tradeoff `--model`/`--permission-mode`
  already accept, and strictly softer (no dead session).
- **Static list + drift tripwire vs runtime probe.** A static `ClaudeEffortLevels`
  keeps the "hermetic, no probing" config convention and adds zero form-open
  latency; the tripwire test sources the truth from `claude` so drift is caught
  in CI-adjacent local runs, not silently shipped. Runtime `claude --help`
  parsing on every form open was rejected (latency, parse fragility, failure
  handling, convention break).
- **Literal `ClaudeEffortLabels` vs derived.** Labels equal values, so a literal
  parallel slice is clearer than a derivation; a test pins len/order parity.

## Testing

Stay hermetic (temp `HOME`; `session/agent` tests need no `HOME`, `app`/`ui`
tests follow existing `TestMain`).

### New: `session/agent/effort_test.go` (mirror `permissionmode_test.go`)
- `ValidEffort`: true for each of the five, false for `""`, `ultracode`, junk.
- `WithEffortFlag`: append onto `"claude"`; replace an existing `--effort` pin;
  quote-carrying program takes the append path (last-wins) — reuse the
  `withFlag` cases from `model_test.go`.
- `EffortFlag`: extracts `--effort x` and `--effort=x`, last-wins, `""` when
  absent.
- **Drift tripwire `TestClaudeEffortLevels_MatchInstalledCLI`:** skip via
  `t.Skip` when `claude` is not on `PATH` (like the real-tmux tests). Run
  `claude --effort __atrium_drift_probe__ --help`, parse the
  `Valid values: a, b, c` list from the warning line (fallback: parse the
  `(low, medium, high, xhigh, max)` suffix of the `--effort` help line), and
  assert the set equals `ClaudeEffortLevels`. This is the "source it from Claude"
  robustness, applied where it can't add runtime cost or flakiness. (`--help`
  short-circuits before any session/API call — confirmed exit 0, no network;
  run it under a temp `HOME` to stay fully hermetic.)

### `app/newsession_test.go` (extend the block at `:564`)
- `composeProgramFlags("claude", "", "", "xhigh")` → `"claude --effort xhigh"`.
- `composeProgramFlags("claude", "opus", "plan", "high")` → all three folded.
- Invalid effort → error.
- Non-Claude program (`"echo"`) → effort ignored (passthrough unchanged).
- Empty effort → no `--effort` added.

### `ui/overlay/textInput_test.go` (+ `chiprow` reuse)
- Effort field present when a candidate program is Claude; absent otherwise.
- `GetEffort()`: `""` on default chip and when disabled; the level after cycling.
- Effort field in the focus ring between Model and Mode; skipped (disabled) when
  a non-Claude profile is selected (drive `syncClaudeFieldsEnabled` via a
  profile switch).
- Constant-height invariant: rendered create-form height is unchanged as focus
  moves onto/off the effort field (the existing overlay height tests cover the
  pattern).

## Verification

`just build` and `just test` must pass before the work is considered complete
(`GO=/path/to/go just ...` if `go` is not on `PATH`). Manually: open `N` on a
Claude profile, confirm the Effort row renders and cycles, create a session with
`--effort xhigh`, and confirm the launched command carries the flag (e.g. via the
tmux pane / `Program` string). Confirm the row is absent/inert for a non-Claude
profile.

## Appendix — Gemini (Phase 2, not built)

No launch flag exists. To preset thinking level, write the worktree's
`.gemini/settings.json`:
```json
{ "modelConfigs": { "overrides": [ {
  "match": { "model": "<resolved-model>" },
  "modelConfig": { "generateContentConfig": { "thinkingConfig": { /* see below */ } } }
} ] } }
```
Branch the `thinkingConfig` on model generation to avoid the "both set" API error:
- **Gemini 2.5** → `"thinkingBudget": <int>` (Pro 128–32768; Flash 0–24576;
  Flash-Lite 0 or 512–24576; `-1` = dynamic). Map a low/med/high band to an int,
  clamped to the model's legal range (2.5 Pro cannot be 0).
- **Gemini 3.x** → `"thinkingLevel": "low"|"medium"|"high"` (support varies:
  3-pro = low/high only).
This needs: a settings writer keyed to the session worktree, model-generation
detection from the profile/`-m` alias, range clamping, and gating the effort
field on `agent.KeyGemini`. A separate spec when picked up.
