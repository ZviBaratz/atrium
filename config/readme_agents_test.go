package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestReadmeNamesEveryProbedAgent is the fourth README drift guard, and it exists because
// adding an agent used to touch three README sentences that no test could see. Every other
// site a new adapter touches has one — paneCoverage derives its keys from the registry, the
// glyph tables are held by TestEveryAgentAdapterHasAnIdentityGlyph, the version pin by
// TestAdaptersExposesSeededVersions — so the README was the one place a sixth agent could
// ship undocumented with a green suite.
//
// It asserts against the FIRST-RUN PROBE SENTENCE specifically, not against the README as a
// whole. A bare document-wide search would pass on any agent whose name happens to appear
// anywhere in the file — "codex" occurs in several unrelated examples — which is the
// coincidental pass TestReadmeDocumentsEveryPaletteToken's own header warns about, and it
// reads one subsection for the same reason. The probe sentence is the one place the README
// claims to enumerate the set, so it is the one place that can be wrong about it.
func TestReadmeNamesEveryProbedAgent(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "README.md"))
	require.NoError(t, err)

	const marker = "Atrium probes for installed agent CLIs ("
	i := strings.Index(string(raw), marker)
	require.GreaterOrEqual(t, i, 0,
		"the README no longer has the first-run probe sentence this guard reads; if it moved, "+
			"move the marker, and if it went, this guard needs a new anchor rather than deleting")
	rest := string(raw)[i+len(marker):]
	j := strings.Index(rest, ")")
	require.GreaterOrEqual(t, j, 0, "the probe sentence's agent list is unterminated")
	listed := rest[:j]

	for _, bin := range knownAgentBins {
		require.Containsf(t, listed, "`"+bin+"`",
			"knownAgentBins probes for %q, and the README's first-run sentence does not name "+
				"it — a user installing that CLI is told Atrium will not find it", bin)
	}

	// The other direction: a name the README lists and the code no longer probes for is just
	// as wrong, and it is the half a one-way check misses.
	for _, name := range strings.Split(listed, ",") {
		name = strings.Trim(strings.TrimSpace(name), "`")
		if name == "" {
			continue
		}
		require.Containsf(t, knownAgentBins, name,
			"the README's first-run sentence names %q, which knownAgentBins does not probe for", name)
	}
}
