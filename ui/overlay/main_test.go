package overlay

import (
	"os"
	"testing"

	"github.com/ZviBaratz/atrium/internal/testutil"
)

// TestMain sandboxes HOME for the whole ui/overlay suite. The settings panel's
// read-only Config file row resolves config.GetConfigDir(), which stats
// $HOME/.atrium and $HOME/.claude-squad, so without this the suite reads the
// developer's real data dir (CLAUDE.md: "Tests must never read or write the
// user's real data dir").
//
// A sandbox here is only load-bearing because that resolution is lazy: a
// package-level var initializer would run before TestMain and capture the real
// HOME regardless — see configFilePath's comment and TestConfigFilePathHonoursSandboxedHome.
func TestMain(m *testing.M) {
	os.Exit(testutil.SandboxHomeMain(m))
}
