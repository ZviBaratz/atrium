# Adaptive light/dark theming + `NO_COLOR` Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make Atrium readable on light-background terminals — a light palette per default family, detection that selects it automatically under `theme: auto`, and spec-compliant `NO_COLOR` support.

**Architecture:** Six stages, A–F, each independently landable and green. The scheme (dark/light) becomes a **third orthogonal axis** in `ui/theme/current.go` alongside the existing palette-name and glyph-set axes; `auto` is a reserved config value resolved in `compose()`, never a registry entry. `NO_COLOR` is a **render-time colour-profile override** (`colorprofile.Ascii`), not a palette — plus explicit handling of the two surfaces the profile cannot reach (tmux's own markup, and the splash's colour-borne brightness channel). Stages A–D deliver usable value with no detection code at all.

**Tech Stack:** Go 1.26; Bubble Tea v2 (`charm.land/bubbletea/v2@v2.0.8`), Lip Gloss v2 (`charm.land/lipgloss/v2@v2.0.5`), `github.com/charmbracelet/colorprofile@v0.4.3`, `github.com/charmbracelet/x/ansi@v0.11.7`, `github.com/ZviBaratz/fresco/v2@v2.0.0`, testify, tmux 3.x.

Design doc: `docs/superpowers/specs/2026-07-30-adaptive-light-dark-theming-design.md`. Issue: #394 (AC corrections posted as issue comment 5129675611).

## Global Constraints

