package app

import (
	"context"
	"errors"
	"testing"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/ui"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/require"
)

// newKillHome builds a home with real storage and one unstarted instance in the
// list, ready to have a kill driven through it.
func newKillHome(t *testing.T) (*home, *session.Instance) {
	t.Helper()
	storage, err := session.NewStorage(config.DefaultState())
	require.NoError(t, err)

	s := spinner.New()
	list := ui.NewList(&s)
	inst, err := session.NewInstance(session.InstanceOptions{
		Title: "doomed", Path: t.TempDir(), Program: "echo", Direct: true,
	})
	require.NoError(t, err)
	list.AddInstance(inst)()
	list.SetSelectedInstance(0)

	return &home{
		ctx:          context.Background(),
		state:        stateDefault,
		appConfig:    config.DefaultConfig(),
		appState:     config.DefaultState(),
		storage:      storage,
		list:         list,
		menu:         ui.NewMenu(),
		errBox:       ui.NewErrBox(),
		lostStrikes:  map[*session.Instance]int{},
		notifySeen:   map[*session.Instance]*notifyState{},
		tabbedWindow: ui.NewTabbedWindow(ui.NewPreviewPane(), ui.NewDiffPane(), ui.NewTerminalPane(context.Background())),
	}, inst
}

// TestKill_RunsOffTheUpdateThreadBehindItsLabel is the shape assertion for the
// whole leg: a kill must name itself before it starts and do its work in a
// goroutine.
//
// It used to do neither. A confirm with no label ran INLINE on the update thread
// (that was the documented behaviour, not an oversight — the teardown touches the
// list, storage and the terminal pane), so the app simply stopped for the duration:
// ~6 subprocesses plus `git worktree remove -f`, which is a recursive delete and
// takes seconds on a fat worktree. With no progress row at all, a slow kill was
// indistinguishable from a hang.
func TestKill_RunsOffTheUpdateThreadBehindItsLabel(t *testing.T) {
	h, inst := newKillHome(t)

	h.confirmKill(inst)
	require.Equal(t, "killing 'doomed'…", h.pendingConfirmBusyLabel,
		"the kill must name its target — this dialog need not target the selected row")

	_, cmd := h.handleConfirmState(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	require.True(t, h.actionInFlight, "a labelled confirm runs off the update thread")
	require.Contains(t, h.menu.String(), "killing", "and shows that label while it runs")

	// The row must still be there: nothing is removed until the I/O reports back.
	require.Len(t, h.list.GetInstances(), 1, "the model must not change until the teardown returns")
	require.NotNil(t, cmd)
}

// TestKill_StorageFailureStillRemovesTheRowAndSaysSo covers the ordering inversion
// the split introduces. The teardown now runs BEFORE the storage delete, so a
// failed delete can no longer abort the removal — tmux and the worktree are
// already gone. Leaving the row would point it at a deleted worktree; leaving the
// storage entry silently would resurrect a ghost at next launch. So: remove the
// row, and name the failure.
func TestKill_StorageFailureStillRemovesTheRowAndSaysSo(t *testing.T) {
	h, inst := newKillHome(t)
	// The instance was never persisted, so DeleteInstance reports "instance not
	// found" — a real storage failure for this path, and the one easiest to stage.
	h.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	h.state = stateDefault

	cmd := h.applyKillDone(killDoneMsg{outcome: killOutcome{inst: inst}})

	require.Empty(t, h.list.GetInstances(), "the row must go even when its record cannot be cleared")
	require.NotNil(t, cmd, "the failure must be reported, not swallowed")
	require.Contains(t, h.menu.NoticeText(), "saved state could not be cleared",
		"a persisted ghost must be named, not left silent")
}

// TestKill_RefusedTeardownLeavesTheSessionAlone: the base-repo branch check runs in
// the goroutine and refuses before touching anything, so the handler must not
// remove a row for a kill that never happened.
func TestKill_RefusedTeardownLeavesTheSessionAlone(t *testing.T) {
	h, inst := newKillHome(t)

	cmd := h.applyKillDone(killDoneMsg{
		outcome: killOutcome{inst: inst},
		refused: errors.New("branch for doomed is checked out in the main repo"),
	})

	require.Len(t, h.list.GetInstances(), 1, "a refused kill must leave the session in place")
	require.NotNil(t, cmd, "and must say why")
}

// TestKill_RetiringInstanceIsNotLostRecovered is the hazard the async window
// creates. For as long as the teardown runs the row still exists, so the 500ms
// metadata poll observes the dying pane, and recoverLostInstances would park it as
// Paused with a "terminal exited" notice. That notice is a lie AND it overwrites
// the kill's own progress row — the two failures compound.
func TestKill_RetiringInstanceIsNotLostRecovered(t *testing.T) {
	h, inst := newKillHome(t)
	h.confirmKill(inst)
	h.handleConfirmState(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	require.True(t, h.retiring[inst], "arming a kill must mark the session as retiring")

	// The poll reports the pane as gone for as many ticks as the recovery
	// threshold needs — exactly what it sees while a teardown is in flight.
	lost := []instanceMetaResult{{instance: inst, sessionLost: true}}
	for range lostSessionRecoverThreshold + 1 {
		require.Empty(t, recoverLostInstances(lost, h.lostStrikes, h.retiring),
			"a session being deliberately torn down must never be 'recovered' as paused")
	}
	require.Zero(t, h.lostStrikes[inst], "and must not even accumulate strikes")

	// The same results without the retiring mark DO recover — otherwise this test
	// would pass against a recovery path that never fires at all.
	require.NotEmpty(t, recoverLostInstances(lost, map[*session.Instance]int{
		inst: lostSessionRecoverThreshold - 1,
	}, nil), "control: an unmarked lost session is still recovered")
}

// TestBatchKill_MutatesTheModelOnlyOnTheMainThread: the batch is where the freeze
// was worst — ten sessions is ~60 subprocesses, ten recursive worktree deletes and
// ten full state.json rewrites. The goroutine may now do all of that, but it must
// not touch the list or storage; those land when the result comes back.
func TestBatchKill_MutatesTheModelOnlyOnTheMainThread(t *testing.T) {
	h, inst := newKillHome(t)

	cmd := h.killInstances([]*session.Instance{inst}, "Kill 1 session?", "x")
	require.Nil(t, cmd, "confirmAction stages the dialog and returns nil")
	require.Equal(t, "killing 1 session…", h.pendingConfirmBusyLabel)

	_, run := h.handleConfirmState(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	require.NotNil(t, run)
	msg := run()
	require.Len(t, h.list.GetInstances(), 1,
		"the teardown goroutine must not remove rows — that is the update thread's job")

	// Unwrap the async envelope and apply it, as Update does.
	done, ok := msg.(asyncActionDoneMsg)
	require.True(t, ok, "a labelled action returns through asyncActionDoneMsg, got %T", msg)
	h.Update(done.result)
	require.Empty(t, h.list.GetInstances(), "applying the result is what removes the row")
}
