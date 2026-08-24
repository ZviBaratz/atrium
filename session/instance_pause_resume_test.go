package session

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ZviBaratz/atrium/cmd/cmd_test"
	"github.com/ZviBaratz/atrium/session/git"
	"github.com/ZviBaratz/atrium/session/tmux"
	"github.com/stretchr/testify/require"
)

// gitOutput runs git in dir and returns its trimmed combined output, failing the
// test on error. (runGit, the sibling helper, discards output.)
func gitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), "git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// autoMsg builds a message in pause's auto-commit format, so tests exercise the
// real detection path rather than a hand-typed string that could drift.
func autoMsg(when string) string {
	return autoPauseCommitPrefix + "'sess' on " + when + " " + autoPauseCommitSuffix
}

// fakeTmuxServer models the one fact the executor these tests used to build could
// not: a tmux session stops existing once something kills it.
//
// That executor ("an executor that reports the tmux session as present") answered
// every command with success, so has-session said "alive" forever — including after
// a Close. It therefore mocked the answer to the exact question #710 turned on, and
// no test built on it could observe that pause left the agent running or that a
// resume reattached a process standing in a deleted directory. The whole pause/resume
// suite stayed green for the life of that bug. The live guards are in
// pause_live_test.go; this is what keeps the tmux-free tests honest about the
// lifecycle they drive.
//
// It answers the three verbs the pause/resume paths turn on and passes everything
// else. new-session is observed on the PTY side because that is where tmux.start
// launches it (through the pty factory, not the executor), so a fake that only
// watched the executor would never come back alive.
type fakeTmuxServer struct {
	alive atomic.Bool
	pty   *recordingPtyFactory
}

// newFakeTmuxServer returns a fake whose session is already running, matching a
// session that has been started.
func newFakeTmuxServer(t *testing.T) *fakeTmuxServer {
	t.Helper()
	s := &fakeTmuxServer{pty: newRecordingPtyFactory(t, nil)}
	s.alive.Store(true)
	return s
}

// Start implements tmux.PtyFactory: a `new-session` that SUCCEEDS brings the session
// up, everything else (the attach-session Restore opens) is recorded and ignored.
//
// Gated on the pty's answer rather than on the verb alone, because a fixture whose
// factory refuses every launch is how the resume paths are driven to fail: flipping
// first would leave that fake reporting a live session for a launch that never
// happened, mocking away the very failure it was built to produce.
func (s *fakeTmuxServer) Start(cmd *exec.Cmd) (*os.File, error) {
	f, err := s.pty.Start(cmd)
	if err == nil && tmuxVerb(cmd, "new-session") {
		s.alive.Store(true)
	}
	return f, err
}

func (s *fakeTmuxServer) Close() { s.pty.Close() }

// exec implements the cmd.Executor side: kill-session takes the session down, and
// has-session reports what is actually there.
func (s *fakeTmuxServer) exec() cmd_test.MockCmdExec {
	return cmd_test.MockCmdExec{
		RunFunc: func(cmd *exec.Cmd) error {
			switch {
			case tmuxVerb(cmd, "kill-session"):
				if !s.alive.Load() {
					// A kill answers like a probe, and it has to: Resume now closes
					// unconditionally, so EVERY resume from an ordinary park kills a
					// session that is already gone. sessionAlreadyGone forgiving that
					// is what lets the resume proceed, which makes it load-bearing on
					// a path no tmux-free test would otherwise drive. Returning nil
					// here mocked the forgiveness instead of exercising it: narrow
					// sessionAlreadyGone and every fixture below would stay green
					// while every real resume aborted.
					return errSessionGone
				}
				s.alive.Store(false)
			case tmuxVerb(cmd, "has-session"):
				if !s.alive.Load() {
					return errSessionGone
				}
			}
			return nil
		},
		OutputFunc: func(*exec.Cmd) ([]byte, error) { return nil, nil },
	}
}

