//go:build !windows

package session

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ZviBaratz/atrium/internal/testutil"
)

// Pause/resume against a REAL tmux server and a REAL git worktree.
//
// The mocked pause/resume tests cannot cover any of this. They build an executor
// that reports the tmux session as present no matter what has been run against it
// (instance_pause_resume_test.go), which is the exact question #710 turned on: does
// a pause leave the agent running, and does the resumed pane still reach its
// worktree? A mock that answers "the session is alive" makes both unaskable, which
// is why every one of them stayed green for the whole life of the bug.
//
// The pane program is bash, so a failure is legible as a shell error rather than an
// agent's rendering. The hazard is program-independent — cwd is a process-level
// inode reference — and #710 measured a live claude holding the same `(deleted)`
// link. It must not be a program that EXITS: Start polls for the session for two
// seconds, and `echo`/`true` can take the pane down first (see app/undo_kill_test.go,
// which uses `sleep 300` for the same reason).

// livePaneTimeout is the budget for a pane to run one command and for a killed
// pane's process to disappear. Generous on purpose: a working pane answers in
// milliseconds, so the length costs nothing, while a broken one fails at the
// deadline either way.
const (
	livePaneTimeout = 10 * time.Second
	livePanePoll    = 10 * time.Millisecond
)

// liveGitSession starts a real tmux session on a real git worktree, running bash.
// The instance is killed at cleanup, so no server outlives the test.
func liveGitSession(t *testing.T, title string) *Instance {
	t.Helper()
	testutil.RequireTmux(t)

	repo := filepath.Join(t.TempDir(), "repo")
	runGit(t, "", "init", repo)
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test User")
	require.NoError(t, os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o644))
	runGit(t, repo, "add", ".")
	runGit(t, repo, "commit", "-m", "initial")

	inst, err := NewInstance(InstanceOptions{Title: title, Path: repo, Program: "bash"})
	require.NoError(t, err)
	inst.SetBaseContext(context.Background())
	require.NoError(t, inst.Start(true))
	t.Cleanup(func() {
		inst.RebindBaseContext(context.Background())
		_ = inst.Kill()
	})
	return inst
}

// runInPane types a command into the agent pane and presses Enter. SendKeys is
// literal (`send-keys -l`), so shell metacharacters arrive as typed.
func runInPane(t *testing.T, inst *Instance, command string) {
	t.Helper()
	require.NoError(t, inst.SendKeys(command))
	require.NoError(t, inst.tmux().TapEnter())
}

// paneShellPID asks the pane's shell for its own pid, writing it to an absolute
// path OUTSIDE the worktree so the answer does not depend on the pane's working
// directory — the very thing the tests below are measuring. It doubles as the
// readiness wait: a pid on disk proves the shell is up and executing what it is
// sent.
func paneShellPID(t *testing.T, inst *Instance) int {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pane.pid")
	runInPane(t, inst, "echo $$ > "+path)

	var pid int
	require.Eventually(t, func() bool {
		raw, err := os.ReadFile(path)
		if err != nil {
			return false
		}
		pid, err = strconv.Atoi(strings.TrimSpace(string(raw)))
		return err == nil && pid > 0
	}, livePaneTimeout, livePanePoll, "the pane never reported a pid at %s", path)
	return pid
}

// requireProcessGone waits for pid to leave the process table. tmux is the pane
// process's parent and reaps it, so ESRCH is the settled answer rather than a
// zombie's.
func requireProcessGone(t *testing.T, pid int, what string) {
	t.Helper()
	require.Eventually(t, func() bool {
		return syscall.Kill(pid, 0) == syscall.ESRCH
	}, livePaneTimeout, livePanePoll, "%s (pid %d) is still running", what, pid)
}

// TestPauseClosesTheSessionAndStopsTheAgent is #710's first acceptance criterion.
// Pause used to detach — tearing down only Atrium's attach pty — so the tmux
// session and the agent survived indefinitely, holding their memory, in a working
// directory the same pause had just deleted. Both halves are asserted, because
// either alone can be true while the other is not: the tmux session can be gone
// while a process it started lives on, and a live session obviously implies a live
// pane.
func TestPauseClosesTheSessionAndStopsTheAgent(t *testing.T) {
	inst := liveGitSession(t, "pause-closes")
	pid := paneShellPID(t, inst)
	require.True(t, inst.TmuxAlive(), "the session must be alive before the pause under test")

	require.NoError(t, inst.Pause())

	assert.False(t, inst.TmuxAlive(), "pause must close the tmux session, not merely detach from it")
	requireProcessGone(t, pid, "the paused session's agent")
}

