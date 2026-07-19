package keys

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// moduleFile walks up from the test's working directory to the module root
// (where go.mod lives) and reads the named file. Test CWD is the package dir, so
// docs at the repo root are reachable regardless of which package runs.
func moduleFile(t *testing.T, name string) string {
	t.Helper()
	dir, err := os.Getwd()
	require.NoError(t, err)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			b, err := os.ReadFile(filepath.Join(dir, name))
			require.NoError(t, err)
			return string(b)
		}
		parent := filepath.Dir(dir)
		require.NotEqualf(t, parent, dir, "reached filesystem root without finding go.mod (looking for %s)", name)
		dir = parent
	}
}

// TestReadmeDocumentsEveryBinding is the anti-rot guard for #382: every key the
// in-app `?` cheatsheet documents (the registry) must also appear in the README's
// Keybindings section, backtick-wrapped in that section specifically so a
// single-char label can't be satisfied by coincidental prose elsewhere.
func TestReadmeDocumentsEveryBinding(t *testing.T) {
	readme := moduleFile(t, "README.md")
	start := strings.Index(readme, "#### Keybindings")
	require.GreaterOrEqual(t, start, 0, "README must have a #### Keybindings section")
	rest := readme[start:]
	end := strings.Index(rest, "#### Filtering")
	require.Greater(t, end, 0, "the Keybindings section must be bounded by the Filtering section")
	section := rest[:end]

	for _, e := range Registry {
		label := e.Binding.Help().Key
		if label == "" {
			continue
		}
		require.Containsf(t, section, "`"+label+"`",
			"README Keybindings table is missing `%s` (%s) — the ? overlay documents it; keep the README in sync",
			label, e.Binding.Help().Desc)
	}
}
