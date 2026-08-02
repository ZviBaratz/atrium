package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/log"
	"github.com/stretchr/testify/require"
)

// The log goes in the data dir because that is the one thing that already tells
// one Atrium instance from another (#566): an isolated instance with its own HOME
// gets its own log rather than interleaving into the real fleet's.
func TestLogDir_IsTheDataDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	want, err := config.GetConfigDir()
	require.NoError(t, err)
	require.Equal(t, want, logDir())
	require.Equal(t, filepath.Join(home, ".atrium"), logDir(),
		"a fresh install logs under ~/.atrium")
}

// A legacy install keeps its existing directory, the same rule the data dir
// itself follows: live state there is addressed by absolute path and is never
// migrated, so the log follows the dir rather than forcing a new one into being.
func TestLogDir_FollowsALegacyDataDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	legacy := filepath.Join(home, ".claude-squad")
	require.NoError(t, os.MkdirAll(legacy, 0o755))

	require.Equal(t, legacy, logDir(),
		"an install predating the rename must not have its log moved out from under it")
}

// With no resolvable home there is no data dir to derive, and no second instance
// to be confused with either. Falling back to the temp dir keeps diagnostics
// instead of dropping them.
func TestLogDir_FallsBackToTheTempDirWithoutAHome(t *testing.T) {
	t.Setenv("HOME", "")
	if _, err := os.UserHomeDir(); err == nil {
		t.Skip("this platform resolves a home directory without $HOME")
	}

	require.Equal(t, os.TempDir(), logDir())
}

// `atrium debug` is how a user is told where to look, and since the log left the
// temp dir it is the only command that reports the path on demand. Both states
// are covered: a live log names its file, an unopenable one says why.
func TestDebugCmd_PrintsTheLogPath(t *testing.T) {
	t.Run("live log", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Cleanup(log.Initialize(t.TempDir(), false))

		out := runDebug(t)

		require.Contains(t, out, "Config: ")
		require.Contains(t, out, "Log: ")
		require.Contains(t, out, filepath.Join(home, ".atrium", "atrium.log"))
		require.NotContains(t, out, "unavailable")
		require.Less(t, strings.Index(out, "Log: "), strings.Index(out, "{"),
			"the log path must sit above the config JSON, not be buried under it")
	})

	t.Run("unopenable log", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root ignores the file mode, so the destination is openable")
		}
		home := t.TempDir()
		t.Setenv("HOME", home)
		dataDir := filepath.Join(home, ".atrium")
		require.NoError(t, os.MkdirAll(dataDir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dataDir, "atrium.log"), nil, 0o444))
		t.Cleanup(log.Initialize(t.TempDir(), false))

		out := runDebug(t)

		require.Contains(t, out, "unavailable",
			"a log that could not be opened must say so rather than be reported as a path")
		require.Contains(t, out, filepath.Join(dataDir, "atrium.log"),
			"the report must name the file it tried")
	})
}

// runDebug executes `atrium debug` with its output captured. The command writes
// through cmd.OutOrStdout precisely so this is possible.
func runDebug(t *testing.T) string {
	t.Helper()
	var buf bytes.Buffer
	debugCmd.SetOut(&buf)
	debugCmd.SetErr(&buf)
	t.Cleanup(func() { debugCmd.SetOut(nil); debugCmd.SetErr(nil) })
	require.NoError(t, debugCmd.RunE(debugCmd, nil))
	return buf.String()
}