// TestResumeAfterPauseCanReadAndWriteItsWorktree is the assertion that covers the
// whole #710 class: a pause→resume round trip inside ONE tmux-server lifetime must
// return a pane that can actually use its worktree.
//
// Before the fix this failed silently in the worst possible way. Resume reattached
// the surviving agent, whose cwd was the inode the worktree removal had unlinked,
// while a fresh worktree sat at that exact path. The pane printed the correct
// $PWD and held the correct conversation, and every file operation in it failed.
// Only a resume after a REBOOT worked, because DoesSessionExist was then false and
// resume took the recreate path.
//
// Everything the pane is asked to do here is therefore RELATIVE, so it resolves
// through the process's working directory rather than a path the test supplies.
// It also re-checks the auto-commit round trip (#141) on the new path: the WIP
// comes back as a pending change, with no `(paused)` artifact left in history.
func TestResumeAfterPauseCanReadAndWriteItsWorktree(t *testing.T) {
	inst := liveGitSession(t, "resume-reaches-worktree")
	wtPath := inst.WorkingDir()
	beforePID := paneShellPID(t, inst)

	// Uncommitted work, so the pause auto-commits it and the resume unwinds it.
	require.NoError(t, os.WriteFile(filepath.Join(wtPath, "before.txt"), []byte("wip\n"), 0o644))

	require.NoError(t, inst.Pause())
	require.NoError(t, inst.Resume())

	afterPID := paneShellPID(t, inst)
	assert.NotEqual(t, beforePID, afterPID, "resume must relaunch the agent, not reattach the stranded one")

	runInPane(t, inst, "git status --porcelain > status.txt && touch resumed-file.txt")
	require.Eventually(t, func() bool {
		_, err := os.Stat(filepath.Join(wtPath, "resumed-file.txt"))
		return err == nil
	}, livePaneTimeout, livePanePoll, "a file the resumed pane created never appeared in the worktree on disk")

	status, err := os.ReadFile(filepath.Join(wtPath, "status.txt"))
	require.NoError(t, err, "the resumed pane could not write git's output into its own worktree")
	assert.Contains(t, string(status), "before.txt",
		"the pane must see the unwound auto-commit as a pending change in the worktree it is standing in")

	assert.False(t, isAutoPauseCommit(gitOutput(t, wtPath, "log", "-1", "--format=%s")),
		"resume must leave no (paused) commit in history")
}

// TestResumeClosesASessionLeftByAnOlderPause covers the upgrade path, and it is the
// only reason Resume still probes for a live session at all: an Atrium that paused
// by detaching leaves exactly this state behind — Paused, worktree removed, tmux
// session and agent still alive — and the first resume under the new build must not
// reattach it. Restoring here is what #710 actually was.
//
// The pre-fix pause is reproduced by its parts rather than by an old binary, so the
// test states the state under test instead of depending on how it was reached.
func TestResumeClosesASessionLeftByAnOlderPause(t *testing.T) {
	inst := liveGitSession(t, "resume-upgrades-old-park")
	wtPath := inst.WorkingDir()
	strandedPID := paneShellPID(t, inst)

	// What pause did before #710: tear down the attach pty, drop the worktree, flip
	// the status — and leave the agent running.
	require.NoError(t, inst.tmux().DetachSafely())
	require.NoError(t, inst.worktree().Remove())
	inst.SetStatus(Paused)
	require.True(t, inst.TmuxAlive(), "the state under test is a park that left the session alive")

	require.NoError(t, inst.Resume())

	requireProcessGone(t, strandedPID, "the agent an older pause stranded")
	require.Equal(t, wtPath, inst.WorkingDir(), "resume must rebuild the worktree at the same path")

	runInPane(t, inst, "touch resumed-file.txt")
	require.Eventually(t, func() bool {
		_, err := os.Stat(filepath.Join(wtPath, "resumed-file.txt"))
		return err == nil
	}, livePaneTimeout, livePanePoll, "the relaunched pane never wrote into the rebuilt worktree")
}
