package app

import (
	"context"
	"fmt"
	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/internal/testutil"
	"github.com/ZviBaratz/atrium/keys"
	"github.com/ZviBaratz/atrium/log"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/session/git"
	"github.com/ZviBaratz/atrium/session/tmux"
	"github.com/ZviBaratz/atrium/ui"
	"github.com/ZviBaratz/atrium/ui/overlay"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMain runs before all tests to set up the test environment. HOME (and
// CLAUDE_CONFIG_DIR) are sandboxed via the shared helper so tests never read
// or overwrite the real Atrium state/config — config.GetConfigDir resolves
// under $HOME, and LoadConfig writes a default config.json on first run.
func TestMain(m *testing.M) {
	// Initialize the logger before any tests run
	log.Initialize(false)

	exitCode := testutil.SandboxHomeMain(m)

	log.Close()
	os.Exit(exitCode)
}

func TestDeliverReadyPrompts(t *testing.T) {
	newInst := func(prompt string) *session.Instance {
		inst, err := session.NewInstance(session.InstanceOptions{
			Title: "s", Path: t.TempDir(), Program: "claude",
		})
		require.NoError(t, err)
		inst.QueuePrompt(prompt)
		return inst
	}

	t.Run("ready instance with a queued prompt is dispatched once, kept queued and marked in-flight", func(t *testing.T) {
		inst := newInst("do the thing")
		cmds := deliverReadyPrompts([]instanceMetaResult{
			{instance: inst, readyForPrompt: true},
		})
		require.Len(t, cmds, 1)
		require.Equal(t, "do the thing", inst.Prompt(),
			"prompt must stay queued until delivery is confirmed (promptDeliveredMsg), so a failed send is retried")
		require.True(t, inst.PromptSending(),
			"the in-flight guard must be set so an overlapping tick can't dispatch the same prompt again")
	})

	t.Run("an in-flight prompt is not dispatched again", func(t *testing.T) {
		inst := newInst("do the thing")
		_, _ = inst.ClaimPrompt() // a prior tick's dispatch raised the guard; its send is still running
		cmds := deliverReadyPrompts([]instanceMetaResult{
			{instance: inst, readyForPrompt: true},
		})
		require.Empty(t, cmds, "a send already in flight must not be re-dispatched")
	})

	t.Run("ready instance with no queued prompt sends nothing", func(t *testing.T) {
		inst := newInst("")
		cmds := deliverReadyPrompts([]instanceMetaResult{
			{instance: inst, readyForPrompt: true},
		})
		require.Empty(t, cmds)
	})

	t.Run("queued prompt is not delivered until the instance is ready", func(t *testing.T) {
		inst := newInst("waiting on trust screen")
		cmds := deliverReadyPrompts([]instanceMetaResult{
			{instance: inst, readyForPrompt: false},
		})
		require.Empty(t, cmds)
		require.Equal(t, "waiting on trust screen", inst.Prompt(), "prompt must remain queued")
		require.False(t, inst.PromptSending(), "a not-ready instance must not be marked in-flight")
	})
}

func TestSendWithRetrySucceedsAfterTransientFailure(t *testing.T) {
	// The bounded retry exists for exactly this case: a send that fails once and then
	// succeeds must be reported as success (nil), with no error surfaced. This is the
	// retry's whole reason for existing, so it gets direct coverage that a real instance
	// (which can only fail, never fail-then-succeed) cannot provide.
	defer func(d time.Duration) { promptSendRetryDelay = d }(promptSendRetryDelay)
	promptSendRetryDelay = 0 // don't sleep between retries in the test

	calls := 0
	err := sendWithRetry(func() error {
		calls++
		if calls == 1 {
			return fmt.Errorf("transient tmux hiccup")
		}
		return nil
	})

	require.NoError(t, err)
	require.Equal(t, 2, calls, "must retry once after the first failure, then stop on success")
}

func TestSendWithRetryGivesUpAfterAttempts(t *testing.T) {
	// A persistent failure (the common dead-pane case) must exhaust the bounded attempts
	// and return the last error so the caller can surface it, rather than retrying forever.
	defer func(d time.Duration) { promptSendRetryDelay = d }(promptSendRetryDelay)
	promptSendRetryDelay = 0

	calls := 0
	wantErr := fmt.Errorf("dead pane")
	err := sendWithRetry(func() error {
		calls++
		return wantErr
	})

	require.Equal(t, promptSendAttempts, calls, "a persistent failure must use every attempt")
	require.ErrorIs(t, err, wantErr, "the last error must be returned for surfacing")
}

func TestSendPromptCmdSurfacesErrorAfterRetries(t *testing.T) {
	// An instance that was never Started makes every SendPrompt attempt fail with
	// "instance not started", so the cmd must exhaust its retries and report the failure
	// as a promptSendErrorMsg instead of swallowing it (the old behavior returned nil and
	// only logged, leaving the session Ready-but-idle with no surfaced error).
	defer func(d time.Duration) { promptSendRetryDelay = d }(promptSendRetryDelay)
	promptSendRetryDelay = 0 // don't sleep between retries in the test

	inst, err := session.NewInstance(session.InstanceOptions{
		Title: "lost-prompt", Path: t.TempDir(), Program: "claude",
	})
	require.NoError(t, err)

	msg := sendPromptCmd(inst, "do the thing")()

	failure, ok := msg.(promptSendErrorMsg)
	require.True(t, ok, "a failed delivery must yield a promptSendErrorMsg, not be swallowed (got %T)", msg)
	require.Same(t, inst, failure.instance, "the message must name the instance whose prompt was lost")
	require.Error(t, failure.err)
}

func TestPromptSendErrorMsgSurfacesInUI(t *testing.T) {
	// Routing a promptSendErrorMsg through Update must surface the failure (here, a toast
	// on the always-on hint bar) so the user learns the queued prompt was lost rather than
	// the error being silently dropped.
	h := newCreateFormHome(t)
	h.errBox.SetSize(80, 1)

	inst, err := session.NewInstance(session.InstanceOptions{
		Title: "lost-prompt", Path: t.TempDir(), Program: "claude",
	})
	require.NoError(t, err)

	_, _ = h.Update(promptSendErrorMsg{instance: inst, err: fmt.Errorf("instance not started")})

	assert.True(t, h.menu.HasNotice(), "a failed queued-prompt delivery must surface, not be swallowed")
}

func TestPromptDeliveredMsgClearsQueuedPrompt(t *testing.T) {
	// A confirmed delivery must retire the queued prompt (so it stops being a poll target
	// and is never re-sent) and release the in-flight guard.
	h := newCreateFormHome(t)
	st, err := session.NewStorage(config.DefaultState())
	require.NoError(t, err)
	h.storage = st // the handler persists the cleared prompt
	inst, err := session.NewInstance(session.InstanceOptions{
		Title: "delivered", Path: t.TempDir(), Program: "claude",
	})
	require.NoError(t, err)
	inst.QueuePrompt("do the thing")
	_, _ = inst.ClaimPrompt()

	_, _ = h.Update(promptDeliveredMsg{instance: inst})

	require.Equal(t, "", inst.Prompt(), "a delivered prompt must be cleared")
	require.True(t, inst.PromptQueuedAt().IsZero(), "the delivery clock must be cleared")
	require.False(t, inst.PromptSending(), "the in-flight guard must be released")
}

func TestPromptDeferredMsgKeepsPromptAndReleasesGuard(t *testing.T) {
	// A soft (unconfirmed) outcome must keep the prompt queued for the next tick but release
	// the in-flight guard so that tick can re-dispatch it.
	h := newCreateFormHome(t)
	inst, err := session.NewInstance(session.InstanceOptions{
		Title: "deferred", Path: t.TempDir(), Program: "claude",
	})
	require.NoError(t, err)
	inst.QueuePrompt("do the thing")
	_, _ = inst.ClaimPrompt()

	_, _ = h.Update(promptDeferredMsg{instance: inst})

	require.Equal(t, "do the thing", inst.Prompt(), "a deferred prompt must stay queued for retry")
	require.False(t, inst.PromptSending(), "the in-flight guard must be released so the next tick can retry")
}

func TestPromptSendErrorMsgClearsPrompt(t *testing.T) {
	// A hard failure must retire the prompt (so the loop stops retrying a dead pane) in
	// addition to surfacing the loss.
	h := newCreateFormHome(t)
	h.errBox.SetSize(80, 1)
	inst, err := session.NewInstance(session.InstanceOptions{
		Title: "lost", Path: t.TempDir(), Program: "claude",
	})
	require.NoError(t, err)
	inst.QueuePrompt("do the thing")
	_, _ = inst.ClaimPrompt()

	_, _ = h.Update(promptSendErrorMsg{instance: inst, err: fmt.Errorf("dead pane")})

	require.Equal(t, "", inst.Prompt(), "a hard-failed prompt must be cleared so the loop stops retrying")
	require.False(t, inst.PromptSending())
}

func TestPromptDeliveryReady(t *testing.T) {
	now := time.Now()
	queued := now.Add(-1 * time.Second)          // queued recently, within grace
	stale := now.Add(-2 * promptDeliveryTimeout) // queued long ago, past grace

	tests := []struct {
		name          string
		state         tmux.PaneState
		awaitingInput bool
		queuedAt      time.Time
		want          bool
	}{
		{
			name:          "idle pane past the gate delivers",
			state:         tmux.PaneIdle,
			awaitingInput: true,
			queuedAt:      queued,
			want:          true,
		},
		{
			name:          "startup gate still up never delivers even when idle",
			state:         tmux.PaneIdle,
			awaitingInput: false,
			queuedAt:      queued,
			want:          false,
		},
		{
			name:          "busy pane within grace keeps waiting",
			state:         tmux.PaneWorking,
			awaitingInput: true,
			queuedAt:      queued,
			want:          false,
		},
		{
			name:          "busy pane past timeout force-delivers once past the gate",
			state:         tmux.PaneWorking,
			awaitingInput: true,
			queuedAt:      stale,
			want:          true,
		},
		{
			name:          "timeout never bypasses the startup gate",
			state:         tmux.PaneWorking,
			awaitingInput: false,
			queuedAt:      stale,
			want:          false,
		},
		{
			name:          "zero queuedAt disables the timeout on a busy pane",
			state:         tmux.PaneWorking,
			awaitingInput: true,
			queuedAt:      time.Time{},
			want:          false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := promptDeliveryReady(tt.state, tt.awaitingInput, tt.queuedAt, now)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestRecoverLostInstances(t *testing.T) {
	newInst := func() *session.Instance {
		inst, err := session.NewInstance(session.InstanceOptions{
			Title: "s", Path: t.TempDir(), Program: "claude",
		})
		require.NoError(t, err)
		return inst
	}

	lost := func(inst *session.Instance) []instanceMetaResult {
		return []instanceMetaResult{{instance: inst, sessionLost: true}}
	}

	t.Run("a live instance is left untouched and clears its strikes", func(t *testing.T) {
		inst := newInst()
		strikes := map[*session.Instance]int{inst: 1}
		recovered := recoverLostInstances([]instanceMetaResult{{instance: inst, sessionLost: false}}, strikes)
		require.False(t, recovered)
		require.False(t, inst.Paused())
		require.Zero(t, strikes[inst], "a live observation resets the dead-strike count")
	})

	t.Run("a single lost observation does NOT recover (debounce)", func(t *testing.T) {
		inst := newInst()
		strikes := map[*session.Instance]int{}
		recovered := recoverLostInstances(lost(inst), strikes)
		require.False(t, recovered, "one transient has-session miss must not tear down a live worktree")
		require.Equal(t, 1, strikes[inst])
	})

	t.Run("a live observation between misses resets the count", func(t *testing.T) {
		inst := newInst()
		strikes := map[*session.Instance]int{}
		recoverLostInstances(lost(inst), strikes)                                                 // strike 1
		recoverLostInstances([]instanceMetaResult{{instance: inst, sessionLost: false}}, strikes) // reset
		recovered := recoverLostInstances(lost(inst), strikes)                                    // strike 1 again
		require.False(t, recovered)
		require.Equal(t, 1, strikes[inst])
	})

	t.Run("recovers only after threshold consecutive misses", func(t *testing.T) {
		inst := newInst()
		strikes := map[*session.Instance]int{}
		var recovered bool
		for i := 0; i < lostSessionRecoverThreshold; i++ {
			require.False(t, recovered, "must not recover before the threshold")
			recovered = recoverLostInstances(lost(inst), strikes)
		}
		require.True(t, recovered, "recovers once confirmed dead on threshold consecutive ticks")
	})

	t.Run("an already-paused instance is skipped", func(t *testing.T) {
		inst := newInst()
		inst.SetStatus(session.Paused)
		strikes := map[*session.Instance]int{}
		recovered := recoverLostInstances(lost(inst), strikes)
		require.False(t, recovered, "an already-paused instance needs no recovery")
	})
	// The actual lost-session -> Paused transition is covered against a real worktree
	// by session.TestRecoverLostSessionTransitionsToPaused.
}

// TestInstanceStartedMsgSetsRunning pins the authoritative fix: the Loading -> Running
// transition is owned by the main-thread instanceStartedMsg handler, not the background
// Start() goroutine. A successful start message must flip the instance to Running so it
// can never stay stuck on the "Setting up workspace..." splash.
func TestInstanceStartedMsgSetsRunning(t *testing.T) {
	spin := spinner.New(spinner.WithSpinner(spinner.MiniDot))
	list := ui.NewList(&spin)

	inst, err := session.NewInstance(session.InstanceOptions{
		Title: "started", Path: t.TempDir(), Program: "echo",
	})
	require.NoError(t, err)
	list.AddInstance(inst)
	list.SelectInstance(inst)
	inst.SetStatus(session.Loading) // the state the new-session flow leaves it in pre-start

	appState := config.DefaultState()
	storage, err := session.NewStorage(appState)
	require.NoError(t, err)
	noAutoAttach := false
	cfg := config.DefaultConfig()
	cfg.AutoAttach = &noAutoAttach

	h := &home{
		ctx:          context.Background(),
		state:        stateDefault,
		appConfig:    cfg,
		appState:     appState,
		storage:      storage,
		list:         list,
		menu:         ui.NewMenu(),
		tabbedWindow: ui.NewTabbedWindow(ui.NewPreviewPane(), ui.NewDiffPane(), ui.NewTerminalPane(context.Background())),
	}

	_, _ = h.Update(instanceStartedMsg{instance: inst})

	require.Equal(t, session.Running, inst.GetStatus(),
		"a completed start must transition the instance out of Loading on the main thread")
}

// TestConfirmationModalStateTransitions tests state transitions without full instance setup
func TestConfirmationModalStateTransitions(t *testing.T) {
	// Create a minimal home struct for testing state transitions
	h := &home{
		ctx:       context.Background(),
		state:     stateDefault,
		appConfig: config.DefaultConfig(),
	}

	t.Run("shows confirmation on D press", func(t *testing.T) {
		// Simulate pressing 'D'
		h.state = stateDefault
		h.confirmationOverlay = nil

		// Manually trigger what would happen in handleKeyPress for 'D'
		h.state = stateConfirm
		h.confirmationOverlay = overlay.NewConfirmationOverlay("[!] Kill session 'test'?")

		assert.Equal(t, stateConfirm, h.state)
		assert.NotNil(t, h.confirmationOverlay)
		assert.False(t, h.confirmationOverlay.Dismissed)
	})

	t.Run("returns to default on y press", func(t *testing.T) {
		// Start in confirmation state
		h.state = stateConfirm
		h.confirmationOverlay = overlay.NewConfirmationOverlay("Test confirmation")

		// Simulate pressing 'y' using HandleKeyPress
		keyMsg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")}
		shouldClose := h.confirmationOverlay.HandleKeyPress(keyMsg)
		if shouldClose {
			h.state = stateDefault
			h.confirmationOverlay = nil
		}

		assert.Equal(t, stateDefault, h.state)
		assert.Nil(t, h.confirmationOverlay)
	})

	t.Run("returns to default on n press", func(t *testing.T) {
		// Start in confirmation state
		h.state = stateConfirm
		h.confirmationOverlay = overlay.NewConfirmationOverlay("Test confirmation")

		// Simulate pressing 'n' using HandleKeyPress
		keyMsg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")}
		shouldClose := h.confirmationOverlay.HandleKeyPress(keyMsg)
		if shouldClose {
			h.state = stateDefault
			h.confirmationOverlay = nil
		}

		assert.Equal(t, stateDefault, h.state)
		assert.Nil(t, h.confirmationOverlay)
	})

	t.Run("returns to default on esc press", func(t *testing.T) {
		// Start in confirmation state
		h.state = stateConfirm
		h.confirmationOverlay = overlay.NewConfirmationOverlay("Test confirmation")

		// Simulate pressing ESC using HandleKeyPress
		keyMsg := tea.KeyMsg{Type: tea.KeyEscape}
		shouldClose := h.confirmationOverlay.HandleKeyPress(keyMsg)
		if shouldClose {
			h.state = stateDefault
			h.confirmationOverlay = nil
		}

		assert.Equal(t, stateDefault, h.state)
		assert.Nil(t, h.confirmationOverlay)
	})
}

// TestConfirmationModalKeyHandling tests the actual key handling in confirmation state
func TestConfirmationModalKeyHandling(t *testing.T) {
	// Import needed packages
	spinner := spinner.New(spinner.WithSpinner(spinner.MiniDot))
	list := ui.NewList(&spinner)

	// Create enough of home struct to test handleKeyPress in confirmation state
	h := &home{
		ctx:                 context.Background(),
		state:               stateConfirm,
		appConfig:           config.DefaultConfig(),
		list:                list,
		menu:                ui.NewMenu(),
		confirmationOverlay: overlay.NewConfirmationOverlay("Kill session?"),
	}

	testCases := []struct {
		name              string
		key               string
		expectedState     state
		expectedDismissed bool
		expectedNil       bool
	}{
		{
			name:              "y key confirms and dismisses overlay",
			key:               "y",
			expectedState:     stateDefault,
			expectedDismissed: true,
			expectedNil:       true,
		},
		{
			name:              "n key cancels and dismisses overlay",
			key:               "n",
			expectedState:     stateDefault,
			expectedDismissed: true,
			expectedNil:       true,
		},
		{
			name:              "esc key cancels and dismisses overlay",
			key:               "esc",
			expectedState:     stateDefault,
			expectedDismissed: true,
			expectedNil:       true,
		},
		{
			name:              "other keys are ignored",
			key:               "x",
			expectedState:     stateConfirm,
			expectedDismissed: false,
			expectedNil:       false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Reset state
			h.state = stateConfirm
			h.confirmationOverlay = overlay.NewConfirmationOverlay("Kill session?")

			// Create key message
			var keyMsg tea.KeyMsg
			if tc.key == "esc" {
				keyMsg = tea.KeyMsg{Type: tea.KeyEscape}
			} else {
				keyMsg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(tc.key)}
			}

			// Call handleKeyPress
			model, _ := h.handleKeyPress(keyMsg)
			homeModel, ok := model.(*home)
			require.True(t, ok)

			assert.Equal(t, tc.expectedState, homeModel.state, "State mismatch for key: %s", tc.key)
			if tc.expectedNil {
				assert.Nil(t, homeModel.confirmationOverlay, "Overlay should be nil for key: %s", tc.key)
			} else {
				assert.NotNil(t, homeModel.confirmationOverlay, "Overlay should not be nil for key: %s", tc.key)
				assert.Equal(t, tc.expectedDismissed, homeModel.confirmationOverlay.Dismissed, "Dismissed mismatch for key: %s", tc.key)
			}
		})
	}
}

// TestConfirmationMessageFormatting tests that confirmation messages are formatted correctly
func TestConfirmationMessageFormatting(t *testing.T) {
	testCases := []struct {
		name            string
		sessionTitle    string
		expectedMessage string
	}{
		{
			name:            "short session name",
			sessionTitle:    "my-feature",
			expectedMessage: "[!] Kill session 'my-feature'? (y/n)",
		},
		{
			name:            "long session name",
			sessionTitle:    "very-long-feature-branch-name-here",
			expectedMessage: "[!] Kill session 'very-long-feature-branch-name-here'? (y/n)",
		},
		{
			name:            "session with spaces",
			sessionTitle:    "feature with spaces",
			expectedMessage: "[!] Kill session 'feature with spaces'? (y/n)",
		},
		{
			name:            "session with special chars",
			sessionTitle:    "feature/branch-123",
			expectedMessage: "[!] Kill session 'feature/branch-123'? (y/n)",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Test the message formatting directly
			actualMessage := fmt.Sprintf("[!] Kill session '%s'? (y/n)", tc.sessionTitle)
			assert.Equal(t, tc.expectedMessage, actualMessage)
		})
	}
}

// TestConfirmationFlowSimulation tests the confirmation flow by simulating the state changes
func TestConfirmationFlowSimulation(t *testing.T) {
	// Create a minimal setup
	spinner := spinner.New(spinner.WithSpinner(spinner.MiniDot))
	list := ui.NewList(&spinner)

	// Add test instance
	instance, err := session.NewInstance(session.InstanceOptions{
		Title:   "test-session",
		Path:    t.TempDir(),
		Program: "claude",
	})
	require.NoError(t, err)
	_ = list.AddInstance(instance)
	list.SetSelectedInstance(0)

	h := &home{
		ctx:       context.Background(),
		state:     stateDefault,
		appConfig: config.DefaultConfig(),
		list:      list,
		menu:      ui.NewMenu(),
	}

	// Simulate what happens when D is pressed
	selected := h.list.GetSelectedInstance()
	require.NotNil(t, selected)

	// This is what the KeyKill handler does
	message := fmt.Sprintf("[!] Kill session '%s'?", selected.Title)
	h.confirmationOverlay = overlay.NewConfirmationOverlay(message)
	h.state = stateConfirm

	// Verify the state
	assert.Equal(t, stateConfirm, h.state)
	assert.NotNil(t, h.confirmationOverlay)
	assert.False(t, h.confirmationOverlay.Dismissed)
	// Test that overlay renders with the correct message
	rendered := h.confirmationOverlay.Render()
	assert.Contains(t, rendered, "Kill session 'test-session'?")
}

// TestConfirmKillDoubleTapAltKey verifies that confirmKill enables the double-tap
// confirm (the kill chord as a second confirm key) only when the toggle is on, and
// that the generic confirmAction path never gets that alt key.
func TestConfirmKillDoubleTapAltKey(t *testing.T) {
	newHomeWithInstance := func(t *testing.T, cfg *config.Config) (*home, *session.Instance) {
		t.Helper()
		spin := spinner.New(spinner.WithSpinner(spinner.MiniDot))
		list := ui.NewList(&spin)
		instance, err := session.NewInstance(session.InstanceOptions{
			Title:   "test-session",
			Path:    t.TempDir(),
			Program: "claude",
		})
		require.NoError(t, err)
		_ = list.AddInstance(instance)
		list.SetSelectedInstance(0)
		return &home{
			ctx:       context.Background(),
			state:     stateDefault,
			appConfig: cfg,
			list:      list,
			menu:      ui.NewMenu(),
		}, instance
	}

	t.Run("toggle on sets the kill chord as an alt confirm key", func(t *testing.T) {
		h, inst := newHomeWithInstance(t, config.DefaultConfig())
		h.confirmKill(inst)
		require.NotNil(t, h.confirmationOverlay)
		assert.Equal(t, keys.KillKey, h.confirmationOverlay.ConfirmAltKey)
	})

	t.Run("toggle off leaves the alt key unset", func(t *testing.T) {
		off := false
		cfg := config.DefaultConfig()
		cfg.KillDoubleTapConfirm = &off
		h, inst := newHomeWithInstance(t, cfg)
		h.confirmKill(inst)
		require.NotNil(t, h.confirmationOverlay)
		assert.Equal(t, "", h.confirmationOverlay.ConfirmAltKey)
	})

	t.Run("generic confirmAction never gets the alt key", func(t *testing.T) {
		h, _ := newHomeWithInstance(t, config.DefaultConfig())
		h.confirmAction("Push branch?", func() tea.Msg { return nil })
		require.NotNil(t, h.confirmationOverlay)
		assert.Equal(t, "", h.confirmationOverlay.ConfirmAltKey)
	})
}

// TestConfirmKillDirtyWarning verifies that confirmKill enriches the dialog message
// with a dirty-state notice when the session has uncommitted changes, and leaves it
// plain when the session is clean or has no cached diff stats.
func TestConfirmKillDirtyWarning(t *testing.T) {
	newHome := func(t *testing.T) (*home, *session.Instance) {
		t.Helper()
		spin := spinner.New(spinner.WithSpinner(spinner.MiniDot))
		list := ui.NewList(&spin)
		inst, err := session.NewInstance(session.InstanceOptions{
			Title:   "test-session",
			Path:    t.TempDir(),
			Program: "claude",
		})
		require.NoError(t, err)
		_ = list.AddInstance(inst)
		list.SetSelectedInstance(0)
		return &home{
			ctx:       context.Background(),
			state:     stateDefault,
			appConfig: config.DefaultConfig(),
			list:      list,
			menu:      ui.NewMenu(),
		}, inst
	}

	t.Run("dirty session adds uncommitted-changes notice", func(t *testing.T) {
		h, inst := newHome(t)
		inst.SetDiffStats(&git.DiffStats{Dirty: true})
		h.confirmKill(inst)
		require.NotNil(t, h.confirmationOverlay)
		rendered := h.confirmationOverlay.Render()
		assert.Contains(t, rendered, "uncommitted")
	})

	t.Run("clean session shows plain message", func(t *testing.T) {
		h, inst := newHome(t)
		inst.SetDiffStats(&git.DiffStats{Dirty: false})
		h.confirmKill(inst)
		require.NotNil(t, h.confirmationOverlay)
		rendered := h.confirmationOverlay.Render()
		assert.Contains(t, rendered, "Kill session 'test-session'?")
		assert.NotContains(t, rendered, "uncommitted")
	})

	t.Run("nil diff stats shows plain message", func(t *testing.T) {
		h, inst := newHome(t)
		// SetDiffStats not called — diffStats is nil
		h.confirmKill(inst)
		require.NotNil(t, h.confirmationOverlay)
		rendered := h.confirmationOverlay.Render()
		assert.Contains(t, rendered, "Kill session 'test-session'?")
		assert.NotContains(t, rendered, "uncommitted")
	})
}

// TestConfirmActionWithDifferentTypes verifies that confirming an action routes
// whatever message it returns — nil, an error, or a custom msg — back through the
// runtime via the command handleKeyPress returns, so Update can dispatch on it.
func TestConfirmActionWithDifferentTypes(t *testing.T) {
	newHome := func() *home {
		return &home{
			ctx:       context.Background(),
			state:     stateDefault,
			appConfig: config.DefaultConfig(),
			menu:      ui.NewMenu(),
		}
	}

	// confirmAndCollect drives the real flow: stash the action, press the confirm
	// key, and return the message carried by the resulting command (if any).
	confirmAndCollect := func(t *testing.T, h *home) tea.Msg {
		t.Helper()
		_, cmd := h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
		assert.Equal(t, stateDefault, h.state)
		assert.Nil(t, h.pendingConfirmAction)
		if cmd == nil {
			return nil
		}
		return cmd()
	}

	t.Run("nil result yields a command carrying a nil message", func(t *testing.T) {
		h := newHome()
		actionCalled := false
		_ = h.confirmAction("Test action?", func() tea.Msg {
			actionCalled = true
			return nil
		})
		msg := confirmAndCollect(t, h)
		assert.True(t, actionCalled, "action should run on confirm")
		assert.Nil(t, msg)
	})

	t.Run("error result is routed back through the runtime", func(t *testing.T) {
		h := newHome()
		expectedErr := fmt.Errorf("test error")
		_ = h.confirmAction("Error action?", func() tea.Msg { return expectedErr })
		msg := confirmAndCollect(t, h)
		err, ok := msg.(error)
		require.True(t, ok, "expected an error message, got %T", msg)
		assert.Equal(t, expectedErr, err)
	})

	t.Run("custom message result is routed back through the runtime", func(t *testing.T) {
		h := newHome()
		_ = h.confirmAction("Custom message action?", func() tea.Msg { return instanceChangedMsg{} })
		msg := confirmAndCollect(t, h)
		_, ok := msg.(instanceChangedMsg)
		assert.True(t, ok, "expected instanceChangedMsg but got %T", msg)
	})
}

// TestMultipleConfirmationsDontInterfere verifies that the single model-owned
// pendingConfirmAction slot is managed correctly across confirmations: cancelling
// clears it, and opening a new confirmation replaces any prior pending action so
// confirming only ever runs the most recently requested one.
func TestMultipleConfirmationsDontInterfere(t *testing.T) {
	newHome := func() *home {
		return &home{
			ctx:       context.Background(),
			state:     stateDefault,
			appConfig: config.DefaultConfig(),
			menu:      ui.NewMenu(),
		}
	}

	t.Run("cancelling then confirming a new action runs only the second", func(t *testing.T) {
		h := newHome()

		action1Called := false
		_ = h.confirmAction("First action?", func() tea.Msg {
			action1Called = true
			return nil
		})
		require.NotNil(t, h.pendingConfirmAction)

		// Cancel the first confirmation: its action must not run.
		_, cmd := h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
		assert.Nil(t, cmd)
		assert.False(t, action1Called, "cancelled action must not run")
		assert.Equal(t, stateDefault, h.state)
		assert.Nil(t, h.pendingConfirmAction)

		// A second, independent confirmation, this time confirmed.
		action2Called := false
		_ = h.confirmAction("Second action?", func() tea.Msg {
			action2Called = true
			return fmt.Errorf("action2 error")
		})
		require.NotNil(t, h.pendingConfirmAction)

		_, cmd = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
		require.NotNil(t, cmd)
		err, ok := cmd().(error)
		require.True(t, ok)
		assert.Equal(t, "action2 error", err.Error())
		assert.True(t, action2Called)
		assert.False(t, action1Called, "first action must never run")
	})

	t.Run("opening a second confirmation replaces the first pending action", func(t *testing.T) {
		h := newHome()

		firstCalled := false
		_ = h.confirmAction("First action?", func() tea.Msg {
			firstCalled = true
			return nil
		})

		// Replace it before confirming — the second action overwrites the slot.
		secondCalled := false
		_ = h.confirmAction("Second action?", func() tea.Msg {
			secondCalled = true
			return nil
		})
		require.NotNil(t, h.pendingConfirmAction)

		_, cmd := h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
		if cmd != nil {
			cmd()
		}
		assert.True(t, secondCalled, "the replacement action should run")
		assert.False(t, firstCalled, "the replaced action must not run")
	})
}

// TestConfirmActionSurfacesActionResult locks in the fix for silently-swallowed
// confirmation errors: confirmAction stashes the action (it does not run it), and
// confirming runs it on the main loop and routes its result message — including an
// error — back through the runtime so Update's `case error` handler can display it.
func TestConfirmActionSurfacesActionResult(t *testing.T) {
	h := &home{
		ctx:       context.Background(),
		state:     stateDefault,
		appConfig: config.DefaultConfig(),
		menu:      ui.NewMenu(),
	}

	wantErr := fmt.Errorf("kill failed")
	_ = h.confirmAction("Kill it?", func() tea.Msg { return wantErr })

	assert.Equal(t, stateConfirm, h.state)
	require.NotNil(t, h.pendingConfirmAction, "confirmAction must stash the action")

	_, cmd := h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	require.NotNil(t, cmd, "confirming must return a command carrying the action result")
	assert.Equal(t, stateDefault, h.state)
	assert.Nil(t, h.pendingConfirmAction)
	assert.Nil(t, h.confirmationOverlay)

	msg := cmd()
	err, ok := msg.(error)
	require.True(t, ok, "expected an error message, got %T", msg)
	assert.Equal(t, wantErr, err)
}

// TestTargetValidityResultUpdatesIndicator verifies the async target-state result, when
// it is for the still-current target, drives the picker's inline hint: "(not a
// directory)" for an invalid target, the direct-session note for a non-git directory,
// and no hint for a git repo.
func TestTargetValidityResultUpdatesIndicator(t *testing.T) {
	const repo = "/some/repo"
	ov := overlay.NewSessionCreateOverlay(nil, nil, []string{repo}, "")
	ov.SetSize(80, 24)
	h := &home{
		ctx:              context.Background(),
		state:            statePrompt,
		appConfig:        config.DefaultConfig(),
		textInputOverlay: ov,
		newSessionPath:   repo,
	}

	_, _ = h.Update(targetValidityResultMsg{path: repo, valid: false})
	assert.Contains(t, ov.Render(), "not a directory", "an invalid result shows the hint")

	_, _ = h.Update(targetValidityResultMsg{path: repo, valid: true, direct: true})
	out := ov.Render()
	assert.NotContains(t, out, "not a directory", "a valid result clears the invalid hint")
	assert.Contains(t, out, "direct session", "a non-git directory shows the direct-session note")

	_, _ = h.Update(targetValidityResultMsg{path: repo, valid: true, direct: false})
	out = ov.Render()
	assert.NotContains(t, out, "not a directory")
	assert.NotContains(t, out, "direct session", "a git repo shows no hint at all")
}

// TestValidityResultResolvesHeadLabel verifies the validity result's resolved HEAD branch
// reaches the branch picker, so the default base option names the actual branch.
func TestValidityResultResolvesHeadLabel(t *testing.T) {
	const repo = "/some/repo"
	ov := overlay.NewSessionCreateOverlay(nil, nil, []string{repo}, "")
	ov.SetSize(80, 40)
	h := &home{
		ctx:              context.Background(),
		state:            statePrompt,
		appConfig:        config.DefaultConfig(),
		textInputOverlay: ov,
		newSessionPath:   repo,
	}
	// Focus the branch picker so its list (including the HEAD option) renders.
	_, _ = h.Update(tea.KeyMsg{Type: tea.KeyTab})
	ov.SetBranchResults(nil, ov.BranchFilterVersion())

	_, _ = h.Update(targetValidityResultMsg{path: repo, valid: true, direct: false, headBranch: "main"})
	assert.Contains(t, ov.Render(), "HEAD (main)", "the resolved branch must reach the picker")
}

// TestGitVerdictTriggersFetchOncePerPath verifies a confirmed-git validity result kicks a
// background fetch for that path — but only the first time it is confirmed during one
// form-session, so flipping between candidates doesn't spam the network.
func TestGitVerdictTriggersFetchOncePerPath(t *testing.T) {
	const repo = "/some/repo"
	ov := overlay.NewSessionCreateOverlay(nil, nil, []string{repo}, "")
	h := &home{
		ctx:              context.Background(),
		state:            statePrompt,
		appConfig:        config.DefaultConfig(),
		textInputOverlay: ov,
		newSessionPath:   repo,
	}

	_, cmd := h.Update(targetValidityResultMsg{path: repo, valid: true, direct: false})
	require.NotNil(t, cmd, "first git verdict for a path must trigger a fetch")
	done, ok := cmd().(branchFetchDoneMsg)
	require.True(t, ok, "the fetch cmd must deliver a branchFetchDoneMsg")
	assert.Equal(t, repo, done.path)

	_, cmd = h.Update(targetValidityResultMsg{path: repo, valid: true, direct: false})
	assert.Nil(t, cmd, "a path is fetched at most once per form-session")
}

// TestValidityResultRepreselectsAccount verifies that when a new target's state lands,
// the account picker is re-pointed at that target's auto-routed account — so the
// displayed selection tracks the chosen project rather than the one the form opened on.
func TestValidityResultRepreselectsAccount(t *testing.T) {
	const dir = "/some/dir"
	accounts := []config.ClaudeAccount{{Name: "a"}, {Name: "b"}, {Name: "c"}}
	ov := overlay.NewSessionCreateOverlay(nil, accounts, []string{dir}, "")
	ov.SetSize(80, 40)
	h := &home{
		ctx:              context.Background(),
		state:            statePrompt,
		appConfig:        config.DefaultConfig(),
		textInputOverlay: ov,
		newSessionPath:   dir,
	}

	// A settled target whose resolved route is "b" (index 1) re-points the untouched
	// picker. direct:true keeps this hermetic (no git) and skips the branch stop.
	_, _ = h.Update(targetValidityResultMsg{path: dir, valid: true, direct: true, accountName: "b"})

	// Reveal the (otherwise touch-gated) selection: focus the picker and nudge once.
	// From the preselected "b" (idx 1) a Right lands on "c" (idx 2); had the preselect
	// not landed (idx 0 → "a"), Right would land on "b". Observing "c" proves it.
	ov.FocusTitle()
	ov.HandleKeyPress(tea.KeyMsg{Type: tea.KeyTab}) // title → prompt
	ov.HandleKeyPress(tea.KeyMsg{Type: tea.KeyTab}) // prompt → account
	ov.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRight})
	acct, ok := ov.GetSelectedAccount()
	require.True(t, ok, "driving the picker marks it touched/overriding")
	assert.Equal(t, "c", acct.Name, "the picker must have been re-preselected to b")
}

