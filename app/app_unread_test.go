package app

import (
	"context"
	"testing"
	"time"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/ui"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/stretchr/testify/require"
)

// newUnreadHome builds a minimal home with one selected, unread-Ready instance,
// mirroring the TestInstanceStartedMsgSetsRunning harness.
func newUnreadHome(t *testing.T) (*home, *session.Instance) {
	t.Helper()
	spin := spinner.New(spinner.WithSpinner(spinner.MiniDot))
	list := ui.NewList(&spin)

	inst, err := session.NewInstance(session.InstanceOptions{
		Title: "unread", Path: t.TempDir(), Program: "echo",
	})
	require.NoError(t, err)
	list.AddInstance(inst)
	list.SelectInstance(inst)
	inst.SetStatus(session.Running)
	inst.SetStatus(session.Ready) // the edge that flags unread
	require.True(t, inst.Unread())

	appState := config.DefaultState()
	storage, err := session.NewStorage(appState)
	require.NoError(t, err)

	return &home{
		ctx:          context.Background(),
		state:        stateDefault,
		appConfig:    config.DefaultConfig(),
		appState:     appState,
		storage:      storage,
		list:         list,
		menu:         ui.NewMenu(),
		tabbedWindow: ui.NewTabbedWindow(ui.NewPreviewPane(), ui.NewDiffPane(), ui.NewTerminalPane(context.Background())),
	}, inst
}

// markSeenAfterDwell must clear unread only once BOTH floors pass — the row has
// been selected for the dwell, and the unread state has been visible for the
// dwell (so a quick-send reply stays bright even on an already-selected row).
func TestMarkSeenAfterDwell_DualFloors(t *testing.T) {
	h, inst := newUnreadHome(t)
	// A long-selected row — the quick-send scenario, where the user fired a
	// prompt without moving the selection and the reply just landed.
	h.selectedSince = time.Now().Add(-time.Hour)

	// Selected long enough, but the unread flag is fresh → stays bright.
	h.markSeenAfterDwell(inst.UnreadAt().Add(readDwell / 2))
	require.True(t, inst.Unread(), "a fresh unread must stay bright through the dwell floor")

	// Both floors satisfied → marked seen.
	h.markSeenAfterDwell(inst.UnreadAt().Add(readDwell + time.Second))
	require.False(t, inst.Unread(), "dwell past both floors must mark the selection seen")
}

// A selection younger than the dwell must not be marked: cursor travel through
// rows (each selected for a fraction of a second) must leave their state intact.
func TestMarkSeenAfterDwell_SelectionFloor(t *testing.T) {
	h, inst := newUnreadHome(t)
	now := inst.UnreadAt().Add(readDwell + time.Second) // unread floor satisfied
	h.selectedSince = now.Add(-readDwell / 2)           // selection floor not

	h.markSeenAfterDwell(now)

	require.True(t, inst.Unread(), "a just-selected row must not be marked seen (cursor travel)")
}

// On startup the first preview tick runs markSeenAfterDwell before
// instanceChanged has ever stamped selectedSince, so the field is still its
// zero value — no dwell can have been observed yet, and the tick must leave the
// unread state alone. Regression test: the zero value used to read as "selected
// ~forever", wiping a restored unread bit ~100ms after launch.
func TestMarkSeenAfterDwell_ZeroSelectedSince(t *testing.T) {
	h, inst := newUnreadHome(t)
	// selectedSince deliberately left at its zero value (pre-first-tick state);
	// the unread floor is satisfied so only the missing selection stamp blocks.

	h.markSeenAfterDwell(inst.UnreadAt().Add(readDwell + time.Second))

	require.True(t, inst.Unread(), "no dwell exists before the first selection stamp; unread must survive the first tick")
}

// The dwell only runs in the default UI state: with an overlay up (help, new
// session, confirm, …) the user is not looking at the preview pane.
func TestMarkSeenAfterDwell_GatedOnDefaultState(t *testing.T) {
	h, inst := newUnreadHome(t)
	h.selectedSince = time.Now().Add(-time.Minute)
	h.state = stateHelp

	h.markSeenAfterDwell(time.Now().Add(time.Minute))

	require.True(t, inst.Unread(), "an overlay must block the dwell from marking seen")
}

// A nil selection must be a no-op, not a panic.
func TestMarkSeenAfterDwell_NilSelection(t *testing.T) {
	h, _ := newUnreadHome(t)
	spin := spinner.New(spinner.WithSpinner(spinner.MiniDot))
	h.list = ui.NewList(&spin) // empty list → nil selection

	require.NotPanics(t, func() { h.markSeenAfterDwell(time.Now()) })
}

// Attaching is the strongest form of visiting: attachExec must mark the target
// seen before handing the terminal over.
func TestAttachExec_MarksTargetSeen(t *testing.T) {
	h, inst := newUnreadHome(t)

	_ = h.attachExec(func() (chan struct{}, error) { return make(chan struct{}), nil }, inst)

	require.False(t, inst.Unread(), "attaching must mark the session seen")
}

// Detach arms the one-shot suppression: an agent that finished while the user
// was attached (watching it) settles Running→Ready on the post-detach poll,
// which must not flag unread.
func TestAttachFinished_SuppressesPostDetachReady(t *testing.T) {
	h, inst := newUnreadHome(t)
	inst.MarkSeen()
	inst.SetStatus(session.Running) // stale status left over from before the attach

	_, _ = h.Update(attachFinishedMsg{killTarget: inst})
	inst.SetStatus(session.Ready) // the post-detach poll settles to idle

	require.False(t, inst.Unread(), "a completion watched while attached must not flag on detach")

	// But an agent still working at detach re-arms normal flagging.
	inst.SetStatus(session.Running)
	_, _ = h.Update(attachFinishedMsg{killTarget: inst})
	inst.SetStatus(session.Running) // observed working clears the suppression
	inst.SetStatus(session.Ready)
	require.True(t, inst.Unread(), "a completion after detach must still flag")
}
