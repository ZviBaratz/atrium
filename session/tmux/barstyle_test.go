package tmux

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	cmd2 "github.com/ZviBaratz/atrium/cmd"
	"github.com/ZviBaratz/atrium/cmd/cmd_test"
	"github.com/ZviBaratz/atrium/internal/testutil"
	"github.com/ZviBaratz/atrium/ui/theme"

	"github.com/stretchr/testify/require"
)

// ApplyBarStyle pushes the ACTIVE theme's bar colours to the live server with a
// constant number of subprocesses, not one per session: status-style is a
// server-global option (-g), so the whole fleet restyles at once. Counting the
// subprocesses is the #380 discipline — a per-session push would be N of them.
func TestApplyBarStyle_ServerGlobalPushCarriesActiveTheme(t *testing.T) {
	defer theme.Set("catppuccin-mocha")()

	ran := recordRuns(t, nil)

	require.NoError(t, ApplyBarStyle(context.Background(), ran.exec))
	require.Len(t, *ran.cmds, 2, "status-style is server-global: a constant push for the whole fleet")

	th := theme.Current()
	for _, sub := range []string{
		"set-option", "-g", "status-style",
		"bg=" + theme.Hex(th.Palette.BarBg),
		"fg=" + theme.Hex(th.Palette.Fg),
	} {
		require.Containsf(t, (*ran.cmds)[0], sub, "style push missing %q: %s", sub, (*ran.cmds)[0])
	}
	require.Contains(t, (*ran.cmds)[1], "refresh-client")
}

// The repaint must be a SEPARATE, ignored command. Batched behind a `;` its failure
// sinks the style push's result, and it fails by design whenever no client is
// attached ("no current client", exit 1) — which is every theme change, since the
// settings overlay is unreachable from inside a session pane.
func TestApplyBarStyle_RepaintFailureDoesNotFailTheStylePush(t *testing.T) {
	ran := recordRuns(t, func(c *exec.Cmd) error {
		if strings.Contains(cmd2.ToString(c), "refresh-client") {
			return errors.New("no current client")
		}
		return nil
	})

	require.NoError(t, ApplyBarStyle(context.Background(), ran.exec),
		"a clientless repaint must not report the whole restyle as failed")
	require.Len(t, *ran.cmds, 2, "the style push must still have been attempted")
	require.NotContains(t, (*ran.cmds)[0], "refresh-client",
		"set-option must not be batched with refresh-client")
}

// A failing set-option is the real error and must still surface.
func TestApplyBarStyle_StylePushFailureSurfaces(t *testing.T) {
	ran := recordRuns(t, func(*exec.Cmd) error { return errors.New("boom") })

	require.Error(t, ApplyBarStyle(context.Background(), ran.exec))
	require.Len(t, *ran.cmds, 1, "a failed style push must not go on to repaint")
}

// tmux_config_override hands the status bar to the user's own config, and
// RewriteManagedConfig honours it by leaving the file alone. A server-global
// set-option would stomp it anyway, so the live half has to honour it too.
func TestApplyBarStyle_NoOpsUnderAConfigOverride(t *testing.T) {
	dir := t.TempDir()
	override := filepath.Join(dir, "mine.conf")
	require.NoError(t, os.WriteFile(override, []byte("# mine\n"), 0o644))
	restoreOverride(t, override)

	ran := recordRuns(t, nil)

	require.NoError(t, ApplyBarStyle(context.Background(), ran.exec))
	require.Empty(t, *ran.cmds,
		"the user's own tmux config owns status-style; Atrium must not push over it")
}

// Init is no longer startup-only: a live theme change calls it from a tea.Cmd
// goroutine while session launches and the 500ms metadata sweep read the same state
// through tmuxConfigPath. Fails under -race if either global loses its atomic.
//
// Init reaches real tmux even though nothing here says so: every call runs
// validateConfig, which starts a probe server and kills it again, so the eight Init
// calls below drive eight new-session/kill-server pairs. Each takes a socket of its
// own (probeSocketName), which is what this test needs and did not used to get: when
// they shared one name, the first teardown killed the server the others were still
// using, and their source-file failed with "no server running" — recorded as a parse
// error, silently disabling the managed conf. Nothing here asserts that (the Init
// errors are discarded on purpose, this being a race test), so the guard for it lives
// in config_probe_test.go. The sandbox TMUX_TMPDIR is what keeps those probe sockets
// out of the shared socket dir, where they used to sit next to the live one (#581).
//
// So it gates on isolation, and only on isolation: RequireTmux would be wrong here,
// because what this test is actually for — that Init and tmuxConfigPath race-freely
// share their globals — holds with or without tmux, and skipping it on a tmux-less
// machine would take that coverage away for an unrelated reason. But the gate has to
// exist. Isolation is the standing promise that a real-tmux site fails loudly rather
// than quietly writing into the developer's fleet, and a site that leaves it to its
// TestMain is a site where a failed sandbox install goes undetected. Conditioned on
// tmux being present for the same reason RequireTmux is not used: with no tmux,
// validateConfig returns before it starts anything (config.go:180) and there is no
// socket to misplace.
func TestInitAndTmuxConfigPath_AreRaceFree(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err == nil {
		testutil.RequireSandboxedTmux(t)
	}
	t.Setenv("HOME", t.TempDir())
	restoreOverride(t, "")

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(2)
		go func() { defer wg.Done(); _ = Init("", true) }()
		go func() { defer wg.Done(); _ = tmuxConfigPath() }()
	}
	wg.Wait()
}

