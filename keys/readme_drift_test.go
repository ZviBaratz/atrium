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
	// Bounded by Remapping keys, not Filtering. The remapping section's action
	// table has a "Default" column full of the same backticked labels, so letting
	// the section run to Filtering would let a label deleted from the keybindings
	// table go on being satisfied by its own entry in the remapping one.
	end := strings.Index(rest, "#### Remapping keys")
	require.Greater(t, end, 0, "the Keybindings section must be bounded by the Remapping keys section")
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

// TestReadmeDocumentsEveryAction is the same guard one layer out, and the
// stronger of the two: an Action is the name a user's config.json binds to, so
// an undocumented one is a feature nobody can find, and a documented one that no
// longer exists is a config line that silently does nothing.
//
// Backtick-wrapped inside the action table specifically, so a name like "help"
// or "new" cannot be satisfied by ordinary prose elsewhere in the README.
func TestReadmeDocumentsEveryAction(t *testing.T) {
	readme := moduleFile(t, "README.md")
	start := strings.Index(readme, "##### Action names")
	require.GreaterOrEqual(t, start, 0, "README must have an Action names table")
	rest := readme[start:]
	end := strings.Index(rest, "#### Filtering")
	require.Greater(t, end, 0, "the Action names table must be bounded by the Filtering section")
	section := rest[:end]

	documented := 0
	for _, e := range Registry {
		if e.Action == "" {
			continue
		}
		documented++
		require.Containsf(t, section, "`"+e.Action+"`",
			"README's action table is missing `%s` (%s) — config can bind it, so it has to be findable",
			e.Action, e.Binding.Help().Desc)
		require.Containsf(t, section, "`"+e.Binding.Help().Key+"`",
			"README's action table must show `%s`'s default key `%s`",
			e.Action, e.Binding.Help().Key)
	}
	require.Positive(t, documented, "the registry must carry actions for this to mean anything")

	// The other direction: a row naming an action the registry does not have would
	// be a config line the user could write and Atrium would refuse.
	known := map[string]bool{}
	for _, e := range Registry {
		if e.Action != "" {
			known[e.Action] = true
		}
	}
	// The table is action | default | action | default, so the action names are
	// the odd-numbered cells. Identifying them by column rather than by shape is
	// the point: a key label like "tab" or "space" is lowercase letters too, and a
	// shape test would read it as an action and demand one exist.
	for _, line := range strings.Split(section, "\n") {
		if !strings.HasPrefix(line, "| `") {
			continue
		}
		cells := strings.Split(line, "|")
		for i := 1; i < len(cells); i += 2 {
			cell := strings.TrimSpace(cells[i])
			if cell == "" {
				continue // the short column of the last row
			}
			name, ok := strings.CutPrefix(cell, "`")
			require.Truef(t, ok, "action cell %q must be backtick-wrapped", cell)
			name, ok = strings.CutSuffix(name, "`")
			require.Truef(t, ok, "action cell %q must be backtick-wrapped", cell)
			require.Truef(t, known[name],
				"README's action table lists `%s`, which is not an action — a user writing "+
					"that in config.json would just get a refusal", name)
		}
	}
}