// TestValidityCheckRoutesDirectSessionByPath verifies that runValidityCheck routes a
// direct (non-git) target — which has no origin remote — to an account via path_matches.
// This is the container-directory case (e.g. ~/quantivly/qspace holds sub-repos but is
// not itself a repo): without path routing it would always fall to the catch-all default.
func TestValidityCheckRoutesDirectSessionByPath(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "quantivly-proj")
	require.NoError(t, os.MkdirAll(dir, 0o755))

	cfg := config.DefaultConfig()
	cfg.ClaudeAccounts = []config.ClaudeAccount{
		{Name: "personal", ConfigDir: "~/.claude"},
		{Name: "quantivly", ConfigDir: "~/.claude-quantivly", PathMatches: []string{"quantivly-proj"}},
	}
	h := &home{ctx: context.Background(), appConfig: cfg}

	msg, ok := h.runValidityCheck(dir)().(targetValidityResultMsg)
	require.True(t, ok)
	assert.True(t, msg.direct, "a non-git temp dir is a direct session")
	assert.Equal(t, "quantivly", msg.accountName, "path_matches must route a direct session with no remote")
}

// TestNonGitVerdictDoesNotTriggerFetch verifies direct/invalid targets never fetch —
// there is no repo to fetch in.
func TestNonGitVerdictDoesNotTriggerFetch(t *testing.T) {
	const dir = "/some/dir"
	ov := overlay.NewSessionCreateOverlay(nil, nil, []string{dir}, "")
	h := &home{
		ctx:              context.Background(),
		state:            statePrompt,
		appConfig:        config.DefaultConfig(),
		textInputOverlay: ov,
		newSessionPath:   dir,
	}

	_, cmd := h.Update(targetValidityResultMsg{path: dir, valid: true, direct: true})
	assert.Nil(t, cmd, "a non-git directory must not trigger a fetch")
	_, cmd = h.Update(targetValidityResultMsg{path: dir, valid: false})
	assert.Nil(t, cmd, "an invalid target must not trigger a fetch")
}

