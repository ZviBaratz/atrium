package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/internal/doctor"
	"github.com/ZviBaratz/atrium/internal/handover"
	"github.com/stretchr/testify/require"
)

// moduleRoot returns the module root, walking up from the test's working
// directory until it finds go.mod.
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	require.NoError(t, err)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		require.NotEqual(t, dir, parent, "walked to the filesystem root without finding go.mod")
		dir = parent
	}
}

// moduleFile reads a file from the module root. Mirrors the same helper in
// config/readme_config_test.go and keys/readme_drift_test.go.
func moduleFile(t *testing.T, name string) string {
	t.Helper()
	return readFile(t, filepath.Join(moduleRoot(t), name))
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

// TestReadmeNamesTheHandoverLock keeps the Scripting section's claim about what `send`
// and `new` consult tied to the artifact they consult, rather than to a sentence nobody
// re-reads. "Prose says why; data says what" (CLAUDE.md): the filename is the datum, and
// a rename or a removal of the mechanism fails here instead of leaving the README
// describing a lock that no longer exists.
//
// It does not check what the prose says ABOUT the lock — no test can — only that the
// name is present, which is the part that goes stale silently.
func TestReadmeNamesTheHandoverLock(t *testing.T) {
	section := readmeSection(t, "#### Scripting Atrium", "#### Keybindings")
	require.Contains(t, section, handover.LockFilename,
		"the Scripting section states which locks the headless commands take; name this one")
	require.Contains(t, section, tuiLockFilename, "and the one it is read alongside")
}

// readmeSection returns the README text between two headings, failing rather than
// searching the whole file: a guard that fell back to the document would keep passing
// after the section it is about was renamed away.
func readmeSection(t *testing.T, startMarker, endMarker string) string {
	t.Helper()
	readme := moduleFile(t, "README.md")
	start := strings.Index(readme, startMarker)
	require.GreaterOrEqual(t, start, 0, "README is missing the %q heading", startMarker)
	end := strings.Index(readme[start:], endMarker)
	require.Greater(t, end, 0, "README is missing the %q heading after %q", endMarker, startMarker)
	return readme[start : start+end]
}

// TestEveryCommandHasAShortDescription: Cobra lists Short in `atrium --help`, so
// an empty one leaves a blank row in the very output a new user reads first.
func TestEveryCommandHasAShortDescription(t *testing.T) {
	for _, c := range rootCmd.Commands() {
		require.NotEmpty(t, strings.TrimSpace(c.Short), "command %q has no Short description", c.Name())
	}
}

// TestHeadlessCommandsRunWhileTheTUIHoldsItsLock pins the property that makes the
// headless surface usable at all: `ls`, `peek`, `send`, `new` and `reap` must run while
// the TUI is up. Only the bare atrium and reset may *hold* tui.lock; a subcommand that
// held it would refuse exactly when a user most wants it.
//
// Holding is the property, not touching. `send` and `new` do acquire the lock — briefly,
// non-blockingly, and after they have already spooled — because that try-acquire is how
// tuiRunning answers "is anyone there to deliver this?" for the warning they print. That
// is why this asserts each command *succeeds* against a held lock rather than asserting
// it never calls acquireTUILock: the second would be false, and the first is what a
// caller actually depends on.
//
// `reap` is the sharpest case rather than one more of the same. It exists for a tmux
// server that outlived its run and is eating memory now, which is a thing a user
// discovers *while* mid-session — a lock would make it refuse in precisely the
// situation it was written for.
//
// `new` is the one whose lock would be most tempting to take, since its effect is the
// heaviest — a worktree, a branch and a running agent, where `send`'s is a queued
// prompt and `reap`'s a dead tmux server. It must not: none of that happens here. The
// work happens in the TUI that already holds the lock, so taking it would make `atrium
// new` work only when there is nothing to execute the request (#703).
func TestHeadlessCommandsRunWhileTheTUIHoldsItsLock(t *testing.T) {
	sandboxDataDir(t)

	// Hold BOTH locks, standing in for a running TUI whose terminal is handed to a
	// session — the parked case #760 added handover.lock for. Holding tui.lock alone
	// would leave the newer probe untested here, and the newer probe is the one whose
	// failure mode is the same temptation: reading "nobody is draining this" as a reason
	// to refuse rather than as a reason to warn. Nothing here may refuse.
	lockPath, err := tuiLockPath()
	require.NoError(t, err)
	release, err := acquireTUILock(lockPath)
	require.NoError(t, err)
	defer release()
	releaseHandover, err := handover.Hold(handover.Payload{Kind: handover.KindAttach, Label: "fix-auth"})
	require.NoError(t, err)
	defer releaseHandover()

	seedInstances(t, inst("fix-auth", "/repo/web"))

	require.NoError(t, runLs(io.Discard, true), "ls must work while a TUI holds the lock")

	_, _, err = send(t, "fix-auth", "", "hello", 0)
	require.NoError(t, err, "send must work while a TUI holds the lock")

	_, _, err = newSession(t, newRequest{title: "another", path: tempRepo(t)})
	require.NoError(t, err, "new must work while a TUI holds the lock")

	f := &fakeTmux{content: "pane\n"}
	require.NoError(t, runPeek(context.Background(), io.Discard, f.exec(), "fix-auth", "", 0, false),
		"peek must work while a TUI holds the lock")

	// Stubbed rather than left to the real scan: it probes the ambient tmux socket,
	// and package main has no TestMain sandboxing TMUX_TMPDIR, so an unstubbed call
	// would reach the developer's live fleet. The lock is taken (or not) by the
	// command, not by the scan, so nothing about this property is stubbed away.
	stubReapCheck(t, doctor.OrphanResult{Supported: true})
	require.NoError(t, runReap(context.Background(), io.Discard, strings.NewReader(""), reapOpts{}),
		"reap must work while a TUI holds the lock")
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
