package app

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/ui"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pressQ sends the global quit key from the default list view.
func pressQ(t *testing.T, h *home) tea.Cmd {
	t.Helper()
	_, cmd := h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	return cmd
}

// isQuit reports whether running cmd yields the tea.QuitMsg that tea.Quit produces.
func isQuit(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	_, ok := cmd().(tea.QuitMsg)
	return ok
}

// newQuitTestHome builds a home wired enough to drive handleQuit and
// handleInstanceStarted: a real list, a working in-memory storage, auto-attach
// disabled (so a completed start never tries to attach), and the overlays those
// paths touch. HOME is already sandboxed by the package TestMain.
func newQuitTestHome(t *testing.T) *home {
	t.Helper()
	spin := spinner.New(spinner.WithSpinner(spinner.MiniDot))
	list := ui.NewList(&spin)

	appState := config.DefaultState()
	storage, err := session.NewStorage(appState)
	require.NoError(t, err)

	noAutoAttach := false
	cfg := config.DefaultConfig()
	cfg.AutoAttach = &noAutoAttach

	// A cancelled context so the transient-notice hide timers returned by
	// handleInfoNotice / handleError resolve immediately (scheduleNoticeHide
	// selects on ctx.Done()) instead of blocking each isQuit() call for the full
	// errToastDuration.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	return &home{
		ctx:          ctx,
		state:        stateDefault,
		appConfig:    cfg,
		appState:     appState,
		storage:      storage,
		list:         list,
		menu:         ui.NewMenu(),
		errBox:       ui.NewErrBox(),
		tabbedWindow: ui.NewTabbedWindow(ui.NewPreviewPane(), ui.NewDiffPane(), ui.NewTerminalPane(context.Background())),
	}
}

// addLoadingInstance appends a session in the Loading phase — the state the
// new-session flow leaves it in while its background Start goroutine runs.
func addLoadingInstance(t *testing.T, h *home, title string) *session.Instance {
	t.Helper()
	inst, err := session.NewInstance(session.InstanceOptions{
		Title: title, Path: t.TempDir(), Program: "echo",
	})
	require.NoError(t, err)
	h.list.AddInstance(inst)
	inst.SetStatus(session.Loading)
	return inst
}

// Fix 2: quitting while a session is still Loading must not drop it. handleQuit
// defers, and the exit completes once the in-flight Start reports done.
func TestQuitWaitsForLoadingSessionThenExits(t *testing.T) {
	h := newQuitTestHome(t)
	inst := addLoadingInstance(t, h, "loading-one")

	cmd := pressQ(t, h)
	assert.False(t, isQuit(cmd), "must not quit while a session is Loading")
	assert.True(t, h.quitRequested, "quit should be armed for after startup")

	// Start completes -> the deferred quit fires.
	_, cmd = h.Update(instanceStartedMsg{instance: inst})
	assert.Equal(t, session.Running, inst.GetStatus())
	assert.True(t, isQuit(cmd), "quit should complete once nothing is Loading")
	assert.False(t, h.quitRequested, "the deferred quit is consumed, not left armed")
}

// Fix 2 regression guard: with nothing Loading, quit exits immediately.
func TestQuitWithNothingLoadingExitsImmediately(t *testing.T) {
	h := newQuitTestHome(t)

	cmd := pressQ(t, h)
	assert.False(t, h.quitRequested)
	assert.True(t, isQuit(cmd))
}

// Fix 2: a deferred quit must wait for EVERY Loading session, not just the first
// to finish — otherwise the still-Loading sibling would be dropped.
func TestQuitWaitsForAllLoadingSessions(t *testing.T) {
	h := newQuitTestHome(t)
	first := addLoadingInstance(t, h, "loading-first")
	second := addLoadingInstance(t, h, "loading-second")

	cmd := pressQ(t, h)
	require.False(t, isQuit(cmd))
	require.True(t, h.quitRequested)

	_, cmd = h.Update(instanceStartedMsg{instance: first})
	assert.False(t, isQuit(cmd), "second session still Loading -> keep waiting")
	assert.True(t, h.quitRequested, "quit stays armed")

	_, cmd = h.Update(instanceStartedMsg{instance: second})
	assert.True(t, isQuit(cmd), "last Loading session done -> quit")
}

