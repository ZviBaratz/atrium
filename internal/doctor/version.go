// Package doctor probes installed agent CLIs and reports whether their versions
// have drifted past the heuristic ceiling Atrium's pane classification was
// verified against. It is the only place that shells out to agent binaries; the
// agent package itself stays pure.
package doctor

import "regexp"

// semver3Re matches a full MAJOR.MINOR.PATCH token; semver2Re a bare MAJOR.MINOR.
var (
	semver3Re = regexp.MustCompile(`\d+\.\d+\.\d+`)
	semver2Re = regexp.MustCompile(`\d+\.\d+`)
)

// parseVersion extracts the version token from --version output. It prefers the
// first full MAJOR.MINOR.PATCH token, falling back to a bare MAJOR.MINOR only
// when no three-component token exists. Preferring the full form skips unrelated
// two-component numbers a CLI may print first (e.g. a "Go 1.21" / "node 18.0"
// toolchain banner) while still parsing agents that report only MAJOR.MINOR
// (codex). The token is re-validated as semver by the caller.
func parseVersion(out string) (string, bool) {
	if m := semver3Re.FindString(out); m != "" {
		return m, true
	}
	if m := semver2Re.FindString(out); m != "" {
		return m, true
	}
	return "", false
}