// TestFetchDoneRefreshesBranchListForCurrentPath verifies a completed fetch re-runs the
// branch search (so newly-fetched refs appear), but only when the fetched path is still
// the current target — a stale completion is dropped.
func TestFetchDoneRefreshesBranchListForCurrentPath(t *testing.T) {
	const repo = "/some/repo"
	ov := overlay.NewSessionCreateOverlay(nil, nil, []string{repo}, "")
	h := &home{
		ctx:              context.Background(),
		state:            statePrompt,
		appConfig:        config.DefaultConfig(),
		textInputOverlay: ov,
		newSessionPath:   repo,
	}

	_, cmd := h.Update(branchFetchDoneMsg{path: repo})
	require.NotNil(t, cmd, "a fetch completion for the current target must refresh the list")
	_, ok := cmd().(branchSearchResultMsg)
	assert.True(t, ok, "the refresh must be a branch search")

	_, cmd = h.Update(branchFetchDoneMsg{path: "/elsewhere"})
	assert.Nil(t, cmd, "a fetch completion for an abandoned path is dropped")
}

// TestBranchSearchErrorClearsSpinner verifies a failed branch search delivers an error
// result that clears the picker's "searching…" state and shows the error hint — the old
// behavior swallowed the error and the spinner never resolved.
func TestBranchSearchErrorClearsSpinner(t *testing.T) {
	const repo = "/some/repo"
	ov := overlay.NewSessionCreateOverlay(nil, nil, []string{repo}, "")
	ov.SetSize(80, 40)
	h := &home{
		ctx:              context.Background(),
		state:            statePrompt,
		appConfig:        config.DefaultConfig(),
		textInputOverlay: ov,
		newSessionPath:   repo,
	}
	// Focus the branch picker (right after the project) so its list UI renders.
	_, _ = h.Update(tea.KeyMsg{Type: tea.KeyTab})
	version := ov.InvalidateBranchSearch()
	require.Contains(t, ov.Render(), "searching")

	_, _ = h.Update(branchSearchResultMsg{version: version, err: true})

	out := ov.Render()
	assert.NotContains(t, out, "searching", "an error result must clear the loading state")
	assert.Contains(t, out, "couldn't list branches")
}