// failingStore is a config.InstanceStorage whose SaveInstances always fails,
// simulating a full disk / read-only data dir.
type failingStore struct{}

func (failingStore) SaveInstances(json.RawMessage) error {
	return errors.New("no space left on device")
}
func (failingStore) GetInstances() json.RawMessage { return nil }
func (failingStore) DeleteAllInstances() error     { return nil }

// Fix 3: when state can't be saved, quit must not trap the user in an unquittable
// TUI — it opens a confirm modal, and confirming exits anyway.
func TestQuitOnSaveFailureOpensConfirmModalAndCanForceQuit(t *testing.T) {
	h := newQuitTestHome(t)
	st, err := session.NewStorage(failingStore{})
	require.NoError(t, err)
	h.storage = st

	cmd := pressQ(t, h)
	assert.False(t, isQuit(cmd), "a save failure must not quit outright")
	assert.Equal(t, stateConfirm, h.state, "it should open the quit-anyway confirm modal")
	require.NotNil(t, h.confirmationOverlay)
	require.NotNil(t, h.pendingConfirmAction)

	// Confirm -> quit anyway.
	_, cmd = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	assert.Equal(t, stateDefault, h.state)
	assert.True(t, isQuit(cmd), "confirming must quit despite the save failure")
}

// Fix 3: cancelling the save-failure confirm returns to the list without quitting.
func TestQuitOnSaveFailureCancelDoesNotQuit(t *testing.T) {
	h := newQuitTestHome(t)
	st, err := session.NewStorage(failingStore{})
	require.NoError(t, err)
	h.storage = st

	require.False(t, isQuit(pressQ(t, h)))
	require.Equal(t, stateConfirm, h.state)

	_, cmd := h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	assert.Equal(t, stateDefault, h.state)
	assert.False(t, isQuit(cmd), "cancelling must not quit")
}

// A deferred quit must not exit the app out from under an overlay the user opened
// while the session was starting: instanceStartedMsg is dispatched regardless of
// m.state, so quitting unconditionally would drop unsaved settings / a half-typed
// rename / the new-session form. The deferred quit is dropped instead; an explicit
// q from the list still exits.
func TestDeferredQuitDoesNotExitFromOpenOverlay(t *testing.T) {
	h := newQuitTestHome(t)
	inst := addLoadingInstance(t, h, "loading-one")

	require.False(t, isQuit(pressQ(t, h)))
	require.True(t, h.quitRequested)

	// The user navigates into an overlay (e.g. the settings panel) mid-startup.
	h.state = stateSettings

	_, cmd := h.Update(instanceStartedMsg{instance: inst})
	assert.False(t, isQuit(cmd), "must not quit out from under an open overlay")
	assert.False(t, h.quitRequested, "the deferred quit is dropped, not left armed")
	assert.Equal(t, stateSettings, h.state, "the overlay stays open")
	assert.Equal(t, session.Running, inst.GetStatus(), "the finished session is still brought up")
}

// If the save fails exactly when a deferred quit completes, the user must still
// get the "Quit anyway?" escape hatch — not a dead-end error toast — and the quit
// must not be silently stranded (leaving quitRequested armed to fire later).
func TestDeferredQuitOnSaveFailureOpensConfirmModal(t *testing.T) {
	h := newQuitTestHome(t)
	inst := addLoadingInstance(t, h, "loading-one")

	require.False(t, isQuit(pressQ(t, h)))
	require.True(t, h.quitRequested)

	// The data dir goes read-only before the in-flight start completes.
	st, err := session.NewStorage(failingStore{})
	require.NoError(t, err)
	h.storage = st

	_, cmd := h.Update(instanceStartedMsg{instance: inst})
	assert.False(t, isQuit(cmd), "a save failure must not quit outright")
	assert.Equal(t, stateConfirm, h.state, "it opens the quit-anyway confirm modal")
	require.NotNil(t, h.confirmationOverlay)
	assert.False(t, h.quitRequested, "the deferred quit is resolved, not stranded")

	_, cmd = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	assert.True(t, isQuit(cmd), "confirming exits despite the save failure")
}

