package git

import (
	"os"
	"testing"

	"github.com/ZviBaratz/atrium/internal/testutil"
)

func TestMain(m *testing.M) { os.Exit(testutil.SandboxHomeMain(m)) }
