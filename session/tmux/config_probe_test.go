package tmux

import (
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/internal/testutil"
)

// TestProbeSocketNameIsUniquePerInvocation pins the property every other edit at this
// site rests on: no two config-parse probes can ever name the same socket.
//
// It is asserted here rather than through validateConfig because the name is what has
// to be provably unique, and a pure function is the only way to say so without racing
// two real tmux servers. Each assertion below kills a different way of getting the
// name wrong:
//
//   - Two calls differ — drop the counter and both Inits in one process collide again.
//     That is reachable: Init is documented safe off the update thread and fires on
//     every live session_context_bar / tmux_config_override / theme change.
//   - The PID is present — drop it and the TUI's probe collides with the daemon's,
//     which are separate processes and so share no counter.
//   - The brand prefix is present — the socket has to stay recognisably Atrium's for
//     the stale-socket report and for a human reading `ls /tmp/tmux-<uid>`.
//
// A collision is not cosmetic. Two Inits sharing a probe server means the first
// teardown kills it under the second, whose source-file then fails "no server
// running"; Init records that as a parse error and every later session launches with
// no custom titles, mouse, clipboard or status bar.
func TestProbeSocketNameIsUniquePerInvocation(t *testing.T) {
	first, second := probeSocketName(), probeSocketName()

	if first == second {
		t.Fatalf("probeSocketName() returned %q twice: two Inits in one process would share a "+
			"probe server, and the first teardown would kill it under the second", first)
	}

	prefix := socketName() + "-precheck-"
	pid := strconv.Itoa(os.Getpid())
	for _, got := range []string{first, second} {
		if !strings.HasPrefix(got, prefix) {
			t.Errorf("probeSocketName() = %q, want the %q prefix", got, prefix)
		}
		// Checked as a hyphen-delimited field, not a substring: a bare strings.Contains
		// would pass on a name that merely happened to contain the digits.
		if !strings.Contains(got, "-"+pid+"-") {
			t.Errorf("probeSocketName() = %q, want it to carry this process's pid (%s): without "+
				"one, the TUI's probe and the daemon's collide — they share no counter", got, pid)
		}
	}
}

// TestValidateConfigLeavesNoProbeSocket is the class (a) guard. tmux never unlinks a
// socket when its server dies, so before this change every validateConfig left a file
// behind — and since the name was fixed, the one in production sat at
// /tmp/tmux-<uid>/atrium-precheck, next to the live socket, for as long as the machine
// was up (#547).
//
// The assertion is over the whole sandbox root rather than one expected path, so it
// catches a probe that leaked to somewhere unexpected as readily as one that was never
// cleaned up. RequireTmux supplies the isolation: this asserts about a directory only
// this test binary writes to, and the package TestMain already installed TMUX_TMPDIR
// there — minting a second root here would test a path production never takes.
func TestValidateConfigLeavesNoProbeSocket(t *testing.T) {
	testutil.RequireTmux(t)
	root := testutil.TmuxRoot(t)

	path := filepath.Join(t.TempDir(), "probe.conf")
	if err := os.WriteFile(path, []byte("set -g mouse on\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if err := validateConfig(path); err != nil {
		t.Fatalf("validateConfig rejected a valid config: %v", err)
	}

	// Negative control, and the reason the leak assertion below is not vacuous.
	// validateConfig is best-effort: it returns nil both when the probe ran and found
	// nothing wrong *and* when the probe server never started at all — and a probe that
	// never started binds no socket, so "nothing leaked" would hold for the wrong
	// reason. A config tmux must reject can only be rejected by a probe that really ran.
	bad := filepath.Join(t.TempDir(), "bad.conf")
	if err := os.WriteFile(bad, []byte("this-is-not-a-tmux-command\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if err := validateConfig(bad); err == nil {
		t.Fatal("validateConfig accepted a config tmux cannot parse: the probe server never " +
			"ran, so the socket assertion below would pass without exercising the teardown")
	}

	if leaked := socketsMatching(t, root, "-precheck"); len(leaked) > 0 {
		t.Fatalf("validateConfig left %v under the socket root %q: tmux does not unlink a "+
			"probe socket when its server dies, so the teardown has to (#547 class (a))",
			leaked, root)
	}
}

// socketsMatching returns every entry under root whose name contains substr.
func socketsMatching(t *testing.T, root, substr string) []string {
	t.Helper()
	var found []string
	if err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.Contains(d.Name(), substr) {
			found = append(found, path)
		}
		return nil
	}); err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return found
}
