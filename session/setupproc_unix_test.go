//go:build !windows

package session

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ZviBaratz/atrium/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// runInBackground starts the script off-thread and returns a channel closed when the
// run returns, having first waited for the phase to prove the process is up. Every test
// here is about WHEN that return happens, so the wait is the assertion's subject and
// cannot be folded into it.
func runInBackground(t *testing.T, inst *Instance, dir string) <-chan struct{} {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		inst.RunSetupScript(dir)
	}()
	require.Eventually(t, func() bool { return inst.SetupPhase() != "" }, 5*time.Second, 5*time.Millisecond,
		"the script never started")
	return done
}

// requireReturns fails unless the run has returned within limit.
func requireReturns(t *testing.T, done <-chan struct{}, limit time.Duration, msg string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(limit):
		t.Fatal(msg)
	}
}

// A script that backgrounds anything must not hold the session. Neither Stdout nor
// Stderr is an *os.File, so os/exec plumbs both through pipes — and Cmd.Wait blocks
// until every holder of the write end closes it, not until the shell exits. A
// `dev-server &` or anything that daemonizes therefore pinned the row on "running setup
// script…" forever, with the agent never launched and no timeout to recover it.
func TestRunSetupScript_ABackgroundedChildDoesNotWedgeTheSession(t *testing.T) {
	dir := writeRepoScriptConfig(t, config.RepoScript{SetupScript: "sleep 45 & echo started"})
	inst := &Instance{ident: identity{title: "web"}, Path: dir}

	done := runInBackground(t, inst, dir)

	// Generously above setupWaitDelay and far below the sleep: what is under test is
	// that the wait is bounded by the delay rather than by the child's own lifetime.
	requireReturns(t, done, 15*time.Second, "a backgrounded child held the setup script open")
	assert.NoError(t, inst.SetupError(),
		"the script itself exited 0 — a process it deliberately left running is not a failure")
	assert.Empty(t, inst.SetupPhase())
}

// The abort has to end the script's whole process GROUP. `sh -c` forks for anything
// compound (`npm ci && npm run build`), so cancelling the context kills the shell and
// leaves the real work running — holding the output pipe, which is the force-quit hang
// AbortSetupScript exists to prevent, and outliving the app besides.
func TestAbortSetupScript_KillsTheWholeProcessGroup(t *testing.T) {
	// A grandchild that reports its OWN pid, which is the only thing that can tell the
	// two failure modes apart: the inner `sh -c` is a real fork+exec, so $$ inside it is
	// its pid and not the outer shell's. The trailing `echo` keeps a shell that
	// exec-optimizes a lone final command from becoming the grandchild instead of
	// forking one.
	dir := writeRepoScriptConfig(t, config.RepoScript{
		SetupScript: `sh -c 'echo $$ > child.pid; sleep 60'; echo done`,
	})
	inst := &Instance{ident: identity{title: "web"}, Path: dir}

	done := runInBackground(t, inst, dir)
	child := waitForPID(t, filepath.Join(dir, "child.pid"))

	inst.AbortSetupScript()

	requireReturns(t, done, 10*time.Second, "AbortSetupScript did not end the script")
	// Asserted on the grandchild rather than inferred from the return above, because
	// the two are independent: WaitDelay alone unblocks the wait — closing the pipes is
	// enough for that — while leaving the build running for its full duration, which is
	// the half that outlives the app.
	assert.Eventually(t, func() bool {
		return errors.Is(syscall.Kill(child, 0), syscall.ESRCH)
	}, 5*time.Second, 10*time.Millisecond, "the script's grandchild outlived the abort")
	assert.Empty(t, inst.SetupPhase())
}

// waitForPID reads a pid a script wrote to path, waiting for it to appear.
func waitForPID(t *testing.T, path string) int {
	t.Helper()
	var pid int
	require.Eventually(t, func() bool {
		raw, err := os.ReadFile(path)
		if err != nil {
			return false
		}
		pid, err = strconv.Atoi(strings.TrimSpace(string(raw)))
		return err == nil && pid > 0
	}, 5*time.Second, 10*time.Millisecond, "the script never reported a pid at %s", path)
	return pid
}
