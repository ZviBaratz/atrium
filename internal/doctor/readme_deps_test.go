package doctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// moduleFile reads a file from the module root, walking up from the test's working
// directory until it finds go.mod. Mirrors the identical helpers in
// readme_commands_test.go, config/readme_config_test.go and keys/readme_drift_test.go —
// each README guard carries its own copy because they live in different packages.
func moduleFile(t *testing.T, name string) string {
	t.Helper()
	dir, err := os.Getwd()
	require.NoError(t, err)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			break
		}
		parent := filepath.Dir(dir)
		require.NotEqual(t, dir, parent, "walked to the filesystem root without finding go.mod")
		dir = parent
	}
	b, err := os.ReadFile(filepath.Join(dir, name))
	require.NoError(t, err)
	return string(b)
}

// The README's Prerequisites section must name every dependency doctor probes, and must
// state the version floor for any dependency that declares one. It is the only place a
// user reads before installing, and nothing else checks it: unlike the commands, config
// and keybinding tables, prerequisites had no drift guard at all until this one — so the
// list silently omitted git, which coreDeps has always marked DepRequired.
func TestReadmeDocumentsEveryCoreDependency(t *testing.T) {
	readme := moduleFile(t, "README.md")

	const (
		startMarker = "### Prerequisites"
		endMarker   = "### Usage"
	)
	start := strings.Index(readme, startMarker)
	require.GreaterOrEqual(t, start, 0, "README is missing the %q heading", startMarker)
	end := strings.Index(readme, endMarker)
	require.Greater(t, end, start, "README is missing the %q heading after %q", endMarker, startMarker)
	section := readme[start:end]

	require.NotEmpty(t, coreDeps, "coreDeps is empty; the guard would pass vacuously")

	checked := 0
	for _, s := range coreDeps {
		// Match the markdown link, not a bare substring: "tmux" also appears inside the
		// install URL, so Contains alone would stay green after the bullet was deleted.
		link := "[" + s.name + "]"
		require.Contains(t, section, link,
			"the README Prerequisites section has no %s bullet for the %q dependency", link, s.name)

		if s.minVersion != "" {
			// Assert on the bullet's own line, so the floor cannot be satisfied by a
			// number that happens to appear elsewhere in the section.
			require.True(t, lineWith(section, link, s.minVersion),
				"the README %s bullet does not state the required version %s", link, s.minVersion)
		}
		checked++
	}
	require.NotZero(t, checked, "every dependency was skipped; the guard would pass vacuously")
}

// The installer is the thing that produces a too-old tmux in the first place (it installs
// whatever the host package manager ships), so its warning has to name the same floor the
// code enforces.
func TestInstallScriptStatesTheTmuxFloor(t *testing.T) {
	script := moduleFile(t, "install.sh")

	floor := ""
	for _, s := range coreDeps {
		if s.bin == "tmux" {
			floor = s.minVersion
		}
	}
	require.NotEmpty(t, floor, "the tmux spec declares no floor; the guard would pass vacuously")
	// A bare require.Contains would dump the whole installer into the failure output.
	require.True(t, strings.Contains(script, `MIN_TMUX_VERSION="`+floor+`"`),
		"install.sh does not set MIN_TMUX_VERSION=%q, so its warning cannot be naming the real floor", floor)
}

// lineWith reports whether some line of section contains both needles.
func lineWith(section, a, b string) bool {
	for _, line := range strings.Split(section, "\n") {
		if strings.Contains(line, a) && strings.Contains(line, b) {
			return true
		}
	}
	return false
}
