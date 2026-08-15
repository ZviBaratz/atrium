package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ZviBaratz/atrium/internal/testutil"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/session/tmux"
	"github.com/ZviBaratz/atrium/ui"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/require"
)

// termShellOnSocket reports whether the instance's terminal shell is alive on Atrium's
// tmux socket, and registers a cleanup that reaps it either way.
//
// The cache cannot answer this, which is the whole point: HasTerminalSession asks
// whether an entry names a shell, and #707 is about a shell that keeps running in a
// deleted directory whatever the map says. ui/terminal_test.go has the same probe, but
// both it and terminalKey are package-private to ui; app re-derives it from the exported
// half rather than growing a production accessor for a test.
//
// It asks the instance for the name rather than appending a suffix of its own: since #708
// the shell's name is OWNED (Instance.termName), so a session that has been renamed since
// its shell was created no longer derives it. Falling back to the mint keeps the probe
// honest for a session whose shell was never created — and for one whose shell was just
// reaped, since the reap releases the name.
func termShellOnSocket(t *testing.T, inst *session.Instance) bool {
	t.Helper()
	name := inst.TerminalSessionName()
	if name == "" {
		name = inst.MintTerminalSessionName()
	}
	return shellNameOnSocket(t, name)
}

// shellNameOnSocket is the same probe against an exact name, for the names an instance no
// longer computes: a reap releases the owned name, so after one the instance can only
// report what a NEW shell would be called, not what the one under test was.
func shellNameOnSocket(t *testing.T, name string) bool {
	t.Helper()
	require.NotEmpty(t, name, "a shell probe needs a name")
	probe := tmux.NewSessionWithName(context.Background(), name, "probe", "/bin/sh")
	t.Cleanup(func() { _ = probe.Close() })
	return probe.DoesSessionExist()
}

// strandedShellHome puts a real, started git session on the terminal tab with a real
// shell created and cached for it, and returns the home and the session's repo path.
// Everything from here on is driven through production entry points.
func strandedShellHome(t *testing.T) (*home, *session.Instance, string) {
	t.Helper()
	inst, repo := gitSession(t, "stranded")

	h := newCreateFormHome(t)
	withStorage(t, h)
	h.spinner = spinner.New()
	h.lostStrikes = map[*session.Instance]int{}
	h.list.AddInstance(inst)()
	h.list.SetSelectedInstance(0)
	h.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	h.state = stateDefault

	h.tabbedWindow.SetActiveTab(ui.TerminalTab)
	t.Cleanup(h.tabbedWindow.CloseTerminal)

	// The production create path: Update resolves a target, the Cmd it returns runs
	// EnsureSession off the update thread.
	_, cmd := h.Update(paneFrameMsg{at: time.Now()})
	require.NotNil(t, cmd, "precondition: the terminal tab must arm a capture")
	cmd()

	require.True(t, h.tabbedWindow.HasTerminalSession(inst), "precondition: the shell is cached")
	require.True(t, termShellOnSocket(t, inst), "precondition: the shell is alive on the socket")
	return h, inst, repo
}

// pressPause drives the real p keybinding and walks its message chain to the
// handler, so what handlePauseDone sees is the message production builds —
// including the WorkingDirGone sample taken in the goroutine. A test that hand-built
// pauseDoneMsg would pass even if that sample were never taken.
//
// Two hops, not one: beginAsyncAction wraps the action's result in an
// asyncActionDoneMsg, and that case re-emits the inner result as a Cmd rather than
// dispatching it inline, so the middle Cmd has to be run too.
func pressPause(t *testing.T, h *home) {
	t.Helper()
	_, cmd := h.handleKeyPress(keyMsg("p"))
	require.NotNil(t, cmd, "precondition: p must start a pause")

	wrapped, ok := cmd().(asyncActionDoneMsg)
	require.True(t, ok, "precondition: the pause runs as an async action")

	_, cmd = h.Update(wrapped)
	require.NotNil(t, cmd, "precondition: the async-done case must re-emit its result")
	inner, ok := cmd().(pauseDoneMsg)
	require.True(t, ok, "precondition: that result is the pause outcome")

	h.Update(inner)
}

// The end-to-end claim, on a real tmux socket: a pause that fails after removing the
// worktree must not leave its shell running in the deleted directory.
//
// Before this fix the shell below survived the pause, and nothing collected it
// afterwards — TabbedWindow.CloseTerminal has no production caller, so it outlived
// the process and the next run's EnsureSession adopted it off the socket, leaving
// `atrium reset` as the only reaper. Verified out of band: such a shell reports the
// right path from $PWD, shows an empty directory, and cannot read a file that exists
// at that path, because it holds the unlinked inode rather than the one a later
// resume re-creates.
//
// The failure is manufactured the way TestPause_RemoveFailureFallsBackToParkedPaused
// does — delete the base repo, so git cannot operate on the worktree — which drives
// pause() down a branch that errors AND frees the directory via its os.RemoveAll
// fallback. That combination is the one handlePauseDone used to skip entirely.
func TestPauseFailure_ReapsTheShellLeftInTheRemovedWorktree(t *testing.T) {
	testutil.RequireTmux(t)
	h, inst, repo := strandedShellHome(t)
	wt, err := inst.GetGitWorktree()
	require.NoError(t, err)
	worktreePath := wt.GetWorktreePath()

	require.NoError(t, os.RemoveAll(repo), "make git unable to operate on the worktree")

	pressPause(t, h)

	require.True(t, inst.Paused(), "precondition: the failed pause still parked the session")
	require.NoDirExists(t, worktreePath, "precondition: the failed pause removed the worktree anyway")
	require.False(t, termShellOnSocket(t, inst),
		"the shell must not still be running on the socket in a worktree that no longer exists")
}

