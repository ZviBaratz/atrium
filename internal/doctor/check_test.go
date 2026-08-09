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

func TestCheckClassifies(t *testing.T) {
	r := fakeRunner{
		out: map[string]string{
			"claude": "2.2.0 (Claude Code)\n", // past the pin, minor -> drifted
			"gemini": "0.27.4\n",              // verified 0.27, minor -> ok
			"codex":  "0.148.0\n",             // past the 0.147.0 pin, minor -> drifted
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
