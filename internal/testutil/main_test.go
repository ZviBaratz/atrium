package testutil

import (
	"os"
	"testing"
)

// TestMain sandboxes this package's own tests with the helper they exercise: the
// tmux-isolation tests below start a real server, and #581's rule is that a
// real-tmux test never runs on the developer's live socket — including the tests
// of the mechanism that enforces it.
func TestMain(m *testing.M) { os.Exit(SandboxHomeMain(m)) }