// errSessionGone is tmux's own wording for a session that is not there, and it has to
// be its own wording: Session.Close and liveness both classify a kill/probe failure by
// this text (sessionAlreadyGone), so a fake that invented its own message would read as
// a real teardown failure rather than a dead session.
var errSessionGone = errors.New("can't find session: sess")

// tmuxVerb reports whether cmd is the given tmux subcommand. tmuxCommand builds
// `tmux -L <socket> [-f conf] <verb> …`, so the verb is one argv element among
// flags rather than a fixed position.
func tmuxVerb(cmd *exec.Cmd, verb string) bool {
	return slices.Contains(cmd.Args, verb)
}

// newParkedTmuxServer returns a fake whose session is already gone — the state a park
// leaves behind, and the one every resume path starts from. It is newFakeTmuxServer with
// the one bit that differs flipped, rather than a second copy of the shape.
func newParkedTmuxServer(t *testing.T) *fakeTmuxServer {
	t.Helper()
	s := newFakeTmuxServer(t)
	s.alive.Store(false)
	return s
}

// newParkedTmuxServerFailingLaunch is newParkedTmuxServer whose agent will not start:
// the pty factory refuses every launch. That is the ordinary way a resume's relaunch
// fails — a program or profile that no longer resolves, a server that will not start a
// session, resource exhaustion — as distinct from the stale session the close guards
// against, which fails earlier and for a different reason.
func newParkedTmuxServerFailingLaunch(t *testing.T, startErr error) *fakeTmuxServer {
	t.Helper()
	s := newParkedTmuxServer(t)
	s.pty.setStartErr(startErr)
	return s
}

// parkedTmux is the fake a parked instance holds, with the server it runs on.
//
// Any fixture that reaches Resume needs one. Resume closes the parked session before it
// looks at the worktree, so a nil tmuxSession now panics where it used to be reached only
// after several early returns — and nil is a shape production cannot produce: Start and
// FromInstanceData both set the field, and Resume refuses an instance that is not started.
//
// The server comes back alongside the session because a caller that wants to assert on the
// launch needs its pty, and returning the session alone is what made those callers rebuild
// this pair by hand.
func parkedTmux(t *testing.T) (*tmux.Session, *fakeTmuxServer) {
	t.Helper()
	srv := newParkedTmuxServer(t)
	return tmux.NewSessionWithDeps(context.Background(), "sess", "claude", srv, srv.exec()), srv
}

// pausableInstance wires a Running instance around a real worktree and a fake tmux
// server that tracks its own liveness, so Pause and Resume run their real teardown
// and relaunch rather than talking to a mock that always says yes.
func pausableInstance(t *testing.T, wt *git.Worktree) *Instance {
	t.Helper()
	srv := newFakeTmuxServer(t)
	ts := tmux.NewSessionWithDeps(context.Background(), "sess", "claude", srv, srv.exec())
	return &Instance{ident: identity{title: "sess"}, status: Running, started: true, gitWorktree: wt, tmuxSession: ts}
}