// TestTargetValidityResultDropsStalePath verifies a result for a path the user has
// already navigated away from is ignored, so it can't clobber the current indicator.
func TestTargetValidityResultDropsStalePath(t *testing.T) {
	const repo = "/some/repo"
	ov := overlay.NewSessionCreateOverlay(nil, nil, []string{repo}, "")
	ov.SetSize(80, 24)
	h := &home{
		ctx:              context.Background(),
		state:            statePrompt,
		appConfig:        config.DefaultConfig(),
		textInputOverlay: ov,
		newSessionPath:   repo,
	}

	// Establish the current target as invalid.
	_, _ = h.Update(targetValidityResultMsg{path: repo, valid: false})
	require.Contains(t, ov.Render(), "not a directory")

	// A late result for a DIFFERENT (stale) path must not flip the indicator.
	_, _ = h.Update(targetValidityResultMsg{path: "/elsewhere", valid: true})
	assert.Contains(t, ov.Render(), "not a directory", "stale-path result is ignored")
}

// TestPathChangeResetsValidityToUnknown verifies that navigating to a different target
// resets the indicator to "unknown" (no hint) instead of asserting the previous path's
// verdict for the new path while the debounced async re-check is in flight.
func TestPathChangeResetsValidityToUnknown(t *testing.T) {
	const repoA = "/some/repo-a"
	const repoB = "/some/repo-b"
	ov := overlay.NewSessionCreateOverlay(nil, nil, []string{repoA, repoB}, "")
	ov.SetSize(80, 24)
	h := &home{
		ctx:              context.Background(),
		state:            statePrompt,
		appConfig:        config.DefaultConfig(),
		textInputOverlay: ov,
		newSessionPath:   repoA,
	}

	// Establish the current target as known-invalid: hint visible.
	_, _ = h.Update(targetValidityResultMsg{path: repoA, valid: false})
	require.Contains(t, ov.Render(), "not a directory")

	// Move the picker selection to repoB (focus starts on the project picker).
	_, _ = h.Update(tea.KeyMsg{Type: tea.KeyDown})
	require.Equal(t, repoB, h.newSessionPath, "the path change must be registered")

	// repoA's verdict must not be shown for repoB; the indicator is unknown until the
	// async check resolves. ("no git isolation" is the project picker's direct-session
	// hint; the branch picker's "direct session" placeholder deliberately persists
	// through the unknown window so the section doesn't flicker on every keystroke.)
	out := ov.Render()
	assert.NotContains(t, out, "not a directory", "stale verdict is cleared on path change")
	assert.NotContains(t, out, "no git isolation", "no hint at all while the state is unknown")
}

