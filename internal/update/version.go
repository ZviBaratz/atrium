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
func IsUpdatableVersion(v string) bool {
	return v != "" && v != "dev" && !strings.Contains(v, "-") && !strings.Contains(v, "+")
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
