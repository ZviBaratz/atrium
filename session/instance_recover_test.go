package session

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ZviBaratz/atrium/cmd/cmd_test"
	"github.com/ZviBaratz/atrium/session/git"
	"github.com/ZviBaratz/atrium/session/tmux"
	"github.com/stretchr/testify/require"
)

// recordingPtyFactory is a tmux.PtyFactory that records every command it is
// asked to start, so tests can assert *which* program a recovery path launched
// (e.g. `claude --continue` vs a blank `claude`). When startErr is set every
// Start fails, letting tests drive the failure branches without a real tmux.
type recordingPtyFactory struct {
	mu       sync.Mutex // guards cmds and opened against concurrent Start/Close
	cmds     []*exec.Cmd
	startErr error
	opened   []*os.File // pty stub files handed out by Start, released by Close
}

// newRecordingPtyFactory builds a recordingPtyFactory and registers its Close with
// t.Cleanup, so the pty stub files and fds it hands out are released at test end
// rather than leaking across the suite. startErr (may be nil) makes every Start fail.
func newRecordingPtyFactory(t *testing.T, startErr error) *recordingPtyFactory {
	t.Helper()
	f := &recordingPtyFactory{startErr: startErr}
	t.Cleanup(f.Close)
	return f
}

// setStartErr changes what Start returns from here on, so one fixture can fail a launch
// and then let the retry through. Taken under the same mutex Start reads the field
// through: a launch still in flight on another goroutine would otherwise race it.
func (f *recordingPtyFactory) setStartErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.startErr = err
}

func (f *recordingPtyFactory) Start(cmd *exec.Cmd) (*os.File, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cmds = append(f.cmds, cmd)
	if f.startErr != nil {
		return nil, f.startErr
	}
	// A real, bidirectional *os.File the caller can Close(); contents are irrelevant.
	// Tracked so Close removes it — otherwise each Start leaks one /tmp file.
	file, err := os.CreateTemp("", "pty-stub")
	if err != nil {
		return nil, err
	}
	f.opened = append(f.opened, file)
	return file, nil
}

// Close closes and removes every pty stub file Start handed out.
func (f *recordingPtyFactory) Close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, file := range f.opened {
		_ = file.Close()
		_ = os.Remove(file.Name())
	}
	f.opened = nil
}

func (f *recordingPtyFactory) commands() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.cmds))
	for _, c := range f.cmds {
		out = append(out, strings.Join(c.Args, " "))
	}
	return out
}

// newTestWorktree stands up a real, valid git worktree in a temp HOME so
// IsValidWorktree() returns true and Cleanup() has something to remove.
func newTestWorktree(t *testing.T) *git.Worktree {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	repoPath := filepath.Join(t.TempDir(), "repo")
	runGit(t, "", "init", repoPath)
	runGit(t, repoPath, "config", "user.email", "test@example.com")
	runGit(t, repoPath, "config", "user.name", "Test User")
	require.NoError(t, os.WriteFile(filepath.Join(repoPath, "README.md"), []byte("hello\n"), 0644))
	runGit(t, repoPath, "add", ".")
	runGit(t, repoPath, "commit", "-m", "initial")

	wt, _, err := git.NewWorktree(context.Background(), repoPath, "sess")
	require.NoError(t, err)
	require.NoError(t, wt.Setup())
	return wt
}

// newTestWorktreeFromBase is newTestWorktree's counterpart for a session created
// from a chosen base branch (baseRef != ""), exercising the Setup path that
// branches off baseRef instead of HEAD. baseRef is the repo's initial branch so
// it resolves locally.
func newTestWorktreeFromBase(t *testing.T) *git.Worktree {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	repoPath := filepath.Join(t.TempDir(), "repo")
	runGit(t, "", "init", repoPath)
	runGit(t, repoPath, "config", "user.email", "test@example.com")
	runGit(t, repoPath, "config", "user.name", "Test User")
	require.NoError(t, os.WriteFile(filepath.Join(repoPath, "README.md"), []byte("hello\n"), 0644))
	runGit(t, repoPath, "add", ".")
	runGit(t, repoPath, "commit", "-m", "initial")

	baseBranch := gitOutput(t, repoPath, "rev-parse", "--abbrev-ref", "HEAD")
	wt, _, err := git.NewWorktreeFromBase(context.Background(), repoPath, "sess", baseBranch)
	require.NoError(t, err)
	require.NoError(t, wt.Setup())
	return wt
}

