package tmux

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Masterminds/semver/v3"
)

// MinVersion is the oldest tmux Atrium can start a session on.
//
// It is 3.2, and it is forced rather than chosen: `new-session -e` landed in 3.2, and
// Atrium passes -e on every session start: Session.start appends `-e ATRIUM=1 -e
// ATRIUM_SESSION=<name>` (atriumMarkerEnv) unconditionally — not only for routed accounts
// — so below 3.2 no session has ever started. Declaring the floor removes no working
// configuration.
//
// Derivation (tmux's own sources, which are stronger evidence than its CHANGES prose):
//
//	tmux 3.1  cmd-new-session.c:42  .args = { "Ac:dDEF:n:Ps:t:x:Xy:", 0, -1 }   no e:
//	tmux 3.2  cmd-new-session.c:42  .args = { "Ac:dDe:EF:f:n:Ps:t:x:Xy:", 0, -1 }   e:
//
// The CHANGES entry "Add -e flag for new-session to set environment variables, like the
// same flag for new-window" sits in the "CHANGES FROM 3.1c TO 3.2" block, not the 3.1
// one — atrium#582's body cites it as 3.1, which is one release too low.
//
// -e is the binding constraint. Everything else Atrium asks of tmux is older: the
// `{ … }` config blocks in atrium.conf.tmpl are 3.0, `send-keys -X`/-N and
// `bind-key -T` are 2.4, `pane-border-status` 2.3, `copy-mode -e` and `-t=` exact-match
// targets 2.1, user options `@name` and `capture-pane -p -e` 1.8. Every remaining conf
// directive and argv flag was verified present in the tmux 3.2 sources.
//
// Nothing here is a "verified on" claim: the floor says what Atrium needs, not which
// versions it has been exercised against.
const MinVersion = "3.2"

// ErrTooOld is the sentinel wrapped by every too-old error; callers match it with
// errors.Is. The message a user sees is built by ErrTooOldFor below.
var ErrTooOld = errors.New("tmux is too old")

// ErrTooOldFor builds the user-facing too-old error naming the version that was found.
// Atrium starts every session with `new-session -e`, which an older tmux rejects — and it
// rejects it inside the pty, so Atrium never sees tmux's stderr and the user would
// otherwise get a bare "timed out waiting for tmux session <name>" two seconds later
// (Session.start's timeout path), which names the tmux session but neither the cause nor
// the version — so nothing in it points at an upgrade.
//
// Deliberately long and multi-clause: handleError routes an error to the persistent info
// modal only when it does not fit the one-line toast (ui/err.go Fits is a pure width
// test), so a terse message would be silently truncated. Only the parsed version token is
// interpolated — never raw `tmux -V` output, whose width is unbounded.
//
// It names the restart because the verdict is probed once per process
// (probeVersionOnce): a user who upgrades tmux in another terminal and presses n again
// is refused with this same message, and without the restart clause there is nothing in
// it to explain why the fix it just recommended appeared not to work.
func ErrTooOldFor(found string) error {
	return fmt.Errorf("%w: found %s, but Atrium needs %s or newer. "+
		"Every session is started with `tmux new-session -e`, which older tmux rejects. "+
		"Upgrade tmux (macOS: brew upgrade tmux; Linux: your distro package may be too old, "+
		"see https://github.com/tmux/tmux/wiki/Installing), then restart Atrium — the "+
		"version is read once at launch. "+
		"Run `atrium doctor` to check dependencies", ErrTooOld, found, MinVersion)
}

// tmuxVersionRe is anchored on tmux's own `-V` grammar: "tmux 3.2", "tmux 3.2a" (a
// letter is a patch release of that minor), "tmux next-3.4" (a prerelease of that
// version). Anchoring is what makes it reject "tmux openbsd-7.4": OpenBSD's base tmux
// reports the OS release, not a tmux version, and the shared doctor.parseVersion would
// happily read that as 7.4 — printing a fabricated version in doctor's table and
// vacuously satisfying any >= 3.x floor.
var tmuxVersionRe = regexp.MustCompile(`^tmux (?:next-)?(\d+\.\d+)`)

// ParseVersion extracts a comparable MAJOR.MINOR token from `tmux -V` output, reporting
// false when the output is not a version Atrium can reason about ("tmux next",
// "tmux master", "tmux openbsd-7.4"). Truncating a trailing letter is conservative: 3.1c
// is a patch of 3.1 and genuinely lacks -e, while 3.2a is a patch of 3.2 and has it.
//
// It lives here rather than in internal/doctor because doctor imports this package for
// MinVersion; a parser there that this package also needed would be an import cycle.
// It is deliberately not a change to doctor.parseVersion, which is shared with agent
// drift classification.
func ParseVersion(out string) (string, bool) {
	m := tmuxVersionRe.FindStringSubmatch(strings.TrimSpace(out))
	if m == nil {
		return "", false
	}
	return m[1], true
}

// BelowMinimum reports whether a parsed version token is older than MinVersion. The
// error is returned rather than swallowed so callers can fail open on a malformed
// constant instead of blocking every user; mirrors internal/doctor's driftExceeds.
func BelowMinimum(parsed string) (bool, error) {
	return belowFloor(parsed, MinVersion)
}

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

// tooOldVersion holds the launch-time verdict: non-nil is the parsed version of a tmux
// confidently known to be below MinVersion. nil — the zero value — means "no verdict",
// which is the fail-open state covering an unparseable version, a failed probe, and the
// case where the probe has not run.
//
// Atomic for the same reason configOverridePath and managedConfigInvalid are (config.go):
// Init no longer runs only at startup, and Available is read from whichever goroutine is
// creating a session.
var tooOldVersion atomic.Pointer[string]

// versionProbeOnce keeps the `tmux -V` exec to one per process. Init is the only caller,
// and it can run on the Bubble Tea update thread (app_layout.go's session_context_bar /
// tmux_config_override case), so every call after the first must be free. A tmux upgraded
// underneath a running Atrium is not picked up until restart — acceptable, since the
// verdict only ever gates creating new sessions.
var versionProbeOnce sync.Once

// versionProbe is the `tmux -V` seam so tests inject output without a real binary.
var versionProbe = func(ctx context.Context) (string, error) {
	// Deliberately not routed through tmuxCommand: -V starts no server and touches no
	// socket, so the -L/-f flags that helper adds are meaningless here. CLAUDE.md's
	// "route tmux calls through tmuxCommand" rule is about which socket a command binds,
	// and this command binds none.
	out, err := exec.CommandContext(ctx, "tmux", "-V").Output()
	return string(out), err
}

// probeVersionOnce records whether the installed tmux is below MinVersion. It never
// returns an error: a version that cannot be read is not evidence of an unusable binary,
// so anything short of a confident below-floor answer leaves the verdict unset.
//
// The budget is deliberately 2s rather than tmuxOpTimeout (10s): the first call happens
// at process startup, but Init is also reachable from the update thread.
func probeVersionOnce() {
	versionProbeOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		out, err := versionProbe(ctx)
		if err != nil {
			return
		}
		v, ok := ParseVersion(out)
		if !ok {
			return
		}
		below, err := BelowMinimum(v)
		if err != nil || !below {
			return
		}
		tooOldVersion.Store(&v)
	})
}
