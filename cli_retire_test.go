package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	cmd_test "github.com/ZviBaratz/atrium/cmd/cmd_test"
	"github.com/ZviBaratz/atrium/internal/outbox"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/session/agent"
	"github.com/ZviBaratz/atrium/session/git"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// cleanProbe answers "nothing at risk, nobody working" for every target — the one
// shape a kill clears. Tests that exercise a refusal override one half of it, so the
// half a test is about is the only thing it mentions.
func cleanProbe() retireProbe {
	return retireProbe{
		stats: func(session.InstanceData) *git.DiffStats { return &git.DiffStats{} },
		busy:  func(session.InstanceData) (bool, error) { return false, nil },
	}
}

// refusingProbe fails if anything asks it. It is how the pause tests prove pause
// does not probe rather than merely that it succeeds anyway: a probe that is never
// called cannot be the reason pause worked.
func refusingProbe(t *testing.T) retireProbe {
	t.Helper()
	return retireProbe{
		stats: func(session.InstanceData) *git.DiffStats {
			t.Error("pause must not compute git stats: it destroys nothing to gate on")
			return &git.DiffStats{}
		},
		busy: func(session.InstanceData) (bool, error) {
			t.Error("pause must not probe the pane")
			return false, nil
		},
	}
}

func retireCmd(t *testing.T, mode outbox.Mode, probe retireProbe, selector string, wait time.Duration) (stdout, stderr string, err error) {
	t.Helper()
	var out, errOut bytes.Buffer
	err = runRetire(&out, &errOut, mode, probe, selector, "", wait)
	return out.String(), errOut.String(), err
}

func spooledRetires(t *testing.T) []outbox.RetireEntry {
	t.Helper()
	entries, err := outbox.ListRetires()
	require.NoError(t, err)
	return entries
}

// TestKillSpoolsARetirementForACleanIdleSession is the core contract: the record the
// drain reads names the resolved target and the verb asked for.
func TestKillSpoolsARetirementForACleanIdleSession(t *testing.T) {
	sandboxDataDir(t)
	d := inst("fix-auth", "/repo/web")
	d.TmuxName = "atrium_web_fix-auth"
	seedInstances(t, d)

	stdout, _, err := retireCmd(t, outbox.ModeKill, cleanProbe(), "fix-auth", 0)
	require.NoError(t, err)
	assert.Contains(t, stdout, "fix-auth")

	entries := spooledRetires(t)
	require.Len(t, entries, 1)
	require.NoError(t, entries[0].Err)
	assert.Equal(t, "fix-auth", entries[0].Retire.Title)
	assert.Equal(t, "/repo/web", entries[0].Retire.Path)
	assert.Equal(t, "atrium_web_fix-auth", entries[0].Retire.TmuxName)
	assert.Equal(t, outbox.ModeKill, entries[0].Retire.Mode)
}

// TestKillRefusesWorkAtRiskAndSpoolsNothing is the gate, and the second assertion is
// the half that matters: a refusal that still spooled would be a kill with an
// explanation printed over it.
func TestKillRefusesWorkAtRiskAndSpoolsNothing(t *testing.T) {
	for _, tc := range []struct {
		name  string
		stats *git.DiffStats
		busy  bool
		says  string
	}{
		{"uncommitted changes", &git.DiffStats{Dirty: true}, false, "uncommitted"},
		{"unpushed commits", &git.DiffStats{Unpushed: 3}, false, "3 unpushed"},
		{"stats that failed", &git.DiffStats{Error: errors.New("no repo")}, false, "could not be established"},
		{"a working agent", &git.DiffStats{}, true, "still working"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sandboxDataDir(t)
			seedInstances(t, inst("fix-auth", "/repo/web"))
			probe := retireProbe{
				stats: func(session.InstanceData) *git.DiffStats { return tc.stats },
				busy:  func(session.InstanceData) (bool, error) { return tc.busy, nil },
			}

			_, _, err := retireCmd(t, outbox.ModeKill, probe, "fix-auth", 0)

			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.says, "the refusal must name the condition that failed")
			assert.Empty(t, spooledRetires(t), "a refused kill spools nothing")
		})
	}
}