// TestPauseResume_RoundTripsWithoutHistoryArtifact is the core acceptance test
// for #141: pausing a dirty session then resuming it must leave branch HEAD and
// the working tree exactly as they were, with no leftover `(paused)` commit.
func TestPauseResume_RoundTripsWithoutHistoryArtifact(t *testing.T) {
	wt := newTestWorktree(t) // HEAD-based (baseRef == ""), so resume reuses the branch
	wtPath := wt.GetWorktreePath()
	repoPath := wt.GetRepoPath()
	branch := wt.GetBranchName()

	baseSHA := gitOutput(t, wtPath, "rev-parse", "HEAD")
	require.NoError(t, os.WriteFile(filepath.Join(wtPath, "work.txt"), []byte("in progress\n"), 0644))

	inst := pausableInstance(t, wt)

	// Pause commits the WIP so it survives the worktree removal.
	require.NoError(t, inst.Pause())
	require.True(t, inst.Paused())
	require.NotEqual(t, baseSHA, gitOutput(t, repoPath, "rev-parse", branch), "pause must commit the WIP")
	require.True(t, isAutoPauseCommit(gitOutput(t, repoPath, "log", "-1", "--format=%s", branch)),
		"the pause commit must be a recognizable auto-commit")

	// Resume must unwind that auto-commit back into pending changes.
	require.NoError(t, inst.Resume())
	require.Equal(t, Running, inst.GetStatus())

	require.Equal(t, baseSHA, gitOutput(t, wtPath, "rev-parse", "HEAD"), "resume must unwind the auto-commit")
	require.NotEmpty(t, gitOutput(t, wtPath, "status", "--porcelain"), "the WIP must return as a pending change")
	content, err := os.ReadFile(filepath.Join(wtPath, "work.txt"))
	require.NoError(t, err)
	require.Equal(t, "in progress\n", string(content), "the edited content must be restored verbatim")
}

// TestPauseResume_BaseRefSession_RoundTripsWithoutDataLoss is the same acceptance
// test for a session created from a chosen base branch (baseRef != ""). Setup must
// reattach to the existing session branch on resume, not force-recreate it from
// baseRef — otherwise the paused WIP commit is force-deleted and lost. This fails
// before the existence-first Setup fix and passes after it.
func TestPauseResume_BaseRefSession_RoundTripsWithoutDataLoss(t *testing.T) {
	wt := newTestWorktreeFromBase(t) // baseRef != "", the path that previously lost WIP
	wtPath := wt.GetWorktreePath()
	repoPath := wt.GetRepoPath()
	branch := wt.GetBranchName()

	baseSHA := gitOutput(t, wtPath, "rev-parse", "HEAD")
	require.NoError(t, os.WriteFile(filepath.Join(wtPath, "work.txt"), []byte("in progress\n"), 0644))

	inst := pausableInstance(t, wt)

	require.NoError(t, inst.Pause())
	require.True(t, inst.Paused())
	require.True(t, isAutoPauseCommit(gitOutput(t, repoPath, "log", "-1", "--format=%s", branch)),
		"the pause commit must be a recognizable auto-commit")

	require.NoError(t, inst.Resume())
	require.Equal(t, Running, inst.GetStatus())

	require.Equal(t, baseSHA, gitOutput(t, wtPath, "rev-parse", "HEAD"),
		"resume must reuse the branch and unwind the auto-commit, not recreate from baseRef")
	require.NotEmpty(t, gitOutput(t, wtPath, "status", "--porcelain"), "the WIP must return as a pending change")
	content, err := os.ReadFile(filepath.Join(wtPath, "work.txt"))
	require.NoError(t, err)
	require.Equal(t, "in progress\n", string(content), "the edited content must be restored verbatim")
}

// TestPauseResume_ReconcilesCachedCommitCount pins the kill-warning accounting
// across a pause→resume→pause cycle. pause folds its auto-WIP commit into the cached
// count so the kill dialog can warn; resume unwinds that commit, so the cache must be
// walked back down too — otherwise a session re-paused before the next metadata poll
// (which skips paused instances) would carry a permanently inflated, persisted count.
func TestPauseResume_ReconcilesCachedCommitCount(t *testing.T) {
	wt := newTestWorktree(t)
	wtPath := wt.GetWorktreePath()

	inst := pausableInstance(t, wt)

	// A never-polled (nil-stats) session with a dirty worktree.
	require.NoError(t, os.WriteFile(filepath.Join(wtPath, "work.txt"), []byte("in progress\n"), 0644))

	// Pause folds the one auto-WIP commit into the cache so the kill dialog warns.
	require.NoError(t, inst.Pause())
	require.NotNil(t, inst.GetDiffStats(), "pause must seed the cached stats it warns from")
	require.Equal(t, 1, inst.GetDiffStats().Commits, "pause folds its auto-WIP commit into the cached count")

	// Resume unwinds that commit; the cache must come back down and the restored
	// changes read as dirty again.
	require.NoError(t, inst.Resume())
	require.Equal(t, 0, inst.GetDiffStats().Commits, "resume walks the pause-bumped count back down")
	require.True(t, inst.GetDiffStats().Dirty, "the unwound WIP returns as a pending change")

	// Re-pausing before any poll must land on 1, not 2: git again holds exactly one
	// WIP commit ahead of base, and the cache must track it.
	require.NoError(t, inst.Pause())
	require.Equal(t, 1, inst.GetDiffStats().Commits, "resume→re-pause must not inflate the cached commit count")
}

