package doctor

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/ZviBaratz/atrium/session/agent"
)

// fakeRunner returns canned --version output (or error) per binary.
type fakeRunner struct {
	out map[string]string
	err map[string]error
}

func (f fakeRunner) version(_ context.Context, bin string) (string, error) {
	if e, ok := f.err[bin]; ok {
		// Any canned output is returned alongside the error, so a fixture can pair
		// a parseable version with a failed probe. That pairing is what separates
		// Check's err != nil branch from its default: both yield StatusUnknown for
		// empty output, so a test using "" cannot tell which branch ran.
		return f.out[bin], e
	}
	if o, ok := f.out[bin]; ok {
		return o, nil
	}
	return "", fmt.Errorf("%w: %s", ErrNotInstalled, bin)
}

func statusFor(results []Result, k agent.Key) Status {
	for _, r := range results {
		if r.Key == k {
			return r.Status
		}
	}
	return StatusNotInstalled
}

// geminiWithinPin is the installed version TestCheckClassifies feeds for gemini: newer than
// the registry's pin, but inside the same minor, so Check's not-drifted branch is the one that
// runs. requireWithinPin holds it there.
//
// It is a 0.55 patch because the pin is 0.55.1, NOT because that is what anyone has installed.
// This value has now moved three times and every move was forced by this guard rather than
// noticed by hand: 0.27.4 originally, 0.55.9 while #713 briefly carried the pin at 0.55.1,
// back to 0.27.4 when #713 reverted it, and 0.55.9 again now that #736 has driven all four
// gemini surfaces and moved the pin for real. Each time the alternative was a row that stayed
// green while silently testing the older-than branch instead.
const geminiWithinPin = "0.55.9"

// requireWithinPin fails unless installed is at-or-above the adapter's pin AND in the same
// minor — the only shape that exercises "newer, but not by enough to be drift".
//
// It exists because StatusOK is not evidence of that branch. driftExceeds returns
// Compare(...) > 0, so installed OLDER than verified is also not drift, also StatusOK, and
// also green. That is not hypothetical: this row read "0.27.4" against a 0.27 pin until #713
// briefly moved the pin to 0.55.1, at which point it kept passing while testing the
// older-than branch instead — silently, because the fixture is a literal and Check runs
// against the LIVE registry. It caught the move back, too: when #713 restored the pin to 0.27
// this failed by name against the 0.55.9 fixture the bump had introduced. Any pin change does
// it in one direction or the other, which is the point — the fixture is checked against the
// pin rather than against a memory of it. When this fails, move geminiWithinPin into the
// pin's minor.
func requireWithinPin(t *testing.T, key agent.Key, installed string) {
	t.Helper()

	var a *agent.Adapter
	for _, cand := range agent.Adapters() {
		if cand.Key == key {
			a = cand
		}
	}
	if a == nil {
		t.Fatalf("no %s adapter in the registry", key)
	}

	drifted, err := driftExceeds(installed, a.VerifiedVersion, a.DriftGranularity)
	if err != nil {
		t.Fatalf("driftExceeds(%q, %q): %v", installed, a.VerifiedVersion, err)
	}
	if drifted {
		t.Fatalf("%s fixture %q is drift against pin %q; it must be inside the pinned %v",
			key, installed, a.VerifiedVersion, a.DriftGranularity)
	}

	older, err := belowFloor(installed, a.VerifiedVersion)
	if err != nil {
		t.Fatalf("belowFloor(%q, %q): %v", installed, a.VerifiedVersion, err)
	}
	if older {
		t.Fatalf("%s fixture %q is OLDER than pin %q, so StatusOK here means "+
			"\"installed < verified\", not \"newer within the granularity\" — the branch this "+
			"row exists to cover is no longer being run", key, installed, a.VerifiedVersion)
	}
}

func TestCheckClassifies(t *testing.T) {
	requireWithinPin(t, agent.KeyGemini, geminiWithinPin)

	r := fakeRunner{
		out: map[string]string{
			"claude": "2.2.0 (Claude Code)\n", // past the pin, minor -> drifted
			// gemini is the only row here testing the NOT-drifted direction, and it has to
			// stay inside the pinned MINOR to do it — see requireWithinPin, which is what
			// actually holds that rather than this sentence.
			"gemini": geminiWithinPin + "\n",
			"codex":  "0.148.0\n", // past the 0.147.0 pin, minor -> drifted
		},
		err: map[string]error{},
	}
	got := Check(context.Background(), agent.Adapters(), r)

	if s := statusFor(got, agent.KeyClaude); s != StatusDrifted {
		t.Errorf("claude status = %v, want StatusDrifted", s)
	}
	if s := statusFor(got, agent.KeyGemini); s != StatusOK {
		t.Errorf("gemini status = %v, want StatusOK", s)
	}
	if s := statusFor(got, agent.KeyCodex); s != StatusDrifted {
		t.Errorf("codex status = %v, want StatusDrifted", s)
	}
	if s := statusFor(got, agent.KeyAider); s != StatusNotInstalled {
		t.Errorf("aider status = %v, want StatusNotInstalled", s)
	}
}