// TestKillRefusesAProbeItCouldNotRun: a pane it could not capture is not an idle
// pane. The busy half is the one probe that can fail for a reason unrelated to the
// session — a dead tmux server — and reading that failure as "not busy" would clear
// the gate on the strength of a missing answer.
func TestKillRefusesAProbeItCouldNotRun(t *testing.T) {
	sandboxDataDir(t)
	seedInstances(t, inst("fix-auth", "/repo/web"))
	probe := cleanProbe()
	probe.busy = func(session.InstanceData) (bool, error) {
		return false, errors.New("no server running")
	}

	_, _, err := retireCmd(t, outbox.ModeKill, probe, "fix-auth", 0)

	require.Error(t, err)
	assert.Empty(t, spooledRetires(t))
}

// TestPauseSpoolsWithoutProbing is why pause ships beside kill. It is the escape
// valve for everything the gate refuses, so it must not consult the gate — a pause
// gated on the same conditions would leave an orchestrator with no way to reclaim
// exactly the workers it most needs to.
func TestPauseSpoolsWithoutProbing(t *testing.T) {
	sandboxDataDir(t)
	seedInstances(t, inst("fix-auth", "/repo/web"))

	_, _, err := retireCmd(t, outbox.ModePause, refusingProbe(t), "fix-auth", 0)
	require.NoError(t, err)

	entries := spooledRetires(t)
	require.Len(t, entries, 1)
	assert.Equal(t, outbox.ModePause, entries[0].Retire.Mode)
}

// TestKillRefusesASessionWithNoWorktreeToRead: a direct session never had a
// worktree and a paused one no longer has it, so nothing about either tree can be
// established — the gate's whole precondition is absent. Refusing here rather than
// letting the probe fail names the actual state instead of surfacing a git error.
func TestKillRefusesASessionWithNoWorktreeToRead(t *testing.T) {
	for _, tc := range []struct {
		name string
		d    session.InstanceData
		says string
	}{
		{"direct", directSession("scratch", "/home/zvi/notes"), "direct"},
		{"paused", pausedSession("fix-auth", "/repo/web"), "paused"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sandboxDataDir(t)
			seedInstances(t, tc.d)

			_, _, err := retireCmd(t, outbox.ModeKill, refusingProbe(t), tc.d.Title, 0)

			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.says)
			assert.Empty(t, spooledRetires(t))
		})
	}
}

// TestPauseRefusesWhatPauseCannotDo mirrors Instance.Pause's own refusals at the
// producer, where there is still somebody to tell. A direct session runs in the
// user's own checkout with no worktree to free, so parking it stops a live agent and
// reclaims nothing; an already-paused session has nothing left to do.
func TestPauseRefusesWhatPauseCannotDo(t *testing.T) {
	for _, tc := range []struct {
		name string
		d    session.InstanceData
		says string
	}{
		{"direct", directSession("scratch", "/home/zvi/notes"), "direct"},
		{"already paused", pausedSession("fix-auth", "/repo/web"), "already paused"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sandboxDataDir(t)
			seedInstances(t, tc.d)

			_, _, err := retireCmd(t, outbox.ModePause, refusingProbe(t), tc.d.Title, 0)

			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.says)
			assert.Empty(t, spooledRetires(t))
		})
	}
}

// TestRetireRefusesAnUnresolvableSelector: resolveSession's refusals reach the
// caller unchanged, ambiguity included. That is most of the protection against an
// agent naming the wrong session — it never guesses between two candidates.
func TestRetireRefusesAnUnresolvableSelector(t *testing.T) {
	sandboxDataDir(t)
	seedInstances(t, inst("api", "/repo/a"), inst("api-v2", "/repo/a"))

	_, _, err := retireCmd(t, outbox.ModeKill, refusingProbe(t), "nope", 0)
	require.Error(t, err)
	assert.Empty(t, spooledRetires(t))
}

// TestRetireRefusesANegativeWait: cobra parses "--wait -5s" happily, and left to a
// wait > 0 test it would silently mean "do not wait" — so a caller that fat-fingered
// a sign would be told the kill was queued and never learn what became of it. Same
// guard, same reason, as runSend's and runNew's.
func TestRetireRefusesANegativeWait(t *testing.T) {
	sandboxDataDir(t)
	seedInstances(t, inst("fix-auth", "/repo/web"))

	_, _, err := retireCmd(t, outbox.ModeKill, cleanProbe(), "fix-auth", -5*time.Second)
	require.Error(t, err)
	assert.Empty(t, spooledRetires(t))
}