// TestUnwindAutoPauseCommits_CollapsesStackedAutoCommits covers the legacy case
// the issue calls out: several reboots stacked multiple auto-commits. A single
// unwind must collapse the whole consecutive run back to the real ancestor.
func TestUnwindAutoPauseCommits_CollapsesStackedAutoCommits(t *testing.T) {
	wt := newTestWorktree(t)
	wtPath := wt.GetWorktreePath()
	baseSHA := gitOutput(t, wtPath, "rev-parse", "HEAD")

	require.NoError(t, os.WriteFile(filepath.Join(wtPath, "a.txt"), []byte("a\n"), 0644))
	require.NoError(t, wt.CommitChanges(autoMsg("Mon")))
	require.NoError(t, os.WriteFile(filepath.Join(wtPath, "b.txt"), []byte("b\n"), 0644))
	require.NoError(t, wt.CommitChanges(autoMsg("Tue")))

	inst := &Instance{ident: identity{title: "sess"}, gitWorktree: wt}
	n, err := inst.unwindAutoPauseCommits(wt)
	require.NoError(t, err)
	require.Equal(t, 2, n, "both stacked auto-commits are reported as unwound")

	require.Equal(t, baseSHA, gitOutput(t, wtPath, "rev-parse", "HEAD"), "all consecutive auto-commits must collapse")
	require.NotEmpty(t, gitOutput(t, wtPath, "status", "--porcelain"))
	require.FileExists(t, filepath.Join(wtPath, "a.txt"))
	require.FileExists(t, filepath.Join(wtPath, "b.txt"))
}

// TestUnwindAutoPauseCommits_PreservesRealCommit guards the safety property: a
// genuine, user-authored commit on top of the branch is never soft-reset.
func TestUnwindAutoPauseCommits_PreservesRealCommit(t *testing.T) {
	wt := newTestWorktree(t)
	wtPath := wt.GetWorktreePath()

	require.NoError(t, os.WriteFile(filepath.Join(wtPath, "auto.txt"), []byte("x\n"), 0644))
	require.NoError(t, wt.CommitChanges(autoMsg("Mon")))
	require.NoError(t, os.WriteFile(filepath.Join(wtPath, "real.txt"), []byte("y\n"), 0644))
	require.NoError(t, wt.CommitChanges("feat: a real change"))
	headBefore := gitOutput(t, wtPath, "rev-parse", "HEAD")

	inst := &Instance{ident: identity{title: "sess"}, gitWorktree: wt}
	n, err := inst.unwindAutoPauseCommits(wt)
	require.NoError(t, err)
	require.Equal(t, 0, n, "a real HEAD is never unwound, so nothing is reported")

	require.Equal(t, headBefore, gitOutput(t, wtPath, "rev-parse", "HEAD"),
		"a real commit at HEAD must never be unwound")
}

