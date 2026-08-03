package doctor

import (
	"github.com/Masterminds/semver/v3"

	"github.com/ZviBaratz/atrium/session/agent"
)

// driftExceeds reports whether installed is newer than verified once both are
// truncated to the given granularity. Components below the granularity are
// zeroed before comparison, so e.g. a minor-granularity adapter ignores patch
// bumps. Returns an error if either string is not valid semver.
func driftExceeds(installed, verified string, g agent.Granularity) (bool, error) {
	iv, err := semver.NewVersion(installed)
	if err != nil {
		return false, err
	}
	vv, err := semver.NewVersion(verified)
	if err != nil {
		return false, err
	}
	return truncate(iv, g).Compare(truncate(vv, g)) > 0, nil
}

// belowFloor reports whether installed is strictly older than floor. Both are parsed
// leniently, so a bare "3.2" compares as 3.2.0 and a dependency exactly at its floor is
// not below it. Returns an error rather than a verdict if either string is not valid
// semver, so a caller can fail open on a malformed constant instead of failing every
// user; the only reachable source of that error is a typo in a spec's minVersion.
func belowFloor(installed, floor string) (bool, error) {
	iv, err := semver.NewVersion(installed)
	if err != nil {
		return false, err
	}
	fv, err := semver.NewVersion(floor)
	if err != nil {
		return false, err
	}
	return iv.LessThan(fv), nil
}

// truncate zeroes the version components below the given granularity.
func truncate(v *semver.Version, g agent.Granularity) *semver.Version {
	switch g {
	case agent.GranularityMajor:
		return semver.New(v.Major(), 0, 0, "", "")
	case agent.GranularityMinor:
		return semver.New(v.Major(), v.Minor(), 0, "", "")
	default:
		return semver.New(v.Major(), v.Minor(), v.Patch(), "", "")
	}
}