// Under NO_COLOR the live push must carry no colour either. This is the same
// status-style option renderManagedConfig writes, reached by the other route: the
// conf covers servers that start LATER, this one covers the servers running right
// now. Honouring only one leaves the fleet's bars disagreeing after a theme change
// — which is the split this file exists to prevent, and it would reappear under
// NO_COLOR alone.
//
// "bg=default,fg=default" rather than empty values: `set-option -g status-style
// "bg=,fg="` is rejected by tmux 3.6 as `invalid style` (exit 1), and through
// source-file that is what validateConfig would catch — disabling the whole managed
// config over a colour preference. Measured.
func TestApplyBarStyleDropsColourUnderMono(t *testing.T) {
	defer theme.SetMono(true)()

	ran := recordRuns(t, nil)
	require.NoError(t, ApplyBarStyle(context.Background(), ran.exec))
	require.Len(t, *ran.cmds, 2, "the push still happens; only its colours are gone")

	push := (*ran.cmds)[0]
	require.NotContains(t, push, "bg=#", "no hex background under NO_COLOR")
	require.NotContains(t, push, "fg=#", "no hex foreground under NO_COLOR")
	require.Contains(t, push, "bg=default,fg=default",
		"the band must be set to the terminal's own colours, not left to tmux's built-in default")
	require.Contains(t, push, "status-style", "the option is still the one being set")
}

// runRecorder captures the commands an Executor was asked to run.
type runRecorder struct {
	exec cmd2.Executor
	cmds *[]string
}

// recordRuns builds an Executor that records every command; run defaults to success.
func recordRuns(t *testing.T, run func(*exec.Cmd) error) runRecorder {
	t.Helper()
	var ran []string
	return runRecorder{
		cmds: &ran,
		exec: cmd_test.MockCmdExec{
			RunFunc: func(c *exec.Cmd) error {
				ran = append(ran, cmd2.ToString(c))
				if run != nil {
					return run(c)
				}
				return nil
			},
			OutputFunc: func(*exec.Cmd) ([]byte, error) { return nil, nil },
		},
	}
}

// restoreOverride pins the package's override for one test and restores it after.
func restoreOverride(t *testing.T, path string) {
	t.Helper()
	prev := configOverridePath.Load()
	configOverridePath.Store(&path)
	t.Cleanup(func() { configOverridePath.Store(prev) })
}

// The colours must come from the theme that is active AT CALL TIME, not from one
// captured earlier. This is the whole point of the task: the managed conf froze
// them at server start, which is why a live theme change left the band stale.
func TestApplyBarStyle_ReadsTheThemeAtCallTime(t *testing.T) {
	rec := recordRuns(t, nil)

	restoreA := theme.Set("tokyo-night")
	require.NoError(t, ApplyBarStyle(context.Background(), rec.exec))
	restoreA()

	restoreB := theme.Set("catppuccin-mocha")
	require.NoError(t, ApplyBarStyle(context.Background(), rec.exec))
	restoreB()

	// Two pushes of two commands each; the style push is the first of each pair.
	require.Len(t, *rec.cmds, 4)
	ran := []string{(*rec.cmds)[0], (*rec.cmds)[2]}
	require.NotEqual(t, ran[0], ran[1],
		"two different themes must produce two different pushes")
	require.Contains(t, ran[0], "bg="+theme.Hex(theme.Get("tokyo-night").Palette.BarBg))
	require.Contains(t, ran[1], "bg="+theme.Hex(theme.Get("catppuccin-mocha").Palette.BarBg))
}

// RewriteManagedConfig's whole value is that the file it writes carries the theme
// that is current WHEN IT RUNS — it is the half of the fix covering servers that
// start after the theme change, and a conf frozen at the launch-time palette would
// make it a no-op. config_render_test.go asserts only that status-style fills with
// SOME truecolor value, so without this nothing pins which one.
func TestRenderManagedConfig_FollowsTheCurrentTheme(t *testing.T) {
	restoreA := theme.Set("tokyo-night")
	dark, err := renderManagedConfig(true)
	restoreA()
	require.NoError(t, err)

	restoreB := theme.Set("catppuccin-mocha")
	mocha, err := renderManagedConfig(true)
	restoreB()
	require.NoError(t, err)

	require.NotEqual(t, string(dark), string(mocha),
		"two different themes must render two different configs")
	require.Contains(t, collapseWS(string(dark)),
		`status-style "bg=`+theme.Hex(theme.Get("tokyo-night").Palette.BarBg))
	require.Contains(t, collapseWS(string(mocha)),
		`status-style "bg=`+theme.Hex(theme.Get("catppuccin-mocha").Palette.BarBg))
	require.False(t, strings.Contains(string(mocha), theme.Hex(theme.Get("tokyo-night").Palette.BarBg)),
		"the previous theme's band colour must not survive into the rewritten conf")
}