// TestResumeKeepsTheBranchWhenTheParkedSessionWillNotClose. Resume closes a session an
// older, detach-only pause left behind (#710) before it relaunches. That close is the one
// step whose failure must ABORT rather than be walked past, and the reason is entirely in
// what would come next: the relaunch fails on Start's duplicate-name guard, leaving this
// call to have re-added the worktree and unwound the auto-commit for a launch that never
// had a chance. When this guard was written that was the smaller half — recreateSession
// answered a failed launch by tearing the worktree down through Worktree.Cleanup, `git
// worktree remove -f` and `git branch -D`, the KILL teardown with no retention ref
// because only Kill records one, so the session's whole history went with it (#741).
//
// So the postcondition is that the park is still a park. The close is ordered above the
// worktree block for exactly that reason, which makes this assertable as outcomes rather
// than as "the damage was smaller": the branch still holds the auto-commit pause made
// (nothing unwound it) and the worktree is still absent (nothing re-added it).
//
// The fixture carries a REAL commit under the auto-pause one so the two are distinguished:
// unwinding is a soft reset onto the real ancestor, so a tip that is still the auto-commit
// proves the abort landed before the unwind rather than merely leaving some branch behind.
// Those two facts are what a regression breaks, and they are read off the repository
// itself — the teardown this guards against is no longer reachable from recreateSession
// at all (#741), so there is nothing left to count the calls of.
func TestResumeKeepsTheBranchWhenTheParkedSessionWillNotClose(t *testing.T) {
	wt := newTestWorktree(t)
	repoPath := wt.GetRepoPath()
	branch := wt.GetBranchName()
	wtPath := wt.GetWorktreePath()

	require.NoError(t, os.WriteFile(filepath.Join(wtPath, "work.txt"), []byte("hours of it\n"), 0644))
	require.NoError(t, wt.CommitChanges("feat: the session's whole history"))
	realSHA := gitOutput(t, repoPath, "rev-parse", branch)

	// The WIP a pause folds into an auto-commit, which a resume that got as far as the
	// worktree would soft-reset back out. Built by autoMsg so it is the subject
	// isAutoPauseCommit actually recognizes, not a hand-typed one that could drift.
	require.NoError(t, os.WriteFile(filepath.Join(wtPath, "wip.txt"), []byte("unfinished\n"), 0644))
	require.NoError(t, wt.CommitChanges(autoMsg("02 Jan 06 15:04 MST")))
	parkSHA := gitOutput(t, repoPath, "rev-parse", branch)
	require.NotEqual(t, realSHA, parkSHA, "the fixture must have an auto-commit to unwind")

	// The park: the worktree is gone, the tmux session is not.
	require.NoError(t, wt.Remove())

	// A server that answers has-session but refuses to kill. Not one of the messages
	// sessionAlreadyGone forgives, so Close reports a genuine teardown failure — the
	// case this test is about — rather than "the session was already dead".
	stuck := cmd_test.MockCmdExec{
		RunFunc: func(cmd *exec.Cmd) error {
			if tmuxVerb(cmd, "kill-session") {
				return errors.New("server not responding")
			}
			return nil
		},
		OutputFunc: func(*exec.Cmd) ([]byte, error) { return nil, nil },
	}
	ts := tmux.NewSessionWithDeps(context.Background(), "sess", "claude", newRecordingPtyFactory(t, nil), stuck)
	inst := &Instance{ident: identity{title: "sess"}, status: Paused, started: true, gitWorktree: wt, tmuxSession: ts}

	err := inst.Resume()

	require.Error(t, err, "a session that will not close cannot be resumed over")
	// BranchTip rather than gitOutput: a deleted branch is the failure under test, and
	// `rev-parse` on one that is gone fails the test with git's "ambiguous argument"
	// instead of saying what was lost.
	tip, exists := git.BranchTip(context.Background(), repoPath, branch)
	require.True(t, exists, "a failed resume deleted the session's branch, and every commit on it with it")
	require.Equal(t, parkSHA, tip,
		"the branch moved: the resume got as far as unwinding the auto-commit before it gave up")
	require.NoDirExists(t, wtPath,
		"the resume materialized the worktree it then refused to launch into, leaving a park that is no longer a park")
	require.ErrorContains(t, err, "parked with", "the report must name the close, not the relaunch it never tried")
}