// claudeProjectDirName mirrors transcript.sanitizeCWD (unexported there): every
// non-alphanumeric rune of the cwd becomes '-'. Kept trivially in sync — the
// transcript package's own TestSanitizeCWD pins the scheme; duplicated here only
// to place a fixture transcript at the path `claude --continue` would read.
func claudeProjectDirName(cwd string) string {
	return strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			return r
		}
		return '-'
	}, cwd)
}

// writeClaudeTranscript drops a non-empty session JSONL under
// <root>/projects/<encoded-cwd>/ so transcript.HasResumable reports a resumable
// conversation for cwd — i.e. the startResuming gate elects `--continue`.
func writeClaudeTranscript(t *testing.T, root, cwd string) {
	t.Helper()
	dir := filepath.Join(root, "projects", claudeProjectDirName(cwd))
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "session.jsonl"), []byte("{}\n"), 0o644))
}

// deadExec fails every tmux command, so DoesSessionExist() reports false and the
// duplicate-name guard in start() does not block the PTY launch.
// The failure text is tmux's own, not a paraphrase. Session.Close classifies a
// kill-session failure by that wording (sessionAlreadyGone) to tell "the session was
// already dead" — the teardown goal, met — from a real failure it must report. A fake
// that invents its own message makes every teardown over a dead session return an
// error, which is a property of the fake and of nothing else.
func deadExec() cmd_test.MockCmdExec {
	return cmd_test.MockCmdExec{
		RunFunc:    func(*exec.Cmd) error { return fmt.Errorf("can't find session: sess") },
		OutputFunc: func(*exec.Cmd) ([]byte, error) { return nil, fmt.Errorf("dead") },
	}
}

// orphanedWorktreeInstance builds an instance whose tmux session is gone (deadExec
// makes has-session fail) and whose worktree points at a nonexistent path, so any
// recovery path degrades to Paused without relaunching. It returns the pty factory
// so callers can assert nothing was launched. HOME is redirected to a temp dir
// because recovery shells out to git, which reads global config from $HOME.
func orphanedWorktreeInstance(t *testing.T) (*Instance, *recordingPtyFactory) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	// A storage-only worktree pointing at a path that does not exist.
	wt := git.NewWorktreeFromStorage(
		context.Background(),
		filepath.Join(t.TempDir(), "repo"),
		filepath.Join(t.TempDir(), "gone"),
		"sess", "session/sess", "", "main", false, "session/")
	pty := newRecordingPtyFactory(t, nil)
	ts := tmux.NewSessionWithDeps(context.Background(), "sess", "claude", pty, deadExec())
	inst := &Instance{ident: identity{title: "sess"}, status: Running, Program: "claude", gitWorktree: wt, tmuxSession: ts}
	return inst, pty
}

// TestRecoverInPlace_OrphanedWorktreeDegradesToPaused asserts that when the
// worktree is gone there is nothing to restart, so recovery leaves the instance
// Paused (branch preserved, recoverable via Resume) without touching tmux.
func TestRecoverInPlace_OrphanedWorktreeDegradesToPaused(t *testing.T) {
	inst, pty := orphanedWorktreeInstance(t)

	inst.recoverInPlace()

	require.True(t, inst.started, "a recovered instance must be marked started")
	require.True(t, inst.Paused(), "an orphaned worktree must degrade to Paused")
	require.Empty(t, pty.cmds, "no session should be launched when the worktree is gone")
}

// TestRecoverInPlace_ResumesConversationWhenWorktreeValid asserts that a valid
// worktree is brought back to Running by resuming the agent's prior conversation
// (StartContinue → `--continue`), not by starting a blank agent.
func TestRecoverInPlace_ResumesConversationWhenWorktreeValid(t *testing.T) {
	wt := newTestWorktree(t)
	cfgDir := t.TempDir()
	writeClaudeTranscript(t, cfgDir, wt.GetWorktreePath())
	pty := newRecordingPtyFactory(t, nil)
	calls := 0
	liveExec := cmd_test.MockCmdExec{
		// First has-session (the duplicate-name guard) must report "gone" so the
		// launch proceeds; the poll that follows must then see it as alive.
		RunFunc: func(*exec.Cmd) error {
			calls++
			if calls == 1 {
				return fmt.Errorf("not yet")
			}
			return nil
		},
		OutputFunc: func(*exec.Cmd) ([]byte, error) { return nil, nil },
	}
	ts := tmux.NewSessionWithDeps(context.Background(), "sess", "claude", pty, liveExec)
	inst := &Instance{ident: identity{title: "sess"}, status: Running, Program: "claude", claudeConfigDir: cfgDir, gitWorktree: wt, tmuxSession: ts}

	inst.recoverInPlace()

	require.True(t, inst.started)
	require.Equal(t, Running, inst.GetStatus(), "a valid worktree must recover to Running")
	require.NotEmpty(t, pty.cmds, "the session must be (re)launched")
	require.Contains(t, pty.commands()[0], "--continue",
		"recovery must resume the prior conversation, not start blank")
}

