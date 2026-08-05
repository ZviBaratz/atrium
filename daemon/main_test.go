package daemon

import (
	"os"
	"testing"

	"github.com/ZviBaratz/atrium/internal/testutil"
)

// TestMain sandboxes HOME (the data dir) and TMUX_TMPDIR (the socket root) for the whole
// package. This one is not merely a backstop: saveAndReport writes into the data dir
// config.GetConfigDir resolves, so without it a test here would spool a file into — and
// unlink one from — the developer's live ~/.atrium (CLAUDE.md, "tests must stay
// hermetic").
func TestMain(m *testing.M) {
	os.Exit(testutil.SandboxHomeMain(m))
}
