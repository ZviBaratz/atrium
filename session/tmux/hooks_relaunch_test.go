package tmux

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/cmd/cmd_test"

	"github.com/stretchr/testify/require"
)

// relaunchableTmux models a tmux server across a close and the launch that follows,
// which the existing mocks cannot: startMockExec answers "gone" exactly once and is
// alive forever after, so a second Start is refused as a duplicate, and liveMockExec
// never reports a session gone at all.
//
// The two sides are split the way the real thing is. kill-session and has-session go
// through the executor; new-session goes through the PTY factory, because that is the
// one tmux command Session.start launches as a pty rather than running. A fake that
// watched only the executor would never see the session come back.
type relaunchableTmux struct {
	alive bool
	pty   *MockPtyFactory
}

func newRelaunchableTmux(t *testing.T) *relaunchableTmux {
	t.Helper()
	return &relaunchableTmux{pty: NewMockPtyFactory(t)}
}

func (r *relaunchableTmux) Start(cmd *exec.Cmd) (*os.File, error) {
	if strings.Contains(cmd.String(), "new-session") {
		r.alive = true
	}
	return r.pty.Start(cmd)
}

func (r *relaunchableTmux) Close() { r.pty.Close() }

func (r *relaunchableTmux) exec() cmd_test.MockCmdExec {
	return cmd_test.MockCmdExec{
		RunFunc: func(cmd *exec.Cmd) error {
			s := cmd.String()
			switch {
			case strings.Contains(s, "kill-session"):
				r.alive = false
			case strings.Contains(s, "has-session"):
				if !r.alive {
					// tmux's own wording: Close classifies a kill-session failure by
					// this text (sessionAlreadyGone) to tell an already-dead session
					// from a real teardown failure.
					return errors.New("can't find session: mock")
				}
			}
			return nil
		},
		OutputFunc: func(*exec.Cmd) ([]byte, error) { return []byte("output"), nil },
	}
}

// TestCloseThenRelaunchReArmsTheHookChannel guards the #492 class over the lifecycle
// #710 introduced. Pause now closes the tmux session, and Close sweeps this session's
// hook directory — so a pause deletes the channel claude's status hooks write to, where
// before only a kill did. If the resume's relaunch did not rebuild it, every resumed
// claude session would come back with the working/ready latch, the in-flight sub-agent
// set (#290), the heartbeat (#311) and the effort chip (#325) permanently dark, and
// nothing would say so: readHookRecord answers "no signal" for a missing file exactly
// as it does before the first hook fires, and the poller falls back to the pane scrape.
//
// The signal is exercised at both ends rather than compared as paths, because two paths
// can agree and still be a directory nothing writes to.
func TestCloseThenRelaunchReArmsTheHookChannel(t *testing.T) {
	forceSettingsFlag(t, true)
	srv := newRelaunchableTmux(t)
	s := NewSessionWithDeps(context.Background(), t.Name(), "claude", srv, srv.exec())
	require.NoError(t, s.Start(t.TempDir()))

	dir, settings := hookArtifacts(t, s.snapshotName())
	// The sandbox HOME is shared across a `go test -count=N` run; don't leak into the next.
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	require.FileExists(t, settings, "the launch wrote a settings.json")
	require.NoError(t, UpdateHookState(bakedStateFile(t, settings), HookEventWorking, HookPayload{}, ""))
	rec, ok := s.readHookRecord()
	require.True(t, ok, "the channel carries a signal before the pause")
	require.Equal(t, hookStateWorking, rec.State)

	// The pause.
	require.NoError(t, s.Close())
	require.NoFileExists(t, settings, "Close sweeps this session's hook directory")
	_, ok = s.readHookRecord()
	require.False(t, ok, "and with it the signal — this is what the resume has to rebuild")

	// The resume.
	require.NoError(t, s.Start(t.TempDir()))
	require.FileExists(t, settings, "the relaunch must re-arm the channel it was parked without")

	written := bakedStateFile(t, settings)
	readPath, err := s.HookStateFile()
	require.NoError(t, err)
	require.Equal(t, written, readPath, "the reader follows the relaunch's freshly baked write path")
	require.Equal(t, filepath.Join(dir, "state"), written)

	require.NoError(t, UpdateHookState(written, HookEventReady, HookPayload{}, ""))
	rec, ok = s.readHookRecord()
	require.True(t, ok, "the resumed session's signal arrives")
	require.Equal(t, hookStateReady, rec.State)
}

// TestRenameWhileParkedReKeysTheHookChannelOnResume is the same property over the one
// window a park opens that a kill never did: a session can be renamed while it is
// parked. The name the swept directory was keyed by is still frozen on the Session, and
// the relaunch has to re-key to the current one — the reader is pinned to the frozen
// value on purpose (#492), so a resume that did not re-freeze would read a directory
// belonging to the name the session no longer has.
func TestRenameWhileParkedReKeysTheHookChannelOnResume(t *testing.T) {
	forceSettingsFlag(t, true)
	srv := newRelaunchableTmux(t)
	s := NewSessionWithDeps(context.Background(), t.Name(), "claude", srv, srv.exec())
	require.NoError(t, s.Start(t.TempDir()))

	parkedDir, parkedSettings := hookArtifacts(t, s.snapshotName())
	t.Cleanup(func() { _ = os.RemoveAll(parkedDir) })
	require.FileExists(t, parkedSettings, "the launch wrote a settings.json")

	require.NoError(t, s.Close())
	require.NoError(t, s.Rename("renamed while parked", Prefix()+"parked_rename"))
	require.NoError(t, s.Start(t.TempDir()))

	renamedDir, renamedSettings := hookArtifacts(t, s.snapshotName())
	t.Cleanup(func() { _ = os.RemoveAll(renamedDir) })
	require.NotEqual(t, parkedDir, renamedDir, "precondition: the rename really moved the key")
	require.FileExists(t, renamedSettings, "the relaunch re-keys to the name the session has now")
	require.NoDirExists(t, parkedDir, "and leaves nothing behind under the name it was parked as")

	written := bakedStateFile(t, renamedSettings)
	readPath, err := s.HookStateFile()
	require.NoError(t, err)
	require.Equal(t, written, readPath, "writer and reader agree on the new name")

	require.NoError(t, UpdateHookState(written, HookEventReady, HookPayload{}, ""))
	rec, ok := s.readHookRecord()
	require.True(t, ok, "the renamed, resumed session's signal arrives")
	require.Equal(t, hookStateReady, rec.State)
}