// TestRecoverInPlace_StartsBlankWhenNoConversation is the regression guard for the
// "No conversation found to continue!" loop: a claude session whose worktree has no
// transcript (e.g. paused before the agent ever ran) must recover by starting blank,
// never with `--continue` (which aborts and bounces the session back to Paused).
func TestRecoverInPlace_StartsBlankWhenNoConversation(t *testing.T) {
	wt := newTestWorktree(t)
	cfgDir := t.TempDir() // deliberately no transcript written
	pty := newRecordingPtyFactory(t, nil)
	calls := 0
	liveExec := cmd_test.MockCmdExec{
		RunFunc: func(*exec.Cmd) error {
			calls++
			if calls == 1 {
				return fmt.Errorf("not yet")
			}
			return nil
		},
		OutputFunc: func(*exec.Cmd) ([]byte, error) { return nil, nil },
	}
	ts := tmux.NewSessionWithDeps(context.Background(), "sess", "claude", pty, liveExec)
	inst := &Instance{ident: identity{title: "sess"}, status: Running, Program: "claude", claudeConfigDir: cfgDir, gitWorktree: wt, tmuxSession: ts}

	inst.recoverInPlace()

	require.Equal(t, Running, inst.GetStatus(), "recovery must still bring the session online")
	require.NotEmpty(t, pty.cmds, "the session must be (re)launched")
	require.NotContains(t, pty.commands()[0], "--continue",
		"with no conversation to continue, recovery must start the agent blank")
}

// TestRecoverInPlace_FailedRestartDegradesToPaused asserts that if the restart
// itself fails, recovery still degrades to Paused rather than aborting — one bad
// session must never block loading the rest — while still having attempted to
// resume the conversation.
func TestRecoverInPlace_FailedRestartDegradesToPaused(t *testing.T) {
	wt := newTestWorktree(t)
	cfgDir := t.TempDir()
	writeClaudeTranscript(t, cfgDir, wt.GetWorktreePath())
	pty := newRecordingPtyFactory(t, fmt.Errorf("pty boom"))
	ts := tmux.NewSessionWithDeps(context.Background(), "sess", "claude", pty, deadExec())
	inst := &Instance{ident: identity{title: "sess"}, status: Running, Program: "claude", claudeConfigDir: cfgDir, gitWorktree: wt, tmuxSession: ts}

	inst.recoverInPlace()

	require.True(t, inst.started)
	require.True(t, inst.Paused(), "a failed restart must degrade to Paused, not abort")
	require.Contains(t, pty.commands()[0], "--continue",
		"recovery must attempt to resume the prior conversation")
}

// TestRecreateSession_StartsBlankWhenNoConversation asserts the Resume fallback
// helper starts the agent blank (no `--continue`) when no transcript exists for the
// worktree — the resume path must not abort on a conversation that was never created.
func TestRecreateSession_StartsBlankWhenNoConversation(t *testing.T) {
	wt := newTestWorktree(t)
	cfgDir := t.TempDir() // deliberately no transcript written
	pty := newRecordingPtyFactory(t, fmt.Errorf("pty boom"))
	ts := tmux.NewSessionWithDeps(context.Background(), "sess", "claude", pty, deadExec())
	inst := &Instance{ident: identity{title: "sess"}, started: true, Program: "claude", claudeConfigDir: cfgDir, gitWorktree: wt, tmuxSession: ts}

	err := inst.recreateSession()

	require.Error(t, err, "a failed launch must still surface an error")
	require.NotContains(t, pty.commands()[0], "--continue",
		"with no conversation, the fallback must start the agent blank")
}

// TestKill_CleansUpWorktreeWhenNotStarted is the regression guard for the resource
// leak where a failed Start() left its worktree (and branch) behind. Start() only
// sets started=true on success, so its deferred Kill() ran while started==false; the
// old !isStarted() early-return then made that cleanup a no-op, leaking the worktree
// NewWorktree/Setup had already created. Kill() must tear down whatever resources
// exist regardless of the started flag. (No tmux session is attached so the assertion
// is the on-disk worktree alone, which is the resource that actually leaked.)
func TestKill_CleansUpWorktreeWhenNotStarted(t *testing.T) {
	wt := newTestWorktree(t)
	// started is left false and no tmux session is set — exactly the state Start()'s
	// deferred Kill() runs in when worktree setup has succeeded but a later step fails.
	inst := &Instance{ident: identity{title: "sess"}, gitWorktree: wt}

	require.NoError(t, inst.Kill())

	valid, err := wt.IsValidWorktree()
	require.NoError(t, err)
	require.False(t, valid, "Kill must remove the worktree even when the instance was never marked started")
}

