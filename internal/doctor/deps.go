package doctor

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/ZviBaratz/atrium/session/tmux"
)

// DepKind distinguishes a hard requirement from an optional dependency.
type DepKind int

const (
	// DepRequired marks a dependency Atrium cannot run without (tmux, git). A
	// missing required dep makes `atrium doctor` exit nonzero.
	DepRequired DepKind = iota
	// DepOptional marks a dependency only some flows need (gh, for push/PR). Its
	// absence or unauthenticated state is reported but never fails doctor.
	DepOptional
)

// DepState is the outcome of one core-dependency probe.
type DepState int

const (
	// DepOK means the binary is on PATH (and, for gh, authenticated).
	DepOK DepState = iota
	// DepMissing means the binary is not on PATH.
	DepMissing
	// DepPresentUnauth means the binary is present but its auth check failed
	// (gh only). Reported, never blocking — auth is a push/PR-time concern.
	DepPresentUnauth
	// DepPresentUnknown means the binary is present but its version output was
	// unparseable. Still usable, so never blocking.
	DepPresentUnknown
	// DepTooOld means the binary is present and its version parsed, but is below the
	// spec's declared floor. As unusable as missing, so it exits nonzero like one.
	//
	// Appended last on purpose: DepOK is iota 0, which is the zero value tests get from
	// a DepResult lookup miss, so the order of the existing values is load-bearing.
	DepTooOld
)

// DepResult is the report for one core dependency.
type DepResult struct {
	Name    string // human/binary name shown in the table ("tmux", "git", "gh")
	Bin     string // binary probed on PATH
	Kind    DepKind
	State   DepState
	Version string // parsed version, "" when missing/unparseable
	Hint    string // remediation hint; empty for DepOK and for a present-but-unknown dep (nothing to fix)
}

// depSpec is a static core-dependency probe definition.
//
// Keyed literals below, not positional: the struct has grown twice and a positional
// literal makes every future field a repo-wide edit.
type depSpec struct {
	name       string
	bin        string
	versionArg string // tmux prints its version to -V; git/gh use --version
	kind       DepKind
	// minVersion is the oldest usable version; "" means the dep has no declared floor.
	minVersion string
	// parse overrides parseVersion for a binary whose -V grammar the generic parser
	// misreads. A field rather than a `bin == "tmux"` branch because the parser is a
	// property of the dependency, and this way it is unit-testable on its own.
	parse func(string) (string, bool)
}

// coreDeps is the fixed set of hard dependencies doctor probes, in display order.
var coreDeps = []depSpec{
	{
		name: "tmux", bin: "tmux", versionArg: "-V", kind: DepRequired,
		// The floor lives in session/tmux, the package whose argv creates it — see
		// tmux.MinVersion for the derivation. Reading the constant rather than repeating
		// "3.2" here is what keeps the number to one home.
		minVersion: tmux.MinVersion,
		// tmux reports "3.2a", "next-3.4" and (on OpenBSD) "openbsd-7.4"; the last would
		// read as version 7.4 through parseVersion and vacuously clear any floor.
		parse: tmux.ParseVersion,
	},
	{name: "git", bin: "git", versionArg: "--version", kind: DepRequired},
	{name: "gh", bin: "gh", versionArg: "--version", kind: DepOptional},
}

// depRunner probes a core-dep binary with its version flag. The method is
// unexported so only in-package fakes (tests) implement it, mirroring Runner.
type depRunner interface {
	probe(ctx context.Context, bin, versionArg string) (string, error)
}

// execDepRunner is the production depRunner: it shells out to `<bin> <versionArg>`.
type execDepRunner struct{}

