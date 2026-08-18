package app

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
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/session/tmux"

	"github.com/stretchr/testify/require"
)

// fakeServer is a tmux server that holds at most one session: any new-session (which
// arrives through the pty factory) puts it up, kill-session takes it away, and die
// stands in for the agent exiting on its own. has-session answers from that one bit,
// in tmux's own wording — Session.Close and liveness both classify a failure by that
// text, so a fake with its own phrasing would make every death read as a probe that
// could not answer.
type fakeServer struct {
	up       atomic.Bool
	opened   []*os.File
	launches atomic.Int32
}

func (f *fakeServer) Start(cmd *exec.Cmd) (*os.File, error) {
	if slices.Contains(cmd.Args, "new-session") {
		f.up.Store(true)
		f.launches.Add(1)
	}
	file, err := os.CreateTemp("", "pty-stub")
	if err != nil {
		return nil, err
	}
	f.opened = append(f.opened, file)
	return file, nil
}

// Close releases the pty stubs Start handed out (tmux.PtyFactory).
func (f *fakeServer) Close() {
	for _, file := range f.opened {
		_ = file.Close()
		_ = os.Remove(file.Name())
	}
	f.opened = nil
}

func (f *fakeServer) exec() cmd_test.MockCmdExec {
	return cmd_test.MockCmdExec{
		RunFunc: func(cmd *exec.Cmd) error {
			switch {
			case slices.Contains(cmd.Args, "kill-session"):
				f.up.Store(false)
			case slices.Contains(cmd.Args, "has-session"):
				if !f.up.Load() {
					return errors.New("can't find session: resumed")
				}
			}
			return nil
		},
		OutputFunc: func(*exec.Cmd) ([]byte, error) { return nil, nil },
	}
}

// die makes the agent's pane vanish the way a crash does: the session goes, nobody
// having asked for it.
func (f *fakeServer) die() { f.up.Store(false) }

// repairableLostInstance returns a live claude session whose LAST launch resumed a
// conversation and whose pane has since died — the state RepairResumingLaunch acts on —
// built entirely through the public API, so what it proves about the caller is real.
//
// A direct session, because it is the one kind Resume can round-trip without a git
// worktree; the transcript under the sandboxed HOME is what makes startResuming elect
// `--continue` rather than a blank launch.
func repairableLostInstance(t *testing.T) (*session.Instance, *fakeServer) {
	t.Helper()
	dir := t.TempDir()
	writeClaudeTranscript(t, dir)

	inst, err := session.NewInstance(session.InstanceOptions{
		Title: "resumed", Path: dir, Program: "claude", Direct: true,
	})
	require.NoError(t, err)
	srv := &fakeServer{}
	t.Cleanup(srv.Close)
	inst.SetTmuxSession(tmux.NewSessionWithDeps(context.Background(), "resumed", "claude", srv, srv.exec()))
	require.NoError(t, inst.Start(true))
	require.NoError(t, inst.RecoverLostSession(), "park it, so the resume below is a real relaunch")
	require.NoError(t, inst.Resume())
	require.Equal(t, int32(2), srv.launches.Load(), "precondition: the resume launched the agent again")

	srv.die()
	return inst, srv
}

// writeClaudeTranscript drops a non-empty session JSONL where the claude transcript
// reader looks for cwd's — under the sandboxed HOME, since the instance carries no
// account-pinned config dir.
func writeClaudeTranscript(t *testing.T, cwd string) {
	t.Helper()
	sanitized := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			return r
		}
		return '-'
	}, cwd)
	dir := filepath.Join(os.Getenv("HOME"), ".claude", "projects", sanitized)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "session.jsonl"), []byte("{}\n"), 0o644))
}

// The wiring, through the real caller: a session whose RESUMING launch died at birth is
// repaired rather than parked (#712). Parking it is what #699 cost — the worktree
// Atrium had just rebuilt is removed again — and the repair has to be tried BEFORE
// RecoverLostSession, which cannot be undone.
//
// Driven through the debounce rather than by calling the repair directly, because the
// repair itself is guarded in session; what is unproven here is that anything calls it.
func TestRecoverLostInstances_RepairsAResumingLaunchInsteadOfParkingIt(t *testing.T) {
	inst, srv := repairableLostInstance(t)
	strikes := map[*session.Instance]int{}
	lost := []instanceMetaResult{{instance: inst, sessionLost: true}}

	var recoveries []lostRecovery
	for range lostSessionRecoverThreshold {
		recoveries = recoverLostInstances(lost, strikes, nil)
	}

	require.Len(t, recoveries, 1, "the repair is still reported — it is the one recovery a user cannot see")
	require.True(t, recoveries[0].relaunchedBlank)
	require.NoError(t, recoveries[0].err)
	require.False(t, inst.Paused(), "a session that came back must not also be parked")
	require.True(t, srv.up.Load(), "and its agent must be running again")
	require.Equal(t, int32(3), srv.launches.Load(), "which takes exactly one more launch")
	require.Empty(t, strikes, "a live session carries no strikes into the next tick")

	resumed, known := inst.ResumedConversation()
	require.False(t, resumed, "the relaunch was blank, and the notice reads this")
	require.True(t, known)
}

// The control: a session that did NOT die out of a resuming launch still parks. Without
// it, a repair wired to fire unconditionally would pass the test above.
func TestRecoverLostInstances_StillParksASessionWithNoResumeToRepair(t *testing.T) {
	dir := t.TempDir() // deliberately no transcript: the relaunch below is blank
	inst, err := session.NewInstance(session.InstanceOptions{
		Title: "blank", Path: dir, Program: "claude", Direct: true,
	})
	require.NoError(t, err)
	srv := &fakeServer{}
	t.Cleanup(srv.Close)
	inst.SetTmuxSession(tmux.NewSessionWithDeps(context.Background(), "blank", "claude", srv, srv.exec()))
	require.NoError(t, inst.Start(true))
	require.NoError(t, inst.RecoverLostSession())
	require.NoError(t, inst.Resume())
	srv.die()

	strikes := map[*session.Instance]int{}
	lost := []instanceMetaResult{{instance: inst, sessionLost: true}}
	var recoveries []lostRecovery
	for range lostSessionRecoverThreshold {
		recoveries = recoverLostInstances(lost, strikes, nil)
	}

	require.Len(t, recoveries, 1)
	require.False(t, recoveries[0].relaunchedBlank, "there was no resume flag to drop")
	require.True(t, inst.Paused(), "so the ordinary park is what must happen")
}