// TestConfirmActionCancelDoesNotRun verifies cancelling never executes the action.
func TestConfirmActionCancelDoesNotRun(t *testing.T) {
	h := &home{
		ctx:       context.Background(),
		state:     stateDefault,
		appConfig: config.DefaultConfig(),
		menu:      ui.NewMenu(),
	}

	ran := false
	_ = h.confirmAction("Kill it?", func() tea.Msg { ran = true; return nil })

	_, cmd := h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	assert.Nil(t, cmd)
	assert.False(t, ran, "cancelled action must not run")
	assert.Equal(t, stateDefault, h.state)
	assert.Nil(t, h.pendingConfirmAction)
}

// TestConfirmationModalVisualAppearance tests that confirmation modal has distinct visual appearance
func TestConfirmationModalVisualAppearance(t *testing.T) {
	h := &home{
		ctx:       context.Background(),
		state:     stateDefault,
		appConfig: config.DefaultConfig(),
	}

	// Create a test confirmation overlay
	message := "[!] Delete everything?"
	h.confirmationOverlay = overlay.NewConfirmationOverlay(message)
	h.state = stateConfirm

	// Verify the overlay was created with confirmation settings
	assert.NotNil(t, h.confirmationOverlay)
	assert.Equal(t, stateConfirm, h.state)
	assert.False(t, h.confirmationOverlay.Dismissed)

	// Test the overlay render (we can test that it renders without errors)
	rendered := h.confirmationOverlay.Render()
	assert.NotEmpty(t, rendered)

	// Test that it includes the message content and instructions
	assert.Contains(t, rendered, "Delete everything?")
	assert.Contains(t, rendered, "Press")
	assert.Contains(t, rendered, "to confirm")
	assert.Contains(t, rendered, "to cancel")

	// Test that the danger indicator is preserved
	assert.Contains(t, rendered, "[!")
}

