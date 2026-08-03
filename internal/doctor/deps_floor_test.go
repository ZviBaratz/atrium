package doctor

import (
	"context"
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/session/tmux"
)

// tmuxSpec is the real tmux spec with an overridable floor, so the same version table
// can be run against the production floor and against a floor that cannot refuse
// anything (the negative control below).
func tmuxSpec(minVersion string) depSpec {
	return depSpec{
		name: "tmux", bin: "tmux", versionArg: "-V", kind: DepRequired,
		minVersion: minVersion,
		parse:      tmux.ParseVersion,
	}
}

// floorCases is one table driven twice: once against tmux.MinVersion, where the
// below-floor rows must be refused, and once against a floor no version can be below,
// where every parseable row must come back DepOK.
var floorCases = []struct {
	out     string
	version string
	state   DepState // against the real floor
	why     string
}{
	{"tmux 1.8\n", "1.8", DepTooOld, "RHEL/CentOS 7 — the case atrium#582 opens with"},
	{"tmux 2.7\n", "2.7", DepTooOld, ""},
	{"tmux 3.0\n", "3.0", DepTooOld, ""},
	{"tmux 3.1\n", "3.1", DepTooOld, "the version the issue body wrongly declared sufficient"},
	{"tmux 3.1c\n", "3.1", DepTooOld, "the newest tmux without new-session -e (Debian 11)"},
	{"tmux 3.2\n", "3.2", DepOK, "exactly at the floor"},
	{"tmux 3.2a\n", "3.2", DepOK, "Ubuntu 22.04"},
	{"tmux next-3.4\n", "3.4", DepOK, ""},
	{"tmux 3.6\n", "3.6", DepOK, ""},
	// Not a version we can reason about, so not evidence of an unusable binary.
	{"tmux openbsd-7.4\n", "", DepPresentUnknown, "an OS release; must not be read as 7.4 and clear the floor"},
}

func checkTmuxOnly(t *testing.T, spec depSpec, out string) DepResult {
	t.Helper()
	r := fakeDepRunner{out: map[string]string{"tmux": out}}
	return stateFor(checkDeps(context.Background(), []depSpec{spec}, r, "linux", nil), "tmux")
}

// The gate fires. Without the comparison in checkDeps every row here reports DepOK,
// which is exactly the bug: `atrium doctor` printing "tmux 1.8 ok".
func TestCheckDeps_VersionFloor(t *testing.T) {
	for _, c := range floorCases {
		got := checkTmuxOnly(t, tmuxSpec(tmux.MinVersion), c.out)
		if got.State != c.state {
			t.Errorf("checkDeps(%q).State = %v, want %v — %s", c.out, got.State, c.state, c.why)
		}
		if got.Version != c.version {
			t.Errorf("checkDeps(%q).Version = %q, want %q", c.out, got.Version, c.version)
		}
		wantUnmet := c.state == DepTooOld
		if unmet := RequiredUnmet([]DepResult{got}); unmet != wantUnmet {
			t.Errorf("RequiredUnmet after %q = %v, want %v", c.out, unmet, wantUnmet)
		}
		if c.state == DepTooOld && !strings.Contains(got.Hint, tmux.MinVersion) {
			t.Errorf("too-old hint for %q = %q, want it to name the required version %s", c.out, got.Hint, tmux.MinVersion)
		}
	}
}

// NEGATIVE CONTROL. The identical table, with a floor nothing can be below, must leave
// every parseable version at DepOK. A gate that also "fires" when the gate is absent
// proves nothing about the gate.
//
// The floor is a real "0.0" rather than "": "" exercises the skip branch, while "0.0"
// exercises the comparison itself. Together with the table above this kills both an
// unconditional downgrade and an inverted comparison — under `>` instead of `<`, every
// row here would flip to DepTooOld.
func TestCheckDeps_VersionFloor_NegativeControl(t *testing.T) {
	for _, c := range floorCases {
		want := DepOK
		if c.state == DepPresentUnknown {
			want = DepPresentUnknown // unparseable regardless of the floor
		}
		got := checkTmuxOnly(t, tmuxSpec("0.0"), c.out)
		if got.State != want {
			t.Errorf("with floor 0.0, checkDeps(%q).State = %v, want %v", c.out, got.State, want)
		}
	}
}

// A spec with no floor must never be downgraded — git and gh rely on this.
func TestCheckDeps_NoFloorNeverTooOld(t *testing.T) {
	got := checkTmuxOnly(t, tmuxSpec(""), "tmux 1.8\n")
	if got.State != DepOK {
		t.Fatalf("with no floor, tmux 1.8 = %v, want DepOK", got.State)
	}
}

