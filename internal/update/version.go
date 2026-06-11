// Package update implements Atrium's self-update: a cached check of GitHub
// Releases plus a checksum-validated atomic binary swap (via go-selfupdate).
// The swap never disturbs running processes — they hold the old inode — so an
// installed update takes effect on the next launch.
package update

import (
	"strings"

	"github.com/Masterminds/semver/v3"
)

// IsUpdatableVersion reports whether the running build can self-update: only a
// clean release version (e.g. "0.6.0") qualifies. "dev" (unstamped builds) and
// git-describe strings ("0.6.0-5-gabc123") are inert — they have no
// corresponding release asset, and a dev build usually outpaces the latest tag.
// The strict-semver parse is the real gate: the justfile's `git describe
// --always` fallback stamps a bare commit SHA in a tagless clone, and anything
// that isn't X.Y.Z would panic in the library's semver comparison (or, for an
// all-digit SHA, silently compare as an enormous version).
func IsUpdatableVersion(v string) bool {
	if v == "" || v == "dev" || strings.Contains(v, "-") || strings.Contains(v, "+") {
		return false
	}
	_, err := semver.StrictNewVersion(v)
	return err == nil
}

// isNewer reports whether candidate is a strictly newer semver than current.
// Unparseable versions are never newer, so bad data can't trigger an update
// prompt or an auto-install.
func isNewer(candidate, current string) bool {
	cand, err := semver.NewVersion(candidate)
	if err != nil {
		return false
	}
	cur, err := semver.NewVersion(current)
	if err != nil {
		return false
	}
	return cand.GreaterThan(cur)
}