// TestShouldAutoOpen covers the auto-attach gating policy. An instance built via
// NewInstance is never started, so Started() is false — which both encodes the
// "never attach a session that didn't come up" guard and keeps these (and any future)
// tests off the real-PTY attach path. The positive path is exercised by manual verification.
func TestShouldAutoOpen(t *testing.T) {
	newHomeWithAutoAttach := func(enabled bool) *home {
		cfg := config.DefaultConfig()
		cfg.AutoAttach = &enabled
		return &home{ctx: context.Background(), appConfig: cfg}
	}
	newInst := func(prompt string) *session.Instance {
		inst, err := session.NewInstance(session.InstanceOptions{
			Title:   "t",
			Path:    t.TempDir(),
			Program: "claude",
		})
		require.NoError(t, err)
		inst.QueuePrompt(prompt)
		return inst
	}

	t.Run("flag off, no prompt", func(t *testing.T) {
		assert.False(t, newHomeWithAutoAttach(false).shouldAutoOpen(newInst(""), false))
	})
	t.Run("flag on, prompt set", func(t *testing.T) {
		assert.False(t, newHomeWithAutoAttach(true).shouldAutoOpen(newInst("do a thing"), true))
	})
	t.Run("flag on, prompt delivered before the parked start message", func(t *testing.T) {
		// The keeper can deliver (and clear) the prompt while instanceStartedMsg is
		// parked during an attach; the creation-time hadPrompt flag must keep the
		// suppression so detaching from another session doesn't force-attach this one.
		inst := newInst("do a thing")
		inst.ClearPrompt()
		assert.False(t, newHomeWithAutoAttach(true).shouldAutoOpen(inst, true))
	})
	t.Run("flag on, no prompt, but not started", func(t *testing.T) {
		// The most eligible case by policy, yet still false because the session is not
		// running — the Started/TmuxAlive guard holds.
		assert.False(t, newHomeWithAutoAttach(true).shouldAutoOpen(newInst(""), false))
	})
}