// A session whose Start hangs must not trap the user. Pressing quit a second time
// while a session is still Loading escalates to a force-quit confirm that abandons
// the in-flight start, so the TUI is never unquittable.
func TestSecondQuitWhileLoadingOffersForceQuit(t *testing.T) {
	h := newQuitTestHome(t)
	_ = addLoadingInstance(t, h, "loading-one")

	require.False(t, isQuit(pressQ(t, h)), "first quit defers")
	require.True(t, h.quitRequested)
	require.Equal(t, stateDefault, h.state)

	// Second quit while still Loading -> a force-quit confirm modal.
	require.False(t, isQuit(pressQ(t, h)), "second quit opens a confirm, not an outright exit")
	assert.Equal(t, stateConfirm, h.state, "a force-quit confirm modal opens")
	require.NotNil(t, h.confirmationOverlay)

	_, cmd := h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	assert.True(t, isQuit(cmd), "confirming abandons the starting session and exits")
}

// Cancelling the force-quit confirm keeps waiting: the quit stays armed and
// completes normally once the session finally finishes starting.
func TestForceQuitCancelKeepsWaiting(t *testing.T) {
	h := newQuitTestHome(t)
	inst := addLoadingInstance(t, h, "loading-one")

	require.False(t, isQuit(pressQ(t, h)))
	require.False(t, isQuit(pressQ(t, h)), "second quit opens the force-quit modal")
	require.Equal(t, stateConfirm, h.state)

	_, cmd := h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	assert.False(t, isQuit(cmd), "cancelling force-quit does not exit")
	assert.True(t, h.quitRequested, "still armed — the session is still starting")
	assert.Equal(t, stateDefault, h.state)

	_, cmd = h.Update(instanceStartedMsg{instance: inst})
	assert.True(t, isQuit(cmd), "the still-armed quit completes when the start finishes")
}

// When a quit is deferred and one of several Loading sessions FAILS while a
// sibling is still Loading, the start error must be surfaced (not swallowed by the
// "finishing startup" notice) and the quit stays deferred.
func TestDeferredQuitSurfacesStartErrorWhileSiblingLoads(t *testing.T) {
	h := newQuitTestHome(t)
	first := addLoadingInstance(t, h, "loading-first")
	_ = addLoadingInstance(t, h, "loading-second")

	require.False(t, isQuit(pressQ(t, h)))
	require.True(t, h.quitRequested)

	_, cmd := h.Update(instanceStartedMsg{instance: first, err: errors.New("worktree boom")})
	assert.False(t, isQuit(cmd), "the surviving sibling keeps the quit deferred")
	assert.True(t, h.quitRequested, "quit stays armed")
	require.True(t, h.menu.HasNotice(), "a notice is shown")
	assert.Contains(t, h.menu.NoticeText(), "worktree boom", "the start error is surfaced, not swallowed")
}

// #439: a slow background Start() must not steal the selection. If the user
// navigated to another session while the new one was starting, the completing
// instanceStartedMsg must preserve their cursor rather than snapping it back to
// the just-started session. (newQuitTestHome disables auto-attach, so the
// auto-open path — which would legitimately re-select and attach — is off.)
func TestInstanceStartedPreservesNavigatedAwaySelection(t *testing.T) {
	h := newQuitTestHome(t)
	first := addLoadingInstance(t, h, "loading-first")   // the just-created session
	second := addLoadingInstance(t, h, "loading-second") // where the user navigated to
	h.list.SelectInstance(second)

	_, _ = h.Update(instanceStartedMsg{instance: first, hadPrompt: true})

	assert.Same(t, second, h.list.GetSelectedInstance(),
		"a completed async start must not steal the cursor from the user's chosen row")
	assert.Equal(t, session.Running, first.GetStatus(),
		"the start still completes normally — only the selection is left alone")
}

// #439 companion: when the user has NOT navigated away, the started instance
// stays selected (the common case must be untouched), and it still transitions
// to Running.
func TestInstanceStartedKeepsSelectionWhenNotNavigated(t *testing.T) {
	h := newQuitTestHome(t)
	inst := addLoadingInstance(t, h, "loading-one")
	h.list.SelectInstance(inst)

	_, _ = h.Update(instanceStartedMsg{instance: inst, hadPrompt: true})

	assert.Same(t, inst, h.list.GetSelectedInstance(),
		"the started session stays selected when the user never moved")
	assert.Equal(t, session.Running, inst.GetStatus())
}
