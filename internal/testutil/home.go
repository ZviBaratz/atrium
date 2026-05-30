// Package testutil provides shared helpers for tests across the module.
package testutil

import (
	"os"
	"testing"
)

// SandboxHomeMain points HOME at a throwaway temp directory for the duration of a
// package's tests, then runs them. This keeps tests from reading or writing the
// developer's real Atrium data directory (~/.atrium or a legacy ~/.claude-squad)
// and — because the tmux socket name derives from config.RuntimeName, which is
// resolved from HOME — keeps real-tmux tests on an isolated "atrium" socket
// instead of the user's live "claudesquad" sessions.
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
	return m.Run()
}