- **Gate:** `PATH=$PATH:$HOME/go/bin just ci` must be green before any commit is called done. `golangci-lint` lives in `~/go/bin`, which is *not* on mise's PATH — without the prefix the sweep dies at `lint` with exit 127, which reads like a broken recipe. Also run `go test -race -shuffle=on ./...`.
- **Lint through `just lint`, never a bare `golangci-lint run`.** The recipe keys `GOLANGCI_LINT_CACHE` to this worktree; a bare run uses the global cache and reports stale findings from a sibling worktree (#486). Scope with `just lint ./ui/...`.
- **`app/testdata/frames/*.txt` (18 states × 2 sizes) must stay byte-identical in every task of this plan.** A theme change is a colour change, not a layout change. If a frame golden moves, stop and find out why — do not regenerate.
- **`app/testdata/colours.txt` must also stay byte-identical in every task of this plan.** It is generated at the *default* theme; nothing here changes what the default renders (Stage F's `auto`-with-no-detection resolves to that same default). A move means a leak.
- **Tests must stay hermetic.** Never read or write the user's real data dir. Any new test that can reach `config`/`state`/`tmux` writes must set `HOME` to a temp dir (see `config/config_test.go` and `app/app_test.go` `TestMain`).
- **Commits:** Conventional Commits, lowercase (`feat: …`, `fix: …`, `test: …`, `docs: …`).
- **Every exported symbol needs a doc comment** (`revive:exported` via `just lint`). Never name anything `max`, `min`, or `len` (`revive:redefines-builtin-id`).
- **Declare nothing you do not read.** `unused` fails CI on a const or field added for an assertion you meant to write and didn't — and `build`, `vet` and `fmt-check` all pass happily while it does.
- **Drift sites:** read `.claude/skills/tui-drift-sites/SKILL.md` before touching `keys/`, `config/types.go`, `ui/theme/`, or `ui/menu.go`. Sites touched by this plan are named per task.
- Regenerate a golden with `CS_UPDATE_GOLDEN=1 go test ./app/ -run <TestName>`.
- If `go` is not on PATH, pass it explicitly: `GO=/path/to/go just test`.

---

## File Structure

**Created**

| Path | Responsibility |
|---|---|
| `ui/theme/contrast_test.go` | WCAG contrast oracle over every registered palette + the two brand colours. Test-only; no production symbol. |
| `ui/theme/light.go` | The light palettes (`tokyoNightDay`, `catppuccinLatte`) and the dark→light twin table. Data only — but derived from upstream, not copied; see Task 3 Step 1. |
| `ui/theme/scheme.go` | The dark/light axis. **Created in Stage C** (`IsLight` + `relLuminanceOf`, moved out of the test file), extended in Stage E with the `Scheme` enum, the pure `ResolveScheme` ladder and `SetScheme`. No bubbletea import — `theme` stays a leaf. |
| `ui/theme/scheme_test.go` | Table tests for `ResolveScheme` and the twin/`auto` composition. |
| `ui/theme/mono.go` | `Mono()`/`SetMono()` — the one predicate the *non-lipgloss* emitters consult under `NO_COLOR`. |
| `session/tmux/barstyle.go` | `ApplyBarStyle` — pushes `status-style` to the live server, fleet-wide, in one batched subprocess. |
| `session/tmux/barstyle_test.go` | Fake-executor guard on the batched argv. |
| `app/scheme.go` | The Bubble Tea wiring: OSC 11 request Cmds, `BackgroundColorMsg` handling, `COLORFGBG` read, the flip that re-themes, and `applySchemeQueryCmd` — the fourth query point, found in review after the other three were green. |
| `app/scheme_test.go` | Guards on the wiring: latching, the flip's bar push, AC#5's no-input equivalence, and each of the four query points independently. |
| `internal/doctor/scheme.go` | `CheckScheme`/`RenderScheme` — the rungs doctor can read, and the one it must name but cannot probe. |
| `internal/doctor/scheme_test.go` | Environ injection, the later-entry-wins tie-break, and the section's formatting convention. |
| `config/theme_test.go` | `TestGetTheme`: empty → default, and the case folding `auto` depends on. |
| `app/testdata/colours-light.txt` | Colour fingerprint of all 18 states under `tokyo-night-day`. |
| `app/nocolour_test.go` | The `NO_COLOR` oracle: frames through a `colorprofile.Ascii` writer. |

**Modified**

| Path | Change |
|---|---|
| `ui/theme/registry.go` | Register the two light themes; `SelectableNames()`; `AutoThemeName`. |
| `ui/theme/agent.go` | `agentColorsLight` — the two brand accents darkened to clear the 3.0 floor — and `AgentGlyph` selecting between the tables on `IsLight`. Added in Stage C, Task 3 Step 2b; the plan had no such step. |
| `ui/theme/current.go` | Third axis (`curScheme`) and `auto` resolution in `compose()`. |
| `session/tmux/config.go` | `renderManagedConfig` honours `theme.Mono()`. |
| `ui/contextbar.go` | Omit `#[fg=…]` under `theme.Mono()`, keep `#[bold]`. |
| `ui/splash.go` | `LumRange: 0` on a light palette and under `theme.Mono()` — **rain exempt from both** (see Task 4 Step 9 for the measurement, Task 6 Step 5 for the Mono half). `splashLumRange` takes the variant so it can say so. |
| `app/app.go` | `tea.WithColorProfile(colorprofile.Ascii)` under `NO_COLOR`; `RequestBackgroundColor` in `Init`. |
| `app/app_update.go` | `tea.BackgroundColorMsg` case; focus re-query on `tea.FocusMsg`. |
| `app/app_layout.go` | `applySettingChange("theme")` pushes the bar style and, via `applySchemeQueryCmd(key)`, is the fourth query point; `repaintAfterAttach` re-queries. Stage E also split `applyBarStyleCmd(key)` into a keyless `barStylePushCmd()`, so detection can push the band without passing a settings-row key it is not. |
| `app/frameparity_test.go` | Pin `SetScheme` in `newParityHome`; the light fingerprint test. |
| `ui/overlay/settings_schema.go` | The `theme` row's `options` uses `SelectableNames()`; `summary`; `defaultDisplay`. |
| `ui/overlay/settings_test.go` | `TestSettingsOverlay_CycleThemeWraps` cycles `SelectableNames()`. |
| `config/accessors.go` | `DefaultTheme` const + `GetTheme()`, normalizing empty → the default **and folding case**: `auto` is not a registry entry, so unlike a palette name it is not saved by `Get()`'s own lowercasing. |
| `config/types.go` | `Theme` doc comment gains `auto`. |
| `config/config.go` | **No change in any stage.** Task 7 pointed `DefaultConfig()` at the `DefaultTheme` const (`config/config.go:92` is `Theme: DefaultTheme`), which is what makes Stage F a one-line edit to `config/accessors.go` alone. |
| `internal/doctor/*.go` | Report the detected scheme and which rung answered. |
| `README.md` | The `theme` config row; a `NO_COLOR` note. |

---

## Stage A — the tmux bar follows a live theme change ✅ SHIPPED (`161fe81`)

A pre-existing bug, fixed ahead of any light-mode work. `session/tmux/config.go:79-80` bakes `status-style` into the managed conf, which tmux reads **only when the server starts**, while `ComposeSessionContext` pushes `#[fg=…]` markup every metadata tick. So a live theme change already leaves every session's bar half-diverged: new text colours on the launch-time band. An automatic dark→light flip would make that visible across the whole fleet at once.

`status-style` is set with `-g` (server-global), so one `set-option -g` covers every session.

### Task 1: Push `status-style` to the live tmux server on a theme change

**Files:**
- Create: `session/tmux/barstyle.go`
- Create: `session/tmux/barstyle_test.go`
- Modify: `app/app_layout.go` — the `case "theme", "glyph_set":` arm at `app/app_layout.go:266-279`
- Modify: `session/tmux/config.go` — export a re-render entry point for the managed conf

**Interfaces:**
- Consumes: `tmuxCommand(ctx, args...)` (`session/tmux/command.go:26`), `tmuxOpTimeout` (`command.go:14`), `cmd.Executor` (`cmd/cmd.go:16`), `theme.Current()`, `theme.Hex()`.
- Produces:
  - `func tmux.ApplyBarStyle(ctx context.Context, cmdExec cmd.Executor) error` — reads `theme.Current()` itself and pushes `status-style` + `refresh-client -S` in one batched command. Returns nil when no server is running (nothing to restyle is not an error).
  - `func tmux.RewriteManagedConfig(contextBar bool) error` — re-materializes the managed conf from the *current* theme, for a server that starts later.
  - `func (m *home) applyBarStyleCmd() tea.Cmd` — the `tea.Cmd` wrapper; nil when the context bar is off.

- [x] **Step 1: Write the failing test for the batched argv**

Create `session/tmux/barstyle_test.go`. Modelled on `session/tmux/context_test.go`, which is the precedent for a fake-executor argv assertion in this package.

```go
package tmux

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	cmd2 "github.com/ZviBaratz/atrium/cmd"
	"github.com/ZviBaratz/atrium/cmd/cmd_test"
	"github.com/ZviBaratz/atrium/ui/theme"

	"github.com/stretchr/testify/require"
)

// ApplyBarStyle pushes the ACTIVE theme's bar colours to the live server in one
// batched command. One subprocess, not one per session: status-style is a
// server-global option (-g), so the whole fleet restyles at once. Counting the
// subprocesses is the #380 discipline — a per-session push would be N of them on
// the update thread.
func TestApplyBarStyle_OneBatchedCommandCarriesActiveTheme(t *testing.T) {
	defer theme.Set("catppuccin-mocha")()

	var ran []string
	cmdExec := cmd_test.MockCmdExec{
		RunFunc: func(c *exec.Cmd) error {
			ran = append(ran, cmd2.ToString(c))
			return nil
		},
		OutputFunc: func(c *exec.Cmd) ([]byte, error) { return nil, nil },
	}

	require.NoError(t, ApplyBarStyle(context.Background(), cmdExec))
	require.Len(t, ran, 1, "status-style is server-global: one subprocess for the fleet")

	th := theme.Current()
	for _, sub := range []string{
		"set-option", "-g", "status-style",
		"bg=" + theme.Hex(th.Palette.BarBg),
		"fg=" + theme.Hex(th.Palette.Fg),
		"refresh-client",
	} {
		require.Containsf(t, ran[0], sub, "batched command missing %q: %s", sub, ran[0])
	}
}

// The colours must come from the theme that is active AT CALL TIME, not from one
// captured earlier. This is the whole point of the task: the managed conf froze
// them at server start, which is why a live theme change left the band stale.
func TestApplyBarStyle_ReadsTheThemeAtCallTime(t *testing.T) {
	var ran []string
	cmdExec := cmd_test.MockCmdExec{
		RunFunc: func(c *exec.Cmd) error {
			ran = append(ran, cmd2.ToString(c))
			return nil
		},
		OutputFunc: func(c *exec.Cmd) ([]byte, error) { return nil, nil },
	}

	restoreA := theme.Set("tokyo-night")
	require.NoError(t, ApplyBarStyle(context.Background(), cmdExec))
	restoreA()

	restoreB := theme.Set("catppuccin-mocha")
	require.NoError(t, ApplyBarStyle(context.Background(), cmdExec))
	restoreB()

	require.Len(t, ran, 2)
	require.NotEqual(t, ran[0], ran[1],
		"two different themes must produce two different pushes")
	require.Contains(t, ran[0], "bg="+theme.Hex(theme.Get("tokyo-night").Palette.BarBg))
	require.Contains(t, ran[1], "bg="+theme.Hex(theme.Get("catppuccin-mocha").Palette.BarBg))
}
```

- [x] **Step 2: Run the test to verify it fails**

Run: `go test ./session/tmux/ -run TestApplyBarStyle -v`
Expected: FAIL to compile — `undefined: ApplyBarStyle`.

- [x] **Step 3: Write `session/tmux/barstyle.go`**

```go
package tmux

import (
	"context"
	"fmt"

	"github.com/ZviBaratz/atrium/cmd"
	"github.com/ZviBaratz/atrium/ui/theme"
)

// barstyle.go keeps the in-session status bar's BAND in step with a live theme
// change.
//
// The band's colours (status-style) are baked into the managed tmux config, and
// tmux reads -f only when a SERVER starts — so before this existed, a theme
// change restyled the bar's text (Atrium pushes @atrium_left with #[fg=...]
// markup on every metadata tick, see context.go) while leaving the band on the
// launch-time colours. Half the bar moved and half did not, which a dark->light
// flip turns from a subtle mismatch into an unreadable one.
//
// status-style is a server-global option, so one set-option -g restyles every
// session at once. That is why this is a package-level function taking an
// Executor rather than a Session method: there is nothing per-session about it,
// and a per-session push would be N subprocesses on the update thread (#380).

// ApplyBarStyle pushes the active theme's status-bar band colours to the live
// tmux server and repaints, in one batched command.
//
// It reads theme.Current() at call time rather than taking colours as arguments:
// the value being fixed too early is precisely the bug this exists to fix, and a
// colour passed in as an argument is a wire no test guards.
//
// A failure is not fatal to the caller. The common one is "no server running",
// which is not a problem to report — there are no bars to restyle, and the
// managed config (rewritten alongside this by the caller) already carries the new
// colours for whenever a server does start.
func ApplyBarStyle(ctx context.Context, cmdExec cmd.Executor) error {
	th := theme.Current()
	style := fmt.Sprintf("bg=%s,fg=%s", theme.Hex(th.Palette.BarBg), theme.Hex(th.Palette.Fg))

	opCtx, cancel := context.WithTimeout(ctx, tmuxOpTimeout)
	defer cancel()

	c := tmuxCommand(opCtx,
		"set-option", "-g", "status-style", style, ";",
		"refresh-client", "-S",
	)
	if err := cmdExec.Run(c); err != nil {
		return fmt.Errorf("failed to apply tmux status-bar style: %w", err)
	}
	return nil
}

// RewriteManagedConfig re-materializes the managed tmux config from the CURRENT
// theme, so a server that starts after a live theme change opens with the new
// band rather than the one baked in at launch.
//
// It is the deferred half of ApplyBarStyle: that one fixes servers already
// running, this one fixes servers not yet started. Both are needed, or the fleet
// diverges depending on when each session was created.
func RewriteManagedConfig(contextBar bool) error {
	return Init(configOverridePath, contextBar)
}
```

**Note on `RewriteManagedConfig`:** it delegates to `Init` deliberately, passing the *stored* `configOverridePath` back in so an override the user set at launch is preserved. `Init` already returns early for an override, writes atomically enough for this purpose (a full `os.WriteFile`), and re-runs `validateConfig` — which is what we want, since a theme whose hex somehow broke the file should fall back rather than lock the user out of the pane.

- [x] **Step 4: Run the test to verify it passes**

Run: `go test ./session/tmux/ -run 'TestApplyBarStyle' -v`
Expected: PASS, both tests.

- [x] **Step 5: Wire it into the live theme change**

Modify `app/app_layout.go`. The existing arm is at `app/app_layout.go:266`; add the bar push to what it returns.

```go
	case "theme", "glyph_set":
		// Styles read theme.Current() lazily at render time, so swapping the
		// palette / glyph set plus a forced repaint restyles the whole UI in place.
		theme.Set(m.appConfig.Theme)
		theme.SetGlyphSet(m.appConfig.GetGlyphSet())
		// The spinner snapshots its frames at construction (assembleHome), so a
		// rung change that alters them (ascii's |/-\ vs the Braille dots) would not
		// show until relaunch. The list holds &m.spinner, so re-seeding the frames
		// here re-frames the running spinner in place.
		m.spinner.Spinner = spinner.Spinner{
			Frames: theme.Current().Glyphs.SpinnerFrames,
			FPS:    theme.Current().Glyphs.SpinnerFPS,
		}
		// The in-session bar's BAND lives in tmux, not in this frame: its colours
		// are a server option baked in when the server started, so restyling the
		// TUI alone leaves every attached pane's header on the old band while its
		// text (pushed per tick as #[fg=...] markup) moves. Push both halves.
		return tea.Sequence(tea.ClearScreen, tea.Batch(tea.RequestWindowSize, m.applyBarStyleCmd()))
```

And add the Cmd alongside it, in the same file:

```go
// applyBarStyleCmd restyles every live session's in-pane status band for the
// theme that is now active, off the update thread.
//
// Two writes, because they cover two different populations: ApplyBarStyle fixes
// the sessions running right now (status-style is server-global, so that is one
// subprocess for the fleet), and RewriteManagedConfig fixes the ones started
// later, whose server will read the config file instead. Skipping either leaves
// the fleet's bars disagreeing depending on when each session was created.
//
// Both failures are logged, not surfaced: the bar is cosmetic, the user just
// asked for a theme, and the most common failure is simply that no tmux server is
// running yet.
func (m *home) applyBarStyleCmd() tea.Cmd {
	if !m.appConfig.GetSessionContextBar() {
		return nil
	}
	contextBar := m.appConfig.GetSessionContextBar()
	ctx := m.ctx
	return func() tea.Msg {
		if err := tmux.RewriteManagedConfig(contextBar); err != nil {
			log.WarningLog.Printf("failed to rewrite managed tmux config after theme change: %v", err)
		}
		if err := tmux.ApplyBarStyle(ctx, cmd.Exec{}); err != nil {
			log.WarningLog.Printf("failed to restyle live session bars after theme change: %v", err)
		}
		return nil
	}
}
```

Add `"github.com/ZviBaratz/atrium/cmd"` to `app/app_layout.go`'s imports. `log`, `tmux` and `tea` are already imported by the package; check `app/app_layout.go`'s own import block and add what is missing there specifically.

- [x] **Step 6: Write the failing test that the theme arm returns the push**

Add to `app/app_layout_test.go` (or the file where `applySettingChange` is already tested — `grep -rn "applySettingChange" app/*_test.go` first and put it beside its siblings).

```go
// applySettingChange("theme") must return a Cmd that includes the tmux bar push.
// This is the wire the ladder's own guards cannot see: theme.Set and the spinner
// re-seed are both observable in-process, but the bar lives in another process,
// so nothing else in the suite notices when the push is dropped.
func TestApplySettingChange_ThemePushesTmuxBarStyle(t *testing.T) {
	m := newCreateFormHome(t)
	m.appConfig.SessionContextBar = boolPtr(true)
	m.appConfig.Theme = "catppuccin-mocha"

	require.NotNil(t, m.applyBarStyleCmd(),
		"with the context bar on, a theme change must push the bar style")

	m.appConfig.SessionContextBar = boolPtr(false)
	require.Nil(t, m.applyBarStyleCmd(),
		"with the context bar off there is no band to restyle")
}
```

Both helpers already exist and are verified: `newCreateFormHome` at `app/newsession_test.go:37` (there is no `newTestHome`), `boolPtr` at `app/frames_test.go:552`, and `Config.SessionContextBar` is a `*bool` (`config/types.go:279`). Use them; do not add duplicates.

- [x] **Step 7: Run the app tests**

Run: `go test ./app/ -run 'TestApplySettingChange_ThemePushesTmuxBarStyle' -v`
Expected: PASS.

- [x] **Step 8: Verify no golden moved**

Run: `go test ./app/ -run 'TestFrameParity|TestFrameColourFingerprint' -count=1`
Expected: PASS with no golden regenerated. This task changes no rendering.

- [x] **Step 9: Mutation-verify the guard**

Temporarily delete `m.applyBarStyleCmd()` from the `tea.Batch` in the theme arm and confirm `TestApplySettingChange_ThemePushesTmuxBarStyle` still passes (it tests the helper, not the wiring) — then **strengthen it** so it does not. Assert on the Cmd the arm returns, not just on the helper:

```go
// The helper existing is not the same as the arm calling it. #391's finding: a
// derived value passed as an argument is a wire nothing guards — and so is a Cmd
// the ladder forgot to batch.
func TestApplySettingChange_ThemeArmBatchesTheBarPush(t *testing.T) {
	m := newCreateFormHome(t)
	m.appConfig.SessionContextBar = boolPtr(true)
	m.appConfig.Theme = "catppuccin-mocha"

	var pushed int32
	restore := swapBarStyleApplier(func() { atomic.AddInt32(&pushed, 1) })
	defer restore()

	cmd := m.applyBarStyleCmd()
	require.NotNil(t, cmd)
	cmd() // run the Cmd's body directly; it returns nil
	require.Equal(t, int32(1), atomic.LoadInt32(&pushed))
}
```

This needs a package-var seam so the test does not shell to tmux. Introduce it in `app/app_layout.go` next to `applyBarStyleCmd`, following the `tmuxAvailable` idiom already used in `app/app_session.go:33`:

```go
// barStyleApplier is the tmux bar push, as a package var so tests can substitute
// a recorder instead of shelling to a real server — the same seam idiom as
// tmuxAvailable in app_session.go.
var barStyleApplier = func(ctx context.Context, contextBar bool) {
	if err := tmux.RewriteManagedConfig(contextBar); err != nil {
		log.WarningLog.Printf("failed to rewrite managed tmux config after theme change: %v", err)
	}
	if err := tmux.ApplyBarStyle(ctx, cmd.Exec{}); err != nil {
		log.WarningLog.Printf("failed to restyle live session bars after theme change: %v", err)
	}
}
```

and have `applyBarStyleCmd` call `barStyleApplier(ctx, contextBar)` inside its closure. Then `swapBarStyleApplier` is a two-line test helper in `app/app_layout_test.go`. Restructure Step 5's code to this shape rather than leaving both versions.

Re-run: `go test ./app/ -run 'TestApplySettingChange_Theme' -v` — expected PASS. Then delete `m.applyBarStyleCmd()` from the `tea.Batch` again and confirm the *new* test **fails**. Restore.

- [x] **Step 10: Run the full gate**

Run: `PATH=$PATH:$HOME/go/bin just ci && go test -race -shuffle=on ./...`
Expected: all green.

- [x] **Step 11: Commit**

```bash
git add session/tmux/barstyle.go session/tmux/barstyle_test.go app/app_layout.go app/app_layout_test.go
git commit -m "fix(tmux): restyle live session bars on a theme change

status-style is baked into the managed config, which tmux reads only when a
server starts, while the bar's TEXT colours are pushed per metadata tick as
#[fg=...] markup. So a live theme change already moved half the bar and left
the band on the launch-time colours. Push both halves: set-option -g for the
servers running now (one subprocess for the fleet, the option is
server-global), and a config rewrite for the ones started later.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
```

---

## Stage B — the contrast oracle ✅ SHIPPED (`8ce6d85`)

Nothing in the suite today can see "unreadable". This is the gate the light palette must pass, so it exists before the palette. Test-only, zero behaviour change.

### Task 2: A WCAG contrast oracle over every palette

**Files:**
- Create: `ui/theme/contrast_test.go`

**Interfaces:**
- Consumes: `theme.Palette` fields, `theme.Names()`, `theme.Get()`, `theme.Color` (= `image/color.Color`), `agentColors` (package-private, `ui/theme/agent.go:24`).
- Produces: nothing exported. Later tasks rely on the *test names* `TestPaletteContrastFloors` and `TestLightPaletteMatchesItsDarkTwin`, and on the fact that a new registered theme is picked up automatically by iterating `Names()`.

- [x] **Step 1: Write the test**

Create `ui/theme/contrast_test.go`. Note it is in package `theme` (not `theme_test`) so it can read `agentColors`.

```go
package theme

import (
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

// contrast_test.go is the oracle for the one property a rendering test cannot
// see: whether the palette is READABLE. Every other theme guard here asks about
// width, shape or vocabulary; none of them would notice a foreground that
// disappeared into the background, which is exactly how three dark-tuned themes
// shipped as the only options (#394).
//
// The floors are set from the MINIMUM across the shipped dark themes with margin,
// so this lands green on what exists today and constrains what is added next. It
// is not an accessibility certification — the tokens Atrium deliberately renders
// faint would fail WCAG AA and should — it is a floor under "did someone pick a
// colour nobody can see".

// relLuminance is WCAG 2.1's relative luminance. color.Color reports 16-bit
// alpha-premultiplied components, which for the opaque palette colours Atrium
// ships is the 8-bit value repeated (0xNN * 0x101), so >>8 recovers the byte.
func relLuminance(c Color) float64 {
	r16, g16, b16, _ := c.RGBA()
	lin := func(v uint32) float64 {
		s := float64(v>>8&0xff) / 255
		if s <= 0.03928 {
			return s / 12.92
		}
		return math.Pow((s+0.055)/1.055, 2.4)
	}
	return 0.2126*lin(r16) + 0.7152*lin(g16) + 0.0722*lin(b16)
}

// contrastRatio is WCAG 2.1's contrast ratio: 1.0 for two identical colours,
// 21.0 for black on white. Order-independent.
func contrastRatio(a, b Color) float64 {
	la, lb := relLuminance(a), relLuminance(b)
	hi, lo := math.Max(la, lb), math.Min(la, lb)
	return (hi + 0.05) / (lo + 0.05)
}

// tokenFloor is the minimum contrast a palette token must hold against its own
// theme's Bg. Bg is the reference because Atrium never paints a full-screen
// background — Palette.Bg means "the colour of the void", i.e. what the terminal
// itself shows — so a token's legibility is its ratio against it.
//
// The tiers are roles, not tastes. Status and text tokens carry meaning and get
// 4.5. The three tokens Atrium deliberately recedes (FgDim, Working, and
// AccentMuted, whose 2.56-on-tokyo-night to 8.69-on-mocha spread is why it cannot
// share Accent's floor) get 2.4. FgFaint and BarBg are the faint slate — in both
// shipped themes they are literally the SAME colour, a deliberate choice, not a
// defect — so they get 1.6. BgElevated is a selection fill that must merely be
// distinguishable, so 1.1.
var tokenFloors = map[string]struct {
	floor float64
	get   func(Palette) Color
}{
	"Fg":          {4.5, func(p Palette) Color { return p.Fg }},
	"Accent":      {4.5, func(p Palette) Color { return p.Accent }},
	"Purple":      {4.5, func(p Palette) Color { return p.Purple }},
	"Success":     {4.5, func(p Palette) Color { return p.Success }},
	"Pending":     {4.5, func(p Palette) Color { return p.Pending }},
	"Attention":   {4.5, func(p Palette) Color { return p.Attention }},
	"Danger":      {4.5, func(p Palette) Color { return p.Danger }},
	"Cyan":        {4.5, func(p Palette) Color { return p.Cyan }},
	"SuccessDim":  {3.0, func(p Palette) Color { return p.SuccessDim }},
	"FgDim":       {2.4, func(p Palette) Color { return p.FgDim }},
	"Working":     {2.4, func(p Palette) Color { return p.Working }},
	"AccentMuted": {2.4, func(p Palette) Color { return p.AccentMuted }},
	"FgFaint":     {1.6, func(p Palette) Color { return p.FgFaint }},
	"BarBg":       {1.6, func(p Palette) Color { return p.BarBg }},
	"BgElevated":  {1.1, func(p Palette) Color { return p.BgElevated }},
}

// pairFloors are the token pairs that meet each other directly rather than over
// Bg, each at the site that renders them. Every one of these is a real render:
// Foreground(Bg) over Background(Accent) is the picker's selected row
// (ui/overlay/styles.go), over Background(Attention) is the notice banner
// (app/banner.go), Fg over BgElevated is the selected list row (ui/row.go), and
// Fg over BarBg is the diff anchor (ui/diff_anchor.go).
var pairFloors = []struct {
	name  string
	floor float64
	fg    func(Palette) Color
	bg    func(Palette) Color
}{
	{"BadgeFg on BadgeBg", 4.5, func(p Palette) Color { return p.BadgeFg }, func(p Palette) Color { return p.BadgeBg }},
	{"Bg on Accent (selected row)", 4.5, func(p Palette) Color { return p.Bg }, func(p Palette) Color { return p.Accent }},
	{"Bg on Attention (banner)", 4.5, func(p Palette) Color { return p.Bg }, func(p Palette) Color { return p.Attention }},
	{"Fg on BgElevated (selected list row)", 4.5, func(p Palette) Color { return p.Fg }, func(p Palette) Color { return p.BgElevated }},
	{"Fg on BarBg (diff anchor)", 4.5, func(p Palette) Color { return p.Fg }, func(p Palette) Color { return p.BarBg }},
}

// KNOWN, DELIBERATELY UNASSERTED: FgDim on BarBg. ui/contextbar.go's barState
// renders Paused and the default state in FgDim on the bar's band, which is
// 1.44:1 on tokyo-night and 1.87:1 on catppuccin-mocha — while contextbar.go:59's
// own comment says "dim greys wash out" there. It is a real legibility defect on
// the DARK themes, found by this oracle while it was being written, and it is
// filed rather than fixed here: fixing it means choosing a new colour for a
// state, which is a design decision and not #394's subject. Do not add it to
// pairFloors without fixing barState in the same change.

// TestPaletteContrastFloors holds every registered palette to the floors above.
// Iterating Names() rather than a hand-listed table is deliberate: a theme
// registered later is covered without anyone remembering to add it here.
func TestPaletteContrastFloors(t *testing.T) {
	names := Names()
	require.NotEmpty(t, names)

	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			p := Get(name).Palette
			for token, spec := range tokenFloors {
				got := contrastRatio(spec.get(p), p.Bg)
				require.GreaterOrEqualf(t, got, spec.floor,
					"%s: %s contrast against Bg is %.2f, below the %.2f floor for its role",
					name, token, got, spec.floor)
			}
			for _, pair := range pairFloors {
				got := contrastRatio(pair.fg(p), pair.bg(p))
				require.GreaterOrEqualf(t, got, pair.floor,
					"%s: %s contrast is %.2f, below the %.2f floor",
					name, pair.name, got, pair.floor)
			}
		})
	}
}

// TestAgentBrandColoursStayLegible covers the only two colours in the whole app
// that are NOT palette tokens: ui/theme/agent.go's Claude and Gemini brand
// accents, documented there as theme-independent. Theme-independent means every
// palette has to carry them, so a palette they vanish against is the palette's
// problem — and without this they would be the one pair of colours no oracle
// looks at.
func TestAgentBrandColoursStayLegible(t *testing.T) {
	const brandFloor = 3.0 // glyphs, not prose: a single mark at width 1
	for _, name := range Names() {
		p := Get(name).Palette
		for key, c := range agentColors {
			got := contrastRatio(c, p.Bg)
			require.GreaterOrEqualf(t, got, brandFloor,
				"%s: the %s brand glyph is %.2f against Bg, below the %.2f floor",
				name, key, got, brandFloor)
		}
	}
}
```

- [x] **Step 2: Run it**

Run: `go test ./ui/theme/ -run 'TestPaletteContrastFloors|TestAgentBrandColoursStayLegible' -v`
Expected: **PASS** on all three registered themes. The floors were computed from the shipped values with margin.

If anything fails, do **not** loosen the floor and do not change a shipped colour — report the number and stop. The measured baselines are in the design doc's reference table; a mismatch means either the arithmetic or the table is wrong, and both are worth knowing before a palette is built on top.

- [x] **Step 3: Mutation-verify the oracle**

Three mutations, each must turn the test red:

1. In `ui/theme/registry.go`, change `tokyoNight.Palette.Fg` to `lipgloss.Color("#1d1e29")` (nearly `Bg`). Expected: `TestPaletteContrastFloors/tokyo-night` fails naming `Fg`. Revert.
2. Change `tokyoNight.Palette.BadgeFg` to the same value as `BadgeBg` (`#bb9af7`). Expected: fails naming `BadgeFg on BadgeBg`. Revert.
3. In `ui/theme/agent.go`, change `"claude"` to `lipgloss.Color("#1a1b26")`. Expected: `TestAgentBrandColoursStayLegible` fails. Revert.

If a mutation passes, the oracle is not reading what it claims — suspect the mutation first (`grep` that the value you edited is the one the test reads), then the test.

- [x] **Step 4: Run the gate**

Run: `PATH=$PATH:$HOME/go/bin just lint ./ui/... && go test ./ui/... -count=1`
Expected: green. `just lint` matters here specifically because `tokenFloors`/`pairFloors` are the kind of table `unused` complains about if a field is declared and never read.

- [x] **Step 5: Commit**

```bash
git add ui/theme/contrast_test.go
git commit -m "test(theme): add a WCAG contrast oracle over every palette

The suite could see width, shape and vocabulary but not legibility, which
is how three dark-tuned themes shipped as the only options. Floors are set
from the minimum across the shipped themes with margin, tiered by token
role, and cover the two hardcoded brand colours in agent.go — the only
colours in the app no palette owns.

Found while writing it, filed rather than fixed: barState renders Paused in
FgDim on the bar band at 1.44:1 (tokyo-night) / 1.87:1 (mocha), which
contextbar.go's own comment predicts. Deliberately not asserted.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
```

- [x] **Step 6: File the barState follow-up**

```bash
gh issue create --title "barState renders Paused in FgDim on the bar band at 1.44:1" \
  --label enhancement --label area:ux \
  --body "$(cat <<'EOF'
`ui/contextbar.go`'s `barState` returns `Palette.FgDim` for `session.Paused` and
for the default case, and that text is rendered on the bar's `BarBg` band.
Measured contrast: **1.44:1** on tokyo-night, **1.87:1** on catppuccin-mocha.

`ui/contextbar.go:59`'s own comment already says why this is wrong — "where dim
greys wash out — so hierarchy comes from weight, not color" — and then the Paused
and default arms use a dim grey anyway.

Surfaced by the contrast oracle added in #394 Stage B, which deliberately does
*not* assert this pair (`contrast_test.go`'s `pairFloors` carries a comment
saying so). Fixing it means choosing a new colour for a state, which is a design
decision rather than part of #394.

When fixed, add `FgDim on BarBg` to `pairFloors` in the same change.
EOF
)"
```

---

## Stage C — the light palettes ✅ SHIPPED (#560, `1e3d268`)

Data only. No `auto`, no detection, no change at the 152 `theme.Current()` call sites. Immediately usable via `theme: tokyo-night-day`, which is what makes a real light-terminal eyeball round possible this early.

> **Corrected after the fact.** Three things below were written before the work and
> falsified by it. They are rewritten in place rather than annotated, because Stage D
> is executed from this file and a struck-through step is still a step someone types.
> What changed, and why it matters beyond Stage C:
>
> 1. **The palette hex was recalled, not sourced, and did not survive the oracle.**
>    Task 3 Step 1's original table missed eight token floors and all five pair floors
>    on `day`, six and three on `latte`. Replaced with what shipped. The general lesson
>    for any later palette: upstream light themes are tuned for editor prose and do not
>    clear floors derived from dark palettes.
> 2. **Task 3 needed a step nobody planned: the agent brand accents.** Claude's
>    `#d97757` peaks at 3.12:1 against *pure white*, so it cannot clear the 3.0 brand
>    floor on any real light background. No palette tuning reaches it. Added as Step 2b,
>    numbered to leave the existing Step 3 onwards where other prose already cites them.
> 3. **Task 4 Step 5's `t.Setenv` test could not work**, and Task 4 Step 9's
>    "if confetti → splash-off" fork was resolved differently. Both rewritten.
>
> **Stage D inherits (2) and (3)'s shape.** `theme.SetMono` is another package global
> with a restore, and `NoColorRequested` takes `environ` as a parameter precisely
> because env reads resist testing — see the warning on Task 5 Step 1.

### Task 3: Register `tokyo-night-day` and `catppuccin-latte`

**Files:**
- Create: `ui/theme/light.go`
- Modify: `ui/theme/registry.go` — the `registry` map at `ui/theme/registry.go:231-235`
- Create: `app/testdata/colours-light.txt` (generated)
- Modify: `app/frameparity_test.go` — add the light fingerprint test

**Interfaces:**
- Consumes: `theme.Palette`, `theme.Borders`, `plainGlyphs()`, `lipgloss.RoundedBorder()`.
- Produces:
  - `theme.lightTwin` — `map[string]string`, dark theme name → light theme name. Package-private. Read by `TestLightPaletteMatchesItsDarkTwin` from Step 4 onwards, which is what makes the pairing an assertion rather than a comment, and by Stage E's `compose()` once `auto` exists. (An earlier draft called Stage E its *only* consumer, which was wrong by one before Stage C had even finished — the same under-counting this branch is correcting elsewhere. State what reads it, not how many things do.)
  - Registry names `"tokyo-night-day"` and `"catppuccin-latte"`.

- [x] **Step 1: Write `ui/theme/light.go`**

These are the values that shipped. They are **derived from** the canonical upstream light palettes, not copied from them.

Upstream is the source of *hue identity* only. Neither `day` nor `latte` clears the contrast oracle as published — both are tuned for editor prose, not for floors set from two dark palettes people have read for months. The rule that produced the table below: keep the verbatim upstream token wherever one passes, else take the same colour with its HSL lightness lowered (hue and saturation preserved) to the minimum that clears the binding constraint, and name the origin in a comment.

Two sourcing traps worth carrying forward. **tokyonight's `day` is computed, not authored** — `lua/tokyonight/colors/day.lua` loads `night` and runs `Util.invert`, so there is no table to copy; the resolved values live in the generated extras (`extras/lua/tokyonight_day.lua`, `extras/kitty/tokyonight_day.conf`). Catppuccin is straightforward: `catppuccin/palette`'s `palette.json`.

And **the binding constraint is usually the twin band, not the floor.** "A light token holds ≥55% of its dark twin's ratio" asks for more than 4.5 whenever the dark twin is strong: `latte`'s Attention answers mocha's 12.91, so it needs 7.10 and a cheerful yellow lands as a dark amber. Deriving against the floor alone yields a palette the band then rejects.

```go
package theme

import "charm.land/lipgloss/v2"

// light.go carries the light-background palettes.
//
// They are ordinary registered themes, not a mode: selectable by name today, and
// what `theme: auto` resolves to on a light terminal once detection exists
// (#394 Stage E). Registering them normally is what gets them the existing
// guards for free — canonical-hex validation, glyph widths, the settings picker,
// and the contrast oracle.
//
// The token that needs the most care is Bg. Atrium never paints a full-screen
// background, so Palette.Bg is not a fill: it is "the colour of the void", used
// as a FOREGROUND on filled chips (ui/overlay/styles.go's selected row,
// app/banner.go's notice) and as the fade's substitute background
// (ui/overlay/overlay.go). On a light palette it therefore has to be near-white
// AND the Accent and Attention it sits on have to be saturated enough to carry
// it — which is what pairFloors in contrast_test.go asserts.

// See ui/theme/light.go for the shipped file, whose comments carry each token's
// upstream origin. The values:
//
//   tokyo-night-day (Bg #e1e2e7)          catppuccin-latte (Bg #eff1f5)
//     Bg          #e1e2e7  bg               Bg          #eff1f5  base
//     BgElevated  #c4c8da  bg_highlight     BgElevated  #ccd0da  surface0
//     BarBg       #a8aecb  fg_gutter        BarBg       #bcc0cc  surface1
//     Fg          #243f7e  fg, darkened     Fg          #494c65  text, darkened
//     FgDim       #6172b0  fg_dark          FgDim       #6c6f85  subtext0 (NOT overlay0)
//     FgFaint     #a8aecb  == BarBg         FgFaint     #bcc0cc  == BarBg
//     Accent      #155fc4  blue, darkened   Accent      #125ef4  blue, darkened
//     AccentMuted #718adb  blue0, darkened  AccentMuted #177181  sapphire, darkened
//     Purple      #7847bd  purple VERBATIM  Purple      #8839ef  mauve VERBATIM
//     Success     #49612f  green, darkened  Success     #28641b  green, darkened
//     SuccessDim  #587539  green VERBATIM   SuccessDim  #3e9b2a  green, darkened
//     Working     #6172b0  == FgDim         Working     #6c6f85  == FgDim
//     Pending     #005c7c  cyan, darkened   Pending     #026187  sky, darkened
//     Attention   #8a4800  orange, darkened Attention   #6d450e  yellow, darkened
//     Danger      #b13636  red1, darkened   Danger      #d20f39  red VERBATIM
//     Cyan        #005c7c  == Pending       Cyan        #026187  == Pending
//     BadgeBg     #7847bd  == Purple        BadgeBg     #8839ef  == Purple
//     BadgeFg     #e1e2e7  == Bg            BadgeFg     #eff1f5  == Bg
//
// Three judgement calls the table cannot show. day's Purple takes upstream `purple`
// rather than deriving from `magenta` (the role-faithful source), because a real
// upstream colour beats a derived one and `purple` passes verbatim at 4.73. day's
// Attention derives from `orange`, not `yellow`, whose derivation reads as khaki at
// 0.25 over the floor. latte's FgDim is `subtext0` because mocha's role-mate
// `overlay0` is 2.30 on latte, under its 2.40 floor.
//
// And one inherited oddity to leave alone: latte's AccentMuted ends up HIGHER
// contrast than its Accent (5.00 vs 4.72), inverting the name. That comes from
// mocha's own outlier (sapphire at 8.69 against tokyo-night's 2.56, the spread
// contrast_test.go's tier note already calls out) reaching latte through the twin
// band. Say so rather than hide it.

// lightTwin maps a dark theme to its light counterpart. It is what `theme: auto`
// walks once detection exists (#394 Stage E); until then it is the statement of
// which pairs are pairs, kept here beside the palettes so a light theme added
// later cannot be registered without deciding what it is the twin of.
//
// Only the DEFAULT family's pair is reachable from `auto`, by design: a
// catppuccin-mocha user wanting adaptivity selects `auto` and gets tokyo-night.
// Per-family adaptivity would need a second config field and would create an
// invalid-combination space (a family with no twin, asked to go light).
var lightTwin = map[string]string{
	"tokyo-night":      "tokyo-night-day",
	"catppuccin-mocha": "catppuccin-latte",
}
```

- [x] **Step 2: Register them**

In `ui/theme/registry.go`, extend the map:

```go
// registry maps theme names to themes. Adding a theme is one var + one entry.
var registry = map[string]*Theme{
	"tokyo-night":      tokyoNight,
	"catppuccin-mocha": catppuccinMocha,
	"tokyo-night-day":  tokyoNightDay,
	"catppuccin-latte": catppuccinLatte,
	"unicode":          unicodeFallback,
}
```

- [x] **Step 2b: Give the agent brand accents a light form** *(added after the fact — the plan had no such step and Task 3 cannot go green without it)*

`TestAgentBrandColoursStayLegible` holds the two hardcoded `agent.go` brand accents to 3.0 against **every** registered theme's `Bg`. Registering a light palette turns it red, and no palette value fixes it: claude's `#d97757` peaks at **3.12:1 against pure white**, needing a background lighter than `#fcfcfc` to clear 3.0. It measures 2.41 on tokyo-night-day and 2.76 on catppuccin-latte; gemini's `#4285f4` measures 2.75 and 3.15.

The fix is a second table of the *same hues*, darkened only as far as the floor requires, selected by polarity in `AgentGlyph`:

```go
var agentColorsLight = map[string]Color{
	"claude": lipgloss.Color("#cc552e"), // #d97757 darkened: 2.41 -> 3.31 on tokyo-night-day
	"gemini": lipgloss.Color("#2774f2"), // #4285f4 darkened: 2.75 -> 3.34 on tokyo-night-day
}
```

Two consequences for the rest of the plan. **`theme.IsLight` moves from Task 4 Step 3 into Task 3**, since `AgentGlyph` needs it — create `ui/theme/scheme.go` here. And **the brand oracle must resolve through `AgentGlyph`** rather than reading `agentColors` directly: with two tables, a test that reads either map alone passes while the wrong one ships. Resolving the colour that renders covers both with no second assertion.

Generalise before Stage E adds more palettes: *compute a colour's ceiling against pure white before assuming it can be made to work.* A hue with a high L\* has no legible form on paper, and that is a property of the brand, not of the palette.

- [x] **Step 3: Run the existing guards — they now cover the new palettes for free**

Run: `go test ./ui/theme/ ./ui/ ./ui/overlay/ -count=1`
Expected: PASS. Specifically these now iterate five themes instead of three, with no edit: `TestPaletteContrastFloors`, `TestAgentBrandColoursStayLegible`, `TestGlyphWidths`, `TestSplashPalettesAreCanonicalHex` (`ui/splash_test.go:24`), `TestSettingsOverlay_CycleThemeWraps` (`ui/overlay/settings_test.go:253` — length-agnostic, so it still passes).

If `TestPaletteContrastFloors` fails on a light theme, **that is the oracle doing its job** — adjust the *palette value*, never the floor. If `TestSplashPalettesAreCanonicalHex` fails, a splash anchor (`Danger`, `Purple`, `Accent`, `Cyan`, `Fg`) is not canonical `#rrggbb`.

- [x] **Step 4: Write the twin-parity test**

Add to `ui/theme/contrast_test.go`:

```go
// TestLightPaletteMatchesItsDarkTwin is the relative half of the oracle: rather
// than asserting a light palette hits absolute numbers someone picked, it asserts
// each light theme is AS READABLE AS the dark theme it twins. That removes the
// taste constant from the interesting direction — the dark themes are the ones
// people have actually read for months, so their ratios are the specification.
//
// The tolerance is wide on purpose. Matching a dark palette's contrast exactly on
// a light background is not achievable (light backgrounds compress the available
// range at the bright end), and a narrow band would make the test a colour-picker
// rather than a guard. What it catches is a token that is off by a FACTOR — the
// pastel that looked fine on slate and vanishes on paper.
func TestLightPaletteMatchesItsDarkTwin(t *testing.T) {
	require.NotEmpty(t, lightTwin, "no pairs to check")

	// A light token may hold between 55% and 210% of its dark twin's ratio.
	const lo, hi = 0.55, 2.10

	for dark, light := range lightTwin {
		t.Run(dark+"->"+light, func(t *testing.T) {
			dp, lp := Get(dark).Palette, Get(light).Palette
			require.Equal(t, light, Get(light).Name,
				"lightTwin names a theme that is not registered under that name")

			for token, spec := range tokenFloors {
				dr := contrastRatio(spec.get(dp), dp.Bg)
				lr := contrastRatio(spec.get(lp), lp.Bg)
				ratio := lr / dr
				require.Truef(t, ratio >= lo && ratio <= hi,
					"%s: %s holds %.2f contrast where %s holds %.2f (%.0f%% of it, outside %.0f-%.0f%%)",
					light, token, lr, dark, dr, ratio*100, lo*100, hi*100)
			}
		})
	}
}
```

- [x] **Step 5: Run it**

Run: `go test ./ui/theme/ -run TestLightPaletteMatchesItsDarkTwin -v`
Expected: PASS. If a token is outside the band, retune that token in `light.go` — the band is the specification, and a token 3× off is the bug this catches.

- [x] **Step 6: Verify the default's goldens did not move**

Run: `go test ./app/ -run 'TestFrameParity|TestFrameColourFingerprint' -count=1`
Expected: PASS, nothing regenerated. Nothing renders in the new palettes yet, so **if either moves, something leaked** — most likely a package-global theme mutation escaping a test's `restore()`.

- [x] **Step 7: Add the light colour fingerprint**

Add to `app/frameparity_test.go`, directly after `TestFrameColourFingerprint`:

```go
// TestLightFrameColourFingerprint is TestFrameColourFingerprint under the light
// palette. It exists because colours.txt is generated at the DEFAULT theme and
// must never move (that immovability is what proves `theme: auto` with no
// detection is a no-op), which would otherwise leave the light palette with no
// rendering guard at all — only its hex values checked, never what the frame
// actually emits.
//
// Regenerate with:
//
//	CS_UPDATE_GOLDEN=1 go test ./app/ -run TestLightFrameColourFingerprint
func TestLightFrameColourFingerprint(t *testing.T) {
	t.Cleanup(theme.Set("tokyo-night-day"))

	const w, h = 120, 40
	var b strings.Builder
	for _, fs := range frameStates() {
		counts := map[string]int{}
		for _, seq := range sgrRE.FindAllString(newParityHome(t, fs, w, h).View().Content, -1) {
			counts[seq]++
		}
		seqs := make([]string, 0, len(counts))
		for seq := range counts {
			seqs = append(seqs, seq)
		}
		sort.Strings(seqs)

		fmt.Fprintf(&b, "## %s (%d distinct)\n", fs.name, len(seqs))
		for _, seq := range seqs {
			fmt.Fprintf(&b, "  %-24s %d\n", strings.ReplaceAll(seq, "\x1b", "ESC"), counts[seq])
		}
	}
	compareGolden(t, filepath.Join("testdata", "colours-light.txt"), b.String())
}
```

**Watch the ordering trap.** `newParityHome` calls `t.Cleanup(theme.Set(cfg.Theme))` internally (`app/frameparity_test.go:165`), which **re-pins the theme to the default on every call**. So the `t.Cleanup(theme.Set(...))` above will be undone by the first `newParityHome`. Fix it by having the test set the theme *inside* the loop, after each `newParityHome`, or better: extend `newParityHome` with an explicit theme parameter. Take the second route — add a variant so the pin is not a race between two `Cleanup` stacks:

```go
// newParityHomeThemed is newParityHome pinned to a named theme instead of the
// config default. Split out rather than parameterizing every call site, because
// the default-theme path is what eighteen goldens depend on and it should not
// grow an argument that could be passed wrong.
func newParityHomeThemed(t *testing.T, fs frameState, w, h int, themeName string) *home {
	t.Helper()
	m := newParityHome(t, fs, w, h)
	// AFTER newParityHome, whose own theme.Set pin would otherwise win. The
	// frame is re-rendered by the caller reading View(), and styles read
	// theme.Current() lazily, so setting it here restyles the same model.
	t.Cleanup(theme.Set(themeName))
	return m
}
```

and call `newParityHomeThemed(t, fs, w, h, "tokyo-night-day")` in the test above.

- [x] **Step 8: Verify the pin actually took, then baseline**

This is the step that catches the trap above. First run **without** a golden and read the output:

```
CS_UPDATE_GOLDEN=1 go test ./app/ -run TestLightFrameColourFingerprint
```

Then diff it against the dark one:

```
diff app/testdata/colours.txt app/testdata/colours-light.txt | head -30
```

Expected: **substantially different colour values**, e.g. `38;2;36;63;126` (light `Fg` `#243f7e`) present in the light file and absent from the dark one. If the two files are identical, the theme pin did not take — the guard is vacuous and the golden is a lie. Fix the ordering before proceeding.

Take the sentinel from the *shipped* `light.go`, not from this plan's memory of it: an earlier draft of this step named `#3760bf`, the recalled `Fg` that never survived the oracle, and a sentinel that is absent from a *correct* golden sends the reader to debug the pin instead of the step.

- [x] **Step 9: Mutation-verify the light fingerprint**

Change `tokyoNightDay.Palette.Accent` from its shipped `#155fc4` to `lipgloss.Color("#155fc5")` (one bit). Run `go test ./app/ -run TestLightFrameColourFingerprint`. Expected: **FAIL** with a colour-count diff. Revert.

The mutation has to be one bit off *what is in the file*. `#2e7de9` — upstream's `blue`, which this step used to name — is the value the darkening rule replaced, so mutating to it would be a whole-colour change and would prove less than it looks: a guard can catch a large perturbation while missing the small one that a real edit looks like.

Then confirm the separation still holds: with that same mutation in place, `go test ./app/ -run TestFrameParity` must **PASS** — a colour change must not reach a layout golden. That is #393's mutation-proved separation, re-verified for the new file.

- [x] **Step 10: Document the new themes**

`README.md`'s `theme` row (line ~986) currently reads:

```
| `theme` | Appearance | string | `"tokyo-night"` | color palette + border style |
```

Update the description to name the light options. `TestReadmeDocumentsEveryConfigField` only checks the field's presence, so this is not guarded — `git grep tokyo-night README.md` afterwards to catch other mentions.

- [x] **Step 11: Run the full gate**

Run: `PATH=$PATH:$HOME/go/bin just ci && go test -race -shuffle=on ./...`
Expected: green. `-shuffle=on` matters here: two theme-mutating tests now exist in `app/`, and shuffle is what finds a `restore()` that leaks.

- [x] **Step 12: Commit**

```bash
git add ui/theme/light.go ui/theme/registry.go ui/theme/contrast_test.go \
        app/frameparity_test.go app/testdata/colours-light.txt README.md
git commit -m "feat(theme): add tokyo-night-day and catppuccin-latte

Ordinary registered themes, selectable by name today, and what theme: auto
will resolve to on a light terminal (#394 Stage E). Registering them
normally gets them every existing guard for free: canonical hex, glyph
widths, the settings picker, and the contrast oracle.

Adds colours-light.txt, because colours.txt is generated at the default
theme and must stay immovable — which would otherwise leave the light
palette with no guard on what it actually emits.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
```

### Task 4: Keep the splash legible on a light palette

fresco's luminance ramp walks L\* from a near-black floor *up* to the hue (`shade.go`'s `splashLumHexAt`), and `shadeAt`'s comment says why: *"stop 0 is near-black ink on a dark pane and never worth emitting."* On a light field that inverts — the dim cells become the most visible ones and the vignette edge becomes a dark halo. Atrium supplies hues, not the ramp; `rainRampFloor` is not a parameter. So this is **not** fixable by retuning Atrium's five splash tokens, which is where the issue's "fresco needs no change" claim breaks.

The escape hatch is one Atrium already wires: `fresco.Options.LumRange`. At `lumRange 0`, `shadeAt` short-circuits (`return hue, true`), the ramp is never consulted, and all brightness rides glyph density.

**Files:**
- Modify: `ui/splash.go` — the `fresco.Render` call at `ui/splash.go:101-107` and `splashLumRangeOverride` handling
- Modify: `ui/splash_test.go`

**Interfaces:**
- Consumes: `fresco.Options.LumRange *float64`, `theme.Current()`, `splashLumRangeOverride()` (existing, `ui/splash.go:97`), and `theme.IsLight` — **already created in Task 3 Step 2b**, which needed it for the agent brand accents. Task 4 is its second consumer, not its author.
- Produces: no new exported symbols in `ui/theme`. (An earlier draft had `IsLight` produced here; Step 2b moved it one task earlier and Steps 1–4 below are kept as its guard, not its introduction.)

- [x] **Step 1: Write the failing test for the light-palette predicate**

Add to `ui/theme/contrast_test.go` (it already has `relLuminance`):

```go
// TestIsLightAgreesWithTheRegistry pins which shipped palettes are light. The
// predicate exists because three consumers need the same answer — the agent
// brand accents (agent.go), the splash's brightness channel (ui/splash.go) and
// the scheme axis — and independent luminance thresholds would eventually
// disagree about a palette in the middle.
func TestIsLightAgreesWithTheRegistry(t *testing.T) {
	for _, name := range []string{"tokyo-night", "catppuccin-mocha", "unicode"} {
		require.Falsef(t, IsLight(Get(name).Palette), "%s is a dark palette", name)
	}
	for _, name := range []string{"tokyo-night-day", "catppuccin-latte"} {
		require.Truef(t, IsLight(Get(name).Palette), "%s is a light palette", name)
	}
}
```

- [x] **Step 2: Run it to verify it fails**

Run: `go test ./ui/theme/ -run TestIsLightAgreesWithTheRegistry -v`
Expected: FAIL to compile — `undefined: IsLight` — **but only if you are running this plan in its original order.** Task 3 Step 2b now creates `IsLight` and `ui/theme/scheme.go`, because the agent brand accents needed the predicate before the splash did. If Step 2b is already done, this step compiles and passes on the first run, and that is correct, not a missed red.

- [x] **Step 3: Confirm `IsLight`**

`ui/theme/scheme.go` already exists from Task 3 Step 2b. Check that it holds this, and write it there if Step 2b was skipped:

```go
package theme

// scheme.go owns the dark/light axis: what makes a palette light, and (from
// #394 Stage E) how a detected terminal background selects one.

// IsLight reports whether a palette is built for a light-background terminal.
//
// The test is the relative luminance of Bg, at WCAG's 0.5-ish midpoint. Bg is the
// right token to ask because Atrium never paints a full-screen background — Bg is
// its statement about what the terminal itself is showing.
//
// One predicate, three consumers: the agent brand accents (agent.go), the
// splash's brightness channel (ui/splash.go) and the scheme axis below. Two
// independent thresholds would eventually disagree about a palette in the
// middle, and the disagreement would show up as a splash tuned for the wrong
// polarity.
func IsLight(p Palette) bool {
	return relLuminanceOf(p.Bg) > 0.35
}
```

`relLuminance` currently lives in `contrast_test.go` and cannot be called from production code. Move it into `ui/theme/scheme.go` as `relLuminanceOf` (exported name not needed — same package), and have `contrast_test.go` call `relLuminanceOf` instead of its own copy. **Delete the test-file copy** rather than leaving two; `unused` will flag the orphan and, worse, the two could drift.

The threshold is 0.35, not 0.5: `#e1e2e7` has relative luminance ≈ 0.77 and `#1a1b26` ≈ 0.011, so the gap is enormous and the exact cut is not load-bearing — 0.35 leaves room for a genuinely mid-tone palette to be classed light, which is the safer error (a light-tuned splash on a mid background reads; a dark-tuned one on a mid background inverts).

- [x] **Step 4: Run it to verify it passes**

Run: `go test ./ui/theme/ -count=1`
Expected: PASS, including the moved `relLuminanceOf`.

- [x] **Step 5: Write the failing test for the splash's light path**

Add to `ui/splash_test.go`:

```go
// On a light palette the splash must not use fresco's luminance channel. Its ramp
// walks L* from a near-black floor UP to the hue (fresco's shade.go), so on a
// light field the dim cells come out as the darkest ink on screen — the vignette
// edge inverts into a halo. lumRange 0 short-circuits that ramp entirely and puts
// all brightness back on glyph density, which is directionally correct on either
// polarity.
//
// This asserts the resolved LumRange rather than the rendered field, because the
// rendered field is fresco's business and the decision is Atrium's.
func TestSplashLumRangeIsZeroOnALightPalette(t *testing.T) {
	defer theme.Set("tokyo-night")()
	dark := splashLumRange(fresco.Tunnel)
	require.Nil(t, dark, "on a dark palette Atrium must not override the variant's shipped lumRange")

	defer theme.Set("tokyo-night-day")()
	light := splashLumRange(fresco.Tunnel)
	require.NotNil(t, light, "a light palette must pin lumRange")
	require.Equal(t, 0.0, *light, "lumRange 0 is the endpoint that skips the ramp")
}

// The dev override still wins, rain included. It is the knob used to tune a variant
// by eye, so a palette-derived default that silently ignored it would make the
// tuning loop lie.
func TestSplashLumRangeOverrideBeatsThePaletteDefault(t *testing.T) {
	t.Cleanup(theme.Set("tokyo-night-day"))
	pinSplashLumRange(t, 0.75, true)

	got := splashLumRange(fresco.Tunnel)
	require.NotNil(t, got)
	require.Equal(t, 0.75, *got, "the explicit override must beat the light-palette default")
}

// pinSplashLumRange drives the package's own override state directly, restoring it
// when the test ends. NOT t.Setenv: splash_variants.go resolves
// ATRIUM_SPLASH_LUMRANGE in init(), so by the time a test body runs the read has
// already happened and setting the variable changes nothing. That is the same
// constraint TestLumRangeEnvReachesRender answers by spawning a subprocess —
// correct there, because its subject IS the env plumbing. Here the subject is the
// ladder consuming the resolved value, so writing that value is more direct. Same
// package, so no production seam is added; the lock matters because
// splashLumRangeOverride reads both vars under splashSelMu and -race runs this
// package.
func pinSplashLumRange(t *testing.T, v float64, set bool) {
	t.Helper()
	splashSelMu.Lock()
	prevVal, prevSet := splashLumRangeVal, splashLumRangeSet
	splashLumRangeVal, splashLumRangeSet = v, set
	splashSelMu.Unlock()
	t.Cleanup(func() {
		splashSelMu.Lock()
		defer splashSelMu.Unlock()
		splashLumRangeVal, splashLumRangeSet = prevVal, prevSet
	})
}
```

**The trap this step hides**, and the reason the helper above exists: the first draft of this test used `t.Setenv("ATRIUM_SPLASH_LUMRANGE", "0.75")` and could never have passed. `ui/splash_variants.go`'s `init()` resolves that variable at package load, before any test body runs — which is exactly why the pre-existing `TestLumRangeEnvReachesRender` drives it through a subprocess. A value read in `init()` is unreachable from `t.Setenv`, and the failure looks like a broken ladder rather than a broken test.

Before writing this, read `ui/splash.go`'s override handling and `splashLumRangeOverride`'s real signature (`grep -n ATRIUM_SPLASH_LUMRANGE ui/*.go`).

- [x] **Step 6: Run it to verify it fails**

Run: `go test ./ui/ -run TestSplashLumRange -v`
Expected: FAIL to compile — `undefined: splashLumRange`.

- [x] **Step 7: Extract the ladder in `ui/splash.go`**

Replace the inline override handling at `ui/splash.go:95-99` with a named ladder, and call it from `splashScene`:

```go
// splashLumRange resolves how the splash field splits brightness between glyph
// density and colour luminance: the dev override if set, else 0 on a light
// palette, else nil (fresco's per-variant default).
//
// The light rung is not a taste choice. fresco's luminance ramp walks L* from a
// near-black floor up to the hue, so its dim end is DARK — correct on a dark
// pane, inverted on a light one, where "barely there" would render as the
// heaviest ink on screen. lumRange 0 is fresco's documented endpoint where the
// ramp is never consulted at all and density carries everything, which is the
// only direction that works on both polarities without a fresco change.
//
// A monochrome render takes the same rung for the mirror-image reason: with
// colour stripped, a colour-borne brightness channel carries nothing, so the
// field would flatten. See theme.Mono (#394 Stage D).
//
// The variant is a parameter rather than another splashActiveVariant() call so
// the rung is a pure function of what the caller is about to render. The
// process-wide pick is latched, so a second call would agree today; taking it
// as an argument means the rule cannot drift from the field it describes.
func splashLumRange(variant fresco.Variant) *float64 {
	if r, ok := splashLumRangeOverride(); ok {
		return &r
	}
	if theme.IsLight(theme.Current().Palette) && variant != fresco.Rain {
		zero := 0.0
		return &zero
	}
	return nil
}
```

The `variant` parameter and the `!= fresco.Rain` clause are **Step 9's findings, folded back into this step** so the file has one signature rather than two. Write them here; Step 9 is where the measurement that justifies them is recorded, and it is the step that would have discovered them if you were running this plan cold. The doc comment above is abridged — the shipped one in `ui/splash.go` carries the full measured numbers.

and at the `fresco.Render` call:

```go
	field := fresco.Render(width, height, frame, fresco.Options{
		Palette:  splashPalette(theme.Current().Palette),
		Variant:  variant,
		FocalRow: focalRow,
		LumRange: splashLumRange(variant),
		Profile:  splashProfile(),
	})
```

Delete the now-dead `var lum *float64` block above it.

- [x] **Step 8: Run it to verify it passes**

Run: `go test ./ui/ -count=1`
Expected: PASS.

- [x] **Step 9: Look at it — the step no test can do**

Build and drive the splash on a **real light terminal**:

```bash
just build
# Isolate: HOME alone does NOT isolate the tmux socket.
export TMUX_TMPDIR=/tmp/atr-light          # short path: sockets must fit sun_path
export HOME=/tmp/atr-light-home
mkdir -p "$TMUX_TMPDIR" "$HOME"
./bin/atrium
```

Set the terminal to a light background, then in Atrium: `s` for settings, cycle `theme` to `tokyo-night-day`, and view the empty-state splash (kill or pause all sessions, or start in the fresh `HOME` which has none). Cycle `splash` through all five variants.

**ANSWERED. It reads — for four of five variants — and the fifth is why this step existed.**

Method, recorded because it is reusable and beats a headless tmux run: dump `SplashScreensaver(120, 40, frame)` per variant and setting from a tiny Go harness in its own module (`replace` to the worktree), then rasterize the ANSI to PNG with PIL and a monospace font on the palette's own `Bg`, and look at the images. Then back the eyeball with a number — **ink coverage and the edge:core ratio *is* the vignette**, which is what turns "this looks wrong" into a measurement someone can act on.

Measured at 120×40 under tokyo-night-day:

| variant | ink @ lumRange 0 | edge:core, ramp | edge:core, lumRange 0 |
|---|---|---|---|
| tunnel | 82.6% | 77:96 | 59:93 |
| ripple | 26.5% | 25:43 | 13:33 |
| galaxy | 63.3% | 65:94 | 32:71 |
| aurora | 19.5% | 4:42 | 0:38 |
| **rain** | **95.0%** | 16:45 | **83:100 — no vignette** |

`lumRange 0` is **not** confetti on light. Four variants read *better* at 0 than on the ramp, because a density vignette survives the polarity flip and a luminance one does not.

**Rain is exempt, and no test could have predicted it.** Rain's brightness is entirely luminance — fresco ships it at `lumRange: 1` — so moving that onto density leaves it nowhere to go and the pane fills solid. `ui/splash_variants.go`'s own comment already called that out as the absurdity of a low override on rain ("the pane fills with white katakana"); the light rung would have promoted a documented dev-only footgun into the shipped path for one launch in five, rain being both a rotation member *and* `splashDefaultVariant`. Leaving rain on the ramp is merely inverted, which is the lesser harm. So the ladder takes the variant as a parameter:

```go
if theme.IsLight(theme.Current().Palette) && variant != fresco.Rain {
```

That is the signature Step 7 above already writes — folded back there so the plan states it once, rather than shipping a no-argument version in Step 7 and amending it here. This step is where the measurement that earns it lives.

Splash-off on a light palette — the fallback this step originally named — was **rejected on the evidence**: it would discard four working variants to avoid one broken one.

Note for Stage D: rain's exemption carries over to `theme.Mono` unchanged, and Stage D's Task 6 Step 5 is where that is written down. The reasoning is not "it has neither channel" — that is true but does not decide anything — it is that `lumRange 0` fills rain's pane *solid*, and what causes that is rain's own luminance-only brightness, not whatever stripped the colour.

Either way, file the fresco issue — **filed as ZviBaratz/fresco#82**:

```bash
gh issue create --repo ZviBaratz/fresco \
  --title "Light-background support: the luminance ramp walks toward black" \
  --body "$(cat <<'EOF'
`splashLumHexAt` walks L* from `rainRampFloor` (near-black) up to the hue, and
`shadeAt` documents the consequence: "stop 0 is near-black ink on a dark pane and
never worth emitting."

On a **light** background that inverts. The dim cells become the darkest ink on
screen, so a field's fading edge renders as a dark halo instead of fading out —
the opposite of the design. A caller cannot fix it by supplying lighter palette
anchors, because the ramp's direction and its floor are fresco's, and
`rainRampFloor` is not a parameter.

Atrium's workaround (atrium#394) is `Options.LumRange: 0`, which short-circuits
`shadeAt` entirely and puts all brightness back on glyph density. That works on
either polarity but gives up the luminance channel — i.e. the fields render as
the pure density ramp this channel was added to replace.

A proper fix would be either a polarity option that inverts the ramp (walk L*
down from a near-white ceiling to the hue), or exposing `rainRampFloor` so a
caller can raise it above the hue's own L*.
EOF
)"
```

- [x] **Step 10: Verify no golden moved, then gate**

Run: `go test ./app/ -run 'TestFrameParity|TestFrameColourFingerprint|TestLightFrameColourFingerprint' -count=1`

Expected: `TestFrameParity` and `TestFrameColourFingerprint` PASS unchanged. `TestLightFrameColourFingerprint` — check whether the splash renders in any of the 18 states at 120×40 (`grep -n screensaver app/frameparity_test.go`; the `screensaver` state does). If it does, this golden **legitimately moves**; regenerate it, read the diff, and confirm the change is confined to the `## screensaver` block.

Run: `PATH=$PATH:$HOME/go/bin just ci && go test -race -shuffle=on ./...`

- [x] **Step 11: Commit**

```bash
git add ui/splash.go ui/splash_test.go ui/theme/scheme.go ui/theme/contrast_test.go
git commit -m "fix(splash): skip fresco's luminance ramp on a light palette

fresco's ramp walks L* from a near-black floor up to the hue, so its dim end
is dark — correct on a dark pane, inverted on a light one, where the fading
edge of a field renders as the heaviest ink on screen. Atrium supplies hues,
not the ramp, and rainRampFloor is not a parameter, so this is not fixable
by retuning the five splash tokens.

LumRange 0 is fresco's documented endpoint where the ramp is never consulted
and density carries all brightness — the one direction that works on both
polarities without a fresco change. Filed ZviBaratz/fresco for a real light
ramp.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
```

---

## Stage D — `NO_COLOR` ✅ SHIPPED (#568, `1609261`)

Measured, not assumed: Bubble Tea already runs `colorprofile.Detect(p.output, p.environ)` (`tea.go:1083`) and colorprofile does consult `NO_COLOR` — but through `strconv.ParseBool`, so `NO_COLOR=yes` / `x` / `0` / `2` are all ignored while no-color.org mandates off for **any non-empty value**. And `colorprofile.Ascii` is the right target, not `NoTTY`: measured through `colorprofile.Writer`, `Ascii` drops every colour form (including hand-written SGR like the overlay fade's) and keeps bold/italic/underline, where `NoTTY` strips those too and flattens the hierarchy that makes monochrome navigable.

### Task 5: Honour `NO_COLOR` at the renderer, with its own oracle

**Files:**
- Create: `ui/theme/mono.go`
- Create: `app/nocolour_test.go`
- Modify: `app/app.go` — the `tea.NewProgram` call at `app/app.go:58`
- Modify: `go.mod` — promote `github.com/charmbracelet/colorprofile` from indirect to direct
- Modify: `README.md`

**Interfaces:**
- Consumes: `colorprofile.Profile`, `colorprofile.Ascii`, `colorprofile.Writer`, `tea.WithColorProfile` (`options.go:153`).
- Produces:
  - `func theme.SetMono(on bool)` and `func theme.Mono() bool` — the predicate the **non-lipgloss** emitters consult. Task 6's only dependency.
  - `func theme.NoColorRequested(environ []string) bool` — the spec-compliant env test, taking `environ` so it is testable without `t.Setenv`.

- [x] **Step 1: Write the failing test for the env predicate**

> **Carry Stage C's trap in.** `NoColorRequested` taking `environ` as a parameter is what makes it testable at all — keep that, and resist "simplifying" it to read `os.Environ()` internally. Stage C lost time to the mirror image: a test used `t.Setenv` against a value another package resolved in `init()`, which no amount of setting could reach. Any predicate whose input is read at package load needs either a parameter (this) or a subprocess (`TestLumRangeEnvReachesRender`). `SetMono` is the other half of the same shape — a package global with a restore, so it must be exercised under `-shuffle=on`, which is what finds a `restore()` that leaks.

Create `ui/theme/mono_test.go`:

```go
package theme

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// NoColorRequested implements no-color.org literally: colour is off when the
// variable is PRESENT AND NON-EMPTY, regardless of value. That is deliberately
// stricter than the dependency's own handling — colorprofile parses NO_COLOR with
// strconv.ParseBool, so NO_COLOR=yes, =x, =0 and =2 all leave colour ON, which is
// four spec violations Atrium would inherit by doing nothing. Measured on
// colorprofile v0.4.3.
func TestNoColorRequested(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  []string
		want bool
	}{
		{"absent", []string{"TERM=xterm-256color"}, false},
		{"empty is not a request", []string{"NO_COLOR="}, false},
		{"one", []string{"NO_COLOR=1"}, true},
		{"true", []string{"NO_COLOR=true"}, true},
		// The four the dependency gets wrong. Each of these is a spec-mandated
		// "off" that colorprofile.Env leaves at TrueColor.
		{"zero is still a request", []string{"NO_COLOR=0"}, true},
		{"false is still a request", []string{"NO_COLOR=false"}, true},
		{"yes", []string{"NO_COLOR=yes"}, true},
		{"arbitrary", []string{"NO_COLOR=x"}, true},
		{"later entry wins, as os.Environ semantics", []string{"NO_COLOR=1", "NO_COLOR="}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, NoColorRequested(tc.env))
		})
	}
}

// Mono is a global that renderers read, so it must be restorable the way Set and
// SetGlyphSet are — otherwise one test leaves every later one monochrome, which
// under -shuffle is a different suite every run.
func TestSetMonoRestores(t *testing.T) {
	require.False(t, Mono(), "colour is on by default")
	restore := SetMono(true)
	require.True(t, Mono())
	restore()
	require.False(t, Mono())
}
```

- [x] **Step 2: Run it to verify it fails**

Run: `go test ./ui/theme/ -run 'TestNoColorRequested|TestSetMonoRestores' -v`
Expected: FAIL to compile — `undefined: NoColorRequested`, `undefined: Mono`.

- [x] **Step 3: Implement `ui/theme/mono.go`**

```go
package theme

import "strings"

// mono.go is the NO_COLOR seam.
//
// Colour is removed in two different places, on purpose, because two different
// kinds of emitter produce it:
//
//  1. Everything Atrium renders through Lip Gloss is stripped at the RENDERER, by
//     handing Bubble Tea the colorprofile.Ascii profile (app.Run). That covers the
//     frame wholesale, including escapes Atrium writes by hand — the overlay
//     fade's SGR rewrite is the case that matters — and it keeps bold, italic and
//     underline, which is what leaves a monochrome UI navigable. colorprofile's
//     NoTTY profile would strip those too and flatten the hierarchy; measured.
//
//  2. Everything Atrium emits that Lip Gloss never sees has to opt out itself,
//     and Mono() is how it asks. There are two such surfaces: tmux's own #[fg=...]
//     markup and status-style (tmux renders the status line, so the profile never
//     touches it — and it is the most colour-saturated surface Atrium owns), and
//     the splash's colour-borne brightness channel (with colour gone, a channel
//     that spends brightness on colour spends it on nothing).
//
// Mono deliberately does NOT blank the palette. A monochrome palette would defeat
// (1) — the renderer's own strip is what handles the hand-written escapes — and it
// would lose the bold/italic/underline hierarchy along the way. Do not "simplify"
// this by making Current() return greys.

var mono bool

// Mono reports whether colour output is suppressed. Read by the emitters Lip
// Gloss does not cover; see the file comment for why that is not everything.
func Mono() bool { return mono }

// SetMono suppresses or restores colour for the non-Lip-Gloss emitters, returning
// a function that restores the previous value — matching Set and SetGlyphSet, so
// a test cannot leave the rest of the suite monochrome.
func SetMono(on bool) (restore func()) {
	prev := mono
	mono = on
	return func() { mono = prev }
}

// NoColorRequested reports whether environ asks for no colour, per
// https://no-color.org: the variable being PRESENT AND NON-EMPTY is the request,
// whatever its value.
//
// Atrium implements this itself rather than leaning on the dependency, because
// colorprofile parses NO_COLOR through strconv.ParseBool — so NO_COLOR=yes, =x,
// =0 and =2 leave colour fully on. Four spec violations, inherited for free by
// anyone who assumes the stack handles it. Measured on colorprofile v0.4.3.
//
// environ is a parameter rather than an os.Environ() call so the rule is testable
// as a pure function. Later entries win, matching os.Environ semantics for a
// duplicated name.
func NoColorRequested(environ []string) bool {
	requested := false
	for _, kv := range environ {
		name, value, ok := strings.Cut(kv, "=")
		if !ok || name != "NO_COLOR" {
			continue
		}
		requested = value != ""
	}
	return requested
}
```

- [x] **Step 4: Run it to verify it passes**

Run: `go test ./ui/theme/ -count=1`
Expected: PASS.

- [x] **Step 5: Write the failing `NO_COLOR` frame oracle**

Create `app/nocolour_test.go`. This is a **separate** oracle because `TestFrameColourFingerprint` reads `View().Content`, which is *pre-writer* — the profile route is invisible to it.

```go
package app

import (
	"bytes"
	"regexp"
	"strings"
	"testing"

	"github.com/charmbracelet/colorprofile"
	"github.com/stretchr/testify/require"
)

// nocolour_test.go is the NO_COLOR oracle, and it has to be its own file because
// the existing colour fingerprint cannot see what it guards.
// TestFrameColourFingerprint reads View().Content — the string the model
// produces, BEFORE Bubble Tea's writer down-samples it — so a fix that lives in
// the colour profile is completely invisible there. This test puts the frame
// through the writer instead.

// colourSGRRE matches the SGR forms that carry COLOUR: truecolor, 256-colour,
// and the 16-colour foreground/background ranges (30-37, 40-47, 90-97, 100-107).
// It deliberately does not match bold/italic/underline — those must SURVIVE, and
// asserting their survival is what stops a later "fix" reaching for NoTTY, which
// strips them and flattens the whole hierarchy.
var colourSGRRE = regexp.MustCompile(`\x1b\[[0-9;:]*(?:[34]8[;:]|(?:^|;)(?:3[0-7]|4[0-7]|9[0-7]|10[0-7])(?:;|m))[0-9;:]*m`)

// asciiProfileFrame renders a state and pushes it through the writer Bubble Tea
// uses under NO_COLOR, returning what would actually reach the terminal.
func asciiProfileFrame(t *testing.T, fs frameState, w, h int) string {
	t.Helper()
	var buf bytes.Buffer
	writer := &colorprofile.Writer{Forward: &buf, Profile: colorprofile.Ascii}
	_, err := writer.WriteString(newParityHome(t, fs, w, h).View().Content)
	require.NoError(t, err)
	return buf.String()
}

// TestNoColorFrameCarriesNoColour is AC#3's mechanical half: under the Ascii
// profile no colour survives, in any state.
func TestNoColorFrameCarriesNoColour(t *testing.T) {
	const w, h = 120, 40
	for _, fs := range frameStates() {
		t.Run(fs.name, func(t *testing.T) {
			out := asciiProfileFrame(t, fs, w, h)
			if found := colourSGRRE.FindAllString(out, -1); len(found) > 0 {
				t.Errorf("state %s emitted %d colour sequences under the Ascii profile; first: %q",
					fs.name, len(found), strings.ReplaceAll(found[0], "\x1b", "ESC"))
			}
		})
	}
}

// TestNoColorFramePreservesAttributes is the other half, and it is the one that
// makes the UI navigable rather than merely colourless: bold survives. Without
// this assertion, swapping Ascii for NoTTY would keep TestNoColorFrameCarriesNoColour
// green while destroying every non-colour distinction on screen.
func TestNoColorFramePreservesAttributes(t *testing.T) {
	const w, h = 120, 40
	// The default state's list renders a bold display name; the help overlay's
	// title is bold too. Two states, so the assertion does not rest on one.
	for _, want := range []string{"default", "help"} {
		var found bool
		for _, fs := range frameStates() {
			if fs.name != want {
				continue
			}
			out := asciiProfileFrame(t, fs, w, h)
			found = strings.Contains(out, "\x1b[1m")
			require.Truef(t, found,
				"state %s lost bold under the Ascii profile: monochrome must keep the weight hierarchy", want)
		}
		require.Truef(t, found, "state %q not present in frameStates()", want)
	}
}
```

- [x] **Step 6: Run it to verify the colour half fails and the attribute half passes**

Run: `go test ./app/ -run 'TestNoColorFrame' -v`

Expected: `TestNoColorFramePreservesAttributes` **PASS** (the profile already strips colour but keeps attributes — this test passes before any production change, which is fine: it is a regression guard, not a driver). `TestNoColorFrameCarriesNoColour` should **also pass** immediately, because the writer does the work. That is the point of the measurement: this half of AC#3 is already true at the writer.

**If `TestNoColorFrameCarriesNoColour` fails**, read the reported sequence. Either `colourSGRRE` is over- or under-matching (test it against the strings in the design doc's measured `Ascii` output), or a frame carries colour by a path the writer does not convert — which would be a real finding worth stopping on.

- [x] **Step 7: Wire the profile in `app.Run`**

Modify `app/app.go:58`:

```go
	// NO_COLOR (https://no-color.org) is honoured by pinning the renderer's colour
	// profile rather than by blanking the palette. The profile converts every
	// colour Bubble Tea writes — including escapes Atrium hand-wrote into the frame,
	// which the overlay fade does — while leaving bold, italic and underline intact,
	// so the UI goes monochrome without going flat. colorprofile.NoTTY would strip
	// those attributes too; measured.
	//
	// Atrium tests the variable itself instead of relying on colorprofile.Detect,
	// which parses it through strconv.ParseBool and so ignores NO_COLOR=yes, =x, =0
	// and =2 — all four of which the spec says mean off. theme.Mono carries the same
	// decision to the emitters this profile cannot reach (tmux markup, the splash).
	opts := []tea.ProgramOption{tea.WithContext(ctx)}
	if theme.NoColorRequested(os.Environ()) {
		theme.SetMono(true)
		opts = append(opts, tea.WithColorProfile(colorprofile.Ascii))
	}
	p := tea.NewProgram(h, opts...)
```

Add imports: `"os"` and `"github.com/charmbracelet/colorprofile"`. `theme` and `tea` are already imported in `app/app.go`.

The `SetMono` restore function is deliberately discarded here — this is process startup, the same way `theme.Set`'s return is discarded at startup.

- [x] **Step 8: Promote the dependency and verify**

```bash
go mod tidy
git diff go.mod
```

Expected: `github.com/charmbracelet/colorprofile v0.4.3` moves out of the `// indirect` block. No version change — it is already in the graph at that version via Bubble Tea.

- [x] **Step 9: Verify the goldens did not move and the gate is green**

Run: `go test ./app/ -run 'TestFrameParity|TestFrameColourFingerprint|TestLightFrameColourFingerprint' -count=1`
Expected: PASS unchanged. `View().Content` is untouched by a profile change, which is exactly why the new oracle was needed.

Run: `PATH=$PATH:$HOME/go/bin just ci && go test -race -shuffle=on ./...`

- [x] **Step 10: Mutation-verify both halves of the oracle**

1. Change `colorprofile.Ascii` to `colorprofile.TrueColor` in `app.Run`. Neither new test notices — they construct their own writer. **That is a real gap**: nothing asserts `Run` picks the right profile. Close it with a third test that does not need a live program:

```go
// TestNoColorPicksTheAsciiProfile guards the WIRE, not the mechanism. The two
// tests above build their own writer, so both stay green if app.Run stops passing
// the profile at all — the same class as #391's "a derived value passed as an
// argument is a wire nothing guards".
func TestNoColorPicksTheAsciiProfile(t *testing.T) {
	require.Equal(t, colorprofile.Ascii, noColorProfile(),
		"NO_COLOR must select Ascii: it drops colour and keeps bold/italic/underline, where NoTTY strips those too")
}
```

Extract the constant into a named function in `app/app.go` so the test can reach it:

```go
// noColorProfile is the colour profile NO_COLOR selects. Named rather than
// inlined so the choice between Ascii and NoTTY — which is the difference between
// a monochrome UI and a flat one — is one identifier a test can pin.
func noColorProfile() colorprofile.Profile { return colorprofile.Ascii }
```

and use `noColorProfile()` at the call site. Re-run; then mutate it to `colorprofile.NoTTY` and confirm the new test **fails**. Revert.

2. In `colourSGRRE`, delete the `[34]8[;:]` alternative. Confirm `TestNoColorFrameCarriesNoColour` still passes with a *deliberately* colourful writer (temporarily set `Profile: colorprofile.TrueColor` in `asciiProfileFrame`) — it should **fail** with the full regex and **pass** with the mutated one, proving the truecolor branch is load-bearing. Revert both.

- [x] **Step 11: Document it**

Add a `NO_COLOR` line to `README.md`, in the section that covers environment variables (`grep -n "^#### \|^### " README.md | grep -i "environ\|config"` to find it; if there is no such section, put it beside the `theme` row's table with a sentence). State that any non-empty value disables colour and that bold/italic/underline are kept.

- [x] **Step 12: Commit**

```bash
git add ui/theme/mono.go ui/theme/mono_test.go app/app.go app/nocolour_test.go go.mod go.sum README.md
git commit -m "feat: honour NO_COLOR (any non-empty value)

Not free, despite the v2 stack: colorprofile parses NO_COLOR through
strconv.ParseBool, so NO_COLOR=yes, =x, =0 and =2 all leave colour fully on
— four spec violations inherited by assuming the dependency handles it. So
Atrium tests the variable itself and pins the renderer to
colorprofile.Ascii, which drops every colour form (including the overlay
fade's hand-written SGR) while keeping bold, italic and underline. NoTTY
would strip those too and flatten the hierarchy that makes monochrome
navigable; measured.

Needs its own oracle: the existing colour fingerprint reads View().Content,
which is pre-writer, so a profile-level fix is invisible to it.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
```

### Task 6: Decolourise the surfaces the profile cannot reach

tmux renders its own status line, so Bubble Tea's profile never touches it. Left alone, `NO_COLOR` would be violated inside every attached pane — the surface where a user spends most of their time.

**Files:**
- Modify: `ui/contextbar.go` — `ComposeSessionContext` (`ui/contextbar.go:66`)
- Modify: `session/tmux/config.go` — `renderManagedConfig` (`session/tmux/config.go:67`)
- Modify: `ui/splash.go` — `splashLumRange`
- Modify: `ui/contextbar_test.go`, `session/tmux/config_test.go`, `ui/splash_test.go`

**Interfaces:**
- Consumes: `theme.Mono()` from Task 5.
- Produces: no new exported symbols.

- [x] **Step 1: Write the failing tests**

Add to `ui/contextbar_test.go` (check the real file name first: `ls ui/contextbar*_test.go`; if absent, create `ui/contextbar_test.go` in package `ui`):

```go
// Under NO_COLOR the pushed header must carry no tmux colour markup. tmux renders
// the status line itself, so Bubble Tea's colour profile never sees this string —
// it is the one surface where "the stack handles it" is false, and it is the most
// colour-saturated thing Atrium owns.
//
// #[bold] stays. Weight is not colour, and it is what keeps the session name
// findable once the tint is gone.
func TestComposeSessionContextDropsColourUnderMono(t *testing.T) {
	defer theme.SetMono(true)()

	inst := newBarTestInstance(t)
	_, left := ComposeSessionContext(inst, "myrepo")

	require.NotContains(t, left, "#[fg=", "no foreground markup under NO_COLOR")
	require.Contains(t, left, "#[bold]", "weight must survive: it replaces colour as the hierarchy")
	require.Contains(t, left, "myrepo", "the content itself is unchanged")
}

// And with colour on, the markup is still there — the negative control. Without
// it, a ComposeSessionContext that emitted no colour at all would pass the test
// above, and the assertion would prove nothing.
func TestComposeSessionContextKeepsColourByDefault(t *testing.T) {
	inst := newBarTestInstance(t)
	_, left := ComposeSessionContext(inst, "myrepo")
	require.Contains(t, left, "#[fg=", "colour markup is the default")
}
```

`newBarTestInstance` must build a `*session.Instance` the way the package's existing tests do — `grep -rn "session.NewInstance" ui/*_test.go` and copy that construction rather than inventing one. It must not touch the real data dir.

Add to `session/tmux/config_test.go`:

```go
// The managed config's status-style must carry no colours under NO_COLOR. tmux
// reads this file, not Bubble Tea, so the profile cannot help here either.
func TestRenderManagedConfigDropsColourUnderMono(t *testing.T) {
	defer theme.SetMono(true)()

	out, err := renderManagedConfig(true)
	require.NoError(t, err)
	require.NotContains(t, string(out), "bg=#", "no hex background under NO_COLOR")
	require.NotContains(t, string(out), "fg=#", "no hex foreground under NO_COLOR")
	require.Contains(t, string(out), "status", "the bar itself is still configured")
}

// Negative control: with colour on, the hex IS interpolated.
func TestRenderManagedConfigCarriesThemeColoursByDefault(t *testing.T) {
	out, err := renderManagedConfig(true)
	require.NoError(t, err)
	require.Contains(t, string(out), "bg="+theme.Hex(theme.Current().Palette.BarBg))
}
```

Add to `ui/splash_test.go`:

```go
// Under NO_COLOR the splash takes the same rung a light palette does, for the
// mirror-image reason: with colour stripped, a brightness channel that spends
// itself on colour spends it on nothing, and the field flattens to a uniform
// wash. lumRange 0 puts brightness back on glyph density, which survives
// monochrome.
func TestSplashLumRangeIsZeroUnderMono(t *testing.T) {
	defer theme.Set("tokyo-night")() // a DARK palette, so only Mono can be the cause
	defer theme.SetMono(true)()

	got := splashLumRange(fresco.Tunnel)
	require.NotNil(t, got)
	require.Equal(t, 0.0, *got)
}

// Rain keeps its exemption under Mono, for the reason Stage C measured on light:
// its brightness is entirely luminance, so lumRange 0 leaves it nowhere to go and
// the pane fills solid (95% of cells inked, edge:core 83:100 — no vignette). That
// is true of rain whatever stripped the colour. A flat field is the lesser harm
// against a solid one, and fresco#82 is where it gets fixed properly.
func TestSplashLumRangeExemptsRainUnderMono(t *testing.T) {
	defer theme.Set("tokyo-night")()
	defer theme.SetMono(true)()

	require.Nil(t, splashLumRange(fresco.Rain),
		"rain must keep its shipped lumRange under NO_COLOR: at 0 the pane fills solid")
}
```

- [x] **Step 2: Run them to verify they fail**

Run: `go test ./ui/ ./session/tmux/ -run 'Mono|ColourByDefault|ColoursByDefault' -v`
Expected: the three Mono tests FAIL; the two negative controls PASS — and so does `TestSplashLumRangeExemptsRainUnderMono`, which is a third negative control rather than a fourth failing test (see Step 5 for why, and for how to prove it can fail at all).

- [x] **Step 3: Implement the `ui/contextbar.go` change**

```go
// tmuxFg wraps s in tmux foreground markup, or returns it unchanged under
// NO_COLOR. tmux renders the status line itself, so Bubble Tea's colour profile
// never sees this string — this is the one surface that has to opt out by hand.
// #[default] is still emitted either way: it resets attributes as well as colour,
// and the format relies on it to close each field.
func tmuxFg(colour, s string) string {
	if theme.Mono() {
		return s + "#[default]"
	}
	return "#[fg=" + colour + "]" + s + "#[default]"
}
```

and rewrite the three `fmt.Fprintf` colour sites in `ComposeSessionContext` to route through it. Read `ui/contextbar.go:74-82` carefully first: the `#[default]` placement is load-bearing (its comment explains that it resets fg *and* attributes back to `status-style`), and the trailing space after the agent glyph must be preserved exactly, or the bar's spacing shifts.

- [x] **Step 4: Implement the `session/tmux/config.go` change**

In `renderManagedConfig`, leave the template alone and empty the values instead:

```go
	th := theme.Current()
	barBg, barFg := theme.Hex(th.Palette.BarBg), theme.Hex(th.Palette.Fg)
	if theme.Mono() {
		// tmux reads this file, not Bubble Tea, so the colour profile cannot strip
		// it — the values have to be absent. tmux treats an empty option value as
		// "unset", which is the honest translation of "no colour" and is exactly
		// what theme.Hex already returns for a nil colour.
		barBg, barFg = "", ""
	}
```

**Then check what the template does with empty values.** `set-option -g status-style "bg=,fg="` may be a tmux parse error, which `validateConfig` would catch and then disable the whole managed config — a much worse outcome than a coloured bar. Verify:

```bash
tmux -L atr-probe new-session -d 'sleep 30'
tmux -L atr-probe set-option -g status-style "bg=,fg=" ; echo "exit=$?"
tmux -L atr-probe kill-server
```

If it errors, guard the whole `status-style` line in the template with `{{if .BarBg}}` instead of interpolating empties, and assert in the test that the line is absent rather than that it holds no hex. Adjust `TestRenderManagedConfigDropsColourUnderMono` to match whichever shape is correct.

- [x] **Step 5: Implement the `ui/splash.go` change**

Extend `splashLumRange`'s light rung. The shipped rung is `if theme.IsLight(theme.Current().Palette) && variant != fresco.Rain` — **read it in `ui/splash.go` before editing**, because Mono joins the left side of that `&&` and must not be allowed to escape the rain exemption on the right:

```go
	if (theme.Mono() || theme.IsLight(theme.Current().Palette)) && variant != fresco.Rain {
		zero := 0.0
		return &zero
	}
```

The parenthesisation is the whole point. `theme.Mono() || theme.IsLight(…) && variant != fresco.Rain` parses as `Mono || (IsLight && …)` in Go, which puts rain back on lumRange 0 whenever `NO_COLOR` is set — the solid-pane failure Stage C measured, reintroduced through the one variant that is both a rotation member and `splashDefaultVariant`.

`TestSplashLumRangeExemptsRainUnderMono` is the guard for exactly that, and note what kind of guard it is: it is **green before this step and green after it**, because rain returns `nil` on a dark palette either way. It is not a failing-first test — it is the negative control that only the wrong parenthesisation can break. Prove it can: write the unparenthesised version first, watch it go red, then fix it. A guard never seen red is a guard nobody has checked.

Rain's exemption is answered here rather than revisited: this is the "Note for Stage D" Stage C left. With colour stripped rain has neither channel, so neither rung helps it — but lumRange 0 makes it *worse* (solid) rather than merely flat, so the exemption holds for the same measured reason it holds on light. fresco#82 remains the real fix.

Update the function's doc comment so the `theme.Mono` sentence it already mentions is now a condition it actually reads, not a forward reference, and so the rain paragraph names both rungs rather than only the light one.

- [x] **Step 6: Run the tests to verify they pass**

Run: `go test ./ui/ ./session/tmux/ -count=1`
Expected: PASS, including both negative controls.

- [x] **Step 7: Drive it live**

```bash
just build
export TMUX_TMPDIR=/tmp/atr-nc HOME=/tmp/atr-nc-home
mkdir -p "$TMUX_TMPDIR" "$HOME"
NO_COLOR=yes ./bin/atrium
```

`NO_COLOR=yes` specifically — that is the value the dependency ignores, so it is the one that proves Atrium's own check is live. Create a session, attach, and confirm: the TUI is monochrome with bold still visible; the in-pane header band carries no tint. Then repeat with `NO_COLOR=1` and confirm no difference. Then run with `NO_COLOR=` (empty) and confirm colour returns.

- [x] **Step 8: Verify goldens and gate**

Run: `go test ./app/ -run 'TestFrameParity|TestFrameColourFingerprint|TestLightFrameColourFingerprint|TestNoColorFrame' -count=1`
Expected: PASS unchanged — none of these render the tmux bar or set Mono.

Run: `PATH=$PATH:$HOME/go/bin just ci && go test -race -shuffle=on ./...`

`-shuffle=on` is important: `SetMono` is a new package global, and a leaked `restore()` would make later tests monochrome. If a `session/tmux` or `ui` test fails only under shuffle, look for a missing `defer`.

- [x] **Step 9: Commit**

```bash
git add ui/contextbar.go ui/contextbar_test.go session/tmux/config.go \
        session/tmux/atrium.conf.tmpl session/tmux/config_test.go ui/splash.go ui/splash_test.go
git commit -m "feat(nocolor): decolourise the tmux bar and the splash

Bubble Tea's colour profile covers everything Lip Gloss renders, but tmux
draws its own status line — so #[fg=...] markup and status-style are the one
surface NO_COLOR would otherwise be violated on, and it is where a user
spends most of their time. #[bold] stays: weight replaces colour as the
hierarchy.

The splash takes the same rung a light palette does, for the mirror-image
reason — with colour gone, a colour-borne brightness channel carries
nothing.

Each change ships with a negative control, so a version that emitted no
colour at all could not pass by accident.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
```

---

## Stage E — detection, `theme: auto`, live re-theme ✅ SHIPPED (#573, `c0c9810`)

**What the steps below got wrong, corrected in place but summarised here because
Stage F inherits the same files.** Four code blocks would not have compiled or would
have undone shipped work: Task 7 Step 4's var block reverts Stage D's
`atomic.Pointer`; `Scheme` has to be `int32`, not `int`, or `gosec` G115 fails the
gate on every `Swap`; Task 8's `drainCmd` duplicates `runCmdTree`, which already
existed; and `applyBarStyleCmd` takes a `key string`, so Task 8's argument-less call
was not the signature.

Two things the plan did not have at all. **Detection has a FOURTH query point** — the
settings panel selecting `auto`, which is the one site where the gate that suppressed
every earlier query is itself what changed. And **an unparseable OSC 11 reply reports
"dark" with confidence** (`isDarkColor(nil)` is `true`), so it has to be fed to the
ladder as a non-reply rather than trusted.

The live matrix Stage F is gated on is recorded on issue #394, comment 5156554261.
**It is thin**: no real light terminal was available, and `COLORFGBG` was unset on both
terminals tested, so every OSC 11 case was injected into the pty rather than witnessed.

Mode 2031 is deliberately **not** used. It is decodable in the pinned stack (`x/ansi@v0.11.7` has `SetLightDarkMode`; ultraviolet decodes `CSI ? 997 ; N n` at `decoder.go:432`; `translateInputEvent` ends in `return e` so the events reach `Update`; `tea.RawMsg` can emit the DECSET) — but it is a *persistent mode* and Bubble Tea unwinds only the modes it owns declaratively. `restoreTerminalState` (`tty.go:33`) restores termios and nothing else, so an unmatched `\x1b[?2031h` leaks past quit, past the signal path (`app.Run` returns on `ctx.Done()` without dispatching `Update`), and past every `tea.Exec` attach — where the terminal keeps emitting `CSI?997;Nn` into a stream tmux owns, **injecting stray bytes into the agent pane**. OSC 11 is a stateless query with nothing to unwind. Filed as a follow-up paired with #396.

### Task 7: The scheme axis and the `auto` resolution (inert)

No bubbletea, no I/O. `ui/theme` stays a leaf package.

**Files:**
- Modify: `ui/theme/scheme.go` (created in Task 3 Step 2b)
- Create: `ui/theme/scheme_test.go`
- Modify: `ui/theme/current.go`
- Modify: `ui/theme/registry.go` — add `AutoThemeName`, `SelectableNames()`
- Modify: `config/accessors.go` — add `GetTheme()`
- Modify: `config/types.go` — `Theme`'s doc comment
- Modify: `ui/overlay/settings_schema.go` — the `theme` row
- Modify: `ui/overlay/settings_test.go` — `TestSettingsOverlay_CycleThemeWraps`
- Modify: `README.md`

**Interfaces:**
- Produces:
  - `const theme.AutoThemeName = "auto"`
  - `type theme.Scheme int` with `theme.SchemeUnknown`, `theme.SchemeDark`, `theme.SchemeLight`
  - `func theme.ResolveScheme(bgIsDark *bool, colorfgbg string) Scheme` — the pure ladder
  - `func theme.SetScheme(s Scheme) (restore func())`
  - `func theme.CurrentScheme() Scheme`
  - `func theme.SelectableNames() []string` — `auto` first, then the registry sorted
  - `func (c *config.Config) GetTheme() string`

- [x] **Step 1: Write the failing tests**

Create `ui/theme/scheme_test.go`:

```go
package theme

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func boolp(b bool) *bool { return &b }

// ResolveScheme is the detection ladder as a pure function, so every rung and
// every failure is testable with no terminal involved. The rungs, highest first:
// an OSC 11 answer, then COLORFGBG, then unknown.
//
// COLORFGBG sits BELOW OSC 11 and can never correct it: the variable is
// stale-prone — it survives into child processes after the terminal's theme
// changes — so it is a hint used only in the absence of an answer, never a
// correction to one.
func TestResolveScheme(t *testing.T) {
	for _, tc := range []struct {
		name      string
		bgIsDark  *bool
		colorfgbg string
		want      Scheme
	}{
		{"osc11 says dark", boolp(true), "", SchemeDark},
		{"osc11 says light", boolp(false), "", SchemeLight},
		{"osc11 outranks a disagreeing COLORFGBG", boolp(true), "0;15", SchemeDark},
		{"osc11 outranks an agreeing COLORFGBG", boolp(false), "0;15", SchemeLight},
		{"COLORFGBG light background", nil, "0;15", SchemeLight},
		{"COLORFGBG dark background", nil, "15;0", SchemeDark},
		{"COLORFGBG default background is no answer", nil, "15;default", SchemeUnknown},
		{"COLORFGBG malformed is no answer", nil, "nonsense", SchemeUnknown},
		{"COLORFGBG three fields uses the last", nil, "0;7;15", SchemeLight},
		{"nothing at all", nil, "", SchemeUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, ResolveScheme(tc.bgIsDark, tc.colorfgbg))
		})
	}
}

// AC#5, as a statement about the code path: `auto` with NO detection resolves to
// exactly the default theme. This is the palette-level half; the frame-level half
// is that app/testdata/colours.txt stays byte-identical, asserted in app.
func TestAutoWithoutDetectionIsTheDefaultTheme(t *testing.T) {
	defer SetScheme(SchemeUnknown)()
	defer Set(AutoThemeName)()

	require.Equal(t, Get(DefaultThemeName).Palette, Current().Palette,
		"auto with no detection must be byte-for-byte the shipped default")
}

// A detected light terminal under `auto` selects the default family's light twin.
func TestAutoWithLightSchemeSelectsTheTwin(t *testing.T) {
	defer SetScheme(SchemeLight)()
	defer Set(AutoThemeName)()

	require.Equal(t, Get(lightTwin[DefaultThemeName]).Palette, Current().Palette)
	require.True(t, IsLight(Current().Palette))
}

// AC#4, structurally rather than by convention: only `auto` reads the scheme
// axis, so an explicitly named theme cannot be switched by detection. Asserted
// for EVERY registered name, not a sample — a theme added later is covered.
func TestNamedThemesNeverFollowTheScheme(t *testing.T) {
	for _, name := range Names() {
		t.Run(name, func(t *testing.T) {
			restoreName := Set(name)
			defer restoreName()

			restoreDark := SetScheme(SchemeDark)
			dark := Current().Palette
			restoreDark()

			restoreLight := SetScheme(SchemeLight)
			light := Current().Palette
			restoreLight()

			require.Equal(t, dark, light,
				"an explicitly named theme must render identically under either scheme")
		})
	}
}

// SetScheme must restore, like Set and SetGlyphSet — and it must restore the
// scheme WITHOUT clobbering the palette name, since the two axes are independent.
func TestSetSchemeRestoresWithoutTouchingTheName(t *testing.T) {
	defer Set("catppuccin-mocha")()

	restore := SetScheme(SchemeLight)
	require.Equal(t, SchemeLight, CurrentScheme())
	require.Equal(t, "catppuccin-mocha", Current().Name)
	restore()
	require.Equal(t, SchemeUnknown, CurrentScheme())
	require.Equal(t, "catppuccin-mocha", Current().Name)
}

// SelectableNames is what the settings picker offers: `auto` plus every registered
// theme. It lives here rather than in the overlay so theme vocabulary has one
// home — and it is guarded in both directions so the list cannot drift from the
// registry.
func TestSelectableNames(t *testing.T) {
	got := SelectableNames()
	require.Equal(t, AutoThemeName, got[0], "auto leads: it is the recommended value")
	require.Len(t, got, len(Names())+1)

	for _, n := range Names() {
		require.Contains(t, got, n)
	}
	for _, n := range got {
		if n == AutoThemeName {
			continue
		}
		require.NotNil(t, Get(n))
		require.Equalf(t, n, Get(n).Name,
			"%q is offered by the picker but Get falls back for it — a dead option", n)
	}
}
```

- [x] **Step 2: Run to verify it fails**

Run: `go test ./ui/theme/ -run 'TestResolveScheme|TestAuto|TestNamedThemes|TestSetScheme|TestSelectableNames' -v`
Expected: FAIL to compile — `undefined: ResolveScheme`, `SchemeDark`, `AutoThemeName`, `SelectableNames`, `CurrentScheme`.

- [x] **Step 3: Implement the scheme axis**

Append to `ui/theme/scheme.go`:

```go
// Scheme is the detected polarity of the terminal's background.
//
// int32, NOT int: it is stored in an atomic.Int32 (curScheme), and a narrowing
// conversion on every Set is a gosec G115 that fails `just lint` — which is the
// only check that sees it. Matching the type to its storage removes the conversion
// instead of silencing the linter.
type Scheme int32

const (
	// SchemeUnknown means detection has produced no answer — either it has not run
	// yet, or nothing answered. It is the zero value on purpose: absence of
	// evidence resolves to the shipped dark default, which is what makes
	// introducing detection a no-op for anyone it cannot reach.
	SchemeUnknown Scheme = iota
	// SchemeDark means the terminal reported a dark background.
	SchemeDark
	// SchemeLight means the terminal reported a light background.
	SchemeLight
)

// ResolveScheme runs the detection ladder over its inputs and returns the
// polarity, or SchemeUnknown when nothing answered.
//
// bgIsDark is an OSC 11 answer (nil when the terminal did not reply);
// colorfgbg is the raw COLORFGBG environment value ("" when unset).
//
// Two properties are load-bearing:
//
//   - It LATCHES at the caller. "No answer" is SchemeUnknown, never a flip to a
//     default — a terminal that stays quiet must leave the current scheme alone
//     rather than be treated as having reported dark. This function reports the
//     absence; the caller is responsible for not acting on it.
//
//   - COLORFGBG can never correct an OSC 11 answer. The variable is inherited by
//     child processes and is not updated when the terminal's theme changes, so it
//     is routinely stale. It is a hint for terminals that do not answer OSC 11,
//     and nothing more.
func ResolveScheme(bgIsDark *bool, colorfgbg string) Scheme {
	if bgIsDark != nil {
		if *bgIsDark {
			return SchemeDark
		}
		return SchemeLight
	}
	return schemeFromColorFGBG(colorfgbg)
}

// schemeFromColorFGBG reads the background half of COLORFGBG, which rxvt and
// friends set as "fg;bg" (sometimes "fg;bold;bg"). The last field is the
// background, as an ANSI palette index 0-15; "default" means the terminal
// declined to say, which is no answer rather than a dark one.
//
// Indices 0-6 and 8 are the dark half of the 16-colour palette, 7 and 9-15 the
// light half. That is the same split every other consumer of this variable uses;
// it is crude, which is exactly why this rung sits below OSC 11.
func schemeFromColorFGBG(v string) Scheme {
	if v == "" {
		return SchemeUnknown
	}
	fields := strings.Split(v, ";")
	bg := strings.TrimSpace(fields[len(fields)-1])
	n, err := strconv.Atoi(bg)
	if err != nil || n < 0 || n > 15 {
		return SchemeUnknown
	}
	if n == 7 || n >= 9 {
		return SchemeLight
	}
	return SchemeDark
}
```

Add `"strconv"` and `"strings"` to the file's imports.

- [x] **Step 4: Add the third axis to `ui/theme/current.go`**

**The block below is written against a pre-Stage-D `current.go` and would REVERT
shipped work — read the file before editing it.** `current` is an
`atomic.Pointer[Theme]` initialised in an `init()`, not a plain var: every write is
`current.Store(compose())`, never `current = compose()`. The "no locking is needed"
sentence is likewise no longer true of the file this would land in.

`curScheme` shipped as an `atomic.Int32`, not the plain var below. Nothing reads it
off the update loop — `barStyleColours` reaches `Current()` and `Mono()` and never
this — so the `curName`/`curGlyphSet` exemption was available and was DECLINED:
`CurrentScheme()` is exported directly beside `Current()`, which promises any
goroutine, and two neighbouring getters with opposite concurrency contracts is a
footgun one word closes. `mono` went through this exact transition in Stage D review.

```go
var (
	curName     = DefaultThemeName
	curGlyphSet = GlyphSetPlain // safe default: plain glyphs, never tofu on a bare terminal
	curScheme   atomic.Int32    // a Scheme; the zero value is SchemeUnknown, on purpose
	current     atomic.Pointer[Theme]
)

// compose builds the active theme from the current palette + glyph-set + scheme
// selection. It copies the registry entry so it never mutates the shared palette
// theme.
//
// AutoThemeName is resolved here rather than being a registry entry, because
// Get must return a concrete eighteen-token palette and `auto` has none — an
// `auto` entry would have to hold a fiction, which the canonical-hex and contrast
// oracles would then dutifully validate. Resolving it here is also what makes
// AC#4 structural: this is the only place curScheme is read, so a named theme
// cannot follow the terminal.
func compose() *Theme {
	name := curName
	if name == AutoThemeName {
		name = DefaultThemeName
		if Scheme(curScheme.Load()) == SchemeLight {
			if twin, ok := lightTwin[name]; ok {
				name = twin
			}
		}
	}
	t := *Get(name)
	t.Glyphs = glyphsFor(curGlyphSet)
	return &t
}

// SetScheme records the detected terminal polarity and recomposes, preserving the
// palette and glyph-set selections, and returns a function that restores the
// previous scheme. It has no effect on the rendered theme unless the palette
// selection is AutoThemeName.
func SetScheme(s Scheme) (restore func()) {
	prev := curScheme.Swap(int32(s))
	current.Store(compose())
	return func() { curScheme.Store(prev); current.Store(compose()) }
}

// CurrentScheme reports the scheme most recently recorded by SetScheme. Safe to
// call from any goroutine, like Current(); see the note on curScheme above.
func CurrentScheme() Scheme { return Scheme(curScheme.Load()) }
```

`Set` and `SetGlyphSet` already snapshot and restore both of their own axes; leave them alone — a scheme change is not theirs to undo, and having three functions each restore three axes is how a restore starts clobbering a sibling.

- [x] **Step 5: Add `AutoThemeName` and `SelectableNames` to `ui/theme/registry.go`**

```go
// AutoThemeName is the reserved theme value that follows the terminal's detected
// background polarity, selecting the default family's dark palette or its light
// twin. It is deliberately NOT a registry entry: Get must return a concrete
// palette, and `auto` has none. See compose() in current.go.
const AutoThemeName = "auto"

// SelectableNames returns what a user may set `theme` to: AutoThemeName first
// (it is the recommended value), then every registered theme, sorted.
//
// It lives here rather than in the settings overlay so theme vocabulary has one
// home. Names() deliberately still returns only the registry, because every
// existing caller that iterates it — the splash's canonical-hex check, the glyph
// width sweep, the contrast oracle — wants real palettes, and would test `auto`
// vacuously.
func SelectableNames() []string {
	names := Names()
	sort.Strings(names)
	return append([]string{AutoThemeName}, names...)
}
```

Add `"sort"` to `ui/theme/registry.go`'s imports.

- [x] **Step 6: Run to verify the theme tests pass**

Run: `go test ./ui/theme/ -count=1 -v`
Expected: PASS, all of them.

- [x] **Step 7: Add `config.GetTheme()`**

In `config/accessors.go`, following the `GetGlyphSet` pattern at `config/accessors.go:236`:

```go
// GetTheme returns the configured theme name, normalizing the empty value to the
// shipped default. Every consumer must go through this rather than reading
// c.Theme, so "unset" has one meaning: the theme layer resolves AutoThemeName
// specially, and an empty string would silently miss that branch and render the
// dark default on a light terminal.
func (c *Config) GetTheme() string {
	if c == nil || strings.TrimSpace(c.Theme) == "" {
		return DefaultConfig().Theme
	}
	return c.Theme
}
```

**`DefaultConfig()` must not be called here** — `config/accessors.go` is reached from `ui/overlay/settings_schema.go`, whose row builder must stay pure (no exec, no filesystem), and `DefaultConfig()` resolves the OS user to derive `branch_prefix`. Use a package constant instead:

```go
// DefaultTheme is the theme a config with no `theme` set resolves to. A constant
// rather than a read of DefaultConfig(), because the settings schema's row
// builder must stay pure and DefaultConfig resolves the OS user.
const DefaultTheme = "tokyo-night"
```

and have both `GetTheme()` and `DefaultConfig()` (`config/config.go:92`) use it. That also makes Stage F a one-line change to one constant.

Then replace `theme.Set(m.appConfig.Theme)` at `app/app_layout.go:269` with `theme.Set(m.appConfig.GetTheme())`, and check for other raw reads: `grep -rn "appConfig.Theme\|cfg.Theme\|\.Theme\b" --include='*.go' . | grep -v _test | grep -v ui/theme`.

- [x] **Step 8: Wire the settings row**

In `ui/overlay/settings_schema.go`, the `theme` row (`ui/overlay/settings_schema.go:510-530`):

```go
			defaultDisplay: func() string { return config.DefaultTheme },
			summary:        "Colour palette and border style. `auto` follows the terminal's background.",
			get: func(c *config.Config) string { return c.GetTheme() },
			set: func(c *config.Config, v string) error {
				c.Theme = v
				return nil
			},
			options: func(c *config.Config) []string { return theme.SelectableNames() },
```

- [x] **Step 9: Fix the cycle test, which this breaks**

`TestSettingsOverlay_CycleThemeWraps` (`ui/overlay/settings_test.go:253`) builds `names := theme.Names()` and asserts a full cycle of `len(names)` returns to the start. The picker now offers `len(Names())+1` options, so **the cycle will no longer close and this test will fail**. That is the drift guard working; update it:

```go
	names := theme.SelectableNames()
```

and delete the now-redundant `sort.Strings(names)` (`SelectableNames` returns them ordered). Remove `"sort"` from the file's imports if nothing else there uses it — `unused` will fail CI otherwise.

- [x] **Step 10: Document the `auto` value**

`config/types.go:247-251` — extend the `Theme` doc comment to name `auto` and say that a named theme never auto-switches.

`README.md`'s `theme` row — add `auto` to the description. `TestReadmeDocumentsEveryConfigField` only checks presence, so this is unguarded; `git grep -n 'theme' README.md` afterwards.

- [x] **Step 11: Verify the goldens are still immovable and gate**

Run: `go test ./app/ -run 'TestFrameParity|TestFrameColourFingerprint|TestLightFrameColourFingerprint' -count=1`
Expected: PASS unchanged. `curScheme` starts `SchemeUnknown` and the default theme is still `tokyo-night`, so nothing renders differently.

Run: `PATH=$PATH:$HOME/go/bin just ci && go test -race -shuffle=on ./...`

- [x] **Step 12: Mutation-verify the AC#4 and AC#5 guards**

1. In `compose()`, delete the `if name == AutoThemeName` guard so *every* theme follows the scheme. Expected: `TestNamedThemesNeverFollowTheScheme` **fails** for `tokyo-night` and `catppuccin-mocha`. Revert.
2. In `compose()`, change the `Scheme(curScheme.Load()) == SchemeLight` test to `!=`. Expected: `TestAutoWithoutDetectionIsTheDefaultTheme` **fails** (unknown now picks the twin) — the AC#5 guard catching an inverted default. Revert.
3. Give `curScheme` a non-zero starting value — `curScheme.Store(int32(SchemeLight))` in an `init()`, since an `atomic.Int32` has no initializer to edit. Expected: `TestAutoWithoutDetectionIsTheDefaultTheme` still passes (it sets the scheme itself) but `TestSetSchemeRestoresWithoutTouchingTheName` **fails** on the restored value. If neither fails, the zero-value guarantee is unguarded — add `require.Equal(t, SchemeUnknown, CurrentScheme())` as the first line of a fresh test that runs before any `SetScheme`. Revert.

- [x] **Step 13: Commit**

```bash
git add ui/theme/scheme.go ui/theme/scheme_test.go ui/theme/current.go ui/theme/registry.go \
        config/accessors.go config/config.go config/types.go \
        ui/overlay/settings_schema.go ui/overlay/settings_test.go app/app_layout.go README.md
git commit -m "feat(theme): add the scheme axis and the reserved auto theme

Scheme is a third orthogonal axis beside the palette name and the glyph set,
read in exactly one place — compose() — and only when the palette selection
is the reserved value \`auto\`. That is what makes AC#4 structural rather
than conventional: a named theme cannot follow the terminal, for every
registered name, including ones added later.

auto is not a registry entry. Get must return a concrete eighteen-token
palette and auto has none, so an entry would hold a fiction that the
canonical-hex and contrast oracles would then validate.

Inert: no detection yet, curScheme starts unknown, and every golden is
unchanged.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
```

### Task 8: The Bubble Tea wiring

**Files:**
- Create: `app/scheme.go`
- Create: `app/scheme_test.go`
- Modify: `app/app.go` — `Init()` (`app/app.go:639`)
- Modify: `app/app_update.go` — `tea.FocusMsg` (`app/app_update.go:386`), plus a new `tea.BackgroundColorMsg` case
- Modify: `app/app_layout.go` — `repaintAfterAttach` (`app/app_layout.go:429`)

**Interfaces:**
- Consumes: `tea.RequestBackgroundColor` (a `func() Msg`, so it **is** a `tea.Cmd` — pass it unparenthesised, like `tea.RequestWindowSize`), `tea.BackgroundColorMsg` with `IsDark() bool` (`color.go:75`), `theme.ResolveScheme`, `theme.SetScheme`, `theme.CurrentScheme`, `theme.AutoThemeName`, and the bar push from Task 1 — which by the time this runs is `m.barStylePushCmd()`, the keyless half Stage E split out of `applyBarStyleCmd(key)`.
- Produces: `func (m *home) requestSchemeCmd() tea.Cmd`, `func (m *home) applyDetectedScheme(s theme.Scheme) tea.Cmd`.

- [x] **Step 1: Write the failing tests**

Create `app/scheme_test.go`:

```go
package app

import (
	"testing"

	"github.com/ZviBaratz/atrium/ui/theme"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/require"
)

// A terminal that answers "light" while `theme: auto` is configured must re-theme
// and push the tmux bar. The bar push is the half nothing else would notice: it
// lives in another process, and Stage A's own tests only cover the settings-panel
// route into it.
func TestBackgroundColorMsgLightRethemesAndPushesTheBar(t *testing.T) {
	m := newCreateFormHome(t)
	m.appConfig.Theme = theme.AutoThemeName
	m.appConfig.SessionContextBar = boolPtr(true)
	t.Cleanup(theme.Set(theme.AutoThemeName))
	t.Cleanup(theme.SetScheme(theme.SchemeUnknown))

	var pushed int32
	defer swapBarStyleApplier(func(context.Context, bool) { atomic.AddInt32(&pushed, 1) })()

	_, cmd := m.Update(tea.BackgroundColorMsg{Color: lightBackground()})
	require.NotNil(t, cmd, "a scheme change must command a repaint and a bar push")
	runCmdTree(cmd)

	require.Equal(t, theme.SchemeLight, theme.CurrentScheme())
	require.True(t, theme.IsLight(theme.Current().Palette),
		"auto plus a light terminal must render the light palette")
	require.Equal(t, int32(1), atomic.LoadInt32(&pushed), "the in-pane bar must follow the flip")
}

// An unchanged answer must be a no-op. Without this, every refocus would clear
// the screen and re-push the bar for the whole fleet — a subprocess per focus
// event, which is the #380 defect class exactly.
func TestBackgroundColorMsgUnchangedIsANoOp(t *testing.T) {
	m := newCreateFormHome(t)
	m.appConfig.Theme = theme.AutoThemeName
	t.Cleanup(theme.Set(theme.AutoThemeName))
	t.Cleanup(theme.SetScheme(theme.SchemeDark))

	var pushed int32
	defer swapBarStyleApplier(func(context.Context, bool) { atomic.AddInt32(&pushed, 1) })()

	_, cmd := m.Update(tea.BackgroundColorMsg{Color: darkBackground()})
	require.Nil(t, cmd, "re-reporting the same scheme must command nothing")
	require.Equal(t, int32(0), atomic.LoadInt32(&pushed))
}

// AC#4 through the live path: a named theme ignores a detected flip entirely.
// theme.TestNamedThemesNeverFollowTheScheme proves compose() ignores the axis;
// this proves the app does not reach past it and call Set itself.
func TestBackgroundColorMsgIgnoredForANamedTheme(t *testing.T) {
	m := newCreateFormHome(t)
	m.appConfig.Theme = "catppuccin-mocha"
	t.Cleanup(theme.Set("catppuccin-mocha"))
	t.Cleanup(theme.SetScheme(theme.SchemeUnknown))

	var pushed int32
	defer swapBarStyleApplier(func(context.Context, bool) { atomic.AddInt32(&pushed, 1) })()

	_, cmd := m.Update(tea.BackgroundColorMsg{Color: lightBackground()})
	require.Nil(t, cmd)
	require.Equal(t, "catppuccin-mocha", theme.Current().Name)
	require.Equal(t, int32(0), atomic.LoadInt32(&pushed))
}

// Refocus re-queries. This is the whole of AC#2 on terminals without mode 2031,
// which is all of them as far as Atrium is concerned (see the Stage E preamble).
func TestFocusMsgRequeriesTheBackgroundColour(t *testing.T) {
	m := newCreateFormHome(t)
	m.appConfig.Theme = theme.AutoThemeName

	_, cmd := m.Update(tea.FocusMsg{})
	require.NotNil(t, cmd, "refocus must re-query: a flip while blurred is otherwise invisible")
	require.True(t, m.focused, "the notification gate must still see the focus")
}

// A named theme must not spend a query it cannot act on.
func TestFocusMsgDoesNotQueryForANamedTheme(t *testing.T) {
	m := newCreateFormHome(t)
	m.appConfig.Theme = "tokyo-night"

	_, cmd := m.Update(tea.FocusMsg{})
	require.Nil(t, cmd)
	require.True(t, m.focused)
}
```

`lightBackground()` and `darkBackground()` are the only helpers this file needs; write them at the bottom of `app/scheme_test.go`:

```go
// lightBackground and darkBackground are colours whose IsDark() answers are
// unambiguous. tea.BackgroundColorMsg embeds color.Color and derives IsDark from
// it, so the test drives the real predicate rather than a bool.
func lightBackground() color.Color { return color.RGBA{0xe1, 0xe2, 0xe7, 0xff} }
func darkBackground() color.Color  { return color.RGBA{0x1a, 0x1b, 0x26, 0xff} }
```

**Do not write a `drainCmd` — `runCmdTree` already exists** (`app/app_config_update_test.go:59`, added by Stage A) and already recurses structurally through both `tea.Batch` and `tea.Sequence`, matching on "a slice of `tea.Cmd`" because `sequenceMsg` is unexported. A second walker would be a duplicate of a helper the same package already proves against `tea.Sequence(tea.ClearScreen, tea.Batch(...))`. The blocks above call `runCmdTree(cmd)` for that reason — an earlier draft of this step defined and called a `drainCmd`, and a one-level `_ = cmd()` would not have reached the bar push under `tea.Sequence`.

The blocks above are also written against the tree's signatures rather than this plan's first draft: `swapBarStyleApplier` takes `func(context.Context, bool)`, not `func()`, so the counters are `int32` incremented with `atomic.AddInt32`; and the bar push inside `applyDetectedScheme` is `barStylePushCmd()`, since `applyBarStyleCmd` takes a `key string` this path has none of.

`tea.BackgroundColorMsg`'s field **is** embedded (`struct{ color.Color }`, `color.go:67`, `image/color`), so the embedded field is named `Color` and `tea.BackgroundColorMsg{Color: c}` compiles. Verified on v2.0.8.

- [x] **Step 2: Run to verify it fails**

Run: `go test ./app/ -run 'TestBackgroundColorMsg|TestFocusMsg' -v`
Expected: FAIL — no `BackgroundColorMsg` case exists, so `Update` returns nil and the theme never moves.

- [x] **Step 3: Implement `app/scheme.go`**

```go
package app

import (
	"os"

	"github.com/ZviBaratz/atrium/ui/theme"

	tea "charm.land/bubbletea/v2"
)

// scheme.go is the terminal-polarity detection wiring: the queries Atrium sends,
// what it does with an answer, and where it re-asks.
//
// Mode 2031 (the terminal PUSHING a scheme change) is deliberately not used, even
// though it is decodable in this stack: x/ansi carries SetLightDarkMode,
// ultraviolet decodes CSI ? 997 ; N n into Dark/LightColorSchemeEvent, and
// translateInputEvent passes unrecognised ultraviolet events through untouched, so
// a case for them would compile today.
//
// It is a PERSISTENT MODE, and that is the problem. Bubble Tea unwinds only the
// modes it owns declaratively through tea.View; restoreTerminalState restores
// termios and nothing else. So an unmatched ESC[?2031h outlives Atrium three
// ways: past quit, past the signal path (Run returns on ctx.Done() without
// dispatching Update, so no Cmd-based reset can run), and past every tea.Exec
// attach — where the terminal keeps emitting CSI?997;Nn into an input stream tmux
// now owns, injecting stray bytes into the agent's pane. Owning that lifecycle is
// real work, and #396 has to build it anyway for the kitty keyboard protocol.
//
// OSC 11 is a query: stateless, nothing to unwind, answered by nearly everything.
// So Atrium asks — at startup, on refocus, after a detach, and when the settings
// panel selects `auto` — rather than being told. The first three re-ask on behalf
// of a selection that was ALREADY `auto`; the fourth is the one where it just
// became so, and it was missing from this plan entirely.

// requestSchemeCmd asks the terminal for its background colour, or nil when the
// answer could not be acted on.
//
// Gating on the configured theme rather than querying unconditionally is what
// keeps a named theme from spending a query per focus event that it would then
// discard. The gate reads config, not theme.Current(): `auto` resolved to a dark
// palette is still `auto`, and Current() cannot tell that from a user who named
// tokyo-night.
//
// RequestBackgroundColor is a func() Msg, so it IS a Cmd — passed unparenthesised,
// like tea.RequestWindowSize.
func (m *home) requestSchemeCmd() tea.Cmd {
	if m.appConfig.GetTheme() != theme.AutoThemeName {
		return nil
	}
	return tea.RequestBackgroundColor
}

// applyDetectedScheme records a detected polarity and, if it changed anything,
// re-themes: a hard repaint plus the tmux bar push, exactly as the settings
// panel's theme arm does.
//
// It returns nil for an unchanged scheme, and that is load-bearing rather than an
// optimization. Atrium re-queries on every refocus, so without the comparison
// each focus event would clear the screen and spawn a subprocess for the whole
// fleet — a subprocess count that grows with a behaviour the user cannot see,
// which is the #380 defect class.
//
// SchemeUnknown is dropped rather than applied. The ladder reports "nothing
// answered" as unknown, and treating that as a flip to dark would mean a terminal
// that went quiet for one query undid a correct detection. Detection latches:
// only a real answer moves it.
func (m *home) applyDetectedScheme(s theme.Scheme) tea.Cmd {
	if s == theme.SchemeUnknown {
		return nil
	}
	if m.appConfig.GetTheme() != theme.AutoThemeName {
		return nil
	}
	if s == theme.CurrentScheme() {
		return nil
	}
	theme.SetScheme(s)
	return tea.Sequence(
		tea.ClearScreen,
		tea.Batch(tea.RequestWindowSize, m.barStylePushCmd()),
	)
}

// initialScheme is the startup ladder's lower rung, read once: COLORFGBG, for
// terminals that will never answer the OSC 11 query Init also sends.
//
// It is deliberately applied BEFORE the query rather than instead of it. An
// answer that arrives later overrides this, because ResolveScheme ranks OSC 11
// above COLORFGBG — the variable is inherited by child processes and is not
// updated when the terminal's theme changes, so it is routinely stale and must
// never correct a live answer.
func initialScheme() theme.Scheme {
	return theme.ResolveScheme(nil, os.Getenv("COLORFGBG"))
}
```

- [x] **Step 4: Wire the query points and the handler (there are FOUR — see below)**

In `app/app.go`'s `Init()` (`app/app.go:642`), add to the `tea.Batch`:

```go
		m.requestSchemeCmd(), // nil unless theme: auto — tea.Batch skips nil cmds
```

and apply the COLORFGBG rung before the program starts, in `newHome` or `assembleHome` — wherever `theme.Set` is already called at startup (`grep -n "theme.Set" app/*.go | grep -v _test`). Put it immediately after, so the ladder's order is visible in one place:

```go
	// The lower detection rung, read once at startup for terminals that never
	// answer OSC 11. Init's query outranks it if one arrives.
	theme.SetScheme(initialScheme())
```

In `app/app_update.go`, add the handler beside the focus cases:

```go
	case tea.BackgroundColorMsg:
		// The terminal answered the OSC 11 query (from Init, a refocus, or a
		// detach). IsDark is Bubble Tea's own luminance test on the reported
		// colour, so Atrium does not second-guess it.
		return m, m.applyDetectedScheme(theme.ResolveScheme(boolPtrOf(msg.IsDark()), ""))
```

with a tiny helper beside it, because `ResolveScheme` takes `*bool` so that "no answer" is expressible:

```go
// boolPtrOf lifts a definite answer into the *bool ResolveScheme takes, whose nil
// means "the terminal did not reply".
func boolPtrOf(b bool) *bool { return &b }
```

`COLORFGBG` is passed as `""` here deliberately — an OSC 11 answer outranks it, so feeding it in would be dead weight at best and, if the ranking were ever inverted, a stale value silently overriding a live one.

And extend the focus case:

```go
	case tea.FocusMsg:
		// The terminal regained focus: while focused, background sessions stay silent
		// (the user is watching the fleet). See maybeNotify.
		m.focused = true
		// Refocus is also when to re-ask the terminal's background colour. Atrium
		// does not enable mode 2031 (see app/scheme.go), so a scheme change while
		// blurred — which is when an OS-level dark/light switch usually happens — is
		// invisible until something asks. Returns nil unless theme: auto.
		return m, m.requestSchemeCmd()
```

In `app/app_layout.go`'s `repaintAfterAttach`, add the query to the batch:

```go
func (m *home) repaintAfterAttach(cmds ...tea.Cmd) tea.Cmd {
	return tea.Sequence(
		tea.ClearScreen,
		tea.Batch(append(cmds,
			func() tea.Msg {
				return tea.WindowSizeMsg{Width: m.windowWidth, Height: m.windowHeight}
			},
			// Detection was blind for the whole attach: tea.Exec suspended the loop
			// and tmux owned the terminal, so neither an OSC 11 reply nor a focus
			// event could reach us. This is the one moment we know that, so re-ask.
			m.requestSchemeCmd(),
		)...),
	)
}
```

**The fourth query point, and the reason it is not a fourth copy of the same call.**
The three above all ask on behalf of a selection that was *already* `auto`. The
settings panel is the one site where the **gate itself** is what changed: the user
picks `auto`, and a session launched on a named palette has spent no query at
`Init` — because `requestSchemeCmd`'s gate read the theme this change is replacing.
So `curScheme` is still whatever `COLORFGBG` said at startup (usually nothing), and
composing `auto` against it renders the shipped dark default. On a light terminal
that is the wrong palette, and nothing corrects it until the user happens to blur
and refocus. The row is `timingLive`, whose `footerNote()` is `""` — the panel
promises the change applies immediately.

In `app/scheme.go`, beside `requestSchemeCmd`:

```go
// applySchemeQueryCmd is requestSchemeCmd for the settings panel's theme arm.
//
// Keyed like applyBarStyleCmd, and for the same reason: the arm is shared with
// glyph_set, which cannot change the palette selection, so it has nothing to
// re-detect. Gating on the key rather than on requestSchemeCmd's config check
// alone is what keeps a rung change from spending a query — that check passes
// under `auto` no matter which row moved.
func (m *home) applySchemeQueryCmd(key string) tea.Cmd {
	if key != "theme" {
		return nil
	}
	return m.requestSchemeCmd()
}
```

and wire it into `applySettingChange`'s theme arm in `app/app_layout.go`, beside the
bar push that is already keyed the same way:

```go
		return tea.Sequence(tea.ClearScreen, tea.Batch(
			tea.RequestWindowSize,
			m.applyBarStyleCmd(key),
			m.applySchemeQueryCmd(key),
		))
```

**Generalise the miss.** The first three points were found by asking where the
*gate is read*. This one is only visible from where the *gated value is written*.
When a feature is gated on a config value, enumerate the **write** sites of that
value, not just the read sites of the gate.

- [x] **Step 5: Run to verify the tests pass**

Run: `go test ./app/ -run 'TestBackgroundColorMsg|TestFocusMsg' -v`
Expected: PASS. If `TestBackgroundColorMsgLightRethemesAndPushesTheBar` shows `pushed == 0`, the Cmd walker is not reaching the push under `tea.Sequence` — that is the trap Step 1 flags, and `runCmdTree` is the fix. Correct it there, not by weakening the assertion.

- [x] **Step 6: Verify AC#5 at the frame level**

Run: `go test ./app/ -run 'TestFrameParity|TestFrameColourFingerprint|TestLightFrameColourFingerprint' -count=1`

Expected: PASS, **byte-identical**. This is AC#5's real proof: the whole detection mechanism is now live, and with no detection input the 18-state colour fingerprint has not moved a byte. `newParityHome` pins the configured theme (still `tokyo-night`), so `auto` is not even exercised — which is why the explicit `SetScheme` pin is inert here, and why it was safe to land it in this stage rather than in Stage F. It is Task 10 Steps 1–2, pulled forward: the pin guards the goldens against the *axis* this stage introduces, not only against the default Stage F flips.

- [x] **Step 7: Drive it live — the matrix**

This is the part no test can do. Build once and run the same binary on a light and a dark terminal:

```bash
just build
export TMUX_TMPDIR=/tmp/atr-detect HOME=/tmp/atr-detect-home
mkdir -p "$TMUX_TMPDIR" "$HOME"
printf '{"theme":"auto"}\n' > "$HOME/.atrium/config.json"   # mkdir -p "$HOME/.atrium" first
./bin/atrium
```

For each terminal available (at minimum one light and one dark; note which):

1. **Startup.** Does the palette match the terminal's polarity? Which rung answered — check `atrium doctor` (Task 9) or the warning log.
2. **Refocus flip.** With Atrium running and focused, switch the OS/terminal theme, then click away and back. The palette should flip on refocus.
3. **Attach → detach.** Attach to a session, switch the terminal theme while attached, detach. The palette should flip on detach, and the in-pane bar's band should match on the next attach.
4. **Named theme.** Set `theme: tokyo-night` and repeat 1–3. Nothing should ever switch.
5. **No reply.** Run under a terminal that does not answer OSC 11 if one is available (`TERM=dumb` is a crude stand-in), and confirm the palette stays dark rather than flickering.

Record which terminals answered OSC 11 and which fell to `COLORFGBG` or to unknown. **Stage F is gated on this list.**

- [x] **Step 8: Gate**

Run: `PATH=$PATH:$HOME/go/bin just ci && go test -race -shuffle=on ./...`

`-race` matters here specifically: `theme.SetScheme` writes a package global from a message handler while `barStyleApplier`'s goroutine reads `theme.Current()`. Rendering is single-threaded on the Bubble Tea loop, but `applyBarStyleCmd`'s closure is not — check that `ApplyBarStyle` reading `theme.Current()` off the update thread does not race the `SetScheme` that scheduled it. If `-race` reports it, capture the two hex strings on the update thread and pass them in as arguments. **That contradicts Task 1's "read the theme at call time" design**, so if it comes to that, update `ApplyBarStyle`'s doc comment and its `TestApplyBarStyle_ReadsTheThemeAtCallTime` test rather than leaving a comment that describes the old shape.

- [x] **Step 9: Commit**

```bash
git add app/scheme.go app/scheme_test.go app/app.go app/app_update.go app/app_layout.go
git commit -m "feat(theme): detect the terminal's background and follow it under auto

OSC 11 at startup, on refocus, and after a detach. Mode 2031 is decodable in
this stack but deliberately unused: it is a persistent mode nothing unwinds,
so an unmatched DECSET outlives Atrium past quit, past the signal path, and
past every attach — where the terminal's CSI?997;Nn reports go into an input
stream tmux owns and land in the agent's pane. Filed with #396, which must
build that lifecycle anyway.

Detection latches: no reply leaves the scheme alone rather than resolving to
a default, and COLORFGBG is a rung below OSC 11 that can never correct it.
An unchanged answer commands nothing, so re-querying on every refocus does
not clear the screen or spawn a subprocess per focus event.

AC#5 holds: with detection live and no input, all eighteen colour
fingerprints are byte-identical.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
```

### Task 9: `atrium doctor` reports the detected scheme

**Files:**
- Modify: `internal/doctor/deps.go` or a new `internal/doctor/scheme.go` — read `internal/doctor/render.go` first to match how sections are rendered
- Modify: `main.go` — the `doctorCmd` body at `main.go:297-345`
- Create/modify: `internal/doctor/scheme_test.go`

**Interfaces:**
- Consumes: `theme.ResolveScheme`, `theme.Scheme`, `os.Getenv("COLORFGBG")`.
- Produces: `func doctor.CheckScheme(environ []string) SchemeResult` and `func doctor.RenderScheme(SchemeResult) string`, matching the `CheckDeps`/`RenderDeps` pair at `internal/doctor/deps.go:90` and `internal/doctor/render.go`.

- [x] **Step 1: Write the failing test**

Create `internal/doctor/scheme_test.go`:

```go
package doctor

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// doctor reports what detection can and cannot see, because a user whose theme
// did not adapt has no other way to find out WHY. The three answers are
// materially different: "your terminal said light", "your terminal did not
// answer, so COLORFGBG decided", and "nothing answered, so it stayed dark".
func TestCheckSchemeNamesTheRungThatAnswered(t *testing.T) {
	t.Run("COLORFGBG answers", func(t *testing.T) {
		got := CheckScheme([]string{"COLORFGBG=0;15"})
		out := RenderScheme(got)
		require.Contains(t, out, "light")
		require.Contains(t, out, "COLORFGBG")
	})

	t.Run("nothing answers", func(t *testing.T) {
		got := CheckScheme([]string{"TERM=xterm-256color"})
		out := RenderScheme(got)
		require.Contains(t, strings.ToLower(out), "dark")
		require.Contains(t, strings.ToLower(out), "no")
	})
}

// doctor runs OUTSIDE the Bubble Tea loop, so it cannot send an OSC 11 query and
// wait for the reply. Saying so is better than implying it probed and found
// nothing — the distinction is the whole value of the section.
func TestRenderSchemeSaysItCannotProbeOSC11(t *testing.T) {
	out := RenderScheme(CheckScheme([]string{"TERM=xterm-256color"}))
	require.Contains(t, out, "OSC 11",
		"doctor must name the rung it cannot test, not silently omit it")
}
```

- [x] **Step 2: Run to verify it fails**

Run: `go test ./internal/doctor/ -run TestCheckScheme -v`
Expected: FAIL to compile — `undefined: CheckScheme`.

- [x] **Step 3: Implement it**

Read `internal/doctor/render.go` and match its section formatting exactly (header style, indentation, the two-space `%-18s` convention used in `oom.go:190`). Then:

```go
package doctor

import (
	"fmt"
	"strings"

	"github.com/ZviBaratz/atrium/ui/theme"
)

// SchemeResult is what doctor can determine about terminal-polarity detection
// without a Bubble Tea loop.
//
// OSC11Probed is always false, and that is the point rather than a stub: doctor
// is a one-shot command, not a TUI, so it cannot send the query and wait for the
// reply. Reporting "not probed here" is honest; omitting the rung would let a
// user read "no answer" as "your terminal does not support it".
type SchemeResult struct {
	Scheme      theme.Scheme // what the rungs doctor CAN read resolve to
	ColorFGBG   string       // the raw value, "" when unset
	OSC11Probed bool         // always false; see the type comment
}

// CheckScheme resolves the detection rungs available outside the TUI.
func CheckScheme(environ []string) SchemeResult {
	var colorfgbg string
	for _, kv := range environ {
		if name, value, ok := strings.Cut(kv, "="); ok && name == "COLORFGBG" {
			colorfgbg = value
		}
	}
	return SchemeResult{
		Scheme:    theme.ResolveScheme(nil, colorfgbg),
		ColorFGBG: colorfgbg,
	}
}

// RenderScheme formats the detection report.
func RenderScheme(r SchemeResult) string {
	var b strings.Builder
	b.WriteString("\nTerminal background detection\n")

	switch r.Scheme {
	case theme.SchemeLight:
		fmt.Fprintf(&b, "  %-18s light (from COLORFGBG=%s)\n", "resolved", r.ColorFGBG)
	case theme.SchemeDark:
		fmt.Fprintf(&b, "  %-18s dark (from COLORFGBG=%s)\n", "resolved", r.ColorFGBG)
	default:
		fmt.Fprintf(&b, "  %-18s dark (no answer from any rung doctor can read)\n", "resolved")
	}

	if r.ColorFGBG == "" {
		fmt.Fprintf(&b, "  %-18s unset\n", "COLORFGBG")
	}
	fmt.Fprintf(&b, "  %-18s not probed here — it needs the running TUI, which queries at\n", "OSC 11")
	fmt.Fprintf(&b, "  %-18s startup, on refocus, and after a detach\n", "")
	fmt.Fprintf(&b, "  %-18s set theme: auto to follow whichever rung answers\n", "")
	return b.String()
}
```

- [x] **Step 4: Wire it into `main.go`**

In `doctorCmd`'s `RunE`, after the capacity section (`main.go:344`):

```go
			fmt.Print(doctor.RenderScheme(doctor.CheckScheme(os.Environ())))
```

- [x] **Step 5: Run and eyeball**

Run: `go test ./internal/doctor/ -count=1` — expected PASS.
Run: `just build && ./bin/atrium doctor` and `COLORFGBG=0;15 ./bin/atrium doctor` — read both. The section must be legible beside the existing ones and must not claim to have probed OSC 11.

- [x] **Step 6: Gate and commit**

```bash
PATH=$PATH:$HOME/go/bin just ci && go test -race -shuffle=on ./...
git add internal/doctor/scheme.go internal/doctor/scheme_test.go main.go
git commit -m "feat(doctor): report terminal background detection

A user whose theme did not adapt has no other way to find out why, and the
three answers are materially different: the terminal said light, the
terminal was silent so COLORFGBG decided, or nothing answered so it stayed
dark. doctor names the rung it CANNOT test too — it has no Bubble Tea loop,
so it cannot send an OSC 11 query and wait, and omitting that would let 'no
answer' read as 'unsupported'.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
```

---

## Stage F — make `auto` the default

**Gated on Task 8 Step 7's live matrix, which exists and is recorded on issue #394
(comment 5156554261).** Read it before starting: every case behaved, but the list is
**thin**. No genuinely light terminal was available, so every OSC 11 case was an
answer injected into the pty rather than one a terminal volunteered; and `COLORFGBG`
was unset on both terminals tested, so that rung has no live witness at all.

**That is not the same as "every terminal on it behaved", which is what this gate
asks for.** Run `theme: auto` on a real light terminal before flipping the default —
that is the acceptance test for the issue's actual title, and it is the one thing the
matrix does not yet cover. If it misbehaves, stop: the fix ships (Stages A–E) and the
default does not.

**Steps 1 and 2 are already done** — they landed in Stage E (`c0c9810`) rather than
here, because the pin guards the goldens against the axis Stage E introduced, not
only against the default Stage F flips. They are ticked below and their reasoning is
kept for the record; start at Step 3.

### Task 10: Flip the shipped default to `auto`

**Files:**
- Modify: `config/accessors.go` — the `DefaultTheme` constant added in Task 7
- Modify: `app/frameparity_test.go` — `newParityHome`
- Modify: `README.md`, `config/types.go`

**Interfaces:**
- Consumes: everything from Stages A–E. Produces nothing new.

- [x] **Step 1: Add the scheme pin to `newParityHome` FIRST** — *done in Stage E, `c0c9810`.*

Before changing the default, make the fixture immune to it. This is what shipped, beside the existing pins in `app/frameparity_test.go` (`theme.Set` and `theme.SetGlyphSet`, now at lines 171-172):

```go
	cfg := config.DefaultConfig()
	t.Cleanup(theme.Set(cfg.GetTheme())) // GetTheme, not cfg.Theme, since Task 7
	t.Cleanup(theme.SetGlyphSet(cfg.GetGlyphSet()))
	// The scheme axis, pinned for exactly the reason the other two are: it is a
	// package global other tests in this package mutate, so under -shuffle the frame
	// would otherwise inherit whichever detection state ran last. Inert while the
	// shipped default names a palette — compose() reads the scheme only for `auto` —
	// which is what makes it safe to land with the axis rather than with the default
	// flip that will need it. Dark is what a terminal that does not answer gets, and
	// what these goldens are baselined at.
	t.Cleanup(theme.SetScheme(theme.SchemeDark))
```

The rationale as drafted here said the pin was needed *because* the shipped default
was `auto`. It landed a stage early, while the default is still `tokyo-night`, so the
reason that survived contact is the `-shuffle` one — the axis is a package global
regardless of what the default names. Step 2 below is the check that made the
difference visible.

- [x] **Step 2: Verify the pin changes nothing yet** — *done in Stage E; all three checksums unmoved.*

Run: `go test ./app/ -run 'TestFrameParity|TestFrameColourFingerprint|TestLightFrameColourFingerprint' -count=1 -shuffle=on`
Expected: PASS byte-identical. The default is still `tokyo-night`, so the pin is inert — which is what makes it safe to land before the flip.

- [ ] **Step 3: Flip the default**

**The two options below have swapped places since this was written.** Task 7 shipped
`DefaultTheme` with a doc comment that commits to the literal *on purpose*: "Spelled
out rather than imported from ui/theme for the same reason GlyphSet* below are:
config's vocabulary is the on-disk one, and ui/theme is deliberately a leaf that no
atrium package appears in." Importing `theme` here would falsify the comment on the
line above the change, and break a convention `ui/theme/registry.go` documents from
the other side. So **spell the literal and pin it with a test** — the "fallback"
below is now the primary path.

In `config/accessors.go`, change only the value and the first sentence:

```go
// DefaultTheme is the theme a config with no `theme` set resolves to: `auto`,
// which follows the terminal's detected background polarity and resolves to the
// shipped dark palette when nothing answers.
// ... (keep the existing second paragraph, which covers both the purity argument
// and the leaf convention)
const DefaultTheme = "auto"
```

**The import edge is legal but unwanted, and both halves of that were re-verified on
`c0c9810`:** `git grep -n "ZviBaratz/atrium" -- 'ui/theme/*.go'` prints nothing, so
`config` → `ui/theme` would not cycle; and `config/*.go` imports no `ui/theme` today,
so Step 3 would be introducing the first such edge rather than following one. Prefer
the literal, and pin the two spellings together so a typo cannot silently ship the
dark default forever:

```go
// TestDefaultThemeMatchesTheReservedAutoName pins config's spelling of the
// reserved value to theme's. config spells it rather than importing the constant,
// to keep ui/theme a leaf — the same trade GlyphSet* makes — so the two strings
// are held together here instead of by the compiler. A typo would not fail to
// build: it would resolve as an unknown palette and silently ship the dark
// default forever, on exactly the terminals this issue is about.
func TestDefaultThemeMatchesTheReservedAutoName(t *testing.T) {
	require.Equal(t, theme.AutoThemeName, config.DefaultTheme)
}
```

Put that test in a package that may import both — `app` is safe.

**This test is not optional under the literal-spelling path.** It is the only thing
standing between a typo and a silent no-op, because `Get()` falls back for any
unrecognised name rather than erroring.

- [ ] **Step 4: Run every golden**

Run: `go test ./app/ -run 'TestFrameParity|TestFrameColourFingerprint|TestLightFrameColourFingerprint' -count=1 -shuffle=on`

Expected: **PASS byte-identical.** This is the self-proof: `auto` with `SchemeDark` resolves to `tokyo-night`, so the shipped default renders exactly what it rendered before. **If a golden moves, do not regenerate it** — it means `compose()`'s `auto` branch does not resolve to the default, and the flip is not safe.

- [ ] **Step 5: Run the whole suite**

Run: `go test ./... -count=1` then `PATH=$PATH:$HOME/go/bin just ci && go test -race -shuffle=on ./...`

Expect failures in tests that assert the default theme *name* — `git grep -n '"tokyo-night"' -- '*_test.go'` and read each. A test asserting the default palette should keep asserting the palette; a test asserting the string `"tokyo-night"` as *the default* now asserts `"auto"`. Fix each on its merits, and do not blanket-replace: `ui/theme`'s own `TestGetFallback` is about `Get`'s behaviour for unknown names and must keep naming `tokyo-night`, because `DefaultThemeName` did not change.

**That grep is not sufficient, and the assertions it misses are the dangerous ones.**
`auto` is the first theme value that is not the name of a palette, so
`cfg.Theme != theme.Current().Name` becomes possible for the first time — and a test
comparing those two *expressions* contains no `"tokyo-night"` literal to grep for.
`app/settings_test.go:90-91` does exactly that (`h.appConfig.Theme` against
`theme.DefaultThemeName`, then against `theme.Current().Name`) and does **not** appear
in the grep above. It happens to survive the flip, because one `Right` from `auto`
lands on a real palette — but it survives by arithmetic on the sorted option list, not
by design, and a reordering of `SelectableNames()` would break it silently.

So run the second sweep too, and read every hit:

```sh
git grep -n 'appConfig\.Theme\|cfg\.Theme' -- '*_test.go'
```

The rule to apply: after the flip, `Theme` is what the user *selected* and
`Current().Name` is what that *resolved to*. They are equal for every named palette
and unequal for `auto`. Any assertion that conflates them needs to say which one it
means.

- [ ] **Step 6: Verify the settings panel shows it**

Run `just build && ./bin/atrium` with a fresh `HOME`, open settings (`s`), and confirm the `theme` row reads `auto` and that the `Modified` dot is **absent** — the value equals the default, so a dot would be a lie. Cycle right once and back and confirm the dot appears and clears.

- [ ] **Step 7: Update the docs**

`README.md`'s `theme` row default column: `` `"tokyo-night"` `` → `` `"auto"` ``, with the description explaining that `auto` follows the terminal and falls back to `tokyo-night`. `config/types.go`'s `Theme` doc comment likewise.

- [ ] **Step 8: Drive it live one more time, both polarities**

Fresh `HOME` (so no `theme` key is set at all), on a light terminal and a dark one. Confirm the right palette appears with no configuration. This is the acceptance test for the issue's actual title.

- [ ] **Step 9: Commit**

```bash
git add config/accessors.go app/frameparity_test.go README.md config/types.go
git commit -m "feat(theme): default to auto

The issue's title is that Atrium is unreadable on light terminals, and an
opt-in fix does not reach the user who does not know the option exists.

Self-proving: auto with no detection resolves to tokyo-night, so all
eighteen colour fingerprints and all thirty-six layout goldens are
byte-identical — the oracle proves the default change is inert on the
terminals that were already fine. newParityHome pins the scheme axis
alongside the palette and glyph set, so the goldens cannot resolve their
palette from another test's detection state under -shuffle.

Gated on the live terminal matrix from Stage E.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
```

- [ ] **Step 10: Close out**

```bash
gh pr checks <n>   # a local gate is not a green CI
```

Then update the issue with the matrix results and close it, ticking #394 in epic #370.

---

## Self-review

**Spec coverage.** Every section of the design doc maps to a task: the tmux bar gap → Task 1; the contrast oracle → Task 2; the light palettes and `colours-light.txt` → Task 3; the splash's luminance ramp → Task 4; `NO_COLOR` at the renderer plus its oracle → Task 5; the tmux/splash surfaces the profile cannot reach → Task 6; the scheme axis, `auto`, and `SelectableNames` → Task 7; the detection wiring and its query points → Task 8 (which enumerated three; a fourth, the settings panel selecting `auto`, was found in review); the doctor report → Task 9; the default flip → Task 10. The design's H2 (the fade) has **no task**, deliberately: it is an eyeball item, and it is covered by Task 3 Step 11's live round and Task 4 Step 9's, both of which render a modal over a light backdrop. The two follow-ups the design defers (fresco's light ramp, `barState`'s 1.44:1) are filed by Task 4 Step 9 and Task 2 Step 6 respectively.

**Type consistency.** `theme.Scheme`/`SchemeUnknown`/`SchemeDark`/`SchemeLight`, `ResolveScheme(*bool, string) Scheme`, `SetScheme(Scheme) func()`, `CurrentScheme() Scheme`, `IsLight(Palette) bool`, `Mono() bool`, `SetMono(bool) func()`, `NoColorRequested([]string) bool`, `AutoThemeName`, `SelectableNames() []string`, `lightTwin map[string]string`, `relLuminanceOf(Color) float64`, `tmux.ApplyBarStyle(context.Context, cmd.Executor) error`, `tmux.RewriteManagedConfig(bool) error`, `config.DefaultTheme`, `(*Config).GetTheme() string`, `(*home).requestSchemeCmd() tea.Cmd`, `(*home).applyDetectedScheme(theme.Scheme) tea.Cmd`, `doctor.CheckScheme([]string) SchemeResult`, `doctor.RenderScheme(SchemeResult) string`, `barStyleApplier` — each is defined in exactly one task and used with that spelling and signature everywhere after.

**One symbol broke that rule, and it is the one to check first when reading an
un-run step.** `applyBarStyleCmd` was written argument-less in Task 1 and shipped
that way; Stage E gave it a `key string` and split the keyless half out as
`barStylePushCmd()` (`app/app_layout.go:452` and `:468`). Task 8's steps were
written against the Task 1 spelling and have been corrected to the tree's, but the
lesson generalises past this plan: **a signature is not a fact a plan can state
once.** The others above held because nothing later re-opened them.

`(*home).applySchemeQueryCmd(key string) tea.Cmd` (`app/scheme.go:84`) is the one
symbol in Stage E that no task defines, because the need for it was found in review
rather than in planning — see Task 8 Step 4's fourth query point.

Two deliberate cross-task couplings, called out where they bite: `relLuminance` is written in `contrast_test.go` (Task 2) and **moved** to `scheme.go` as `relLuminanceOf` in Task 3 Step 2b — the step that creates `scheme.go`, because `IsLight` has to exist before `AgentGlyph` can call it — with the test-file copy deleted rather than duplicated. And `barStyleApplier` is introduced in Task 1 Step 9 as a test seam, then relied on by Task 8's tests — so Task 1's Step 5 code must be restructured to that shape rather than leaving both versions, which Step 9 says explicitly.

**Three places the plan tells the implementer to verify rather than trust.** `tea.BackgroundColorMsg`'s construction (embedded field, so `{Color: c}` may not compile), the Cmd walker's adequacy against `tea.Sequence`/`tea.Batch`, and whether tmux accepts `status-style "bg=,fg="` without a parse error. Each has a named fallback, because guessing any of them wrong produces a green test that guards nothing.

**All three came back, and two came back differently than the fallbacks anticipated.**
`{Color: c}` compiles (the embedded field is named `Color`). `drainCmd` was never
needed — `runCmdTree` already existed and already recursed correctly. And tmux 3.6
*rejects* `bg=,fg=` as `invalid style`, through `source-file` as well, which disables
the entire managed config — so Stage D uses `"default"` rather than the empty string.
Instructing "verify this" worked; the value came from the measurement each time, not
from the fallback the plan guessed.

**A fourth thing was verifiable and nobody thought to ask.** Detection has four query
points, not the three the plan enumerated: the settings panel selecting `auto` is one,
and it is the only site where the gate that suppressed every earlier query is itself
what changed. It was found by review, after the three listed points were all wired and
green. The lesson for a plan of this shape: when a feature is gated on a config value,
enumerate the WRITE sites of that value, not only the read sites of the gate.