// TestResumeKeepsTheBranchWhenTheRelaunchFails is the other route into the same loss,
// and the one TestResumeKeepsTheBranchWhenTheParkedSessionWillNotClose cannot reach: the
// parked session closes cleanly, the worktree comes back, and the AGENT is what will not
// start — a program or profile that no longer resolves, a tmux server that refuses a
// session, resource exhaustion (#741).
//
// A park removes the worktree, so this is the ORDINARY resume rather than an edge case.
// Resume re-added it and used to tell recreateSession it could therefore roll it back,
// which ran Worktree.Cleanup — `git worktree remove -f` and `git branch -D`, the KILL
// teardown, with no retention ref because only Kill records one. So a mistyped profile
// took the session's whole history with it.
//
// The postcondition is the state the resume found, not a smaller loss: branch, history
// and the parked work all survive, and a second Resume is the entire retry. The fixture
// carries a REAL commit under the auto-pause one so the tip reports how far the resume
// got — landing on the real ancestor is the unwind having run, which is what separates
// this route from the abort its sibling covers, where the tip is still the auto-commit.
//
// The pending-change assertion is the sharp one, and it is why the answer is no rollback
// at all rather than a gentler one. unwindAutoPauseCommits is a SOFT reset, so by the time
// the launch fails the parked work exists only as uncommitted changes in the worktree and
// the branch tip has moved back off it: retaining the branch first would pin a tip that
// predates that work, and even the pause teardown (Worktree.Remove) discards it.
func TestResumeKeepsTheBranchWhenTheRelaunchFails(t *testing.T) {
	wt := newTestWorktree(t)
	repoPath := wt.GetRepoPath()
	branch := wt.GetBranchName()
	wtPath := wt.GetWorktreePath()

	require.NoError(t, os.WriteFile(filepath.Join(wtPath, "work.txt"), []byte("hours of it\n"), 0644))
	require.NoError(t, wt.CommitChanges("feat: the session's whole history"))
	realSHA := gitOutput(t, repoPath, "rev-parse", branch)

	// The WIP pause folded into an auto-commit, built by autoMsg so it is the subject
	// isAutoPauseCommit actually recognizes rather than a hand-typed one that could drift.
	require.NoError(t, os.WriteFile(filepath.Join(wtPath, "wip.txt"), []byte("unfinished\n"), 0644))
	require.NoError(t, wt.CommitChanges(autoMsg("02 Jan 06 15:04 MST")))
	parkSHA := gitOutput(t, repoPath, "rev-parse", branch)
	require.NotEqual(t, realSHA, parkSHA, "the fixture must have an auto-commit to unwind")

	// The park in full: the worktree is gone and so is the session, so the close is
	// forgiven and the resume runs its whole worktree block rather than aborting above it.
	require.NoError(t, wt.Remove())

	srv := newParkedTmuxServerFailingLaunch(t, errors.New("pty boom"))
	ts := tmux.NewSessionWithDeps(context.Background(), "sess", "claude", srv, srv.exec())
	inst := &Instance{ident: identity{title: "sess"}, status: Paused, started: true, gitWorktree: wt, tmuxSession: ts}

	err := inst.Resume()

	require.Error(t, err, "an agent that will not start must fail the resume")
	require.True(t, inst.Paused(), "a failed resume must leave the session parked, not Running")

	// BranchTip rather than gitOutput, for the reason its sibling documents: a deleted
	// branch is the failure under test, and `rev-parse` on one that is gone fails the
	// test with git's "ambiguous argument" instead of saying what was lost.
	tip, exists := git.BranchTip(context.Background(), repoPath, branch)
	require.True(t, exists, "a failed relaunch deleted the session's branch, and every commit on it with it")
	require.Equal(t, realSHA, tip,
		"the tip must be the real ancestor: this is the route that runs the unwind, not the one that aborts above it")

	valid, vErr := wt.IsValidWorktree()
	require.NoError(t, vErr)
	require.True(t, valid, "the worktree this resume materialized must survive for the retry")
	onDisk, rErr := os.ReadFile(filepath.Join(wtPath, "wip.txt"))
	require.NoError(t, rErr, "the parked work must be back on disk")
	require.Equal(t, "unfinished\n", string(onDisk))
	require.NotEmpty(t, gitOutput(t, wtPath, "status", "--porcelain"),
		"and it must be PENDING: the unwind soft-reset it out of history, so nothing else is holding it")
}