// The unversioned path, kept on a synthetic adapter rather than on whichever registry
// entry happens to be unpinned. It used to ride on codex, which meant #510's drive —
// pinning codex at the version it drove — silently deleted this coverage instead of
// failing. An adapter with no VerifiedVersion has nothing to compare against, so it
// reports Unknown while still surfacing the version it found.
func TestCheckUnversionedAdapterIsUnknown(t *testing.T) {
	unpinned := &agent.Adapter{Key: "claude", DisplayName: "Unpinned"}
	r := fakeRunner{out: map[string]string{"claude": "9.9.9\n"}}

	got := Check(context.Background(), []*agent.Adapter{unpinned}, r)

	if len(got) != 1 {
		t.Fatalf("Check returned %d results, want 1", len(got))
	}
	if got[0].Status != StatusUnknown {
		t.Errorf("status = %v, want StatusUnknown (no VerifiedVersion to compare against)", got[0].Status)
	}
	if got[0].Installed != "9.9.9" {
		t.Errorf("installed = %q, want %q reported even when unversioned", got[0].Installed, "9.9.9")
	}
}

func TestCheckUnparseableVersionIsUnknown(t *testing.T) {
	r := fakeRunner{out: map[string]string{"claude": "weird build\n"}}
	got := Check(context.Background(), agent.Adapters(), r)
	if s := statusFor(got, agent.KeyClaude); s != StatusUnknown {
		t.Errorf("claude status = %v, want StatusUnknown", s)
	}
}

func TestDriftedFilter(t *testing.T) {
	in := []Result{
		{Key: agent.KeyClaude, Status: StatusDrifted},
		{Key: agent.KeyGemini, Status: StatusOK},
	}
	out := Drifted(in)
	if len(out) != 1 || out[0].Key != agent.KeyClaude {
		t.Errorf("Drifted() = %+v, want only claude", out)
	}
}

// TestCheck_NonInstalledError_IsUnknown guards the case where the runner returns
// a non-ErrNotInstalled error (e.g. the binary is on PATH but exec fails with
// a signal or timeout). That path is distinct from ErrNotInstalled — the binary
// exists but its output cannot be trusted, so the result is StatusUnknown, not
// StatusNotInstalled.
//
// The fixture pairs the error with output that would otherwise classify as
// StatusOK (it equals the adapter's pin). That pairing is load-bearing: a probe
// error with empty output also reaches StatusUnknown through classify's
// unparseable-output path, so a test using "" would still pass with the err != nil
// branch deleted. Here, deleting it yields StatusOK and fails the test.
func TestCheck_NonInstalledError_IsUnknown(t *testing.T) {
	a := &agent.Adapter{
		Key: agent.KeyClaude, DisplayName: "Claude Code",
		VerifiedVersion: "2.1.0",
	}
	r := fakeRunner{
		out: map[string]string{"claude": "2.1.0\n"},
		err: map[string]error{"claude": errors.New("exec: signal: killed")},
	}
	got := Check(context.Background(), []*agent.Adapter{a}, r)
	if s := statusFor(got, agent.KeyClaude); s != StatusUnknown {
		t.Errorf("claude status = %v, want StatusUnknown for non-ErrNotInstalled error", s)
	}
}

// TestCheck_BadVerifiedVersionIsUnknown guards the classify() → driftExceeds error
// path: if an adapter carries a non-semver VerifiedVersion (guarded in the real
// registry by TestRegistryVerifiedVersionsParse), the result is StatusUnknown
// rather than panicking. The installed output is valid so parseVersion succeeds
// and driftExceeds is reached; it is driftExceeds that errors on the bad verified
// string, and classify must propagate that as StatusUnknown.
func TestCheck_BadVerifiedVersionIsUnknown(t *testing.T) {
	bad := &agent.Adapter{
		Key: agent.KeyClaude, DisplayName: "Claude Code",
		VerifiedVersion: "not-semver",
	}
	r := fakeRunner{out: map[string]string{"claude": "2.1.0\n"}}
	got := Check(context.Background(), []*agent.Adapter{bad}, r)
	if s := statusFor(got, agent.KeyClaude); s != StatusUnknown {
		t.Errorf("claude status = %v, want StatusUnknown when VerifiedVersion is invalid semver", s)
	}
}