// The off-cadence poll handler applies the polled state to the instance immediately, and
// leaves a paused instance untouched (mirroring the metadata-tick skip).
func TestInstancePolledMsgAppliesStatus(t *testing.T) {
	newInst := func() *session.Instance {
		inst, err := session.NewInstance(session.InstanceOptions{
			Title: "s", Path: t.TempDir(), Program: "claude",
		})
		require.NoError(t, err)
		return inst
	}
	h := &home{ctx: context.Background(), state: stateDefault}

	t.Run("active instance gets the polled status", func(t *testing.T) {
		inst := newInst()
		inst.SetStatus(session.Ready)
		h.Update(instancePolledMsg{instance: inst, state: tmux.PaneWorking})
		assert.Equal(t, session.Running, inst.GetStatus())
	})

	t.Run("paused instance is left untouched", func(t *testing.T) {
		inst := newInst()
		inst.SetStatus(session.Paused)
		h.Update(instancePolledMsg{instance: inst, state: tmux.PaneWorking})
		assert.Equal(t, session.Paused, inst.GetStatus(), "a poll result must not resurrect a paused instance")
	})
}

// pollSelectedCmd is a no-op for anything that can't be polled, so the selection-change
// refresh never spawns a doomed capture.
func TestPollSelectedCmdGuards(t *testing.T) {
	assert.Nil(t, pollSelectedCmd(nil, 0), "nil selection yields no command")

	inst, err := session.NewInstance(session.InstanceOptions{
		Title: "s", Path: t.TempDir(), Program: "claude",
	})
	require.NoError(t, err)
	assert.Nil(t, pollSelectedCmd(inst, 0), "an unstarted instance yields no command")
}

// TestApplyMetadataResults covers the shared apply helper used by both the periodic tick
// and the one-shot detach sweep: it applies the full metadata (pane state, diff, PR, model,
// mode) to active rows and skips lost/paused ones.
func TestApplyMetadataResults(t *testing.T) {
	newHome := func() (*home, *ui.List) {
		spin := spinner.New(spinner.WithSpinner(spinner.MiniDot))
		list := ui.NewList(&spin)
		return &home{
			ctx:       context.Background(),
			state:     stateDefault,
			appConfig: config.DefaultConfig(),
			list:      list,
		}, list
	}
	newInst := func() *session.Instance {
		inst, err := session.NewInstance(session.InstanceOptions{
			Title: "s", Path: t.TempDir(), Program: "claude",
		})
		require.NoError(t, err)
		return inst
	}

	t.Run("applies the full metadata to an active instance", func(t *testing.T) {
		h, list := newHome()
		inst := newInst()
		inst.SetStatus(session.Running)
		_ = list.AddInstance(inst)
		stats := &git.DiffStats{Added: 3, Removed: 1}
		pr := &git.PRStatus{}

		cmds := h.applyMetadataResults([]instanceMetaResult{
			{
				instance: inst, state: tmux.PaneIdle, diffStats: stats,
				prStatus: pr,
				model:    "claude-opus-4-8", modelOK: true,
				mode: "plan", modeOK: true,
			},
		})

		require.Empty(t, cmds, "no queued prompts means no delivery commands")
		assert.Equal(t, session.Ready, inst.GetStatus(), "PaneIdle must apply as Ready")
		assert.Same(t, stats, inst.GetDiffStats(), "the fresh diff stats must be stored")
		assert.Same(t, pr, inst.GetPRStatus(), "the fresh PR status must be stored")
		assert.Equal(t, "claude-opus-4-8", inst.ModelInfo(), "the fresh model must be applied")
		assert.Equal(t, "plan", inst.PermissionModeInfo(), "the fresh permission mode must be applied")
	})

	t.Run("skips a lost or paused instance", func(t *testing.T) {
		h, _ := newHome()
		lost := newInst()
		lost.SetStatus(session.Running)
		paused := newInst()
		paused.SetStatus(session.Paused)

		h.applyMetadataResults([]instanceMetaResult{
			{instance: lost, state: tmux.PaneIdle, sessionLost: true},
			{instance: paused, state: tmux.PaneIdle},
		})

		assert.Equal(t, session.Running, lost.GetStatus(), "a lost session must not be applied (it awaits recovery)")
		assert.Equal(t, session.Paused, paused.GetStatus(), "a paused session must not be resurrected")
	})

	t.Run("retains prior model and mode when compute reports not-OK", func(t *testing.T) {
		h, list := newHome()
		inst := newInst()
		inst.SetStatus(session.Running)
		_ = list.AddInstance(inst)

		// A first sweep establishes the model and permission mode.
		h.applyMetadataResults([]instanceMetaResult{
			{instance: inst, state: tmux.PaneIdle, model: "claude-opus-4-8", modelOK: true, mode: "plan", modeOK: true},
		})
		require.Equal(t, "claude-opus-4-8", inst.ModelInfo())
		require.Equal(t, "plan", inst.PermissionModeInfo())

		// A later sweep whose compute could not produce fresh values (OK=false, e.g.
		// non-claude/unavailable/unchanged) must not clobber the established meta.
		h.applyMetadataResults([]instanceMetaResult{
			{instance: inst, state: tmux.PaneIdle, modelOK: false, modeOK: false},
		})
		assert.Equal(t, "claude-opus-4-8", inst.ModelInfo(), "a not-OK model compute must retain the prior model")
		assert.Equal(t, "plan", inst.PermissionModeInfo(), "a not-OK mode compute must retain the prior mode")
	})
}