// TestTheFakeServerStaysDownWhenTheLaunchFails pins the one fact fakeTmuxServer models
// that every failing-launch fixture below rests on: a session comes up only if the launch
// did.
//
// Gated on the verb alone, the fake flipped alive on `new-session` BEFORE the pty had
// answered, so a fixture built to refuse every launch was asserting against a server that
// claimed the agent was running. Driving that through Resume cannot show it: tmux's own
// start answers a pty failure by killing any partial session, and the fake's exec forgives
// that kill by clearing alive again, so the end state is identical either way. The
// difference exists only at Start, which is why this is asserted here and not through a
// resume.
func TestTheFakeServerStaysDownWhenTheLaunchFails(t *testing.T) {
	srv := newParkedTmuxServerFailingLaunch(t, errors.New("pty boom"))
	require.False(t, srv.alive.Load(), "a parked server starts with its session already gone")

	// Never run — the fake only inspects the argv.
	_, err := srv.Start(exec.CommandContext(context.Background(), "tmux", "new-session", "-d", "-s", "sess"))

	require.Error(t, err, "the fixture exists to fail a launch")
	require.False(t, srv.alive.Load(),
		"the fake brought a session up for a launch that never happened")
}

// TestASecondResumeRecoversTheSessionAFailedOneLeft executes the other half of the
// contract #741 rests on. Leaving the worktree standing is only acceptable because the
// retry is a plain second Resume, which three separate comments assert and nothing ran —
// so the divergence between the first attempt and the retry was unmeasured.
//
// The retry takes a different path through Resume than the attempt that failed: the
// worktree is valid now, so Setup, the unwind and the setup script are all skipped. The
// tip assertion is what proves the skipped unwind is right rather than merely quiet — a
// second soft reset would land below the real ancestor and eat a genuine commit.
func TestASecondResumeRecoversTheSessionAFailedOneLeft(t *testing.T) {
	wt := newTestWorktree(t)
	repoPath := wt.GetRepoPath()
	branch := wt.GetBranchName()
	wtPath := wt.GetWorktreePath()

	require.NoError(t, os.WriteFile(filepath.Join(wtPath, "work.txt"), []byte("hours of it\n"), 0644))
	require.NoError(t, wt.CommitChanges("feat: the session's whole history"))
	realSHA := gitOutput(t, repoPath, "rev-parse", branch)
	require.NoError(t, os.WriteFile(filepath.Join(wtPath, "wip.txt"), []byte("unfinished\n"), 0644))
	require.NoError(t, wt.CommitChanges(autoMsg("02 Jan 06 15:04 MST")))
	require.NoError(t, wt.Remove())

	srv := newParkedTmuxServerFailingLaunch(t, errors.New("pty boom"))
	ts := tmux.NewSessionWithDeps(context.Background(), "sess", "claude", srv, srv.exec())
	inst := &Instance{ident: identity{title: "sess"}, status: Paused, started: true, gitWorktree: wt, tmuxSession: ts}

	require.Error(t, inst.Resume(), "the first attempt must fail, or there is no retry to test")
	require.True(t, inst.Paused(), "and must leave the session parked, which is what makes a retry possible")

	// Whatever made the launch fail is fixed: a corrected profile, a server that will
	// take a session now.
	srv.pty.setStartErr(nil)

	require.NoError(t, inst.Resume(), "the retry must be a plain second Resume, with no rescue in between")
	require.Equal(t, Running, inst.GetStatus())

	tip, exists := git.BranchTip(context.Background(), repoPath, branch)
	require.True(t, exists, "the branch must have survived both attempts")
	require.Equal(t, realSHA, tip,
		"the retry unwound a second time: the auto-commit was already gone, so this reset ate a real commit")
	onDisk, rErr := os.ReadFile(filepath.Join(wtPath, "wip.txt"))
	require.NoError(t, rErr, "the parked work must still be there after the retry")
	require.Equal(t, "unfinished\n", string(onDisk))
	require.NotEmpty(t, gitOutput(t, wtPath, "status", "--porcelain"),
		"and still pending: the retry must not have committed or discarded it")
}

