package main

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/ui/theme"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// theme_launch_test.go guards #574: the managed tmux config used to be rendered before
// the configured theme was ever activated, so every session started in a run opened
// with the DEFAULT palette's status band whatever config.json said.
//
// The reproduction here is the issue's own, minus the interactive step: write a
// config.json naming a non-default palette, run the startup sequence, read the band out
// of the file it wrote.

// launchSandbox gives a test its own data dir and takes tmux off PATH.
//
// Removing tmux is what keeps this hermetic rather than merely isolated: without it
// validateConfig starts a real tmux server to parse the file it just wrote, and a
// package with no TestMain has no private TMUX_TMPDIR for that server to live in. The
// managed conf is written before that validation either way, so nothing under test is
// skipped — see session/tmux/config.go's Init.
func launchSandbox(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", t.TempDir())
	// Restore the process-wide theme selection, which ApplyThemeAtLaunch moves. Both
	// axes, and registered before the call so each restore captures the state this test
	// found rather than the one it created.
	t.Cleanup(theme.Set(theme.DefaultThemeName))
	t.Cleanup(theme.SetUserThemes(nil))
	return home
}

// barStyle reads the status-style line out of the managed tmux config.
var barStyle = regexp.MustCompile(`(?m)^set-option -g\s+status-style\s+"([^"]*)"`)

func managedBand(t *testing.T, home string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(home, ".atrium", "atrium.conf"))
	require.NoError(t, err, "the managed tmux config must have been written")
	m := barStyle.FindSubmatch(raw)
	require.NotNil(t, m, "no status-style line in the managed config:\n%s", raw)
	return string(m[1])
}

// TestManagedConfCarriesTheConfiguredPalette is #574 itself. catppuccin-mocha rather
// than a made-up palette because the issue names it, and because its band differs from
// the default's in both fields — a fix that only got the background right would still
// fail this.
func TestManagedConfCarriesTheConfiguredPalette(t *testing.T) {
	home := launchSandbox(t)
	cfg := config.DefaultConfig()
	cfg.Theme = "catppuccin-mocha"
	require.NoError(t, config.SaveConfig(cfg))

	initAppearanceAndTmux(config.LoadConfig())

	mocha := theme.Get("catppuccin-mocha").Palette
	assert.Equal(t, "bg="+theme.Hex(mocha.BarBg)+",fg="+theme.Hex(mocha.Fg), managedBand(t, home))

	dflt := theme.Get(theme.DefaultThemeName).Palette
	assert.NotEqual(t, "bg="+theme.Hex(dflt.BarBg)+",fg="+theme.Hex(dflt.Fg), managedBand(t, home),
		"precondition: the two palettes' bands differ, so this test can tell them apart")
}

// TestManagedConfCarriesAUserThemesPalette is the same guard one layer out: a palette
// that exists only as a file in the data dir has to be registered before the conf is
// rendered, not merely before the first frame.
func TestManagedConfCarriesAUserThemesPalette(t *testing.T) {
	home := launchSandbox(t)
	themes := filepath.Join(home, ".atrium", "themes")
	require.NoError(t, os.MkdirAll(themes, 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(themes, "midnight.json"),
		[]byte(`{"extends": "tokyo-night", "palette": {"bar_bg": "#3a4060"}}`), 0o600))

	cfg := config.DefaultConfig()
	cfg.Theme = "midnight"
	require.NoError(t, config.SaveConfig(cfg))

	initAppearanceAndTmux(config.LoadConfig())

	assert.Equal(t, "bg=#3a4060,fg="+theme.Hex(theme.Get("tokyo-night").Palette.Fg),
		managedBand(t, home), "the band must come from the user's file")
	assert.Equal(t, "midnight", theme.Current().Name, "and the TUI must be on the same palette")
}

// TestRefusedUserThemeDoesNotStrandTheLaunch: a theme file the oracle refuses must
// leave the launch on the default palette rather than half-applied or aborted. The
// refusal itself is reported by newHome's buffered notice and by `atrium doctor`; this
// asserts the startup path survives it.
func TestRefusedUserThemeDoesNotStrandTheLaunch(t *testing.T) {
	home := launchSandbox(t)
	themes := filepath.Join(home, ".atrium", "themes")
	require.NoError(t, os.MkdirAll(themes, 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(themes, "washed.json"),
		[]byte(`{"palette": {"fg": "#111111"}}`), 0o600))

	cfg := config.DefaultConfig()
	cfg.Theme = "washed"
	require.NoError(t, config.SaveConfig(cfg))

	initAppearanceAndTmux(config.LoadConfig())

	dflt := theme.Get(theme.DefaultThemeName).Palette
	assert.Equal(t, "bg="+theme.Hex(dflt.BarBg)+",fg="+theme.Hex(dflt.Fg), managedBand(t, home))
	assert.NotContains(t, theme.Names(), "washed", "a refused palette must not be registered")
}

// TestTmuxInitIsOnlyReachedThroughInitAppearanceAndTmux is what stops #574 coming back.
//
// The two tests above drive the function that owns the order, so on their own they
// prove only that THAT function is correct — a new startup path calling tmux.Init
// directly would reintroduce the defect with every one of them still green. This reads
// main.go and holds the call site to one.
func TestTmuxInitIsOnlyReachedThroughInitAppearanceAndTmux(t *testing.T) {
	src, err := os.ReadFile("main.go")
	require.NoError(t, err)

	calls := regexp.MustCompile(`tmux\.Init\(`).FindAllStringIndex(string(src), -1)
	require.Len(t, calls, 1,
		"tmux.Init must be called only from initAppearanceAndTmux, which activates the theme first (#574)")

	fn := regexp.MustCompile(`(?m)^func initAppearanceAndTmux\(`).FindStringIndex(string(src))
	require.NotNil(t, fn, "initAppearanceAndTmux is gone; the ordering has no owner")
	end := regexp.MustCompile(`(?m)^}`).FindStringIndex(string(src)[fn[1]:])
	require.NotNil(t, end)
	assert.Truef(t, calls[0][0] > fn[1] && calls[0][0] < fn[1]+end[1],
		"the tmux.Init call is outside initAppearanceAndTmux")
}
