package config

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestReadmeNamesEveryProbedAgent guards the README's first-run probe sentence, which claims to
// enumerate the agent CLIs Atrium looks for and so is the one line in the document that can be
// wrong about the set.
//
// NOT "the last unguarded site", which is what this said when it landed, and not an ordinal
// either — two other guards in this tree already claim to be the fourth. Adding an agent used
// to touch four user-facing enumerations and this guard covered one of them: `atrium profiles
// detect --help`, `atrium doctor --help` and the first-run overlay's no-agents-found line were
// all still naming four agents two adapters later, and the overlay's is the one shown to a user
// with nothing installed — so the agent they were being told to install was the one Atrium had
// just added. Those three now render KnownAgentBins instead of restating it, which is why they
// need no guard; the README is prose and cannot, so it keeps this one.
//
// It reads the probe sentence rather than searching the whole document: a bare document-wide
// search would pass on any agent whose name appears anywhere in the file — "codex" occurs in
// several unrelated examples — which is the coincidental pass TestReadmeDocumentsEveryPaletteToken's
// own header warns about.
func TestReadmeNamesEveryProbedAgent(t *testing.T) {
	readme := moduleFile(t, "README.md")

	const marker = "Atrium probes for installed agent CLIs ("
	i := strings.Index(readme, marker)
	require.GreaterOrEqual(t, i, 0,
		"the README no longer has the first-run probe sentence this guard reads; if it moved, "+
			"move the marker, and if it went, this guard needs a new anchor rather than deleting")
	rest := readme[i+len(marker):]
	j := strings.Index(rest, ")")
	require.GreaterOrEqual(t, j, 0, "the probe sentence's agent list is unterminated")
	listed := rest[:j]

	bins := KnownAgentBins()
	for _, bin := range bins {
		require.Containsf(t, listed, "`"+bin+"`",
			"Atrium probes for %q, and the README's first-run sentence does not name it — a "+
				"user installing that CLI is told Atrium will not find it", bin)
	}

	// The other direction: a name the README lists and the code no longer probes for is just
	// as wrong, and it is the half a one-way check misses.
	for _, name := range strings.Split(listed, ",") {
		name = strings.Trim(strings.TrimSpace(name), "`")
		if name == "" {
			continue
		}
		require.Containsf(t, bins, name,
			"the README's first-run sentence names %q, which Atrium does not probe for", name)
	}
}

// TestKnownAgentBinsTracksTheRegistry is the tie that did not exist. KnownAgentBins was a
// hand-written literal, so an adapter added to the registry and forgotten there was missing from BOTH sides of
// every comparison: the glyph guard, the version pin and paneCoverage all passed, this file's
// README check compared the README against the short list and passed too, and the new agent was
// simply never probed for. Deriving the list is the fix; this is what holds the derivation.
func TestKnownAgentBinsTracksTheRegistry(t *testing.T) {
	bins := KnownAgentBins()
	require.NotEmpty(t, bins)
	for _, bin := range bins {
		require.Equalf(t, bin, string(agentResolveKey(bin)),
			"%q must resolve to the adapter it is named for, or detection seeds a profile "+
				"whose heuristics belong to a different agent", bin)
	}
	require.Equal(t, "claude", bins[0],
		"picker order is registry order, and seededDefaultConfig makes the first entry the "+
			"default program")
}