// TestResumeClosesTheParkedSessionEvenWhenLivenessCannotAnswer pins why that close is
// unconditional rather than gated on DoesSessionExist, which is the shape it had first.
//
// DoesSessionExist is `liveness() == sessionAlive`, so it returns false for two different
// things: "no session", and "no answer" — a socket tmux cannot open for any reason but
// ENOENT, a probe the context cancelled (socketUnreachable, session/tmux/tmux.go). Gating
// on it therefore skips the close for precisely the servers that cannot be reached, which
// is the same population that cannot be killed. The resume then walks on and relaunches
// over a session that may still be running the agent inside the worktree the park deleted
// — #710's own symptom, reached through the guard written to prevent it.
//
// Close is the call that can tell those apart, because it asks the server to do something
// and classifies the refusal: sessionAlreadyGone forgives a session that is genuinely gone,
// and anything else — including this — is a teardown that did not happen.
func TestResumeClosesTheParkedSessionEvenWhenLivenessCannotAnswer(t *testing.T) {
	wt := newTestWorktree(t)
	repoPath := wt.GetRepoPath()
	branch := wt.GetBranchName()

	require.NoError(t, os.WriteFile(filepath.Join(wt.GetWorktreePath(), "work.txt"), []byte("hours of it\n"), 0644))
	require.NoError(t, wt.CommitChanges("feat: the session's whole history"))
	sessionSHA := gitOutput(t, repoPath, "rev-parse", branch)
	require.NoError(t, wt.Remove())

	// tmux's wording for a socket it cannot open, with an errno that is NOT ENOENT: the
	// server may be alive and serving this very session. sessionAlreadyGone matches the
	// connect failure only when paired with "no such file or directory", so this is a real
	// failure to both callers — while liveness reads it as neither alive nor gone.
	unreachable := errors.New("error connecting to /run/user/1000/atrium/default (Permission denied)")
	pty := newRecordingPtyFactory(t, nil)
	sealed := cmd_test.MockCmdExec{
		RunFunc: func(cmd *exec.Cmd) error {
			if tmuxVerb(cmd, "has-session") || tmuxVerb(cmd, "kill-session") {
				return unreachable
			}
			return nil
		},
		OutputFunc: func(*exec.Cmd) ([]byte, error) { return nil, nil },
	}
	ts := tmux.NewSessionWithDeps(context.Background(), "sess", "claude", pty, sealed)
	inst := &Instance{ident: identity{title: "sess"}, status: Paused, started: true, gitWorktree: wt, tmuxSession: ts}

	err := inst.Resume()

	require.ErrorContains(t, err, "parked with",
		"an unreachable socket is not an absent session: the close must be attempted, and its failure must abort")
	require.Empty(t, pty.cmds,
		"the resume launched a second agent over a session it never managed to close")
	tip, exists := git.BranchTip(context.Background(), repoPath, branch)
	require.True(t, exists, "a failed resume deleted the session's branch")
	require.Equal(t, sessionSHA, tip, "the branch must come through untouched")
}
