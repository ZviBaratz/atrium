package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// moduleFile reads a file from the module root, walking up from the test's
// working directory until it finds go.mod. Mirrors the same helper in
// config/readme_config_test.go and keys/readme_drift_test.go.
func moduleFile(t *testing.T, name string) string {
	t.Helper()
	dir, err := os.Getwd()
	require.NoError(t, err)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			data, err := os.ReadFile(filepath.Join(dir, name))
			require.NoError(t, err)
			return string(data)
		}
		parent := filepath.Dir(dir)
		require.NotEqual(t, dir, parent, "walked to the filesystem root without finding go.mod")
		dir = parent
	}
}

// TestReadmeDocumentsEveryCommand is the third README drift guard, alongside
// config.TestReadmeDocumentsEveryConfigField and keys.TestReadmeDocumentsEveryBinding.
//
// It exists because the Usage section used to be a hand-copied paste of Cobra's
// help output with nothing checking it, and it had already drifted: it described
// doctor by a Short string the command no longer had. A command a user cannot
// discover may as well not ship.
func TestReadmeDocumentsEveryCommand(t *testing.T) {
	readme := moduleFile(t, "README.md")

	const (
		startMarker = "### Usage"
		endMarker   = "#### Keybindings"
	)
	start := strings.Index(readme, startMarker)
	require.GreaterOrEqual(t, start, 0, "README is missing the %q heading", startMarker)
	end := strings.Index(readme, endMarker)
	require.Greater(t, end, start, "README is missing the %q heading after %q", endMarker, startMarker)
	section := readme[start:end]

	commands := rootCmd.Commands()
	require.NotEmpty(t, commands, "rootCmd has no subcommands; the guard would pass vacuously")

	documented := 0
	for _, c := range commands {
		// Hidden commands are internal plumbing invoked by Atrium itself (the
		// `hook` receiver tmux calls back into), never typed by a user. They are
		// absent from `atrium --help` on purpose, so documenting them would be
		// noise rather than drift.
		if c.Hidden {
			continue
		}
		name := c.Name()
		// Require an actual row in the commands table, not merely the name
		// somewhere in the section. Several commands are also mentioned in the
		// surrounding prose, so a bare "contains" would stay green after a row
		// was deleted — the guard would then only catch a command documented
		// nowhere at all, which is the easy half of the drift.
		require.True(t, hasCommandRow(section, name),
			"the README Usage section has no commands-table row (`| `%s` | … |`) for the %q command", name, name)
		documented++
	}
	require.NotZero(t, documented, "every command was skipped; the guard would pass vacuously")
}

// TestEveryCommandHasAShortDescription: Cobra lists Short in `atrium --help`, so
// an empty one leaves a blank row in the very output a new user reads first.
func TestEveryCommandHasAShortDescription(t *testing.T) {
	for _, c := range rootCmd.Commands() {
		require.NotEmpty(t, strings.TrimSpace(c.Short), "command %q has no Short description", c.Name())
	}
}

// TestHeadlessCommandsTakeNoTUILock pins the property that makes the headless
// surface usable at all: `ls`, `peek` and `send` must run while the TUI is up.
// Only the bare atrium and reset may take tui.lock, so a subcommand that started
// acquiring it would refuse exactly when a user most wants it.
func TestHeadlessCommandsTakeNoTUILock(t *testing.T) {
	sandboxDataDir(t)

	// Hold the lock, standing in for a running TUI.
	lockPath, err := tuiLockPath()
	require.NoError(t, err)
	release, err := acquireTUILock(lockPath)
	require.NoError(t, err)
	defer release()

	seedInstances(t, inst("fix-auth", "/repo/web"))

	require.NoError(t, runLs(io.Discard, true), "ls must work while a TUI holds the lock")

	_, _, err = send(t, "fix-auth", "", "hello", 0)
	require.NoError(t, err, "send must work while a TUI holds the lock")

	f := &fakeTmux{content: "pane\n"}
	require.NoError(t, runPeek(context.Background(), io.Discard, f.exec(), "fix-auth", "", 0, false),
		"peek must work while a TUI holds the lock")
}

// hasCommandRow reports whether the section contains a markdown table row whose
// first cell is exactly the backticked command name.
func hasCommandRow(section, name string) bool {
	want := "| `" + name + "` |"
	for _, line := range strings.Split(section, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), want) {
			return true
		}
	}
	return false
}