// Fail open on a malformed floor. This branch is otherwise unreachable — parseVersion
// only ever emits digits-and-dots, which semver always accepts — so without this test
// the `err == nil` guard is untested, and dropping it would make a typo'd floor refuse
// every user's tmux.
func TestCheckDeps_MalformedFloorFailsOpen(t *testing.T) {
	got := checkTmuxOnly(t, tmuxSpec("not-a-version"), "tmux 3.6\n")
	if got.State != DepOK {
		t.Fatalf("with a malformed floor, tmux 3.6 = %v, want DepOK (a bad constant must not block anyone)", got.State)
	}
}

// Every DepState needs its own case in label(). The switch ends in a default, so a state
// added without one renders as another state's text and nothing complains — there is no
// exhaustive linter enabled. This is the guard for that whole class, not just DepTooOld.
func TestDepStateLabels_AllStatesExplicit(t *testing.T) {
	all := []DepState{DepOK, DepMissing, DepPresentUnauth, DepPresentUnknown, DepTooOld}

	seen := map[string]DepState{}
	for _, s := range all {
		got := s.label()
		if got == unknownStateLabel {
			t.Errorf("DepState(%d) has no explicit case in label(); it falls through to %q", s, unknownStateLabel)
		}
		if prev, dup := seen[got]; dup {
			t.Errorf("DepState(%d) and DepState(%d) both render as %q; states must be distinguishable", prev, s, got)
		}
		seen[got] = s
	}

	// Pins the enumeration itself: a new state appended to the iota is invisible to the
	// loop above unless someone adds it here too, so make the omission loud.
	if int(DepTooOld) != len(all)-1 {
		t.Fatalf("DepTooOld = %d but the table has %d states; add the new DepState to `all`", DepTooOld, len(all))
	}
}

// installHint must never advise installing a binary that is already present. The
// previous version of this test hand-enumerated two states, so DepTooOld — which falls
// through to the install branch — was invisible to it. Table-driven over every present
// state so the next one is covered without anyone remembering to add it.
func TestInstallHint_PresentStatesDoNotAdviseReinstall(t *testing.T) {
	presentStates := []DepState{DepPresentUnauth, DepPresentUnknown, DepTooOld}
	gh := depSpec{name: "gh", bin: "gh"}
	tmuxDep := tmuxSpec(tmux.MinVersion)

	for _, goos := range []string{"darwin", "linux", "windows"} {
		for _, state := range presentStates {
			spec := tmuxDep
			if state == DepPresentUnauth {
				spec = gh // only gh has an auth sub-check
			}
			hint := installHint(goos, spec, state)
			if strings.Contains(hint, "install:") {
				t.Errorf("installHint(%q, %s, %v) = %q, which advises reinstalling a present binary",
					goos, spec.bin, state, hint)
			}
		}
	}

	// The two present states that carry a fix must still say what it is.
	if unauth := installHint("darwin", gh, DepPresentUnauth); !strings.Contains(unauth, "gh auth login") {
		t.Errorf("unauthenticated gh hint = %q, want it to point at gh auth login", unauth)
	}
	if unknown := installHint("linux", tmuxDep, DepPresentUnknown); unknown != "" {
		t.Errorf("present-but-unknown hint = %q, want empty (nothing to fix)", unknown)
	}
}

// The too-old hint must interpolate the spec's floor rather than hardcode it. Asserted
// with a synthetic floor no constant in the tree carries, so a literal "3.2" fails here.
func TestInstallHint_TooOldNamesTheSpecFloor(t *testing.T) {
	spec := tmuxSpec("9.9")
	for _, goos := range []string{"darwin", "linux", "windows"} {
		hint := installHint(goos, spec, DepTooOld)
		if !strings.Contains(hint, "9.9") {
			t.Errorf("installHint(%q, DepTooOld) = %q, want it to name the spec's floor 9.9", goos, hint)
		}
	}
}

// The floor doctor enforces has to be the one session/tmux declares, or doctor blesses a
// tmux that cannot start a session.
func TestCoreDepsTmuxFloorMatchesTmuxPackage(t *testing.T) {
	for _, s := range coreDeps {
		if s.bin != "tmux" {
			continue
		}
		if s.minVersion != tmux.MinVersion {
			t.Fatalf("coreDeps tmux floor = %q, want tmux.MinVersion (%q)", s.minVersion, tmux.MinVersion)
		}
		if s.parse == nil {
			t.Fatal("coreDeps tmux has no parse override; the generic parser reads `tmux openbsd-7.4` as 7.4")
		}
		return
	}
	t.Fatal("coreDeps has no tmux entry; this guard would pass vacuously")
}
