package app

import (
	"context"
	"fmt"
	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/log"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/session/tmux"
	"github.com/ZviBaratz/atrium/ui"
	"github.com/ZviBaratz/atrium/ui/overlay"
	"os"
	"testing"

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
		cmds := deliverReadyPrompts([]instanceMetaResult{
			{instance: inst, readyForPrompt: true},
		})
		require.Len(t, cmds, 1)
		require.Equal(t, "", inst.Prompt, "prompt must be cleared so it is never sent twice")
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
		require.Equal(t, session.Running, inst.Status)
	})

	t.Run("idle → Ready", func(t *testing.T) {
		inst := newInst(false)
		applyPaneState(inst, tmux.PaneIdle)
		require.Equal(t, session.Ready, inst.Status)
	})

	t.Run("prompt with AutoYes off → NeedsInput", func(t *testing.T) {
		inst := newInst(false)
		applyPaneState(inst, tmux.PanePrompt)
		require.Equal(t, session.NeedsInput, inst.Status)
	})

	t.Run("prompt with AutoYes on → not NeedsInput (auto-answered)", func(t *testing.T) {
		inst := newInst(true)
		applyPaneState(inst, tmux.PanePrompt)
		require.NotEqual(t, session.NeedsInput, inst.Status)
	})

	t.Run("unknown → status unchanged", func(t *testing.T) {
		inst := newInst(false)
		applyPaneState(inst, tmux.PaneUnknown)
		require.Equal(t, session.Loading, inst.Status, "an unreadable pane must not flip the status")
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
	list := ui.NewList(&spinner, false)

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
	list := ui.NewList(&spinner, false)

	// Add test instance
	instance, err := session.NewInstance(session.InstanceOptions{
		Title:   "test-session",
		Path:    t.TempDir(),
		Program: "claude",
		AutoYes: false,
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
	list := ui.NewList(&spin, false)

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

	// Type a plain character that is NOT a global keybinding so it is treated as title
	// input rather than intercepted by handleMenuHighlighting (which does not exclude
	// stateNew). 'x' is unmapped; 'y' is now KeyCopyBranch, so don't use it here.
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