// The negative control, on the same socket: the one pause failure that KEEPS its
// worktree must keep its shell too. When the auto-pause commit fails, pause() leaves
// the worktree and the uncommitted work on disk deliberately (#270) — and that shell
// is sitting in a live directory, holding the work the user is about to rescue.
//
// This is the case that rejects the tempting fix. "Reap when the pause errored"
// passes every assertion in the test above and kills the shell here.
func TestPauseFailure_KeepsTheShellWhenTheWorktreeSurvives(t *testing.T) {
	testutil.RequireTmux(t)
	h, inst, repo := strandedShellHome(t)
	wt, err := inst.GetGitWorktree()
	require.NoError(t, err)
	worktreePath := wt.GetWorktreePath()

	// Break the identity so the auto-pause commit fails deterministically, and dirty
	// the worktree so pause attempts it at all. useConfigOnly is what makes this
	// portable: git auto-detects user@host on some platforms otherwise.
	runGitIn(t, repo, "config", "user.useConfigOnly", "true")
	runGitIn(t, repo, "config", "--unset", "user.email")
	runGitIn(t, repo, "config", "--unset", "user.name")
	wip := filepath.Join(worktreePath, "wip.txt")
	require.NoError(t, os.WriteFile(wip, []byte("uncommitted work\n"), 0o644))

	pressPause(t, h)

	require.True(t, inst.Paused(), "precondition: the failed pause still parked the session")
	require.FileExists(t, wip, "precondition: the WIP pause could not commit is left on disk")
	require.True(t, termShellOnSocket(t, inst),
		"the shell in a worktree the pause deliberately kept must survive: it is the one the user rescues the WIP with")
}

// Both assertions above are about the socket rather than the cache, deliberately.
// HasTerminalSession cannot answer "is it still cached" here: CaptureTarget refuses
// a Paused instance before it ever looks in the map (ui/terminal.go:244), so it
// reports false for both outcomes. The socket is also the observable that matters —
// the cache is self-correcting once the session is dead, since EnsureSession drops
// an entry whose DoesSessionExist comes back false and creates a fresh shell.

// #708's own scenario, end to end on a real socket and through production entry points
// only: rename a session with an open terminal tab, then pause it SUCCESSFULLY. No failure
// and no race is required — the ordinary happy path was enough.
//
// The shell's cache key used to be the instance's tmux name, which AdoptRename rewrites, so
// handlePauseDone's reap computed a name no entry used and closed nothing. The worktree
// came out from under a live shell that nothing named, and since TabbedWindow.CloseTerminal
// has no production caller it outlived the process too.
//
// Unlike the pause-failure tests above this one uses a real tmux rename: gitSession starts a
// real agent session, so Instance.Rename moves it on the socket while its `_term` sibling
// stays where it was — which is the asymmetry the whole fix is about.
func TestRenameThenPause_ReapsTheShellUnderItsPreRenameName(t *testing.T) {
	testutil.RequireTmux(t)
	h, inst, _ := strandedShellHome(t)

	shell := inst.TerminalSessionName()
	require.NotEmpty(t, shell, "precondition: the shell's name is owned once it is created")
	agentBefore := inst.TmuxSessionName()

	// The production rename: the same Cmd the R key builds, then the handler that adopts
	// the identity it earned. Nothing here touches the terminal pane, which is the point.
	msg, ok := renameIOCmd(inst, inst.Title+"-renamed", "")().(renameDoneMsg)
	require.True(t, ok, "precondition: renameIOCmd must report a rename outcome")
	require.NoError(t, msg.err, "precondition: this rename must succeed")
	h.Update(msg)

	require.NotEqual(t, agentBefore, inst.TmuxSessionName(),
		"precondition: the rename moved the agent session's name")
	require.Equal(t, shell, inst.TerminalSessionName(),
		"the shell keeps the name it was created under")

	wt, err := inst.GetGitWorktree()
	require.NoError(t, err)
	worktreePath := wt.GetWorktreePath()

	pressPause(t, h)

	require.True(t, inst.Paused(), "precondition: the pause parked the session")
	require.NoDirExists(t, worktreePath, "precondition: the pause removed the worktree")
	require.False(t, shellNameOnSocket(t, shell),
		"the renamed session's shell survived the pause under its pre-rename name, "+
			"in a worktree that no longer exists, with `atrium reset` the only thing left to reap it")
}
