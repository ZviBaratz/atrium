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
