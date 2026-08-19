package handover

import (
	"os"
	"testing"

	"github.com/ZviBaratz/atrium/internal/testutil"
)

// TestMain is a backstop: every test here also points HOME at its own temp dir, but
// pinning it for the whole package means a test that forgets can still never take a
// lock in, or truncate a file under, the user's real ~/.atrium (CLAUDE.md).
func TestMain(m *testing.M) {
	os.Exit(testutil.SandboxHomeMain(m))
}