// Resume must surface a typed *git.BranchCheckedOutError when the session branch
// is already checked out elsewhere — the app layer keys its detach-and-recover
// offer off errors.As against that type, so the type is the cross-package
// contract (a reworded message must not silently break recovery).
func TestResume_BranchCheckedOutReturnsTypedError(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repoPath := filepath.Join(t.TempDir(), "repo")
	runGit(t, "", "init", repoPath)
	runGit(t, repoPath, "config", "user.email", "test@example.com")
	runGit(t, repoPath, "config", "user.name", "Test User")
	require.NoError(t, os.WriteFile(filepath.Join(repoPath, "README.md"), []byte("hi\n"), 0644))
	runGit(t, repoPath, "add", ".")
	runGit(t, repoPath, "commit", "-m", "initial")
	// The base repo itself holds the session branch — the common Checkout case.
	runGit(t, repoPath, "switch", "-c", "session/sess")

	wt := git.NewWorktreeFromStorage(
		context.Background(),
		repoPath, filepath.Join(t.TempDir(), "wt"),
		"sess", "session/sess", "", "main", true, "session/")
	// A parked session, not a nil one: Resume closes what the park left behind before it
	// checks the branch, so this refusal is now reached with the close already done.
	ts, _ := parkedTmux(t)
	inst := &Instance{ident: identity{title: "sess"}, status: Paused, started: true, gitWorktree: wt, tmuxSession: ts}

	err := inst.Resume()
	require.Error(t, err)
	var busy *git.BranchCheckedOutError
	require.ErrorAs(t, err, &busy, "Resume must return a *git.BranchCheckedOutError")
	require.NotEmpty(t, busy.Path, "the error should name the holding worktree")
}

// TestRecreateSession_KeepsTheWorktreeWhenTheLaunchFails is the unit-level guard that a
// failed launch tears nothing down, and it is unconditional — there is no longer a flag
// with a destructive side.
//
// Worktree.Cleanup is the KILL teardown — `git worktree remove -f` plus `git branch
// -D`. It ran here as a launch-failure rollback, gated on whether this same call had
// materialized the worktree. That gate was the wrong question: it asks about the
// directory, while the destructive half is the branch, and no caller of this function
// ever created the branch (#741). Whichever park Resume met, the same teardown destroyed
// uncommitted work and deleted the branch holding the session's history.
//
// The end-to-end guards are TestResumeKeepsTheBranchWhenTheRelaunchFails, for the park
// that removed the worktree, and TestParkedOverflowSurvivesAFailedResume for the park
// that left one — a budget-parked session takes that path on every single resume, its
// tmux session having died with the server.
func TestRecreateSession_KeepsTheWorktreeWhenTheLaunchFails(t *testing.T) {
	wt := newTestWorktree(t)
	cfgDir := t.TempDir()
	writeClaudeTranscript(t, cfgDir, wt.GetWorktreePath())
	wip := filepath.Join(wt.GetWorktreePath(), "wip.txt")
	require.NoError(t, os.WriteFile(wip, []byte("half-finished\n"), 0o644))

	pty := newRecordingPtyFactory(t, fmt.Errorf("pty boom"))
	ts := tmux.NewSessionWithDeps(context.Background(), "sess", "claude", pty, deadExec())
	inst := &Instance{ident: identity{title: "sess"}, started: true, Program: "claude", claudeConfigDir: cfgDir, gitWorktree: wt, tmuxSession: ts}

	require.Error(t, inst.recreateSession(), "the failed launch must still surface")

	// Rides along on the one act rather than in a fixture of its own: the argv is elected
	// before the launch is attempted, so a failure must not change which command it
	// carried. TestRecreateSession_StartsBlankWhenNoConversation is the negative.
	require.Contains(t, pty.commands()[0], "--continue",
		"the fallback must resume the prior conversation, not start blank")

	valid, vErr := wt.IsValidWorktree()
	require.NoError(t, vErr)
	require.True(t, valid, "a failed launch must not tear the worktree down")
	onDisk, rErr := os.ReadFile(wip)
	require.NoError(t, rErr)
	require.Equal(t, "half-finished\n", string(onDisk), "and so must the work it holds")
}
