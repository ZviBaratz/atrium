package doctor

import (
	"testing"

	"github.com/Masterminds/semver/v3"

	"github.com/ZviBaratz/atrium/session/agent"
)

// TestRegistryVerifiedVersionsParse guards the registry against a typo'd ceiling.
// A non-empty VerifiedVersion that isn't valid semver makes driftExceeds error,
// which classify() turns into StatusUnknown — silently disabling drift detection
// for that agent, the exact silent failure this guard exists to prevent. Keep
// this in lockstep with any VerifiedVersion edit in session/agent/registry.go.
func TestRegistryVerifiedVersionsParse(t *testing.T) {
	for _, a := range agent.Adapters() {
		if a.VerifiedVersion == "" {
			continue // unversioned adapters never compare; nothing to validate
		}
		if _, err := semver.NewVersion(a.VerifiedVersion); err != nil {
			t.Errorf("%s VerifiedVersion %q is not valid semver: %v", a.Key, a.VerifiedVersion, err)
		}
	}
}

func TestDriftExceeds(t *testing.T) {
	cases := []struct {
		name      string
		installed string
		verified  string
		gran      agent.Granularity
		want      bool
	}{
		{"claude patch newer drifts", "2.1.179", "2.1.170", agent.GranularityPatch, true},
		{"claude patch equal no drift", "2.1.170", "2.1.170", agent.GranularityPatch, false},
		{"claude patch older no drift", "2.1.165", "2.1.170", agent.GranularityPatch, false},
		{"gemini minor newer drifts", "0.45.1", "0.27", agent.GranularityMinor, true},
		{"gemini patch within minor no drift", "0.27.5", "0.27.0", agent.GranularityMinor, false},
		{"major gran ignores minor bump", "1.4.0", "1.2.0", agent.GranularityMajor, false},
		{"major gran catches major bump", "2.0.0", "1.9.0", agent.GranularityMajor, true},
	}
	for _, c := range cases {
		got, err := driftExceeds(c.installed, c.verified, c.gran)
		if err != nil {
			t.Fatalf("%s: unexpected error: %v", c.name, err)
		}
		if got != c.want {
			t.Errorf("%s: driftExceeds(%q,%q) = %v, want %v", c.name, c.installed, c.verified, got, c.want)
		}
	}
}

func TestDriftExceedsBadVersionErrors(t *testing.T) {
	if _, err := driftExceeds("not-a-version", "2.1.0", agent.GranularityPatch); err == nil {
		t.Error("expected error for unparseable installed version")
	}
}