func (execDepRunner) probe(ctx context.Context, bin, versionArg string) (string, error) {
	path, err := exec.LookPath(bin)
	if err != nil {
		return "", ErrNotInstalled // reuse the sentinel from check.go
	}
	out, err := exec.CommandContext(ctx, path, versionArg).Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// authChecker reports whether gh is authenticated. Injected so tests never shell
// to a real gh and so deps.go need not import session/git (which would pull a
// package edge and its network-timeout constants). Return nil when authenticated.
type authChecker func(ctx context.Context) error

// CheckDeps probes the core dependencies against the real environment. goos
// selects install hints (pass runtime.GOOS); auth runs the gh auth sub-check when
// gh is present (nil disables it, leaving gh at DepOK on mere presence).
func CheckDeps(ctx context.Context, goos string, auth authChecker) []DepResult {
	return checkDeps(ctx, coreDeps, execDepRunner{}, goos, auth)
}

// checkDeps is the testable core of CheckDeps: a fake depRunner, an explicit goos,
// and a stubbable auth checker keep it hermetic. It never errors — every spec
// yields one DepResult whose State carries the outcome.
func checkDeps(ctx context.Context, specs []depSpec, r depRunner, goos string, auth authChecker) []DepResult {
	results := make([]DepResult, 0, len(specs))
	for _, s := range specs {
		res := DepResult{Name: s.name, Bin: s.bin, Kind: s.kind}
		out, err := r.probe(ctx, s.bin, s.versionArg)
		switch {
		case errors.Is(err, ErrNotInstalled):
			res.State = DepMissing
		case err != nil:
			// Present but the version probe failed (odd output, timeout). Treat as
			// present-unknown: the binary resolved on PATH, so it is not missing.
			res.State = DepPresentUnknown
		default:
			parse := s.parse
			if parse == nil {
				parse = parseVersion
			}
			if v, ok := parse(out); ok {
				res.Version = v
				res.State = DepOK
			} else {
				res.State = DepPresentUnknown
			}
			// A parsed version below the spec's floor is as unusable as a missing binary.
			// Runs before the gh auth sub-check so a too-old binary reports its age rather
			// than an auth failure that is beside the point. A comparison error (only
			// reachable from a malformed minVersion constant) leaves the state alone: a
			// typo in the floor must not block every user.
			if res.State == DepOK && s.minVersion != "" {
				if below, err := belowFloor(res.Version, s.minVersion); err == nil && below {
					res.State = DepTooOld
				}
			}
			// gh's presence isn't enough — run the auth sub-check when supplied.
			if res.State == DepOK && s.bin == "gh" && auth != nil {
				if auth(ctx) != nil {
					res.State = DepPresentUnauth
				}
			}
		}
		if res.State != DepOK {
			res.Hint = installHint(goos, s, res.State)
		}
		results = append(results, res)
	}
	return results
}

// installHint returns the remediation string for a dependency in a non-OK state.
// It is state-aware so it never tells the user to reinstall a binary that is
// already present: an unauthenticated gh only needs `gh auth login`, and a
// present-but-unparseable-version binary has nothing to install (no hint). Only a
// genuinely missing binary gets an OS-appropriate install command. goos is
// injected (runtime.GOOS at the callsite) so hint selection is testable on any
// host, mirroring internal/actions.chooseOpener; gh's Linux install points at the
// docs rather than a wrong `apt install gh`.
func installHint(goos string, s depSpec, state DepState) string {
	switch state {
	case DepPresentUnauth:
		// gh is installed; only its auth sub-check failed.
		return "run: gh auth login"
	case DepPresentUnknown:
		// On PATH but the version was unreadable — nothing to install or fix.
		return ""
	case DepTooOld:
		// Present, so the fix is an upgrade. An install line would be wrong twice: it
		// advises reinstalling a binary that is already there, and on Linux the distro
		// package that produced the old version will produce it again.
		//
		// Three branches, matching the install ladder below rather than collapsing to
		// darwin/everything-else: "your distro package may be too old" is Linux-specific
		// advice, and a Windows user has no distro package to blame.
		switch goos {
		case "darwin":
			return fmt.Sprintf("upgrade: brew upgrade %s (needs %s or newer)", s.bin, s.minVersion)
		case "linux":
			return fmt.Sprintf("upgrade %s to %s or newer (your distro package may be too old; "+
				"see the %s project's install docs)", s.bin, s.minVersion, s.bin)
		default:
			return fmt.Sprintf("upgrade %s to %s or newer (see the %s project's install docs)",
				s.bin, s.minVersion, s.bin)
		}
	}
	if s.bin == "gh" {
		switch goos {
		case "darwin":
			return "install: brew install gh, then run: gh auth login"
		default:
			return "install: https://github.com/cli/cli#installation, then run: gh auth login"
		}
	}
	switch goos {
	case "darwin":
		return "install: brew install " + s.bin
	case "linux":
		return "install: sudo apt install " + s.bin
	default:
		return fmt.Sprintf("install: see the %s project's install docs", s.bin)
	}
}

// RequiredUnmet reports whether any required dependency is unusable — missing, or
// present but below its declared floor — so the doctor command can exit nonzero for
// scripts/CI. A too-old required binary is as unusable as an absent one, which is why
// this is one predicate rather than two: the rendered table above it already says which.
//
// An optional dep (gh) never trips it, including when merely unauthenticated. Neither
// does DepPresentUnknown, deliberately: an unreadable version is not evidence of an
// unusable binary.
//
// Named for what it tests. It was MissingRequired until the floor landed, at which point
// that name described only half of what it returned.
func RequiredUnmet(results []DepResult) bool {
	for _, r := range results {
		if r.Kind == DepRequired && (r.State == DepMissing || r.State == DepTooOld) {
			return true
		}
	}
	return false
}

// unknownStateLabel is what label() renders for a DepState it does not know. Every
// declared state has an explicit case, so this string reaching the UI means a new state
// was added without one — which TestDepStateLabels_AllStatesExplicit catches. It is
// deliberately distinct from any real label: a default that returned a plausible string
// is how a new state ships silently mislabelled.
const unknownStateLabel = "⚠ unrecognized state"

func (s DepState) label() string {
	switch s {
	case DepOK:
		return "ok"
	case DepMissing:
		return "not installed"
	case DepPresentUnauth:
		return "⚠ not authenticated"
	case DepPresentUnknown:
		return "⚠ unknown version"
	case DepTooOld:
		return "⚠ too old"
	default:
		return unknownStateLabel
	}
}

// RenderDeps formats core-dependency results as an aligned, newline-terminated
// table under a "Core dependencies:" header, parallel to Render's agent table. A
// hint line is appended only for a dependency that needs action.
func RenderDeps(results []DepResult) string {
	var b strings.Builder
	b.WriteString("Core dependencies:\n")
	for _, r := range results {
		version := r.Version
		if version == "" {
			version = "-"
		}
		fmt.Fprintf(&b, "  %-6s %-10s %s\n", r.Name, version, r.State.label())
		if r.Hint != "" {
			fmt.Fprintf(&b, "         → %s\n", r.Hint)
		}
	}
	return b.String()
}
