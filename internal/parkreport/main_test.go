package parkreport

import (
	"os"
	"testing"

	"github.com/ZviBaratz/atrium/internal/testutil"
)

// TestMain is a backstop: every test here also calls sandbox(t) for its own isolated
// data dir, but pinning HOME for the whole package means a test that forgets can still
// never read or unlink a file in the user's real ~/.atrium (CLAUDE.md).
func TestMain(m *testing.M) {
	os.Exit(testutil.SandboxHomeMain(m))
}
