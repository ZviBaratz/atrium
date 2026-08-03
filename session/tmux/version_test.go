package tmux

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/Masterminds/semver/v3"
)

// ParseVersion must read tmux's own -V grammar, which the generic doctor.parseVersion
// does not: the anchor is what stops "tmux openbsd-7.4" (OpenBSD's base tmux reports the
// OS release, not a tmux version) from being read as version 7.4 and clearing any floor.
func TestParseVersion(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
		why  string
	}{
		{"tmux 1.8\n", "1.8", true, "RHEL/CentOS 7's tmux"},
		{"tmux 2.7\n", "2.7", true, ""},
		{"tmux 3.0\n", "3.0", true, ""},
		{"tmux 3.1\n", "3.1", true, "the version atrium#582 wrongly named as the floor"},
		{"tmux 3.1c\n", "3.1", true, "a letter is a patch release of 3.1, which still lacks -e"},
		{"tmux 3.2\n", "3.2", true, "the floor itself"},
		{"tmux 3.2a\n", "3.2", true, "a patch release of 3.2, which has -e (Ubuntu 22.04 ships this)"},
		{"tmux next-3.4\n", "3.4", true, "an upstream prerelease of 3.4"},
		{"tmux 3.6\n", "3.6", true, ""},
		{"tmux 3.4 (Debian 3.4-1)\n", "3.4", true, "a distro suffix must not confuse the match"},
		{"tmux openbsd-7.4\n", "", false, "an OS release, not a tmux version"},
		{"tmux next\n", "", false, ""},
		{"tmux master\n", "", false, ""},
		{"", "", false, ""},
	}
	for _, c := range cases {
		got, ok := ParseVersion(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("ParseVersion(%q) = (%q, %v), want (%q, %v) — %s", c.in, got, ok, c.want, c.ok, c.why)
		}
	}
}

// MinVersion must be valid semver, or BelowMinimum errors on every call and the floor
// silently stops being enforced anywhere. Mirrors compare_test.go's registry guard.
func TestMinVersionIsValidSemver(t *testing.T) {
	if _, err := semver.NewVersion(MinVersion); err != nil {
		t.Fatalf("MinVersion = %q is not valid semver: %v", MinVersion, err)
	}
}

// The boundary is the whole point: 3.1c is the newest tmux without new-session -e and
// must be refused; 3.2 exactly at the floor must pass.
func TestBelowMinimum(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"1.8", true},
		{"2.7", true},
		{"3.0", true},
		{"3.1", true},
		{"3.2", false}, // at the floor, not below it
		{"3.4", false},
		{"3.6", false},
	}
	for _, c := range cases {
		got, err := BelowMinimum(c.in)
		if err != nil {
			t.Errorf("BelowMinimum(%q) errored: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("BelowMinimum(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

// A malformed floor must surface as an error, not as a verdict. Callers fail open on it;
// if this returned (true, nil) a typo in MinVersion would refuse every tmux.
func TestBelowFloorRejectsMalformedFloor(t *testing.T) {
	if _, err := belowFloor("3.6", "not-a-version"); err == nil {
		t.Fatal("belowFloor with a malformed floor returned no error; a typo'd floor would block every user")
	}
}

// resetVersionVerdict clears the probe-once state so each subtest starts from "no
// verdict". The production path deliberately probes once per process; tests need to
// exercise several outcomes.
func resetVersionVerdict(t *testing.T) {
	t.Helper()
	origProbe := versionProbe
	t.Cleanup(func() {
		versionProbe = origProbe
		versionProbeOnce = sync.Once{}
		tooOldVersion.Store(nil)
	})
	versionProbeOnce = sync.Once{}
	tooOldVersion.Store(nil)
}

func TestProbeVersionOnce(t *testing.T) {
	cases := []struct {
		name    string
		out     string
		err     error
		tooOld  bool
		wantVer string
	}{
		{name: "below the floor is refused", out: "tmux 1.8\n", tooOld: true, wantVer: "1.8"},
		{name: "one release below the floor is refused", out: "tmux 3.1c\n", tooOld: true, wantVer: "3.1"},
		{name: "exactly at the floor passes", out: "tmux 3.2\n"},
		{name: "a patch of the floor passes", out: "tmux 3.2a\n"},
		{name: "newer passes", out: "tmux 3.6\n"},
		// Fail-open cases: none of these is evidence of an unusable tmux.
		{name: "an unreadable version passes", out: "tmux next\n"},
		{name: "an OpenBSD base tmux passes", out: "tmux openbsd-7.4\n"},
		{name: "a failed probe passes", err: errors.New("boom")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resetVersionVerdict(t)
			versionProbe = func(context.Context) (string, error) { return c.out, c.err }
			probeVersionOnce()

			v := tooOldVersion.Load()
			if c.tooOld {
				if v == nil {
					t.Fatalf("probe of %q left no too-old verdict", c.out)
				}
				if *v != c.wantVer {
					t.Errorf("verdict = %q, want %q", *v, c.wantVer)
				}
				return
			}
			if v != nil {
				t.Fatalf("probe of %q (err %v) recorded too-old %q; this input must fail open", c.out, c.err, *v)
			}
		})
	}
}

// The exec must happen once per process: Init is reachable from the Bubble Tea update
// thread (app_layout.go's session_context_bar case), so a repeat probe would stall it.
func TestProbeVersionOnceRunsExactlyOnce(t *testing.T) {
	resetVersionVerdict(t)
	calls := 0
	versionProbe = func(context.Context) (string, error) {
		calls++
		return "tmux 3.6\n", nil
	}
	probeVersionOnce()
	probeVersionOnce()
	probeVersionOnce()
	if calls != 1 {
		t.Fatalf("versionProbe called %d times, want 1", calls)
	}
}
