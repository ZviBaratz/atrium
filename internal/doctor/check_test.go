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
		return "", e
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
			"codex":  "0.12.0\n",              // unversioned adapter -> unknown
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
	if s := statusFor(got, agent.KeyCodex); s != StatusUnknown {
		t.Errorf("codex status = %v, want StatusUnknown (unversioned)", s)
	}
	if s := statusFor(got, agent.KeyAider); s != StatusNotInstalled {
		t.Errorf("aider status = %v, want StatusNotInstalled", s)
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
// exists but its output is unavailable, so the result is StatusUnknown, not
// StatusNotInstalled.
func TestCheck_NonInstalledError_IsUnknown(t *testing.T) {
	r := fakeRunner{
		err: map[string]error{
			"claude": errors.New("exec: signal: killed"),
		},
	}
	got := Check(context.Background(), agent.Adapters(), r)
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
