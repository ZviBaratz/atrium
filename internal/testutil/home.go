// Package testutil provides shared helpers for tests across the module.
package testutil

import (
	"os"
	"testing"
)

// SandboxHomeMain points HOME at a throwaway temp directory (and unsets
// CLAUDE_CONFIG_DIR, which outranks HOME in transcript-root resolution) for the
// duration of a package's tests, then runs them. This keeps tests from reading
// or writing the developer's real Atrium data directory (~/.atrium or a legacy
// ~/.claude-squad) or Claude Code config, and — because the tmux socket name
// derives from config.RuntimeName, which is resolved from HOME — keeps
// real-tmux tests on an isolated "atrium" socket instead of the user's live
// "claudesquad" sessions.
//
// Use it from a package's TestMain:
//
//	func TestMain(m *testing.M) { os.Exit(testutil.SandboxHomeMain(m)) }
//
// It panics rather than falling back to the real HOME, so a setup failure can
// never silently run tests against live state.
func SandboxHomeMain(m *testing.M) int {
	tmp, err := os.MkdirTemp("", "atrium-test-home-")
	if err != nil {
		panic("testutil: failed to create sandbox HOME: " + err.Error())
	}
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
	return m.Run()
}
