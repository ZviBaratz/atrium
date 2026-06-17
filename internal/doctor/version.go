// Package doctor probes installed agent CLIs and reports whether their versions
// have drifted past the heuristic ceiling Atrium's pane classification was
// verified against. It is the only place that shells out to agent binaries; the
// agent package itself stays pure.
package doctor

import "regexp"

// versionRe matches the first MAJOR.MINOR[.PATCH] token in --version output.
var versionRe = regexp.MustCompile(`\d+\.\d+(?:\.\d+)?`)

// parseVersion extracts the first semver-shaped token from a --version line.
func parseVersion(out string) (string, bool) {
	m := versionRe.FindString(out)
	if m == "" {
		return "", false
	}
	return m, true
}