// TestApplyDiffStats covers the diff-compute-failure branch: a DiffStats carrying an
// Error must drop any stale numbers (store nil) rather than display them — including the
// expected pre-baseline "base commit SHA not set" case, which is silent but still clears.
func TestApplyDiffStats(t *testing.T) {
	newInst := func() *session.Instance {
		inst, err := session.NewInstance(session.InstanceOptions{
			Title: "s", Path: t.TempDir(), Program: "claude",
		})
		require.NoError(t, err)
		return inst
	}

	t.Run("a generic compute error clears stale stats", func(t *testing.T) {
		inst := newInst()
		inst.SetDiffStats(&git.DiffStats{Added: 9})
		applyDiffStats(inst, &git.DiffStats{Error: fmt.Errorf("boom")})
		assert.Nil(t, inst.GetDiffStats(), "a diff error must drop stale numbers")
	})

	t.Run("the expected pre-baseline error also clears stats", func(t *testing.T) {
		inst := newInst()
		inst.SetDiffStats(&git.DiffStats{Added: 9})
		applyDiffStats(inst, &git.DiffStats{Error: fmt.Errorf("base commit SHA not set")})
		assert.Nil(t, inst.GetDiffStats(), "the expected pre-baseline error is silent but still clears stats")
	})

	t.Run("clean stats are stored", func(t *testing.T) {
		inst := newInst()
		stats := &git.DiffStats{Added: 3, Removed: 1}
		applyDiffStats(inst, stats)
		assert.Same(t, stats, inst.GetDiffStats(), "valid stats must be stored as-is")
	})
}

// TestMetadataUpdateDoneMsg pins the periodic handler's tick lifecycle (the inverse of
// the one-shot sweep in TestMetadataSweepDoneMsgDoesNotRescheduleTick): it advances the
// full-sweep phase counter and re-arms the metadata tick — but only while the app
// context is live. On a cancelled context (shutdown) the counter still advances but the
// tick is NOT re-armed, ending the self-chaining loop.
func TestMetadataUpdateDoneMsg(t *testing.T) {
	newHome := func(ctx context.Context) (*home, *session.Instance) {
		spin := spinner.New(spinner.WithSpinner(spinner.MiniDot))
		list := ui.NewList(&spin)
		inst, err := session.NewInstance(session.InstanceOptions{
			Title: "s", Path: t.TempDir(), Program: "claude",
		})
		require.NoError(t, err)
		inst.SetStatus(session.Running)
		_ = list.AddInstance(inst)
		return &home{
			ctx:          ctx,
			state:        stateDefault,
			appConfig:    config.DefaultConfig(),
			list:         list,
			metadataTick: 7,
			lostStrikes:  map[*session.Instance]int{},
		}, inst
	}

	t.Run("a live context advances the phase counter and re-arms the tick", func(t *testing.T) {
		h, inst := newHome(context.Background())
		_, cmd := h.Update(metadataUpdateDoneMsg{results: []instanceMetaResult{
			{instance: inst, state: tmux.PaneIdle},
		}})
		assert.Equal(t, uint64(8), h.metadataTick, "the periodic handler must advance the full-sweep phase")
		assert.NotNil(t, cmd, "a live context must re-arm the metadata tick")
	})

	t.Run("a cancelled context advances the counter but stops the loop", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		h, inst := newHome(ctx)
		_, cmd := h.Update(metadataUpdateDoneMsg{results: []instanceMetaResult{
			{instance: inst, state: tmux.PaneIdle},
		}})
		assert.Equal(t, uint64(8), h.metadataTick, "the counter still advances before the ctx check")
		assert.Nil(t, cmd, "a cancelled context must not re-arm the tick (no second loop after shutdown)")
	})
}

// TestMetadataSweepDoneMsgDoesNotRescheduleTick pins the load-bearing invariant of the
// one-shot detach sweep: it applies its results but must NOT advance the full-sweep phase
// counter or re-arm the periodic tick — otherwise a second tick loop would run forever.
func TestMetadataSweepDoneMsgDoesNotRescheduleTick(t *testing.T) {
	spin := spinner.New(spinner.WithSpinner(spinner.MiniDot))
	list := ui.NewList(&spin)
	inst, err := session.NewInstance(session.InstanceOptions{
		Title: "s", Path: t.TempDir(), Program: "claude",
	})
	require.NoError(t, err)
	inst.SetStatus(session.Running)
	_ = list.AddInstance(inst)

	h := &home{
		ctx:          context.Background(),
		state:        stateDefault,
		appConfig:    config.DefaultConfig(),
		list:         list,
		metadataTick: 7,
	}

	_, cmd := h.Update(metadataSweepDoneMsg{results: []instanceMetaResult{
		{instance: inst, state: tmux.PaneIdle},
	}})

	assert.Equal(t, session.Ready, inst.GetStatus(), "the sweep must apply the fresh pane state")
	assert.Equal(t, uint64(7), h.metadataTick, "a one-shot sweep must not advance the periodic full-sweep phase")
	assert.Nil(t, cmd, "the sweep must not reschedule the metadata tick (no second loop)")
}

// TestStalePaneCapturesDroppedAfterAttach pins the attachGen guard: a pane-state
// capture stamped with an older generation was taken before a terminal attach ran,
// and the keeper may have advanced the very dialog it observed — replaying it would
// TapEnter whatever is up now (worst case a plan-approval screen auto-yes never
// answers). Stale results must be dropped; the periodic tick must still re-arm.
func TestStalePaneCapturesDroppedAfterAttach(t *testing.T) {
	newHome := func() (*home, *session.Instance) {
		spin := spinner.New(spinner.WithSpinner(spinner.MiniDot))
		list := ui.NewList(&spin)
		inst, err := session.NewInstance(session.InstanceOptions{
			Title: "s", Path: t.TempDir(), Program: "claude",
		})
		require.NoError(t, err)
		inst.SetStatus(session.Running)
		_ = list.AddInstance(inst)
		return &home{
			ctx:         context.Background(),
			state:       stateDefault,
			appConfig:   config.DefaultConfig(),
			list:        list,
			lostStrikes: map[*session.Instance]int{},
			attachGen:   1, // an attach ran since the captures below were created (gen 0)
		}, inst
	}

	t.Run("periodic tick: results dropped, tick still re-arms", func(t *testing.T) {
		h, inst := newHome()
		_, cmd := h.Update(metadataUpdateDoneMsg{results: []instanceMetaResult{
			{instance: inst, state: tmux.PaneIdle},
		}, attachGen: 0})
		assert.Equal(t, session.Running, inst.GetStatus(), "a stale capture must not be applied")
		assert.Equal(t, uint64(1), h.metadataTick, "the phase counter still advances")
		assert.NotNil(t, cmd, "the tick chain must survive a dropped batch")
	})

	t.Run("detach sweep: results dropped", func(t *testing.T) {
		h, inst := newHome()
		_, cmd := h.Update(metadataSweepDoneMsg{results: []instanceMetaResult{
			{instance: inst, state: tmux.PaneIdle},
		}, attachGen: 0})
		assert.Equal(t, session.Running, inst.GetStatus(), "a stale sweep must not be applied")
		assert.Nil(t, cmd)
	})

	t.Run("selection poll: result dropped", func(t *testing.T) {
		h, inst := newHome()
		_, _ = h.Update(instancePolledMsg{instance: inst, state: tmux.PaneIdle, attachGen: 0})
		assert.Equal(t, session.Running, inst.GetStatus(), "a stale selection poll must not be applied")
	})

	t.Run("current-generation results still apply", func(t *testing.T) {
		h, inst := newHome()
		_, _ = h.Update(metadataSweepDoneMsg{results: []instanceMetaResult{
			{instance: inst, state: tmux.PaneIdle},
		}, attachGen: 1})
		assert.Equal(t, session.Ready, inst.GetStatus(), "a fresh capture applies normally")
	})
}
