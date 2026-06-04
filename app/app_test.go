package app

import (
	"context"
	"fmt"
	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/keys"
	"github.com/ZviBaratz/atrium/log"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/session/tmux"
	"github.com/ZviBaratz/atrium/ui"
	"github.com/ZviBaratz/atrium/ui/overlay"
	"os"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMain runs before all tests to set up the test environment.
func TestMain(m *testing.M) {
	// Sandbox HOME so tests never read or overwrite the real ~/.claude-squad
	// state/config — config.GetConfigDir resolves under $HOME, and LoadConfig
	// writes a default config.json on first run.
	tmpHome, err := os.MkdirTemp("", "cs-test-home-")
	if err == nil {
		_ = os.Setenv("HOME", tmpHome)
	}

	// Initialize the logger before any tests run
	log.Initialize(false)

	exitCode := m.Run()

	log.Close()
	if tmpHome != "" {
		_ = os.RemoveAll(tmpHome)
	}
	os.Exit(exitCode)
}

func TestDeliverReadyPrompts(t *testing.T) {
	newInst := func(prompt string) *session.Instance {
		inst, err := session.NewInstance(session.InstanceOptions{
			Title: "s", Path: t.TempDir(), Program: "claude",
		})
		require.NoError(t, err)
		inst.Prompt = prompt
		return inst
	}

	t.Run("ready instance with a queued prompt is delivered once and cleared", func(t *testing.T) {
		inst := newInst("do the thing")
		inst.PromptQueuedAt = time.Now()
		cmds := deliverReadyPrompts([]instanceMetaResult{
			{instance: inst, readyForPrompt: true},
		})
		require.Len(t, cmds, 1)
		require.Equal(t, "", inst.Prompt, "prompt must be cleared so it is never sent twice")
		require.True(t, inst.PromptQueuedAt.IsZero(),
			"PromptQueuedAt must be cleared alongside the prompt so a later tick can't re-trigger the timeout")
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
		require.Equal(t, "waiting on trust screen", inst.Prompt, "prompt must remain queued")
	})
}

func TestPromptDeliveryReady(t *testing.T) {
	now := time.Now()
	queued := now.Add(-1 * time.Second)          // queued recently, within grace
	stale := now.Add(-2 * promptDeliveryTimeout) // queued long ago, past grace

	tests := []struct {
		name      string
		state     tmux.PaneState
		gateReady bool
		queuedAt  time.Time
		want      bool
	}{
		{
			name:      "idle pane past the gate delivers",
			state:     tmux.PaneIdle,
			gateReady: true,
			queuedAt:  queued,
			want:      true,
		},
		{
			name:      "startup gate still up never delivers even when idle",
			state:     tmux.PaneIdle,
			gateReady: false,
			queuedAt:  queued,
			want:      false,
		},
		{
			name:      "busy pane within grace keeps waiting",
			state:     tmux.PaneWorking,
			gateReady: true,
			queuedAt:  queued,
			want:      false,
		},
		{
			name:      "busy pane past timeout force-delivers once past the gate",
			state:     tmux.PaneWorking,
			gateReady: true,
			queuedAt:  stale,
			want:      true,
		},
		{
			name:      "timeout never bypasses the startup gate",
			state:     tmux.PaneWorking,
			gateReady: false,
			queuedAt:  stale,
			want:      false,
		},
		{
			name:      "zero queuedAt disables the timeout on a busy pane",
			state:     tmux.PaneWorking,
			gateReady: true,
			queuedAt:  time.Time{},
			want:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := promptDeliveryReady(tt.state, tt.gateReady, tt.queuedAt, now)
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

func TestApplyPaneState(t *testing.T) {
	newInst := func(autoYes bool) *session.Instance {
		inst, err := session.NewInstance(session.InstanceOptions{
			Title: "s", Path: t.TempDir(), Program: "claude",
		})
		require.NoError(t, err)
		inst.AutoYes = autoYes
		inst.SetStatus(session.Loading) // a recognizable prior state
		return inst
	}

	t.Run("working → Running", func(t *testing.T) {
		inst := newInst(false)
		applyPaneState(inst, tmux.PaneWorking)
		require.Equal(t, session.Running, inst.GetStatus())
	})

	t.Run("idle → Ready", func(t *testing.T) {
		inst := newInst(false)
		applyPaneState(inst, tmux.PaneIdle)
		require.Equal(t, session.Ready, inst.GetStatus())
	})

	t.Run("prompt with AutoYes off → NeedsInput", func(t *testing.T) {
		inst := newInst(false)
		applyPaneState(inst, tmux.PanePrompt)
		require.Equal(t, session.NeedsInput, inst.GetStatus())
	})

	t.Run("prompt with AutoYes on → not NeedsInput (auto-answered)", func(t *testing.T) {
		inst := newInst(true)
		applyPaneState(inst, tmux.PanePrompt)
		require.NotEqual(t, session.NeedsInput, inst.GetStatus())
	})

	t.Run("unknown → status unchanged", func(t *testing.T) {
		inst := newInst(false)
		applyPaneState(inst, tmux.PaneUnknown)
		require.Equal(t, session.Loading, inst.GetStatus(), "an unreadable pane must not flip the status")
	})
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
	ov := overlay.NewSessionCreateOverlay(nil, []string{repo})
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

// TestTargetValidityResultDropsStalePath verifies a result for a path the user has
// already navigated away from is ignored, so it can't clobber the current indicator.
func TestTargetValidityResultDropsStalePath(t *testing.T) {
	const repo = "/some/repo"
	ov := overlay.NewSessionCreateOverlay(nil, []string{repo})
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
	ov := overlay.NewSessionCreateOverlay(nil, []string{repoA, repoB})
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
	// async check resolves.
	out := ov.Render()
	assert.NotContains(t, out, "not a directory", "stale verdict is cleared on path change")
	assert.NotContains(t, out, "direct session", "no hint at all while the state is unknown")
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

// newStateNewHome builds a home sitting in stateNew with three sessions: two pre-existing
// ones (repoA, then repoB) and a freshly created repoA session that grouped insertion places
// at index 1 (between them). The new session is tracked via h.newInstance, exactly as the
// n/N handlers do. It returns the home plus the new and trailing instances for assertions.
func newStateNewHome(t *testing.T) (h *home, newInst, trailing *session.Instance) {
	t.Helper()
	spin := spinner.New(spinner.WithSpinner(spinner.MiniDot))
	list := ui.NewList(&spin)

	existingA, err := session.NewInstance(session.InstanceOptions{Title: "a", Path: "/tmp/repoA", Program: "echo"})
	require.NoError(t, err)
	trailing, err = session.NewInstance(session.InstanceOptions{Title: "b", Path: "/tmp/repoB", Program: "echo"})
	require.NoError(t, err)
	list.AddInstance(existingA)
	list.AddInstance(trailing)

	// New repoA session: grouped insertion puts it at index 1, so it is neither the last
	// item nor (after a selection move) reliably the selected one.
	newInst, err = session.NewInstance(session.InstanceOptions{Title: "", Path: "/tmp/repoA", Program: "echo"})
	require.NoError(t, err)
	list.AddInstance(newInst)
	list.SelectInstance(newInst)

	require.Same(t, trailing, list.GetInstances()[list.NumInstances()-1],
		"setup precondition: the new instance must not be the last list item")

	h = &home{
		ctx:       context.Background(),
		state:     stateNew,
		appConfig: config.DefaultConfig(),
		list:      list,
		menu:      ui.NewMenu(),

		newInstance: newInst,
	}
	return h, newInst, trailing
}

// Regression: when grouped insertion places the new session mid-list, typed characters must
// reach the new session, not whatever happens to be the last item. Before the fix the
// stateNew handler used GetInstances()[NumInstances()-1] and typed into the trailing session
// (which, when started, raised "cannot change title of a started instance").
func TestStateNew_TypingTargetsNewInstanceNotLast(t *testing.T) {
	h, newInst, trailing := newStateNewHome(t)

	h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})

	require.Equal(t, "x", newInst.Title, "new instance should receive the typed title")
	require.Equal(t, "b", trailing.Title, "trailing instance title must be untouched")
}

// Regression: a background instanceStartedMsg can SelectInstance another session while the
// user is still naming a new one (showHelpScreen is a no-op for returning users, so the state
// stays stateNew). Typing must follow the tracked m.newInstance, not the moved selection.
func TestStateNew_TypingSurvivesSelectionHijack(t *testing.T) {
	h, newInst, trailing := newStateNewHome(t)

	// Simulate the hijack: selection moves onto the trailing (would-be started) session.
	h.list.SelectInstance(trailing)
	require.Same(t, trailing, h.list.GetSelectedInstance(), "precondition: selection moved off the new instance")

	// Type a plain character; in stateNew every rune is title input.
	h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})

	require.Equal(t, "x", newInst.Title, "title must follow the tracked new instance, not the selection")
	require.Equal(t, "b", trailing.Title, "the now-selected instance must be untouched")
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
		inst.Prompt = prompt
		return inst
	}

	t.Run("flag off, no prompt", func(t *testing.T) {
		assert.False(t, newHomeWithAutoAttach(false).shouldAutoOpen(newInst("")))
	})
	t.Run("flag on, prompt set", func(t *testing.T) {
		assert.False(t, newHomeWithAutoAttach(true).shouldAutoOpen(newInst("do a thing")))
	})
	t.Run("flag on, no prompt, but not started", func(t *testing.T) {
		// The most eligible case by policy, yet still false because the session is not
		// running — the Started/TmuxAlive guard holds.
		assert.False(t, newHomeWithAutoAttach(true).shouldAutoOpen(newInst("")))
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

// pollSelectedCmd is a no-op for anything that can't be polled, so the switch/detach
// refresh never spawns a doomed capture.
func TestPollSelectedCmdGuards(t *testing.T) {
	assert.Nil(t, pollSelectedCmd(nil, false), "nil selection yields no command")
	assert.Nil(t, pollSelectedCmd(nil, true), "nil selection yields no command (fresh)")

	inst, err := session.NewInstance(session.InstanceOptions{
		Title: "s", Path: t.TempDir(), Program: "claude",
	})
	require.NoError(t, err)
	assert.Nil(t, pollSelectedCmd(inst, false), "an unstarted instance yields no command")
	assert.Nil(t, pollSelectedCmd(inst, true), "an unstarted instance yields no command (fresh)")
}
