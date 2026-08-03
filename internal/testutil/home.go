// Package testutil provides shared helpers for tests across the module.
package testutil

import (
	"os"
	"testing"
)

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
	tmp, err := os.MkdirTemp("", "atrium-test-home-")
	if err != nil {
		panic("testutil: failed to create sandbox HOME: " + err.Error())
	}
	// This defer is the whole cleanup, and an exit that skips it leaks the directory.
	// The fix for that is at the source — nothing in a test may call os.Exit, which
	// TestNoTestCallsOsExit enforces — deliberately not a sweep that deletes stale
	// siblings. A recursive delete over a glob of $TMPDIR is one wrong prefix away
	// from taking the developer's live tmux socket with it, which is precisely what
	// it did here once.
	defer func() { _ = os.RemoveAll(tmp) }()
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
