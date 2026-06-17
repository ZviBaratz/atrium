package doctor

import (
	"context"
	"errors"
	"os/exec"
	"time"

	"github.com/ZviBaratz/atrium/session/agent"
)

// ProbeTimeout bounds the agent `--version` probes so a wedged binary can't pin
// either `atrium doctor` or the startup drift check. Shared by both callers so
// the manual command and the background probe stay in lockstep.
const ProbeTimeout = 10 * time.Second

// Status is the drift classification of one agent CLI.
type Status int

const (
	// StatusOK means the agent is installed and within the verified ceiling.
	StatusOK Status = iota
	// StatusDrifted means the installed version is past the verified ceiling.
	StatusDrifted
	// StatusUnknown means the agent is installed but its version is unparseable,
	// or the adapter is unversioned (no VerifiedVersion to compare against).
	StatusUnknown
	// StatusNotInstalled means the binary is not on PATH.
	StatusNotInstalled
)

// Result is the drift report for one agent.
type Result struct {
	Key       agent.Key
	Name      string
	Installed string // parsed installed version, "" when not installed/unparseable
	Verified  string // adapter's VerifiedVersion, "" when unversioned
	Status    Status
}

// ErrNotInstalled is returned (wrapped) by a Runner when the binary is absent.
var ErrNotInstalled = errors.New("not installed")

// Runner probes an agent binary's reported version. The method is unexported so
// only in-package fakes (tests) can implement it.
type Runner interface {
	version(ctx context.Context, bin string) (string, error)
}

// execRunner is the production Runner: it shells out to `<bin> --version`.
type execRunner struct{}

func (execRunner) version(ctx context.Context, bin string) (string, error) {
	path, err := exec.LookPath(bin)
	if err != nil {
		return "", ErrNotInstalled
	}
	out, err := exec.CommandContext(ctx, path, "--version").Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// Check probes each adapter's binary (by its Key) and classifies drift. It
// never errors: every adapter yields a Result whose Status carries the outcome.
func Check(ctx context.Context, adapters []*agent.Adapter, r Runner) []Result {
	results := make([]Result, 0, len(adapters))
	for _, a := range adapters {
		res := Result{Key: a.Key, Name: a.DisplayName, Verified: a.VerifiedVersion}
		out, err := r.version(ctx, string(a.Key))
		switch {
		case errors.Is(err, ErrNotInstalled):
			res.Status = StatusNotInstalled
		case err != nil:
			res.Status = StatusUnknown
		default:
			res.Status, res.Installed = classify(out, a)
		}
		results = append(results, res)
	}
	return results
}

// classify turns a successful --version capture into a Status and the parsed
// installed version for one adapter. The version is "" only when the output is
// unparseable (StatusUnknown); an unversioned adapter still reports its parsed
// version so `atrium doctor` can show it.
func classify(out string, a *agent.Adapter) (Status, string) {
	v, ok := parseVersion(out)
	if !ok {
		return StatusUnknown, ""
	}
	if a.VerifiedVersion == "" {
		return StatusUnknown, v
	}
	drift, err := driftExceeds(v, a.VerifiedVersion, a.DriftGranularity)
	if err != nil {
		return StatusUnknown, v
	}
	if drift {
		return StatusDrifted, v
	}
	return StatusOK, v
}

// CheckInstalled probes the recognized adapters against the real environment.
func CheckInstalled(ctx context.Context) []Result {
	return Check(ctx, agent.Adapters(), execRunner{})
}

// Drifted returns the subset of results classified as drifted.
func Drifted(results []Result) []Result {
	var out []Result
	for _, r := range results {
		if r.Status == StatusDrifted {
			out = append(out, r)
		}
	}
	return out
}