// TestRetireWarnsWhenNothingIsDraining: the record is durable, so this is a warning
// and never a refusal — but an agent that is not told will wait for a teardown that
// is not coming until a TUI starts.
func TestRetireWarnsWhenNothingIsDraining(t *testing.T) {
	sandboxDataDir(t)
	seedInstances(t, inst("fix-auth", "/repo/web"))

	_, stderr, err := retireCmd(t, outbox.ModeKill, cleanProbe(), "fix-auth", 0)
	require.NoError(t, err)
	assert.Contains(t, stderr, "no atrium TUI is running")
}

// TestRetireWaitReportsARejection closes the loop the receipt protocol exists for: a
// drain that refuses must reach the blocked producer as a non-zero exit carrying the
// reason, not as a timeout and not as a success.
func TestRetireWaitReportsARejection(t *testing.T) {
	sandboxDataDir(t)
	seedInstances(t, inst("fix-auth", "/repo/web"))
	_, _, err := retireCmd(t, outbox.ModeKill, cleanProbe(), "fix-auth", 0)
	require.NoError(t, err)
	entries := spooledRetires(t)
	require.Len(t, entries, 1)
	require.NoError(t, outbox.Reject(entries[0].Path, "it has uncommitted changes"))

	// A second kill of the same session finds the receipt from the first.
	_, _, err = retireCmd(t, outbox.ModeKill, cleanProbe(), "fix-auth", 0)
	require.NoError(t, err)
	fresh := spooledRetires(t)
	require.Len(t, fresh, 1)
	require.NoError(t, outbox.Reject(fresh[0].Path, "its agent is still working"))

	err = awaitSpool(fresh[0].Path, "", 50*time.Millisecond, spoolWaitCopy{
		refused:  "atrium did not retire the session",
		timedOut: func() string { return "timed out" },
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "its agent is still working")
}

// TestBusyProbeReadsThePaneThroughTheAgentAdapter is the live probe's tmux half. It
// pins the composition rather than the classification: the pane is captured for the
// session's own tmux name and judged by the adapter its recorded program resolves to,
// so a probe wired to the wrong name or a fixed adapter would clear a busy session.
func TestBusyProbeReadsThePaneThroughTheAgentAdapter(t *testing.T) {
	d := inst("fix-auth", "/repo/web")
	d.TmuxName = "atrium_web_fix-auth"

	working := &fakeTmux{content: "· Thinking… (3s)\n\n  esc to interrupt\n"}
	busy, err := busyFromPane(context.Background(), working.exec(), d)
	require.NoError(t, err)
	assert.True(t, busy, "claude's busy marker in the footer means working")
	assert.Contains(t, working.argvFor("capture-pane"), "capture-pane")

	idle := &fakeTmux{content: "> \n\n  ? for shortcuts\n"}
	busy, err = busyFromPane(context.Background(), idle.exec(), d)
	require.NoError(t, err)
	assert.False(t, busy)
}

// TestBusyProbeFailsRatherThanGuessing: a capture that errors must not answer
// "idle". This is the input the gate treats as the difference between a session that
// is finished and one it simply could not look at.
func TestBusyProbeFailsRatherThanGuessing(t *testing.T) {
	d := inst("fix-auth", "/repo/web")
	d.TmuxName = "atrium_web_fix-auth"
	broken := cmd_test.MockCmdExec{
		RunFunc:    func(*exec.Cmd) error { return errors.New("no server") },
		OutputFunc: func(*exec.Cmd) ([]byte, error) { return nil, errors.New("no server running") },
	}

	_, err := busyFromPane(context.Background(), broken, d)
	assert.Error(t, err)
}

// TestStatsProbeReadsARealWorktree is the live probe's git half, and the one test
// here that would catch a worktree rehydrated from the wrong stored fields. It runs
// real git against a real worktree, because that is the only way to tell
// NewWorktreeFromStorage wired with the right columns from one wired with plausible
// wrong ones — a BaseCommitSHA left empty makes RepoStats return an error rather than
// a wrong number, which the gate reports as unestablished for every session forever.
func TestStatsProbeReadsARealWorktree(t *testing.T) {
	d, worktree := sessionInRealRepo(t)

	stats := statsFromWorktree(context.Background(), d, "")
	require.NoError(t, stats.Error, "a rehydrated worktree must be readable")
	assert.False(t, stats.Dirty, "a freshly created worktree is clean")

	require.NoError(t, os.WriteFile(filepath.Join(worktree, "scratch.txt"), []byte("wip"), 0o644))
	dirty := statsFromWorktree(context.Background(), d, "")
	require.NoError(t, dirty.Error)
	assert.True(t, dirty.Dirty, "an untracked file is uncommitted work a kill would destroy")
}

// directSession is an instance with no repository at all: no worktree, no branch.
func directSession(title, path string) session.InstanceData {
	d := inst(title, path)
	d.Direct = true
	return d
}

// pausedSession is an instance parked with its branch kept and its worktree gone.
func pausedSession(title, path string) session.InstanceData {
	d := inst(title, path)
	d.Status = session.Paused
	return d
}

// sessionInRealRepo builds a real repository with a real session worktree on a
// session branch, and returns the InstanceData a TUI would have persisted for it
// alongside the worktree path. The fixture for the one test that must not fake git.
func sessionInRealRepo(t *testing.T) (session.InstanceData, string) {
	t.Helper()
	repo := gitRepoWithBranches(t)
	worktree := filepath.Join(t.TempDir(), "wt")

	run := func(args ...string) string {
		t.Helper()
		c := exec.CommandContext(t.Context(), "git", args...)
		c.Dir = repo
		c.Env = append(os.Environ(),
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
		out, err := c.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
		return string(bytes.TrimSpace(out))
	}
	base := run("rev-parse", "HEAD")
	run("worktree", "add", "-b", "zvi/fix-auth", worktree)

	d := inst("fix-auth", repo)
	d.Branch = "zvi/fix-auth"
	d.Worktree = session.GitWorktreeData{
		RepoPath:      repo,
		WorktreePath:  worktree,
		SessionName:   "fix-auth",
		BranchName:    "zvi/fix-auth",
		BaseCommitSHA: base,
		BaseRef:       "main",
	}
	return d, worktree
}

// TestBusyProbeRefusesAnAgentItCannotObserve is the hole the dead nil-adapter branch
// was groping at and missed. agent.Resolve never returns nil — it falls back to
// Generic — so the case that actually exists is an adapter that resolves fine and
// declares nothing to detect a turn with. aider is one and Generic is the other, and
// for both HasBusyMarker is unconditionally false, which the gate would read as "idle,
// go ahead".
//
// That is precisely the failure the gate exists to prevent: an answer that means "I
// cannot tell" arriving as one that means "nothing is happening". So the probe refuses
// on the adapter rather than on the pane, and it does so before spending a capture.
func TestBusyProbeRefusesAnAgentItCannotObserve(t *testing.T) {
	for _, program := range []string{"aider", "some-unknown-agent"} {
		t.Run(program, func(t *testing.T) {
			d := inst("fix-auth", "/repo/web")
			d.Program = program
			d.TmuxName = "atrium_web_fix-auth"
			require.NotNil(t, agent.Resolve(program), "precondition: an adapter always resolves")
			require.False(t, agent.Resolve(program).CanDetectBusy(),
				"precondition: this adapter cannot establish a turn in flight")

			f := &fakeTmux{content: "whatever\n"}
			_, err := busyFromPane(context.Background(), f.exec(), d)

			require.Error(t, err)
			assert.Contains(t, err.Error(), program)
			assert.Nil(t, f.argvFor("capture-pane"),
				"and refuses before spending a tmux call it cannot use the output of")
		})
	}
}

// TestKillRefusesAnAgentItCannotObserve is the end of that path: the refusal reaches
// the caller and nothing is spooled.
func TestKillRefusesAnAgentItCannotObserve(t *testing.T) {
	sandboxDataDir(t)
	d := inst("fix-auth", "/repo/web")
	d.Program = "aider"
	seedInstances(t, d)

	probe := liveRetireProbe(context.Background(), (&fakeTmux{content: "x"}).exec())
	_, _, err := retireCmd(t, outbox.ModeKill, probe, "fix-auth", 0)

	require.Error(t, err)
	assert.Empty(t, spooledRetires(t))
}
