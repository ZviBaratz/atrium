// Package testutil provides shared helpers for tests across the module.
package testutil

import (
	"os"
	"testing"
)

// homeRootPrefix names a sandbox HOME root. Like tmuxRootPrefix it is doing safety
// work: sweepStaleRoots deletes directories matching it, so it has to be
// distinctive enough that it cannot match anything a developer put in $TMPDIR
// themselves. Deliberately not shared with internal/update's own
// "atrium-update-test-home-" roots — that TestMain sandboxes by hand and is not
// swept, so the prefixes must not overlap.
const homeRootPrefix = "atrium-test-home-"

// SandboxHomeMain isolates a package's tests from the developer's live Atrium for
// their whole run, then runs them. It sandboxes two independent things:
//
//   - The data directory, by pointing HOME at a throwaway temp directory (and
//     unsetting CLAUDE_CONFIG_DIR and XDG_CONFIG_HOME, which outrank it). This
//     keeps tests from reading or writing ~/.atrium, a legacy ~/.claude-squad, or
//     Claude Code config.
//   - The tmux socket directory, by pointing TMUX_TMPDIR at a private root and
//     reaping every server under it afterwards.
//
// The second is not implied by the first, which is the trap this helper used to
// document the wrong way round. tmux resolves `-L <name>` to
// $TMUX_TMPDIR/tmux-<uid>/<name>, and Atrium's name comes from
// config.RuntimeName: that returns "atrium" for a sandbox HOME *and* for every
// non-legacy install, so a sandboxed HOME leaves real-tmux tests binding the exact
// socket the developer's running Atrium is on. Only the directory separates them
// (#581). RequireTmux enforces that this ran.
//
// Use it from a package's TestMain:
//
//	func TestMain(m *testing.M) { os.Exit(testutil.SandboxHomeMain(m)) }
//
// It panics rather than falling back to the real HOME, so a setup failure can
// never silently run tests against live state. The socket sandbox fails the other
// way — if it cannot be installed it is simply absent, and the per-test gates fail
// the tests that needed it. Same guarantee, smaller blast radius: a host without a
// usable /tmp still runs every test that never touches tmux.
//
// That second guarantee is only as good as the gates' coverage, and it is the half
// worth checking when a real-tmux site is added. Every site that can reach a real
// server carries RequireTmux or, where a plain skip has to be kept, the bare
// RequireSandboxedTmux — including the two that do not look like real-tmux sites at
// all: session/tmux's TestSessionDeathStopsProbing (its own LookPath skip) and
// TestInitAndTmuxConfigPath_AreRaceFree (Init starts a probe server via
// validateConfig). A site that relies on this TestMain alone is a site where a failed
// install is silent, which is the state #581 describes.
func SandboxHomeMain(m *testing.M) int {
	tmp, err := os.MkdirTemp("", homeRootPrefix)
	if err != nil {
		panic("testutil: failed to create sandbox HOME: " + err.Error())
	}
	// Marked before anything else can look at it, and a failure is fatal for the same
	// reason the rest of this function's failures are: the marker is what lets a later
	// run recognise this directory as an orphan, so an unmarked root is one nothing
	// will ever reclaim — the leak this whole file exists to stop, made permanent.
	if err := markRootOwner(tmp); err != nil {
		_ = os.RemoveAll(tmp)
		panic("testutil: failed to mark sandbox HOME: " + err.Error())
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	// Reap the HOME roots of runs that never reached that defer. Every abort skips it
	// — a signal, a -timeout kill, an os.Exit from inside a test — and unlike the tmux
	// root there is no server to strand, only a directory. It still added up to 633 of
	// them over five days before this existed.
	sweepStaleRoots(os.TempDir(), homeRootPrefix, tmp, nil)
	if err := os.Setenv("HOME", tmp); err != nil {
		panic("testutil: failed to set sandbox HOME: " + err.Error())
	}
	// CLAUDE_CONFIG_DIR outranks HOME in transcript-root resolution
	// (session/transcript), so a developer shell that exports it — any shell
	// inside Claude Code does — would quietly point tests back at the real
	// ~/.claude. Drop it so every lookup stays inside the sandbox.
	if err := os.Unsetenv("CLAUDE_CONFIG_DIR"); err != nil {
		panic("testutil: failed to unset CLAUDE_CONFIG_DIR: " + err.Error())
	}
	// XDG_CONFIG_HOME outranks HOME the same way for git: it resolves its global
	// excludes file from $XDG_CONFIG_HOME/git/ignore *before* $HOME/.config/git/ignore.
	// Tests that assert on ignore state (session/git) would otherwise read the
	// developer's real global gitignore, so a host that globally ignores a name a
	// test uses — node_modules is the obvious one — fails it on correct code.
	// Unsetting it lets the lookup fall through to the sandboxed HOME.
	if err := os.Unsetenv("XDG_CONFIG_HOME"); err != nil {
		panic("testutil: failed to unset XDG_CONFIG_HOME: " + err.Error())
	}
	// Armed here, before m.Run, so the reaper exists before any test can start a
	// server — a server started under a root nothing is committed to killing is
	// exactly the orphan #547 is about. Registered after the HOME cleanup above so
	// LIFO reaps the servers first: the socket root is a sibling of the sandbox HOME,
	// not a child of it, but the servers themselves are the other way round — their
	// panes' working directories and the atrium.conf they sourced live under that
	// HOME, so it has to outlast the kill.
	teardownTmux := installSandboxTmuxTmpdir()
	defer teardownTmux()
	return m.Run()
}
